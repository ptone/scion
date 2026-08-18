// Command dockerfile-stages turns a Dockerfile into the flat stage table that
// hack/check-dockerfile-stages.sh applies its rules to.
//
// WHY THIS EXISTS AS A GO PROGRAM.
//
// The rules in the shell script were never the problem. Over two review rounds
// and two authors, SEVEN separate bugs were found in the awk that used to build
// this table -- comments ending in a backslash, tab-separated parser
// directives, four flavours of heredoc, empty continuation lines, continuation
// joins that inserted a space Docker does not insert, and doubled trailing
// backslashes. Every one of them shipped a GREEN guard: the rules were correct
// and were being handed a file that was not the one Docker builds. A rule
// cannot defend against being given the wrong input, and a second
// implementation of somebody else's parser does not converge -- each fix only
// teaches the next attacker where the model is thin.
//
// So the line model comes from BuildKit itself, which is the code Docker uses.
// This file contains NO rules. It walks the AST and prints it. Everything that
// decides whether the Dockerfile is acceptable stays in the shell script, in
// one place, where the self-test and the review corpus can reach it.
//
// It lives in its own module (hack/dockerfile-stages/go.mod) so the repository's
// own dependency graph is untouched: adding buildkit to the root module bumps
// eleven unrelated dependencies -- otel, protobuf, grpc-gateway, klog -- and the
// Go directive, which is not a bill a Dockerfile lint gets to run up. The repo
// already carries ten nested modules under extras/, so this is the established
// pattern rather than a new one.
//
// Output, in file order:
//
//	DIRECTIVE escape <token>      only when the escape token is not a backslash
//	WARN <text>                   a parser warning, e.g. an empty continuation line
//	HEREDOC <line> <<word>        a heredoc redirection, as BuildKit detects one
//	STAGE <n> <base> <name-or--> <line>
//	INSTR <n> <VERB> <rest>
//
// Stage names and base references are lowercased because Docker matches them
// case-insensitively -- except a base containing a `$`, which is left verbatim:
// it is a build-arg expansion, not a stage name, and ARG names ARE
// case-sensitive, so lowercasing it would print a variable that does not exist.
// No expansion is attempted. The script refuses such a base inside the runtime
// chain, because `--build-arg` can change it at build time and it is therefore
// not a property of the file at all.
//
// A parse error is fatal and exits 2: a file BuildKit will not read is not a
// file this guard should have an opinion about.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// format renders one AST node as a verb and its arguments, as close to the text
// that was written as the AST allows.
//
// ONBUILD is the reason this is a function rather than a loop. BuildKit parses
// `ONBUILD USER 1000` into an ONBUILD node whose Next is an EMPTY WRAPPER whose
// single child is the real `USER 1000` node -- so walking the Next chain and
// collecting .Value yields "", and `ONBUILD USER 1000` renders as a bare
// ONBUILD with no arguments. Rule 6 then sees an ONBUILD with no verb and lets
// it through. That regression was caught by corpus fixture 65 during this
// migration, which is the fixture that exists for exactly this instruction.
func format(n *parser.Node) (string, []string) {
	verb := strings.ToUpper(n.Value)
	args := append([]string{}, n.Flags...)
	var elems []string
	for c := n.Next; c != nil; c = c.Next {
		if c.Value == "" && len(c.Children) > 0 {
			// A sub-command (ONBUILD, and HEALTHCHECK CMD): splice it in.
			for _, sub := range c.Children {
				sv, sa := format(sub)
				elems = append(elems, sv)
				elems = append(elems, sa...)
			}
			continue
		}
		elems = append(elems, c.Value)
	}
	// ENV/LABEL/ARG key-value pairs come out of the AST as name, value and a
	// bare "=" marking that the source used the equals form. Put them back
	// together so the table shows what was written: the rules read this text,
	// and `ENV HOME=/x` and `ENV HOME /x` are both legal.
	elems = rejoinPairs(elems)
	if n.Attributes["json"] {
		b, err := json.Marshal(elems)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return verb, append(args, string(b))
	}
	return verb, append(args, elems...)
}

// rejoinPairs turns [A 1 = B 2 =] back into [A=1 B=2] and leaves anything that
// is not in that shape alone. A trailing empty element (the legacy `ENV k v`
// form) is dropped.
func rejoinPairs(elems []string) []string {
	out := make([]string, 0, len(elems))
	for i := 0; i < len(elems); i++ {
		if i+2 < len(elems) && elems[i+2] == "=" {
			out = append(out, elems[i]+"="+elems[i+1])
			i += 2
			continue
		}
		if elems[i] == "" && i == len(elems)-1 {
			continue
		}
		out = append(out, elems[i])
	}
	return out
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: dockerfile-stages <path to Dockerfile>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer f.Close()

	res, err := parser.Parse(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	out := &strings.Builder{}
	if res.EscapeToken != '\\' {
		fmt.Fprintf(out, "DIRECTIVE escape %c\n", res.EscapeToken)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "WARN %s\n", w.Short)
	}

	stage := 0
	for _, n := range res.AST.Children {
		for _, h := range n.Heredocs {
			fmt.Fprintf(out, "HEREDOC %d <<%s\n", n.StartLine, h.Name)
		}
		verb, args := format(n)
		// Exec form is re-serialised as JSON so the shell's rules see the same
		// text a human wrote, rather than the AST's element list.
		if verb == "FROM" {
			stage++
			i := 0
			for i < len(args) && strings.HasPrefix(args[i], "--") {
				i++
			}
			base, name := "", "-"
			if i < len(args) {
				base = args[i]
				if !strings.Contains(base, "$") {
					base = strings.ToLower(base)
				}
			}
			if i+2 < len(args) && strings.EqualFold(args[i+1], "as") {
				name = strings.ToLower(args[i+2])
			}
			fmt.Fprintf(out, "STAGE %d %s %s %d\n", stage, base, name, n.StartLine)
			continue
		}
		// Instructions before the first FROM (global ARG, and nothing else that
		// is legal) belong to no stage. They are reported as stage 0, which no
		// rule looks at, rather than dropped: a dropped line is invisible to
		// anyone diffing this output against another reading of the file.
		fmt.Fprintf(out, "INSTR %d %s %s\n", stage, verb, strings.Join(args, " "))
	}
	fmt.Print(out.String())
}

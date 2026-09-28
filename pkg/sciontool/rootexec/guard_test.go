/*
Copyright 2026 The Scion Authors.
*/

package rootexec

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// guardedDirs are the root-context packages this guard sweeps: every
// package whose code can run as root (or as a dropped child a root process
// forked) somewhere in the substrate actor lifecycle. This is the narrow,
// enforceable slice of the wider "never resolve a bare command name against
// an inherited PATH" rule: a static check over exec.Command/
// exec.CommandContext call sites, not a full data-flow prover.
var guardedDirs = []string{
	"cmd/sciontool",
	"cmd/sciontool/commands",
	"pkg/sciontool/substrate",
	"pkg/sciontool/hooks",
	"pkg/sciontool/hooks/dialects",
	"pkg/sciontool/hooks/handlers",
	"pkg/sciontool/metadata",
	"pkg/sciontool/supervisor",
	"pkg/sciontool/services",
}

// execSiteAllowlist lists every exec.Command/exec.CommandContext call site
// in guardedDirs whose command argument this guard's static check cannot
// itself prove routes through rootexec.Resolve (or is otherwise not a
// PATH-searched bare name), keyed by "relative/path.go:line" (the line of
// the call's opening parenthesis) with a one-line reason. Each reason below
// is the record of why that site is exempt — there is no separate document
// to cross-reference.
//
// A revert of any of these call sites back to an actual bare name literal
// is still caught: the allowlist only excuses "this guard cannot prove the
// current expression safe by itself", never "any expression is fine here".
var execSiteAllowlist = map[string]string{
	// Runs before RunInit populates any workload-owned directory, so no
	// workload-influenceable PATH entry exists yet: realigning the "scion"
	// system account's uid/gid.
	"cmd/sciontool/commands/init.go:2291": "before workload setup (host-user realignment); no workload-influenceable PATH entry exists yet",
	"cmd/sciontool/commands/init.go:2296": "before workload setup (host-user realignment); no workload-influenceable PATH entry exists yet",
	"cmd/sciontool/commands/init.go:2398": "before workload setup (direct /etc/passwd,/etc/group sed fallback); no workload-influenceable PATH entry exists yet",
	"cmd/sciontool/commands/init.go:2407": "before workload setup (direct /etc/passwd,/etc/group sed fallback); no workload-influenceable PATH entry exists yet",

	// gitCloneWorkspace's clone-path git calls: configureGitCommand sets a
	// Credential to (uid, gid) whenever uid > 0, which requirePrivilegeDropOrFail
	// (RunInit, before this ever runs) guarantees on substrate. These always
	// run dropped there.
	"cmd/sciontool/commands/init.go:2589": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2607": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2622": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2678": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2692": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2703": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2722": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2741": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2749": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2753": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:2763": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",
	"cmd/sciontool/commands/init.go:3201": "dropped: configureGitCommand sets Credential when uid>0, guaranteed on substrate",

	// The harness-provision subcommand's own subprocess: by the time this
	// code runs, the OS process is already the dropped workload uid/gid
	// (hooks.buildDroppedProvisionCmd sets Credential on the *parent* fork
	// that execs this subcommand in the first place) — never root.
	"cmd/sciontool/commands/harness.go:157": "runs inside the already-dropped harness-provision subprocess; never root",

	// hooks/lifecycle.go's non-enforced executeScript: unchanged, pre-
	// existing behavior for every runtime except substrate
	// (EnforcePrivilegeDrop is false there) — root is not a security
	// boundary on those runtimes. path is an absolute path built by the
	// caller, not a bare name.
	"pkg/sciontool/hooks/lifecycle.go:309": "non-enforced branch (non-substrate runtimes only); path is an absolute path, not a bare name",

	// execViaFd's fexecve-equivalent: execScriptPath is the fixed
	// "/proc/self/fd/<n>" magic path, never PATH-resolved.
	"pkg/sciontool/hooks/exec_enforced.go:231": "execScriptPath is a fixed /proc/self/fd magic path, never PATH-resolved",

	// supervisor.Run: args[0] is the operator/harness-selected entrypoint.
	// Run() sets a Credential before Start() whenever UID/GID are supplied,
	// and fails closed (ErrPrivilegeDropRequired) when RequirePrivilegeDrop
	// is set and they are not — the same Go-level "drop before exec" model
	// rootexec's own doc comment describes as the preferred route: the
	// bare-name lookup happens in the (root) parent using its inherited
	// PATH, but the resulting process always executes under the dropped
	// identity, so a planted binary there can never run as anything other
	// than the workload's own uid.
	"pkg/sciontool/supervisor/supervisor.go:136": "Go-level Credential drop before exec, or fails closed; never root",

	// services.Manager.start: svc.spec.Command[0] comes from a workload-
	// supplied services.yaml. Manager.Start requires uid/gid>0 (or fails
	// closed under requirePrivilegeDrop) before any service is started —
	// the identical Go-level drop-before-exec model as supervisor.Run.
	"pkg/sciontool/services/manager.go:373": "Go-level Credential drop before exec, or fails closed; never root",

	// runExec's outer sh: suCmd is built by execAsUserCmd, which resolves
	// "sh"/"su"/"whoami" via rootexec.Resolve (execResolve in tests) before
	// ever returning — see execAsUserCmd's own doc comment. The call site
	// here indexes into that already-resolved slice, which this guard's
	// static check cannot itself follow across the function-call boundary.
	"pkg/sciontool/substrate/exec.go:84": "suCmd[0]/suCmd[1:] come from execAsUserCmd, which resolves via rootexec.Resolve",
}

// aliasKind records what kind of exec constructor a package-level var
// aliases: argIndex is the position of the command-name argument (0 for
// exec.Command-shaped, 1 for exec.CommandContext-shaped, which takes a
// context.Context first).
type aliasKind struct{ argIndex int }

func TestNoRootContextExecUsesABareUnresolvedCommandName(t *testing.T) {
	repoRoot := findRepoRoot(t)

	fset := token.NewFileSet()
	var violations []string
	seenAllowlist := map[string]bool{}

	for _, dir := range guardedDirs {
		absDir := filepath.Join(repoRoot, dir)
		entries, err := os.ReadDir(absDir)
		if err != nil {
			t.Fatalf("read %s: %v", absDir, err)
		}

		// Pass 1: collect this package's own exec.Command/exec.CommandContext
		// aliases (e.g. "var execCommandContext = exec.CommandContext"),
		// across every non-test file in the directory.
		aliases := map[string]aliasKind{}
		files := map[string]*ast.File{}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(absDir, e.Name())
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			files[path] = f
			collectExecAliases(f, aliases)
		}

		// Pass 2: find every exec.Command/exec.CommandContext (direct or
		// aliased) call and classify its command-name argument.
		for path, f := range files {
			relPath, err := filepath.Rel(repoRoot, path)
			if err != nil {
				t.Fatalf("relpath %s: %v", path, err)
			}
			relPath = filepath.ToSlash(relPath)

			funcLikes := collectFuncLikes(f)

			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				argIndex, isExecCall := classifyExecCall(call, aliases)
				if !isExecCall {
					return true
				}
				if len(call.Args) <= argIndex {
					return true
				}
				arg := call.Args[argIndex]
				line := fset.Position(call.Pos()).Line
				siteKey := fmt.Sprintf("%s:%d", relPath, line)

				if isProvenSafe(arg, funcLikes, call.Pos()) {
					return true
				}
				if reason, ok := execSiteAllowlist[siteKey]; ok {
					seenAllowlist[siteKey] = true
					_ = reason
					return true
				}
				violations = append(violations, fmt.Sprintf(
					"%s: exec call's command argument is not provably routed through rootexec.Resolve (or an absolute/literal path), and is not on the allowlist",
					siteKey))
				return true
			})
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("found %d root-context exec call(s) with an unresolved bare command name:\n%s",
			len(violations), strings.Join(violations, "\n"))
	}

	// Keep the allowlist honest: an entry for a site that no longer exists
	// (the call was removed, moved, or genuinely fixed) should be deleted,
	// not left to silently mask the next real regression at that line.
	var stale []string
	for site := range execSiteAllowlist {
		if !seenAllowlist[site] {
			stale = append(stale, site)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("execSiteAllowlist has %d stale entry/entries (no matching call site found) — remove them:\n%s",
			len(stale), strings.Join(stale, "\n"))
	}
}

// collectExecAliases scans f for top-level "var X = exec.Command" or
// "var X = exec.CommandContext" declarations and records them in aliases.
func collectExecAliases(f *ast.File, aliases map[string]aliasKind) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, val := range vs.Values {
				sel, ok := val.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok || pkgIdent.Name != "exec" {
					continue
				}
				switch sel.Sel.Name {
				case "Command":
					aliases[vs.Names[i].Name] = aliasKind{argIndex: 0}
				case "CommandContext":
					aliases[vs.Names[i].Name] = aliasKind{argIndex: 1}
				}
			}
		}
	}
}

// classifyExecCall reports whether call is a (possibly aliased)
// exec.Command/exec.CommandContext invocation, and if so, which argument
// index carries the command name.
func classifyExecCall(call *ast.CallExpr, aliases map[string]aliasKind) (argIndex int, ok bool) {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		pkgIdent, ok := fun.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "exec" {
			return 0, false
		}
		switch fun.Sel.Name {
		case "Command":
			return 0, true
		case "CommandContext":
			return 1, true
		}
		return 0, false
	case *ast.Ident:
		if k, ok := aliases[fun.Name]; ok {
			return k.argIndex, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// funcLike is either a *ast.FuncDecl or a *ast.FuncLit, treated uniformly
// as "a function body with a position range".
type funcLike struct {
	body     *ast.BlockStmt
	pos, end token.Pos
}

// collectFuncLikes returns every function declaration and function literal
// in f, so isProvenSafe can find the SMALLEST one enclosing a given call —
// necessary because this codebase routinely builds an exec.Cmd inside a
// closure (e.g. configureSharedWorkspaceGit's runPrivateGitConfig) whose
// own local variables are what a backward scan must see.
func collectFuncLikes(f *ast.File) []funcLike {
	var out []funcLike
	ast.Inspect(f, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				out = append(out, funcLike{body: fn.Body, pos: fn.Body.Pos(), end: fn.Body.End()})
			}
		case *ast.FuncLit:
			out = append(out, funcLike{body: fn.Body, pos: fn.Body.Pos(), end: fn.Body.End()})
		}
		return true
	})
	return out
}

// enclosingFunc finds the smallest funcLike containing pos.
func enclosingFunc(funcLikes []funcLike, pos token.Pos) *funcLike {
	var best *funcLike
	for i := range funcLikes {
		fl := funcLikes[i]
		if fl.pos <= pos && pos <= fl.end {
			if best == nil || (fl.end-fl.pos) < (best.end-best.pos) {
				b := fl
				best = &b
			}
		}
	}
	return best
}

// isProvenSafe reports whether arg (the command-name argument of an exec
// call at position callPos) is one of:
//   - a string literal containing "/" (not a bare name PATH would search for)
//   - an inline call to rootexec.Resolve(...)
//   - an identifier whose nearest preceding assignment, anywhere in the
//     smallest enclosing function/closure, is either an inline
//     rootexec.Resolve(...) call or a string literal containing "/"
//
// Anything else — including an identifier with no local assignment found
// (e.g. a function parameter), a bare string literal, or an identifier
// last assigned from a bare string literal — is NOT proven safe, and the
// call site must be on execSiteAllowlist instead.
func isProvenSafe(arg ast.Expr, funcLikes []funcLike, callPos token.Pos) bool {
	switch e := arg.(type) {
	case *ast.BasicLit:
		return literalIsPathLike(e)
	case *ast.CallExpr:
		return isRootexecResolveCall(e)
	case *ast.Ident:
		fl := enclosingFunc(funcLikes, callPos)
		if fl == nil {
			return false
		}
		rhs := lastAssignmentBefore(fl.body, e.Name, callPos)
		if rhs == nil {
			return false
		}
		switch r := rhs.(type) {
		case *ast.CallExpr:
			return isRootexecResolveCall(r)
		case *ast.BasicLit:
			return literalIsPathLike(r)
		default:
			return false
		}
	default:
		return false
	}
}

// literalIsPathLike reports whether a BasicLit STRING contains a "/" —
// i.e. it is a path, not a bare name that would be searched for on PATH.
func literalIsPathLike(lit *ast.BasicLit) bool {
	if lit.Kind != token.STRING {
		return false
	}
	v := strings.Trim(lit.Value, "`\"")
	return strings.ContainsRune(v, '/')
}

// isRootexecResolveCall reports whether call is exactly "rootexec.Resolve(...)".
func isRootexecResolveCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	return ok && pkgIdent.Name == "rootexec" && sel.Sel.Name == "Resolve"
}

// lastAssignmentBefore scans body for the textually-last assignment (":="
// or "=") to name at pos < before, and returns its right-hand side
// expression, or nil if none is found. It does not attempt real scoping —
// only "is there an assignment to this name earlier in this same
// function/closure body" — which is exactly the shape every call site in
// this codebase actually uses (resolve, then use a few lines later in the
// same function).
func lastAssignmentBefore(body *ast.BlockStmt, name string, before token.Pos) ast.Expr {
	var found ast.Expr
	var foundPos token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if ok && assign.Pos() < before {
			for i, lhs := range assign.Lhs {
				id, idOk := lhs.(*ast.Ident)
				if !idOk || id.Name != name || i >= len(assign.Rhs) {
					continue
				}
				if assign.Pos() > foundPos {
					found = assign.Rhs[i]
					foundPos = assign.Pos()
				}
			}
		}
		// Always keep descending: a matching assignment can be nested
		// inside an if/for/switch/block at any depth within this function.
		return true
	})
	return found
}

// findRepoRoot walks up from this test file's own directory until it finds
// go.mod, so the guard works regardless of the working directory `go test`
// happens to run it from.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root (go.mod) walking up from " + thisFile)
		}
		dir = parent
	}
}

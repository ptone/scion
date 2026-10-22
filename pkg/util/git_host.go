// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package util

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Host-side git invocations.
//
// The hub and the broker run git on the host in repositories that agent
// containers can also write: a linked checkout whose .git is mounted into
// worktree-per-agent containers, the shared checkout of a hub-native
// worktree-per-agent project, and the shared clone of a shared-workspace
// project. Git reads behaviour from the repository's own config and hooks,
// so these invocations run without repository hooks and without
// repository-defined commands:
//
//   - HostGitArgs is the fixed set of -c settings every host-side call
//     carries (hooks, fsmonitor, pager, the ext transport).
//   - HostGitCommand adds, for commands that write the work tree or talk to
//     a remote, settings derived from the repository's own config: filter
//     drivers defined there are replaced with the global/system definition
//     (or disabled), and for remote operations a repository config that
//     sets transport, credential or include keys is refused.
//
// Global and system config are the host user's and stay in effect: git-lfs
// installs its filter there, so LFS content is still checked out. Command
// line settings (-c) take precedence over every config file, including
// files pulled in with include.path, which is why pinning works without
// rewriting the repository config.

// HostGitArgs returns the -c settings every host-side git invocation in a
// repository that agents can write carries, ahead of the subcommand:
//
//   - core.hooksPath=/dev/null: no hook runs (post-checkout,
//     reference-transaction, post-merge, ...). Hooks are repository content
//     here, not host configuration.
//   - core.fsmonitor=false: no file-system monitor command runs when git
//     reads the index.
//   - core.pager=cat: never starts a configured pager.
//   - protocol.ext.allow=never: the ext:: transport, which runs a command,
//     is never used, whatever a config file allows.
func HostGitArgs() []string {
	return []string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "protocol.ext.allow=never",
	}
}

// hostGitNetworkArgs are added to HostGitArgs for remote operations:
// submodules are not fetched (their URLs come from repository content) and
// signature verification, which runs gpg.program, is off for the merge.
var hostGitNetworkArgs = []string{
	"-c", "fetch.recurseSubmodules=false",
	"-c", "submodule.recurse=false",
	"-c", "merge.verifySignatures=false",
}

// HostGitOptions configures HostGitCommand.
type HostGitOptions struct {
	// Config holds extra key=value settings passed as -c after HostGitArgs,
	// for example "safe.directory=<path>".
	Config []string
	// Env is the command environment; nil means os.Environ().
	Env []string
	// Network marks a command that fetches from or pushes to a remote. The
	// command is refused when the repository's own config sets a key in
	// unsafeNetworkConfigKeys.
	Network bool
}

// UnsafeRepoConfigError reports a repository config key that a host-side
// remote operation refuses to run with.
type UnsafeRepoConfigError struct {
	Dir   string
	Key   string
	Scope string
}

func (e *UnsafeRepoConfigError) Error() string {
	return fmt.Sprintf("git in %s: the repository's %s config sets %s, which host-side git does not use for remote operations; remove it from the repository config", e.Dir, e.Scope, e.Key)
}

// unsafeNetworkConfigKeys matches (lower-cased) keys that, set in the
// repository's own config, change where a fetch connects, which command it
// runs, or which credentials it sends:
//
//   - url.<base>.insteadOf / pushInsteadOf: rewrite the remote URL.
//   - credential.*: credential helpers (commands) and per-URL settings.
//   - core.sshCommand, core.askPass, core.gitProxy: commands.
//   - remote.<name>.uploadpack / receivepack / proxy / vcs: commands, proxy
//     and remote helper selection.
//   - include.path, includeIf.<cond>.path: pull in other files.
//   - protocol.*: transport allow-lists.
//   - http[.<url>].proxy: sends traffic, credentials included, elsewhere.
//   - gpg.*: signature programs.
//   - core.worktree: moves the work tree a merge writes to.
//   - core.alternateRefsCommand: a command.
var unsafeNetworkConfigKeys = []*regexp.Regexp{
	regexp.MustCompile(`^url\..*\.(insteadof|pushinsteadof)$`),
	regexp.MustCompile(`^credential\.`),
	regexp.MustCompile(`^core\.(sshcommand|askpass|gitproxy|worktree|alternaterefscommand|alternaterefsprefixes)$`),
	regexp.MustCompile(`^remote\..*\.(uploadpack|receivepack|proxy|vcs)$`),
	regexp.MustCompile(`^include\.path$`),
	regexp.MustCompile(`^includeif\..*\.path$`),
	regexp.MustCompile(`^protocol\.`),
	regexp.MustCompile(`^http\.(.*\.)?proxy$`),
	regexp.MustCompile(`^gpg\.`),
}

// filterDriverVars are the per-driver variables git reads for a filter
// driver (gitattributes(5)).
var filterDriverVars = []string{"clean", "smudge", "process", "required"}

// repoConfigScope reports whether a git config scope is part of the
// repository (and so writable by whoever can write the repository's .git):
// "local" is .git/config, "worktree" a worktree's config.worktree. Entries
// from included files carry the scope of the including file.
func repoConfigScope(scope string) bool {
	return scope == "local" || scope == "worktree"
}

type gitConfigEntry struct {
	scope string
	key   string
	value string
}

// parseGitConfigList parses `git config --show-scope -z --list` output:
// records "<scope>\0<key>\n<value>\0", or "<scope>\0<key>\0" for a key
// without a value.
func parseGitConfigList(out []byte) []gitConfigEntry {
	fields := bytes.Split(out, []byte{0})
	var entries []gitConfigEntry
	for i := 0; i+1 < len(fields); i += 2 {
		scope := string(fields[i])
		kv := string(fields[i+1])
		key, value, _ := strings.Cut(kv, "\n")
		entries = append(entries, gitConfigEntry{scope: scope, key: key, value: value})
	}
	return entries
}

// splitConfigKey splits a config key into section, subsection and
// variable: "filter.My.Driver.smudge" is ("filter", "My.Driver", "smudge").
// The subsection is empty for a two-part key.
func splitConfigKey(key string) (section, subsection, variable string) {
	first := strings.Index(key, ".")
	last := strings.LastIndex(key, ".")
	if first < 0 {
		return key, "", ""
	}
	if first == last {
		return key[:first], "", key[last+1:]
	}
	return key[:first], key[first+1 : last], key[last+1:]
}

// hostGitConfigPins reads the config git sees in dir and returns the extra
// -c settings HostGitCommand adds: every filter driver variable (clean,
// smudge, process, required) the repository config sets is set again to
// its global/system value, or, when there is none, to the empty value (no
// command; an empty process disables the whole driver) and required to
// false. Variables the repository does not set keep their global/system
// value. For a network command it returns an
// *UnsafeRepoConfigError for the first repository key in
// unsafeNetworkConfigKeys.
func hostGitConfigPins(ctx context.Context, dir string, lead []string, env []string, network bool) ([]string, error) {
	listArgs := append(append([]string{}, lead...), "config", "--show-scope", "-z", "--list")
	cmd := exec.CommandContext(ctx, "git", listArgs...)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read git config in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	entries := parseGitConfigList(out)

	repoDrivers := map[string]map[string]bool{}
	trusted := map[string]string{}
	for _, e := range entries {
		lower := strings.ToLower(e.key)
		section, subsection, variable := splitConfigKey(e.key)
		if repoConfigScope(e.scope) {
			if network {
				for _, re := range unsafeNetworkConfigKeys {
					if re.MatchString(lower) {
						return nil, &UnsafeRepoConfigError{Dir: dir, Key: e.key, Scope: e.scope}
					}
				}
			}
			if strings.EqualFold(section, "filter") && subsection != "" {
				v := strings.ToLower(variable)
				if slices.Contains(filterDriverVars, v) {
					if repoDrivers[subsection] == nil {
						repoDrivers[subsection] = map[string]bool{}
					}
					repoDrivers[subsection][v] = true
				}
			}
			continue
		}
		if (e.scope == "system" || e.scope == "global") && strings.EqualFold(section, "filter") && subsection != "" {
			// Later entries win, as in git: system is listed before global.
			trusted[subsection+"."+strings.ToLower(variable)] = e.value
		}
	}

	names := make([]string, 0, len(repoDrivers))
	for name := range repoDrivers {
		names = append(names, name)
	}
	sort.Strings(names)
	var pins []string
	for _, name := range names {
		for _, v := range filterDriverVars {
			if !repoDrivers[name][v] {
				continue
			}
			value, ok := trusted[name+"."+v]
			if !ok && v == "required" {
				value = "false"
			}
			pins = append(pins, "-c", "filter."+name+"."+v+"="+value)
		}
	}
	return pins, nil
}

// HostGitCommand builds a git command run in dir (cmd.Dir) with
// HostGitArgs, opts.Config, and the settings derived from the repository
// config (see hostGitConfigPins) ahead of args. Use it for host-side
// commands that write the work tree (worktree add, checkout, switch,
// merge), refresh the index (worktree remove, status) or talk to a remote
// (opts.Network). It runs one `git config --list` in dir first and returns
// an error, without running anything else, if that fails or the
// repository config is refused.
func HostGitCommand(ctx context.Context, dir string, opts HostGitOptions, args ...string) (*exec.Cmd, error) {
	if dir == "" {
		return nil, fmt.Errorf("HostGitCommand: dir is required")
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	lead := HostGitArgs()
	if opts.Network {
		lead = append(lead, hostGitNetworkArgs...)
	}
	for _, kv := range opts.Config {
		lead = append(lead, "-c", kv)
	}
	pins, err := hostGitConfigPins(ctx, dir, lead, env, opts.Network)
	if err != nil {
		return nil, err
	}
	full := append(append(append([]string{}, lead...), pins...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd, nil
}

// hostGit returns a git command with HostGitArgs ahead of args, for
// host-side commands that neither write the work tree nor read the index
// through filters (rev-parse, worktree list, show-ref, branch -D, config).
func hostGit(args ...string) *exec.Cmd {
	return exec.Command("git", append(HostGitArgs(), args...)...)
}

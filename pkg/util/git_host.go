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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
//     carries (hooks, fsmonitor, pager, the ext transport, submodule
//     recursion, merge signature checks).
//   - HostGitEnv drops GIT_* variables that choose a repository, add config
//     or name programs from the environment git runs with.
//   - HostGitCommand adds, for commands that write the work tree or talk to
//     a remote, settings derived from the repository's own config: filter
//     drivers defined there are replaced with the global/system definition
//     (or disabled), and for remote operations a repository config with keys
//     outside a small allowlist is refused. It checks again after the
//     command that the repository config it depended on did not change.
//
// Global and system config are the host user's and stay in effect: git-lfs
// installs its filter there, so LFS content is still checked out. Command
// line settings (-c) take precedence over every config file, including
// files pulled in with include.path, which is why pinning works without
// rewriting the repository config.
//
// The repository config these commands read can still be written by agents
// on runtimes where it is mounted read-write. The check after each command
// detects a lasting change; keeping agents from writing the repository's git
// config and hooks at all is done by the runtime mounts.

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
//   - submodule.recurse=false, fetch.recurseSubmodules=false: git never
//     works inside submodules, whose own config (.git/modules/<name>) is not
//     part of the checks here, and never fetches from submodule URLs.
//   - merge.verifySignatures=false: a merge never runs gpg.program.
func HostGitArgs() []string {
	return []string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		"-c", "protocol.ext.allow=never",
		"-c", "submodule.recurse=false",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "merge.verifySignatures=false",
	}
}

// hostGitEnvKept are the GIT_* variables HostGitEnv keeps. They choose the
// host user's own config files and the transport and authentication the
// host operator set up; none of them chooses a repository or adds
// repository config.
var hostGitEnvKept = map[string]bool{
	"GIT_CONFIG_GLOBAL":   true,
	"GIT_CONFIG_SYSTEM":   true,
	"GIT_CONFIG_NOSYSTEM": true,
	"GIT_SSH":             true,
	"GIT_SSH_COMMAND":     true,
	"GIT_SSH_VARIANT":     true,
	"GIT_ASKPASS":         true,
	"GIT_TERMINAL_PROMPT": true,
	"GIT_SSL_CAINFO":      true,
	"GIT_SSL_CAPATH":      true,
	"GIT_ALLOW_PROTOCOL":  true,
	"GIT_HTTP_USER_AGENT": true,
	// Only stops repository discovery from walking above the listed
	// directories.
	"GIT_CEILING_DIRECTORIES": true,
	// Makes git treat every repository as owned by someone else (stricter
	// safe.directory checks); used by tests.
	"GIT_TEST_ASSUME_DIFFERENT_OWNER": true,
}

// HostGitEnv returns env without GIT_* variables other than hostGitEnvKept:
// no inherited GIT_DIR, GIT_WORK_TREE, GIT_COMMON_DIR, GIT_INDEX_FILE,
// GIT_CONFIG_COUNT/KEY/VALUE, GIT_CONFIG_PARAMETERS, GIT_EXEC_PATH, editor or
// pager variables. Callers append the GIT_* variables they set themselves.
func HostGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") && !hostGitEnvKept[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// HostGitOptions configures HostGitCommand.
type HostGitOptions struct {
	// Config holds extra key=value settings passed as -c after HostGitArgs,
	// for example "safe.directory=<path>".
	Config []string
	// Env is the command environment; nil means HostGitEnv(os.Environ()).
	// A caller that sets it starts from HostGitEnv itself.
	Env []string
	// Network marks a command that fetches from or pushes to a remote. It
	// has no effect on the policy check (deny-by-default applies either way);
	// it only widens the before/after snapshot to every repository key.
	Network bool
	// Policy selects the deny-by-default allow-list the repository config is
	// checked against before the command runs. The zero value,
	// RepoPolicyManaged, is the strict list for a Scion-managed repository;
	// RepoPolicyLinked is the wider list for the user's own checkout.
	Policy RepoConfigPolicy
	// AllowedWorktreePath, when set, is the one path a repository-scope
	// core.worktree may equal without the command being refused.
	AllowedWorktreePath string
}

// ErrRepoConfigChanged is returned (wrapped) when the repository config a
// host-side git command depended on changed while the command ran.
var ErrRepoConfigChanged = errors.New("the repository's git config changed while host-side git ran")

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

// hostGitConfig is what HostGitCommand derives from one read of the config
// git sees in a directory.
type hostGitConfig struct {
	// pins are the extra -c settings for repository filter drivers.
	pins []string
	// snapshot lists the repository-scope entries the pins and the network
	// refusal depend on, to compare after the command.
	snapshot []string
}

// readHostGitConfig lists the config git sees in dir (with the leading
// args lead) and derives hostGitConfig from it:
//
//   - every filter driver variable (clean, smudge, process, required) the
//     repository config sets is set again to its global/system value, or,
//     when there is none, to the empty value (no command; an empty process
//     disables the whole driver) and required to false. Variables the
//     repository does not set keep their global/system value.
//
// The snapshot of repository-scope keys it records is compared after the
// command (HostGitCmd); for a network command it covers every key, otherwise
// the filter keys. Refusal is the deny-by-default policy's job
// (checkRepoConfigPolicy), not this function's.
func readHostGitConfig(ctx context.Context, dir string, lead []string, env []string, network bool) (hostGitConfig, error) {
	listArgs := append(append([]string{}, lead...), "config", "--show-scope", "-z", "--list")
	cmd := exec.CommandContext(ctx, "git", listArgs...)
	cmd.Dir = dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return hostGitConfig{}, fmt.Errorf("read git config in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}

	var cfg hostGitConfig
	repoDrivers := map[string]map[string]bool{}
	trusted := map[string]string{}
	for _, e := range parseGitConfigList(out) {
		section, subsection, variable := splitConfigKey(e.key)
		isFilter := strings.EqualFold(section, "filter") && subsection != ""
		if repoConfigScope(e.scope) {
			if isFilter || network {
				cfg.snapshot = append(cfg.snapshot, e.scope+"\x00"+e.key+"\x00"+e.value)
			}
			if isFilter {
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
		if (e.scope == "system" || e.scope == "global") && isFilter {
			// Later entries win, as in git: system is listed before global.
			trusted[subsection+"."+strings.ToLower(variable)] = e.value
		}
	}

	names := make([]string, 0, len(repoDrivers))
	for name := range repoDrivers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, v := range filterDriverVars {
			if !repoDrivers[name][v] {
				continue
			}
			value, ok := trusted[name+"."+v]
			if !ok && v == "required" {
				value = "false"
			}
			cfg.pins = append(cfg.pins, "-c", "filter."+name+"."+v+"="+value)
		}
	}
	return cfg, nil
}

// HostGitCmd is a host-side git command built by HostGitCommand. Run,
// Output and CombinedOutput run it and then read the repository config
// again: if the entries it was built from (repository filter drivers, and
// for a remote operation every repository key) changed, they return an
// error wrapping ErrRepoConfigChanged, after logging it, whatever the
// command's own result.
type HostGitCmd struct {
	*exec.Cmd
	verify func() error
}

// Run runs the command, then checks the repository config.
func (c *HostGitCmd) Run() error {
	return c.after(c.Cmd.Run())
}

// Output runs the command and returns its standard output, then checks the
// repository config.
func (c *HostGitCmd) Output() ([]byte, error) {
	out, err := c.Cmd.Output()
	return out, c.after(err)
}

// CombinedOutput runs the command and returns its combined output, then
// checks the repository config.
func (c *HostGitCmd) CombinedOutput() ([]byte, error) {
	out, err := c.Cmd.CombinedOutput()
	return out, c.after(err)
}

func (c *HostGitCmd) after(runErr error) error {
	if c.verify == nil {
		return runErr
	}
	if err := c.verify(); err != nil {
		slog.Error("host-side git: repository config changed while the command ran",
			"dir", c.Dir, "args", c.Args, "error", err)
		if runErr != nil {
			return fmt.Errorf("%w (command error: %v)", err, runErr)
		}
		return err
	}
	return runErr
}

// HostGitCommand builds a git command run in dir (cmd.Dir) with HostGitArgs,
// opts.Config and the repository filter-driver pins ahead of args. Use it for
// host-side commands that write the work tree (worktree add, checkout,
// switch, merge), refresh the index (status) or talk to a remote
// (opts.Network).
//
// Before building the command it reads the repository config files as data
// (never through git) and refuses, returning a *RepoConfigRefusedError
// without running anything, when the config holds an include directive or a
// key outside opts.Policy's allow-list (checkRepoConfigPolicy). It then reads
// the config once through git to compute the filter pins and a snapshot;
// running the returned command reads the config again afterwards and reports
// a change (HostGitCmd). The policy read is the primary control; the pins,
// HostGitArgs and the snapshot are defence in depth.
func HostGitCommand(ctx context.Context, dir string, opts HostGitOptions, args ...string) (*HostGitCmd, error) {
	if dir == "" {
		return nil, fmt.Errorf("HostGitCommand: dir is required")
	}
	env := opts.Env
	if env == nil {
		env = HostGitEnv(os.Environ())
	}
	allowOpts := repoConfigAllowOptions{worktreePath: opts.AllowedWorktreePath}
	if err := checkRepoConfigPolicy(dir, env, opts.Policy, allowOpts); err != nil {
		return nil, err
	}
	lead := HostGitArgs()
	for _, kv := range opts.Config {
		lead = append(lead, "-c", kv)
	}
	cfg, err := readHostGitConfig(ctx, dir, lead, env, opts.Network)
	if err != nil {
		return nil, err
	}
	full := append(append(append([]string{}, lead...), cfg.pins...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = env
	verify := func() error {
		// Re-check the policy (an agent may have added a refused key) and the
		// snapshot. A fresh context: the command's own may have expired.
		if err := checkRepoConfigPolicy(dir, env, opts.Policy, allowOpts); err != nil {
			return fmt.Errorf("%w: %v", ErrRepoConfigChanged, err)
		}
		after, err := readHostGitConfig(context.Background(), dir, lead, env, opts.Network)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRepoConfigChanged, err)
		}
		if !slices.Equal(cfg.snapshot, after.snapshot) {
			return fmt.Errorf("%w in %s", ErrRepoConfigChanged, dir)
		}
		return nil
	}
	return &HostGitCmd{Cmd: cmd, verify: verify}, nil
}

// hostGit returns a git command with HostGitArgs ahead of args and the
// environment from HostGitEnv, for host-side commands that neither write
// the work tree nor read the index through filters (rev-parse, worktree
// list, show-ref, branch -D, config). A caller that sets cmd.Env starts
// from HostGitEnv.
func hostGit(args ...string) *exec.Cmd {
	cmd := exec.Command("git", append(HostGitArgs(), args...)...)
	cmd.Env = HostGitEnv(os.Environ())
	return cmd
}

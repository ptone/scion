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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Deny-by-default repository config policy.
//
// Before a host-side git command that writes the work tree or talks to a
// remote runs in a repository agents can write, its config is read as data
// (never through git, which would run include directives and could split a
// -c pin at an '=') and checked against an allow-list. Anything outside the
// list, and any include/includeIf directive, refuses the command. The fixed
// -c pins (HostGitArgs), the environment scrub (HostGitEnv) and the
// before/after comparison stay on top as defence in depth.
//
// Two policies:
//   - RepoPolicyManaged: a repository Scion creates and owns — a hub-native
//     shared base, a shared-workspace clone. Only the keys git and Scion
//     write for it are allowed.
//   - RepoPolicyLinked: the user's own linked checkout. A wider list of keys
//     that cannot run a command or redirect network traffic is allowed too,
//     so ordinary local config does not stop an agent starting. Keys whose
//     value can carry a command are value-checked.
//
// Keys that execute a command or redirect the network (core.sshCommand,
// core.hooksPath, core.pager, core.editor, core.fsmonitor, credential.*,
// url.*.insteadOf, http.*, gpg.*, any *.program, alias.*, remote.* upload/
// receive/proxy/vcs) are never on either list, so deny-by-default refuses
// them. The read is as-data and does not depend on git's own config
// resolution, so a subsection containing '=' or quotes cannot slip a key
// past the check.

// RepoConfigPolicy selects the allow-list a host-side git command uses.
type RepoConfigPolicy int

const (
	// RepoPolicyManaged is the strict list for a Scion-managed repository.
	RepoPolicyManaged RepoConfigPolicy = iota
	// RepoPolicyLinked is the wider list for the user's own checkout.
	RepoPolicyLinked
)

// RepoConfigRefusedError reports that a repository config key (or an
// include directive) is not allowed for a host-side git command.
type RepoConfigRefusedError struct {
	Dir    string
	Key    string
	Value  string
	Reason string
}

func (e *RepoConfigRefusedError) Error() string {
	if e.Reason == "" {
		e.Reason = "not on the host-side git allow-list"
	}
	return fmt.Sprintf("git in %s refused: repository config sets %s, which is %s; remove it from the repository's .git/config (or config.worktree)", e.Dir, e.Key, e.Reason)
}

// standardLFSFilter is the exact filter.lfs.* command set `git lfs install`
// writes. filter.lfs.* is allowed only when it matches byte for byte.
var standardLFSFilter = map[string]string{
	"clean":   "git-lfs clean -- %f",
	"smudge":  "git-lfs smudge -- %f",
	"process": "git-lfs filter-process",
}

// submoduleUpdateAllowed are the submodule.<name>.update values that run no
// command (never a "!command").
var submoduleUpdateAllowed = map[string]bool{
	"none": true, "checkout": true, "rebase": true, "merge": true,
}

// repoConfigAllowOptions carries context a value check needs.
type repoConfigAllowOptions struct {
	// worktreePath, when set, is the one broker-known path core.worktree
	// may equal. Empty refuses any core.worktree.
	worktreePath string
}

// allowVar reports whether v is allowed under policy, with a reason when it
// is not. It never allows a key that can run a command, read or write an
// arbitrary host path, or redirect the network; keys whose value may carry
// one of those are value-checked. topLevel below means [section] (no
// subsection) — [section ""] (an empty quoted subsection) is a different
// section and is treated as subsectioned, so it cannot impersonate a
// top-level key.
func allowVar(policy RepoConfigPolicy, v gitConfigVar, opts repoConfigAllowOptions) (bool, string) {
	sec := v.Section
	sub := v.Subsection
	name := v.Name
	topLevel := !v.HasSubsection
	hasSub := v.HasSubsection

	// include/includeIf pull in other files: always refused (the caller
	// refuses before reaching here too).
	if sec == "include" || sec == "includeif" {
		return false, "an include directive"
	}
	// Any variable named "program" names an executable (gpg.program,
	// gpg.ssh.program, ...).
	if name == "program" {
		return false, "a program setting"
	}

	// Keys allowed under both policies: what git and Scion write for a
	// repository they manage.
	switch sec {
	case "core":
		if topLevel {
			switch name {
			case "repositoryformatversion", "filemode", "bare", "logallrefupdates",
				"ignorecase", "precomposeunicode", "symlinks":
				return true, ""
			case "worktree":
				if opts.worktreePath != "" && v.Value == opts.worktreePath {
					return true, ""
				}
				return false, "a work-tree redirect"
			}
		}
	case "extensions":
		if topLevel {
			switch name {
			case "worktreeconfig", "relativeworktrees", "objectformat", "refstorage":
				return true, ""
			}
		}
	case "remote":
		if hasSub && (name == "url" || name == "fetch") {
			return true, ""
		}
	case "branch":
		if hasSub {
			switch name {
			case "remote", "merge", "rebase":
				return true, ""
			}
		}
		if topLevel && name == "autosetupmerge" {
			return true, ""
		}
	case "gc":
		if topLevel && name == "auto" {
			return true, ""
		}
	case "user":
		if topLevel && (name == "name" || name == "email") {
			return true, ""
		}
	}

	if policy == RepoPolicyManaged {
		return false, "not on the managed-repository allow-list"
	}

	// Wider list for the user's own linked checkout: keys that run nothing,
	// read or write no arbitrary host path, and redirect no network traffic.
	// A few keys that would run a command are allowed here only because a
	// fixed -c pin on every host-side call (HostGitArgs) overrides their
	// repository value — core.hooksPath, core.fsmonitor, core.pager and
	// submodule.recurse — so a planted value never executes.
	switch sec {
	case "core":
		if topLevel {
			switch name {
			case "autocrlf", "eol", "safecrlf", "whitespace", "commentchar",
				"quotepath", "longpaths", "protecthfs", "protectntfs",
				"checkstat", "trustctime", "untrackedcache", "fsyncmethod",
				"splitindex", "preloadindex", "abbrev",
				"sparsecheckout", "sparsecheckoutcone",
				// Neutralised by the HostGitArgs pins on every call.
				"hookspath", "fsmonitor", "pager":
				return true, ""
			}
		}
	case "remote":
		if hasSub {
			switch name {
			case "pushurl", "tagopt", "prune", "prunetags", "mirror",
				"promisor", "partialclonefilter", "fetchrecursesubmodules",
				"followremotehead":
				return true, ""
			case "uploadpack", "receivepack", "proxy", "vcs":
				return false, "a remote command or proxy override"
			}
		}
	case "branch":
		if hasSub {
			switch name {
			case "description", "pushremote", "mergeoptions", "autosetuprebase":
				return true, ""
			}
		}
		if topLevel && name == "autosetuprebase" {
			return true, ""
		}
	case "pull":
		if topLevel && (name == "rebase" || name == "ff" || name == "octopus" || name == "twohead") {
			return true, ""
		}
	case "push":
		if topLevel {
			switch name {
			case "default", "autosetupremote", "followtags", "gpgsign":
				return true, ""
			}
		}
	case "fetch":
		if topLevel {
			switch name {
			case "prune", "prunetags", "fsckobjects", "output", "parallel":
				return true, ""
			}
		}
	case "commit":
		if topLevel && (name == "gpgsign" || name == "verbose") {
			return true, ""
		}
	case "tag":
		if topLevel && (name == "gpgsign" || name == "sort" || name == "forcesignannotated") {
			return true, ""
		}
	case "init":
		if topLevel && name == "defaultbranch" {
			return true, ""
		}
	case "maintenance":
		// The background maintenance tasks are git built-ins; these keys only
		// enable/schedule them and name no command.
		if topLevel && (name == "auto" || name == "strategy" || name == "autodetach") {
			return true, ""
		}
		if hasSub && (name == "enabled" || name == "schedule") {
			return true, ""
		}
	case "user":
		// Signing key identity (a key id or path) is inert data; a planted
		// gpg/ssh *.program that would act on it is refused above, which
		// refuses the whole config.
		if topLevel && name == "signingkey" {
			return true, ""
		}
	case "gpg":
		// SSH/commit signing format only — inert data. Any gpg program key is
		// refused (name == "program" above), and any gpg.<fmt>.program would
		// be too; gpg.ssh.allowedSignersFile and the like (host-file reads)
		// are not on the list.
		if topLevel && name == "format" {
			return true, ""
		}
		return false, "a gpg setting that is not inert"
	case "color":
		// color.<slot> and color.<cmd>.<slot>: enums/booleans, never a
		// command, and no color key names a program.
		if topLevel && colorTopKeys[name] {
			return true, ""
		}
		if hasSub && colorCmds[strings.ToLower(sub)] {
			return true, ""
		}
	case "advice":
		if topLevel {
			// advice.* are all booleans.
			return true, ""
		}
	case "rerere":
		if topLevel && (name == "enabled" || name == "autoupdate") {
			return true, ""
		}
	case "status":
		if topLevel && statusKeys[name] {
			return true, ""
		}
	case "diff":
		if topLevel && diffTopKeys[name] {
			return true, ""
		}
		if hasSub && diffDriverKeys[name] {
			// A named diff driver's data attributes only (never textconv,
			// command or cachetextconv).
			return true, ""
		}
	case "merge":
		if topLevel && mergeTopKeys[name] {
			return true, ""
		}
		if hasSub && (name == "name" || name == "recursive") {
			// A named merge driver's description/strategy only (never driver
			// or cmd).
			return true, ""
		}
	case "submodule":
		if topLevel && name == "recurse" {
			// Pinned off by HostGitArgs, so inert.
			return true, ""
		}
		if hasSub {
			switch name {
			case "update":
				if submoduleUpdateAllowed[strings.ToLower(v.Value)] {
					return true, ""
				}
				return false, "a submodule update command"
			case "url":
				if submoduleURLAllowed(v.Value) {
					return true, ""
				}
				return false, "a submodule URL with a non-https/ssh transport"
			case "active", "ignore", "shallow", "branch", "fetchrecursesubmodules":
				return true, ""
			}
		}
		return false, "a submodule setting that is not on the allow-list"
	case "filter":
		if strings.EqualFold(sub, "lfs") && hasSub {
			if name == "required" {
				return true, ""
			}
			if want, ok := standardLFSFilter[name]; ok && v.Value == want {
				return true, ""
			}
			return false, "a non-standard git-lfs filter command"
		}
		return false, "a filter driver command"
	case "lfs":
		// Deny-by-default: only a small set of inert top-level keys. Every
		// subsectioned lfs.* (lfs.<url>.access, lfs.extension.<n>.*,
		// lfs.customtransfer.<n>.*) and the redirect/exec/host-path keys
		// (url, pushurl, storage, standalonetransferagent) are refused.
		if topLevel && lfsInertKeys[name] {
			return true, ""
		}
		return false, "a git-lfs setting that is not inert"
	}

	if policy == RepoPolicyLinked {
		return false, "not on the linked-checkout allow-list"
	}
	return false, "not on the allow-list"
}

// submoduleURLAllowed reports whether a submodule URL uses only the https or
// ssh transport (so recursion, were it ever on, could not reach an ext:: or
// file transport). Submodule recursion is pinned off regardless; this is a
// second bound.
func submoduleURLAllowed(url string) bool {
	u := strings.TrimSpace(url)
	lower := strings.ToLower(u)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "ssh://") {
		return true
	}
	// scp-like syntax user@host:path is ssh, but must not be a transport
	// prefix like ext::/file:/ and must not be an absolute or relative path.
	if strings.Contains(u, "://") {
		return false
	}
	if at := strings.Index(u, "@"); at > 0 {
		rest := u[at+1:]
		if colon := strings.Index(rest, ":"); colon > 0 && !strings.Contains(rest[:colon], "/") {
			return true
		}
	}
	return false
}

// Explicit per-section key sets for the linked-checkout policy. They list
// keys that carry only data or enums (never a command, path or program), so
// nothing in these sections can make host-side git execute. Any key not
// listed is refused by deny-by-default.
var (
	colorTopKeys = set("ui", "pager", "advice", "showbranch")
	colorCmds    = set("branch", "diff", "decorate", "grep", "interactive",
		"log", "push", "remote", "status", "transport", "add")
	statusKeys = set("short", "branch", "showstash", "showuntrackedfiles",
		"submodulesummary", "relativepaths", "displaycommentprefix",
		"renamelimit", "renames", "aheadbehind")
	diffTopKeys = set("algorithm", "renames", "renamelimit", "indentheuristic",
		"colormoved", "colormovedws", "context", "interhunkcontext",
		"mnemonicprefix", "noprefix", "wordregex", "submodule", "relative",
		"ignoresubmodules", "dirstat", "statgraphwidth", "wserrorhighlight",
		"suppressblankempty", "colorwords", "tabsize", "orderfile")
	diffDriverKeys = set("binary", "wordregex", "xfuncname")
	mergeTopKeys   = set("conflictstyle", "ff", "renamelimit", "renames",
		"stat", "log", "autostash", "verbosity", "directoryrenames",
		"renormalize", "branchdesc")
	// Inert git-lfs keys (timeouts, counts, booleans, in-repo globs). No
	// URL, path, storage location, custom transfer or extension command.
	lfsInertKeys = set("concurrenttransfers", "dialtimeout", "tlstimeout",
		"activitytimeout", "fetchrecentrefsdays", "fetchrecentcommitsdays",
		"fetchrecentremoterefs", "fetchrecentalways", "pruneoffsetdays",
		"pruneverifyremotealways", "locksverifyeverywhere", "locksverify",
		"skipdownloaderrors", "setlockablereadonly", "fetchinclude",
		"fetchexclude", "batch", "transfermaxretries", "contenttype")
)

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// checkRepoConfigPolicy reads, as data, every repository config file git
// would read for a command in dir — the common .git/config, and, when
// extensions.worktreeConfig is on, the main worktree's config.worktree and
// this worktree's config.worktree — and returns a *RepoConfigRefusedError
// for the first include directive or key the policy does not allow. It then
// cross-checks the parsed repository-scope entries against git's own
// --show-scope listing and refuses on any difference, so a parser divergence
// fails closed. It fails closed on anything it cannot locate, read or parse.
func checkRepoConfigPolicy(ctx context.Context, dir string, env []string, policy RepoConfigPolicy, opts repoConfigAllowOptions) error {
	files, err := repoConfigFiles(dir, env)
	if err != nil {
		return fmt.Errorf("git in %s: cannot locate the repository config to check it: %w", dir, err)
	}
	var parsed []gitConfigVar
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("git in %s: cannot read %s: %w", dir, f, err)
		}
		vars, err := parseGitConfigFile(data)
		if err != nil {
			return fmt.Errorf("git in %s: cannot parse %s (refusing): %w", dir, f, err)
		}
		parsed = append(parsed, vars...)
	}
	for _, v := range parsed {
		if v.Section == "include" || v.Section == "includeif" {
			return &RepoConfigRefusedError{Dir: dir, Key: v.Key(), Value: v.Value, Reason: "an include directive"}
		}
		if ok, reason := allowVar(policy, v, opts); !ok {
			return &RepoConfigRefusedError{Dir: dir, Key: v.Key(), Value: v.Value, Reason: reason}
		}
	}
	return crossCheckAgainstGit(ctx, dir, env, parsed)
}

// crossCheckAgainstGit lists the repository-scope config git itself resolves
// (local and worktree scope) and refuses if it differs from the parsed set,
// so any divergence between the as-data parser and git fails closed. git runs
// with the same host-side pins and scrubbed environment.
func crossCheckAgainstGit(ctx context.Context, dir string, env []string, parsed []gitConfigVar) error {
	args := append(HostGitArgs(), "config", "--show-scope", "-z", "--list")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git in %s: cannot list the repository config to cross-check it: %w", dir, err)
	}
	parsedSet := make(map[string]int, len(parsed))
	for _, v := range parsed {
		parsedSet[strings.ToLower(v.Key())+"\x00"+v.Value]++
	}
	// Every repository-scope entry git actually resolves must appear in the
	// parsed set. The parsed set may hold more — config.worktree is parsed
	// even when extensions.worktreeConfig is off, which git then ignores — so
	// this is a subset check, not equality: it catches any key or value the
	// parser missed or read differently (a parser divergence), which is the
	// security-relevant direction, while allowing the intentional extra.
	for _, e := range parseGitConfigList(out) {
		if !repoConfigScope(e.scope) {
			continue
		}
		k := strings.ToLower(e.key) + "\x00" + e.value
		if parsedSet[k] <= 0 {
			return &RepoConfigRefusedError{Dir: dir, Key: e.key, Reason: "read differently by the config parser and by git; refusing to be safe"}
		}
		parsedSet[k]--
	}
	return nil
}

// repoConfigFiles returns the config files to check for a command in dir,
// honouring GIT_COMMON_DIR / GIT_DIR in env when set: the common
// .git/config, the main worktree's config.worktree (in the common dir), and
// this worktree's config.worktree. config.worktree is checked ALWAYS, even
// when extensions.worktreeConfig is off, so a refused key placed there is
// never missed (fail closed) — git would read it the moment the extension is
// turned on. It resolves a .git gitfile pointer and a worktree's commondir
// file without running git.
func repoConfigFiles(dir string, env []string) ([]string, error) {
	commonDir, perWorktreeDir, err := resolveGitDirs(dir, env)
	if err != nil {
		return nil, err
	}
	files := []string{
		filepath.Join(commonDir, "config"),
		filepath.Join(commonDir, "config.worktree"),
	}
	if perWorktreeDir != "" && perWorktreeDir != commonDir {
		files = append(files, filepath.Join(perWorktreeDir, "config.worktree"))
	}
	return files, nil
}

// resolveGitDirs returns the common git directory and the per-worktree git
// directory for dir. env's GIT_COMMON_DIR and GIT_DIR take precedence.
func resolveGitDirs(dir string, env []string) (commonDir, perWorktreeDir string, err error) {
	envCommon := gitEnvValue(env, "GIT_COMMON_DIR")
	envGitDir := gitEnvValue(env, "GIT_DIR")

	perWorktreeDir = envGitDir
	if perWorktreeDir == "" {
		perWorktreeDir, err = gitDirFromWorkTree(dir)
		if err != nil {
			return "", "", err
		}
	}
	commonDir = envCommon
	if commonDir == "" {
		commonDir = commonDirFrom(perWorktreeDir)
	}
	return absClean(commonDir, dir), absClean(perWorktreeDir, dir), nil
}

// gitDirFromWorkTree resolves <dir>/.git: a directory is the git dir itself;
// a "gitdir: <path>" file points to the per-worktree admin directory.
func gitDirFromWorkTree(dir string) (string, error) {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return gitPath, nil
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("%s is not a gitdir pointer", gitPath)
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if target == "" {
		return "", fmt.Errorf("%s has an empty gitdir pointer", gitPath)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return target, nil
}

// commonDirFrom returns the common git directory for a per-worktree git
// directory: its "commondir" file, resolved, or gitDir itself.
func commonDirFrom(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	rel := strings.TrimSpace(string(data))
	if rel == "" {
		return gitDir
	}
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(gitDir, rel)
}

func absClean(path, base string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

// gitEnvValue returns the value of the last matching KEY=... entry in env,
// or "".
func gitEnvValue(env []string, key string) string {
	val := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			val = v
		}
	}
	return val
}

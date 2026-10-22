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
	"fmt"
	"os"
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
// is not. It never allows a key that can run a command; keys whose value may
// carry one are value-checked.
func allowVar(policy RepoConfigPolicy, v gitConfigVar, opts repoConfigAllowOptions) (bool, string) {
	sec := v.Section
	sub := v.Subsection
	name := v.Name

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
	case "extensions":
		switch name {
		case "worktreeconfig", "relativeworktrees", "objectformat", "refstorage":
			return true, ""
		}
	case "remote":
		if sub != "" && (name == "url" || name == "fetch") {
			return true, ""
		}
	case "branch":
		if sub != "" {
			switch name {
			case "remote", "merge", "rebase":
				return true, ""
			}
		}
		if sub == "" && name == "autosetupmerge" {
			return true, ""
		}
	case "gc":
		if name == "auto" {
			return true, ""
		}
	case "user":
		if name == "name" || name == "email" {
			return true, ""
		}
	}

	if policy == RepoPolicyManaged {
		return false, "not on the managed-repository allow-list"
	}

	// Wider list for the user's own linked checkout: keys that run nothing
	// and redirect no network traffic. Keys whose value can carry a command
	// are value-checked.
	switch sec {
	case "core":
		switch name {
		case "autocrlf", "eol", "safecrlf", "whitespace", "commentchar",
			"quotepath", "longpaths", "protecthfs", "protectntfs",
			"checkstat", "trustctime", "untrackedcache", "fsyncmethod",
			"splitindex", "preloadindex", "abbrev", "pager-is-not-a-key":
			return true, ""
		}
	case "remote":
		if sub != "" {
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
		if sub != "" {
			switch name {
			case "description", "pushremote", "mergeoptions", "autosetuprebase":
				return true, ""
			}
		}
		if sub == "" && name == "autosetuprebase" {
			return true, ""
		}
	case "pull":
		if name == "rebase" || name == "ff" || name == "octopus" || name == "twohead" {
			return true, ""
		}
	case "push":
		switch name {
		case "default", "autosetupremote", "followtags", "gpgsign":
			return true, ""
		}
	case "fetch":
		switch name {
		case "prune", "prunetags", "fsckobjects", "output", "parallel":
			return true, ""
		}
	case "commit":
		if name == "gpgsign" || name == "verbose" {
			return true, ""
		}
	case "tag":
		if name == "gpgsign" || name == "sort" || name == "forcesignannotated" {
			return true, ""
		}
	case "init":
		if name == "defaultbranch" {
			return true, ""
		}
	case "color", "advice", "rerere", "status", "diff", "merge":
		// Only booleans/enums here never run a command. Refuse the ones that
		// name a program; allow the rest of these display/behaviour knobs.
		if diffMergeRunsCommand(sec, sub, name) {
			return false, "a diff/merge command or textconv"
		}
		return true, ""
	case "submodule":
		if sub != "" && name == "update" {
			if submoduleUpdateAllowed[strings.ToLower(v.Value)] {
				return true, ""
			}
			return false, "a submodule update command"
		}
		return false, "a submodule setting"
	case "filter":
		if strings.EqualFold(sub, "lfs") {
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
		// git-lfs data keys. Anything naming a path or a custom transfer
		// program runs or reads host files.
		if strings.EqualFold(sub, "customtransfer") || name == "path" {
			return false, "a git-lfs transfer command"
		}
		return true, ""
	}

	return false, "not on the linked-checkout allow-list"
}

// diffMergeRunsCommand reports whether a diff.* or merge.* key names a
// command or textconv (which git would execute).
func diffMergeRunsCommand(sec, sub, name string) bool {
	if sec == "diff" {
		if sub != "" && (name == "textconv" || name == "command" || name == "cachetextconv") {
			return true
		}
		if sub == "" && (name == "external" || name == "tool") {
			return true
		}
	}
	if sec == "merge" {
		if sub != "" && (name == "driver" || name == "cmd") {
			return true
		}
		if sub == "" && name == "tool" {
			return true
		}
	}
	return false
}

// checkRepoConfigPolicy reads the repository config files git would read for
// a command in dir (the common .git/config and, when present, the worktree's
// config.worktree), as data, and returns a *RepoConfigRefusedError for the
// first include directive or key that policy does not allow. It fails closed:
// a config file it cannot locate or parse is an error.
func checkRepoConfigPolicy(dir string, env []string, policy RepoConfigPolicy, opts repoConfigAllowOptions) error {
	files, err := repoConfigFiles(dir, env)
	if err != nil {
		return fmt.Errorf("git in %s: cannot locate the repository config to check it: %w", dir, err)
	}
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
		for _, v := range vars {
			if v.Section == "include" || v.Section == "includeif" {
				return &RepoConfigRefusedError{Dir: dir, Key: v.Key(), Value: v.Value, Reason: "an include directive"}
			}
			if ok, reason := allowVar(policy, v, opts); !ok {
				return &RepoConfigRefusedError{Dir: dir, Key: v.Key(), Value: v.Value, Reason: reason}
			}
		}
	}
	return nil
}

// repoConfigFiles returns the config files to check for a git command run in
// dir, honouring GIT_COMMON_DIR / GIT_DIR in env when set: the common
// directory's config, and the per-worktree config.worktree when it exists.
// It resolves a .git gitfile pointer and a worktree's commondir file,
// without running git.
func repoConfigFiles(dir string, env []string) ([]string, error) {
	commonDir, perWorktreeDir, err := resolveGitDirs(dir, env)
	if err != nil {
		return nil, err
	}
	files := []string{filepath.Join(commonDir, "config")}
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

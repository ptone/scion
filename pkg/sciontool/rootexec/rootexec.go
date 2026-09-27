/*
Copyright 2026 The Scion Authors.
*/

// Package rootexec is the one place every root-context subprocess in
// sciontool resolves a bare command name and builds its environment.
//
// On substrate, root is a security boundary, but PID 1's own inherited PATH
// includes a directory the workload owns outright:
// "/usr/local/share/npm-global/bin" is chowned to the workload uid so the
// harness's own npm-installed tools can be found on PATH — and it sits
// ahead of "/usr/bin" on that PATH. A root process that execs a bare name
// like "sh", "iptables", "git", or "su" and lets the kernel/libc (or Go's
// os/exec, which reproduces the same search using os.Getenv("PATH")) walk
// that PATH can be handed a binary the workload planted there, and run it
// as root.
//
// Resolve fixes this by never consulting the ambient PATH at all: it
// searches a fixed, hardcoded list of directories that hold only
// image-installed, root-owned content, and independently verifies — by
// fd-walk, not by trusting the directory names — that both the resolved
// binary and every real directory leading to it are owned by uid 0 (or, on
// a runtime with no separate root/workload identity at all, by this
// process's own uid) and not group- or other-writable. Env builds the
// companion environment for the resulting exec.Cmd from scratch, so nothing
// PID 1 happened to inherit (another PATH, an LD_PRELOAD, a BASH_ENV) can
// steer it either.
package rootexec

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/dirfd"
)

// SearchPath is the fixed, hardcoded list of directories Resolve searches,
// in order, for a bare command name. It deliberately does not include
// "/usr/local/sbin" or "/usr/local/bin" (a common home for locally-installed
// software, and not guaranteed root-owned on every image), and it
// deliberately does not include "/opt/scion/bin": that directory's
// ownership varies by how a given image stages sciontool itself, and a
// caller that needs to re-invoke this same binary should resolve it via
// "/proc/self/exe" (the running inode) instead of a PATH search — see this
// package's doc comment and cmd/sciontool/commands' reExecWithCleanEnv for
// why a path-based re-exec of "myself" is its own, distinct hazard that a
// fixed search list does not solve.
//
// This list is intentionally NOT os.Getenv("PATH") and Resolve never reads
// that variable: every entry here is a location every supported image
// installs only root-owned system binaries into, regardless of whether that
// image also happens to merge "/bin" into "/usr/bin" (Resolve's
// verification tolerates that — see OpenNoFollowRootOwnedFile — the
// directories named here are what a caller may point PATH-free tooling at,
// not a claim that each is a distinct inode).
var SearchPath = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// errNotBareName is returned by Resolve when name is empty or already
// contains a path separator — Resolve exists to turn a bare name into a
// safe absolute path, not to re-verify a path a caller already built.
var errNotBareName = errors.New("rootexec: not a bare command name")

// Resolve searches SearchPath, in order, for name, and returns the first
// candidate that verifies as trusted: every real directory in its resolved
// path, and the resolved binary itself, owned by uid 0 (or this process's
// own euid — see dirfd's chainIsTrusted for why) and free of the group- and
// other-write bits.
//
// A candidate is first fully resolved with filepath.EvalSymlinks, so a
// legitimate root-installed symlink chain (e.g. Debian's
// "/usr/sbin/iptables" -> "/etc/alternatives/iptables" ->
// "/usr/sbin/iptables-nft", all root-owned directories) is followed rather
// than refused outright; OpenNoFollowRootOwnedFile then verifies the fully
// resolved destination's own chain by fd, so a hop through anything
// workload-writable is what actually gets refused, not symlinks as a class.
//
// Fails closed: if name is not found as a trusted executable under any
// SearchPath entry, Resolve returns an error and the caller must not exec
// anything — there is no fallback to $PATH or to any other location.
func Resolve(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '/') {
		return "", fmt.Errorf("%w: %q", errNotBareName, name)
	}

	var errs []error
	for _, dir := range SearchPath {
		candidate := filepath.Join(dir, name)
		resolved, err := verifyTrustedExecutable(candidate)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", candidate, err))
			continue
		}
		return resolved, nil
	}
	return "", fmt.Errorf("rootexec: %q not found as a trusted executable under %s: %w",
		name, strings.Join(SearchPath, ":"), errors.Join(errs...))
}

// verifyTrustedExecutable resolves candidate's full symlink chain, then
// verifies the destination the way OpenNoFollowRootOwnedFile's own doc
// comment describes, returning the destination path on success.
func verifyTrustedExecutable(candidate string) (string, error) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	f, err := dirfd.OpenNoFollowRootOwnedFile(resolved)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	return resolved, nil
}

// Env builds a hardened environment for a root-context exec.Cmd: the fixed
// PATH from SearchPath, plus whatever explicit "KEY=VALUE" pairs the caller
// supplies. Nothing else is carried over — no LD_*, BASH_ENV, ENV, IFS,
// GIT_*, PYTHON*, or anything else PID 1 happens to have inherited — a
// caller that genuinely needs one of those must state it explicitly in
// extra, the same way configureSharedWorkspaceGit states HOME,
// GIT_CONFIG_NOSYSTEM, and GIT_CONFIG_GLOBAL for its own git child.
func Env(extra ...string) []string {
	env := make([]string, 0, len(extra)+1)
	env = append(env, "PATH="+strings.Join(SearchPath, ":"))
	env = append(env, extra...)
	return env
}

// noInheritedExecEnvPrefixes and noInheritedExecEnvNames are the variables
// SanitizeInheritedEnv strips outright — the same closed set Env's own doc
// comment describes never being carried over into a from-scratch
// environment, applied here to a caller that (unlike every other root exec
// site) cannot simply build its whole environment from scratch, because it
// legitimately needs to keep other inherited, workload-derived variables
// (see SanitizeInheritedEnv's own doc comment).
var noInheritedExecEnvNames = map[string]bool{
	"PATH":     true,
	"BASH_ENV": true,
	"ENV":      true,
	"IFS":      true,
}

// isNeverInheritedExecEnvName reports whether key must never survive into a
// root exec's environment: an exact match against noInheritedExecEnvNames,
// or an LD_*, GIT_* (except the two names a shared-workspace git rewrite
// deliberately sets itself — see SanitizeInheritedEnv's caller), or
// PYTHON* prefix.
func isNeverInheritedExecEnvName(key string) bool {
	if noInheritedExecEnvNames[key] {
		return true
	}
	switch {
	case strings.HasPrefix(key, "LD_"):
		return true
	case strings.HasPrefix(key, "PYTHON"):
		return true
	case strings.HasPrefix(key, "GIT_") && key != "GIT_CONFIG_NOSYSTEM" && key != "GIT_CONFIG_GLOBAL":
		return true
	}
	return false
}

// SanitizeInheritedEnv returns a copy of env with PATH replaced by the fixed
// SearchPath, and every LD_*, BASH_ENV, ENV, IFS, GIT_* (other than
// GIT_CONFIG_NOSYSTEM/GIT_CONFIG_GLOBAL), and PYTHON* entry dropped
// outright — for the one root-context caller (a pre-start hook, which
// legitimately needs to keep its other workload-derived variables, e.g.
// HOME=AgentHome, so staged content resolves) that cannot simply build its
// whole environment from scratch the way every other root exec site in this
// codebase does. Order is preserved for everything that survives; the fixed
// PATH is appended once at the end.
func SanitizeInheritedEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "PATH" || isNeverInheritedExecEnvName(key) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "PATH="+strings.Join(SearchPath, ":"))
	return out
}

// SelfExe returns the path a root process must use to re-exec itself:
// "/proc/self/exe", the kernel's magic symlink to the running inode. Unlike
// os.Executable() (which re-reads a path from disk that may itself sit
// somewhere workload-writable, and so could resolve to a binary planted
// there since this process started) or a PATH search (Resolve is for
// external tools, and deliberately excludes any directory this binary might
// itself be staged in — see SearchPath's doc comment), execve on
// "/proc/self/exe" always re-execs the exact in-memory image already
// running, regardless of what — if anything — now sits at its on-disk path.
func SelfExe() string {
	return "/proc/self/exe"
}

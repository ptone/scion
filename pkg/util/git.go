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
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// IsGitRepo returns true if the current working directory is inside a git repository.
func IsGitRepo() bool {
	return IsGitRepoDir("")
}

// IsGitRepoDir returns true if the specified directory is inside a git repository.
func IsGitRepoDir(dir string) bool {
	args := []string{"rev-parse", "--is-inside-work-tree"}
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	err := cmd.Run()
	return err == nil
}

// GetGitVersion returns the git version string and the path to the git binary used.
func GetGitVersion() (string, string, error) {
	gitPath := os.Getenv("SCION_GIT_BINARY")
	if gitPath == "" {
		var err error
		gitPath, err = exec.LookPath("git")
		if err != nil {
			return "", "", err
		}
	}
	cmd := exec.Command(gitPath, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", gitPath, err
	}
	// Output is usually "git version 2.47.0"
	version := strings.TrimPrefix(strings.TrimSpace(string(output)), "git version ")
	return version, gitPath, nil
}

// CheckGitVersion returns an error if the git version is less than 2.47.0.
func CheckGitVersion() error {
	version, gitPath, err := GetGitVersion()
	if err != nil {
		return fmt.Errorf("failed to get git version: %w", err)
	}

	if err := CompareGitVersion(version, 2, 47); err != nil {
		return fmt.Errorf("git version 2.47.0 or newer is required; scion requires worktree support with relative paths (found %s at %s)", version, gitPath)
	}

	return nil
}

// CompareGitVersion returns an error if the version string is less than major.minor
func CompareGitVersion(version string, minMajor, minMinor int) error {
	// Simple version comparison
	// Format is expected to start with major.minor
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return fmt.Errorf("unexpected git version format: %s", version)
	}

	var major, minor int
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return fmt.Errorf("failed to parse git major version from %s: %w", parts[0], err)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minor); err != nil {
		return fmt.Errorf("failed to parse git minor version from %s: %w", parts[1], err)
	}

	if major < minMajor || (major == minMajor && minor < minMinor) {
		return fmt.Errorf("version %s is less than %d.%d", version, minMajor, minMinor)
	}

	return nil
}

// RepoRoot returns the absolute path to the root of the git repository.
func RepoRoot() (string, error) {
	return RepoRootDir("")
}

// RepoRootDir returns the absolute path to the root of the git repository for the specified directory.
func RepoRootDir(dir string) (string, error) {
	args := []string{"rev-parse", "--show-toplevel"}
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If rev-parse fails, it might be because we're in a .git directory.
		// Try running from parent.
		if dir != "" {
			parent := filepath.Dir(dir)
			if parent != dir {
				return RepoRootDir(parent)
			}
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// GetCommonGitDir returns the absolute path to the common git directory (the main .git dir).
func GetCommonGitDir(dir string) (string, error) {
	args := []string{"rev-parse", "--git-common-dir"}
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	commonDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(commonDir) {
		if dir == "" {
			var err error
			dir, err = os.Getwd()
			if err != nil {
				return "", err
			}
		}
		commonDir = filepath.Join(dir, commonDir)
	}
	return filepath.Clean(commonDir), nil
}

// IsIgnored returns true if the given path is ignored by git.
func IsIgnored(dir, path string) bool {
	cmd := exec.Command("git", "check-ignore", "-q", path)
	if dir != "" {
		cmd.Dir = dir
	}
	err := cmd.Run()
	return err == nil
}

// CreateWorktree creates a new git worktree at the specified path with a new branch.
func CreateWorktree(path, branch string) error {
	// Guard: refuse to create worktrees inside an agent container.
	// SCION_HOST_UID is set by the runtime when launching containers.
	// Creating worktrees inside containers produces path-identity mismatches
	// because --relative-paths are computed against the container mount layout,
	// not the host filesystem.
	if os.Getenv("SCION_HOST_UID") != "" {
		return fmt.Errorf("cannot create worktree: running inside an agent container (SCION_HOST_UID is set)")
	}

	// Resolve the main repository root via the common git dir. This is correct
	// even when called from within a worktree: GetCommonGitDir returns the
	// shared .git directory of the main repo, whose parent is the true root.
	dir := filepath.Dir(path)
	commonDir, err := GetCommonGitDir(dir)
	if err != nil {
		return fmt.Errorf("failed to find git common dir for worktree: %w", err)
	}
	root := filepath.Dir(commonDir)

	// git worktree add --relative-paths -b <branch> <path>
	// We run from root to ensure --relative-paths are calculated from root
	cmd := exec.Command("git", "worktree", "add", "--relative-paths", "-b", branch, path)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		outputStr := string(output)
		// If branch already exists, try to just add it
		if strings.Contains(outputStr, "already exists") {
			cmd = exec.Command("git", "worktree", "add", "--relative-paths", path, branch)
			cmd.Dir = root
			if output, err := cmd.CombinedOutput(); err != nil {
				outputStr = string(output)
				if strings.Contains(outputStr, "already checked out") {
					return fmt.Errorf("branch '%s' is already checked out in another worktree", branch)
				}
				return fmt.Errorf("failed to create worktree: %s", strings.TrimSpace(outputStr))
			}
			return nil
		}
		return fmt.Errorf("failed to create worktree: %s", strings.TrimSpace(outputStr))
	}
	return nil
}

// RemoveWorktree removes a git worktree at the specified path.
//
// Instead of using "git worktree remove" (which does its own directory
// deletion and can trigger macOS autofs timeouts on symlinks pointing to
// container-internal paths), this function:
//  1. Gathers git metadata (branch name, repo root) while the worktree exists.
//  2. Removes the worktree directory using RemoveAllSafe (which uses unlinkat
//     to avoid autofs triggers).
//  3. Runs "git worktree prune" to clean up the now-stale worktree record.
func RemoveWorktree(path string, deleteBranch bool) (bool, error) {
	var branchName string
	var repoRoot string
	branchDeleted := false

	// Get the common git dir (main repo's .git dir) — needed for both
	// pruning and optional branch deletion.
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-common-dir")
	output, err := cmd.Output()
	if err == nil {
		commonDir := strings.TrimSpace(string(output))
		if !filepath.IsAbs(commonDir) {
			// If relative, it's relative to the worktree root
			commonDir = filepath.Join(path, commonDir)
		}
		repoRoot = filepath.Dir(commonDir)
	}

	if deleteBranch {
		// Get branch name
		cmd = exec.Command("git", "-C", path, "branch", "--show-current")
		output, err = cmd.Output()
		if err == nil {
			branchName = strings.TrimSpace(string(output))
		}
	}

	// Remove the worktree directory ourselves using RemoveAllSafe, which
	// uses unlinkat for symlinks to avoid triggering macOS autofs timeouts.
	// This replaces "git worktree remove" which uses its own (slow) deletion.
	Debugf("RemoveWorktree: removing worktree directory %s via RemoveAllSafe", path)
	removeStart := time.Now()
	if err := RemoveAllSafe(path); err != nil {
		Debugf("RemoveWorktree: RemoveAllSafe failed in %v: %v", time.Since(removeStart), err)
		return false, err
	}
	Debugf("RemoveWorktree: directory removal completed in %v", time.Since(removeStart))

	// Prune stale worktree records — git still has a reference to the
	// directory we just deleted; pruning cleans that up.
	if repoRoot != "" {
		Debugf("RemoveWorktree: pruning stale worktree records in %s", repoRoot)
		_ = PruneWorktreesIn(repoRoot)
	} else {
		_ = PruneWorktrees()
	}

	if deleteBranch && branchName != "" && repoRoot != "" {
		// Now delete the branch from the main repo
		Debugf("RemoveWorktree: deleting branch %s", branchName)
		branchDeleteStart := time.Now()
		cmd := exec.Command("git", "-C", repoRoot, "branch", "-D", branchName)
		if err := cmd.Run(); err == nil {
			branchDeleted = true
			Debugf("RemoveWorktree: branch delete completed in %v", time.Since(branchDeleteStart))
		} else {
			Debugf("RemoveWorktree: branch delete failed in %v: %v", time.Since(branchDeleteStart), err)
		}
	}
	return branchDeleted, nil
}

// PruneWorktrees prunes worktree information for worktrees that no longer exist.
// It runs from the current working directory.
func PruneWorktrees() error {
	// Guard: skip pruning inside agent containers. SCION_HOST_UID is set by the
	// runtime when launching containers. Pruning inside a container can destroy
	// worktree metadata that appears stale from the container's mount layout but
	// is actively used by sibling agents on the host.
	if os.Getenv("SCION_HOST_UID") != "" {
		return nil
	}
	cmd := exec.Command("git", "worktree", "prune")
	return cmd.Run()
}

// PruneWorktreesIn prunes worktree information from the specified repository root.
func PruneWorktreesIn(repoRoot string) error {
	// Guard: skip pruning inside agent containers (see PruneWorktrees).
	if os.Getenv("SCION_HOST_UID") != "" {
		return nil
	}
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "prune")
	return cmd.Run()
}

// DeleteBranchIn deletes a git branch from the specified repository root.
// Returns true if the branch was deleted, false otherwise.
func DeleteBranchIn(repoRoot, branchName string) bool {
	cmd := exec.Command("git", "-C", repoRoot, "branch", "-D", branchName)
	return cmd.Run() == nil
}

// FindWorktreeByBranch returns the absolute path of the worktree checked out to the specified branch.
// It returns an empty string if not found.
func FindWorktreeByBranch(branchName string) (string, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	blocks := strings.Split(string(output), "\n\n")
	targetRef := "refs/heads/" + branchName

	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		var path string
		var branch string
		for _, line := range lines {
			if strings.HasPrefix(line, "worktree ") {
				path = strings.TrimPrefix(line, "worktree ")
				if strings.HasPrefix(path, "\"") {
					if unquoted, err := strconv.Unquote(path); err == nil {
						path = unquoted
					}
				}
			} else if strings.HasPrefix(line, "branch ") {
				branch = strings.TrimPrefix(line, "branch ")
			}
		}
		if branch == targetRef {
			return path, nil
		}
	}
	return "", nil
}

// BranchExists returns true if the branch exists in the repository.
func BranchExists(branchName string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	err := cmd.Run()
	return err == nil
}

// GetGitRemote returns the origin remote URL of the current repository.
// Returns empty string if not in a git repo or no origin remote exists.
func GetGitRemote() string {
	return GetGitRemoteDir("")
}

// GetGitRemoteDir returns the origin remote URL of the repository at the specified directory.
func GetGitRemoteDir(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// ExtractRepoName extracts the repository name from a git remote URL.
// Handles SSH (git@github.com:org/repo.git) and HTTPS (https://github.com/org/repo.git) formats.
func ExtractRepoName(remoteURL string) string {
	if remoteURL == "" {
		return ""
	}

	// Remove trailing .git
	remoteURL = strings.TrimSuffix(remoteURL, ".git")

	// Handle SSH format: git@github.com:org/repo
	if strings.Contains(remoteURL, ":") && strings.Contains(remoteURL, "@") {
		parts := strings.Split(remoteURL, ":")
		if len(parts) == 2 {
			pathParts := strings.Split(parts[1], "/")
			if len(pathParts) > 0 {
				return pathParts[len(pathParts)-1]
			}
		}
	}

	// Handle HTTPS format: https://github.com/org/repo
	parts := strings.Split(remoteURL, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}

	return remoteURL
}

// NormalizeGitRemote normalizes a git remote URL to a canonical form for consistent matching.
// It removes protocols (https, http, ssh, git), strips user info (credentials, tokens),
// handles SSH format (git@host:path), removes the .git suffix, and lowercases the host.
// This ensures the same repository produces the same normalized string regardless of
// access method (SSH key, HTTPS token, plain HTTPS).
// Examples:
//   - https://github.com/org/repo.git -> github.com/org/repo
//   - git@github.com:org/repo.git -> github.com/org/repo
//   - https://x-access-token:TOKEN@github.com/org/repo.git -> github.com/org/repo
//   - ssh://git@github.com/org/repo.git -> github.com/org/repo
func NormalizeGitRemote(remote string) string {
	if remote == "" {
		return ""
	}

	// Lowercase for consistent prefix/suffix matching
	remote = strings.ToLower(remote)

	// Remove protocol prefix
	remote = strings.TrimPrefix(remote, "https://")
	remote = strings.TrimPrefix(remote, "http://")
	remote = strings.TrimPrefix(remote, "ssh://")
	remote = strings.TrimPrefix(remote, "git://")

	// Handle SSH shorthand format (git@host:path → host/path)
	if strings.HasPrefix(remote, "git@") {
		remote = strings.TrimPrefix(remote, "git@")
		remote = strings.Replace(remote, ":", "/", 1)
	}

	// Strip user info (user@, user:pass@) for scheme-based URLs.
	// This handles token-authenticated HTTPS URLs like x-access-token:TOKEN@github.com/...
	if atIdx := strings.Index(remote, "@"); atIdx >= 0 {
		slashIdx := strings.Index(remote, "/")
		if slashIdx < 0 || atIdx < slashIdx {
			remote = remote[atIdx+1:]
		}
	}

	// Remove trailing slashes and .git suffix
	remote = strings.TrimRight(remote, "/")
	remote = strings.TrimSuffix(remote, ".git")

	return remote
}

// IsGitURL returns true if the string looks like a valid remote git URL.
// Accepts HTTPS, SSH shorthand (git@host:path), ssh://, and git:// schemes.
// Rejects empty strings, local paths, bare hostnames, and strings without a path containing '/'.
func IsGitURL(s string) bool {
	if s == "" {
		return false
	}

	// Reject local paths (absolute or relative)
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return false
	}

	// SSH shorthand: git@host:org/repo
	if strings.HasPrefix(s, "git@") {
		// Must have a colon separating host from path, and the path must contain '/'
		colonIdx := strings.Index(s, ":")
		if colonIdx < 0 || colonIdx == len(s)-1 {
			return false
		}
		path := s[colonIdx+1:]
		return strings.Contains(path, "/")
	}

	// Scheme-based URLs
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(strings.ToLower(s), scheme) {
			rest := s[len(scheme):]
			// Must have a host and a path with '/'
			// Strip optional user@ prefix for ssh://git@host/path
			if atIdx := strings.Index(rest, "@"); atIdx >= 0 {
				rest = rest[atIdx+1:]
			}
			// Must have host/path with at least one '/' in the path portion
			slashIdx := strings.Index(rest, "/")
			if slashIdx < 1 || slashIdx == len(rest)-1 {
				return false
			}
			return true
		}
	}

	return false
}

// ToHTTPSCloneURL converts any git URL to HTTPS clone form with a .git suffix.
// SSH shorthand and ssh:// URLs are converted; HTTPS URLs are passed through
// (with .git appended if missing). Azure DevOps URLs (dev.azure.com,
// visualstudio.com, or paths containing /_git/) are an exception: .git is
// never appended and is stripped if already present, because ADO encodes the
// repository via the /_git/<repo> path segment.
func ToHTTPSCloneURL(gitURL string) string {
	if gitURL == "" {
		return ""
	}

	result := gitURL

	// Strip known schemes
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(strings.ToLower(result), scheme) {
			result = result[len(scheme):]
			break
		}
	}

	// Handle SSH shorthand: git@host:org/repo
	if strings.HasPrefix(result, "git@") {
		result = strings.TrimPrefix(result, "git@")
		result = strings.Replace(result, ":", "/", 1)
	}

	// Strip optional user@ prefix (for ssh://git@host/path after scheme removal)
	if atIdx := strings.Index(result, "@"); atIdx >= 0 {
		slashIdx := strings.Index(result, "/")
		if slashIdx < 0 || atIdx < slashIdx {
			result = result[atIdx+1:]
		}
	}

	// Strip trailing slashes
	result = strings.TrimRight(result, "/")

	// Azure DevOps URLs use /_git/ in the path to identify the repo;
	// appending .git would break them. For ADO we strip any erroneous
	// .git suffix. For every other host we ensure the .git suffix is present.
	if isAzureDevOpsURL(result) {
		result = strings.TrimSuffix(result, ".git")
	} else if !strings.HasSuffix(result, ".git") {
		result += ".git"
	}

	return "https://" + result
}

// isAzureDevOpsURL reports whether hostAndPath (without scheme) looks like an
// Azure DevOps clone URL. It matches dev.azure.com, *.visualstudio.com hosts,
// and any path containing the /_git/ segment that ADO uses.
func isAzureDevOpsURL(hostAndPath string) bool {
	lower := strings.ToLower(hostAndPath)
	if strings.HasPrefix(lower, "dev.azure.com/") {
		return true
	}
	// <org>.visualstudio.com/...
	if dotIdx := strings.Index(lower, "."); dotIdx > 0 {
		rest := lower[dotIdx:]
		if strings.HasPrefix(rest, ".visualstudio.com/") || rest == ".visualstudio.com" {
			return true
		}
	}
	// Catch-all: any URL with the /_git/ path segment is ADO-style.
	if strings.Contains(lower, "/_git/") {
		return true
	}
	return false
}

// ExtractOrgRepo extracts the organization and repository name from a git URL.
// It uses NormalizeGitRemote to get the canonical "host/org/repo" form, then
// returns the last two path components.
func ExtractOrgRepo(gitURL string) (org, repo string) {
	normalized := NormalizeGitRemote(gitURL)
	if normalized == "" {
		return "", ""
	}

	parts := strings.Split(normalized, "/")
	if len(parts) < 3 {
		// Not enough segments (need host/org/repo)
		if len(parts) == 2 {
			return "", parts[1]
		}
		return "", ""
	}

	return parts[len(parts)-2], parts[len(parts)-1]
}

// authenticatedCloneURL embeds OAuth2 credentials in an HTTPS clone URL.
//
// It parses the URL rather than substituting on the scheme prefix: a remote
// that already carries userinfo (Azure DevOps emits
// "https://org@dev.azure.com/org/project/_git/repo") would otherwise end up
// with two "@" separators and fail to resolve. url.UserPassword also escapes
// tokens containing reserved characters.
//
// Only https remotes are rewritten. The previous implementation keyed off the
// literal "https://" prefix, so ssh:// and git:// remotes were left alone;
// url.Parse alone would happily inject an OAuth token into those, so the
// scheme is checked explicitly to preserve that behaviour.
//
// The original URL is returned unchanged when there is no token, when it
// cannot be parsed, and when it is not https.
func authenticatedCloneURL(cloneURL, token string) string {
	if token == "" {
		return cloneURL
	}
	parsed, err := url.Parse(cloneURL)
	if err != nil || parsed.Scheme != "https" {
		return cloneURL
	}
	parsed.User = url.UserPassword("oauth2", token)
	return parsed.String()
}

// CloneSharedWorkspace clones a git repository into the specified workspace path
// for use as a shared workspace project. It configures git identity and optionally
// uses a token for authentication.
//
// If the requested branch does not exist on the remote, the clone falls back to
// the remote's default branch and creates the requested branch locally.
func CloneSharedWorkspace(workspacePath, cloneURL, branch, token string) error {
	authURL := authenticatedCloneURL(cloneURL, token)

	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, authURL, workspacePath)

	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()

	if err != nil && branch != "" && isRemoteBranchNotFound(string(output)) {
		// The branch doesn't exist on the remote yet. Clone the default branch
		// instead and create the requested branch locally.
		_ = os.RemoveAll(workspacePath)

		fallbackArgs := []string{"clone", authURL, workspacePath}
		fallbackCmd := exec.Command("git", fallbackArgs...)
		fallbackCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		output, err = fallbackCmd.CombinedOutput()
		if err != nil {
			sanitized := strings.TrimSpace(sanitizeGitOutput(string(output), token))
			gitErr := ClassifyGitError(sanitized)
			if guidance := gitErr.UserGuidance(); guidance != "" {
				return &GitError{Kind: gitErr.Kind, Message: fmt.Sprintf("git clone failed: %s (%s)", sanitized, guidance)}
			}
			return &GitError{Kind: gitErr.Kind, Message: fmt.Sprintf("git clone failed: %s", sanitized)}
		}

		// Create the requested branch locally
		checkoutCmd := exec.Command("git", "-C", workspacePath, "checkout", "-b", branch)
		if checkoutOut, checkoutErr := checkoutCmd.CombinedOutput(); checkoutErr != nil {
			return fmt.Errorf("failed to create local branch %q: %s", branch, strings.TrimSpace(string(checkoutOut)))
		}
	} else if err != nil {
		sanitized := strings.TrimSpace(sanitizeGitOutput(string(output), token))
		gitErr := ClassifyGitError(sanitized)
		if guidance := gitErr.UserGuidance(); guidance != "" {
			return &GitError{Kind: gitErr.Kind, Message: fmt.Sprintf("git clone failed: %s (%s)", sanitized, guidance)}
		}
		return &GitError{Kind: gitErr.Kind, Message: fmt.Sprintf("git clone failed: %s", sanitized)}
	}

	if err := gitConfig(workspacePath, "user.name", "Scion"); err != nil {
		return fmt.Errorf("failed to configure git user.name: %w", err)
	}
	if err := gitConfig(workspacePath, "user.email", "agent@scion.dev"); err != nil {
		return fmt.Errorf("failed to configure git user.email: %w", err)
	}

	if token != "" {
		if err := gitConfig(workspacePath, "remote.origin.url", cloneURL); err != nil {
			Debugf("CloneSharedWorkspace: failed to sanitize remote URL: %v", err)
		}
	}

	return nil
}

// isRemoteBranchNotFound checks whether git clone stderr indicates that the
// requested branch does not exist on the remote (as opposed to the repo itself
// not being found or an auth error).
func isRemoteBranchNotFound(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "remote branch") && strings.Contains(lower, "not found")
}

// SyncedGitRemote describes the host-known-good remote configuration to
// regenerate into a synced workspace's .git/config. Leave RemoteURL empty for
// workspaces that have no legitimate git remote (e.g. hub-native projects);
// FilterSyncedGitMetadata then writes a config with no remote or branch
// tracking sections at all.
type SyncedGitRemote struct {
	RemoteURL string
}

// syncedGitDirAllowlist is the set of top-level .git entries that survive an
// admin-surface rebuild in FilterSyncedGitMetadata. Everything else is
// deleted.
//
// This is deliberately an allowlist rather than a denylist. git has more than
// one way to redirect config/hook lookup to a different location in the
// tree — a "commondir" file repoints the whole gitdir's config/hooks/objects/
// refs at another directory, and ".git/modules/<sub>/{hooks,config}" (reached
// via a submodule gitlink) carries its own hooks that fire on fetch even
// without an explicit --recurse-submodules. A denylist that only knows to
// strip "hooks/", "config", and "info/" leaves both standing. Keeping only
// the content/hash entries a shared-workspace or hub-native checkout
// legitimately needs, and deleting everything else, closes redirection
// vectors by construction rather than by enumeration.
var syncedGitDirAllowlist = map[string]bool{
	"HEAD":        true,
	"objects":     true,
	"refs":        true,
	"packed-refs": true,
	"index":       true,
	"logs":        true,
	"shallow":     true,
}

// syncedGitHeadInfo is the validated content of a landed .git/HEAD.
type syncedGitHeadInfo struct {
	branch   string // set when HEAD is a symref to refs/heads/<branch>
	detached bool   // true when HEAD is a bare object id
}

// FilterSyncedGitMetadata rebuilds the admin surface of a .git directory that
// was just landed by copying an untrusted workspace snapshot (a container
// filesystem sync, or a GCS blob mirrored from one) onto a host directory
// that host-side git will later run in. Call it immediately after any such
// landing, before that directory is used for any host-side git operation,
// and on every landing (a repeated sync can re-create anything a one-time
// rebuild would leave in place).
//
// Shape gate: a missing .git is a no-op — hub-native projects legitimately
// have none, and absence is not an execution vector. A .git that is not a
// real directory (a symlink, or a gitfile whose "gitdir: <path>" redirects
// elsewhere) is itself the redirection surface this function exists to
// close: it is removed outright and an error is returned, rather than
// attempting a partial strip through the redirect.
//
// A real .git directory is rebuilt in place:
//
//  1. The landed (not yet touched) config is checked for
//     extensions.objectFormat / extensions.refStorage. A non-sha1 object
//     format or reftable ref storage changes the on-disk layout in ways the
//     allowlist below and the regenerated config do not account for, so such
//     a repo is rejected (removed, error returned) rather than corrupted.
//  2. The landed (not yet touched) HEAD is validated: it must be a symref to
//     refs/heads/<valid ref name> or a bare hex object id. Anything else
//     (e.g. a HEAD of "ref: ../../elsewhere") is rejected the same way.
//  3. Every top-level .git entry not in syncedGitDirAllowlist is deleted —
//     explicitly including commondir, modules/, worktrees/, hooks/, config,
//     config.worktree, and info/. Within the kept objects/ tree,
//     objects/info/ (which holds alternates and http-alternates — both can
//     point the object store at another tenant's repository) is removed too.
//  4. .git/config is replaced wholesale with a minimal config built from the
//     caller-supplied, host-known-good remote and the branch validated in
//     (2) — never from anything pod/GCS-provided. Hub-native workspaces
//     (remote.RemoteURL == "") get identity+core only, no remote: there is no
//     upstream to synthesize.
func FilterSyncedGitMetadata(workspacePath string, remote SyncedGitRemote) error {
	gitDir := filepath.Join(workspacePath, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Hub-native projects legitimately have no .git; with none
			// present there is no admin surface to neutralize.
			return nil
		}
		return fmt.Errorf("stat %s: %w", gitDir, err)
	}
	if !info.IsDir() {
		// A gitfile (a plain file containing "gitdir: <elsewhere>") or a
		// symlink both redirect git to a directory this function never
		// inspects. Do not attempt to neutralize through the redirect;
		// remove it outright so nothing later mistakes it for a usable,
		// neutralized repository.
		_ = RemoveAllSafe(gitDir)
		return fmt.Errorf("filter synced git metadata: %s is not a real directory (gitfile/symlink); removed rather than partially neutralized", gitDir)
	}

	if err := checkSyncedGitFormatSupported(gitDir); err != nil {
		_ = RemoveAllSafe(gitDir)
		return fmt.Errorf("filter synced git metadata: %w", err)
	}

	head, err := validateSyncedGitHead(gitDir)
	if err != nil {
		_ = RemoveAllSafe(gitDir)
		return fmt.Errorf("filter synced git metadata: %w", err)
	}

	entries, err := os.ReadDir(gitDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", gitDir, err)
	}
	for _, e := range entries {
		if syncedGitDirAllowlist[e.Name()] {
			continue
		}
		if err := RemoveAllSafe(filepath.Join(gitDir, e.Name())); err != nil {
			return fmt.Errorf("remove non-allowlisted git dir entry %q: %w", e.Name(), err)
		}
	}

	// objects/info/ (alternates, http-alternates) can point the object store
	// at another tenant's repository. objects/ is otherwise content-only
	// (hashes), so this is the one thing removed from inside it.
	if err := RemoveAllSafe(filepath.Join(gitDir, "objects", "info")); err != nil {
		return fmt.Errorf("remove objects/info: %w", err)
	}

	branch := ""
	if !head.detached {
		branch = head.branch
	}
	if err := writeSyncedGitConfig(gitDir, remote, branch); err != nil {
		return fmt.Errorf("regenerate synced git config: %w", err)
	}

	return nil
}

// checkSyncedGitFormatSupported reads extensions.objectFormat and
// extensions.refStorage from the landed (untrusted) .git/config, before it
// is deleted or regenerated — this is the only point that information is
// available. Scion always clones sha1/files repositories; a landed repo
// declaring anything else is rejected rather than silently corrupted (the
// allowlist drops reftable/, and a regenerated config would drop the
// extension key git needs to read a non-default format). sha256/reftable
// shared-workspace repos are a documented non-goal.
func checkSyncedGitFormatSupported(gitDir string) error {
	configPath := filepath.Join(gitDir, "config")
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat landed config: %w", err)
	}

	objectFormat, err := readGitConfigValue(configPath, "extensions.objectFormat")
	if err != nil {
		return fmt.Errorf("read extensions.objectFormat: %w", err)
	}
	if objectFormat != "" && objectFormat != "sha1" {
		return fmt.Errorf("unsupported extensions.objectFormat %q", objectFormat)
	}

	refStorage, err := readGitConfigValue(configPath, "extensions.refStorage")
	if err != nil {
		return fmt.Errorf("read extensions.refStorage: %w", err)
	}
	if refStorage == "reftable" {
		return fmt.Errorf("unsupported extensions.refStorage %q", refStorage)
	}

	return nil
}

// readGitConfigValue reads a single key from a config file on disk using
// `git config -f`, a plumbing read of that file's text with no repository
// context and no hook/filter execution. A missing key (exit status 1) is
// reported as "", nil; any other failure (e.g. an unparseable config file)
// is propagated so callers can fail closed rather than guess.
func readGitConfigValue(configPath, key string) (string, error) {
	cmd := exec.Command("git", "config", "-f", configPath, "--get", key)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// validateSyncedGitHead reads and validates a landed .git/HEAD. It must be
// either a symref to refs/heads/<valid ref name> or a bare hex object id;
// anything else (a HEAD of "ref: ../../elsewhere", garbage content, etc.) is
// rejected.
func validateSyncedGitHead(gitDir string) (syncedGitHeadInfo, error) {
	headBytes, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return syncedGitHeadInfo{}, fmt.Errorf("read HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headBytes))

	const symrefPrefix = "ref: "
	if strings.HasPrefix(head, symrefPrefix) {
		ref := strings.TrimPrefix(head, symrefPrefix)
		const branchPrefix = "refs/heads/"
		if !strings.HasPrefix(ref, branchPrefix) || len(ref) == len(branchPrefix) {
			return syncedGitHeadInfo{}, fmt.Errorf("HEAD symref %q does not point at a branch under refs/heads/", ref)
		}
		if !isValidGitRefName(ref) {
			return syncedGitHeadInfo{}, fmt.Errorf("HEAD symref %q is not a valid ref name", ref)
		}
		return syncedGitHeadInfo{branch: strings.TrimPrefix(ref, branchPrefix)}, nil
	}

	if isHexObjectID(head) {
		return syncedGitHeadInfo{detached: true}, nil
	}

	return syncedGitHeadInfo{}, fmt.Errorf("HEAD content %q is neither a valid branch symref nor a hex object id", head)
}

// isValidGitRefName reports whether ref is a well-formed git reference name,
// deferring to git's own check-ref-format rather than reimplementing its
// rules (which reject ".." path traversal, among others).
func isValidGitRefName(ref string) bool {
	return exec.Command("git", "check-ref-format", ref).Run() == nil
}

// isHexObjectID reports whether s is a plausible sha1 object id: exactly 40
// lowercase hex characters. (sha256 repositories are rejected earlier, by
// checkSyncedGitFormatSupported, so 40 is the only length considered here.)
func isHexObjectID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// syncedGitSafeBranchName restricts the charset FilterSyncedGitMetadata will
// write into config as a branch name, on top of isValidGitRefName. git ref
// names may legally contain characters such as '"' and ']' — safe once
// written through `git config --file` (which owns escaping; see
// writeSyncedGitConfig) but not trusted any further than necessary: a branch
// name outside this charset is simply not written into the regenerated
// config — the remote is still configured, just without branch tracking for
// it, the same "no tracking information" degrade as any other branch that
// happens to have no tracking configured.
var syncedGitSafeBranchName = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// writeSyncedGitConfig atomically replaces gitDir/config with a minimal,
// host-generated config. Every value is written through `git config --file`
// — never string-templated into the file as text. This matters because
// branch is derived from the landed .git/HEAD and so is not fully trusted
// even after ref-name validation: git ref names may legally contain '"' and
// ']', and a naive `[branch "<name>"]` text template could let such a branch
// name terminate the section header early and add config keys not derived
// from host state (e.g. core.fsmonitor, a hook). `git config --file` parses
// and re-escapes the key/value itself, so this holds regardless of what
// characters branch contains.
//
// When remote.RemoteURL is empty (no legitimate remote is known for this
// workspace, e.g. a hub-native project), no remote or branch section is
// written at all, rather than trusting anything from the landed config.
func writeSyncedGitConfig(gitDir string, remote SyncedGitRemote, branch string) error {
	configPath := filepath.Join(gitDir, "config")
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, nil, 0644); err != nil {
		return err
	}

	set := func(key, value string) error {
		cmd := exec.Command("git", "config", "--file", tmpPath, key, value)
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("git config --file %s: %w (%s)", key, err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	base := [][2]string{
		{"core.repositoryformatversion", "0"},
		{"core.filemode", "true"},
		{"core.bare", "false"},
		{"core.logallrefupdates", "true"},
		{"user.name", "Scion"},
		{"user.email", "agent@scion.dev"},
	}
	for _, kv := range base {
		if err := set(kv[0], kv[1]); err != nil {
			return err
		}
	}

	if remote.RemoteURL != "" {
		if err := set("remote.origin.url", remote.RemoteURL); err != nil {
			return err
		}
		if err := set("remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return err
		}
		if branch != "" && syncedGitSafeBranchName.MatchString(branch) {
			if err := set(fmt.Sprintf("branch.%s.remote", branch), "origin"); err != nil {
				return err
			}
			if err := set(fmt.Sprintf("branch.%s.merge", branch), "refs/heads/"+branch); err != nil {
				return err
			}
		}
	}

	return os.Rename(tmpPath, configPath)
}

// PullCommitInfo describes a single commit that was pulled.
type PullCommitInfo struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// PullResult contains the structured result of a git pull operation.
type PullResult struct {
	Updated bool             `json:"updated"`
	Commits []PullCommitInfo `json:"commits,omitempty"`
}

// PullSharedWorkspace runs `git pull` in the specified workspace path to update
// it from the remote. It optionally uses a token for authentication and sanitizes
// credentials from error output. Returns structured commit information.
func PullSharedWorkspace(workspacePath, token string) (*PullResult, error) {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	// If a token is provided, configure a temporary credential helper via env
	if token != "" {
		// Use a one-shot credential helper that provides the token
		helper := fmt.Sprintf("!f() { echo username=oauth2; echo password=%s; }; f", token)
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=credential.helper",
			"GIT_CONFIG_VALUE_0="+helper,
		)
	}

	// Capture HEAD before pulling so we can enumerate new commits afterward.
	oldHead, _ := exec.Command("git", "-C", workspacePath, "rev-parse", "HEAD").Output()
	oldHeadStr := strings.TrimSpace(string(oldHead))

	cmd := exec.Command("git", "-C", workspacePath, "pull", "--ff-only")
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	sanitized := sanitizeGitOutput(strings.TrimSpace(string(output)), token)
	if err != nil {
		gitErr := ClassifyGitError(sanitized)
		if guidance := gitErr.UserGuidance(); guidance != "" {
			return nil, &GitError{Kind: gitErr.Kind, Message: fmt.Sprintf("git pull failed: %s (%s)", sanitized, guidance)}
		}
		return nil, &GitError{Kind: gitErr.Kind, Message: fmt.Sprintf("git pull failed: %s", sanitized)}
	}

	newHead, _ := exec.Command("git", "-C", workspacePath, "rev-parse", "HEAD").Output()
	newHeadStr := strings.TrimSpace(string(newHead))

	result := &PullResult{}
	if oldHeadStr == "" || newHeadStr == "" || oldHeadStr == newHeadStr {
		return result, nil
	}

	result.Updated = true

	logCmd := exec.Command("git", "-C", workspacePath, "log", "--oneline", "--no-decorate",
		oldHeadStr+".."+newHeadStr)
	logOut, err := logCmd.Output()
	if err != nil {
		return result, nil
	}

	lines := strings.Split(strings.TrimSpace(string(logOut)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		info := PullCommitInfo{Hash: parts[0]}
		if len(parts) > 1 {
			info.Subject = parts[1]
		}
		result.Commits = append(result.Commits, info)
	}

	return result, nil
}

// gitConfig sets a git config value in the specified repository.
func gitConfig(repoPath, key, value string) error {
	cmd := exec.Command("git", "-C", repoPath, "config", key, value)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", key, strings.TrimSpace(string(output)))
	}
	return nil
}

// GitErrorKind classifies git operation failures for appropriate error handling.
type GitErrorKind int

const (
	// GitErrUnknown is the default for unrecognized errors.
	GitErrUnknown GitErrorKind = iota
	// GitErrAuth indicates authentication or authorization failure (401/403).
	GitErrAuth
	// GitErrNotFound indicates the repository or branch was not found (404).
	GitErrNotFound
	// GitErrNetwork indicates a network connectivity issue.
	GitErrNetwork
	// GitErrNonFastForward indicates a pull failed because local commits diverge.
	GitErrNonFastForward
)

// GitError wraps a git operation error with a classified kind and sanitized message.
type GitError struct {
	Kind    GitErrorKind
	Message string
}

func (e *GitError) Error() string {
	return e.Message
}

// ClassifyGitError inspects sanitized git stderr and returns a classified GitError.
func ClassifyGitError(sanitizedStderr string) *GitError {
	lower := strings.ToLower(sanitizedStderr)

	kind := GitErrUnknown
	switch {
	case strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "could not read username") ||
		strings.Contains(lower, "invalid credentials") ||
		strings.Contains(lower, "403") ||
		strings.Contains(lower, "401"):
		kind = GitErrAuth
	case strings.Contains(lower, "repository not found") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "404"):
		kind = GitErrNotFound
	case strings.Contains(lower, "could not resolve host") ||
		strings.Contains(lower, "unable to access") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "timed out"):
		kind = GitErrNetwork
	case strings.Contains(lower, "not possible to fast-forward") ||
		strings.Contains(lower, "non-fast-forward"):
		kind = GitErrNonFastForward
	}

	return &GitError{Kind: kind, Message: sanitizedStderr}
}

// UserGuidance returns a user-facing hint for the error kind.
func (e *GitError) UserGuidance() string {
	switch e.Kind {
	case GitErrAuth:
		return "Check that GITHUB_TOKEN (or GitHub App credentials) are valid and have repository read access."
	case GitErrNotFound:
		return "Verify the repository URL exists and that the token has access to it (private repos require explicit access)."
	case GitErrNetwork:
		return "A network error occurred. Check connectivity and try again."
	case GitErrNonFastForward:
		return "The workspace has local commits that diverge from the remote. Merge or reset manually before pulling."
	default:
		return ""
	}
}

// sanitizeGitOutput removes a token from git output to prevent credential leaks.
func sanitizeGitOutput(output, token string) string {
	if token == "" {
		return output
	}
	return strings.ReplaceAll(output, token, "***")
}

// scionNamespace is a fixed UUID v5 namespace used by HashProjectID for
// deterministic identifier derivation (e.g. cache keys).
var scionNamespace = uuid.MustParse("a1b8e4f0-7c3d-4a1e-9f2b-6d5c8e7a0b1f")

// HashProjectID computes a deterministic identifier from a normalized input
// string.  It uses UUID v5 (SHA-1 based) with a fixed Scion namespace to
// produce a valid UUID that is stable for a given input.
//
// NOTE: Project IDs are random UUIDs (see config.GenerateProjectID).  This
// function is NOT used for project-ID generation — it is retained for other
// deterministic identifier needs such as cache keys.
func HashProjectID(normalized string) string {
	return uuid.NewSHA1(scionNamespace, []byte(normalized)).String()
}

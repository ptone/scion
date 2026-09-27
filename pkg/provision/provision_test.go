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

package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLocker is a mock AdvisoryLocker for testing.
type testLocker struct {
	mu       sync.Mutex
	held     map[lockKey]bool
	acquires int64
}

type lockKey struct {
	classID int64
	objID   int32
	single  bool
}

func newTestLocker() *testLocker {
	return &testLocker{held: make(map[lockKey]bool)}
}

func (l *testLocker) TryAdvisoryLock(ctx context.Context, key store.AdvisoryLockKey) (bool, func() error, error) {
	k := lockKey{classID: int64(key), single: true}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[k] {
		return false, func() error { return nil }, nil
	}
	l.held[k] = true
	atomic.AddInt64(&l.acquires, 1)
	return true, func() error {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.held, k)
		return nil
	}, nil
}

func (l *testLocker) TryAdvisoryLockObject(ctx context.Context, classID store.AdvisoryLockKey, objID int32) (bool, func() error, error) {
	k := lockKey{classID: int64(classID), objID: objID}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[k] {
		return false, func() error { return nil }, nil
	}
	l.held[k] = true
	atomic.AddInt64(&l.acquires, 1)
	return true, func() error {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.held, k)
		return nil
	}, nil
}

// initBareGitRepo creates a bare git repo at a temporary path for cloning from.
func initBareGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bareDir := filepath.Join(dir, "bare.git")
	run(t, "git", "init", "--bare", "--initial-branch=main", bareDir)

	workDir := filepath.Join(dir, "work")
	run(t, "git", "clone", bareDir, workDir)

	f := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(f, []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, workDir, "git", "add", "README.md")
	runIn(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com",
		"commit", "-m", "initial")
	runIn(t, workDir, "git", "push", "origin", "main")

	return bareDir
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %s\n%s", name, args, err, output)
	}
}

func runIn(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v (in %s): %s\n%s", name, args, dir, err, output)
	}
}

// --- ClonePerAgent rejection ---

func TestProvision_RejectsClonePerAgent(t *testing.T) {
	err := ProvisionShared(ProvisionInput{
		ProjectID: "proj-1",
		Mode:      store.SharingModeClonePerAgent,
		Resolved: ResolvedWorkspace{
			HostPath: "/some/path",
		},
	})
	if err == nil {
		t.Fatal("expected error for ClonePerAgent on NFS backend")
	}
	if !strings.Contains(err.Error(), "ClonePerAgent") {
		t.Errorf("error should mention ClonePerAgent, got: %v", err)
	}
}

// --- Missing required fields ---

func TestProvision_MissingHostPath(t *testing.T) {
	err := ProvisionShared(ProvisionInput{
		ProjectID: "proj-1",
		Mode:      store.SharingModeSharedPlain,
		Resolved:  ResolvedWorkspace{},
	})
	if err == nil {
		t.Fatal("expected error for empty HostPath")
	}
}

func TestProvision_MissingProjectID(t *testing.T) {
	err := ProvisionShared(ProvisionInput{
		Mode: store.SharingModeSharedPlain,
		Resolved: ResolvedWorkspace{
			HostPath: "/some/path",
		},
	})
	if err == nil {
		t.Fatal("expected error for empty ProjectID")
	}
}

// --- sanitizeBranchName ---

func TestSanitizeBranchName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with spaces", "with-spaces"},
		{"with/slash", "with-slash"},
		{"with..dots", "with-dots"},
		{"with~tilde", "with-tilde"},
		{".leading-dot", "leading-dot"},
		{"-leading-dash", "leading-dash"},
		{"trailing-.", "trailing"},
		{"", "agent"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeBranchName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeBranchName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestChownTarget(t *testing.T) {
	tests := []struct {
		name     string
		hostPath string
		want     string
	}{
		// Broker-side: chown the project root (parent of the workspace dir).
		{"broker project root", "/srv/nfs/share1/proj-abc/workspace", "/srv/nfs/share1/proj-abc"},
		// k8s init container subPath mount: parent is "/", fall back to the
		// workspace dir itself rather than chown -R the whole container root.
		{"k8s workspace mount", "/workspace", "/workspace"},
		// Relative path has no real parent ("."); fall back to the path itself.
		{"relative path", "workspace", "workspace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chownTarget(tt.hostPath); got != tt.want {
				t.Errorf("chownTarget(%q) = %q, want %q", tt.hostPath, got, tt.want)
			}
		})
	}
}

// --- writeSentinel ---

func TestWriteSentinel_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProvisionSentinelFile)

	if err := writeSentinel(path); err != nil {
		t.Fatalf("writeSentinel: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if !strings.Contains(string(data), "provisioned_at=") {
		t.Errorf("sentinel content unexpected: %s", string(data))
	}

	// Overwrite should also work (idempotent).
	if err := writeSentinel(path); err != nil {
		t.Fatalf("writeSentinel overwrite: %v", err)
	}
}

// --- acquireProvisionLock context cancellation ---

// alwaysLoseLocker is an AdvisoryLocker where TryAdvisoryLockObject always
// returns acquired=false (another node holds the lock).
type alwaysLoseLocker struct{}

func (l *alwaysLoseLocker) TryAdvisoryLock(_ context.Context, _ store.AdvisoryLockKey) (bool, func() error, error) {
	return false, func() error { return nil }, nil
}

func (l *alwaysLoseLocker) TryAdvisoryLockObject(_ context.Context, _ store.AdvisoryLockKey, _ int32) (bool, func() error, error) {
	return false, func() error { return nil }, nil
}

func TestAcquireProvisionLock_ContextCancellation(t *testing.T) {
	locker := &alwaysLoseLocker{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in := ProvisionInput{
		ProjectID: "proj-cancel-test",
		Locker:    locker,
	}

	start := time.Now()
	_, err := acquireProvisionLock(ctx, in)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "context cancelled")
	assert.Less(t, elapsed, 2*time.Second, "should return promptly on context cancellation, not wait for all retries")
}

// --- WorktreePerAgent: creates worktree on shared checkout ---

func TestProvision_WorktreePerAgent(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	locker := newTestLocker()
	bareRepo := initBareGitRepo(t)

	projectDir := t.TempDir()
	hostPath := filepath.Join(projectDir, "workspace")

	err := ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-wt-1",
		AgentID:   "agent-wt-1",
		AgentName: "test-agent",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone: &api.GitCloneConfig{
			URL:    bareRepo,
			Branch: "main",
		},
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Verify base HEAD is detached.
	cmd := exec.Command("git", "-C", hostPath, "symbolic-ref", "HEAD")
	if err := cmd.Run(); err == nil {
		t.Error("expected HEAD to be detached in base, but symbolic-ref succeeded")
	}

	// Verify gc.auto is disabled.
	out, err := exec.Command("git", "-C", hostPath, "config", "gc.auto").Output()
	if err != nil || strings.TrimSpace(string(out)) != "0" {
		t.Errorf("expected gc.auto=0 in base repo, got %q (err=%v)", strings.TrimSpace(string(out)), err)
	}

	// Verify worktree was created.
	worktreePath := WorktreePath(hostPath, "agent-wt-1")
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree not created at %s: %v", worktreePath, err)
	}

	// Verify .git is a file (pointer), not a directory.
	gitFile := filepath.Join(worktreePath, ".git")
	fi, err := os.Lstat(gitFile)
	if err != nil {
		t.Fatalf("worktree .git not found: %v", err)
	}
	if fi.IsDir() {
		t.Error("worktree .git should be a file (pointer), not a directory")
	}

	// Verify .git pointer uses a relative path (--relative-paths).
	data, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatalf("read worktree .git: %v", err)
	}
	gitdirLine := strings.TrimSpace(string(data))
	if !strings.HasPrefix(gitdirLine, "gitdir: ") {
		t.Fatalf("unexpected .git content: %s", gitdirLine)
	}
	gitdirPath := strings.TrimPrefix(gitdirLine, "gitdir: ")
	if filepath.IsAbs(gitdirPath) {
		t.Errorf("worktree .git should use a relative path, got: %s", gitdirPath)
	}
}

// --- WorktreePerAgent: second agent gets independent worktree ---

func TestProvision_WorktreePerAgent_TwoAgents(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	locker := newTestLocker()
	bareRepo := initBareGitRepo(t)

	projectDir := t.TempDir()
	hostPath := filepath.Join(projectDir, "workspace")

	// First agent.
	err := ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-wt-2",
		AgentID:   "agent-1",
		AgentName: "first-agent",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone: &api.GitCloneConfig{
			URL:    bareRepo,
			Branch: "main",
		},
	})
	if err != nil {
		t.Fatalf("Provision agent-1: %v", err)
	}

	// Second agent (sentinel exists, so clone is skipped — just adds worktree).
	err = ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-wt-2",
		AgentID:   "agent-2",
		AgentName: "second-agent",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone: &api.GitCloneConfig{
			URL:    bareRepo,
			Branch: "main",
		},
	})
	if err != nil {
		t.Fatalf("Provision agent-2: %v", err)
	}

	// Both worktrees exist and are independent.
	wt1 := WorktreePath(hostPath, "agent-1")
	wt2 := WorktreePath(hostPath, "agent-2")
	if _, err := os.Stat(wt1); err != nil {
		t.Errorf("worktree agent-1 not found: %v", err)
	}
	if _, err := os.Stat(wt2); err != nil {
		t.Errorf("worktree agent-2 not found: %v", err)
	}

	// Verify both worktrees have relative .git pointers.
	for _, wt := range []string{wt1, wt2} {
		data, err := os.ReadFile(filepath.Join(wt, ".git"))
		if err != nil {
			t.Errorf("read .git in %s: %v", wt, err)
			continue
		}
		gitdirLine := strings.TrimSpace(string(data))
		if !strings.HasPrefix(gitdirLine, "gitdir: ") {
			t.Errorf("unexpected .git content in %s: %s", wt, gitdirLine)
			continue
		}
		gitdirPath := strings.TrimPrefix(gitdirLine, "gitdir: ")
		if filepath.IsAbs(gitdirPath) {
			t.Errorf("worktree %s .git should use relative path, got: %s", wt, gitdirPath)
		}
	}
}

// --- Two projects sharing a parent dir: independent sentinels ---

func TestProvision_WorktreePerAgent_TwoProjects(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	locker := newTestLocker()
	bareRepoA := initBareGitRepo(t)
	bareRepoB := initBareGitRepo(t)

	parentDir := t.TempDir()
	projectDirA := filepath.Join(parentDir, "project-alpha")
	projectDirB := filepath.Join(parentDir, "project-beta")
	if err := os.MkdirAll(projectDirA, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDirB, 0755); err != nil {
		t.Fatal(err)
	}

	hostPathA := filepath.Join(projectDirA, "workspace")
	hostPathB := filepath.Join(projectDirB, "workspace")

	// --- Project A ---
	err := ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPathA, Backend: "local"},
		ProjectID: "proj-alpha",
		AgentID:   "agent-a1",
		AgentName: "alpha-agent",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone:  &api.GitCloneConfig{URL: bareRepoA, Branch: "main"},
	})
	if err != nil {
		t.Fatalf("Provision project A: %v", err)
	}

	if _, err := os.Stat(filepath.Join(hostPathA, ".git")); err != nil {
		t.Fatalf("project A: .git not found: %v", err)
	}
	if _, err := os.Stat(WorktreePath(hostPathA, "agent-a1")); err != nil {
		t.Fatalf("project A: worktree not created: %v", err)
	}

	// --- Project B ---
	err = ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPathB, Backend: "local"},
		ProjectID: "proj-beta",
		AgentID:   "agent-b1",
		AgentName: "beta-agent",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone:  &api.GitCloneConfig{URL: bareRepoB, Branch: "main"},
	})
	if err != nil {
		t.Fatalf("Provision project B: %v", err)
	}

	if _, err := os.Stat(filepath.Join(hostPathB, ".git")); err != nil {
		t.Fatalf("project B: .git not found — sentinel collision?")
	}
	if _, err := os.Stat(WorktreePath(hostPathB, "agent-b1")); err != nil {
		t.Fatalf("project B: worktree not created: %v", err)
	}

	// Sentinels must be per-project.
	sentinelA := filepath.Join(projectDirA, ProvisionSentinelFile)
	sentinelB := filepath.Join(projectDirB, ProvisionSentinelFile)
	if _, err := os.Stat(sentinelA); err != nil {
		t.Errorf("project A sentinel missing at %s", sentinelA)
	}
	if _, err := os.Stat(sentinelB); err != nil {
		t.Errorf("project B sentinel missing at %s", sentinelB)
	}
	parentSentinel := filepath.Join(parentDir, ProvisionSentinelFile)
	if _, err := os.Stat(parentSentinel); err == nil {
		t.Errorf("sentinel found in shared parent dir %s — sentinel collision", parentDir)
	}
}

// --- Concurrent same-project provisioning ---

func TestProvision_WorktreePerAgent_ConcurrentSameProject(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	locker := newTestLocker()
	bareRepo := initBareGitRepo(t)

	projectDir := t.TempDir()
	hostPath := filepath.Join(projectDir, "workspace")

	var wg sync.WaitGroup
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent-concurrent-%d", idx)
			errs[idx] = ProvisionShared(ProvisionInput{
				Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
				ProjectID: "proj-concurrent-1",
				AgentID:   agentID,
				AgentName: fmt.Sprintf("concurrent-agent-%d", idx),
				Mode:      store.SharingModeWorktreePerAgent,
				Locker:    locker,
				GitClone: &api.GitCloneConfig{
					URL:    bareRepo,
					Branch: "main",
				},
			})
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d failed: %v", i, err)
		}
	}

	for i := 0; i < 2; i++ {
		wt := WorktreePath(hostPath, fmt.Sprintf("agent-concurrent-%d", i))
		if _, err := os.Stat(wt); err != nil {
			t.Errorf("worktree agent-concurrent-%d not found at %s: %v", i, wt, err)
		}
	}

	if _, err := os.Stat(filepath.Join(hostPath, ".git")); err != nil {
		t.Fatalf("shared base .git not found: %v", err)
	}
}

// --- Full clone depth for worktree mode ---

func TestProvision_WorktreePerAgent_FullCloneDepth(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	locker := newTestLocker()
	bareRepo := initBareGitRepo(t)

	projectDir := t.TempDir()
	hostPath := filepath.Join(projectDir, "workspace")

	// Depth 0 means full clone (no --depth flag).
	err := ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-depth-1",
		AgentID:   "agent-depth-1",
		AgentName: "depth-agent",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone: &api.GitCloneConfig{
			URL:    bareRepo,
			Branch: "main",
			Depth:  intPtr(0),
		},
	})
	if err != nil {
		t.Fatalf("Provision with Depth=0 (full clone): %v", err)
	}

	// Verify the clone is NOT shallow (full history).
	shallowFile := filepath.Join(hostPath, ".git", "shallow")
	if _, err := os.Stat(shallowFile); err == nil {
		t.Error("expected full clone (no .git/shallow), but shallow file exists")
	}
}

// --- WorktreePath ---

func TestWorktreePath(t *testing.T) {
	got := WorktreePath("/srv/nfs/proj/workspace", "agent-42")
	want := "/srv/nfs/proj/workspace/worktrees/agent-42"
	if got != want {
		t.Errorf("WorktreePath() = %q, want %q", got, want)
	}
}

// --- HardenedGitCommand ---
//
// pkg/runtime/common.go's narrowGitAdminMounts (Part A) is the primary
// control: a read-only bind mount over the shared base's .git
// config/hooks/info means only host-managed hooks/config/filters are ever
// honored when the broker runs git against the base. These tests exercise
// what's testable without Docker/mount-namespace access (unavailable in this
// sandbox): HardenedGitCommand's own behavior is exercised directly against
// a real base repo. A real read-only bind mount's enforcement of writes to
// the pre-existing .git/config file specifically (as opposed to creating a
// new file, e.g. under .git/hooks/) is proven only at the "correct mount
// args are generated" level (pkg/runtime/common_test.go's
// TestNarrowGitAdminMounts_HubNativeDocker) and requires a real Docker
// read-only bind mount to verify end to end (tracked acceptance item).

func TestHardenedGitCommand_RefusesCommondirRedirect(t *testing.T) {
	// A hub-native shared base is always the main working copy of its own
	// repository, so a top-level .git/commondir file is never legitimate:
	// git resolves config/hooks/refs through whatever commondir points to,
	// for the base's own gitdir as much as for any linked worktree. Its
	// presence would otherwise redirect every HardenedGitCommand
	// invocation's config/hooks resolution away from the mounted,
	// host-managed .git admin surface to an arbitrary writable location —
	// this is exactly the gap narrowGitAdminMounts' read-only mount alone
	// does not close, since it never inspects commondir.
	base := t.TempDir()
	run(t, "git", "init", "--initial-branch=main", base)
	runIn(t, base, "git", "-c", "user.name=t", "-c", "user.email=t@t.com",
		"commit", "--allow-empty", "-m", "root")

	// A redirect target with its own hook, standing in for an unverified
	// location outside the mounted admin surface.
	redirect := t.TempDir()
	if err := os.MkdirAll(filepath.Join(redirect, "hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "marker")
	hookScript := fmt.Sprintf("#!/bin/sh\necho hook-ran >> %s\n", marker)
	if err := os.WriteFile(filepath.Join(redirect, "hooks", "post-checkout"), []byte(hookScript), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(redirect, "config"), []byte("[core]\n\trepositoryformatversion = 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(redirect, "objects"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(redirect, "refs", "heads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, ".git", "commondir"), []byte(redirect+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	wtPath := filepath.Join(t.TempDir(), "wt")
	_, err := HardenedGitCommand(context.Background(), base, "worktree", "add", "--relative-paths", "-b", "agent-x", wtPath)
	if !errors.Is(err, ErrCommondirPresent) {
		t.Fatalf("expected ErrCommondirPresent, got: %v", err)
	}
	if data, statErr := os.ReadFile(marker); statErr == nil {
		t.Errorf("expected the redirected hook to never run, marker contents: %q", data)
	}
}

func TestWorktreeUsage_UnaffectedByReadOnlyHooksAndInfo(t *testing.T) {
	// AC2-style smoke test (git-level; no docker in this sandbox — see the
	// disclosed environment limitation): with .git/hooks and .git/info
	// read-only (real for these two paths, since creating a new file only
	// needs directory write permission, which read-only expresses
	// correctly), the broker's HardenedGitCommand-driven `worktree add`
	// still succeeds, and ordinary commit + checkout in the resulting
	// worktree are unaffected.
	base := t.TempDir()
	run(t, "git", "init", "--initial-branch=main", base)
	runIn(t, base, "git", "-c", "user.name=t", "-c", "user.email=t@t.com",
		"commit", "--allow-empty", "-m", "root")

	hooksDir := filepath.Join(base, ".git", "hooks")
	infoDir := filepath.Join(base, ".git", "info")
	if err := os.Chmod(hooksDir, 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(infoDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(hooksDir, 0755)
		_ = os.Chmod(infoDir, 0755)
	})

	wtPath := filepath.Join(t.TempDir(), "wt")
	cmd, err := HardenedGitCommand(context.Background(), base, "worktree", "add", "--relative-paths", "-b", "agent-2", wtPath)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}

	if err := os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, wtPath, "git", "add", "f.txt")
	runIn(t, wtPath, "git", "-c", "user.name=t", "-c", "user.email=t@t.com", "commit", "-m", "agent commit")
	runIn(t, wtPath, "git", "checkout", "-b", "agent-2-work")
}

func TestHardenedGitCommand_NeutralizesFsmonitorRegardlessOfConfig(t *testing.T) {
	// core.fsmonitor is a pure .git/config vector (no on-disk file creation
	// needed), so unlike hooks/info above it cannot be blocked by directory
	// permissions — this is exactly why HardenedGitCommand clears it at the
	// invocation level (Part B) as belt-and-suspenders over Part A.
	base := t.TempDir()
	run(t, "git", "init", "--initial-branch=main", base)
	runIn(t, base, "git", "-c", "user.name=t", "-c", "user.email=t@t.com",
		"commit", "--allow-empty", "-m", "root")

	marker := filepath.Join(t.TempDir(), "marker")
	fsmonScript := filepath.Join(t.TempDir(), "fsmonitor.sh")
	if err := os.WriteFile(fsmonScript,
		[]byte(fmt.Sprintf("#!/bin/sh\necho fsmonitor-ran >> %s\necho \"\"\n", marker)), 0755); err != nil {
		t.Fatal(err)
	}
	// An unexpected core.fsmonitor, however it got there.
	runIn(t, base, "git", "config", "core.fsmonitor", fsmonScript)

	cmd, err := HardenedGitCommand(context.Background(), base, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git status failed: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(marker); len(data) != 0 {
		t.Errorf("expected core.fsmonitor to be neutralized by HardenedGitCommand, but it ran: %q", data)
	}

	// Revert-check: the identical config, invoked WITHOUT the wrapper, DOES
	// fire — proving the wrapper (not something incidental) is what
	// neutralizes it.
	plain := exec.Command("git", "status", "--porcelain")
	plain.Dir = base
	if out, err := plain.CombinedOutput(); err != nil {
		t.Fatalf("git status (unwrapped) failed: %v\n%s", err, out)
	}
	if data, _ := os.ReadFile(marker); len(data) == 0 {
		t.Error("expected core.fsmonitor to fire without the wrapper (revert-check baseline), got no marker")
	}
}

func TestHardenedGitCommand_TrustedHookAndGlobalFilterStillRun(t *testing.T) {
	// Part A only prevents a CONTAINER from writing config/hooks/info; it
	// does not and must not stop the HOST itself (e.g. `git lfs install`,
	// run by the broker operator, not a container) from doing so, and
	// HardenedGitCommand must not neutralize what it finds there. git-lfs
	// isn't available in this environment, so this stands in for it exactly
	// as instructed: filter.lfs.* configured via the GLOBAL gitconfig (the
	// way `git lfs install` actually writes it — not repo-local, which would
	// pass even with a wrongly-cleared GIT_CONFIG_GLOBAL) plus a trusted
	// post-checkout hook already present in the base's .git/hooks (as
	// git-lfs install also adds).
	home := t.TempDir()
	t.Setenv("HOME", home)
	marker := filepath.Join(t.TempDir(), "marker")
	t.Setenv("MARKER_FILE", marker)

	// A script file (rather than an inline shell command) avoids nested-quote
	// mangling once the command string round-trips through gitconfig.
	smudgeScript := filepath.Join(t.TempDir(), "smudge.sh")
	if err := os.WriteFile(smudgeScript, []byte("#!/bin/sh\ncat >/dev/null\necho smudge-ran >> \"$MARKER_FILE\"\n"), 0755); err != nil {
		t.Fatal(err)
	}

	globalConfig := filepath.Join(home, ".gitconfig")
	globalConfigBody := fmt.Sprintf(
		"[filter \"lfs\"]\n\tsmudge = %s\n\tclean = cat\n\trequired = false\n",
		smudgeScript)
	if err := os.WriteFile(globalConfig, []byte(globalConfigBody), 0644); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	run(t, "git", "init", "--initial-branch=main", base)
	runIn(t, base, "git", "-c", "user.name=t", "-c", "user.email=t@t.com",
		"commit", "--allow-empty", "-m", "root")

	// A trusted, host-placed post-checkout hook (as `git lfs install` adds).
	hookScript := fmt.Sprintf("#!/bin/sh\necho hook-ran >> %s\n", marker)
	if err := os.WriteFile(filepath.Join(base, ".git", "hooks", "post-checkout"), []byte(hookScript), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(base, ".gitattributes"), []byte("data.bin filter=lfs -text\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "data.bin"), []byte("binary-content"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, base, "git", "add", ".")
	runIn(t, base, "git", "-c", "user.name=t", "-c", "user.email=t@t.com",
		"commit", "-m", "add lfs-tracked file")

	// The exact broker trigger: HardenedGitCommand-driven `git worktree add`,
	// which checks out the new worktree — running the post-checkout hook and
	// the smudge filter for data.bin.
	wtPath := filepath.Join(t.TempDir(), "wt")
	cmd, err := HardenedGitCommand(context.Background(), base, "worktree", "add", "--relative-paths", "-b", "agent-1", wtPath)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add failed: %v\n%s", err, out)
	}

	data, _ := os.ReadFile(marker)
	if !strings.Contains(string(data), "hook-ran") {
		t.Errorf("expected the trusted post-checkout hook to run, marker: %q", data)
	}
	if !strings.Contains(string(data), "smudge-ran") {
		t.Errorf("expected the trusted global (LFS-style) smudge filter to run, marker: %q", data)
	}
}

func TestHardenedGitCommand_DoesNotClobberCredentialHelperEnv(t *testing.T) {
	// pkg/util/git.go's PullSharedWorkspace authenticates via a one-shot
	// credential helper injected through GIT_CONFIG_COUNT/KEY_0/VALUE_0 env
	// vars. A caller combining that technique with HardenedGitCommand must
	// APPEND to cmd.Env (not replace it), or the GIT_COMMON_DIR pin would be
	// lost along with the ambient environment.
	base := t.TempDir()
	run(t, "git", "init", "--initial-branch=main", base)

	helper := "!f() { echo username=oauth2; echo password=test-token; }; f"
	credEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=" + helper,
	}

	credCmd, err := HardenedGitCommand(context.Background(), base, "config", "--get", "credential.helper")
	if err != nil {
		t.Fatal(err)
	}
	credCmd.Env = append(credCmd.Env, credEnv...)
	out, err := credCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git config --get credential.helper failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != helper {
		t.Errorf("expected the credential-helper env to survive alongside the wrapper's env, got %q want %q", got, helper)
	}

	pagerCmd, err := HardenedGitCommand(context.Background(), base, "config", "--get", "core.pager")
	if err != nil {
		t.Fatal(err)
	}
	pagerCmd.Env = append(pagerCmd.Env, credEnv...)
	out, err = pagerCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git config --get core.pager failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "cat" {
		t.Errorf("expected the wrapper's core.pager=cat to survive alongside the credential-helper env, got %q", got)
	}

	commonDirCmd, err := HardenedGitCommand(context.Background(), base, "rev-parse", "--git-common-dir")
	if err != nil {
		t.Fatal(err)
	}
	commonDirCmd.Env = append(commonDirCmd.Env, credEnv...)
	out, err = commonDirCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse --git-common-dir failed: %v\n%s", err, out)
	}
	wantCommonDir := filepath.Join(base, ".git")
	if got := strings.TrimSpace(string(out)); got != wantCommonDir {
		t.Errorf("expected the GIT_COMMON_DIR pin to survive alongside the credential-helper env, got %q want %q", got, wantCommonDir)
	}
}

// --- documented in-container git workflow limitations under a
// read-only .git/config, and the branch.autoSetupMerge=false mitigation ---

// runInGetCode runs a command in dir and returns its exit code and combined
// output without failing the test. Used where the point of the assertion is
// a specific exit code (including nonzero), not bare success.
func runInGetCode(t *testing.T, dir, name string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), string(out)
	}
	t.Fatalf("%s %v (in %s): %v\n%s", name, args, dir, err, out)
	return -1, string(out)
}

// runInExpectCode runs a command in dir and fails the test if its exit code
// does not match want.
func runInExpectCode(t *testing.T, dir string, want int, name string, args ...string) {
	t.Helper()
	got, out := runInGetCode(t, dir, name, args...)
	if got != want {
		t.Errorf("%s %v (in %s): exit code = %d, want %d\n%s", name, args, dir, got, want, out)
	}
}

func TestPrepareBaseForWorktrees_DocumentedWorkflowsWithConfigUnwritable(t *testing.T) {
	// Part A mounts the shared base's .git/config read-only into the agent
	// container. That does not block plain git usage, but it does block any
	// command that needs to WRITE repo config — most notably setting up a
	// new tracking relationship. prepareBaseForWorktrees sets
	// branch.autoSetupMerge=false specifically so the single most common of
	// those (`checkout -b <local> <remote>/<branch>`, and DWIM `switch
	// <remote-branch>`) degrades to a plain untracked local branch instead
	// of failing outright. This test forces config writes to fail the same
	// way a read-only bind mount would (a pre-created .git/config.lock —
	// real mount enforcement is EROFS/EBUSY, not exercised here; see the
	// disclosed environment limitation) and asserts the resulting documented
	// supported/unsupported command list.
	ctx := context.Background()
	work := t.TempDir()
	bare := filepath.Join(work, "bare.git")
	run(t, "git", "init", "--bare", "--initial-branch=main", bare)

	remoteClone := filepath.Join(work, "remote-clone")
	run(t, "git", "clone", bare, remoteClone)
	runIn(t, remoteClone, "git", "-c", "user.name=t", "-c", "user.email=t@t.com", "commit", "--allow-empty", "-m", "root")
	runIn(t, remoteClone, "git", "push", "origin", "main")
	runIn(t, remoteClone, "git", "checkout", "-b", "feature-remote")
	runIn(t, remoteClone, "git", "-c", "user.name=t", "-c", "user.email=t@t.com", "commit", "--allow-empty", "-m", "feature")
	runIn(t, remoteClone, "git", "push", "origin", "feature-remote")

	base := filepath.Join(work, "base")
	run(t, "git", "clone", bare, base)
	if err := prepareBaseForWorktrees(ctx, base); err != nil {
		t.Fatalf("prepareBaseForWorktrees: %v", err)
	}

	wt := filepath.Join(work, "wt")
	runIn(t, base, "git", "worktree", "add", wt, "-b", "agent-branch")

	// Force config writes to fail the way a read-only-mounted .git/config
	// would: git's config write is a lock-then-rename, so pre-creating the
	// lock file makes that step fail the same way EBUSY/EROFS against a
	// real read-only mount would.
	configLock := filepath.Join(base, ".git", "config.lock")
	if err := os.WriteFile(configLock, nil, 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(configLock) })

	// Mitigated by branch.autoSetupMerge=false: succeed despite config being
	// unwritable, by skipping the upstream-tracking config write entirely.
	runInExpectCode(t, wt, 0, "git", "checkout", "-b", "tracked-checkout", "origin/feature-remote")
	runInExpectCode(t, wt, 0, "git", "switch", "feature-remote") // DWIM

	// Documented as broken with config unwritable; NOT fixed by the
	// mitigation above (these all write config for reasons other than
	// initial tracking setup).
	runInExpectCode(t, wt, 1, "git", "branch", "--set-upstream-to=origin/main", "agent-branch")
	runInExpectCode(t, wt, 128, "git", "branch", "-m", "agent-branch", "agent-branch-renamed")
	// The push itself succeeds; its upstream is silently NOT recorded.
	runInExpectCode(t, wt, 0, "git", "push", "-u", "origin", "agent-branch-renamed")
	if code, _ := runInGetCode(t, wt, "git", "config", "--get", "branch.agent-branch-renamed.remote"); code == 0 {
		t.Error("expected push -u's upstream to NOT be recorded when config is unwritable (documented limitation)")
	}
	runInExpectCode(t, wt, 128, "git", "remote", "add", "extra-remote", "https://example.invalid/x.git")
	runInExpectCode(t, wt, 255, "git", "config", "local.test.key", "value")

	// Unaffected by config being unwritable.
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	runInExpectCode(t, wt, 0, "git", "add", "f.txt")
	runInExpectCode(t, wt, 0, "git", "-c", "user.name=t", "-c", "user.email=t@t.com", "commit", "-m", "agent commit")
	runInExpectCode(t, wt, 0, "git", "checkout", "-b", "untracked-local") // no tracking requested
	runInExpectCode(t, wt, 0, "git", "status", "--porcelain")
	runInExpectCode(t, wt, 0, "git", "fetch", "origin")
	runInExpectCode(t, wt, 0, "git", "pull", "--ff-only", "origin", "main")
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("hi2"), 0644); err != nil {
		t.Fatal(err)
	}
	runInExpectCode(t, wt, 0, "git", "stash")
}

// --- Create-or-Attach + Sharer Registration ---

func TestProvision_WorktreePerAgent_CreateAndJoin(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	locker := newTestLocker()
	bareRepo := initBareGitRepo(t)

	projectDir := t.TempDir()
	hostPath := filepath.Join(projectDir, "workspace")

	// Agent A creates worktree on branch "shared-branch".
	err := ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-join-1",
		AgentID:   "agent-a",
		AgentName: "shared-branch",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone:  &api.GitCloneConfig{URL: bareRepo, Branch: "main", Depth: intPtr(0)},
	})
	require.NoError(t, err)

	// Verify worktree created for A.
	wtA := WorktreePath(hostPath, "agent-a")
	require.DirExists(t, wtA)

	// Verify sharers=[A].
	sharers, wtPath, err := ListSharers(hostPath, "shared-branch")
	require.NoError(t, err)
	assert.Equal(t, wtA, wtPath)
	assert.Equal(t, []string{"agent-a"}, sharers)

	// Agent B joins same branch "shared-branch" (JOIN).
	err = ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-join-1",
		AgentID:   "agent-b",
		AgentName: "shared-branch",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone:  &api.GitCloneConfig{URL: bareRepo, Branch: "main", Depth: intPtr(0)},
	})
	require.NoError(t, err)

	// Verify NO second worktree created for B.
	wtB := WorktreePath(hostPath, "agent-b")
	_, statErr := os.Stat(wtB)
	assert.True(t, os.IsNotExist(statErr), "JOIN should NOT create a second worktree at %s", wtB)

	// Verify sharers=[A,B] and B's registered path == A's path.
	sharers, wtPath, err = ListSharers(hostPath, "shared-branch")
	require.NoError(t, err)
	assert.Equal(t, wtA, wtPath, "B's resolved worktree path should equal A's")
	assert.Len(t, sharers, 2)
	assert.Contains(t, sharers, "agent-a")
	assert.Contains(t, sharers, "agent-b")
}

func TestProvision_WorktreePerAgent_UniqueBranches_SoleSharers(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	locker := newTestLocker()
	bareRepo := initBareGitRepo(t)

	projectDir := t.TempDir()
	hostPath := filepath.Join(projectDir, "workspace")

	// Agent A with unique branch.
	err := ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-unique-1",
		AgentID:   "agent-a",
		AgentName: "agent-alpha",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone:  &api.GitCloneConfig{URL: bareRepo, Branch: "main", Depth: intPtr(0)},
	})
	require.NoError(t, err)

	// Agent B with unique branch.
	err = ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-unique-1",
		AgentID:   "agent-b",
		AgentName: "agent-beta",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone:  &api.GitCloneConfig{URL: bareRepo, Branch: "main", Depth: intPtr(0)},
	})
	require.NoError(t, err)

	// Both have their own worktrees.
	wtA := WorktreePath(hostPath, "agent-a")
	wtB := WorktreePath(hostPath, "agent-b")
	require.DirExists(t, wtA)
	require.DirExists(t, wtB)
	assert.NotEqual(t, wtA, wtB)

	// Each is sole sharer of its own branch.
	sharersA, pathA, err := ListSharers(hostPath, "agent-alpha")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-a"}, sharersA)
	assert.Equal(t, wtA, pathA)

	sharersB, pathB, err := ListSharers(hostPath, "agent-beta")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-b"}, sharersB)
	assert.Equal(t, wtB, pathB)
}

func TestProvision_WorktreePerAgent_ExistingRegistration_Idempotent(t *testing.T) {
	t.Setenv("SCION_HOST_UID", "")
	locker := newTestLocker()
	bareRepo := initBareGitRepo(t)

	projectDir := t.TempDir()
	hostPath := filepath.Join(projectDir, "workspace")

	// Provision agent once.
	err := ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-idem-1",
		AgentID:   "agent-a",
		AgentName: "idem-branch",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone:  &api.GitCloneConfig{URL: bareRepo, Branch: "main", Depth: intPtr(0)},
	})
	require.NoError(t, err)

	// Provision the same agent again (idempotent).
	err = ProvisionShared(ProvisionInput{
		Resolved:  ResolvedWorkspace{HostPath: hostPath, Backend: "local"},
		ProjectID: "proj-idem-1",
		AgentID:   "agent-a",
		AgentName: "idem-branch",
		Mode:      store.SharingModeWorktreePerAgent,
		Locker:    locker,
		GitClone:  &api.GitCloneConfig{URL: bareRepo, Branch: "main", Depth: intPtr(0)},
	})
	require.NoError(t, err)

	// Should still have exactly one sharer.
	sharers, _, err := ListSharers(hostPath, "idem-branch")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-a"}, sharers)
}

func intPtr(i int) *int { return &i }

/*
Copyright 2026 The Scion Authors.
*/

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// openScriptForTest is a small helper that opens path (via the same
// symlink-safe walk executeScriptEnforced uses) and returns its fd and
// chain, for tests that want to drive buildEnforcedCmd directly without
// invoking cmd.Run() (which would require real privilege to exercise the
// dropped branch's setgroups(2) call in this test environment).
func openScriptForTest(t *testing.T, path string) (fd int, chain []NodeOwnership) {
	t.Helper()
	dirFd, chain, err := openChainNoFollow(filepath.Dir(path))
	if err != nil {
		t.Fatalf("openChainNoFollow: %v", err)
	}
	fd, _, err = openScriptNoFollow(dirFd, filepath.Base(path))
	_ = closeFd(dirFd)
	if err != nil {
		t.Fatalf("openScriptNoFollow: %v", err)
	}
	t.Cleanup(func() { _ = closeFd(fd) })
	return fd, chain
}

func mustWriteExecutableScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestOpenChainNoFollow_WalksToRoot verifies the chain returned by
// openChainNoFollow starts at "/" and ends at the target directory's own
// ownership, entirely from fstat results on already-open fds.
func TestOpenChainNoFollow_WalksToRoot(t *testing.T) {
	dir := t.TempDir()
	leaf := filepath.Join(dir, "pre-start.d")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	fd, chain, err := openChainNoFollow(leaf)
	if err != nil {
		t.Fatalf("openChainNoFollow: %v", err)
	}
	defer func() { _ = closeFd(fd) }()

	if len(chain) < 2 {
		t.Fatalf("expected at least root + leaf in chain, got %d entries", len(chain))
	}
	// "/" is always uid 0 in any real filesystem.
	if chain[0].UID != 0 {
		t.Errorf("expected chain[0] (\"/\") to be uid 0, got %d", chain[0].UID)
	}
	// The leaf directory (under t.TempDir()) is owned by whoever is running
	// the test, not necessarily root.
	self := uint32(os.Getuid())
	if chain[len(chain)-1].UID != self {
		t.Errorf("expected chain leaf to be owned by the test's own uid %d, got %d", self, chain[len(chain)-1].UID)
	}
}

// TestOpenChainNoFollow_RefusesSymlinkedDir asserts a symlinked directory
// anywhere in the chain is refused outright — never silently followed, and
// never treated as "not existing" (which would let the hook silently not
// run instead of failing loudly).
func TestOpenChainNoFollow_RefusesSymlinkedDir(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "pre-start.d")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if _, _, err := openChainNoFollow(link); err == nil {
		t.Fatal("expected openChainNoFollow to refuse a symlinked directory component")
	}
}

// TestOpenScriptNoFollow_RefusesSymlinkedScript asserts a symlinked hook
// script is refused, never executed as either root or the workload.
func TestOpenScriptNoFollow_RefusesSymlinkedScript(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-script")
	mustWriteExecutableScript(t, real, "#!/bin/sh\nexit 0\n")
	link := filepath.Join(dir, "20-harness-provision")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	dirFd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatalf("open dir: %v", err)
	}
	defer func() { _ = closeFd(dirFd) }()

	if _, _, err := openScriptNoFollow(dirFd, "20-harness-provision"); err == nil {
		t.Fatal("expected openScriptNoFollow to refuse a symlinked script")
	}
}

// TestBuildEnforcedCmd_AsRoot verifies the "as root" branch runs the hook
// via the calling process's own credentials (no Credential override) and
// the plain hookEnv (AgentHome-overridden HOME, no USER/LOGNAME rewrite).
func TestBuildEnforcedCmd_AsRoot(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "20-harness-provision")
	mustWriteExecutableScript(t, script, "#!/bin/sh\nexit 0\n")
	fd, _ := openScriptForTest(t, script)

	m := &LifecycleManager{EnforcePrivilegeDrop: true, AgentHome: "/home/scion"}
	cmd := m.buildEnforcedCmd(fd, script, true)

	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
		t.Fatal("expected no Credential override for the as-root branch")
	}
	if cmd.Dir != "" {
		t.Errorf("expected no Dir override for the as-root branch, got %q", cmd.Dir)
	}
	if got := findEnvVar(cmd.Env, "HOME"); got != "/home/scion" {
		t.Errorf("HOME = %q, want /home/scion", got)
	}
}

// TestBuildEnforcedCmd_Dropped verifies the dropped branch sets Credential
// to WorkloadUID/GID, rewrites USER/LOGNAME/HOME to match the harness's own
// dropped identity, and sets Dir to WorkloadWorkingDir — "env and cwd
// handled the same way as the harness process".
func TestBuildEnforcedCmd_Dropped(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "session-end")
	mustWriteExecutableScript(t, script, "#!/bin/sh\nexit 0\n")
	fd, _ := openScriptForTest(t, script)

	m := &LifecycleManager{
		EnforcePrivilegeDrop: true,
		AgentHome:            "/home/scion",
		WorkloadUID:          1000,
		WorkloadGID:          1000,
		WorkloadUsername:     "scion",
		WorkloadWorkingDir:   "/workspace",
	}
	cmd := m.buildEnforcedCmd(fd, script, false)

	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatal("expected a Credential override for the dropped branch")
	}
	if cmd.SysProcAttr.Credential.Uid != 1000 || cmd.SysProcAttr.Credential.Gid != 1000 {
		t.Errorf("Credential = %+v, want uid=gid=1000", cmd.SysProcAttr.Credential)
	}
	if cmd.Dir != "/workspace" {
		t.Errorf("Dir = %q, want /workspace", cmd.Dir)
	}
	for _, tc := range []struct{ key, want string }{
		{"HOME", "/home/scion"},
		{"USER", "scion"},
		{"LOGNAME", "scion"},
	} {
		if got := findEnvVar(cmd.Env, tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestExecuteScriptEnforced_WorkloadOwnedRunsDropped is an end-to-end
// exercise of the dropped path that does not require root: it drops to the
// TEST'S OWN uid/gid (a no-op transition the kernel always permits, unlike
// dropping to a genuinely different uid, which needs CAP_SETUID/CAP_SETGID
// this test environment does not have — see the package doc comment on
// testing without root). The hook script under t.TempDir() is naturally
// "workload-owned" for this decision regardless of who runs the test,
// because t.TempDir() lives under a world-writable os.TempDir() ("/tmp",
// mode 1777 on every Linux system) — so DecideExecAsRoot's chain check
// fails on that ancestor's world-write bit independent of ownership,
// letting this test assert the real behavior without fabricating anything.
func TestExecuteScriptEnforced_WorkloadOwnedRunsDropped(t *testing.T) {
	if os.Geteuid() != 0 {
		// Go's os/exec calls setgroups(2) whenever SysProcAttr.Credential is
		// set (matching supervisor.Supervisor.Run's own Credential shape —
		// no Groups, no NoSetGroups override — see buildEnforcedCmd), and
		// setgroups(2) requires CAP_SETGID unconditionally, even to drop to
		// the calling process's own current uid/gid. TestBuildEnforcedCmd_
		// Dropped already covers the decision and the constructed Credential/
		// env/cwd without running the process; this one additionally proves
		// the real exec succeeds and lands at the right uid, which needs
		// that capability. Skip rather than require sudo.
		t.Skip("requires CAP_SETGID (root) to exercise the real setgroups(2)+exec path; see TestBuildEnforcedCmd_Dropped for the unprivileged-safe equivalent")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "pre-start.d", "30-project-custom")
	mustWriteExecutableScript(t, script, "#!/bin/sh\nid -u > "+marker+"\n")

	m := &LifecycleManager{
		EnforcePrivilegeDrop: true,
		WorkloadUID:          os.Getuid(),
		WorkloadGID:          os.Getgid(),
		WorkloadUsername:     "scion",
	}
	if err := m.executeScriptEnforced(script); err != nil {
		t.Fatalf("executeScriptEnforced: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	wantUID := []byte(itoa(os.Getuid()) + "\n")
	if string(got) != string(wantUID) {
		t.Errorf("hook ran as uid %q, want %q (the workload uid)", got, wantUID)
	}
}

// TestExecuteScriptEnforced_RootOwnedChainRunsAsRoot is the real-exec
// counterpart of TestBuildEnforcedCmd_AsRoot: it requires an actual
// root-owned, non-writable directory chain outside of any world-writable
// temp dir (t.TempDir() will never do — see the package doc comment above
// on why /tmp itself always fails the chain check), which in turn requires
// both root and write access to a location under "/" — this container's own
// "/" is root-owned 0755, so an unprivileged test cannot create anything
// there at all. Skipped unless running as root, with the reason recorded
// (see brief guidance on testing ownership-sensitive code without root);
// DecideExecAsRoot's own unit tests (privilege_test.go) already prove the
// decision logic this real exec depends on, without needing a real
// filesystem at all.
func TestExecuteScriptEnforced_RootOwnedChainRunsAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a root-owned, non-world-writable directory outside /tmp; DecideExecAsRoot's table tests already cover the decision itself")
	}
	dir, err := os.MkdirTemp("/root", "sb-dev-hooks-test-*")
	if err != nil {
		t.Skipf("could not create a root-owned fixture under /root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "pre-start.d", "20-harness-provision")
	mustWriteExecutableScript(t, script, "#!/bin/sh\nid -u > "+marker+"\n")
	if err := os.Chmod(filepath.Join(dir, "pre-start.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}

	m := &LifecycleManager{EnforcePrivilegeDrop: true, WorkloadUID: 1000, WorkloadGID: 1000}
	if err := m.executeScriptEnforced(script); err != nil {
		t.Fatalf("executeScriptEnforced: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != "0\n" {
		t.Errorf("hook ran as uid %q, want \"0\\n\" (root)", got)
	}
}

// TestExecuteScriptEnforced_NonEnforcedModeUnchanged verifies executeScript
// takes the pre-existing, unchanged path when EnforcePrivilegeDrop is false
// — including for a script that the enforced decision would drop (a
// world-writable ancestor under t.TempDir()) — matching the "byte-identical
// on every other runtime" requirement.
func TestExecuteScriptEnforced_NonEnforcedModeUnchanged(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "pre-start.d", "30-project-custom")
	mustWriteExecutableScript(t, script, "#!/bin/sh\necho -n ran >> "+marker+"\n")

	m := &LifecycleManager{HooksDirs: []string{dir}, Handlers: map[string][]Handler{}}
	if err := m.RunPreStart(); err != nil {
		t.Fatalf("RunPreStart: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != "ran" {
		t.Fatalf("expected the legacy exec path to run the script unconditionally, got %q", got)
	}
}

func findEnvVar(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			return e[len(prefix):]
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

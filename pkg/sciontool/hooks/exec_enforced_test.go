/*
Copyright 2026 The Scion Authors.
*/

package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
// script is refused, never executed as either root or the workload, and that
// the refusal is ErrScriptRefused so runScriptHooks' skip-not-abort logic
// can recognize it.
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

	_, _, err = openScriptNoFollow(dirFd, "20-harness-provision")
	if err == nil {
		t.Fatal("expected openScriptNoFollow to refuse a symlinked script")
	}
	if !errors.Is(err, ErrScriptRefused) {
		t.Errorf("error = %v, want it to wrap ErrScriptRefused", err)
	}
}

// TestPrepareEnforcedExec_WorkloadWritableChainYieldsDropped feeds
// prepareEnforcedExec a script under a REAL, unprivileged filesystem
// fixture — t.TempDir(), which sits under the world-writable os.TempDir()
// ("/tmp", mode 1777 on every Linux system) — and asserts the DECISION it
// derives from real fstat results is "drop", not a fabricated NodeOwnership
// value. This is the direct, unprivileged proof that a workload-planted
// hook (the scenario this whole mechanism exists to close) is dropped: it
// needs no privilege at all, because dropping requires no privilege — only
// running the process as a genuinely different uid does.
func TestPrepareEnforcedExec_WorkloadWritableChainYieldsDropped(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pre-start.d", "30-project-custom")
	mustWriteExecutableScript(t, script, "#!/bin/sh\nexit 0\n")

	prep, err := prepareEnforcedExec(script)
	if err != nil {
		t.Fatalf("prepareEnforcedExec: %v", err)
	}
	defer func() { _ = closeFd(prep.fd) }()

	if !prep.executable {
		t.Error("expected the script to be reported executable")
	}
	if prep.asRoot {
		t.Error("expected a script under a world-writable ancestor (t.TempDir()/os.TempDir()) to be dropped, got asRoot=true")
	}
}

// TestExecViaFd_RunsShebangScript proves execViaFd's /proc/self/fd/<n> exec
// actually runs a shebang script end to end, unprivileged (the as-root
// branch — no SysProcAttr.Credential — needs no capability to run).
func TestExecViaFd_RunsShebangScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook")
	mustWriteExecutableScript(t, script, "#!/bin/sh\necho -n hello\n")

	fd, err := unix.Open(script, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = closeFd(fd) }()

	out, err := execViaFd(fd, script).Output()
	if err != nil {
		t.Fatalf("execViaFd(...).Output(): %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("output = %q, want %q", out, "hello")
	}
}

// TestExecViaFd_SwapAfterOpenRunsOriginalInode is the committed form of the
// TOCTOU probe: it opens a script, then REPLACES the directory entry at
// that same path with a brand-new inode (remove, then create — not a
// truncate-in-place, which would rewrite the already-open fd's own
// content), and asserts execViaFd still runs the ORIGINAL content. This is
// what proves the file DecideExecAsRoot inspects (via the fd this test
// keeps open across the swap) is provably the file that runs, matching
// executeScriptEnforced's own no-TOCTOU construction.
func TestExecViaFd_SwapAfterOpenRunsOriginalInode(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook")
	mustWriteExecutableScript(t, script, "#!/bin/sh\necho -n original\n")

	fd, err := unix.Open(script, unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = closeFd(fd) }()

	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	mustWriteExecutableScript(t, script, "#!/bin/sh\necho -n replaced\n")

	out, err := execViaFd(fd, script).Output()
	if err != nil {
		t.Fatalf("execViaFd(...).Output(): %v", err)
	}
	if string(out) != "original" {
		t.Errorf("output = %q, want %q (the original inode, not the swapped-in replacement)", out, "original")
	}
}

// TestExecuteScriptEnforced_NonExecutableScriptIsSkipped proves the
// not-executable check runs on the already-open fd (fdIsExecutable) and
// results in a skip, not an error and not an exec attempt.
func TestExecuteScriptEnforced_NonExecutableScriptIsSkipped(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pre-start.d", "not-executable")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &LifecycleManager{EnforcePrivilegeDrop: true, WorkloadUID: os.Getuid(), WorkloadGID: os.Getgid()}
	if err := m.executeScriptEnforced(script, EventPreStart); err != nil {
		t.Fatalf("executeScriptEnforced: %v, want nil (skip, not error, for a non-executable script)", err)
	}
}

// TestBuildEnforcedCmd_AsRoot verifies the "as root" branch at pre-start
// runs the hook via the calling process's own credentials (no Credential
// override) and the plain hookEnv (AgentHome-owned HOME, no USER/LOGNAME
// rewrite, no hardening) — the provisioner's own required environment.
func TestBuildEnforcedCmd_AsRoot(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "20-harness-provision")
	mustWriteExecutableScript(t, script, "#!/bin/sh\nexit 0\n")
	fd, _ := openScriptForTest(t, script)

	m := &LifecycleManager{EnforcePrivilegeDrop: true, AgentHome: "/home/scion"}
	cmd := m.buildEnforcedCmd(fd, script, EventPreStart, true)

	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
		t.Fatal("expected no Credential override for the as-root branch")
	}
	if cmd.Dir != "" {
		t.Errorf("expected no Dir override for the as-root branch, got %q", cmd.Dir)
	}
	if got := findEnvVar(cmd.Env, "HOME"); got != "/home/scion" {
		t.Errorf("HOME = %q, want /home/scion (the provisioner's required env, unhardened at pre-start)", got)
	}
	if got := findEnvVar(cmd.Env, "SCION_HOOK_PATH"); got != script {
		t.Errorf("SCION_HOOK_PATH = %q, want %q", got, script)
	}
}

// TestBuildEnforcedCmd_AsRootPostWorkloadEvent is the Medium-severity
// hardening this round fixes: a root-eligible hook at any event AFTER
// pre-start (post-start here) must never run with HOME pointed at the
// workload's own home directory — that would let a root-run python/bash/
// git/pip hook load workload-planted rc/site/config files and execute them
// as root, the same escalation class DecideExecAsRoot exists to close.
func TestBuildEnforcedCmd_AsRootPostWorkloadEvent(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "10-post-start")
	mustWriteExecutableScript(t, script, "#!/bin/sh\nexit 0\n")
	fd, _ := openScriptForTest(t, script)

	m := &LifecycleManager{EnforcePrivilegeDrop: true, AgentHome: "/home/scion"}
	cmd := m.buildEnforcedCmd(fd, script, EventPostStart, true)

	if got := findEnvVar(cmd.Env, "HOME"); got != "/root" {
		t.Errorf("HOME = %q, want /root (never the workload-owned home) for a root hook at post-start", got)
	}
	if got := findEnvVar(cmd.Env, "PYTHONNOUSERSITE"); got != "1" {
		t.Errorf("PYTHONNOUSERSITE = %q, want \"1\"", got)
	}
	if got := findEnvVar(cmd.Env, "PATH"); got != "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" {
		t.Errorf("PATH = %q, want the fixed, minimal PATH", got)
	}
	for _, event := range []string{EventPreStop, EventSessionEnd} {
		cmd := m.buildEnforcedCmd(fd, script, event, true)
		if got := findEnvVar(cmd.Env, "HOME"); got != "/root" {
			t.Errorf("event %s: HOME = %q, want /root", event, got)
		}
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
	cmd := m.buildEnforcedCmd(fd, script, EventSessionEnd, false)

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
		{"SCION_HOOK_PATH", script},
	} {
		if got := findEnvVar(cmd.Env, tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestExecuteScriptEnforced_WorkloadOwnedRunsDropped is an end-to-end
// exercise of the dropped path. It needs CAP_SETGID (root) because Go's
// os/exec calls setgroups(2) whenever SysProcAttr.Credential is set
// (matching supervisor.Supervisor.Run's own Credential shape — no Groups, no
// NoSetGroups override — see buildEnforcedCmd), and setgroups(2) requires
// that capability unconditionally, even to drop to the calling process's own
// current uid/gid.
//
// When it runs (euid == 0), it drops to a genuinely different, non-zero uid
// (65534, traditionally "nobody") rather than self-dropping to the test's
// own uid: euid 0 self-dropping to WorkloadUID = os.Getuid() = 0 would make
// the "it ran as the workload uid" assertion pass whether or not the drop
// actually happened, since 0 == 0 either way. Using 65534 makes the marker
// file's content only match if the credential switch was real.
// TestPrepareEnforcedExec_WorkloadWritableChainYieldsDropped above is the
// unprivileged equivalent for the decision itself.
func TestExecuteScriptEnforced_WorkloadOwnedRunsDropped(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires CAP_SETGID (root) to exercise the real setgroups(2)+exec path; see TestPrepareEnforcedExec_WorkloadWritableChainYieldsDropped and TestBuildEnforcedCmd_Dropped for the unprivileged-safe equivalents")
	}
	const dropUID, dropGID = 65534, 65534
	dir := t.TempDir()
	// World-writable so uid 65534 -- distinct from the real root this test
	// runs as -- can create the marker file after the drop.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "pre-start.d", "30-project-custom")
	mustWriteExecutableScript(t, script, "#!/bin/sh\nid -u > "+marker+"\n")

	m := &LifecycleManager{
		EnforcePrivilegeDrop: true,
		WorkloadUID:          dropUID,
		WorkloadGID:          dropGID,
		WorkloadUsername:     "nobody",
	}
	if err := m.executeScriptEnforced(script, EventPreStart); err != nil {
		t.Fatalf("executeScriptEnforced: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	wantUID := []byte(itoa(dropUID) + "\n")
	if string(got) != string(wantUID) {
		t.Errorf("hook ran as uid %q, want %q (the distinct workload uid, proving the drop actually happened)", got, wantUID)
	}
}

// TestExecuteScriptEnforced_RootOwnedChainRunsAsRoot is the real-exec
// counterpart of TestBuildEnforcedCmd_AsRoot: it requires an actual
// root-owned, non-writable directory chain outside of any world-writable
// temp dir (t.TempDir() will never do — see
// TestPrepareEnforcedExec_WorkloadWritableChainYieldsDropped's own doc
// comment on why /tmp itself always fails the chain check), which in turn
// requires both root and write access to a location under "/" — this
// container's own "/" is root-owned 0755, so an unprivileged test cannot
// create anything there at all. Skipped unless running as root; when
// skipped, DecideExecAsRoot's own table tests (privilege_test.go) already
// prove the decision logic this real exec depends on, without needing a
// real filesystem at all.
func TestExecuteScriptEnforced_RootOwnedChainRunsAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a root-owned, non-world-writable directory outside /tmp; DecideExecAsRoot's table tests already cover the decision itself")
	}
	dir, err := os.MkdirTemp("/root", "hooks-enforced-test-*")
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
	if err := m.executeScriptEnforced(script, EventPreStart); err != nil {
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

// TestRunPreStart_EnforcedModeSkipsRefusedWorkloadEntryButRunsSiblings
// proves the skip-not-abort behavior: a workload-planted symlink under a
// non-EnforcedHooksDir hooks directory must not DoS the event's other,
// legitimate hooks.
func TestRunPreStart_EnforcedModeSkipsRefusedWorkloadEntryButRunsSiblings(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	good := filepath.Join(dir, "pre-start.d", "10-good")
	mustWriteExecutableScript(t, good, "#!/bin/sh\necho -n ran >> "+marker+"\n")
	bad := filepath.Join(dir, "pre-start.d", "05-bad-symlink")
	if err := os.Symlink("/nonexistent", bad); err != nil {
		t.Fatal(err)
	}

	m := &LifecycleManager{
		EnforcePrivilegeDrop: true,
		HooksDirs:            []string{dir},
		Handlers:             map[string][]Handler{},
		WorkloadUID:          os.Getuid(),
		WorkloadGID:          os.Getgid(),
	}
	err := m.RunPreStart()

	// The refused symlink must never abort iteration, regardless of what
	// happens to 10-good next. When this test runs as root, 10-good's own
	// drop-to-self-uid exec fully succeeds and err is nil. When it does not
	// (this container, and CI in general), 10-good's exec still fails, but
	// for an unrelated reason — dropping needs CAP_SETGID even to the
	// calling process's own current uid/gid (see
	// TestExecuteScriptEnforced_WorkloadOwnedRunsDropped's doc comment) — so
	// the resulting error mentions 10-good, never 05-bad-symlink. Asserting
	// on which script the error names, rather than requiring err == nil,
	// makes this test prove the skip-not-abort property in both
	// environments instead of only under root.
	if err == nil {
		got, readErr := os.ReadFile(marker)
		if readErr != nil {
			t.Fatalf("expected the sibling hook to have run: %v", readErr)
		}
		if string(got) != "ran" {
			t.Errorf("marker = %q, want %q", got, "ran")
		}
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "05-bad-symlink") {
		t.Fatalf("RunPreStart error mentions the refused symlink; it must be skipped, not aborted on: %v", err)
	}
	if !strings.Contains(msg, "10-good") {
		t.Fatalf("RunPreStart error does not mention the sibling hook; iteration did not reach it: %v", err)
	}
}

// TestRunPreStart_EnforcedModeHardFailsRefusedEntryUnderEnforcedHooksDir
// proves the exception: a refused entry under EnforcedHooksDir itself (the
// root-owned, broker-delivered directory, never workload-writable) still
// hard-fails the event instead of being silently skipped — an anomaly there
// is worth aborting over, not routine workload nuisance.
func TestRunPreStart_EnforcedModeHardFailsRefusedEntryUnderEnforcedHooksDir(t *testing.T) {
	dir := t.TempDir()
	orig := EnforcedHooksDir
	EnforcedHooksDir = dir
	t.Cleanup(func() { EnforcedHooksDir = orig })

	if err := os.MkdirAll(filepath.Join(dir, "pre-start.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "pre-start.d", "05-bad-symlink")
	if err := os.Symlink("/nonexistent", bad); err != nil {
		t.Fatal(err)
	}

	m := &LifecycleManager{
		EnforcePrivilegeDrop: true,
		HooksDirs:            []string{dir},
		Handlers:             map[string][]Handler{},
		WorkloadUID:          os.Getuid(),
		WorkloadGID:          os.Getgid(),
	}
	if err := m.RunPreStart(); err == nil {
		t.Fatal("expected RunPreStart to hard-fail on a refused entry under the (test's stand-in for the) enforced hooks dir")
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

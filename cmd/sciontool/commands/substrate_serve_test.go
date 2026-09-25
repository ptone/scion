/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent/state"
	"github.com/GoogleCloudPlatform/scion/pkg/substratecaps"
)

func TestSubstrateServeCommand_Help(t *testing.T) {
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetArgs([]string{"substrate-serve", "--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "substrate-serve") {
		t.Error("help output should mention 'substrate-serve'")
	}
	if !strings.Contains(output, "addr") {
		t.Error("help output should mention the --addr flag")
	}
}

func TestSubstrateServeCommand_AddrFlagDefault(t *testing.T) {
	flag := substrateServeCmd.Flags().Lookup("addr")
	if flag == nil {
		t.Fatal("addr flag not found")
	}
	// phase1-spec.md §2.1: the router targets :80 by default.
	if flag.DefValue != ":80" {
		t.Errorf("expected default addr :80, got %s", flag.DefValue)
	}
}

// TestSubstrateServeCommand_Integration_SIGTERMNotForwarded is a real
// subprocess integration test (mirrors TestInitCommand_Integration's
// pattern) proving the Phase 1 requirement from phase1-spec.md §2.1:
// substrate-serve logs SIGTERM but does not forward it to the harness and
// does not exit. It builds the sciontool binary, boots `substrate-serve`,
// bootstraps a long-lived child, sends the running process a real SIGTERM,
// and confirms both the control server and the child survive it.
func TestSubstrateServeCommand_Integration_SIGTERMNotForwarded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("SCION_INTEGRATION_TEST") == "" {
		t.Skip("skipping integration test: SCION_INTEGRATION_TEST not set")
	}

	// Clear Hub env vars so the subprocess cannot talk to a real Hub. See
	// issue #123 / scrubHubEnv's use in TestInitCommand_Integration.
	scrubHubEnv(t)

	// substrate-serve's synchronous /bootstrap precondition
	// (checkPrivilegeDropFeasible) deliberately refuses to bootstrap
	// without every capability in substratecaps.Required — checked here by
	// iterating that shared list, not a hardcoded subset, so this test
	// tracks it automatically as capabilities are added — which this
	// integration test's unprivileged subprocess never has (unlike a real
	// Substrate actor, which is granted exactly that set — see
	// substrate_template.go). This test is about SIGTERM handling, not the
	// privilege drop, so skip rather than fail when the environment can't
	// satisfy a precondition this test was never exercising on purpose.
	for _, c := range substratecaps.Required {
		if !hasCapBit(c.EffBit) {
			t.Skipf("skipping: this environment lacks CAP_%s, so the bootstrap privilege-drop precondition would reject the request before this test's real subject (SIGTERM handling) is ever reached", c.Name)
		}
	}

	// The runner running as root (rather than as an unprivileged CI/dev
	// account) means we can't safely pick a SCION_HOST_UID/GID that's
	// guaranteed to already match the real "scion" account without either
	// performing a real /etc/passwd edit (exactly what this test must not
	// do — see the HOST_UID comment below) or assuming this is a throwaway
	// container, which isn't something this test can verify.
	if os.Getuid() == 0 {
		t.Skip("skipping: this integration test only runs as a non-root user")
	}
	// SCION_HOST_UID/GID are set to the runner's own actual "scion" account
	// (looked up here, not hardcoded and not just this process's own
	// os.Getuid()): the subprocess resolves "scion" from the same real
	// /etc/passwd this test process runs under, so using that account's own
	// recorded uid/gid guarantees setupHostUser takes the "already correct"
	// shortcut — no usermod/sed edit against the runner's real
	// /etc/passwd/group is ever attempted, regardless of what uid this test
	// process itself happens to be running as. If there's no local "scion"
	// account at all, skip: any other value would force a real edit
	// attempt, which is exactly what this test must not do.
	scionUser, err := user.Lookup("scion")
	if err != nil {
		t.Skipf("skipping: no local \"scion\" account to borrow a safe SCION_HOST_UID/GID from: %v", err)
	}
	hostUID, hostGID := scionUser.Uid, scionUser.Gid

	binPath := filepath.Join(t.TempDir(), "sciontool-test")
	buildCmd := exec.Command("go", "build", "-buildvcs=false", "-o", binPath, "../")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Skipf("failed to build sciontool for integration test: %v\n%s", err, out)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", findFreePort(t))
	baseURL := "http://" + addr

	cmd := exec.Command(binPath, "substrate-serve", "--addr", addr)
	// HOME is redirected to a throwaway directory as defence in depth: this
	// test's subprocess is expected to only exercise the happy path (a
	// successful bootstrap), but if a future change ever made it reach a
	// RunInit failure, reportInitFailure's agentHome fallback must not
	// resolve to this machine's real, ambient $HOME (see TestMain's own
	// HOME redirection in this package for the incident this defends
	// against — a subprocess isn't automatically covered by that, since it
	// gets a fresh environment from cmd.Env, not the test binary's own).
	// skipRootfsFixupEnv: this subprocess runs the real runSubstrateServe
	// (and answers the real /bootstrap below) against this machine's real
	// "/" and real "scion" home — nothing about this test binary's own
	// TestMain sandboxing reaches a real exec'd child. Whoever runs this
	// integration test locally with real CAP_CHOWN would otherwise have
	// entries under their own real home Lchowned by either fixup call site.
	// This never weakens checkPrivilegeDropFeasible (see skipRootfsFixupEnv's
	// own doc comment): the bootstrap below still only succeeds because this
	// runner's own "scion" home already satisfies that check without the
	// fixup's help.
	cmd.Env = append(filterHubEnv(os.Environ()), "HOME="+t.TempDir(), skipRootfsFixupEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start substrate-serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("substrate-serve stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	})

	waitForHealthzState(t, baseURL, "awaiting-bootstrap", 5*time.Second)

	const controlToken = "test-control-token"
	// A distinctive sleep duration doubles as the pgrep marker below so this
	// test doesn't collide with an unrelated "sleep" process on the runner.
	const marker = "60013"
	bootstrapBody, err := json.Marshal(map[string]any{
		// SCION_HOST_UID/GID: satisfies checkPrivilegeDropFeasible's
		// precondition alongside the capability skip above — a real
		// Substrate bootstrap always sets these (buildBootstrapEnv). Set to
		// the runner's own uid/gid (see above) so setupHostUser takes the
		// "already correct" shortcut with no real /etc/passwd edit.
		"env":           map[string]string{"SCION_HOST_UID": hostUID, "SCION_HOST_GID": hostGID},
		"files":         []any{},
		"start_cmd":     "sleep " + marker,
		"control_token": controlToken,
	})
	if err != nil {
		t.Fatalf("marshal bootstrap body: %v", err)
	}
	bootstrapReq, err := http.NewRequest(http.MethodPost, baseURL+"/scion/v1/bootstrap", bytes.NewReader(bootstrapBody))
	if err != nil {
		t.Fatalf("build bootstrap request: %v", err)
	}
	bootstrapReq.Header.Set("Authorization", "Bearer any-nonce")
	bootstrapResp, err := http.DefaultClient.Do(bootstrapReq)
	if err != nil {
		t.Fatalf("bootstrap request failed: %v", err)
	}
	_ = bootstrapResp.Body.Close()
	if bootstrapResp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want 200", bootstrapResp.StatusCode)
	}

	waitForHealthzState(t, baseURL, "running", 5*time.Second)

	childAlive := func() bool {
		out := execViaControlServer(t, baseURL, controlToken, []string{"pgrep", "-f", "sleep " + marker})
		return strings.TrimSpace(out) != ""
	}
	if !waitUntil(childAlive, 5*time.Second) {
		t.Fatalf("harness child did not start in time; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM to substrate-serve: %v", err)
	}

	// Give the process time to receive and (mis)handle the signal before
	// asserting survival.
	time.Sleep(1 * time.Second)

	waitForHealthzState(t, baseURL, "running", 3*time.Second)

	if !childAlive() {
		t.Fatal("harness child was killed after SIGTERM; substrate-serve must log it, not forward it (Phase 1 limitation)")
	}
}

// TestRunSubstrateServe_CallsRootfsFixupBeforeListening proves call site 1
// (see fixupRootfsForScion's doc comment): runSubstrateServe must run the
// rootfs fixup before the control server can ever answer /healthz, so a
// restored actor's golden snapshot always reflects the corrected rootfs.
// runSubstrateServe is otherwise a strictly sequential function — the fixup
// call and the listen attempt can't race each other — so it's enough to
// force the listen attempt to fail immediately (by holding the port open
// ourselves first) and confirm the fixup ran anyway, rather than standing up
// a real, reachable server on a free port. This is also the "the startup
// call gets removed" mutation check: deleting the startupRootfsFixup("/")
// call in runSubstrateServe fails this test.
func TestRunSubstrateServe_CallsRootfsFixupBeforeListening(t *testing.T) {
	orig := startupRootfsFixup
	t.Cleanup(func() { startupRootfsFixup = orig })
	var called bool
	var gotRoot string
	startupRootfsFixup = func(root string) {
		called = true
		gotRoot = root
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port to force a listen failure: %v", err)
	}
	defer func() { _ = l.Close() }()

	if code := runSubstrateServe(l.Addr().String()); code != 1 {
		t.Fatalf("runSubstrateServe() = %d, want 1 (the address is already in use, so ListenAndServe must fail)", code)
	}
	if !called {
		t.Error("runSubstrateServe returned without ever calling startupRootfsFixup")
	}
	if gotRoot != "/" {
		t.Errorf("startupRootfsFixup called with root = %q, want \"/\"", gotRoot)
	}
}

// TestRunSubstrateServe_DoesNotLeakSignalGoroutine is a regression test:
// runSubstrateServe's SIGTERM-handling goroutine must exit on every return
// path, not leak one per call — see runSubstrateServe's own signal.Stop/
// close defer, and the project log for why this matters even though
// production only ever calls this once.
func TestRunSubstrateServe_DoesNotLeakSignalGoroutine(t *testing.T) {
	orig := startupRootfsFixup
	t.Cleanup(func() { startupRootfsFixup = orig })
	startupRootfsFixup = func(string) {}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port to force a listen failure: %v", err)
	}
	defer func() { _ = l.Close() }()

	runtime.GC()
	before := runtime.NumGoroutine()

	const iterations = 20
	for i := 0; i < iterations; i++ {
		if code := runSubstrateServe(l.Addr().String()); code != 1 {
			t.Fatalf("run %d: runSubstrateServe() = %d, want 1 (the address is already in use)", i, code)
		}
	}

	// Generous, not required: signal.Stop/close happens synchronously in
	// runSubstrateServe's own defer before it returns, so there should be
	// nothing left to settle — this just protects against flakiness from
	// the Go runtime's own goroutine bookkeeping.
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= before+5 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after > before+5 {
		t.Errorf("goroutine count grew from %d to %d over %d runSubstrateServe calls that each failed to listen — want it to stay flat (no per-call leak)", before, after, iterations)
	}
}

// TestRunSubstrateServe_SkipRootfsFixupEnv proves skipRootfsFixupEnv
// actually skips call site 1 when set — the escape hatch
// TestSubstrateServeCommand_Integration_SIGTERMNotForwarded's subprocess
// relies on to never touch a real machine's real home.
func TestRunSubstrateServe_SkipRootfsFixupEnv(t *testing.T) {
	t.Setenv(skipRootfsFixupEnv, "1")
	orig := startupRootfsFixup
	t.Cleanup(func() { startupRootfsFixup = orig })
	var called bool
	startupRootfsFixup = func(string) { called = true }

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port to force a listen failure: %v", err)
	}
	defer func() { _ = l.Close() }()

	if code := runSubstrateServe(l.Addr().String()); code != 1 {
		t.Fatalf("runSubstrateServe() = %d, want 1 (the address is already in use)", code)
	}
	if called {
		t.Error("startupRootfsFixup was called despite the skip env var being set")
	}
}

// TestRunSubstrateServe_RootfsFixupEnvUnset_CallSite1Runs is
// TestRunSubstrateServe_SkipRootfsFixupEnv's mirror: with the env unset
// (the production default), call site 1 must still run.
func TestRunSubstrateServe_RootfsFixupEnvUnset_CallSite1Runs(t *testing.T) {
	orig := startupRootfsFixup
	t.Cleanup(func() { startupRootfsFixup = orig })
	var called bool
	startupRootfsFixup = func(string) { called = true }

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port to force a listen failure: %v", err)
	}
	defer func() { _ = l.Close() }()

	if code := runSubstrateServe(l.Addr().String()); code != 1 {
		t.Fatalf("runSubstrateServe() = %d, want 1 (the address is already in use)", code)
	}
	if !called {
		t.Error("startupRootfsFixup was not called with the skip env unset")
	}
}

// TestSubstrateServeRootfsFixup_SkipEnv proves skipRootfsFixupEnv also gates
// call site 2 (the /bootstrap fallback), not just call site 1 —
// TestSubstrateServeCommand_Integration_SIGTERMNotForwarded's real
// subprocess POSTs /bootstrap, which reaches this call site against a real
// "/" and real "scion" home whenever it isn't gated the same way.
func TestSubstrateServeRootfsFixup_SkipEnv(t *testing.T) {
	t.Setenv(skipRootfsFixupEnv, "1")
	orig := bootstrapRootfsFixup
	t.Cleanup(func() { bootstrapRootfsFixup = orig })
	var called bool
	bootstrapRootfsFixup = func(string) { called = true }

	substrateServeRootfsFixup()

	if called {
		t.Error("bootstrapRootfsFixup was called despite the skip env var being set")
	}
}

// TestSubstrateServeRootfsFixup_EnvUnset_Runs is
// TestSubstrateServeRootfsFixup_SkipEnv's mirror: with the env unset, call
// site 2 must still run — the production default.
func TestSubstrateServeRootfsFixup_EnvUnset_Runs(t *testing.T) {
	orig := bootstrapRootfsFixup
	t.Cleanup(func() { bootstrapRootfsFixup = orig })
	var called bool
	var gotRoot string
	bootstrapRootfsFixup = func(root string) { called = true; gotRoot = root }

	substrateServeRootfsFixup()

	if !called {
		t.Error("bootstrapRootfsFixup was not called with the skip env unset")
	}
	if gotRoot != "/" {
		t.Errorf("bootstrapRootfsFixup called with root = %q, want \"/\"", gotRoot)
	}
}

// TestSubstrateServeBootstrap_SkipRootfsFixupEnvSet_StillRejectsUnfixedRootfs
// pins the binding invariant on skipRootfsFixupEnv at the wiring level, not
// by calling checkPrivilegeDropFeasible directly: it must skip ONLY the
// /bootstrap rootfs fixup (call site 2, bootstrapRootfsFixup), never the
// privilege-drop precondition substrate-serve wires into the Server. It
// drives POST /scion/v1/bootstrap through newSubstrateServeServer, the same
// wiring runSubstrateServe uses, with a rootfs that still needs the fixup
// (here: $HOME reported root-owned, exactly what fixupRootfsForScion would
// have corrected). This proves the knob can only make bootstrap fail closed
// sooner, never bypass the check — a regression that made the env also
// short-circuit substrateServePrivilegeDropChecker (or
// checkPrivilegeDropFeasible itself) would make this test see the bootstrap
// accepted and the init runner started, and fail.
func TestSubstrateServeBootstrap_SkipRootfsFixupEnvSet_StillRejectsUnfixedRootfs(t *testing.T) {
	t.Setenv(skipRootfsFixupEnv, "1")

	origDeps := defaultPrivilegeDropPreconditionDeps
	t.Cleanup(func() { defaultPrivilegeDropPreconditionDeps = origDeps })
	d := fakePrivilegeDropDeps(t)
	// $HOME owned by root, not the target uid — the condition the fixup
	// exists to correct.
	d.statPath = statPathOverride("/home/scion", fakeFileInfo{mode: fs.ModeDir | 0o755, uid: 0, gid: 0})
	defaultPrivilegeDropPreconditionDeps = d

	origFixup := bootstrapRootfsFixup
	t.Cleanup(func() { bootstrapRootfsFixup = origFixup })
	var fixupCalled bool
	bootstrapRootfsFixup = func(string) { fixupCalled = true }

	var initCalled bool
	stubRunInit := func(argv []string, opts InitRunOptions) int {
		initCalled = true
		return 0
	}

	srv := newSubstrateServeServer(stubRunInit)
	rec := doSubstrateServeJSON(t, srv, "POST", "/scion/v1/bootstrap", "any-token", map[string]any{
		"env":           map[string]string{},
		"files":         []any{},
		"start_cmd":     "true",
		"control_token": "tok",
	})

	if rec.Code == 200 || rec.Code < 400 {
		t.Errorf("status = %d, want a non-2xx rejection — skipRootfsFixupEnv must never weaken the privilege-drop precondition", rec.Code)
	}
	// The init runner is started asynchronously by handleBootstrap on
	// success (see its own doc comment); give any wrongly-started goroutine
	// a moment to flip the flag before asserting it never did.
	time.Sleep(20 * time.Millisecond)
	if initCalled {
		t.Error("the init runner was invoked despite the privilege-drop precondition failing; the harness must never start")
	}
	if fixupCalled {
		t.Error("bootstrapRootfsFixup was called despite the skip env var being set")
	}
}

// -----------------------------------------------------------------------
// resolveSubstrateHarnessCwd / InitRunOptions.WorkingDir wiring
// (sb-dev-cwd: the harness process tree must not start at cmd.Dir="/".
// sb-dev-cwd-r2: the fallback must resolve the SCION uid's home, not
// substrate-serve's own root $HOME, and every candidate must be verified
// searchable by the scion uid/gid, not just stat-able by root.)
// -----------------------------------------------------------------------

// fakeScionUserForHarnessCwd is resolveSubstrateHarnessCwd tests' shared
// lookupUser fake: uid/gid 1000/1000, home "/home/scion" — the same scion
// user fakePrivilegeDropDeps uses.
func fakeScionUserForHarnessCwd(string) (*user.User, error) {
	return &user.User{Username: "scion", Uid: "1000", Gid: "1000", HomeDir: "/home/scion"}, nil
}

// fakeSubstrateHarnessCwdDeps builds substrateHarnessCwdDeps from plain
// maps, so tests never depend on this machine's real SCION_WORKSPACE_PATH,
// filesystem, or "scion" user. leafDirs controls only the workspace/home
// candidate(s) under test; "/" and "/home" are always a plain, world-
// searchable directory, matching every real deployment, so a test only has
// to fake out the one leaf it's exercising rather than the whole ancestor
// chain.
func fakeSubstrateHarnessCwdDeps(env map[string]string, leafDirs map[string]fs.FileInfo) substrateHarnessCwdDeps {
	return substrateHarnessCwdDeps{
		getenv: func(k string) string { return env[k] },
		// p is cleaned before either lookup, matching os.Stat's own
		// semantics (the kernel resolves "." and ".." lexically before ever
		// touching the filesystem): a non-canonical spelling like "/.", "//"
		// or "/tmp/.." must reach this fake exactly as the real filesystem
		// would see it, or a test relying on production's own
		// filepath.Clean would pass vacuously whether or not that Clean
		// call is actually there (sb-dev-cwd-r4, R1).
		stat: func(p string) (os.FileInfo, error) {
			clean := filepath.Clean(p)
			switch clean {
			case "/", "/home":
				return fakeFileInfo{mode: fs.ModeDir | 0o755}, nil
			}
			if info, ok := leafDirs[clean]; ok {
				return info, nil
			}
			return nil, fmt.Errorf("stat %s: no such file or directory", p)
		},
		// None of these fake paths are real symlinks, so resolving one is a
		// no-op; a test exercising the symlink-target check below wires its
		// own evalSymlinks against a real filesystem instead.
		evalSymlinks: func(p string) (string, error) { return p, nil },
		lookupUser:   fakeScionUserForHarnessCwd,
	}
}

// usableDirInfo is a plain, world-searchable directory: canSearchDir treats
// it as searchable by any uid/gid via the "other" bit, regardless of who
// nominally owns it.
var usableDirInfo = fakeFileInfo{mode: fs.ModeDir | 0o755}

// TestResolveSubstrateHarnessCwd_UsesWorkspaceWhenValid is the normal case:
// SCION_WORKSPACE_PATH exists, is a directory, and is searchable by the
// scion uid — matching the image's WORKDIR the way Docker/Podman/Kubernetes
// already get for free.
func TestResolveSubstrateHarnessCwd_UsesWorkspaceWhenValid(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/workspace"},
		map[string]fs.FileInfo{"/workspace": usableDirInfo},
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/workspace" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want %q", got, "/workspace")
	}
}

// TestResolveSubstrateHarnessCwd_DefaultsWorkspacePath proves the default
// "/workspace" is used when SCION_WORKSPACE_PATH is unset, mirroring
// RunInit's own default (see checkPrivilegeDropFeasible's workspacePath
// handling).
func TestResolveSubstrateHarnessCwd_DefaultsWorkspacePath(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{},
		map[string]fs.FileInfo{"/workspace": usableDirInfo},
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/workspace" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want default %q", got, "/workspace")
	}
}

// TestResolveSubstrateHarnessCwd_IgnoresAmbientHOMEWhenUnset and
// TestResolveSubstrateHarnessCwd_IgnoresAmbientHOMEWhenRoot are R1's
// regression tests: substrate-serve's own $HOME (root's — e.g. "/root", the
// usual container-runtime default for uid 0 — or unset) must never be
// consulted at all. The fallback is always the scion user's real home from
// lookupUser("scion"), independent of $HOME. "/root" is deliberately also
// stat-able here (and would satisfy the old, buggy code's checks) so a
// regression that reads $HOME again would make this test observe "/root"
// instead of "/home/scion" and fail.
func TestResolveSubstrateHarnessCwd_IgnoresAmbientHOMEWhenUnset(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{}, // HOME unset; SCION_WORKSPACE_PATH unset -> default, deliberately absent below
		map[string]fs.FileInfo{"/home/scion": usableDirInfo},
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/home/scion" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want the scion user's home %q", got, "/home/scion")
	}
}

func TestResolveSubstrateHarnessCwd_IgnoresAmbientHOMEWhenRoot(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"HOME": "/root"},
		map[string]fs.FileInfo{"/root": usableDirInfo, "/home/scion": usableDirInfo},
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/home/scion" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want the scion user's home %q, never $HOME=/root", got, "/home/scion")
	}
}

// TestResolveSubstrateHarnessCwd_FallsBackWhenWorkspaceNotSearchableByScionUID
// is R1's core bug: a workspace directory that stats fine (as root, the uid
// resolveSubstrateHarnessCwd itself runs as) but is not searchable by the
// scion uid/gid — here, owned by root, mode 0700 — must NOT be chosen,
// because supervisor.Run's chdir happens AFTER the credential drop to the
// scion uid.
func TestResolveSubstrateHarnessCwd_FallsBackWhenWorkspaceNotSearchableByScionUID(t *testing.T) {
	rootOnly := fakeFileInfo{mode: fs.ModeDir | 0o700, uid: 0, gid: 0}
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/workspace"},
		map[string]fs.FileInfo{"/workspace": rootOnly, "/home/scion": usableDirInfo},
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/home/scion" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want fallback %q (workspace is root-only 0700, scion uid 1000 can't search it)", got, "/home/scion")
	}
}

// TestResolveSubstrateHarnessCwd_FallsBackWhenWorkspaceIsAFile covers "not a
// directory" (as opposed to merely missing or unsearchable) from the same
// fallback rule.
func TestResolveSubstrateHarnessCwd_FallsBackWhenWorkspaceIsAFile(t *testing.T) {
	regularFile := fakeFileInfo{mode: 0o644}
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/workspace"},
		map[string]fs.FileInfo{"/workspace": regularFile, "/home/scion": usableDirInfo},
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/home/scion" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want fallback %q", got, "/home/scion")
	}
}

// TestResolveSubstrateHarnessCwd_RejectsNonAbsoluteWorkspacePath is the
// "also take" item: a relative SCION_WORKSPACE_PATH is stat'd (and would
// resolve) relative to substrate-serve's own cwd, which won't match a trust
// entry — so it must be rejected and treated the same as "unusable",
// falling through to the scion home.
func TestResolveSubstrateHarnessCwd_RejectsNonAbsoluteWorkspacePath(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "relative/workspace"},
		map[string]fs.FileInfo{"relative/workspace": usableDirInfo, "/home/scion": usableDirInfo},
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/home/scion" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want fallback %q (a relative SCION_WORKSPACE_PATH must be rejected)", got, "/home/scion")
	}
}

// TestResolveSubstrateHarnessCwd_NeverFallsBackToRoot is the explicit guard:
// even when the scion user's own home directory happens to be "/",
// resolveSubstrateHarnessCwd must never return "/" — the exact bug this fix
// exists to avoid, and "/" must never be added to any trust list. The
// workspace candidate is deliberately unusable so the home fallback is
// exercised.
func TestResolveSubstrateHarnessCwd_NeverFallsBackToRoot(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/nope"},
		map[string]fs.FileInfo{"/": usableDirInfo},
	)
	d.lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: "1000", Gid: "1000", HomeDir: "/"}, nil
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if got == "/" {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, must never fall back to \"/\"", got)
	}
	if err == nil {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, <nil>, want an error (the only fallback is \"/\", which must be refused)", got)
	}
}

// TestResolveSubstrateHarnessCwd_CanonicalisesNonCanonicalRootSpellings is
// the canonicalisation Optional from round 2: dirUsableForScion's "never
// '/'" guard used to be a plain string comparison, and parentDirs already
// cleans its own output, so a candidate that is merely a non-canonical
// spelling of "/" reached the guard already reduced to "/" and slipped
// through it. Every spelling below is lexically "/" and must still be
// refused, whether it arrives via SCION_WORKSPACE_PATH...
func TestResolveSubstrateHarnessCwd_CanonicalisesNonCanonicalRootSpellings(t *testing.T) {
	for _, spelling := range []string{"/.", "//", "/tmp/.."} {
		t.Run(spelling, func(t *testing.T) {
			d := fakeSubstrateHarnessCwdDeps(
				map[string]string{"SCION_WORKSPACE_PATH": spelling},
				map[string]fs.FileInfo{},
			)
			d.lookupUser = func(string) (*user.User, error) {
				return &user.User{Uid: "1000", Gid: "1000", HomeDir: "/nope"}, nil
			}
			got, err := resolveSubstrateHarnessCwd(d)
			if got == "/" {
				t.Fatalf("resolveSubstrateHarnessCwd() = %q, must never fall back to \"/\" (non-canonical spelling %q)", got, spelling)
			}
			if err == nil {
				t.Fatalf("resolveSubstrateHarnessCwd() = %q, <nil>, want an error", got)
			}
		})
	}
}

// TestResolveSubstrateHarnessCwd_CanonicalisesHomeDirRootSpelling is the
// same guard exercised through the scion user's HomeDir (e.g. an
// /etc/passwd entry of "/.") instead of SCION_WORKSPACE_PATH.
func TestResolveSubstrateHarnessCwd_CanonicalisesHomeDirRootSpelling(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/nope"},
		map[string]fs.FileInfo{},
	)
	d.lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: "1000", Gid: "1000", HomeDir: "/."}, nil
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if got == "/" {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, must never fall back to \"/\" (HomeDir \"/.\")", got)
	}
	if err == nil {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, <nil>, want an error", got)
	}
}

// TestResolveSubstrateHarnessCwd_ReturnsCanonicalPath proves the other half
// of the canonicalisation fix: a usable candidate that is merely spelled
// non-canonically is returned cleaned, not verbatim, so PWD ends up
// canonical too (see dirUsableForScion's doc comment).
func TestResolveSubstrateHarnessCwd_ReturnsCanonicalPath(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/workspace/."},
		map[string]fs.FileInfo{"/workspace": usableDirInfo},
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/workspace" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want the canonical %q", got, "/workspace")
	}
}

// TestResolveSubstrateHarnessCwd_FallsBackWhenSymlinkTargetParentUnsearchable
// is the symlink-target Optional from round 2: a candidate that is itself a
// symlink can pass the lexical ancestor walk — stat follows the final
// symlink, and a root process's plain stat succeeds regardless of the
// target's own ancestor permissions — while its resolved target sits behind
// a directory the scion uid/gid can't actually search. supervisor.Run's
// chdir happens after the privilege drop, so that's what has to hold, not
// what this (root) process's own stat can reach. Modelled with a fake
// evalSymlinks so the resolved target's parent can be independently stat'd
// as root-owned 0700 — the same "unusable" fixture
// FallsBackWhenWorkspaceNotSearchableByScionUID uses, just reached via the
// resolved path instead of the lexical one.
func TestResolveSubstrateHarnessCwd_FallsBackWhenSymlinkTargetParentUnsearchable(t *testing.T) {
	rootOnly := fakeFileInfo{mode: fs.ModeDir | 0o700, uid: 0, gid: 0}
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/workspace"},
		map[string]fs.FileInfo{
			"/workspace":  usableDirInfo, // the symlink itself: lexically fine
			"/data/ws":    usableDirInfo, // the resolved target itself: fine
			"/data":       rootOnly,      // the target's parent: root-only 0700
			"/home/scion": usableDirInfo,
		},
	)
	d.evalSymlinks = func(p string) (string, error) {
		if p == "/workspace" {
			return "/data/ws", nil
		}
		return p, nil
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/home/scion" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want fallback %q (the symlink target's parent, /data, is root-only 0700)", got, "/home/scion")
	}
}

// TestResolveSubstrateHarnessCwd_SymlinkResolvingToRoot_Rejected is O1's
// sibling in the fake-deps suite (sb-dev-cwd-r4, R2): a candidate whose
// EvalSymlinks target is exactly "/" must be rejected outright, even though
// "/" is always searchable and would otherwise pass the resolved-chain walk
// trivially (no ancestors to check). See
// TestResolveSubstrateHarnessCwd_EffectiveCwd_SymlinkedWorkspaceResolvingToRoot_FallsBack
// below for the real-filesystem version of this same case.
func TestResolveSubstrateHarnessCwd_SymlinkResolvingToRoot_Rejected(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/workspace"},
		map[string]fs.FileInfo{"/workspace": usableDirInfo, "/home/scion": usableDirInfo},
	)
	d.evalSymlinks = func(p string) (string, error) {
		if p == "/workspace" {
			return "/", nil
		}
		return p, nil
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/home/scion" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want fallback %q (a symlink target of \"/\" must be rejected)", got, "/home/scion")
	}
}

// TestResolveSubstrateHarnessCwd_EvalSymlinksError_MakesCandidateUnusable is
// O1: dirUsableForScion's EvalSymlinks error path (a broken link, a cycle,
// ...) must reject the candidate with that error as the reason, never treat
// an error as if it were success. evalSymlinks deliberately returns the
// candidate ITSELF alongside the error (not "" or some other path): that
// makes "real != candidate" false regardless of the error, so a mutant that
// stops checking err (e.g. `real, _ := d.evalSymlinks(candidate)`) sees
// real == candidate, skips the resolved-chain walk entirely, and returns
// "usable" — accepting the workspace instead of falling back. Only actually
// checking err catches that.
func TestResolveSubstrateHarnessCwd_EvalSymlinksError_MakesCandidateUnusable(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/workspace"},
		map[string]fs.FileInfo{"/workspace": usableDirInfo, "/home/scion": usableDirInfo},
	)
	d.evalSymlinks = func(p string) (string, error) {
		if p == "/workspace" {
			return "/workspace", fmt.Errorf("lstat /workspace: too many levels of symbolic links")
		}
		return p, nil
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != "/home/scion" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want fallback %q (an EvalSymlinks error must make the candidate unusable, not be ignored)", got, "/home/scion")
	}
}

// TestResolveSubstrateHarnessCwd_RejectsNonAbsoluteHomeDir is the sb-dev-cwd-r4
// self-audit's own finding: unlike SCION_WORKSPACE_PATH (rejected by
// resolveSubstrateHarnessCwd's own switch before it ever reaches
// dirUsableForScion), scionUser.HomeDir was never checked for being
// absolute. filepath.Clean("") == "." — so an /etc/passwd entry with an
// empty (or otherwise relative) home directory used to resolve to a
// relative candidate that the literal `candidate == "/"` guard doesn't
// catch. "." and the candidate itself are deliberately both stat-able and
// searchable here (leafDirs), so only the new IsAbs guard — not a
// dirsSearchable rejection — is what makes this fail.
func TestResolveSubstrateHarnessCwd_RejectsNonAbsoluteHomeDir(t *testing.T) {
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/nope"},
		map[string]fs.FileInfo{".": usableDirInfo, "relative-home": usableDirInfo},
	)
	d.lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: "1000", Gid: "1000", HomeDir: "relative-home"}, nil
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err == nil {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, <nil>, want an error (a relative HomeDir must never be used)", got)
	}
	if got != "" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want %q alongside the error", got, "")
	}
}

// TestResolveSubstrateHarnessCwd_ScionUserLookupFails_ReturnsError covers
// the "scion" user itself being unresolvable — resolveSubstrateHarnessCwd
// has no uid/gid to gate any candidate on, so it must fail rather than
// guess.
func TestResolveSubstrateHarnessCwd_ScionUserLookupFails_ReturnsError(t *testing.T) {
	d := substrateHarnessCwdDeps{
		getenv: func(string) string { return "" },
		stat:   func(string) (os.FileInfo, error) { return nil, fmt.Errorf("stat: no such file or directory") },
		lookupUser: func(string) (*user.User, error) {
			return nil, fmt.Errorf("user: unknown user scion")
		},
	}
	if _, err := resolveSubstrateHarnessCwd(d); err == nil {
		t.Fatal("resolveSubstrateHarnessCwd() error = nil, want an error when the scion user can't be resolved")
	}
}

// TestResolveSubstrateHarnessCwd_BothUnusable_ReturnsErrorNamingPathsAndUID
// is the substrate-lead amendment's explicit requirement: when neither the
// workspace nor the scion home resolve, the error must name every candidate
// tried (quoted path, plus reason) and the uid it was checked for, and
// nothing else — never falling back to an unset cmd.Dir (which would
// inherit "/").
func TestResolveSubstrateHarnessCwd_BothUnusable_ReturnsErrorNamingPathsAndUID(t *testing.T) {
	rootOnly := fakeFileInfo{mode: fs.ModeDir | 0o700, uid: 0, gid: 0}
	d := fakeSubstrateHarnessCwdDeps(
		map[string]string{"SCION_WORKSPACE_PATH": "/workspace"},
		map[string]fs.FileInfo{"/workspace": rootOnly}, // /home/scion deliberately absent -> "missing"
	)
	got, err := resolveSubstrateHarnessCwd(d)
	if err == nil {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, <nil>, want an error", got)
	}
	if got != "" {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want \"\" alongside the error", got)
	}
	msg := err.Error()
	for _, want := range []string{`"/workspace"`, "not searchable", `"/home/scion"`, "missing", "uid 1000"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
}

// -----------------------------------------------------------------------
// substrateServeReportCwdFailure (the exit-18 Hub report Optional).
// -----------------------------------------------------------------------

// TestSubstrateServeReportCwdFailure_WritesPhaseErrorAndMessage proves the
// no-usable-harness-cwd path reports to local agent-info state the same way
// reportInitFailure's other callers do (see
// TestReportInitFailure_WritesPhaseErrorAndMessage), driven directly
// against a fake lookupUser and a temp directory rather than through the
// real resolveSubstrateHarnessCwd/newSubstrateServeServer wiring — that
// wiring is covered separately by
// TestSubstrateServeBootstrap_NoUsableHarnessCwd_ReportsInitFailureToLocalState.
func TestSubstrateServeReportCwdFailure_WritesPhaseErrorAndMessage(t *testing.T) {
	scrubHubEnv(t)
	tmpHome := t.TempDir()
	cause := fmt.Errorf("substrate: no usable harness working directory for uid 1000: tried %q (missing), %q (missing)", "/workspace", "/home/scion")

	substrateServeReportCwdFailure(substrateHarnessCwdDeps{
		lookupUser: func(string) (*user.User, error) {
			return &user.User{HomeDir: tmpHome}, nil
		},
	}, cause)

	raw, err := os.ReadFile(filepath.Join(tmpHome, "agent-info.json"))
	if err != nil {
		t.Fatalf("expected agent-info.json to be written: %v", err)
	}
	var info struct {
		Phase  string `json:"phase"`
		Detail struct {
			Message string `json:"message"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("unmarshal agent-info.json %q: %v", raw, err)
	}
	if info.Phase != string(state.PhaseError) {
		t.Errorf("agent-info.json phase = %q, want %q", info.Phase, state.PhaseError)
	}
	if info.Detail.Message != cause.Error() {
		t.Errorf("agent-info.json detail.message = %q, want %q", info.Detail.Message, cause.Error())
	}
}

// TestSubstrateServeReportCwdFailure_FallsBackToHOMEWhenScionUserLookupFails
// covers substrateServeReportCwdFailure's own fallback: if the "scion" user
// can't be looked up either, the local report still has to land somewhere,
// so it falls back to $HOME — mirroring resolveAgentHome's own rootless
// fallback (init.go) — rather than losing the report entirely.
func TestSubstrateServeReportCwdFailure_FallsBackToHOMEWhenScionUserLookupFails(t *testing.T) {
	scrubHubEnv(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	cause := fmt.Errorf("substrate: cannot resolve the scion user for the harness working directory: user: unknown user scion")

	substrateServeReportCwdFailure(substrateHarnessCwdDeps{
		lookupUser: func(string) (*user.User, error) { return nil, fmt.Errorf("user: unknown user scion") },
	}, cause)

	if _, err := os.ReadFile(filepath.Join(tmpHome, "agent-info.json")); err != nil {
		t.Fatalf("expected agent-info.json to be written under the $HOME fallback: %v", err)
	}
}

// assertRealChdirSucceeds spawns a real child process with cmd.Dir=dir — the
// same mechanism supervisor.Run uses — and fails the test if the child
// can't actually start there. This is what makes the tests below assert the
// EFFECTIVE cwd, not merely the string resolveSubstrateHarnessCwd returns.
func assertRealChdirSucceeds(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("pwd")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("spawning a real child with Dir=%q failed: %v", dir, err)
	}
	want, evalErr := filepath.EvalSymlinks(dir)
	if evalErr != nil {
		want = dir
	}
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("child's real cwd = %q, want %q", got, want)
	}
}

// assertRealChdirFails is assertRealChdirSucceeds's counterpart, proving a
// rejected candidate really is unusable, not merely disliked by a fake stat.
func assertRealChdirFails(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("pwd")
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Errorf("spawning a real child with Dir=%q unexpectedly succeeded", dir)
	}
}

// TestResolveSubstrateHarnessCwd_EffectiveCwd_WorkspaceReallyEnterable
// proves the normal case's return value is a directory a real child process
// can really chdir into, not just a string a fake stat approved.
func TestResolveSubstrateHarnessCwd_EffectiveCwd_WorkspaceReallyEnterable(t *testing.T) {
	workspace := t.TempDir()
	d := substrateHarnessCwdDeps{
		getenv:       func(k string) string { return map[string]string{"SCION_WORKSPACE_PATH": workspace}[k] },
		stat:         os.Stat,
		evalSymlinks: filepath.EvalSymlinks,
		lookupUser: func(string) (*user.User, error) {
			return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: t.TempDir()}, nil
		},
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != workspace {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, want %q", got, workspace)
	}
	assertRealChdirSucceeds(t, got)
}

// TestResolveSubstrateHarnessCwd_EffectiveCwd_FallsBackWhenWorkspaceReallyUnsearchable
// uses a REAL directory made unsearchable by anyone (mode 0, which blocks
// even its own owner) — the same failure class the review demonstrated with
// a real Dir="/root" as a non-root process — to prove the fallback decision
// tracks what a real chdir can actually do, not just a stat call a
// privileged process could make.
func TestResolveSubstrateHarnessCwd_EffectiveCwd_FallsBackWhenWorkspaceReallyUnsearchable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root bypasses directory permission bits (DAC_OVERRIDE), so this case can't be reproduced as root")
	}
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(workspace, 0o755) }) // let TempDir's own cleanup remove it
	home := t.TempDir()

	d := substrateHarnessCwdDeps{
		getenv:       func(k string) string { return map[string]string{"SCION_WORKSPACE_PATH": workspace}[k] },
		stat:         os.Stat,
		evalSymlinks: filepath.EvalSymlinks,
		lookupUser: func(string) (*user.User, error) {
			return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: home}, nil
		},
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != home {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, want fallback %q", got, home)
	}
	assertRealChdirFails(t, workspace) // proves the rejection was correct
	assertRealChdirSucceeds(t, got)
}

// TestResolveSubstrateHarnessCwd_EffectiveCwd_BothReallyUnusable_Errors is
// the same real-filesystem setup with the scion home ALSO unusable: the
// harness start must fail rather than ever fall back to an inherited "/".
func TestResolveSubstrateHarnessCwd_EffectiveCwd_BothReallyUnusable_Errors(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root bypasses directory permission bits (DAC_OVERRIDE)")
	}
	workspace := t.TempDir()
	home := t.TempDir()
	for _, dir := range []string{workspace, home} {
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		dir := dir
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	d := substrateHarnessCwdDeps{
		getenv:       func(k string) string { return map[string]string{"SCION_WORKSPACE_PATH": workspace}[k] },
		stat:         os.Stat,
		evalSymlinks: filepath.EvalSymlinks,
		lookupUser: func(string) (*user.User, error) {
			return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: home}, nil
		},
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err == nil {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, <nil>, want an error", got)
	}
	if got == "/" {
		t.Fatal("resolveSubstrateHarnessCwd() must never return \"/\"")
	}
	assertRealChdirFails(t, workspace)
	assertRealChdirFails(t, home)
}

// TestResolveSubstrateHarnessCwd_EffectiveCwd_SymlinkedWorkspaceReallyEnterable
// proves the added symlink-target check (see
// FallsBackWhenSymlinkTargetParentUnsearchable) doesn't reject an ordinary,
// fully-accessible symlinked workspace — e.g. a bind mount surfaced as a
// symlink, the common case this must keep working — and that the returned
// (and real-chdir-checked) value is the logical symlink path, not its
// resolved target.
func TestResolveSubstrateHarnessCwd_EffectiveCwd_SymlinkedWorkspaceReallyEnterable(t *testing.T) {
	target := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(target, workspace); err != nil {
		t.Fatal(err)
	}

	d := substrateHarnessCwdDeps{
		getenv:       func(k string) string { return map[string]string{"SCION_WORKSPACE_PATH": workspace}[k] },
		stat:         os.Stat,
		evalSymlinks: filepath.EvalSymlinks,
		lookupUser: func(string) (*user.User, error) {
			return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: t.TempDir()}, nil
		},
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v", err)
	}
	if got != workspace {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, want the logical symlink path %q, not its resolved target", got, workspace)
	}
	assertRealChdirSucceeds(t, got)
}

// TestResolveSubstrateHarnessCwd_EffectiveCwd_SymlinkedWorkspaceResolvingToRoot_FallsBack
// is R2's real-FS regression test (sb-dev-cwd-r4): a real os.Symlink("/",
// link) as SCION_WORKSPACE_PATH must never be chosen, even though a real
// chdir into it would actually succeed — it lands in the real "/", which is
// exactly what the "never /" constraint forbids regardless of whether the
// chdir syscall itself works. assertRealChdirSucceeds is deliberately NOT
// run against workspace here (it would trivially succeed, since "/" is
// always enterable) — the assertion that matters is that resolution never
// picks it, and instead falls back to home.
func TestResolveSubstrateHarnessCwd_EffectiveCwd_SymlinkedWorkspaceResolvingToRoot_FallsBack(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink("/", workspace); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()

	d := substrateHarnessCwdDeps{
		getenv:       func(k string) string { return map[string]string{"SCION_WORKSPACE_PATH": workspace}[k] },
		stat:         os.Stat,
		evalSymlinks: filepath.EvalSymlinks,
		lookupUser: func(string) (*user.User, error) {
			return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: home}, nil
		},
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err != nil {
		t.Fatalf("resolveSubstrateHarnessCwd() error = %v, want fallback to home (workspace resolves to \"/\")", err)
	}
	if got == "/" || got == workspace {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, must reject a symlink resolving to \"/\" and fall back to %q", got, home)
	}
	if got != home {
		t.Errorf("resolveSubstrateHarnessCwd() = %q, want fallback %q", got, home)
	}
	assertRealChdirSucceeds(t, got)
}

// TestResolveSubstrateHarnessCwd_EffectiveCwd_WorkspaceAndHomeBothResolveToRoot_Errors
// is the same real-FS setup with the fallback ALSO a symlink resolving to
// "/": the harness start must fail outright rather than ever return an
// effective cwd of "/".
func TestResolveSubstrateHarnessCwd_EffectiveCwd_WorkspaceAndHomeBothResolveToRoot_Errors(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "root-link-ws")
	if err := os.Symlink("/", workspace); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "root-link-home")
	if err := os.Symlink("/", home); err != nil {
		t.Fatal(err)
	}

	d := substrateHarnessCwdDeps{
		getenv:       func(k string) string { return map[string]string{"SCION_WORKSPACE_PATH": workspace}[k] },
		stat:         os.Stat,
		evalSymlinks: filepath.EvalSymlinks,
		lookupUser: func(string) (*user.User, error) {
			return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: home}, nil
		},
	}
	got, err := resolveSubstrateHarnessCwd(d)
	if err == nil {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, <nil>, want an error (both candidates resolve to \"/\")", got)
	}
	if got == "/" {
		t.Fatal(`resolveSubstrateHarnessCwd() must never return "/"`)
	}
}

// TestResolveSubstrateHarnessCwd_EffectiveCwd_RelativeHomeDir_NeverUsed is
// the real-filesystem version of
// TestResolveSubstrateHarnessCwd_RejectsNonAbsoluteHomeDir (sb-dev-cwd-r4
// self-audit): an empty scionUser.HomeDir cleans to "." (filepath.Clean's
// own documented behaviour for ""), which a real chdir resolves against
// THIS PROCESS's own cwd — standing in here for substrate-serve's real cwd,
// typically "/" for a container's PID 1. The test relocates this process's
// cwd to a throwaway directory (restored via t.Cleanup) precisely so that,
// if the guard regressed, the returned "." would be a real, enterable
// directory — proving the rejection is about the candidate being relative
// at all, not about that directory happening to be unusable.
func TestResolveSubstrateHarnessCwd_EffectiveCwd_RelativeHomeDir_NeverUsed(t *testing.T) {
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwdStandIn := t.TempDir()
	if err := os.Chdir(cwdStandIn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	d := substrateHarnessCwdDeps{
		getenv: func(k string) string {
			return map[string]string{"SCION_WORKSPACE_PATH": filepath.Join(t.TempDir(), "does-not-exist")}[k]
		},
		stat:         os.Stat,
		evalSymlinks: filepath.EvalSymlinks,
		lookupUser: func(string) (*user.User, error) {
			return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: ""}, nil
		},
	}
	got, resolveErr := resolveSubstrateHarnessCwd(d)
	if resolveErr == nil {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, <nil>, want an error (HomeDir \"\" must never resolve to a relative candidate)", got)
	}
	if got != "" && !filepath.IsAbs(got) {
		t.Fatalf("resolveSubstrateHarnessCwd() = %q, must never return a relative path", got)
	}
}

// TestSubstrateServeInitOptions_SetsWorkingDirFromRealEnv drives
// substrateServeInitOptions end to end against the real
// defaultSubstrateHarnessCwdDeps (real os.Getenv/os.Stat, and a
// withScionUserLookup-faked "scion" user — the real lookup is disabled
// under test, see TestMain), proving the wiring — not just
// resolveSubstrateHarnessCwd in isolation — actually threads
// SCION_WORKSPACE_PATH into InitRunOptions.WorkingDir.
func TestSubstrateServeInitOptions_SetsWorkingDirFromRealEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCION_WORKSPACE_PATH", dir)
	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: t.TempDir()}, nil
	})

	opts, err := substrateServeInitOptions(false)
	if err != nil {
		t.Fatalf("substrateServeInitOptions(false) error = %v", err)
	}
	if opts.WorkingDir != dir {
		t.Errorf("substrateServeInitOptions(false).WorkingDir = %q, want %q", opts.WorkingDir, dir)
	}
}

// TestSubstrateServeInitOptions_FallsBackToHomeFromRealEnv is
// TestSubstrateServeInitOptions_SetsWorkingDirFromRealEnv's fallback
// counterpart against the real deps: the scion user's home (via
// withScionUserLookup), not $HOME, is what's used.
func TestSubstrateServeInitOptions_FallsBackToHomeFromRealEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCION_WORKSPACE_PATH", filepath.Join(t.TempDir(), "does-not-exist"))
	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: home}, nil
	})

	opts, err := substrateServeInitOptions(false)
	if err != nil {
		t.Fatalf("substrateServeInitOptions(false) error = %v", err)
	}
	if opts.WorkingDir != home {
		t.Errorf("substrateServeInitOptions(false).WorkingDir = %q, want fallback %q", opts.WorkingDir, home)
	}
}

// TestSubstrateServeBootstrap_ThreadsWorkingDirToInitRunner drives a real
// bootstrap request through newSubstrateServeServer (the same wiring
// runSubstrateServe uses), the way
// TestSubstrateServeBootstrap_SkipRootfsFixupEnvSet_StillRejectsUnfixedRootfs
// already does, and proves two things at once:
//
//  1. the InitRunOptions the InitRunner receives carries WorkingDir resolved
//     from the bootstrap request's own env (req.Env is applied via
//     os.Setenv before the init runner is invoked — see handleBootstrap);
//  2. argv (childArgs) is exactly ["sh", "-c", req.StartCmd] — unchanged
//     from before this fix — proving the fix never parses or rewrites the
//     tmux invocation string that pkg/runtime builds; it only adds a cwd via
//     InitRunOptions, which is what makes cmd.Dir apply uniformly to that
//     whole `sh -c "tmux new-session ..."` process (see
//     resolveSubstrateHarnessCwd's doc comment for why that single
//     mechanism covers both the plain child and the tmux session).
//
// sb-dev-cwd-r2 (R2): the InitRunner runs in handleBootstrap's own goroutine
// (server.go), so the previous version of this test — an unsynchronised
// package-level var written there and polled from the test goroutine — was
// a real data race (`go test -race` failed) and, via handleBootstrap's
// os.Setenv("SCION_WORKSPACE_PATH", ...), leaked that env var into every
// later test in the package. Fixed by handing the result over a buffered
// channel (select with a timeout, no shared mutable state) and by
// t.Setenv-ing the var to "" first so its Cleanup restores whatever this
// process's real ambient value was.
func TestSubstrateServeBootstrap_ThreadsWorkingDirToInitRunner(t *testing.T) {
	origDeps := defaultPrivilegeDropPreconditionDeps
	t.Cleanup(func() { defaultPrivilegeDropPreconditionDeps = origDeps })
	defaultPrivilegeDropPreconditionDeps = fakePrivilegeDropDeps(t)

	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid()), HomeDir: t.TempDir()}, nil
	})

	// Restored by t.Cleanup regardless of what handleBootstrap's os.Setenv
	// does to it below — see the doc comment above.
	t.Setenv("SCION_WORKSPACE_PATH", "")

	workspace := t.TempDir()

	type initCall struct {
		argv []string
		opts InitRunOptions
	}
	calls := make(chan initCall, 1)
	stubRunInit := func(argv []string, opts InitRunOptions) int {
		calls <- initCall{argv: argv, opts: opts}
		return 0
	}

	srv := newSubstrateServeServer(stubRunInit)
	const startCmd = "tmux new-session -d -s scion -n agent /bin/sh -c 'echo hi'"
	rec := doSubstrateServeJSON(t, srv, "POST", "/scion/v1/bootstrap", "any-token", map[string]any{
		"env":           map[string]string{"SCION_WORKSPACE_PATH": workspace},
		"files":         []any{},
		"start_cmd":     startCmd,
		"control_token": "tok",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got initCall
	select {
	case got = <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("init runner was not invoked within 2s")
	}

	wantArgv := []string{"sh", "-c", startCmd}
	if len(got.argv) != len(wantArgv) || got.argv[0] != wantArgv[0] || got.argv[1] != wantArgv[1] || got.argv[2] != wantArgv[2] {
		t.Errorf("init runner argv = %v, want %v (the tmux invocation string must stay untouched)", got.argv, wantArgv)
	}
	if got.opts.WorkingDir != workspace {
		t.Errorf("init runner InitRunOptions.WorkingDir = %q, want %q (resolved from the bootstrap request's own SCION_WORKSPACE_PATH)", got.opts.WorkingDir, workspace)
	}
	if !got.opts.RequirePrivilegeDrop {
		t.Error("init runner InitRunOptions.RequirePrivilegeDrop = false, want true (unaffected by this change)")
	}
}

// TestSubstrateServeBootstrap_NoUsableHarnessCwd_ReportsInitFailureToLocalState
// drives a real bootstrap request through newSubstrateServeServer (the same
// wiring runSubstrateServe uses) all the way to the no-usable-harness-cwd
// (exit-18) path, and proves substrateServeReportCwdFailure is actually
// wired in there — not just available as a helper — the same way
// TestRunInit_StagedSecretsDecodeFailure_ReportsInitFailure proves RunInit's
// own staged-secrets failure path is wired to reportInitFailure.
//
// Both candidates are made unusable by giving the scion identity a uid/gid
// that doesn't match this test process's own (real files/dirs are owned by
// the real test process, so an unrelated uid/gid fails canSearchDir's
// owner/group checks and, since t.TempDir() dirs are typically not
// world-searchable, its "other" check too — no chmod needed). The local
// agent-info.json write itself still succeeds because it runs as this
// (real, owning) process, not as the simulated scion uid — exactly the
// substrate-serve-runs-as-root-before-the-drop asymmetry
// substrateServeReportCwdFailure's own doc comment describes.
func TestSubstrateServeBootstrap_NoUsableHarnessCwd_ReportsInitFailureToLocalState(t *testing.T) {
	scrubHubEnv(t)
	origPD := defaultPrivilegeDropPreconditionDeps
	t.Cleanup(func() { defaultPrivilegeDropPreconditionDeps = origPD })
	defaultPrivilegeDropPreconditionDeps = fakePrivilegeDropDeps(t)

	agentHome := t.TempDir()
	workspace := t.TempDir()
	withScionUserLookup(t, func(string) (*user.User, error) {
		return &user.User{Uid: "1", Gid: "1", HomeDir: agentHome}, nil
	})
	t.Setenv("SCION_WORKSPACE_PATH", workspace)

	var initCalled bool
	stubRunInit := func(argv []string, opts InitRunOptions) int {
		initCalled = true
		return 0
	}

	srv := newSubstrateServeServer(stubRunInit)
	rec := doSubstrateServeJSON(t, srv, "POST", "/scion/v1/bootstrap", "any-token", map[string]any{
		"env":           map[string]string{},
		"files":         []any{},
		"start_cmd":     "true",
		"control_token": "tok",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want 200 (the no-usable-cwd failure surfaces asynchronously, after acceptance — see handleBootstrap); body=%s", rec.Code, rec.Body.String())
	}

	// handleBootstrap runs the init runner in its own goroutine. Poll
	// /healthz for StateInitFailed rather than for agent-info.json's mere
	// existence on disk (sb-dev-cwd-r3): the init-runner goroutine sets
	// s.initFailed under s.mu only AFTER newSubstrateServeServer's wrapper
	// (substrateServeInitOptions's error path) returns, and that wrapper
	// calls substrateServeReportCwdFailure — which does the agent-info.json
	// write — synchronously, in program order, before it returns. So by the
	// time this goroutine observes s.initFailed==true through s.mu, the
	// write has already happened-before it: a real synchronization edge and
	// proof the goroutine ran to completion, neither of which "the file
	// exists" gives (that goroutine could still be inside SetMessage /
	// hub.NewClient when the file first appears).
	var lastState string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		healthz := doSubstrateServeJSON(t, srv, "GET", "/scion/v1/healthz", "", nil)
		var body struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(healthz.Body.Bytes(), &body); err == nil {
			lastState = body.State
			if lastState == "init-failed" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastState != "init-failed" {
		t.Fatalf("healthz state = %q, want %q (after 2s)", lastState, "init-failed")
	}

	raw, readErr := os.ReadFile(filepath.Join(agentHome, "agent-info.json"))
	if readErr != nil {
		t.Fatalf("expected agent-info.json to be written to the scion home after the no-usable-cwd path: %v", readErr)
	}
	if initCalled {
		t.Error("the init runner was invoked despite no usable harness cwd; the harness must never start")
	}
	var info struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("unmarshal agent-info.json %q: %v", raw, err)
	}
	if info.Phase != string(state.PhaseError) {
		t.Errorf("agent-info.json phase = %q, want %q", info.Phase, state.PhaseError)
	}
}

func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForHealthzState(t *testing.T, baseURL, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/scion/v1/healthz")
		if err == nil {
			var body struct {
				State string `json:"state"`
			}
			if decodeErr := json.NewDecoder(resp.Body).Decode(&body); decodeErr == nil {
				last = body.State
			}
			_ = resp.Body.Close()
			if last == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("healthz state = %q, want %q (after %s)", last, want, timeout)
}

func execViaControlServer(t *testing.T, baseURL, controlToken string, argv []string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"argv": argv, "user": "scion", "timeout_s": 5})
	if err != nil {
		t.Fatalf("marshal exec body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/scion/v1/exec", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build exec request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+controlToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("exec request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Stdout string `json:"stdout"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode exec response: %v", err)
	}
	return out.Stdout
}

func waitUntil(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cond()
}

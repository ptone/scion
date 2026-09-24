/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

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

// TestCheckPrivilegeDropFeasible_SkipRootfsFixupEnvSet_StillRejectsUnfixedRootfs
// pins the binding invariant on skipRootfsFixupEnv: it must skip ONLY the
// fixup, never the privilege-drop precondition. With the env set and a
// rootfs that still needs the fixup (here: $HOME not owned by the target
// uid, exactly what fixupRootfsForScion would have corrected),
// checkPrivilegeDropFeasible — substrate-serve's synchronous /bootstrap
// precondition, wired independently of skipRootfsFixupEnv — must still
// reject. This proves the knob can only make bootstrap fail closed sooner,
// never bypass the check.
func TestCheckPrivilegeDropFeasible_SkipRootfsFixupEnvSet_StillRejectsUnfixedRootfs(t *testing.T) {
	t.Setenv(skipRootfsFixupEnv, "1")
	d := fakePrivilegeDropDeps(t)
	// $HOME owned by root, not the target uid — the condition the fixup
	// exists to correct.
	d.statPath = statPathOverride("/home/scion", fakeFileInfo{mode: fs.ModeDir | 0o755, uid: 0, gid: 0})
	if err := checkPrivilegeDropFeasible(d); !errors.Is(err, errPrivilegeDropPrecondition) {
		t.Errorf("checkPrivilegeDropFeasible() = %v, want errPrivilegeDropPrecondition — skipRootfsFixupEnv must never weaken this check", err)
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

/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

	binPath := filepath.Join(t.TempDir(), "sciontool-test")
	buildCmd := exec.Command("go", "build", "-buildvcs=false", "-o", binPath, "../")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Skipf("failed to build sciontool for integration test: %v\n%s", err, out)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", findFreePort(t))
	baseURL := "http://" + addr

	cmd := exec.Command(binPath, "substrate-serve", "--addr", addr)
	cmd.Env = filterHubEnv(os.Environ())
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
		"env":           map[string]string{},
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

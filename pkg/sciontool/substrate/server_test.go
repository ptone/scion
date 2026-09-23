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

package substrate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

func doJSON(t *testing.T, h http.Handler, method, path, bearer string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("unmarshal response %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestHealthz_InitiallyAwaitingBootstrap(t *testing.T) {
	srv := NewServer(WithChownOwner(-1, -1))
	rec := doJSON(t, srv.Handler(), http.MethodGet, "/scion/v1/healthz", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[HealthzResponse](t, rec)
	if got.State != StateAwaitingBootstrap {
		t.Errorf("state = %q, want %q", got.State, StateAwaitingBootstrap)
	}
}

func TestBootstrap_BadNonceRejected(t *testing.T) {
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithNonceVerifier(StaticNonceVerifier{Expected: "correct-nonce"}),
	)

	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "wrong-nonce", BootstrapRequest{
		StartCmd: "true",
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if srv.isBootstrapped() {
		t.Error("a rejected bootstrap must not consume the single-shot slot")
	}
}

func TestBootstrap_MissingBearerRejected(t *testing.T) {
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithNonceVerifier(StaticNonceVerifier{Expected: "correct-nonce"}),
	)

	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "", BootstrapRequest{StartCmd: "true"})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestBootstrap_SingleShot_SecondCallGets409(t *testing.T) {
	var runCount int
	var mu sync.Mutex
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithInitRunner(func(argv []string, forwardTermSignal bool) int {
			mu.Lock()
			runCount++
			mu.Unlock()
			if forwardTermSignal {
				t.Error("bootstrap must call the init runner with forwardTermSignal=false")
			}
			return 0
		}),
	)

	req := BootstrapRequest{StartCmd: "true", ControlToken: "tok-1"}

	first := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "any-token", req)
	if first.Code != http.StatusOK {
		t.Fatalf("first bootstrap status = %d, want 200: %s", first.Code, first.Body.String())
	}

	second := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "any-token", BootstrapRequest{StartCmd: "true", ControlToken: "tok-2"})
	if second.Code != http.StatusConflict {
		t.Fatalf("second bootstrap status = %d, want 409", second.Code)
	}

	// healthz should now report running.
	health := doJSON(t, srv.Handler(), http.MethodGet, "/scion/v1/healthz", "", nil)
	got := decodeJSON[HealthzResponse](t, health)
	if got.State != StateRunning {
		t.Errorf("state after bootstrap = %q, want %q", got.State, StateRunning)
	}

	// Give the async init-runner goroutine a moment to run, then confirm it
	// only ran once (the rejected second request must not re-trigger init).
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := runCount
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if runCount != 1 {
		t.Errorf("init runner invoked %d times, want exactly 1", runCount)
	}
}

func TestBootstrap_WritesFilesWithParentDirsAndEnv(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "nested", "deep", "config.json")

	content := []byte(`{"hello":"world"}`)
	req := BootstrapRequest{
		Env: map[string]string{
			"SCION_SUBSTRATE_TEST_VAR": "set-by-bootstrap",
		},
		Files: []BootstrapFile{
			{Path: filePath, Mode: 0o600, ContentB64: base64.StdEncoding.EncodeToString(content)},
		},
		StartCmd:     "true",
		ControlToken: "tok",
	}

	t.Setenv("SCION_SUBSTRATE_TEST_VAR", "")
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithInitRunner(func(argv []string, forwardTermSignal bool) int { return 0 }),
	)

	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "any-token", req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("expected bootstrap file to exist: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("file content = %q, want %q", got, content)
	}
	if info, err := os.Stat(filePath); err == nil {
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
		}
	}

	if v := os.Getenv("SCION_SUBSTRATE_TEST_VAR"); v != "set-by-bootstrap" {
		t.Errorf("SCION_SUBSTRATE_TEST_VAR = %q, want %q", v, "set-by-bootstrap")
	}
}

// TestWriteBootstrapFile_EnforcesModeOnPreExistingFile asserts that writing
// to a file that already exists at a looser mode (as if baked into the
// image) still ends with exactly the requested mode and the new content —
// not the pre-existing file's mode or content. os.WriteFile's mode argument
// only applies to a newly created file's open(2) call and has no effect on
// a file that already exists; it only truncates and rewrites contents.
func TestWriteBootstrapFile_EnforcesModeOnPreExistingFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "credential.json")

	// Pre-create the file at a looser mode, as if baked into the image.
	if err := os.WriteFile(filePath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("failed to pre-create file: %v", err)
	}

	srv := NewServer(WithChownOwner(-1, -1))
	f := BootstrapFile{
		Path:       filePath,
		Mode:       0o600,
		ContentB64: base64.StdEncoding.EncodeToString([]byte("fresh")),
	}
	if err := srv.writeBootstrapFile(f); err != nil {
		t.Fatalf("writeBootstrapFile: %v", err)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 (pre-existing file's mode must not survive)", info.Mode().Perm())
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "fresh" {
		t.Errorf("content = %q, want %q", got, "fresh")
	}
}

// TestWriteBootstrapFile_SetsModeAndOwnerAtomically asserts that
// writeFileAtomicMode's write-to-temp-then-rename result has exactly the
// requested mode, owner, and content, for both a fresh file and a
// pre-existing one. (The absence of a readable-at-wrong-mode window during
// the write isn't itself observable from a single-threaded test — what's
// verifiable, and what this pins, is that the function never produces a
// file with the wrong mode or owner once it returns.)
func TestWriteBootstrapFile_SetsModeAndOwnerAtomically(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ownership check uses syscall.Stat_t (Linux only)")
	}

	uid := os.Getuid()
	gid := os.Getgid()

	assertModeAndOwner := func(t *testing.T, path string, wantMode os.FileMode, wantContent string) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != wantMode {
			t.Errorf("mode = %v, want %v", info.Mode().Perm(), wantMode)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("could not read platform-specific stat info")
		}
		if int(stat.Uid) != uid {
			t.Errorf("uid = %d, want %d", stat.Uid, uid)
		}
		if int(stat.Gid) != gid {
			t.Errorf("gid = %d, want %d", stat.Gid, gid)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != wantContent {
			t.Errorf("content = %q, want %q", got, wantContent)
		}
	}

	t.Run("fresh file", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "fresh.json")

		srv := NewServer(WithChownOwner(uid, gid))
		f := BootstrapFile{
			Path:       filePath,
			Mode:       0o600,
			ContentB64: base64.StdEncoding.EncodeToString([]byte("fresh-secret")),
		}
		if err := srv.writeBootstrapFile(f); err != nil {
			t.Fatalf("writeBootstrapFile: %v", err)
		}
		assertModeAndOwner(t, filePath, 0o600, "fresh-secret")
	})

	t.Run("pre-existing file at a different mode", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "existing.json")
		if err := os.WriteFile(filePath, []byte("stale"), 0o644); err != nil {
			t.Fatalf("failed to pre-create file: %v", err)
		}

		srv := NewServer(WithChownOwner(uid, gid))
		f := BootstrapFile{
			Path:       filePath,
			Mode:       0o640,
			ContentB64: base64.StdEncoding.EncodeToString([]byte("replaced-secret")),
		}
		if err := srv.writeBootstrapFile(f); err != nil {
			t.Fatalf("writeBootstrapFile: %v", err)
		}
		assertModeAndOwner(t, filePath, 0o640, "replaced-secret")
	})
}

func TestBootstrap_RejectsRelativePath(t *testing.T) {
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithInitRunner(func(argv []string, forwardTermSignal bool) int { return 0 }),
	)
	req := BootstrapRequest{
		Files:        []BootstrapFile{{Path: "relative/path.txt", ContentB64: base64.StdEncoding.EncodeToString([]byte("x"))}},
		StartCmd:     "true",
		ControlToken: "tok",
	}
	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "any-token", req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for an invalid bootstrap file path", rec.Code)
	}
}

func TestExec_RequiresControlToken(t *testing.T) {
	srv := NewServer(WithChownOwner(-1, -1))
	// Not bootstrapped yet: no control token exists, so exec must always 401.
	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/exec", "anything", ExecRequest{Argv: []string{"echo", "hi"}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 before bootstrap", rec.Code)
	}
}

func TestExec_WrongTokenRejected(t *testing.T) {
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithInitRunner(func(argv []string, forwardTermSignal bool) int { return 0 }),
	)
	bootstrap := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "any-token", BootstrapRequest{
		StartCmd: "true", ControlToken: "the-real-token",
	})
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, want 200", bootstrap.Code)
	}

	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/exec", "wrong-token", ExecRequest{Argv: []string{"echo", "hi"}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for wrong control token", rec.Code)
	}
}

func TestExec_SucceedsWithCorrectToken(t *testing.T) {
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithInitRunner(func(argv []string, forwardTermSignal bool) int { return 0 }),
	)
	doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "any-token", BootstrapRequest{
		StartCmd: "true", ControlToken: "the-real-token",
	})

	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/exec", "the-real-token", ExecRequest{
		Argv: []string{"echo", "hello-substrate"},
		User: "scion",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[ExecResponse](t, rec)
	if got.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0 (stderr=%q)", got.ExitCode, got.Stderr)
	}
	if !bytes.Contains([]byte(got.Stdout), []byte("hello-substrate")) {
		t.Errorf("stdout = %q, want it to contain %q", got.Stdout, "hello-substrate")
	}
	if got.Truncated {
		t.Error("truncated = true, want false for small output")
	}
}

func TestExec_RejectsUnknownUser(t *testing.T) {
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithInitRunner(func(argv []string, forwardTermSignal bool) int { return 0 }),
	)
	doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "any-token", BootstrapRequest{
		StartCmd: "true", ControlToken: "tok",
	})

	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/exec", "tok", ExecRequest{
		Argv: []string{"echo", "hi"},
		User: "nobody",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unsupported user", rec.Code)
	}
}

func TestExec_NonZeroExitCodePropagated(t *testing.T) {
	srv := NewServer(
		WithChownOwner(-1, -1),
		WithInitRunner(func(argv []string, forwardTermSignal bool) int { return 0 }),
	)
	doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/bootstrap", "any-token", BootstrapRequest{
		StartCmd: "true", ControlToken: "tok",
	})

	rec := doJSON(t, srv.Handler(), http.MethodPost, "/scion/v1/exec", "tok", ExecRequest{
		Argv: []string{"sh", "-c", "exit 7"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[ExecResponse](t, rec)
	if got.ExitCode != 7 {
		t.Errorf("exit_code = %d, want 7", got.ExitCode)
	}
}

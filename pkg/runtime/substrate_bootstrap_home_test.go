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

package runtime

import (
	"encoding/base64"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// decodeBootstrapFileContent is a small test helper: buildBootstrapFiles and
// homeBootstrapFiles only expose ContentB64 on the wire type; tests need the
// raw bytes back to assert on content.
func decodeBootstrapFileContent(t *testing.T, f bootstrapFile) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(f.ContentB64)
	if err != nil {
		t.Fatalf("content_b64 for %s did not decode: %v", f.Path, err)
	}
	return string(data)
}

func findBootstrapFile(files []bootstrapFile, path string) (bootstrapFile, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return bootstrapFile{}, false
}

// -----------------------------------------------------------------------
// homeBootstrapFiles: walk, precedence via mode/paths, skip-and-count.
// -----------------------------------------------------------------------

func TestHomeBootstrapFiles_NestedFileKeepsRelPathAndMode(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(nested, []byte(`{"a":1}`), 0o640); err != nil {
		t.Fatal(err)
	}

	files, err := homeBootstrapFiles(home, "/home/scion")
	if err != nil {
		t.Fatalf("homeBootstrapFiles: %v", err)
	}

	f, ok := findBootstrapFile(files, "/home/scion/.claude/settings.json")
	if !ok {
		t.Fatalf("expected an entry for /home/scion/.claude/settings.json, got %+v", files)
	}
	if f.Mode != 0o640 {
		t.Errorf("mode = %o, want %o", f.Mode, 0o640)
	}
	if got := decodeBootstrapFileContent(t, f); got != `{"a":1}` {
		t.Errorf("content = %q, want %q", got, `{"a":1}`)
	}
}

func TestHomeBootstrapFiles_SkipsSymlinkFifoSocketAndCountsThem(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("fifo/unix-socket fixtures require Linux (syscall.Mkfifo, AF_UNIX bind)")
	}
	home := t.TempDir()

	// A regular file, which must be shipped.
	regularPath := filepath.Join(home, "regular.txt")
	if err := os.WriteFile(regularPath, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A symlink to that regular file — must be skipped, never followed.
	symlinkPath := filepath.Join(home, "link.txt")
	if err := os.Symlink(regularPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// A fifo — must be skipped (reading one could block).
	fifoPath := filepath.Join(home, "a.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	// A unix domain socket — must be skipped.
	sockPath := filepath.Join(home, "a.sock")
	if err := syscall.Bind(mustSocketFD(t), &syscall.SockaddrUnix{Name: sockPath}); err != nil {
		t.Fatalf("bind unix socket: %v", err)
	}

	files, err := homeBootstrapFiles(home, "/home/scion")
	if err != nil {
		t.Fatalf("homeBootstrapFiles: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("files = %+v, want exactly the one regular file", files)
	}
	if files[0].Path != "/home/scion/regular.txt" {
		t.Errorf("shipped path = %q, want /home/scion/regular.txt", files[0].Path)
	}
	if got := decodeBootstrapFileContent(t, files[0]); got != "kept" {
		t.Errorf("content = %q, want %q", got, "kept")
	}

	// Nothing outside homeDir was read: the symlink's target (also inside
	// homeDir here) is irrelevant, but prove the symlink entry itself never
	// produced a second files entry under any path.
	for _, f := range files {
		if strings.Contains(f.Path, "link.txt") {
			t.Errorf("symlink entry was shipped: %+v", f)
		}
	}
}

// mustSocketFD creates a unix domain socket fd for the bind() call in the
// test above and registers cleanup; syscall.Bind needs a raw fd, not the
// higher-level net.Listen("unix", ...) which also creates the file itself
// (equally valid, but this keeps the fixture explicit about the socket type
// being created).
func mustSocketFD(t *testing.T) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	return fd
}

func TestHomeBootstrapFiles_EmptyHomeDirIsNoop(t *testing.T) {
	files, err := homeBootstrapFiles("", "/home/scion")
	if err != nil {
		t.Fatalf("homeBootstrapFiles(\"\", ...) = %v, want nil error", err)
	}
	if files != nil {
		t.Errorf("homeBootstrapFiles(\"\", ...) = %+v, want nil", files)
	}
}

func TestHomeBootstrapFiles_MissingHomeDirIsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := homeBootstrapFiles(missing, "/home/scion")
	if err == nil {
		t.Fatal("homeBootstrapFiles with a missing HomeDir: expected an error, got nil")
	}
}

// -----------------------------------------------------------------------
// buildBootstrapFiles: precedence, dedup, cap, empty-HomeDir golden.
// -----------------------------------------------------------------------

func TestBuildBootstrapFiles_EmptyHomeDirGolden(t *testing.T) {
	authFilePath := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(authFilePath, []byte("auth-content"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := RunConfig{
		UnixUsername: "scion",
		ResolvedAuth: &api.ResolvedAuth{
			Files: []api.FileMapping{
				{SourcePath: authFilePath, ContainerPath: "~/.creds/auth-file"},
			},
		},
		ResolvedSecrets: []api.ResolvedSecret{
			{Name: "GITHUB_TOKEN", Type: "file", Target: "~/.git-credentials", Value: "secret-content"},
		},
	}

	files, err := buildBootstrapFiles(cfg)
	if err != nil {
		t.Fatalf("buildBootstrapFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v, want exactly 2 (unchanged from pre-home-delivery behavior)", files)
	}

	auth, ok := findBootstrapFile(files, "/home/scion/.creds/auth-file")
	if !ok {
		t.Fatalf("missing auth file entry: %+v", files)
	}
	if auth.Mode != defaultFileMode {
		t.Errorf("auth file mode = %o, want %o", auth.Mode, defaultFileMode)
	}
	if got := decodeBootstrapFileContent(t, auth); got != "auth-content" {
		t.Errorf("auth file content = %q, want %q", got, "auth-content")
	}

	secret, ok := findBootstrapFile(files, "/home/scion/.git-credentials")
	if !ok {
		t.Fatalf("missing secret file entry: %+v", files)
	}
	if got := decodeBootstrapFileContent(t, secret); got != "secret-content" {
		t.Errorf("secret file content = %q, want %q", got, "secret-content")
	}
}

func TestBuildBootstrapFiles_PrecedenceHomeAuthSecret(t *testing.T) {
	home := t.TempDir()
	// Home ships a file at the same relative path as the auth file below —
	// the auth file must win.
	if err := os.WriteFile(filepath.Join(home, "shadowed-by-auth"), []byte("from-home"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Home also ships a file that the secret below will override.
	if err := os.WriteFile(filepath.Join(home, "shadowed-by-secret"), []byte("from-home-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Home ships a file nothing else touches — must survive untouched.
	if err := os.WriteFile(filepath.Join(home, "home-only"), []byte("home-only-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	authFilePath := filepath.Join(t.TempDir(), "auth-src")
	if err := os.WriteFile(authFilePath, []byte("from-auth"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := RunConfig{
		UnixUsername: "scion",
		HomeDir:      home,
		ResolvedAuth: &api.ResolvedAuth{
			Files: []api.FileMapping{
				{SourcePath: authFilePath, ContainerPath: "~/shadowed-by-auth"},
			},
		},
		ResolvedSecrets: []api.ResolvedSecret{
			{Name: "S", Type: "file", Target: "~/shadowed-by-secret", Value: "from-secret"},
		},
	}

	files, err := buildBootstrapFiles(cfg)
	if err != nil {
		t.Fatalf("buildBootstrapFiles: %v", err)
	}

	// Exactly one entry per path — the dedup contract.
	seen := map[string]int{}
	for _, f := range files {
		seen[f.Path]++
	}
	for path, n := range seen {
		if n != 1 {
			t.Errorf("path %s appears %d times, want exactly 1", path, n)
		}
	}

	byAuth, ok := findBootstrapFile(files, "/home/scion/shadowed-by-auth")
	if !ok {
		t.Fatal("missing /home/scion/shadowed-by-auth")
	}
	if got := decodeBootstrapFileContent(t, byAuth); got != "from-auth" {
		t.Errorf("shadowed-by-auth content = %q, want %q (auth must win over home)", got, "from-auth")
	}

	bySecret, ok := findBootstrapFile(files, "/home/scion/shadowed-by-secret")
	if !ok {
		t.Fatal("missing /home/scion/shadowed-by-secret")
	}
	if got := decodeBootstrapFileContent(t, bySecret); got != "from-secret" {
		t.Errorf("shadowed-by-secret content = %q, want %q (secret must win over home)", got, "from-secret")
	}

	homeOnly, ok := findBootstrapFile(files, "/home/scion/home-only")
	if !ok {
		t.Fatal("missing /home/scion/home-only")
	}
	if got := decodeBootstrapFileContent(t, homeOnly); got != "home-only-content" {
		t.Errorf("home-only content = %q, want %q", got, "home-only-content")
	}
}

func TestBuildBootstrapFiles_CapAtCapPasses(t *testing.T) {
	home := t.TempDir()
	data := make([]byte, maxBootstrapFilesTotalBytes)
	if err := os.WriteFile(filepath.Join(home, "big"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := RunConfig{UnixUsername: "scion", HomeDir: home}
	files, err := buildBootstrapFiles(cfg)
	if err != nil {
		t.Fatalf("buildBootstrapFiles at exactly the cap: unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d entries, want 1", len(files))
	}
}

func TestBuildBootstrapFiles_CapPlusOneFails(t *testing.T) {
	home := t.TempDir()
	data := make([]byte, maxBootstrapFilesTotalBytes+1)
	if err := os.WriteFile(filepath.Join(home, "big"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := RunConfig{UnixUsername: "scion", HomeDir: home}
	_, err := buildBootstrapFiles(cfg)
	if err == nil {
		t.Fatal("buildBootstrapFiles at cap+1: expected an error, got nil")
	}
	// Error hygiene: names the cap and the total, never a path.
	if strings.Contains(err.Error(), home) {
		t.Errorf("error leaked the home directory path: %v", err)
	}
	if !strings.Contains(err.Error(), "16777217") {
		t.Errorf("error = %v, want it to name the total size (16777217)", err)
	}
	if !strings.Contains(err.Error(), "16777216") {
		t.Errorf("error = %v, want it to name the cap (16777216)", err)
	}
}

// TestBuildBootstrapFiles_ErrorHygiene_SentinelSecretInHomeFile proves that a
// home file's own content — settings.json can carry env, which is
// secret-grade per the design — never appears in an error string, matching
// the existing redaction contract for auth/secret files.
func TestBuildBootstrapFiles_ErrorHygiene_SentinelSecretInHomeFile(t *testing.T) {
	const sentinel = "FAKE-SENTINEL-home-file-secret-content-not-a-real-credential"
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	// Force an over-cap error by also shipping one huge file, so
	// buildBootstrapFiles' error path actually runs with the sentinel file
	// present in the same batch.
	big := make([]byte, maxBootstrapFilesTotalBytes+1)
	if err := os.WriteFile(filepath.Join(home, "big"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := buildBootstrapFiles(RunConfig{UnixUsername: "scion", HomeDir: home})
	if err == nil {
		t.Fatal("expected a cap error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Errorf("error leaked home file content: %v", err)
	}
}

func TestDedupeBootstrapFilesByPath_LaterWinsStablePosition(t *testing.T) {
	a := []bootstrapFile{{Path: "/x", ContentB64: "a"}, {Path: "/y", ContentB64: "y1"}}
	b := []bootstrapFile{{Path: "/z", ContentB64: "z"}}
	c := []bootstrapFile{{Path: "/x", ContentB64: "c"}} // overrides a's /x

	got := dedupeBootstrapFilesByPath(a, b, c)

	var paths []string
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	sort.Strings(paths) // order isn't the point here; content-per-path is
	want := []string{"/x", "/y", "/z"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths = %v, want %v", paths, want)
		}
	}

	x, ok := findBootstrapFile(got, "/x")
	if !ok || x.ContentB64 != "c" {
		t.Errorf("/x = %+v, want content_b64 %q (later entry must win)", x, "c")
	}
}

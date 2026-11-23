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

package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
)

// memStore implements PartSink and PartSource in memory for tests.
type memStore struct {
	parts     map[int][]byte
	putCalls  atomic.Int64
	putErrAt  int // >= 0 to fail Put at this part index
	getCounts map[int]int
}

func newMemStore() *memStore {
	return &memStore{
		parts:     make(map[int][]byte),
		putErrAt:  -1,
		getCounts: make(map[int]int),
	}
}

func (m *memStore) Put(_ context.Context, index int, part io.ReadSeeker, size int64, hash string) error {
	m.putCalls.Add(1)
	if m.putErrAt >= 0 && index == m.putErrAt {
		return errors.New("injected sink failure")
	}
	b, err := io.ReadAll(part)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return errors.New("sink size mismatch")
	}
	if got := transfer.HashBytes(b); got != hash {
		return errors.New("sink hash mismatch")
	}
	m.parts[index] = b
	return nil
}

func (m *memStore) Get(_ context.Context, p Part) (io.ReadCloser, error) {
	m.getCounts[p.Index]++
	b, ok := m.parts[p.Index]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected scratch dir %s to be empty, found %v", dir, names)
	}
}

// buildRawTarGz builds a single-part RootManifest and memStore from custom tar
// entries so T3/T4 can craft malicious or edge-case archives.
func buildRawTarGz(t *testing.T, writeFn func(tw *tar.Writer)) (RootManifest, *memStore) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	writeFn(tw)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	raw := buf.Bytes()
	store := newMemStore()
	store.parts[0] = raw
	rm := RootManifest{
		Parts: []Part{{Index: 0, Size: int64(len(raw)), Hash: transfer.HashBytes(raw)}},
	}
	return rm, store
}

// TestT1_RoundTrip_EmptyRoot verifies an empty root produces a valid bundle and extracts cleanly.
func TestT1_RoundTrip_EmptyRoot(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	scratch := t.TempDir()

	srcRoot, err := os.OpenRoot(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srcRoot.Close() }()

	dstRoot, err := os.OpenRoot(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dstRoot.Close() }()

	store := newMemStore()
	lim := DefaultWorkspaceLimits()
	rm, err := Write(ctx, srcRoot, scratch, WorkspaceExcluder(), lim, store)
	if err != nil {
		t.Fatalf("Write empty root: %v", err)
	}
	if rm.Entries != 0 || rm.Bytes != 0 || len(rm.Parts) != 1 {
		t.Fatalf("unexpected empty RootManifest: %+v", rm)
	}
	assertDirEmpty(t, scratch)

	if err := Extract(ctx, dstRoot, scratch, rm, lim, store); err != nil {
		t.Fatalf("Extract empty root: %v", err)
	}
	assertDirEmpty(t, dstDir)
	assertDirEmpty(t, scratch)
}

// TestT1_RoundTrip_GitRepo verifies that a real git repo (with commit,
// uncommitted change, untracked file, empty dirs) round-trips intact and
// WorkspaceExcluder keeps .git while dropping root .scion.
func TestT1_RoundTrip_GitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	scratch := t.TempDir()

	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return string(out)
	}

	runGit(srcDir, "init")
	if err := os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(srcDir, "add", "README.md")
	runGit(srcDir, "commit", "-m", "initial commit")

	// Uncommitted modification + untracked file + empty dir + root .scion (excluded) + nested .scion (kept).
	if err := os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("hello modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "empty-dir", "nested-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, ".scion"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, ".scion", "settings.yaml"), []byte("secret: no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "sub", ".scion"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", ".scion", "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srcRoot, err := os.OpenRoot(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srcRoot.Close() }()

	dstRoot, err := os.OpenRoot(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dstRoot.Close() }()

	lim := DefaultWorkspaceLimits()
	est, err := Estimate(ctx, srcRoot, WorkspaceExcluder(), lim)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}

	store := newMemStore()
	rm, err := Write(ctx, srcRoot, scratch, WorkspaceExcluder(), lim, store)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if est.Entries != rm.Entries || est.Bytes != rm.Bytes || est.Skipped != rm.Skipped {
		t.Fatalf("Estimate %+v != Write %+v", est, rm)
	}
	if rm.Skipped.Excluded != 1 {
		t.Fatalf("expected 1 excluded entry (root .scion dir), got %+v", rm.Skipped)
	}

	if err := Extract(ctx, dstRoot, scratch, rm, lim, store); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	assertDirEmpty(t, scratch)

	// Root .scion must be absent, nested sub/.scion/keep.txt must be present.
	if _, err := os.Stat(filepath.Join(dstDir, ".scion")); !os.IsNotExist(err) {
		t.Fatalf("expected root .scion to be excluded, stat err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dstDir, "sub", ".scion", "keep.txt")); err != nil || string(got) != "keep\n" {
		t.Fatalf("nested .scion/keep.txt = %q, err=%v", got, err)
	}
	if fi, err := os.Stat(filepath.Join(dstDir, "empty-dir", "nested-empty")); err != nil || !fi.IsDir() {
		t.Fatalf("empty dir missing: err=%v", err)
	}

	// Verify git history and working tree status survived intact.
	srcLog := runGit(srcDir, "log", "--oneline")
	dstLog := runGit(dstDir, "log", "--oneline")
	if srcLog != dstLog {
		t.Fatalf("git log mismatch:\nsrc: %s\ndst: %s", srcLog, dstLog)
	}
	srcStatus := runGit(srcDir, "status", "--porcelain")
	dstStatus := runGit(dstDir, "status", "--porcelain")
	wantStatus := strings.ReplaceAll(srcStatus, "?? .scion/\n", "")
	if dstStatus != wantStatus {
		t.Fatalf("git status mismatch:\nwant: %s\ndst: %s", wantStatus, dstStatus)
	}
}

// TestT2_HomeExclusions verifies every home exclusion class from design §3.4 items 1-4.
func TestT2_HomeExclusions(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	scratch := t.TempDir()

	excludedFiles := []string{
		// Item 1 (caller-supplied per-config resolved secrets/auth)
		".config/my-secret.json",
		".claude/.credentials.json",
		// Item 2 (baseline Scion secrets/tokens/reset marker/temp names)
		".scion/scion-token",
		".scion/scion-token.tmp",
		".scion/secrets.json",
		".scion/harness/secrets/API_KEY",
		".scion/harness/outputs/out.env",
		".scion-home-reset-id",
		".scion/.scion-token.tmp-0123456789abcdef",
		".scion/.agent-info.json.tmp-42-1730000000",
		// Item 3 (D6 identity-bound credentials)
		".config/gcloud/credentials.db",
		".config/gcloud/access_tokens.db",
		".config/gcloud/application_default_credentials.json",
		".config/gcloud/legacy_credentials/user@example.com/adc.json",
		".docker/config.json",
		".kube/config",
		// Item 4 (D6 caches)
		".cache/pip/wheel.whl",
		".npm/_cacache/index-v5/aa",
		"go/pkg/mod/cache/download/foo.zip",
	}
	keptFiles := []string{
		".bashrc",
		".bash_history",
		"notes.txt",
		".config/gcloud/configurations/config_default",
		".scion/history.log",
		"notes.tmp",
	}

	for _, rel := range append(append([]string{}, excludedFiles...), keptFiles...) {
		full := filepath.Join(srcDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("data:"+rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	srcRoot, err := os.OpenRoot(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srcRoot.Close() }()
	dstRoot, err := os.OpenRoot(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dstRoot.Close() }()

	ex := HomeExcluder([]string{".config/my-secret.json", ".claude/.credentials.json"})
	store := newMemStore()
	lim := DefaultHomeLimits()

	rm, err := Write(ctx, srcRoot, scratch, ex, lim, store)
	if err != nil {
		t.Fatalf("Write home: %v", err)
	}
	if rm.Skipped.Excluded == 0 {
		t.Fatalf("expected non-zero excluded count")
	}

	if err := Extract(ctx, dstRoot, scratch, rm, lim, store); err != nil {
		t.Fatalf("Extract home: %v", err)
	}

	for _, rel := range excludedFiles {
		if _, err := os.Stat(filepath.Join(dstDir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("excluded home file %q was NOT excluded (stat err=%v)", rel, err)
		}
	}
	for _, rel := range keptFiles {
		got, err := os.ReadFile(filepath.Join(dstDir, filepath.FromSlash(rel)))
		if err != nil || string(got) != "data:"+rel {
			t.Errorf("kept home file %q = %q, err=%v", rel, got, err)
		}
	}
}

// TestT3_ReaderRefusals verifies every malicious/corrupt input class in design §9 T3.
func TestT3_ReaderRefusals(t *testing.T) {
	ctx := context.Background()
	lim := DefaultWorkspaceLimits()

	t.Run("unknown manifest version", func(t *testing.T) {
		for _, ver := range []string{"", "bundle/2", "1.0"} {
			m := Manifest{
				Version:         ver,
				ReincarnationID: "rid-1",
				Roots: map[string]RootManifest{
					RootWorkspace: {Parts: []Part{{Index: 0, Size: 10, Hash: transfer.HashBytes([]byte("0123456789"))}}},
				},
			}
			if err := m.Validate(); !errors.Is(err, ErrUnsupportedVersion) {
				t.Errorf("version %q: Validate err = %v, want ErrUnsupportedVersion", ver, err)
			}
			dstRoot, _ := os.OpenRoot(t.TempDir())
			defer func() { _ = dstRoot.Close() }()
			if err := ExtractFromManifest(ctx, dstRoot, t.TempDir(), m, RootWorkspace, lim, newMemStore()); !errors.Is(err, ErrUnsupportedVersion) {
				t.Errorf("version %q: ExtractFromManifest err = %v, want ErrUnsupportedVersion", ver, err)
			}
		}
		if _, err := ParseManifest([]byte(`{"version":"bundle/99","reincarnationId":"r","roots":{}}`)); !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("ParseManifest unknown version err = %v, want ErrUnsupportedVersion", err)
		}
	})

	t.Run("hash mismatch refused before decompression", func(t *testing.T) {
		// Payload is NOT valid gzip at all. If gunzip ran before hash check,
		// gzip.NewReader would fail with gzip.ErrHeader instead of ErrHashMismatch.
		notGzip := []byte("definitely not gzip bytes at all")
		store := newMemStore()
		store.parts[0] = notGzip
		rm := RootManifest{
			Parts: []Part{{
				Index: 0,
				Size:  int64(len(notGzip)),
				Hash:  transfer.HashBytes([]byte("different content")),
			}},
		}
		dstRoot, _ := os.OpenRoot(t.TempDir())
		defer func() { _ = dstRoot.Close() }()
		scratch := t.TempDir()
		err := Extract(ctx, dstRoot, scratch, rm, lim, store)
		if !errors.Is(err, ErrHashMismatch) {
			t.Fatalf("got err = %v, want ErrHashMismatch", err)
		}
		assertDirEmpty(t, scratch)
	})

	t.Run("truncated part size mismatch", func(t *testing.T) {
		rm, store := buildRawTarGz(t, func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "a.txt", Size: 5, Mode: 0o644})
			_, _ = tw.Write([]byte("hello"))
		})
		// Truncate stored bytes without updating manifest.
		store.parts[0] = store.parts[0][:len(store.parts[0])/2]
		dstRoot, _ := os.OpenRoot(t.TempDir())
		defer func() { _ = dstRoot.Close() }()
		scratch := t.TempDir()
		err := Extract(ctx, dstRoot, scratch, rm, lim, store)
		if !errors.Is(err, ErrPartSizeMismatch) {
			t.Fatalf("got err = %v, want ErrPartSizeMismatch", err)
		}
		assertDirEmpty(t, scratch)
	})

	t.Run("truncated gzip stream with matching forged hash", func(t *testing.T) {
		rm, store := buildRawTarGz(t, func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "a.txt", Size: 5, Mode: 0o644})
			_, _ = tw.Write([]byte("hello"))
		})
		trunc := store.parts[0][:len(store.parts[0])-4]
		store.parts[0] = trunc
		rm.Parts[0].Size = int64(len(trunc))
		rm.Parts[0].Hash = transfer.HashBytes(trunc)

		dstRoot, _ := os.OpenRoot(t.TempDir())
		defer func() { _ = dstRoot.Close() }()
		scratch := t.TempDir()
		err := Extract(ctx, dstRoot, scratch, rm, lim, store)
		if err == nil {
			t.Fatalf("expected error on truncated gzip stream, got nil")
		}
		assertDirEmpty(t, scratch)
	})

	cases := []struct {
		name    string
		wantErr error
		write   func(tw *tar.Writer)
	}{
		{
			name: "parent traversal ../x",
			write: func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "../x", Size: 1, Mode: 0o644})
				_, _ = tw.Write([]byte("x"))
			},
		},
		{
			name: "absolute path /abs",
			write: func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "/abs", Size: 1, Mode: 0o644})
				_, _ = tw.Write([]byte("x"))
			},
		},
		{
			name:    "duplicate entry",
			wantErr: ErrDuplicateEntry,
			write: func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "dup.txt", Size: 1, Mode: 0o644})
				_, _ = tw.Write([]byte("a"))
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "dup.txt", Size: 1, Mode: 0o644})
				_, _ = tw.Write([]byte("b"))
			},
		},
		{
			name:    "escaping relative symlink",
			wantErr: ErrEscapingSymlink,
			write: func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "a/escape", Linkname: "../../etc/passwd", Mode: 0o777})
			},
		},
		{
			name:    "absolute symlink",
			wantErr: ErrEscapingSymlink,
			write: func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "abslink", Linkname: "/etc/passwd", Mode: 0o777})
			},
		},
		{
			name:    "hardlink refused",
			wantErr: ErrUnsupportedEntryType,
			write: func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "orig.txt", Size: 1, Mode: 0o644})
				_, _ = tw.Write([]byte("a"))
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeLink, Name: "hard.txt", Linkname: "orig.txt"})
			},
		},
		{
			name:    "char device refused",
			wantErr: ErrUnsupportedEntryType,
			write: func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeChar, Name: "devnull", Mode: 0o666, Devmajor: 1, Devminor: 3})
			},
		},
		{
			name:    "fifo refused",
			wantErr: ErrUnsupportedEntryType,
			write: func(tw *tar.Writer) {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeFifo, Name: "pipe", Mode: 0o644})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rm, store := buildRawTarGz(t, tc.write)
			dstRoot, _ := os.OpenRoot(t.TempDir())
			defer func() { _ = dstRoot.Close() }()
			scratch := t.TempDir()
			err := Extract(ctx, dstRoot, scratch, rm, lim, store)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			assertDirEmpty(t, scratch)
		})
	}
}

// TestT4_Limits_WriterAndReader verifies all four bounds on both Write (with
// fast abort and no further sink calls) and Extract (including forged-manifest
// gzip bomb).
func TestT4_Limits_WriterAndReader(t *testing.T) {
	ctx := context.Background()

	t.Run("writer bytes limit fast abort", func(t *testing.T) {
		srcDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(srcDir, "big.bin"), bytes.Repeat([]byte("a"), 200), 0o644); err != nil {
			t.Fatal(err)
		}
		srcRoot, _ := os.OpenRoot(srcDir)
		defer func() { _ = srcRoot.Close() }()
		scratch := t.TempDir()
		store := newMemStore()
		lim := Limits{MaxBytes: 100, MaxEntries: 10, MaxDepth: 10, MaxPathLen: 100, PartSize: 64}

		_, err := Write(ctx, srcRoot, scratch, nil, lim, store)
		var le *ErrLimitExceeded
		if !errors.As(err, &le) || le.Field != "bytes" {
			t.Fatalf("got err = %v, want ErrLimitExceeded(bytes)", err)
		}
		if store.putCalls.Load() != 0 {
			t.Fatalf("expected 0 sink.Put calls on fast abort, got %d", store.putCalls.Load())
		}
		assertDirEmpty(t, scratch)
	})

	t.Run("writer entries limit", func(t *testing.T) {
		srcDir := t.TempDir()
		for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
			if err := os.WriteFile(filepath.Join(srcDir, n), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		srcRoot, _ := os.OpenRoot(srcDir)
		defer func() { _ = srcRoot.Close() }()
		scratch := t.TempDir()
		store := newMemStore()
		lim := Limits{MaxBytes: 1000, MaxEntries: 2, MaxDepth: 10, MaxPathLen: 100, PartSize: 64 << 10}

		_, err := Write(ctx, srcRoot, scratch, nil, lim, store)
		var le *ErrLimitExceeded
		if !errors.As(err, &le) || le.Field != "entries" {
			t.Fatalf("got err = %v, want ErrLimitExceeded(entries)", err)
		}
		if store.putCalls.Load() != 0 {
			t.Fatalf("expected 0 sink.Put calls on fast abort, got %d", store.putCalls.Load())
		}
		assertDirEmpty(t, scratch)
	})

	t.Run("writer depth limit", func(t *testing.T) {
		srcDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(srcDir, "a", "b", "c"), 0o755); err != nil {
			t.Fatal(err)
		}
		srcRoot, _ := os.OpenRoot(srcDir)
		defer func() { _ = srcRoot.Close() }()
		lim := Limits{MaxBytes: 1000, MaxEntries: 10, MaxDepth: 2, MaxPathLen: 100, PartSize: 64 << 10}

		_, err := Write(ctx, srcRoot, t.TempDir(), nil, lim, newMemStore())
		var le *ErrLimitExceeded
		if !errors.As(err, &le) || le.Field != "depth" {
			t.Fatalf("got err = %v, want ErrLimitExceeded(depth)", err)
		}
	})

	t.Run("writer path_len limit", func(t *testing.T) {
		srcDir := t.TempDir()
		longName := strings.Repeat("x", 50)
		if err := os.WriteFile(filepath.Join(srcDir, longName), []byte("hi"), 0o644); err != nil {
			t.Fatal(err)
		}
		srcRoot, _ := os.OpenRoot(srcDir)
		defer func() { _ = srcRoot.Close() }()
		lim := Limits{MaxBytes: 1000, MaxEntries: 10, MaxDepth: 10, MaxPathLen: 20, PartSize: 64 << 10}

		_, err := Write(ctx, srcRoot, t.TempDir(), nil, lim, newMemStore())
		var le *ErrLimitExceeded
		if !errors.As(err, &le) || le.Field != "path_len" {
			t.Fatalf("got err = %v, want ErrLimitExceeded(path_len)", err)
		}
	})

	t.Run("reader forged-manifest gzip bomb", func(t *testing.T) {
		// 4 MiB of zeros compresses to ~4 KiB, with a forged RootManifest claiming 10 bytes.
		const bombSize = 4 << 20
		rm, store := buildRawTarGz(t, func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "bomb.bin", Size: bombSize, Mode: 0o644})
			zeros := make([]byte, 32*1024)
			for rem := bombSize; rem > 0; {
				n := min(len(zeros), rem)
				_, _ = tw.Write(zeros[:n])
				rem -= n
			}
		})
		// Forge tiny manifest counts.
		rm.Entries = 1
		rm.Bytes = 10

		dstRoot, _ := os.OpenRoot(t.TempDir())
		defer func() { _ = dstRoot.Close() }()
		scratch := t.TempDir()
		lim := Limits{MaxBytes: 64 << 10, MaxEntries: 100, MaxDepth: 10, MaxPathLen: 256, PartSize: 64 << 10}

		err := Extract(ctx, dstRoot, scratch, rm, lim, store)
		var le *ErrLimitExceeded
		if !errors.As(err, &le) || le.Field != "bytes" {
			t.Fatalf("got err = %v, want ErrLimitExceeded(bytes)", err)
		}
		assertDirEmpty(t, scratch)
	})

	t.Run("reader entries depth path_len limits", func(t *testing.T) {
		rmEntries, storeEntries := buildRawTarGz(t, func(tw *tar.Writer) {
			for _, name := range []string{"a", "b", "c"} {
				_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: 1, Mode: 0o644})
				_, _ = tw.Write([]byte("1"))
			}
		})
		dstRoot, _ := os.OpenRoot(t.TempDir())
		defer func() { _ = dstRoot.Close() }()
		err := Extract(ctx, dstRoot, t.TempDir(), rmEntries, Limits{MaxBytes: 1000, MaxEntries: 2, MaxDepth: 10, MaxPathLen: 100, PartSize: 64 << 10}, storeEntries)
		var le *ErrLimitExceeded
		if !errors.As(err, &le) || le.Field != "entries" {
			t.Fatalf("reader entries err = %v, want ErrLimitExceeded(entries)", err)
		}

		rmDepth, storeDepth := buildRawTarGz(t, func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "a/b/c.txt", Size: 1, Mode: 0o644})
			_, _ = tw.Write([]byte("1"))
		})
		dstRoot2, _ := os.OpenRoot(t.TempDir())
		defer func() { _ = dstRoot2.Close() }()
		err = Extract(ctx, dstRoot2, t.TempDir(), rmDepth, Limits{MaxBytes: 1000, MaxEntries: 10, MaxDepth: 2, MaxPathLen: 100, PartSize: 64 << 10}, storeDepth)
		if !errors.As(err, &le) || le.Field != "depth" {
			t.Fatalf("reader depth err = %v, want ErrLimitExceeded(depth)", err)
		}

		rmLen, storeLen := buildRawTarGz(t, func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: strings.Repeat("p", 30), Size: 1, Mode: 0o644})
			_, _ = tw.Write([]byte("1"))
		})
		dstRoot3, _ := os.OpenRoot(t.TempDir())
		defer func() { _ = dstRoot3.Close() }()
		err = Extract(ctx, dstRoot3, t.TempDir(), rmLen, Limits{MaxBytes: 1000, MaxEntries: 10, MaxDepth: 10, MaxPathLen: 10, PartSize: 64 << 10}, storeLen)
		if !errors.As(err, &le) || le.Field != "path_len" {
			t.Fatalf("reader path_len err = %v, want ErrLimitExceeded(path_len)", err)
		}
	})
}

// TestT5_PartSplittingAndCleanup verifies exact part boundaries, streaming of a
// large file, and temp-file cleanup on both success and error.
func TestT5_PartSplittingAndCleanup(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	scratch := t.TempDir()

	// Write incompressible pseudo-random-ish bytes (deterministic LCG) so the
	// gzip output spans multiple parts at PartSize = 1024.
	payload := make([]byte, 8192)
	var x uint32 = 0x12345678
	for i := range payload {
		x = x*1664525 + 1013904223
		payload[i] = byte(x >> 24)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "random.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	// Also write a larger 2 MiB sparse/zero file to exercise streaming.
	if err := os.WriteFile(filepath.Join(srcDir, "sparse.bin"), make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	srcRoot, _ := os.OpenRoot(srcDir)
	defer func() { _ = srcRoot.Close() }()
	dstRoot, _ := os.OpenRoot(dstDir)
	defer func() { _ = dstRoot.Close() }()

	const partSize int64 = 1024
	lim := Limits{
		MaxBytes:   4 << 20,
		MaxEntries: 100,
		MaxDepth:   16,
		MaxPathLen: 256,
		PartSize:   partSize,
	}
	store := newMemStore()

	rm, err := Write(ctx, srcRoot, scratch, nil, lim, store)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	assertDirEmpty(t, scratch)

	if len(rm.Parts) < 4 {
		t.Fatalf("expected >= 4 parts at PartSize=%d, got %d", partSize, len(rm.Parts))
	}
	for i, p := range rm.Parts {
		if p.Index != i {
			t.Errorf("part %d has Index %d", i, p.Index)
		}
		if i < len(rm.Parts)-1 && p.Size != partSize {
			t.Errorf("non-final part %d has size %d, want exact %d", i, p.Size, partSize)
		}
		if i == len(rm.Parts)-1 && (p.Size <= 0 || p.Size > partSize) {
			t.Errorf("final part %d has invalid size %d", i, p.Size)
		}
	}

	if err := Extract(ctx, dstRoot, scratch, rm, lim, store); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	assertDirEmpty(t, scratch)

	gotRand, err := os.ReadFile(filepath.Join(dstDir, "random.bin"))
	if err != nil || !bytes.Equal(gotRand, payload) {
		t.Fatalf("random.bin round-trip mismatch (err=%v)", err)
	}
	gotSparse, err := os.ReadFile(filepath.Join(dstDir, "sparse.bin"))
	if err != nil || len(gotSparse) != 2<<20 {
		t.Fatalf("sparse.bin round-trip mismatch (len=%d, err=%v)", len(gotSparse), err)
	}

	// Verify temp-file cleanup when sink.Put fails mid-stream.
	failStore := newMemStore()
	failStore.putErrAt = 2
	if _, err := Write(ctx, srcRoot, scratch, nil, lim, failStore); err == nil {
		t.Fatalf("expected Write error when sink fails at part 2")
	}
	assertDirEmpty(t, scratch)
}

func TestCanonicalManifestAndMaxParts(t *testing.T) {
	m := Manifest{
		Version:         ManifestVersion,
		ReincarnationID: "rid-123",
		Roots: map[string]RootManifest{
			RootWorkspace: {
				Parts:   []Part{{Index: 0, Size: 12, Hash: transfer.HashBytes([]byte("0123456789ab"))}},
				Entries: 1,
				Bytes:   5,
			},
		},
	}
	raw, hash, err := CanonicalManifest(m)
	if err != nil {
		t.Fatalf("CanonicalManifest: %v", err)
	}
	if !transfer.IsContentHash(hash) {
		t.Fatalf("invalid manifest hash %q", hash)
	}
	parsed, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if parsed.Version != ManifestVersion || parsed.ReincarnationID != "rid-123" {
		t.Fatalf("unexpected parsed manifest: %+v", parsed)
	}

	lim := DefaultWorkspaceLimits()
	// 4 GiB / 64 MiB = 64; + 8 = 72.
	if got := lim.MaxParts(); got != 72 {
		t.Fatalf("DefaultWorkspaceLimits.MaxParts() = %d, want 72", got)
	}
}

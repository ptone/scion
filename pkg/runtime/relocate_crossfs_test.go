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

// This file contains diagnostic tests for the planned relocateToScion function.
//
// The function does not yet exist in the codebase (cloudrun_sandbox_runtime.go
// is planned). These tests reproduce the DESCRIBED algorithm to verify whether
// cross-filesystem os.Rename causes silent data loss.
//
// Algorithm under test (from architect's description):
//   1. Read entries in src directory
//   2. For each entry, os.Rename(src/entry, dst/entry)
//   3. If rename fails, log "could not rename to /scion, skipping" and continue
//   4. After loop, os.RemoveAll(src)
//   5. os.Symlink(dst, src) — so old paths resolve to new location

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// relocateToScion is the algorithm described by the architect for the planned
// cloudrun_sandbox_runtime.go function. Reproduced here verbatim for testing.
//
// It moves files from src to dst entry-by-entry using os.Rename, then removes
// src and creates a symlink src → dst.
func relocateToScion(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read src dir: %w", err)
	}

	for _, entry := range entries {
		oldPath := filepath.Join(src, entry.Name())
		newPath := filepath.Join(dst, entry.Name())
		if err := os.Rename(oldPath, newPath); err != nil {
			slog.Warn("could not rename to /scion, skipping",
				"src", oldPath,
				"dst", newPath,
				"error", err,
			)
			continue
		}
	}

	// Remove source directory unconditionally.
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove src: %w", err)
	}

	// Create symlink so old paths still resolve.
	if err := os.Symlink(dst, src); err != nil {
		return fmt.Errorf("symlink: %w", err)
	}

	return nil
}

// TestRelocateToScion_CrossFilesystem tests the relocateToScion algorithm when
// src and dst are on different filesystems (/dev/shm = tmpfs, /tmp = overlayfs).
//
// Architect's prediction: every os.Rename fails with EXDEV, each is logged and
// skipped, then os.RemoveAll(src) deletes all source files. Net effect: files
// gone, dst empty, symlink pointing at an empty directory. Data loss.
func TestRelocateToScion_CrossFilesystem(t *testing.T) {
	// --- Setup: src on tmpfs (/dev/shm), dst on regular filesystem (/tmp) ---
	srcDir, err := os.MkdirTemp("/dev/shm", "relocate-test-src-")
	if err != nil {
		t.Fatalf("create src dir on /dev/shm: %v", err)
	}
	t.Cleanup(func() {
		// Clean up: remove the symlink (or dir) and the dst
		os.RemoveAll(srcDir)
		// srcDir might be a symlink now, os.Remove handles that
		os.Remove(srcDir)
	})

	dstDir, err := os.MkdirTemp("/tmp", "relocate-test-dst-")
	if err != nil {
		t.Fatalf("create dst dir on /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dstDir) })

	// --- Verify different filesystems ---
	var srcStat, dstStat syscall.Statfs_t
	if err := syscall.Statfs(srcDir, &srcStat); err != nil {
		t.Fatalf("statfs src: %v", err)
	}
	if err := syscall.Statfs(dstDir, &dstStat); err != nil {
		t.Fatalf("statfs dst: %v", err)
	}
	t.Logf("src filesystem type: 0x%x (at %s)", srcStat.Type, srcDir)
	t.Logf("dst filesystem type: 0x%x (at %s)", dstStat.Type, dstDir)
	if srcStat.Type == dstStat.Type {
		t.Skipf("src and dst are on the same filesystem type (0x%x) — cannot test cross-fs behavior", srcStat.Type)
	}
	t.Logf("CONFIRMED: src and dst are on different filesystems")

	// --- Populate src with test files ---
	testFiles := map[string]string{
		".tmux.conf":                `set-hook -g pane-exited "run-shell '/usr/local/bin/sciontool hooks pane-exited #{pane_id}'"`,
		"agent-info.json":          `{"agent_id":"test-123","name":"test-agent"}`,
		".scion/config.yaml":       `hub_url: https://hub.example.com`,
		".scion/agents/a1/state":   `running`,
		"nested/deep/file.txt":     `deep nested content`,
	}
	for relPath, content := range testFiles {
		fullPath := filepath.Join(srcDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", relPath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	// --- Record what was in src before the call ---
	srcEntriesBefore, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("read src before: %v", err)
	}
	t.Logf("src entries BEFORE relocateToScion: %d", len(srcEntriesBefore))
	for _, e := range srcEntriesBefore {
		t.Logf("  src/%s (dir=%v)", e.Name(), e.IsDir())
	}

	// --- Verify os.Rename would fail with EXDEV (sanity check) ---
	probeFile := filepath.Join(srcDir, ".relocate-probe")
	if err := os.WriteFile(probeFile, []byte("probe"), 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	probeErr := os.Rename(probeFile, filepath.Join(dstDir, ".relocate-probe"))
	if probeErr != nil {
		t.Logf("CONFIRMED: os.Rename across filesystems fails: %v", probeErr)
		os.Remove(probeFile) // clean up probe
	} else {
		// Rename succeeded — unexpected! Clean up and note it.
		os.Remove(filepath.Join(dstDir, ".relocate-probe"))
		t.Logf("WARNING: os.Rename across filesystems SUCCEEDED — test may not expose the bug")
	}

	// --- Call the function under test ---
	t.Logf("--- Calling relocateToScion(%s, %s) ---", srcDir, dstDir)
	err = relocateToScion(srcDir, dstDir)
	t.Logf("relocateToScion returned: %v", err)

	// --- Check outcomes ---

	// 1. Does the symlink exist?
	linkTarget, linkErr := os.Readlink(srcDir)
	if linkErr != nil {
		t.Logf("OUTCOME: srcDir is NOT a symlink (err: %v)", linkErr)
	} else {
		t.Logf("OUTCOME: srcDir IS a symlink -> %s", linkTarget)
	}

	// 2. What's in the destination?
	dstEntries, dstErr := os.ReadDir(dstDir)
	if dstErr != nil {
		t.Logf("OUTCOME: cannot read dstDir: %v", dstErr)
	} else {
		t.Logf("OUTCOME: dst has %d entries", len(dstEntries))
		for _, e := range dstEntries {
			t.Logf("  dst/%s (dir=%v)", e.Name(), e.IsDir())
		}
	}

	// 3. What's at the symlink target (should be same as dst)?
	if linkErr == nil {
		symlinkEntries, symlinkErr := os.ReadDir(srcDir) // follows symlink
		if symlinkErr != nil {
			t.Logf("OUTCOME: cannot read through symlink: %v", symlinkErr)
		} else {
			t.Logf("OUTCOME: reading through symlink finds %d entries", len(symlinkEntries))
			for _, e := range symlinkEntries {
				t.Logf("  (via symlink) %s (dir=%v)", e.Name(), e.IsDir())
			}
		}
	}

	// 4. Are any source files still accessible via their original paths?
	t.Logf("--- Checking original file paths ---")
	filesFound := 0
	filesLost := 0
	for relPath, expectedContent := range testFiles {
		origPath := filepath.Join(srcDir, relPath)
		dstPath := filepath.Join(dstDir, relPath)

		// Check via original path (through symlink)
		origData, origErr := os.ReadFile(origPath)
		// Check via dst path directly
		dstData, dstErr := os.ReadFile(dstPath)

		if origErr != nil && dstErr != nil {
			t.Logf("  LOST: %s — not at src (%v) or dst (%v)", relPath, origErr, dstErr)
			filesLost++
		} else if dstErr != nil {
			t.Logf("  PARTIAL: %s — at src but not dst (src content: %q)", relPath, string(origData))
			filesFound++
		} else if origErr != nil {
			t.Logf("  MOVED: %s — at dst but not src via symlink (dst content: %q)", relPath, string(dstData))
			filesFound++
		} else {
			if string(origData) == expectedContent && string(dstData) == expectedContent {
				t.Logf("  OK: %s — accessible via both paths, content matches", relPath)
			} else {
				t.Logf("  MISMATCH: %s — orig=%q, dst=%q, expected=%q", relPath, string(origData), string(dstData), expectedContent)
			}
			filesFound++
		}
	}

	// --- Verdict ---
	t.Logf("=== VERDICT ===")
	t.Logf("Files found (via any path): %d / %d", filesFound, len(testFiles))
	t.Logf("Files lost: %d / %d", filesLost, len(testFiles))

	if filesLost > 0 {
		t.Logf("DATA LOSS CONFIRMED: %d files were destroyed by relocateToScion on cross-filesystem move", filesLost)
		t.Logf("Architect's prediction HOLDS: os.Rename fails with EXDEV, files are skipped, then os.RemoveAll deletes them all")
	} else {
		t.Logf("NO DATA LOSS: all files survived the cross-filesystem relocateToScion call")
		t.Logf("Architect's prediction BROKEN")
	}

	if dstErr == nil && len(dstEntries) == 0 && filesLost == len(testFiles) {
		t.Errorf("CRITICAL: All %d files destroyed — dst is empty, src removed, data gone", len(testFiles))
	}
}

// TestRelocateToScion_SameFilesystem is the control test: src and dst on the
// same filesystem (both under /tmp). This should work correctly.
func TestRelocateToScion_SameFilesystem(t *testing.T) {
	// --- Setup: both on /tmp (same filesystem) ---
	srcDir, err := os.MkdirTemp("/tmp", "relocate-test-samefs-src-")
	if err != nil {
		t.Fatalf("create src dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(srcDir)
		os.Remove(srcDir)
	})

	dstDir, err := os.MkdirTemp("/tmp", "relocate-test-samefs-dst-")
	if err != nil {
		t.Fatalf("create dst dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dstDir) })

	// --- Verify same filesystem ---
	var srcStat, dstStat syscall.Statfs_t
	if err := syscall.Statfs(srcDir, &srcStat); err != nil {
		t.Fatalf("statfs src: %v", err)
	}
	if err := syscall.Statfs(dstDir, &dstStat); err != nil {
		t.Fatalf("statfs dst: %v", err)
	}
	t.Logf("src filesystem type: 0x%x (at %s)", srcStat.Type, srcDir)
	t.Logf("dst filesystem type: 0x%x (at %s)", dstStat.Type, dstDir)
	if srcStat.Type != dstStat.Type {
		t.Skipf("src and dst are on different filesystems — cannot test same-fs behavior")
	}
	t.Logf("CONFIRMED: src and dst are on the same filesystem")

	// --- Populate src with test files ---
	testFiles := map[string]string{
		".tmux.conf":                `set-hook -g pane-exited "run-shell '/usr/local/bin/sciontool hooks pane-exited #{pane_id}'"`,
		"agent-info.json":          `{"agent_id":"test-123","name":"test-agent"}`,
		".scion/config.yaml":       `hub_url: https://hub.example.com`,
		".scion/agents/a1/state":   `running`,
		"nested/deep/file.txt":     `deep nested content`,
	}
	for relPath, content := range testFiles {
		fullPath := filepath.Join(srcDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", relPath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	srcEntriesBefore, _ := os.ReadDir(srcDir)
	t.Logf("src entries BEFORE: %d", len(srcEntriesBefore))
	for _, e := range srcEntriesBefore {
		t.Logf("  src/%s (dir=%v)", e.Name(), e.IsDir())
	}

	// --- Call the function under test ---
	t.Logf("--- Calling relocateToScion(%s, %s) ---", srcDir, dstDir)
	err = relocateToScion(srcDir, dstDir)
	if err != nil {
		t.Fatalf("relocateToScion returned error: %v", err)
	}
	t.Logf("relocateToScion returned: nil (success)")

	// --- Check outcomes ---

	// 1. Symlink exists?
	linkTarget, linkErr := os.Readlink(srcDir)
	if linkErr != nil {
		t.Errorf("OUTCOME: srcDir is NOT a symlink: %v", linkErr)
	} else {
		t.Logf("OUTCOME: srcDir IS a symlink -> %s", linkTarget)
		if linkTarget != dstDir {
			t.Errorf("symlink target %q != expected %q", linkTarget, dstDir)
		}
	}

	// 2. All files accessible via dst and via symlink?
	filesFound := 0
	filesLost := 0
	for relPath, expectedContent := range testFiles {
		origPath := filepath.Join(srcDir, relPath)
		dstPath := filepath.Join(dstDir, relPath)

		origData, origErr := os.ReadFile(origPath)
		dstData, dstErr := os.ReadFile(dstPath)

		if origErr != nil && dstErr != nil {
			t.Errorf("LOST: %s — not at src (%v) or dst (%v)", relPath, origErr, dstErr)
			filesLost++
		} else if origErr != nil {
			t.Errorf("PARTIAL: %s — at dst but not accessible via symlink: %v", relPath, origErr)
			filesFound++
		} else if dstErr != nil {
			t.Errorf("PARTIAL: %s — accessible via symlink but not at dst: %v", relPath, dstErr)
			filesFound++
		} else {
			if string(origData) != expectedContent {
				t.Errorf("CONTENT MISMATCH (via symlink): %s — got %q, want %q", relPath, string(origData), expectedContent)
			}
			if string(dstData) != expectedContent {
				t.Errorf("CONTENT MISMATCH (dst): %s — got %q, want %q", relPath, string(dstData), expectedContent)
			}
			t.Logf("  OK: %s — accessible via both paths, content correct", relPath)
			filesFound++
		}
	}

	// --- Verdict ---
	t.Logf("=== VERDICT ===")
	t.Logf("Files found: %d / %d", filesFound, len(testFiles))
	t.Logf("Files lost: %d / %d", filesLost, len(testFiles))

	if filesLost > 0 {
		t.Errorf("DATA LOSS on same-filesystem: %d files lost (this should never happen)", filesLost)
	} else {
		t.Logf("Same-filesystem relocate works correctly — all files moved and accessible")
	}
}

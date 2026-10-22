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

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// eval resolves symlinks so comparisons hold on platforms (e.g. macOS) where
// t.TempDir() lives under a symlinked prefix while git reports the real path.
func eval(t *testing.T, p string) string {
	t.Helper()
	if p == "" {
		return ""
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return r
}

// createRealWorktree creates root/worktrees/<name> as a genuine git worktree
// of root (via util.CreateWorktree, the same helper production code uses),
// and returns its path. root must already be a git repo (setupGitRepo).
func createRealWorktree(t *testing.T, root, name string) string {
	t.Helper()
	worktreesDir := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		t.Fatalf("mkdir worktrees dir: %v", err)
	}
	worktree := filepath.Join(worktreesDir, name)
	if err := util.CreateWorktree(worktree, name); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	return worktree
}

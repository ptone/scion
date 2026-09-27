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

//go:build !no_sqlite

package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
	"github.com/stretchr/testify/require"
)

// runGitFixture runs a git command in dir, failing the test on error. It
// exists here (rather than importing a test helper from pkg/util) because
// pkg/util's equivalent helpers are unexported test-only functions.
func runGitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, out)
	}
}

// buildFixtureLandedWorkspace creates a small, valid git clone at dir with a
// post-merge hook already present in .git/hooks — standing in for whatever a
// broker previously uploaded to the GCS "files" prefix these tests never
// actually touch (this repo has no GCS test infrastructure/emulator).
func buildFixtureLandedWorkspace(t *testing.T, dir string) {
	t.Helper()
	sourceDir := t.TempDir()
	runGitFixture(t, sourceDir, "init", "-q", "-b", "main")
	runGitFixture(t, sourceDir, "config", "user.email", "a@example.com")
	runGitFixture(t, sourceDir, "config", "user.name", "a")
	runGitFixture(t, sourceDir, "commit", "-q", "--allow-empty", "-m", "init")

	if err := util.CloneSharedWorkspace(dir, sourceDir, "", ""); err != nil {
		t.Fatalf("fixture clone failed: %v", err)
	}
	hookPath := filepath.Join(dir, ".git", "hooks", "post-merge")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho ran\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

// TestHandleProjectCacheNotify_LandingIsFiltered proves that
// handleProjectCacheNotify's landing goes through the same
// lock+unconditional-rebuild path as the other two landing sites
// (syncWorkspaceOnStop, syncHubManagedWorkspaceBack), rather than writing
// GCS content straight onto the filesystem as it used to. It substitutes
// syncFromGCSFunc for a local copy (no GCS backend/emulator exists in this
// repo's test infrastructure) that lands a fixture .git carrying a hook,
// then asserts the hook is gone after the handler runs. Reverting
// handleProjectCacheNotify to call gcp.SyncFromGCS directly (skipping
// landProjectWorkspace) would fail this test: the hook would still be
// there.
func TestHandleProjectCacheNotify_LandingIsFiltered(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	srv, st := testServer(t)
	ctx := context.Background()
	srv.SetStorage(newMockStorage("test-bucket"))

	project := &store.Project{
		ID:        tid("cache-notify-landing-project"),
		Name:      "Cache Notify Landing Test",
		Slug:      "cache-notify-landing",
		CreatedBy: DevUserID,
		OwnerID:   DevUserID,
	}
	require.NoError(t, st.CreateProject(ctx, project))

	fixture := t.TempDir()
	buildFixtureLandedWorkspace(t, fixture)
	if _, err := os.Stat(filepath.Join(fixture, ".git", "hooks", "post-merge")); err != nil {
		t.Fatalf("test fixture setup broken: %v", err)
	}

	orig := syncFromGCSFunc
	t.Cleanup(func() { syncFromGCSFunc = orig })
	syncFromGCSFunc = func(_ context.Context, _, _, localPath string) error {
		return util.CopyDir(fixture, localPath)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+project.ID+"/workspace/cache/notify", nil)
	rec := httptest.NewRecorder()
	srv.handleProjectCacheNotify(rec, req, project)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	workspacePath, err := srv.hubManagedProjectPath(project.Slug)
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(workspacePath, ".git", "hooks", "post-merge")); !os.IsNotExist(err) {
		t.Errorf("expected the landed hook to be removed by the admin-surface rebuild, stat err: %v", err)
	}
}

// TestLandProjectWorkspace_FiltersLandedContent exercises landProjectWorkspace
// directly — the helper refreshProjectCacheFromBroker now calls for its
// download step (project_cache.go's other former direct-SyncFromGCS call
// site). refreshProjectCacheFromBroker itself is not exercised end to end
// here: it also tunnels a broker upload request over the control channel,
// which is out of scope for this test. What matters for this fix is that
// the landing step it now delegates to is the same filtered helper.
func TestLandProjectWorkspace_FiltersLandedContent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	srv, st := testServer(t)
	ctx := context.Background()
	srv.SetStorage(newMockStorage("test-bucket"))

	project := &store.Project{
		ID:        tid("cache-refresh-landing-project"),
		Name:      "Cache Refresh Landing Test",
		Slug:      "cache-refresh-landing",
		CreatedBy: DevUserID,
		OwnerID:   DevUserID,
	}
	require.NoError(t, st.CreateProject(ctx, project))

	fixture := t.TempDir()
	buildFixtureLandedWorkspace(t, fixture)

	orig := syncFromGCSFunc
	t.Cleanup(func() { syncFromGCSFunc = orig })
	syncFromGCSFunc = func(_ context.Context, _, _, localPath string) error {
		return util.CopyDir(fixture, localPath)
	}

	storagePath := "irrelevant-storage-path"
	if err := srv.landProjectWorkspace(ctx, project, storagePath); err != nil {
		t.Fatalf("landProjectWorkspace failed: %v", err)
	}

	workspacePath, err := srv.hubManagedProjectPath(project.Slug)
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(workspacePath, ".git", "hooks", "post-merge")); !os.IsNotExist(err) {
		t.Errorf("expected the landed hook to be removed by the admin-surface rebuild, stat err: %v", err)
	}
}

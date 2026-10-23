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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/projectfs"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Project storage phase 2 (ptone/scion#5813, ptone/scion#5814): the hub's
// workspace Files tab, archive and pull resolve through the layout
// authority. On a shared backend the hub reads and writes the same
// projects/<uuid>/workspace tree agents mount (no legacy slug-keyed
// copy, no content probe); on the local backend (workstation server) the
// path is unchanged, ~/.scion/projects/<slug>. Project configuration is
// seeded at ProjectRoot(uuid)/.scion on shared backends (design-v7 §P1a).

// workspaceLayoutFixture is a hub with a fresh HOME and an NFS mount root
// whose share directory exists (the share is "mounted").
type workspaceLayoutFixture struct {
	srv       *Server
	s         store.Store
	home      string
	mountRoot string
	shareBase string
}

func newWorkspaceLayoutFixture(t *testing.T) workspaceLayoutFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("K_SERVICE", "")
	t.Setenv("CLOUD_RUN_INSTANCE", "")
	srv, s := testServer(t)
	mountRoot := t.TempDir()
	shareBase := filepath.Join(mountRoot, "share1")
	require.NoError(t, os.MkdirAll(shareBase, 0o755))
	return workspaceLayoutFixture{srv: srv, s: s, home: home, mountRoot: mountRoot, shareBase: shareBase}
}

func (f workspaceLayoutFixture) useNFS() {
	f.srv.config.WorkspaceStorageConfig = nfsConfig(f.mountRoot)
}

func workspaceFilesURL(projectID string) string {
	return fmt.Sprintf("/api/v1/projects/%s/workspace/files", projectID)
}

func listWorkspaceFiles(t *testing.T, srv *Server, projectID string) []string {
	t.Helper()
	rec := doRequest(t, srv, http.MethodGet, workspaceFilesURL(projectID), nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var resp ProjectWorkspaceListResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	var names []string
	for _, f := range resp.Files {
		names = append(names, f.Path)
	}
	return names
}

// TestProjectWorkspaceFiles_NFS_UUIDTree pins the T4 fix for the workspace:
// with server.workspace_storage: nfs, a web upload lands in
// <mount_root>/<share>/projects/<uuid>/workspace (where agents mount it),
// a file an agent writes there is listed, and nothing is written under a
// legacy slug-keyed tree on the share or the hub's local slug path.
func TestProjectWorkspaceFiles_NFS_UUIDTree(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	f.useNFS()
	t.Setenv("K_SERVICE", "scion-hub") // a Cloud Run Service: the write gate passes on NFS
	project, _ := createTestHubManagedProject(t, f.srv, "UUID Workspace")

	rec := doMultipartRequest(t, f.srv, http.MethodPost, workspaceFilesURL(project.ID),
		map[string][]byte{"from-web.txt": []byte("web")})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	ws := filepath.Join(f.shareBase, "projects", project.ID, "workspace")
	got, err := os.ReadFile(filepath.Join(ws, "from-web.txt"))
	require.NoError(t, err, "the upload lands in the uuid tree agents mount")
	assert.Equal(t, "web", string(got))

	require.NoError(t, os.WriteFile(filepath.Join(ws, "from-agent.txt"), []byte("agent"), 0o644))
	assert.ElementsMatch(t, []string{"from-web.txt", "from-agent.txt"}, listWorkspaceFiles(t, f.srv, project.ID))

	entries, err := os.ReadDir(f.shareBase)
	require.NoError(t, err)
	for _, e := range entries {
		assert.Equal(t, "projects", e.Name(), "the share holds only the uuid tree (no legacy slug-keyed copy)")
	}
	_, err = os.Stat(filepath.Join(f.home, ".scion", "projects", project.Slug, "from-web.txt"))
	assert.True(t, os.IsNotExist(err), "nothing is written to the hub's local slug path")
}

// TestProjectWorkspaceFiles_Local_SlugPathUnchanged is the T1 regression
// check: with no workspace storage configured the Files tab serves
// ~/.scion/projects/<slug>, exactly as before.
func TestProjectWorkspaceFiles_Local_SlugPathUnchanged(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	project, workspacePath := createTestHubManagedProject(t, f.srv, "Local Workspace")
	require.Equal(t, filepath.Join(f.home, ".scion", "projects", project.Slug), workspacePath)

	rec := doMultipartRequest(t, f.srv, http.MethodPost, workspaceFilesURL(project.ID),
		map[string][]byte{"local.txt": []byte("local")})
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	got, err := os.ReadFile(filepath.Join(workspacePath, "local.txt"))
	require.NoError(t, err)
	assert.Equal(t, "local", string(got))
	_, err = os.Stat(filepath.Join(workspacePath, ".scion", "settings.yaml"))
	assert.NoError(t, err, "the local backend seeds .scion inside the workspace, as before")
}

// TestProjectWorkspaceFiles_VolumeBackends_UUIDTree: the platform volume
// backends resolve the same uuid tree under /mnt/<volume>/<subpath_root>.
func TestProjectWorkspaceFiles_VolumeBackends_UUIDTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *config.V1WorkspaceStorageConfig
	}{
		{"cloudrun-volume", &config.V1WorkspaceStorageConfig{Backend: "cloudrun-volume", CloudRunVolume: &config.V1CloudRunVolumeConfig{VolumeName: "ws"}}},
		{"gke-shared-volume", &config.V1WorkspaceStorageConfig{Backend: "gke-shared-volume", GKESharedVolume: &config.V1GKESharedVolumeConfig{VolumeName: "ws", SubPathRoot: "tree"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkspaceLayoutFixture(t)
			base := t.TempDir()
			setVolumeMountBase(t, base)
			require.NoError(t, os.MkdirAll(filepath.Join(base, "ws"), 0o755))
			f.srv.config.WorkspaceStorageConfig = tc.cfg
			project, _ := createTestHubManagedProject(t, f.srv, "Volume "+tc.name)

			res, err := f.srv.projectWorkspaceResolved(project)
			require.NoError(t, err)
			root := config.SubPathRootOrDefault("")
			if tc.cfg.GKESharedVolume != nil {
				root = "tree"
			}
			assert.Equal(t, filepath.Join(base, "ws", root, project.ID, "workspace"), res.HostPath)
			assert.True(t, res.Backend.Durable())

			rec := doRequest(t, f.srv, http.MethodPut, workspaceFilesURL(project.ID)+"/v.txt", ProjectWorkspaceWriteRequest{Content: "v"})
			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			_, err = os.Stat(filepath.Join(res.HostPath, "v.txt"))
			assert.NoError(t, err)
		})
	}
}

// TestProjectWorkspaceFiles_VolumeBackendWithoutName_FailsClosed: a volume
// backend with no volume name has no mount point; the hub never falls back
// to instance-local disk (ptone/scion#1073): the Files tab answers 503 and
// nothing is created locally.
func TestProjectWorkspaceFiles_VolumeBackendWithoutName_FailsClosed(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	project, workspacePath := createTestHubManagedProject(t, f.srv, "Unnamed Volume")
	require.NoError(t, os.RemoveAll(workspacePath))
	f.srv.config.WorkspaceStorageConfig = &config.V1WorkspaceStorageConfig{Backend: "cloudrun-volume"}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var code int
		if method == http.MethodGet {
			code = doRequest(t, f.srv, method, workspaceFilesURL(project.ID), nil).Code
		} else {
			code = doMultipartRequest(t, f.srv, method, workspaceFilesURL(project.ID), map[string][]byte{"a.txt": []byte("a")}).Code
		}
		assert.Equal(t, http.StatusServiceUnavailable, code, method)
	}
	_, err := os.Stat(workspacePath)
	assert.True(t, os.IsNotExist(err), "nothing is created on instance-local disk")
}

// TestProjectWorkspaceFiles_EmptyPerAgent_Conflict: an empty-per-agent
// project has no project-scoped workspace (projectfs.ErrNoMount), so the
// Files tab answers 409 server-side instead of serving a directory no agent
// mounts.
func TestProjectWorkspaceFiles_EmptyPerAgent_Conflict(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	project, _ := createTestHubManagedProject(t, f.srv, "Empty Per Agent")
	project.Labels = map[string]string{store.LabelWorkspaceMode: string(store.SharingModeEmptyPerAgent)}
	require.NoError(t, f.s.UpdateProject(context.Background(), project))
	require.True(t, project.IsEmptyPerAgent(), "fixture check")

	_, err := f.srv.projectWorkspaceResolved(project)
	require.ErrorIs(t, err, projectfs.ErrNoMount)

	for _, url := range []string{workspaceFilesURL(project.ID), fmt.Sprintf("/api/v1/projects/%s/workspace/archive", project.ID)} {
		rec := doRequest(t, f.srv, http.MethodGet, url, nil)
		assert.Equal(t, http.StatusConflict, rec.Code, "%s: %s", url, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "each agent gets its own private workspace directory")
	}
}

// TestProjectWorkspaceFiles_RemoteLinkedProject_ConflictNoCache: a linked
// project served only by a remote broker has no workspace the hub can
// reach; the Files tab answers 409 and the hub keeps no cached copy
// (design-v7 OQ-R1, ptone/scion#5816).
func TestProjectWorkspaceFiles_RemoteLinkedProject_ConflictNoCache(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	project := createTestGitProject(t, f.srv, "Remote Linked WS", "github.com/test/remote-linked-ws")
	addProviderBroker(t, f.s, project.ID, "remote")

	for _, shared := range []bool{false, true} {
		if shared {
			f.useNFS()
		}
		rec := doRequest(t, f.srv, http.MethodGet, workspaceFilesURL(project.ID), nil)
		require.Equal(t, http.StatusConflict, rec.Code, "shared=%v body: %s", shared, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "remote runtime brokers without shared storage")
	}
	_, err := os.Stat(filepath.Join(f.home, ".scion", "projects", project.Slug))
	assert.True(t, os.IsNotExist(err), "no hub-side cache directory is created")
	for _, route := range []string{"workspace/cache/refresh", "workspace/cache/status", "workspace/cache/notify", "sync/status"} {
		rec := doRequest(t, f.srv, http.MethodGet, fmt.Sprintf("/api/v1/projects/%s/%s", project.ID, route), nil)
		assert.NotEqual(t, http.StatusOK, rec.Code, "the linked-project cache route %s is gone", route)
	}
}

// TestProjectWorkspaceFiles_EmbeddedLinkedProject_ProviderPath: a linked
// project with a co-located (embedded) broker keeps serving the broker's
// local path.
func TestProjectWorkspaceFiles_EmbeddedLinkedProject_ProviderPath(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	project := createTestGitProject(t, f.srv, "Embedded Linked WS", "github.com/test/embedded-linked-ws")
	b := addProviderBroker(t, f.s, project.ID, "embedded")
	f.srv.SetEmbeddedBrokerID(b.ID)
	checkout := t.TempDir()
	require.NoError(t, f.s.AddProjectProvider(context.Background(), &store.ProjectProvider{
		ProjectID: project.ID, BrokerID: b.ID, BrokerName: b.Name, Status: b.Status, LocalPath: checkout,
	}))
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "main.go"), []byte("package main"), 0o644))

	assert.Equal(t, []string{"main.go"}, listWorkspaceFiles(t, f.srv, project.ID))
}

// TestInitHubManagedProject_SharedBackend_SeedsProjectRoot pins design-v7
// §P1a on the hub: on a shared backend the hub seeds the project
// configuration (settings with the hub connection and project ID) at
// projects/<uuid>/.scion beside workspace/, inside no Mount, and writes no
// .scion into the workspace agents mount.
func TestInitHubManagedProject_SharedBackend_SeedsProjectRoot(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	f.useNFS()
	project, _ := createTestHubManagedProject(t, f.srv, "Seeded Root")

	cfgDir := filepath.Join(f.shareBase, "projects", project.ID, ".scion")
	data, err := os.ReadFile(filepath.Join(cfgDir, "settings.yaml"))
	require.NoError(t, err, "the project configuration is seeded at the project root")
	assert.Contains(t, string(data), project.ID, "the seeded settings carry the hub project ID")
	info, err := os.Lstat(cfgDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Zero(t, info.Mode().Perm()&0o022, "the configuration directory is not group/other-writable")

	_, err = os.Stat(filepath.Join(f.shareBase, "projects", project.ID, "workspace", ".scion"))
	assert.True(t, os.IsNotExist(err), "no .scion is created inside the workspace agents mount")
	_, err = os.Stat(filepath.Join(f.home, ".scion", "projects", project.Slug))
	assert.True(t, os.IsNotExist(err), "nothing is seeded on the hub's local disk")

	dir, err := f.srv.projectConfigDir(project)
	require.NoError(t, err)
	assert.Equal(t, cfgDir, dir)

	// The identity files beside settings.yaml: project-id and project-slug
	// (fs-cleanup-arch P3b Q7), one line each, mode 0644.
	for name, want := range map[string]string{projectkeys.ProjectIDFile: project.ID, projectkeys.ProjectSlugFile: project.Slug} {
		data, err := os.ReadFile(filepath.Join(cfgDir, name))
		require.NoError(t, err, name)
		assert.Equal(t, want+"\n", string(data), name)
		info, err := os.Lstat(filepath.Join(cfgDir, name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), name)
	}
}

// TestProjectSlugRename_SharedBackend_UpdatesProjectSlugFile: renaming a
// project's slug on a shared backend rewrites <ProjectRoot>/.scion/
// project-slug; nothing slug-keyed moves.
func TestProjectSlugRename_SharedBackend_UpdatesProjectSlugFile(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	f.useNFS()
	project, _ := createTestHubManagedProject(t, f.srv, "Slug Before")
	slugFile := filepath.Join(f.shareBase, "projects", project.ID, ".scion", projectkeys.ProjectSlugFile)
	data, err := os.ReadFile(slugFile)
	require.NoError(t, err)
	require.Equal(t, project.Slug+"\n", string(data), "fixture check")

	oldSlug := project.Slug
	project.Slug = "slug-after"
	f.srv.migrateProjectSlug(context.Background(), project, oldSlug)

	data, err = os.ReadFile(slugFile)
	require.NoError(t, err)
	assert.Equal(t, "slug-after\n", string(data))
	_, err = os.Stat(filepath.Join(f.home, ".scion", "projects", "slug-after"))
	assert.True(t, os.IsNotExist(err), "nothing slug-keyed is created on the hub's local disk")

	// A project without a project configuration directory yet (created
	// before the upgrade, not migrated) gets none from a rename.
	other := &store.Project{ID: "aaaaaaaa-0000-4000-8000-0000000000c1", Name: "Legacy", Slug: "legacy-old"}
	other.Slug = "legacy-new"
	f.srv.migrateProjectSlug(context.Background(), other, "legacy-old")
	_, err = os.Stat(filepath.Join(f.shareBase, "projects", other.ID))
	assert.True(t, os.IsNotExist(err), "a rename never creates the project configuration")
}

// TestProjectSlugFile_LocalBackendUnaffected: on the local backend the seed
// keeps its existing form (no project-id / project-slug files are added).
func TestProjectSlugFile_LocalBackendUnaffected(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	_, workspacePath := createTestHubManagedProject(t, f.srv, "Local Slug")
	for _, name := range []string{projectkeys.ProjectIDFile, projectkeys.ProjectSlugFile} {
		_, err := os.Stat(filepath.Join(workspacePath, ".scion", name))
		assert.True(t, os.IsNotExist(err), "%s is not written on the local backend", name)
	}
}

// TestCreateProject_SharedBackendUnmounted_RollsBack: a shared backend whose
// share is not mounted cannot hold the project configuration; the create is
// rolled back with 503 rather than seeding instance-local disk.
func TestCreateProject_SharedBackendUnmounted_RollsBack(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	f.srv.config.WorkspaceStorageConfig = nfsConfig(filepath.Join(f.home, "not-mounted"))

	rec := doRequest(t, f.srv, http.MethodPost, "/api/v1/projects", CreateProjectRequest{Name: "Unmounted Share"})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "body: %s", rec.Body.String())
	result, err := f.s.ListProjects(context.Background(), store.ProjectFilter{}, store.ListOptions{})
	require.NoError(t, err)
	for _, p := range result.Items {
		assert.NotEqual(t, "Unmounted Share", p.Name, "the project is rolled back")
	}
}

// TestCreateSharedWorkspaceProject_SharedBackend_NoHubClone: on a shared
// backend the hub does not clone a shared-workspace project into the tree
// agents mount (the first agent provisions it under the provisioning lock);
// it only seeds the project configuration at the project root.
func TestCreateSharedWorkspaceProject_SharedBackend_NoHubClone(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	f.useNFS()

	rec := doRequest(t, f.srv, http.MethodPost, "/api/v1/projects", CreateProjectRequest{
		Name: "Shared Git NFS", GitRemote: "github.com/test/shared-git-nfs", WorkspaceMode: store.WorkspaceModeShared,
	})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var project store.Project
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&project))
	require.True(t, project.IsSharedWorkspace(), "fixture check")

	_, err := os.Stat(filepath.Join(f.shareBase, "projects", project.ID, "workspace"))
	assert.True(t, os.IsNotExist(err), "the hub does not populate the shared workspace")
	_, err = os.Stat(filepath.Join(f.shareBase, "projects", project.ID, ".scion", "settings.yaml"))
	assert.NoError(t, err, "the project configuration is seeded at the project root")
}

// TestAlignClonedProjectIdentities_SharedBackendLeavesWorkspaceScion: on a
// shared backend a .scion in the workspace is repository content (design-v7
// §P1a rule 2); the hub's cloned-identity alignment does not run there and
// never rewrites it.
func TestAlignClonedProjectIdentities_SharedBackendLeavesWorkspaceScion(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	f.useNFS()
	project := &store.Project{
		ID: "aaaaaaaa-0000-4000-8000-0000000000b1", Name: "Repo Scion", Slug: "repo-scion",
		GitRemote: "github.com/test/repo-scion", Labels: map[string]string{store.LabelWorkspaceMode: store.WorkspaceModeShared},
	}
	require.NoError(t, f.s.CreateProject(context.Background(), project))
	wsScion := filepath.Join(f.shareBase, "projects", project.ID, "workspace", ".scion")
	require.NoError(t, os.MkdirAll(wsScion, 0o755))
	repoID := "bbbbbbbb-0000-4000-8000-0000000000b2"
	require.NoError(t, config.WriteProjectID(wsScion, repoID))

	counts := f.srv.alignClonedProjectIdentities(context.Background())
	assert.Zero(t, counts.aligned)
	got, err := config.ReadProjectID(wsScion)
	require.NoError(t, err)
	assert.Equal(t, repoID, got, "a repository .scion never takes the hub's identity on a shared backend")
}

// TestProjectWorkspaceFiles_MigrationNoticeAbsentByDefault: the list
// response carries no migrationNotice when there is nothing to migrate.
func TestProjectWorkspaceFiles_MigrationNoticeAbsentByDefault(t *testing.T) {
	f := newWorkspaceLayoutFixture(t)
	project, _ := createTestHubManagedProject(t, f.srv, "No Notice")
	rec := doRequest(t, f.srv, http.MethodGet, workspaceFilesURL(project.ID), nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "migrationNotice")
}

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

package runtimebroker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer/bundle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMoveBundle_T7_CommitSemanticsAndMarker covers design §9 T7:
//   - workspace commit into empty dir; conflict on existing name fails closed
//   - home merge where provision-rendered files win (kept, source skipped and counted)
//   - imported marker (.scion-move-bundle-<rid>) prevents repeat import (409)
//   - crash mid-commit leaves only .bundle-* siblings which marker-guarded delete removes
//   - ProvisionOnly with WorkspaceSource="move-bundle" leaves workspace empty, renders home, writes move marker
func TestMoveBundle_T7_CommitSemanticsAndMarker(t *testing.T) {
	t.Run("workspace commit into empty dir and fail on existing name", func(t *testing.T) {
		_, _, wsDir, _ := newMoveBundleTestAgentDir(t, "dev")
		stageName, err := newMoveBundleStageName(filepath.Base(wsDir))
		require.NoError(t, err)
		stagePath := filepath.Join(filepath.Dir(wsDir), stageName)
		require.NoError(t, os.MkdirAll(filepath.Join(stagePath, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stagePath, "a.txt"), []byte("alpha"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(stagePath, "sub", "b.txt"), []byte("beta"), 0o644))

		require.NoError(t, commitMoveBundleWorkspace(wsDir, stageName))
		gotA, err := os.ReadFile(filepath.Join(wsDir, "a.txt"))
		require.NoError(t, err)
		assert.Equal(t, "alpha", string(gotA))
		gotB, err := os.ReadFile(filepath.Join(wsDir, "sub", "b.txt"))
		require.NoError(t, err)
		assert.Equal(t, "beta", string(gotB))

		// Second commit with colliding top-level name must fail closed without overwriting.
		stage2, err := newMoveBundleStageName(filepath.Base(wsDir))
		require.NoError(t, err)
		stage2Path := filepath.Join(filepath.Dir(wsDir), stage2)
		require.NoError(t, os.MkdirAll(stage2Path, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stage2Path, "a.txt"), []byte("overwrite"), 0o644))

		err = commitMoveBundleWorkspace(wsDir, stage2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
		stillA, err := os.ReadFile(filepath.Join(wsDir, "a.txt"))
		require.NoError(t, err)
		assert.Equal(t, "alpha", string(stillA))
	})

	t.Run("home merge where provision wins and skips are counted", func(t *testing.T) {
		_, _, _, homeDir := newMoveBundleTestAgentDir(t, "dev")
		// Provision-rendered files in homeDir:
		require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".scion"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".scion", "agent-info.json"), []byte(`{"target":true}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".gitconfig"), []byte("[user]\n\tname = target\n"), 0o644))

		// Staged home from source broker:
		stageName, err := newMoveBundleStageName(filepath.Base(homeDir))
		require.NoError(t, err)
		stagePath := filepath.Join(filepath.Dir(homeDir), stageName)
		require.NoError(t, os.MkdirAll(filepath.Join(stagePath, ".scion"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stagePath, ".scion", "agent-info.json"), []byte(`{"source":true}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(stagePath, ".scion", "history.log"), []byte("from-source\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(stagePath, ".gitconfig"), []byte("[user]\n\tname = source\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(stagePath, "notes.txt"), []byte("agent notes"), 0o600))

		skipped, err := commitMoveBundleHome(homeDir, stageName)
		require.NoError(t, err)
		assert.Equal(t, int64(2), skipped, "agent-info.json and .gitconfig must be skipped")

		// Provision-rendered files kept intact.
		gotInfo, err := os.ReadFile(filepath.Join(homeDir, ".scion", "agent-info.json"))
		require.NoError(t, err)
		assert.Equal(t, `{"target":true}`, string(gotInfo))
		gotGit, err := os.ReadFile(filepath.Join(homeDir, ".gitconfig"))
		require.NoError(t, err)
		assert.Contains(t, string(gotGit), "target")

		// Source-only files merged into home.
		gotHist, err := os.ReadFile(filepath.Join(homeDir, ".scion", "history.log"))
		require.NoError(t, err)
		assert.Equal(t, "from-source\n", string(gotHist))
		gotNotes, err := os.ReadFile(filepath.Join(homeDir, "notes.txt"))
		require.NoError(t, err)
		assert.Equal(t, "agent notes", string(gotNotes))
	})

	t.Run("imported marker prevents repeat import with 409", func(t *testing.T) {
		_, _, srcWS, srcHome := newMoveBundleTestAgentDir(t, "dev")
		require.NoError(t, os.WriteFile(filepath.Join(srcWS, "x.txt"), []byte("x"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(srcHome, "y.txt"), []byte("y"), 0o644))

		partsData := map[string][]byte{}
		wsRoot, err := os.OpenRoot(srcWS)
		require.NoError(t, err)
		wsRM, err := bundle.Write(context.Background(), wsRoot, t.TempDir(), bundle.WorkspaceExcluder(), bundle.DefaultWorkspaceLimits(),
			partSinkFunc(func(_ context.Context, idx int, r io.ReadSeeker, _ int64, _ string) error {
				b, err := io.ReadAll(r)
				partsData[fmt.Sprintf("workspace/%d", idx)] = b
				return err
			}))
		require.NoError(t, err)
		_ = wsRoot.Close()

		homeRoot, err := os.OpenRoot(srcHome)
		require.NoError(t, err)
		homeRM, err := bundle.Write(context.Background(), homeRoot, t.TempDir(), moveBundleHomeExcluder(), bundle.DefaultHomeLimits(),
			partSinkFunc(func(_ context.Context, idx int, r io.ReadSeeker, _ int64, _ string) error {
				b, err := io.ReadAll(r)
				partsData[fmt.Sprintf("home/%d", idx)] = b
				return err
			}))
		require.NoError(t, err)
		_ = homeRoot.Close()

		manifest := bundle.Manifest{
			Version:         bundle.ManifestVersion,
			ReincarnationID: "rid-marker",
			Roots: map[string]bundle.RootManifest{
				bundle.RootWorkspace: wsRM,
				bundle.RootHome:      homeRM,
			},
		}
		_, manifestHash, err := bundle.CanonicalManifest(manifest)
		require.NoError(t, err)

		objSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.URL.Path, "/")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(partsData[key])
		}))
		defer objSrv.Close()

		dstProject, dstAgentDir, _, _ := newMoveBundleTestAgentDir(t, "dev")
		dstSrv := newTestServer(t)
		dstSrv.moveBundle = moveBundleSettings{
			client: objSrv.Client(),
			fetchDownloadURLs: func(_ context.Context, _, _, _ string, _ *api.MoveBundleDownloadURLsRequest) (*api.MoveBundleDownloadURLsResponse, error) {
				var parts []api.MoveBundleDownloadPartURL
				for rootName, rm := range manifest.Roots {
					for _, p := range rm.Parts {
						parts = append(parts, api.MoveBundleDownloadPartURL{
							Root:    rootName,
							Index:   p.Index,
							URL:     fmt.Sprintf("%s/%s/%d", objSrv.URL, rootName, p.Index),
							Expires: time.Now().Add(15 * time.Minute),
						})
					}
				}
				return &api.MoveBundleDownloadURLsResponse{Parts: parts, ExpiresInSeconds: 900}, nil
			},
		}

		impReq := api.MoveBundleImportRequest{
			ReincarnationID: "rid-marker",
			ProjectID:       "p1",
			ProjectPath:     dstProject,
			Manifest:        manifest,
			ManifestHash:    manifestHash,
		}
		rec := doMoveBundleReq(t, dstSrv, http.MethodPost, "/api/v1/agents/dev/move-bundle/import?projectId=p1", impReq)
		require.Equal(t, http.StatusAccepted, rec.Code)
		st := waitForMoveBundleJob(t, dstSrv, "dev", "p1", api.MoveBundleJobKindImport, "rid-marker")
		require.Equal(t, api.MoveBundleJobStateSucceeded, st.State, "error=%s", st.Error)

		// Verify marker exists on disk.
		ok, err := hasMoveBundleImportedMarker(dstAgentDir, "rid-marker")
		require.NoError(t, err)
		assert.True(t, ok)

		// Repeat import on a fresh server (simulating restart) must be refused with 409.
		dstSrv2 := newTestServer(t)
		rec2 := doMoveBundleReq(t, dstSrv2, http.MethodPost, "/api/v1/agents/dev/move-bundle/import?projectId=p1", impReq)
		assert.Equal(t, http.StatusConflict, rec2.Code)
		assert.Contains(t, rec2.Body.String(), api.BrokerErrCodeMoveBundleAlreadyImported)
	})

	t.Run("crash mid-commit leaves .bundle-* siblings removed by marker-guarded delete", func(t *testing.T) {
		_, _, srcWS, srcHome := newMoveBundleTestAgentDir(t, "dev")
		require.NoError(t, os.WriteFile(filepath.Join(srcWS, "w.txt"), []byte("w"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(srcHome, "h.txt"), []byte("h"), 0o644))

		partsData := map[string][]byte{}
		wsRoot, err := os.OpenRoot(srcWS)
		require.NoError(t, err)
		wsRM, err := bundle.Write(context.Background(), wsRoot, t.TempDir(), bundle.WorkspaceExcluder(), bundle.DefaultWorkspaceLimits(),
			partSinkFunc(func(_ context.Context, idx int, r io.ReadSeeker, _ int64, _ string) error {
				b, err := io.ReadAll(r)
				partsData[fmt.Sprintf("workspace/%d", idx)] = b
				return err
			}))
		require.NoError(t, err)
		_ = wsRoot.Close()

		homeRoot, err := os.OpenRoot(srcHome)
		require.NoError(t, err)
		homeRM, err := bundle.Write(context.Background(), homeRoot, t.TempDir(), moveBundleHomeExcluder(), bundle.DefaultHomeLimits(),
			partSinkFunc(func(_ context.Context, idx int, r io.ReadSeeker, _ int64, _ string) error {
				b, err := io.ReadAll(r)
				partsData[fmt.Sprintf("home/%d", idx)] = b
				return err
			}))
		require.NoError(t, err)
		_ = homeRoot.Close()

		manifest := bundle.Manifest{
			Version:         bundle.ManifestVersion,
			ReincarnationID: "rid-crash",
			Roots: map[string]bundle.RootManifest{
				bundle.RootWorkspace: wsRM,
				bundle.RootHome:      homeRM,
			},
		}
		_, manifestHash, err := bundle.CanonicalManifest(manifest)
		require.NoError(t, err)

		objSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.URL.Path, "/")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(partsData[key])
		}))
		defer objSrv.Close()

		dstProject, dstAgentDir, _, _ := newMoveBundleTestAgentDir(t, "dev")
		// Simulate the move marker written by fresh-agent-dir provision.
		require.NoError(t, os.WriteFile(filepath.Join(dstAgentDir, agent.MoveMarkerFile), []byte("rid-crash\n"), 0o600))

		prevHook := testHookMoveBundleCommit
		testHookMoveBundleCommit = func(step string) error {
			if step == "before_home_commit" {
				return errMoveBundleCommitCrash
			}
			return nil
		}
		t.Cleanup(func() { testHookMoveBundleCommit = prevHook })

		dstSrv := newTestServer(t)
		dstSrv.moveBundle = moveBundleSettings{
			client: objSrv.Client(),
			fetchDownloadURLs: func(_ context.Context, _, _, _ string, _ *api.MoveBundleDownloadURLsRequest) (*api.MoveBundleDownloadURLsResponse, error) {
				var parts []api.MoveBundleDownloadPartURL
				for rootName, rm := range manifest.Roots {
					for _, p := range rm.Parts {
						parts = append(parts, api.MoveBundleDownloadPartURL{
							Root:    rootName,
							Index:   p.Index,
							URL:     fmt.Sprintf("%s/%s/%d", objSrv.URL, rootName, p.Index),
							Expires: time.Now().Add(15 * time.Minute),
						})
					}
				}
				return &api.MoveBundleDownloadURLsResponse{Parts: parts, ExpiresInSeconds: 900}, nil
			},
		}

		rec := doMoveBundleReq(t, dstSrv, http.MethodPost, "/api/v1/agents/dev/move-bundle/import?projectId=p1", api.MoveBundleImportRequest{
			ReincarnationID: "rid-crash",
			ProjectID:       "p1",
			ProjectPath:     dstProject,
			Manifest:        manifest,
			ManifestHash:    manifestHash,
		})
		require.Equal(t, http.StatusAccepted, rec.Code)
		st := waitForMoveBundleJob(t, dstSrv, "dev", "p1", api.MoveBundleJobKindImport, "rid-crash")
		assert.Equal(t, api.MoveBundleJobStateFailed, st.State)

		// .bundle-* staging siblings exist inside dstAgentDir and no imported marker was written.
		entries, err := os.ReadDir(dstAgentDir)
		require.NoError(t, err)
		var stageCount int
		for _, e := range entries {
			if isMoveBundleStageName(e.Name(), "workspace") || isMoveBundleStageName(e.Name(), "home") {
				stageCount++
			}
		}
		assert.Equal(t, 2, stageCount, "expected .bundle-* siblings after simulated crash")
		imported, err := hasMoveBundleImportedMarker(dstAgentDir, "rid-crash")
		require.NoError(t, err)
		assert.False(t, imported, "imported marker must not be written before both roots commit")

		// Marker-guarded rollback delete removes dstAgentDir including the .bundle-* siblings.
		_, err = agent.DeleteHubAgentFiles("dev", "dev", dstProject, "", false)
		require.NoError(t, err)
		_, err = os.Stat(dstAgentDir)
		assert.True(t, os.IsNotExist(err), "agentDir and .bundle-* siblings must be removed by rollback delete")
	})

	t.Run("ProvisionOnly validation for WorkspaceSource=move-bundle", func(t *testing.T) {
		srv := newTestServer(t)
		// WorkspaceSource="move-bundle" without fresh-agent-dir + moveReincarnationId -> 400.
		rec := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
			Name:            "dev",
			ProvisionOnly:   true,
			WorkspaceSource: api.WorkspaceSourceMoveBundle,
		})
		assert.Equal(t, http.StatusBadRequest, rec.Code)

		// Unsupported workspaceSource -> 400.
		rec2 := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents", CreateAgentRequest{
			Name:                       "dev",
			ProvisionOnly:              true,
			ExpectExistingNFSWorkspace: agent.MoveWorkspaceFreshAgentDir,
			MoveReincarnationID:        "rid-1",
			WorkspaceSource:            "bogus",
		})
		assert.Equal(t, http.StatusBadRequest, rec2.Code)
	})
}

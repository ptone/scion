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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer/bundle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMoveBundleTestAgentDir sets up a local project + agent dir with workspace
// and home subdirectories and returns (projectPath, agentDir, workspaceDir, homeDir).
func newMoveBundleTestAgentDir(t *testing.T, agentSlug string) (string, string, string, string) {
	t.Helper()
	projectRoot := t.TempDir()
	scionDir := filepath.Join(projectRoot, ".scion")
	require.NoError(t, os.MkdirAll(scionDir, 0o755))

	agentDir := config.GetAgentDir(scionDir, agentSlug, false)
	wsDir := filepath.Join(agentDir, "workspace")
	homeDir := config.GetAgentHomePath(scionDir, agentSlug)
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	require.NoError(t, os.MkdirAll(homeDir, 0o755))
	return scionDir, agentDir, wsDir, homeDir
}

func doMoveBundleReq(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func waitForMoveBundleJob(t *testing.T, srv *Server, agentSlug, projectID, kind, rid string) api.MoveBundleJobStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	path := fmt.Sprintf("/api/v1/agents/%s/move-bundle/%s/%s", agentSlug, kind, rid)
	if projectID != "" {
		path += "?projectId=" + projectID
	}
	for time.Now().Before(deadline) {
		rec := doMoveBundleReq(t, srv, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
		var st api.MoveBundleJobStatus
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
		if st.State != api.MoveBundleJobStateRunning {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s job %s", kind, rid)
	return api.MoveBundleJobStatus{}
}

func TestBrokerCapabilities_AdvertiseAgentMoveBundle(t *testing.T) {
	srv := newTestServer(t)
	rec := doMoveBundleReq(t, srv, http.MethodGet, "/api/v1/info", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp BrokerInfoResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Capabilities)
	assert.True(t, resp.Capabilities.AgentMoveBundle, "/api/v1/info must advertise AgentMoveBundle")
	assert.True(t, StaticCapabilities(nil, "").AgentMoveBundle, "StaticCapabilities must advertise AgentMoveBundle")
	assert.Contains(t, StaticCapabilityNames(nil), "agentMoveBundle")
}

func TestMoveBundlePrecheck_DurabilityAndEstimate(t *testing.T) {
	projectPath, _, wsDir, homeDir := newMoveBundleTestAgentDir(t, "dev")
	require.NoError(t, os.WriteFile(filepath.Join(wsDir, "hello.txt"), []byte("hello world"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, "notes.txt"), []byte("my notes"), 0o600))
	// Excluded secret in home should not be counted.
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".scion"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".scion", "secrets.json"), bytes.Repeat([]byte("s"), 4096), 0o600))

	t.Run("docker durable and within limits", func(t *testing.T) {
		srv := newTestServer(t)
		srv.moveBundle.durabilityRuntime = "docker"
		rec := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev/move-bundle/precheck", api.MoveBundlePrecheckRequest{
			ProjectPath: projectPath,
			Limits:      api.MoveBundleLimits{WorkspaceMaxBytes: 1 << 20, HomeMaxBytes: 1 << 20},
		})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp api.MoveBundlePrecheckResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.True(t, resp.Durable)
		assert.Empty(t, resp.Reason)
		assert.Empty(t, resp.Over)
		assert.Equal(t, int64(11), resp.Workspace.Bytes)
		assert.Equal(t, int64(8), resp.Home.Bytes)
	})

	t.Run("podman and apple container durable", func(t *testing.T) {
		for _, rt := range []string{"podman", "container", "apple"} {
			srv := newTestServer(t)
			srv.moveBundle.durabilityRuntime = rt
			rec := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev/move-bundle/precheck", api.MoveBundlePrecheckRequest{
				ProjectPath: projectPath,
			})
			require.Equal(t, http.StatusOK, rec.Code)
			var resp api.MoveBundlePrecheckResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.True(t, resp.Durable, "runtime %s should be durable", rt)
		}
	})

	t.Run("kubernetes and cloudrun not yet supported", func(t *testing.T) {
		for _, rt := range []string{"kubernetes", "cloudrun", "cloudrun-sandbox"} {
			srv := newTestServer(t)
			srv.moveBundle.durabilityRuntime = rt
			rec := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev/move-bundle/precheck", api.MoveBundlePrecheckRequest{
				ProjectPath: projectPath,
			})
			require.Equal(t, http.StatusOK, rec.Code)
			var resp api.MoveBundlePrecheckResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.False(t, resp.Durable, "runtime %s should not be durable in phase 1", rt)
			assert.Equal(t, "not yet supported", resp.Reason)
		}
	})

	t.Run("over-limit early stop", func(t *testing.T) {
		srv := newTestServer(t)
		srv.moveBundle.durabilityRuntime = "docker"
		rec := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev/move-bundle/precheck", api.MoveBundlePrecheckRequest{
			ProjectPath: projectPath,
			Limits:      api.MoveBundleLimits{WorkspaceMaxBytes: 5},
		})
		require.Equal(t, http.StatusOK, rec.Code)
		var resp api.MoveBundlePrecheckResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.True(t, resp.Durable)
		assert.Equal(t, "bytes", resp.Workspace.Over)
		assert.Equal(t, "workspace.bytes", resp.Over)
	})

	t.Run("invalid path returns durable=false with clear reason and no over field", func(t *testing.T) {
		badProj, _, badWS, _ := newMoveBundleTestAgentDir(t, "dev-badpath")
		require.NoError(t, os.WriteFile(filepath.Join(badWS, `bad\name.txt`), []byte("x"), 0o644))

		srv := newTestServer(t)
		srv.moveBundle.durabilityRuntime = "docker"
		rec := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev-badpath/move-bundle/precheck", api.MoveBundlePrecheckRequest{
			ProjectPath: badProj,
		})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp api.MoveBundlePrecheckResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.False(t, resp.Durable)
		assert.Contains(t, resp.Reason, "cannot be bundled")
		assert.Contains(t, resp.Reason, "backslash")
		assert.Empty(t, resp.Over)
		assert.Empty(t, resp.Workspace.Over)
	})
}

// TestMoveBundle_T6_ExportImportJobModelAndTransport covers design §9 T6:
//   - round-trip export + import over httptest object store
//   - idempotent POST with same rid
//   - 409 conflict on different rid while running
//   - timeout -> failed
//   - 404 for unknown/lost job after restart + GC after TTL
//   - URL redaction in errors (signed query token never appears)
//   - signed-URL refresh at expires-60s and on 401/403
func TestMoveBundle_T6_ExportImportJobModelAndTransport(t *testing.T) {
	t.Run("roundtrip export and import plus idempotent POST and 404 after restart", func(t *testing.T) {
		srcProject, srcAgentDir, srcWS, srcHome := newMoveBundleTestAgentDir(t, "dev")
		require.NoError(t, os.WriteFile(filepath.Join(srcWS, "main.go"), []byte("package main\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(srcWS, ".git", "refs"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(srcWS, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(srcHome, ".bash_history"), []byte("git status\n"), 0o600))
		// Persist a file-type secret target in scion-agent.json and place an
		// injected secret file + harness auth file in home; neither may be bundled.
		require.NoError(t, os.WriteFile(filepath.Join(srcAgentDir, "scion-agent.json"),
			[]byte(`{"user":"scion","secrets":[{"key":"my_api_key","type":"file","target":"~/.config/custom-app/secret.key"}]}`), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(srcHome, ".config", "custom-app"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(srcHome, ".config", "custom-app", "secret.key"), []byte("injected-secret-value"), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(srcHome, ".config", "hub-secret"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(srcHome, ".config", "hub-secret", "token.pem"), []byte("hub-injected-secret"), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(srcHome, ".claude"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(srcHome, ".claude", ".credentials.json"), []byte("claude-oauth-token"), 0o600))
		// D6 excluded identity credential must not be exported.
		require.NoError(t, os.MkdirAll(filepath.Join(srcHome, ".kube"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(srcHome, ".kube", "config"), []byte("secret-kubeconfig"), 0o600))

		// Fake object store backed by httptest.
		var storeMu sync.Mutex
		objects := map[string][]byte{}
		objSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.URL.Path, "/")
			switch r.Method {
			case http.MethodPut:
				data, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				storeMu.Lock()
				objects[key] = data
				storeMu.Unlock()
				w.WriteHeader(http.StatusOK)
			case http.MethodGet:
				storeMu.Lock()
				data, ok := objects[key]
				storeMu.Unlock()
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		}))
		defer objSrv.Close()

		srcSrv := newTestServer(t)
		srcSrv.moveBundle = moveBundleSettings{
			client: objSrv.Client(),
			fetchUploadURL: func(_ context.Context, _, _, rid string, req *api.MoveBundleUploadURLRequest) (*api.MoveBundleUploadURLResponse, error) {
				u := fmt.Sprintf("%s/%s/%s/part-%05d.tgz?X-Goog-Signature=TOPSECRETUPLOAD", objSrv.URL, rid, req.Root, req.PartIndex)
				return &api.MoveBundleUploadURLResponse{
					URL:     u,
					Method:  http.MethodPut,
					Headers: map[string]string{"Content-Type": "application/octet-stream"},
					Expires: time.Now().Add(15 * time.Minute),
				}, nil
			},
		}

		exportReq := api.MoveBundleExportRequest{
			ReincarnationID:   "rid-1",
			AgentID:           "agent-uuid-1",
			ProjectID:         "proj-1",
			ProjectPath:       srcProject,
			SecretFileTargets: []string{"~/.config/hub-secret/token.pem"},
			Limits:            api.MoveBundleLimits{PartSize: 1024},
		}
		rec := doMoveBundleReq(t, srcSrv, http.MethodPost, "/api/v1/agents/dev/move-bundle/export?projectId=proj-1", exportReq)
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

		// Idempotent POST with the same rid returns 202 with the same job.
		rec2 := doMoveBundleReq(t, srcSrv, http.MethodPost, "/api/v1/agents/dev/move-bundle/export?projectId=proj-1", exportReq)
		require.Equal(t, http.StatusAccepted, rec2.Code, rec2.Body.String())

		st := waitForMoveBundleJob(t, srcSrv, "dev", "proj-1", api.MoveBundleJobKindExport, "rid-1")
		require.Equal(t, api.MoveBundleJobStateSucceeded, st.State, "error=%s", st.Error)
		require.NotNil(t, st.Manifest)

		// Simulated broker restart (fresh server) returns 404 for lost job.
		restartedSrv := newTestServer(t)
		rec404 := doMoveBundleReq(t, restartedSrv, http.MethodGet, "/api/v1/agents/dev/move-bundle/export/rid-1?projectId=proj-1", nil)
		assert.Equal(t, http.StatusNotFound, rec404.Code)

		// Import onto a target broker.
		dstProject, _, dstWS, dstHome := newMoveBundleTestAgentDir(t, "dev")
		_, manifestHash, err := bundle.CanonicalManifest(*st.Manifest)
		require.NoError(t, err)

		dstSrv := newTestServer(t)
		dstSrv.moveBundle = moveBundleSettings{
			client: objSrv.Client(),
			fetchDownloadURLs: func(_ context.Context, _, _, rid string, _ *api.MoveBundleDownloadURLsRequest) (*api.MoveBundleDownloadURLsResponse, error) {
				var parts []api.MoveBundleDownloadPartURL
				for rootName, rm := range st.Manifest.Roots {
					for _, p := range rm.Parts {
						parts = append(parts, api.MoveBundleDownloadPartURL{
							Root:    rootName,
							Index:   p.Index,
							URL:     fmt.Sprintf("%s/%s/%s/part-%05d.tgz?X-Goog-Signature=TOPSECRETDL", objSrv.URL, rid, rootName, p.Index),
							Expires: time.Now().Add(15 * time.Minute),
						})
					}
				}
				return &api.MoveBundleDownloadURLsResponse{
					Parts:            parts,
					Expires:          time.Now().Add(15 * time.Minute),
					ExpiresInSeconds: 900,
				}, nil
			},
		}

		importReq := api.MoveBundleImportRequest{
			ReincarnationID: "rid-1",
			AgentID:         "agent-uuid-1",
			ProjectID:       "proj-1",
			ProjectPath:     dstProject,
			Manifest:        *st.Manifest,
			ManifestHash:    manifestHash,
		}
		impRec := doMoveBundleReq(t, dstSrv, http.MethodPost, "/api/v1/agents/dev/move-bundle/import?projectId=proj-1", importReq)
		require.Equal(t, http.StatusAccepted, impRec.Code, impRec.Body.String())

		impSt := waitForMoveBundleJob(t, dstSrv, "dev", "proj-1", api.MoveBundleJobKindImport, "rid-1")
		require.Equal(t, api.MoveBundleJobStateSucceeded, impSt.State, "error=%s", impSt.Error)

		gotMain, err := os.ReadFile(filepath.Join(dstWS, "main.go"))
		require.NoError(t, err)
		assert.Equal(t, "package main\n", string(gotMain))
		gotHist, err := os.ReadFile(filepath.Join(dstHome, ".bash_history"))
		require.NoError(t, err)
		assert.Equal(t, "git status\n", string(gotHist))
		// D6 excluded credential, injected secret file target, and harness auth file were never copied.
		_, err = os.Stat(filepath.Join(dstHome, ".kube", "config"))
		assert.True(t, os.IsNotExist(err))
		_, err = os.Stat(filepath.Join(dstHome, ".config", "custom-app", "secret.key"))
		assert.True(t, os.IsNotExist(err), "injected file-type secret target must be excluded from bundle")
		_, err = os.Stat(filepath.Join(dstHome, ".config", "hub-secret", "token.pem"))
		assert.True(t, os.IsNotExist(err), "hub-supplied secret file target must be excluded from bundle")
		_, err = os.Stat(filepath.Join(dstHome, ".claude", ".credentials.json"))
		assert.True(t, os.IsNotExist(err), "harness auth file target must be excluded from bundle")
	})

	t.Run("409 conflict on different rid while running", func(t *testing.T) {
		srcProject, _, srcWS, _ := newMoveBundleTestAgentDir(t, "dev")
		require.NoError(t, os.WriteFile(filepath.Join(srcWS, "a.txt"), []byte("data"), 0o644))

		block := make(chan struct{})
		entered := make(chan struct{}, 2)
		srv := newTestServer(t)
		srv.moveBundle = moveBundleSettings{
			fetchUploadURL: func(ctx context.Context, _, _, _ string, _ *api.MoveBundleUploadURLRequest) (*api.MoveBundleUploadURLResponse, error) {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-block:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return nil, context.Canceled
			},
		}

		rec1 := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev/move-bundle/export?projectId=p1", api.MoveBundleExportRequest{
			ReincarnationID: "rid-a",
			ProjectID:       "p1",
			ProjectPath:     srcProject,
		})
		require.Equal(t, http.StatusAccepted, rec1.Code)
		<-entered

		// Different rid while rid-a is running -> 409 Conflict.
		rec2 := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev/move-bundle/export?projectId=p1", api.MoveBundleExportRequest{
			ReincarnationID: "rid-b",
			ProjectID:       "p1",
			ProjectPath:     srcProject,
		})
		assert.Equal(t, http.StatusConflict, rec2.Code)
		assert.Contains(t, rec2.Body.String(), api.BrokerErrCodeMoveBundleJobConflict)

		close(block)
		_ = waitForMoveBundleJob(t, srv, "dev", "p1", api.MoveBundleJobKindExport, "rid-a")
	})

	t.Run("job timeout transitions to failed", func(t *testing.T) {
		srcProject, _, srcWS, _ := newMoveBundleTestAgentDir(t, "dev")
		require.NoError(t, os.WriteFile(filepath.Join(srcWS, "a.txt"), []byte("data"), 0o644))

		srv := newTestServer(t)
		srv.moveBundle = moveBundleSettings{
			fetchUploadURL: func(ctx context.Context, _, _, _ string, _ *api.MoveBundleUploadURLRequest) (*api.MoveBundleUploadURLResponse, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}

		rec := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev/move-bundle/export?projectId=p1", api.MoveBundleExportRequest{
			ReincarnationID: "rid-timeout",
			ProjectID:       "p1",
			ProjectPath:     srcProject,
			Limits:          api.MoveBundleLimits{TimeoutSeconds: 1},
		})
		require.Equal(t, http.StatusAccepted, rec.Code)

		st := waitForMoveBundleJob(t, srv, "dev", "p1", api.MoveBundleJobKindExport, "rid-timeout")
		assert.Equal(t, api.MoveBundleJobStateFailed, st.State)
		assert.Contains(t, st.Error, "timeout")
	})

	t.Run("URL redaction in export and import errors", func(t *testing.T) {
		const secretToken = "SUPER-SECRET-SIGNED-URL-TOKEN-98765"
		srcProject, _, srcWS, _ := newMoveBundleTestAgentDir(t, "dev")
		require.NoError(t, os.WriteFile(filepath.Join(srcWS, "a.txt"), []byte("data"), 0o644))

		// Object server returns 403 on PUT.
		objSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("SignatureDoesNotMatch " + secretToken))
		}))
		defer objSrv.Close()

		srv := newTestServer(t)
		srv.moveBundle = moveBundleSettings{
			client:      objSrv.Client(),
			partBackoff: []time.Duration{},
			fetchUploadURL: func(_ context.Context, _, _, _ string, _ *api.MoveBundleUploadURLRequest) (*api.MoveBundleUploadURLResponse, error) {
				return &api.MoveBundleUploadURLResponse{
					URL:     objSrv.URL + "/part?X-Goog-Signature=" + secretToken,
					Method:  http.MethodPut,
					Expires: time.Now().Add(15 * time.Minute),
				}, nil
			},
		}

		rec := doMoveBundleReq(t, srv, http.MethodPost, "/api/v1/agents/dev/move-bundle/export?projectId=p1", api.MoveBundleExportRequest{
			ReincarnationID: "rid-redact",
			ProjectID:       "p1",
			ProjectPath:     srcProject,
		})
		require.Equal(t, http.StatusAccepted, rec.Code)

		st := waitForMoveBundleJob(t, srv, "dev", "p1", api.MoveBundleJobKindExport, "rid-redact")
		assert.Equal(t, api.MoveBundleJobStateFailed, st.State)
		assert.NotContains(t, st.Error, secretToken, "signed URL token must never appear in job error")
		assert.NotContains(t, st.Error, objSrv.URL, "signed URL host must never appear in job error")
		assert.Contains(t, st.Error, "http_403")
	})

	t.Run("signed-URL refresh at expires-60s and on 403", func(t *testing.T) {
		// Create a multi-part bundle (workspace has 2 parts, home has 1 part).
		_, _, srcWS, srcHome := newMoveBundleTestAgentDir(t, "dev")
		// Incompressible-ish data so PartSize splits into multiple parts.
		require.NoError(t, os.WriteFile(filepath.Join(srcWS, "big.bin"), bytes.Repeat([]byte("abcdefgh12345678"), 256), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(srcHome, "h.txt"), []byte("home"), 0o644))

		partsData := map[string][]byte{}
		wsRoot, err := os.OpenRoot(srcWS)
		require.NoError(t, err)
		wsRM, err := bundle.Write(context.Background(), wsRoot, t.TempDir(), bundle.WorkspaceExcluder(), bundle.Limits{
			MaxBytes: 1 << 20, MaxEntries: 100, MaxDepth: 16, MaxPathLen: 256, PartSize: 64,
		}, partSinkFunc(func(_ context.Context, idx int, r io.ReadSeeker, _ int64, _ string) error {
			b, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			partsData[fmt.Sprintf("workspace/%d", idx)] = b
			return nil
		}))
		require.NoError(t, err)
		_ = wsRoot.Close()
		require.GreaterOrEqual(t, len(wsRM.Parts), 2, "need at least 2 workspace parts to test URL refresh")

		homeRoot, err := os.OpenRoot(srcHome)
		require.NoError(t, err)
		homeRM, err := bundle.Write(context.Background(), homeRoot, t.TempDir(), moveBundleHomeExcluder("", nil), bundle.Limits{
			MaxBytes: 1 << 20, MaxEntries: 100, MaxDepth: 16, MaxPathLen: 256, PartSize: 64,
		},
			partSinkFunc(func(_ context.Context, idx int, r io.ReadSeeker, _ int64, _ string) error {
				b, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				partsData[fmt.Sprintf("home/%d", idx)] = b
				return nil
			}))
		require.NoError(t, err)
		_ = homeRoot.Close()

		manifest := bundle.Manifest{
			Version:         bundle.ManifestVersion,
			ReincarnationID: "rid-refresh",
			Roots: map[string]bundle.RootManifest{
				bundle.RootWorkspace: wsRM,
				bundle.RootHome:      homeRM,
			},
		}
		_, manifestHash, err := bundle.CanonicalManifest(manifest)
		require.NoError(t, err)

		// Generation 1 URLs expire within 30s (< 60s refresh margin) OR return 403 on first hit;
		// Generation 2 URLs succeed with 15m TTL.
		var fetchCount atomic.Int32
		objSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("gen") == "1" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			key := strings.TrimPrefix(r.URL.Path, "/")
			data, ok := partsData[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		}))
		defer objSrv.Close()

		dstProject, _, dstWS, _ := newMoveBundleTestAgentDir(t, "dev")
		dstSrv := newTestServer(t)
		dstSrv.moveBundle = moveBundleSettings{
			client: objSrv.Client(),
			fetchDownloadURLs: func(_ context.Context, _, _, _ string, _ *api.MoveBundleDownloadURLsRequest) (*api.MoveBundleDownloadURLsResponse, error) {
				gen := fetchCount.Add(1)
				expSec := int64(900)
				if gen == 1 {
					// First generation expires in 30s (< 60s margin) AND returns 403 if used.
					expSec = 30
				}
				var parts []api.MoveBundleDownloadPartURL
				for rootName, rm := range manifest.Roots {
					for _, p := range rm.Parts {
						parts = append(parts, api.MoveBundleDownloadPartURL{
							Root:    rootName,
							Index:   p.Index,
							URL:     fmt.Sprintf("%s/%s/%d?gen=%d&sig=SECRET", objSrv.URL, rootName, p.Index, gen),
							Expires: time.Now().Add(time.Duration(expSec) * time.Second),
						})
					}
				}
				return &api.MoveBundleDownloadURLsResponse{
					Parts:            parts,
					Expires:          time.Now().Add(time.Duration(expSec) * time.Second),
					ExpiresInSeconds: expSec,
				}, nil
			},
		}

		rec := doMoveBundleReq(t, dstSrv, http.MethodPost, "/api/v1/agents/dev/move-bundle/import?projectId=p1", api.MoveBundleImportRequest{
			ReincarnationID: "rid-refresh",
			ProjectID:       "p1",
			ProjectPath:     dstProject,
			Manifest:        manifest,
			ManifestHash:    manifestHash,
			Limits:          api.MoveBundleLimits{PartSize: 64},
		})
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

		st := waitForMoveBundleJob(t, dstSrv, "dev", "p1", api.MoveBundleJobKindImport, "rid-refresh")
		require.Equal(t, api.MoveBundleJobStateSucceeded, st.State, "error=%s", st.Error)
		assert.GreaterOrEqual(t, fetchCount.Load(), int32(2), "expected signed URLs to be refreshed")

		got, err := os.ReadFile(filepath.Join(dstWS, "big.bin"))
		require.NoError(t, err)
		assert.Equal(t, bytes.Repeat([]byte("abcdefgh12345678"), 256), got)
	})

	t.Run("job registry GC removes finished jobs after 1h TTL", func(t *testing.T) {
		now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
		reg := newMoveBundleJobRegistry(func() time.Time { return now }, time.Hour)
		key := moveBundleAgentKey{ProjectID: "p1", AgentSlug: "dev", Kind: api.MoveBundleJobKindExport}
		job, conflict, created := reg.start(key, "rid-gc", func() {})
		require.True(t, created)
		require.False(t, conflict)
		job.markSucceeded(nil, nil, now)

		// Still present at +59m.
		now = now.Add(59 * time.Minute)
		_, ok := reg.get(key, "rid-gc")
		assert.True(t, ok)

		// Swept at +61m.
		now = now.Add(2 * time.Minute)
		_, ok = reg.get(key, "rid-gc")
		assert.False(t, ok)
	})
}

type partSinkFunc func(ctx context.Context, index int, part io.ReadSeeker, size int64, hash string) error

func (f partSinkFunc) Put(ctx context.Context, index int, part io.ReadSeeker, size int64, hash string) error {
	return f(ctx, index, part, size, hash)
}

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// --- #1075: factory wiring of workspace storage into the K8s runtime ---
//
// These tests exercise the REAL GetRuntime against a stub Kubernetes API
// server. Nothing here constructs KubernetesRuntime.WorkspaceStorage on the
// test side — the only way the assertions can pass is if the factory's
// "kubernetes"/"k8s" case copies it out of server settings. Deleting that
// assignment turns them red, which is the whole point: the defect in #1075
// was that the assignment never existed.

// startStubAPIServer starts an httptest server that answers the only call the
// factory makes against a cluster: Discovery().ServerVersion(), used by both
// Client.Verify() and Client.IsGKE(). gitVersion selects whether the stub
// looks like GKE ("v1.29.4-gke.1043002") or vanilla Kubernetes.
func startStubAPIServer(t *testing.T, gitVersion string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"major":"1","minor":"29","gitVersion":%q,"platform":"linux/amd64"}`, gitVersion)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeKubeconfig writes a kubeconfig pointing at the stub API server and
// returns its path.
func writeKubeconfig(t *testing.T, dir, server string) string {
	t.Helper()
	path := filepath.Join(dir, "kubeconfig")
	content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: stub
  cluster:
    server: %s
contexts:
- name: stub
  context:
    cluster: stub
    user: stub
current-context: stub
users:
- name: stub
  user: {}
`, server)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// setupK8sFactoryEnv points HOME at a temp dir holding a global settings.yaml
// with a kubernetes profile, and KUBECONFIG at a stub cluster. workspaceStorage
// is spliced into the settings file verbatim (indented under `server:`), or
// omitted entirely when empty.
func setupK8sFactoryEnv(t *testing.T, workspaceStorage string) {
	t.Helper()

	// Clear PATH so runtime auto-detection cannot override the settings-based
	// resolution on machines that happen to have docker/podman installed.
	t.Setenv("PATH", "")

	// Run from a scratch directory so the repository's own .scion settings
	// cannot leak into the resolved configuration.
	t.Chdir(t.TempDir())

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("SCION_GROVE", "")

	srv := startStubAPIServer(t, "v1.29.4")
	t.Setenv("KUBECONFIG", writeKubeconfig(t, tmpHome, srv.URL))

	globalDir := filepath.Join(tmpHome, ".scion")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global dir: %v", err)
	}

	serverBlock := ""
	if workspaceStorage != "" {
		serverBlock = "server:\n" + workspaceStorage
	}

	settings := fmt.Sprintf(`schema_version: "1"
active_profile: k8s
runtimes:
  kubernetes:
    type: kubernetes
    namespace: scion-agents
profiles:
  k8s:
    runtime: kubernetes
%s`, serverBlock)

	if err := os.WriteFile(filepath.Join(globalDir, "settings.yaml"), []byte(settings), 0o644); err != nil {
		t.Fatalf("write settings.yaml: %v", err)
	}
}

func TestGetRuntime_Kubernetes_WiresGKESharedVolumeStorage(t *testing.T) {
	setupK8sFactoryEnv(t, `  workspace_storage:
    backend: gke-shared-volume
    gke_shared_volume:
      volume_name: scion-workspaces
      pv_claim_name: scion-workspaces-pvc
      subpath_root: projects
`)

	rt := GetRuntime("", "k8s")

	k8sRT, ok := rt.(*KubernetesRuntime)
	if !ok {
		t.Fatalf("expected *KubernetesRuntime, got %T (%v)", rt, rt)
	}
	if k8sRT.WorkspaceStorage == nil {
		t.Fatal("KubernetesRuntime.WorkspaceStorage is nil — the factory did not thread " +
			"server.workspace_storage into the kubernetes runtime (#1075)")
	}
	if got, want := k8sRT.WorkspaceStorage.Backend, "gke-shared-volume"; got != want {
		t.Errorf("WorkspaceStorage.Backend = %q, want %q", got, want)
	}
	if k8sRT.WorkspaceStorage.GKESharedVolume == nil {
		t.Fatal("WorkspaceStorage.GKESharedVolume is nil")
	}
	if got, want := k8sRT.WorkspaceStorage.GKESharedVolume.PVClaimName, "scion-workspaces-pvc"; got != want {
		t.Errorf("GKESharedVolume.PVClaimName = %q, want %q", got, want)
	}
}

func TestGetRuntime_Kubernetes_WiresNFSStorage(t *testing.T) {
	setupK8sFactoryEnv(t, `  workspace_storage:
    backend: nfs
    nfs:
      mount_root: /mnt/nfs
      shares:
        - id: share0
          server: 10.0.0.2
          export: /scion-workspaces
          pv_name: scion-workspaces-pv
`)

	rt := GetRuntime("", "k8s")

	k8sRT, ok := rt.(*KubernetesRuntime)
	if !ok {
		t.Fatalf("expected *KubernetesRuntime, got %T (%v)", rt, rt)
	}
	if k8sRT.WorkspaceStorage == nil {
		t.Fatal("KubernetesRuntime.WorkspaceStorage is nil — the documented NFS-on-Kubernetes " +
			"path cannot engage without it (#1075)")
	}
	if got, want := k8sRT.WorkspaceStorage.Backend, "nfs"; got != want {
		t.Errorf("WorkspaceStorage.Backend = %q, want %q", got, want)
	}
	if k8sRT.WorkspaceStorage.NFS == nil || len(k8sRT.WorkspaceStorage.NFS.Shares) != 1 {
		t.Fatalf("expected one NFS share, got %+v", k8sRT.WorkspaceStorage.NFS)
	}
	if got, want := k8sRT.WorkspaceStorage.NFS.Shares[0].PVName, "scion-workspaces-pv"; got != want {
		t.Errorf("NFS.Shares[0].PVName = %q, want %q", got, want)
	}
}

func TestGetRuntime_Kubernetes_NoWorkspaceStorageConfigured(t *testing.T) {
	// No server block at all: the runtime must come back with nil storage
	// (and the pod path keeps today's EmptyDir behavior).
	setupK8sFactoryEnv(t, "")

	rt := GetRuntime("", "k8s")

	k8sRT, ok := rt.(*KubernetesRuntime)
	if !ok {
		t.Fatalf("expected *KubernetesRuntime, got %T (%v)", rt, rt)
	}
	if k8sRT.WorkspaceStorage != nil {
		t.Errorf("expected nil WorkspaceStorage when none is configured, got %+v", k8sRT.WorkspaceStorage)
	}
	// Sanity: the rest of the kubernetes case still ran.
	if got, want := k8sRT.DefaultNamespace, "scion-agents"; got != want {
		t.Errorf("DefaultNamespace = %q, want %q — factory did not apply runtime config", got, want)
	}
}

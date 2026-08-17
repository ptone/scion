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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// --- #1075: Run() must derive the pod's workspace volume from the runtime's
// workspace storage config ---
//
// These tests go through the real Run(), not buildPod, and construct a
// RunConfig with EMPTY workspace fields — the state production is always in.
// The only path from the runtime's WorkspaceStorage to the pod spec is
// Run -> applyWorkspaceStorage, so removing that call (or the derivation
// inside it) changes the measured volume from a PVC to an EmptyDir.

// runAndCapturePod runs the agent against the fake clientset and returns the
// pod that was created. Run always errors here — waitForPodReady never sees a
// Ready pod from a fake clientset — but the pod spec has already been
// submitted by then, which is what we assert on.
func runAndCapturePod(t *testing.T, r *KubernetesRuntime, cfg RunConfig) *corev1.Pod {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, runErr := r.Run(ctx, cfg)
	if runErr == nil {
		t.Fatal("expected Run to fail at waitForPodReady with a fake clientset")
	}

	pods, err := r.Client.Clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected exactly 1 pod created, got %d (Run error: %v)", len(pods.Items), runErr)
	}
	return &pods.Items[0]
}

// workspaceVolume returns the pod's workspace volume and the agent
// container's mount of it.
func workspaceVolume(t *testing.T, pod *corev1.Pod) (corev1.Volume, corev1.VolumeMount) {
	t.Helper()

	var vol corev1.Volume
	found := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == "workspace" {
			vol, found = v, true
			break
		}
	}
	if !found {
		t.Fatal("no workspace volume in pod spec")
	}

	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.Name == "workspace" {
			return vol, m
		}
	}
	t.Fatal("no workspace volume mount on the agent container")
	return corev1.Volume{}, corev1.VolumeMount{}
}

// pvcClaimName returns the claim name of the workspace volume, or "" when the
// workspace is an EmptyDir. This is the value that #1075 pinned to "": under
// the bug no configuration could move it.
func pvcClaimName(vol corev1.Volume) string {
	if vol.PersistentVolumeClaim == nil {
		return ""
	}
	return vol.PersistentVolumeClaim.ClaimName
}

func gkeSharedVolumeRuntime(t *testing.T) *KubernetesRuntime {
	t.Helper()
	r := newNFSTestK8sRuntime()
	r.WorkspaceStorage = &config.V1WorkspaceStorageConfig{
		Backend: "gke-shared-volume",
		GKESharedVolume: &config.V1GKESharedVolumeConfig{
			VolumeName:  "scion-workspaces",
			PVClaimName: "scion-workspaces-pvc",
			SubPathRoot: "projects",
		},
	}
	return r
}

// baseRunConfig is the production shape: nothing workspace-storage related is
// set by the caller. Under #1075 this is the only shape that ever reached the
// K8s runtime.
func baseRunConfig(name string, env ...string) RunConfig {
	return RunConfig{
		Name:         name,
		Image:        "test-image",
		UnixUsername: "scion",
		ProjectID:    "proj-1075",
		Env:          env,
	}
}

func TestRun_GKESharedVolume_WorkspaceIsPVCNotEmptyDir(t *testing.T) {
	r := gkeSharedVolumeRuntime(t)

	pod := runAndCapturePod(t, r, baseRunConfig("scion-test-gke-shared"))
	vol, mount := workspaceVolume(t, pod)

	// Correct: "scion-workspaces-pvc". Broken (#1075, or Run no longer
	// calling applyWorkspaceStorage): "" — the volume is an EmptyDir.
	if got, want := pvcClaimName(vol), "scion-workspaces-pvc"; got != want {
		t.Errorf("workspace PVC claim name = %q, want %q (EmptyDir=%v)", got, want, vol.EmptyDir != nil)
	}
	if vol.EmptyDir != nil {
		t.Error("workspace volume is an EmptyDir; gke-shared-volume must use the shared PVC")
	}
	if got, want := mount.SubPath, "projects/proj-1075/workspace"; got != want {
		t.Errorf("workspace mount subPath = %q, want %q", got, want)
	}
}

func TestRun_NFS_WorkspaceIsPVCNotEmptyDir(t *testing.T) {
	r := newNFSTestK8sRuntime()
	r.WorkspaceStorage = &config.V1WorkspaceStorageConfig{
		Backend: "nfs",
		NFS: &config.V1NFSConfig{
			MountRoot:   "/mnt/nfs",
			SubPathRoot: "projects",
			UID:         1234,
			GID:         5678,
			Shares: []config.V1NFSShare{{
				ID:     "share0",
				Server: "10.0.0.2",
				Export: "/scion-workspaces",
				PVName: "scion-workspaces-pv",
			}},
		},
	}

	pod := runAndCapturePod(t, r, baseRunConfig("scion-test-nfs-wired"))
	vol, mount := workspaceVolume(t, pod)

	// Correct: "scion-workspaces-pv". Broken: "" (EmptyDir).
	if got, want := pvcClaimName(vol), "scion-workspaces-pv"; got != want {
		t.Errorf("workspace PVC claim name = %q, want %q (EmptyDir=%v)", got, want, vol.EmptyDir != nil)
	}
	if got, want := mount.SubPath, "projects/proj-1075/workspace"; got != want {
		t.Errorf("workspace mount subPath = %q, want %q", got, want)
	}
	// NFS UID/GID travel with the backend config: fsGroup is the configured
	// GID, not the broker host's.
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.FSGroup == nil {
		t.Fatal("pod has no fsGroup")
	}
	if got, want := *pod.Spec.SecurityContext.FSGroup, int64(5678); got != want {
		t.Errorf("fsGroup = %d, want %d (configured nfs.gid)", got, want)
	}
}

func TestRun_ClonePerAgent_WorkspaceStaysEmptyDir(t *testing.T) {
	// clone-per-agent promises each agent its own clone, so it must never be
	// handed the shared volume even when one is configured. The mode arrives
	// in the agent env; if the runtime stops reading that env key the mode
	// silently defaults to shared-plain and this pod gets the shared PVC.
	r := gkeSharedVolumeRuntime(t)

	pod := runAndCapturePod(t, r,
		baseRunConfig("scion-test-clone-per-agent", "SCION_WORKSPACE_MODE=clone-per-agent"))
	vol, mount := workspaceVolume(t, pod)

	// Correct: "" (EmptyDir). Broken (env key not read): "scion-workspaces-pvc".
	if got := pvcClaimName(vol); got != "" {
		t.Errorf("clone-per-agent pod got shared PVC %q; isolation requires a node-local workspace", got)
	}
	if vol.EmptyDir == nil {
		t.Errorf("clone-per-agent workspace volume should be EmptyDir, got %+v", vol.VolumeSource)
	}
	if mount.SubPath != "" {
		t.Errorf("clone-per-agent mount subPath = %q, want empty", mount.SubPath)
	}
}

func TestRun_WorktreePerAgent_WorkspaceIsPVC(t *testing.T) {
	// The counterpart to the clone-per-agent case: a mode that DOES share
	// storage still reaches the PVC. Together the two prove the env value is
	// read rather than ignored in a direction that happens to look right.
	r := gkeSharedVolumeRuntime(t)

	pod := runAndCapturePod(t, r,
		baseRunConfig("scion-test-worktree-per-agent", "SCION_WORKSPACE_MODE=worktree-per-agent"))
	vol, _ := workspaceVolume(t, pod)

	if got, want := pvcClaimName(vol), "scion-workspaces-pvc"; got != want {
		t.Errorf("workspace PVC claim name = %q, want %q", got, want)
	}
}

func TestRun_NoWorkspaceStorage_WorkspaceStaysEmptyDir(t *testing.T) {
	// No storage configured: today's behavior, unchanged.
	r := newNFSTestK8sRuntime()

	pod := runAndCapturePod(t, r, baseRunConfig("scion-test-no-storage"))
	vol, mount := workspaceVolume(t, pod)

	if got := pvcClaimName(vol); got != "" {
		t.Errorf("unconfigured runtime produced PVC %q, want EmptyDir", got)
	}
	if vol.EmptyDir == nil {
		t.Errorf("workspace volume should be EmptyDir, got %+v", vol.VolumeSource)
	}
	if mount.SubPath != "" {
		t.Errorf("mount subPath = %q, want empty", mount.SubPath)
	}
}

func TestRun_MisconfiguredSharedBackend_FailsInsteadOfEmptyDir(t *testing.T) {
	// A shared backend that cannot resolve must fail the launch. Falling back
	// to an EmptyDir would reproduce the failure mode #1075 is about:
	// configuration accepted, silently doing nothing.
	r := newNFSTestK8sRuntime()
	r.WorkspaceStorage = &config.V1WorkspaceStorageConfig{
		Backend: "gke-shared-volume",
		GKESharedVolume: &config.V1GKESharedVolumeConfig{
			// volume_name missing
			PVClaimName: "scion-workspaces-pvc",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := r.Run(ctx, baseRunConfig("scion-test-misconfigured"))
	if err == nil {
		t.Fatal("expected Run to fail for a shared backend that cannot resolve")
	}
	if !strings.Contains(err.Error(), "gke-shared-volume") {
		t.Errorf("error should name the backend, got: %v", err)
	}

	pods, listErr := r.Client.Clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if listErr != nil {
		t.Fatalf("list pods: %v", listErr)
	}
	if len(pods.Items) != 0 {
		t.Errorf("misconfigured backend should create no pods, got %d", len(pods.Items))
	}
}

func TestRun_MissingProjectID_FailsInsteadOfEmptyDir(t *testing.T) {
	// The subPath is per-project; without a project ID the backend cannot
	// isolate the workspace, so the launch must fail rather than mount the
	// volume root or fall back to an EmptyDir.
	r := gkeSharedVolumeRuntime(t)
	cfg := baseRunConfig("scion-test-no-project")
	cfg.ProjectID = ""

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := r.Run(ctx, cfg); err == nil {
		t.Fatal("expected Run to fail when the shared backend has no ProjectID to scope the subPath")
	}
}

func TestWorkspaceSharingModeFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want store.WorkspaceSharingMode
	}{
		{"absent defaults to shared-plain", nil, store.SharingModeSharedPlain},
		{"shared-plain", []string{"SCION_WORKSPACE_MODE=shared-plain"}, store.SharingModeSharedPlain},
		{"clone-per-agent", []string{"SCION_WORKSPACE_MODE=clone-per-agent"}, store.SharingModeClonePerAgent},
		{"worktree-per-agent", []string{"SCION_WORKSPACE_MODE=worktree-per-agent"}, store.SharingModeWorktreePerAgent},
		{"wire format 'per-agent'", []string{"SCION_WORKSPACE_MODE=per-agent"}, store.SharingModeClonePerAgent},
		{"unknown defaults to shared-plain", []string{"SCION_WORKSPACE_MODE=nonsense"}, store.SharingModeSharedPlain},
		{"found among other vars", []string{"FOO=bar", "SCION_WORKSPACE_MODE=clone-per-agent", "BAZ=qux"}, store.SharingModeClonePerAgent},
		{"value containing '='", []string{"SCION_WORKSPACE_MODE=clone-per-agent=x"}, store.SharingModeSharedPlain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workspaceSharingModeFromEnv(tt.env); got != tt.want {
				t.Errorf("workspaceSharingModeFromEnv(%v) = %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}

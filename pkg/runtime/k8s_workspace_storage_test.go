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

	"github.com/GoogleCloudPlatform/scion/pkg/api"
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
		if _, err := fmt.Fprintf(w, `{"major":"1","minor":"29","gitVersion":%q,"platform":"linux/amd64"}`, gitVersion); err != nil {
			t.Errorf("stub API server: write /version response: %v", err)
		}
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

// listPods returns every pod the fake clientset has recorded.
func listPods(t *testing.T, r *KubernetesRuntime) []corev1.Pod {
	t.Helper()
	pods, err := r.Client.Clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	return pods.Items
}

// runAndCapturePod runs the agent against the fake clientset and returns the
// pod that was created. Run cannot complete here — waitForPodReady never sees
// a Ready pod from a fake clientset — so it runs in the background until the
// pod spec has been submitted, which is what we assert on, and is then
// cancelled. Waiting for the pod rather than for a fixed deadline keeps the
// test fast and keeps a loaded machine from turning it into a flake.
func runAndCapturePod(t *testing.T, r *KubernetesRuntime, cfg RunConfig) *corev1.Pod {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, cfg)
		done <- err
	}()

	deadline := time.After(10 * time.Second)
	for {
		if pods := listPods(t, r); len(pods) == 1 {
			cancel()
			<-done
			return &pods[0]
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned before creating a pod: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for the pod to be created")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// runExpectingFailure drives Run for a configuration that must not reach pod
// creation, and returns the error. The generous deadline is never consumed
// when the code is correct — it only bounds the failure case, where Run would
// otherwise sit in waitForPodReady.
func runExpectingFailure(t *testing.T, r *KubernetesRuntime, cfg RunConfig, why string) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := r.Run(ctx, cfg)
	if err == nil {
		t.Fatalf("expected Run to fail: %s", why)
	}
	return err
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

	err := runExpectingFailure(t, r, baseRunConfig("scion-test-misconfigured"),
		"a shared backend that cannot resolve")
	if !strings.Contains(err.Error(), "gke-shared-volume") {
		t.Errorf("error should name the backend, got: %v", err)
	}
	if n := len(listPods(t, r)); n != 0 {
		t.Errorf("misconfigured backend should create no pods, got %d", n)
	}
}

func TestRun_SharedBackendWithoutPVClaimName_FailsInsteadOfEmptyDir(t *testing.T) {
	// A backend that resolves and realizes but yields no claim name is the
	// nastiest shape of misconfiguration: without the guard the pod comes up
	// with an EmptyDir AND the workspace sync skipped, which is strictly worse
	// than #1075. Delete the desc.PVClaimName == "" check in
	// applyWorkspaceStorage and both of these go red.
	tests := []struct {
		name    string
		storage *config.V1WorkspaceStorageConfig
		backend string
	}{
		{
			name: "gke-shared-volume without pv_claim_name",
			storage: &config.V1WorkspaceStorageConfig{
				Backend: "gke-shared-volume",
				GKESharedVolume: &config.V1GKESharedVolumeConfig{
					VolumeName:  "scion-workspaces",
					SubPathRoot: "projects",
					// pv_claim_name missing: Resolve and Realize both succeed
				},
			},
			backend: "gke-shared-volume",
		},
		{
			name: "nfs without shares[0].pv_name",
			storage: &config.V1WorkspaceStorageConfig{
				Backend: "nfs",
				NFS: &config.V1NFSConfig{
					MountRoot:   "/mnt/nfs",
					SubPathRoot: "projects",
					Shares: []config.V1NFSShare{{
						ID:     "share0",
						Server: "10.0.0.2",
						Export: "/scion-workspaces",
						// pv_name missing
					}},
				},
			},
			backend: "nfs",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newNFSTestK8sRuntime()
			r.WorkspaceStorage = tc.storage

			err := runExpectingFailure(t, r, baseRunConfig("scion-test-no-claim"),
				"a shared backend that resolved no PVC name")
			if !strings.Contains(err.Error(), tc.backend) {
				t.Errorf("error should name the backend %q, got: %v", tc.backend, err)
			}
			// The operator has to be told which setting to fill in.
			if !strings.Contains(err.Error(), "pv_claim_name") || !strings.Contains(err.Error(), "pv_name") {
				t.Errorf("error should name the setting to fix, got: %v", err)
			}
			if n := len(listPods(t, r)); n != 0 {
				t.Errorf("expected no pod to be created, got %d", n)
			}
		})
	}
}

func TestRun_MissingProjectID_FailsInsteadOfEmptyDir(t *testing.T) {
	// The subPath is per-project; without a project ID the backend cannot
	// isolate the workspace, so the launch must fail rather than mount the
	// volume root or fall back to an EmptyDir.
	r := gkeSharedVolumeRuntime(t)
	cfg := baseRunConfig("scion-test-no-project")
	cfg.ProjectID = ""

	err := runExpectingFailure(t, r, cfg,
		"the shared backend has no ProjectID to scope the subPath")
	if !strings.Contains(err.Error(), "gke-shared-volume") {
		t.Errorf("error should name the backend, got: %v", err)
	}
	if n := len(listPods(t, r)); n != 0 {
		t.Errorf("expected no pod to be created, got %d", n)
	}
}

// --- The workspace-sync decision on shared storage ---
//
// These pin the decision function rather than the branch in Run: the sync
// stage runs after waitForPodReady, and a fake clientset can neither make a
// pod Ready nor serve the exec subresource the sync uses (its RESTClient()
// panics in rest.NewRequest). See the PR body's known-gap note.

func TestShouldSyncWorkspaceToPod(t *testing.T) {
	sharedCfg := func(name string, env ...string) RunConfig {
		return RunConfig{
			Name:                 name,
			Workspace:            "/host/projects/demo",
			WorkspaceBackendName: "gke-shared-volume",
			NFSPVClaimName:       "scion-workspaces-pvc",
			Env:                  env,
		}
	}

	withInitClone := sharedCfg("init-container")
	withInitClone.GitCloneForInit = &api.GitCloneConfig{URL: "https://example.com/demo.git"}

	tests := []struct {
		name string
		cfg  RunConfig
		want bool
		why  string
	}{
		{
			name: "shared volume, no populating mechanism",
			cfg:  sharedCfg("non-git"),
			want: true,
			why:  "nothing else puts the project bytes on the volume, so skipping leaves /workspace empty",
		},
		{
			name: "shared volume, container clones itself",
			cfg:  sharedCfg("git-backed", "SCION_GIT_CLONE_URL=https://example.com/demo.git"),
			want: false,
			why:  "the container populates the volume during init",
		},
		{
			name: "shared volume, provisioning init container",
			cfg:  withInitClone,
			want: false,
			why:  "the init container populates the volume before the agent starts",
		},
		{
			name: "shared volume, clone URL present but empty",
			cfg:  sharedCfg("empty-clone-url", "SCION_GIT_CLONE_URL="),
			want: true,
			why:  "an empty value clones nothing",
		},
		{
			name: "local backend keeps today's behaviour",
			cfg:  RunConfig{Name: "local", Workspace: "/host/projects/demo"},
			want: true,
			why:  "the EmptyDir has no other source of bytes",
		},
		{
			name: "local backend, git-backed, still syncs",
			cfg:  RunConfig{Name: "local-git", Workspace: "/host/p", Env: []string{"SCION_GIT_CLONE_URL=https://x/y.git"}},
			want: true,
			why:  "the shared-storage reasoning must not change local pods",
		},
		{
			name: "shared backend without a claim name is not a shared volume",
			cfg:  RunConfig{Name: "no-claim", Workspace: "/host/p", WorkspaceBackendName: "nfs"},
			want: true,
			why:  "no PVC is mounted, so the pod is on an EmptyDir",
		},
		{
			name: "no host workspace, nothing to copy",
			cfg:  RunConfig{Name: "no-workspace", WorkspaceBackendName: "nfs", NFSPVClaimName: "pvc"},
			want: false,
			why:  "there is no source path to sync from",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSyncWorkspaceToPod(tc.cfg); got != tc.want {
				t.Errorf("shouldSyncWorkspaceToPod() = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

func TestWorkspaceListingIsEmpty(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{"empty directory", "", true},
		{"newline only", "\n", true},
		{"whitespace only", "  \n\t", true},
		{"a file", "README.md\n", false},
		{"a lone .git", ".git\n", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspaceListingIsEmpty(tc.out); got != tc.want {
				t.Errorf("workspaceListingIsEmpty(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// TestSharedWorkspaceNeedsSeeding_ChecksUnavailable pins the failure policy:
// when the volume cannot be inspected we seed it. An unpopulated workspace is
// the worse outcome, and the failure surfaces through the sync rather than as
// a pod that quietly came up empty.
func TestSharedWorkspaceNeedsSeeding_ChecksUnavailable(t *testing.T) {
	r := gkeSharedVolumeRuntime(t) // fake clientset: execInPod cannot run
	cfg := baseRunConfig("scion-test-seed-check")
	cfg.Workspace = "/host/projects/demo"
	cfg.WorkspaceBackendName = "gke-shared-volume"
	cfg.NFSPVClaimName = "scion-workspaces-pvc"

	if !r.sharedWorkspaceNeedsSeeding(context.Background(), "default", "some-pod", cfg) {
		t.Error("an unavailable volume check must fall back to seeding, not to skipping the sync")
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

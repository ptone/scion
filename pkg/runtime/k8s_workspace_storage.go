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
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// workspaceModeEnvKey is the agent env var carrying the project's resolved
// workspace sharing mode. The broker injects it on every dispatch
// (runtimebroker/start_context.go, defaulting to shared-plain) and the hub
// sets it in httpdispatcher.go, so it is the mode signal available to a
// runtime at Run time.
const workspaceModeEnvKey = "SCION_WORKSPACE_MODE"

// gitCloneURLEnvKey is the agent env var the broker sets for a git-backed
// project (runtimebroker/start_context.go). The container clones itself from
// it during init (cmd/sciontool/commands/init.go), which is what populates a
// shared workspace volume in the absence of the provisioning init container.
const gitCloneURLEnvKey = "SCION_GIT_CLONE_URL"

// sharedWorkspaceBackend reports whether a backend name denotes shared,
// cluster-attached workspace storage as opposed to node-local storage.
// Both shared backends realize as a PVC in the pod spec; the difference is
// only who provisions the volume (an operator-managed NFS export vs a
// GKE-managed shared volume such as Filestore CSI).
func sharedWorkspaceBackend(name string) bool {
	return name == "nfs" || name == "gke-shared-volume"
}

// usesSharedWorkspacePVC reports whether this pod's workspace is served by a
// shared PVC rather than the node-local EmptyDir. Every shared-workspace
// branch in the K8s pod path tests exactly this — the advisory lock, the
// workspace sync skip, the shared-dir PVCs, fsGroup, the workspace volume and
// the provisioning init container — so a future backend is admitted at all of
// them at once rather than at seven of eight.
func usesSharedWorkspacePVC(cfg RunConfig) bool {
	return sharedWorkspaceBackend(cfg.WorkspaceBackendName) && cfg.NFSPVClaimName != ""
}

// workspacePopulatedOnVolume reports whether something other than the
// kubectl-cp sync puts the project's bytes on the shared volume: the
// provisioning init container (N2-2), or the container cloning itself from the
// broker's clone URL during init.
//
// This is the property the workspace-sync skip must track. Tracking the
// backend *name* instead is what made the skip fire for pods that had no
// populating mechanism at all, leaving /workspace empty.
func workspacePopulatedOnVolume(cfg RunConfig) bool {
	if cfg.GitCloneForInit != nil {
		return true
	}
	for _, e := range cfg.Env {
		if k, v, ok := strings.Cut(e, "="); ok && k == gitCloneURLEnvKey && v != "" {
			return true
		}
	}
	return false
}

// shouldSyncWorkspaceToPod reports whether Run should copy the host workspace
// into the pod. It is the sync decision that can be made before the pod
// exists; shared volumes take one further runtime check (see
// sharedWorkspaceNeedsSeeding).
//
// Local-backend pods are unaffected by the shared-storage logic and keep
// today's behaviour exactly: sync whenever there is a host workspace.
func shouldSyncWorkspaceToPod(cfg RunConfig) bool {
	if cfg.Workspace == "" {
		return false
	}
	return !(usesSharedWorkspacePVC(cfg) && workspacePopulatedOnVolume(cfg))
}

// workspaceListingIsEmpty interprets the output of listing the pod's workspace
// directory. Anything at all on the volume — including a lone .git — means a
// previous agent already populated it.
func workspaceListingIsEmpty(lsOutput string) bool {
	return strings.TrimSpace(lsOutput) == ""
}

// sharedWorkspaceNeedsSeeding reports whether the host copy should be extracted
// onto the shared volume, which is true only while the volume is still empty.
//
// A shared volume is not a fresh EmptyDir. It outlives the pod and, in
// shared-plain, every agent in the project mounts the same subPath
// (projects/<pid>/workspace — the backends put no agent component in it). The
// host copy is refreshed only when someone runs `scion sync --direction from`;
// nothing syncs pod→host automatically. So re-extracting it on each start
// would restore stale bytes over whatever the agents currently working in that
// directory have written — syncToPod runs `tar -xz`, which overwrites
// same-named files in place.
//
// Seeding once while the directory is empty gives the first agent a populated
// workspace and every later agent the shared one, with nothing overwritten.
//
// If the check itself fails we seed: an unpopulated workspace is the worse
// outcome, and a broken exec channel surfaces as a sync error rather than as a
// pod that quietly came up empty.
func (r *KubernetesRuntime) sharedWorkspaceNeedsSeeding(ctx context.Context, namespace, podName string, cfg RunConfig) bool {
	out, err := r.execInPod(ctx, namespace, podName, []string{"sh", "-c", "ls -A /workspace 2>/dev/null | head -1"})
	if err != nil {
		runtimeLog.Warn("Could not inspect shared workspace volume; seeding it from the host copy",
			"agent", cfg.Name, "backend", cfg.WorkspaceBackendName, "error", err)
		return true
	}
	if workspaceListingIsEmpty(out) {
		return true
	}
	runtimeLog.Info("Shared workspace volume already populated; leaving it as it is",
		"agent", cfg.Name, "backend", cfg.WorkspaceBackendName,
		"pvc", cfg.NFSPVClaimName, "subpath", cfg.NFSSubPath, "phase", "workspace-sync-skip")
	return false
}

// workspaceSharingModeFromEnv reads the project's workspace sharing mode from
// the agent env. An absent or unrecognized value resolves to shared-plain,
// which is the same default the broker applies.
func workspaceSharingModeFromEnv(env []string) store.WorkspaceSharingMode {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == workspaceModeEnvKey {
			return store.ResolveWorkspaceSharingMode(v)
		}
	}
	return store.ResolveWorkspaceSharingMode("")
}

// applyWorkspaceStorage derives the workspace storage fields of a RunConfig
// from the runtime's configured backend and returns the updated copy. It is
// the bridge between server settings (threaded onto the runtime by the
// factory) and the RunConfig fields that the pod path reads — without it,
// every shared-workspace branch below is unreachable and pods always get an
// EmptyDir (#1075).
//
// It is a no-op when no workspace storage is configured, and when the
// selected backend is node-local. clone-per-agent projects always resolve to
// the local backend (SelectWorkspaceBackend), which is deliberate: that mode
// promises the agent a clone of its own, so it must not be handed the shared
// volume.
//
// A misconfigured shared backend is an error, not a silent downgrade: an
// operator who asks for shared workspace storage and gets an ephemeral
// EmptyDir instead has no way to notice.
func (r *KubernetesRuntime) applyWorkspaceStorage(cfg RunConfig) (RunConfig, error) {
	if r.WorkspaceStorage == nil {
		return cfg, nil
	}

	mode := workspaceSharingModeFromEnv(cfg.Env)
	backend := SelectWorkspaceBackend(r.WorkspaceStorage, mode)
	if !sharedWorkspaceBackend(backend.Name()) {
		return cfg, nil
	}

	sharedDirNames := make([]string, 0, len(cfg.SharedDirs))
	for _, sd := range cfg.SharedDirs {
		sharedDirNames = append(sharedDirNames, sd.Name)
	}

	resolved, err := backend.Resolve(ResolveInput{
		ProjectID:      cfg.ProjectID,
		ProjectDir:     cfg.Workspace,
		Mode:           mode,
		SharedDirNames: sharedDirNames,
	})
	if err != nil {
		return cfg, fmt.Errorf("workspace storage backend %q: %w", backend.Name(), err)
	}

	desc, err := backend.Realize(RealizeInput{
		Resolved:           resolved,
		ContainerWorkspace: cfg.ContainerWorkspace,
	})
	if err != nil {
		return cfg, fmt.Errorf("workspace storage backend %q: %w", backend.Name(), err)
	}

	if desc.PVClaimName == "" {
		return cfg, fmt.Errorf("workspace storage backend %q resolved no PersistentVolumeClaim name; "+
			"set nfs.shares[0].pv_name (nfs) or gke_shared_volume.pv_claim_name (gke-shared-volume)",
			backend.Name())
	}

	cfg.WorkspaceBackendName = backend.Name()
	cfg.NFSPVClaimName = desc.PVClaimName
	cfg.NFSSubPath = desc.SubPath

	// Only NFSGID is read on the K8s path (the fsGroup branch in buildPod).
	// NFSUID and NFSStorageClass are deliberately left alone: the docker and
	// cloudrun paths read them, this one does not, and setting fields nothing
	// consumes is how wiring comes to look live when it is not.
	if nfs := r.WorkspaceStorage.NFS; nfs != nil && backend.Name() == "nfs" {
		cfg.NFSGID = nfs.GID
	}

	runtimeLog.Info("Workspace storage resolved for pod",
		"agent", cfg.Name, "project_id", cfg.ProjectID, "backend", cfg.WorkspaceBackendName,
		"pvc", cfg.NFSPVClaimName, "subpath", cfg.NFSSubPath, "mode", string(mode))

	return cfg, nil
}

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
// branch in the K8s pod path tests exactly this, and this is the full list of
// them: the advisory lock, the workspace-sync skip and its seeding check, the
// shared-dir PVC creation no-op, the fsGroup choice, the workspace volume and
// its subPath mount, the provisioning init container, and the shared-dir
// subPath mounts. A future backend admitted here is admitted at all of them at
// once, which is what stops it being admitted at all but one.
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
	return !usesSharedWorkspacePVC(cfg) || !workspacePopulatedOnVolume(cfg)
}

// workspaceListingIsEmpty interprets the output of listing the pod's workspace
// directory. Anything at all on the volume — including a lone .git — means a
// previous agent already populated it.
func workspaceListingIsEmpty(lsOutput string) bool {
	return strings.TrimSpace(lsOutput) == ""
}

// sharedWorkspaceNeedsSeeding reports whether the host copy should be extracted
// onto the shared volume, which is true only while the volume is still empty.
// It takes the listing as a function value rather than reaching for the exec
// subresource itself; see workspaceSyncDeps for why.
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
func sharedWorkspaceNeedsSeeding(ctx context.Context, listWorkspace func(context.Context) (string, error), cfg RunConfig) bool {
	out, err := listWorkspace(ctx)
	if err != nil {
		runtimeLog.Warn("Could not inspect shared workspace volume; seeding it from the host copy",
			"agent", cfg.Name, "backend", cfg.WorkspaceBackendName, "error", err)
		return true
	}
	return workspaceListingIsEmpty(out)
}

// workspaceSyncDeps carries the pod-side effects of the workspace stage as
// function values: listing the pod's workspace, copying the host workspace
// into it, and fixing ownership afterwards.
//
// All three go through the API server's exec subresource, which a fake
// clientset cannot serve — so with the effects inlined, the decision between
// them could not be reached from a test at all, only re-implemented in one.
// That is what let round 1's defect (a shared-volume pod mounting an empty
// /workspace) live in four lines of Run with a green suite. The seam exists so
// the decision is driven, not restated.
type workspaceSyncDeps struct {
	// listWorkspace returns the pod's /workspace listing (empty when the
	// directory is empty).
	listWorkspace func(ctx context.Context) (string, error)
	// copyWorkspace extracts the host workspace into the pod's /workspace.
	copyWorkspace func(ctx context.Context) error
	// chownWorkspace hands /workspace to the container's unix user.
	chownWorkspace func(ctx context.Context) error
}

// podWorkspaceSyncDeps binds the real, exec-backed effects to one pod.
func (r *KubernetesRuntime) podWorkspaceSyncDeps(namespace, podName string, cfg RunConfig) workspaceSyncDeps {
	if r.workspaceSyncDepsOverride != nil {
		return r.workspaceSyncDepsOverride(namespace, podName, cfg)
	}
	return workspaceSyncDeps{
		listWorkspace: func(ctx context.Context) (string, error) {
			return r.execInPod(ctx, namespace, podName,
				[]string{"sh", "-c", "ls -A /workspace 2>/dev/null | head -1"})
		},
		copyWorkspace: func(ctx context.Context) error {
			return r.syncWithRetry(ctx, func() error {
				return r.syncToPod(ctx, namespace, podName, cfg.Workspace, "/workspace")
			})
		},
		chownWorkspace: func(ctx context.Context) error {
			chownCmd := fmt.Sprintf("chown -R %s:%s /workspace", cfg.UnixUsername, cfg.UnixUsername)
			_, err := r.execInPod(ctx, namespace, podName, []string{"sh", "-c", chownCmd})
			return err
		},
	}
}

// syncWorkspaceStage decides whether the host workspace is copied into the pod
// and, when it is, copies it and fixes ownership.
//
// The skip for shared-volume pods tracks the mechanism that populates the
// volume, not the backend name: skip only when the bytes arrive some other way
// (the provisioning init container, or the container's own git clone). A
// shared-volume pod with neither would otherwise mount the PVC and find
// /workspace empty (#1075 round 1).
//
// One further condition applies on shared storage, checked against the volume
// once the pod is up: the host copy seeds it only while it is still empty. See
// sharedWorkspaceNeedsSeeding.
//
// Local-backend pods are untouched by all of this and RETAIN today's
// behaviour: sync whenever there is a host workspace.
func (r *KubernetesRuntime) syncWorkspaceStage(ctx context.Context, cfg RunConfig, deps workspaceSyncDeps) error {
	if !shouldSyncWorkspaceToPod(cfg) {
		if usesSharedWorkspacePVC(cfg) {
			logWorkspaceSyncSkip(cfg, "populated-by-container-clone")
		}
		return nil
	}
	if usesSharedWorkspacePVC(cfg) && !sharedWorkspaceNeedsSeeding(ctx, deps.listWorkspace, cfg) {
		logWorkspaceSyncSkip(cfg, "volume-already-populated")
		return nil
	}

	runtimeLog.Info("Syncing workspace", "agent", cfg.Name, "source", cfg.Workspace, "phase", "workspace-sync")
	fmt.Printf("  Syncing workspace (%s -> /workspace)...\n", cfg.Workspace)
	if err := deps.copyWorkspace(ctx); err != nil {
		return fmt.Errorf("failed to sync workspace: %w", err)
	}
	// Fix ownership: tar extraction runs as root via K8s exec, so extracted
	// files are owned by root. Non-fatal, as it has always been.
	if err := deps.chownWorkspace(ctx); err != nil {
		runtimeLog.Debug("Failed to chown workspace (non-fatal)", "error", err)
	}
	return nil
}

// logWorkspaceSyncSkip records a skipped workspace sync with a discriminated
// reason. The two reasons are different claims about the world — "something
// else will populate this volume" versus "something else already did" — and
// an operator reading the log needs to know which one fired.
func logWorkspaceSyncSkip(cfg RunConfig, reason string) {
	runtimeLog.Info("Skipping workspace sync (bytes come from the shared volume, not from a host copy)",
		"agent", cfg.Name, "backend", cfg.WorkspaceBackendName, "skip_reason", reason,
		"pvc", cfg.NFSPVClaimName, "subpath", cfg.NFSSubPath, "phase", "workspace-sync-skip")
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
		// Two very different states reach here. Asking for node-local storage
		// is the legitimate no-op. Asking for a backend this runtime cannot
		// realize is not: SelectWorkspaceBackend falls through to local for an
		// unrecognized name (and its switch is case-sensitive where
		// ApplyNFSDefaults is not, so "NFS" gets its defaults applied and then
		// resolves to local), and "cloudrun-volume" resolves to a real backend
		// that this pod path has no branch for. Both would hand the operator
		// an EmptyDir with no way to notice — the #1075 signature.
		configured := strings.TrimSpace(r.WorkspaceStorage.Backend)
		if configured == "" || strings.EqualFold(configured, "local") || mode == store.SharingModeClonePerAgent {
			return cfg, nil
		}
		return cfg, fmt.Errorf("workspace storage backend %q is not supported by the kubernetes runtime; "+
			"use \"nfs\" or \"gke-shared-volume\"", configured)
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

	// ContainerWorkspace is deliberately not passed: it only feeds
	// desc.Target, and the K8s pod path does not read Target — buildPod
	// hardcodes MountPath "/workspace" in both arms, and the seeding check
	// lists that same literal. Threading a value to a field nobody reads is
	// how wiring comes to look live when it is not (see the NFSGID note below).
	desc, err := backend.Realize(RealizeInput{Resolved: resolved})
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

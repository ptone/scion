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
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// workspaceModeEnvKey is the agent env var carrying the project's resolved
// workspace sharing mode. The broker injects it on every dispatch
// (runtimebroker/start_context.go, defaulting to shared-plain) and the hub
// sets it in httpdispatcher.go, so it is the mode signal available to a
// runtime at Run time.
const workspaceModeEnvKey = "SCION_WORKSPACE_MODE"

// sharedWorkspaceBackend reports whether a backend name denotes shared,
// cluster-attached workspace storage as opposed to node-local storage.
// Both shared backends realize as a PVC in the pod spec; the difference is
// only who provisions the volume (an operator-managed NFS export vs a
// GKE-managed shared volume such as Filestore CSI).
func sharedWorkspaceBackend(name string) bool {
	return name == "nfs" || name == "gke-shared-volume"
}

// usesSharedWorkspacePVC reports whether this pod's workspace is served by a
// shared PVC rather than the node-local EmptyDir. It is the single condition
// behind every shared-workspace branch in the K8s pod path, named once so a
// future backend is admitted everywhere at once rather than at seven of eight
// call sites.
func usesSharedWorkspacePVC(cfg RunConfig) bool {
	return sharedWorkspaceBackend(cfg.WorkspaceBackendName) && cfg.NFSPVClaimName != ""
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

	if nfs := r.WorkspaceStorage.NFS; nfs != nil && backend.Name() == "nfs" {
		cfg.NFSUID = nfs.UID
		cfg.NFSGID = nfs.GID
		cfg.NFSStorageClass = nfs.StorageClass
	}

	runtimeLog.Info("Workspace storage resolved for pod",
		"agent", cfg.Name, "project_id", cfg.ProjectID, "backend", cfg.WorkspaceBackendName,
		"pvc", cfg.NFSPVClaimName, "subpath", cfg.NFSSubPath, "mode", string(mode))

	return cfg, nil
}

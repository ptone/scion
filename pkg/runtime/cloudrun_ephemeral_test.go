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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"

	"github.com/GoogleCloudPlatform/scion/pkg/agenthome"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

func TestEphemeralVolumeSize(t *testing.T) {
	mem := func(m, disk string) *api.ResourceSpec {
		return &api.ResourceSpec{Limits: api.ResourceList{Memory: m}, Disk: disk}
	}
	for _, tc := range []struct {
		name    string
		spec    *api.ResourceSpec
		want    string
		wantErr string
	}{
		{name: "half of 4Gi", spec: mem("4Gi", ""), want: "2048Mi"},
		{name: "half of 3Gi", spec: mem("3Gi", ""), want: "1536Mi"},
		{name: "half rounds down to MiB", spec: mem("3001Mi", ""), want: "1500Mi"},
		{name: "half of 2Gi is the floor of headroom", spec: mem("2Gi", ""), want: "1024Mi"},
		{name: "requests.memory when no limit", spec: &api.ResourceSpec{Requests: api.ResourceList{Memory: "4Gi"}}, want: "2048Mi"},
		{name: "disk set", spec: mem("4Gi", "3Gi"), want: "3072Mi"},
		{name: "disk at the floor", spec: mem("2Gi", "512Mi"), want: "512Mi"},
		{name: "disk leaves under 1Gi", spec: mem("2Gi", "1.5Gi"),
			wantErr: "resources.limits.memory (2Gi) must leave at least 1Gi after resources.disk (1.5Gi). Raise the memory or lower resources.disk."},
		{name: "default half leaves under 1Gi", spec: mem("1536Mi", ""),
			wantErr: "must leave at least 1Gi after resources.disk (768Mi, half the memory by default)"},
		{name: "disk under the floor", spec: mem("4Gi", "256Mi"),
			wantErr: "resources.disk for an ephemeral Cloud Run agent must be at least 512Mi (got 256Mi)."},
		{name: "platform default memory", spec: nil,
			wantErr: "sized at half of resources.limits.memory (512Mi) by default, which is under the 512Mi minimum"},
		{name: "bad disk", spec: mem("4Gi", "lots"), wantErr: "invalid resources.disk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ephemeralVolumeSize(tc.spec)
			if tc.wantErr != "" {
				var rle *ResourceLimitsError
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.As(err, &rle) {
					t.Fatalf("ephemeralVolumeSize = %q, %v; want a ResourceLimitsError containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ephemeralVolumeSize = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestCloudRunEphemeralInstance_ShareMountIsOnlySeed (flat): an ephemeral
// instance of a flat Cloud Run runtime has exactly one share mount, the
// read-only home seed, and its workspace and home on the in-memory volume.
func TestCloudRunEphemeralInstance_ShareMountIsOnlySeed(t *testing.T) {
	rt, err := newFlatCloudRunRuntime(config.CloudRunConfig{ProjectID: flatTestProjectID, Location: flatTestRegion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := goldenInstanceCfg()
	cfg.Persistence = api.PersistenceEphemeral
	cfg.Resources = &api.ResourceSpec{Limits: api.ResourceList{Memory: "4Gi"}}
	paths := goldenInstancePaths()
	inst, err := rt.buildCloudRunInstance(cfg, 1000, 1000, paths)
	if err != nil {
		t.Fatal(err)
	}
	var nfs []*runpb.Volume
	byName := map[string]*runpb.Volume{}
	for _, v := range inst.Volumes {
		byName[v.Name] = v
		if v.GetNfs() != nil {
			nfs = append(nfs, v)
		}
	}
	if len(nfs) != 1 || nfs[0].GetNfs().GetPath() != paths.homeExportPath || !nfs[0].GetNfs().GetReadOnly() {
		t.Fatalf("share volumes = %v, want only the home leaf, read-only", nfs)
	}
	local := byName[ephemeralVolumeName].GetEmptyDir()
	if local == nil || local.GetMedium() != runpb.EmptyDirVolumeSource_MEMORY || local.GetSizeLimit() != "2048Mi" {
		t.Fatalf("ephemeral volume = %v, want MEMORY with sizeLimit 2048Mi", local)
	}
	mounts := map[string]*runpb.VolumeMount{}
	for _, m := range inst.Containers[0].VolumeMounts {
		mounts[m.MountPath] = m
	}
	for path, want := range map[string][2]string{
		"/workspace":          {ephemeralVolumeName, "workspace"},
		"/home/scion":         {ephemeralVolumeName, "home"},
		api.HomeSeedMountPath: {ephemeralHomeSeedVolume, ""},
		k8sMemDir:             {cloudRunMemVolume, ""},
	} {
		m := mounts[path]
		if m == nil || m.Name != want[0] || m.SubPath != want[1] {
			t.Errorf("mount at %s = %v, want volume %s subPath %q", path, m, want[0], want[1])
		}
	}
	seeds := 0
	for _, e := range inst.Containers[0].Env {
		if e.Name == api.HomeSeedEnvVar {
			seeds++
			if e.GetValue() != api.HomeSeedMountPath {
				t.Errorf("%s = %q, want %q", api.HomeSeedEnvVar, e.GetValue(), api.HomeSeedMountPath)
			}
		}
	}
	if seeds != 1 {
		t.Errorf("%s set %d times, want once", api.HomeSeedEnvVar, seeds)
	}
}

// TestCloudRunProvisionNFS_EphemeralNoWorkspaceDir: an ephemeral
// clone-per-agent provision creates only the home leaf on the share.
func TestCloudRunProvisionNFS_EphemeralNoWorkspaceDir(t *testing.T) {
	orig := cloudRunRequireNFSFilesystem
	cloudRunRequireNFSFilesystem = func(string) error { return nil }
	t.Cleanup(func() { cloudRunRequireNFSFilesystem = orig })
	r := &CloudRunRuntime{config: &config.CloudRunConfig{ProjectID: "gcp-project", Location: "us-central1", NFSServer: "10.0.0.2", NFSExport: "/scion-workspaces"}}
	share := t.TempDir()
	cfg := RunConfig{
		Labels:               crTestLabels,
		WorkspaceBackendName: "nfs",
		ProjectID:            "proj-123",
		Workspace:            filepath.Join(share, "projects", "proj-123", "workspace"),
		Env:                  clonePerAgentEnv,
		GitClone:             &api.GitCloneConfig{URL: "file:///nonexistent/repo.git"},
		Persistence:          api.PersistenceEphemeral,
	}
	paths, err := r.provisionCloudRunNFS(context.Background(), cfg, crTestAgentID, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatalf("provisionCloudRunNFS: %v", err)
	}
	if paths.workspaceExportPath != "" || paths.workspaceHostPath != "" {
		t.Errorf("workspace paths = %q, %q; want none", paths.workspaceExportPath, paths.workspaceHostPath)
	}
	if _, err := os.Stat(filepath.Join(share, "projects", "proj-123", "agents", crTestSlug, "workspace")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("agent workspace directory on the share: %v, want none", err)
	}
	if fi, err := os.Stat(paths.homeHostPath); err != nil || !fi.IsDir() {
		t.Errorf("home leaf %s not created: %v", paths.homeHostPath, err)
	}
}

func TestCheckCloudRunEphemeral(t *testing.T) {
	ok := RunConfig{Persistence: api.PersistenceEphemeral, Env: clonePerAgentEnv, GitClone: &api.GitCloneConfig{URL: "https://example.com/r.git"},
		Resources: &api.ResourceSpec{Limits: api.ResourceList{Memory: "4Gi"}}}
	if err := checkCloudRunEphemeral(ok); err != nil {
		t.Fatalf("clone-per-agent ephemeral: %v", err)
	}
	shared := ok
	shared.Env = []string{"SCION_WORKSPACE_MODE=shared-plain"}
	if err := checkCloudRunEphemeral(shared); err == nil || !strings.Contains(err.Error(), "clone-per-agent") {
		t.Fatalf("shared-plain ephemeral = %v, want a clone-per-agent refusal", err)
	}
	if err := checkCloudRunEphemeral(RunConfig{}); err != nil {
		t.Fatalf("persistent: %v", err)
	}
	if a, b := newEphemeralHomeResetID(), newEphemeralHomeResetID(); a == b || len(a) > 64 {
		t.Errorf("reset IDs %q, %q: want distinct and within the marker bound", a, b)
	}
}

// TestCheckPersistenceSupported: only a runtime with the capability runs
// an ephemeral agent; persistent runs anywhere.
func TestCheckPersistenceSupported(t *testing.T) {
	cr := &CloudRunRuntime{}
	other := &DockerRuntime{}
	if err := CheckPersistenceSupported(cr, api.PersistenceEphemeral); err != nil {
		t.Errorf("cloudrun: %v", err)
	}
	if err := CheckPersistenceSupported(other, api.PersistenceEphemeral); err == nil || !strings.Contains(err.Error(), "Cloud Run only") {
		t.Errorf("docker ephemeral = %v, want a refusal", err)
	}
	if err := CheckPersistenceSupported(other, ""); err != nil {
		t.Errorf("docker persistent: %v", err)
	}
}

// TestCloudRunHomeLimitsMatchSeedLimits: the broker's home copy and
// sciontool's seed copy use the same bounds.
func TestCloudRunHomeLimitsMatchSeedLimits(t *testing.T) {
	want := agenthome.Limits{MaxFiles: cloudRunHomeMaxEntries, MaxBytes: cloudRunHomeMaxBytes, MaxDepth: cloudRunHomeMaxDepth}
	if agenthome.ProvisionedHomeLimits != want {
		t.Errorf("agenthome.ProvisionedHomeLimits = %+v, want %+v", agenthome.ProvisionedHomeLimits, want)
	}
}

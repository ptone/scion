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
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"cloud.google.com/go/run/apiv2/runpb"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// An ephemeral Cloud Run instance (ptone/scion#7780) keeps its workspace
// and its home on two instance-local emptyDir volumes, "ws" at the
// workspace and "home" at the home, with no subPath (Cloud Run Instances
// with subPath mounts next to an NFS volume failed at exec in the live
// spikes). The agent's home leaf on the share is mounted read-only as the
// home seed, which sciontool init copies into $HOME. The medium, the
// sizing rule and the layout live here only: switching to DISK once Cloud
// Run Instances accept it is a change to ephemeralVolumeMedium and the
// sizing constants, nothing else.
//
// Context for the defaults (live, 2026-11-14): a ptone/scion clone onto a
// memory volume took 14 s, against about 185 s on the share, and one
// checkout uses about 260 MiB of memory.
const (
	// ephemeralVolumeMedium is MEMORY: Cloud Run Instances reject a DISK
	// emptyDir (design r7). Data on it counts against the memory limit.
	ephemeralVolumeMedium = runpb.EmptyDirVolumeSource_MEMORY

	// The total ephemeral size S is at least ephemeralMinSizeMiB and must
	// leave the instance ephemeralMinHeadroomMiB of memory. The home volume
	// takes ephemeralHomeSizeMiB of S, the workspace volume the rest.
	ephemeralMinSizeMiB     = 1024
	ephemeralMinHeadroomMiB = 1024
	ephemeralHomeSizeMiB    = 512

	ephemeralWorkspaceVolume = "ws"
	ephemeralHomeVolume      = "home"
	ephemeralHomeSeedVolume  = "home-seed"
)

// ephemeralVolumeSize returns the size limits of the workspace and home
// volumes as "<n>Mi". The total S is resources.disk when set, otherwise
// half the instance memory limit, both rounded down to whole MiB; the home
// gets ephemeralHomeSizeMiB and the workspace the rest. It refuses an S
// under ephemeralMinSizeMiB, and one that leaves the instance less than
// ephemeralMinHeadroomMiB of memory, with a *ResourceLimitsError in user
// terms.
func ephemeralVolumeSize(spec *api.ResourceSpec) (ws, home string, err error) {
	res, err := cloudRunContainerResources(spec)
	if err != nil {
		return "", "", err
	}
	memShown := res.GetLimits()["memory"]
	if memShown == "" {
		memShown = fmt.Sprintf("%dMi", cloudRunDefaultMemoryMiB)
	}
	memQ, err := resource.ParseQuantity(memShown)
	if err != nil {
		return "", "", err
	}
	memMiB := memQ.Value() / cloudRunMiB
	if spec != nil && spec.Limits.Memory != "" {
		memShown = spec.Limits.Memory
	} else if spec != nil && spec.Requests.Memory != "" {
		memShown = spec.Requests.Memory
	}
	memShown = quoteResourceValue(memShown)
	memShown = memShown[1 : len(memShown)-1]
	headroom := fmtMiB(ephemeralMinHeadroomMiB)

	disk := ""
	if spec != nil {
		disk = spec.Disk
	}
	sizeMiB, sizeShown := memMiB/2, ""
	if disk != "" {
		q, err := resource.ParseQuantity(disk)
		if err != nil {
			return "", "", NewResourceLimitsError("cloudrun: invalid resources.disk resource value %s: %v", quoteResourceValue(disk), err)
		}
		sizeMiB, sizeShown = q.Value()/cloudRunMiB, quoteResourceValue(disk)
		sizeShown = sizeShown[1 : len(sizeShown)-1]
	}
	if sizeMiB < ephemeralMinSizeMiB {
		if disk == "" {
			return "", "", NewResourceLimitsError("cloudrun: an ephemeral Cloud Run agent keeps its workspace in memory, sized at half of resources.limits.memory (%s) by default, which is under the %s minimum. Raise the memory or set resources.disk.",
				memShown, fmtMiB(ephemeralMinSizeMiB))
		}
		return "", "", NewResourceLimitsError("cloudrun: resources.disk for an ephemeral Cloud Run agent must be at least %s (got %s).", fmtMiB(ephemeralMinSizeMiB), sizeShown)
	}
	if memMiB-sizeMiB < ephemeralMinHeadroomMiB {
		if disk == "" {
			sizeShown = fmt.Sprintf("%dMi, half the memory by default", sizeMiB)
		}
		return "", "", NewResourceLimitsError("cloudrun: an ephemeral Cloud Run agent keeps its workspace in memory: resources.limits.memory (%s) must leave at least %s after resources.disk (%s). Raise the memory or lower resources.disk.",
			memShown, headroom, sizeShown)
	}
	return fmt.Sprintf("%dMi", sizeMiB-ephemeralHomeSizeMiB), fmt.Sprintf("%dMi", int64(ephemeralHomeSizeMiB)), nil
}

// fmtMiB shows n MiB as whole Gi when it is one, else as Mi.
func fmtMiB(n int64) string {
	if n%1024 == 0 {
		return fmt.Sprintf("%dGi", n/1024)
	}
	return fmt.Sprintf("%dMi", n)
}

// cloudRunEphemeralVolumes returns the ephemeral instance's volumes and
// mounts: the workspace and home memory volumes at workspaceMount and
// homeMount, and the read-only home seed (the agent's home leaf on the
// share).
func cloudRunEphemeralVolumes(cfg RunConfig, nfsPaths *cloudRunNFSProvisionPaths, workspaceMount, homeMount string) ([]*runpb.Volume, []*runpb.VolumeMount, error) {
	wsSize, homeSize, err := ephemeralVolumeSize(cfg.Resources)
	if err != nil {
		return nil, nil, err
	}
	memory := func(name, size string) *runpb.Volume {
		return &runpb.Volume{Name: name, VolumeType: &runpb.Volume_EmptyDir{EmptyDir: &runpb.EmptyDirVolumeSource{
			Medium: ephemeralVolumeMedium, SizeLimit: size,
		}}}
	}
	volumes := []*runpb.Volume{
		memory(ephemeralWorkspaceVolume, wsSize),
		memory(ephemeralHomeVolume, homeSize),
		{Name: ephemeralHomeSeedVolume, VolumeType: &runpb.Volume_Nfs{Nfs: &runpb.NFSVolumeSource{
			Server: nfsPaths.server, Path: nfsPaths.homeExportPath, ReadOnly: true,
		}}},
	}
	mounts := []*runpb.VolumeMount{
		{Name: ephemeralWorkspaceVolume, MountPath: workspaceMount},
		{Name: ephemeralHomeVolume, MountPath: homeMount},
		{Name: ephemeralHomeSeedVolume, MountPath: api.HomeSeedMountPath},
	}
	return volumes, mounts, nil
}

// checkCloudRunEphemeral refuses an ephemeral run the runtime cannot
// honour: only a clone-per-agent workspace is re-created at every start
// (sciontool init clones into the empty workspace), and the volume must fit
// the instance memory (ephemeralVolumeSize). A persistent run passes.
func checkCloudRunEphemeral(cfg RunConfig) error {
	if !cfg.Persistence.IsEphemeral() {
		return nil
	}
	if !isClonePerAgentRun(cfg) {
		return fmt.Errorf("cloudrun: ephemeral persistence needs a git project with the clone-per-agent workspace mode, so the workspace can be cloned again at every start")
	}
	_, _, err := ephemeralVolumeSize(cfg.Resources)
	return err
}

// newEphemeralHomeResetID returns a reset ID unique to this start, so
// provisionCloudRunHome clears the home leaf (the seed) before seeding it.
func newEphemeralHomeResetID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ephemeral-" + hex.EncodeToString(b)
}

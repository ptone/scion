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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// LabelBindSources records, at container creation, the host-path binds the
// broker passed to the runtime (base64 of a JSON []BindMount, sources
// resolved). The broker sets it on the create command line; container
// labels cannot be changed after creation, and the workload has no access
// to the runtime's API. It is read back only where a runtime cannot report
// a container's mounts itself (the Apple container runtime).
const LabelBindSources = "scion.bind_sources"

// BindMount is one host-path bind of a container.
type BindMount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// ErrMountsUnverifiable is returned when a container's binds cannot be
// determined. Callers treat it as unsafe.
var ErrMountsUnverifiable = errors.New("container mounts could not be verified")

// MountInspector is implemented by runtimes that can report the host-path
// binds of a container they created. Runtimes that bind no host paths of
// the broker host (Kubernetes, Cloud Run) do not implement it.
type MountInspector interface {
	InspectBindMounts(ctx context.Context, id string) ([]BindMount, error)
}

// CheckBindMounts applies the Runtime Broker control-path check
// (checkBindSource) to the sources of binds, as reported for a container
// that already exists. It returns the first refusal.
func CheckBindMounts(binds []BindMount) error {
	for _, b := range binds {
		if _, err := checkBindSource(b.Source); err != nil {
			return fmt.Errorf("container mount at %s: %w", b.Destination, err)
		}
	}
	return nil
}

// inspectContainerBinds reads the bind mounts of a Docker or Podman
// container from "inspect". Only binds are returned: named volumes and
// tmpfs mounts are the runtime's own storage, not broker-host paths.
func inspectContainerBinds(ctx context.Context, command, id string) ([]BindMount, error) {
	out, err := runSimpleCommand(ctx, command, "inspect", "--format", "{{json .Mounts}}", id)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect: %v", ErrMountsUnverifiable, err)
	}
	return parseInspectBinds([]byte(out))
}

func parseInspectBinds(out []byte) ([]BindMount, error) {
	var mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	}
	if err := json.Unmarshal(out, &mounts); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrMountsUnverifiable, err)
	}
	var binds []BindMount
	for _, m := range mounts {
		if m.Type == "bind" {
			binds = append(binds, BindMount{Source: m.Source, Destination: m.Destination})
		}
	}
	return binds, nil
}

// encodeBindSourcesLabel is the LabelBindSources value for binds.
func encodeBindSourcesLabel(binds []BindMount) (string, error) {
	data, err := json.Marshal(binds)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// decodeBindSourcesLabel parses a LabelBindSources value. A missing or
// malformed label is ErrMountsUnverifiable.
func decodeBindSourcesLabel(labels map[string]string) ([]BindMount, error) {
	v, ok := labels[LabelBindSources]
	if !ok {
		return nil, fmt.Errorf("%w: no %s label", ErrMountsUnverifiable, LabelBindSources)
	}
	data, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("%w: %s label: %v", ErrMountsUnverifiable, LabelBindSources, err)
	}
	var binds []BindMount
	if err := json.Unmarshal(data, &binds); err != nil {
		return nil, fmt.Errorf("%w: %s label: %v", ErrMountsUnverifiable, LabelBindSources, err)
	}
	return binds, nil
}

// InspectBindMounts reports the container's binds from docker inspect.
func (r *DockerRuntime) InspectBindMounts(ctx context.Context, id string) ([]BindMount, error) {
	return inspectContainerBinds(ctx, r.Command, id)
}

// InspectBindMounts reports the container's binds from podman inspect.
func (r *PodmanRuntime) InspectBindMounts(ctx context.Context, id string) ([]BindMount, error) {
	return inspectContainerBinds(ctx, r.Command, id)
}

// InspectBindMounts reports the binds recorded in the container's
// LabelBindSources label at creation. The Apple container runtime's own
// inspect output is not parsed yet, so this is the broker's record of what
// it asked for, not the runtime's view of what it mounted.
func (r *AppleContainerRuntime) InspectBindMounts(ctx context.Context, id string) ([]BindMount, error) {
	agents, err := r.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: list: %v", ErrMountsUnverifiable, err)
	}
	for _, a := range agents {
		if a.ContainerID == id || a.Name == id {
			return decodeBindSourcesLabel(a.Labels)
		}
	}
	return nil, fmt.Errorf("%w: container %s not found", ErrMountsUnverifiable, id)
}

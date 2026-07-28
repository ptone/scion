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
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	corev1 "k8s.io/api/core/v1"
)

// expectedResources is the full set of Kubernetes defaults an agent should end
// up with when it specifies nothing of its own.
type expectedResources struct {
	cpuRequest string
	memRequest string
	cpuLimit   string
	memLimit   string
	disk       string
}

func assertPodResources(t *testing.T, res corev1.ResourceRequirements, want expectedResources) {
	t.Helper()

	check := func(list corev1.ResourceList, name corev1.ResourceName, want, where string) {
		q, ok := list[name]
		if !ok {
			t.Errorf("%s: %s missing, want %q", where, name, want)
			return
		}
		if got := q.String(); got != want {
			t.Errorf("%s: %s = %q, want %q", where, name, got, want)
		}
	}

	check(res.Requests, corev1.ResourceCPU, want.cpuRequest, "requests")
	check(res.Requests, corev1.ResourceMemory, want.memRequest, "requests")
	check(res.Limits, corev1.ResourceCPU, want.cpuLimit, "limits")
	check(res.Limits, corev1.ResourceMemory, want.memLimit, "limits")
	check(res.Requests, corev1.ResourceEphemeralStorage, want.disk, "requests")
	check(res.Limits, corev1.ResourceEphemeralStorage, want.disk, "limits")
}

// TestBuildPod_ResourceDefaults_AreAppliedPerField is the regression test for
// the K8s half of the 2026-07-28 hub outage follow-up (issue #612, bug #6).
//
// PR #894 made the provision layer always attach
// config.BuiltinDefaultResources() — a spec that sets limits.cpu and nothing
// else. The K8s adapter's fallback was all-or-nothing (`if config.Resources ==
// nil`), so it stopped firing entirely and K8s agents silently lost their
// memory limit, disk request and CPU/memory requests, keeping only
// limits.cpu=2.
//
// Every case below must produce the complete default set, whichever subset of
// it arrived from an upstream tier.
func TestBuildPod_ResourceDefaults_AreAppliedPerField(t *testing.T) {
	full := expectedResources{
		cpuRequest: "250m",
		memRequest: "512Mi",
		cpuLimit:   "2",
		memLimit:   "4Gi",
		disk:       "10Gi",
	}

	tests := []struct {
		name string
		in   *api.ResourceSpec
		want expectedResources
	}{
		{
			name: "nil spec",
			in:   nil,
			want: full,
		},
		{
			name: "empty spec",
			in:   &api.ResourceSpec{},
			want: full,
		},
		{
			// The exact spec the provision layer now always supplies. Before
			// the fix this yielded limits.cpu=2 and nothing else.
			name: "builtin defaults from the provision layer",
			in:   config.BuiltinDefaultResources(),
			want: full,
		},
		{
			name: "only a CPU limit",
			in:   &api.ResourceSpec{Limits: api.ResourceList{CPU: "8"}},
			want: expectedResources{
				cpuRequest: "250m",
				memRequest: "512Mi",
				cpuLimit:   "8",
				memLimit:   "4Gi",
				disk:       "10Gi",
			},
		},
		{
			name: "only a disk request",
			in:   &api.ResourceSpec{Disk: "50Gi"},
			want: expectedResources{
				cpuRequest: "250m",
				memRequest: "512Mi",
				cpuLimit:   "2",
				memLimit:   "4Gi",
				disk:       "50Gi",
			},
		},
		{
			name: "only requests",
			in:   &api.ResourceSpec{Requests: api.ResourceList{CPU: "1", Memory: "2Gi"}},
			want: expectedResources{
				cpuRequest: "1",
				memRequest: "2Gi",
				cpuLimit:   "2",
				memLimit:   "4Gi",
				disk:       "10Gi",
			},
		},
		{
			name: "fully specified spec is untouched",
			in: &api.ResourceSpec{
				Requests: api.ResourceList{CPU: "500m", Memory: "1Gi"},
				Limits:   api.ResourceList{CPU: "4", Memory: "8Gi"},
				Disk:     "20Gi",
			},
			want: expectedResources{
				cpuRequest: "500m",
				memRequest: "1Gi",
				cpuLimit:   "4",
				memLimit:   "8Gi",
				disk:       "20Gi",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, _, _ := newTestK8sRuntime()

			pod, err := rt.buildPod("default", RunConfig{
				Name:         "res-defaults",
				Image:        "test:latest",
				UnixUsername: "scion",
				Resources:    tt.in,
			})
			if err != nil {
				t.Fatalf("buildPod failed: %v", err)
			}

			assertPodResources(t, pod.Spec.Containers[0].Resources, tt.want)
		})
	}
}

// TestBuildPod_ResourceDefaults_DoNotMutateCallerSpec guards the copy in
// buildPod. RunConfig is passed by value but Resources is a pointer into the
// caller's state, so filling in defaults in place would leak K8s-specific
// values back into a spec that may be reused for another runtime.
func TestBuildPod_ResourceDefaults_DoNotMutateCallerSpec(t *testing.T) {
	rt, _, _ := newTestK8sRuntime()

	spec := &api.ResourceSpec{Limits: api.ResourceList{CPU: "2"}}
	before := *spec

	if _, err := rt.buildPod("default", RunConfig{
		Name:         "no-mutate",
		Image:        "test:latest",
		UnixUsername: "scion",
		Resources:    spec,
	}); err != nil {
		t.Fatalf("buildPod failed: %v", err)
	}

	if *spec != before {
		t.Errorf("buildPod mutated the caller's ResourceSpec: got %+v, want %+v", *spec, before)
	}
}

// TestK8sDefaultCPULimit_MatchesBuiltin keeps the adapter's CPU ceiling in step
// with the cross-runtime built-in, so a K8s agent and a Docker agent throttle
// at the same point.
func TestK8sDefaultCPULimit_MatchesBuiltin(t *testing.T) {
	if got := config.BuiltinDefaultResources().Limits.CPU; got != k8sDefaultCPULimit {
		t.Errorf("built-in CPU limit %q diverges from the Kubernetes default %q", got, k8sDefaultCPULimit)
	}
}

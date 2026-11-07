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
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/substratecaps"
	"github.com/GoogleCloudPlatform/scion/pkg/substrateenv"
	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
)

// -----------------------------------------------------------------------
// egress_trust_bundle: golden template, off
// -----------------------------------------------------------------------

// TestBuildActorTemplate_EgressTrustBundleUnset_MatchesPreChangeGolden pins
// buildActorTemplate's v1 output with EgressTrustBundle unset (the default):
// no system-info volume, no /run/ate mount, Env nil, the container's wakeup
// probe on the serve healthz, resources only at template level, and no
// snapshot_config. want is hand-built, with literal values, not derived
// from the function under test, so a regression that silently changes the
// unset-case shape is caught here rather than compared against itself.
func TestBuildActorTemplate_EgressTrustBundleUnset_MatchesPreChangeGolden(t *testing.T) {
	image := "repo/image@sha256:" + strings.Repeat("c", 64)
	sc := config.V1SubstrateConfig{
		WorkerSelector: map[string]string{"pool": "x"},
	}
	resources := &api.ResourceSpec{Limits: api.ResourceList{CPU: "2", Memory: "4Gi"}}

	got := mustBuildActorTemplate(t, "scion-test", "scion-abc123", image, sc, resources)

	want := &ateapi.ActorTemplate{
		Metadata: &ateapi.ResourceMetadata{Atespace: "scion-test", Name: "scion-abc123"},
		Containers: []*ateapi.Container{
			{
				Name:    "scion-agent",
				Image:   image,
				Command: []string{"/usr/local/bin/sciontool", "substrate-serve"},
				Env:     nil,
				VolumeMounts: []*ateapi.VolumeMount{
					{Name: "workspace", MountPath: "/workspace"},
				},
				SecurityContext: &ateapi.SecurityContext{
					Capabilities: &ateapi.Capabilities{Add: slices.Clone(substratecaps.Names())},
				},
				WakeupProbe: &ateapi.WakeupProbe{
					HttpGet:        &ateapi.HttpGetAction{Path: "/scion/v1/healthz", Port: 80},
					TimeoutSeconds: 120,
				},
			},
		},
		Volumes: []*ateapi.Volume{
			{Name: "workspace", DurableDir: &ateapi.DurableDirVolumeSource{}},
		},
		Resources: &ateapi.ResourceRequirements{
			Limits: &ateapi.Resources{CpuMillicores: 2000, MemoryBytes: 4 << 30},
		},
		WorkerSelector: &ateapi.Selector{MatchLabels: sc.WorkerSelector},
	}

	if !proto.Equal(got, want) {
		t.Errorf("mustBuildActorTemplate(t, ) with EgressTrustBundle unset differs from the pre-change golden template:\ngot:  %s\nwant: %s", got, want)
	}
}

// TestBuildActorTemplate_EgressTrustBundleSet confirms the exact volume,
// mount, and five env vars buildActorTemplate adds when EgressTrustBundle
// is set — mirroring substrate's demos/egress/egress-mitm-template.yaml.tmpl
// and docs/egress-trust-bundle.md.
func TestBuildActorTemplate_EgressTrustBundleSet(t *testing.T) {
	image := "repo/image@sha256:" + strings.Repeat("d", 64)
	sc := config.V1SubstrateConfig{EgressTrustBundle: "egress-mitm.ate.dev"}

	tmpl := mustBuildActorTemplate(t, "scion-test", "scion-def456", image, sc, nil)

	wantVolumes := []*ateapi.Volume{
		{Name: "workspace", DurableDir: &ateapi.DurableDirVolumeSource{}},
		{
			Name: "system-info",
			SystemInfo: &ateapi.SystemInfoVolumeSource{
				DataSources: []*ateapi.SystemInfoDataSource{
					{
						TrustBundle: &ateapi.TrustBundleDataSource{
							Names: []string{"egress-mitm.ate.dev"},
							Path:  "trust-bundle.pem",
						},
					},
				},
			},
		},
	}
	if len(tmpl.GetVolumes()) != len(wantVolumes) {
		t.Fatalf("Volumes = %d, want %d: %v", len(tmpl.GetVolumes()), len(wantVolumes), tmpl.GetVolumes())
	}
	for i, want := range wantVolumes {
		if !proto.Equal(tmpl.GetVolumes()[i], want) {
			t.Errorf("Volumes[%d] = %s, want %s", i, tmpl.GetVolumes()[i], want)
		}
	}

	wantMounts := []*ateapi.VolumeMount{
		{Name: "workspace", MountPath: "/workspace"},
		{Name: "system-info", MountPath: "/run/ate"},
	}
	if len(tmpl.GetContainers()) != 1 {
		t.Fatalf("Containers = %d, want 1", len(tmpl.GetContainers()))
	}
	gotMounts := tmpl.GetContainers()[0].GetVolumeMounts()
	if len(gotMounts) != len(wantMounts) {
		t.Fatalf("VolumeMounts = %d, want %d: %v", len(gotMounts), len(wantMounts), gotMounts)
	}
	for i, want := range wantMounts {
		if !proto.Equal(gotMounts[i], want) {
			t.Errorf("VolumeMounts[%d] = %s, want %s", i, gotMounts[i], want)
		}
	}

	wantEnv := []*ateapi.EnvVar{
		{Name: "NODE_EXTRA_CA_CERTS", Value: "/run/ate/trust-bundle.pem"},
		{Name: "GIT_SSL_CAINFO", Value: "/run/ate/trust-bundle.pem"},
		{Name: "SSL_CERT_FILE", Value: "/run/ate/trust-bundle.pem"},
		{Name: "CURL_CA_BUNDLE", Value: "/run/ate/trust-bundle.pem"},
		{Name: "SSL_CERT_DIR", Value: "/run/ate"},
	}
	gotEnv := tmpl.GetContainers()[0].GetEnv()
	if len(gotEnv) != len(wantEnv) {
		t.Fatalf("Env = %d entries, want %d: %v", len(gotEnv), len(wantEnv), gotEnv)
	}
	for i, want := range wantEnv {
		if !proto.Equal(gotEnv[i], want) {
			t.Errorf("Env[%d] = %s, want %s", i, gotEnv[i], want)
		}
	}
}

// TestBuildActorTemplate_EnvNamesMatchSharedTrustBundleVarNames is the
// template-side half of the tie between buildActorTemplate's Env and
// pkg/sciontool/substrate's own trustBundleEnvPairs candidate list: both
// derive from substrateenv.TrustBundleVarNames directly, so a name added to
// the template without updating that shared slice (or vice versa) is
// caught here rather than surfacing as exec silently losing a var the
// template already carries.
func TestBuildActorTemplate_EnvNamesMatchSharedTrustBundleVarNames(t *testing.T) {
	image := "repo/image@sha256:" + strings.Repeat("f", 64)
	sc := config.V1SubstrateConfig{EgressTrustBundle: "egress-mitm.ate.dev"}

	tmpl := mustBuildActorTemplate(t, "scion-test", "scion-fedcba", image, sc, nil)

	gotNames := make([]string, len(tmpl.GetContainers()[0].GetEnv()))
	for i, e := range tmpl.GetContainers()[0].GetEnv() {
		gotNames[i] = e.GetName()
	}
	if !slices.Equal(gotNames, substrateenv.TrustBundleVarNames) {
		t.Errorf("buildActorTemplate() Env names = %v, want substrateenv.TrustBundleVarNames = %v", gotNames, substrateenv.TrustBundleVarNames)
	}
}

// -----------------------------------------------------------------------
// substrateTemplateName: unchanged when unset, different when set
// -----------------------------------------------------------------------

// substrateTemplateNameFixture is one row of the table-driven
// substrateTemplateName tests below: an image/config/resources input, and
// the literal name substrateTemplateName produces for it with
// EgressTrustBundle unset. Both rows were re-pinned for the v1 hash ("v1|"
// prefix, explicit field list, resolved cpu_millicores and memory_bytes),
// which deliberately gives every template a new name.
type substrateTemplateNameFixture struct {
	name      string
	image     string
	sc        config.V1SubstrateConfig
	resources *api.ResourceSpec
	wantUnset string
}

func substrateTemplateNameFixtures() []substrateTemplateNameFixture {
	return []substrateTemplateNameFixture{
		{
			// Pins the literal for an image plus explicit resources, so
			// any change to the hash's field list or encoding is caught.
			name:      "base",
			image:     "repo/image@sha256:" + strings.Repeat("e", 64),
			sc:        config.V1SubstrateConfig{},
			resources: &api.ResourceSpec{Limits: api.ResourceList{CPU: "2", Memory: "4Gi"}},
			wantUnset: "scion-0d398c448faf",
		},
		{
			// Worker selector set, resources nil (so the built-in
			// substrate defaults are exercised in the hash input too).
			name:  "worker selector, nil resources",
			image: "repo/image@sha256:" + strings.Repeat("g", 64),
			sc: config.V1SubstrateConfig{
				WorkerSelector: map[string]string{"pool": "scion-agents"},
			},
			resources: nil,
			wantUnset: "scion-5a264bd0a233",
		},
	}
}

// TestBuildActorTemplate_LaunchArgv0IsAlwaysAbsolute is the guard test for
// the substrate actor's own launch entrypoint: PID 1's argv[0] is exec'd by
// whatever starts the container, before any of this codebase's own code
// (including pkg/sciontool/rootexec's fixed-PATH resolver) ever runs, so a
// bare name here would be looked up against the container's own PATH —
// which on this runtime includes a workload-owned directory ahead of any
// trusted system directory (see the rootexec package doc comment for the
// general shape of that hazard). Every container in the template, across
// the fixture table, must have an absolute argv[0]. Kubernetes's own
// "sciontool provision"/"sciontool" init-container argv are deliberately
// out of scope: that runtime rebuilds its container from a fresh image
// layer on every restart, so there is no persistent, workload-writable
// PATH entry for a bare name there to resolve against.
func TestBuildActorTemplate_LaunchArgv0IsAlwaysAbsolute(t *testing.T) {
	for _, tc := range substrateTemplateNameFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := mustBuildActorTemplate(t, "scion-test", "scion-abc123", tc.image, tc.sc, tc.resources)
			for _, c := range tmpl.Containers {
				if len(c.Command) == 0 {
					t.Fatalf("container %q has an empty Command", c.Name)
				}
				if !strings.HasPrefix(c.Command[0], "/") {
					t.Errorf("container %q Command[0] = %q, want an absolute path", c.Name, c.Command[0])
				}
			}
		})
	}
}

// TestSubstrateTemplateName_UnchangedWhenEgressTrustBundleUnset pins the
// literal name substrateTemplateName produces, with EgressTrustBundle
// unset, across a table of fixture configs (the original fixture plus one
// with a worker selector and nil resources).
func TestSubstrateTemplateName_UnchangedWhenEgressTrustBundleUnset(t *testing.T) {
	for _, tc := range substrateTemplateNameFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			got := substrateTemplateName(tc.image, tc.sc, mustResolveSubstrateResources(t, tc.resources))
			if got != tc.wantUnset {
				t.Errorf("substrateTemplateName() with EgressTrustBundle unset = %q, want pinned value %q", got, tc.wantUnset)
			}
		})
	}
}

// TestSubstrateTemplateName_ChangesWhenEgressTrustBundleSet confirms
// setting EgressTrustBundle changes the template's content-address, across
// the same fixture table — an existing golden template built before the
// setting was turned on must not be silently reused once it is, since it
// lacks the volume/mount/env a resumed or newly-scheduled actor would
// otherwise need.
func TestSubstrateTemplateName_ChangesWhenEgressTrustBundleSet(t *testing.T) {
	for _, tc := range substrateTemplateNameFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			before := substrateTemplateName(tc.image, tc.sc, mustResolveSubstrateResources(t, tc.resources))

			sc := tc.sc
			sc.EgressTrustBundle = "egress-mitm.ate.dev"
			after := substrateTemplateName(tc.image, sc, mustResolveSubstrateResources(t, tc.resources))

			if before == after {
				t.Error("substrateTemplateName() did not change when EgressTrustBundle was set — an existing golden template would be silently reused without the trust-bundle volume/mount/env")
			}
		})
	}
}

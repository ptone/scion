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

package runtimebroker

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// profileFilterTestServer creates a Server whose buildInfoProfiles reads from
// a settings.yaml in a temporary directory. The caller controls which profiles
// are declared in settings and which broker runtime type is passed.
func profileFilterTestServer(t *testing.T, settingsYAML string) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	dotScion := filepath.Join(tmpDir, ".scion")
	if err := os.Mkdir(dotScion, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotScion, "settings.yaml"), []byte(settingsYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultServerConfig()
	cfg.BrokerID = "test-broker-id"
	cfg.BrokerName = "test-host"
	cfg.ForceRuntime = "mock"

	mgr := &mockManager{}
	rt := &runtime.MockRuntime{NameFunc: func() string { return "mock" }}
	return New(cfg, mgr, rt)
}

// profileNames returns the sorted list of profile names from buildInfoProfiles.
func profileNames(profiles []BrokerProfile) []string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	sort.Strings(names)
	return names
}

// profileTypes returns a "name/type" summary string for each profile, sorted.
func profileTypes(profiles []BrokerProfile) []string {
	out := make([]string, len(profiles))
	for i, p := range profiles {
		out[i] = p.Name + "/" + p.Type
	}
	sort.Strings(out)
	return out
}

// oldWorkstationDefaults is the settings YAML with the old workstation-era
// defaults: local/docker and remote/kubernetes, no seeded default profile.
const oldWorkstationDefaults = `schema_version: "1"
active_profile: local
profiles:
    local:
        runtime: docker
    remote:
        runtime: kubernetes
runtimes:
    docker:
        type: docker
    kubernetes:
        type: kubernetes
`

// seededCloudRunSandboxDefaults includes the seeded default/cloudrun-sandbox
// profile that Shape A (task #92) added for the single-node tier.
const seededCloudRunSandboxDefaults = `schema_version: "1"
active_profile: default
profiles:
    local:
        runtime: docker
    remote:
        runtime: kubernetes
    default:
        runtime: cloudrun-sandbox
runtimes:
    docker:
        type: docker
    kubernetes:
        type: kubernetes
    cloudrun-sandbox:
        type: cloudrun-sandbox
`

// emptyRuntimeProfileSettings declares a profile with runtime: "" which
// inherits the broker's default runtime type. Used for rows 6 and 7.
const emptyRuntimeProfileSettings = `schema_version: "1"
active_profile: local
profiles:
    local:
        runtime: docker
    remote:
        runtime: kubernetes
    unqualified:
        runtime: ""
runtimes:
    docker:
        type: docker
    kubernetes:
        type: kubernetes
`

// ---------------------------------------------------------------------------
// Row tests: seven rows from the brief's table. Rows 1, 3, 4, 6 are the
// load-bearing ones that claim Shape B disturbs no working tier.
// ---------------------------------------------------------------------------

// TestBuildInfoProfiles_Row1_DockerBroker_NoChange tests that a docker
// (workstation) broker with local/docker + remote/kubernetes profiles returns
// both profiles. This must not change between the old filter and Shape B.
func TestBuildInfoProfiles_Row1_DockerBroker_NoChange(t *testing.T) {
	srv := profileFilterTestServer(t, oldWorkstationDefaults)
	profiles := srv.buildInfoProfiles("docker")

	if len(profiles) != 2 {
		t.Fatalf("row 1: expected 2 profiles for docker broker, got %d: %v",
			len(profiles), profileTypes(profiles))
	}
	names := profileNames(profiles)
	if names[0] != "local" || names[1] != "remote" {
		t.Errorf("row 1: expected [local, remote], got %v", names)
	}
}

// TestBuildInfoProfiles_Row2_CloudRunSandboxBroker_ShapeBFix tests the fix:
// a cloudrun-sandbox broker with seeded profiles returns ONLY the default
// profile (1), not remote + default (2) as under the old filter.
func TestBuildInfoProfiles_Row2_CloudRunSandboxBroker_ShapeBFix(t *testing.T) {
	srv := profileFilterTestServer(t, seededCloudRunSandboxDefaults)
	profiles := srv.buildInfoProfiles("cloudrun-sandbox")

	// Shape B expectation: only default/cloudrun-sandbox survives.
	if len(profiles) != 1 {
		t.Fatalf("row 2: expected 1 profile for cloudrun-sandbox broker with Shape B, got %d: %v",
			len(profiles), profileTypes(profiles))
	}
	if profiles[0].Name != "default" || profiles[0].Type != "cloudrun-sandbox" {
		t.Errorf("row 2: expected default/cloudrun-sandbox, got %s/%s",
			profiles[0].Name, profiles[0].Type)
	}
}

// TestBuildInfoProfiles_Row3_KubernetesBroker_NoChange tests that a kubernetes
// (multi-node) broker returns only the remote/kubernetes profile. This must not
// change between the old filter and Shape B.
func TestBuildInfoProfiles_Row3_KubernetesBroker_NoChange(t *testing.T) {
	srv := profileFilterTestServer(t, oldWorkstationDefaults)
	profiles := srv.buildInfoProfiles("kubernetes")

	if len(profiles) != 1 {
		t.Fatalf("row 3: expected 1 profile for kubernetes broker, got %d: %v",
			len(profiles), profileTypes(profiles))
	}
	if profiles[0].Name != "remote" || profiles[0].Type != "kubernetes" {
		t.Errorf("row 3: expected remote/kubernetes, got %s/%s",
			profiles[0].Name, profiles[0].Type)
	}
}

// TestBuildInfoProfiles_Row4_PodmanBroker_NoChange tests that a podman broker
// with local/docker + remote/kubernetes profiles returns both profiles. Podman
// is local-only (like docker) so both are served. Must not change.
func TestBuildInfoProfiles_Row4_PodmanBroker_NoChange(t *testing.T) {
	srv := profileFilterTestServer(t, oldWorkstationDefaults)
	profiles := srv.buildInfoProfiles("podman")

	if len(profiles) != 2 {
		t.Fatalf("row 4: expected 2 profiles for podman broker, got %d: %v",
			len(profiles), profileTypes(profiles))
	}
	names := profileNames(profiles)
	if names[0] != "local" || names[1] != "remote" {
		t.Errorf("row 4: expected [local, remote], got %v", names)
	}
}

// TestBuildInfoProfiles_Row5_CloudRunInstancesBroker_BlastRadius tests the
// blast radius of Shape B on the unseeded cloudrun-instances tier. With only
// local/docker and remote/kubernetes declared, the filter excludes both and the
// len(profiles)==0 tail synthesises a default/cloudrun-instances profile.
//
// This row is what ptone's decision weighs: today this broker offers
// remote/kubernetes (which it cannot serve); under Shape B it offers a
// synthesised default (which works via the error fallback).
func TestBuildInfoProfiles_Row5_CloudRunInstancesBroker_BlastRadius(t *testing.T) {
	srv := profileFilterTestServer(t, oldWorkstationDefaults)
	profiles := srv.buildInfoProfiles("cloudrun-instances")

	// Shape B: neither local/docker nor remote/kubernetes passes
	// canBrokerServeRuntime("cloudrun-instances", ...), so the list is empty
	// and the len(profiles)==0 fallback synthesises a default.
	if len(profiles) != 1 {
		t.Fatalf("row 5: expected 1 synthesised profile for cloudrun-instances broker, got %d: %v",
			len(profiles), profileTypes(profiles))
	}
	if profiles[0].Name != "default" {
		t.Errorf("row 5: expected synthesised profile named 'default', got %q", profiles[0].Name)
	}
	if profiles[0].Type != "cloudrun-instances" {
		t.Errorf("row 5: expected synthesised profile type 'cloudrun-instances', got %q", profiles[0].Type)
	}
}

// TestBuildInfoProfiles_Row6_EmptyRuntimeProfile_NoChange tests that a profile
// with runtime: "" inherits the broker's default runtime type and always
// matches its own broker. This is the invariant that makes the seeded
// template's guarantee work. Tested for multiple broker types.
func TestBuildInfoProfiles_Row6_EmptyRuntimeProfile_NoChange(t *testing.T) {
	brokerTypes := []string{"docker", "cloudrun-sandbox", "podman", "cloudrun-instances", "kubernetes"}

	for _, brokerType := range brokerTypes {
		t.Run(brokerType, func(t *testing.T) {
			srv := profileFilterTestServer(t, emptyRuntimeProfileSettings)
			profiles := srv.buildInfoProfiles(brokerType)

			// The unqualified profile (runtime: "") should always be present
			// because it inherits the broker's default runtime.
			found := false
			for _, p := range profiles {
				if p.Name == "unqualified" {
					found = true
					if p.Type != brokerType {
						t.Errorf("row 6 (%s): expected unqualified profile type to be %q (inherited), got %q",
							brokerType, brokerType, p.Type)
					}
					break
				}
			}
			if !found {
				t.Errorf("row 6 (%s): expected unqualified profile to be kept, but it was filtered out. profiles: %v",
					brokerType, profileTypes(profiles))
			}
		})
	}
}

// TestBuildInfoProfiles_Row7_CloudRunInstancesBroker_EmptyRuntimeProfile tests
// the seventh row from the 12:39 update: a cloudrun-instances broker with
// local/docker, remote/kubernetes, and an empty-runtime profile. Under Shape B
// the profile count goes from 2 → 1: the empty-runtime profile survives
// (inherits cloudrun-instances) but remote/kubernetes is dropped.
func TestBuildInfoProfiles_Row7_CloudRunInstancesBroker_EmptyRuntimeProfile(t *testing.T) {
	srv := profileFilterTestServer(t, emptyRuntimeProfileSettings)
	profiles := srv.buildInfoProfiles("cloudrun-instances")

	// Shape B: only the unqualified profile survives (inherits cloudrun-instances).
	// local/docker and remote/kubernetes are both filtered out.
	if len(profiles) != 1 {
		t.Fatalf("row 7: expected 1 profile for cloudrun-instances broker, got %d: %v",
			len(profiles), profileTypes(profiles))
	}
	if profiles[0].Name != "unqualified" {
		t.Errorf("row 7: expected profile 'unqualified', got %q", profiles[0].Name)
	}
	if profiles[0].Type != "cloudrun-instances" {
		t.Errorf("row 7: expected type 'cloudrun-instances' (inherited), got %q", profiles[0].Type)
	}
}

// ---------------------------------------------------------------------------
// Regression test: old workstation defaults on a cloudrun-sandbox broker.
// Before Shape B this returned 2 profiles (remote + default). Shape B changes
// this to 1 (default only). This test documents the post-Shape-B expectation.
//
// Originally named TestBuildInfoProfiles_OldWorkstationDefaults_Task92_Regression;
// renamed because it no longer documents a regression — it documents the
// corrected filter behaviour after Shape B.
// ---------------------------------------------------------------------------

func TestBuildInfoProfiles_CloudRunSandboxFilter_Task92(t *testing.T) {
	srv := profileFilterTestServer(t, seededCloudRunSandboxDefaults)
	profiles := srv.buildInfoProfiles("cloudrun-sandbox")

	// After Shape B: only 1 profile survives (default/cloudrun-sandbox).
	// The remote/kubernetes profile is correctly excluded because a
	// cloudrun-sandbox broker cannot serve a kubernetes runtime.
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile for cloudrun-sandbox broker, got %d: %v",
			len(profiles), profileTypes(profiles))
	}
	if profiles[0].Name != "default" {
		t.Errorf("expected profile name 'default', got %q", profiles[0].Name)
	}
	if profiles[0].Type != "cloudrun-sandbox" {
		t.Errorf("expected profile type 'cloudrun-sandbox', got %q", profiles[0].Type)
	}
}

// ---------------------------------------------------------------------------
// Row 5 dispatch: assert what resolveManagerForOpts returns for the
// synthesised default profile on a cloudrun-instances broker. The spike
// narrated this but did not measure it (rule 22). We call the function and
// assert the result.
// ---------------------------------------------------------------------------

func TestBuildInfoProfiles_Row5_Dispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	// Set up settings with only old workstation defaults — the cloudrun-instances
	// broker will get a synthesised default profile.
	dotScion := filepath.Join(tmpDir, ".scion")
	if err := os.Mkdir(dotScion, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotScion, "settings.yaml"), []byte(oldWorkstationDefaults), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultServerConfig()
	cfg.BrokerID = "test-broker-id"
	cfg.BrokerName = "test-host"
	cfg.ForceRuntime = "cloudrun-instances"

	mgr := &mockManager{}
	rt := &runtime.MockRuntime{NameFunc: func() string { return "cloudrun-instances" }}
	srv := New(cfg, mgr, rt)

	// Verify the synthesised profile exists.
	profiles := srv.buildInfoProfiles("cloudrun-instances")
	if len(profiles) != 1 || profiles[0].Name != "default" {
		t.Fatalf("precondition: expected synthesised default profile, got %v", profileTypes(profiles))
	}

	// Verify that ResolveRuntime("default") errors — the synthesised
	// profile is not in settings, so resolution fails.
	vs, _, vsErr := config.LoadEffectiveSettings(dotScion)
	if vsErr != nil {
		t.Fatalf("failed to load settings: %v", vsErr)
	}
	_, _, resolveErr := vs.ResolveRuntime("default")
	if resolveErr == nil {
		t.Fatal("expected ResolveRuntime('default') to error for synthesised profile, but it succeeded")
	}

	// resolveManagerForOpts with a profile not in settings should return
	// the default manager (s.manager). This is the fallback path: ForceRuntime
	// is set to "cloudrun-instances" which matches the runtime, so it returns
	// s.manager directly.
	opts := api.StartOptions{
		Name:        "test-agent",
		Profile:     "default",
		ProjectPath: dotScion,
	}
	resolved := srv.resolveManagerForOpts(opts)
	if resolved != srv.manager {
		t.Error("expected resolveManagerForOpts to return the default manager (s.manager) " +
			"for a synthesised profile not in settings, but it returned a different manager")
	}
}

// ---------------------------------------------------------------------------
// Named inversion mutation: canBrokerServeRuntime returns true unconditionally.
// This test is run AFTER implementing Shape B to prove the predicate excludes
// something. When canBrokerServeRuntime is mutated to always return true, row 2
// should go red (cloudrun-sandbox broker would keep all 3 profiles instead of 1).
// ---------------------------------------------------------------------------

func TestBuildInfoProfiles_Mutation_AlwaysTrue(t *testing.T) {
	// This test is identical to row 2 — its purpose is to be the mutation target.
	// When canBrokerServeRuntime is mutated to always return true, the assertion
	// below fails because all 3 profiles survive instead of 1.
	srv := profileFilterTestServer(t, seededCloudRunSandboxDefaults)
	profiles := srv.buildInfoProfiles("cloudrun-sandbox")

	if len(profiles) != 1 {
		t.Fatalf("mutation check: expected 1 profile (only default/cloudrun-sandbox), got %d: %v",
			len(profiles), profileTypes(profiles))
	}
}

// ---------------------------------------------------------------------------
// Row 8: operator-named runtime key whose type differs from the key.
//
// An operator writes runtimes.prod-cluster.type: kubernetes with a profile
// big.runtime: prod-cluster. On a kubernetes broker, the profile must
// survive the filter because the resolved type IS kubernetes — even though
// the map key "prod-cluster" does not match. Before the key→type resolution
// fix, canBrokerServeRuntime compared the broker's type against the raw map
// key and silently dropped the profile (fail-closed on an unrecognised name).
// ---------------------------------------------------------------------------

const operatorNamedRuntimeSettings = `schema_version: "1"
active_profile: big
profiles:
    big:
        runtime: prod-cluster
    local:
        runtime: docker
runtimes:
    prod-cluster:
        type: kubernetes
        context: prod-context
        namespace: agents
    docker:
        type: docker
`

func TestBuildInfoProfiles_Row8_OperatorNamedRuntime_KeyDiffersFromType(t *testing.T) {
	srv := profileFilterTestServer(t, operatorNamedRuntimeSettings)
	profiles := srv.buildInfoProfiles("kubernetes")

	// The "big" profile (runtime key "prod-cluster", resolved type "kubernetes")
	// must survive on a kubernetes broker. The embedded defaults also add
	// "remote" (runtime: kubernetes) which survives for the same reason.
	// "local" (docker) is filtered out: canBrokerServeRuntime("kubernetes",
	// "docker") is false.
	byName := make(map[string]BrokerProfile, len(profiles))
	for _, p := range profiles {
		byName[p.Name] = p
	}

	// CRITICAL: "big" must be present — this is the operator-named runtime test.
	bigP, hasBig := byName["big"]
	if !hasBig {
		t.Fatalf("row 8: profile 'big' missing; profiles are: %v", profileTypes(profiles))
	}
	// The Type field in BrokerProfile must be the RESOLVED type, not the key.
	// This assertion pins the cross-package contract with isNodeBoundBroker
	// (pkg/hub/harness_config_handlers.go:47-53): that function indexes
	// nodeBoundProfileTypes with p.Type. If Type carried the raw key
	// "prod-cluster" instead of the resolved "kubernetes", four image-
	// management endpoints (:754, :1198, :1363, :1437) would regress to
	// HTTP 400 for operator-keyed docker/podman brokers.
	if bigP.Type != "kubernetes" {
		t.Errorf("row 8: expected resolved type 'kubernetes', got %q", bigP.Type)
	}
	// Context should be resolved from the runtime config.
	if bigP.Context != "prod-context" {
		t.Errorf("row 8: expected context 'prod-context', got %q", bigP.Context)
	}

	// "local" (docker) must be filtered out on a kubernetes broker.
	if _, hasLocal := byName["local"]; hasLocal {
		t.Error("row 8: profile 'local' (docker) should be filtered out on a kubernetes broker")
	}
}

// TestBuildInfoProfiles_Row8_OperatorNamedRuntime_LocalBroker verifies the
// same operator-named runtime on a local broker. A docker broker should keep
// all profiles: "big" (prod-cluster → kubernetes), "local" (docker), and
// "remote" (kubernetes, from embedded defaults).
func TestBuildInfoProfiles_Row8_OperatorNamedRuntime_LocalBroker(t *testing.T) {
	srv := profileFilterTestServer(t, operatorNamedRuntimeSettings)
	profiles := srv.buildInfoProfiles("docker")

	// Local brokers serve everything — all profiles must survive.
	byName := make(map[string]BrokerProfile, len(profiles))
	for _, p := range profiles {
		byName[p.Name] = p
	}

	if _, hasBig := byName["big"]; !hasBig {
		t.Errorf("row 8 (local): profile 'big' should be present on docker broker, profiles: %v", profileTypes(profiles))
	}
	if _, hasLocal := byName["local"]; !hasLocal {
		t.Errorf("row 8 (local): profile 'local' should be present on docker broker, profiles: %v", profileTypes(profiles))
	}
}

// ---------------------------------------------------------------------------
// Row 9: empty-runtime profile on a non-local broker.
//
// A profile that omits 'runtime:' entirely inherits the broker's default
// runtime type. This is a common config shape: a profile that overrides
// only resources (e.g. CPU/memory) while inheriting the broker's runtime.
// It must survive the filter on EVERY broker type, including non-local.
// ---------------------------------------------------------------------------

const emptyRuntimeOnlySettings = `schema_version: "1"
active_profile: resources-only
profiles:
    resources-only:
        runtime: ""
`

func TestBuildInfoProfiles_Row9_EmptyRuntime_NonLocalBroker(t *testing.T) {
	brokerTypes := []string{"kubernetes", "cloudrun-sandbox", "cloudrun-instances"}

	for _, brokerType := range brokerTypes {
		t.Run(brokerType, func(t *testing.T) {
			srv := profileFilterTestServer(t, emptyRuntimeOnlySettings)
			profiles := srv.buildInfoProfiles(brokerType)

			// The resources-only profile (runtime: "") inherits the broker's
			// default runtime type. canBrokerServeRuntime(x, x) is always true.
			found := false
			for _, p := range profiles {
				if p.Name == "resources-only" {
					found = true
					if p.Type != brokerType {
						t.Errorf("row 9 (%s): expected inherited type %q, got %q",
							brokerType, brokerType, p.Type)
					}
					break
				}
			}
			if !found {
				t.Errorf("row 9 (%s): profile 'resources-only' with empty runtime should survive, but was filtered out. profiles: %v",
					brokerType, profileTypes(profiles))
			}
		})
	}
}

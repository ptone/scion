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

// Spike — task #93: Measure buildInfoProfiles output for cloudrun-sandbox and
// cloudrun-instances brokers under current filtering vs Shape B predicate.
// This file is throwaway — it is on scion/spike-row5 and will never be merged.

package runtimebroker

import (
	"fmt"
	"sort"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// ---------------------------------------------------------------------------
// Reproduce the buildInfoProfiles filtering logic in isolation so we can test
// it with controlled VersionedSettings without needing a full Server or
// filesystem setup. The algorithm matches handlers.go:183-233 exactly.
// ---------------------------------------------------------------------------

// buildInfoProfilesFromSettings reproduces the current (production) filtering
// logic in buildInfoProfiles, taking settings directly rather than loading
// from disk.
func buildInfoProfilesFromSettings(vs *config.VersionedSettings, defaultRuntimeType string) []BrokerProfile {
	if vs == nil || len(vs.Profiles) == 0 {
		return []BrokerProfile{
			{Name: "default", Type: defaultRuntimeType, Available: true},
		}
	}

	names := make([]string, 0, len(vs.Profiles))
	for name := range vs.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	var profiles []BrokerProfile
	for _, name := range names {
		profileCfg := vs.Profiles[name]
		rtType := profileCfg.Runtime
		if rtType == "" {
			rtType = defaultRuntimeType
		}

		// ---- CURRENT FILTER (production) ----
		if !isLocalOnlyRuntime(defaultRuntimeType) && isLocalOnlyRuntime(rtType) {
			continue
		}

		var ctx, ns string
		if vs.Runtimes != nil {
			if rtCfg, ok := vs.Runtimes[rtType]; ok {
				ctx = rtCfg.Context
				ns = rtCfg.Namespace
			}
		}

		profiles = append(profiles, BrokerProfile{
			Name:      name,
			Type:      rtType,
			Available: true,
			Context:   ctx,
			Namespace: ns,
		})
	}

	if len(profiles) == 0 {
		return []BrokerProfile{
			{Name: "default", Type: defaultRuntimeType, Available: true},
		}
	}

	return profiles
}

// canBrokerServeRuntime is the Shape B positive predicate.
// Returns true if the broker type can serve the given profile type:
//   - true if types are equal
//   - true if the broker is local-only (local brokers serve everything)
//   - false otherwise
func canBrokerServeRuntime(brokerType, profileType string) bool {
	if brokerType == profileType {
		return true
	}
	if isLocalOnlyRuntime(brokerType) {
		return true
	}
	return false
}

// buildInfoProfilesShapeB reproduces the filtering logic with Shape B predicate.
func buildInfoProfilesShapeB(vs *config.VersionedSettings, defaultRuntimeType string) []BrokerProfile {
	if vs == nil || len(vs.Profiles) == 0 {
		return []BrokerProfile{
			{Name: "default", Type: defaultRuntimeType, Available: true},
		}
	}

	names := make([]string, 0, len(vs.Profiles))
	for name := range vs.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	var profiles []BrokerProfile
	for _, name := range names {
		profileCfg := vs.Profiles[name]
		rtType := profileCfg.Runtime
		if rtType == "" {
			rtType = defaultRuntimeType
		}

		// ---- SHAPE B FILTER ----
		if !canBrokerServeRuntime(defaultRuntimeType, rtType) {
			continue
		}

		var ctx, ns string
		if vs.Runtimes != nil {
			if rtCfg, ok := vs.Runtimes[rtType]; ok {
				ctx = rtCfg.Context
				ns = rtCfg.Namespace
			}
		}

		profiles = append(profiles, BrokerProfile{
			Name:      name,
			Type:      rtType,
			Available: true,
			Context:   ctx,
			Namespace: ns,
		})
	}

	if len(profiles) == 0 {
		return []BrokerProfile{
			{Name: "default", Type: defaultRuntimeType, Available: true},
		}
	}

	return profiles
}

// ---------------------------------------------------------------------------
// Stock workstation defaults: profiles local/docker and remote/kubernetes.
// ---------------------------------------------------------------------------

func stockWorkstationProfiles() map[string]config.V1ProfileConfig {
	return map[string]config.V1ProfileConfig{
		"local":  {Runtime: "docker"},
		"remote": {Runtime: "kubernetes"},
	}
}

func stockWorkstationRuntimes() map[string]config.V1RuntimeConfig {
	return map[string]config.V1RuntimeConfig{
		"docker":     {Type: "docker"},
		"kubernetes": {Type: "kubernetes"},
	}
}

// Row 2 adds a seeded "default" profile with runtime cloudrun-sandbox
// (this is what the task #92 branch adds).
func row2Profiles() map[string]config.V1ProfileConfig {
	p := stockWorkstationProfiles()
	p["default"] = config.V1ProfileConfig{Runtime: "cloudrun-sandbox"}
	return p
}

func row2Runtimes() map[string]config.V1RuntimeConfig {
	r := stockWorkstationRuntimes()
	r["cloudrun-sandbox"] = config.V1RuntimeConfig{Type: "cloudrun-sandbox"}
	return r
}

// Row 6: a profile with runtime: "" — should inherit defaultRuntimeType.
func row6Profiles() map[string]config.V1ProfileConfig {
	p := stockWorkstationProfiles()
	p["inherit-test"] = config.V1ProfileConfig{Runtime: ""} // empty inherits defaultRuntimeType
	return p
}

// ---------------------------------------------------------------------------
// Test: Measure current and Shape B behaviour side-by-side.
// ---------------------------------------------------------------------------

func TestSpike_BuildInfoProfiles_BlastRadiusTable(t *testing.T) {
	type profileResult struct {
		Name string
		Type string
	}

	tests := []struct {
		name               string
		defaultRuntimeType string // the broker's runtime type
		profiles           map[string]config.V1ProfileConfig
		runtimes           map[string]config.V1RuntimeConfig
	}{
		{
			// Row 1 (baseline): docker broker, stock profiles.
			// Both local/docker and remote/kubernetes should pass.
			name:               "row1_docker_broker_stock",
			defaultRuntimeType: "docker",
			profiles:           stockWorkstationProfiles(),
			runtimes:           stockWorkstationRuntimes(),
		},
		{
			// Row 2: cloudrun-sandbox broker, stock profiles + seeded default/cloudrun-sandbox.
			// Predicted: today=2 (remote+default), Shape B=1 (default only).
			name:               "row2_cloudrun-sandbox_broker_with_seeded_default",
			defaultRuntimeType: "cloudrun-sandbox",
			profiles:           row2Profiles(),
			runtimes:           row2Runtimes(),
		},
		{
			// Row 5: cloudrun-instances broker, stock profiles, NO seeded profile.
			// Predicted: today=1 (remote/kubernetes), Shape B=0 → synthesised default.
			name:               "row5_cloudrun-instances_broker_stock",
			defaultRuntimeType: "cloudrun-instances",
			profiles:           stockWorkstationProfiles(),
			runtimes:           stockWorkstationRuntimes(),
		},
		{
			// Row 6: profile with runtime:"" should inherit defaultRuntimeType.
			// Using docker as broker; the empty-runtime profile should become docker.
			name:               "row6_empty_runtime_inherits_docker",
			defaultRuntimeType: "docker",
			profiles:           row6Profiles(),
			runtimes:           stockWorkstationRuntimes(),
		},
		{
			// Row 6 variant: cloudrun-instances broker, profile with runtime:"".
			// The empty-runtime profile inherits cloudrun-instances.
			// Today: passes filter (neither side is local-only). Under Shape B:
			// canBrokerServeRuntime("cloudrun-instances", "cloudrun-instances") = true.
			name:               "row6_empty_runtime_inherits_cloudrun-instances",
			defaultRuntimeType: "cloudrun-instances",
			profiles:           row6Profiles(),
			runtimes:           stockWorkstationRuntimes(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vs := &config.VersionedSettings{
				Profiles: tt.profiles,
				Runtimes: tt.runtimes,
			}

			currentProfiles := buildInfoProfilesFromSettings(vs, tt.defaultRuntimeType)
			shapeBProfiles := buildInfoProfilesShapeB(vs, tt.defaultRuntimeType)

			toResults := func(profiles []BrokerProfile) []profileResult {
				results := make([]profileResult, len(profiles))
				for i, p := range profiles {
					results[i] = profileResult{Name: p.Name, Type: p.Type}
				}
				return results
			}

			currentResults := toResults(currentProfiles)
			shapeBResults := toResults(shapeBProfiles)

			t.Logf("Broker type: %s", tt.defaultRuntimeType)
			t.Logf("Current filter → %d profiles: %v", len(currentResults), currentResults)
			t.Logf("Shape B filter → %d profiles: %v", len(shapeBResults), shapeBResults)

			// Log individual profiles for clarity
			for _, p := range currentProfiles {
				t.Logf("  CURRENT: name=%q type=%q available=%v", p.Name, p.Type, p.Available)
			}
			for _, p := range shapeBProfiles {
				t.Logf("  SHAPEB:  name=%q type=%q available=%v", p.Name, p.Type, p.Available)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Row 5 dispatch path — does the synthesised "default" profile work?
// ---------------------------------------------------------------------------

func TestSpike_Row5_DispatchPath(t *testing.T) {
	// Under Shape B, row 5 (cloudrun-instances broker, stock profiles) returns 0
	// matching profiles → the len==0 tail synthesises {Name:"default", Type:"cloudrun-instances"}.
	//
	// Question 1: What is in the list?
	// Question 2: Does dispatch succeed?
	//
	// Dispatch path: resolveManagerForOpts loads settings, calls
	// vs.ResolveRuntime(opts.Profile). When opts.Profile is "default" but no
	// profile named "default" exists in settings, ResolveRuntime returns an error.
	// resolveManagerForOpts then falls back to s.manager (the default manager for
	// the broker). For a cloudrun-instances broker, s.manager IS the
	// cloudrun-instances manager. So the synthesised profile successfully dispatches
	// to the correct manager — but via the error-fallback path.

	t.Run("question1_what_is_in_the_list", func(t *testing.T) {
		vs := &config.VersionedSettings{
			Profiles: stockWorkstationProfiles(),
			Runtimes: stockWorkstationRuntimes(),
		}

		profiles := buildInfoProfilesShapeB(vs, "cloudrun-instances")

		t.Logf("Shape B output for cloudrun-instances broker:")
		for _, p := range profiles {
			t.Logf("  name=%q type=%q available=%v", p.Name, p.Type, p.Available)
		}

		// Verify it's the synthesised default
		if len(profiles) != 1 {
			t.Fatalf("expected 1 synthesised profile, got %d", len(profiles))
		}
		if profiles[0].Name != "default" {
			t.Errorf("expected name 'default', got %q", profiles[0].Name)
		}
		if profiles[0].Type != "cloudrun-instances" {
			t.Errorf("expected type 'cloudrun-instances', got %q", profiles[0].Type)
		}
	})

	t.Run("question2_does_dispatch_succeed", func(t *testing.T) {
		// Simulate what resolveManagerForOpts does when it receives the
		// synthesised profile name "default".
		//
		// Step 1: Load settings (stock workstation defaults)
		vs := &config.VersionedSettings{
			ActiveProfile: "local",
			Profiles:      stockWorkstationProfiles(),
			Runtimes:      stockWorkstationRuntimes(),
		}

		// Step 2: vs.ResolveRuntime("default") — profile "default" does not exist
		_, _, err := vs.ResolveRuntime("default")
		t.Logf("ResolveRuntime('default') error: %v", err)

		if err == nil {
			t.Fatal("expected error from ResolveRuntime('default') since no 'default' profile exists in stock settings")
		}

		// Step 3: When err != nil, resolveManagerForOpts returns s.manager.
		// For a cloudrun-instances broker, s.manager IS the cloudrun-instances manager.
		// So: dispatch DOES reach the correct manager, but via the error path.
		t.Log("Dispatch outcome: ResolveRuntime errors → falls back to s.manager (the broker's own manager)")
		t.Log("For cloudrun-instances broker, s.manager = cloudrun-instances manager")
		t.Log("RESULT: Dispatch succeeds, but via error-fallback, not a positive match")

		// Step 4: What about vs.ResolveRuntime("") — using ActiveProfile?
		_, runtimeType, err2 := vs.ResolveRuntime("")
		t.Logf("ResolveRuntime('') with ActiveProfile='local': runtimeType=%q, err=%v", runtimeType, err2)

		if err2 != nil {
			t.Logf("ActiveProfile resolution also fails: %v", err2)
		} else {
			t.Logf("ActiveProfile resolves to runtime type %q", runtimeType)
			// In resolveManagerForOpts, if runtimeType != broker's runtime type,
			// it tries to create a new manager for that runtime.
			// But the opts.Profile is "default" (from the synthesised profile),
			// so it doesn't go through this path — it goes through the error path above.
		}
	})

	t.Run("question2b_dispatch_with_empty_profile", func(t *testing.T) {
		// What if opts.Profile is "" instead of "default"?
		// This is what happens when the user doesn't select a profile.
		// resolveManagerForOpts calls vs.ResolveRuntime(""), which uses ActiveProfile.
		vs := &config.VersionedSettings{
			ActiveProfile: "local",
			Profiles:      stockWorkstationProfiles(),
			Runtimes:      stockWorkstationRuntimes(),
		}

		_, runtimeType, err := vs.ResolveRuntime("")
		t.Logf("ResolveRuntime('') → runtimeType=%q, err=%v", runtimeType, err)

		if err == nil {
			t.Logf("Active profile 'local' resolves to runtime type %q", runtimeType)
			t.Logf("For cloudrun-instances broker: %q != 'cloudrun-instances', so resolveManagerForOpts would try to create a different manager", runtimeType)
			t.Log("This means with empty profile, the broker dispatches to the WRONG runtime (docker, not cloudrun-instances)")
		}
	})
}

// ---------------------------------------------------------------------------
// Test: Row 6 — empty runtime inherits defaultRuntimeType before filter runs.
// ---------------------------------------------------------------------------

func TestSpike_Row6_EmptyRuntimeInheritance(t *testing.T) {
	tests := []struct {
		name               string
		defaultRuntimeType string
		expectedMatch      bool // should the inherit-test profile survive the filter?
	}{
		{
			name:               "docker_broker_empty_runtime_inherits_docker",
			defaultRuntimeType: "docker",
			expectedMatch:      true, // docker==docker
		},
		{
			name:               "kubernetes_broker_empty_runtime_inherits_kubernetes",
			defaultRuntimeType: "kubernetes",
			expectedMatch:      true, // kubernetes==kubernetes
		},
		{
			name:               "cloudrun-sandbox_broker_empty_runtime_inherits_cloudrun-sandbox",
			defaultRuntimeType: "cloudrun-sandbox",
			expectedMatch:      true, // cloudrun-sandbox==cloudrun-sandbox, neither is local-only
		},
		{
			name:               "cloudrun-instances_broker_empty_runtime_inherits_cloudrun-instances",
			defaultRuntimeType: "cloudrun-instances",
			expectedMatch:      true, // cloudrun-instances==cloudrun-instances
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vs := &config.VersionedSettings{
				Profiles: map[string]config.V1ProfileConfig{
					"inherit-test": {Runtime: ""}, // empty runtime
				},
				Runtimes: map[string]config.V1RuntimeConfig{},
			}

			// Current filter
			currentProfiles := buildInfoProfilesFromSettings(vs, tt.defaultRuntimeType)
			// Shape B filter
			shapeBProfiles := buildInfoProfilesShapeB(vs, tt.defaultRuntimeType)

			t.Logf("Broker: %s, Profile runtime: (empty → inherits %s)", tt.defaultRuntimeType, tt.defaultRuntimeType)

			// Check current: the inherited type should always match its own broker
			// Current filter: !isLocalOnlyRuntime(broker) && isLocalOnlyRuntime(profile)
			// Since profile inherits broker type, both sides are the same, so the
			// filter only skips if broker is non-local AND profile is local — which
			// can't happen when they're the same type.
			foundCurrent := false
			for _, p := range currentProfiles {
				if p.Name == "inherit-test" {
					foundCurrent = true
					t.Logf("  CURRENT: inherit-test survived, type=%q", p.Type)
					if p.Type != tt.defaultRuntimeType {
						t.Errorf("expected inherited type %q, got %q", tt.defaultRuntimeType, p.Type)
					}
				}
			}

			foundShapeB := false
			for _, p := range shapeBProfiles {
				if p.Name == "inherit-test" {
					foundShapeB = true
					t.Logf("  SHAPEB: inherit-test survived, type=%q", p.Type)
					if p.Type != tt.defaultRuntimeType {
						t.Errorf("expected inherited type %q, got %q", tt.defaultRuntimeType, p.Type)
					}
				}
			}

			if tt.expectedMatch && !foundCurrent {
				t.Error("CURRENT: expected inherit-test to survive filter, but it was filtered out")
			}
			if tt.expectedMatch && !foundShapeB {
				t.Error("SHAPEB: expected inherit-test to survive filter, but it was filtered out")
			}

			t.Logf("  Current: found=%v, Shape B: found=%v", foundCurrent, foundShapeB)
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Exhaustive before/after summary table.
// ---------------------------------------------------------------------------

func TestSpike_SummaryTable(t *testing.T) {
	type scenario struct {
		label              string
		defaultRuntimeType string
		profiles           map[string]config.V1ProfileConfig
		runtimes           map[string]config.V1RuntimeConfig
	}

	scenarios := []scenario{
		{"Row1 docker", "docker", stockWorkstationProfiles(), stockWorkstationRuntimes()},
		{"Row2 cloudrun-sandbox+default", "cloudrun-sandbox", row2Profiles(), row2Runtimes()},
		{"Row5 cloudrun-instances", "cloudrun-instances", stockWorkstationProfiles(), stockWorkstationRuntimes()},
		{"Row6 docker+empty-runtime", "docker", row6Profiles(), stockWorkstationRuntimes()},
		{"Row6 cloudrun-instances+empty-runtime", "cloudrun-instances", row6Profiles(), stockWorkstationRuntimes()},
	}

	t.Log("=== BLAST RADIUS TABLE ===")
	t.Log(fmt.Sprintf("%-42s | %-8s | %-8s | Current Profiles                      | Shape B Profiles", "Scenario", "Cur #", "ShB #"))
	t.Log("-------------------------------------------+----------+----------+---------------------------------------+----------------------------------")

	for _, sc := range scenarios {
		vs := &config.VersionedSettings{
			Profiles: sc.profiles,
			Runtimes: sc.runtimes,
		}

		cur := buildInfoProfilesFromSettings(vs, sc.defaultRuntimeType)
		shb := buildInfoProfilesShapeB(vs, sc.defaultRuntimeType)

		fmtProfiles := func(pp []BrokerProfile) string {
			var parts []string
			for _, p := range pp {
				parts = append(parts, fmt.Sprintf("%s/%s", p.Name, p.Type))
			}
			s := ""
			for i, p := range parts {
				if i > 0 {
					s += ", "
				}
				s += p
			}
			return s
		}

		t.Log(fmt.Sprintf("%-42s | %-8d | %-8d | %-37s | %s",
			sc.label, len(cur), len(shb), fmtProfiles(cur), fmtProfiles(shb)))
	}
}

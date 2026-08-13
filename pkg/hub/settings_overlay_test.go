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

package hub

import (
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// TestApplySnapshot_WritesRuntimesProfiles verifies that ApplySnapshot
// writes Runtimes, Profiles, HarnessConfigs, and ImageRegistry to s.config.
func TestApplySnapshot_WritesRuntimesProfiles(t *testing.T) {
	srv := &Server{
		maintenance: NewMaintenanceState(false, ""),
	}

	snap := Layer1Snapshot{
		Runtimes: map[string]config.V1RuntimeConfig{
			"cloudrun": {
				Type: "cloudrun_instances",
				CloudRun: &config.V1CloudRunConfig{
					Project: "test-project",
					Region:  "us-central1",
				},
			},
		},
		Profiles: map[string]config.V1ProfileConfig{
			"default": {Runtime: "cloudrun"},
			"local":   {Runtime: "docker"},
		},
		HarnessConfigs: map[string]config.HarnessConfigEntry{
			"claude": {Harness: "claude", Image: "ghcr.io/test/claude:latest"},
		},
		ImageRegistry: "ghcr.io/myorg",
	}

	results := ApplySnapshot(srv, snap)

	// Verify config fields are written
	if len(srv.config.Runtimes) != 1 {
		t.Fatalf("Runtimes: want 1, got %d", len(srv.config.Runtimes))
	}
	if srv.config.Runtimes["cloudrun"].CloudRun == nil {
		t.Fatal("Runtimes[cloudrun].CloudRun: want non-nil")
	}
	if srv.config.Runtimes["cloudrun"].CloudRun.Project != "test-project" {
		t.Errorf("Runtimes[cloudrun].CloudRun.Project: want test-project, got %s", srv.config.Runtimes["cloudrun"].CloudRun.Project)
	}
	if srv.config.Runtimes["cloudrun"].CloudRun.Region != "us-central1" {
		t.Errorf("Runtimes[cloudrun].CloudRun.Region: want us-central1, got %s", srv.config.Runtimes["cloudrun"].CloudRun.Region)
	}

	if len(srv.config.Profiles) != 2 {
		t.Fatalf("Profiles: want 2, got %d", len(srv.config.Profiles))
	}
	if srv.config.Profiles["default"].Runtime != "cloudrun" {
		t.Errorf("Profiles[default].Runtime: want cloudrun, got %s", srv.config.Profiles["default"].Runtime)
	}

	if len(srv.config.HarnessConfigs) != 1 {
		t.Fatalf("HarnessConfigs: want 1, got %d", len(srv.config.HarnessConfigs))
	}
	if srv.config.HarnessConfigs["claude"].Harness != "claude" {
		t.Errorf("HarnessConfigs[claude].Harness: want claude, got %s", srv.config.HarnessConfigs["claude"].Harness)
	}

	if srv.config.ImageRegistry != "ghcr.io/myorg" {
		t.Errorf("ImageRegistry: want ghcr.io/myorg, got %s", srv.config.ImageRegistry)
	}

	// Check applied list
	applied, ok := results["applied"].([]string)
	if !ok {
		t.Fatal("results[applied] is not []string")
	}
	appliedSet := make(map[string]bool)
	for _, a := range applied {
		appliedSet[a] = true
	}
	for _, want := range []string{"runtimes", "profiles", "harness_configs", "image_registry"} {
		if !appliedSet[want] {
			t.Errorf("applied missing %q", want)
		}
	}
}

// TestApplySnapshot_NilMapsPreserveExisting verifies that nil maps in the
// snapshot do not overwrite existing values in s.config.
func TestApplySnapshot_NilMapsPreserveExisting(t *testing.T) {
	srv := &Server{
		maintenance: NewMaintenanceState(false, ""),
	}
	// Pre-populate config
	srv.config.Runtimes = map[string]config.V1RuntimeConfig{
		"docker": {Type: "docker"},
	}
	srv.config.Profiles = map[string]config.V1ProfileConfig{
		"default": {Runtime: "docker"},
	}

	// Snapshot with nil maps — should not overwrite
	snap := Layer1Snapshot{
		// Runtimes, Profiles, HarnessConfigs are nil
		ImageRegistry: "", // empty = no override
	}

	ApplySnapshot(srv, snap)

	if len(srv.config.Runtimes) != 1 {
		t.Errorf("Runtimes should be preserved, got %d entries", len(srv.config.Runtimes))
	}
	if len(srv.config.Profiles) != 1 {
		t.Errorf("Profiles should be preserved, got %d entries", len(srv.config.Profiles))
	}
}

// TestApplySnapshot_EmptyMapClearsSection verifies that an empty (non-nil)
// map in the snapshot replaces existing values (admin explicitly cleared).
func TestApplySnapshot_EmptyMapClearsSection(t *testing.T) {
	srv := &Server{
		maintenance: NewMaintenanceState(false, ""),
	}
	srv.config.Runtimes = map[string]config.V1RuntimeConfig{
		"docker": {Type: "docker"},
	}

	snap := Layer1Snapshot{
		Runtimes: map[string]config.V1RuntimeConfig{}, // explicitly empty
	}

	ApplySnapshot(srv, snap)

	if srv.config.Runtimes == nil {
		t.Fatal("Runtimes should be empty map, not nil")
	}
	if len(srv.config.Runtimes) != 0 {
		t.Errorf("Runtimes should be empty, got %d entries", len(srv.config.Runtimes))
	}
}

// TestPushSettingsOverlay_CallbackInvoked verifies that pushSettingsOverlay
// calls the registered callback with the correct overlay values.
func TestPushSettingsOverlay_CallbackInvoked(t *testing.T) {
	srv := &Server{
		maintenance: NewMaintenanceState(false, ""),
	}
	srv.config.Runtimes = map[string]config.V1RuntimeConfig{
		"cloudrun": {Type: "cloudrun_instances"},
	}
	srv.config.Profiles = map[string]config.V1ProfileConfig{
		"default": {Runtime: "cloudrun"},
	}
	srv.config.HarnessConfigs = map[string]config.HarnessConfigEntry{
		"claude": {Harness: "claude"},
	}
	srv.config.ImageRegistry = "ghcr.io/test"

	var received SettingsOverlay
	var mu sync.Mutex
	srv.SetSettingsOverlayCallback(func(overlay SettingsOverlay) {
		mu.Lock()
		received = overlay
		mu.Unlock()
	})

	overlay := SettingsOverlay{
		Runtimes:       srv.config.Runtimes,
		Profiles:       srv.config.Profiles,
		HarnessConfigs: srv.config.HarnessConfigs,
		ImageRegistry:  srv.config.ImageRegistry,
	}
	srv.pushSettingsOverlay(overlay)

	mu.Lock()
	defer mu.Unlock()

	if len(received.Runtimes) != 1 {
		t.Errorf("overlay.Runtimes: want 1, got %d", len(received.Runtimes))
	}
	if received.Runtimes["cloudrun"].Type != "cloudrun_instances" {
		t.Errorf("overlay.Runtimes[cloudrun].Type: want cloudrun_instances, got %s", received.Runtimes["cloudrun"].Type)
	}
	if len(received.Profiles) != 1 {
		t.Errorf("overlay.Profiles: want 1, got %d", len(received.Profiles))
	}
	if len(received.HarnessConfigs) != 1 {
		t.Errorf("overlay.HarnessConfigs: want 1, got %d", len(received.HarnessConfigs))
	}
	if received.ImageRegistry != "ghcr.io/test" {
		t.Errorf("overlay.ImageRegistry: want ghcr.io/test, got %s", received.ImageRegistry)
	}
}

// TestPushSettingsOverlay_NilCallback verifies that pushSettingsOverlay is
// a no-op when no callback is registered (file-only mode).
func TestPushSettingsOverlay_NilCallback(t *testing.T) {
	srv := &Server{
		maintenance: NewMaintenanceState(false, ""),
	}
	// No callback set — should not panic
	srv.pushSettingsOverlay(SettingsOverlay{})
}

// TestApplySnapshot_InvokesSettingsOverlayCallback verifies that
// ApplySnapshot triggers the settings overlay callback with the values
// from the snapshot.
func TestApplySnapshot_InvokesSettingsOverlayCallback(t *testing.T) {
	var received SettingsOverlay
	var mu sync.Mutex
	called := false

	srv := &Server{
		maintenance: NewMaintenanceState(false, ""),
	}
	srv.SetSettingsOverlayCallback(func(overlay SettingsOverlay) {
		mu.Lock()
		received = overlay
		called = true
		mu.Unlock()
	})

	snap := Layer1Snapshot{
		Runtimes: map[string]config.V1RuntimeConfig{
			"docker": {Type: "docker"},
		},
		Profiles: map[string]config.V1ProfileConfig{
			"default": {Runtime: "docker"},
		},
		HarnessConfigs: map[string]config.HarnessConfigEntry{
			"claude": {Harness: "claude"},
		},
		ImageRegistry: "ghcr.io/test",
	}

	ApplySnapshot(srv, snap)

	mu.Lock()
	defer mu.Unlock()

	if !called {
		t.Fatal("settings overlay callback was not invoked")
	}
	if len(received.Runtimes) != 1 {
		t.Errorf("overlay.Runtimes: want 1, got %d", len(received.Runtimes))
	}
	if received.ImageRegistry != "ghcr.io/test" {
		t.Errorf("overlay.ImageRegistry: want ghcr.io/test, got %s", received.ImageRegistry)
	}
}

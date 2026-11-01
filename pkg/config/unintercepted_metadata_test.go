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

package config

import (
	"testing"
)

func TestUninterceptedMetadataOptOut(t *testing.T) {
	tr := true
	for _, tc := range []struct {
		name        string
		vs          *VersionedSettings
		wantEnabled bool
		wantAck     bool
	}{
		{name: "nil settings"},
		{name: "no runtime block", vs: &VersionedSettings{}},
		{name: "no opt-out", vs: &VersionedSettings{Runtime: &V1RuntimeDefaultsConfig{EnforceResourceDefaults: &tr}}},
		{name: "enabled only", vs: &VersionedSettings{Runtime: &V1RuntimeDefaultsConfig{
			AllowUninterceptedMetadata: &V1UninterceptedMetadataOptOut{Enabled: true}}}, wantEnabled: true},
		{name: "both", vs: &VersionedSettings{Runtime: &V1RuntimeDefaultsConfig{
			AllowUninterceptedMetadata: &V1UninterceptedMetadataOptOut{Enabled: true, AcknowledgeCloudHost: true}}},
			wantEnabled: true, wantAck: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enabled, ack := UninterceptedMetadataOptOut(tc.vs)
			if enabled != tc.wantEnabled || ack != tc.wantAck {
				t.Errorf("UninterceptedMetadataOptOut() = (%v, %v), want (%v, %v)", enabled, ack, tc.wantEnabled, tc.wantAck)
			}
		})
	}
}

// TestUninterceptedMetadataOptOut_GlobalSettingsFile checks that the
// documented YAML keys load from the global settings file and pass schema
// validation.
func TestUninterceptedMetadataOptOut_GlobalSettingsFile(t *testing.T) {
	const doc = `schema_version: "1"
runtime:
  allow_unintercepted_metadata:
    enabled: true
    acknowledge_cloud_host: true
`
	errs, err := ValidateSettings([]byte(doc), "1")
	if err != nil {
		t.Fatalf("ValidateSettings: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("schema validation errors: %v", errs)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFlatTestFile(t, home+"/.scion/settings.yaml", doc)
	vs, _, err := LoadGlobalSettings()
	if err != nil {
		t.Fatal(err)
	}
	if enabled, ack := UninterceptedMetadataOptOut(vs); !enabled || !ack {
		t.Errorf("opt-out = (%v, %v), want (true, true)", enabled, ack)
	}
}

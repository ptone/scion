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
	"reflect"
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

// TestSettingsOverlayCannotCarryUninterceptedMetadataOptOut pins that the
// hub's settings overlay (applied by LoadGlobalSettingsWithOverlay) has no
// path to runtime.allow_unintercepted_metadata: no type reachable from
// SettingsOverlay is the runtime defaults block, the opt-out, or a whole
// VersionedSettings that would carry them. The opt-out is read with
// LoadGlobalSettings, which applies no overlay; this keeps the overlay from
// becoming a hub-writable path to it if a loader is ever switched. The walk
// is over static types, so it cannot see through an interface (any) field;
// it assumes the overlay holds only concrete settings types, as it does.
func TestSettingsOverlayCannotCarryUninterceptedMetadataOptOut(t *testing.T) {
	forbidden := map[reflect.Type]bool{
		reflect.TypeOf(V1RuntimeDefaultsConfig{}):       true,
		reflect.TypeOf(V1UninterceptedMetadataOptOut{}): true,
		reflect.TypeOf(VersionedSettings{}):             true,
	}
	seen := map[reflect.Type]bool{}
	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		if forbidden[typ] {
			t.Errorf("SettingsOverlay reaches %s at %s; the hub overlay must not be able to carry %s",
				typ, path, "runtime.allow_unintercepted_metadata")
			return
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(typ.Elem(), path+"[]")
		case reflect.Map:
			walk(typ.Key(), path+"{key}")
			walk(typ.Elem(), path+"{}")
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				walk(f.Type, path+"."+f.Name)
			}
		}
	}
	walk(reflect.TypeOf(SettingsOverlay{}), "SettingsOverlay")
	if len(seen) < 5 {
		t.Fatalf("walked only %d types; the guard would pass vacuously", len(seen))
	}
}

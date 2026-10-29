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

package opsettings

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// TestRuntimeSchemaTypedFieldParity pins the runtimes section schema's
// property set to V1RuntimeConfig's JSON field names, and its
// additionalProperties to false, so no submitted runtime key passes
// validation only to be dropped by the typed decode.
func TestRuntimeSchemaTypedFieldParity(t *testing.T) {
	sec, ok := rawSchemas["runtimes"]
	if !ok {
		t.Fatal("no runtimes section schema")
	}
	entry, ok := sec["additionalProperties"].(map[string]interface{})
	if !ok {
		t.Fatalf("runtimes section has no per-entry schema: %v", sec["additionalProperties"])
	}
	if ap, ok := entry["additionalProperties"].(bool); !ok || ap {
		t.Errorf("per-entry schema additionalProperties = %v, want false", entry["additionalProperties"])
	}
	props, _ := entry["properties"].(map[string]interface{})
	var schemaKeys []string
	for k := range props {
		schemaKeys = append(schemaKeys, k)
	}
	sort.Strings(schemaKeys)

	var structKeys []string
	typ := reflect.TypeOf(config.V1RuntimeConfig{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Anonymous {
			t.Fatalf("V1RuntimeConfig embeds %s; this test does not handle embedded fields", f.Name)
		}
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		structKeys = append(structKeys, name)
	}
	sort.Strings(structKeys)
	if !reflect.DeepEqual(schemaKeys, structKeys) {
		t.Errorf("runtimes section schema properties differ from V1RuntimeConfig fields:\n  schema: %v\n  struct: %v", schemaKeys, structKeys)
	}

	if errs := Validate("runtimes", json.RawMessage(`{"k": {"type": "kubernetes", "no_such_key": 1}}`)); len(errs) == 0 {
		t.Error("an unknown runtime key passed validation")
	}
	if errs := Validate("runtimes", json.RawMessage(`{"c": {"type": "cloudrun", "cloudrun": {"project_id": "p", "no_such": "x"}}}`)); len(errs) == 0 {
		t.Error("an unknown key in a runtime's cloudrun block passed validation")
	}
}

// TestRuntimePlacementSchemaSnakeCase checks that runtimes and profiles
// accept the placement fields in snake case only, with the closed
// toleration object, and refuse the camelCase spellings.
func TestRuntimePlacementSchemaSnakeCase(t *testing.T) {
	valid := `{"node_selector": {"scion.dev/pool": "agents"}, "tolerations": [{"key": "scion.dev/pool", "operator": "Equal", "value": "agents", "effect": "NoSchedule"}], "runtime_class_name": "gvisor"}`
	for _, sec := range []string{"runtimes", "profiles"} {
		doc := `{"x": ` + strings.Replace(valid, "{", `{"`+map[string]string{"runtimes": "type", "profiles": "runtime"}[sec]+`": "kubernetes", `, 1) + `}`
		if errs := Validate(sec, json.RawMessage(doc)); len(errs) > 0 {
			t.Errorf("%s: valid placement refused: %v", sec, errs)
		}
		for _, bad := range []string{
			`{"nodeSelector": {"a": "b"}}`,
			`{"runtimeClassName": "gvisor"}`,
			`{"node_selector": {"a": 1}}`,
			`{"node_selector": ["a"]}`,
			`{"tolerations": [{"key": "a", "operator": "Gt"}]}`,
			`{"tolerations": [{"key": "a", "effect": "Sometimes"}]}`,
			`{"tolerations": {"key": "a"}}`,
			`{"runtime_class_name": 3}`,
		} {
			if errs := Validate(sec, json.RawMessage(`{"x": `+bad+`}`)); len(errs) == 0 {
				t.Errorf("%s: %s passed validation", sec, bad)
			}
		}
	}
}

// TestTolerationSecondsRejected checks that a toleration's
// tolerationSeconds, in either spelling, is refused rather than dropped.
func TestTolerationSecondsRejected(t *testing.T) {
	for _, sec := range []string{"runtimes", "profiles"} {
		for _, key := range []string{"tolerationSeconds", "toleration_seconds"} {
			doc := `{"x": {"tolerations": [{"key": "a", "operator": "Exists", "` + key + `": 30}]}}`
			if errs := Validate(sec, json.RawMessage(doc)); len(errs) == 0 {
				t.Errorf("%s: %s passed validation", sec, key)
			}
		}
	}
}

// TestFlatPlacementRemainsLayerZero checks that flat instance placement
// (server.broker.instances and every key under it) is never a Layer-1
// key, so the admin settings API cannot write it.
func TestFlatPlacementRemainsLayerZero(t *testing.T) {
	keys := []string{
		"server.broker.instances",
		"server.broker.instances.0.runtime_target.node_selector",
		"server.broker.instances.0.runtime_target.tolerations",
		"server.broker.instances.0.runtime_target.runtime_class_name",
	}
	for _, k := range keys {
		if IsLayer1Key(k) || OwningSection(k) != "" {
			t.Errorf("%s is a Layer-1 key (section %q)", k, OwningSection(k))
		}
	}
	layer1, layer0, _ := ClassifyKeys(keys)
	if len(layer1) != 0 || len(layer0) != len(keys) {
		t.Fatalf("classified layer1 %v, layer0 %v", layer1, layer0)
	}
	for _, sec := range Registry {
		for _, p := range sec.KoanfPaths {
			if p == "server.broker" || strings.HasPrefix(p, "server.broker.") {
				t.Errorf("section %s owns %s", sec.Name, p)
			}
		}
	}
}

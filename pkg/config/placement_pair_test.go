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
	"sort"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// The pair check names settings paths with fixed reasons, sorted, never a
// submitted value; a profile on a built-in default runtime is checked
// against it, and a profile without placement may name any runtime.
func TestValidatePlacementSettingsFixedReasons(t *testing.T) {
	runtimes, profiles := settingsScanFixture()
	runtimes["k8s-bad"] = V1RuntimeConfig{Type: "kubernetes", Context: "c2", Namespace: "x",
		NodeSelector: map[string]string{"secret key!": "v"}, Tolerations: []api.K8sToleration{{Key: "k", Operator: "Bogus"}}, RuntimeClassName: "Secret_Class"}
	profiles["builtin"] = V1ProfileConfig{Runtime: "docker", RuntimeClassName: "gvisor"}
	profiles["builtin-k8s"] = V1ProfileConfig{Runtime: "kubernetes", RuntimeClassName: "gvisor"}
	errs := ValidatePlacementSettings(runtimes, profiles)
	got := map[string]SettingsPlacementCategory{}
	var paths []string
	for _, e := range errs {
		got[e.Path+" "+e.Message] = e.Category
		paths = append(paths, e.Path)
		if strings.Contains(strings.ToLower(e.Message), "secret") || strings.Contains(e.Message, "batch") {
			t.Errorf("reason carries a value: %+v", e)
		}
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("paths not sorted: %v", paths)
	}
	want := map[string]SettingsPlacementCategory{
		"runtimes.k8s-bad.node_selector " + placementReasonSelector:          SettingsInvalidPlacementField,
		"runtimes.k8s-bad.tolerations " + placementReasonTolerations:         SettingsInvalidPlacementField,
		"runtimes.k8s-bad.runtime_class_name " + placementReasonRuntimeClass: SettingsInvalidPlacementField,
		"runtimes.local " + placementReasonNotKubernetes:                     SettingsPlacementOnNonKubernetes,
		"profiles.on-docker " + placementReasonNotKubernetes:                 SettingsPlacementOnNonKubernetes,
		"profiles.builtin " + placementReasonNotKubernetes:                   SettingsPlacementOnNonKubernetes,
		"profiles.missing.runtime " + placementReasonRuntimeMissing:          SettingsProfileRuntimeMissing,
		"profiles.pool-other.node_selector " + placementReasonPoolConflict:   SettingsPoolSelectorConflict,
		"runtimes.k8s-a " + SiblingDisagreementCause("k8s-a", "k8s-b"):       SettingsSiblingPlacementDisagreement,
	}
	if len(got) != len(want) {
		t.Errorf("got %d findings, want %d: %v", len(got), len(want), got)
	}
	for k, c := range want {
		if got[k] != c {
			t.Errorf("missing or miscategorized %q (got %q)", k, got[k])
		}
	}
	if errs := ValidatePlacementSettings(nil, map[string]V1ProfileConfig{"p": {Runtime: "anything"}}); len(errs) != 0 {
		t.Fatalf("a profile without placement on an unknown runtime: %+v", errs)
	}
}

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

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// PlacementSettingsError is one placement finding in the runtimes and
// profiles settings: the settings path to repair, a fixed reason (never a
// submitted value) and the category the pre-upgrade scan counts it under.
type PlacementSettingsError struct {
	Path     string
	Message  string
	Category SettingsPlacementCategory
}

// Fixed reasons of PlacementSettingsError.
const (
	placementReasonSelector       = "not a valid Kubernetes node selector"
	placementReasonTolerations    = "not a valid toleration list"
	placementReasonRuntimeClass   = "not a valid RuntimeClass name"
	placementReasonNotKubernetes  = "placement fields are not valid for this runtime type"
	placementReasonRuntimeMissing = "names a runtime entry that does not exist"
	placementReasonPoolConflict   = "pool selector conflicts with inherited placement"
)

// ValidatePlacementSettings checks the placement of a complete runtimes and
// profiles pair, the one semantic check behind Hub writes and the
// pre-upgrade scan: each placement field as the Kubernetes API server
// checks it; no placement on a runtime entry that is not Kubernetes, or on
// a profile whose runtime entry is not; the runtime entry of a profile
// that sets placement must exist (in runtimes, or among the built-in
// default runtimes); a profile
// may not set the pool selector to another value than its runtime entry;
// and runtime entries with the same context and namespace must agree
// (LiteralSiblingGroups: the Hub cannot resolve a remote current context,
// so other aliases are compared by the Runtime Broker at start). Results
// are sorted by path, then reason.
func ValidatePlacementSettings(runtimes map[string]V1RuntimeConfig, profiles map[string]V1ProfileConfig) []PlacementSettingsError {
	var errs []PlacementSettingsError
	add := func(path, msg string, c SettingsPlacementCategory) {
		errs = append(errs, PlacementSettingsError{Path: path, Message: msg, Category: c})
	}
	fields := func(path string, sel map[string]string, tols []api.K8sToleration, rc string) {
		if ValidateNodeSelector(sel) != nil {
			add(path+".node_selector", placementReasonSelector, SettingsInvalidPlacementField)
		}
		if ValidateTolerations(tols) != nil {
			add(path+".tolerations", placementReasonTolerations, SettingsInvalidPlacementField)
		}
		if ValidateRuntimeClassName(rc) != nil {
			add(path+".runtime_class_name", placementReasonRuntimeClass, SettingsInvalidPlacementField)
		}
	}
	for name, rt := range runtimes {
		path := "runtimes." + name
		fields(path, rt.NodeSelector, rt.Tolerations, rt.RuntimeClassName)
		if RuntimeEntryHasPlacement(rt) && !IsKubernetesRuntimeEntry(name, rt) {
			add(path, placementReasonNotKubernetes, SettingsPlacementOnNonKubernetes)
		}
	}
	builtins := defaultBuiltinCatalog().Runtimes
	for name, p := range profiles {
		path := "profiles." + name
		fields(path, p.NodeSelector, p.Tolerations, p.RuntimeClassName)
		if p.Runtime == "" {
			continue
		}
		rt, ok := runtimes[p.Runtime]
		if !ok {
			rt, ok = builtins[p.Runtime]
		}
		if !ok {
			if ProfileHasPlacement(p) {
				add(path+".runtime", placementReasonRuntimeMissing, SettingsProfileRuntimeMissing)
			}
			continue
		}
		if ProfileHasPlacement(p) && !IsKubernetesRuntimeEntry(p.Runtime, rt) {
			add(path, placementReasonNotKubernetes, SettingsPlacementOnNonKubernetes)
		}
		if ProfilePoolConflict(rt, p) {
			add(path+".node_selector", placementReasonPoolConflict, SettingsPoolSelectorConflict)
		}
	}
	for _, e := range ValidatePlacement(runtimes, nil, LiteralSiblingGroups(runtimes)) {
		if strings.HasPrefix(e.Message, "runtime entries ") {
			add(e.Path, e.Message, SettingsSiblingPlacementDisagreement)
		}
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].Path != errs[j].Path {
			return errs[i].Path < errs[j].Path
		}
		return errs[i].Message < errs[j].Message
	})
	return errs
}

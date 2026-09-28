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
	"testing"
)

func writeHomeSettings(t *testing.T, settingsYAML string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	scionDir := filepath.Join(home, ".scion")
	if err := os.MkdirAll(scionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scionDir, "settings.yaml"), []byte(settingsYAML), 0644); err != nil {
		t.Fatal(err)
	}
}

func infoProfilesByName(profiles []BrokerProfile) map[string]BrokerProfile {
	m := make(map[string]BrokerProfile, len(profiles))
	for _, p := range profiles {
		m[p.Name] = p
	}
	return m
}

// A profile whose runtime entry is keyed differently from its type is
// advertised as the resolved type, with that entry's context/namespace;
// the local-only filter also applies to the resolved type, not the key.
func TestBuildInfoProfiles_RuntimeKeyDiffersFromType_AdvertisesResolvedType(t *testing.T) {
	writeHomeSettings(t, `schema_version: "1"
active_profile: staging
runtimes:
  substrate-nip:
    type: substrate
  k8s-staging:
    type: kubernetes
    context: staging-ctx
    namespace: staging-ns
  workstation-docker:
    type: docker
profiles:
  substrate-nip:
    runtime: substrate-nip
  staging:
    runtime: k8s-staging
  workstation:
    runtime: workstation-docker
`)

	byName := infoProfilesByName((&Server{}).buildInfoProfiles("kubernetes"))

	if p, ok := byName["substrate-nip"]; !ok || p.Type != "substrate" {
		t.Errorf("substrate-nip = %+v (present=%v), want Type %q", p, ok, "substrate")
	}
	p, ok := byName["staging"]
	if !ok || p.Type != "kubernetes" || p.Context != "staging-ctx" || p.Namespace != "staging-ns" {
		t.Errorf("staging = %+v (present=%v), want Type kubernetes, Context staging-ctx, Namespace staging-ns", p, ok)
	}
	if p, ok := byName["workstation"]; ok {
		t.Errorf("workstation (type docker, key workstation-docker) = %+v, want it filtered out on a non-local broker", p)
	}
}

// Settings whose runtime keys equal their types advertise exactly what
// they did before: the key is the type.
func TestBuildInfoProfiles_RuntimeKeyEqualsType_Unchanged(t *testing.T) {
	writeHomeSettings(t, `schema_version: "1"
active_profile: local
runtimes:
  docker:
    type: docker
  kubernetes:
    type: kubernetes
    context: kctx
    namespace: kns
profiles:
  local:
    runtime: docker
  remote:
    runtime: kubernetes
  unnamed: {}
  dangling:
    runtime: no-such-runtime
`)

	byName := infoProfilesByName((&Server{}).buildInfoProfiles("docker"))

	want := map[string]BrokerProfile{
		"local":    {Name: "local", Type: "docker", Available: true},
		"remote":   {Name: "remote", Type: "kubernetes", Available: true, Context: "kctx", Namespace: "kns"},
		"unnamed":  {Name: "unnamed", Type: "docker", Available: true},
		"dangling": {Name: "dangling", Type: "no-such-runtime", Available: true},
	}
	for name, w := range want {
		if got, ok := byName[name]; !ok || got != w {
			t.Errorf("%s = %+v (present=%v), want %+v", name, got, ok, w)
		}
	}
}

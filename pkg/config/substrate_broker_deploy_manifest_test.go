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
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestDeploySubstrateBrokerYAML_SettingsHaveOnlySubstrateProfiles loads the
// settings.yaml embedded in the REAL deploy/substrate/broker.yaml (envsubst
// placeholders filled with a dummy value) on top of the embedded defaults,
// and requires every profile to resolve to a substrate runtime. Unlike
// substrate_broker_profiles_test.go's
// substrateBrokerProfilesOverride (a hand-maintained copy of the same
// stanza, which can drift from the manifest silently), this fails if the
// manifest's local/remote repoint is ever removed or edited incorrectly.
func TestDeploySubstrateBrokerYAML_SettingsHaveOnlySubstrateProfiles(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "substrate", "broker.yaml"))
	require.NoError(t, err)
	expanded := os.Expand(string(raw), func(string) string { return "placeholder" })

	var settings string
	dec := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	for {
		var doc struct {
			Kind     string                `yaml:"kind"`
			Metadata struct{ Name string } `yaml:"metadata"`
			Data     map[string]string     `yaml:"data"`
		}
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else {
			require.NoError(t, err)
		}
		if doc.Kind == "ConfigMap" && doc.Metadata.Name == "scion-substrate-broker-settings" {
			settings = doc.Data["settings.yaml"]
		}
	}
	require.NotEmpty(t, settings, "scion-substrate-broker-settings ConfigMap settings.yaml not found in broker.yaml")

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".scion"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".scion", "settings.yaml"), []byte(settings), 0o644))

	vs, err := LoadVersionedSettings(filepath.Join(tmpDir, "no-project", ".scion"))
	require.NoError(t, err)
	require.NotEmpty(t, vs.Profiles)
	for name, p := range vs.Profiles {
		rt, ok := vs.Runtimes[p.Runtime]
		if !ok || rt.Type != "substrate" {
			t.Errorf("profile %q -> runtime %q (type %q), want a substrate runtime", name, p.Runtime, rt.Type)
		}
	}
	require.Equal(t, "substrate-prod", vs.Profiles["local"].Runtime)
	require.Equal(t, "substrate-prod", vs.Profiles["remote"].Runtime)
}

// brokerManifestDoc is the subset of a Kubernetes object the RBAC test
// below inspects.
type brokerManifestDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Rules []struct {
		APIGroups     []string `yaml:"apiGroups"`
		Resources     []string `yaml:"resources"`
		ResourceNames []string `yaml:"resourceNames"`
		Verbs         []string `yaml:"verbs"`
	} `yaml:"rules"`
	Subjects []struct {
		Kind      string `yaml:"kind"`
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"subjects"`
	RoleRef struct {
		Kind string `yaml:"kind"`
		Name string `yaml:"name"`
	} `yaml:"roleRef"`
	Data map[string]string `yaml:"data"`
}

// TestDeploySubstrateBrokerYAML_AgentStateNamespaceAndRBAC pins the durable
// agent state deployment surface in the REAL deploy/substrate/broker.yaml:
// a dedicated ${STATE_NAMESPACE} Namespace; a Role there granting exactly
// get/list/create/update/delete on secrets (no watch, no patch) bound to the
// broker's ServiceAccount; no new permission in the broker's own namespace
// (in particular nothing on secrets, so its hub credentials stay out of
// reach) or cluster-wide; and settings whose state_namespace names that
// namespace.
func TestDeploySubstrateBrokerYAML_AgentStateNamespaceAndRBAC(t *testing.T) {
	const brokerNS, stateNS = "broker-ns", "state-ns"
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "substrate", "broker.yaml"))
	require.NoError(t, err)
	expanded := os.Expand(string(raw), func(name string) string {
		switch name {
		case "BROKER_NAMESPACE":
			return brokerNS
		case "STATE_NAMESPACE":
			return stateNS
		}
		return "placeholder"
	})

	var docs []brokerManifestDoc
	dec := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	for {
		var doc brokerManifestDoc
		if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else {
			require.NoError(t, err)
		}
		if doc.Kind != "" {
			docs = append(docs, doc)
		}
	}

	var stateNamespaceObj bool
	var stateRoles, stateBindings []brokerManifestDoc
	var settings string
	for _, d := range docs {
		switch {
		case d.Kind == "Namespace" && d.Metadata.Name == stateNS:
			stateNamespaceObj = true
		case d.Metadata.Namespace == stateNS && d.Kind == "Role":
			stateRoles = append(stateRoles, d)
		case d.Metadata.Namespace == stateNS && d.Kind == "RoleBinding":
			stateBindings = append(stateBindings, d)
		case d.Metadata.Namespace == stateNS:
			t.Errorf("%s %q in the state namespace, want nothing there but its Role and RoleBinding", d.Kind, d.Metadata.Name)
		case d.Kind == "ConfigMap" && d.Metadata.Name == "scion-substrate-broker-settings":
			settings = d.Data["settings.yaml"]
		}

		// No new permission anywhere but the state namespace: the broker
		// namespace keeps exactly its TokenRequest Role, and nothing outside
		// the state namespace mentions secrets.
		if d.Kind == "Role" && d.Metadata.Namespace == brokerNS {
			require.Equal(t, "scion-substrate-broker-tokenrequest", d.Metadata.Name, "unexpected Role in the broker namespace")
			require.Len(t, d.Rules, 1)
			require.Equal(t, []string{"serviceaccounts/token"}, d.Rules[0].Resources)
			require.Equal(t, []string{"create"}, d.Rules[0].Verbs)
		}
		if (d.Kind == "Role" || d.Kind == "ClusterRole") && d.Metadata.Namespace != stateNS {
			for _, r := range d.Rules {
				for _, res := range r.Resources {
					if res == "secrets" || res == "*" {
						t.Errorf("%s %q grants %v on %q outside the state namespace", d.Kind, d.Metadata.Name, r.Verbs, res)
					}
				}
			}
		}
	}

	require.True(t, stateNamespaceObj, "no Namespace ${STATE_NAMESPACE} in broker.yaml")

	require.Len(t, stateRoles, 1, "want exactly one Role in the state namespace")
	role := stateRoles[0]
	require.Len(t, role.Rules, 1, "state Role must have exactly one rule")
	rule := role.Rules[0]
	require.Equal(t, []string{""}, rule.APIGroups)
	require.Equal(t, []string{"secrets"}, rule.Resources)
	require.Empty(t, rule.ResourceNames)
	require.ElementsMatch(t, []string{"get", "list", "create", "update", "delete"}, rule.Verbs,
		"state Role verbs must be exactly get/list/create/update/delete (no watch, no patch)")

	require.Len(t, stateBindings, 1, "want exactly one RoleBinding in the state namespace")
	rb := stateBindings[0]
	require.Equal(t, "Role", rb.RoleRef.Kind)
	require.Equal(t, role.Metadata.Name, rb.RoleRef.Name)
	require.Len(t, rb.Subjects, 1)
	require.Equal(t, "ServiceAccount", rb.Subjects[0].Kind)
	require.Equal(t, "scion-substrate-broker", rb.Subjects[0].Name)
	require.Equal(t, brokerNS, rb.Subjects[0].Namespace)

	require.NotEmpty(t, settings, "scion-substrate-broker-settings ConfigMap settings.yaml not found in broker.yaml")
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".scion"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".scion", "settings.yaml"), []byte(settings), 0o644))
	vs, err := LoadVersionedSettings(filepath.Join(tmpDir, "no-project", ".scion"))
	require.NoError(t, err)
	rt, ok := vs.Runtimes["substrate-prod"]
	require.True(t, ok, "settings define no substrate-prod runtime")
	require.NotNil(t, rt.Substrate)
	require.Equal(t, stateNS, rt.Substrate.StateNamespace, "settings' state_namespace must name the state namespace")
}

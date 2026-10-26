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
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// provisionCredsBackend serves a fixed project-scope listing and records
// the calls resolveProvisionCredentials makes. It never serves a value by
// name: Get fails the test.
type provisionCredsBackend struct {
	secret.SecretBackend
	t       *testing.T
	listErr error
	metas   []secret.SecretMeta
	lists   int
	fetches int
}

func (b *provisionCredsBackend) List(context.Context, secret.Filter) ([]secret.SecretMeta, error) {
	b.lists++
	return b.metas, b.listErr
}

func (b *provisionCredsBackend) FetchValues(context.Context, []secret.SecretMeta) (map[string]secret.FetchResult, error) {
	b.fetches++
	return nil, errors.New("not expected")
}

func (b *provisionCredsBackend) Get(context.Context, string, string, string) (*secret.SecretWithValue, error) {
	b.t.Errorf("provision credentials must not read a value by name")
	return nil, errors.New("not expected")
}

// provisionSkillRefs returns a skill reference lister that returns uris as
// the merged skill references of every agent.
func provisionSkillRefs(uris ...string) func(context.Context, *store.Agent) []api.SkillReference {
	return func(context.Context, *store.Agent) []api.SkillReference {
		refs := make([]api.SkillReference, 0, len(uris))
		for _, u := range uris {
			refs = append(refs, api.SkillReference{URI: u})
		}
		return refs
	}
}

func newProvisionCredsDispatcher(backend secret.SecretBackend) (*HTTPAgentDispatcher, *lockedBuffer) {
	buf := &lockedBuffer{}
	// debug=false: the failure warnings and the count must not depend on it.
	d := NewHTTPAgentDispatcherWithClient(nil, nil, false, slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	d.secretBackend = backend
	return d, buf
}

func findProvisionCredsRecord(recs []map[string]any, level, msgSuffix string) map[string]any {
	for _, r := range recs {
		if r["level"] == level && strings.HasSuffix(r["msg"].(string), msgSuffix) {
			return r
		}
	}
	return nil
}

// TestGHCredentialKeysForSkills pins the key set to the broker resolver's
// lookup: ?token= names only its secret; any other GitHub ref names its
// repo key, its owner key and GITHUB_TOKEN; other refs name nothing.
func TestGHCredentialKeysForSkills(t *testing.T) {
	for _, tc := range []struct {
		name string
		uris []string
		want []string
	}{
		{"none", nil, nil},
		{"registry_and_gcp_only", []string{"skill://scion/global/test@1.0.0", "gcp-skill://reg/skill", ""}, nil},
		{"unparseable_gh", []string{"gh://only-owner"}, nil},
		{"gh_shorthand", []string{"gh://Acme-Corp/tools/review@v1"},
			[]string{"GH_ACME_CORP", "GH_ACME_CORP__TOOLS", "GITHUB_TOKEN"}},
		{"github_url", []string{"https://github.com/acme/tools/tree/main/skills/review"},
			[]string{"GH_ACME", "GH_ACME__TOOLS", "GITHUB_TOKEN"}},
		{"token_param_only", []string{"gh://acme/tools/review?token=MY_SKILL_TOKEN"},
			[]string{"MY_SKILL_TOKEN"}},
		{"merged_and_deduplicated", []string{
			"gh://acme/tools/a",
			"gh://acme/tools/b",
			"gh://acme/other/c",
			"gh://beta/repo/d?token=BETA_TOKEN",
			"skill://scion/global/test",
		}, []string{"BETA_TOKEN", "GH_ACME", "GH_ACME__OTHER", "GH_ACME__TOOLS", "GITHUB_TOKEN"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refs []api.SkillReference
			for _, u := range tc.uris {
				refs = append(refs, api.SkillReference{URI: u})
			}
			assert.Equal(t, tc.want, ghCredentialKeysForSkills(refs))
		})
	}
}

// TestProvisionCredentials_NoGitHubRefsSendsNothing: with no gh:// skill
// reference (no lister, or only other references), nothing is listed,
// authorized or fetched, and the count 0 is logged.
func TestProvisionCredentials_NoGitHubRefsSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lister func(context.Context, *store.Agent) []api.SkillReference
	}{
		{"no_lister", nil},
		{"no_refs", provisionSkillRefs()},
		{"non_github_refs", provisionSkillRefs("skill://scion/global/test@1.0.0", "gcp-skill://reg/skill")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &provisionCredsBackend{t: t, metas: []secret.SecretMeta{
				{ID: "s1", Name: "GITHUB_TOKEN", SecretType: store.SecretTypeEnvironment, Scope: store.ScopeProject},
			}}
			d, buf := newProvisionCredsDispatcher(backend)
			d.skillRefLister = tc.lister
			agent := &store.Agent{ID: "agent-1", ProjectID: "project-1"}

			assert.Nil(t, d.resolveProvisionCredentials(context.Background(), agent, "DispatchAgentStart"))
			assert.Zero(t, backend.lists, "no secret is listed")
			assert.Zero(t, backend.fetches, "no secret is fetched")
			info := findProvisionCredsRecord(buf.records(t), "INFO", "ProvisionCredentials resolved")
			require.NotNil(t, info, "log: %s", buf.String())
			assert.EqualValues(t, 0, info["count"])
		})
	}
}

func TestResolveProvisionCredentials_ListFailureWarns(t *testing.T) {
	backend := &provisionCredsBackend{t: t, listErr: errors.New("backend unavailable")}
	d, buf := newProvisionCredsDispatcher(backend)
	d.skillRefLister = provisionSkillRefs("gh://acme/tools/review")
	agent := &store.Agent{ID: "agent-1", ProjectID: "project-1"}

	creds := d.resolveProvisionCredentials(context.Background(), agent, "buildCreateRequest")
	assert.Nil(t, creds)
	assert.Zero(t, backend.fetches)

	rec := findProvisionCredsRecord(buf.records(t), "WARN", "failed to list project secrets for ProvisionCredentials")
	require.NotNil(t, rec, "a List failure must be logged at Warn without debug; log: %s", buf.String())
	assert.Equal(t, "agent-1", rec["agent_id"])
	assert.Equal(t, "project-1", rec["project_id"])
}

// TestProvisionCredentials_NoAuthzServiceSendsNothing: a matching project
// secret is not fetched or sent when the delivery credential cannot be
// minted (no authorization service); the failure is logged at Warn.
func TestProvisionCredentials_NoAuthzServiceSendsNothing(t *testing.T) {
	backend := &provisionCredsBackend{t: t, metas: []secret.SecretMeta{
		{ID: "s1", Name: "GH_ACME", SecretType: store.SecretTypeEnvironment, Scope: store.ScopeProject, ScopeID: "project-1"},
	}}
	d, buf := newProvisionCredsDispatcher(backend)
	d.skillRefLister = provisionSkillRefs("gh://acme/tools/review")
	agent := &store.Agent{ID: "agent-1", ProjectID: "project-1"}

	assert.Nil(t, d.resolveProvisionCredentials(context.Background(), agent, "DispatchAgentRestart"))
	assert.Equal(t, 1, backend.lists)
	assert.Zero(t, backend.fetches, "nothing is fetched without an authorization")
	rec := findProvisionCredsRecord(buf.records(t), "WARN", "ProvisionCredentials not authorized; none sent")
	require.NotNil(t, rec, "log: %s", buf.String())
	assert.Equal(t, "agent-1", rec["agent_id"])
}

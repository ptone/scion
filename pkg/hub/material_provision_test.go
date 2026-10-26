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

//go:build !no_sqlite

package hub

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// provisionTestRefs are the merged skill references of the provision
// credential fixtures: a bare gh:// ref (repo key, owner key and
// GITHUB_TOKEN), a ?token= ref (only its named secret) and a registry ref
// (nothing).
var provisionTestRefs = []string{
	"gh://acme/tools/review",
	"gh://beta/repo/deploy?token=BETA_TOKEN",
	"skill://scion/global/test@1.0.0",
}

// newProvisionCredsFixture is a launch delivery fixture (newLDFixture) whose
// dispatcher reads provisionTestRefs as the agent's merged skill
// references and whose secret backend records what it is asked to fetch.
func newProvisionCredsFixture(t *testing.T, name string, opts ldOptions) (*ldFixture, *ldRecordingSecretBackend, *lockedBuffer) {
	t.Helper()
	m := newLDFixture(t, name, opts)
	rec := &ldRecordingSecretBackend{SecretBackend: m.backend}
	m.d.SetSecretBackend(rec)
	m.d.SetSkillRefLister(provisionSkillRefs(provisionTestRefs...))
	buf := &lockedBuffer{}
	m.d.log = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return m, rec, buf
}

// fetchedNames returns the names of the secrets rec was asked to fetch.
func fetchedNames(rec *ldRecordingSecretBackend) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	names := make([]string, 0, len(rec.fetched))
	for _, m := range rec.fetched {
		names = append(names, m.Name)
	}
	return names
}

// TestProvisionCredentials_LimitedToSkillFetchKeys: only the project
// secrets the agent's gh:// refs can use are fetched and sent, whatever
// their type or injection mode; secrets outside the key set (including the
// owner key of a ?token= ref) are neither fetched nor sent. Each sent entry
// is the secret's name and stored value, as before.
func TestProvisionCredentials_LimitedToSkillFetchKeys(t *testing.T) {
	m, rec, _ := newProvisionCredsFixture(t, "pc-limited", ldOptions{})
	project := m.agent.ProjectID
	m.secret(t, "GH_ACME__TOOLS", "repo-value", store.SecretTypeEnvironment, store.ScopeProject, project)
	m.secret(t, "GH_ACME", "owner-value", store.SecretTypeFile, store.ScopeProject, project)
	m.secret(t, "GITHUB_TOKEN", "default-value", store.SecretTypeVariable, store.ScopeProject, project)
	m.secret(t, "BETA_TOKEN", "named-value", store.SecretTypeEnvironment, store.ScopeProject, project)
	// Outside the key set.
	m.secret(t, "GH_BETA", "beta-owner-value", store.SecretTypeEnvironment, store.ScopeProject, project)
	m.secret(t, "GH_BETA__REPO", "beta-repo-value", store.SecretTypeEnvironment, store.ScopeProject, project)
	m.secret(t, "ANTHROPIC_API_KEY", "llm-value", store.SecretTypeEnvironment, store.ScopeProject, project)
	m.secret(t, "DEPLOY_PASSWORD", "deploy-value", store.SecretTypeFile, store.ScopeProject, project)

	got := m.d.resolveProvisionCredentials(context.Background(), m.agent, "test")
	assert.Equal(t, map[string]string{
		"GH_ACME__TOOLS": "repo-value",
		"GH_ACME":        "owner-value",
		"GITHUB_TOKEN":   "default-value",
		"BETA_TOKEN":     "named-value",
	}, got)
	assert.ElementsMatch(t, []string{"GH_ACME__TOOLS", "GH_ACME", "GITHUB_TOKEN", "BETA_TOKEN"}, fetchedNames(rec),
		"only key-set secrets are fetched")
	assert.Zero(t, rec.gets, "no value is read by name")
	assert.Zero(t, rec.resolves, "Resolve is never called")
}

// TestProvisionCredentials_UserScopeTokenNotSent: only project scope is
// sent. A matching user-scope (owner), hub-scope or broker-scope secret is
// neither fetched nor sent, alone or beside a project secret of the same
// name.
func TestProvisionCredentials_UserScopeTokenNotSent(t *testing.T) {
	seedOtherScopes := func(t *testing.T, m *ldFixture) {
		t.Helper()
		m.secret(t, "GITHUB_TOKEN", "user-value", store.SecretTypeEnvironment, store.ScopeUser, m.agent.OwnerID)
		m.secret(t, "GH_ACME", "user-owner-value", store.SecretTypeEnvironment, store.ScopeUser, m.agent.OwnerID)
		m.secret(t, "GH_ACME__TOOLS", "hub-value", store.SecretTypeEnvironment, store.ScopeHub, m.hubID)
		m.secret(t, "BETA_TOKEN", "broker-value", store.SecretTypeEnvironment, store.ScopeRuntimeBroker, m.broker.ID)
	}
	t.Run("no_project_secret", func(t *testing.T) {
		m, rec, _ := newProvisionCredsFixture(t, "pc-user-only", ldOptions{})
		seedOtherScopes(t, m)

		assert.Nil(t, m.d.resolveProvisionCredentials(context.Background(), m.agent, "test"))
		assert.Empty(t, fetchedNames(rec), "nothing is fetched")
	})
	t.Run("beside_project_secret", func(t *testing.T) {
		m, rec, _ := newProvisionCredsFixture(t, "pc-user-beside", ldOptions{})
		seedOtherScopes(t, m)
		m.secret(t, "GH_ACME", "project-value", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)

		got := m.d.resolveProvisionCredentials(context.Background(), m.agent, "test")
		assert.Equal(t, map[string]string{"GH_ACME": "project-value"}, got)
		rec.mu.Lock()
		defer rec.mu.Unlock()
		require.Len(t, rec.fetched, 1)
		assert.Equal(t, store.ScopeProject, rec.fetched[0].Scope)
	})
}

// TestProvisionCredentials_RefusedAssociationNotSentOrFetched: when the
// agent's project association is refused for delivery (each row a single
// fault on an admitted fixture), no matching project secret is sent and
// none is fetched.
func TestProvisionCredentials_RefusedAssociationNotSentOrFetched(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts ldOptions
		prep func(t *testing.T, m *ldFixture)
	}{
		{name: "no_delegation_edge", opts: ldOptions{noEdge: true}},
		{name: "root_not_admitted_to_project", opts: ldOptions{rootMemberNone: true}},
		{name: "agent_held", prep: func(t *testing.T, m *ldFixture) {
			holdAgentFor(t, m.store, m.agent.ID, m.agent.ProjectID, m.projectOwnerID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec, _ := newProvisionCredsFixture(t, "pc-refused-"+tc.name, tc.opts)
			m.secret(t, "GH_ACME", "project-value", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
			m.secret(t, "GITHUB_TOKEN", "default-value", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
			if tc.prep != nil {
				tc.prep(t, m)
			}

			assert.Nil(t, m.d.resolveProvisionCredentials(context.Background(), m.agent, "test"))
			assert.Empty(t, fetchedNames(rec), "a refused secret is never fetched")
			assert.Zero(t, rec.gets)
		})
	}
}

// TestProvisionCredentials_AdmittedControl is the admitted control for the
// refused rows above: the same fixture and secrets, unchanged, are sent.
func TestProvisionCredentials_AdmittedControl(t *testing.T) {
	m, rec, _ := newProvisionCredsFixture(t, "pc-control", ldOptions{})
	m.secret(t, "GH_ACME", "project-value", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
	m.secret(t, "GITHUB_TOKEN", "default-value", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)

	got := m.d.resolveProvisionCredentials(context.Background(), m.agent, "test")
	assert.Equal(t, map[string]string{"GH_ACME": "project-value", "GITHUB_TOKEN": "default-value"}, got)
	assert.ElementsMatch(t, []string{"GH_ACME", "GITHUB_TOKEN"}, fetchedNames(rec))
}

// TestProvisionCredentials_MintBeforeFetch: the delivery credential is
// minted from the stored agent and every matching item admitted before
// any value is fetched.
func TestProvisionCredentials_MintBeforeFetch(t *testing.T) {
	m, _, _ := newProvisionCredsFixture(t, "pc-order", ldOptions{})
	var log []string
	m.d.SetSecretBackend(&ldRecordingSecretBackend{SecretBackend: m.backend, log: &log})
	m.secret(t, "GH_ACME", "a", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
	m.secret(t, "GH_ACME__TOOLS", "b", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
	hdWithStore(t, m.authz, &ldOrderStore{Store: m.store, target: m.agent.ID, log: &log})

	got := m.d.resolveProvisionCredentials(context.Background(), m.agent, "test")
	require.Len(t, got, 2)

	firstAgent, lastEdges, firstFetch := -1, -1, -1
	for i, e := range log {
		switch e {
		case "agent":
			if firstAgent < 0 {
				firstAgent = i
			}
		case "edges":
			lastEdges = i
		case "fetch":
			if firstFetch < 0 {
				firstFetch = i
			}
		}
	}
	require.GreaterOrEqual(t, firstAgent, 0, "the credential was minted from the stored agent: %v", log)
	require.GreaterOrEqual(t, lastEdges, 0, "the admission walk ran: %v", log)
	require.GreaterOrEqual(t, firstFetch, 0, "values were fetched: %v", log)
	assert.Less(t, firstAgent, firstFetch, "mint precedes fetch: %v", log)
	assert.Less(t, lastEdges, firstFetch, "every admission precedes fetch: %v", log)
}

// TestProvisionCredentials_FetchFailures: a failed item is left out and
// logged by name only; a whole-call fetch error sends nothing; secrets the
// fetch could not read because the request ended are reported in one Warn.
func TestProvisionCredentials_FetchFailures(t *testing.T) {
	seed := func(t *testing.T, m *ldFixture) (failing, ok secret.SecretMeta) {
		t.Helper()
		failing = m.secret(t, "GH_ACME", "value-that-must-not-be-logged-1", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
		ok = m.secret(t, "GITHUB_TOKEN", "value-that-must-not-be-logged-2", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
		return failing, ok
	}
	t.Run("item_error", func(t *testing.T) {
		m, _, buf := newProvisionCredsFixture(t, "pc-item-err", ldOptions{})
		failing, _ := seed(t, m)
		m.d.SetSecretBackend(&ldFaultSecretBackend{SecretBackend: m.backend, itemErrID: failing.ID})

		got := m.d.resolveProvisionCredentials(context.Background(), m.agent, "buildCreateRequest")
		assert.Equal(t, map[string]string{"GITHUB_TOKEN": "value-that-must-not-be-logged-2"}, got)
		recs := buf.records(t)
		warn := findProvisionCredsRecord(recs, "WARN", "failed to get project secret for ProvisionCredentials")
		require.NotNil(t, warn, "log: %s", buf.String())
		assert.Equal(t, "GH_ACME", warn["secret"])
		info := findProvisionCredsRecord(recs, "INFO", "ProvisionCredentials resolved")
		require.NotNil(t, info, "log: %s", buf.String())
		assert.EqualValues(t, 1, info["count"])
		assert.NotContains(t, buf.String(), "value-that-must-not-be-logged", "secret values must never be logged")
	})
	t.Run("whole_fetch_error", func(t *testing.T) {
		m, _, buf := newProvisionCredsFixture(t, "pc-fetch-err", ldOptions{})
		seed(t, m)
		m.d.SetSecretBackend(&ldFaultSecretBackend{SecretBackend: m.backend, fetchErr: errors.New("backend unavailable")})

		assert.Nil(t, m.d.resolveProvisionCredentials(context.Background(), m.agent, "DispatchAgentStart"))
		warn := findProvisionCredsRecord(buf.records(t), "WARN", "failed to fetch project secrets for ProvisionCredentials")
		require.NotNil(t, warn, "log: %s", buf.String())
	})
	t.Run("request_ended", func(t *testing.T) {
		m, _, buf := newProvisionCredsFixture(t, "pc-cancel", ldOptions{})
		seed(t, m)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m.d.SetSecretBackend(&provisionCancelBackend{SecretBackend: m.backend, cancel: cancel})

		assert.Nil(t, m.d.resolveProvisionCredentials(ctx, m.agent, "DispatchAgentRestart"))
		var perSecret, summary int
		var rec map[string]any
		for _, r := range buf.records(t) {
			if r["level"] != "WARN" {
				continue
			}
			switch msg := r["msg"].(string); {
			case msg == "DispatchAgentRestart: failed to get project secret for ProvisionCredentials":
				perSecret++
			case msg == "DispatchAgentRestart: ProvisionCredentials fetch stopped early; some project secrets were not fetched":
				summary++
				rec = r
			}
		}
		assert.Zero(t, perSecret, "no per-secret Warn when the request ended; log: %s", buf.String())
		require.Equal(t, 1, summary, "log: %s", buf.String())
		assert.EqualValues(t, 2, rec["not_fetched"])
	})
}

// provisionCancelBackend ends the request when FetchValues is called and
// reports every item as failed with the context error, as a backend whose
// reads were interrupted would.
type provisionCancelBackend struct {
	secret.SecretBackend
	cancel context.CancelFunc
}

func (b *provisionCancelBackend) FetchValues(ctx context.Context, metas []secret.SecretMeta) (map[string]secret.FetchResult, error) {
	b.cancel()
	out := make(map[string]secret.FetchResult, len(metas))
	for _, m := range metas {
		out[m.ID] = secret.FetchResult{Err: ctx.Err()}
	}
	return out, nil
}

// TestProvisionCredentials_CallerParity: create, start and restart send
// the broker the same provision credentials for the same agent, refs and
// secrets: the key-set project secrets only.
func TestProvisionCredentials_CallerParity(t *testing.T) {
	want := map[string]string{"GH_ACME": "project-value", "BETA_TOKEN": "named-value"}
	seed := func(t *testing.T, m *ldFixture) {
		t.Helper()
		m.secret(t, "GH_ACME", "project-value", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
		m.secret(t, "BETA_TOKEN", "named-value", store.SecretTypeFile, store.ScopeProject, m.agent.ProjectID)
		m.secret(t, "UNRELATED_KEY", "unrelated", store.SecretTypeEnvironment, store.ScopeProject, m.agent.ProjectID)
		m.secret(t, "GITHUB_TOKEN", "user-value", store.SecretTypeEnvironment, store.ScopeUser, m.agent.OwnerID)
	}

	m, _, _ := newProvisionCredsFixture(t, "pc-parity", ldOptions{})
	seed(t, m)
	ctx := context.Background()

	req, err := m.d.buildCreateRequest(ctx, m.agent, "buildCreateRequest")
	require.NoError(t, err)
	assert.Equal(t, want, req.ProvisionCredentials, "create")

	require.NoError(t, m.d.DispatchAgentStart(ctx, m.agent, "", false))
	require.True(t, m.client.startCalled)
	assert.Equal(t, want, m.client.lastStartExtras.ProvisionCredentials, "start")

	require.NoError(t, m.d.DispatchAgentRestart(ctx, m.agent))
	require.True(t, m.client.restartCalled)
	assert.Equal(t, want, m.client.lastRestartExtras.ProvisionCredentials, "restart")
}

// TestProvisionCredentials_NotInContainerEnv: a provision credential goes
// to the broker only. An as_needed project secret in the key set is sent
// as a provision credential and is not placed in the container env or
// secrets of the restart.
func TestProvisionCredentials_NotInContainerEnv(t *testing.T) {
	m, _, _ := newProvisionCredsFixture(t, "pc-not-env", ldOptions{})
	_, _, err := m.backend.Set(context.Background(), &secret.SetSecretInput{
		Name: "GH_ACME", Value: "provision-only", SecretType: store.SecretTypeEnvironment, Target: "GH_ACME",
		Scope: store.ScopeProject, ScopeID: m.agent.ProjectID, InjectionMode: store.InjectionModeAsNeeded, CreatedBy: m.projectOwnerID,
	})
	require.NoError(t, err)

	require.NoError(t, m.d.DispatchAgentRestart(context.Background(), m.agent))
	require.True(t, m.client.restartCalled)
	assert.Equal(t, map[string]string{"GH_ACME": "provision-only"}, m.client.lastRestartExtras.ProvisionCredentials)
	assert.NotContains(t, m.client.lastRestartResolvedEnv, "GH_ACME")
	for _, s := range m.client.lastRestartExtras.ResolvedSecrets {
		assert.NotEqual(t, "GH_ACME", s.Name)
		assert.NotEqual(t, "provision-only", s.Value)
	}
}

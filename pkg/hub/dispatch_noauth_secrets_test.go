// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
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
	"fmt"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// A no-auth agent gets no secrets from the secret store except GITHUB_TOKEN
// on every dispatch path (ptone/scion#6103): create, start and restart.

const (
	noAuthLLMKey        = "FAKE_NOAUTH_LLM_KEY"
	noAuthLLMValue      = "FAKE-noauth-llm-value"
	noAuthFileName      = "FAKE_NOAUTH_CRED_FILE"
	noAuthFileValue     = "FAKE-noauth-file-value"
	noAuthFileTarget    = "~/.fake-noauth/credentials.json"
	noAuthGHProjectVal  = "FAKE-noauth-gh-project-value"
	noAuthGHUserVal     = "FAKE-noauth-gh-user-value"
	noAuthHubID         = "test-hub-noauth"
	noAuthBackendSecret = "FAKE-noauth-hub-secret"
)

// noAuthSecret is a secret stored for the fixture's agent: scope is
// store.ScopeProject (the agent's project) or store.ScopeUser (its owner).
type noAuthSecret struct {
	name, value, typ, target, scope, mode string
}

// newNoAuthFixture returns a run-dispatch fixture whose agent has the given
// no-auth setting and a delivered config (so a start is a plain start), with
// a real local secret backend on the fixture's store.
func newNoAuthFixture(t *testing.T, noAuth bool) (*runDispatchFixture, *secret.LocalBackend) {
	t.Helper()
	f := newRunDispatchFixture(t)
	f.agent.AppliedConfig.NoAuth = noAuth
	f.agent.AppliedConfig.ConfigDelivered = true
	b := secret.NewLocalBackend(f.store, noAuthHubID, noAuthBackendSecret)
	f.d.SetSecretBackend(b)
	return f, b
}

func setNoAuthSecret(t *testing.T, f *runDispatchFixture, b *secret.LocalBackend, s noAuthSecret) {
	t.Helper()
	scopeID := f.agent.ProjectID
	if s.scope == store.ScopeUser {
		scopeID = f.agent.OwnerID
	}
	mode := s.mode
	if mode == "" {
		mode = store.InjectionModeAlways
	}
	_, _, err := b.Set(context.Background(), &secret.SetSecretInput{
		Name: s.name, Value: s.value, SecretType: s.typ, Target: s.target,
		Scope: s.scope, ScopeID: scopeID, InjectionMode: mode,
		CreatedBy: f.agent.OwnerID, UpdatedBy: f.agent.OwnerID,
	})
	require.NoError(t, err)
}

// seedNoAuthSecrets stores an always-mode environment secret and an
// always-mode file secret (LLM-auth shaped) and, when ghScope is not empty,
// a GITHUB_TOKEN environment secret in that scope.
func seedNoAuthSecrets(t *testing.T, f *runDispatchFixture, b *secret.LocalBackend, ghScope string) {
	t.Helper()
	setNoAuthSecret(t, f, b, noAuthSecret{name: noAuthLLMKey, value: noAuthLLMValue,
		typ: store.SecretTypeEnvironment, target: noAuthLLMKey, scope: store.ScopeProject})
	setNoAuthSecret(t, f, b, noAuthSecret{name: noAuthFileName, value: noAuthFileValue,
		typ: store.SecretTypeFile, target: noAuthFileTarget, scope: store.ScopeProject})
	switch ghScope {
	case store.ScopeProject:
		setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
			typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeProject})
	case store.ScopeUser:
		setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHUserVal,
			typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeUser})
	}
}

// startPaths are the two dispatches that use buildStartEnv. env returns the
// env the broker received; sent returns everything the broker received in
// the call (env, secrets for a start, and the extras), formatted, so a test
// can check that no secret value reached the broker by any field. A restart
// has no secrets parameter; its env and extras are what it sends.
var startPaths = []struct {
	name     string
	dispatch func(ctx context.Context, f *runDispatchFixture) error
	env      func(f *runDispatchFixture) map[string]string
	sent     func(f *runDispatchFixture) string
}{
	{
		name: "start",
		dispatch: func(ctx context.Context, f *runDispatchFixture) error {
			return f.d.DispatchAgentStart(ctx, f.agent, "", false)
		},
		env: func(f *runDispatchFixture) map[string]string { return f.client.lastResolvedEnv },
		sent: func(f *runDispatchFixture) string {
			return fmt.Sprintf("%v %+v %+v", f.client.lastResolvedEnv, f.client.lastStartResolvedSecrets, f.client.lastStartExtras)
		},
	},
	{
		name:     "restart",
		dispatch: func(ctx context.Context, f *runDispatchFixture) error { return f.d.DispatchAgentRestart(ctx, f.agent) },
		env:      func(f *runDispatchFixture) map[string]string { return f.client.lastRestartResolvedEnv },
		sent: func(f *runDispatchFixture) string {
			return fmt.Sprintf("%v %+v", f.client.lastRestartResolvedEnv, f.client.lastRestartExtras)
		},
	},
}

// assertNoAuthEnvWithheld checks that env carries no value of the stored
// LLM-auth secrets under any key.
func assertNoAuthEnvWithheld(t *testing.T, env map[string]string, what string) {
	t.Helper()
	_, ok := env[noAuthLLMKey]
	assert.False(t, ok, "%s: the environment secret is withheld", what)
	for k, v := range env {
		assert.NotEqual(t, noAuthLLMValue, v, "%s: the environment secret value is under %s", what, k)
		assert.NotEqual(t, noAuthFileValue, v, "%s: the file secret value is under %s", what, k)
	}
}

func assertNoAuthStartPath(t *testing.T, ghScope, wantGH string) {
	t.Helper()
	for _, p := range startPaths {
		t.Run(p.name, func(t *testing.T) {
			f, b := newNoAuthFixture(t, true)
			seedNoAuthSecrets(t, f, b, ghScope)

			require.NoError(t, p.dispatch(context.Background(), f))
			env := p.env(f)
			require.NotNil(t, env, "the broker was called")
			assertNoAuthEnvWithheld(t, env, p.name)
			sent := p.sent(f)
			assert.NotContains(t, sent, noAuthLLMValue, "%s sends no environment secret by any field", p.name)
			assert.NotContains(t, sent, noAuthFileValue, "%s sends no file secret by any field", p.name)
			if p.name == "start" {
				assert.Empty(t, f.client.lastStartResolvedSecrets, "start sends no secrets")
			}
			assert.Equal(t, wantGH, env["GITHUB_TOKEN"], "GITHUB_TOKEN is still delivered")
		})
	}
}

func TestDispatchAgentStart_NoAuth_WithholdsSecretsKeepsGitHubToken(t *testing.T) {
	f, b := newNoAuthFixture(t, true)
	seedNoAuthSecrets(t, f, b, store.ScopeProject)

	require.NoError(t, f.d.DispatchAgentStart(context.Background(), f.agent, "", false))
	require.True(t, f.client.startCalled, "a plain start")
	assert.False(t, f.client.createCalled, "not routed through create")
	assertNoAuthEnvWithheld(t, f.client.lastResolvedEnv, "start")
	assert.Empty(t, f.client.lastStartResolvedSecrets, "start sends no file or environment secrets")
	assert.Equal(t, noAuthGHProjectVal, f.client.lastResolvedEnv["GITHUB_TOKEN"], "GITHUB_TOKEN is still delivered")
}

func TestDispatchAgentRestart_NoAuth_WithholdsSecretsKeepsGitHubToken(t *testing.T) {
	f, b := newNoAuthFixture(t, true)
	seedNoAuthSecrets(t, f, b, store.ScopeProject)

	require.NoError(t, f.d.DispatchAgentRestart(context.Background(), f.agent))
	require.True(t, f.client.restartCalled)
	assertNoAuthEnvWithheld(t, f.client.lastRestartResolvedEnv, "restart")
	assert.Equal(t, noAuthGHProjectVal, f.client.lastRestartResolvedEnv["GITHUB_TOKEN"], "GITHUB_TOKEN is still delivered")
}

// With no project-scoped GITHUB_TOKEN, the owner's user-scoped one is
// delivered, as on create.
func TestBuildStartEnv_NoAuth_GitHubTokenFallsBackToUserScope(t *testing.T) {
	assertNoAuthStartPath(t, store.ScopeUser, noAuthGHUserVal)
}

// An agent without no-auth still gets its secrets on start and restart.
func TestBuildStartEnv_WithoutNoAuth_DeliversSecrets(t *testing.T) {
	for _, p := range startPaths {
		t.Run(p.name, func(t *testing.T) {
			f, b := newNoAuthFixture(t, false)
			seedNoAuthSecrets(t, f, b, store.ScopeProject)

			require.NoError(t, p.dispatch(context.Background(), f))
			env := p.env(f)
			assert.Equal(t, noAuthLLMValue, env[noAuthLLMKey], "the environment secret is delivered")
			assert.Equal(t, noAuthGHProjectVal, env["GITHUB_TOKEN"])
			if p.name == "start" {
				var names []string
				for _, s := range f.client.lastStartResolvedSecrets {
					names = append(names, s.Name)
				}
				assert.Contains(t, names, noAuthFileName, "start sends the file secret")
				assert.Contains(t, names, noAuthLLMKey, "start sends the environment secret")
			}
		})
	}
}

// buildStartEnv and buildCreateRequest deliver the same secret-store values,
// with the same classification and the same number of secrets for the
// broker, with and without no-auth.
func TestBuildStartEnv_NoAuth_MatchesCreate(t *testing.T) {
	for _, noAuth := range []bool{true, false} {
		t.Run(map[bool]string{true: "no-auth", false: "auth"}[noAuth], func(t *testing.T) {
			ctx := context.Background()
			f, b := newNoAuthFixture(t, noAuth)
			seedNoAuthSecrets(t, f, b, store.ScopeProject)

			req, err := f.d.buildCreateRequest(ctx, f.agent, "TestNoAuthParity")
			require.NoError(t, err)
			for _, site := range []mintSite{mintSiteStart, mintSiteRestart} {
				res, err := f.d.buildStartEnv(ctx, f.agent, "TestNoAuthParity", site)
				require.NoError(t, err, site)
				for _, k := range []string{noAuthLLMKey, "GITHUB_TOKEN"} {
					cv, cok := req.ResolvedEnv[k]
					sv, sok := res.env[k]
					assert.Equal(t, cok, sok, "%s: %s present on create and start alike", site, k)
					assert.Equal(t, cv, sv, "%s: %s value", site, k)
					assert.Equal(t, req.EnvClassifications[k], res.classifications[k], "%s: %s classification", site, k)
				}
				assert.Len(t, res.secrets, len(req.ResolvedSecrets), "%s: secrets for the broker", site)
			}
			if noAuth {
				assert.Equal(t, api.EnvKindSecretFetchable, req.EnvClassifications["GITHUB_TOKEN"])
				assert.Empty(t, req.ResolvedSecrets)
			} else {
				assert.NotEmpty(t, req.ResolvedSecrets)
			}
		})
	}
}

// A no-auth start resolves no secrets, so a failing secret list (which
// fails a normal start closed) does not stop it; GITHUB_TOKEN is read by
// name and still delivered. Create behaves the same way.
func TestBuildStartEnv_NoAuth_DoesNotResolveSecrets(t *testing.T) {
	for _, noAuth := range []bool{true, false} {
		for _, p := range startPaths {
			t.Run(map[bool]string{true: "no-auth/", false: "auth/"}[noAuth]+p.name, func(t *testing.T) {
				f, b := newNoAuthFixture(t, noAuth)
				seedNoAuthSecrets(t, f, b, store.ScopeProject)
				f.d.SetSecretBackend(secret.NewLocalBackend(failListSecretsStore{f.store}, noAuthHubID, noAuthBackendSecret))

				err := p.dispatch(context.Background(), f)
				if !noAuth {
					require.Error(t, err)
					var resErr *secretResolutionError
					assert.ErrorAs(t, err, &resErr)
					assert.Nil(t, p.env(f), "nothing reaches the broker")
					return
				}
				require.NoError(t, err)
				env := p.env(f)
				assertNoAuthEnvWithheld(t, env, p.name)
				assert.Equal(t, noAuthGHProjectVal, env["GITHUB_TOKEN"])
			})
		}
	}
}

// The as-needed pass (resolveAsNeededForKeys) resolves no secrets for a
// no-auth agent either, on every path that runs it: create with env gather,
// provision, finalize env, and a first start routed through create. The
// broker reports failClosedKey as needed (gatherNeedsClient) and the only
// source for it is an as-needed environment secret.

func seedAsNeededSecret(t *testing.T, f *runDispatchFixture, b *secret.LocalBackend) {
	t.Helper()
	setNoAuthSecret(t, f, b, noAuthSecret{name: failClosedKey, value: failClosedValue,
		typ: store.SecretTypeEnvironment, target: failClosedKey, scope: store.ScopeProject,
		mode: store.InjectionModeAsNeeded})
}

// assertAsNeededPath runs dispatch for a no-auth agent and for a control
// agent without no-auth. The no-auth dispatch sends the broker one request
// (no replay) that does not carry the as-needed value, and noAuthOutcome
// checks how the path reports the unmet need. The control dispatch succeeds
// and its replay carries the value.
func assertAsNeededPath(t *testing.T, prepare func(f *runDispatchFixture),
	dispatch func(ctx context.Context, f *runDispatchFixture) error,
	noAuthOutcome func(t *testing.T, f *runDispatchFixture, err error)) {
	t.Helper()
	t.Run("no-auth", func(t *testing.T) {
		f, b := newNoAuthFixture(t, true)
		if prepare != nil {
			prepare(f)
		}
		seedAsNeededSecret(t, f, b)
		setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
			typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeProject})
		calls := gatherNeedsClient(f)

		err := dispatch(context.Background(), f)
		assert.Equal(t, 1, *calls, "the needs report only; no replay with the secret")
		require.NotNil(t, f.client.lastCreateReq)
		_, ok := f.client.lastCreateReq.ResolvedEnv[failClosedKey]
		assert.False(t, ok, "the as-needed secret is not delivered")
		for k, v := range f.client.lastCreateReq.ResolvedEnv {
			assert.NotEqual(t, failClosedValue, v, "the as-needed secret value is under %s", k)
		}
		assert.Equal(t, noAuthGHProjectVal, f.client.lastCreateReq.ResolvedEnv["GITHUB_TOKEN"], "GITHUB_TOKEN is still delivered")
		if err != nil {
			assert.NotContains(t, err.Error(), failClosedValue)
		}
		noAuthOutcome(t, f, err)
	})
	t.Run("auth control", func(t *testing.T) {
		f, b := newNoAuthFixture(t, false)
		if prepare != nil {
			prepare(f)
		}
		seedAsNeededSecret(t, f, b)
		calls := gatherNeedsClient(f)

		require.NoError(t, dispatch(context.Background(), f))
		assert.Equal(t, 2, *calls, "the needs report and the replay")
		require.NotNil(t, f.client.lastCreateReq)
		assert.Equal(t, failClosedValue, f.client.lastCreateReq.ResolvedEnv[failClosedKey], "the replay carries the as-needed value")
	})
}

func TestResolveAsNeededForKeys_NoAuth_SkipsSecrets(t *testing.T) {
	const envKey, envValue = "FAKE_NOAUTH_ASNEEDED_VAR", "FAKE-noauth-asneeded-var-value"
	keys := []string{failClosedKey, envKey}

	t.Run("no-auth", func(t *testing.T) {
		f, b := newNoAuthFixture(t, true)
		seedAsNeededSecret(t, f, b)
		seedAsNeededEnv(t, f, envKey, envValue)
		// A secret list that fails: the pass must not list secrets at all.
		f.d.SetSecretBackend(secret.NewLocalBackend(failListSecretsStore{f.store}, noAuthHubID, noAuthBackendSecret))

		got, err := f.d.resolveAsNeededForKeys(context.Background(), f.agent, keys, nil)
		require.NoError(t, err, "no secret resolution is attempted")
		_, ok := got[failClosedKey]
		assert.False(t, ok, "the as-needed secret is not resolved")
		assert.Equal(t, envValue, got[envKey], "stored as-needed env vars are unchanged")
	})
	t.Run("auth control", func(t *testing.T) {
		f, b := newNoAuthFixture(t, false)
		seedAsNeededSecret(t, f, b)
		seedAsNeededEnv(t, f, envKey, envValue)

		got, err := f.d.resolveAsNeededForKeys(context.Background(), f.agent, keys, nil)
		require.NoError(t, err)
		assert.Equal(t, failClosedValue, got[failClosedKey])
		assert.Equal(t, envValue, got[envKey])
	})
}

func TestDispatchAgentCreateWithGather_NoAuth_AsNeededSecretNotDelivered(t *testing.T) {
	var res *CreateDispatchResult
	assertAsNeededPath(t, nil,
		func(ctx context.Context, f *runDispatchFixture) error {
			var err error
			res, err = f.d.DispatchAgentCreateWithGather(ctx, f.agent)
			return err
		},
		func(t *testing.T, _ *runDispatchFixture, err error) {
			require.NoError(t, err)
			envReqs := res.EnvRequirements()
			require.NotNil(t, envReqs, "the need is reported as unmet")
			assert.Contains(t, envReqs.Needs, failClosedKey)
		})
}

func TestDispatchAgentProvision_NoAuth_AsNeededSecretNotDelivered(t *testing.T) {
	assertAsNeededPath(t, nil,
		func(ctx context.Context, f *runDispatchFixture) error {
			return f.d.DispatchAgentProvision(ctx, f.agent)
		},
		func(t *testing.T, _ *runDispatchFixture, err error) {
			assert.ErrorIs(t, err, errProvisionEnvUnmet)
		})
}

func TestDispatchFinalizeEnv_NoAuth_AsNeededSecretNotDelivered(t *testing.T) {
	assertAsNeededPath(t, nil,
		func(ctx context.Context, f *runDispatchFixture) error {
			_, err := f.d.DispatchFinalizeEnv(ctx, f.agent, nil)
			return err
		},
		func(t *testing.T, _ *runDispatchFixture, err error) {
			var missing *ErrEnvStillMissing
			require.ErrorAs(t, err, &missing, "the need is reported as still missing")
			assert.Contains(t, missing.Requirements.Needs, failClosedKey)
		})
}

func TestDispatchAgentStart_NoAuth_ThroughCreateAsNeededSecretNotDelivered(t *testing.T) {
	assertAsNeededPath(t, neverStarted,
		func(ctx context.Context, f *runDispatchFixture) error {
			return f.d.DispatchAgentStart(ctx, f.agent, "", false)
		},
		func(t *testing.T, f *runDispatchFixture, err error) {
			assert.ErrorIs(t, err, errProvisionEnvUnmet)
			assert.False(t, f.client.startCalled, "nothing started")
		})
}

// The GITHUB_TOKEN a no-auth agent still gets is read by name, so it gets
// the checks normal resolution applies: it is delivered only when
// resolveSecrets and the env merge would put the same value in
// env["GITHUB_TOKEN"].

const noAuthGHOtherTarget = "FAKE_NOAUTH_OTHER_TARGET"

// noAuthGHVariants are GITHUB_TOKEN secrets in the agent's project scope;
// delivered says whether normal resolution puts the value in
// env["GITHUB_TOKEN"].
// There is no empty-target row: the secret backend stores a secret with no
// target under its name as the target, so such a secret is the first row.
var noAuthGHVariants = []struct {
	name      string
	typ       string
	target    string
	mode      string
	delivered bool
}{
	{"environment, target GITHUB_TOKEN, always", store.SecretTypeEnvironment, "GITHUB_TOKEN", store.InjectionModeAlways, true},
	{"environment, other target", store.SecretTypeEnvironment, noAuthGHOtherTarget, store.InjectionModeAlways, false},
	{"file", store.SecretTypeFile, "~/.fake-noauth/gh-token", store.InjectionModeAlways, false},
	{"file, target GITHUB_TOKEN", store.SecretTypeFile, "GITHUB_TOKEN", store.InjectionModeAlways, false},
	{"variable", store.SecretTypeVariable, "GITHUB_TOKEN", store.InjectionModeAlways, false},
	{"internal", store.SecretTypeInternal, "GITHUB_TOKEN", store.InjectionModeAlways, false},
	{"environment, target GITHUB_TOKEN, as needed", store.SecretTypeEnvironment, "GITHUB_TOKEN", store.InjectionModeAsNeeded, false},
}

// noAuthGHEnvs returns env["GITHUB_TOKEN"] (and whether it is set) as
// create, start and restart build it for f's agent.
func noAuthGHEnvs(t *testing.T, f *runDispatchFixture) map[string]*string {
	t.Helper()
	ctx := context.Background()
	out := map[string]*string{}
	pick := func(env map[string]string) *string {
		if v, ok := env["GITHUB_TOKEN"]; ok {
			return &v
		}
		return nil
	}
	req, err := f.d.buildCreateRequest(ctx, f.agent, "TestNoAuthGitHubToken")
	require.NoError(t, err)
	out["create"] = pick(req.ResolvedEnv)
	for _, site := range []mintSite{mintSiteStart, mintSiteRestart} {
		res, err := f.d.buildStartEnv(ctx, f.agent, "TestNoAuthGitHubToken", site)
		require.NoError(t, err, site)
		out[string(site)] = pick(res.env)
	}
	return out
}

func TestNoAuthGitHubToken_TargetRestrictedNotDelivered(t *testing.T) {
	restricted := []struct{ name, typ, target string }{
		{"other target", store.SecretTypeEnvironment, noAuthGHOtherTarget},
		{"file", store.SecretTypeFile, "~/.fake-noauth/gh-token"},
	}
	for _, r := range restricted {
		t.Run(r.name, func(t *testing.T) {
			f, b := newNoAuthFixture(t, true)
			setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
				typ: r.typ, target: r.target, scope: store.ScopeProject})
			for path, got := range noAuthGHEnvs(t, f) {
				assert.Nil(t, got, "%s: a target-restricted GITHUB_TOKEN is not delivered", path)
			}
			req, err := f.d.buildCreateRequest(context.Background(), f.agent, "TestNoAuthGitHubToken")
			require.NoError(t, err)
			for k, v := range req.ResolvedEnv {
				assert.NotEqual(t, noAuthGHProjectVal, v, "the token value is under %s", k)
			}
		})
		t.Run(r.name+", user-scope token deliverable", func(t *testing.T) {
			f, b := newNoAuthFixture(t, true)
			setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
				typ: r.typ, target: r.target, scope: store.ScopeProject})
			setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHUserVal,
				typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeUser})
			for path, got := range noAuthGHEnvs(t, f) {
				require.NotNil(t, got, "%s: the user-scope token is delivered", path)
				assert.Equal(t, noAuthGHUserVal, *got, path)
			}
		})
	}
}

func TestNoAuthGitHubToken_AsNeededNotInjectedEagerly(t *testing.T) {
	f, b := newNoAuthFixture(t, true)
	setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
		typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeProject,
		mode: store.InjectionModeAsNeeded})
	for path, got := range noAuthGHEnvs(t, f) {
		assert.Nil(t, got, "%s: an as-needed GITHUB_TOKEN is not injected eagerly", path)
	}
}

func TestNoAuthGitHubToken_MatchesNormalResolution(t *testing.T) {
	for _, v := range noAuthGHVariants {
		t.Run(v.name, func(t *testing.T) {
			delivered := map[bool]map[string]*string{}
			for _, noAuth := range []bool{false, true} {
				f, b := newNoAuthFixture(t, noAuth)
				setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
					typ: v.typ, target: v.target, scope: store.ScopeProject, mode: v.mode})
				delivered[noAuth] = noAuthGHEnvs(t, f)
			}
			for path, normal := range delivered[false] {
				gotNormal := normal != nil && *normal == noAuthGHProjectVal
				assert.Equal(t, v.delivered, gotNormal, "%s: normal resolution", path)
				noAuth := delivered[true][path]
				gotNoAuth := noAuth != nil && *noAuth == noAuthGHProjectVal
				assert.Equal(t, gotNormal, gotNoAuth, "%s: no-auth delivers GITHUB_TOKEN exactly when normal resolution does", path)
			}
		})
	}
}

// The test-admin scope clamp normal resolution applies
// (restrictTestAdminRootedSecrets) also applies to the no-auth GITHUB_TOKEN:
// an agent rooted at a test admin identity outside its projects gets no
// project-scoped token, but its own user-scoped one.
func TestNoAuthGitHubToken_TestAdminScopeWithheld(t *testing.T) {
	srv, s, _ := tsaServer(t)
	ctx := context.Background()
	issuer := taUser(t, s, "tsa-noauth-iss", store.SystemRoleSuperAdmin)
	root := tsaIssue(t, srv, issuer, testAdminRoleSuperAdmin).Identity.ID
	ownProject := tid("tsa-noauth-own")
	taProject(t, s, ownProject, root)
	otherProject := tid("tsa-noauth-other")
	taProject(t, s, otherProject, issuer.ID)

	ghMode := func(scope, value, mode string) *secret.SecretWithValue {
		return &secret.SecretWithValue{SecretMeta: secret.SecretMeta{Name: "GITHUB_TOKEN",
			SecretType: store.SecretTypeEnvironment, Target: "GITHUB_TOKEN", Scope: scope,
			InjectionMode: mode}, Value: value}
	}
	gh := func(scope, value string) *secret.SecretWithValue {
		return ghMode(scope, value, store.InjectionModeAlways)
	}
	cases := []struct {
		name      string
		rootID    string
		projectID string
		user      bool
		want      string
	}{
		{"test admin root outside its projects: project token withheld", root, otherProject, false, ""},
		{"test admin root outside its projects: own user token delivered", root, otherProject, true, noAuthGHUserVal},
		{"test admin root in its own project: project token delivered", root, ownProject, false, noAuthGHProjectVal},
		{"human root: project token delivered", issuer.ID, otherProject, false, noAuthGHProjectVal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &mockGitTokenSecretBackend{getScopedResponses: map[string]*secret.SecretWithValue{
				"GITHUB_TOKEN:" + secret.ScopeProject: gh(secret.ScopeProject, noAuthGHProjectVal),
			}}
			if tc.user {
				backend.getScopedResponses["GITHUB_TOKEN:"+secret.ScopeUser] = gh(secret.ScopeUser, noAuthGHUserVal)
			}
			d := NewHTTPAgentDispatcherWithClient(s, &mockRuntimeBrokerClient{}, false, nil)
			d.SetHubID("test-hub")
			d.SetAuthzService(srv.authzService)
			d.SetSecretBackend(backend)
			agent := &store.Agent{ID: generateID(), Name: "tsa-noauth", ProjectID: tc.projectID,
				OwnerID: tc.rootID, Ancestry: []string{tc.rootID},
				AppliedConfig: &store.AgentAppliedConfig{NoAuth: true}}

			env := map[string]string{}
			var cls map[string]api.EnvKind
			d.injectNoAuthGitHubToken(ctx, agent, env, &cls)
			assert.Equal(t, tc.want, env["GITHUB_TOKEN"])
		})
		// The same clamp on the as-needed pass, for an as-needed token.
		t.Run(tc.name+" (as-needed pass)", func(t *testing.T) {
			backend := &mockGitTokenSecretBackend{getScopedResponses: map[string]*secret.SecretWithValue{
				"GITHUB_TOKEN:" + secret.ScopeProject: ghMode(secret.ScopeProject, noAuthGHProjectVal, store.InjectionModeAsNeeded),
			}}
			if tc.user {
				backend.getScopedResponses["GITHUB_TOKEN:"+secret.ScopeUser] = ghMode(secret.ScopeUser, noAuthGHUserVal, store.InjectionModeAsNeeded)
			}
			d := NewHTTPAgentDispatcherWithClient(s, &mockRuntimeBrokerClient{}, false, nil)
			d.SetHubID("test-hub")
			d.SetAuthzService(srv.authzService)
			d.SetSecretBackend(backend)
			agent := &store.Agent{ID: generateID(), Name: "tsa-noauth", ProjectID: tc.projectID,
				OwnerID: tc.rootID, Ancestry: []string{tc.rootID},
				AppliedConfig: &store.AgentAppliedConfig{NoAuth: true}}

			got, err := d.resolveAsNeededForKeys(ctx, agent, []string{"GITHUB_TOKEN"}, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got["GITHUB_TOKEN"])
		})
	}
}

// An as-needed GITHUB_TOKEN reaches a no-auth agent through the as-needed
// pass (resolveAsNeededForKeys), strictly under that key and with the same
// filter as the first pass, and nothing else is resolved for it there.
func TestResolveAsNeededForKeys_NoAuth_GitHubTokenOnly(t *testing.T) {
	keys := []string{"GITHUB_TOKEN", failClosedKey, noAuthGHOtherTarget}
	asNeededGH := func(f *runDispatchFixture, b *secret.LocalBackend, typ, target, scope, value string) {
		setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: value, typ: typ, target: target,
			scope: scope, mode: store.InjectionModeAsNeeded})
	}
	t.Run("as-needed GITHUB_TOKEN delivered, no other secret", func(t *testing.T) {
		f, b := newNoAuthFixture(t, true)
		seedAsNeededSecret(t, f, b)
		asNeededGH(f, b, store.SecretTypeEnvironment, "GITHUB_TOKEN", store.ScopeProject, noAuthGHProjectVal)
		// A failing secret list: the exemption reads GITHUB_TOKEN by name only.
		f.d.SetSecretBackend(secret.NewLocalBackend(failListSecretsStore{f.store}, noAuthHubID, noAuthBackendSecret))

		got, err := f.d.resolveAsNeededForKeys(context.Background(), f.agent, keys, nil)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"GITHUB_TOKEN": noAuthGHProjectVal}, got, "only GITHUB_TOKEN")
	})
	t.Run("target-restricted GITHUB_TOKEN not delivered", func(t *testing.T) {
		for _, r := range []struct{ name, typ, target string }{
			{"other target", store.SecretTypeEnvironment, noAuthGHOtherTarget},
			{"file", store.SecretTypeFile, "~/.fake-noauth/gh-token"},
		} {
			t.Run(r.name, func(t *testing.T) {
				f, b := newNoAuthFixture(t, true)
				asNeededGH(f, b, r.typ, r.target, store.ScopeProject, noAuthGHProjectVal)

				got, err := f.d.resolveAsNeededForKeys(context.Background(), f.agent, keys, nil)
				require.NoError(t, err)
				assert.Empty(t, got, "a GITHUB_TOKEN for another target is not delivered under any key")
			})
		}
	})
	t.Run("project token restricted: user-scope token delivered", func(t *testing.T) {
		f, b := newNoAuthFixture(t, true)
		asNeededGH(f, b, store.SecretTypeEnvironment, noAuthGHOtherTarget, store.ScopeProject, noAuthGHProjectVal)
		asNeededGH(f, b, store.SecretTypeEnvironment, "GITHUB_TOKEN", store.ScopeUser, noAuthGHUserVal)

		got, err := f.d.resolveAsNeededForKeys(context.Background(), f.agent, keys, nil)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"GITHUB_TOKEN": noAuthGHUserVal}, got)
	})
	t.Run("auth control", func(t *testing.T) {
		f, b := newNoAuthFixture(t, false)
		seedAsNeededSecret(t, f, b)
		asNeededGH(f, b, store.SecretTypeEnvironment, "GITHUB_TOKEN", store.ScopeProject, noAuthGHProjectVal)

		got, err := f.d.resolveAsNeededForKeys(context.Background(), f.agent, keys, nil)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"GITHUB_TOKEN": noAuthGHProjectVal, failClosedKey: failClosedValue}, got)
	})
}

// A no-auth agent's GITHUB_TOKEN is delivered exactly once, on the pass of
// its injection mode: an eager (always) token on the first request, an
// as-needed one only on the replay that answers the broker's need. The
// broker reports each key the request does not carry as needed.
func TestNoAuthGitHubToken_DeliveredOnceOnItsModesPass(t *testing.T) {
	paths := []struct {
		name     string
		prepare  func(f *runDispatchFixture)
		dispatch func(ctx context.Context, f *runDispatchFixture) error
	}{
		{"create with env gather", nil, func(ctx context.Context, f *runDispatchFixture) error {
			_, err := f.d.DispatchAgentCreateWithGather(ctx, f.agent)
			return err
		}},
		{"provision", nil, func(ctx context.Context, f *runDispatchFixture) error {
			return f.d.DispatchAgentProvision(ctx, f.agent)
		}},
		{"start", neverStarted, func(ctx context.Context, f *runDispatchFixture) error {
			return f.d.DispatchAgentStart(ctx, f.agent, "", false)
		}},
	}
	for _, mode := range []string{store.InjectionModeAlways, store.InjectionModeAsNeeded} {
		for _, p := range paths {
			t.Run(mode+"/"+p.name, func(t *testing.T) {
				f, b := newNoAuthFixture(t, true)
				if p.prepare != nil {
					p.prepare(f)
				}
				setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
					typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeProject, mode: mode})
				var sent []map[string]string
				f.client.createWithGatherFunc = func(_ context.Context, _, _ string, req *RemoteCreateAgentRequest) (*RemoteAgentResponse, *RemoteEnvRequirementsResponse, error) {
					sent = append(sent, maps.Clone(req.ResolvedEnv))
					if req.ResolvedEnv["GITHUB_TOKEN"] == "" {
						return nil, &RemoteEnvRequirementsResponse{AgentID: req.ID, Needs: []string{"GITHUB_TOKEN"}}, nil
					}
					return &RemoteAgentResponse{Agent: &RemoteAgentInfo{ID: req.ID, Slug: req.Slug, Name: req.Name},
						Created: true, Reprovisioned: req.Reprovision}, nil, nil
				}

				require.NoError(t, p.dispatch(context.Background(), f))
				if mode == store.InjectionModeAlways {
					if p.name == "start" {
						// No as-needed entries: a plain start, first pass only.
						assert.True(t, f.client.startCalled, "a plain start")
						assert.Empty(t, sent, "no create")
						assert.Equal(t, noAuthGHProjectVal, f.client.lastResolvedEnv["GITHUB_TOKEN"], "delivered on the first pass")
						return
					}
					require.Len(t, sent, 1, "delivered on the first request; no replay")
					assert.Equal(t, noAuthGHProjectVal, sent[0]["GITHUB_TOKEN"], "delivered on the first pass")
					return
				}
				require.Len(t, sent, 2, "the needs report and one replay")
				_, eager := sent[0]["GITHUB_TOKEN"]
				assert.False(t, eager, "an as-needed GITHUB_TOKEN is not delivered on the first pass")
				assert.Equal(t, noAuthGHProjectVal, sent[1]["GITHUB_TOKEN"], "delivered on the as-needed pass")
				assert.False(t, f.client.startCalled, "a routed start, not a plain one")
			})
		}
	}
}

// The no-auth GITHUB_TOKEN lookup tries the project scope first and the
// owner's user scope only when the project has no deliverable token (the
// ruled order, mirroring resolveCloneToken's clone-token cascade). This is
// deliberately unlike normal resolution, where a user-scope secret
// outranks a project-scope one of the same name: with both present, a
// no-auth agent gets the project token, on the first pass and on the
// as-needed pass.
func TestNoAuthGitHubToken_ProjectScopeBeforeUserScope(t *testing.T) {
	t.Run("first pass", func(t *testing.T) {
		f, b := newNoAuthFixture(t, true)
		setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
			typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeProject})
		setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHUserVal,
			typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeUser})
		for path, got := range noAuthGHEnvs(t, f) {
			require.NotNil(t, got, path)
			assert.Equal(t, noAuthGHProjectVal, *got, "%s: the project token wins", path)
		}
	})
	t.Run("as-needed pass", func(t *testing.T) {
		f, b := newNoAuthFixture(t, true)
		setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHProjectVal,
			typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeProject,
			mode: store.InjectionModeAsNeeded})
		setNoAuthSecret(t, f, b, noAuthSecret{name: "GITHUB_TOKEN", value: noAuthGHUserVal,
			typ: store.SecretTypeEnvironment, target: "GITHUB_TOKEN", scope: store.ScopeUser,
			mode: store.InjectionModeAsNeeded})
		got, err := f.d.resolveAsNeededForKeys(context.Background(), f.agent, []string{"GITHUB_TOKEN"}, nil)
		require.NoError(t, err)
		assert.Equal(t, noAuthGHProjectVal, got["GITHUB_TOKEN"], "the project token wins")
	})
}

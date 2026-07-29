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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the #591 fail-open in checkBrokerDispatchAccess
// (handlers_runtime_brokers.go), the twin of canDispatchToBroker.
//
// The two functions are one decision written twice. Their tests deliberately
// reuse the same fixture and the same broker-identity helper from
// authz_bypass_agents_test.go rather than growing a parallel set: a predicate
// tested two different ways is a predicate that can be shown correct twice and
// still differ. The last test in this file compares them directly on the same
// inputs.
//
// Test naming: everything file-local is prefixed bypassBrokers.

// bypassBrokersRestrictedBroker creates a broker that is NOT auto-provide,
// optionally linked to a project as a provider. Auto-provide short-circuits to
// allow before the caller is examined, so it would mask every case here.
func bypassBrokersRestrictedBroker(t *testing.T, f *bypassAgentsFixture, linkTo string) *store.RuntimeBroker {
	t.Helper()
	b := &store.RuntimeBroker{
		ID:          uuid.New().String(),
		Name:        "restricted-" + uuid.New().String()[:8],
		Slug:        "restricted-" + uuid.New().String()[:8],
		Status:      store.BrokerStatusOnline,
		AutoProvide: false,
		CreatedBy:   f.owner.ID,
		Created:     time.Now(),
		Updated:     time.Now(),
	}
	require.NoError(t, f.store.CreateRuntimeBroker(context.Background(), b))
	if linkTo != "" {
		require.NoError(t, f.store.AddProjectProvider(context.Background(), &store.ProjectProvider{
			ProjectID:  linkTo,
			BrokerID:   b.ID,
			BrokerName: b.Name,
			Status:     store.BrokerStatusOnline,
		}))
	}
	return b
}

// bypassBrokersAgentCtx builds a context carrying an agent identity with the
// given project and scopes.
func bypassBrokersAgentCtx(f *bypassAgentsFixture, projectID string, scopes ...AgentTokenScope) context.Context {
	return contextWithIdentity(context.Background(), &agentIdentityWrapper{&AgentTokenClaims{
		Claims:    jwt.Claims{Subject: f.caller.ID},
		ProjectID: projectID,
		Scopes:    scopes,
	}})
}

// bypassBrokersCheck runs checkBrokerDispatchAccess and returns its verdict
// alongside the response it wrote. Unlike its twin, this function answers in
// status codes as well as a bool, and a denial that returns false without
// writing anything would leave the caller (createAgent,
// handlers_agents_core.go:418) returning 200 with an empty body. So every case
// asserts the response, not just the verdict.
func bypassBrokersCheck(t *testing.T, f *bypassAgentsFixture, ctx context.Context, brokerID string) (bool, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	allowed := f.srv.checkBrokerDispatchAccess(ctx, rec, brokerID)
	return allowed, rec
}

// TestBypassBrokers_CheckBrokerDispatchAccess pins the endpoint-side twin.
//
// Its old `no user identity → allow` branch admitted every non-user caller on
// the create path: it did not merely skip a check, it treated the absence of a
// recognisable caller as proof of permission.
//
// The nil case is *inverted*, not deleted. GetIdentityFromContext returns a
// literal nil interface for unauthenticated requests, and handing that to
// CheckAccess panics on identity.Type() — a 500, not a deny.
func TestBypassBrokers_CheckBrokerDispatchAccess(t *testing.T) {
	t.Run("unauthenticated caller is denied", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		b := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		allowed, rec := bypassBrokersCheck(t, f, context.Background(), b.ID)
		assert.False(t, allowed, "an empty context must deny, not allow — and must not panic")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("unauthenticated caller is denied even on an auto-provide broker", func(t *testing.T) {
		// AutoProvide is a property of the broker, not a licence for anonymous
		// callers: the identity nil-check sits ahead of it.
		f := bypassAgentsSetup(t)
		allowed, rec := bypassBrokersCheck(t, f, context.Background(), f.broker.ID)
		assert.False(t, allowed)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("broker-typed caller is denied", func(t *testing.T) {
		// Deliberate behaviour change. CheckAccess already answered broker
		// identities with "unknown identity type"; the fail-open branch was the
		// only thing letting them past, and it let everything past. Driven
		// through the real middleware so this proves genuine broker traffic
		// lands in the default arm, not merely that a default arm exists.
		f := bypassAgentsSetup(t)
		b := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		allowed, rec := bypassBrokersCheck(t, f, bypassAgentsBrokerCtx(t, f), b.ID)
		assert.False(t, allowed, "a broker-typed caller must not pass the dispatch gate")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("agent with the create scope may dispatch within its own project", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		b := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		allowed, rec := bypassBrokersCheck(t, f, bypassBrokersAgentCtx(f, f.proj.ID, ScopeAgentCreate), b.ID)
		assert.True(t, allowed, "this is the flow the gate exists to preserve: broker selection "+
			"completing a create that authorizeAgentCreate already allowed")
		assert.Equal(t, http.StatusOK, rec.Code, "an allow must not write an error response")
	})

	t.Run("agent without the create scope is denied", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		b := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		allowed, rec := bypassBrokersCheck(t, f,
			bypassBrokersAgentCtx(f, f.proj.ID, ScopeAgentStatusUpdate), b.ID)
		assert.False(t, allowed)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("agent is denied a broker that does not serve its project", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		b := bypassBrokersRestrictedBroker(t, f, f.other.ID)
		allowed, rec := bypassBrokersCheck(t, f,
			bypassBrokersAgentCtx(f, f.proj.ID, ScopeAgentCreate), b.ID)
		assert.False(t, allowed, "holding the create scope must not reach brokers outside the agent's project")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("auto-provide broker is open to authorized callers, not to every caller", func(t *testing.T) {
		// #591 behaviour change (task 27): AutoProvide relaxes ONLY the
		// project-linkage requirement, never identity-type or scope. So an
		// auto-provide broker stays open to any AUTHORIZED caller — a scoped agent
		// from ANY project, and an authenticated user — but a scopeless agent is
		// denied on it just as on a restricted broker.
		f := bypassAgentsSetup(t)

		// A scoped agent whose project the auto-provide broker does NOT serve:
		// allowed, because linkage is the only thing AutoProvide skips.
		allowedAgent, _ := bypassBrokersCheck(t, f, bypassBrokersAgentCtx(f, f.other.ID, ScopeAgentCreate), f.broker.ID)
		assert.True(t, allowedAgent, "a scoped agent from any project may use an available-to-all broker")

		// An authenticated non-owner user: allowed (the available-to-all user path).
		stranger := &store.User{
			ID:          tid("bypass-brokers-autoprovide-user"),
			Email:       "autoprovide-brokers-user@example.com",
			DisplayName: "Stranger",
			Role:        store.UserRoleMember,
			Status:      "active",
			Created:     time.Now(),
		}
		require.NoError(t, f.store.CreateUser(context.Background(), stranger))
		strangerCtx := contextWithIdentity(context.Background(),
			NewAuthenticatedUser(stranger.ID, stranger.Email, stranger.DisplayName, string(stranger.Role), "cli"))
		allowedUser, _ := bypassBrokersCheck(t, f, strangerCtx, f.broker.ID)
		assert.True(t, allowedUser, "an authenticated user may use an available-to-all broker")

		// A scopeless agent: denied even here — scope is enforced regardless of AutoProvide.
		deniedAgent, rec := bypassBrokersCheck(t, f, bypassBrokersAgentCtx(f, f.proj.ID), f.broker.ID)
		assert.False(t, deniedAgent, "a scopeless agent is denied even on an auto-provide broker")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("user path is unchanged", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		b := bypassBrokersRestrictedBroker(t, f, f.proj.ID)

		ownerCtx := contextWithIdentity(context.Background(),
			NewAuthenticatedUser(f.owner.ID, f.owner.Email, f.owner.DisplayName, string(f.owner.Role), "cli"))
		allowed, _ := bypassBrokersCheck(t, f, ownerCtx, b.ID)
		assert.True(t, allowed, "the broker's creator must still be able to dispatch to it")

		stranger := &store.User{
			ID:          tid("bypass-brokers-stranger"),
			Email:       "stranger-brokers@example.com",
			DisplayName: "Stranger",
			Role:        store.UserRoleMember,
			Status:      "active",
			Created:     time.Now(),
		}
		require.NoError(t, f.store.CreateUser(context.Background(), stranger))
		strangerCtx := contextWithIdentity(context.Background(),
			NewAuthenticatedUser(stranger.ID, stranger.Email, stranger.DisplayName, string(stranger.Role), "cli"))
		denied, rec := bypassBrokersCheck(t, f, strangerCtx, b.ID)
		assert.False(t, denied, "an unrelated user must not dispatch to someone else's non-auto-provide broker")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("dev identity takes the user path", func(t *testing.T) {
		// "dev" is a user kind, not an unknown type: it must be evaluated by
		// policy and must not fall into the default deny arm.
		f := bypassAgentsSetup(t)
		b := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		devCtx := contextWithIdentity(context.Background(), &bypassAgentsDevIdentity{
			UserIdentity: NewAuthenticatedUser(f.owner.ID, f.owner.Email, f.owner.DisplayName,
				string(f.owner.Role), "cli"),
		})
		allowed, _ := bypassBrokersCheck(t, f, devCtx, b.ID)
		assert.True(t, allowed, "a dev caller must be evaluated by policy, not rejected as an unknown type")
	})

	t.Run("unknown broker still reports the store error", func(t *testing.T) {
		// The lookup moved below the identity check, so this pins that a
		// missing broker is still answered as a missing broker for an
		// authenticated caller, rather than being masked as a permission denial.
		f := bypassAgentsSetup(t)
		allowed, rec := bypassBrokersCheck(t, f,
			bypassBrokersAgentCtx(f, f.proj.ID, ScopeAgentCreate), uuid.New().String())
		assert.False(t, allowed)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

// TestBypassBrokers_TwinsAgree is the anti-drift test.
//
// checkBrokerDispatchAccess and canDispatchToBroker are the same decision
// written twice, in two files, converted by two people. Each is individually
// tested above and in authz_bypass_agents_test.go, but passing tests on both
// sides does not establish that they AGREE — that is a separate property, and
// it is the one that matters: if they drift, the weaker of the two becomes the
// hole, and the create path consults both.
//
// So this evaluates both over the same matrix of caller kinds and brokers and
// asserts identical verdicts. A future edit to one that is not mirrored in the
// other fails here rather than in production.
func TestBypassBrokers_TwinsAgree(t *testing.T) {
	f := bypassAgentsSetup(t)

	serving := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
	foreign := bypassBrokersRestrictedBroker(t, f, f.other.ID)
	unlinked := bypassBrokersRestrictedBroker(t, f, "")

	ownerCtx := contextWithIdentity(context.Background(),
		NewAuthenticatedUser(f.owner.ID, f.owner.Email, f.owner.DisplayName, string(f.owner.Role), "cli"))
	devCtx := contextWithIdentity(context.Background(), &bypassAgentsDevIdentity{
		UserIdentity: NewAuthenticatedUser(f.owner.ID, f.owner.Email, f.owner.DisplayName,
			string(f.owner.Role), "cli"),
	})

	callers := []struct {
		name string
		ctx  context.Context
	}{
		{"unauthenticated", context.Background()},
		{"broker", bypassAgentsBrokerCtx(t, f)},
		{"agent with create scope", bypassBrokersAgentCtx(f, f.proj.ID, ScopeAgentCreate)},
		{"agent without create scope", bypassBrokersAgentCtx(f, f.proj.ID, ScopeAgentStatusUpdate)},
		{"agent in another project", bypassBrokersAgentCtx(f, f.other.ID, ScopeAgentCreate)},
		{"agent with no project", bypassBrokersAgentCtx(f, "", ScopeAgentCreate)},
		{"owner user", ownerCtx},
		{"dev", devCtx},
	}

	brokers := []struct {
		name   string
		broker *store.RuntimeBroker
	}{
		{"serves the agent's project", serving},
		{"serves another project", foreign},
		{"serves no project", unlinked},
		{"auto-provide", f.broker},
	}

	for _, c := range callers {
		for _, b := range brokers {
			t.Run(c.name+"/"+b.name, func(t *testing.T) {
				viaHelper := f.srv.canDispatchToBroker(c.ctx, b.broker)
				viaHandler, _ := bypassBrokersCheck(t, f, c.ctx, b.broker.ID)
				assert.Equal(t, viaHelper, viaHandler,
					"canDispatchToBroker and checkBrokerDispatchAccess must agree; they are "+
						"one decision written twice and the create path consults both")
			})
		}
	}
}

// TestBypassBrokers_UpdateAndDeleteBroker covers the other two #591 sites in
// handlers_runtime_brokers.go: updateRuntimeBroker (:342 before conversion) and
// deleteRuntimeBroker (:387). Both had the Group A shape —
//
//	if userIdent := GetUserIdentityFromContext(ctx); userIdent != nil { check }
//
// — so the check ran for users and was skipped in silence for every other caller
// kind. These are separate from the dispatch fail-open above; they are in the
// same file and are converted in the same pass.
//
// Requests are driven through the full middleware chain via Server.Handler(),
// not by calling the handler, because the point is what a real credential can
// do end to end.
func TestBypassBrokers_UpdateAndDeleteBroker(t *testing.T) {
	t.Run("broker-typed caller cannot rename a broker", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		target := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		rec := f.asBroker(t, http.MethodPatch, "/api/v1/runtime-brokers/"+target.ID,
			map[string]interface{}{"name": "hijacked"})
		assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())

		got, err := f.store.GetRuntimeBroker(context.Background(), target.ID)
		require.NoError(t, err)
		assert.NotEqual(t, "hijacked", got.Name, "the denied update must not have been applied")
	})

	t.Run("agent caller cannot rename a broker", func(t *testing.T) {
		// Even a broker that serves the agent's own project: dispatching to a
		// broker and administering one are different permissions.
		f := bypassAgentsSetup(t)
		target := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		rec := f.asAgent(t, http.MethodPatch, "/api/v1/runtime-brokers/"+target.ID,
			map[string]interface{}{"name": "hijacked"}, ScopeAgentCreate)
		assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())

		got, err := f.store.GetRuntimeBroker(context.Background(), target.ID)
		require.NoError(t, err)
		assert.NotEqual(t, "hijacked", got.Name)
	})

	t.Run("broker-typed caller cannot delete a broker", func(t *testing.T) {
		// Delete is the worst of the two: it also unlinks the broker from every
		// project and clears their default_runtime_broker_id.
		f := bypassAgentsSetup(t)
		target := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		rec := f.asBroker(t, http.MethodDelete, "/api/v1/runtime-brokers/"+target.ID, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())

		_, err := f.store.GetRuntimeBroker(context.Background(), target.ID)
		assert.NoError(t, err, "the broker must still exist after a denied delete")
	})

	t.Run("agent caller cannot delete a broker", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		target := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		rec := f.asAgent(t, http.MethodDelete, "/api/v1/runtime-brokers/"+target.ID, nil, ScopeAgentCreate)
		assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())

		_, err := f.store.GetRuntimeBroker(context.Background(), target.ID)
		assert.NoError(t, err)
	})

	t.Run("a broker cannot delete itself", func(t *testing.T) {
		// The most plausible-sounding case, and still no: broker lifecycle is
		// administered from the hub side. Deregistration has its own path.
		f := bypassAgentsSetup(t)
		rec := f.asBroker(t, http.MethodDelete, "/api/v1/runtime-brokers/"+f.broker.ID, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())

		_, err := f.store.GetRuntimeBroker(context.Background(), f.broker.ID)
		assert.NoError(t, err, "a broker must not be able to delete its own registration")
	})

	t.Run("user path is unchanged", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		target := bypassBrokersRestrictedBroker(t, f, f.proj.ID)

		rec := doRequestAsUser(t, f.srv, f.owner, http.MethodPatch, "/api/v1/runtime-brokers/"+target.ID,
			map[string]interface{}{"name": "renamed-by-owner"})
		assert.Equal(t, http.StatusOK, rec.Code, "the broker's creator must still rename it; body: %s",
			rec.Body.String())
		got, err := f.store.GetRuntimeBroker(context.Background(), target.ID)
		require.NoError(t, err)
		assert.Equal(t, "renamed-by-owner", got.Name, "the allowed update must have been applied")

		rec = doRequestAsUser(t, f.srv, f.owner, http.MethodDelete, "/api/v1/runtime-brokers/"+target.ID, nil)
		assert.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
	})

	t.Run("unrelated user is denied", func(t *testing.T) {
		f := bypassAgentsSetup(t)
		target := bypassBrokersRestrictedBroker(t, f, f.proj.ID)
		stranger := &store.User{
			ID:          tid("bypass-brokers-admin-stranger"),
			Email:       "stranger-admin@example.com",
			DisplayName: "Stranger",
			Role:        store.UserRoleMember,
			Status:      "active",
			Created:     time.Now(),
		}
		require.NoError(t, f.store.CreateUser(context.Background(), stranger))

		rec := doRequestAsUser(t, f.srv, stranger, http.MethodDelete, "/api/v1/runtime-brokers/"+target.ID, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
		_, err := f.store.GetRuntimeBroker(context.Background(), target.ID)
		assert.NoError(t, err)
	})
}

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

// #591 NB-8D7-1 — handleAgentPTY over-denial.
//
// handleAgentPTY used a blanket inline deny of every non-user caller. It failed
// CLOSED, so it was never a hole, but it OVER-DENIED: an ANCESTOR could not
// attach to its OWN DESCENDANT's PTY, a flow design 5.5 retains through the
// ancestry bypass. The fix is the design:598 check, s.authorize with
// agentResource and ActionAttach, which routes the decision to the engine.
//
// WHAT MAKES THE PASS ARM CREDIBLE. A PTY attach cannot complete in a unit test
// — it wants a websocket and a live runtime broker — so "passed" is pinned on
// the FIRST THING PAST THE GATE instead: the target agents here carry no
// RuntimeBrokerID, so a caller who clears authorization lands on 422
// no_runtime_broker. That code is reachable ONLY from beyond the gate, which is
// what makes it a positive witness rather than a not-403. Asserting NotEqual(403)
// would bank a 400 or a 404 from a renamed route as success.
//
// ON THE ANON ARM: anonymous is refused by the upstream auth middleware, so it
// is a FLOOR, not a gate-liveness witness. It is asserted here to document that
// floor and would stay 401 with this gate deleted entirely — it proves nothing
// about the gate and must not be cited as if it did.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// ptyPath is the PTY route. The dispatcher only reaches handleAgentPTY when the
// request is BOTH /api/v1/agents/<id>/pty AND a websocket upgrade
// (handlers_agents_core.go), so every helper below sets the upgrade headers.
func ptyPath(agentID string) string { return "/api/v1/agents/" + agentID + "/pty" }

func ptyUpgradeHeaders(req *http.Request) *http.Request {
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	return req
}

// ptyAsAgent issues a PTY attach with a real agent token minted by the real
// token service and served through the real Handler(). Scopes are nil, matching
// the fixture default: handleAgentPTY performs no scope check, and deliberately
// does not route through authorizeAgentLifecycle (which would admit project
// peers on scope alone — see the handler comment).
func ptyAsAgent(t *testing.T, f *wsGateFixture, caller *store.Agent, target string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(caller.ID, caller.ProjectID, nil, nil)
	require.NoError(t, err)

	req := ptyUpgradeHeaders(f.newRequest(http.MethodGet, ptyPath(target), nil))
	req.Header.Set("X-Scion-Agent-Token", tok)
	return f.serve(req)
}

func ptyAsUser(t *testing.T, f *wsGateFixture, u *store.User, target string) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetUserTokenService()
	require.NotNil(t, svc)
	tok, _, err := svc.GenerateAccessToken(u.ID, u.Email, u.DisplayName, string(u.Role), ClientTypeAPI)
	require.NoError(t, err)

	req := ptyUpgradeHeaders(f.newRequest(http.MethodGet, ptyPath(target), nil))
	req.Header.Set("Authorization", "Bearer "+tok)
	return f.serve(req)
}

// ptyBrokerServe signs a prebuilt request with the fixture's broker credentials.
// It mirrors wsGateFixture.asBroker, which builds its own request and so cannot
// carry the websocket upgrade headers this route requires.
func ptyBrokerServe(t *testing.T, f *wsGateFixture, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "pty-nonce-" + uuid.New().String()
	req.Header.Set(HeaderBrokerID, f.broker.ID)
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderNonce, nonce)

	svc := f.srv.brokerAuthService
	require.NotNil(t, svc, "broker auth service must be configured")
	mac := hmac.New(sha256.New, f.brokerSecret)
	mac.Write(svc.buildCanonicalString(req, timestamp, nonce))
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return f.serve(req)
}

// ptyMakeAgent creates an agent in the given project with an explicit ancestry
// chain and NO runtime broker, so a caller who passes the gate stops at 422.
func ptyMakeAgent(t *testing.T, f *wsGateFixture, name, projectID string, ancestry []string) *store.Agent {
	t.Helper()
	a := &store.Agent{
		ID: tid(name), Slug: tid(name), Name: name,
		ProjectID: projectID,
		CreatedBy: f.owner.ID, OwnerID: f.owner.ID,
		Ancestry: ancestry,
	}
	require.NoError(t, f.store.CreateAgent(context.Background(), a))
	return a
}

// TestPTYAttach_AncestorPassesSiblingAndCrossProjectDenied is the NB-8D7-1 pin.
//
// RED-WITHOUT-FIX (measured before commit, by restoring the blanket
// `if GetUserIdentityFromContext(ctx) == nil { 403 }`): the ancestor arm goes
// 422 -> 403. Every deny arm below stays 403 either way, which is exactly why
// the deny arms alone could not have caught this regression and why the pass
// arm is the load-bearing one.
func TestPTYAttach_AncestorPassesSiblingAndCrossProjectDenied(t *testing.T) {
	f := wsGateSetup(t)

	// parent -> child, both in projA. The child's ancestry names the parent, so
	// the parent is a GENUINE ancestor rather than merely a project peer.
	parent := ptyMakeAgent(t, f, "pty-parent", f.projA.ID, []string{f.owner.ID})
	child := ptyMakeAgent(t, f, "pty-child", f.projA.ID, []string{f.owner.ID, parent.ID})

	// A same-project agent that is NOT in the child's ancestry. This is the arm
	// that separates "ancestry bypass" from "anyone in the project": if attach
	// were ever routed through the project read baseline or through
	// authorizeAgentLifecycle's scope-only agent arm, this would pass.
	sibling := ptyMakeAgent(t, f, "pty-sibling", f.projA.ID, []string{f.owner.ID})

	// A cross-project agent, ancestry deliberately empty of anything in projA.
	outsider := ptyMakeAgent(t, f, "pty-outsider", f.projB.ID, []string{f.owner.ID})

	t.Run("ANCESTOR attaching to its own descendant PASSES the gate", func(t *testing.T) {
		rec := ptyAsAgent(t, f, parent, child.ID)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code,
			"an ancestor must clear the PTY gate on its own descendant and stop at the "+
				"runtime-broker check (422). Got %d. A 403 here is the NB-8D7-1 over-denial "+
				"regression: the blanket non-user deny is back, or ActionAttach stopped "+
				"reaching the ancestry bypass in checkAccessForAgent. body=%s",
			rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), ErrCodeNoRuntimeBroker,
			"the 422 must be the no-runtime-broker body — that code is reachable only from "+
				"PAST the authorization gate, and it is what makes this a positive pass "+
				"witness rather than a not-403; body=%s", rec.Body.String())
	})

	t.Run("SIBLING in the same project is DENIED", func(t *testing.T) {
		rec := ptyAsAgent(t, f, sibling, child.ID)
		require.Equal(t, http.StatusForbidden, rec.Code,
			"a same-project agent with no ancestry claim must be refused: attach is not "+
				"read-class, so the project read baseline must not reach it (authz.go step 3, "+
				"property 3). body=%s", rec.Body.String())
	})

	t.Run("CROSS-PROJECT agent is DENIED", func(t *testing.T) {
		rec := ptyAsAgent(t, f, outsider, child.ID)
		require.Equal(t, http.StatusForbidden, rec.Code,
			"an agent from another project must be refused; body=%s", rec.Body.String())
	})

	t.Run("BROKER is DENIED", func(t *testing.T) {
		req := ptyUpgradeHeaders(f.newRequest(http.MethodGet, ptyPath(child.ID), nil))
		rec := ptyBrokerServe(t, f, req)
		require.Equal(t, http.StatusForbidden, rec.Code,
			"a broker is neither UserIdentity nor AgentIdentity, so CheckAccess must answer "+
				"'unknown identity type' before any bypass runs. This arm is why the fix is "+
				"s.authorize and not a loosened identity-kind check: widening to 'non-user "+
				"callers may attach' would have admitted this caller. body=%s", rec.Body.String())
	})

	t.Run("USER owner still PASSES", func(t *testing.T) {
		rec := ptyAsUser(t, f, f.owner, child.ID)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code,
			"the user path must be unchanged by this conversion — the owner is in the "+
				"child's ancestry and owns it, and must still clear the gate; body=%s",
			rec.Body.String())
	})

	t.Run("FLOOR ONLY anonymous is refused upstream", func(t *testing.T) {
		rec := f.serve(ptyUpgradeHeaders(f.newRequest(http.MethodGet, ptyPath(child.ID), nil)))
		require.Equal(t, http.StatusUnauthorized, rec.Code,
			"anonymous must be refused; body=%s", rec.Body.String())
		// Deliberately NOT a gate-liveness witness: the auth middleware refuses
		// this request before handleAgentPTY runs, so it would stay 401 with the
		// gate deleted. Do not cite it as evidence the gate works.
	})
}

// TestPTYAttach_TicketStubPreconditionForContextAuthorize pins the precondition
// the converted call depends on, so the precondition is EXERCISED rather than
// merely asserted in a comment that nothing checks.
//
// handleAgentPTY resolves a ticket identity into a LOCAL variable, while
// s.authorize resolves identity from the REQUEST CONTEXT. Those two agree only
// because validatePTYTicket is still a stub returning nil, so a non-nil identity
// at the gate can only have come from the context. Implement tickets without
// injecting the identity into the context (contextWithIdentity + r.WithContext)
// and every valid ticket holder gets a 401.
//
// WHEN THIS FAILS, FIX handleAgentPTY — do not update this test to match. Its
// failure means the precondition is gone, not that the expectation was wrong.
func TestPTYAttach_TicketStubPreconditionForContextAuthorize(t *testing.T) {
	f := wsGateSetup(t)
	require.Nil(t, f.srv.validatePTYTicket(context.Background(), "any-ticket-value"),
		"validatePTYTicket now returns an identity, which breaks the precondition behind "+
			"the s.authorize call in handleAgentPTY: that call reads identity from the "+
			"request context and will 401 ticket holders. Inject the ticket identity into "+
			"the request context before authorizing.")
}

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
	"net/http"

	"github.com/GoogleCloudPlatform/scion/pkg/agentkeys"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// KeysAuthzDecision is the shared evaluation result authorizeAgentKeys
// produces (.design/agent-keys-contract.md §3, ptone/scion#2191 / task 2.1,
// ptone/scion#2195). Both ExecuteAgentKeys (task 2.2) and the temporary
// message-raw bridge (task 2.3) consume this result directly rather than
// reimplementing the policy: this file evaluates the policy once and returns
// a decision; each caller turns that decision into its own response shape
// (2.2's direct keys envelope vs. 2.3's bridge, which must reach the
// identical decision a direct /keys call would for the same caller and
// target — contract AK-24/AK-25 auth parity).
//
// Allowed is true exactly when the caller may proceed to the next admission
// step (rate limiting, runtime/phase checks); it says nothing about whether
// dispatch will ultimately succeed. When Allowed is false, Outcome is always
// one of agentkeys.OutcomeKeysDenied or agentkeys.OutcomeCrossProjectKeysUnsupported
// — the only two outcomes this policy layer can produce — and is the zero
// value ("") when Allowed is true, since a caller of this function decides
// the eventual agentkeys.OutcomeDispatched (or a later failure outcome)
// after admission steps this function does not gate. Reason is a short,
// content-free string for audit logging; it is never returned to an HTTP
// caller verbatim (both 2.2 and 2.3's error envelopes carry only the
// sanitized, fixed messages their own outcome tables define — see contract
// §2.4a).
type KeysAuthzDecision struct {
	Allowed bool
	Outcome agentkeys.Outcome
	Reason  string
}

// authorizeAgentKeys is the single authorization gate for the agent-keys
// operation (contract §3 "Authorization"). It maps decision 2's initial
// policy — the route action is not a new independently granted permission —
// onto the existing attach authority, with exact parity to
// authorizeAgentLifecycle(ActionAttach), the same check the PTY endpoint
// already enforces (pty_handlers.go):
//
//   - Human session / user access token: ActionAttach on the target agent,
//     evaluated through the same AK1 kernel CheckAccess call every other
//     attach-gated route uses. This is where owner/privacy/cross-member
//     restrictions, UAT credential/project-boundary caveats (contract
//     "User access token" row), and any explicit deny already live — this
//     function does not special-case any of them, the same way
//     authorizeAgentLifecycle does not. There is deliberately no
//     ancestry/owner/super-admin *piercing* path here of the kind
//     authorizeAgentMessage's user branch has: self/parent/ancestor status
//     alone must not bypass attach authority for keys (contract "no
//     self/parent/ancestor shortcut"; issue #2195 AC3).
//   - Agent credential: must hold ScopeAgentLifecycle and share the target's
//     current project exactly — no self/parent/ancestor shortcut. A mismatch
//     is reported as agentkeys.OutcomeCrossProjectKeysUnsupported (422), a
//     distinct outcome from every other denial's OutcomeKeysDenied (403).
//   - Broker credential and every other principal kind (including a
//     cross-Hub federated agent identity): denied. A broker credential
//     authenticates Hub-to-broker execution under the internal contract
//     (§4), never direct public /keys authority.
//
// Message modes (open/closed/none/etc.) are never read here: contract
// decision 5 and issue #2195 AC2 both require that message-plane
// configuration neither grants nor denies keys, so this function has no
// dependency on store.Agent.MessageMode at all — it is not merely untested,
// it is structurally absent from the evaluation.
//
// target must already be resolved: the caller (2.2's route dispatch, or
// 2.3's bridge) is responsible for resolving the specific agent before
// calling this function, exactly as every other attach-gated action already
// does. The project-scoped route's agent-credential cross-project refusal
// must be decided *before* that resolution happens at all (contract §3.1
// invariant 4, AK-21c) — see authorizeAgentKeysCrossProject below for the
// pre-resolution building block that lets 2.2 satisfy that ordering without
// this function needing to support a partially-known target.
func (s *Server) authorizeAgentKeys(r *http.Request, target *store.Agent) KeysAuthzDecision {
	identity := GetIdentityFromContext(r.Context())
	if identity == nil {
		// Unreachable in production: the shared auth middleware answers an
		// unauthenticated request with 401 before any handler runs, so a nil
		// identity never reaches this function in practice (contract's
		// OperationID policy, §2.5). Fail closed anyway rather than panic,
		// mirroring authorizeAgentLifecycle's own defensive nil check.
		return s.denyAgentKeys(r, Resource{Type: "agent"}, "no authenticated identity")
	}
	if target == nil {
		return s.denyAgentKeys(r, Resource{Type: "agent"}, "nil target agent")
	}
	resource := agentResource(target)

	switch identity.Type() {
	case "agent":
		agentIdent, ok := identity.(AgentIdentity)
		if !ok {
			return s.denyAgentKeys(r, resource, "invalid agent identity")
		}
		if !agentIdent.HasScope(ScopeAgentLifecycle) {
			return s.denyAgentKeys(r, resource, "missing scope "+string(ScopeAgentLifecycle))
		}
		if agentIdent.ProjectID() != target.ProjectID {
			return s.denyAgentKeysCrossProject(r, resource, "agent project mismatch")
		}
		return KeysAuthzDecision{Allowed: true, Reason: "agent credential, same project, lifecycle scope"}

	case "user", "dev":
		userIdent, ok := identity.(UserIdentity)
		if !ok {
			return s.denyAgentKeys(r, resource, "invalid user identity")
		}
		decision := s.authzService.CheckAccess(r.Context(), userIdent, resource, ActionAttach)
		if !decision.Allowed {
			return s.denyAgentKeys(r, resource, "agent.attach permission denied: "+decision.Reason)
		}
		return KeysAuthzDecision{Allowed: true, Reason: "agent.attach permission granted"}

	default:
		// Broker credentials, federated agent identities, and any other
		// principal kind: never direct public keys authority (contract §3
		// "Broker credential" row; issue #2195 "unsupported principal kinds
		// are rejected").
		return s.denyAgentKeys(r, resource, "identity type "+identity.Type()+" may not call the keys operation")
	}
}

// authorizeAgentKeysCrossProject evaluates only the agent-credential
// cross-project refusal (contract §3.1), using the caller's identity and an
// already-resolved target project ID, without resolving or requiring any
// specific target agent record. It exists because the project-scoped route
// must decide this refusal *before* resolving the agent-by-slug-or-id target
// at all (contract §3.1 invariant 4, AK-21c: "no target-agent lookup of any
// kind, successful or not, may precede or be required by this comparison") —
// the full authorizeAgentKeys above cannot satisfy that ordering on its own
// because it requires an already-resolved *store.Agent.
//
// Returns nil when this function reaches no verdict: either the caller is
// not an authenticated agent identity (a human caller has no blanket
// cross-project ban — contract "Human cross-project use is allowed"), or the
// agent identity's own project already matches targetProjectID. nil does
// NOT mean "allowed": the caller must still resolve the target and call
// authorizeAgentKeys for the complete evaluation (scope, and — for the
// top-level route, which resolves its target first and so never calls this
// function at all — the equivalent project comparison folded into that
// single call).
//
// Returns a non-nil, always-denied decision (Outcome ==
// agentkeys.OutcomeCrossProjectKeysUnsupported) exactly when the caller is
// an authenticated agent identity whose own project differs from
// targetProjectID.
func (s *Server) authorizeAgentKeysCrossProject(r *http.Request, targetProjectID string) *KeysAuthzDecision {
	identity := GetIdentityFromContext(r.Context())
	agentIdent, ok := identity.(AgentIdentity)
	if !ok {
		return nil
	}
	if agentIdent.ProjectID() == targetProjectID {
		return nil
	}
	resource := Resource{Type: "agent", ParentType: "project", ParentID: targetProjectID}
	d := s.denyAgentKeysCrossProject(r, resource, "agent project mismatch")
	return &d
}

// denyAgentKeys logs the structured authorization-denial record every other
// denial path in this package produces (logAuthzDenial, #591) and returns
// the generic keys_denied decision.
func (s *Server) denyAgentKeys(r *http.Request, resource Resource, reason string) KeysAuthzDecision {
	logAuthzDenial(r, GetIdentityFromContext(r.Context()), resource, ActionAttach, reason)
	return KeysAuthzDecision{Allowed: false, Outcome: agentkeys.OutcomeKeysDenied, Reason: reason}
}

// denyAgentKeysCrossProject is denyAgentKeys' sibling for the one denial
// reason that maps to a different wire outcome
// (agentkeys.OutcomeCrossProjectKeysUnsupported, 422) rather than the
// generic agentkeys.OutcomeKeysDenied (403).
func (s *Server) denyAgentKeysCrossProject(r *http.Request, resource Resource, reason string) KeysAuthzDecision {
	logAuthzDenial(r, GetIdentityFromContext(r.Context()), resource, ActionAttach, reason)
	return KeysAuthzDecision{Allowed: false, Outcome: agentkeys.OutcomeCrossProjectKeysUnsupported, Reason: reason}
}

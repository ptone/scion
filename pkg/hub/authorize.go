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
	"log/slog"
	"net/http"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// This file holds the shared, fail-closed authorization guards for hub
// handlers. Before these existed, every handler hand-wrote the same
// fetch-identity → nil-check → CheckAccess → writeError sequence, and the
// common form of that idiom silently skipped the check for any caller that was
// not a user (ptone/scion#591). Handlers should call these helpers rather than
// reproducing the idiom.
//
// THAT REFERENCE IS THE CANONICAL ONE. The same number appears unqualified in
// comments across this package as a pointer back to here. It is qualified in
// this one place, because the number is ambiguous: on the fork it is the issue
// this work fixes, while GoogleCloudPlatform/scion#591 is an unrelated merged
// PR. GitHub does not autolink inside file contents, so a bare reference in Go
// source misleads a human rather than producing a wrong link — which is why the
// pointers elsewhere were left alone rather than churned across 25 files.

// logAuthzDenial emits the structured warning that every authorization denial
// produces. The defining property of the #591 bypass was that it was silent:
// a denial that is not logged cannot be detected, and an over-tight policy
// baseline cannot be diagnosed. The field names are part of the contract —
// operators and alerting key off them — so keep them stable.
func logAuthzDenial(r *http.Request, identity Identity, resource Resource, action Action, reason string) {
	var principalType, principalID string
	if identity != nil {
		principalType = identity.Type()
		principalID = identity.ID()
	}
	var path string
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	slog.Warn("authorization denied",
		"principal_type", principalType,
		"principal_id", principalID,
		"resource_type", resource.Type,
		"resource_id", resource.ID,
		"action", action,
		"reason", reason,
		"path", path,
	)
}

// writeForbidden writes a 403 carrying msg, or the generic "Insufficient
// permissions" body when msg is empty.
func writeForbidden(w http.ResponseWriter, msg string) {
	if msg == "" {
		Forbidden(w)
		return
	}
	writeError(w, http.StatusForbidden, ErrCodeForbidden, msg, nil)
}

// authorize performs a fail-closed authorization check for any identity kind.
// It writes 401 for an unauthenticated caller, 403 when access is denied, and
// returns false in both cases, so callers write:
//
//	if !s.authorize(w, r, agentResource(agent), ActionDelete) { return }
//
// Unlike the pre-#591 idiom it MUST NOT be wrapped in an identity-kind guard:
// nil and non-user identities are denied here, not skipped.
//
// It takes the *http.Request rather than a context.Context so that the denial
// log can name the request path and so a future audit hook has the request.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, resource Resource, action Action) bool {
	return s.authorizeWithMessage(w, r, resource, action, "")
}

// authorizeMsg is authorize with a caller-supplied 403 body, for the handful of
// sites whose existing denial text is real user-facing guidance rather than a
// restatement of "denied". Prefer plain authorize: several of the pre-existing
// messages were actively misleading about who may pass.
func (s *Server) authorizeMsg(w http.ResponseWriter, r *http.Request, resource Resource, action Action, msg string) bool {
	return s.authorizeWithMessage(w, r, resource, action, msg)
}

// authorizeWithMessage is the single implementation behind authorize and
// authorizeMsg. An empty msg yields the generic 403 body.
func (s *Server) authorizeWithMessage(w http.ResponseWriter, r *http.Request, resource Resource, action Action, msg string) bool {
	ctx := r.Context()
	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		Unauthorized(w)
		return false
	}
	decision := s.authzService.CheckAccess(ctx, identity, resource, action)
	if !decision.Allowed {
		logAuthzDenial(r, identity, resource, action, decision.Reason)
		writeForbidden(w, msg)
		return false
	}
	return true
}

// authorizeProjectReadNoOracle gates a project READ so that the observable answer
// is the same whether the project does not exist or the caller may not read it.
// It checks agent project-isolation, then CheckAccess for ActionRead; on ANY
// denial it renders the byte-identical response the handler's missing-project
// path renders — writeErrorFromErr on store.ErrNotFound: 404 / code not_found /
// message "Resource not found" — so a refused-real project and a missing one
// cannot be told apart on status, error code, OR message. Use it like s.authorize:
//
//	if !s.authorizeProjectReadNoOracle(w, r, project) { return }
//
// The message-parity is load-bearing: an earlier revision rendered
// NotFound(w, "Project") ("Project not found") on denial, which agreed with the
// missing path on status and code but not message, leaving a one-bit existence
// oracle over project ids (aid-verify (c) on 3e753be5); corrected forward here.
//
// It inlines the agent-isolation check rather than calling
// requireProjectVisibleToAgent so the isolation denial renders that same missing
// body — requireProjectVisibleToAgent answers "Project not found", which is right
// for its other callers but would reopen the oracle here. It is deliberately
// stricter than the eight s.authorize gates, which keep a 403-vs-404 existence
// split.
//
// CONSEQUENCE - the no-oracle property is VERB-LOCAL. It holds for the READ
// verb here, but PATCH (updateProject, ActionUpdate) and DELETE (deleteProject,
// ActionDelete) on the SAME project url still render 403-for-real /
// 404-for-missing, so a caller learns whether a project id exists by switching
// verb - the existence oracle survives one verb away (rev1 measured 6/6:
// outsider-user, cross-project-agent and broker, both verbs, all differ).
// Whether the eight s.authorize gates should harmonize to 404 is a security
// decision, not one to settle inline; it is re-filed to the lead (Rule 13) as
// the harmonization follow-on and is deliberately NOT resolved in this PR.
// Read-only: for a mutating verb use s.authorize instead.
func (s *Server) authorizeProjectReadNoOracle(w http.ResponseWriter, r *http.Request, project *store.Project) bool {
	ctx := r.Context()
	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		Unauthorized(w)
		return false
	}
	if project == nil {
		// Caller bug, not a policy outcome: every call site fetches the project and
		// returns on the store error before calling this. Fail closed rather than
		// panic in projectResource/project.ID below (I68, recurrence of I56), and
		// render the same missing body every other denial here renders so the guard
		// cannot itself become an existence oracle. Mirrors the nil-project guard in
		// requireProjectVisibleToAgent.
		logAuthzDenial(r, identity, Resource{Type: "project"}, ActionRead, "nil project")
		writeErrorFromErr(w, store.ErrNotFound, "")
		return false
	}
	resource := projectResource(project)

	// Every denial renders the same body the missing-project path renders, so no
	// arm distinguishes real-but-forbidden from missing.
	deny := func(reason string) bool {
		logAuthzDenial(r, identity, resource, ActionRead, reason)
		writeErrorFromErr(w, store.ErrNotFound, "")
		return false
	}

	// Agent isolation first (mirrors requireProjectVisibleToAgent's ordering): a
	// cross-project agent must not learn the project exists.
	if agentIdent := GetAgentIdentityFromContext(ctx); agentIdent != nil && project.ID != agentIdent.ProjectID() {
		return deny("agent outside project (rendered as missing, no existence oracle)")
	}
	if !s.authzService.CheckAccess(ctx, identity, resource, ActionRead).Allowed {
		return deny("project read denied (rendered as missing, no existence oracle)")
	}
	return true
}

// authorizeImportAgentRead gates the resource-import/discover AGENT branch on the
// project read baseline, IN ADDITION to the create scope and project-match checks
// the caller has already passed at the call site (those stay — do not remove them).
//
// #591 (Rule 18a): ScopeAgentCreate is a WRITE capability. Before this, holding
// it (and being in-project) also authorized READ enumeration of the project
// subtree — discoverFromWorkspace/importFromWorkspace os.ReadDir the workspace
// and disclose sibling directory names — and an explicit deny policy revoking the
// agent's read baseline (authz.go:239, revocable by design) was ignored here even
// though authorizeProjectWorkspaceAccess honours that same deny for the same
// caller on the same project. This closes that divergence: the create scope no
// longer authorizes read enumeration, and a revoked read is refused here too.
//
// It renders 403 on denial, NOT the 404 that authorizeProjectReadNoOracle renders,
// because the caller is an in-project agent that already knows its own project
// exists (the project-match check above constant-403s every other project id), so
// refusing with 403 introduces no existence oracle. The resource keys on the
// project scope alone (Type/ID), which is all the agent read decision consults
// (checkAccessForAgent → projectIDForResource → the read baseline / project-scoped
// policies); owner/labels are not needed and the project need not be pre-loaded.
// Call only inside the agent branch, after the scope and project-match checks pass.
func (s *Server) authorizeImportAgentRead(ctx context.Context, w http.ResponseWriter, agent AgentIdentity, projectID, verb string) bool {
	resource := Resource{Type: "project", ID: projectID}
	if s.authzService.CheckAccess(ctx, agent, resource, ActionRead).Allowed {
		return true
	}
	logAuthzDenial(nil, agent, resource, ActionRead,
		"import/discover agent read denied (create scope does not authorize read enumeration)")
	writeError(w, http.StatusForbidden, ErrCodeForbidden,
		"You don't have permission to "+verb+" in this project", nil)
	return false
}

// authorizeImportUserRead is the USER-branch counterpart of
// authorizeImportAgentRead, and closes the same hole on the other caller kind.
// 1b72a060 fixed the agent branch only, while the denial reason it wrote —
// "create scope does not authorize read enumeration" — was already caller
// agnostic: the fix was scoped narrower than its own justification. The user
// branch kept authorizing a READ enumeration with a WRITE grant (ActionCreate),
// so a project member holding only agent-create could enumerate a project
// subtree it has no read access to. No deny policy is required to reach it.
//
// Call only INSIDE the user branch and only AFTER the ActionCreate check, so a
// caller needs BOTH create and read. Not usable on the global-scope arms of
// handleResourcesImport/handleResourcesDiscover: those pass no project (the
// resource there is deliberately ownerless and parentless, hub-admin by
// construction), and this check keys on Resource{Type: "project", ID: projectID},
// which is meaningless with an empty ID.
//
// WHY 403 AND NOT THE 404 OF authorizeProjectReadNoOracle — and note this is NOT
// the reason the agent helper above gives. That one relies on the caller being
// an in-project agent that already knows its own project exists. A user calling
// with an arbitrary project id knows no such thing, so that reasoning does not
// transfer and must not be copied. The reason here is ordering: this check is
// unreachable until the caller has ALREADY passed ActionCreate on this same
// project, which itself 403s. Anyone who can see this denial has already been
// told the project exists and that they may create in it, so the 403 discloses
// nothing the preceding check did not. If this call is ever moved ahead of the
// create check, that argument dies and the no-oracle 404 becomes the right
// response — re-derive it if you reorder, do not assume it survived.
func (s *Server) authorizeImportUserRead(ctx context.Context, w http.ResponseWriter, user UserIdentity, projectID, verb string) bool {
	resource := Resource{Type: "project", ID: projectID}
	if s.authzService.CheckAccess(ctx, user, resource, ActionRead).Allowed {
		return true
	}
	logAuthzDenial(nil, user, resource, ActionRead,
		"import/discover user read denied (create scope does not authorize read enumeration)")
	writeError(w, http.StatusForbidden, ErrCodeForbidden,
		"You don't have permission to "+verb+" in this project", nil)
	return false
}

// authorizeAgentCreate gates agent creation for every caller kind. Exhaustive
// and fail-closed. Replaces the caller-kind branch in createAgent, which had no
// else clause, and supplies the gate createProjectAgent never had.
//
// The agent path is scope-gated rather than policy-gated: sub-agent creation is
// a template-administered capability (ScopeAgentCreate), constrained to the
// calling agent's own project.
func (s *Server) authorizeAgentCreate(w http.ResponseWriter, r *http.Request, projectID string) bool {
	ctx := r.Context()
	resource := Resource{
		Type:       "agent",
		ParentType: "project",
		ParentID:   projectID,
	}

	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		Unauthorized(w)
		return false
	}

	// A switch with a terminating default, not a chain of ifs: an unhandled
	// caller kind falling through the guard is precisely the #591 bug.
	switch identity.Type() {
	case "agent":
		agentIdent, ok := identity.(AgentIdentity)
		if !ok {
			logAuthzDenial(r, identity, resource, ActionCreate, "invalid agent identity")
			writeForbidden(w, "")
			return false
		}
		if !agentIdent.HasScope(ScopeAgentCreate) {
			logAuthzDenial(r, identity, resource, ActionCreate,
				"missing scope "+string(ScopeAgentCreate))
			writeForbidden(w, "Missing required scope: "+string(ScopeAgentCreate))
			return false
		}
		if agentIdent.ProjectID() != projectID {
			logAuthzDenial(r, identity, resource, ActionCreate, "agent project mismatch")
			writeForbidden(w, "Agents can only create sub-agents within their own project")
			return false
		}
		return true

	case "user", "dev":
		userIdent, ok := identity.(UserIdentity)
		if !ok {
			logAuthzDenial(r, identity, resource, ActionCreate, "invalid user identity")
			writeForbidden(w, "")
			return false
		}
		decision := s.authzService.CheckAccess(ctx, userIdent, resource, ActionCreate)
		if !decision.Allowed {
			logAuthzDenial(r, identity, resource, ActionCreate, decision.Reason)
			writeForbidden(w, "You don't have permission to create agents in this project")
			return false
		}
		return true

	default:
		logAuthzDenial(r, identity, resource, ActionCreate,
			"identity type may not create agents")
		writeForbidden(w, "")
		return false
	}
}

// authorizeAgentLifecycle gates start/stop/suspend/restart/message/exec on an
// existing agent, for every caller kind. Exhaustive and fail-closed.
//
// An agent caller passes on ScopeAgentLifecycle within its own project, which
// includes project peers as well as its own descendants. That breadth is
// deliberate (design Q3): the scope is template-administered rather than
// ambient, and handleProjectBroadcast already reads it as conferring exactly
// this authority. Narrowing it later is a change to this one function.
//
// Because the agent arm authorizes on scope alone and never calls CheckAccess,
// an explicit deny policy binding does NOT restrain a scoped agent's lifecycle
// actions: an operator who binds a deny to stop one agent from restarting its
// peers will find it has no effect here (deny bindings remain effective for user
// callers, which go through CheckAccess). This is the user-approved Q3 behaviour,
// not an oversight.
func (s *Server) authorizeAgentLifecycle(w http.ResponseWriter, r *http.Request, agent *store.Agent) bool {
	ctx := r.Context()

	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		Unauthorized(w)
		return false
	}
	if agent == nil {
		// Caller bug rather than a policy outcome; deny rather than panic.
		logAuthzDenial(r, identity, Resource{Type: "agent"}, ActionAttach, "nil agent")
		writeForbidden(w, "")
		return false
	}
	resource := agentResource(agent)

	switch identity.Type() {
	case "agent":
		agentIdent, ok := identity.(AgentIdentity)
		if !ok {
			logAuthzDenial(r, identity, resource, ActionAttach, "invalid agent identity")
			writeForbidden(w, "")
			return false
		}
		if !agentIdent.HasScope(ScopeAgentLifecycle) {
			logAuthzDenial(r, identity, resource, ActionAttach,
				"missing scope "+string(ScopeAgentLifecycle))
			writeForbidden(w, "Missing required scope: "+string(ScopeAgentLifecycle))
			return false
		}
		if agentIdent.ProjectID() != agent.ProjectID {
			logAuthzDenial(r, identity, resource, ActionAttach, "agent project mismatch")
			writeForbidden(w, "Agents can only manage agents within their own project")
			return false
		}
		return true

	case "user", "dev":
		userIdent, ok := identity.(UserIdentity)
		if !ok {
			logAuthzDenial(r, identity, resource, ActionAttach, "invalid user identity")
			writeForbidden(w, "")
			return false
		}
		decision := s.authzService.CheckAccess(ctx, userIdent, resource, ActionAttach)
		if !decision.Allowed {
			logAuthzDenial(r, identity, resource, ActionAttach, decision.Reason)
			writeForbidden(w, "")
			return false
		}
		return true

	default:
		logAuthzDenial(r, identity, resource, ActionAttach,
			"identity type may not act on agent lifecycle")
		writeForbidden(w, "")
		return false
	}
}

// requireAdmin returns the calling user identity if it is a hub admin, writing
// 401 for an unauthenticated caller and 403 for any authenticated caller that
// is not an admin user, and returning false in both cases.
//
// It resolves the identity with GetIdentityFromContext rather than
// GetUserIdentityFromContext. The latter returns nil for agent and broker
// callers, which conflates "nobody is authenticated" with "the authenticated
// caller is not a user" — the same conflation that produced #591. Here it
// merely produced a wrong status code (an authenticated agent was told
// "Authentication required"), but the distinction is worth keeping honest:
// this helper gates the hub's admin endpoints.
//
// A user access token embeds the MINTING user's role, so a plain role
// comparison would let an admin-minted, project-scoped UAT pass and reach every
// endpoint using this helper — even though authorize() rejects the same token.
// To match authorize(), requireAdmin runs enforceUATConstraints on a scoped
// identity BEFORE the role comparison, exactly as checkAccessForUser runs it
// before its admin bypass (authz.go). A UAT is project-scoped by construction
// and never carries the hub:manage scope this synthetic hub resource demands, so
// it is denied here just as it is in authorize().
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (UserIdentity, bool) {
	// Synthetic resource: requireAdmin is a role check on the hub itself
	// rather than a policy check on an addressable resource.
	resource := Resource{Type: "hub", ID: r.URL.Path}

	identity := GetIdentityFromContext(r.Context())
	if identity == nil {
		Unauthorized(w)
		return nil, false
	}

	// The assertion, rather than a switch on Type(), is deliberate: the question
	// this helper asks is "can this caller answer Role()". It admits both "user"
	// and dev-auth "dev" identities (DevUser implements UserIdentity) and denies
	// everything else, including a "user"-typed identity too degenerate to have
	// a role.
	user, ok := identity.(UserIdentity)
	if !ok {
		logAuthzDenial(r, identity, resource, ActionManage, "non-user identity")
		Forbidden(w)
		return nil, false
	}

	// A UAT is project-scoped by construction and must not confer hub-wide admin
	// authority just because its minting user is an admin. Enforce the token's
	// constraints first, mirroring authorize()'s ordering (enforceUATConstraints
	// before the admin bypass); a scoped token lacking hub:manage is denied here
	// exactly as it is there. A non-scoped identity (a genuine session user or a
	// dev user) is unaffected.
	if scoped, ok := user.(*ScopedUserIdentity); ok {
		if denied := s.authzService.enforceUATConstraints(scoped, resource, ActionManage); denied != nil {
			logAuthzDenial(r, identity, resource, ActionManage, denied.Reason)
			Forbidden(w)
			return nil, false
		}
	}

	if user.Role() != store.UserRoleAdmin {
		logAuthzDenial(r, identity, resource, ActionManage, "not an admin")
		Forbidden(w)
		return nil, false
	}
	return user, true
}

// requireProjectVisibleToAgent enforces project isolation for agent callers
// before authorization runs, answering 404 and returning false when the caller
// is an agent belonging to a different project.
//
// Ordering is the whole point of this helper, so it must be called BEFORE
// s.authorize rather than after. Authorization alone answers 403 for a
// cross-project agent, and 403-versus-404 is itself a disclosure: it confirms
// the project exists to a caller who cannot otherwise establish that. Running
// isolation first collapses both cases to 404.
//
// It is deliberately a no-op for every non-agent caller:
//
//   - Users are unaffected. A non-member user already receives 403 from
//     authorize, that behaviour is long-standing and tested elsewhere, and
//     changing it here would be an unrelated user-visible change smuggled into
//     an authorization fix.
//   - Brokers and unauthenticated callers fall through to authorize, which
//     denies them. Denying them here instead would answer 404 where 403 and 401
//     are correct.
//
// It grants nothing: it only narrows a cross-project agent's answer to 404.
// Every caller it passes still faces the endpoint's own authorization — at the
// seven current call sites that is the raw GetUserIdentityFromContext-plus-else
// idiom, not an s.authorize call.
//
// Rule 18a: authorizeProjectReadNoOracle inlines an INDEPENDENT COPY of this
// agent-isolation check (it renders the missing body rather than "Project not
// found" to avoid an existence oracle). The two are not wired together, so
// anyone hardening the isolation logic here must edit that copy in step.
func (s *Server) requireProjectVisibleToAgent(w http.ResponseWriter, r *http.Request, project *store.Project) bool {
	agentIdent := GetAgentIdentityFromContext(r.Context())
	if agentIdent == nil {
		return true
	}
	if project == nil {
		// Caller bug rather than a policy outcome; deny rather than panic, as
		// authorizeAgentLifecycle does for a nil agent. Not reachable at the
		// current call sites (each fetches the project and returns on error
		// before calling this), but the next call site inherits the guard rather
		// than a nil dereference (Rule 18a).
		logAuthzDenial(r, agentIdent, Resource{Type: "project"}, ActionRead, "nil project")
		NotFound(w, "Project")
		return false
	}
	if project.ID != agentIdent.ProjectID() {
		logAuthzDenial(r, agentIdent, projectResource(project), ActionRead,
			"agent outside project")
		NotFound(w, "Project")
		return false
	}
	return true
}

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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Masterminds/semver/v3"
	"golang.org/x/sync/errgroup"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/artifacts/remotefetch"
	"github.com/GoogleCloudPlatform/scion/pkg/githubfetch"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/githubapp"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/permissions"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
)

const retiredSkillScopeCoreMessage = `invalid scope "core": the "core" skill scope is no longer supported; use "global" (skill://scion/global/<name>) instead`

// CreateSkillRequest is the request body for creating a skill.
type CreateSkillRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Scope       string   `json:"scope"`
	ScopeID     string   `json:"scopeId,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// CreateSkillResponse is the response for skill creation.
type CreateSkillResponse struct {
	Skill *store.Skill `json:"skill"`
}

// ListSkillsResponse is the response for listing skills.
type ListSkillsResponse struct {
	Skills     []SkillWithCapabilities `json:"skills"`
	NextCursor string                  `json:"nextCursor,omitempty"`
	TotalCount int                     `json:"totalCount"`
	// TotalCountApproximate marks TotalCount as a lower bound: set when a
	// per-item list's candidate pool crossed authorizedListMaxCandidates
	// before the count scan finished. Only the agent delegated
	// credential's list sets it.
	TotalCountApproximate bool          `json:"totalCountApproximate,omitempty"`
	Capabilities          *Capabilities `json:"_capabilities,omitempty"`
}

// SkillWithCapabilities wraps a store.Skill with capability annotations.
type SkillWithCapabilities struct {
	store.Skill
	Cap *Capabilities `json:"_capabilities,omitempty"`
}

// PublishVersionRequest is the request body for creating a skill version.
type PublishVersionRequest struct {
	Version string              `json:"version"`
	Files   []FileUploadRequest `json:"files,omitempty"`
}

// PublishVersionResponse is the response for version creation.
type PublishVersionResponse struct {
	Version    *store.SkillVersion `json:"version"`
	UploadURLs []UploadURLInfo     `json:"uploadUrls,omitempty"`
}

// FinalizeSkillVersionRequest is the request body for finalizing a skill version.
type FinalizeSkillVersionRequest struct {
	Version  string         `json:"version"`
	Manifest *SkillManifest `json:"manifest"`
}

// SkillManifest is the manifest of uploaded skill files.
type SkillManifest struct {
	Files []store.TemplateFile `json:"files"`
}

// ResolveSkillsRequest is the request body for batch skill resolution.
type ResolveSkillsRequest struct {
	Skills    []ResolveSkillRef `json:"skills"`
	ProjectID string            `json:"projectId,omitempty"`
	UserID    string            `json:"userId,omitempty"`
}

// ResolveSkillRef is a reference to a skill to resolve.
type ResolveSkillRef struct {
	URI string `json:"uri"`
}

// ResolveSkillsResponse is the response for batch skill resolution.
type ResolveSkillsResponse struct {
	Resolved []ResolvedSkillResponse `json:"resolved"`
	Errors   []ResolveSkillError     `json:"errors,omitempty"`
}

// ResolvedSkillResponse is a single resolved skill in the batch response.
type ResolvedSkillResponse struct {
	URI                string            `json:"uri"`
	Name               string            `json:"name"`
	ResolvedVersion    string            `json:"resolvedVersion"`
	ContentHash        string            `json:"contentHash"`
	ContentHashV2      string            `json:"contentHashV2,omitempty"` // skill content hash v2 (ptone/scion#5935), set only while hub.skill_content_hash_v2 is on
	Files              []DownloadURLInfo `json:"files"`
	Deprecated         bool              `json:"deprecated,omitempty"`
	DeprecationMessage string            `json:"deprecationMessage,omitempty"`
	ReplacementURI     string            `json:"replacementUri,omitempty"`
	// DownloadToken is a short-lived, read-only (contents:read, metadata:read),
	// repository-scoped GitHub App installation token carried so an authorized
	// download consumer (the project's provider broker or an agent of the
	// project) downloads the skill's files with the project credential rather
	// than falling back to the creating user's credential (ptone/scion#5028).
	// Never returned to user or user-access-token callers; omitted when
	// resolved anonymously or for non-gh:// skills; never stored in the
	// resolution cache.
	DownloadToken string `json:"downloadToken,omitempty"`
}

// ResolveSkillError describes a resolution failure for a single skill.
// Message is the fixed reason phrase for Code (see
// agent.SkillResolutionPhrase). Status is the HTTP status of the upstream
// response that failed the resolution, when one is known. CorrelationID
// ties the error to the server-side log record that holds the full error
// (see newSkillResolveError).
type ResolveSkillError struct {
	URI           string `json:"uri"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	Status        int    `json:"status,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
}

// UpdateSkillRequest is the request body for updating a skill.
type UpdateSkillRequest struct {
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// DeprecateVersionRequest is the request body for deprecating a skill version.
type DeprecateVersionRequest struct {
	Message        string `json:"message"`
	ReplacementURI string `json:"replacementUri,omitempty"`
}

// handleSkills dispatches /api/v1/skills (GET=list, POST=create).
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listSkills(w, r)
	case http.MethodPost:
		s.createSkill(w, r)
	default:
		MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handleSkillByID dispatches /api/v1/skills/{id}[/{action}[/{subId}]].
func (s *Server) handleSkillByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/skills/")
	if path == "" {
		NotFound(w, "Skill")
		return
	}

	parts := strings.SplitN(path, "/", 3)
	skillID := parts[0]

	// Batch resolve is routed through a non-UUID path segment.
	if skillID == "resolve" {
		s.handleSkillsResolve(w, r)
		return
	}

	if len(parts) == 1 {
		s.handleSkillCRUD(w, r, skillID)
		return
	}

	action := parts[1]
	switch action {
	case "versions":
		if len(parts) == 3 {
			s.handleSkillVersionByID(w, r, skillID, parts[2])
		} else {
			s.handleSkillVersions(w, r, skillID)
		}
	case "upload":
		s.handleSkillUpload(w, r, skillID)
	case "finalize":
		s.handleSkillFinalize(w, r, skillID)
	case "download":
		s.handleSkillDownload(w, r, skillID)
	case "resolve":
		s.handleSkillResolveSingle(w, r, skillID)
	case "files":
		// parts is split into at most 3 segments, so parts[2] carries the
		// full (possibly nested) file path.
		filePath := ""
		if len(parts) == 3 {
			filePath = parts[2]
		}
		s.handleSkillFiles(w, r, skillID, filePath)
	default:
		NotFound(w, "Skill action")
	}
}

// handleSkillCRUD handles basic skill CRUD operations.
func (s *Server) handleSkillCRUD(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		s.getSkill(w, r, id)
	case http.MethodPatch:
		s.updateSkill(w, r, id)
	case http.MethodDelete:
		s.deleteSkill(w, r, id)
	default:
		MethodNotAllowed(w, http.MethodGet, http.MethodPatch, http.MethodDelete)
	}
}

// listSkills lists skills with filtering.
func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	if !checkAgentReadScope(w, r) {
		return
	}

	ctx := r.Context()
	query := r.URL.Query()

	filter := store.SkillFilter{
		Name:    query.Get("name"),
		Scope:   query.Get("scope"),
		ScopeID: query.Get("scopeId"),
		OwnerID: query.Get("ownerId"),
		Status:  query.Get("status"),
		Search:  query.Get("search"),
	}
	if filter.Scope == "core" {
		ValidationError(w, retiredSkillScopeCoreMessage, nil)
		return
	}
	if tagsParam := query.Get("tags"); tagsParam != "" {
		filter.Tags = strings.Split(tagsParam, ",")
	}

	if filter.Status == "" {
		filter.Status = "active"
	}

	identity := GetIdentityFromContext(ctx)

	// An agent delegated credential takes its scope only from the delegated
	// list arm; no project.list scope is resolved for it
	// (.design/agent-delegation.md §11.8).
	if d, ok := identity.(*DelegatedAgentIdentity); ok && d != nil {
		s.listSkillsDelegated(w, r, d, filter)
		return
	}

	// A user access token lists skills only when its ceiling carries
	// skill.list and skill.list is eligible for its boundary kind. Without
	// it the list is empty: no row query runs.
	token, isToken := identity.(*ScopedUserIdentity)
	if isToken && !uatListPermitted(token, "skill.list") {
		writeJSON(w, http.StatusOK, ListSkillsResponse{Skills: []SkillWithCapabilities{}, TotalCount: 0})
		return
	}

	// ptone/scion#1901 (pagination follow-up): resolve the caller's project
	// memberships and push the whole read boundary (hub scope, own user
	// scope, member projects) into the store query, ahead of COUNT and
	// LIMIT. "project.list" is the right proxy permission here: every
	// project role that carries skill.list also carries project.list, and
	// only the elevated hub-admin/super-admin roles resolve to an
	// unrestricted (IsAll) scope — exactly the pair of facts (my projects;
	// am I unrestricted) this predicate needs. See skillAccessScopePredicate
	// in pkg/store/entadapter/skill_store.go. Visibility no longer widens
	// this boundary (ptone/scion#1903): an anonymous caller gets an
	// AccessScope with no matching terms, which authorizes nothing.
	// A token's project set is further reduced by its boundary, and it must
	// carry skill.list (checked above and again here).
	scopeResult, err := s.authzService.resolveListScopesWithCeiling(ctx, identity, "project.list", "skill.list")
	resolutionFailed := err != nil
	if resolutionFailed {
		// ptone/scion#1954: a principal-resolution error here (e.g. a
		// federated identity whose ID isn't a bare UUID, which trips
		// parseUUID in GetEffectiveGroups/GetEffectiveGroupsForAgent) must
		// not take the whole endpoint down for every caller. Logged with
		// principal type only: never the raw principal ID.
		principalType := "anonymous"
		if identity != nil {
			principalType = identity.Type()
		}
		logAuthzCheckFault(ctx, nil, "list_skills_scope",
			"listSkills: failed to resolve list scopes; failing closed", err,
			"principal_type", principalType)
	}
	switch {
	case identity == nil:
		// ptone/scion#1903 removed the public-visibility bypass: an
		// anonymous caller now gets a scope with no matching terms, which
		// authorizes nothing.
		filter.AccessScope = &store.SkillAccessScope{}
	case resolutionFailed:
		// ptone/scion#1954: the same principal-resolution error that failed
		// ResolveListScopes also makes Decide() fail closed on every row's
		// per-row capability check below (see authz.go), so this principal
		// can never actually read anything regardless of what the query
		// predicate offers. Giving it the ordinary no-grant-user floor
		// (IncludeHubScope + CallerID + ProjectIDs) would only produce a
		// nonzero totalCount over an empty page — exactly the count/page
		// mismatch this fix closes for agents below — and would needlessly
		// disclose the hub-wide skill count to a principal that can't read
		// any of them. Match what Decide() actually grants: nothing.
		filter.AccessScope = &store.SkillAccessScope{}
	case isAgentIdentity(identity):
		// ptone/scion#1968: agents read exactly their granted set — the hub
		// catalog (global), their own project's skills, and their
		// creator's own user-scoped skills, each gated
		// by the agent JWT scope restriction and the delegation ceiling. See
		// agentSkillAccessScope for why per-bucket probes equal the per-row
		// decision, which keeps totalCount and pages consistent with point
		// reads (the ptone/scion#1954 count/page mismatch).
		//
		// This case must stay before the IsAll() check below
		// (ptone/scion#1954): an agent always gets an explicit, bounded
		// predicate, never an unfiltered query.
		filter.AccessScope = s.agentSkillAccessScope(ctx, identity.(AgentIdentity))
	case scopeResult.Scopes.IsAll():
		// identity holds an unrestricted (hub-admin/super-admin) scope —
		// leave filter.AccessScope nil so the query is unfiltered.
	default:
		filter.AccessScope = &store.SkillAccessScope{
			IncludeHubScope: true,
			CallerID:        identity.ID(),
			ProjectIDs:      scopeResult.Scopes.ProjectIDs(),
		}
	}

	// A token's page, TotalCount and cursor come from the per-row skill
	// read gate, so they count only rows the token may read. Its cursor is
	// sealed and bound to the credential.
	if isToken {
		s.listSkillsForToken(w, r, token, filter, scopeResult.Scopes)
		return
	}

	result, err := s.store.ListSkills(ctx, filter, listOptionsFromQuery(query))
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// The store query above already applies the caller's read boundary, so
	// result.Items only contains rows the caller may see and result.TotalCount
	// only counts those rows — pagination cannot crowd them out. The
	// capability computation below is defense in depth (it also derives the
	// per-row "_capabilities" the response returns), not the access decision.
	skills := make([]SkillWithCapabilities, 0, len(result.Items))
	if identity != nil {
		resources := make([]Resource, len(result.Items))
		for i := range result.Items {
			resources[i] = skillResource(&result.Items[i])
		}
		caps := s.authzService.ComputeCapabilitiesBatch(ctx, identity, resources, "skill")
		for i := range result.Items {
			if !capabilityAllows(caps[i], ActionRead) {
				continue
			}
			skills = append(skills, SkillWithCapabilities{Skill: result.Items[i], Cap: caps[i]})
		}
	}

	var scopeCap *Capabilities
	if identity != nil {
		scopeCap = s.skillListCapabilities(ctx, filter.Scope, filter.ScopeID, scopeResult.Scopes)
	}

	writeJSON(w, http.StatusOK, ListSkillsResponse{
		Skills:       skills,
		NextCursor:   result.NextCursor,
		TotalCount:   result.TotalCount,
		Capabilities: scopeCap,
	})
}

// listSkillsForToken writes the skill list for a user access token. The
// store query keeps the token's read boundary (filter.AccessScope), and
// every candidate row then passes the token's own skill read decision.
// The page, TotalCount and next cursor are computed over the rows that pass,
// so a row the token may not read is neither returned nor counted. The
// cursor is sealed and bound to the endpoint, the filter and the
// credential, so it opens only for this token.
func (s *Server) listSkillsForToken(w http.ResponseWriter, r *http.Request, token *ScopedUserIdentity, filter store.SkillFilter, projects ScopeSet) {
	ctx, query := r.Context(), r.URL.Query()
	limit := listOptionsFromQuery(query).Limit
	if limit > authorizedListMaxPageSize {
		limit = authorizedListMaxPageSize
	}
	cursorBinding := scopedCursorBinding("skills", filter, token)
	readable := func(ctx context.Context, identity Identity, resources []Resource) ([]bool, error) {
		caps := s.authzService.ComputeCapabilitiesForActions(ctx, identity, resources, []Action{ActionRead})
		allowed := make([]bool, len(caps))
		for i := range caps {
			allowed[i] = capabilityAllows(caps[i], ActionRead)
		}
		return allowed, nil
	}
	result, err := listAuthorizedOrAll(
		ctx, token, query.Get("cursor"), limit, cursorBinding, s.listCursorSealer, true,
		func(ctx context.Context, opts store.ListOptions) (*store.ListResult[store.Skill], error) {
			return s.store.ListSkills(ctx, filter, opts)
		},
		skillResource,
		func(sk *store.Skill) string { return authorizedListCursor(sk.Created, sk.ID, cursorBinding) },
		readable,
	)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	// The read gate above decides only ActionRead per candidate; the full
	// capability set is computed once, for the returned page only.
	resources := make([]Resource, len(result.Items))
	for i := range result.Items {
		resources[i] = skillResource(&result.Items[i])
	}
	caps := s.authzService.ComputeCapabilitiesBatch(ctx, token, resources, "skill")
	skills := make([]SkillWithCapabilities, 0, len(result.Items))
	for i := range result.Items {
		skills = append(skills, SkillWithCapabilities{Skill: result.Items[i], Cap: caps[i]})
	}
	writeJSON(w, http.StatusOK, ListSkillsResponse{
		Skills:       skills,
		NextCursor:   result.NextCursor,
		TotalCount:   result.TotalCount,
		Capabilities: s.skillListCapabilities(ctx, filter.Scope, filter.ScopeID, projects),
	})
}

// skillListCapabilities returns the list-level capabilities for a skills
// list request. "create" is reported when the caller can create a skill in
// at least one scope the request covers: any scope when no scope filter is
// given, otherwise only the filtered scope (and, for project and user
// scopes, the filtered scopeId when one is set). Each scope is checked the
// same way createSkill authorizes a create in it. Listing is not reported
// as a capability: a 200 response already means the caller may list.
//
// projects is the caller's project list scope from ResolveListScopes; it
// supplies the candidate projects for a project-scope check without a
// scopeId.
func (s *Server) skillListCapabilities(ctx context.Context, scopeFilter, scopeIDFilter string, projects ScopeSet) *Capabilities {
	scopes := []string{scopeFilter}
	if scopeFilter == "" {
		scopes = []string{store.SkillScopeUser, store.SkillScopeProject, store.SkillScopeGlobal}
	}
	for _, scope := range scopes {
		if s.canCreateSkillInScope(ctx, scope, scopeIDFilter, projects) {
			return &Capabilities{Actions: []string{string(ActionCreate)}}
		}
	}
	return &Capabilities{Actions: []string{}}
}

// canCreateSkillInScope reports whether the caller in ctx could create a
// skill in scope (restricted to scopeID when it is set), following the
// authorization branches of createSkill.
func (s *Server) canCreateSkillInScope(ctx context.Context, scope, scopeID string, projects ScopeSet) bool {
	switch scope {
	case store.SkillScopeUser:
		// createSkill always places a user-scoped skill in the caller's own
		// user scope, so another user's scope is never creatable.
		userIdent := GetUserIdentityFromContext(ctx)
		return userIdent != nil && (scopeID == "" || scopeID == userIdent.ID())
	case store.SkillScopeGlobal:
		userIdent := GetUserIdentityFromContext(ctx)
		if userIdent == nil {
			return false
		}
		return s.authzService.CheckAccess(ctx, userIdent, skillScopeResource(scope, ""), catalogWriteAction(permissions.ResourceSkill, scope, ActionCreate)).Allowed
	case store.SkillScopeProject:
		if agentIdent := GetAgentIdentityFromContext(ctx); agentIdent != nil {
			own := agentIdent.ProjectID()
			if own == "" || (scopeID != "" && scopeID != own) {
				return false
			}
			return s.authzService.CheckAccess(ctx, agentIdent, skillScopeResource(store.SkillScopeProject, own), ActionCreate).Allowed
		}
		userIdent := GetUserIdentityFromContext(ctx)
		if userIdent == nil {
			return false
		}
		candidates := projects.ProjectIDs()
		switch {
		case scopeID != "":
			candidates = []string{scopeID}
		case projects.IsAll():
			// An unrestricted caller's project list is not enumerated; ask
			// whether it may create in project scope at all.
			candidates = []string{""}
		}
		for _, projectID := range candidates {
			if s.authzService.CheckAccess(ctx, userIdent, skillScopeResource(store.SkillScopeProject, projectID), ActionCreate).Allowed {
				return true
			}
		}
	}
	return false
}

// agentSkillAccessScope derives an agent's list predicate from the same
// authorization decisions its point reads get (ptone/scion#1968).
//
// For an agent principal every term of Decide depends on a skill row only
// through its bucket: (scope kind, project) for hub and project skills, and
// (scope kind, owning user) for user skills. The project-scoped JWT binding,
// the synthetic agent-skill-catalog binding (Step 5b/5b2, global only
// after Step 5c), the personal-skill progeny relationship grant (Step 9, user
// skills owned by the agent's origin user only, with that user's admission to
// the agent's execution project, a per-agent fact), the JWT scope restriction
// and access constraints (Step 7), and the delegation ceiling (Step 10,
// permission-level at the agent's project) all read nothing else from the
// row. So one probe per bucket equals the per-row outcome for every row in
// that bucket, and the store predicate built from the probes returns exactly
// the rows the per-row check allows.
//
// The only user bucket that can be granted is the origin user's, so that is
// the only one probed; every other user's skills stay out of the predicate.
func (s *Server) agentSkillAccessScope(ctx context.Context, agent AgentIdentity) *store.SkillAccessScope {
	scope := &store.SkillAccessScope{}
	scope.IncludeHubScope = s.authzService.CheckAccess(ctx, agent,
		skillScopeResource(store.SkillScopeGlobal, ""), ActionRead).Allowed
	if projectID := agent.ProjectID(); projectID != "" {
		if s.authzService.CheckAccess(ctx, agent,
			skillScopeResource(store.SkillScopeProject, projectID), ActionRead).Allowed {
			scope.ProjectIDs = []string{projectID}
		}
	}
	if origin := agent.OriginUserID(); origin != "" {
		if s.authzService.CheckAccess(ctx, agent,
			skillScopeResource(store.SkillScopeUser, origin), ActionRead).Allowed {
			scope.CallerID = origin
		}
	}
	return scope
}

// isAgentIdentity reports whether identity is a local or federated agent.
// Both agentIdentityWrapper and FederatedAgentIdentity implement
// AgentIdentity; BrokerIdentity and UserIdentity implementations
// deliberately do not (see brokerIdentityImpl's doc comment, DEF-58).
func isAgentIdentity(identity Identity) bool {
	_, ok := identity.(AgentIdentity)
	return ok
}

// readSkillWriteBody decodes a create or update skill request body into v.
// Skills no longer carry a visibility setting (access follows the skill's
// scope), so a body that still sends one is rejected with 400 rather than
// having the field silently dropped. The body must be a single JSON value:
// trailing data is rejected, so the visibility check always covers exactly
// the value decoded into v. The body is limited by readRawBody (413 when
// exceeded). On failure it writes the error response and returns false.
func readSkillWriteBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	body, err := readRawBody(w, r)
	if err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Request body too large", nil)
			return false
		}
		BadRequest(w, "Invalid request body: "+err.Error())
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	var value json.RawMessage
	if err := dec.Decode(&value); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		BadRequest(w, "Invalid request body: unexpected data after the JSON value")
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) == nil {
		for name := range fields {
			// encoding/json matches struct fields case-insensitively, so
			// treat the key the same way.
			if strings.EqualFold(name, "visibility") {
				ValidationError(w, "visibility is not supported: access to a skill is determined by its scope",
					map[string]interface{}{"field": "visibility"})
				return false
			}
		}
	}
	if err := json.Unmarshal(value, v); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return false
	}
	return true
}

// writeSkillMissingScope answers 403 missing_scope when an agent presenting a
// real token (with a token ID) is denied a write on a skill in its own project
// because its token lacks project:skill:write (ptone/scion#9452). It never
// grants access and never fires for global, user, or another project's skills,
// avoiding any cross-project existence leakage.
func writeSkillMissingScope(w http.ResponseWriter, identity Identity, scope, scopeID string) bool {
	if scope != store.SkillScopeProject || scopeID == "" {
		return false
	}
	agent, ok := identity.(*agentIdentityWrapper)
	if !ok || agent == nil || agent.AgentTokenClaims == nil || agent.ID() == "" || agent.TokenID() == "" {
		return false
	}
	if agent.ProjectID() != scopeID {
		return false
	}
	if agentHasAnyScope(agent, []string{string(ScopeProjectSkillWrite)}) {
		return false
	}
	writeError(w, http.StatusForbidden, apiclient.ErrCodeMissingScope,
		fmt.Sprintf("the credential does not carry the %s scope needed to publish skills in this project", ScopeProjectSkillWrite),
		map[string]any{
			"scope":  string(ScopeProjectSkillWrite),
			"scopes": []string{string(ScopeProjectSkillWrite)},
		})
	return true
}

// createSkill creates a new skill record.
func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req CreateSkillRequest
	if !readSkillWriteBody(w, r, &req) {
		return
	}

	if req.Name == "" {
		ValidationError(w, "name is required", nil)
		return
	}

	if err := api.ValidateSkillName(req.Name); err != nil {
		ValidationError(w, fmt.Sprintf("invalid skill name: %v", err), nil)
		return
	}

	// Validate scope
	scope := req.Scope
	if scope == "" {
		scope = store.SkillScopeGlobal
	}
	switch scope {
	case store.SkillScopeGlobal, store.SkillScopeProject, store.SkillScopeUser:
	case "core":
		ValidationError(w, retiredSkillScopeCoreMessage, nil)
		return
	default:
		ValidationError(w, fmt.Sprintf("invalid scope %q: must be one of global, project, user", scope), nil)
		return
	}

	// Authorize
	switch scope {
	case store.SkillScopeGlobal:
		userIdent := GetUserIdentityFromContext(ctx)
		if userIdent == nil {
			if GetIdentityFromContext(ctx) != nil {
				writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to create global skills", nil)
				return
			}
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
			return
		}
		// A global skill is a new record in the hub catalog: the
		// check is skill.create_global on a collection-level request at the
		// hub scope, so a user access token needs skill:create_global on a
		// hub boundary.
		decision := s.authzService.CheckAccessWithEvidence(ctx, userIdent, skillScopeResource(scope, ""),
			catalogWriteAction(permissions.ResourceSkill, scope, ActionCreate), hubCollectionEvidence("skill.create_global"))
		if !decision.Allowed {
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to create global skills", nil)
			return
		}
	case store.SkillScopeProject:
		if req.ScopeID == "" {
			writeError(w, http.StatusBadRequest, "scope_id_required",
				"scopeId is required for project-scoped skills", nil)
			return
		}
		identity := GetIdentityFromContext(ctx)
		if identity == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
			return
		}
		decision := s.authzService.CheckAccess(ctx, identity,
			skillScopeResource(store.SkillScopeProject, req.ScopeID), ActionCreate)
		if !decision.Allowed {
			if writeSkillMissingScope(w, identity, store.SkillScopeProject, req.ScopeID) {
				return
			}
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to create skills in this project", nil)
			return
		}
		if agentIdent, ok := identity.(AgentIdentity); ok {
			if s.agentStandingForbidden(ctx, w, agentIdent.ID()) {
				return
			}
		}
	case store.SkillScopeUser:
		userIdent := GetUserIdentityFromContext(ctx)
		if userIdent == nil {
			if GetIdentityFromContext(ctx) != nil {
				writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to create skills in user scope", nil)
				return
			}
			writeError(w, http.StatusUnauthorized, "unauthorized", "User authentication required for user-scoped skills", nil)
			return
		}
		if !requireProfileWriter(w, r) {
			return
		}
		req.ScopeID = userIdent.ID()
		// A user-scope skill is a new record in the caller's user scope: a
		// user access token needs skill:create on a hub boundary.
		decision := s.authzService.CheckAccess(ctx, userIdent, skillUserScopeResource(userIdent), ActionCreate)
		if !decision.Allowed {
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to create skills in your user scope", nil)
			return
		}
	}

	slug := api.Slugify(req.Name)

	skill := &store.Skill{
		ID:          api.NewUUID(),
		Name:        req.Name,
		Slug:        slug,
		Description: req.Description,
		Tags:        req.Tags,
		Scope:       scope,
		ScopeID:     req.ScopeID,
		Status:      "active",
	}

	// Set owner from identity
	if identity := GetIdentityFromContext(ctx); identity != nil {
		skill.OwnerID = identity.ID()
		skill.CreatedBy = identity.ID()
		skill.UpdatedBy = identity.ID()
	}

	// Generate storage path and URI
	storagePath := storage.SkillStoragePath(s.HubID(), skill.Scope, skill.ScopeID, skill.Slug)
	skill.StoragePath = storagePath

	stor := s.GetStorage()
	if stor != nil {
		skill.StorageBucket = stor.Bucket()
		skill.StorageURI = storage.SkillStorageURI(s.HubID(), stor.Bucket(), skill.Scope, skill.ScopeID, skill.Slug)
	}

	if err := s.store.CreateSkill(ctx, skill); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict", "A skill with this slug already exists in the target scope", nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusCreated, CreateSkillResponse{Skill: skill})
}

// getSkill retrieves a skill with capabilities.
func (s *Server) getSkill(w http.ResponseWriter, r *http.Request, id string) {
	if !checkAgentReadScope(w, r) {
		return
	}

	ctx := r.Context()
	skill, err := s.store.GetSkill(ctx, id)
	if err != nil {
		writeSkillLookupError(w, err)
		return
	}

	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		NotFound(w, "Skill")
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), ActionRead)
	if !decision.Allowed {
		NotFound(w, "Skill")
		return
	}

	resp := SkillWithCapabilities{Skill: *skill}
	resp.Cap = s.authzService.ComputeCapabilities(ctx, identity, skillResource(skill))

	writeJSON(w, http.StatusOK, resp)
}

// updateSkill updates specific skill fields.
func (s *Server) updateSkill(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	existing, err := s.store.GetSkill(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Authorize
	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
		return
	}
	if !requireSkillProfileWriter(w, r, existing) {
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(existing), catalogWriteAction(permissions.ResourceSkill, existing.Scope, ActionUpdate))
	if !decision.Allowed {
		if writeSkillMissingScope(w, identity, existing.Scope, existing.ScopeID) {
			return
		}
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to update this skill", nil)
		return
	}

	var updates UpdateSkillRequest
	if !readSkillWriteBody(w, r, &updates) {
		return
	}

	if updates.Name != "" {
		existing.Name = updates.Name
		existing.Slug = api.Slugify(updates.Name)
	}
	if updates.Description != "" {
		existing.Description = updates.Description
	}
	if updates.Tags != nil {
		existing.Tags = updates.Tags
	}

	if identity := GetIdentityFromContext(ctx); identity != nil {
		existing.UpdatedBy = identity.ID()
	}

	if err := s.store.UpdateSkill(ctx, existing); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, existing)
}

// deleteSkill soft-deletes a skill by setting status to archived.
func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	existing, err := s.store.GetSkill(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Authorize
	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
		return
	}
	if !requireSkillProfileWriter(w, r, existing) {
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(existing), catalogWriteAction(permissions.ResourceSkill, existing.Scope, ActionDelete))
	if !decision.Allowed {
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to delete this skill", nil)
		return
	}

	if err := s.store.DeleteSkill(ctx, id); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleSkillVersions handles /api/v1/skills/{id}/versions (GET=list, POST=create).
func (s *Server) handleSkillVersions(w http.ResponseWriter, r *http.Request, skillID string) {
	switch r.Method {
	case http.MethodGet:
		s.listSkillVersions(w, r, skillID)
	case http.MethodPost:
		s.publishSkillVersion(w, r, skillID)
	default:
		MethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

// handleSkillVersionByID handles /api/v1/skills/{id}/versions/{versionId}[/deprecate].
func (s *Server) handleSkillVersionByID(w http.ResponseWriter, r *http.Request, skillID, versionID string) {
	if strings.HasSuffix(versionID, "/deprecate") {
		vid := strings.TrimSuffix(versionID, "/deprecate")
		s.deprecateSkillVersion(w, r, skillID, vid)
		return
	}
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, http.MethodGet)
		return
	}
	s.getSkillVersion(w, r, skillID, versionID)
}

// listSkillVersions lists versions for a skill.
func (s *Server) listSkillVersions(w http.ResponseWriter, r *http.Request, skillID string) {
	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeSkillLookupError(w, err)
		return
	}

	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		NotFound(w, "Skill")
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), ActionRead)
	if !decision.Allowed {
		NotFound(w, "Skill")
		return
	}

	// Version status filter (skill_version_status.go), applied in the store
	// so the page and totalCount agree.
	var filter store.SkillVersionFilter
	if !s.skillVersionsAllStatuses(ctx, identity, skill) {
		filter.Statuses = skillVersionListedStatuses
	}
	result, err := s.store.ListSkillVersions(ctx, skillID, filter, store.ListOptions{
		Limit: 100,
	})
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	for i := range result.Items {
		s.withSkillContentHashV2(&result.Items[i])
	}

	writeJSON(w, http.StatusOK, result)
}

// getSkillVersion retrieves a specific skill version.
func (s *Server) getSkillVersion(w http.ResponseWriter, r *http.Request, skillID, versionID string) {
	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeSkillLookupError(w, err)
		return
	}

	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		NotFound(w, "Skill")
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), ActionRead)
	if !decision.Allowed {
		NotFound(w, "Skill")
		return
	}

	sv, err := s.store.GetSkillVersion(ctx, versionID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// A version of another skill, and a version outside the status filter
	// (skill_version_status.go), get the same response as a missing
	// version ID.
	if sv.SkillID != skillID || !s.skillVersionVisible(ctx, identity, skill, sv) {
		writeErrorFromErr(w, store.ErrNotFound, "")
		return
	}

	writeJSON(w, http.StatusOK, s.withSkillContentHashV2(sv))
}

// deprecateSkillVersion marks a published skill version as deprecated.
func (s *Server) deprecateSkillVersion(w http.ResponseWriter, r *http.Request, skillID, versionID string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, http.MethodPost)
		return
	}

	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
		return
	}
	if !requireSkillProfileWriter(w, r, skill) {
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), catalogWriteAction(permissions.ResourceSkill, skill.Scope, ActionUpdate))
	if !decision.Allowed {
		if writeSkillMissingScope(w, identity, skill.Scope, skill.ScopeID) {
			return
		}
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to deprecate versions of this skill", nil)
		return
	}

	var req DeprecateVersionRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}
	if req.Message == "" {
		ValidationError(w, "message is required for deprecation", nil)
		return
	}

	sv, err := s.store.GetSkillVersion(ctx, versionID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if sv.SkillID != skillID {
		NotFound(w, "SkillVersion")
		return
	}
	if sv.Status != store.SkillVersionStatusPublished {
		writeError(w, http.StatusConflict, "conflict",
			fmt.Sprintf("only published versions can be deprecated (current status: %s)", sv.Status), nil)
		return
	}

	sv.Status = store.SkillVersionStatusDeprecated
	sv.DeprecationMessage = req.Message
	sv.ReplacementURI = req.ReplacementURI

	if err := s.store.UpdateSkillVersion(ctx, sv); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, s.withSkillContentHashV2(sv))
}

// publishSkillVersion creates a new draft version and returns upload URLs.
func (s *Server) publishSkillVersion(w http.ResponseWriter, r *http.Request, skillID string) {
	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Authorize: publishing a version is an update on the skill
	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
		return
	}
	if !requireSkillProfileWriter(w, r, skill) {
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), catalogWriteAction(permissions.ResourceSkill, skill.Scope, ActionUpdate))
	if !decision.Allowed {
		if writeSkillMissingScope(w, identity, skill.Scope, skill.ScopeID) {
			return
		}
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to publish versions for this skill", nil)
		return
	}
	if agentIdent, ok := identity.(AgentIdentity); ok {
		if s.agentStandingForbidden(ctx, w, agentIdent.ID()) {
			return
		}
	}

	// Dispatch based on content type: multipart uploads are handled inline,
	// while JSON requests go through the existing two-phase upload flow.
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/form-data") {
		s.publishSkillVersionMultipart(w, r, skill)
		return
	}

	var req PublishVersionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Version == "" {
		ValidationError(w, "version is required", nil)
		return
	}

	// Validate semver
	if _, err := semver.NewVersion(req.Version); err != nil {
		ValidationError(w, fmt.Sprintf("invalid semver version %q: %s", req.Version, err.Error()), nil)
		return
	}

	if err := validateUploadFilePaths(req.Files); err != nil {
		if !writeInvalidFilePathError(w, err) {
			ValidationError(w, "files are invalid", nil)
		}
		return
	}

	// Check for an existing version with this number.
	// - Draft: reuse it (idempotent retry of an incomplete publish).
	// - Published/deprecated/archived: reject as conflict.
	var sv *store.SkillVersion
	existing, err := s.store.GetSkillVersionByNumber(ctx, skillID, req.Version)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErrorFromErr(w, err, "")
		return
	}
	if err == nil {
		switch existing.Status {
		case store.SkillVersionStatusDraft:
			sv = existing
		case store.SkillVersionStatusPublished:
			writeError(w, http.StatusConflict, "conflict",
				fmt.Sprintf("version %s is already published and immutable; publish a new version instead", req.Version), nil)
			return
		default:
			writeError(w, http.StatusConflict, "conflict",
				fmt.Sprintf("version %s already exists with status %q", req.Version, existing.Status), nil)
			return
		}
	}

	if sv == nil {
		// Create new draft version
		sv = &store.SkillVersion{
			ID:      api.NewUUID(),
			SkillID: skillID,
			Version: req.Version,
			Status:  store.SkillVersionStatusDraft,
		}

		if identity := GetIdentityFromContext(ctx); identity != nil {
			sv.PublisherID = identity.ID()
		}

		if err := s.store.CreateSkillVersion(ctx, sv); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				writeError(w, http.StatusConflict, "conflict",
					fmt.Sprintf("version %s already exists for this skill", req.Version), nil)
				return
			}
			writeErrorFromErr(w, err, "")
			return
		}
	}

	response := PublishVersionResponse{
		Version: sv,
	}

	// Generate upload URLs if files were specified and storage is available
	if len(req.Files) > 0 {
		stor := s.GetStorage()
		if stor != nil {
			versionPath := skill.StoragePath + "/" + req.Version
			uploadURLs, _, err := generateUploadURLs(ctx, stor, versionPath, req.Files)
			if err == nil && len(uploadURLs) > 0 {
				if stor.Provider() == storage.ProviderLocal {
					hubURL := s.requestBaseURL(r)
					uploadURLs = rewriteLocalUploadURLs(uploadURLs, hubURL, "skills", skillID)
					uploadURLs = withSkillVersionUploadQuery(uploadURLs, req.Version)
				}
				response.UploadURLs = uploadURLs
			}
		}
	}

	writeJSON(w, http.StatusCreated, response)
}

// publishSkillVersionMultipart handles multipart/form-data skill version
// publishing. Files are uploaded inline in the request body rather than via
// the two-phase signed-URL flow. The caller has already authenticated and
// authorized the request.
func (s *Server) publishSkillVersionMultipart(w http.ResponseWriter, r *http.Request, skill *store.Skill) {
	ctx := r.Context()

	// a) Size limit: 50 MB max request body.
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)

	// b) Parse multipart form: 10 MB in memory, rest spills to disk.
	cleanup, err := parseMultipartUpload(r, 10<<20)
	defer cleanup()
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 50MB limit", nil)
			return
		}
		BadRequest(w, "Failed to parse multipart form: "+err.Error())
		return
	}

	// c) Extract and validate version.
	version := r.FormValue("version")
	if version == "" {
		ValidationError(w, "version is required", nil)
		return
	}
	if _, err := semver.NewVersion(version); err != nil {
		ValidationError(w, fmt.Sprintf("invalid semver version %q: %s", version, err.Error()), nil)
		return
	}

	// d) Check for existing published version (immutability).
	existing, err := s.store.GetSkillVersionByNumber(ctx, skill.ID, version)
	if err == nil && existing.Status == store.SkillVersionStatusPublished {
		writeError(w, http.StatusConflict, "conflict",
			fmt.Sprintf("version %s is already published and immutable; publish a new version instead", version), nil)
		return
	}

	// e) Extract and validate files.
	fileHeaders := r.MultipartForm.File["file"]
	if len(fileHeaders) == 0 {
		ValidationError(w, "at least one file is required", nil)
		return
	}
	if len(fileHeaders) > 50 {
		ValidationError(w, "too many files (max 50)", nil)
		return
	}

	hasSkillMD := false
	seenFilenames := make(map[string]struct{}, len(fileHeaders))
	for _, fh := range fileHeaders {
		name := fh.Filename

		// Security: reject dangerous filenames.
		if name == "" {
			ValidationError(w, "file has empty filename", nil)
			return
		}
		if strings.Contains(name, "..") {
			ValidationError(w, fmt.Sprintf("filename %q contains path traversal sequence", name), nil)
			return
		}
		if strings.HasPrefix(name, "/") {
			ValidationError(w, fmt.Sprintf("filename %q must not start with /", name), nil)
			return
		}
		if strings.ContainsRune(name, 0) {
			ValidationError(w, fmt.Sprintf("filename %q contains null byte", name), nil)
			return
		}

		// Duplicate filename check.
		if _, dup := seenFilenames[name]; dup {
			ValidationError(w, fmt.Sprintf("duplicate filename %q", name), nil)
			return
		}
		seenFilenames[name] = struct{}{}

		// Per-file size limit: 10 MB.
		if fh.Size > 10*1024*1024 {
			ValidationError(w, fmt.Sprintf("file %q exceeds 10MB limit", name), nil)
			return
		}

		if name == "SKILL.md" {
			hasSkillMD = true
		}
	}
	if !hasSkillMD {
		ValidationError(w, "SKILL.md is required", nil)
		return
	}

	// f) Create draft version.
	sv := &store.SkillVersion{
		ID:      api.NewUUID(),
		SkillID: skill.ID,
		Version: version,
		Status:  store.SkillVersionStatusDraft,
	}
	if identity := GetIdentityFromContext(ctx); identity != nil {
		sv.PublisherID = identity.ID()
	}
	if err := s.store.CreateSkillVersion(ctx, sv); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "conflict",
				fmt.Sprintf("version %s already exists for this skill", version), nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	// g) Upload files to storage and compute hashes.
	stor := s.GetStorage()
	if stor == nil {
		_ = s.store.DeleteSkillVersion(ctx, sv.ID)
		RuntimeError(w, "Storage not configured")
		return
	}

	type uploadResult struct {
		file store.TemplateFile
	}
	results := make([]uploadResult, len(fileHeaders))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fileUploadConcurrency)
	for i, fh := range fileHeaders {
		i, fh := i, fh
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}

			f, err := fh.Open()
			if err != nil {
				return fmt.Errorf("failed to open file %s: %w", fh.Filename, err)
			}
			defer func() { _ = f.Close() }()

			data, err := io.ReadAll(f)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", fh.Filename, err)
			}

			hash := transfer.HashBytes(data)
			objectPath := skill.StoragePath + "/" + version + "/" + fh.Filename

			if _, err := stor.Upload(gctx, objectPath, bytes.NewReader(data), storage.UploadOptions{}); err != nil {
				return fmt.Errorf("failed to upload file %s: %w", fh.Filename, err)
			}

			results[i] = uploadResult{
				file: store.TemplateFile{
					Path: fh.Filename,
					Size: int64(len(data)),
					Hash: hash,
				},
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		_ = s.store.DeleteSkillVersion(ctx, sv.ID)
		slog.ErrorContext(ctx, "skill file upload failed", "skill_id", skill.ID, "version", version, "error", upstreamText(err.Error()))
		RuntimeError(w, "Failed to upload files")
		return
	}

	// Build manifest from results.
	manifest := make([]store.TemplateFile, len(results))
	for i, r := range results {
		manifest[i] = r.file
	}

	// i) Compute content hash (v1: skill versions are pinned by it).
	contentHash := computeSkillContentHash(manifest)

	// j) Update version to published.
	sv.Status = store.SkillVersionStatusPublished
	sv.Files = manifest
	sv.ContentHash = contentHash
	if err := s.store.UpdateSkillVersion(ctx, sv); err != nil {
		_ = s.store.DeleteSkillVersion(ctx, sv.ID)
		writeErrorFromErr(w, err, "")
		return
	}

	// k) Return response.
	writeJSON(w, http.StatusCreated, PublishVersionResponse{Version: s.withSkillContentHashV2(sv)})
}

// handleSkillUpload handles requests for upload URLs for a skill.
func (s *Server) handleSkillUpload(w http.ResponseWriter, r *http.Request, skillID string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, http.MethodPost)
		return
	}

	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Authorize
	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
		return
	}
	if !requireSkillProfileWriter(w, r, skill) {
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), catalogWriteAction(permissions.ResourceSkill, skill.Scope, ActionUpdate))
	if !decision.Allowed {
		if writeSkillMissingScope(w, identity, skill.Scope, skill.ScopeID) {
			return
		}
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to upload files for this skill", nil)
		return
	}
	if agentIdent, ok := identity.(AgentIdentity); ok {
		if s.agentStandingForbidden(ctx, w, agentIdent.ID()) {
			return
		}
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	var req struct {
		Version string              `json:"version"`
		Files   []FileUploadRequest `json:"files"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Version == "" {
		ValidationError(w, "version is required", nil)
		return
	}
	if _, err := semver.NewVersion(req.Version); err != nil {
		ValidationError(w, fmt.Sprintf("invalid semver version %q: %s", req.Version, err.Error()), nil)
		return
	}
	if len(req.Files) == 0 {
		ValidationError(w, "at least one file is required", nil)
		return
	}

	versionPath := skill.StoragePath + "/" + req.Version
	uploadURLs, manifestURL, err := generateUploadURLs(ctx, stor, versionPath, req.Files)
	if err != nil {
		if writeInvalidFilePathError(w, err) {
			return
		}
		slog.ErrorContext(ctx, "skill upload URLs failed", "skill_id", skill.ID, "version", req.Version, "error", upstreamText(err.Error()))
		RuntimeError(w, "Failed to generate upload URLs")
		return
	}

	if stor.Provider() == storage.ProviderLocal {
		hubURL := s.requestBaseURL(r)
		uploadURLs = rewriteLocalUploadURLs(uploadURLs, hubURL, "skills", skillID)
		uploadURLs = withSkillVersionUploadQuery(uploadURLs, req.Version)
	}

	writeJSON(w, http.StatusOK, UploadResponse{
		UploadURLs:  uploadURLs,
		ManifestURL: manifestURL,
	})
}

// handleSkillFinalize finalizes a skill version after file upload.
func (s *Server) handleSkillFinalize(w http.ResponseWriter, r *http.Request, skillID string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, http.MethodPost)
		return
	}

	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Authorize
	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
		return
	}
	if !requireSkillProfileWriter(w, r, skill) {
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), catalogWriteAction(permissions.ResourceSkill, skill.Scope, ActionUpdate))
	if !decision.Allowed {
		if writeSkillMissingScope(w, identity, skill.Scope, skill.ScopeID) {
			return
		}
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to finalize versions for this skill", nil)
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	var req FinalizeSkillVersionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Version == "" {
		ValidationError(w, "version is required", nil)
		return
	}
	if req.Manifest == nil || len(req.Manifest.Files) == 0 {
		ValidationError(w, "manifest with files is required", nil)
		return
	}
	if err := validateManifestFilePaths(req.Manifest.Files); err != nil {
		if !writeInvalidFilePathError(w, err) {
			ValidationError(w, "manifest files are invalid", nil)
		}
		return
	}

	// Validate SKILL.md is present
	hasSkillMD := false
	for _, f := range req.Manifest.Files {
		if f.Path == "SKILL.md" {
			hasSkillMD = true
			break
		}
	}
	if !hasSkillMD {
		ValidationError(w, "SKILL.md is required in the manifest", nil)
		return
	}

	// Validate file count and sizes
	if len(req.Manifest.Files) > 50 {
		ValidationError(w, "too many files (max 50)", nil)
		return
	}
	var totalSize int64
	for _, f := range req.Manifest.Files {
		if f.Size > 10*1024*1024 {
			ValidationError(w, fmt.Sprintf("file %q exceeds 10MB limit", f.Path), nil)
			return
		}
		totalSize += f.Size
	}
	if totalSize > 50*1024*1024 {
		ValidationError(w, "total file size exceeds 50MB limit", nil)
		return
	}

	// Look up the draft version
	sv, err := s.store.GetSkillVersionByNumber(ctx, skillID, req.Version)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if sv.Status == store.SkillVersionStatusPublished {
		writeError(w, http.StatusConflict, "conflict",
			fmt.Sprintf("version %s is already published and immutable", req.Version), nil)
		return
	}

	// Verify files exist in storage and compute content hash
	versionPath := skill.StoragePath + "/" + req.Version
	contentHash, err := verifyAndFinalizeFiles(ctx, stor, versionPath, req.Manifest.Files)
	if err != nil {
		if writeInvalidFilePathError(w, err) {
			return
		}
		var notFound *fileNotFoundError
		if errors.As(err, &notFound) {
			ValidationError(w, err.Error(), nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	// Update version to published
	sv.Files = req.Manifest.Files
	sv.ContentHash = contentHash
	sv.Status = store.SkillVersionStatusPublished

	if err := s.store.UpdateSkillVersion(ctx, sv); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, s.withSkillContentHashV2(sv))
}

// handleSkillDownload returns signed URLs for downloading skill version files.
func (s *Server) handleSkillDownload(w http.ResponseWriter, r *http.Request, skillID string) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, http.MethodGet)
		return
	}

	ctx := r.Context()
	query := r.URL.Query()
	version := query.Get("version")
	if version == "" {
		version = "latest"
	}

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeSkillLookupError(w, err)
		return
	}

	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		NotFound(w, "Skill")
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), ActionRead)
	if !decision.Allowed {
		NotFound(w, "Skill")
		return
	}
	if agentIdent, ok := identity.(AgentIdentity); ok && s.agentStanding(ctx, agentIdent.ID()) != nil {
		NotFound(w, "Skill")
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	// Resolve version
	sv, err := s.resolveSkillVersion(ctx, skillID, version)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	if len(sv.Files) == 0 {
		ValidationError(w, "version has no files", nil)
		return
	}

	versionPath := skill.StoragePath + "/" + sv.Version
	downloadURLs, manifestURL, expires, err := generateDownloadURLs(ctx, stor, versionPath, s.legacyFallbackPath(versionPath), sv.Files)
	if err != nil {
		slog.ErrorContext(ctx, "skill download URLs failed", "skill_id", skill.ID, "version", sv.Version, "error", upstreamText(err.Error()))
		RuntimeError(w, fmt.Sprintf("skill %q version %q: failed to generate download URLs", skill.Name, sv.Version))
		return
	}

	if stor.Provider() == storage.ProviderLocal {
		hubURL := s.requestBaseURL(r)
		downloadURLs = rewriteLocalDownloadURLs(downloadURLs, hubURL, "skills", skillID)
		downloadURLs = withSkillVersionDownloadQuery(downloadURLs, sv.Version)
	}

	writeJSON(w, http.StatusOK, DownloadResponse{
		Files:       downloadURLs,
		ManifestURL: manifestURL,
		Expires:     expires,
	})
}

// handleSkillResolveSingle resolves a single skill version (for debug/test).
func (s *Server) handleSkillResolveSingle(w http.ResponseWriter, r *http.Request, skillID string) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w, http.MethodGet)
		return
	}

	ctx := r.Context()

	skill, err := s.store.GetSkill(ctx, skillID)
	if err != nil {
		writeSkillLookupError(w, err)
		return
	}

	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		NotFound(w, "Skill")
		return
	}
	decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), ActionRead)
	if !decision.Allowed {
		NotFound(w, "Skill")
		return
	}

	version := r.URL.Query().Get("version")
	if version == "" {
		version = "latest"
	}

	sv, err := s.resolveSkillVersion(ctx, skillID, version)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, s.withSkillContentHashV2(sv))
}

// handleSkillsResolve handles batch skill resolution: POST /api/v1/skills/resolve.
func (s *Server) handleSkillsResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w, http.MethodPost)
		return
	}

	ctx := r.Context()

	if agentIdent := GetAgentIdentityFromContext(ctx); agentIdent != nil {
		if s.agentStandingForbidden(ctx, w, agentIdent.ID()) {
			return
		}
	}

	var req ResolveSkillsRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Skills) == 0 {
		ValidationError(w, "at least one skill reference is required", nil)
		return
	}

	const maxResolveItems = 50
	if len(req.Skills) > maxResolveItems {
		ValidationError(w, fmt.Sprintf("too many skills in request (max %d)", maxResolveItems), nil)
		return
	}

	stor := s.GetStorage()

	var resolved []ResolvedSkillResponse
	var resolveErrors []ResolveSkillError

	// Resolving a gh:// URI with a non-empty ProjectID makes the Hub mint that
	// project's GitHub App token, so the caller must be authorized for the
	// project — otherwise anyone could borrow another project's installation to
	// read its private repositories. An empty ProjectID resolves anonymously
	// (no token minted). Evaluated once, up front, and only when the request
	// actually contains a gh:// URI.
	ghProjectAllowed := true
	ghCanReceiveDownloadToken := false
	ghDeniedMessage := "caller may not resolve GitHub skills for this project"
	if hasGitHubSkillRef(req.Skills) {
		if req.ProjectID != "" {
			ghProjectAllowed = s.canUseProjectGitHubToken(ctx, req.ProjectID)
			ghCanReceiveDownloadToken = ghProjectAllowed && s.canReceiveProjectGitHubDownloadToken(ctx, req.ProjectID)
		} else {
			// With no ProjectID the Hub fetches public repositories and
			// writes its resolution cache on the caller's behalf: a user
			// access token needs skill:read on a hub boundary.
			ghProjectAllowed = s.tokenMayResolvePublicGitHubSkills(r)
			ghDeniedMessage = "caller may not resolve GitHub skills"
		}
	}

	// Per-request memos for (owner,repo,ref,tokenScope) → commitSHA and
	// (projectID, lower(owner), lower(repo)) → Credential. URIs sharing a repo
	// in one batch pay one credential lookup and one ref→SHA lookup instead of
	// repeating Secret Manager / DB / GitHub RPCs per URI.
	refSHAMemo := newGHSHAMemo()
	credMemo := newGHCredMemo()

	for _, skillRef := range req.Skills {
		// GitHub skill resolution: gh:// URIs are handled by the Hub's GitHub resolution cache
		if strings.HasPrefix(skillRef.URI, "gh://") {
			if !ghProjectAllowed {
				resolveErrors = append(resolveErrors, *newSkillResolveError(ctx, skillResolveSourceGitHub,
					skillRef.URI, agent.SkillErrCodeForbidden, 0, ghDeniedMessage))
				continue
			}
			ghResolved, err := s.resolveGitHubSkillWithCredMemo(ctx, skillRef.URI, req.ProjectID, refSHAMemo, credMemo)
			if err != nil {
				resolveErrors = append(resolveErrors, *newSkillResolveError(ctx, skillResolveSourceGitHub,
					skillRef.URI, ghErrorCode(err), ghErrorStatus(err), err.Error()))
			} else {
				if !ghCanReceiveDownloadToken {
					ghResolved.DownloadToken = ""
				}
				if s.skillContentHashV2Enabled() {
					// v2 over the git blob IDs and the git tree modes, the
					// metadata the broker installs and verifies from.
					if _, v2, ok := resolvedSkillContentHashes(ghResolved.Files); ok {
						ghResolved.ContentHashV2 = v2
					}
				}
				resolved = append(resolved, *ghResolved)
			}
			continue
		}

		uri, err := api.ParseSkillURI(skillRef.URI)
		if err != nil {
			resolveErrors = append(resolveErrors, *newSkillResolveError(ctx, skillResolveSourceRegistry,
				skillRef.URI, agent.SkillErrCodeInvalidURI, 0, err.Error()))
			continue
		}

		// Federation: non-scion registry → proxy to external
		if uri.Registry != "scion" && uri.Registry != "" {
			fedResolved, resolveErr := s.federateResolve(ctx, uri.Registry, skillRef)
			if resolveErr != nil {
				resolveErrors = append(resolveErrors, *resolveErr)
			} else {
				resolved = append(resolved, *fedResolved)
			}
			continue
		}

		baseURL := ""
		if stor != nil && stor.Provider() == storage.ProviderLocal {
			baseURL = s.requestBaseURL(r)
		}
		entry, resolveErr := s.resolveRegistrySkillRef(ctx, GetIdentityFromContext(ctx), skillRef.URI, uri,
			req.ProjectID, req.UserID, baseURL)
		if resolveErr != nil {
			resolveErrors = append(resolveErrors, *resolveErr)
			continue
		}
		resolved = append(resolved, *entry)
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, ResolveSkillsResponse{
		Resolved: resolved,
		Errors:   resolveErrors,
	})
}

// resolveSkill finds a skill and version by URI, searching scopes in
// priority order, on behalf of identity (nil for an unauthenticated caller).
//
// ptone/scion#1901 finding F2: authorization runs here, per candidate,
// before any version-specific detail is computed — and a candidate the
// caller cannot read (identity is nil and the skill isn't public, or
// CheckAccess denies) is skipped exactly like a scope with no matching slug
// at all, continuing the search rather than stopping to report a
// distinguishable reason. Two things this closes:
//   - a batch/single resolve of a skill name the caller cannot read now
//     produces the exact same "not found" outcome as guessing a nonexistent
//     name, instead of a distinguishable "forbidden";
//   - "found but version could not be resolved" (below) can now only ever
//     describe a candidate the caller was already authorized to read, so it
//     can no longer confirm the existence of a skill version to a caller who
//     cannot read the skill itself.
func (s *Server) resolveSkill(ctx context.Context, identity Identity, uri *api.SkillURI, projectID string) (*store.Skill, *store.SkillVersion, error) {
	scopes := determineScopeSearchOrder(uri, projectID)

	var versionErr error
	for _, sc := range scopes {
		// Skip scoped lookups that require a scopeID when none is available
		if sc.scopeID == "" && (sc.scope == store.SkillScopeProject || sc.scope == store.SkillScopeUser) {
			continue
		}

		skill, err := s.store.GetSkillBySlug(ctx, uri.Name, sc.scope, sc.scopeID)
		if err != nil {
			continue
		}

		if identity == nil {
			continue
		}
		decision := s.authzService.CheckAccess(ctx, identity, skillResource(skill), ActionRead)
		if !decision.Allowed {
			slog.WarnContext(ctx, "skill resolve candidate denied, continuing scope search",
				"skill_id", skill.ID, "scope", sc.scope, "identity_type", identity.Type(), "reason", decision.Reason)
			continue
		}

		sv, err := s.resolveSkillVersion(ctx, skill.ID, uri.Version)
		if err != nil {
			versionErr = err
			continue
		}

		return skill, sv, nil
	}
	if versionErr != nil {
		return nil, nil, fmt.Errorf("skill %q found but version %q could not be resolved: %w", uri.Name, uri.Version, versionErr)
	}
	return nil, nil, fmt.Errorf("skill %q not found in any scope", uri.Name)
}

type scopeEntry struct {
	scope   string
	scopeID string
}

// determineScopeSearchOrder returns the scope search order for skill resolution.
func determineScopeSearchOrder(uri *api.SkillURI, projectID string) []scopeEntry {
	// If explicit scope is set, search only that scope.
	if uri.Scope != "" {
		return []scopeEntry{{scope: uri.Scope, scopeID: uri.ScopeID}}
	}

	// Default search order: user > project > global
	var order []scopeEntry
	if uri.ScopeID != "" {
		order = append(order, scopeEntry{scope: store.SkillScopeUser, scopeID: uri.ScopeID})
	}
	if projectID != "" {
		order = append(order, scopeEntry{scope: store.SkillScopeProject, scopeID: projectID})
	}
	order = append(order, scopeEntry{scope: store.SkillScopeGlobal})
	return order
}

// expandScopeAliases fills in scope IDs from request context.
func expandScopeAliases(uri *api.SkillURI, projectID, userID string) {
	if uri.Scope == store.SkillScopeProject && uri.ScopeID == "" && projectID != "" {
		uri.ScopeID = projectID
	}
	if uri.Scope == store.SkillScopeUser && uri.ScopeID == "" && userID != "" {
		uri.ScopeID = userID
	}
}

// skillResource constructs a Resource from a store.Skill for capability computation.
func skillResource(s *store.Skill) Resource {
	if s == nil {
		return Resource{Type: "skill"}
	}
	r := skillScopeResource(s.Scope, s.ScopeID)
	r.ID = s.ID
	r.OwnerID = s.OwnerID
	return r
}

// writeSkillLookupError writes the response for a failed store.GetSkill
// lookup on a read surface. ptone/scion#1901 finding F2a: a missing skill
// and a skill that exists but the caller cannot read must be byte-for-byte
// indistinguishable, so both use NotFound(w, "Skill") — the same call the
// CheckAccess-denied branch a few lines below already uses. Before this fix,
// a missing skill instead went through writeErrorFromErr, which emits the
// generic {"code":"not_found","message":"Resource not found"} — same status
// and code, but different message text than the denied-access body — letting
// an unauthorized caller distinguish "doesn't exist" from "exists, denied"
// by string-comparing responses. Errors other than store.ErrNotFound (a
// genuine backend failure) still go through writeErrorFromErr unchanged.
func writeSkillLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		NotFound(w, "Skill")
		return
	}
	writeErrorFromErr(w, err, "")
}

// skillScopeResource builds an ad hoc "skill" Resource for authorization
// checks that have a scope to evaluate but no concrete store.Skill record
// (e.g. minting a project's GitHub App token before any specific skill is
// resolved — see canUseProjectGitHubToken). scope should be one of the
// store.SkillScope* constants; scopeID is the owning user or project ID and
// is ignored for global scope.
//
// ptone/scion#1901 finding F3/F4: every ad hoc skill Resource literal must
// set ScopeKind through this constructor (or skillResource, for a real
// record), never by hand — filterHubWideSkillGrants only narrows the
// curated hub-member/hub-viewer grant when ScopeKind is populated, so a
// hand-built literal that forgets it silently reopens the #1901 leak. See
// TestSkillResourceLiterals_AllUseCanonicalConstructor.
func skillScopeResource(scope, scopeID string) Resource {
	r := Resource{Type: "skill", ScopeKind: scope}
	if scope == store.SkillScopeProject && scopeID != "" {
		r.ParentType = "project"
		r.ParentID = scopeID
	}
	if scope == store.SkillScopeUser {
		r.ScopeUserID = scopeID
	}
	return r
}

// skillUserScopeResource builds the "skill" Resource for a new skill in
// the caller's own user scope. Like templateUserScopeResource, it takes the
// authenticated UserIdentity, so the owner and scope user always name the
// caller and never a request field.
func skillUserScopeResource(userIdent UserIdentity) Resource {
	r := skillScopeResource(store.SkillScopeUser, userIdent.ID())
	r.OwnerID = userIdent.ID()
	return r
}

// hasGitHubSkillRef reports whether any reference in the batch is a gh:// URI.
func hasGitHubSkillRef(refs []ResolveSkillRef) bool {
	for _, ref := range refs {
		if strings.HasPrefix(ref.URI, "gh://") {
			return true
		}
	}
	return false
}

// skillGitHubReadOnlyPermissions is the least-privilege permission set used
// when minting a project's GitHub App token for gh:// skill resolution and
// broker/agent file downloads (ptone/scion#5028). It requests read-only access
// to repository contents and metadata and never write access.
var skillGitHubReadOnlyPermissions = githubapp.TokenPermissions{
	Contents: "read",
	Metadata: "read",
}

// canReceiveProjectGitHubDownloadToken reports whether the caller in ctx is an
// authorized download consumer for projectID's read-only GitHub App skill
// download token: a BrokerIdentity registered as a provider for projectID, or
// an AgentIdentity belonging to projectID in good standing. Users and user
// access tokens never receive the raw GitHub App token in the resolve response
// (ptone/scion#5028).
func (s *Server) canReceiveProjectGitHubDownloadToken(ctx context.Context, projectID string) bool {
	if projectID == "" {
		return false
	}
	// A BrokerIdentity is NOT a global privilege: broker join is unauthenticated,
	// so anyone can mint one. A broker may only borrow the token of a project it
	// is registered to serve, which GetProjectProvider confirms.
	if brokerIdent := GetBrokerIdentityFromContext(ctx); brokerIdent != nil {
		if s.store == nil {
			return false
		}
		_, err := s.store.GetProjectProvider(ctx, projectID, brokerIdent.BrokerID())
		return err == nil
	}
	if agentIdent := GetAgentIdentityFromContext(ctx); agentIdent != nil {
		if agentIdent.ProjectID() != projectID {
			return false
		}
		return s.agentStanding(ctx, agentIdent.ID()) == nil
	}
	return false
}

// canUseProjectGitHubToken reports whether the caller in ctx is permitted to have
// the Hub mint projectID's GitHub App token on their behalf. Agents are confined
// to their own project; users must hold read access on the project's skills;
// brokers must be registered as a provider for the project.
// Unauthenticated callers are always denied.
func (s *Server) canUseProjectGitHubToken(ctx context.Context, projectID string) bool {
	if GetBrokerIdentityFromContext(ctx) != nil || GetAgentIdentityFromContext(ctx) != nil {
		return s.canReceiveProjectGitHubDownloadToken(ctx, projectID)
	}
	// Last arm, and the only one left: anything that is not a broker or an agent
	// must be a user holding read on the project's skills. The nil test is
	// written as an explicit deny rather than as a guard around the CheckAccess
	// call, because a guarded call is the #591 shape — there the deny is implied
	// by the absence of an else, and an edit that adds code after the block
	// silently turns it into an allow.
	userIdent := GetUserIdentityFromContext(ctx)
	if userIdent == nil {
		return false
	}
	if s.authzService == nil {
		return false
	}
	return s.authzService.CheckAccess(ctx, userIdent, skillScopeResource(store.SkillScopeProject, projectID), ActionRead).Allowed
}

// tokenMayResolvePublicGitHubSkills reports whether a request may have the
// Hub resolve gh:// skills from public repositories with no project
// credentials. A user access token needs skill.read on the global catalog,
// which resolves to the hub scope, so the token needs skill:read on a hub
// boundary. The check applies to user access token requests.
func (s *Server) tokenMayResolvePublicGitHubSkills(r *http.Request) bool {
	ctx := r.Context()
	userIdent := GetUserIdentityFromContext(ctx)
	if userIdent == nil || !isUserAccessTokenRequest(r, userIdent) {
		return true
	}
	if s.authzService == nil {
		return false
	}
	return s.authzService.CheckAccess(ctx, userIdent, skillScopeResource(store.SkillScopeGlobal, ""), ActionRead).Allowed
}

// skillGitHubEndpoint returns the GitHub endpoint for Hub skill resolution,
// honouring GitHub Enterprise APIBaseURL / RawBaseURL overrides when configured.
func (s *Server) skillGitHubEndpoint() githubfetch.Endpoint {
	ep := githubfetch.DefaultEndpoint()
	if s.config.GitHubAppConfig.APIBaseURL != "" {
		ep.APIBase = s.config.GitHubAppConfig.APIBaseURL
	}
	if s.config.GitHubAppConfig.RawBaseURL != "" {
		ep.RawBase = s.config.GitHubAppConfig.RawBaseURL
	}
	return ep
}

// skillGitHubDoerForEndpoint returns the githubfetch.Doer used for Hub skill
// resolution: tests may inject s.skillGitHubDoer; otherwise hosted Hubs use
// remotefetch.NetworkVetted (SSRF-hardened, IP denylist) and workstation Hubs
// use remotefetch.NetworkSystem (loopback, honours user proxy).
func (s *Server) skillGitHubDoerForEndpoint(ep githubfetch.Endpoint) githubfetch.Doer {
	if s.skillGitHubDoer != nil {
		return s.skillGitHubDoer
	}
	lim := githubfetch.SkillLimits()
	network := remotefetch.NetworkVetted
	if s.workstation {
		network = remotefetch.NetworkSystem
	}
	return githubfetch.NewDoer(ep, network, 16<<20, lim.RequestTimeout, s.resourceLog)
}

// getSkillGitHubClient returns a githubfetch.Client for Hub skill resolution.
func (s *Server) getSkillGitHubClient(ep githubfetch.Endpoint) *githubfetch.Client {
	return githubfetch.New(githubfetch.Options{
		Endpoint:   ep,
		Doer:       s.skillGitHubDoerForEndpoint(ep),
		Cooldown:   s.githubCooldown(),
		Clock:      s.ghClockNow,
		MaxRetries: 1,
	})
}

func (s *Server) deleteGHRepoScopeCache(ctx context.Context, ep githubfetch.Endpoint, owner, repo, scope string) {
	if s.ghResolutionStore == nil {
		return
	}
	refPfx, treePfx := githubfetch.RepoCacheKeyPrefixes(ep.APIHost(), owner, repo)
	if err := s.ghResolutionStore.DeleteByRepoAndScope(ctx, refPfx, treePfx, scope); err != nil {
		slog.WarnContext(ctx, "github_resolution_cache: failed to delete denied repo scope entries",
			"owner", owner, "repo", repo, "error", err)
	}
}

// hubGitHubRefreshTimeout bounds a background cache refresh kicked off by a
// stale hit (see resolveGitHubSkill), once detached from the request that
// triggered it. Generous enough for a commit lookup plus a contents listing
// — the Hub never downloads file bytes itself — without hanging forever if
// upstream is unresponsive.
const hubGitHubRefreshTimeout = 2 * time.Minute

// hubGHStaleRevalidateTimeout caps how long a request with a stale credentialed
// tree entry waits synchronously for a live revalidation flight before serving
// the stale entry and letting the detached flight finish and record its own
// outcome (transient refresh-failure backoff or authoritative denial purge).
// Kept well below the broker's 30s Hub client timeout so a hanging GitHub
// upstream serves stale instead of failing with a caller context timeout.
var hubGHStaleRevalidateTimeout = 5 * time.Second

// ghRefreshFailureBackoff is the nominal duration a background stale-refresh
// waits before retrying the same cache key after a failure (jittered via
// agent.JitteredTTL so failed refreshes do not all retry together). Without
// this, a persistently failing ref (rate limit, outage) would start a brand
// new refresh attempt on every single stale hit, while silently continuing to
// serve the stale value regardless.
const ghRefreshFailureBackoff = 1 * time.Minute

// hubGHRefreshConcurrency bounds how many background GitHub skill refreshes
// may hit GitHub concurrently across distinct cache keys, so a burst of stale
// entries (for example right after a rate-limit cooldown ends) drains smoothly
// instead of stampeding GitHub's rate limit again.
const hubGHRefreshConcurrency = 2

func (s *Server) ghClockNow() time.Time {
	if s.ghNow != nil {
		return s.ghNow()
	}
	return time.Now()
}

func (s *Server) ghRand() func() float64 {
	if s.ghRandFloat64 != nil {
		return s.ghRandFloat64
	}
	return rand.Float64
}

func (s *Server) ghRefreshSemaphore() chan struct{} {
	s.ghRefreshSemOnce.Do(func() {
		if s.ghRefreshSem == nil {
			s.ghRefreshSem = make(chan struct{}, hubGHRefreshConcurrency)
		}
	})
	return s.ghRefreshSem
}

// recentGHRefreshFailure reports whether a background refresh for cacheKey
// is still within its jittered failure backoff window.
func (s *Server) recentGHRefreshFailure(cacheKey string) bool {
	s.ghRefreshFailMu.Lock()
	defer s.ghRefreshFailMu.Unlock()
	retryAfter, ok := s.ghLastRefreshFailure[cacheKey]
	return ok && s.ghClockNow().Before(retryAfter)
}

func (s *Server) recordGHRefreshFailure(cacheKey string) {
	s.ghRefreshFailMu.Lock()
	defer s.ghRefreshFailMu.Unlock()
	if s.ghLastRefreshFailure == nil {
		s.ghLastRefreshFailure = make(map[string]time.Time)
	}
	backoff := agent.JitteredTTL(ghRefreshFailureBackoff, s.ghRand())
	s.ghLastRefreshFailure[cacheKey] = s.ghClockNow().Add(backoff)
}

func (s *Server) clearGHRefreshFailure(cacheKey string) {
	s.ghRefreshFailMu.Lock()
	defer s.ghRefreshFailMu.Unlock()
	delete(s.ghLastRefreshFailure, cacheKey)
}

// ghSHAMemo is a mutex-guarded (owner,repo,ref,tokenScope) → commitSHA memo,
// shared across every gh:// URI in one handleSkillsResolve call (see
// resolveGitHubSkill and fetchAndCacheGitHubSkill) to avoid redundant
// commits/{ref} lookups for URIs that share the same tuple.
//
// It must tolerate concurrent access even though handleSkillsResolve's loop
// itself calls resolveGitHubSkill one URI at a time: a flight is detached
// (see fetchAndCacheGitHubSkill's caller) and so can still be running on its
// own goroutine after its own caller's ctx has ended and resolveGitHubSkill
// has already moved on — at which point resolveGitHubSkill's own ctx.Err()
// guard stops any *new* flight for that request from starting, but it cannot
// retroactively stop one that is already in flight from finishing. A plain
// map here would then be one flight's in-progress write racing nothing
// *else* under correct code, but a mutex costs nothing on the hot path and
// removes any dependence on that guard alone being sufficient — including
// against a future change that calls resolveGitHubSkill for several URIs in
// parallel.
//
// A nil *ghSHAMemo is valid and disables memoisation (treated as always-miss
// on get, and set is a no-op), exactly like a nil map did before.
type ghSHAMemo struct {
	mu sync.Mutex
	m  map[string]string
}

func newGHSHAMemo() *ghSHAMemo {
	return &ghSHAMemo{m: make(map[string]string)}
}

func (g *ghSHAMemo) get(key string) (string, bool) {
	if g == nil {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.m[key]
	return v, ok
}

func (g *ghSHAMemo) set(key, value string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.m[key] = value
}

// ghCredMemo is a per-request mutex-guarded memo mapping
// (projectID, lower(owner), lower(repo)) to the resolved githubfetch.Credential
// (including anonymous/absent results). Scoped strictly to one HTTP request so
// rotation and revocation take effect on the next request.
type ghCredMemo struct {
	mu sync.Mutex
	m  map[string]githubfetch.Credential
}

func newGHCredMemo() *ghCredMemo {
	return &ghCredMemo{m: make(map[string]githubfetch.Credential)}
}

func (g *ghCredMemo) get(key string) (githubfetch.Credential, bool) {
	if g == nil {
		return githubfetch.Credential{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.m[key]
	return v, ok
}

func (g *ghCredMemo) set(key string, cred githubfetch.Credential) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.m[key] = cred
}

// ghFlightJoinHook, when non-nil, is called immediately before every caller —
// leader and followers alike — calls ghResolveFlight.DoChan for cacheKey.
// Tests use it to know precisely when a second (or later) caller has reached
// the point of joining an in-flight resolution, without polling or sleeping:
// the first invocation for a key is the caller that will become the flight
// leader; any later invocation for the same key, made while that leader's
// call is still outstanding, is a caller that will join it as a follower.
//
// Held in an atomic.Pointer, not a plain var, for the same reason as the
// broker's flightJoinHook (github_resolution_cache.go): a background refresh
// goroutine started by one test can still be running when that test returns
// and a later test installs its own hook, and reading/writing a plain var
// across those two goroutines with no synchronization is a data race.
var ghFlightJoinHook atomic.Pointer[func(string)]

func injectGHFlightJoin(cacheKey string) {
	if hook := ghFlightJoinHook.Load(); hook != nil {
		(*hook)(cacheKey)
	}
}

// ghStaleServeHook, when non-nil, is called synchronously each time
// resolveGitHubSkill serves a stale entry, with the cache key and whether a
// background refresh was started. Tests use it to assert that no refresh was
// started without waiting for one. Atomic for the same reason as
// ghFlightJoinHook.
var ghStaleServeHook atomic.Pointer[func(cacheKey string, refreshStarted bool)]

func injectGHStaleServe(cacheKey string, refreshStarted bool) {
	if hook := ghStaleServeHook.Load(); hook != nil {
		(*hook)(cacheKey, refreshStarted)
	}
}

// githubCooldown returns the rate-limit cooldown tracker for gh://
// resolution: s.ghCooldown when set (tests), else the process-wide tracker
// shared with the broker-side resolver.
func (s *Server) githubCooldown() *agent.GitHubCooldown {
	if s.ghCooldown != nil {
		return s.ghCooldown
	}
	return agent.SharedGitHubCooldown()
}

// resolveGitHubSkill resolves a gh:// skill URI via the Hub's GitHub resolution
// cache and pkg/githubfetch. Callers must enforce canUseProjectGitHubToken /
// tokenMayResolvePublicGitHubSkills before calling this method.
//
// Cache and revocation rules:
//   - Keys come from githubfetch.RefCacheKey and githubfetch.TreeCacheKey,
//     scoped by cred.EffectiveScope() ("anon", "app:<id>", "secret:<hmac>").
//   - For anon scopes, stale branch entries within MaxResolutionStaleAge (24h)
//     are served immediately while a background refresh updates them.
//   - For credentialed scopes (non-anon), stale entries are served only when
//     the scope's cooldown is active or a live conditional refresh fails
//     transiently (rate_limited, timeout, unreachable, upstream).
//   - An authoritative denial (not_found, auth_rejected, forbidden) deletes all
//     cached ref and tree rows for {host, owner, repo, scope}, writes the
//     1-minute negative memo, and is never masked by a stale entry.
func (s *Server) resolveGitHubSkill(ctx context.Context, rawURI, projectID string, refSHAMemo *ghSHAMemo) (*ResolvedSkillResponse, error) {
	return s.resolveGitHubSkillWithCredMemo(ctx, rawURI, projectID, refSHAMemo, nil)
}

func (s *Server) resolveGitHubSkillWithCredMemo(ctx context.Context, rawURI, projectID string, refSHAMemo *ghSHAMemo, credMemo *ghCredMemo) (*ResolvedSkillResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 1. Parse gh:// URI
	ghRef, err := agent.ParseGitHubSkillURI(rawURI)
	if err != nil {
		return nil, fmt.Errorf("invalid gh:// URI: %w", err)
	}

	// gh:// URIs with ?token= name a ProvisionCredentials secret that lives on
	// the broker, not the Hub.
	if ghRef.TokenSecretName != "" {
		return nil, fmt.Errorf("gh:// URI with ?token= must be resolved by the local resolver")
	}

	// Default an omitted ref to HEAD so gh://o/r/p and gh://o/r/p@HEAD share
	// one cache entry.
	if ghRef.Ref == "" {
		ghRef.Ref = "HEAD"
	}
	isBranchRef := !githubfetch.IsFullCommitSHA(ghRef.Ref)

	// 2. Resolve credential via unified Hub precedence (D2), memoized per
	// (projectID, lower(owner), lower(repo)) for this request. The caller has
	// already passed canUseProjectGitHubToken for non-empty projectID.
	var cred githubfetch.Credential
	if projectID != "" {
		credKey := projectID + "\x00" + strings.ToLower(ghRef.Owner) + "/" + strings.ToLower(ghRef.Repo)
		if cached, ok := credMemo.get(credKey); ok {
			cred = cached
		} else {
			cred = s.githubCredential(ctx, ghCredRequest{
				Scope:          store.TemplateScopeProject,
				ScopeID:        projectID,
				Owner:          ghRef.Owner,
				Repo:           ghRef.Repo,
				Quiet:          true,
				AppPermissions: &skillGitHubReadOnlyPermissions,
			})
			credMemo.set(credKey, cred)
		}
	} else {
		cred = githubfetch.Credential{
			Scope: githubfetch.AnonScope,
			Kind:  githubfetch.CredAnonymous,
		}
	}
	scope := cred.EffectiveScope()
	isAnon := scope == githubfetch.AnonScope

	// 3. Compute canonical cache and flight keys
	ep := s.skillGitHubEndpoint()
	apiHost := ep.APIHost()
	var refKey, flightKey string
	if isBranchRef {
		refKey = githubfetch.RefCacheKey(apiHost, ghRef.Owner, ghRef.Repo, ghRef.Ref, scope)
		flightKey = refKey + ":" + strings.Trim(ghRef.SkillPath, "/")
	} else {
		flightKey = githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, ghRef.Ref, ghRef.SkillPath, scope)
	}

	// 4. Check DB cache
	var freshRef *GitHubCacheEntry
	var staleRef *GitHubCacheEntry
	var staleTree *GitHubCacheEntry

	downloadToken := ""
	if cred.Kind == githubfetch.CredAppInstallation {
		downloadToken = cred.Token
	}

	if s.ghResolutionStore != nil {
		if !isBranchRef {
			entry, hit, gerr := s.ghResolutionStore.Get(ctx, flightKey)
			if gerr != nil {
				slog.WarnContext(ctx, "github_resolution_cache: cache lookup failed",
					"uri", rawURI, "error", gerr)
			} else if hit {
				slog.InfoContext(ctx, "github_resolution_cache: cache hit",
					"uri", rawURI, "commit_sha", safeShortSHA(entry.CommitSHA), "cache_hit", true)
				return buildResolvedSkillResponse(ghRef, entry, downloadToken), nil
			}
		} else {
			refEntry, refHit, gerr := s.ghResolutionStore.Get(ctx, refKey)
			if gerr != nil {
				slog.WarnContext(ctx, "github_resolution_cache: ref cache lookup failed",
					"uri", rawURI, "error", gerr)
			} else if refHit {
				freshRef = refEntry
				treeKey := githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, refEntry.CommitSHA, ghRef.SkillPath, scope)
				treeEntry, treeHit, terr := s.ghResolutionStore.Get(ctx, treeKey)
				if terr != nil {
					slog.WarnContext(ctx, "github_resolution_cache: tree cache lookup failed",
						"uri", rawURI, "error", terr)
				} else if treeHit {
					slog.InfoContext(ctx, "github_resolution_cache: cache hit",
						"uri", rawURI, "commit_sha", safeShortSHA(treeEntry.CommitSHA), "cache_hit", true)
					return buildResolvedSkillResponse(ghRef, treeEntry, downloadToken), nil
				}
			} else {
				sRef, sOK, sErr := s.ghResolutionStore.GetStale(ctx, refKey, agent.DefaultResolutionCacheTTL, agent.MaxResolutionStaleAge)
				if sErr != nil {
					slog.WarnContext(ctx, "github_resolution_cache: stale lookup failed",
						"uri", rawURI, "error", sErr)
				} else if sOK {
					staleRef = sRef
					treeKey := githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, sRef.CommitSHA, ghRef.SkillPath, scope)
					sTree, tOK, tErr := s.ghResolutionStore.GetTreeForStaleRef(ctx, treeKey)
					if tErr != nil {
						slog.WarnContext(ctx, "github_resolution_cache: stale tree lookup failed",
							"uri", rawURI, "error", tErr)
					} else if tOK {
						staleTree = sTree
						if isAnon {
							refreshStarted := false
							if _, cooling := s.githubCooldown().Active(scope); cooling {
								slog.WarnContext(ctx, "github_resolution_cache: serving stale entry, skipping refresh during a rate-limit cooldown",
									"uri", rawURI, "commit_sha", safeShortSHA(staleTree.CommitSHA))
							} else if s.recentGHRefreshFailure(flightKey) {
								slog.WarnContext(ctx, "github_resolution_cache: serving stale entry, skipping refresh after a recent failure",
									"uri", rawURI, "commit_sha", safeShortSHA(staleTree.CommitSHA))
							} else {
								slog.InfoContext(ctx, "github_resolution_cache: serving stale entry, refreshing in background",
									"uri", rawURI, "commit_sha", safeShortSHA(staleTree.CommitSHA))
								refreshStarted = true
								go s.refreshGitHubSkillInBackground(ep, flightKey, refKey, rawURI, ghRef, cred, staleRef, staleTree)
							}
							injectGHStaleServe(flightKey, refreshStarted)
							return buildResolvedSkillResponse(ghRef, staleTree, downloadToken), nil
						}
						// Non-anon scope: serve stale immediately when the
						// scope's cooldown is active or a recent transient
						// refresh failure is in backoff (avoiding paying a full
						// upstream timeout on every request while GitHub is
						// degraded). Authoritative denials never record a
						// refresh failure — they purge the cache instead.
						if _, cooling := s.githubCooldown().Active(scope); cooling {
							slog.WarnContext(ctx, "github_resolution_cache: serving stale entry during active cooldown",
								"uri", rawURI, "commit_sha", safeShortSHA(staleTree.CommitSHA))
							injectGHStaleServe(flightKey, false)
							return buildResolvedSkillResponse(ghRef, staleTree, downloadToken), nil
						}
						if s.recentGHRefreshFailure(flightKey) {
							slog.WarnContext(ctx, "github_resolution_cache: serving stale entry during transient refresh-failure backoff",
								"uri", rawURI, "commit_sha", safeShortSHA(staleTree.CommitSHA))
							injectGHStaleServe(flightKey, false)
							return buildResolvedSkillResponse(ghRef, staleTree, downloadToken), nil
						}
					}
				}
			}
		}
	}

	// Negative memo (not_found / auth_rejected / forbidden) within FailureMemoTTL.
	if ferr := s.ghFailures.Recent(flightKey); ferr != nil {
		slog.DebugContext(ctx, "github_resolution_cache: returning remembered denial", "uri", rawURI)
		return nil, ferr
	}

	// A miss during a rate-limit cooldown fails immediately without starting a flight.
	if retryAt, cooling := s.githubCooldown().Active(scope); cooling {
		ra := retryAt.Sub(s.ghClockNow())
		if ra < 0 {
			ra = 0
		}
		return nil, &githubfetch.Error{
			Kind:       githubfetch.KindRateLimited,
			Status:     http.StatusTooManyRequests,
			RetryAfter: ra,
			Repo:       ghRef.Owner + "/" + ghRef.Repo,
			CredKind:   cred.Kind,
			Hint:       cred.Hint,
		}
	}

	// 5. Cache miss (or credentialed stale revalidation): coalesce concurrent
	// callers for flightKey into one detached fetch.
	injectGHFlightJoin(flightKey)
	resultCh := s.ghResolveFlight.DoChan(flightKey, func() (result interface{}, ferr error) {
		defer func() {
			if r := recover(); r != nil {
				ferr = fmt.Errorf("panic during GitHub skill resolution for %s: %v", flightKey, r)
			}
		}()

		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hubGitHubRefreshTimeout)
		defer cancel()

		// Re-check fresh cache in case a concurrent flight just landed.
		flightFreshRef := freshRef
		if s.ghResolutionStore != nil {
			if !isBranchRef {
				if entry, hit, gerr := s.ghResolutionStore.Get(flightCtx, flightKey); gerr == nil && hit {
					return entry, nil
				}
			} else if rEntry, rHit, gerr := s.ghResolutionStore.Get(flightCtx, refKey); gerr == nil && rHit {
				tKey := githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, rEntry.CommitSHA, ghRef.SkillPath, scope)
				if tEntry, tHit, terr := s.ghResolutionStore.Get(flightCtx, tKey); terr == nil && tHit {
					return tEntry, nil
				}
				flightFreshRef = rEntry
			}
		}
		if ferr := s.ghFailures.Recent(flightKey); ferr != nil {
			slog.DebugContext(ctx, "github_resolution_cache: returning remembered denial", "uri", rawURI)
			return nil, ferr
		}

		entry, ferr := s.fetchAndCacheGitHubSkill(flightCtx, ep, flightKey, refKey, rawURI, ghRef, cred, isBranchRef, flightFreshRef, staleRef, staleTree, refSHAMemo)
		if ferr != nil {
			// Record transient failures inside the detached flight so backoff
			// takes effect even if the initiating caller stopped waiting early
			// (e.g. hubGHStaleRevalidateTimeout fired or caller ctx ended).
			// Authoritative denials are never transient and purge the cache
			// instead of recording a refresh failure.
			if isGHTransientFailure(ferr) {
				s.recordGHRefreshFailure(flightKey)
			}
			return nil, ferr
		}
		s.clearGHRefreshFailure(flightKey)
		return entry, nil
	})

	var staleTimer <-chan time.Time
	if staleTree != nil {
		t := time.NewTimer(hubGHStaleRevalidateTimeout)
		defer t.Stop()
		staleTimer = t.C
	}

	select {
	case res := <-resultCh:
		if res.Err != nil {
			if staleTree != nil && isGHTransientFailure(res.Err) {
				slog.WarnContext(ctx, "github_resolution_cache: refresh failed transiently; serving stale entry",
					"uri", rawURI, "commit_sha", safeShortSHA(staleTree.CommitSHA), "error", res.Err)
				injectGHStaleServe(flightKey, false)
				return buildResolvedSkillResponse(ghRef, staleTree, downloadToken), nil
			}
			return nil, res.Err
		}
		// Build the response from this caller's own ghRef, not whichever
		// caller happened to lead or already have the result cached: the
		// flight (and the cache re-check above) share one *GitHubCacheEntry
		// across every caller keyed to flightKey, but two callers can reach
		// the same flightKey with different raw URI text (a bare ref vs
		// "@HEAD", or different owner/repo letter case) — the entry carries
		// none of that, so each caller supplies it from its own parsed ghRef.
		return buildResolvedSkillResponse(ghRef, res.Val.(*GitHubCacheEntry), downloadToken), nil
	case <-staleTimer:
		slog.WarnContext(ctx, "github_resolution_cache: credentialed revalidation timed out; serving stale entry while flight completes",
			"uri", rawURI, "commit_sha", safeShortSHA(staleTree.CommitSHA))
		injectGHStaleServe(flightKey, true)
		return buildResolvedSkillResponse(ghRef, staleTree, downloadToken), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// refreshGitHubSkillInBackground re-resolves ghRef and updates the cache on
// behalf of an anonymous caller that was already served a stale value.
func (s *Server) refreshGitHubSkillInBackground(
	ep githubfetch.Endpoint,
	flightKey, refKey, rawURI string,
	ghRef *agent.GitHubSkillRef,
	cred githubfetch.Credential,
	staleRef, staleTree *GitHubCacheEntry,
) {
	injectGHFlightJoin(flightKey)
	ctx, cancel := context.WithTimeout(context.Background(), hubGitHubRefreshTimeout)
	defer cancel()

	scope := cred.EffectiveScope()
	apiHost := ep.APIHost()

	resultCh := s.ghResolveFlight.DoChan(flightKey, func() (result interface{}, ferr error) {
		defer func() {
			if r := recover(); r != nil {
				ferr = fmt.Errorf("panic during background GitHub skill refresh for %s: %v", flightKey, r)
			}
		}()
		if s.ghResolutionStore != nil {
			if rEntry, rHit, gerr := s.ghResolutionStore.Get(ctx, refKey); gerr == nil && rHit {
				tKey := githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, rEntry.CommitSHA, ghRef.SkillPath, scope)
				if tEntry, tHit, terr := s.ghResolutionStore.Get(ctx, tKey); terr == nil && tHit {
					return tEntry, nil
				}
			}
		}
		sem := s.ghRefreshSemaphore()
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if s.ghResolutionStore != nil {
			if rEntry, rHit, gerr := s.ghResolutionStore.Get(ctx, refKey); gerr == nil && rHit {
				tKey := githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, rEntry.CommitSHA, ghRef.SkillPath, scope)
				if tEntry, tHit, terr := s.ghResolutionStore.Get(ctx, tKey); terr == nil && tHit {
					return tEntry, nil
				}
			}
		}
		if retryAt, cooling := s.githubCooldown().Active(scope); cooling {
			ra := retryAt.Sub(s.ghClockNow())
			if ra < 0 {
				ra = 0
			}
			return nil, &githubfetch.Error{
				Kind:       githubfetch.KindRateLimited,
				Status:     http.StatusTooManyRequests,
				RetryAfter: ra,
				Repo:       ghRef.Owner + "/" + ghRef.Repo,
				CredKind:   cred.Kind,
				Hint:       cred.Hint,
			}
		}
		return s.fetchAndCacheGitHubSkill(ctx, ep, flightKey, refKey, rawURI, ghRef, cred, true, nil, staleRef, staleTree, nil)
	})

	select {
	case res := <-resultCh:
		if res.Err != nil {
			s.recordGHRefreshFailure(flightKey)
			slog.WarnContext(ctx, "github_resolution_cache: background refresh failed",
				"uri", rawURI, "error", res.Err)
		} else {
			s.clearGHRefreshFailure(flightKey)
		}
	case <-ctx.Done():
		s.recordGHRefreshFailure(flightKey)
		slog.WarnContext(ctx, "github_resolution_cache: background refresh timed out",
			"uri", rawURI, "error", ctx.Err())
	}
}

// fetchAndCacheGitHubSkill resolves ghRef via githubfetch.Client (conditional
// ResolveRef + ListTree), writes ref and tree entries into github_resolution_cache,
// and returns the tree entry. On authoritative denial (not_found, auth_rejected,
// forbidden), it purges all cached rows for {host, owner, repo, scope} and
// writes the 1-minute negative memo.
func (s *Server) fetchAndCacheGitHubSkill(
	ctx context.Context,
	ep githubfetch.Endpoint,
	flightKey, refKey, rawURI string,
	ghRef *agent.GitHubSkillRef,
	cred githubfetch.Credential,
	isBranchRef bool,
	freshRef, staleRef, staleTree *GitHubCacheEntry,
	refSHAMemo *ghSHAMemo,
) (*GitHubCacheEntry, error) {
	scope := cred.EffectiveScope()
	apiHost := ep.APIHost()
	src := githubfetch.Source{
		Owner: ghRef.Owner,
		Repo:  ghRef.Repo,
		Ref:   ghRef.Ref,
		Path:  ghRef.SkillPath,
	}
	client := s.getSkillGitHubClient(ep)

	refreshTreeExpiry := func(tKey string, entry *GitHubCacheEntry) *GitHubCacheEntry {
		if scope == githubfetch.AnonScope || s.ghResolutionStore == nil {
			return entry
		}
		updated := *entry
		updated.ExpiresAt = s.ghClockNow().Add(agent.JitteredTTL(agent.DefaultResolutionCacheTTL, s.ghRand()))
		if err := s.ghResolutionStore.Put(ctx, tKey, updated); err != nil {
			slog.WarnContext(ctx, "github_resolution_cache: failed to extend tree entry",
				"uri", rawURI, "error", err)
			return entry
		}
		return &updated
	}

	var commitSHA string
	refProven := false
	if !isBranchRef {
		commitSHA = ghRef.Ref
	} else if freshRef != nil {
		commitSHA = freshRef.CommitSHA
	} else if memoSHA, seen := refSHAMemo.get(refKey); seen {
		commitSHA = memoSHA
	} else {
		prevETag := ""
		if staleRef != nil {
			prevETag = staleRef.ETag
		}
		resolved, _, notMod, err := client.ResolveRefConditional(ctx, src, cred, prevETag)
		if err != nil {
			if isGHAuthoritativeDenial(err) {
				s.deleteGHRepoScopeCache(ctx, ep, ghRef.Owner, ghRef.Repo, scope)
				s.rememberGHDenial(flightKey, err)
			}
			return nil, err
		}
		refProven = true
		if notMod && staleRef != nil {
			commitSHA = staleRef.CommitSHA
			refEntry := GitHubCacheEntry{
				CommitSHA:   commitSHA,
				BundleHash:  "ref:" + commitSHA,
				TokenScope:  scope,
				ETag:        resolved.ETag,
				ExpiresAt:   s.ghClockNow().Add(agent.JitteredTTL(agent.DefaultResolutionCacheTTL, s.ghRand())),
				OriginalURI: rawURI,
			}
			s.ghFailures.Clear(flightKey)
			if s.ghResolutionStore != nil {
				if err := s.ghResolutionStore.Put(ctx, refKey, refEntry); err != nil {
					slog.WarnContext(ctx, "github_resolution_cache: failed to extend ref entry after 304",
						"uri", rawURI, "error", err)
				}
			}
			if staleTree != nil {
				tKey := githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, commitSHA, ghRef.SkillPath, scope)
				staleTree = refreshTreeExpiry(tKey, staleTree)
				slog.InfoContext(ctx, "github_resolution_cache: ref 304 not modified, extended TTL without re-listing",
					"uri", rawURI, "commit_sha", safeShortSHA(commitSHA))
				return staleTree, nil
			}
		} else {
			commitSHA = resolved.CommitSHA
			refSHAMemo.set(refKey, commitSHA)
			refEntry := GitHubCacheEntry{
				CommitSHA:   commitSHA,
				BundleHash:  "ref:" + commitSHA,
				TokenScope:  scope,
				ETag:        resolved.ETag,
				ExpiresAt:   s.ghClockNow().Add(agent.JitteredTTL(agent.DefaultResolutionCacheTTL, s.ghRand())),
				OriginalURI: rawURI,
			}
			if s.ghResolutionStore != nil {
				if err := s.ghResolutionStore.Put(ctx, refKey, refEntry); err != nil {
					slog.WarnContext(ctx, "github_resolution_cache: failed to store ref entry",
						"uri", rawURI, "error", err)
				}
			}
		}
	}

	treeKey := githubfetch.TreeCacheKey(apiHost, ghRef.Owner, ghRef.Repo, commitSHA, ghRef.SkillPath, scope)
	if staleTree != nil && staleTree.CommitSHA == commitSHA {
		s.ghFailures.Clear(flightKey)
		if refProven {
			staleTree = refreshTreeExpiry(treeKey, staleTree)
		}
		return staleTree, nil
	}
	if s.ghResolutionStore != nil {
		if existing, hit, gerr := s.ghResolutionStore.Get(ctx, treeKey); gerr == nil && hit {
			s.ghFailures.Clear(flightKey)
			if refProven {
				existing = refreshTreeExpiry(treeKey, existing)
			}
			return existing, nil
		}
	}

	tree, err := client.ListTree(ctx, src, commitSHA, cred, githubfetch.SkillLimits())
	if err != nil {
		if isGHAuthoritativeDenial(err) {
			var ghe *githubfetch.Error
			if refProven && errors.As(err, &ghe) && ghe.Kind == githubfetch.KindNotFound {
				// A live ResolveRefConditional call succeeded in this flight,
				// proving repo and credential access right now; a 404 from
				// ListTree therefore means this skill path does not exist at
				// commitSHA. Delete only this path's tree row so a missing path
				// does not evict other skills/refs in the shared repo scope.
				if s.ghResolutionStore != nil {
					if derr := s.ghResolutionStore.Delete(ctx, treeKey); derr != nil {
						slog.WarnContext(ctx, "github_resolution_cache: failed to delete tree entry on path not_found",
							"uri", rawURI, "error", derr)
					}
				}
			} else {
				// Purge the entire {host, owner, repo, scope} cache whenever
				// access was not freshly proven in this flight (fresh ref cache
				// hit, refSHAMemo hit, or SHA-pinned !isBranchRef) or on
				// auth_rejected/forbidden. For SHA-pinned resolves, a ListTree
				// 404 purges the repo scope (including anon rows) by design,
				// since ListTree alone cannot distinguish a missing SHA from
				// credential revocation.
				s.deleteGHRepoScopeCache(ctx, ep, ghRef.Owner, ghRef.Repo, scope)
			}
			s.rememberGHDenial(flightKey, err)
		}
		return nil, err
	}

	if len(tree.Files) == 0 {
		return nil, fmt.Errorf("no files found at %s in %s/%s", ghRef.SkillPath, ghRef.Owner, ghRef.Repo)
	}

	fileEntries := make([]GitHubFileEntry, 0, len(tree.Files))
	for _, f := range tree.Files {
		fileEntries = append(fileEntries, GitHubFileEntry{
			Path: f.Path,
			URL:  ep.RawURL(src, commitSHA, f.Path),
			Hash: f.BlobSHA,
			Size: f.Size,
			Mode: fmt.Sprintf("%04o", f.Mode),
		})
	}

	bundleHash := computeBundleHash(fileEntries)

	// For non-anon scopes, cap every tree row (whether written by a branch
	// resolve or a SHA-pinned resolve) to DefaultResolutionCacheTTL (30m) so a
	// later SHA-pinned resolve reusing that TreeCacheKey can never serve stale
	// metadata past one fresh TTL after credential revocation. Anon keeps 24h.
	treeTTL := agent.DefaultSHAResolutionCacheTTL
	if scope != githubfetch.AnonScope {
		treeTTL = agent.DefaultResolutionCacheTTL
	}

	entry := GitHubCacheEntry{
		CommitSHA:   commitSHA,
		FileEntries: fileEntries,
		BundleHash:  bundleHash,
		TokenScope:  scope,
		ExpiresAt:   s.ghClockNow().Add(agent.JitteredTTL(treeTTL, s.ghRand())),
		OriginalURI: rawURI,
	}

	s.ghFailures.Clear(flightKey)
	if s.ghResolutionStore != nil {
		if err := s.ghResolutionStore.Put(ctx, treeKey, entry); err != nil {
			slog.WarnContext(ctx, "github_resolution_cache: failed to store tree entry",
				"uri", rawURI, "error", err)
		}
	}

	slog.InfoContext(ctx, "github_resolution_cache: cache miss, resolved via API",
		"uri", rawURI, "commit_sha", safeShortSHA(commitSHA), "files", len(fileEntries), "cache_hit", false)

	return &entry, nil
}

// buildResolvedSkillResponse constructs a ResolvedSkillResponse from a cache entry.
//
// Hash values here are git blob object IDs (bare 40-char hex), not the
// "sha256:<hex>" digests used elsewhere in the API: the Hub resolves gh://
// skills from GitHub metadata alone and never downloads the bytes, so a
// sha256 digest is not available to it. The client recognises the format and
// verifies accordingly (see hashFileAs in pkg/agent/skill_resolver.go).
// ContentHash is likewise a digest over the git blob IDs.
func buildResolvedSkillResponse(ghRef *agent.GitHubSkillRef, entry *GitHubCacheEntry, downloadToken string) *ResolvedSkillResponse {
	files := make([]DownloadURLInfo, len(entry.FileEntries))
	for i, f := range entry.FileEntries {
		files[i] = DownloadURLInfo{
			Path: f.Path,
			URL:  f.URL,
			Size: f.Size,
			Hash: f.Hash,
			// Empty only on a row written before modes were recorded,
			// which is served only when a re-resolve failed; the client
			// writes such a file 0644, as before (ptone/scion#5781).
			Mode: f.Mode,
		}
	}

	return &ResolvedSkillResponse{
		URI:             ghRef.Raw,
		Name:            ghRef.SkillName,
		ResolvedVersion: safeShortSHA(entry.CommitSHA), // Short SHA for display
		ContentHash:     entry.BundleHash,
		Files:           files,
		DownloadToken:   downloadToken,
	}
}

// safeShortSHA returns the first 12 characters of a SHA string, or the full
// string if it is shorter than 12 characters (e.g. in tests or after DB
// corruption). Prevents a panic on bare sha[:12] slicing.
func safeShortSHA(sha string) string {
	if len(sha) < 12 {
		return sha
	}
	return sha[:12]
}

// computeBundleHash computes the content hash from file entries. Like every
// skill hash it is transfer.ComputeContentHashV1 (see computeSkillContentHash).
func computeBundleHash(files []GitHubFileEntry) string {
	// Convert to transfer.FileInfo for hash computation
	fileInfos := make([]transfer.FileInfo, len(files))
	for i, f := range files {
		fileInfos[i] = transfer.FileInfo{
			Path: f.Path,
			Hash: f.Hash,
		}
	}
	return transfer.ComputeContentHashV1(fileInfos)
}

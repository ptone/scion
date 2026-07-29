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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hub/imagecheck"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

type imageManager interface {
	imagecheck.LocalImageExister
	PullImage(ctx context.Context, image string) error
	RemoveImage(ctx context.Context, image string) error
}

var nodeBoundProfileTypes = map[string]bool{
	"docker": true,
	"podman": true,
	"apple":  true,
}

func isNodeBoundBroker(broker *store.RuntimeBroker) bool {
	for _, p := range broker.Profiles {
		if nodeBoundProfileTypes[p.Type] {
			return true
		}
	}
	return false
}

type AggregatedImageStatusResponse struct {
	Image        string               `json:"image"`
	Registry     *RegistryImageStatus `json:"registry"`
	Brokers      []BrokerImageEntry   `json:"brokers"`
	ProxyBrokers []ProxyBrokerEntry   `json:"proxy_brokers,omitempty"`
}

type RegistryImageStatus struct {
	Image     string    `json:"image"`
	Exists    bool      `json:"exists"`
	Hash      string    `json:"hash,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

type BrokerImageEntry struct {
	BrokerID         string                  `json:"broker_id"`
	BrokerName       string                  `json:"broker_name"`
	Reachable        bool                    `json:"reachable"`
	Unsupported      bool                    `json:"unsupported,omitempty"`
	LocalShort       *BrokerImageEntityState `json:"local_short,omitempty"`
	LocalLong        *BrokerImageEntityState `json:"local_long,omitempty"`
	NewerInRegistry  bool                    `json:"newer_in_registry,omitempty"`
	ResolvedImage    string                  `json:"resolved_image,omitempty"`
	ResolutionSource string                  `json:"resolution_source,omitempty"`
}

type ProxyBrokerEntry struct {
	BrokerID   string `json:"broker_id"`
	BrokerName string `json:"broker_name"`
	Runtime    string `json:"runtime"`
}

func (s *Server) checkRegistryImage(ctx context.Context, longImage string) RegistryImageStatus {
	now := time.Now()
	if longImage == "" {
		return RegistryImageStatus{CheckedAt: now}
	}
	if imagecheck.IsBareImageName(longImage) {
		return RegistryImageStatus{
			Image:     longImage,
			CheckedAt: now,
		}
	}
	result := s.imageChecker.CheckRemoteOnly(ctx, longImage)
	return RegistryImageStatus{
		Image:     longImage,
		Exists:    result.Status == "valid",
		Hash:      result.Hash,
		CheckedAt: result.CheckedAt,
	}
}

// CreateHarnessConfigRequest is the request body for creating a harness config.
type CreateHarnessConfigRequest struct {
	Name        string                   `json:"name"`
	Slug        string                   `json:"slug,omitempty"`
	DisplayName string                   `json:"displayName,omitempty"`
	Description string                   `json:"description,omitempty"`
	Harness     string                   `json:"harness"`
	Scope       string                   `json:"scope"`
	ScopeID     string                   `json:"scopeId,omitempty"`
	Config      *store.HarnessConfigData `json:"config,omitempty"`
	Visibility  string                   `json:"visibility,omitempty"`
	Files       []FileUploadRequest      `json:"files,omitempty"`
}

// CreateHarnessConfigResponse is the response for harness config creation.
type CreateHarnessConfigResponse struct {
	HarnessConfig *store.HarnessConfig `json:"harnessConfig"`
	UploadURLs    []UploadURLInfo      `json:"uploadUrls,omitempty"`
	ManifestURL   string               `json:"manifestUrl,omitempty"`
}

// HarnessConfigManifest is the manifest of uploaded harness config files.
type HarnessConfigManifest struct {
	Version string               `json:"version"`
	Harness string               `json:"harness,omitempty"`
	Files   []store.TemplateFile `json:"files"`
}

// handleHarnessConfigs handles the /api/v1/harness-configs endpoint.
func (s *Server) handleHarnessConfigs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listHarnessConfigs(w, r)
	case http.MethodPost:
		s.createHarnessConfig(w, r)
	default:
		MethodNotAllowed(w)
	}
}

// listHarnessConfigs lists harness configs with filtering.
func (s *Server) listHarnessConfigs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	filter := store.HarnessConfigFilter{
		Name:        query.Get("name"),
		Scope:       query.Get("scope"),
		ScopeID:     query.Get("scopeId"),
		ProjectID:   query.Get("projectId"),
		Harness:     query.Get("harness"),
		Status:      query.Get("status"),
		ImageStatus: query.Get("image_status"),
		Search:      query.Get("search"),
	}

	// Default to active harness configs only
	if filter.Status == "" {
		filter.Status = store.HarnessConfigStatusActive
	}

	limit := 50
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	result, err := s.store.ListHarnessConfigs(ctx, filter, store.ListOptions{
		Limit:  limit,
		Cursor: query.Get("cursor"),
	})
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Compute per-item and scope capabilities (mirrors listTemplatesV2).
	identity := GetIdentityFromContext(ctx)
	items := make([]HarnessConfigWithCapabilities, len(result.Items))
	if identity != nil {
		resources := make([]Resource, len(result.Items))
		for i := range result.Items {
			resources[i] = harnessConfigResource(&result.Items[i])
		}
		caps := s.authzService.ComputeCapabilitiesBatch(ctx, identity, resources, "harness_config")
		for i := range result.Items {
			items[i] = HarnessConfigWithCapabilities{HarnessConfig: result.Items[i], Cap: caps[i]}
		}
	} else {
		for i := range result.Items {
			items[i] = HarnessConfigWithCapabilities{HarnessConfig: result.Items[i]}
		}
	}

	var scopeCap *Capabilities
	if identity != nil {
		scopeCap = s.authzService.ComputeScopeCapabilities(ctx, identity, "", "", "harness_config")
	}

	writeJSON(w, http.StatusOK, ListHarnessConfigsResponse{
		HarnessConfigs: items,
		NextCursor:     result.NextCursor,
		TotalCount:     result.TotalCount,
		Capabilities:   scopeCap,
	})
}

// harnessConfigUserScope describes, for a user-scoped harness config, which
// user it must belong to for the caller to be allowed to act on it.
//
// The two fields exist because "no owner recorded" has to mean opposite things
// on the two kinds of call, and collapsing them into one empty-string rule
// would be a fail-open:
//
//   - On an EXISTING record, ownerID is the record's OwnerID and an empty value
//     means the record names no owner, so no caller can prove they are it, so
//     everyone is denied. This is what deleteHarnessConfig has always done.
//   - On a CREATE, there is no record yet; ownerID is the requested target user
//     and an empty value means "for myself", which is allowed. Set isNew.
type harnessConfigUserScope struct {
	ownerID string
	isNew   bool
}

// authorizeHarnessConfigScope is the scope-aware gate for the harness-config
// write endpoints. It is the switch that deleteHarnessConfig has carried since
// it was written, lifted out so the other write endpoints share it rather than
// each growing their own copy.
//
// They had grown their own copies, and the copies had diverged, which is the
// reason this is a function and not a fourth transcription:
//
//   - createHarnessConfig had no switch at all. POST /api/v1/harness-configs
//     performed no authorization, so any authenticated caller could create a
//     harness config at global scope with an attacker-chosen Config.Image, and
//     broker nodes pull that image on the next agent start.
//   - updateHarnessConfig and patchHarnessConfig had no switch either, and
//     update rewrites Config.Image on an existing record — the same effect as
//     the create hole, reached by a different verb.
//   - handleHarnessConfigClone had a switch with a global arm and a project arm
//     and nothing else. A clone whose destination scope was "user", or was any
//     string the switch did not name, fell straight out of the bottom and was
//     created unauthorized. A switch over a request-controlled string is
//     exhaustive over the values its author had in mind and silent about every
//     other, and silence here rendered as permission.
//
// Hence the default arm, which denies. Adding a scope constant without adding
// an arm here now refuses that scope instead of admitting it, and the person
// adding it has to come here and say what it means.
//
// This gate authorizes WHO may write a harness config. It says nothing about
// WHAT the config contains: the image reference, its registry, and the branch
// it names are not validated here and are a separate layer (wave-2). Keep the
// two orthogonal — a caller who is allowed to write is not thereby vouched for.
//
// verb is the lowercase action word used in the denial messages ("create",
// "update", "delete"), so a refusal names the thing that was refused.
func (s *Server) authorizeHarnessConfigScope(w http.ResponseWriter, r *http.Request,
	scope, scopeID string, userScope harnessConfigUserScope, action Action, verb string) bool {
	ctx := r.Context()

	switch scope {
	case store.HarnessConfigScopeGlobal:
		userIdent := GetUserIdentityFromContext(ctx)
		if userIdent == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
			return false
		}
		decision := s.authzService.CheckAccess(ctx, userIdent, Resource{Type: "harness_config"}, action)
		if !decision.Allowed {
			writeError(w, http.StatusForbidden, ErrCodeForbidden,
				"You do not have permission to "+verb+" global resources", nil)
			return false
		}
		return true

	case store.HarnessConfigScopeProject:
		if agentIdent := GetAgentIdentityFromContext(ctx); agentIdent != nil {
			if !agentIdent.HasScope(ScopeAgentCreate) {
				writeError(w, http.StatusForbidden, ErrCodeForbidden, "Missing required scope", nil)
				return false
			}
			if scopeID != agentIdent.ProjectID() {
				writeError(w, http.StatusForbidden, ErrCodeForbidden,
					"Agents can only manage resources within their own project", nil)
				return false
			}
			return true
		}
		if userIdent := GetUserIdentityFromContext(ctx); userIdent != nil {
			decision := s.authzService.CheckAccess(ctx, userIdent, Resource{
				Type: "harness_config", ParentType: "project", ParentID: scopeID,
			}, action)
			if !decision.Allowed {
				writeError(w, http.StatusForbidden, ErrCodeForbidden,
					"You do not have permission to "+verb+" resources in this project", nil)
				return false
			}
			return true
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
		return false

	case store.HarnessConfigScopeUser:
		// DELIBERATE ABSENCE, and it is load-bearing. Read the next paragraph
		// carefully, because an earlier version of this note got it wrong in a
		// way that would have made a reader relax.
		//
		// The store persists OwnerID perfectly well: written and read back
		// unchanged, measured. What is missing is any code path that SETS it.
		// createHarnessConfig builds the record field by field from a typed
		// request and omits OwnerID; clone does the same. So on every record
		// that exists today ownerID is "", the comparison below never matches,
		// and this arm denies update, patch and delete for everyone.
		//
		// It is dead by the contingent absence of an assignment, not "by
		// construction" — which is precisely why it needs a note rather than a
		// deletion. It fails closed today, so it is preserved rather than
		// "fixed" here. Whether OwnerID should be populated is a pending
		// decision held outside this change.
		//
		// One assignment did exist and was removed: updateHarnessConfig wrote
		// the whole record from the request body, so a PUT carrying ownerId
		// stored it. Measured on the pre-fix shape — an in-project agent PUT
		// scope "user" with an attacker-chosen ownerId and the store took all
		// of it. What stops that now is a single line, and it is not near here:
		//
		//	updateHarnessConfig, `hc.OwnerID = existing.OwnerID`
		//
		// This arm's safety depends on that line. If it goes, this arm becomes
		// live and attacker-controlled in the same edit, and the edit will look
		// like housekeeping.
		//
		// If you are the one who makes OwnerID populated: this arm and the one
		// in handleHarnessConfigReimport come alive in the same commit as your
		// change, and so do the update, patch and delete paths that reach this
		// one. Re-review them together. Note what "alive" means here, because
		// it is not only an allow — measured with OwnerID populated, the
		// recorded owner is admitted and a hub admin is REFUSED on the same
		// record, since this arm compares identity and never consults role.
		// A single line elsewhere turns four currently-unreachable paths into
		// reachable ones at once, and none of them will announce themselves.
		userIdent := GetUserIdentityFromContext(ctx)
		if userIdent == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
			return false
		}
		if userScope.isNew && userScope.ownerID == "" {
			// Creating a user-scoped config with no target named: it is the
			// caller's own.
			return true
		}
		if userScope.ownerID != userIdent.ID() {
			writeError(w, http.StatusForbidden, ErrCodeForbidden,
				"You do not have permission to "+verb+" another user's harness config", nil)
			return false
		}
		return true

	default:
		writeError(w, http.StatusForbidden, ErrCodeForbidden,
			capitalizeFirst(verb)+" is not supported for this resource scope", nil)
		return false
	}
}

// capitalizeFirst uppercases the first byte of an ASCII word. Used only to
// render the verb passed to authorizeHarnessConfigScope at the start of a
// sentence, so that the caller supplies one verb rather than two spellings of
// it that could disagree.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// createHarnessConfig creates a harness config with optional file upload URLs.
func (s *Server) createHarnessConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req CreateHarnessConfigRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		ValidationError(w, "name is required", nil)
		return
	}
	if req.Harness == "" {
		ValidationError(w, "harness is required", nil)
		return
	}

	slug := req.Slug
	if slug == "" {
		slug = api.Slugify(req.Name)
	}

	hc := &store.HarnessConfig{
		ID:          api.NewUUID(),
		Name:        req.Name,
		Slug:        slug,
		DisplayName: req.DisplayName,
		Description: req.Description,
		Harness:     req.Harness,
		Config:      req.Config,
		Scope:       req.Scope,
		ScopeID:     req.ScopeID,
		Visibility:  req.Visibility,
		Status:      store.HarnessConfigStatusPending,
	}

	if hc.Scope == "" {
		hc.Scope = store.HarnessConfigScopeGlobal
	}
	if hc.Visibility == "" {
		hc.Visibility = store.VisibilityPrivate
	}

	// Gate after the scope defaulting above and before anything is written.
	// After, because an omitted scope means global and global is the most
	// privileged of the three — authorizing the empty string the caller sent
	// rather than the global scope it resolves to would gate the request that
	// was typed instead of the one that will execute. Before, because this is
	// the last point at which nothing has happened yet.
	if !s.authorizeHarnessConfigScope(w, r, hc.Scope, hc.ScopeID,
		harnessConfigUserScope{ownerID: hc.ScopeID, isNew: true}, ActionCreate, "create") {
		return
	}

	// If no files provided, mark as active immediately
	if len(req.Files) == 0 {
		hc.Status = store.HarnessConfigStatusActive
	}

	// Generate storage path and URI
	storagePath := storage.HarnessConfigStoragePath(s.HubID(), hc.Scope, hc.ScopeID, hc.Slug)
	hc.StoragePath = storagePath

	stor := s.GetStorage()
	if stor != nil {
		hc.StorageBucket = stor.Bucket()
		hc.StorageURI = storage.HarnessConfigStorageURI(s.HubID(), stor.Bucket(), hc.Scope, hc.ScopeID, hc.Slug)
	}

	if err := s.store.CreateHarnessConfig(ctx, hc); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	response := CreateHarnessConfigResponse{
		HarnessConfig: hc,
	}

	// Generate upload URLs if files were specified and storage is available
	if len(req.Files) > 0 && stor != nil {
		uploadURLs, manifestURL, err := generateUploadURLs(ctx, stor, storagePath, req.Files)
		if err == nil || len(uploadURLs) > 0 {
			response.UploadURLs = uploadURLs
			response.ManifestURL = manifestURL
		}
	}

	writeJSON(w, http.StatusCreated, response)
}

// handleHarnessConfigByID handles individual harness config operations.
func (s *Server) handleHarnessConfigByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/harness-configs/")
	if path == "" {
		NotFound(w, "HarnessConfig")
		return
	}

	parts := strings.SplitN(path, "/", 2)
	hcID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "":
		s.handleHarnessConfigCRUD(w, r, hcID)
	case "upload":
		s.handleHarnessConfigUpload(w, r, hcID)
	case "finalize":
		s.handleHarnessConfigFinalize(w, r, hcID)
	case "download":
		s.handleHarnessConfigDownload(w, r, hcID)
	case "clone":
		s.handleHarnessConfigClone(w, r, hcID)
	case "validate":
		s.handleHarnessConfigValidate(w, r, hcID)
	case "check-image":
		s.handleHarnessConfigCheckImage(w, r, hcID)
	case "image-status":
		s.handleHarnessConfigImageStatus(w, r, hcID)
	case "local-image":
		s.handleHarnessConfigDeleteLocalImage(w, r, hcID)
	case "pull-image":
		s.handleHarnessConfigPullImage(w, r, hcID)
	case "reimport":
		s.handleHarnessConfigReimport(w, r, hcID)
	case "files":
		s.handleHarnessConfigFiles(w, r, hcID, "")
	default:
		if strings.HasPrefix(action, "files/") {
			filePath := strings.TrimPrefix(action, "files/")
			s.handleHarnessConfigFiles(w, r, hcID, filePath)
			return
		}
		NotFound(w, "HarnessConfig action")
	}
}

// handleHarnessConfigCRUD handles basic harness config CRUD operations.
func (s *Server) handleHarnessConfigCRUD(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		s.getHarnessConfig(w, r, id)
	case http.MethodPut:
		s.updateHarnessConfig(w, r, id)
	case http.MethodPatch:
		s.patchHarnessConfig(w, r, id)
	case http.MethodDelete:
		s.deleteHarnessConfig(w, r, id)
	default:
		MethodNotAllowed(w)
	}
}

func (s *Server) getHarnessConfig(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	resp := HarnessConfigWithCapabilities{HarnessConfig: *hc}
	if identity := GetIdentityFromContext(ctx); identity != nil {
		resp.Cap = s.authzService.ComputeCapabilities(ctx, identity, harnessConfigResource(hc))
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) harnessConfigImage(hc *store.HarnessConfig) string {
	if hc.Config != nil {
		return hc.Config.Image
	}
	return ""
}

func extractImageFromStorage(ctx context.Context, stor storage.Storage, storagePath string) string {
	objectPath := storagePath + "/config.yaml"
	reader, _, err := stor.Download(ctx, objectPath)
	if err != nil || reader == nil {
		return ""
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return ""
	}
	entry, err := config.ParseHarnessConfigYAML(data)
	if err != nil {
		return ""
	}
	return entry.Image
}

func (s *Server) updateHarnessConfig(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	existing, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Fetch, then authorize on the STORED record's scope — not on any scope the
	// body carries. This handler unmarshals the whole record from the request,
	// including Scope, ScopeID and Config.Image; authorizing on the submitted
	// scope would let a caller name a scope they are allowed to write and then
	// rewrite a record in one they are not.
	//
	// It has to be gated at all because an update rewrites Config.Image on an
	// existing config, which is the same attacker-image effect that gating
	// create closes — reached by PUT instead of POST. Gating create alone would
	// have moved the hole rather than shut it.
	if !s.authorizeHarnessConfigScope(w, r, existing.Scope, existing.ScopeID,
		harnessConfigUserScope{ownerID: existing.OwnerID}, ActionUpdate, "update") {
		return
	}

	var hc store.HarnessConfig
	if err := readJSON(r, &hc); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	// Preserve immutable fields
	hc.ID = existing.ID
	hc.Created = existing.Created
	hc.CreatedBy = existing.CreatedBy

	// Scope, ScopeID and OwnerID are preserved from the stored record, and that
	// is an authorization boundary, not housekeeping.
	//
	// The gate above authorizes the scope the record IS in. Everything below
	// writes the record from the body. Without these three lines those are two
	// different scopes: an agent authorized on its own project's record PUT it
	// back with scope "global" and an attacker-chosen Config.Image and got 200,
	// and the store then held a global record carrying that image — measured,
	// and pinned by TestHCAuthz_UpdateCannotPromoteScope. The gate checked
	// where the record was and never where the caller was moving it to, which
	// is precisely the end state gating create was supposed to prevent,
	// reached by PUT instead of POST. handleHarnessConfigClone got this right
	// by authorizing its destination scope; update authorized no destination
	// at all.
	//
	// Preserving rather than rejecting is deliberate and is the conservative
	// reading: re-scoping is not a supported operation today, so there is no
	// destination to authorize. Note the consequence, because it is a real
	// one — a caller who submits a changed scope receives 200 and no move,
	// which reads like success. If re-scope is ever wanted as a feature, do
	// not simply delete these lines: authorize the destination scope the way
	// clone does, in addition to the source scope gated above.
	//
	// The OwnerID line specifically carries more weight than its neighbours,
	// and it is not obvious from here. Two authorization arms — the user-scope
	// arm of authorizeHarnessConfigScope and its twin in
	// handleHarnessConfigReimport — decide by comparing a record's OwnerID
	// against the caller. They are safe today only because no code path ever
	// sets OwnerID, so the comparison never matches and both arms deny. This
	// line is what keeps that true: without it a PUT body sets OwnerID to
	// whatever it likes.
	//
	// Composed with the scope line above, removing them hands an attacker who
	// may write any project-scoped config a record that is user-scoped and
	// owned by them — after which those arms admit the attacker and refuse
	// everyone else, hub admins included, because they compare identity and
	// never consult role. The config stays active and image-bearing and nobody
	// can update or delete it. Measured on the pre-fix shape: PUT returned 200
	// and the store held scope "user", scopeID "", ownerID as submitted.
	hc.Scope = existing.Scope
	hc.ScopeID = existing.ScopeID
	hc.OwnerID = existing.OwnerID

	if hc.Slug == "" {
		hc.Slug = api.Slugify(hc.Name)
	}

	if err := s.store.UpdateHarnessConfig(ctx, &hc); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, hc)
}

func (s *Server) patchHarnessConfig(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()

	existing, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Same gate as updateHarnessConfig, on the stored record's scope, and
	// deliberately not a weaker one. PATCH touches fewer fields than PUT today:
	// the struct below lists name, slug, displayName, description and
	// visibility, so it cannot reach Config.Image and cannot re-scope — it
	// mutates the record already fetched. The gate is not justified by what
	// PATCH can do today. It is justified because which fields that struct
	// happens to list is not an authorization boundary; it is a list that
	// grows, and it grows in a diff that will not look like a security change.
	if !s.authorizeHarnessConfigScope(w, r, existing.Scope, existing.ScopeID,
		harnessConfigUserScope{ownerID: existing.OwnerID}, ActionUpdate, "update") {
		return
	}

	var updates struct {
		Name        string `json:"name,omitempty"`
		Slug        string `json:"slug,omitempty"`
		DisplayName string `json:"displayName,omitempty"`
		Description string `json:"description,omitempty"`
		Visibility  string `json:"visibility,omitempty"`
	}

	if err := readJSON(r, &updates); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if updates.Name != "" {
		existing.Name = updates.Name
		if updates.Slug == "" {
			existing.Slug = api.Slugify(updates.Name)
		}
	}
	if updates.Slug != "" {
		existing.Slug = updates.Slug
	}
	if updates.DisplayName != "" {
		existing.DisplayName = updates.DisplayName
	}
	if updates.Description != "" {
		existing.Description = updates.Description
	}
	if updates.Visibility != "" {
		existing.Visibility = updates.Visibility
	}

	if err := s.store.UpdateHarnessConfig(ctx, existing); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) deleteHarnessConfig(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	query := r.URL.Query()

	deleteFiles := query.Get("deleteFiles") == "true"

	existing, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	// Authorize: check source scope for ActionDelete.
	//
	// This switch was the original and the other write endpoints were meant to
	// match it; instead one of them copied two of its four arms and the rest
	// had none. It is now a call to the extracted helper — same four arms, same
	// messages, same decisions — so that the endpoints cannot drift apart
	// again. Behaviour here is unchanged; the tests that pinned it still pass.
	if !s.authorizeHarnessConfigScope(w, r, existing.Scope, existing.ScopeID,
		harnessConfigUserScope{ownerID: existing.OwnerID}, ActionDelete, "delete") {
		return
	}

	if deleteFiles && existing.StoragePath != "" {
		if stor := s.GetStorage(); stor != nil {
			_ = stor.DeletePrefix(ctx, existing.StoragePath)
		}
	}

	if err := s.store.DeleteHarnessConfig(ctx, id); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleHarnessConfigUpload handles requests for upload URLs.
func (s *Server) handleHarnessConfigUpload(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	var req UploadRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Files) == 0 {
		ValidationError(w, "at least one file is required", nil)
		return
	}

	if hc.StoragePath == "" {
		RuntimeError(w, "Harness config storage path not configured (id: "+id+")")
		return
	}

	uploadURLs, manifestURL, err := generateUploadURLs(ctx, stor, hc.StoragePath, req.Files)
	if err != nil {
		RuntimeError(w, "Failed to generate upload URLs: "+err.Error())
		return
	}
	if len(uploadURLs) == 0 && len(req.Files) > 0 {
		RuntimeError(w, "Failed to generate upload URLs")
		return
	}

	writeJSON(w, http.StatusOK, UploadResponse{
		UploadURLs:  uploadURLs,
		ManifestURL: manifestURL,
	})
}

// handleHarnessConfigFinalize finalizes a harness config after file upload.
func (s *Server) handleHarnessConfigFinalize(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	var req struct {
		Manifest *HarnessConfigManifest `json:"manifest"`
	}
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.Manifest == nil || len(req.Manifest.Files) == 0 {
		ValidationError(w, "manifest with files is required", nil)
		return
	}

	contentHash, err := verifyAndFinalizeFiles(ctx, stor, hc.StoragePath, req.Manifest.Files)
	if err != nil {
		ValidationError(w, err.Error(), nil)
		return
	}

	hc.Files = req.Manifest.Files
	hc.ContentHash = contentHash
	hc.Status = store.HarnessConfigStatusActive

	if image := extractImageFromStorage(ctx, stor, hc.StoragePath); image != "" {
		if hc.Config == nil {
			hc.Config = &store.HarnessConfigData{}
		}
		hc.Config.Image = image
	}

	if err := s.store.UpdateHarnessConfig(ctx, hc); err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, hc)
}

// handleHarnessConfigCheckImage triggers an immediate image status re-check.
// POST /api/v1/harness-configs/{id}/check-image
func (s *Server) handleHarnessConfigCheckImage(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()
	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	image := s.harnessConfigImage(hc)
	if image == "" {
		writeError(w, http.StatusBadRequest, "no_image", "Harness config has no image configured", nil)
		return
	}

	registry := s.resolveImageRegistry()
	resolvedImage := config.RewriteImageRegistry(image, registry)
	slog.Info("checking image status", "id", hc.ID, "image", image, "resolved", resolvedImage, "registry", registry)

	if imagecheck.IsBareImageName(image) {
		status := store.HarnessConfigImageStatusUnknown
		source := "broker_check"

		if s.brokerClient != nil {
			brokerResult, err := s.store.ListRuntimeBrokers(ctx, store.RuntimeBrokerFilter{}, store.ListOptions{Limit: 100})
			if err == nil {
				var found bool
				var wg sync.WaitGroup
				var mu sync.Mutex
				for i := range brokerResult.Items {
					b := &brokerResult.Items[i]
					if _, isPlugin := b.Labels["scion.io/plugin"]; isPlugin {
						continue
					}
					if !s.canDispatchToBroker(ctx, b) {
						continue
					}
					if !isNodeBoundBroker(b) {
						continue
					}
					wg.Add(1)
					go func(broker *store.RuntimeBroker) {
						defer wg.Done()
						brokerCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
						defer cancel()
						imgResp, err := s.brokerClient.ImageStatus(brokerCtx, broker.ID, broker.Endpoint, image, "")
						if err != nil {
							return
						}
						if imgResp != nil && imgResp.LocalShort != nil && imgResp.LocalShort.Exists {
							mu.Lock()
							found = true
							mu.Unlock()
						}
					}(b)
				}
				wg.Wait()
				if found {
					status = store.HarnessConfigImageStatusValid
				}
			}
		}

		if status != store.HarnessConfigImageStatusValid && resolvedImage != image {
			result := s.imageChecker.CheckRemoteOnly(ctx, resolvedImage)
			if result.Status == "valid" {
				status = store.HarnessConfigImageStatusValid
				source = "registry"
			}
		}

		now := time.Now()
		if err := s.store.UpdateHarnessConfigImageStatus(ctx, hc.ID, status, now); err != nil {
			slog.Warn("failed to persist image status", "id", hc.ID, "error", err)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"image_status":            status,
			"image_status_checked_at": now,
			"source":                  source,
			"resolved_image":          image,
		})
		return
	}

	result := s.imageChecker.CheckRemoteOnly(ctx, resolvedImage)
	slog.Info("image check result", "id", hc.ID, "status", result.Status, "source", result.Source, "error", result.Error)

	if err := s.store.UpdateHarnessConfigImageStatus(ctx, hc.ID, result.Status, result.CheckedAt); err != nil {
		slog.Warn("failed to persist image status", "id", hc.ID, "error", err)
	}

	resp := map[string]any{
		"image_status":            result.Status,
		"image_status_checked_at": result.CheckedAt,
		"source":                  result.Source,
		"resolved_image":          resolvedImage,
	}
	if result.Error != "" {
		resp["error"] = result.Error
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleHarnessConfigDownload returns signed URLs for downloading harness config files.
func (s *Server) handleHarnessConfigDownload(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	stor := s.GetStorage()
	if stor == nil {
		RuntimeError(w, "Storage not configured")
		return
	}

	if len(hc.Files) == 0 {
		ValidationError(w, "harness config has no files", nil)
		return
	}

	downloadURLs, manifestURL, expires, err := generateDownloadURLs(ctx, stor, hc.StoragePath, s.legacyFallbackPath(hc.StoragePath), hc.Files)
	if err != nil {
		RuntimeError(w, fmt.Sprintf("harness-config %q: %s — run 'scion harness-config validate %s' to diagnose", hc.Name, err, hc.Name))
		return
	}

	writeJSON(w, http.StatusOK, DownloadResponse{
		Files:       downloadURLs,
		ManifestURL: manifestURL,
		Expires:     expires,
	})
}

// handleHarnessConfigValidate validates a harness-config's storage consistency.
func (s *Server) handleHarnessConfigValidate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()
	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	rec := harnessConfigToRecord(hc)
	rs := s.harnessConfigStore(hc.Harness)
	report, err := rs.ValidateStorage(ctx, rec)
	if err != nil {
		RuntimeError(w, fmt.Sprintf("validation failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, report)
}

// handleHarnessConfigClone creates a copy of a harness config.
func (s *Server) handleHarnessConfigClone(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	source, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	var req CloneTemplateRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		ValidationError(w, "name is required", nil)
		return
	}

	// Resolve scope ID
	scopeID := req.ScopeID
	if scopeID == "" && req.ProjectID != "" {
		scopeID = req.ProjectID
	}

	// Authorize: check destination scope for ActionCreate
	destScope := req.Scope
	if destScope == "" {
		destScope = source.Scope
	}
	if destScope == "" {
		destScope = store.HarnessConfigScopeGlobal
	}
	// This switch used to be written out here with a global arm, a project arm,
	// and no others. A clone to "user" scope, or to any scope string the two
	// arms did not name, matched nothing and fell through to the create below
	// with no authorization performed at all — the miss rendered as a hit.
	// destScope comes from the request body, so the set of values that reached
	// the bottom was the caller's to choose.
	//
	// The shared helper supplies the missing user arm and, more importantly, a
	// default arm that denies.
	if !s.authorizeHarnessConfigScope(w, r, destScope, scopeID,
		harnessConfigUserScope{ownerID: scopeID, isNew: true}, ActionCreate, "create") {
		return
	}

	clone := &store.HarnessConfig{
		ID:          api.NewUUID(),
		Name:        req.Name,
		Slug:        api.Slugify(req.Name),
		DisplayName: source.DisplayName,
		Description: source.Description,
		Harness:     source.Harness,
		Config:      source.Config,
		Scope:       destScope,
		ScopeID:     scopeID,
		Visibility:  req.Visibility,
		Status:      store.HarnessConfigStatusPending,
	}

	if clone.Visibility == "" {
		clone.Visibility = source.Visibility
	}

	storagePath := storage.HarnessConfigStoragePath(s.HubID(), clone.Scope, clone.ScopeID, clone.Slug)
	clone.StoragePath = storagePath

	stor := s.GetStorage()
	if stor != nil {
		clone.StorageBucket = stor.Bucket()
		clone.StorageURI = storage.HarnessConfigStorageURI(s.HubID(), stor.Bucket(), clone.Scope, clone.ScopeID, clone.Slug)
	}

	if stor != nil && len(source.Files) > 0 && source.StoragePath != "" {
		for _, file := range source.Files {
			srcPath := source.StoragePath + "/" + file.Path
			dstPath := storagePath + "/" + file.Path
			if _, err := stor.Copy(ctx, srcPath, dstPath); err != nil {
				_ = stor.DeletePrefix(ctx, storagePath)
				RuntimeError(w, "Failed to copy files: "+err.Error())
				return
			}
		}
		clone.Files = source.Files
		clone.ContentHash = source.ContentHash
		clone.Status = store.HarnessConfigStatusActive
	}

	if err := s.store.CreateHarnessConfig(ctx, clone); err != nil {
		if stor != nil {
			_ = stor.DeletePrefix(ctx, storagePath)
		}
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeError(w, http.StatusConflict, "conflict", "A resource with this slug already exists in the target scope. Choose a different name.", nil)
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusCreated, clone)
}

// ReimportHarnessConfigRequest is the optional request body for the reimport endpoint.
type ReimportHarnessConfigRequest struct {
	SourceURL string `json:"sourceUrl,omitempty"`
}

// handleHarnessConfigReimport re-imports a harness-config from its stored
// source_url (or an override URL). POST /api/v1/harness-configs/{id}/reimport
func (s *Server) handleHarnessConfigReimport(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()

	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}
	if hc == nil {
		NotFound(w, "HarnessConfig")
		return
	}

	var req ReimportHarnessConfigRequest
	if r.Body != nil && r.Body != http.NoBody {
		if err := readJSON(r, &req); err != nil {
			BadRequest(w, "Invalid request body: "+err.Error())
			return
		}
	}

	sourceURL := req.SourceURL
	if sourceURL == "" {
		sourceURL = hc.SourceURL
	}
	if sourceURL == "" {
		writeError(w, http.StatusBadRequest, "no_source_url",
			"No source URL stored and none provided. Use the sourceUrl field to specify one.", nil)
		return
	}

	sourceURL = config.NormalizeTemplateSourceURL(sourceURL)

	// Authorize: same as import — harness_config:create on the owning scope.
	//
	// DO NOT convert this switch to authorizeHarnessConfigScope. It looks like
	// the fourth copy of that switch and it is deliberately the last one left:
	// its project arm calls s.authorizeProjectImport, which the shared helper
	// does not and must not, because import authorization is a different
	// question from write authorization. Folding this in would silently drop
	// that call, and the resulting diff would look like tidying.
	//
	// It is also not a template. The other three copies of this switch had
	// drifted from each other — one of them was missing arms entirely and let
	// unnamed scopes through — which is why they were consolidated. If you need
	// a scope switch somewhere new, call authorizeHarnessConfigScope; do not
	// copy this one.
	switch hc.Scope {
	case store.HarnessConfigScopeGlobal:
		userIdent := GetUserIdentityFromContext(ctx)
		if userIdent == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
			return
		}
		decision := s.authzService.CheckAccess(ctx, userIdent, Resource{Type: "harness_config"}, ActionCreate)
		if !decision.Allowed {
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to reimport global resources", nil)
			return
		}
	case store.HarnessConfigScopeProject:
		if !s.authorizeProjectImport(ctx, w, hc.ScopeID, "harness-configs") {
			return
		}
	case store.HarnessConfigScopeUser:
		// DELIBERATE ABSENCE — the twin of the user arm of
		// authorizeHarnessConfigScope, and read that note in full before
		// touching this one.
		//
		// In short: the store persists OwnerID faithfully (written and read
		// back unchanged, measured); what is absent is any code path that sets
		// it, so on every record today hc.OwnerID is "" and this comparison
		// never matches. That makes the arm dead by the contingent absence of
		// an assignment — not "by construction". It fails closed, so it is
		// preserved as written rather than "fixed" here.
		//
		// The assignment that did exist has been removed, and what holds the
		// property now is one line elsewhere:
		//
		//	updateHarnessConfig, `hc.OwnerID = existing.OwnerID`
		//
		// Before it, a PUT body's ownerId flowed straight to the store
		// (measured), which would have made this arm live and
		// attacker-controlled. If that line goes, this arm and its twin come
		// alive in the same edit.
		//
		// Whether OwnerID should be populated is a pending decision held
		// outside this change. Whoever makes it must re-review this arm
		// together with the one above: they come alive at once, and "alive"
		// includes refusing hub admins on a record whose recorded owner is
		// someone else, since neither arm consults role.
		userIdent := GetUserIdentityFromContext(ctx)
		if userIdent == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required", nil)
			return
		}
		if hc.OwnerID != userIdent.ID() {
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to reimport another user's harness config", nil)
			return
		}
	default:
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "Reimport is not supported for this resource scope", nil)
		return
	}

	if s.GetStorage() == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage is not configured", nil)
		return
	}

	kind := s.harnessConfigImportKind()
	run := func(progress importProgressFunc) ([]string, error) {
		return s.importFromRemote(ctx, hc.ScopeID, sourceURL, hc.Scope, kind, progress, nil)
	}

	if importAcceptsNDJSON(r) {
		s.streamImport(w, run)
		return
	}

	var failures []ImportFailure
	imported, err := run(failureCollector(&failures))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reimport_failed", err.Error(), nil)
		return
	}

	if len(imported) == 0 && len(failures) > 0 {
		reasons := make([]string, len(failures))
		for i, f := range failures {
			reasons[i] = f.Name + ": " + f.Reason
		}
		writeError(w, http.StatusBadRequest, "reimport_failed",
			"config.yaml validation failed: "+strings.Join(reasons, "; "), nil)
		return
	}

	writeJSON(w, http.StatusOK, ImportHarnessConfigsResponse{
		HarnessConfigs: imported,
		Count:          len(imported),
		Failed:         failures,
	})
}

// handleHarnessConfigImageStatus returns per-broker aggregated image status.
// GET /api/v1/harness-configs/{id}/image-status
func (s *Server) handleHarnessConfigImageStatus(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()
	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	image := s.harnessConfigImage(hc)
	if image == "" {
		writeError(w, http.StatusBadRequest, "no_image", "Harness config has no image configured", nil)
		return
	}

	registry := s.resolveImageRegistry()
	longImage := config.RewriteImageRegistry(image, registry)

	shortImage := image
	if !imagecheck.IsBareImageName(image) {
		shortImage = ""
	}

	registryStatus := s.checkRegistryImage(ctx, longImage)

	if s.brokerClient == nil {
		if s.imageManager != nil {
			entry := s.buildLocalImageEntry(ctx, shortImage, longImage, registryStatus)
			writeJSON(w, http.StatusOK, AggregatedImageStatusResponse{
				Image:    image,
				Registry: &registryStatus,
				Brokers:  []BrokerImageEntry{entry},
			})
			return
		}
		writeJSON(w, http.StatusOK, AggregatedImageStatusResponse{
			Image:    image,
			Registry: &registryStatus,
		})
		return
	}

	brokerResult, err := s.store.ListRuntimeBrokers(ctx, store.RuntimeBrokerFilter{}, store.ListOptions{Limit: 100})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "broker_list_failed", fmt.Sprintf("Failed to list brokers: %v", err), nil)
		return
	}

	var nodeBound []*store.RuntimeBroker
	var proxyEntries []ProxyBrokerEntry
	for i := range brokerResult.Items {
		b := &brokerResult.Items[i]
		if _, isPlugin := b.Labels["scion.io/plugin"]; isPlugin {
			continue
		}
		if !s.canDispatchToBroker(ctx, b) {
			continue
		}
		if isNodeBoundBroker(b) {
			nodeBound = append(nodeBound, b)
		} else {
			var runtimeTypes []string
			seen := map[string]bool{}
			for _, p := range b.Profiles {
				if !seen[p.Type] {
					runtimeTypes = append(runtimeTypes, p.Type)
					seen[p.Type] = true
				}
			}
			runtime := strings.Join(runtimeTypes, ",")
			proxyEntries = append(proxyEntries, ProxyBrokerEntry{
				BrokerID: b.ID, BrokerName: b.Name, Runtime: runtime,
			})
		}
	}

	brokerEntries := make([]BrokerImageEntry, len(nodeBound))
	var wg sync.WaitGroup
	for i, broker := range nodeBound {
		wg.Add(1)
		go func(idx int, b *store.RuntimeBroker) {
			defer wg.Done()
			entry := BrokerImageEntry{
				BrokerID:   b.ID,
				BrokerName: b.Name,
			}

			brokerCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			imgResp, err := s.brokerClient.ImageStatus(brokerCtx, b.ID, b.Endpoint, shortImage, longImage)
			if err != nil {
				var unsupported *BrokerUnsupportedError
				if errors.As(err, &unsupported) {
					entry.Reachable = true
					entry.Unsupported = true
				} else {
					entry.Reachable = false
				}
			} else if imgResp != nil {
				entry.Reachable = true
				entry.LocalShort = imgResp.LocalShort
				entry.LocalLong = imgResp.LocalLong

				if entry.LocalLong != nil && entry.LocalLong.Exists && registryStatus.Hash != "" && entry.LocalLong.Hash != "" {
					entry.NewerInRegistry = registryStatus.Hash != entry.LocalLong.Hash
				}

				switch {
				case entry.LocalShort != nil && entry.LocalShort.Exists:
					entry.ResolvedImage = shortImage
					entry.ResolutionSource = "local_short"
				case entry.LocalLong != nil && entry.LocalLong.Exists:
					entry.ResolvedImage = longImage
					entry.ResolutionSource = "local_long"
				case registryStatus.Exists:
					entry.ResolvedImage = longImage
					entry.ResolutionSource = "remote"
				default:
					entry.ResolutionSource = "none"
				}
			}
			brokerEntries[idx] = entry
		}(i, broker)
	}
	wg.Wait()

	if len(nodeBound) == 0 && s.imageManager != nil {
		entry := s.buildLocalImageEntry(ctx, shortImage, longImage, registryStatus)
		brokerEntries = append(brokerEntries, entry)
	}

	resp := AggregatedImageStatusResponse{
		Image:        image,
		Registry:     &registryStatus,
		Brokers:      brokerEntries,
		ProxyBrokers: proxyEntries,
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildLocalImageEntry constructs a BrokerImageEntry using the hub's
// co-located container runtime (Docker/Podman) when no broker client is
// available. This ensures workstation-mode users see pulled image state
// and the Build Image option.
func (s *Server) buildLocalImageEntry(ctx context.Context, shortImage, longImage string, registryStatus RegistryImageStatus) BrokerImageEntry {
	result := s.imageChecker.CheckAll(ctx, shortImage, longImage)

	brokerName := "Local Runtime"
	if namer, ok := s.imageManager.(interface{ Name() string }); ok {
		if n := namer.Name(); n != "" {
			brokerName = n
		}
	}

	entry := BrokerImageEntry{
		BrokerName: brokerName,
		Reachable:  true,
	}
	if shortImage != "" {
		entry.LocalShort = &BrokerImageEntityState{Exists: result.LocalShort.Exists, Hash: result.LocalShort.Hash}
	}
	if longImage != "" {
		entry.LocalLong = &BrokerImageEntityState{Exists: result.LocalLong.Exists, Hash: result.LocalLong.Hash}
	}

	if entry.LocalLong != nil && entry.LocalLong.Exists && registryStatus.Hash != "" && entry.LocalLong.Hash != "" {
		entry.NewerInRegistry = registryStatus.Hash != entry.LocalLong.Hash
	}

	switch {
	case entry.LocalShort != nil && entry.LocalShort.Exists:
		entry.ResolvedImage = shortImage
		entry.ResolutionSource = "local_short"
	case entry.LocalLong != nil && entry.LocalLong.Exists:
		entry.ResolvedImage = longImage
		entry.ResolutionSource = "local_long"
	case registryStatus.Exists:
		entry.ResolvedImage = longImage
		entry.ResolutionSource = "remote"
	default:
		entry.ResolutionSource = "none"
	}

	return entry
}

// handleHarnessConfigDeleteLocalImage removes the local short-form image.
// DELETE /api/v1/harness-configs/{id}/local-image?broker_id=...
func (s *Server) handleHarnessConfigDeleteLocalImage(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()
	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	image := s.harnessConfigImage(hc)
	if image == "" || !imagecheck.IsBareImageName(image) {
		writeError(w, http.StatusBadRequest, "no_local_image", "Harness config has no short-form image to delete", nil)
		return
	}

	brokerID := r.URL.Query().Get("broker_id")
	if brokerID != "" {
		if s.brokerClient == nil {
			writeError(w, http.StatusServiceUnavailable, "no_broker_client", "Broker routing not available", nil)
			return
		}
		broker, err := s.store.GetRuntimeBroker(ctx, brokerID)
		if err != nil {
			writeErrorFromErr(w, err, "")
			return
		}
		if !s.canDispatchToBroker(ctx, broker) {
			writeError(w, http.StatusForbidden, "forbidden", "You do not have permission to perform image operations on this broker", nil)
			return
		}
		if !isNodeBoundBroker(broker) {
			writeError(w, http.StatusBadRequest, "invalid_broker", "Image operations are only supported on node-bound brokers", nil)
			return
		}
		if err := s.brokerClient.DeleteImage(ctx, broker.ID, broker.Endpoint, image); err != nil {
			writeError(w, http.StatusInternalServerError, "remove_failed", fmt.Sprintf("Failed to remove image on broker %s: %v", broker.Name, err), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "image": image, "broker_id": broker.ID})
		return
	}

	if s.imageManager == nil {
		writeError(w, http.StatusServiceUnavailable, "no_runtime", "Container runtime not available", nil)
		return
	}

	exists, err := s.imageManager.ImageExists(ctx, image)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "check_failed", fmt.Sprintf("Failed to check image: %v", err), nil)
		return
	}
	if !exists {
		writeJSON(w, http.StatusOK, map[string]string{"status": "not_found", "image": image})
		return
	}

	if err := s.imageManager.RemoveImage(ctx, image); err != nil {
		writeError(w, http.StatusInternalServerError, "remove_failed", fmt.Sprintf("Failed to remove image: %v", err), nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "image": image})
}

// handleHarnessConfigPullImage pulls the latest image from the remote registry.
// POST /api/v1/harness-configs/{id}/pull-image?broker_id=...
func (s *Server) handleHarnessConfigPullImage(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	ctx := r.Context()
	hc, err := s.store.GetHarnessConfig(ctx, id)
	if err != nil {
		writeErrorFromErr(w, err, "")
		return
	}

	image := s.harnessConfigImage(hc)
	if image == "" {
		writeError(w, http.StatusBadRequest, "no_image", "Harness config has no image configured", nil)
		return
	}

	registry := s.resolveImageRegistry()
	pullImage := config.RewriteImageRegistry(image, registry)

	brokerID := r.URL.Query().Get("broker_id")
	if brokerID != "" {
		if s.brokerClient == nil {
			writeError(w, http.StatusServiceUnavailable, "no_broker_client", "Broker routing not available", nil)
			return
		}
		broker, err := s.store.GetRuntimeBroker(ctx, brokerID)
		if err != nil {
			writeErrorFromErr(w, err, "")
			return
		}
		if !s.canDispatchToBroker(ctx, broker) {
			writeError(w, http.StatusForbidden, "forbidden", "You do not have permission to perform image operations on this broker", nil)
			return
		}
		if !isNodeBoundBroker(broker) {
			writeError(w, http.StatusBadRequest, "invalid_broker", "Image operations are only supported on node-bound brokers", nil)
			return
		}
		if err := s.brokerClient.PullImage(ctx, broker.ID, broker.Endpoint, pullImage); err != nil {
			writeError(w, http.StatusInternalServerError, "pull_failed", fmt.Sprintf("Failed to pull image on broker %s: %v", broker.Name, err), nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "pulled", "image": pullImage, "broker_id": broker.ID})
		return
	}

	if s.imageManager == nil {
		writeError(w, http.StatusServiceUnavailable, "no_runtime", "Container runtime not available", nil)
		return
	}

	if err := s.imageManager.PullImage(ctx, pullImage); err != nil {
		writeError(w, http.StatusInternalServerError, "pull_failed", fmt.Sprintf("Failed to pull image: %v", err), nil)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "pulled", "image": pullImage})
}

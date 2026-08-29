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
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/go-jose/go-jose/v4/jwt"
)

// agentSecretFetchRequest is the request body for POST /api/v1/agent/secrets.
type agentSecretFetchRequest struct {
	Keys []string `json:"keys"` // Secret key names to fetch.
}

// agentSecretFetchResponse is the response body for POST /api/v1/agent/secrets.
// It carries results per key, each with a distinct outcome.
type agentSecretFetchResponse struct {
	Secrets []agentSecretResult `json:"secrets"`
}

// agentSecretResult is the per-key result in the fetch response.
type agentSecretResult struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Status string `json:"status"`          // "ok", "entitled_but_unavailable", "access_withdrawn", "not_found"
	Error  string `json:"error,omitempty"` // Human-readable error for non-ok statuses.
}

// Status constants for per-key fetch results.
const (
	secretFetchStatusOK                = "ok"
	secretFetchStatusUnavailable       = "entitled_but_unavailable"
	secretFetchStatusAccessWithdrawn   = "access_withdrawn"
	secretFetchStatusNotFound          = "not_found"
)

// handleAgentSecretFetch handles POST /api/v1/agent/secrets.
//
// This is the channel that allows agents to fetch secret values by key name,
// replacing injection via process argv. (#127, P2c)
//
// Two gates, both required:
//
//	Gate 1 (stored list): the credential row's EntitledSecretKeys, looked up
//	by JTI hash. Cheap, blocks enumeration, session-stable.
//
//	Gate 2 (live resolution): scope filter + progeny authz at request time.
//	Makes revocation take effect immediately.
//
// Five outcomes per key — see the switch in the inner loop.
//
// The middleware fails open on credential-store errors (auth.go:179-183)
// and on missing credential rows (auth.go:174-178). This handler does its
// own lookup and fails closed where the middleware failed open.
func (s *Server) handleAgentSecretFetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	claims := GetAgentFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized,
			"agent authentication required", nil)
		return
	}

	// Parse request body.
	var req agentSecretFetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest,
			"invalid request body: "+err.Error(), nil)
		return
	}
	if len(req.Keys) == 0 {
		writeError(w, http.StatusBadRequest, ErrCodeInvalidRequest,
			"at least one key is required", nil)
		return
	}

	// --- Gate 1: stored entitlement list ---
	//
	// Re-fetch the credential row by JTI hash. The middleware already fetched
	// this row (auth.go:159) but kept only cred.ID, discarding
	// EntitledSecretKeys. The double fetch is accepted and load-bearing:
	// the middleware fails open on ErrNotFound and store errors, but this
	// handler fails closed.
	jtiHash := hashJTI(claims.ID)
	cred, credErr := s.store.GetAgentCredentialByJTIHash(r.Context(), jtiHash)

	if credErr != nil {
		if errors.Is(credErr, store.ErrNotFound) {
			// Row 5: no credential row at all. Legacy pre-table token.
			// Fail closed with a distinct, actionable message.
			slog.Warn("Secret fetch: no credential row for token (legacy/pre-table)",
				"agent_id", claims.Subject, "jti_hash", jtiHash[:8])
			writeError(w, http.StatusForbidden, ErrCodeForbidden,
				"this token predates entitlement recording; "+
					"restart the agent or refresh its token to obtain a current credential", nil)
			return
		}
		// Store error — fail closed. The middleware failed open for
		// availability; we fail closed because this endpoint hands out
		// secret values.
		slog.Error("Secret fetch: credential store lookup failed",
			"agent_id", claims.Subject, "error", credErr)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError,
			"credential lookup failed", nil)
		return
	}

	// Three states of the entitlement column:
	//   nil  = minted by a path that does not record entitlement (bug)
	//   []   = recorded, entitled to zero secrets (normal)
	//   [...] = entitled to specific keys
	if cred.EntitledSecretKeys == nil {
		// Column NULL: a minting path is broken. Fail closed, loudly.
		slog.Error("Secret fetch: credential has NULL entitled_secret_keys — minting path bug",
			"agent_id", claims.Subject,
			"credential_id", cred.ID,
			"jti_hash", jtiHash[:8])
		writeError(w, http.StatusForbidden, ErrCodeForbidden,
			"entitlement was never recorded for this credential; "+
				"this indicates a bug in the credential-minting path — contact the operator", nil)
		return
	}

	// Build the entitled set for O(1) lookup.
	entitledSet := make(map[string]struct{}, len(cred.EntitledSecretKeys))
	for _, k := range cred.EntitledSecretKeys {
		entitledSet[k] = struct{}{}
	}

	// --- Gate 2: live resolution ---
	//
	// Resolve all secrets the agent is currently authorized for. This is the
	// same path as injection-time resolution, with the same scope tuples and
	// the same authz checks. A secret de-scoped or with withdrawn authz
	// after credential mint will not appear here.
	agent, err := s.store.GetAgent(r.Context(), claims.Subject)
	if err != nil {
		slog.Error("Secret fetch: agent lookup failed",
			"agent_id", claims.Subject, "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError,
			"agent lookup failed", nil)
		return
	}

	var resolvedMap map[string]secret.SecretWithValue
	if s.secretBackend != nil {
		var resolveOpts *secret.ResolveOpts
		if len(agent.Ancestry) > 1 && s.authzService != nil {
			agentID := agent.ID
			ancestry := agent.Ancestry
			resolveOpts = &secret.ResolveOpts{
				AgentAncestry: ancestry,
				AuthzCheck: func(meta secret.SecretMeta) bool {
					decision := s.authzService.CheckAccess(r.Context(), &agentIdentityWrapper{
						AgentTokenClaims: &AgentTokenClaims{
							Claims:    jwt.Claims{Subject: agentID},
							ProjectID: agent.ProjectID,
							Ancestry:  ancestry,
						},
					}, Resource{
						Type: "secret",
						ID:   meta.ID,
					}, ActionRead)
					return decision.Allowed
				},
			}
		}

		resolved, resolveErr := s.secretBackend.Resolve(
			r.Context(), agent.OwnerID, agent.ProjectID, agent.RuntimeBrokerID, resolveOpts)
		if resolveErr != nil {
			slog.Error("Secret fetch: resolution failed",
				"agent_id", claims.Subject, "error", resolveErr)
			writeError(w, http.StatusInternalServerError, ErrCodeInternalError,
				"secret resolution failed", nil)
			return
		}

		resolvedMap = make(map[string]secret.SecretWithValue, len(resolved))
		for _, sv := range resolved {
			resolvedMap[sv.Name] = sv
		}
	}

	// --- Produce per-key results ---
	results := make([]agentSecretResult, 0, len(req.Keys))
	for _, key := range req.Keys {
		_, entitled := entitledSet[key]
		sv, resolved := resolvedMap[key]

		var result agentSecretResult
		result.Key = key

		switch {
		case !entitled:
			// Row 4: not in stored list → indistinguishable from "does not exist".
			result.Status = secretFetchStatusNotFound
			result.Error = "secret not found"

		case entitled && resolved:
			// Row 1: in stored list, resolves now → the value.
			result.Status = secretFetchStatusOK
			result.Value = sv.Value

		case entitled && !resolved:
			// Could be row 2 (exists but unreadable) or row 3 (no longer
			// authorized). Distinguish by checking if the key appears in
			// the listing (what exists) vs. the resolution (what resolved).
			//
			// If the key is in the listing but not in the resolution, it
			// either failed to decrypt (row 2) or was de-scoped/authz-
			// withdrawn (row 3). We use computeEntitledSecretKeys to check
			// current listing — if the key is still listed, it's row 2
			// (exists but value unreadable). If not listed, it's row 3
			// (access withdrawn).
			currentlyListed := false
			currentKeys, listErr := computeEntitledSecretKeys(
				r.Context(), s.secretBackend, s.store, s.authzService, agent)
			if listErr == nil {
				for _, ck := range currentKeys {
					if ck == key {
						currentlyListed = true
						break
					}
				}
			}

			if currentlyListed {
				// Row 2: in stored list, exists, but value unreadable.
				result.Status = secretFetchStatusUnavailable
				result.Error = "secret is entitled but its value could not be read; " +
					"this may be a transient decryption or backend error"
			} else {
				// Row 3: in stored list, but no longer authorized or in scope.
				result.Status = secretFetchStatusAccessWithdrawn
				result.Error = "access to this secret has been withdrawn since this token was issued; " +
					"refresh the token to update entitlement"
			}
		}

		results = append(results, result)
	}

	resp := agentSecretFetchResponse{Secrets: results}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("Secret fetch: failed to encode response",
			"agent_id", claims.Subject, "error", err)
	}
}

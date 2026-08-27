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

	"github.com/GoogleCloudPlatform/scion/pkg/messaging"
)

// conversationResolveRequest is the POST body for /api/v1/conversations/resolve.
//
// Security: sender identity is always derived from the authenticated caller.
// Body-supplied sender fields were removed because they allowed any authenticated
// principal to impersonate another and bypass participant auth checks (G-1).
type conversationResolveRequest struct {
	Reference string `json:"reference"`
	ProjectID string `json:"project_id"`
}

// conversationResolveResponse is the response body for /api/v1/conversations/resolve.
type conversationResolveResponse struct {
	ConversationID string `json:"conversation_id"`
	Created        bool   `json:"created"`
}

// handleConversationsResolve handles POST /api/v1/conversations/resolve.
//
// Takes a conversation reference string (conv:<id>, @<agent>, @<email>,
// #<thread>) and resolves it to a conversation ID using messaging.Resolve.
// Creates the conversation if needed (resolve-or-create for @ references).
func (s *Server) handleConversationsResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		MethodNotAllowed(w)
		return
	}

	// Require authentication (user or agent).
	user := GetUserIdentityFromContext(r.Context())
	agentIdent := GetAgentIdentityFromContext(r.Context())
	if user == nil && agentIdent == nil {
		Forbidden(w)
		return
	}

	var req conversationResolveRequest
	if err := readJSON(r, &req); err != nil {
		BadRequest(w, "Invalid request body: "+err.Error())
		return
	}

	if req.Reference == "" {
		ValidationError(w, "reference is required", nil)
		return
	}

	// Sender identity is always derived from the authenticated caller.
	senderKind := ""
	senderID := ""
	if agentIdent != nil {
		senderKind = "agent"
		senderID = agentIdent.ID()
	} else if user != nil {
		senderKind = "user"
		senderID = user.ID()
	}

	rctx := messaging.ResolveContext{
		SenderPrincipalKind: senderKind,
		SenderPrincipalID:   senderID,
		ProjectID:           req.ProjectID,
	}

	result, err := messaging.Resolve(r.Context(), s.store, req.Reference, rctx)
	if err != nil {
		// Check for resolution-specific errors.
		if resErr, ok := err.(*messaging.ResolutionError); ok {
			switch resErr.Reason {
			case "not-found":
				writeError(w, http.StatusNotFound, ErrCodeNotFound, resErr.Error(), nil)
			case "boundary-violation":
				writeError(w, http.StatusForbidden, ErrCodeForbidden, resErr.Error(), nil)
			case "not-a-participant":
				writeError(w, http.StatusForbidden, ErrCodeForbidden, resErr.Error(), nil)
			case "no-shared-project":
				ValidationError(w, resErr.Error(), nil)
			case "ambiguous":
				writeError(w, http.StatusConflict, ErrCodeValidationError, resErr.Error(), nil)
			default:
				writeErrorFromErr(w, err, "")
			}
			return
		}
		writeErrorFromErr(w, err, "")
		return
	}

	writeJSON(w, http.StatusOK, conversationResolveResponse{
		ConversationID: result.ConversationID,
		Created:        result.Created,
	})
}

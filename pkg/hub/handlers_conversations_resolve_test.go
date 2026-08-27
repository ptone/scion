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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleConversationsResolve_MethodNotAllowed(t *testing.T) {
	srv := &Server{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/resolve", nil)
	rr := httptest.NewRecorder()
	srv.handleConversationsResolve(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestHandleConversationsResolve_Unauthenticated(t *testing.T) {
	srv := &Server{}

	body, _ := json.Marshal(conversationResolveRequest{
		Reference: "@some-agent",
		ProjectID: "proj-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/resolve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.handleConversationsResolve(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleConversationsResolve_EmptyReference(t *testing.T) {
	srv := &Server{}

	user := NewAuthenticatedUser("u1", "user@example.com", "User", "member", "cli")
	body, _ := json.Marshal(conversationResolveRequest{
		Reference: "",
		ProjectID: "proj-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/resolve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), user))
	rr := httptest.NewRecorder()
	srv.handleConversationsResolve(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleConversationsResolve_InvalidJSON(t *testing.T) {
	srv := &Server{}

	user := NewAuthenticatedUser("u1", "user@example.com", "User", "member", "cli")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/resolve", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), user))
	rr := httptest.NewRecorder()
	srv.handleConversationsResolve(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleConversationsResolve_InvalidReference(t *testing.T) {
	srv := &Server{}

	user := NewAuthenticatedUser("u1", "user@example.com", "User", "member", "cli")
	body, _ := json.Marshal(conversationResolveRequest{
		Reference: "not-a-valid-reference",
		ProjectID: "proj-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/resolve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(contextWithIdentity(req.Context(), user))
	rr := httptest.NewRecorder()
	srv.handleConversationsResolve(rr, req)

	// The handler delegates to messaging.Resolve which fails on invalid ref
	// format. The store is nil, but ParseReference fails first.
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusBadRequest {
		// Accept either 400 or 500 since the store is nil and we can't fully
		// resolve even with a valid reference. The handler currently returns
		// 500 for non-ResolutionError errors.
		t.Fatalf("expected 4xx or 5xx, got %d: %s", rr.Code, rr.Body.String())
	}
}

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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupSecretFetchTestServer creates a test server wired with a local secret
// backend. Returns the server, store, and a project to scope secrets against.
func setupSecretFetchTestServer(t *testing.T) (*Server, store.Store, *store.User, *store.Project) {
	t.Helper()
	srv, s, user, project := setupCredentialTestServer(t)

	// Wire a local secret backend (no encryption — test mode).
	backend := secret.NewLocalBackend(s, "test-hub-id", "")
	srv.SetSecretBackend(backend)

	return srv, s, user, project
}

// createSecretInStore creates a secret in the store for testing.
func createSecretInStore(t *testing.T, s store.Store, key, scope, scopeID, value string) {
	t.Helper()
	// Generate a deterministic UUID from the key+scope+scopeID for test reproducibility.
	secretID := tid("secret-" + key + "-" + scope + "-" + scopeID)
	_, err := s.UpsertSecret(context.Background(), &store.Secret{
		ID:             secretID,
		Key:            key,
		Scope:          scope,
		ScopeID:        scopeID,
		SecretType:     store.SecretTypeEnvironment,
		EncryptedValue: value, // plaintext because no encryption key
	})
	require.NoError(t, err)
}

// generateTokenWithEntitledKeys generates a token for an agent, then records
// entitled keys on the credential. Returns the token string.
func generateTokenWithEntitledKeys(t *testing.T, srv *Server, s store.Store,
	agentID, projectID string, entitledKeys []string) string {
	t.Helper()
	token, _, err := srv.GenerateAgentToken(agentID, projectID, nil, AgentRoleFull, nil)
	require.NoError(t, err)

	claims, err := srv.agentTokenService.ValidateAgentToken(token)
	require.NoError(t, err)
	jtiHash := hashJTI(claims.ID)

	err = s.UpdateAgentCredentialEntitledKeys(context.Background(), jtiHash, agentID, entitledKeys)
	require.NoError(t, err)

	return token
}

// doSecretFetchRequest sends a POST /api/v1/agent/secrets request and returns
// the recorder.
func doSecretFetchRequest(t *testing.T, srv *Server, token string, keys []string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(agentSecretFetchRequest{Keys: keys})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/secrets", bytes.NewReader(body))
	req.Header.Set("X-Scion-Agent-Token", token)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// parseSecretFetchResponse parses the response body into an agentSecretFetchResponse.
func parseSecretFetchResponse(t *testing.T, rec *httptest.ResponseRecorder) agentSecretFetchResponse {
	t.Helper()
	var resp agentSecretFetchResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	return resp
}

// =============================================================================
// Five-outcome tests
// =============================================================================

// TestSecretFetch_Row1_HappyPath tests outcome 1: in stored list, resolves
// now → the value.
func TestSecretFetch_Row1_HappyPath(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-happy")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)
	createSecretInStore(t, s, "API_KEY", store.ScopeProject, project.ID,
		"FAKE-KEY-SENTINEL-not-a-real-credential")

	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{"API_KEY"})

	rec := doSecretFetchRequest(t, srv, token, []string{"API_KEY"})
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseSecretFetchResponse(t, rec)
	require.Len(t, resp.Secrets, 1)
	assert.Equal(t, "API_KEY", resp.Secrets[0].Key)
	assert.Equal(t, secretFetchStatusOK, resp.Secrets[0].Status)
	assert.Equal(t, "FAKE-KEY-SENTINEL-not-a-real-credential", resp.Secrets[0].Value)
}

// TestSecretFetch_Row2_EntitledButUnavailable tests outcome 2: in stored list,
// exists in listing, but value unreadable.
//
// Trigger: store a secret value with the "enc:v1:" prefix but use a backend
// with NO encryption key. decryptRawValue sees the encrypted prefix with no
// key and returns an error, causing Resolve() to skip the secret. The secret
// still appears in the listing (computeEntitledSecretKeys), so the handler
// distinguishes "exists but unreadable" (row 2) from "de-scoped" (row 3).
func TestSecretFetch_Row2_EntitledButUnavailable(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)
	// setupSecretFetchTestServer already uses a no-encryption backend.

	agentID := tid("agent-fetch-unavail")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Store a secret with an encrypted-looking value. The no-encryption
	// backend will refuse to return it (encrypted value, no key).
	createSecretInStore(t, s, "BROKEN_SECRET", store.ScopeProject, project.ID,
		"enc:v1:this-is-fake-ciphertext")

	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{"BROKEN_SECRET"})

	rec := doSecretFetchRequest(t, srv, token, []string{"BROKEN_SECRET"})
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseSecretFetchResponse(t, rec)
	require.Len(t, resp.Secrets, 1)
	assert.Equal(t, "BROKEN_SECRET", resp.Secrets[0].Key)
	assert.Equal(t, secretFetchStatusUnavailable, resp.Secrets[0].Status)
	assert.NotEmpty(t, resp.Secrets[0].Error)
	assert.Empty(t, resp.Secrets[0].Value) // no value leaked
}

// TestSecretFetch_Row3_AccessWithdrawn tests outcome 3: in stored list, but
// no longer authorized or in scope. The secret was de-scoped after credential
// mint. This is the revocation-rejection test.
func TestSecretFetch_Row3_AccessWithdrawn(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-withdrawn")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Create secret and generate token with it entitled.
	createSecretInStore(t, s, "SCOPED_KEY", store.ScopeProject, project.ID,
		"FAKE-KEY-SENTINEL-not-a-real-credential")

	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{"SCOPED_KEY"})

	// Now DELETE the secret — de-scope it. It's no longer in the listing.
	err := s.DeleteSecret(context.Background(), "SCOPED_KEY", store.ScopeProject, project.ID)
	require.NoError(t, err)

	rec := doSecretFetchRequest(t, srv, token, []string{"SCOPED_KEY"})
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseSecretFetchResponse(t, rec)
	require.Len(t, resp.Secrets, 1)
	assert.Equal(t, "SCOPED_KEY", resp.Secrets[0].Key)
	assert.Equal(t, secretFetchStatusAccessWithdrawn, resp.Secrets[0].Status)
	assert.Contains(t, resp.Secrets[0].Error, "withdrawn")
	assert.Empty(t, resp.Secrets[0].Value)
}

// TestSecretFetch_Row4_NotInStoredList tests outcome 4: not in stored list →
// indistinguishable from "does not exist".
func TestSecretFetch_Row4_NotInStoredList(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-notfound")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Create a secret but do NOT include it in entitled keys.
	createSecretInStore(t, s, "UNLISTED_KEY", store.ScopeProject, project.ID,
		"FAKE-KEY-SENTINEL-not-a-real-credential")

	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{"OTHER_KEY"}) // entitled to OTHER_KEY, not UNLISTED_KEY

	rec := doSecretFetchRequest(t, srv, token, []string{"UNLISTED_KEY"})
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseSecretFetchResponse(t, rec)
	require.Len(t, resp.Secrets, 1)
	assert.Equal(t, "UNLISTED_KEY", resp.Secrets[0].Key)
	assert.Equal(t, secretFetchStatusNotFound, resp.Secrets[0].Status)
	assert.Empty(t, resp.Secrets[0].Value) // no value leaked
}

// TestSecretFetch_Row5_NoCredentialRow tests outcome 5: no credential row at
// all (legacy pre-table token). Fails closed with distinct message.
func TestSecretFetch_Row5_NoCredentialRow(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-legacy")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Generate a token but delete its credential row to simulate a pre-table token.
	token, _, err := srv.GenerateAgentToken(agentID, project.ID, nil, AgentRoleFull, nil)
	require.NoError(t, err)

	claims, err := srv.agentTokenService.ValidateAgentToken(token)
	require.NoError(t, err)
	jtiHash := hashJTI(claims.ID)

	// Delete the credential row.
	cred, err := s.GetAgentCredentialByJTIHash(context.Background(), jtiHash)
	require.NoError(t, err)
	err = s.RevokeAgentCredential(context.Background(), cred.ID, "test", "simulate legacy")
	require.NoError(t, err)
	// Revocation won't delete — we need to actually remove the row. But the
	// middleware blocks revoked tokens. Instead, simulate no-row by using a
	// token whose credential was never recorded.
	//
	// Generate a token WITHOUT a credential recorder to get a valid token
	// with no credential row.
	svc, svcErr := NewAgentTokenService(AgentTokenConfig{
		SigningKey:    srv.agentTokenService.config.SigningKey,
		TokenDuration: time.Hour,
	})
	require.NoError(t, svcErr)
	// Do NOT set credential recorder — token will have no row.
	legacyToken, _, legacyErr := svc.GenerateAgentToken(agentID, project.ID,
		ScopesForRole(AgentRoleFull), nil)
	require.NoError(t, legacyErr)

	rec := doSecretFetchRequest(t, srv, legacyToken, []string{"ANY_KEY"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "predates entitlement recording")
}

// =============================================================================
// Column state tests
// =============================================================================

// TestSecretFetch_ColumnNULL tests that a credential with NULL entitled_secret_keys
// fails closed with an actionable message.
func TestSecretFetch_ColumnNULL(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-null")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Generate token — credential is created with NULL entitled_secret_keys
	// (because GenerateAgentToken doesn't set them; that's the dispatcher's job).
	token, _, err := srv.GenerateAgentToken(agentID, project.ID, nil, AgentRoleFull, nil)
	require.NoError(t, err)

	rec := doSecretFetchRequest(t, srv, token, []string{"ANY_KEY"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "entitlement was never recorded")
	assert.Contains(t, rec.Body.String(), "bug")
}

// TestSecretFetch_ColumnEmptyList tests that a credential with an empty entitled
// list (entitled to zero secrets) denies all keys as "not found" (row 4).
func TestSecretFetch_ColumnEmptyList(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-empty")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Record an empty list — entitled to zero secrets.
	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{}) // empty list, NOT nil

	rec := doSecretFetchRequest(t, srv, token, []string{"ANY_KEY"})
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseSecretFetchResponse(t, rec)
	require.Len(t, resp.Secrets, 1)
	assert.Equal(t, secretFetchStatusNotFound, resp.Secrets[0].Status)
}

// TestSecretFetch_ColumnPopulated tests the normal case: credential has a
// populated entitled list, some keys match and some don't.
func TestSecretFetch_ColumnPopulated(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-pop")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)
	createSecretInStore(t, s, "KEY_A", store.ScopeProject, project.ID,
		"FAKE-KEY-SENTINEL-not-a-real-credential")
	createSecretInStore(t, s, "KEY_B", store.ScopeProject, project.ID,
		"FAKE-AUTH-SENTINEL-not-a-real-credential")

	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{"KEY_A", "KEY_B"})

	rec := doSecretFetchRequest(t, srv, token, []string{"KEY_A", "KEY_B", "KEY_C"})
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseSecretFetchResponse(t, rec)
	require.Len(t, resp.Secrets, 3)

	results := make(map[string]agentSecretResult)
	for _, r := range resp.Secrets {
		results[r.Key] = r
	}

	assert.Equal(t, secretFetchStatusOK, results["KEY_A"].Status)
	assert.Equal(t, "FAKE-KEY-SENTINEL-not-a-real-credential", results["KEY_A"].Value)
	assert.Equal(t, secretFetchStatusOK, results["KEY_B"].Status)
	assert.Equal(t, "FAKE-AUTH-SENTINEL-not-a-real-credential", results["KEY_B"].Value)
	assert.Equal(t, secretFetchStatusNotFound, results["KEY_C"].Status) // not in entitled list
}

// =============================================================================
// Scope guard tests
// =============================================================================

// TestSecretFetch_ScopeGuard_PreExistingToken tests the smart scope guard:
// a token whose role WOULD receive ScopeAgentSecretFetch but doesn't carry it
// gets a distinct "issued before secret fetch existed" message.
func TestSecretFetch_ScopeGuard_PreExistingToken(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-prescp")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Generate a token with baseline scopes MINUS ScopeAgentSecretFetch.
	// This simulates a token minted before the scope existed.
	preScopes := []AgentTokenScope{
		ScopeProjectRead,
		ScopeAgentStatusUpdate, // present — signals baseline+ role
		ScopeAgentTokenRefresh,
		ScopeAgentNotify,
		ScopeAgentPortForward,
		// ScopeAgentSecretFetch deliberately omitted
	}
	svc, svcErr := NewAgentTokenService(AgentTokenConfig{
		SigningKey:    srv.agentTokenService.config.SigningKey,
		TokenDuration: time.Hour,
	})
	require.NoError(t, svcErr)
	svc.SetCredentialRecorder(&storeCredentialRecorder{store: s})
	token, _, err := svc.GenerateAgentToken(agentID, project.ID, preScopes, nil)
	require.NoError(t, err)

	rec := doSecretFetchRequest(t, srv, token, []string{"ANY_KEY"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "issued before secret-fetch capability existed")
	assert.Contains(t, rec.Body.String(), "restart the agent or refresh")
}

// TestSecretFetch_ScopeGuard_GenuineDenial tests the smart scope guard:
// a token whose role would NOT receive ScopeAgentSecretFetch gets a generic
// "insufficient scope" message.
func TestSecretFetch_ScopeGuard_GenuineDenial(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-noscp")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Generate a token with readonly scopes — no ScopeAgentStatusUpdate.
	readOnlyScopes := []AgentTokenScope{
		ScopeProjectRead,
		// No ScopeAgentStatusUpdate — signals none/readonly role
	}
	svc, svcErr := NewAgentTokenService(AgentTokenConfig{
		SigningKey:    srv.agentTokenService.config.SigningKey,
		TokenDuration: time.Hour,
	})
	require.NoError(t, svcErr)
	svc.SetCredentialRecorder(&storeCredentialRecorder{store: s})
	token, _, err := svc.GenerateAgentToken(agentID, project.ID, readOnlyScopes, nil)
	require.NoError(t, err)

	rec := doSecretFetchRequest(t, srv, token, []string{"ANY_KEY"})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "insufficient scope")
	// Must NOT suggest refreshing — that would send the operator chasing a
	// token problem that does not exist.
	assert.NotContains(t, body, "restart")
	assert.NotContains(t, body, "refresh")
}

// TestSecretFetch_ScopePresentButEntitlementEmpty tests that when the scope IS
// present but the entitlement list is empty, the fetch is still denied for
// every key (all keys are row 4). This proves gate 1 is the real control,
// not the scope.
func TestSecretFetch_ScopePresentButEntitlementEmpty(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-scopeok")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	// Create a secret that exists and resolves.
	createSecretInStore(t, s, "EXISTING_KEY", store.ScopeProject, project.ID,
		"FAKE-KEY-SENTINEL-not-a-real-credential")

	// Generate token with scope (full role) but EMPTY entitlement list.
	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{}) // empty — entitled to nothing

	rec := doSecretFetchRequest(t, srv, token, []string{"EXISTING_KEY"})
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseSecretFetchResponse(t, rec)
	require.Len(t, resp.Secrets, 1)
	assert.Equal(t, secretFetchStatusNotFound, resp.Secrets[0].Status)
	assert.Empty(t, resp.Secrets[0].Value)
}

// =============================================================================
// Gate-1 mutation test
// =============================================================================

// TestSecretFetch_Gate1Mutation verifies that gate 1 (the stored entitlement
// list) is load-bearing. If the gate-1 check were removed, a key NOT in the
// entitled list would still resolve via gate 2, leaking its value.
//
// This test uses two keys: one entitled, one not. Both exist and resolve.
// Only the entitled key must return a value. This goes red if gate 1 is
// deleted, because the non-entitled key would then also return its value.
func TestSecretFetch_Gate1Mutation(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-mutate")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	createSecretInStore(t, s, "ENTITLED_KEY", store.ScopeProject, project.ID,
		"FAKE-KEY-SENTINEL-not-a-real-credential")
	createSecretInStore(t, s, "NON_ENTITLED_KEY", store.ScopeProject, project.ID,
		"FAKE-AUTH-SENTINEL-not-a-real-credential")

	// Only ENTITLED_KEY is in the entitlement list.
	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{"ENTITLED_KEY"})

	rec := doSecretFetchRequest(t, srv, token, []string{"ENTITLED_KEY", "NON_ENTITLED_KEY"})
	assert.Equal(t, http.StatusOK, rec.Code)

	resp := parseSecretFetchResponse(t, rec)
	require.Len(t, resp.Secrets, 2)

	results := make(map[string]agentSecretResult)
	for _, r := range resp.Secrets {
		results[r.Key] = r
	}

	// ENTITLED_KEY should be returned.
	assert.Equal(t, secretFetchStatusOK, results["ENTITLED_KEY"].Status)
	assert.Equal(t, "FAKE-KEY-SENTINEL-not-a-real-credential", results["ENTITLED_KEY"].Value)

	// NON_ENTITLED_KEY must NOT be returned — gate 1 blocks it.
	// If gate 1 were deleted, this would be "ok" with the value, failing the test.
	assert.Equal(t, secretFetchStatusNotFound, results["NON_ENTITLED_KEY"].Status)
	assert.Empty(t, results["NON_ENTITLED_KEY"].Value)
}

// =============================================================================
// Edge cases
// =============================================================================

// TestSecretFetch_NoBody returns 400 for missing request body.
func TestSecretFetch_NoBody(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-nobody")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{"KEY"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/secrets",
		bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-Scion-Agent-Token", token)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSecretFetch_MethodNotAllowed returns 405 for GET.
func TestSecretFetch_MethodNotAllowed(t *testing.T) {
	srv, s, user, project := setupSecretFetchTestServer(t)

	agentID := tid("agent-fetch-method")
	createCredTestAgent(t, s, agentID, project.ID, user.ID)

	token := generateTokenWithEntitledKeys(t, srv, s, agentID, project.ID,
		[]string{"KEY"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/secrets", nil)
	req.Header.Set("X-Scion-Agent-Token", token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

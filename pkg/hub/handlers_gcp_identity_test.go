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
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestGroveForSA(t *testing.T, srv *Server, s store.Store) string {
	t.Helper()
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/groves", map[string]string{
		"name": "test-grove-sa",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "create grove: %s", rec.Body.String())
	var grove store.Grove
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&grove))
	return grove.ID
}

func TestCreateGCPServiceAccount_Success(t *testing.T) {
	srv, s := testServer(t)
	groveID := createTestGroveForSA(t, srv, s)

	body := map[string]string{
		"email":     "agent@my-project.iam.gserviceaccount.com",
		"projectId": "my-project",
	}

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", groveID), body)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var sa store.GCPServiceAccount
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&sa))
	assert.Equal(t, "agent@my-project.iam.gserviceaccount.com", sa.Email)
	assert.Equal(t, "my-project", sa.ProjectID)
	assert.NotEmpty(t, sa.ID)
}

func TestCreateGCPServiceAccount_MissingEmail(t *testing.T) {
	srv, s := testServer(t)
	groveID := createTestGroveForSA(t, srv, s)

	body := map[string]string{
		"projectId": "my-project",
	}

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", groveID), body)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Equal(t, ErrCodeInvalidRequest, errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, "email")
}

func TestCreateGCPServiceAccount_MissingProjectID(t *testing.T) {
	srv, s := testServer(t)
	groveID := createTestGroveForSA(t, srv, s)

	body := map[string]string{
		"email": "agent@my-project.iam.gserviceaccount.com",
	}

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", groveID), body)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Equal(t, ErrCodeInvalidRequest, errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, "projectId")
}

func TestCreateGCPServiceAccount_MissingBothFields(t *testing.T) {
	srv, s := testServer(t)
	groveID := createTestGroveForSA(t, srv, s)

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", groveID), map[string]string{})
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Equal(t, ErrCodeInvalidRequest, errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, "email")
	assert.Contains(t, errResp.Error.Message, "projectId")
}

func TestCreateGCPServiceAccount_InvalidJSON(t *testing.T) {
	srv, s := testServer(t)
	groveID := createTestGroveForSA(t, srv, s)

	rec := doRequestRaw(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", groveID),
		[]byte("not-json"), "application/json")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Equal(t, ErrCodeInvalidRequest, errResp.Error.Code)
	assert.Contains(t, errResp.Error.Message, "invalid request body")
}

func TestCreateGCPServiceAccount_GroveNotFound(t *testing.T) {
	srv, _ := testServer(t)

	body := map[string]string{
		"email":     "agent@my-project.iam.gserviceaccount.com",
		"projectId": "my-project",
	}

	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/groves/nonexistent-grove-id/gcp-service-accounts", body)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCreateGCPServiceAccount_Duplicate(t *testing.T) {
	srv, s := testServer(t)
	groveID := createTestGroveForSA(t, srv, s)

	body := map[string]string{
		"email":     "agent@my-project.iam.gserviceaccount.com",
		"projectId": "my-project",
	}

	// First create should succeed
	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", groveID), body)
	require.Equal(t, http.StatusCreated, rec.Code, "first create: %s", rec.Body.String())

	// Second create with same email should conflict
	rec = doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", groveID), body)
	require.Equal(t, http.StatusConflict, rec.Code)

	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Equal(t, ErrCodeConflict, errResp.Error.Code)
}

// mockGCPServiceAccountAdmin is a test implementation of GCPServiceAccountAdmin.
type mockGCPServiceAccountAdmin struct {
	createErr   error
	policyErr   error
	createdSAs  []string // track created account IDs
	lastEmail   string
	lastProject string
}

func (m *mockGCPServiceAccountAdmin) CreateServiceAccount(_ context.Context, projectID, accountID, _, _ string) (string, string, error) {
	if m.createErr != nil {
		return "", "", m.createErr
	}
	email := fmt.Sprintf("%s@%s.iam.gserviceaccount.com", accountID, projectID)
	m.createdSAs = append(m.createdSAs, accountID)
	m.lastEmail = email
	m.lastProject = projectID
	return email, "unique-id-123", nil
}

func (m *mockGCPServiceAccountAdmin) SetIAMPolicy(_ context.Context, saEmail, _, _ string) error {
	return m.policyErr
}

func testServerWithMinting(t *testing.T) (*Server, store.Store, *mockGCPServiceAccountAdmin) {
	t.Helper()
	srv, s := testServer(t)
	mock := &mockGCPServiceAccountAdmin{}
	srv.SetGCPServiceAccountAdmin(mock)
	srv.SetGCPProjectID("test-hub-project")

	// Set a mock token generator so the hub SA email is available
	srv.SetGCPTokenGenerator(&mockGCPTokenGenerator{email: "hub-sa@test-hub-project.iam.gserviceaccount.com"})

	return srv, s, mock
}

// mockGCPTokenGenerator implements GCPTokenGenerator for testing.
type mockGCPTokenGenerator struct {
	email string
}

func (m *mockGCPTokenGenerator) GenerateAccessToken(_ context.Context, _ string, _ []string) (*GCPAccessToken, error) {
	return &GCPAccessToken{AccessToken: "test-token", ExpiresIn: 3600, TokenType: "Bearer"}, nil
}

func (m *mockGCPTokenGenerator) GenerateIDToken(_ context.Context, _ string, _ string) (*GCPIDToken, error) {
	return &GCPIDToken{Token: "test-id-token"}, nil
}

func (m *mockGCPTokenGenerator) VerifyImpersonation(_ context.Context, _ string) error {
	return nil
}

func (m *mockGCPTokenGenerator) ServiceAccountEmail() string {
	return m.email
}

func TestMintGCPServiceAccount_Success(t *testing.T) {
	srv, _, mock := testServerWithMinting(t)
	groveID := createTestGroveForSA(t, srv, nil)

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
		map[string]string{})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var sa store.GCPServiceAccount
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&sa))
	assert.True(t, sa.Managed)
	assert.True(t, sa.Verified)
	assert.Contains(t, sa.Email, "@test-hub-project.iam.gserviceaccount.com")
	assert.Contains(t, sa.Email, "scion-")
	assert.Equal(t, "test-hub-project", sa.ProjectID)
	assert.Len(t, mock.createdSAs, 1)
}

func TestMintGCPServiceAccount_CustomAccountID(t *testing.T) {
	srv, _, mock := testServerWithMinting(t)
	groveID := createTestGroveForSA(t, srv, nil)

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
		map[string]string{
			"account_id":   "my-pipeline",
			"display_name": "My Pipeline SA",
		})
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var sa store.GCPServiceAccount
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&sa))
	assert.True(t, sa.Managed)
	assert.Equal(t, "scion-my-pipeline@test-hub-project.iam.gserviceaccount.com", sa.Email)
	assert.Equal(t, "My Pipeline SA", sa.DisplayName)
	assert.Equal(t, "scion-my-pipeline", mock.createdSAs[0])
}

func TestMintGCPServiceAccount_AccountIDTooLong(t *testing.T) {
	srv, _, _ := testServerWithMinting(t)
	groveID := createTestGroveForSA(t, srv, nil)

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
		map[string]string{
			"account_id": "this-is-a-very-long-account-id-that-exceeds",
		})
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Equal(t, ErrCodeValidationError, errResp.Error.Code)
}

func TestMintGCPServiceAccount_NotConfigured(t *testing.T) {
	srv, _ := testServer(t) // No minting configured
	groveID := createTestGroveForSA(t, srv, nil)

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
		map[string]string{})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestMintGCPServiceAccount_GroveNotFound(t *testing.T) {
	srv, _, _ := testServerWithMinting(t)

	rec := doRequest(t, srv, http.MethodPost,
		"/api/v1/groves/nonexistent-grove-id/gcp-service-accounts/mint",
		map[string]string{})
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMintGCPServiceAccount_NoAuth(t *testing.T) {
	srv, _, _ := testServerWithMinting(t)
	groveID := createTestGroveForSA(t, srv, nil)

	rec := doRequestNoAuth(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
		map[string]string{})
	// Should be forbidden without auth
	assert.True(t, rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden,
		"expected 401 or 403, got %d", rec.Code)
}

func TestMintGCPServiceAccount_PerGroveCap(t *testing.T) {
	srv, _, _ := testServerWithMinting(t)
	srv.config.GCPMintCapPerGrove = 2
	groveID := createTestGroveForSA(t, srv, nil)

	// Mint first two — should succeed
	for i := 0; i < 2; i++ {
		rec := doRequest(t, srv, http.MethodPost,
			fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
			map[string]string{})
		require.Equal(t, http.StatusCreated, rec.Code, "mint %d: %s", i+1, rec.Body.String())
	}

	// Third mint should be rejected
	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
		map[string]string{})
	require.Equal(t, http.StatusConflict, rec.Code, "expected cap enforcement: %s", rec.Body.String())

	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error.Message, "per-grove mint limit")
}

func TestMintGCPServiceAccount_GlobalCap(t *testing.T) {
	srv, _, _ := testServerWithMinting(t)
	srv.config.GCPMintCapGlobal = 3

	// Create two groves and mint in each
	groveID1 := createTestGroveForSA(t, srv, nil)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/groves", map[string]string{
		"name": "test-grove-sa-2",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var grove2 struct{ ID string }
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&grove2))
	groveID2 := grove2.ID

	// Mint 2 in grove 1, 1 in grove 2 (total 3)
	for i := 0; i < 2; i++ {
		rec := doRequest(t, srv, http.MethodPost,
			fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID1),
			map[string]string{})
		require.Equal(t, http.StatusCreated, rec.Code)
	}
	rec = doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID2),
		map[string]string{})
	require.Equal(t, http.StatusCreated, rec.Code)

	// Fourth mint (in either grove) should be rejected
	rec = doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID2),
		map[string]string{})
	require.Equal(t, http.StatusConflict, rec.Code)

	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error.Message, "global mint limit")
}

func TestListGCPServiceAccounts_IncludesMintQuota(t *testing.T) {
	srv, _, _ := testServerWithMinting(t)
	srv.config.GCPMintCapPerGrove = 5
	srv.config.GCPMintCapGlobal = 10
	groveID := createTestGroveForSA(t, srv, nil)

	// Mint one SA
	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
		map[string]string{})
	require.Equal(t, http.StatusCreated, rec.Code)

	// List should include quota info
	rec = doRequest(t, srv, http.MethodGet,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", groveID), nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Items     []json.RawMessage `json:"items"`
		MintQuota *struct {
			GroveMinted  int `json:"grove_minted"`
			GroveCap     int `json:"grove_cap"`
			GlobalMinted int `json:"global_minted"`
			GlobalCap    int `json:"global_cap"`
		} `json:"mint_quota"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.NotNil(t, resp.MintQuota, "mint_quota should be present")
	assert.Equal(t, 1, resp.MintQuota.GroveMinted)
	assert.Equal(t, 5, resp.MintQuota.GroveCap)
	assert.Equal(t, 1, resp.MintQuota.GlobalMinted)
	assert.Equal(t, 10, resp.MintQuota.GlobalCap)
}

func TestMintGCPServiceAccount_ManagedFlagSet(t *testing.T) {
	srv, _, _ := testServerWithMinting(t)
	groveID := createTestGroveForSA(t, srv, nil)

	rec := doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID),
		map[string]string{})
	require.Equal(t, http.StatusCreated, rec.Code)

	var sa struct {
		Managed   bool   `json:"managed"`
		ManagedBy string `json:"managedBy"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&sa))
	assert.True(t, sa.Managed)
}

func TestMintGCPServiceAccount_PerGroveCap_DifferentGroves(t *testing.T) {
	srv, _, _ := testServerWithMinting(t)
	srv.config.GCPMintCapPerGrove = 1

	groveID1 := createTestGroveForSA(t, srv, nil)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/groves", map[string]string{
		"name": "test-grove-sa-3",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var grove2 struct{ ID string }
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&grove2))

	// Mint in grove 1 — should succeed
	rec = doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID1),
		map[string]string{})
	require.Equal(t, http.StatusCreated, rec.Code)

	// Mint in grove 2 — should also succeed (different grove)
	rec = doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", grove2.ID),
		map[string]string{})
	require.Equal(t, http.StatusCreated, rec.Code)

	// Second mint in grove 1 — should be rejected
	rec = doRequest(t, srv, http.MethodPost,
		fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts/mint", groveID1),
		map[string]string{})
	require.Equal(t, http.StatusConflict, rec.Code)
}

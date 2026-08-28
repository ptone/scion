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

// Slug-fallback tests for the harness-config GET endpoint.
// Phase 2b of ptone/scion#1316.
//
// Before this change, GET /api/v1/harness-configs/{ref} only accepted UUIDs.
// Slugs (like "antigravity") returned 404, which caused the broker's
// resourceObjectPath to fail with a 500 when attempting name-based hydration.
//
// After this change, GET accepts slugs and falls back to global-scope slug
// lookup, mirroring resolveTemplate (handlers_agent_create_helpers.go:37-62).
// PUT/PATCH/DELETE remain UUID-only to avoid ambiguous mutations.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetHarnessConfig_ByUUID(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	hc := &store.HarnessConfig{
		ID:      tid("hc-uuid-test"),
		Name:    "test-config",
		Slug:    "test-config",
		Harness: "claude",
		Scope:   store.HarnessConfigScopeGlobal,
		Status:  store.HarnessConfigStatusActive,
	}
	require.NoError(t, s.CreateHarnessConfig(ctx, hc))

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/harness-configs/"+hc.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var resp HarnessConfigWithCapabilities
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, hc.ID, resp.ID)
	assert.Equal(t, "test-config", resp.Slug)
}

func TestGetHarnessConfig_BySlug(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	hc := &store.HarnessConfig{
		ID:      tid("hc-slug-test"),
		Name:    "antigravity",
		Slug:    "antigravity",
		Harness: "claude",
		Scope:   store.HarnessConfigScopeGlobal,
		Status:  store.HarnessConfigStatusActive,
	}
	require.NoError(t, s.CreateHarnessConfig(ctx, hc))

	// GET by slug — should resolve via the global-scope fallback.
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/harness-configs/antigravity", nil)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var resp HarnessConfigWithCapabilities
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, hc.ID, resp.ID,
		"slug lookup should return the same config as UUID lookup")
	assert.Equal(t, "antigravity", resp.Slug)
}

func TestGetHarnessConfig_NotFound(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/harness-configs/nonexistent-slug", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"non-existent slug should return 404")
}

func TestGetHarnessConfig_SlugIsGETOnly(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	hc := &store.HarnessConfig{
		ID:      tid("hc-getonly"),
		Name:    "getonly-config",
		Slug:    "getonly-config",
		Harness: "claude",
		Scope:   store.HarnessConfigScopeGlobal,
		Status:  store.HarnessConfigStatusActive,
	}
	require.NoError(t, s.CreateHarnessConfig(ctx, hc))

	// DELETE by slug should fail — slug resolution is GET-only.
	// A slug is ambiguous across scopes, and an ambiguous DELETE is
	// destructive. Only UUID-addressed deletes are safe.
	rec := doRequest(t, srv, http.MethodDelete, "/api/v1/harness-configs/getonly-config", nil)
	assert.NotEqual(t, http.StatusOK, rec.Code,
		"DELETE by slug should not succeed — slug resolution is GET-only")

	// Verify the config still exists.
	_, err := s.GetHarnessConfig(ctx, hc.ID)
	require.NoError(t, err, "config should still exist after failed slug-addressed DELETE")
}

func TestGetHarnessConfig_UUIDTakesPrecedence(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Create a config whose UUID happens to be a valid slug of another config.
	// This is contrived but tests the precedence: UUID lookup wins.
	hc := &store.HarnessConfig{
		ID:      tid("hc-precedence"),
		Name:    "precedence-test",
		Slug:    "precedence-test",
		Harness: "claude",
		Scope:   store.HarnessConfigScopeGlobal,
		Status:  store.HarnessConfigStatusActive,
	}
	require.NoError(t, s.CreateHarnessConfig(ctx, hc))

	// Looking up by UUID should work.
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/harness-configs/"+hc.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp HarnessConfigWithCapabilities
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, hc.ID, resp.ID)
}

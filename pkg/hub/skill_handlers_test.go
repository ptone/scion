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
	"net/http"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

func TestSkillCRUD(t *testing.T) {
	srv, s := testServer(t)

	// Create a skill
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/skills", CreateSkillRequest{
		Name:        "test-skill",
		DisplayName: "Test Skill",
		Description: "A test skill",
		Scope:       "global",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var created store.Skill
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Create: failed to decode response: %v", err)
	}
	if created.Name != "test-skill" {
		t.Errorf("Create: expected name 'test-skill', got %q", created.Name)
	}
	if created.Status != store.SkillStatusActive {
		t.Errorf("Create: expected status 'active', got %q", created.Status)
	}
	if created.ID == "" {
		t.Error("Create: expected non-empty ID")
	}

	// Get the skill
	rec = doRequest(t, srv, http.MethodGet, "/api/v1/skills/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("Get: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var got store.Skill
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("Get: failed to decode response: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("Get: expected ID %q, got %q", created.ID, got.ID)
	}

	// Update the skill
	rec = doRequest(t, srv, http.MethodPut, "/api/v1/skills/"+created.ID, UpdateSkillRequest{
		Description: "Updated description",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated store.Skill
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("Update: failed to decode response: %v", err)
	}
	if updated.Description != "Updated description" {
		t.Errorf("Update: expected description 'Updated description', got %q", updated.Description)
	}

	// List skills
	rec = doRequest(t, srv, http.MethodGet, "/api/v1/skills", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("List: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var listResp ListSkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("List: failed to decode response: %v", err)
	}
	if len(listResp.Skills) != 1 {
		t.Errorf("List: expected 1 skill, got %d", len(listResp.Skills))
	}

	// Delete the skill
	rec = doRequest(t, srv, http.MethodDelete, "/api/v1/skills/"+created.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Delete: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify deleted
	rec = doRequest(t, srv, http.MethodGet, "/api/v1/skills/"+created.ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("Get after delete: expected 404, got %d", rec.Code)
	}

	// Suppress unused import warning
	_ = s
}

func TestSkillCreate_Validation(t *testing.T) {
	srv, _ := testServer(t)

	// Missing name
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/skills", CreateSkillRequest{
		Scope: "global",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 422 for missing name, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSkillCreate_DefaultScope(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/skills", CreateSkillRequest{
		Name: "scoped-skill",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var created store.Skill
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if created.Scope != store.SkillScopeGlobal {
		t.Errorf("expected scope 'global', got %q", created.Scope)
	}
}

func TestSkillList_FilterByScope(t *testing.T) {
	srv, _ := testServer(t)

	// Create global skill
	doRequest(t, srv, http.MethodPost, "/api/v1/skills", CreateSkillRequest{
		Name:  "global-skill",
		Scope: "global",
	})
	// Create project skill
	doRequest(t, srv, http.MethodPost, "/api/v1/skills", CreateSkillRequest{
		Name:    "project-skill",
		Scope:   "project",
		ScopeID: "proj-1",
	})

	// List only global skills
	rec := doRequest(t, srv, http.MethodGet, "/api/v1/skills?scope=global", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ListSkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(resp.Skills) != 1 {
		t.Errorf("expected 1 global skill, got %d", len(resp.Skills))
	}
	if len(resp.Skills) > 0 && resp.Skills[0].Name != "global-skill" {
		t.Errorf("expected 'global-skill', got %q", resp.Skills[0].Name)
	}
}

func TestSkillMethodNotAllowed(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPatch, "/api/v1/skills", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestSkillVersionLifecycle(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Create a skill via the store directly for setup
	skill := &store.Skill{
		ID:     "skill-v1",
		Name:   "versioned-skill",
		Scope:  "global",
		Status: store.SkillStatusActive,
	}
	if err := s.CreateSkill(ctx, skill); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	// Publish a version
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/skills/skill-v1/versions", PublishVersionRequest{
		Version:   "1.0.0",
		Changelog: "Initial release",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("PublishVersion: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var pvResp PublishVersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pvResp); err != nil {
		t.Fatalf("PublishVersion: failed to decode: %v", err)
	}
	if pvResp.Version == nil {
		t.Fatal("PublishVersion: version is nil")
	}
	if pvResp.Version.Version != "1.0.0" {
		t.Errorf("PublishVersion: expected version '1.0.0', got %q", pvResp.Version.Version)
	}

	// List versions
	rec = doRequest(t, srv, http.MethodGet, "/api/v1/skills/skill-v1/versions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListVersions: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var versionsResp struct {
		Versions []store.SkillVersion `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &versionsResp); err != nil {
		t.Fatalf("ListVersions: failed to decode: %v", err)
	}
	if len(versionsResp.Versions) != 1 {
		t.Errorf("ListVersions: expected 1 version, got %d", len(versionsResp.Versions))
	}

	// Get specific version
	rec = doRequest(t, srv, http.MethodGet, "/api/v1/skills/skill-v1/versions/1.0.0", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetVersion: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSkillVersionPublish_Validation(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	skill := &store.Skill{
		ID:     "skill-val",
		Name:   "val-skill",
		Scope:  "global",
		Status: store.SkillStatusActive,
	}
	if err := s.CreateSkill(ctx, skill); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	// Missing version
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/skills/skill-val/versions", PublishVersionRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 422 for missing version, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSkillResolve(t *testing.T) {
	srv, s := testServer(t)
	ctx := context.Background()

	// Create a skill and version
	skill := &store.Skill{
		ID:     "skill-resolve",
		Name:   "scion",
		Scope:  "global",
		Status: store.SkillStatusActive,
	}
	if err := s.CreateSkill(ctx, skill); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	version := &store.SkillVersion{
		ID:          "ver-1",
		SkillID:     "skill-resolve",
		Version:     "1.0.0",
		ContentHash: "sha256:abc123",
		Status:      store.SkillVersionStatusPublished,
		Files: []store.SkillFile{
			{Path: "SKILL.md", Size: 100, Hash: "sha256:def456"},
		},
	}
	if err := s.CreateSkillVersion(ctx, version); err != nil {
		t.Fatalf("CreateSkillVersion: %v", err)
	}

	// Resolve
	rec := doRequest(t, srv, http.MethodPost, "/api/v1/skills/resolve", SkillResolveRequest{
		Skills: []SkillResolveRef{
			{URI: "scion"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Resolve: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SkillResolveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Resolve: failed to decode: %v", err)
	}

	if len(resp.Resolved) != 1 {
		t.Fatalf("Resolve: expected 1 resolved skill, got %d", len(resp.Resolved))
	}
	if resp.Resolved[0].Name != "scion" {
		t.Errorf("Resolve: expected name 'scion', got %q", resp.Resolved[0].Name)
	}
	if resp.Resolved[0].ResolvedVersion != "1.0.0" {
		t.Errorf("Resolve: expected version '1.0.0', got %q", resp.Resolved[0].ResolvedVersion)
	}
}

func TestSkillResolve_NotFound(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodPost, "/api/v1/skills/resolve", SkillResolveRequest{
		Skills: []SkillResolveRef{
			{URI: "nonexistent-skill"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("Resolve: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp SkillResolveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Resolve: failed to decode: %v", err)
	}

	if len(resp.Errors) != 1 {
		t.Fatalf("Resolve: expected 1 error, got %d", len(resp.Errors))
	}
	if resp.Errors[0].Error != "skill not found" {
		t.Errorf("Resolve: expected 'skill not found', got %q", resp.Errors[0].Error)
	}
}

func TestSkillResolve_MethodNotAllowed(t *testing.T) {
	srv, _ := testServer(t)

	rec := doRequest(t, srv, http.MethodGet, "/api/v1/skills/resolve", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestParseSkillURI(t *testing.T) {
	tests := []struct {
		uri       string
		wantName  string
		wantScope string
		wantVer   string
	}{
		{
			uri:       "scion",
			wantName:  "scion",
			wantScope: "global",
			wantVer:   "",
		},
		{
			uri:       "skill://scion/global/scion@1.0.0",
			wantName:  "scion",
			wantScope: "global",
			wantVer:   "1.0.0",
		},
		{
			uri:       "skill://scion/project/my-skill@^1.0",
			wantName:  "my-skill",
			wantScope: "project",
			wantVer:   "^1.0",
		},
		{
			uri:       "skill:///global/test@latest",
			wantName:  "test",
			wantScope: "global",
			wantVer:   "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			name, scope, _, version := parseSkillURI(tt.uri, "proj-1", "user-1")
			if name != tt.wantName {
				t.Errorf("name: got %q, want %q", name, tt.wantName)
			}
			if scope != tt.wantScope {
				t.Errorf("scope: got %q, want %q", scope, tt.wantScope)
			}
			if version != tt.wantVer {
				t.Errorf("version: got %q, want %q", version, tt.wantVer)
			}
		})
	}
}

func TestComputeSkillContentHash(t *testing.T) {
	files := []store.SkillFile{
		{Path: "scripts/a.sh", Hash: "sha256:aaa"},
		{Path: "SKILL.md", Hash: "sha256:bbb"},
	}

	hash1 := computeSkillContentHash(files)
	if hash1 == "" {
		t.Fatal("expected non-empty hash")
	}
	if !containsPrefix(hash1, "sha256:") {
		t.Errorf("expected hash to start with 'sha256:', got %q", hash1)
	}

	// Same files in different order should produce the same hash
	reversed := []store.SkillFile{
		{Path: "SKILL.md", Hash: "sha256:bbb"},
		{Path: "scripts/a.sh", Hash: "sha256:aaa"},
	}
	hash2 := computeSkillContentHash(reversed)
	if hash1 != hash2 {
		t.Errorf("expected same hash for same files in different order: %q != %q", hash1, hash2)
	}
}

func containsPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

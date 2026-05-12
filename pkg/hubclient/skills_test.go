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

package hubclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSkillService_List(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills" {
			t.Errorf("expected path /api/v1/skills, got %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		// Validate query params
		if r.URL.Query().Get("scope") != "global" {
			t.Errorf("expected scope=global, got %s", r.URL.Query().Get("scope"))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"skills": []Skill{
				{ID: "s1", Name: "test-skill", Scope: "global", Status: "active"},
			},
			"totalCount": 1,
		})
	}))
	defer server.Close()

	c, _ := New(server.URL)
	resp, err := c.Skills().List(context.Background(), &ListSkillsOptions{
		Scope: "global",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Skills) != 1 {
		t.Errorf("expected 1 skill, got %d", len(resp.Skills))
	}
	if resp.Skills[0].Name != "test-skill" {
		t.Errorf("expected name 'test-skill', got %q", resp.Skills[0].Name)
	}
}

func TestSkillService_Get(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills/skill-1" {
			t.Errorf("expected path /api/v1/skills/skill-1, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Skill{
			ID:   "skill-1",
			Name: "test-skill",
		})
	}))
	defer server.Close()

	c, _ := New(server.URL)
	skill, err := c.Skills().Get(context.Background(), "skill-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if skill.ID != "skill-1" {
		t.Errorf("expected ID 'skill-1', got %q", skill.ID)
	}
}

func TestSkillService_Create(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req CreateSkillRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Name != "new-skill" {
			t.Errorf("expected name 'new-skill', got %q", req.Name)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(CreateSkillResponse{
			Skill: &Skill{
				ID:   "created-id",
				Name: "new-skill",
			},
		})
	}))
	defer server.Close()

	c, _ := New(server.URL)
	resp, err := c.Skills().Create(context.Background(), &CreateSkillRequest{
		Name:  "new-skill",
		Scope: "global",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if resp.Skill == nil {
		t.Fatal("expected non-nil skill in response")
	}
	if resp.Skill.ID != "created-id" {
		t.Errorf("expected ID 'created-id', got %q", resp.Skill.ID)
	}
}

func TestSkillService_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c, _ := New(server.URL)
	err := c.Skills().Delete(context.Background(), "skill-1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestSkillService_PublishVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills/skill-1/versions" {
			t.Errorf("expected path /api/v1/skills/skill-1/versions, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(SkillVersion{
			ID:      "ver-1",
			SkillID: "skill-1",
			Version: "1.0.0",
			Status:  "published",
		})
	}))
	defer server.Close()

	c, _ := New(server.URL)
	ver, err := c.Skills().PublishVersion(context.Background(), "skill-1", &PublishVersionRequest{
		Version: "1.0.0",
	})
	if err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}
	if ver.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", ver.Version)
	}
}

func TestSkillService_ListVersions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills/skill-1/versions" {
			t.Errorf("expected path /api/v1/skills/skill-1/versions, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ListVersionsResponse{
			Versions: []SkillVersion{
				{ID: "ver-1", Version: "1.0.0"},
			},
		})
	}))
	defer server.Close()

	c, _ := New(server.URL)
	resp, err := c.Skills().ListVersions(context.Background(), "skill-1")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(resp.Versions) != 1 {
		t.Errorf("expected 1 version, got %d", len(resp.Versions))
	}
}

func TestSkillService_Resolve(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/skills/resolve" {
			t.Errorf("expected path /api/v1/skills/resolve, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ResolveSkillsResponse{
			Resolved: []ResolvedSkill{
				{
					URI:             "skill://scion/global/scion@^1.0",
					Name:            "scion",
					ResolvedVersion: "1.3.2",
					ContentHash:     "sha256:abc123",
					Files: []DownloadURLInfo{
						{
							Path: "SKILL.md",
							URL:  "https://storage.example.com/signed",
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	c, _ := New(server.URL)
	resp, err := c.Skills().Resolve(context.Background(), &ResolveSkillsRequest{
		Skills: []SkillRef{
			{URI: "skill://scion/global/scion@^1.0"},
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resp.Resolved) != 1 {
		t.Fatalf("expected 1 resolved, got %d", len(resp.Resolved))
	}
	if resp.Resolved[0].Name != "scion" {
		t.Errorf("expected name 'scion', got %q", resp.Resolved[0].Name)
	}
	if resp.Resolved[0].ResolvedVersion != "1.3.2" {
		t.Errorf("expected version '1.3.2', got %q", resp.Resolved[0].ResolvedVersion)
	}
}

func TestSkillService_Skills_Accessor(t *testing.T) {
	c, err := New("https://hub.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Skills() == nil {
		t.Error("expected non-nil skills service")
	}
}

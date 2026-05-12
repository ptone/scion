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

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
)

// ---------- ParseSkillURI tests ----------

func TestParseSkillURI_FullForm(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected SkillURI
	}{
		{
			name:  "full URI with version",
			input: "skill://scion/core/scion@^1.0",
			expected: SkillURI{
				Registry: "scion",
				Scope:    "core",
				Name:     "scion",
				Version:  "^1.0",
			},
		},
		{
			name:  "full URI exact version",
			input: "skill://scion/core/security-audit@1.2.3",
			expected: SkillURI{
				Registry: "scion",
				Scope:    "core",
				Name:     "security-audit",
				Version:  "1.2.3",
			},
		},
		{
			name:  "full URI no version",
			input: "skill://scion/core/scion",
			expected: SkillURI{
				Registry: "scion",
				Scope:    "core",
				Name:     "scion",
				Version:  "latest",
			},
		},
		{
			name:  "external registry",
			input: "skill://registry.agentskills.io/global/my-skill@~1.2",
			expected: SkillURI{
				Registry: "registry.agentskills.io",
				Scope:    "global",
				Name:     "my-skill",
				Version:  "~1.2",
			},
		},
		{
			name:  "omitted registry",
			input: "skill:///core/scion@^1.0",
			expected: SkillURI{
				Registry: "scion", // defaults to "scion"
				Scope:    "core",
				Name:     "scion",
				Version:  "^1.0",
			},
		},
		{
			name:  "project scope",
			input: "skill://scion/project/my-proj/custom-skill@latest",
			expected: SkillURI{
				Registry: "scion",
				Scope:    "project",
				Name:     "my-proj/custom-skill",
				Version:  "latest",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSkillURI(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Registry != tt.expected.Registry {
				t.Errorf("Registry: got %q, want %q", got.Registry, tt.expected.Registry)
			}
			if got.Scope != tt.expected.Scope {
				t.Errorf("Scope: got %q, want %q", got.Scope, tt.expected.Scope)
			}
			if got.Name != tt.expected.Name {
				t.Errorf("Name: got %q, want %q", got.Name, tt.expected.Name)
			}
			if got.Version != tt.expected.Version {
				t.Errorf("Version: got %q, want %q", got.Version, tt.expected.Version)
			}
		})
	}
}

func TestParseSkillURI_BareName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected SkillURI
	}{
		{
			name:  "bare name",
			input: "scion",
			expected: SkillURI{
				Registry: "scion",
				Name:     "scion",
				Version:  "latest",
			},
		},
		{
			name:  "bare name with version",
			input: "security-audit@1.2.0",
			expected: SkillURI{
				Registry: "scion",
				Name:     "security-audit",
				Version:  "1.2.0",
			},
		},
		{
			name:  "bare name with caret version",
			input: "my-skill@^2.0",
			expected: SkillURI{
				Registry: "scion",
				Name:     "my-skill",
				Version:  "^2.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSkillURI(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Registry != tt.expected.Registry {
				t.Errorf("Registry: got %q, want %q", got.Registry, tt.expected.Registry)
			}
			if got.Name != tt.expected.Name {
				t.Errorf("Name: got %q, want %q", got.Name, tt.expected.Name)
			}
			if got.Version != tt.expected.Version {
				t.Errorf("Version: got %q, want %q", got.Version, tt.expected.Version)
			}
		})
	}
}

func TestParseSkillURI_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty string", input: ""},
		{name: "scheme only", input: "skill://"},
		{name: "scheme with empty path", input: "skill:///"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSkillURI(tt.input)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestSkillURI_String(t *testing.T) {
	tests := []struct {
		name     string
		uri      SkillURI
		expected string
	}{
		{
			name: "full form",
			uri: SkillURI{
				Registry: "scion",
				Scope:    "core",
				Name:     "scion",
				Version:  "^1.0",
			},
			expected: "skill://scion/core/scion@^1.0",
		},
		{
			name: "no version (latest)",
			uri: SkillURI{
				Registry: "scion",
				Scope:    "core",
				Name:     "scion",
				Version:  "latest",
			},
			expected: "skill://scion/core/scion",
		},
		{
			name: "no scope",
			uri: SkillURI{
				Registry: "scion",
				Name:     "my-skill",
				Version:  "1.0.0",
			},
			expected: "skill://scion/my-skill@1.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.uri.String()
			if got != tt.expected {
				t.Errorf("String(): got %q, want %q", got, tt.expected)
			}
		})
	}
}

// ---------- skillRefToURI tests ----------

func TestSkillRefToURI(t *testing.T) {
	tests := []struct {
		name     string
		ref      api.SkillReference
		expected string
	}{
		{
			name:     "URI takes precedence",
			ref:      api.SkillReference{URI: "skill://scion/core/scion@^1.0", Name: "ignored"},
			expected: "skill://scion/core/scion@^1.0",
		},
		{
			name:     "name only",
			ref:      api.SkillReference{Name: "scion"},
			expected: "scion",
		},
		{
			name:     "name with version",
			ref:      api.SkillReference{Name: "security-audit", Version: "^1.0"},
			expected: "security-audit@^1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := skillRefToURI(tt.ref)
			if got != tt.expected {
				t.Errorf("skillRefToURI: got %q, want %q", got, tt.expected)
			}
		})
	}
}

// ---------- context helpers tests ----------

func TestContextHubClient(t *testing.T) {
	ctx := context.Background()

	// No hub client in context
	if got := hubClientFromContext(ctx); got != nil {
		t.Errorf("expected nil, got %v", got)
	}

	// With hub client
	mock := &mockHubClient{}
	ctx = ContextWithHubClient(ctx, mock)
	got := hubClientFromContext(ctx)
	if got == nil {
		t.Fatal("expected hub client, got nil")
	}
	if got != mock {
		t.Errorf("got different client than expected")
	}
}

// ---------- resolveSkillReferences tests ----------

func TestResolveSkillReferences_NoRefsNoOp(t *testing.T) {
	records, err := resolveSkillReferences(context.Background(), nil, nil, "/tmp/test", "", "")
	if err != nil {
		t.Fatalf("expected nil error for empty refs, got: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected no records, got %d", len(records))
	}
}

func TestResolveSkillReferences_NoClientNoOp(t *testing.T) {
	refs := []api.SkillReference{{Name: "test-skill"}}
	records, err := resolveSkillReferences(context.Background(), nil, refs, "/tmp/test", "", "")
	if err != nil {
		t.Fatalf("expected nil error with nil client, got: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected no records, got %d", len(records))
	}
}

func TestResolveSkillReferences_RequiredSkillError(t *testing.T) {
	mock := &mockHubClient{
		skillsSvc: &mockSkillService{
			resolveResp: &hubclient.ResolveSkillsResponse{
				Errors: []hubclient.ResolveError{
					{URI: "missing-skill", Error: "skill not found"},
				},
			},
		},
	}

	refs := []api.SkillReference{
		{Name: "missing-skill", Optional: false},
	}

	destDir := t.TempDir()
	_, err := resolveSkillReferences(context.Background(), mock, refs, destDir, "", "")
	if err == nil {
		t.Fatal("expected error for required skill that fails resolution")
	}
	if got := err.Error(); got != "skill missing-skill: skill not found" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestResolveSkillReferences_OptionalSkillErrorSkipped(t *testing.T) {
	mock := &mockHubClient{
		skillsSvc: &mockSkillService{
			resolveResp: &hubclient.ResolveSkillsResponse{
				Errors: []hubclient.ResolveError{
					{URI: "optional-skill", Error: "skill not found"},
				},
			},
		},
	}

	refs := []api.SkillReference{
		{Name: "optional-skill", Optional: true},
	}

	destDir := t.TempDir()
	_, err := resolveSkillReferences(context.Background(), mock, refs, destDir, "", "")
	if err != nil {
		t.Fatalf("expected nil error for optional skill failure, got: %v", err)
	}
}

func TestResolveSkillReferences_SuccessfulDownload(t *testing.T) {
	skillContent := []byte("# SKILL.md\nname: test-skill\n---\nTest skill instructions")
	contentHash := transfer.HashBytes(skillContent)

	mock := &mockHubClient{
		skillsSvc: &mockSkillService{
			resolveResp: &hubclient.ResolveSkillsResponse{
				Resolved: []hubclient.ResolvedSkill{
					{
						URI:             "test-skill",
						Name:            "test-skill",
						ResolvedVersion: "1.0.0",
						ContentHash:     contentHash,
						Files: []hubclient.DownloadURLInfo{
							{
								Path: "SKILL.md",
								URL:  "https://storage.example.com/skill.md",
								Hash: contentHash,
							},
						},
					},
				},
			},
			downloadData: map[string][]byte{
				"https://storage.example.com/skill.md": skillContent,
			},
		},
	}

	refs := []api.SkillReference{
		{Name: "test-skill"},
	}

	destDir := t.TempDir()
	records, err := resolveSkillReferences(context.Background(), mock, refs, destDir, "proj-123", "user-456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file was written
	skillPath := filepath.Join(destDir, "test-skill", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("failed to read skill file: %v", err)
	}
	if string(data) != string(skillContent) {
		t.Errorf("skill file content mismatch: got %q, want %q", string(data), string(skillContent))
	}

	// Verify returned records
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.Name != "test-skill" {
		t.Errorf("record.Name=%q want test-skill", rec.Name)
	}
	if rec.URI != "test-skill" {
		t.Errorf("record.URI=%q want test-skill", rec.URI)
	}
	if rec.ResolvedVersion != "1.0.0" {
		t.Errorf("record.ResolvedVersion=%q want 1.0.0", rec.ResolvedVersion)
	}
	if rec.ContentHash != contentHash {
		t.Errorf("record.ContentHash=%q want %q", rec.ContentHash, contentHash)
	}
	if rec.Source != "registry" {
		t.Errorf("record.Source=%q want registry", rec.Source)
	}
	if rec.InstalledPath != filepath.Join(destDir, "test-skill") {
		t.Errorf("record.InstalledPath=%q want %q", rec.InstalledPath, filepath.Join(destDir, "test-skill"))
	}
}

func TestResolveSkillReferences_AsRename(t *testing.T) {
	skillContent := []byte("# SKILL.md\nTest content")
	contentHash := transfer.HashBytes(skillContent)

	mock := &mockHubClient{
		skillsSvc: &mockSkillService{
			resolveResp: &hubclient.ResolveSkillsResponse{
				Resolved: []hubclient.ResolvedSkill{
					{
						URI:             "my-skill",
						Name:            "my-skill",
						ResolvedVersion: "1.0.0",
						Files: []hubclient.DownloadURLInfo{
							{
								Path: "SKILL.md",
								URL:  "https://storage.example.com/skill.md",
								Hash: contentHash,
							},
						},
					},
				},
			},
			downloadData: map[string][]byte{
				"https://storage.example.com/skill.md": skillContent,
			},
		},
	}

	refs := []api.SkillReference{
		{Name: "my-skill", As: "custom-name"},
	}

	destDir := t.TempDir()
	records, err := resolveSkillReferences(context.Background(), mock, refs, destDir, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file was written under custom name
	skillPath := filepath.Join(destDir, "custom-name", "SKILL.md")
	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		t.Errorf("expected skill file at %s, but it doesn't exist", skillPath)
	}

	// Record should use the "as" name
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Name != "custom-name" {
		t.Errorf("record.Name=%q want custom-name", records[0].Name)
	}
}

func TestResolveSkillReferences_HashMismatch(t *testing.T) {
	skillContent := []byte("# SKILL.md\nTest content")

	mock := &mockHubClient{
		skillsSvc: &mockSkillService{
			resolveResp: &hubclient.ResolveSkillsResponse{
				Resolved: []hubclient.ResolvedSkill{
					{
						URI:             "bad-hash-skill",
						Name:            "bad-hash-skill",
						ResolvedVersion: "1.0.0",
						Files: []hubclient.DownloadURLInfo{
							{
								Path: "SKILL.md",
								URL:  "https://storage.example.com/skill.md",
								Hash: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
							},
						},
					},
				},
			},
			downloadData: map[string][]byte{
				"https://storage.example.com/skill.md": skillContent,
			},
		},
	}

	refs := []api.SkillReference{
		{Name: "bad-hash-skill"},
	}

	destDir := t.TempDir()
	_, err := resolveSkillReferences(context.Background(), mock, refs, destDir, "", "")
	if err == nil {
		t.Fatal("expected error for hash mismatch")
	}
	if got := err.Error(); !contains(got, "hash mismatch") {
		t.Errorf("expected hash mismatch error, got: %s", got)
	}
}

func TestResolveSkillReferences_NestedFiles(t *testing.T) {
	skillMD := []byte("# SKILL.md\nTest content")
	scriptSH := []byte("#!/bin/bash\necho hello")
	hashMD := transfer.HashBytes(skillMD)
	hashSH := transfer.HashBytes(scriptSH)

	mock := &mockHubClient{
		skillsSvc: &mockSkillService{
			resolveResp: &hubclient.ResolveSkillsResponse{
				Resolved: []hubclient.ResolvedSkill{
					{
						URI:             "nested-skill",
						Name:            "nested-skill",
						ResolvedVersion: "1.0.0",
						Files: []hubclient.DownloadURLInfo{
							{Path: "SKILL.md", URL: "https://s.example.com/1", Hash: hashMD},
							{Path: "scripts/run.sh", URL: "https://s.example.com/2", Hash: hashSH},
						},
					},
				},
			},
			downloadData: map[string][]byte{
				"https://s.example.com/1": skillMD,
				"https://s.example.com/2": scriptSH,
			},
		},
	}

	refs := []api.SkillReference{{Name: "nested-skill"}}
	destDir := t.TempDir()

	_, err := resolveSkillReferences(context.Background(), mock, refs, destDir, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify nested directory and files
	scriptPath := filepath.Join(destDir, "nested-skill", "scripts", "run.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("expected script file at %s: %v", scriptPath, err)
	}
	// .sh files should be executable
	if info.Mode()&0100 == 0 {
		t.Errorf("expected executable mode for .sh file, got %v", info.Mode())
	}
}

func TestResolveSkillReferences_AllOptionalHubFailure(t *testing.T) {
	mock := &mockHubClient{
		skillsSvc: &mockSkillService{
			resolveErr: fmt.Errorf("hub connection refused"),
		},
	}

	refs := []api.SkillReference{
		{Name: "opt1", Optional: true},
		{Name: "opt2", Optional: true},
	}

	destDir := t.TempDir()
	_, err := resolveSkillReferences(context.Background(), mock, refs, destDir, "", "")
	if err != nil {
		t.Fatalf("expected nil error when all optional and hub fails, got: %v", err)
	}
}

func TestResolveSkillReferences_RequiredHubFailure(t *testing.T) {
	mock := &mockHubClient{
		skillsSvc: &mockSkillService{
			resolveErr: fmt.Errorf("hub connection refused"),
		},
	}

	refs := []api.SkillReference{
		{Name: "required-skill", Optional: false},
	}

	destDir := t.TempDir()
	_, err := resolveSkillReferences(context.Background(), mock, refs, destDir, "", "")
	if err == nil {
		t.Fatal("expected error when required skill and hub fails")
	}
}

// ---------- helpers ----------

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------- mock types ----------

// mockHubClient implements hubclient.Client for testing.
type mockHubClient struct {
	hubclient.Client
	skillsSvc *mockSkillService
}

func (m *mockHubClient) Skills() hubclient.SkillService {
	return m.skillsSvc
}

// mockSkillService implements hubclient.SkillService for testing.
type mockSkillService struct {
	hubclient.SkillService
	resolveResp  *hubclient.ResolveSkillsResponse
	resolveErr   error
	downloadData map[string][]byte
}

func (m *mockSkillService) Resolve(ctx context.Context, req *hubclient.ResolveSkillsRequest) (*hubclient.ResolveSkillsResponse, error) {
	if m.resolveErr != nil {
		return nil, m.resolveErr
	}
	return m.resolveResp, nil
}

func (m *mockSkillService) DownloadFile(ctx context.Context, url string) ([]byte, error) {
	if m.downloadData == nil {
		return nil, fmt.Errorf("no download data configured")
	}
	data, ok := m.downloadData[url]
	if !ok {
		return nil, fmt.Errorf("URL not found in mock: %s", url)
	}
	return data, nil
}

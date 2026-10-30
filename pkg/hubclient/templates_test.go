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

func TestCreateTemplateRequest_UnmarshalJSON(t *testing.T) {
	t.Run("HandleProjectIdKey", func(t *testing.T) {
		data := `{"name":"tmpl","scope":"project","projectId":"p1"}`
		var req CreateTemplateRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if req.ProjectID != "p1" {
			t.Errorf("Expected project ID 'p1', got '%s'", req.ProjectID)
		}
	})

	t.Run("IgnoresLegacyGroveIdKey", func(t *testing.T) {
		data := `{"name":"tmpl","scope":"project","groveId":"g1"}`
		var req CreateTemplateRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if req.ProjectID != "" {
			t.Errorf("ProjectID = %q, want empty (legacy groveId must not be honored)", req.ProjectID)
		}
	})
}

func TestCreateTemplateRequest_MarshalJSON(t *testing.T) {
	req := CreateTemplateRequest{
		Name:      "tmpl",
		Scope:     "project",
		ProjectID: "p1",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if m["projectId"] != "p1" {
		t.Errorf("Expected projectId 'p1', got %v", m["projectId"])
	}
	if _, ok := m["groveId"]; ok {
		t.Errorf("Expected no groveId field, got %v", m["groveId"])
	}
}

func TestCloneTemplateRequest_UnmarshalJSON(t *testing.T) {
	t.Run("HandleProjectIdKey", func(t *testing.T) {
		data := `{"name":"clone","scope":"project","projectId":"p1"}`
		var req CloneTemplateRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if req.ProjectID != "p1" {
			t.Errorf("Expected project ID 'p1', got '%s'", req.ProjectID)
		}
	})

	t.Run("IgnoresLegacyGroveIdKey", func(t *testing.T) {
		data := `{"name":"clone","scope":"project","groveId":"g1"}`
		var req CloneTemplateRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if req.ProjectID != "" {
			t.Errorf("ProjectID = %q, want empty (legacy groveId must not be honored)", req.ProjectID)
		}
	})

	t.Run("IgnoresLegacyGroveIdKeyWhenProjectIdPresent", func(t *testing.T) {
		data := `{"name":"clone","scope":"project","projectId":"p1","groveId":"g1"}`
		var req CloneTemplateRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			t.Fatalf("Unmarshal failed: %v", err)
		}
		if req.ProjectID != "p1" {
			t.Errorf("ProjectID = %q, want %q (legacy groveId must not be honored)", req.ProjectID, "p1")
		}
	})
}

func TestCloneTemplateRequest_MarshalJSON(t *testing.T) {
	req := CloneTemplateRequest{
		Name:      "clone",
		Scope:     "project",
		ProjectID: "p1",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if m["projectId"] != "p1" {
		t.Errorf("Expected projectId 'p1', got %v", m["projectId"])
	}
	if _, ok := m["groveId"]; ok {
		t.Errorf("Expected no groveId field, got %v", m["groveId"])
	}
}

func TestTemplateReimport(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody ReimportTemplateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ReimportTemplateResponse{Templates: []string{"my-template"}, Count: 1})
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := client.Templates().Reimport(context.Background(), "tmpl-1", "https://github.com/acme/repo/tree/main/t")
	if err != nil {
		t.Fatalf("Reimport: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/templates/tmpl-1/reimport" {
		t.Errorf("unexpected request %s %s", gotMethod, gotPath)
	}
	if gotBody.SourceURL != "https://github.com/acme/repo/tree/main/t" {
		t.Errorf("unexpected sourceUrl %q", gotBody.SourceURL)
	}
	if resp.Count != 1 || len(resp.Templates) != 1 || resp.Templates[0] != "my-template" {
		t.Errorf("unexpected response %+v", resp)
	}
}

func TestTemplateReimport_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"unsupported_source","message":"not supported"}}`))
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Templates().Reimport(context.Background(), "tmpl-1", ""); err == nil {
		t.Fatal("expected an error for a 400 response")
	}
}

func TestTemplate_SourceURLRoundTrip(t *testing.T) {
	var tmpl Template
	if err := json.Unmarshal([]byte(`{"id":"t1","sourceUrl":"https://github.com/acme/repo/tree/main/t"}`), &tmpl); err != nil {
		t.Fatal(err)
	}
	if tmpl.SourceURL != "https://github.com/acme/repo/tree/main/t" {
		t.Errorf("SourceURL not decoded: %q", tmpl.SourceURL)
	}
}

// Template.AgentConfig decodes the Hub's read-only agentConfig snapshot
// (ptone/scion#4223); a legacy "config" member is ignored.
func TestTemplate_AgentConfigDecodes(t *testing.T) {
	var tmpl Template
	data := `{"id":"t1","name":"t","agentConfig":{"model":"opus","message_mode":"lineage"},"config":{"model":"row"}}`
	if err := json.Unmarshal([]byte(data), &tmpl); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if tmpl.AgentConfig == nil || tmpl.AgentConfig.Model != "opus" || tmpl.AgentConfig.MessageMode != "lineage" {
		t.Errorf("AgentConfig = %+v, want model opus, message_mode lineage", tmpl.AgentConfig)
	}
}

// WithDispatchAgent adds agentId to the template metadata and download-URL
// reads (ptone/scion#5770); without it both requests carry no query, as
// before.
func TestTemplateReads_WithDispatchAgent(t *testing.T) {
	var gotQueries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQueries = append(gotQueries, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/templates/tmpl-1/download" {
			_ = json.NewEncoder(w).Encode(DownloadResponse{})
			return
		}
		_ = json.NewEncoder(w).Encode(Template{ID: "tmpl-1"})
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := client.Templates().Get(ctx, "tmpl-1", WithDispatchAgent("agent/1 x")); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := client.Templates().RequestDownloadURLs(ctx, "tmpl-1", WithDispatchAgent("agent/1 x")); err != nil {
		t.Fatalf("RequestDownloadURLs: %v", err)
	}
	if _, err := client.Templates().Get(ctx, "tmpl-1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := client.Templates().RequestDownloadURLs(ctx, "tmpl-1", WithDispatchAgent("")); err != nil {
		t.Fatalf("RequestDownloadURLs: %v", err)
	}
	want := []string{
		"/api/v1/templates/tmpl-1?agentId=agent%2F1+x",
		"/api/v1/templates/tmpl-1/download?agentId=agent%2F1+x",
		"/api/v1/templates/tmpl-1?",
		"/api/v1/templates/tmpl-1/download?",
	}
	if len(gotQueries) != len(want) {
		t.Fatalf("requests = %q, want %q", gotQueries, want)
	}
	for i := range want {
		if gotQueries[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, gotQueries[i], want[i])
		}
	}
}

func TestDispatchAgentOf(t *testing.T) {
	if got := DispatchAgentOf(); got != "" {
		t.Errorf("no options: got %q", got)
	}
	if got := DispatchAgentOf(nil, WithDispatchAgent("a1")); got != "a1" {
		t.Errorf("got %q, want a1", got)
	}
}

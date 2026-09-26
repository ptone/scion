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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// ptone/scion#1975: clone concurrency: make same-name clones atomic.
//
// handleTemplateClone and handleHarnessConfigClone copy files to a storage
// path computed deterministically from (scope, scopeID, slug), then create
// the record. When two requests race to clone into the same destination
// name, both compute the same path; the losing request's failure-path
// cleanup can then run after the winner has already written its files
// there, removing them out from under the just-created record. This suite
// pins the fix: each clone request now writes to a storage path unique to
// itself, so a losing request's cleanup can only ever remove its own
// subtree, and exactly one request ends up with a usable record.
// ============================================================================

const cloneRaceGoroutines = 20

// raceRequest performs an HTTP request without using *testing.T, so it is
// safe to call from concurrent goroutines (t.Fatalf/require are not
// goroutine-safe when called outside the test's own goroutine).
func raceRequest(srv *Server, token, method, path string, body interface{}) int {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// assertExactlyOneWinner runs n concurrent requests via fire and asserts
// exactly one 201 and the rest 409.
func assertExactlyOneWinner(t *testing.T, n int, fire func() int) {
	t.Helper()
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			codes[idx] = fire()
		}(i)
	}
	wg.Wait()

	var winners, conflicts int
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			winners++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected status code %d from a concurrent clone", c)
		}
	}
	assert.Equal(t, 1, winners, "exactly one concurrent clone to the same name must win")
	assert.Equal(t, n-1, conflicts, "every other concurrent clone must be rejected as a conflict")
}

func TestTemplateClone_ConcurrentSameName_GlobalScope_ExactlyOneWinner(t *testing.T) {
	srv, s := testServer(t)
	stor := newCloneMockStorage("test-bucket")
	srv.SetStorage(stor)
	ctx := context.Background()

	sourcePath := storage.TemplateStoragePath(srv.HubID(), store.TemplateScopeGlobal, "", "race-source-global")
	files := []store.TemplateFile{
		{Path: "scion-agent.yaml", Size: int64(len("scion-agent-config: race\n"))},
		{Path: "README.md", Size: int64(len("hello\n"))},
	}
	stor.seedObject(sourcePath+"/scion-agent.yaml", []byte("scion-agent-config: race\n"))
	stor.seedObject(sourcePath+"/README.md", []byte("hello\n"))
	source := &store.Template{
		ID: api.NewUUID(), Slug: "race-source-global", Name: "Source", Harness: "claude",
		Scope: store.TemplateScopeGlobal, Status: store.TemplateStatusActive,
		StoragePath: sourcePath, StorageBucket: "test-bucket", Files: files,
	}
	source.ContentHash = computeContentHash(source.Files)
	require.NoError(t, s.CreateTemplate(ctx, source))

	assertExactlyOneWinner(t, cloneRaceGoroutines, func() int {
		return raceRequest(srv, testDevToken, http.MethodPost, "/api/v1/templates/"+source.ID+"/clone", map[string]interface{}{
			"name":  "race-clone-global",
			"scope": "global",
		})
	})

	winner, err := s.GetTemplateBySlug(ctx, "race-clone-global", store.TemplateScopeGlobal, "")
	require.NoError(t, err)
	require.NotEmpty(t, winner.Files, "the winning clone must have its file manifest populated")

	for _, f := range winner.Files {
		getRec := doRequest(t, srv, http.MethodGet, "/api/v1/templates/"+winner.ID+"/files/"+f.Path, nil)
		require.Equal(t, http.StatusOK, getRec.Code,
			"every file on the winning record must be readable through the files API: %s", getRec.Body.String())
	}
}

func TestTemplateClone_ConcurrentSameName_ProjectScope_ExactlyOneWinner(t *testing.T) {
	srv, s, alice, _, project := setupTemplateAuthzTest(t)
	stor := newCloneMockStorage("test-bucket")
	srv.SetStorage(stor)
	ctx := context.Background()

	sourcePath := storage.TemplateStoragePath(srv.HubID(), store.TemplateScopeGlobal, "", "race-source-project")
	files := []store.TemplateFile{{Path: "scion-agent.yaml", Size: int64(len("scion-agent-config: race\n"))}}
	stor.seedObject(sourcePath+"/scion-agent.yaml", []byte("scion-agent-config: race\n"))
	source := &store.Template{
		ID: api.NewUUID(), Slug: "race-source-project", Name: "Source", Harness: "claude",
		Scope: store.TemplateScopeGlobal, Status: store.TemplateStatusActive,
		StoragePath: sourcePath, StorageBucket: "test-bucket", Files: files,
	}
	source.ContentHash = computeContentHash(source.Files)
	require.NoError(t, s.CreateTemplate(ctx, source))

	token, _, _, err := srv.userTokenService.GenerateTokenPair(alice.ID, alice.Email, alice.DisplayName, alice.Role, ClientTypeWeb)
	require.NoError(t, err)

	assertExactlyOneWinner(t, cloneRaceGoroutines, func() int {
		return raceRequest(srv, token, http.MethodPost, "/api/v1/templates/"+source.ID+"/clone", map[string]interface{}{
			"name":    "race-clone-project",
			"scope":   "project",
			"scopeId": project.ID,
		})
	})

	winner, err := s.GetTemplateBySlug(ctx, "race-clone-project", store.TemplateScopeProject, project.ID)
	require.NoError(t, err)
	require.NotEmpty(t, winner.Files)

	for _, f := range winner.Files {
		getRec := doRequestAsUser(t, srv, alice, http.MethodGet, "/api/v1/templates/"+winner.ID+"/files/"+f.Path, nil)
		require.Equal(t, http.StatusOK, getRec.Code,
			"every file on the winning record must be readable through the files API: %s", getRec.Body.String())
	}
}

// TestTemplateClone_ConcurrentSameName_UserScope_SameOwner_ExactlyOneWinner
// confirms the fix does not regress the already-closed cross-owner case: a
// single owner racing clones into their own user scope must still resolve
// to exactly one winner with intact files.
func TestTemplateClone_ConcurrentSameName_UserScope_SameOwner_ExactlyOneWinner(t *testing.T) {
	srv, s, alice, _, _ := setupTemplateAuthzTest(t)
	stor := newCloneMockStorage("test-bucket")
	srv.SetStorage(stor)
	ctx := context.Background()

	sourcePath := storage.TemplateStoragePath(srv.HubID(), store.TemplateScopeGlobal, "", "race-source-user")
	files := []store.TemplateFile{{Path: "scion-agent.yaml", Size: int64(len("scion-agent-config: race\n"))}}
	stor.seedObject(sourcePath+"/scion-agent.yaml", []byte("scion-agent-config: race\n"))
	source := &store.Template{
		ID: api.NewUUID(), Slug: "race-source-user", Name: "Source", Harness: "claude",
		Scope: store.TemplateScopeGlobal, Status: store.TemplateStatusActive,
		StoragePath: sourcePath, StorageBucket: "test-bucket", Files: files,
	}
	source.ContentHash = computeContentHash(source.Files)
	require.NoError(t, s.CreateTemplate(ctx, source))

	token, _, _, err := srv.userTokenService.GenerateTokenPair(alice.ID, alice.Email, alice.DisplayName, alice.Role, ClientTypeWeb)
	require.NoError(t, err)

	assertExactlyOneWinner(t, cloneRaceGoroutines, func() int {
		return raceRequest(srv, token, http.MethodPost, "/api/v1/templates/"+source.ID+"/clone", map[string]interface{}{
			"name":  "race-clone-user",
			"scope": "user",
		})
	})

	winner, err := s.GetTemplateBySlug(ctx, "race-clone-user", store.TemplateScopeUser, alice.ID)
	require.NoError(t, err)
	require.NotEmpty(t, winner.Files)
}

func TestHarnessConfigClone_ConcurrentSameName_GlobalScope_ExactlyOneWinner(t *testing.T) {
	srv, s := testServer(t)
	stor := newCloneMockStorage("test-bucket")
	srv.SetStorage(stor)
	ctx := context.Background()

	sourcePath := storage.HarnessConfigStoragePath(srv.HubID(), store.HarnessConfigScopeGlobal, "", "race-source-global")
	files := []store.TemplateFile{{Path: "config.yaml", Size: int64(len("harness: claude\n"))}}
	stor.seedObject(sourcePath+"/config.yaml", []byte("harness: claude\n"))
	source := &store.HarnessConfig{
		ID: api.NewUUID(), Slug: "race-source-global", Name: "Source", Harness: "claude",
		Scope: store.HarnessConfigScopeGlobal, Status: store.HarnessConfigStatusActive,
		StoragePath: sourcePath, StorageBucket: "test-bucket", Files: files,
	}
	source.ContentHash = computeContentHash(source.Files)
	require.NoError(t, s.CreateHarnessConfig(ctx, source))

	assertExactlyOneWinner(t, cloneRaceGoroutines, func() int {
		return raceRequest(srv, testDevToken, http.MethodPost, "/api/v1/harness-configs/"+source.ID+"/clone", map[string]interface{}{
			"name":  "race-clone-global",
			"scope": "global",
		})
	})

	winner, err := s.GetHarnessConfigBySlug(ctx, "race-clone-global", store.HarnessConfigScopeGlobal, "")
	require.NoError(t, err)
	require.NotEmpty(t, winner.Files)

	for _, f := range winner.Files {
		getRec := doRequest(t, srv, http.MethodGet, "/api/v1/harness-configs/"+winner.ID+"/files/"+f.Path, nil)
		require.Equal(t, http.StatusOK, getRec.Code,
			"every file on the winning record must be readable through the files API: %s", getRec.Body.String())
	}
}

func TestHarnessConfigClone_ConcurrentSameName_ProjectScope_ExactlyOneWinner(t *testing.T) {
	srv, s, alice, _, project := setupTemplateAuthzTest(t)
	stor := newCloneMockStorage("test-bucket")
	srv.SetStorage(stor)
	ctx := context.Background()

	sourcePath := storage.HarnessConfigStoragePath(srv.HubID(), store.HarnessConfigScopeGlobal, "", "race-source-project")
	files := []store.TemplateFile{{Path: "config.yaml", Size: int64(len("harness: claude\n"))}}
	stor.seedObject(sourcePath+"/config.yaml", []byte("harness: claude\n"))
	source := &store.HarnessConfig{
		ID: api.NewUUID(), Slug: "race-source-project", Name: "Source", Harness: "claude",
		Scope: store.HarnessConfigScopeGlobal, Status: store.HarnessConfigStatusActive,
		StoragePath: sourcePath, StorageBucket: "test-bucket", Files: files,
	}
	source.ContentHash = computeContentHash(source.Files)
	require.NoError(t, s.CreateHarnessConfig(ctx, source))

	token, _, _, err := srv.userTokenService.GenerateTokenPair(alice.ID, alice.Email, alice.DisplayName, alice.Role, ClientTypeWeb)
	require.NoError(t, err)

	assertExactlyOneWinner(t, cloneRaceGoroutines, func() int {
		return raceRequest(srv, token, http.MethodPost, "/api/v1/harness-configs/"+source.ID+"/clone", map[string]interface{}{
			"name":    "race-clone-project",
			"scope":   "project",
			"scopeId": project.ID,
		})
	})

	winner, err := s.GetHarnessConfigBySlug(ctx, "race-clone-project", store.HarnessConfigScopeProject, project.ID)
	require.NoError(t, err)
	require.NotEmpty(t, winner.Files)

	for _, f := range winner.Files {
		getRec := doRequestAsUser(t, srv, alice, http.MethodGet, "/api/v1/harness-configs/"+winner.ID+"/files/"+f.Path, nil)
		require.Equal(t, http.StatusOK, getRec.Code,
			"every file on the winning record must be readable through the files API: %s", getRec.Body.String())
	}
}

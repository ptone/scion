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

package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var readonlyReadScopes = []string{
	"skill:read", "skill:list", "template:read", "template:list", "harness_config:read", "harness_config:list",
}

func TestAgentCreateRoleWarning(t *testing.T) {
	withReads := append([]string{"project:read", "agent:create", "agent:read"}, readonlyReadScopes...)
	cases := []struct {
		name   string
		scopes []string
		want   []string // selectors the warning names; nil = no warning
	}{
		{name: "no agent create", scopes: []string{"project:read", "agent:read"}},
		{name: "documented minimum", scopes: withReads},
		{name: "manage aliases", scopes: []string{"project:read", "agent:manage", "skill:manage", "template:manage", "harness_config:manage"}},
		{name: "unknown scope", scopes: []string{"agent:create", "no:such_scope"}},
		{
			name:   "agent create without the reads",
			scopes: []string{"project:read", "agent:create", "agent:read"},
			want:   []string{"harness_config:list", "harness_config:read", "skill:list", "skill:read", "template:list", "template:read"},
		},
		{
			name:   "agent manage without project read",
			scopes: append([]string{"agent:manage"}, readonlyReadScopes...),
			want:   []string{"project:read"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agentCreateRoleWarning(tc.scopes)
			if tc.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, "Warning: agents this token creates get no agent role above none: the readonly role also needs "+
				strings.Join(tc.want, ", ")+". Add them to --scopes, or create agents with agent role none.", got)
		})
	}
}

// The help table's "scion start / create" row lists exactly agent:create,
// agent:read and the scopes the readonly agent role needs.
func TestHubTokenHelpStartCreateScopesMatchReadonlyRole(t *testing.T) {
	help := hubTokenCmd.Long
	start := strings.Index(help, "scion start / create")
	require.GreaterOrEqual(t, start, 0)
	end := strings.Index(help[start:], "scion message")
	require.Greater(t, end, 0)
	row := help[start+len("scion start / create") : start+end]

	got := strings.FieldsFunc(row, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	want := append([]string{"agent:create", "agent:read"}, hub.AgentRoleUATSelectors(hub.AgentRoleReadOnly).Selectors...)
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got)
}

var helpScopesFlag = regexp.MustCompile(`--scopes\s+(\S+)`)

// Every help example that creates a token able to create agents carries the
// scopes the readonly agent role needs, so it draws no warning.
func TestHubTokenHelpExamplesFitReadonlyRole(t *testing.T) {
	checked := 0
	for _, help := range []string{hubTokenCmd.Long, hubTokenCreateCmd.Long} {
		examples := strings.Split(help, "scion hub token create")[1:]
		for _, ex := range examples {
			var scopes []string
			for _, m := range helpScopesFlag.FindAllStringSubmatch(ex, -1) {
				scopes = append(scopes, splitCommaList([]string{m[1]})...)
			}
			if len(scopes) == 0 {
				continue
			}
			checked++
			assert.Empty(t, agentCreateRoleWarning(scopes), "example scopes %v", scopes)
		}
	}
	assert.GreaterOrEqual(t, checked, 4, "examples found")
}

// runTokenCreate prints the warning on stderr and still creates the token.
func TestRunTokenCreate_AgentCreateRoleWarning(t *testing.T) {
	state := saveHubTokenTestState()
	t.Cleanup(state.restore)

	projectID := "44444444-4444-4444-4444-444444444444"
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+projectID:
			_ = json.NewEncoder(w).Encode(hubclient.Project{ID: projectID, Name: "my-project"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/tokens":
			created = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(hubclient.CreateTokenResponse{
				Token:       "synthetic-fixture-value",
				AccessToken: &hubclient.TokenInfo{ID: "t1", Name: "ci-token", Scopes: []string{"project:read", "agent:create"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	setupHubTokenProject(t, server.URL)

	tokenCreateName = "ci-token"
	tokenCreateProject = projectID
	tokenCreateBoundary = tokenBoundaryProject
	tokenCreateScopes = []string{"project:read,agent:create"}
	tokenCreateExpires = ""
	tokenOutputJSON = false

	var runErr error
	_, errOut := captureStdoutStderr(t, func() {
		runErr = runTokenCreate(nil, nil)
	})
	require.NoError(t, runErr)
	assert.True(t, created, "the token is still created")
	assert.Contains(t, errOut, "Warning: agents this token creates get no agent role above none")
	assert.Contains(t, errOut, "skill:read")
}

// An input error stops runTokenCreate before the warning: the user sees
// only the error.
func TestRunTokenCreate_AgentCreateRoleWarningAfterValidation(t *testing.T) {
	state := saveHubTokenTestState()
	t.Cleanup(state.restore)

	projectID := "55555555-5555-5555-5555-555555555555"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+projectID:
			_ = json.NewEncoder(w).Encode(hubclient.Project{ID: projectID, Name: "my-project"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	setupHubTokenProject(t, server.URL)

	tokenCreateName = "ci-token"
	tokenCreateProject = projectID
	tokenCreateBoundary = tokenBoundaryProject
	tokenCreateScopes = []string{"project:read,agent:create"}
	tokenCreateExpires = "not-a-duration"
	tokenOutputJSON = false

	var runErr error
	_, errOut := captureStdoutStderr(t, func() {
		runErr = runTokenCreate(nil, nil)
	})
	require.Error(t, runErr)
	assert.NotContains(t, errOut, "Warning:")
}

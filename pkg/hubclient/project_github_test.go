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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectService_GitHubPermissions(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody GitHubTokenPermissions

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		switch r.Method {
		case http.MethodPut:
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(gotBody)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)

	ctx := context.Background()

	// Empty projectID fails closed.
	_, err = c.Projects().SetGitHubPermissions(ctx, "", GitHubTokenPermissions{Contents: "read"})
	require.Error(t, err)
	require.Error(t, c.Projects().ResetGitHubPermissions(ctx, ""))

	// PUT round-trip.
	want := GitHubTokenPermissions{Contents: "write", Issues: "read", Metadata: "read"}
	got, err := c.Projects().SetGitHubPermissions(ctx, "proj-1", want)
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/api/v1/projects/proj-1/github-permissions", gotPath)
	assert.Equal(t, want, gotBody)
	assert.Equal(t, &want, got)

	// DELETE round-trip.
	require.NoError(t, c.Projects().ResetGitHubPermissions(ctx, "proj-1"))
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/api/v1/projects/proj-1/github-permissions", gotPath)
}

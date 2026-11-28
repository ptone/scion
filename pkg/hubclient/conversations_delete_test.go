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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationService_Delete(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if r.URL.Path == "/api/v1/conversations/missing-id" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Conversation not found"}}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)

	require.NoError(t, c.Conversations().Delete(context.Background(), "conv-123"))
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/api/v1/conversations/conv-123", gotPath)

	err = c.Conversations().Delete(context.Background(), "missing-id")
	require.Error(t, err)
	assert.True(t, apiclient.IsNotFoundError(err))
}

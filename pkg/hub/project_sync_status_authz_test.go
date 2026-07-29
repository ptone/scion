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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression tests for handleProjectSyncStatus (project_webdav.go), the second
// entry handler in that file and the one the workspace gate missed.
//
// Until the gate landed it had no authorization call at all: measured at
// c48db01a and confirmed independently, a cross-project agent, an unrelated
// user and a runtime broker each received 200 and a full ProjectSyncState —
// every field of it: the IDs of the brokers serving the project, its file and
// byte counts, and LastCommitSHA and LastSyncTime, which between them identify
// the project's exact code state and when it last moved — while the gated
// workspace routes in the same file refused all three. The
// exposure was reachable by asking a neighbouring route the same question.
//
// These tests reuse pcGateFixture from project_cache_authz_test.go, and that
// reuse is the point rather than a convenience: GET /sync/status and
// cache/status return the same ProjectSyncState records, so they must answer
// the same callers the same way. One fixture seeding one canary broker ID lets
// TestSSGate_AgreesWithCacheStatus compare them directly, and a future change
// to either endpoint's gate that does not touch the other shows up as a
// disagreement instead of as nothing.
//
// Test naming: everything file-local is prefixed ssGate.

func ssGatePath(f *pcGateFixture) string {
	return "/api/v1/projects/" + f.projA.ID + "/sync/status"
}

// TestSSGate_OwnerAllowed is the positive control, and it asserts on the
// payload rather than on the status code alone: the 200 must carry the broker
// ID that every refused caller below must not see. Without this, "did not
// disclose the broker ID" would be satisfied by an endpoint that discloses it
// to nobody.
func TestSSGate_OwnerAllowed(t *testing.T) {
	f := pcGateSetup(t)
	rec := f.asUser(t, f.owner, http.MethodGet, ssGatePath(f))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), pcGateSecretBrokerID,
		"the positive control must actually disclose, or the denials below prove nothing")
}

// TestSSGate_InProjectAgentMayRead pins the agent project read baseline
// (authz.go:239): an agent inside the project reads its own project's sync
// state. The gate refuses callers outside the project, not every non-user.
func TestSSGate_InProjectAgentMayRead(t *testing.T) {
	f := pcGateSetup(t)
	rec := f.asAgent(t, f.insider, http.MethodGet, ssGatePath(f))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

func TestSSGate_Denied(t *testing.T) {
	f := pcGateSetup(t)

	cases := []struct {
		name string
		want int
		call func(*testing.T) *httptest.ResponseRecorder
	}{
		{"unrelated user", http.StatusForbidden, func(t *testing.T) *httptest.ResponseRecorder {
			return f.asUser(t, f.outsdr, http.MethodGet, ssGatePath(f))
		}},
		{
			// 404, not 403: authorizeProjectWorkspaceAccess runs
			// requireProjectVisibleToAgent first, so an agent outside the
			// project cannot use the response to learn the project exists.
			"cross-project agent", http.StatusNotFound, func(t *testing.T) *httptest.ResponseRecorder {
				return f.asAgent(t, f.stranger, http.MethodGet, ssGatePath(f))
			},
		},
		{"broker", http.StatusForbidden, func(t *testing.T) *httptest.ResponseRecorder {
			return f.asBroker(t, http.MethodGet, ssGatePath(f))
		}},
		{"unauthenticated", http.StatusUnauthorized, func(t *testing.T) *httptest.ResponseRecorder {
			return f.anonymous(t, http.MethodGet, ssGatePath(f))
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := c.call(t)
			require.Equal(t, c.want, rec.Code, "body: %s", rec.Body.String())
			require.NotContains(t, rec.Body.String(), pcGateSecretBrokerID,
				"a refused response disclosed the serving broker's ID")
			require.NotContains(t, rec.Body.String(), "totalBytes",
				"a refused response returned a sync-status payload")
		})
	}
}

// TestSSGate_AgreesWithCacheStatus compares the two endpoints that return the
// same ProjectSyncState records. It declares the expected verdict per caller
// as well as asserting the two agree: agreement alone is satisfied by two
// endpoints that serve everyone, which is exactly the state this gate was
// added to leave behind.
func TestSSGate_AgreesWithCacheStatus(t *testing.T) {
	f := pcGateSetup(t)
	cachePath := "/api/v1/projects/" + f.projA.ID + "/workspace/cache/status"

	callers := []struct {
		name string
		want int
		do   func(path string) *httptest.ResponseRecorder
	}{
		{"owner", http.StatusOK, func(p string) *httptest.ResponseRecorder {
			return f.asUser(t, f.owner, http.MethodGet, p)
		}},
		{"in-project agent", http.StatusOK, func(p string) *httptest.ResponseRecorder {
			return f.asAgent(t, f.insider, http.MethodGet, p)
		}},
		{"unrelated user", http.StatusForbidden, func(p string) *httptest.ResponseRecorder {
			return f.asUser(t, f.outsdr, http.MethodGet, p)
		}},
		{"cross-project agent", http.StatusNotFound, func(p string) *httptest.ResponseRecorder {
			return f.asAgent(t, f.stranger, http.MethodGet, p)
		}},
		{"broker", http.StatusForbidden, func(p string) *httptest.ResponseRecorder {
			return f.asBroker(t, http.MethodGet, p)
		}},
		{"unauthenticated", http.StatusUnauthorized, func(p string) *httptest.ResponseRecorder {
			return f.anonymous(t, http.MethodGet, p)
		}},
	}

	for _, c := range callers {
		t.Run(c.name, func(t *testing.T) {
			viaSync := c.do(ssGatePath(f))
			viaCache := c.do(cachePath)
			require.Equal(t, viaSync.Code, viaCache.Code,
				"/sync/status and cache/status return the same records and must "+
					"reach the same verdict for %s", c.name)
			require.Equal(t, c.want, viaSync.Code,
				"both endpoints agreed, but on the wrong verdict for %s: %s",
				c.name, viaSync.Body.String())
		})
	}
}

// TestSSGate_MethodDispatchStillFirst records that the gate sits after the
// method check, not before it, so the wrong verb still answers 405 rather than
// 401. Not an authorization property; here so a future reorder is visible.
func TestSSGate_MethodDispatchStillFirst(t *testing.T) {
	f := pcGateSetup(t)
	rec := f.asUser(t, f.owner, http.MethodPost, ssGatePath(f))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "body: %s", rec.Body.String())
}

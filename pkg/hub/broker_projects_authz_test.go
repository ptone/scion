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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Regression tests for N38: GET /api/v1/runtime-brokers/{id}/projects
// (getBrokerProjects, handlers_runtime_brokers.go) had NO authorization. It
// returns the broker's served-project list — ProjectID, name, GitRemote,
// AgentCount and LocalPath, the directory on the broker that IS each project's
// workspace. Measured before the gate, an unrelated user, an agent, and a broker
// each received 200 and the full list, LocalPath included: this is the READ half
// of the fact the provider gate (handleProjectProviders) protects at its own
// site, leaked from this one. It is not a defect in that gate, which is correct
// at its own site.
//
// Lead-ruled posture (04:16Z) is the conservative one used across this PR — gate
// with requireAdmin, deny — with a scope-filtered relaxation (let a caller see
// only the projects it is party to) left as a named follow-on requiring security
// review, not this commit.
//
// Test naming: everything file-local is prefixed bpGate.

// bpGateLeak is a sentinel LocalPath planted on the broker's provider record for
// projA. Its ABSENCE from a refused body is the real signal: a 403 that still
// shipped the workspace path would not be a refusal. It is a bare string rather
// than a real directory because getBrokerProjects reads LocalPath from the store
// and never validates it.
const bpGateLeak = "/n38-leak-canary/workspace-localpath"

func bpGatePath(brokerID string) string {
	return "/api/v1/runtime-brokers/" + brokerID + "/projects"
}

// bpGateAdmin adds an admin user to the reused bypassAgents fixture (whose own
// f.owner is only a member) and plants the sentinel LocalPath on the broker's
// projA provider so the leak has something concrete to leak. Returns the admin.
func bpGateAdmin(t *testing.T, f *bypassAgentsFixture) *store.User {
	t.Helper()
	admin := &store.User{
		ID: tid("n38-admin"), Email: "n38-admin@example.com",
		DisplayName: "N38 Admin", Role: store.UserRoleAdmin, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, f.store.CreateUser(context.Background(), admin))

	// AddProjectProvider upserts on (projectID, brokerID); the fixture already
	// linked f.broker to f.proj without a LocalPath, so this stamps the sentinel
	// onto that same record.
	require.NoError(t, f.store.AddProjectProvider(context.Background(), &store.ProjectProvider{
		ProjectID: f.proj.ID, BrokerID: f.broker.ID, BrokerName: f.broker.Name,
		LocalPath: bpGateLeak, Status: store.BrokerStatusOnline,
	}))
	return admin
}

func bpGateMember(t *testing.T, f *bypassAgentsFixture, name string) *store.User {
	t.Helper()
	u := &store.User{
		ID: tid(name), Email: name + "@example.com", DisplayName: name,
		Role: store.UserRoleMember, Status: "active", Created: time.Now(),
	}
	require.NoError(t, f.store.CreateUser(context.Background(), u))
	return u
}

func bpGateAnon(f *bypassAgentsFixture, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// TestBrokerProjectsGate_Denied is the finding. Each refused caller class gets
// its refusal for BOTH an existing broker id and a missing one, IDENTICALLY:
// requireAdmin sits ahead of the GetRuntimeBroker lookup, so this route is not a
// broker-existence oracle for a refused caller either. And no refused body ships
// the served-project LocalPath or identifiers — the leak the gate exists to
// close.
func TestBrokerProjectsGate_Denied(t *testing.T) {
	cases := []struct {
		name string
		want int
		// setup runs once per subtest, creating any identity it needs, and returns
		// the call bound to it so the real-vs-missing pair reuses one identity
		// rather than creating a duplicate.
		setup func(t *testing.T, f *bypassAgentsFixture) func(path string) *httptest.ResponseRecorder
	}{
		{
			"unrelated user", http.StatusForbidden,
			func(t *testing.T, f *bypassAgentsFixture) func(string) *httptest.ResponseRecorder {
				stranger := bpGateMember(t, f, "n38-stranger")
				return func(p string) *httptest.ResponseRecorder {
					return doRequestAsUser(t, f.srv, stranger, http.MethodGet, p, nil)
				}
			},
		},
		{
			// f.caller is an agent in a project the broker actually serves, so this
			// is the strongest read of "an agent": even an in-serving-project agent
			// is refused, because listing a broker's projects is an admin action.
			"agent", http.StatusForbidden,
			func(t *testing.T, f *bypassAgentsFixture) func(string) *httptest.ResponseRecorder {
				return func(p string) *httptest.ResponseRecorder {
					return f.asAgent(t, http.MethodGet, p, nil, ScopeAgentCreate)
				}
			},
		},
		{
			"broker", http.StatusForbidden,
			func(t *testing.T, f *bypassAgentsFixture) func(string) *httptest.ResponseRecorder {
				return func(p string) *httptest.ResponseRecorder {
					return f.asBroker(t, http.MethodGet, p, nil)
				}
			},
		},
		{
			// The anonymous refusal is the authentication middleware, ahead of the
			// gate — pinned so a change that moves authentication is visible.
			"anonymous", http.StatusUnauthorized,
			func(t *testing.T, f *bypassAgentsFixture) func(string) *httptest.ResponseRecorder {
				return func(p string) *httptest.ResponseRecorder {
					return bpGateAnon(f, http.MethodGet, p)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := bypassAgentsSetup(t)
			bpGateAdmin(t, f) // plant the sentinel LocalPath so a leak would show

			call := c.setup(t, f)
			real := call(bpGatePath(f.broker.ID))
			missing := call(bpGatePath(uuid.New().String()))

			// The finding first: no refused body may disclose the workspace path
			// or the served-project identifiers. Asserted ahead of the status so
			// that removing the gate reds on the leak itself, not on a code.
			for _, rec := range []*httptest.ResponseRecorder{real, missing} {
				body := rec.Body.String()
				require.NotContains(t, body, bpGateLeak,
					"a refused response disclosed the broker's served-project "+
						"LocalPath, which is where the project's workspace lives (%s)", c.name)
				require.NotContains(t, body, f.proj.ID,
					"a refused response disclosed a served project id (%s)", c.name)
				require.NotContains(t, body, f.proj.Name,
					"a refused response disclosed a served project name (%s)", c.name)
			}

			require.Equal(t, c.want, real.Code, "body=%s", real.Body.String())
			require.Equal(t, real.Code, missing.Code,
				"the status reveals whether the broker exists")
			require.Equal(t, real.Body.String(), missing.Body.String(),
				"the body reveals whether the broker exists")
		})
	}
}

// TestBrokerProjectsGate_AdminAllowed is the positive control. It asserts the
// payload, not just the status: an empty 200 to everyone would satisfy every
// refusal above while breaking the feature.
func TestBrokerProjectsGate_AdminAllowed(t *testing.T) {
	f := bypassAgentsSetup(t)
	admin := bpGateAdmin(t, f)

	rec := doRequestAsUser(t, f.srv, admin, http.MethodGet, bpGatePath(f.broker.ID), nil)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Contains(t, rec.Body.String(), bpGateLeak,
		"the admin control did not receive the served-project LocalPath, which "+
			"would make every refusal above vacuous")
	require.Contains(t, rec.Body.String(), f.proj.ID,
		"the admin control did not receive the served-project id")
}

// TestBrokerProjectsGate_AdminGetsExistenceTruth pins that gating ahead of the
// lookup withholds existence from refused callers WITHOUT blinding an authorized
// one: the admin passes the gate and then gets the honest 404 for a broker that
// does not exist, rather than a misdescribing 403.
func TestBrokerProjectsGate_AdminGetsExistenceTruth(t *testing.T) {
	f := bypassAgentsSetup(t)
	admin := bpGateAdmin(t, f)

	rec := doRequestAsUser(t, f.srv, admin, http.MethodGet,
		bpGatePath(uuid.New().String()), nil)
	require.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
}

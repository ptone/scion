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
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// #591 regression for the resource-import/discover AGENT branch
// (handlers_resource_import.go). Measured on an unmodified tree (sibling (iii),
// CONFIRMED-narrow): the agent branch authorized enumeration on
// agentIdent.HasScope(ScopeAgentCreate) + projectID==agent.ProjectID() alone and
// never called CheckAccess(ActionRead). The agent read baseline (authz.go:239) is
// revocable by design — an explicit deny policy is evaluated first and wins — so
// with a deny policy revoking projA read, GET /workspace/files was 403 (the
// workspace gate honours the deny) while POST /discover-templates was STILL 200,
// enumerating sibling directory names on disk. The create WRITE scope authorized a
// READ enumeration, and the deny was ignored on this path only.
//
// The fix (authorizeImportAgentRead) adds CheckAccess(ActionRead) on the project
// to the agent branch at every site, IN ADDITION to the scope and project-match
// checks (which stay). It renders 403 — not the no-oracle 404 of getProject —
// because the caller is an in-project agent that already knows its own project
// exists, so no existence oracle is introduced. The assertion that matters is not
// the status code but that the enumerated names are ABSENT from a refused body: a
// 403 that still ships the directory listing is not a fix.
//
// Red-without-fix: neutering authorizeImportAgentRead to `return true` reds
// exactly the revoked-read arms below (all four routes) while every control —
// anon 401, no-create-scope 403, cross-project 403, and the default-config 200 —
// stays green. Verified by hand before commit.
//
// Test naming: everything file-local is prefixed riGate.

const (
	riGateGoodTemplate = "goodtmpl"
	// riGateVictimDir has no marker file, so discovery reports it only via the
	// Skipped list — it is the enumeration leak the gate must now withhold.
	riGateVictimDir = "confidential-victim-notes"
)

// riGateSeedWorkspace plants a real subtree under projA's workspace root
// (f.workspacePath is what resolveProjectWebDAVPath returns for projA): one valid
// template dir so a successful discover is 200 rather than "no templates found",
// plus a marker-less sibling whose NAME is the enumeration leak. Returns the
// discover/import WorkspacePath to target it.
func riGateSeedWorkspace(t *testing.T, f *wsGateFixture) string {
	t.Helper()
	importDir := filepath.Join(f.workspacePath, "importsrc")
	good := filepath.Join(importDir, riGateGoodTemplate)
	require.NoError(t, os.MkdirAll(good, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(good, "scion-agent.yaml"),
		[]byte("name: goodtmpl\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(importDir, riGateVictimDir), 0o755))
	return "/importsrc"
}

// riGateCreateAgent makes an agent inside projA that carries the create scope.
func riGateCreateAgent(t *testing.T, f *wsGateFixture, name string) *store.Agent {
	t.Helper()
	a := &store.Agent{
		ID: tid(name), Slug: tid(name), Name: name,
		ProjectID: f.projA.ID, CreatedBy: f.owner.ID, OwnerID: f.owner.ID,
		Ancestry: []string{f.owner.ID},
	}
	require.NoError(t, f.store.CreateAgent(context.Background(), a))
	return a
}

// riGateAsAgentCreate issues the request with an agent token that carries the
// create scope (the fixture's asAgent mints nil scopes → status-update only,
// which cannot reach these create-gated handlers at all).
func riGateAsAgentCreate(t *testing.T, f *wsGateFixture, a *store.Agent, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	svc := f.srv.GetAgentTokenService()
	require.NotNil(t, svc)
	tok, err := svc.GenerateAgentToken(a.ID, a.ProjectID, []AgentTokenScope{ScopeAgentCreate}, nil)
	require.NoError(t, err)
	req := f.newRequest(method, path, body)
	req.Header.Set("X-Scion-Agent-Token", tok)
	return f.serve(req)
}

// riGateRevokeRead binds an explicit deny policy on projA read/list to the given
// agent principal. Policy evaluation runs before the read baseline (authz.go:239
// property #1), so the deny wins and the agent's read baseline is revoked.
func riGateRevokeRead(t *testing.T, f *wsGateFixture, agentID string) {
	t.Helper()
	ctx := context.Background()
	deny := &store.Policy{
		ID: tid("rigate-deny-" + agentID), Name: "rigate-deny-read-" + agentID,
		ScopeType: "project", ScopeID: f.projA.ID, ResourceType: "project",
		Actions: []string{"read", "list"}, Effect: "deny", Priority: 100,
	}
	require.NoError(t, f.store.CreatePolicy(ctx, deny))
	require.NoError(t, f.store.AddPolicyBinding(ctx, &store.PolicyBinding{
		PolicyID: deny.ID, PrincipalType: "agent", PrincipalID: agentID,
	}))
}

func riGateDiscoverBody(t *testing.T, wsPath string) []byte {
	t.Helper()
	b, err := json.Marshal(DiscoverResourcesRequest{WorkspacePath: wsPath})
	require.NoError(t, err)
	return b
}

// riGateRequireEnumerationAbsent asserts a refused body ships neither the valid
// template name nor the marker-less victim directory name — the real signal that
// the gate refused BEFORE reading the workspace, not after serializing it.
func riGateRequireEnumerationAbsent(t *testing.T, body string) {
	t.Helper()
	require.NotContains(t, body, riGateVictimDir,
		"a refused discover/import must not ship the enumerated victim dir; body=%s", body)
	require.NotContains(t, body, riGateGoodTemplate,
		"a refused discover/import must not ship the enumeration; body=%s", body)
}

// TestRIGate_DiscoverTemplatesRevokedReadRefused is the core measurement turned
// regression: the same in-project create-scope caller is served the enumeration
// by default and refused once its project read is revoked, with the enumerated
// names absent from the refused body. The default arm doubles as the positive
// control that the legitimate discover flow is not broken.
func TestRIGate_DiscoverTemplatesRevokedReadRefused(t *testing.T) {
	f := wsGateSetup(t)
	// Discovery nil-checks a storage backend before reading the workspace FS; the
	// fixture ships none. Discovery walks the on-disk workspace directly and does
	// not use the backend, so a mock is faithful for the enumeration measured.
	f.srv.SetStorage(newMockStorage("test-bucket"))
	wsPath := riGateSeedWorkspace(t, f)
	discover := "/api/v1/projects/" + f.projA.ID + "/discover-templates"
	body := riGateDiscoverBody(t, wsPath)

	// Positive control / leak demonstration: a default-config in-project agent
	// with the create scope still discovers, and the enumeration includes the
	// marker-less victim dir via Skipped.
	t.Run("default in-project agent still 200", func(t *testing.T) {
		good := riGateCreateAgent(t, f, "rigate-default")
		rec := riGateAsAgentCreate(t, f, good, http.MethodPost, discover, body)
		require.Equal(t, http.StatusOK, rec.Code,
			"default-config create-scope agent must still discover; body=%s", rec.Body.String())
		var out DiscoverResourcesResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		require.Contains(t, out.Resources, riGateGoodTemplate,
			"default discover must find the valid template")
		require.Contains(t, out.Skipped, riGateVictimDir,
			"default discover leaks the marker-less victim dir (the behaviour the gate now conditions on read)")
	})

	// Attack arm: the same caller class, read revoked via deny policy → refused,
	// enumeration absent from the body.
	t.Run("read-revoked in-project agent refused", func(t *testing.T) {
		attacker := riGateCreateAgent(t, f, "rigate-attacker")
		riGateRevokeRead(t, f, attacker.ID)

		// The workspace read gate already refuses this caller — the invariant the
		// import path must now match for the same caller on the same project.
		wsRec := riGateAsAgentCreate(t, f, attacker, http.MethodGet,
			"/api/v1/projects/"+f.projA.ID+"/workspace/files", nil)
		require.Equal(t, http.StatusForbidden, wsRec.Code,
			"baseline: authorizeProjectWorkspaceAccess must refuse the read-revoked agent")

		rec := riGateAsAgentCreate(t, f, attacker, http.MethodPost, discover, body)
		require.Equal(t, http.StatusForbidden, rec.Code,
			"read-revoked agent must be refused at discover; body=%s", rec.Body.String())
		riGateRequireEnumerationAbsent(t, rec.Body.String())
	})
}

// TestRIGate_AllFourRoutesRefuseRevokedRead pins that the gate is present at every
// site named in the fold-in ruling, not just discover-templates: a read-revoked
// in-project create-scope agent is refused with the enumeration absent on all
// four routes (both discover routes and both import routes).
func TestRIGate_AllFourRoutesRefuseRevokedRead(t *testing.T) {
	f := wsGateSetup(t)
	f.srv.SetStorage(newMockStorage("test-bucket"))
	wsPath := riGateSeedWorkspace(t, f)
	attacker := riGateCreateAgent(t, f, "rigate-attacker-all")
	riGateRevokeRead(t, f, attacker.ID)

	base := "/api/v1/projects/" + f.projA.ID
	body := riGateDiscoverBody(t, wsPath)

	for _, route := range []struct {
		name string
		path string
	}{
		{"discover-templates", base + "/discover-templates"},
		{"discover-harness-configs", base + "/discover-harness-configs"},
		{"import-templates", base + "/import-templates"},
		{"import-harness-configs", base + "/import-harness-configs"},
	} {
		t.Run(route.name, func(t *testing.T) {
			rec := riGateAsAgentCreate(t, f, attacker, http.MethodPost, route.path, body)
			require.Equal(t, http.StatusForbidden, rec.Code,
				"read-revoked agent must be refused at %s; body=%s", route.name, rec.Body.String())
			riGateRequireEnumerationAbsent(t, rec.Body.String())
		})
	}
}

// TestRIGate_SharedImportHelperGatesRevokedRead pins the FIFTH gate site — the
// shared authorizeProjectImport helper reached via the generic
// POST /api/v1/resources/discover and /resources/import routes (scope=project) —
// with its own committed red-without-fix arm, matching the four per-project
// routes. The lead ratified keeping this site as a real gate; without a committed
// test a refactor that drops the shared-helper gate would pass CI. These generic
// routes are remote-URL only (no local workspace enumeration), so the property
// under test is that a read-revoked in-project create-scope agent is REFUSED at
// the gate (403) before any discovery/import runs, while a default agent passes
// the gate (and only then fails downstream on the unreachable source URL).
func TestRIGate_SharedImportHelperGatesRevokedRead(t *testing.T) {
	f := wsGateSetup(t)
	// The generic handlers nil-check storage before reaching the scope gate.
	f.srv.SetStorage(newMockStorage("test-bucket"))

	// A remote source URL that will fail to fetch — a caller who PASSES the gate
	// falls through to that failure (non-403), which is exactly how we distinguish
	// "gate passed" from "gate refused" without needing a real remote.
	const bogusSource = "https://example.invalid/does-not-exist.git"
	body := func(kind string) []byte {
		b, err := json.Marshal(map[string]string{
			"kind": kind, "scope": "project", "scopeId": f.projA.ID, "sourceUrl": bogusSource,
		})
		require.NoError(t, err)
		return b
	}

	routes := []struct {
		name string
		path string
		// code is the specific content-bearing downstream error a caller who PASSED
		// the gate hits on the unreachable source URL: handleResourcesDiscover
		// renders 400 "discover_failed" (handlers_resource_import.go:807),
		// handleResourcesImport 400 "import_failed" (:408). Pinning it distinguishes
		// "passed the gate, failed downstream" from "never reached the handler"
		// (dead route/rename/401), which a NotEqual(403) allow arm cannot (N44).
		code string
	}{
		{"resources/discover", "/api/v1/resources/discover", "discover_failed"},
		{"resources/import", "/api/v1/resources/import", "import_failed"},
	}

	t.Run("read-revoked agent refused at shared gate", func(t *testing.T) {
		attacker := riGateCreateAgent(t, f, "rigate-shared-attacker")
		riGateRevokeRead(t, f, attacker.ID)
		for _, route := range routes {
			t.Run(route.name, func(t *testing.T) {
				rec := riGateAsAgentCreate(t, f, attacker, http.MethodPost, route.path, body("template"))
				require.Equal(t, http.StatusForbidden, rec.Code,
					"read-revoked agent must be refused at the shared import gate; body=%s", rec.Body.String())
				require.Contains(t, rec.Body.String(), "permission",
					"the refusal must be the read-gate denial, not a downstream error; body=%s", rec.Body.String())
			})
		}
	})

	t.Run("default agent passes the shared gate", func(t *testing.T) {
		good := riGateCreateAgent(t, f, "rigate-shared-default")
		for _, route := range routes {
			t.Run(route.name, func(t *testing.T) {
				rec := riGateAsAgentCreate(t, f, good, http.MethodPost, route.path, body("template"))
				// N44: pin the specific content-bearing downstream failure, not a
				// NotEqual(403). A dead route, a rename, or a moved middleware would
				// return 404/401 — which passes NotEqual(403)+NotContains("permission")
				// vacuously — so the old allow arm would stay green through exactly the
				// refactor this file exists to catch. 400 + the kind-specific code
				// proves the caller REACHED the handler and only then failed on the
				// unreachable source URL.
				require.Equal(t, http.StatusBadRequest, rec.Code,
					"default-config create-scope agent must pass the gate and fail downstream "+
						"on the unreachable source (400), not be refused or dead-routed; body=%s",
					rec.Body.String())
				require.Contains(t, rec.Body.String(), route.code,
					"a passed gate must reach the handler and render its downstream failure "+
						"code %q, proving the request was not dead-routed; body=%s", route.code, rec.Body.String())
			})
		}
	})
}

// TestRIGate_UnchangedControls confirms the fix leaves the pre-existing controls
// intact: the gate is additive, not a replacement.
func TestRIGate_UnchangedControls(t *testing.T) {
	f := wsGateSetup(t)
	f.srv.SetStorage(newMockStorage("test-bucket"))
	wsPath := riGateSeedWorkspace(t, f)
	discover := "/api/v1/projects/" + f.projA.ID + "/discover-templates"
	body := riGateDiscoverBody(t, wsPath)

	t.Run("unauthenticated 401", func(t *testing.T) {
		rec := f.anonymous(t, http.MethodPost, discover, body)
		require.Equal(t, http.StatusUnauthorized, rec.Code,
			"anonymous discover must be 401 (authn middleware upstream); body=%s", rec.Body.String())
		riGateRequireEnumerationAbsent(t, rec.Body.String())
	})

	t.Run("no create scope 403", func(t *testing.T) {
		// f.insider is in projA; asAgent mints nil scopes → status-update only, so
		// the create-scope check refuses before any read consideration.
		rec := f.asAgent(t, f.insider, http.MethodPost, discover, body)
		require.Equal(t, http.StatusForbidden, rec.Code,
			"agent without create scope must be 403; body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "scope",
			"the refusal must name the missing scope, not the read gate; body=%s", rec.Body.String())
		riGateRequireEnumerationAbsent(t, rec.Body.String())
	})

	t.Run("cross-project create-scope 403", func(t *testing.T) {
		// f.stranger belongs to projB; with a create scope it still may not reach
		// projA — the project-match check refuses before the read gate.
		rec := riGateAsAgentCreate(t, f, f.stranger, http.MethodPost, discover, body)
		require.Equal(t, http.StatusForbidden, rec.Code,
			"cross-project create-scope agent must be 403; body=%s", rec.Body.String())
		require.Contains(t, rec.Body.String(), "their own project",
			"the refusal must be the project-match message, not the read gate; body=%s", rec.Body.String())
		riGateRequireEnumerationAbsent(t, rec.Body.String())
	})
}

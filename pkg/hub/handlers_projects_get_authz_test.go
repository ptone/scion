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
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

// #591 regression for getProject (handlers_projects_core.go). Measured on an
// unmodified tree, getProject held no authorization call of any kind: any
// authenticated caller — an unrelated user, a cross-project agent — GET any
// project by ID and received the full record (name/slug/gitRemote/ownerId, plus
// the handler-enriched ownerName) regardless of visibility. updateProject and
// deleteProject on the same resource already gated with ActionUpdate/delete; the
// read did not. This is the record that supplies the slug the owner-self-grant
// route consumed.
//
// The gate is authorizeProjectReadNoOracle: requireProjectVisibleToAgent then
// CheckAccess(ActionRead), rendering 404 on denial so GET is not an existence
// oracle over project IDs (matches the provider gate's no-oracle shape). The
// assertion that matters is not the status code but that the record is ABSENT
// from a refused body — a 403/404 that still ships name/slug/gitRemote is not a
// fix. Every refusal below store-verifies the body against the real record.
//
// Test naming: everything file-local is prefixed gpGate.

const (
	gpGateSecretName      = "gpgate-secret-name"
	gpGateSecretSlug      = "gpgate-secret-slug"
	gpGateSecretGitRemote = "https://git.invalid/gpgate-secret.git"
)

// gpGateProject creates a fresh private project owned by f.owner with sensitive
// fields populated, so the refusal tests are withholding something real.
func gpGateProject(t *testing.T, f *wsGateFixture) *store.Project {
	t.Helper()
	p := &store.Project{
		ID: tid("gpgate-proj"), Name: gpGateSecretName, Slug: gpGateSecretSlug,
		OwnerID: f.owner.ID, GitRemote: gpGateSecretGitRemote, Visibility: "private",
	}
	require.NoError(t, f.store.CreateProject(context.Background(), p))
	return p
}

// gpGateRequireRecordAbsent asserts none of the sensitive fields leak into a
// refused response body. This is the real signal: a status code alone cannot
// tell a fix from a handler that refuses after serializing the record.
func gpGateRequireRecordAbsent(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{gpGateSecretName, gpGateSecretSlug, gpGateSecretGitRemote} {
		require.NotContains(t, body, secret,
			"a refused getProject must not ship the project record; body=%s", body)
	}
	// ownerName enrichment must never run for a refused caller: the owner's
	// display name ("Owner", set in wsGateSetup) must be absent too.
	require.NotContains(t, body, `"ownerName":"Owner"`,
		"a refused getProject must not enrich/leak the owner display name; body=%s", body)
}

// gpGateMissingBody returns the exact response body the handler renders for a
// project that does not exist, AS SEEN BY THE GIVEN CALLER. getProject calls
// s.store.GetProject before the gate, so a nonexistent id short-circuits to
// writeErrorFromErr(store.ErrNotFound) for any authenticated caller — this is the
// "missing" body a refused-real arm must be byte-identical to (no existence
// oracle over project ids).
//
// N39: the missing body is taken through the same caller-bound `call` as the
// refused-real request, not once as the owner. The property under test is that
// the ATTACKER cannot tell missing from refused, so the control must belong to
// the attacker; principal-independence of the missing body is the assumption
// this test exists to protect, and so is the one thing it must verify rather
// than assume.
func gpGateMissingBody(t *testing.T, call func(path string) *httptest.ResponseRecorder) string {
	t.Helper()
	rec := call("/api/v1/projects/" + tid("gpgate-nonexistent"))
	require.Equal(t, http.StatusNotFound, rec.Code,
		"a nonexistent project must be 404 for this caller; body=%s", rec.Body.String())
	return rec.Body.String()
}

// TestGPGate_AttackArmsRefusedRecordAbsent covers the caller classes measured as
// 200-with-record: an unrelated user, a cross-project agent, and a broker. Each
// must be refused, the record must be absent from the body, AND — the no-oracle
// property — the refused body must be byte-identical to the missing-project body,
// so status, error code, and message all agree with "this project does not
// exist". A 404 whose message reads "Project not found" while missing reads
// "Resource not found" is a one-bit existence oracle; this asserts the full body.
func TestGPGate_AttackArmsRefusedRecordAbsent(t *testing.T) {
	f := wsGateSetup(t)
	p := gpGateProject(t, f)
	path := "/api/v1/projects/" + p.ID

	arms := []struct {
		name string
		// call issues the GET as one attacker principal; the same call fetches
		// both the refused-real body and this arm's own missing-project control.
		call func(path string) *httptest.ResponseRecorder
	}{
		{"unrelated user", func(path string) *httptest.ResponseRecorder {
			return f.asUser(t, f.outsdr, http.MethodGet, path, nil)
		}},
		{"cross-project agent", func(path string) *httptest.ResponseRecorder {
			return f.asAgent(t, f.stranger, http.MethodGet, path, nil)
		}},
		{"broker", func(path string) *httptest.ResponseRecorder {
			// CheckAccess has no broker arm; the no-oracle renderer answers as missing.
			return f.asBroker(t, http.MethodGet, path, nil)
		}},
	}

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			missing := gpGateMissingBody(t, arm.call)

			rec := arm.call(path)
			require.Equal(t, http.StatusNotFound, rec.Code,
				"%s must be refused; body=%s", arm.name, rec.Body.String())
			gpGateRequireRecordAbsent(t, rec.Body.String())
			require.Equal(t, missing, rec.Body.String(),
				"refused-real must be byte-identical to missing FOR THE SAME CALLER "+
					"(no existence oracle)")
		})
	}
}

// TestGPGate_Unauthenticated is the middleware control: no credential is 401,
// upstream of the gate, and still ships no record.
func TestGPGate_Unauthenticated(t *testing.T) {
	f := wsGateSetup(t)
	p := gpGateProject(t, f)

	rec := f.anonymous(t, http.MethodGet, "/api/v1/projects/"+p.ID, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code,
		"unauthenticated caller must be 401; body=%s", rec.Body.String())
	gpGateRequireRecordAbsent(t, rec.Body.String())
}

// TestGPGate_PositiveControls pins that legitimate readers are still served the
// full record: the owner, a hub admin, a member with an explicit project-read
// grant, and the in-project agent reading its own project. Without these, every
// refusal above is satisfiable by a gate that denies everyone.
func TestGPGate_PositiveControls(t *testing.T) {
	f := wsGateSetup(t)
	ctx := context.Background()
	p := gpGateProject(t, f)
	path := "/api/v1/projects/" + p.ID

	// An admin user (role=admin → admin bypass in CheckAccess).
	admin := &store.User{
		ID: tid("gpgate-admin"), Email: "gpgate-admin@example.com",
		DisplayName: "Gp Admin", Role: store.UserRoleAdmin, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, f.store.CreateUser(ctx, admin))

	// A member: a user granted project read via an explicit allow policy binding
	// (the authz model's read grant, distinct from the owner/admin bypass).
	member := &store.User{
		ID: tid("gpgate-member"), Email: "gpgate-member@example.com",
		DisplayName: "Gp Member", Role: store.UserRoleMember, Status: "active",
		Created: time.Now(),
	}
	require.NoError(t, f.store.CreateUser(ctx, member))
	readPol := &store.Policy{
		ID: tid("gpgate-member-read"), Name: "gpgate-member-read-projP",
		ScopeType: "project", ScopeID: p.ID, ResourceType: "project",
		Actions: []string{"read"}, Effect: "allow",
	}
	require.NoError(t, f.store.CreatePolicy(ctx, readPol))
	require.NoError(t, f.store.AddPolicyBinding(ctx, &store.PolicyBinding{
		PolicyID: readPol.ID, PrincipalType: "user", PrincipalID: member.ID,
	}))

	// An agent inside p (read baseline allows read of its own project).
	insiderP := &store.Agent{
		ID: tid("gpgate-insider"), Slug: tid("gpgate-insider"), Name: "gpgate-insider",
		ProjectID: p.ID, CreatedBy: f.owner.ID, OwnerID: f.owner.ID,
	}
	require.NoError(t, f.store.CreateAgent(ctx, insiderP))

	assertServed := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code,
			"legitimate reader must be served; body=%s", rec.Body.String())
		var got ProjectWithCapabilities
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, gpGateSecretName, got.Name)
		require.Equal(t, gpGateSecretSlug, got.Slug)
		require.Equal(t, gpGateSecretGitRemote, got.GitRemote)
		// N40: positive control for the anti-enrichment check in
		// gpGateRequireRecordAbsent, which asserts NotContains(body,
		// `"ownerName":"Owner"`). Without a sibling green proving the owner display
		// name CAN appear in a served body, a renamed json tag, a dropped field, or
		// added indentation would make that NotContains pass vacuously forever and
		// silently retire the check. f.owner's DisplayName is "Owner" (wsGateSetup).
		require.Equal(t, "Owner", got.OwnerName,
			"served getProject must enrich the owner display name, which is the "+
				"presence half that makes the refusal-arm NotContains non-vacuous")
	}

	t.Run("owner", func(t *testing.T) {
		assertServed(t, f.asUser(t, f.owner, http.MethodGet, path, nil))
	})
	t.Run("admin", func(t *testing.T) {
		assertServed(t, f.asUser(t, admin, http.MethodGet, path, nil))
	})
	t.Run("member with read grant", func(t *testing.T) {
		assertServed(t, f.asUser(t, member, http.MethodGet, path, nil))
	})
	t.Run("in-project agent", func(t *testing.T) {
		assertServed(t, f.asAgent(t, insiderP, http.MethodGet, path, nil))
	})
}

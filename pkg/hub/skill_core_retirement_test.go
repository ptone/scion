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
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateSkill_CoreScopeRejected verifies that POST /api/v1/skills with
// scope="core" returns 400 Bad Request with a message naming "global" for
// every caller (super-admin, global-catalog-author, and plain hub member), and
// stores no skill record.
func TestCreateSkill_CoreScopeRejected(t *testing.T) {
	srv, s, alice, _, _ := setupSkillAuthzTest(t)
	ctx := context.Background()

	superAdminID := tid("core-retire-superadmin")
	createTestUserWithRole(t, s, superAdminID, superAdminID+"@test.com", store.UserRoleAdmin, store.SystemRoleSuperAdmin)
	superAdmin, err := s.GetUser(ctx, superAdminID)
	require.NoError(t, err)

	authorID := tid("core-retire-author")
	createTestUserWithRole(t, s, authorID, authorID+"@test.com", store.UserRoleMember, store.SystemRoleGlobalCatalogAuthor)
	ensureHubMembership(ctx, s, authorID)
	author, err := s.GetUser(ctx, authorID)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		user *store.User
	}{
		{"super-admin", superAdmin},
		{"global-catalog-author", author},
		{"hub-member", alice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequestAsUser(t, srv, tc.user, http.MethodPost, "/api/v1/skills", CreateSkillRequest{
				Name:  "retired-core-" + tc.name,
				Scope: "core",
			})
			assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			var errResp ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
			assert.Equal(t, "validation_error", errResp.Error.Code)
			assert.Contains(t, errResp.Error.Message, `"global"`, "400 response should direct callers to global scope")
			assert.Contains(t, errResp.Error.Message, "no longer supported")
		})
	}

	listed, err := s.ListSkills(ctx, store.SkillFilter{}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, listed.Items, "rejected core creates must store nothing")

	// Also verify POST /api/v1/skills/resolve rejects skill://core/... URIs
	// with invalid_uri and logs the guidance naming skill://global/<name>.
	logs := captureJSONSlog(t)
	rr := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve", ResolveSkillsRequest{
		Skills: []ResolveSkillRef{
			{URI: "skill://core/my-skill"},
			{URI: "skill://scion/core/my-skill@1.0"},
		},
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resolveResp ResolveSkillsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resolveResp))
	assert.Empty(t, resolveResp.Resolved)
	require.Len(t, resolveResp.Errors, 2)
	for _, e := range resolveResp.Errors {
		assert.Equal(t, "invalid_uri", e.Code)
	}
	logged := logs()
	var sawGuidance bool
	for _, rec := range logged {
		if msg, _ := rec["error"].(string); strings.Contains(msg, "skill://global/my-skill") {
			sawGuidance = true
			break
		}
	}
	assert.True(t, sawGuidance, "resolver log should record the skill://global/<name> migration hint")
}

// TestListSkills_CoreScopeRejected verifies that GET /api/v1/skills?scope=core
// returns 400 Bad Request with the retired-scope message naming "global".
func TestListSkills_CoreScopeRejected(t *testing.T) {
	srv, s, alice, _, _ := setupSkillAuthzTest(t)
	ctx := context.Background()

	superAdminID := tid("core-list-superadmin")
	createTestUserWithRole(t, s, superAdminID, superAdminID+"@test.com", store.UserRoleAdmin, store.SystemRoleSuperAdmin)
	superAdmin, err := s.GetUser(ctx, superAdminID)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		user *store.User
	}{
		{"super-admin", superAdmin},
		{"hub-member", alice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequestAsUser(t, srv, tc.user, http.MethodGet, "/api/v1/skills?scope=core", nil)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			var errResp ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
			assert.Equal(t, "validation_error", errResp.Error.Code)
			assert.Equal(t, retiredSkillScopeCoreMessage, errResp.Error.Message)
			assert.Contains(t, errResp.Error.Message, `"global"`)
		})
	}
}

// TestDetermineScopeSearchOrder verifies the default search order is
// user -> project -> global, with core completely retired.
func TestDetermineScopeSearchOrder(t *testing.T) {
	t.Run("unscoped with user and project", func(t *testing.T) {
		uri := &api.SkillURI{Registry: "scion", ScopeID: "user-1", Name: "my-skill"}
		got := determineScopeSearchOrder(uri, "proj-1")
		assert.Equal(t, []scopeEntry{
			{scope: store.SkillScopeUser, scopeID: "user-1"},
			{scope: store.SkillScopeProject, scopeID: "proj-1"},
			{scope: store.SkillScopeGlobal},
		}, got)
	})

	t.Run("unscoped with project only", func(t *testing.T) {
		uri := &api.SkillURI{Registry: "scion", Name: "my-skill"}
		got := determineScopeSearchOrder(uri, "proj-1")
		assert.Equal(t, []scopeEntry{
			{scope: store.SkillScopeProject, scopeID: "proj-1"},
			{scope: store.SkillScopeGlobal},
		}, got)
	})

	t.Run("unscoped with neither user nor project", func(t *testing.T) {
		uri := &api.SkillURI{Registry: "scion", Name: "my-skill"}
		got := determineScopeSearchOrder(uri, "")
		assert.Equal(t, []scopeEntry{
			{scope: store.SkillScopeGlobal},
		}, got)
	})

	t.Run("explicit global alias searches only global", func(t *testing.T) {
		uri, err := api.ParseSkillURI("skill://global/my-skill")
		require.NoError(t, err)
		got := determineScopeSearchOrder(uri, "proj-1")
		assert.Equal(t, []scopeEntry{
			{scope: store.SkillScopeGlobal},
		}, got)
	})
}

// TestResolveSkill_DefaultSearchOrderUserProjectGlobal verifies end-to-end
// skill resolution follows user -> project -> global and supports explicit
// skill://global/<slug> and skill://user/<slug> selection.
func TestResolveSkill_DefaultSearchOrderUserProjectGlobal(t *testing.T) {
	srv, s, alice, _, project := setupSkillAuthzTest(t)
	ctx := context.Background()
	stor, err := storage.NewLocal(storage.Config{Provider: storage.ProviderLocal, Bucket: "b", LocalPath: t.TempDir()})
	require.NoError(t, err)
	srv.SetStorage(stor)

	const slug = "layered-skill"
	mkScoped := func(scope, scopeID, ver string) *store.Skill {
		sk := &store.Skill{
			ID:          api.NewUUID(),
			Name:        slug,
			Slug:        slug,
			Scope:       scope,
			ScopeID:     scopeID,
			StoragePath: "hubs/local/skills/" + scope + "/" + scopeID + "/" + slug,
			Status:      "active",
			OwnerID:     alice.ID,
			Created:     time.Now(),
			Updated:     time.Now(),
		}
		require.NoError(t, s.CreateSkill(ctx, sk))
		addLocalSkillVersion(t, srv, stor, sk, ver, map[string][]byte{"SKILL.md": []byte("# v" + ver)})
		return sk
	}

	mkScoped(store.SkillScopeGlobal, "", "1.0.0")
	projSkill := mkScoped(store.SkillScopeProject, project.ID, "2.0.0")
	userSkill := mkScoped(store.SkillScopeUser, alice.ID, "3.0.0")

	aliceIdent := NewAuthenticatedUser(alice.ID, alice.Email, alice.DisplayName, alice.Role, "web")
	unscopedWithUser := &api.SkillURI{Registry: "scion", ScopeID: alice.ID, Name: slug}

	resolveHTTP := func(uriStr, projID, userID string) string {
		rec := doRequestAsUser(t, srv, alice, http.MethodPost, "/api/v1/skills/resolve", ResolveSkillsRequest{
			ProjectID: projID,
			UserID:    userID,
			Skills:    []ResolveSkillRef{{URI: uriStr}},
		})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp ResolveSkillsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Empty(t, resp.Errors, "unexpected errors: %+v", resp.Errors)
		require.Len(t, resp.Resolved, 1)
		return resp.Resolved[0].ResolvedVersion
	}

	// All three present: user wins in resolveSkill(user -> project -> global) and via skill://user/<slug>.
	_, sv, err := srv.resolveSkill(ctx, aliceIdent, unscopedWithUser, project.ID)
	require.NoError(t, err)
	assert.Equal(t, "3.0.0", sv.Version)
	assert.Equal(t, "3.0.0", resolveHTTP("skill://user/"+slug, project.ID, alice.ID))
	// Explicit global alias bypasses user and project.
	assert.Equal(t, "1.0.0", resolveHTTP("skill://global/"+slug, project.ID, alice.ID))

	// Remove user skill: project wins.
	require.NoError(t, s.DeleteSkill(ctx, userSkill.ID))
	_, sv, err = srv.resolveSkill(ctx, aliceIdent, unscopedWithUser, project.ID)
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", sv.Version)
	assert.Equal(t, "2.0.0", resolveHTTP(slug, project.ID, alice.ID))

	// Remove project skill: global wins (no core fallback remains).
	require.NoError(t, s.DeleteSkill(ctx, projSkill.ID))
	_, sv, err = srv.resolveSkill(ctx, aliceIdent, unscopedWithUser, project.ID)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", sv.Version)
	assert.Equal(t, "1.0.0", resolveHTTP(slug, project.ID, alice.ID))
}

// TestMigrateSkillCoreScope_KeepsStoragePath_FilesDownloadable verifies
// end-to-end on the Hub HTTP API that skills migrated from scope="core" to
// scope="global" (including a collision-renamed skill) keep their stored
// StoragePath and continue serving files over /files, /download, and /resolve
// without moving objects in storage.
func TestMigrateSkillCoreScope_KeepsStoragePath_FilesDownloadable(t *testing.T) {
	srv, s, alice, carol, project := setupSkillScopeTest(t)
	ctx := context.Background()

	stor, err := storage.NewLocal(storage.Config{Provider: storage.ProviderLocal, Bucket: "b", LocalPath: t.TempDir()})
	require.NoError(t, err)
	srv.SetStorage(stor)

	now := time.Now()
	// 1. Non-colliding legacy core skill stored at un-scoped legacy path.
	soloCore := &store.Skill{
		ID:          api.NewUUID(),
		Name:        "solo-legacy",
		Slug:        "solo-legacy",
		Scope:       "core",
		StoragePath: "hubs/local/skills/solo-legacy",
		Status:      "active",
		OwnerID:     alice.ID,
		Created:     now,
		Updated:     now,
	}
	require.NoError(t, s.CreateSkill(ctx, soloCore))
	soloBytes := []byte("---\nname: solo-legacy\n---\n# Solo Core Content\n")
	addLocalSkillVersion(t, srv, stor, soloCore, "1.0.0", map[string][]byte{"SKILL.md": soloBytes})

	// 2. Colliding pair: global "clash-skill" and core "clash-skill" with distinct storage paths.
	globalClash := &store.Skill{
		ID:          api.NewUUID(),
		Name:        "clash-skill",
		Slug:        "clash-skill",
		Scope:       store.SkillScopeGlobal,
		StoragePath: "hubs/local/skills/global/clash-skill",
		Status:      "active",
		OwnerID:     alice.ID,
		Created:     now,
		Updated:     now,
	}
	require.NoError(t, s.CreateSkill(ctx, globalClash))
	globalClashBytes := []byte("---\nname: clash-skill\n---\n# Global Clash Content\n")
	addLocalSkillVersion(t, srv, stor, globalClash, "1.0.0", map[string][]byte{"SKILL.md": globalClashBytes})

	coreClash := &store.Skill{
		ID:          api.NewUUID(),
		Name:        "clash-skill",
		Slug:        "clash-skill",
		Scope:       "core",
		StoragePath: "hubs/local/skills/clash-skill",
		Status:      "active",
		OwnerID:     alice.ID,
		Created:     now,
		Updated:     now,
	}
	require.NoError(t, s.CreateSkill(ctx, coreClash))
	coreClashBytes := []byte("---\nname: clash-skill\n---\n# Core Clash Content\n")
	addLocalSkillVersion(t, srv, stor, coreClash, "1.0.0", map[string][]byte{"SKILL.md": coreClashBytes})

	// Seed a project skill injection pointing to skill://core/clash-skill.
	require.NoError(t, s.AddSkillInjection(ctx, &store.SkillInjection{
		ID:        api.NewUUID(),
		Scope:     store.SkillInjectionScopeProject,
		ScopeID:   project.ID,
		SkillURI:  "skill://core/clash-skill",
		SortOrder: 0,
		CreatedAt: now,
		CreatedBy: alice.ID,
	}))

	// Run the startup migration through store.Migrate -> entc.AutoMigrate.
	require.NoError(t, s.Migrate(ctx))

	// Verify DB rows migrated and kept StoragePath intact.
	gotSolo, err := s.GetSkill(ctx, soloCore.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SkillScopeGlobal, gotSolo.Scope)
	assert.Equal(t, "solo-legacy", gotSolo.Slug)
	assert.Equal(t, "hubs/local/skills/solo-legacy", gotSolo.StoragePath)

	gotCoreClash, err := s.GetSkill(ctx, coreClash.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SkillScopeGlobal, gotCoreClash.Scope)
	assert.Equal(t, "clash-skill-core", gotCoreClash.Slug)
	assert.Equal(t, "hubs/local/skills/clash-skill", gotCoreClash.StoragePath)

	injs, err := s.ListSkillInjections(ctx, store.SkillInjectionScopeProject, project.ID)
	require.NoError(t, err)
	require.Len(t, injs, 1)
	assert.Equal(t, "skill://scion/global/clash-skill-core", injs[0].SkillURI)

	// Verify Carol (an ordinary hub member) can read, download, and resolve both migrated skills!
	for _, tc := range []struct {
		name      string
		skillID   string
		uri       string
		wantBytes []byte
	}{
		{"migrated-solo", soloCore.ID, "skill://global/solo-legacy@1.0.0", soloBytes},
		{"migrated-clash-core", coreClash.ID, injs[0].SkillURI, coreClashBytes},
		{"existing-global-clash", globalClash.ID, "skill://global/clash-skill@1.0.0", globalClashBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 1. Direct raw file serve (/files/SKILL.md?raw=true)
			fileRec := doRequestAsUser(t, srv, carol, http.MethodGet,
				"/api/v1/skills/"+tc.skillID+"/files/SKILL.md?version=1.0.0&raw=true", nil)
			require.Equal(t, http.StatusOK, fileRec.Code, fileRec.Body.String())
			assert.Equal(t, string(tc.wantBytes), fileRec.Body.String())

			// 2. Download manifest (/download?version=1.0.0)
			dlRec := doRequestAsUser(t, srv, carol, http.MethodGet,
				"/api/v1/skills/"+tc.skillID+"/download?version=1.0.0", nil)
			require.Equal(t, http.StatusOK, dlRec.Code, dlRec.Body.String())
			var dlResp DownloadResponse
			require.NoError(t, json.Unmarshal(dlRec.Body.Bytes(), &dlResp))
			require.Len(t, dlResp.Files, 1)
			assert.Equal(t, "SKILL.md", dlResp.Files[0].Path)
			assert.NotEmpty(t, dlResp.Files[0].URL)

			// 3. Batch resolve (/api/v1/skills/resolve)
			resRec := doRequestAsUser(t, srv, carol, http.MethodPost, "/api/v1/skills/resolve", ResolveSkillsRequest{
				ProjectID: project.ID,
				UserID:    carol.ID,
				Skills:    []ResolveSkillRef{{URI: tc.uri}},
			})
			require.Equal(t, http.StatusOK, resRec.Code, resRec.Body.String())
			var resResp ResolveSkillsResponse
			require.NoError(t, json.Unmarshal(resRec.Body.Bytes(), &resResp))
			require.Empty(t, resResp.Errors, "errors: %+v", resResp.Errors)
			require.Len(t, resResp.Resolved, 1)
			require.Len(t, resResp.Resolved[0].Files, 1)

			// Fetch the local download URL returned by /resolve through srv.Handler()
			dlURL := resResp.Resolved[0].Files[0].URL
			if idx := strings.Index(dlURL, "/api/v1/"); idx >= 0 {
				dlURL = dlURL[idx:]
			}
			fetchRec := doRequestAsUser(t, srv, carol, http.MethodGet, dlURL, nil)
			require.Equal(t, http.StatusOK, fetchRec.Code, fetchRec.Body.String())
			assert.Equal(t, string(tc.wantBytes), fetchRec.Body.String())
		})
	}
}

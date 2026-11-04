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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The artifacts section on the server-config API (ptone/scion#7107 phase
// 2b, spec r3 condition 4): admin session only, the whole section refused
// for user access tokens (writes and resets), every key change audited,
// file mode refuses. Kill checks K8, K9, K15, K16, K17 and K18.

// artifactsSettingsFixture is a DB-mode server whose public origin is
// loTestHubOrigin, with a SQLite store for audit records behind a switch
// that makes audit writes fail.
func artifactsSettingsFixture(t *testing.T) (*Server, *fakeHubSettingStore, *OperationalSettings, store.Store, *storeFaultSwitch) {
	t.Helper()
	srv, fake, ops, st, fault := publicURLAuditFixture(t)
	srv.config.HubEndpoints = &HubEndpoints{Operator: loTestHubOrigin, Public: loTestHubOrigin}
	return srv, fake, ops, st, fault
}

// storedArtifacts returns the stored artifacts document, or nil when the
// section has no row.
func storedArtifacts(t *testing.T, fake *fakeHubSettingStore) map[string]interface{} {
	t.Helper()
	row, err := fake.GetHubSetting(context.Background(), artifactsSection)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(row.Value, &m))
	return m
}

// artifactsAudits returns the audit records of mutationType for artifacts
// keys, keyed by key.
func artifactsAudits(t *testing.T, st store.Store, mutationType string) []*store.MutationAuditRecord {
	t.Helper()
	recs, _, err := st.ListMutationAudits(context.Background(), store.MutationAuditFilter{MutationType: mutationType})
	require.NoError(t, err)
	var out []*store.MutationAuditRecord
	for _, r := range recs {
		if strings.HasPrefix(r.TargetID, artifactsSection+".") {
			out = append(out, r)
		}
	}
	return out
}

// TestServerConfigArtifacts_TokensRefused: a user access token can write no
// artifacts key, an empty artifacts object included, and cannot reset the
// section; a body mixing an allowed key with artifacts is refused whole and
// the allowed key is not stored. K8, K16, K17.
func TestServerConfigArtifacts_TokensRefused(t *testing.T) {
	srv, s := testServerWithOps(t, nil)
	admin := hubConfigTokenUser(t, s, "art-set-super", store.SystemRoleSuperAdmin)
	key := mintHubConfigToken(t, srv, admin, hubBoundary(), "hub_config:read", "hub_config:update")

	for _, tc := range []struct {
		name string
		body string
		keys []string
	}{
		{"link origin", `{"artifacts":{"link_origin":"https://links.example.net"}}`, []string{"artifacts"}},
		{"link origin cleared", `{"artifacts":{"link_origin":""}}`, []string{"artifacts"}},
		{"max link ttl", `{"artifacts":{"link_max_ttl_hours":100}}`, []string{"artifacts"}},
		{"retention", `{"artifacts":{"default_retention_days":3}}`, []string{"artifacts"}},
		{"empty object", `{"artifacts":{}}`, []string{"artifacts"}},
		{"null", `{"artifacts":null}`, []string{"artifacts"}},
		{"case-folded member", `{"Artifacts":{"link_max_ttl_hours":100}}`, []string{"artifacts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost} {
				rec := doRequestWithToken(t, srv, key, method, "/api/v1/admin/server-config", json.RawMessage(tc.body))
				requireTokenRefusedKeys(t, rec, tc.keys...)
			}
		})
	}

	t.Run("mixed body refused whole", func(t *testing.T) {
		before, beforeErr := s.GetHubSetting(context.Background(), "agent_defaults")
		rec := doRequestWithToken(t, srv, key, http.MethodPut, "/api/v1/admin/server-config",
			json.RawMessage(`{"default_max_turns":9,"artifacts":{"link_max_ttl_hours":100}}`))
		requireTokenRefusedKeys(t, rec, "artifacts")
		after, afterErr := s.GetHubSetting(context.Background(), "agent_defaults")
		assert.Equal(t, beforeErr, afterErr, "the allowed section changed")
		if before != nil && after != nil {
			assert.JSONEq(t, string(before.Value), string(after.Value), "the allowed key was applied")
			assert.Equal(t, before.Revision, after.Revision)
		}
		_, err := s.GetHubSetting(context.Background(), artifactsSection)
		assert.ErrorIs(t, err, store.ErrNotFound, "the artifacts section was written")
		// The same allowed key alone is still a token's to write.
		rec = doRequestWithToken(t, srv, key, http.MethodPut, "/api/v1/admin/server-config", json.RawMessage(`{"default_max_turns":9}`))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("section reset refused", func(t *testing.T) {
		rec := doRequestWithToken(t, srv, key, http.MethodDelete, "/api/v1/admin/server-config/sections/artifacts", nil)
		requireTokenRefusedKeys(t, rec, "artifacts")
	})

	assert.Equal(t, settingsTokenRefused, serverConfigTokenSections[artifactsSection])
	assert.False(t, serverConfigSectionTokenResettable(artifactsSection))
	for _, k := range []string{"artifacts", "artifacts.link_origin", "artifacts.link_max_ttl_hours", "artifacts.default_retention_days"} {
		assert.Equal(t, settingsTokenRefused, serverConfigKeyTokenClass(k), k)
	}
}

// TestServerConfigArtifacts_AdminSessionWrites: an interactive admin
// session writes link_origin (stored normalized) and the TTL keys; keys the
// body leaves out keep their value; "" clears link_origin. The hub then
// uses the setting.
func TestServerConfigArtifacts_AdminSessionWrites(t *testing.T) {
	srv, fake, ops, _, _ := artifactsSettingsFixture(t)

	rr := putServerConfigDB(t, srv, ops, `{"artifacts":{"link_origin":"HTTPS://Links.Example.NET:443/","link_default_ttl_hours":24,"link_max_ttl_hours":48}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	got := storedArtifacts(t, fake)
	assert.Equal(t, "https://links.example.net", got["link_origin"])
	assert.EqualValues(t, 24, got["link_default_ttl_hours"])
	assert.EqualValues(t, 48, got["link_max_ttl_hours"])
	assert.Equal(t, "https://links.example.net", srv.artifactLinkOrigin())

	rr = putServerConfigDB(t, srv, ops, `{"artifacts":{"default_retention_days":30}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	got = storedArtifacts(t, fake)
	assert.Equal(t, "https://links.example.net", got["link_origin"], "an omitted key keeps its value")
	assert.EqualValues(t, 30, got["default_retention_days"])

	rr = putServerConfigDB(t, srv, ops, `{"artifacts":{"link_origin":""}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	got = storedArtifacts(t, fake)
	_, has := got["link_origin"]
	assert.False(t, has, "link_origin not cleared: %v", got)
	assert.Empty(t, srv.artifactLinkOrigin())
}

// TestServerConfigArtifacts_Validation: an invalid link_origin, one equal to
// the hub's public origin, an out-of-range key and a non-object are refused
// with nothing written; a same-site link_origin is saved with a warning.
// Condition 5, K10, K11.
func TestServerConfigArtifacts_Validation(t *testing.T) {
	srv, fake, ops, st, _ := artifactsSettingsFixture(t)

	for _, body := range []string{
		`{"artifacts":{"link_origin":"https://links.example.net/share"}}`,
		`{"artifacts":{"link_origin":"http://links.example.net"}}`,
		`{"artifacts":{"link_origin":"https://u@links.example.net"}}`,
		`{"artifacts":{"link_origin":"` + loTestHubOrigin + `"}}`,
		`{"artifacts":{"link_origin":"https://HUB.example.com:443"}}`,
		`{"artifacts":{"link_origin":"https://hub.example.com:8443"}}`,
		`{"artifacts":{"link_origin":"https://HUB.EXAMPLE.COM"}}`,
		`{"artifacts":{"link_max_ttl_hours":0}}`,
		`{"artifacts":{"link_default_ttl_hours":800}}`,
		`{"artifacts":{"unknown_key":1}}`,
		`{"artifacts":"x"}`,
		`{"artifacts":null}`,
	} {
		rr := putServerConfigDB(t, srv, ops, body)
		assert.Equal(t, http.StatusUnprocessableEntity, rr.Code, "%s: %s", body, rr.Body.String())
		assert.Nil(t, storedArtifacts(t, fake), "%s: written", body)
	}
	assert.Empty(t, artifactsAudits(t, st, gcpIAMAuditMutation), "a refused save was audited")

	rr := putServerConfigDB(t, srv, ops, `{"artifacts":{"link_origin":"https://links.example.com"}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "registrable domain")
	assert.Equal(t, "https://links.example.com", storedArtifacts(t, fake)["link_origin"])

	// A workstation hub on http://localhost:8080: the same host name under
	// another scheme or port is refused; another loopback name is accepted.
	srv.config.HubEndpoints = &HubEndpoints{Operator: "http://localhost:8080", Public: "http://localhost:8080"}
	for _, body := range []string{
		`{"artifacts":{"link_origin":"https://localhost:8080"}}`,
		`{"artifacts":{"link_origin":"http://localhost:9000"}}`,
		`{"artifacts":{"link_origin":"http://LOCALHOST:8080"}}`,
	} {
		rr := putServerConfigDB(t, srv, ops, body)
		assert.Equal(t, http.StatusUnprocessableEntity, rr.Code, "%s: %s", body, rr.Body.String())
		assert.Equal(t, "https://links.example.com", storedArtifacts(t, fake)["link_origin"], "%s: written", body)
	}
	rr = putServerConfigDB(t, srv, ops, `{"artifacts":{"link_origin":"http://127.0.0.1:9000"}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "http://127.0.0.1:9000", storedArtifacts(t, fake)["link_origin"])
}

// TestServerConfigArtifacts_Audited: each artifacts key a save changes
// writes one audit record with its old and new value and the actor; a
// save that changes nothing writes none; a reset records every cleared key.
// K9, K15.
func TestServerConfigArtifacts_Audited(t *testing.T) {
	srv, _, ops, st, _ := artifactsSettingsFixture(t)

	rr := putServerConfigDB(t, srv, ops, `{"artifacts":{"link_origin":"https://links.example.net","link_max_ttl_hours":48}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = putServerConfigDB(t, srv, ops, `{"artifacts":{"link_origin":"https://other.example.net","link_max_ttl_hours":48,"default_retention_days":7}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	// Unchanged: no record.
	rr = putServerConfigDB(t, srv, ops, `{"artifacts":{"link_origin":"https://other.example.net","link_max_ttl_hours":48}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	type change struct{ key, before, after string }
	var got []change
	for _, r := range artifactsAudits(t, st, gcpIAMAuditMutation) {
		assert.Equal(t, gcpIAMAuditTarget, r.TargetType)
		assert.Equal(t, gcpIAMSurfaceKind, r.ExecutorKind)
		assert.NotEmpty(t, r.ActorPrincipalID, "the actor is recorded")
		got = append(got, change{r.TargetID, r.BeforeSummary, r.AfterSummary})
	}
	assert.ElementsMatch(t, []change{
		{"artifacts.link_origin", "", "https://links.example.net"},
		{"artifacts.link_max_ttl_hours", "", "48"},
		{"artifacts.link_origin", "https://links.example.net", "https://other.example.net"},
		{"artifacts.default_retention_days", "", "7"},
	}, got)

	rec := httptest.NewRecorder()
	srv.handleAdminServerConfigSectionReset(rec, adminRequest(http.MethodDelete, "/api/v1/admin/server-config/sections/artifacts", ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got = nil
	for _, r := range artifactsAudits(t, st, gcpIAMAuditMutation) {
		if r.ExecutorID == gcpIAMSurfaceReset {
			got = append(got, change{r.TargetID, r.BeforeSummary, r.AfterSummary})
		}
	}
	assert.ElementsMatch(t, []change{
		{"artifacts.link_origin", "https://other.example.net", ""},
		{"artifacts.link_max_ttl_hours", "48", ""},
		{"artifacts.default_retention_days", "7", ""},
	}, got)
	assert.Empty(t, artifactsAudits(t, st, gcpIAMNotAppliedMutation))
}

// TestServerConfigArtifacts_AuditFailureBlocksChange: when the audit record
// cannot be written, the change is not made.
func TestServerConfigArtifacts_AuditFailureBlocksChange(t *testing.T) {
	srv, fake, ops, _, fault := artifactsSettingsFixture(t)
	rr := putServerConfigDB(t, srv, ops, `{"artifacts":{"link_max_ttl_hours":48}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	fault.Arm()
	rr = putServerConfigDB(t, srv, ops, `{"artifacts":{"link_origin":"https://links.example.net"}}`)
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	_, has := storedArtifacts(t, fake)["link_origin"]
	assert.False(t, has, "the change was made without its audit record")
}

// TestServerConfigArtifacts_FileModeRefused: a hub with no settings
// database refuses the artifacts section for every caller. K18.
func TestServerConfigArtifacts_FileModeRefused(t *testing.T) {
	srv, _, _ := fileModeGCPIdentityServer(t)
	for _, body := range []string{
		`{"artifacts":{"link_origin":"https://links.example.net"}}`,
		`{"artifacts":{}}`,
		`{"default_max_turns":5,"artifacts":{"link_max_ttl_hours":48}}`,
	} {
		rr := httptest.NewRecorder()
		srv.handlePutServerConfig(rr, adminRequest(http.MethodPut, "/api/v1/admin/server-config", body))
		require.Equal(t, http.StatusUnprocessableEntity, rr.Code, "%s: %s", body, rr.Body.String())
		assert.Contains(t, rr.Body.String(), "db_only_section_rejected", body)
	}

	tokenSrv, tokenStore := testServer(t)
	admin := hubConfigTokenUser(t, tokenStore, "art-file-super", store.SystemRoleSuperAdmin)
	key := mintHubConfigToken(t, tokenSrv, admin, hubBoundary(), "hub_config:read", "hub_config:update")
	rec := doRequestWithToken(t, tokenSrv, key, http.MethodPut, "/api/v1/admin/server-config",
		json.RawMessage(`{"artifacts":{"link_max_ttl_hours":48}}`))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "db_only_section_rejected")
}

// artifactsReadFailingStore fails reads of the artifacts section once armed.
type artifactsReadFailingStore struct {
	*fakeHubSettingStore
	armed bool
}

func (f *artifactsReadFailingStore) GetHubSetting(ctx context.Context, section string) (*store.HubSetting, error) {
	if f.armed && section == artifactsSection {
		return nil, errors.New("injected settings read failure")
	}
	return f.fakeHubSettingStore.GetHubSetting(ctx, section)
}

// TestServerConfigArtifacts_ResetRefusedWhenUnreadable: a reset whose
// stored artifacts row cannot be read (other than a missing row) is refused
// and nothing is deleted or audited, so a reset is never made without its
// per-key records.
func TestServerConfigArtifacts_ResetRefusedWhenUnreadable(t *testing.T) {
	srv, fake, ops, st, _ := artifactsSettingsFixture(t)
	rr := putServerConfigDB(t, srv, ops, `{"artifacts":{"link_max_ttl_hours":48}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	before := len(artifactsAudits(t, st, gcpIAMAuditMutation))

	failing := &artifactsReadFailingStore{fakeHubSettingStore: fake, armed: true}
	ops.store = failing
	rec := httptest.NewRecorder()
	srv.handleAdminServerConfigSectionReset(rec, adminRequest(http.MethodDelete, "/api/v1/admin/server-config/sections/artifacts", ""))
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	failing.armed = false
	assert.EqualValues(t, 48, storedArtifacts(t, fake)["link_max_ttl_hours"], "the section was reset")
	assert.Len(t, artifactsAudits(t, st, gcpIAMAuditMutation), before, "a refused reset was audited")
}

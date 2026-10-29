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

package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// placementRuntimesDoc is a stored runtimes section with a pool runtime.
const placementRuntimesDoc = `{"k8s":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"}}}`

// requireRow asserts a section's stored revision and JSON value.
func requirePlacementRow(t *testing.T, f *fakeHubSettingStore, section string, rev int64, doc string) {
	t.Helper()
	f.mu.Lock()
	row := f.settings[section]
	f.mu.Unlock()
	require.NotNil(t, row, "%s row missing", section)
	assert.Equal(t, rev, row.Revision, section)
	assert.JSONEq(t, doc, string(row.Value), section)
}

// requireNoPlacementRow asserts that a section has no stored row.
func requireNoPlacementRow(t *testing.T, f *fakeHubSettingStore, section string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Nil(t, f.settings[section], "%s was written", section)
}

// Invalid placement is refused before anything is written: malformed or
// misspelt keys with 400 validation_failed (checked raw, before the typed
// decode could drop them), invalid values and placement on a runtime that
// is not Kubernetes with 422 validation_error naming the path only.
func TestPutSettingsRejectsInvalidPlacement(t *testing.T) {
	tempSettingsHome(t)
	cases := []struct {
		name, body string
		code       int
		path       string
	}{
		{"camelCase selector", `{"runtimes":{"k":{"type":"kubernetes","nodeSelector":{"a":"b"}}}}`, http.StatusBadRequest, "runtimes.k"},
		{"case-folded selector", `{"profiles":{"p":{"runtime":"docker","Node_Selector":{"a":"b"}}}}`, http.StatusBadRequest, "profiles.p"},
		{"toleration seconds", `{"runtimes":{"k":{"type":"kubernetes","tolerations":[{"key":"a","operator":"Exists","tolerationSeconds":5}]}}}`, http.StatusBadRequest, "runtimes.k"},
		{"selector number", `{"runtimes":{"k":{"type":"kubernetes","node_selector":{"a":1}}}}`, http.StatusBadRequest, "runtimes.k"},
		{"zero toleration seconds", `{"runtimes":{"k":{"type":"kubernetes","tolerations":[{"key":"a","operator":"Exists","tolerationSeconds":0}]}}}`, http.StatusBadRequest, "runtimes.k"},
		{"case-folded toleration key", `{"runtimes":{"k":{"type":"kubernetes","tolerations":[{"Key":"a","operator":"Exists"}]}}}`, http.StatusBadRequest, "runtimes.k"},
		{"case-folded section", `{"Runtimes":{"k":{"type":"kubernetes","nodeSelector":{"a":"b"}}}}`, http.StatusBadRequest, "runtimes"},
		{"case-folded profiles section", `{"PROFILES":{"p":{"runtime":"k8s"}}}`, http.StatusBadRequest, "profiles"},
		{"section sent twice", `{"runtimes":{"k":{"type":"kubernetes","nodeSelector":{"a":"b"}}},"runtimes":{"d":{"type":"docker"}}}`, http.StatusBadRequest, "runtimes"},
		{"invalid selector key", `{"runtimes":{"k":{"type":"kubernetes","node_selector":{"zz-private-value key!":"v"}}}}`, http.StatusUnprocessableEntity, "runtimes.k.node_selector"},
		{"invalid class", `{"profiles":{"p":{"runtime":"kubernetes","runtime_class_name":"Zz_Private_Value"}}}`, http.StatusUnprocessableEntity, "profiles.p.runtime_class_name"},
		{"placement on docker", `{"runtimes":{"d":{"type":"docker","runtime_class_name":"gvisor"}}}`, http.StatusUnprocessableEntity, "runtimes.d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, fake, ops := newTestDBServer(t)
			rr := putServerConfigDB(t, srv, ops, tc.body)
			require.Equal(t, tc.code, rr.Code, rr.Body.String())
			assert.Contains(t, rr.Body.String(), tc.path)
			assert.NotContains(t, strings.ToLower(rr.Body.String()), "zz-private-value", "the response echoes a submitted value")
			assert.NotContains(t, strings.ToLower(rr.Body.String()), "zz_private_value", "the response echoes a submitted value")
			requireNoPlacementRow(t, fake, "runtimes")
			requireNoPlacementRow(t, fake, "profiles")
		})
	}
}

// Runtime entries with the same context and namespace and different
// placement are refused as one save: neither is written.
func TestPutSettingsRejectsSiblingPlacement(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	rr := putServerConfigDB(t, srv, ops, `{"runtimes":{
		"a":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"}},
		"b":{"type":"kubernetes","namespace":"agents"}}}`)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "runtimes.a")
	requireNoPlacementRow(t, fake, "runtimes")

	// With identical base placement the pair saves.
	rr = putServerConfigDB(t, srv, ops, `{"runtimes":{
		"a":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"}},
		"b":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"}}}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

// A save of one section validates the pair with the stored value of the
// section it leaves out: a runtimes change that a stored profile now
// conflicts with is refused, naming the profile, and nothing is written.
func TestPutSettingsValidatesUnsubmittedProfileSection(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	fake.seedWithOrigin("profiles", json.RawMessage(`{"batch":{"runtime":"k8s","node_selector":{"scion.dev/pool":"batch"}}}`), "managed")
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)

	rr := putServerConfigDB(t, srv, ops, `{"runtimes":`+placementRuntimesDoc+`}`)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "profiles.batch.node_selector")
	requireNoPlacementRow(t, fake, "runtimes")

	// A profile with placement on a runtime entry that exists nowhere.
	rr = putServerConfigDB(t, srv, ops, `{"profiles":{"p":{"runtime":"nowhere","runtime_class_name":"gvisor"}}}`)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "profiles.p.runtime")
}

// racingProfilesStore changes the profiles row (another writer) just
// before the guarded runtimes write runs.
type racingProfilesStore struct {
	*fakeHubSettingStore
}

func (s *racingProfilesStore) UpsertHubSettingsGuarded(ctx context.Context, writes []store.HubSettingWrite, reads map[string]int64, updatedBy, origin string) ([]store.HubSetting, error) {
	if _, err := s.fakeHubSettingStore.UpsertHubSetting(ctx, "profiles",
		json.RawMessage(`{"batch":{"runtime":"k8s","node_selector":{"scion.dev/pool":"batch"}}}`), "other@example.com", -1, "managed"); err != nil {
		return nil, err
	}
	return s.fakeHubSettingStore.UpsertHubSettingsGuarded(ctx, writes, reads, updatedBy, origin)
}

// The revision of the omitted section read for the validation is compared
// in the same save: when another writer changes it in between, the save is
// a 409 conflict and writes nothing, also when the client sends no
// expected revisions.
func TestPutSettingsPlacementDependencyRevisionConflict(t *testing.T) {
	for _, body := range []string{
		`{"runtimes":` + placementRuntimesDoc + `}`,
		`{"runtimes":` + placementRuntimesDoc + `,"expected_revisions":{"runtimes":0}}`,
	} {
		t.Run(body, func(t *testing.T) {
			tempSettingsHome(t)
			srv, fake, ops := newTestDBServer(t)
			fake.seedWithOrigin("profiles", json.RawMessage(`{"plain":{"runtime":"k8s"}}`), "managed")
			ops.store = &racingProfilesStore{fakeHubSettingStore: fake}
			_, err := ops.Refresh(context.Background())
			require.NoError(t, err)

			rr := putServerConfigDB(t, srv, ops, body)
			require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
			var resp struct {
				Conflicted []map[string]interface{} `json:"conflicted"`
			}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
			require.Len(t, resp.Conflicted, 2, rr.Body.String())
			assert.Equal(t, "runtimes", resp.Conflicted[0]["section"])
			assert.Nil(t, resp.Conflicted[0]["read"])
			// The omitted section read for the validation, whose revision
			// changed, is listed as read at the revision read.
			assert.Equal(t, map[string]interface{}{
				"section": "profiles", "expected_revision": float64(1), "current_revision": float64(2), "read": true,
			}, resp.Conflicted[1])
			requireNoPlacementRow(t, fake, "runtimes")
		})
	}
}

// A save of both sections writes neither when one of them conflicts, and
// no other section of the save is written either.
func TestPutSettingsPlacementNoPartialWrite(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	fake.seedWithOrigin("profiles", json.RawMessage(`{"plain":{"runtime":"k8s"}}`), "managed")
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)

	rr := putServerConfigDB(t, srv, ops, `{"runtimes":`+placementRuntimesDoc+`,
		"profiles":{"plain":{"runtime":"k8s"},"fast":{"runtime":"k8s","runtime_class_name":"gvisor"}},
		"server":{"hub":{"auto_suspend_stalled":true}},
		"expected_revisions":{"profiles":7}}`)
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	requireNoPlacementRow(t, fake, "runtimes")
	requirePlacementRow(t, fake, "profiles", 1, `{"plain":{"runtime":"k8s"}}`)
	requireNoPlacementRow(t, fake, "lifecycle")

	// With the current revision both sections are written in one save.
	rr = putServerConfigDB(t, srv, ops, `{"runtimes":`+placementRuntimesDoc+`,
		"profiles":{"plain":{"runtime":"k8s"},"fast":{"runtime":"k8s","runtime_class_name":"gvisor"}},
		"expected_revisions":{"profiles":1}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	requirePlacementRow(t, fake, "runtimes", 1, placementRuntimesDoc)
	requirePlacementRow(t, fake, "profiles", 2, `{"plain":{"runtime":"k8s"},"fast":{"runtime":"k8s","runtime_class_name":"gvisor"}}`)
}

// unguardedHubSettingStore hides the guarded batch method of the fake.
type unguardedHubSettingStore struct {
	store.HubSettingStore
}

// A settings store that cannot write runtimes and profiles in one guarded
// transaction fails the save closed with a fixed message, and writes
// nothing; it never falls back to writing the sections one at a time.
func TestPutSettingsGuardedBatchUnsupportedFailsClosed(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	ops.store = unguardedHubSettingStore{HubSettingStore: fake}
	rr := putServerConfigDB(t, srv, ops, `{"runtimes":`+placementRuntimesDoc+`}`)
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "cannot save them in one transaction")
	requireNoPlacementRow(t, fake, "runtimes")
}

// Flat instance placement is Layer 0: a save that sends it, at the top
// level or nested in an accepted section, is refused and writes nothing.
// Each body has one refusal: the instance list under server.broker is a
// bootstrap key, and the other spellings are keys the save would drop.
func TestPutSettingsFlatPlacementNotWritable(t *testing.T) {
	tempSettingsHome(t)
	cases := []struct {
		body, marker string
	}{
		{`{"server":{"broker":{"instances":[{"key":"a","runtime_target":{"type":"kubernetes","node_selector":{"scion.dev/pool":"x"}}}]}}}`, `"layer0_rejected"`},
		{`{"server.broker.instances":[{"key":"a"}]}`, `"unpersisted_keys_rejected"`},
		{`{"runtimes":{"k":{"type":"kubernetes","instances":[{"runtime_target":{"node_selector":{"scion.dev/pool":"x"}}}]}}}`, `"unpersisted_keys_rejected"`},
		{`{"profiles":{"p":{"runtime":"kubernetes","runtime_target":{"node_selector":{"scion.dev/pool":"x"}}}}}`, `"unpersisted_keys_rejected"`},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			srv, fake, ops := newTestDBServer(t)
			rr := putServerConfigDB(t, srv, ops, tc.body)
			require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
			assert.Contains(t, rr.Body.String(), tc.marker)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			assert.Empty(t, fake.settings, "a section was written")
		})
	}
}

// A save that sends only profiles reads the stored runtimes as the
// omitted section for the placement pair check, by its typed fields: a
// stored runtimes row holding a key the closed runtimes schema does not
// know (written by another version, kept on load with a warning) does not
// refuse the save, is not rewritten, and keeps its raw content.
func TestPutSettingsProfilesOnlyKeepsStoredRuntimesUnknownKey(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	stored := `{"k8s":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"},"future_field":{"x":1}}}`
	fake.seedWithOrigin("runtimes", json.RawMessage(stored), "managed")
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)

	rr := putServerConfigDB(t, srv, ops, `{"profiles":{"fast":{"runtime":"k8s","runtime_class_name":"gvisor"}}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	requirePlacementRow(t, fake, "runtimes", 1, stored)
	requirePlacementRow(t, fake, "profiles", 1, `{"fast":{"runtime":"k8s","runtime_class_name":"gvisor"}}`)

	// The placement rules still apply to the stored runtimes: a profile
	// that changes their pool is refused, naming the profile.
	rr = putServerConfigDB(t, srv, ops, `{"profiles":{"batch":{"runtime":"k8s","node_selector":{"scion.dev/pool":"batch"}}}}`)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "profiles.batch.node_selector")
	requirePlacementRow(t, fake, "runtimes", 1, stored)
}

// resetPlacementSectionDB sends DELETE .../sections/{section}.
func resetPlacementSectionDB(srv *Server, section string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	srv.handleAdminServerConfigSectionReset(rr, adminRequest(http.MethodDelete, "/api/v1/admin/server-config/sections/"+section, ""))
	return rr
}

// Resetting runtimes or profiles validates the pair the reset leaves (the
// bootstrap value of the reset section and the stored other section): a
// reset that would leave a profile's placement without its runtime entry
// is refused with 422 naming the section and the path, and the row stays;
// a reset that leaves a valid pair removes the row.
func TestResetPlacementSectionValidatesPair(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	fake.seedWithOrigin("runtimes", json.RawMessage(`{"pool-rt":{"type":"kubernetes","namespace":"agents"}}`), "managed")
	fake.seedWithOrigin("profiles", json.RawMessage(`{"fast":{"runtime":"pool-rt","runtime_class_name":"gvisor"}}`), "managed")
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)

	rr := resetPlacementSectionDB(srv, "runtimes")
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Section string `json:"section"`
				Errors  []struct {
					Path string `json:"path"`
				} `json:"errors"`
			} `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), rr.Body.String())
	assert.Equal(t, ErrCodeValidationError, resp.Error.Code)
	assert.Equal(t, "runtimes", resp.Error.Details.Section)
	require.Len(t, resp.Error.Details.Errors, 1, rr.Body.String())
	assert.Equal(t, "profiles.fast.runtime", resp.Error.Details.Errors[0].Path)
	requirePlacementRow(t, fake, "runtimes", 1, `{"pool-rt":{"type":"kubernetes","namespace":"agents"}}`)

	// Without the profile's placement the same reset is valid.
	fake.seedWithOrigin("profiles", json.RawMessage(`{"fast":{"runtime":"pool-rt"}}`), "managed")
	rr = resetPlacementSectionDB(srv, "runtimes")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	requireNoPlacementRow(t, fake, "runtimes")
	requirePlacementRow(t, fake, "profiles", 1, `{"fast":{"runtime":"pool-rt"}}`)

	// A section with no row is still a 404.
	rr = resetPlacementSectionDB(srv, "runtimes")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

// A reset whose other section changes between the validation read and the
// guarded delete is a 409 listing that section as read, and deletes
// nothing.
func TestResetPlacementSectionRevisionConflict(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	fake.seedWithOrigin("runtimes", json.RawMessage(placementRuntimesDoc), "managed")
	fake.seedWithOrigin("profiles", json.RawMessage(`{"plain":{"runtime":"k8s"}}`), "managed")
	ops.store = &racingProfilesStore{fakeHubSettingStore: fake}
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)

	rr := resetPlacementSectionDB(srv, "runtimes")
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	var resp struct {
		Conflicted []map[string]interface{} `json:"conflicted"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Conflicted, 2, rr.Body.String())
	assert.Equal(t, "runtimes", resp.Conflicted[0]["section"])
	assert.Equal(t, map[string]interface{}{
		"section": "profiles", "expected_revision": float64(1), "current_revision": float64(2), "read": true,
	}, resp.Conflicted[1])
	requirePlacementRow(t, fake, "runtimes", 1, placementRuntimesDoc)
}

// A store without the guarded batch method fails a placement section reset
// closed, deleting nothing.
func TestResetPlacementSectionGuardedBatchUnsupported(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	fake.seedWithOrigin("profiles", json.RawMessage(`{"plain":{"runtime":"k8s"}}`), "managed")
	ops.store = unguardedHubSettingStore{HubSettingStore: fake}
	rr := resetPlacementSectionDB(srv, "profiles")
	require.Equal(t, http.StatusInternalServerError, rr.Code, rr.Body.String())
	requirePlacementRow(t, fake, "profiles", 1, `{"plain":{"runtime":"k8s"}}`)
}

// A runtimes save merges onto the stored row (ptone/scion#6580): a stored
// key that is not a runtime setting is kept with a save warning and never
// refuses the save under the closed runtimes schema; the same key sent by
// the client is refused, and nothing is written.
func TestPutSettingsRuntimesKeptUnknownKeyPasses(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	fake.seedWithOrigin("runtimes", json.RawMessage(`{"k8s":{"type":"kubernetes","namespace":"agents","future_field":{"x":1}}}`), "managed")
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)

	rr := putServerConfigDB(t, srv, ops, `{"runtimes":{"k8s":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"}}}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "runtimes.k8s.future_field")
	requirePlacementRow(t, fake, "runtimes", 2,
		`{"k8s":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"},"future_field":{"x":1}}}`)

	rr = putServerConfigDB(t, srv, ops, `{"runtimes":{"k8s":{"type":"kubernetes","namespace":"agents","bogus_key":1}}}`)
	require.GreaterOrEqual(t, rr.Code, 400, rr.Body.String())
	require.Less(t, rr.Code, 500, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "bogus_key")
	requirePlacementRow(t, fake, "runtimes", 2,
		`{"k8s":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"},"future_field":{"x":1}}}`)
}

// Placement is checked on the merged pair only for what the save changes:
// a stored entry with invalid placement that the save leaves as it is does
// not block a save of another entry or of an unrelated profile; changing
// that entry, or adding a profile on it, still counts.
func TestPutSettingsUntouchedStoredBadPlacementDoesNotBlock(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	bad := `"bad":{"type":"docker","runtime_class_name":"gvisor"}`
	fake.seedWithOrigin("runtimes", json.RawMessage(`{`+bad+`,"k8s":{"type":"kubernetes","namespace":"agents"}}`), "managed")
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)

	// The full map sent back with only k8s changed.
	rr := putServerConfigDB(t, srv, ops, `{"runtimes":{`+bad+`,"k8s":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"}}}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// A profiles-only save of an unrelated profile.
	rr = putServerConfigDB(t, srv, ops, `{"profiles":{"fast":{"runtime":"k8s","runtime_class_name":"gvisor"}}}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// A profile on the bad entry is refused, naming it.
	rr = putServerConfigDB(t, srv, ops, `{"profiles":{"fast":{"runtime":"k8s","runtime_class_name":"gvisor"},"onbad":{"runtime":"bad","runtime_class_name":"gvisor"}}}`)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "profiles.onbad")
	assert.NotContains(t, rr.Body.String(), `runtimes.bad"`)

	// Changing the bad entry itself is refused.
	rr = putServerConfigDB(t, srv, ops, `{"runtimes":{"bad":{"type":"docker","runtime_class_name":"kata"},"k8s":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"}}}}`)
	require.Equal(t, http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "runtimes.bad")
}

// racingRuntimesStore changes the runtimes row (another writer) just before
// the guarded write, after the save merged onto the row it read.
type racingRuntimesStore struct {
	*fakeHubSettingStore
}

func (s *racingRuntimesStore) UpsertHubSettingsGuarded(ctx context.Context, writes []store.HubSettingWrite, reads map[string]int64, updatedBy, origin string) ([]store.HubSetting, error) {
	if _, err := s.fakeHubSettingStore.UpsertHubSetting(ctx, "runtimes",
		json.RawMessage(`{"k8s":{"type":"kubernetes","namespace":"other"}}`), "other@example.com", -1, "managed"); err != nil {
		return nil, err
	}
	return s.fakeHubSettingStore.UpsertHubSettingsGuarded(ctx, writes, reads, updatedBy, origin)
}

// The runtimes write is the compare-and-swap on the row the merge read
// (ptone/scion#6580), in the same guarded transaction as profiles: a
// concurrent runtimes write makes the save a 409 that writes nothing.
func TestPutSettingsRuntimesMergeCASConflict(t *testing.T) {
	tempSettingsHome(t)
	srv, fake, ops := newTestDBServer(t)
	fake.seedWithOrigin("runtimes", json.RawMessage(`{"k8s":{"type":"kubernetes","namespace":"agents"}}`), "managed")
	ops.store = &racingRuntimesStore{fakeHubSettingStore: fake}
	_, err := ops.Refresh(context.Background())
	require.NoError(t, err)

	rr := putServerConfigDB(t, srv, ops, `{"runtimes":{"k8s":{"type":"kubernetes","namespace":"agents","node_selector":{"scion.dev/pool":"agents"}}},
		"profiles":{"fast":{"runtime":"k8s","runtime_class_name":"gvisor"}}}`)
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), `"runtimes"`)
	requirePlacementRow(t, fake, "runtimes", 2, `{"k8s":{"type":"kubernetes","namespace":"other"}}`)
	requireNoPlacementRow(t, fake, "profiles")
}

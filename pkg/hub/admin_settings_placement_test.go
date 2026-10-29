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
		{"invalid selector key", `{"runtimes":{"k":{"type":"kubernetes","node_selector":{"secret key!":"v"}}}}`, http.StatusUnprocessableEntity, "runtimes.k.node_selector"},
		{"invalid class", `{"profiles":{"p":{"runtime":"kubernetes","runtime_class_name":"Secret_Class"}}}`, http.StatusUnprocessableEntity, "profiles.p.runtime_class_name"},
		{"placement on docker", `{"runtimes":{"d":{"type":"docker","runtime_class_name":"gvisor"}}}`, http.StatusUnprocessableEntity, "runtimes.d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, fake, ops := newTestDBServer(t)
			rr := putServerConfigDB(t, srv, ops, tc.body)
			require.Equal(t, tc.code, rr.Code, rr.Body.String())
			assert.Contains(t, rr.Body.String(), tc.path)
			assert.NotContains(t, rr.Body.String(), "secret", "the response echoes a submitted value")
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
			assert.Contains(t, rr.Body.String(), `"runtimes"`)
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
func TestPutSettingsFlatPlacementNotWritable(t *testing.T) {
	tempSettingsHome(t)
	for _, body := range []string{
		`{"server":{"broker":{"instances":[{"key":"a","runtime_target":{"type":"kubernetes","node_selector":{"scion.dev/pool":"x"}}}]}}}`,
		`{"server.broker.instances":[{"key":"a"}]}`,
		`{"runtimes":{"k":{"type":"kubernetes","instances":[{"runtime_target":{"node_selector":{"scion.dev/pool":"x"}}}]}}}`,
		`{"profiles":{"p":{"runtime":"kubernetes","runtime_target":{"node_selector":{"scion.dev/pool":"x"}}}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			srv, fake, ops := newTestDBServer(t)
			rr := putServerConfigDB(t, srv, ops, body)
			require.True(t, rr.Code == http.StatusUnprocessableEntity || rr.Code == http.StatusBadRequest, "%d: %s", rr.Code, rr.Body.String())
			fake.mu.Lock()
			defer fake.mu.Unlock()
			assert.Empty(t, fake.settings, "a section was written")
			assert.False(t, strings.Contains(rr.Body.String(), `"applied"`) && rr.Code == http.StatusOK)
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

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

package runtimebroker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
)

// Additional broker-level coverage for ptone/scion#1808, closing gaps a
// review round found in the first pass: a file-scan hit bypassing the
// record-less-actor probe on delete (H1), a stop lookup error that still
// fell through to the idempotent 202 (H2), a false 409 for a same-project
// stop-then-delete race with no restart involved (M1), the probe's own
// error path never actually being reached by the original probe-error test,
// the broker-level idempotence refinement only being pinned against an
// empty atespace, and the delete-path project-blind guard being unpinned.

const (
	gapAtespaceB = "scion-bbbbbbbbbbbb"
	gapProjBID   = "bbbbbbbbbbbb"
)

// simulateBrokerRestart wipes the substrate runtime's in-memory agent
// records AND control tokens mid-test, leaving the fake ateapi's actors in
// place — what a broker process restart looks like to the runtime
// (ptone/scion#1808). newTestSubstrateBrokerServer already wipes this state
// once at setup for test isolation; this is for a test that needs the wipe
// to happen AFTER it has already driven an agent through Run.
func simulateBrokerRestart(t *testing.T) {
	t.Helper()
	t.Cleanup(runtime.WipeSubstrateAgentStateForTest())
}

// onlyScopedListFails fails only an atespace-scoped ListActors call — the
// shape RecordlessActors' probe makes — leaving an unscoped call (List's own
// cluster-wide listing) unaffected.
func onlyScopedListFails(err error) func(*ateapipb.ListActorsRequest) error {
	return func(in *ateapipb.ListActorsRequest) error {
		if in.GetAtespace() != "" {
			return err
		}
		return nil
	}
}

// onlyUnscopedListFails fails only an unscoped ListActors call — the shape
// SubstrateRuntime.List makes internally (and therefore LookupContainerID,
// through it) — leaving an atespace-scoped probe call unaffected.
func onlyUnscopedListFails(err error) func(*ateapipb.ListActorsRequest) error {
	return func(in *ateapipb.ListActorsRequest) error {
		if in.GetAtespace() == "" {
			return err
		}
		return nil
	}
}

// TestSubstrateBroker_RealRestart_PreRestartAgentIs409_NewAgentDeletes is an
// end-to-end restart: an agent started through this broker, then a restart
// (records and tokens wiped, actor still in ateapi). Delete and stop of that
// agent in its own project must be 409 identity-unknown and touch nothing; a
// NEW agent started after the restart must still delete normally.
func TestSubstrateBroker_RealRestart_PreRestartAgentIs409_NewAgentDeletes(t *testing.T) {
	srv, fc := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "projb", gapProjBID, testProjectScionDir(t, "projb"))
	fc.mu.Lock()
	_, created := fc.actors[gapAtespaceB+"/projb--dev"]
	fc.mu.Unlock()
	if !created {
		t.Fatal("setup: Run did not create projb--dev in the fake ateapi")
	}
	simulateBrokerRestart(t)

	w := httptest.NewRecorder()
	srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil), "dev", gapProjBID)
	if w.Code != http.StatusConflict || decodeBrokerAPIError(t, w) != ErrCodeSubstrateAgentIdentityUnknown {
		t.Fatalf("delete of pre-restart agent: status=%d body=%s, want 409 %s", w.Code, w.Body.String(), ErrCodeSubstrateAgentIdentityUnknown)
	}
	w = httptest.NewRecorder()
	srv.stopAgent(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/dev/stop", nil), "dev", gapProjBID)
	if w.Code != http.StatusConflict || decodeBrokerAPIError(t, w) != ErrCodeSubstrateAgentIdentityUnknown {
		t.Fatalf("stop of pre-restart agent: status=%d body=%s, want 409 %s", w.Code, w.Body.String(), ErrCodeSubstrateAgentIdentityUnknown)
	}
	fc.mu.Lock()
	if n := len(fc.deleteActorCalls); n != 0 {
		t.Errorf("DeleteActor called %d time(s) on a pre-restart agent, want 0", n)
	}
	fc.mu.Unlock()

	runSubstrateAgentForProject(t, srv.manager, "fresh", "projb", gapProjBID, testProjectScionDir(t, "projb"))
	w = httptest.NewRecorder()
	srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/fresh", nil), "fresh", gapProjBID)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete of post-restart agent: status=%d body=%s, want 204", w.Code, w.Body.String())
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if _, ok := fc.actors[gapAtespaceB+"/projb--fresh"]; ok {
		t.Error("post-restart agent still present after a successful delete")
	}
	if _, ok := fc.actors[gapAtespaceB+"/projb--dev"]; !ok {
		t.Error("pre-restart actor was removed by the delete of a different agent")
	}
}

// TestSubstrateBroker_AbsentSlug_OwnAtespaceAllRecorded_StaysIdempotent is
// the idempotence refinement at the broker level: an absent slug in a
// project whose OWN atespace holds only recorded actors must stay a 404
// (delete) and 202 (stop). The existing absent-slug tests use an empty
// atespace, so they cannot tell "count only record-less actors" apart from
// "count all actors".
func TestSubstrateBroker_AbsentSlug_OwnAtespaceAllRecorded_StaysIdempotent(t *testing.T) {
	srv, fc := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "projb", gapProjBID, testProjectScionDir(t, "projb"))

	w := httptest.NewRecorder()
	srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/gone", nil), "gone", gapProjBID)
	if w.Code != http.StatusNotFound {
		t.Errorf("delete absent slug: status=%d body=%s, want 404", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	srv.stopAgent(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/gone/stop", nil), "gone", gapProjBID)
	if w.Code != http.StatusAccepted {
		t.Errorf("stop absent slug: status=%d body=%s, want 202", w.Code, w.Body.String())
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.deleteActorCalls) != 0 {
		t.Errorf("DeleteActor called %v, want none", fc.deleteActorCalls)
	}
}

// TestSubstrateBroker_ProbeOnlyError_ExplicitFailure covers the probe itself
// failing (only the atespace-scoped ListActors call errors; the resolve-time
// unscoped List succeeds) — this must be an explicit 5xx for delete and
// stop, never 404/202. The original probe-error test fails every
// ListActors, so it returns at listErr (delete) or a clean not-found
// (stop) before the probe itself ever runs.
func TestSubstrateBroker_ProbeOnlyError_ExplicitFailure(t *testing.T) {
	srv, fc := newTestSubstrateBrokerServer(t)
	fc.mu.Lock()
	fc.listActorsErrFor = onlyScopedListFails(errors.New("simulated probe failure"))
	fc.mu.Unlock()

	w := httptest.NewRecorder()
	srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil), "dev", gapProjBID)
	if w.Code < 500 {
		t.Errorf("delete with failing probe: status=%d body=%s, want 5xx", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	srv.stopAgent(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/dev/stop", nil), "dev", gapProjBID)
	if w.Code < 500 {
		t.Errorf("stop with failing probe: status=%d body=%s, want 5xx", w.Code, w.Body.String())
	}
}

// TestSubstrateBroker_StopLookupErrorNoRecordless_ExplicitErrorNot202 covers
// the lead-accepted defect: the slug lookup errors (the unscoped List call
// LookupContainerID makes internally fails) while the probe would find zero
// record-less actors. The outcome is unknown, so it must be an explicit
// error, not the idempotent 202. The actor here is RECORDED — exactly the
// case the record-less-actor probe alone cannot catch.
func TestSubstrateBroker_StopLookupErrorNoRecordless_ExplicitErrorNot202(t *testing.T) {
	srv, fc := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "projb", gapProjBID, testProjectScionDir(t, "projb"))
	fc.mu.Lock()
	fc.listActorsErrFor = onlyUnscopedListFails(errors.New("simulated transient list failure"))
	fc.mu.Unlock()

	w := httptest.NewRecorder()
	srv.stopAgent(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/dev/stop", nil), "dev", gapProjBID)
	if w.Code == http.StatusAccepted || w.Code < 400 {
		t.Errorf("stop with failing lookup: status=%d body=%s, want an explicit error, not 202", w.Code, w.Body.String())
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if _, ok := fc.actors[gapAtespaceB+"/projb--dev"]; !ok {
		t.Error("actor removed although the lookup failed")
	}
}

// TestSubstrateBroker_DeleteFileScanHitWithRecordlessActor_NotSuccess covers
// the other lead-flagged High: a delete with a persistent ~/.scion. The
// agent's files survive the restart in the hub-managed project dir, so
// findAgentProjectDir hits and a file-only delete target used to be
// returned before the probe ever ran. The actor is still in ateapi
// (record-less), so reporting success would orphan it. Must be 409
// identity-unknown, with no files deleted and no DeleteActor call.
func TestSubstrateBroker_DeleteFileScanHitWithRecordlessActor_NotSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	srv, fc := newTestSubstrateBrokerServer(t)
	scionB, infoB := makeHubProject(t, home, "projb", gapProjBID, "dev")
	fc.putActor(gapAtespaceB, "projb--dev", "uid-projb-dev-prerestart")

	rec := doDelete(t, srv, "dev", "projectId="+gapProjBID+"&deleteFiles=true")
	if rec.Code/100 == 2 || rec.Code == http.StatusNotFound {
		t.Errorf("delete with file-scan hit and a record-less actor: status=%d body=%s, want 409 (never success while the actor exists)", rec.Code, rec.Body.String())
	} else if code := decodeBrokerAPIError(t, rec); code != ErrCodeSubstrateAgentIdentityUnknown {
		t.Errorf("error code = %q, want %q", code, ErrCodeSubstrateAgentIdentityUnknown)
	}
	assertUntouched(t, scionB, "dev", infoB)
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if _, ok := fc.actors[gapAtespaceB+"/projb--dev"]; !ok {
		t.Error("record-less actor removed")
	}
}

// TestSubstrateBroker_ProjectBlindDelete_NotProbed: a project-blind
// (projectID == "") delete is out of scope for the probe: an absent slug
// stays 404 even when the atespace an empty projectID maps to ("scion-x")
// holds a record-less actor. Pins the delete-path projectID != "" guard.
func TestSubstrateBroker_ProjectBlindDelete_NotProbed(t *testing.T) {
	srv, fc := newTestSubstrateBrokerServer(t)
	fc.putActor("scion-x", "ghost", "uid-ghost")

	w := httptest.NewRecorder()
	srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/nobody", nil), "nobody", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("project-blind delete of absent slug: status=%d body=%s, want 404", w.Code, w.Body.String())
	}
}

// TestNonProberRuntime_RealAgentManager_NotFoundUnchanged: a non-substrate
// runtime behind a REAL *agent.AgentManager (the existing non-prober tests
// use filteringMockManager, which the probe skips at the *agent.AgentManager
// type assertion and so never reaches the RecordlessActorProber assertion at
// all) — not-found delete stays 404 and not-found stop stays 202.
func TestNonProberRuntime_RealAgentManager_NotFoundUnchanged(t *testing.T) {
	rt := &runtime.MockRuntime{
		NameFunc: func() string { return "docker" },
		ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) { return nil, nil },
	}
	mgr := agent.NewManager(rt)
	t.Cleanup(mgr.Close)
	srv := New(DefaultServerConfig(), mgr, rt)

	w := httptest.NewRecorder()
	srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil), "dev", gapProjBID)
	if w.Code != http.StatusNotFound {
		t.Errorf("delete: status=%d body=%s, want 404", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	srv.stopAgent(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/dev/stop", nil), "dev", gapProjBID)
	if w.Code != http.StatusAccepted {
		t.Errorf("stop: status=%d body=%s, want 202", w.Code, w.Body.String())
	}
}

// TestSubstrateBroker_StopThenDeleteWhileActorStillListedDeleting_StaysIdempotent
// covers M1: normal operation, no restart at all. Stop (== Delete in Phase
// 1) drops this process's own record for the actor it just deleted, but
// Delete is fire-and-forget (substrate-runtime.md §9) — the actor can stay
// listed, in ACTOR_STATE_DELETING, for a while afterward. A later
// absent-slug delete of the same project must not mistake that lingering,
// record-less, DELETING entry for a post-restart identity-unknown case: it
// must stay the ordinary idempotent 404, not a false 409. Regression test
// for the tech lead's chosen "stateless" fix (RecordlessActors excludes
// ACTOR_STATE_DELETING), replacing the tombstone design the round-1 fix
// review proposed.
func TestSubstrateBroker_StopThenDeleteWhileActorStillListedDeleting_StaysIdempotent(t *testing.T) {
	srv, fc := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "projb", gapProjBID, testProjectScionDir(t, "projb"))
	fc.mu.Lock()
	uid := fc.actors[gapAtespaceB+"/projb--dev"].GetMetadata().GetUid()
	fc.mu.Unlock()

	w := httptest.NewRecorder()
	srv.stopAgent(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/dev/stop", nil), "dev", gapProjBID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("stop status = %d, want 202; body=%s", w.Code, w.Body.String())
	}

	// The fake's DeleteActor removed the actor outright; a real cluster
	// instead leaves it listed in DELETING for a while (the documented
	// fire-and-forget window). Simulate that: same UID, no record (this
	// process's own Delete already dropped it), state DELETING.
	fc.putActor(gapAtespaceB, "projb--dev", uid)
	fc.mu.Lock()
	fc.actors[gapAtespaceB+"/projb--dev"].Status.State = ateapipb.ActorState_ACTOR_STATE_DELETING
	fc.mu.Unlock()

	w = httptest.NewRecorder()
	srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil), "dev", gapProjBID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete of a slug whose only atespace entry is a DELETING, record-less actor: status=%d body=%s, want 404 (idempotent -- no restart happened)", w.Code, w.Body.String())
	}

	// Re-delete of the now-absent slug must stay 404 too (idempotent).
	w = httptest.NewRecorder()
	srv.deleteAgent(w, httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil), "dev", gapProjBID)
	if w.Code != http.StatusNotFound {
		t.Errorf("re-delete: status=%d body=%s, want 404", w.Code, w.Body.String())
	}
}

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
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/projectcompat"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
)

// putActor directly registers an actor as if some earlier CreateActor call
// had created it, without going through Run — used to simulate an actor
// this SubstrateRuntime instance never itself started (e.g. a broker
// restart), so no in-memory agent record exists for it.
func (f *fakeSubstrateControlClient) putActor(atespace, name, uid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actors[atespace+"/"+name] = &ateapipb.Actor{
		Metadata: &ateapipb.ResourceMetadata{Atespace: atespace, Name: name, Uid: uid},
		Status:   &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING},
	}
}

// newTestSubstrateBrokerServer builds a *Server whose default runtime is a
// real *SubstrateRuntime over a fake ateapi client, for the cross-project
// regression tests below. Returns the server and the fake client so a test
// can both inject actors directly and inspect which calls it received.
func newTestSubstrateBrokerServer(t *testing.T) (*Server, *fakeSubstrateControlClient) {
	t.Helper()
	fc := newFakeSubstrateControlClient(&substrateEgressRecorder{})
	actorServer := newFakeSubstrateActorServer()
	t.Cleanup(actorServer.Close)

	rt := runtime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient(actorServer.URL), nil, config.V1SubstrateConfig{})
	mgr := agent.NewManager(rt)
	t.Cleanup(mgr.Close)

	cfg := DefaultServerConfig()
	cfg.BrokerID = "test-broker-id"
	cfg.BrokerName = "test-host"
	cfg.ForceRuntime = ""
	return New(cfg, mgr, rt), fc
}

// testProjectScionDir creates a real, on-disk project directory named
// projectName (so config.GetProjectName's parent-basename convention
// recovers projectName from it) with a .scion subdirectory, and returns
// that .scion path — the exact shape config.GetResolvedProjectDir resolves
// a real project's ProjectPath to (pkg/agent/run.go's Start stores this
// same resolved path as the "scion.project_path" annotation). A fictional,
// non-existent path doesn't round-trip through
// config.GetResolvedProjectDir/GetProjectName the way a real project
// directory does, so tests that exercise AgentManager.Delete's
// deletionProjectName resolution need a real directory, not a string
// literal.
func testProjectScionDir(t *testing.T, projectName string) string {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, projectName)
	scionDir := filepath.Join(projectDir, ".scion")
	if err := os.MkdirAll(scionDir, 0755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", scionDir, err)
	}
	return scionDir
}

// runSubstrateAgentForProject drives an agent through rt.Run with the same
// labels and annotations pkg/agent/run.go's AgentManager.Start actually
// produces for a hub-dispatched (or CLI) start — see Start's Labels builder
// (scion.name, scion.agent, plus projectcompat.ProjectNameLabels/
// ProjectIDLabels merged in) and its Annotations
// (projectcompat.ProjectPathLabels(projectDir, true), set unconditionally
// once the project directory resolves, lines ~1207-1227) — plus the
// RunConfig.Project/ProjectID fields Start sets directly alongside the
// labels. actorName follows containerName(projectName, agentSlug) =
// "<project>--<agent>", also matching Start's convention.
func runSubstrateAgentForProject(t *testing.T, mgr agent.Manager, agentSlug, projectName, projectID, projectPath string) {
	t.Helper()
	am, ok := mgr.(*agent.AgentManager)
	if !ok {
		t.Fatalf("manager is a %T, want *agent.AgentManager", mgr)
	}
	actorName := projectName + "--" + agentSlug
	labels := map[string]string{
		"scion.name":  agentSlug,
		"scion.agent": "true",
	}
	for k, v := range projectcompat.ProjectNameLabels(projectName, true) {
		labels[k] = v
	}
	for k, v := range projectcompat.ProjectIDLabels(projectID, true) {
		labels[k] = v
	}
	cfg := runtime.RunConfig{
		Name:         actorName,
		Project:      projectName,
		ProjectID:    projectID,
		Image:        "us-docker.pkg.dev/proj/repo/scion-agent@sha256:" + strings.Repeat("a", 64),
		UnixUsername: "scion",
		NoAuth:       true,
		Labels:       labels,
		Annotations:  projectcompat.ProjectPathLabels(projectPath, true),
	}
	if _, err := am.Runtime.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run(%q) error = %v", actorName, err)
	}
}

// TestSubstrateBroker_RecordlessActorNotResolvableAcrossProjects is the
// broker-level counterpart to the runtime-level tests in
// pkg/runtime/substrate_runtime_test.go: it drives the real HTTP-facing
// handlers (stopAgent, deleteAgent) and the real LookupContainerID, against
// a real *Server backed by a real *SubstrateRuntime, to confirm a
// record-less actor in one project is never resolved — let alone acted
// on — by a same-slug request scoped to a DIFFERENT project.
//
// The scenario: after a broker restart, this runtime instance has no
// in-memory record for anything. A single actor exists,
// "scion-aaaaaaaaaaaa/projA--dev" (project A, agent slug "dev"). A request
// for agent "dev" scoped to project B must not find it, and must certainly
// never call DeleteActor on it.
func TestSubstrateBroker_RecordlessActorNotResolvableAcrossProjects(t *testing.T) {
	const (
		atespaceA = "scion-aaaaaaaaaaaa"
		actorA    = "projA--dev"
		projBID   = "bbbbbbbbbbbb"
	)

	t.Run("LookupContainerID scoped to project B returns nothing", func(t *testing.T) {
		srv, fc := newTestSubstrateBrokerServer(t)
		fc.putActor(atespaceA, actorA, "uid-a")

		// A "not found" error alongside an empty containerID is the
		// expected shape here (LookupContainerID returns an error when
		// nothing matches) — what matters is that it never resolves to
		// project A's actor, not that it's silently error-free.
		got, _ := srv.LookupContainerID(context.Background(), "dev", projBID)
		if got != "" {
			t.Errorf(`LookupContainerID("dev", projB) = %q, want "" — must never resolve to project A's actor`, got)
		}
	})

	t.Run("stopAgent scoped to project B makes no Stop/Delete call", func(t *testing.T) {
		srv, fc := newTestSubstrateBrokerServer(t)
		fc.putActor(atespaceA, actorA, "uid-a")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/dev/stop", nil)
		srv.stopAgent(w, req, "dev", projBID)

		fc.mu.Lock()
		_, stillThere := fc.actors[atespaceA+"/"+actorA]
		fc.mu.Unlock()
		if !stillThere {
			t.Error("projA's actor was removed by a project-B-scoped stop — it must be untouched")
		}
	})

	t.Run("deleteAgent scoped to project B makes no DeleteActor call", func(t *testing.T) {
		srv, fc := newTestSubstrateBrokerServer(t)
		fc.putActor(atespaceA, actorA, "uid-a")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
		srv.deleteAgent(w, req, "dev", projBID)

		fc.mu.Lock()
		_, stillThere := fc.actors[atespaceA+"/"+actorA]
		fc.mu.Unlock()
		if !stillThere {
			t.Error("projA's actor was removed by a project-B-scoped delete — it must be untouched")
		}
	})
}

// TestSubstrateBroker_RecordlessActorNoOpEvenInItsOwnProject documents the
// accepted trade-off: a record-less actor is not resolvable by slug even
// through a request scoped to its OWN project, since nothing here can
// verify a record-less actor's project identity strongly enough to trust a
// slug match at all — see pkg/runtime/substrate_runtime.go's List doc
// comment. A same-project stop/delete is therefore also a no-op, not an
// error: the actor is left running, and no DeleteActor call is made.
func TestSubstrateBroker_RecordlessActorNoOpEvenInItsOwnProject(t *testing.T) {
	const (
		atespaceA = "scion-aaaaaaaaaaaa"
		actorA    = "projA--dev"
		projAID   = "aaaaaaaaaaaa"
	)

	t.Run("LookupContainerID scoped to project A also returns nothing", func(t *testing.T) {
		srv, fc := newTestSubstrateBrokerServer(t)
		fc.putActor(atespaceA, actorA, "uid-a")

		got, _ := srv.LookupContainerID(context.Background(), "dev", projAID)
		if got != "" {
			t.Errorf(`LookupContainerID("dev", projA) = %q, want "" (documented no-op: a record-less actor is never resolvable by slug)`, got)
		}
	})

	t.Run("stopAgent scoped to project A is a no-op", func(t *testing.T) {
		srv, fc := newTestSubstrateBrokerServer(t)
		fc.putActor(atespaceA, actorA, "uid-a")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/dev/stop", nil)
		srv.stopAgent(w, req, "dev", projAID)

		fc.mu.Lock()
		_, stillThere := fc.actors[atespaceA+"/"+actorA]
		fc.mu.Unlock()
		if !stillThere {
			t.Error("projA's own actor was removed by a same-project stop — it must be untouched (documented no-op)")
		}
	})

	t.Run("deleteAgent scoped to project A is a no-op", func(t *testing.T) {
		srv, fc := newTestSubstrateBrokerServer(t)
		fc.putActor(atespaceA, actorA, "uid-a")

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
		srv.deleteAgent(w, req, "dev", projAID)

		fc.mu.Lock()
		_, stillThere := fc.actors[atespaceA+"/"+actorA]
		fc.mu.Unlock()
		if !stillThere {
			t.Error("projA's own actor was removed by a same-project delete — it must be untouched (documented no-op)")
		}
	})
}

// TestSubstrateBroker_SameSlugDifferentProjects_DeleteFailsClosed is the
// record-HAVING counterpart to
// TestSubstrateBroker_RecordlessActorNotResolvableAcrossProjects: two
// agents, both started for real through rt.Run with the realistic
// Project/ProjectID/ProjectPath labels and annotations a hub-dispatched
// start produces (runSubstrateAgentForProject), sharing the agent slug
// "dev" in two different projects.
//
// This is the scenario a wrong-actor delete was found in: without
// AgentInfo.ProjectPath, deleteAgent's own resolved projectPath stayed "",
// AgentManager.Delete's deletionProjectName stayed "", and its internal
// unscoped Runtime.List call (map[string]string{"scion.name": "dev"})
// returned both actors — Delete then removed whichever ListActors happened
// to return first, regardless of which project deleteAgent was scoped to.
//
// With ProjectPath populated correctly, AgentManager.Delete's internal List
// call is still unscoped by project — see manager.go — so
// SubstrateRuntime.List's ambiguity guard still can't tell the two apart at
// that specific call site and excludes both. Previously deleteAgent still
// called mgr.Delete anyway, which no-opped (zero DeleteActor calls) and then
// reported the same 204 as a genuine successful delete — a false success:
// the caller could not tell "deleted" from "silently refused" apart. Now
// deleteAgent detects the ambiguity itself, before ever calling mgr.Delete,
// and reports it explicitly as a 409 with a stable, machine-readable code
// (ErrCodeSubstrateAmbiguousSlug) — still fail-closed (zero DeleteActor
// calls, both actors survive), but no longer indistinguishable from success.
func TestSubstrateBroker_SameSlugDifferentProjects_DeleteFailsClosed(t *testing.T) {
	const (
		atespaceA = "scion-aaaaaaaaaaaa"
		actorA    = "proja--dev"
		projAID   = "aaaaaaaaaaaa"
		atespaceB = "scion-bbbbbbbbbbbb"
		actorB    = "projb--dev"
		projBID   = "bbbbbbbbbbbb"
	)

	for _, orderName := range []string{"projA first", "projB first"} {
		t.Run(orderName, func(t *testing.T) {
			srv, fc := newTestSubstrateBrokerServer(t)
			runSubstrateAgentForProject(t, srv.manager, "dev", "proja", projAID, testProjectScionDir(t, "proja"))
			runSubstrateAgentForProject(t, srv.manager, "dev", "projb", projBID, testProjectScionDir(t, "projb"))

			fc.mu.Lock()
			a, aok := fc.actors[atespaceA+"/"+actorA]
			b, bok := fc.actors[atespaceB+"/"+actorB]
			if !aok || !bok {
				fc.mu.Unlock()
				t.Fatalf("test setup: actorA present=%v, actorB present=%v, want both", aok, bok)
			}
			if orderName == "projA first" {
				fc.forceListOrder = []*ateapipb.Actor{a, b}
			} else {
				fc.forceListOrder = []*ateapipb.Actor{b, a}
			}
			fc.mu.Unlock()

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
			srv.deleteAgent(w, req, "dev", projBID)

			if w.Code != http.StatusConflict {
				t.Errorf(`deleteAgent("dev", projB) status = %d, want %d (409 Conflict, ambiguous slug across two projects)`, w.Code, http.StatusConflict)
			}
			var errResp ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
				t.Fatalf("response body is not valid JSON: %v; body=%s", err, w.Body.String())
			}
			if errResp.Error.Code != ErrCodeSubstrateAmbiguousSlug {
				t.Errorf("error code = %q, want %q; body=%s", errResp.Error.Code, ErrCodeSubstrateAmbiguousSlug, w.Body.String())
			}

			fc.mu.Lock()
			defer fc.mu.Unlock()
			if len(fc.deleteActorCalls) != 0 {
				t.Errorf(`deleteAgent("dev", projB) called DeleteActor %v, want zero (ambiguous slug across two projects must fail closed)`, fc.deleteActorCalls)
			}
			if _, ok := fc.actors[atespaceA+"/"+actorA]; !ok {
				t.Error("projA's actor was removed by a project-B-scoped delete — it must never be the wrong-actor target")
			}
			if _, ok := fc.actors[atespaceB+"/"+actorB]; !ok {
				t.Error("projB's actor was removed — the guard makes this a no-op, not a successful scoped delete")
			}
		})
	}
}

// TestSubstrateBroker_SameSlugDifferentProjects_LookupContainerIDFailsClosed
// is LookupContainerID's counterpart to the delete test above:
// LookupContainerID's own internal manager.List call
// (map[string]string{"scion.name": slug}) is likewise unscoped by project
// (server.go), with project filtering applied client-side afterward via
// agentsForProject — so the ambiguity guard excludes both same-slug
// record-having actors before that filtering ever runs. A lookup scoped to
// either project therefore returns "not found", not the wrong project's
// container ID and not the right one either.
func TestSubstrateBroker_SameSlugDifferentProjects_LookupContainerIDFailsClosed(t *testing.T) {
	const (
		projAID = "aaaaaaaaaaaa"
		projBID = "bbbbbbbbbbbb"
	)

	srv, _ := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "proja", projAID, testProjectScionDir(t, "proja"))
	runSubstrateAgentForProject(t, srv.manager, "dev", "projb", projBID, testProjectScionDir(t, "projb"))

	if got, err := srv.LookupContainerID(context.Background(), "dev", projBID); got != "" {
		t.Errorf(`LookupContainerID("dev", projB) = (%q, %v), want ("", err) — ambiguous slug across two projects must fail closed`, got, err)
	}
	if got, err := srv.LookupContainerID(context.Background(), "dev", projAID); got != "" {
		t.Errorf(`LookupContainerID("dev", projA) = (%q, %v), want ("", err) — ambiguous slug across two projects must fail closed`, got, err)
	}
}

// TestSubstrateBroker_UniqueSlugDeleteStillSucceeds is the ordinary-case
// control: exactly one record-having agent for a given slug (the common
// case) is unaffected by either the ProjectPath tracking or the ambiguity
// guard. ProjectPath is populated and the guard's slug tally never exceeds
// one, so deleteAgent resolves and removes it normally.
func TestSubstrateBroker_UniqueSlugDeleteStillSucceeds(t *testing.T) {
	const (
		atespaceA = "scion-aaaaaaaaaaaa"
		actorA    = "proja--dev"
		projAID   = "aaaaaaaaaaaa"
	)

	srv, fc := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "proja", projAID, testProjectScionDir(t, "proja"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
	srv.deleteAgent(w, req, "dev", projAID)

	if w.Code != http.StatusNoContent {
		t.Fatalf("deleteAgent status = %d, want %d; body=%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.deleteActorCalls) == 0 {
		t.Fatal(`deleteAgent("dev", projA) called DeleteActor 0 times, want at least 1 — the single-project happy path must still delete`)
	}
	if _, ok := fc.actors[atespaceA+"/"+actorA]; ok {
		t.Error("projA's actor is still present after delete")
	}
}

// TestSubstrateBroker_UnscopedDeleteNoMatchInProject_FailsClosed covers the
// gap the previous fix's own tests didn't reach: a project-B-scoped delete
// when project B has no record-having "dev" of its own — only project A
// does. deleteAgent's own matchesAgent loop correctly finds no entry for
// this project (matchesAgent checks project ID, so projA's entry never
// matches a projB request), so `projectPath` stays "". Before this fix,
// deleteAgent fell through to mgr.Delete(id, ..., "", ...) anyway;
// AgentManager.Delete's internal Runtime.List call is unscoped by project
// (manager.go), so with an empty deletionProjectName its own project check
// never engaged and it deleted whichever same-slug actor ListActors
// happened to return — project A's, the wrong one.
//
// Now, for substrate specifically, deleteAgent recognizes "no matching
// entry in the requested project" and returns 204 (an idempotent delete —
// the agent is genuinely absent from this project) without ever calling
// mgr.Delete, so AgentManager.Delete's unscoped List is never reached for
// this request at all.
func TestSubstrateBroker_UnscopedDeleteNoMatchInProject_FailsClosed(t *testing.T) {
	const (
		atespaceA = "scion-aaaaaaaaaaaa"
		actorA    = "proja--dev"
		projAID   = "aaaaaaaaaaaa"
		projBID   = "bbbbbbbbbbbb"
	)

	for _, orderName := range []string{"projA actor only, forward order", "projA actor only, reverse order"} {
		t.Run(orderName, func(t *testing.T) {
			srv, fc := newTestSubstrateBrokerServer(t)
			runSubstrateAgentForProject(t, srv.manager, "dev", "proja", projAID, testProjectScionDir(t, "proja"))

			fc.mu.Lock()
			a, ok := fc.actors[atespaceA+"/"+actorA]
			if !ok {
				fc.mu.Unlock()
				t.Fatalf("test setup: actorA not present")
			}
			// A single-element order is trivially "both orders", but set
			// it explicitly so this test doesn't depend on the fake's
			// default map-iteration behavior either.
			fc.forceListOrder = []*ateapipb.Actor{a}
			fc.mu.Unlock()

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
			srv.deleteAgent(w, req, "dev", projBID)

			if w.Code != http.StatusNoContent {
				t.Errorf(`deleteAgent("dev", projB) status = %d, want %d (idempotent: no matching entry in project B)`, w.Code, http.StatusNoContent)
			}

			fc.mu.Lock()
			defer fc.mu.Unlock()
			if len(fc.deleteActorCalls) != 0 {
				t.Errorf(`deleteAgent("dev", projB) called DeleteActor %v, want zero — project B has no "dev" of its own, project A's must never be the fallback target`, fc.deleteActorCalls)
			}
			if _, ok := fc.actors[atespaceA+"/"+actorA]; !ok {
				t.Error("projA's actor was removed by a project-B-scoped delete that had no matching entry in project B — it must never be the wrong-actor target")
			}
		})
	}
}

// TestSubstrateBroker_UnscopedDeleteNoMatchInProject_RecordlessOtherProject
// is the same gap, but with project B holding a record-less actor instead
// of nothing at all: matchesAgent never matches a record-less actor by
// slug at all (it reports its full, project-prefixed actor name as Name —
// see SubstrateRuntime.List's doc comment), so this must fail closed
// exactly the same way as the no-actor-at-all case above.
func TestSubstrateBroker_UnscopedDeleteNoMatchInProject_RecordlessOtherProject(t *testing.T) {
	const (
		atespaceA = "scion-aaaaaaaaaaaa"
		actorA    = "proja--dev"
		projAID   = "aaaaaaaaaaaa"
		atespaceB = "scion-bbbbbbbbbbbb"
		actorB    = "projb--dev"
		projBID   = "bbbbbbbbbbbb"
	)

	srv, fc := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "proja", projAID, testProjectScionDir(t, "proja"))
	// projB's actor is injected directly (never through this runtime's own
	// Run), so it genuinely has no in-memory record — simulating a broker
	// restart.
	fc.putActor(atespaceB, actorB, "uid-projb-recordless")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
	srv.deleteAgent(w, req, "dev", projBID)

	if w.Code != http.StatusNoContent {
		t.Errorf(`deleteAgent("dev", projB) status = %d, want %d (idempotent: no record-having match in project B)`, w.Code, http.StatusNoContent)
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.deleteActorCalls) != 0 {
		t.Errorf(`deleteAgent("dev", projB) called DeleteActor %v, want zero`, fc.deleteActorCalls)
	}
	if _, ok := fc.actors[atespaceA+"/"+actorA]; !ok {
		t.Error("projA's actor was removed — it must never be the wrong-actor target")
	}
	if _, ok := fc.actors[atespaceB+"/"+actorB]; !ok {
		t.Error("projB's record-less actor was removed — it must be untouched (documented no-op, unrelated to this fix)")
	}
}

// TestSubstrateBroker_ScopedDeleteMatchesOwnProject is the mirror of the
// no-match cases above: project B genuinely has its own record-having
// "dev" — matchesAgent finds it, deleteAgent's substrate-only gate does
// not fire (a match was found), and the delete proceeds and succeeds
// normally, scoped to project B by its resolved ProjectPath.
func TestSubstrateBroker_ScopedDeleteMatchesOwnProject(t *testing.T) {
	const (
		atespaceB = "scion-bbbbbbbbbbbb"
		actorB    = "projb--dev"
		projBID   = "bbbbbbbbbbbb"
	)

	srv, fc := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "projb", projBID, testProjectScionDir(t, "projb"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
	srv.deleteAgent(w, req, "dev", projBID)

	if w.Code != http.StatusNoContent {
		t.Fatalf("deleteAgent status = %d, want %d; body=%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.deleteActorCalls) == 0 {
		t.Fatal(`deleteAgent("dev", projB) called DeleteActor 0 times, want at least 1 — project B's own agent must still delete normally`)
	}
	if _, ok := fc.actors[atespaceB+"/"+actorB]; ok {
		t.Error("projB's actor is still present after delete")
	}
}

// TestDeleteAgent_NonSubstrateRuntimeUnchanged proves the new substrate-only
// gate in deleteAgent has zero effect on any other runtime: a mock runtime
// reports a single agent belonging to a different project than the one the
// delete request is scoped to (matchesAgent therefore doesn't match it,
// exactly like the substrate scenario above) — but since this runtime's
// Name() isn't "substrate", deleteAgent must fall through to mgr.Delete
// exactly as it always has, unchanged, even though that call's outcome
// (deleting a differently-project-scoped agent because the generic
// AgentManager.Delete path has no ambiguity guard of its own) is the same
// pre-existing, out-of-scope behavior this task does not touch for any
// runtime but substrate.
func TestDeleteAgent_NonSubstrateRuntimeUnchanged(t *testing.T) {
	deleteCalls := 0
	rt := &runtime.MockRuntime{
		NameFunc: func() string { return "mock" },
		ListFunc: func(ctx context.Context, filter map[string]string) ([]api.AgentInfo, error) {
			return []api.AgentInfo{
				{
					Name:        "dev",
					ContainerID: "mock-projA-dev",
					Project:     "proja",
					ProjectID:   "aaaaaaaaaaaa",
					ProjectPath: "/projects/proja",
					Labels:      map[string]string{"scion.name": "dev", "scion.agent": "true"},
				},
			}, nil
		},
		DeleteFunc: func(ctx context.Context, id string) error {
			deleteCalls++
			return nil
		},
	}
	mgr := agent.NewManager(rt)
	defer mgr.Close()

	cfg := DefaultServerConfig()
	cfg.BrokerID = "test-broker-id"
	cfg.BrokerName = "test-host"
	cfg.ForceRuntime = ""
	srv := New(cfg, mgr, rt)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
	// Scoped to a project the mock's sole entry does not belong to —
	// matchesAgent will not match it, exactly like the substrate
	// no-match scenario above.
	srv.deleteAgent(w, req, "dev", "bbbbbbbbbbbb")

	if w.Code != http.StatusNoContent {
		t.Errorf("deleteAgent status = %d, want %d (non-substrate: unchanged, unmatched still falls through to mgr.Delete and reports success)", w.Code, http.StatusNoContent)
	}
	if deleteCalls == 0 {
		t.Error("non-substrate runtime: Runtime.Delete was never called — deleteAgent's behavior for non-substrate runtimes must be byte-identical to before this fix")
	}
}

// TestSubstrateBroker_NoMatchGate_FiresForNamedSubstrateProfile proves the
// substrate-only gate isn't accidentally scoped to this file's own
// test-only construction (runtime.NewSubstrateRuntimeForTest, always
// literally "substrate" by definition). It builds the runtime the same way
// GetRuntime's "substrate" case does for a real, on-disk settings.json —
// config.LoadEffectiveSettings, then VersionedSettings.ResolveRuntime, then
// runtime.NewSubstrateRuntime (pkg/runtime/factory.go) — for two
// differently-named runtime entries ("substrate-prod", "substrate-nip",
// matching TestResolveManagerForOpts_SubstrateProfilesGetTheirOwnConfig's
// settings shape in substrate_manager_test.go), and confirms the resulting
// runtime's Name() is "substrate" for both.
//
// Why this matters: SubstrateRuntime.Name() is a hardcoded literal
// ("substrate") that never consults its config, so no settings-level name
// can structurally change what it reports — but that guarantee is worth
// pinning down with a real test through the real resolution path rather
// than asserted from reading the one-line method alone, since it is
// exactly what the deleteAgent gate depends on to engage at all for a
// non-default-named substrate profile.
func TestSubstrateBroker_NoMatchGate_FiresForNamedSubstrateProfile(t *testing.T) {
	for _, profileName := range []string{"substrate-prod", "substrate-nip"} {
		t.Run(profileName, func(t *testing.T) {
			projectDir := t.TempDir()
			scionDir := filepath.Join(projectDir, ".scion")
			if err := os.MkdirAll(scionDir, 0755); err != nil {
				t.Fatal(err)
			}
			// Distinguishes each subtest's config from the other's, so
			// SubstrateRuntime's process-wide memoization (keyed on an
			// encoding of the config) builds a fresh instance per subtest
			// instead of reusing one built under the other's fake client.
			settings := `{
				"schema_version": "1",
				"active_profile": "` + profileName + `",
				"runtimes": {
					"` + profileName + `": {
						"type": "substrate",
						"substrate": {
							"api_endpoint": "api.ate-system.svc:443",
							"router_endpoint": "http://atenet-router.ate-system.svc:80",
							"snapshot_storage": "gs://bucket/` + profileName + `/"
						}
					}
				},
				"profiles": {
					"` + profileName + `": {"runtime": "` + profileName + `"}
				}
			}`
			if err := os.WriteFile(filepath.Join(scionDir, "settings.json"), []byte(settings), 0644); err != nil {
				t.Fatal(err)
			}

			resolvedDir, err := config.GetResolvedProjectDir(projectDir)
			if err != nil {
				t.Fatalf("GetResolvedProjectDir error = %v", err)
			}
			vs, _, err := config.LoadEffectiveSettings(resolvedDir)
			if err != nil {
				t.Fatalf("LoadEffectiveSettings error = %v", err)
			}
			rtConfig, runtimeType, err := vs.ResolveRuntime(profileName)
			if err != nil {
				t.Fatalf("ResolveRuntime(%q) error = %v", profileName, err)
			}
			if runtimeType != "substrate" {
				t.Fatalf(`ResolveRuntime(%q) type = %q, want "substrate" regardless of the settings runtime's own name`, profileName, runtimeType)
			}

			fc := newFakeSubstrateControlClient(&substrateEgressRecorder{})
			actorServer := newFakeSubstrateActorServer()
			t.Cleanup(actorServer.Close)
			restore := runtime.SetSubstrateRuntimeBuilderForTest(func(cfg config.V1SubstrateConfig) (*runtime.SubstrateRuntime, error) {
				return runtime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient(actorServer.URL), nil, cfg), nil
			})
			t.Cleanup(restore)

			// The exact call GetRuntime's "substrate" case makes.
			rt, err := runtime.NewSubstrateRuntime(rtConfig.Substrate)
			if err != nil {
				t.Fatalf("NewSubstrateRuntime error = %v", err)
			}
			if rt.Name() != "substrate" {
				t.Fatalf(`rt.Name() = %q, want "substrate" — a differently-named settings profile (%q) must not change what the runtime reports itself as, or deleteAgent's substrate-only gate would silently never engage for it`, rt.Name(), profileName)
			}

			mgr := agent.NewManager(rt)
			t.Cleanup(mgr.Close)
			cfg := DefaultServerConfig()
			cfg.BrokerID = "test-broker-id"
			cfg.BrokerName = "test-host"
			cfg.ForceRuntime = ""
			srv := New(cfg, mgr, rt)

			const (
				atespaceA = "scion-aaaaaaaaaaaa"
				actorA    = "proja--dev"
				projAID   = "aaaaaaaaaaaa"
				projBID   = "bbbbbbbbbbbb"
			)
			runSubstrateAgentForProject(t, srv.manager, "dev", "proja", projAID, testProjectScionDir(t, "proja"))

			// Confirm resolveAgentRuntimeTarget itself — not just the
			// directly-constructed runtime above — reports "substrate" for
			// this named-profile-resolved instance, since that's the value
			// deleteAgent's gate actually checks.
			_, resolvedRuntime := srv.resolveAgentRuntimeTarget(context.Background(), "dev", projBID)
			if resolvedRuntime.Name() != "substrate" {
				t.Fatalf(`resolveAgentRuntimeTarget(...) runtime.Name() = %q, want "substrate"`, resolvedRuntime.Name())
			}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
			srv.deleteAgent(w, req, "dev", projBID)

			if w.Code != http.StatusNoContent {
				t.Errorf(`deleteAgent("dev", projB) status = %d, want %d — the substrate-only gate must fire for a named profile (%q) too, not just this file's directly-constructed test runtimes`, w.Code, http.StatusNoContent, profileName)
			}
			fc.mu.Lock()
			defer fc.mu.Unlock()
			if len(fc.deleteActorCalls) != 0 {
				t.Errorf("DeleteActor called %d times, want 0", len(fc.deleteActorCalls))
			}
			if _, ok := fc.actors[atespaceA+"/"+actorA]; !ok {
				t.Error("projA's actor was removed — wrong-actor delete under a named substrate profile")
			}
		})
	}
}

// TestSubstrateBroker_MatchedDelete_NeverFallsBackToHubManagedProjectScan
// covers a question raised alongside the no-match gate above: deleteAgent
// also has a second, older fallback — findAgentInHubManagedProjects — that
// activates when "if projectPath == "" && deleteFiles" (handlers.go). That
// fallback takes only the bare agent name, no project identifier, and
// returns the FIRST hub-managed project directory (findAgentInHubManagedProjects
// walks every global project directory, current and legacy-named) that
// happens to contain an "agents/<name>" entry — exactly the kind of
// project-blind, order-dependent resolution the ambiguity guard above
// exists to avoid.
//
// For a MATCHED substrate entry, though, this fallback is unreachable: the
// matching loop already set projectPath from the matched entry's own
// AgentInfo.ProjectPath, which is non-empty for any agent that actually
// went through AgentManager.Start (config.GetResolvedProjectDir either
// resolves to a real, non-empty path and Start proceeds to set the
// "scion.project_path" annotation unconditionally, or it errors and Start
// returns before any agent record is ever created — see run.go — so a
// matched, record-having entry can only exist with a populated
// ProjectPath already on it). The "projectPath == """ guard on the
// fallback therefore never opens for a matched entry, and the project-
// blind scan never runs — this test proves that by planting a decoy
// "other project" whose own agents/dev directory the scan WOULD return if
// it were ever invoked, and confirming a deleteFiles=true delete of the
// real, matched dev agent in project B never touches it.
//
// If this ever regresses (e.g. a future change stops setting ProjectPath
// unconditionally), the fallback's project-blind resolution would apply to
// substrate too and could point file deletion at a same-named agent
// directory in a different project — the file-system analogue of the
// wrong-actor delete this branch's other tests target. Per policy this is
// closed for substrate specifically if ever found reachable; a non-
// substrate instance of the same fallback is pkg/agent's generic
// slug-matching hardening territory, not this branch's.
func TestSubstrateBroker_MatchedDelete_NeverFallsBackToHubManagedProjectScan(t *testing.T) {
	// Point config.GetGlobalDir() (via os.UserHomeDir()) at a temp HOME so
	// the decoy hub-managed project below is the only thing
	// findAgentInHubManagedProjects could possibly find.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	decoyAgentDir := filepath.Join(tmpHome, ".scion", "projects", "decoy-project", ".scion", "agents", "dev")
	if err := os.MkdirAll(decoyAgentDir, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(decoyAgentDir, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("do not delete"), 0644); err != nil {
		t.Fatal(err)
	}

	// Confirm the decoy is actually findable by the project-blind scan —
	// otherwise this test would pass for the wrong reason (the scan simply
	// finding nothing, not the matched-entry guard preventing the call).
	if got := findAgentInHubManagedProjects("dev"); got == "" {
		t.Fatalf("test setup: findAgentInHubManagedProjects(\"dev\") = %q, want the decoy project's .scion dir to be found", got)
	}

	const projBID = "bbbbbbbbbbbb"
	srv, _ := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "projb", projBID, testProjectScionDir(t, "projb"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev?deleteFiles=true", nil)
	srv.deleteAgent(w, req, "dev", projBID)

	if w.Code != http.StatusNoContent {
		t.Fatalf("deleteAgent status = %d, want %d; body=%s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("decoy project's agent dir was touched (sentinel gone or dir removed: %v) — a matched substrate delete must never fall back to the project-blind hub-managed-project scan", err)
	}
}

// TestSubstrateBroker_NoMatchDelete_NeverMarksWrongProjectAgentInfo covers
// the same project-blind fallback for the NO-MATCH case: the substrate-only
// gate is placed before both findAgentInHubManagedProjects and the
// soft-delete agent-info.json marking, specifically so that neither one
// ever runs off of a project-blind guess. Without that ordering, a
// project-B-scoped delete for a "dev" that doesn't exist in project B,
// with deleteFiles=true and softDelete=true, would resolve projectPath to
// whichever OTHER project's agents/dev directory the scan finds first and
// mark THAT project's agent-info.json as deleted — a real cross-project
// data-integrity bug even though no file content is deleted (only a status
// field is overwritten).
func TestSubstrateBroker_NoMatchDelete_NeverMarksWrongProjectAgentInfo(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	decoyAgentHome := filepath.Join(tmpHome, ".scion", "projects", "decoy-project", ".scion", "agents", "dev", "home")
	if err := os.MkdirAll(decoyAgentHome, 0755); err != nil {
		t.Fatal(err)
	}
	agentInfoPath := filepath.Join(decoyAgentHome, "agent-info.json")
	const originalContent = `{"phase":"running"}`
	if err := os.WriteFile(agentInfoPath, []byte(originalContent), 0644); err != nil {
		t.Fatal(err)
	}

	if got := findAgentInHubManagedProjects("dev"); got == "" {
		t.Fatalf("test setup: findAgentInHubManagedProjects(\"dev\") = %q, want the decoy project's .scion dir to be found", got)
	}

	const (
		atespaceA = "scion-aaaaaaaaaaaa"
		actorA    = "proja--dev"
		projAID   = "aaaaaaaaaaaa"
		projBID   = "bbbbbbbbbbbb"
	)
	srv, fc := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "proja", projAID, testProjectScionDir(t, "proja"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev?deleteFiles=true&softDelete=true", nil)
	srv.deleteAgent(w, req, "dev", projBID)

	if w.Code != http.StatusNoContent {
		t.Errorf(`deleteAgent("dev", projB, deleteFiles=true, softDelete=true) status = %d, want %d`, w.Code, http.StatusNoContent)
	}
	got, err := os.ReadFile(agentInfoPath)
	if err != nil {
		t.Fatalf("decoy agent-info.json vanished: %v", err)
	}
	if string(got) != originalContent {
		t.Errorf("decoy project's agent-info.json = %s, want unchanged %s — a no-match substrate delete must never mark a project-blind guess's agent-info.json as deleted", got, originalContent)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if _, ok := fc.actors[atespaceA+"/"+actorA]; !ok {
		t.Error("projA's actor was removed — it must be untouched")
	}
}

// captureAgentLifecycleLogs redirects the default slog logger into a
// buffer for the duration of the test. s.agentLifecycleLog
// (pkg/runtimebroker/server.go) is built from slog.Default() once, at
// New() time (logging.Subsystem just wraps whatever the current default
// is), so this must run before the *Server under test is constructed to
// see its output.
func captureAgentLifecycleLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestSubstrateBroker_NoMatchDelete_LogsAtInfo confirms the no-match gate's
// silent no-op is still visible in broker logs: an info-level line naming
// the agent slug and project ID, no secrets, so an operator can tell a
// substrate delete resolved to "already gone" rather than the request
// simply vanishing.
func TestSubstrateBroker_NoMatchDelete_LogsAtInfo(t *testing.T) {
	buf := captureAgentLifecycleLogs(t)

	const (
		projAID = "aaaaaaaaaaaa"
		projBID = "bbbbbbbbbbbb"
	)
	srv, _ := newTestSubstrateBrokerServer(t)
	runSubstrateAgentForProject(t, srv.manager, "dev", "proja", projAID, testProjectScionDir(t, "proja"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/dev", nil)
	srv.deleteAgent(w, req, "dev", projBID)

	if w.Code != http.StatusNoContent {
		t.Fatalf("deleteAgent status = %d, want %d", w.Code, http.StatusNoContent)
	}

	found := false
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		if rec["level"] == "INFO" && rec["agent_id"] == "dev" && rec["project_id"] == projBID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no info-level log line with agent_id=%q project_id=%q found; log output:\n%s", "dev", projBID, buf.String())
	}
}

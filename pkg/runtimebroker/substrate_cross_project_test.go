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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
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
// This is the scenario a wrong-actor delete was found in: before this
// round's fix, AgentInfo.ProjectPath was never set, so deleteAgent's own
// resolved projectPath stayed "", AgentManager.Delete's deletionProjectName
// stayed "", and its internal unscoped Runtime.List call
// (map[string]string{"scion.name": "dev"}) returned both actors — Delete
// then removed whichever ListActors happened to return first, regardless
// of which project deleteAgent was scoped to.
//
// After the fix, ProjectPath is populated correctly, but
// AgentManager.Delete's internal List call is still unscoped by project —
// see manager.go — so SubstrateRuntime.List's ambiguity guard (this
// round's second, unconditional half) still can't tell the two apart at
// that specific call site and excludes both: deleteAgent scoped to either
// project becomes a no-op (zero DeleteActor calls, both actors left
// running), not a wrong-actor delete. This is the accepted, reported
// trade-off — see the project log for the full list of broker operations
// this affects.
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

// TestSubstrateBroker_UniqueSlugDeleteStillSucceeds is the D1 happy-path
// control: exactly one record-having agent for a given slug (the common
// case) is unaffected by either half of this round's fix. ProjectPath is
// populated and the ambiguity guard's slug tally never exceeds one, so
// deleteAgent resolves and removes it normally.
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

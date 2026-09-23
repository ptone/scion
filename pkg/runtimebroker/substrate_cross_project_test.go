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
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
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

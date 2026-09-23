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

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	scionruntime "github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
)

// fakeSubstrateControlClient is a minimal, package-local
// ateapipb.ControlClient fake covering exactly the calls SubstrateRuntime's
// Run/List/Delete make. It embeds the interface so any call this test never
// exercises panics loudly (nil pointer dereference) rather than silently
// doing nothing, instead of duplicating pkg/runtime's own more complete
// fake — that one lives in a _test.go file, so it isn't importable from
// here, and this test only needs a small slice of the interface.
type fakeSubstrateControlClient struct {
	ateapipb.ControlClient

	mu                sync.Mutex
	actors            map[string]*ateapipb.Actor // key: "<atespace>/<name>"
	deleteActorCalls  []*ateapipb.DeleteActorRequest
	deleteEgressCalls []*ateapipb.DeleteActorEgressPolicyRequest
}

func newFakeSubstrateControlClient() *fakeSubstrateControlClient {
	return &fakeSubstrateControlClient{actors: make(map[string]*ateapipb.Actor)}
}

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

func (f *fakeSubstrateControlClient) CreateAtespace(ctx context.Context, in *ateapipb.CreateAtespaceRequest, opts ...grpc.CallOption) (*ateapipb.Atespace, error) {
	return &ateapipb.Atespace{}, nil
}

func (f *fakeSubstrateControlClient) GetActorTemplate(ctx context.Context, in *ateapipb.GetActorTemplateRequest, opts ...grpc.CallOption) (*ateapipb.ActorTemplate, error) {
	return nil, status.Error(codes.NotFound, "not found")
}

func (f *fakeSubstrateControlClient) CreateActorTemplate(ctx context.Context, in *ateapipb.CreateActorTemplateRequest, opts ...grpc.CallOption) (*ateapipb.ActorTemplate, error) {
	tmpl := in.GetActorTemplate()
	tmpl.Status = &ateapipb.ActorTemplateStatus{
		GoldenSnapshotStatus: &ateapipb.GoldenSnapshotStatus{
			GoldenTag: &ateapipb.ObjectRef{Atespace: "ate-golden", Name: "golden"},
		},
	}
	return tmpl, nil
}

func (f *fakeSubstrateControlClient) CreateActor(ctx context.Context, in *ateapipb.CreateActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	actor := &ateapipb.Actor{
		Metadata: &ateapipb.ResourceMetadata{
			Atespace: in.GetActor().GetMetadata().GetAtespace(),
			Name:     in.GetActor().GetMetadata().GetName(),
			Uid:      "uid-" + in.GetActor().GetMetadata().GetName(),
		},
		Status: &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING},
	}
	f.actors[actor.GetMetadata().GetAtespace()+"/"+actor.GetMetadata().GetName()] = actor
	return actor, nil
}

func (f *fakeSubstrateControlClient) CreateActorEgressPolicy(ctx context.Context, in *ateapipb.CreateActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error) {
	return &ateapipb.EgressPolicy{}, nil
}

func (f *fakeSubstrateControlClient) ResumeActor(ctx context.Context, in *ateapipb.ResumeActorRequest, opts ...grpc.CallOption) (*ateapipb.ResumeActorResponse, error) {
	return &ateapipb.ResumeActorResponse{}, nil
}

func (f *fakeSubstrateControlClient) GetActor(ctx context.Context, in *ateapipb.GetActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.actors[in.GetActor().GetAtespace()+"/"+in.GetActor().GetName()]; ok {
		return a, nil
	}
	return nil, status.Error(codes.NotFound, "not found")
}

func (f *fakeSubstrateControlClient) ListActors(ctx context.Context, in *ateapipb.ListActorsRequest, opts ...grpc.CallOption) (*ateapipb.ListActorsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var actors []*ateapipb.Actor
	for _, a := range f.actors {
		actors = append(actors, a)
	}
	return &ateapipb.ListActorsResponse{Actors: actors}, nil
}

func (f *fakeSubstrateControlClient) DeleteActor(ctx context.Context, in *ateapipb.DeleteActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteActorCalls = append(f.deleteActorCalls, in)
	delete(f.actors, in.GetActor().GetAtespace()+"/"+in.GetActor().GetName())
	return &ateapipb.Actor{}, nil
}

func (f *fakeSubstrateControlClient) DeleteActorEgressPolicy(ctx context.Context, in *ateapipb.DeleteActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteEgressCalls = append(f.deleteEgressCalls, in)
	return &ateapipb.EgressPolicy{}, nil
}

// newFakeSubstrateActorServer stands in for sciontool substrate-serve,
// reached through a substrate.RouterClient exactly like the real router —
// just enough of the bootstrap handshake (healthz + bootstrap) for Run to
// complete, for the test case that needs a real in-memory agent record
// (which only Run itself creates).
func newFakeSubstrateActorServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/scion/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "awaiting-bootstrap"})
	})
	mux.HandleFunc("/scion/v1/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

// TestSubstrateAgentManagerDelete_RecordExists is D1's regression test for
// the live-cluster defect where `scion delete` never removed the Substrate
// actor: AgentManager.Delete (pkg/agent/manager.go) resolves the caller's
// agent slug to a container via Runtime.List, matching on AgentInfo.Name.
// Before the fix, SubstrateRuntime.List reported the actor name
// (containerName(project, agent), project-prefixed) as Name instead of the
// bare agent slug, so the match never succeeded and Delete silently
// returned success without calling Runtime.Stop/Runtime.Delete at all —
// ptone/scion#1819 tracks hardening AgentManager.Delete's silent-no-op
// shape in general; this drives the real path (a real *SubstrateRuntime and
// a real agent.Manager, not a reimplementation of either) to confirm the
// substrate-specific root cause is fixed.
//
// This case starts the agent for real (through Run), so an in-memory
// agent record exists — the common case, and the one that was silently
// broken even though the record had the correct "scion.name" label the
// whole time (see substrate_runtime.go's List for why: the bug was in what
// Name was set to, not in the label).
func TestSubstrateAgentManagerDelete_RecordExists(t *testing.T) {
	fc := newFakeSubstrateControlClient()
	actorServer := newFakeSubstrateActorServer()
	defer actorServer.Close()

	rt := scionruntime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient(actorServer.URL), nil, config.V1SubstrateConfig{
		SnapshotStorage:   "gs://bucket/prefix/",
		SandboxConfigName: "gvisor-default",
	})

	const agentSlug = "sb-smoke-2"
	cfg := scionruntime.RunConfig{
		Name:         "myproj--" + agentSlug, // containerName("myproj", agentSlug)
		ProjectID:    "550e8400-e29b-41d4-a716-446655440002",
		Image:        "us-docker.pkg.dev/proj/repo/scion-agent@sha256:" + strings.Repeat("a", 64),
		UnixUsername: "scion",
		NoAuth:       true, // skip needing a real api.Harness for buildSubstrateStartCmd
		Labels:       map[string]string{"scion.name": agentSlug, "scion.agent": "true"},
	}
	if _, err := rt.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mgr := NewManager(rt)
	defer mgr.Close()

	if _, err := mgr.Delete(context.Background(), agentSlug, false, "", false); err != nil {
		t.Fatalf("Delete(%q) error = %v", agentSlug, err)
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.deleteActorCalls) == 0 {
		t.Fatal("DeleteActor was never called — Delete() silently no-opped instead of finding the actor")
	}
	got := fc.deleteActorCalls[len(fc.deleteActorCalls)-1].GetActor()
	if got.GetName() != cfg.Name {
		t.Errorf("DeleteActor actor name = %q, want %q", got.GetName(), cfg.Name)
	}
	if !strings.HasPrefix(got.GetAtespace(), "scion-") {
		t.Errorf("DeleteActor atespace = %q, want a \"scion-\"-prefixed atespace", got.GetAtespace())
	}
	if len(fc.deleteEgressCalls) == 0 {
		t.Fatal("DeleteActorEgressPolicy was never called — Delete() silently no-opped instead of finding the actor")
	}
	gotEgress := fc.deleteEgressCalls[len(fc.deleteEgressCalls)-1].GetActor()
	if gotEgress.GetName() != cfg.Name || gotEgress.GetAtespace() != got.GetAtespace() {
		t.Errorf("DeleteActorEgressPolicy actor = %s/%s, want %s/%s", gotEgress.GetAtespace(), gotEgress.GetName(), got.GetAtespace(), cfg.Name)
	}
}

// TestSubstrateAgentManagerDelete_NoRecord is D1's second required case: an
// actor with no in-memory agent record at all, simulating a broker restart
// (phase1-spec.md §2.2's List row explicitly accepts losing records across
// a restart, but not losing the actor from List, or from Delete, entirely).
// The actor here is injected directly into the fake client, never through
// this SubstrateRuntime's own Run, so — unlike the record-exists case —
// there is genuinely no substrateAgentRecords entry for it; List must
// derive the "scion.name" label used for the match from the actor name
// itself.
func TestSubstrateAgentManagerDelete_NoRecord(t *testing.T) {
	fc := newFakeSubstrateControlClient()
	const (
		atespace  = "scion-someproj"
		agentSlug = "sb-smoke-3"
		actorName = "myproj--" + agentSlug
	)
	fc.putActor(atespace, actorName, "uid-orphan")

	rt := scionruntime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient("http://unused"), nil, config.V1SubstrateConfig{})

	mgr := NewManager(rt)
	defer mgr.Close()

	if _, err := mgr.Delete(context.Background(), agentSlug, false, "", false); err != nil {
		t.Fatalf("Delete(%q) error = %v", agentSlug, err)
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.deleteActorCalls) == 0 {
		t.Fatal("DeleteActor was never called — Delete() silently no-opped instead of finding the record-less actor")
	}
	got := fc.deleteActorCalls[len(fc.deleteActorCalls)-1].GetActor()
	if got.GetAtespace() != atespace || got.GetName() != actorName {
		t.Errorf("DeleteActor actor = %s/%s, want %s/%s", got.GetAtespace(), got.GetName(), atespace, actorName)
	}
	if len(fc.deleteEgressCalls) == 0 {
		t.Fatal("DeleteActorEgressPolicy was never called — Delete() silently no-opped instead of finding the record-less actor")
	}
}

// TestSubstrateAgentManagerDelete_RecordlessAmbiguousSlugDeletesNothing is
// the invariant a record-less List entry must never violate: a slug lookup
// must never resolve to an actor whose ownership can't be verified. Two
// different projects' actors, both record-less, both named "<project>--dev"
// — an unscoped Delete("dev") must find neither, not pick one arbitrarily.
// Before this fix, whichever actor ListActors happened to return second
// would end up as List's sole "scion.name"="dev" entry (map iteration order
// is undefined), so Delete deleted THAT ONE — a different project's actor —
// instead of doing nothing. Both actor orderings are exercised as subtests;
// running the whole test under a high -count is an additional check that
// the outcome truly doesn't depend on map iteration order.
func TestSubstrateAgentManagerDelete_RecordlessAmbiguousSlugDeletesNothing(t *testing.T) {
	const (
		atespaceA = "scion-aaaaaaaaaaaa"
		actorA    = "projA--dev"
		atespaceB = "scion-bbbbbbbbbbbb"
		actorB    = "projB--dev"
	)

	for _, order := range []string{"A then B", "B then A"} {
		t.Run(order, func(t *testing.T) {
			fc := newFakeSubstrateControlClient()
			if order == "A then B" {
				fc.putActor(atespaceA, actorA, "uid-a")
				fc.putActor(atespaceB, actorB, "uid-b")
			} else {
				fc.putActor(atespaceB, actorB, "uid-b")
				fc.putActor(atespaceA, actorA, "uid-a")
			}

			rt := scionruntime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient("http://unused"), nil, config.V1SubstrateConfig{})
			mgr := NewManager(rt)
			defer mgr.Close()

			if _, err := mgr.Delete(context.Background(), "dev", false, "", false); err != nil {
				t.Fatalf(`Delete("dev") error = %v`, err)
			}

			fc.mu.Lock()
			defer fc.mu.Unlock()
			if len(fc.deleteActorCalls) != 0 {
				t.Fatalf(`Delete("dev") called DeleteActor %v, want no calls at all (ambiguous slug, neither actor's ownership is verified)`, fc.deleteActorCalls)
			}
			if len(fc.deleteEgressCalls) != 0 {
				t.Fatalf(`Delete("dev") called DeleteActorEgressPolicy %v, want no calls at all`, fc.deleteEgressCalls)
			}
			// Both actors must still be present — genuinely untouched, not
			// merely "not the target of a recorded call".
			if _, ok := fc.actors[atespaceA+"/"+actorA]; !ok {
				t.Error("projA's actor was removed from the fake's own actor store")
			}
			if _, ok := fc.actors[atespaceB+"/"+actorB]; !ok {
				t.Error("projB's actor was removed from the fake's own actor store")
			}
		})
	}
}

// TestSubstrateAgentManagerDelete_RecordExistsAndRecordlessSameSlug is C1's
// second required case: a record-EXISTS actor's real slug must resolve
// correctly even when an unrelated, different-project, record-less actor
// happens to invert to the same slug. Delete("dev") must remove only the
// record-having actor; the record-less other-project actor must be
// untouched.
func TestSubstrateAgentManagerDelete_RecordExistsAndRecordlessSameSlug(t *testing.T) {
	fc := newFakeSubstrateControlClient()
	actorServer := newFakeSubstrateActorServer()
	defer actorServer.Close()

	rt := scionruntime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient(actorServer.URL), nil, config.V1SubstrateConfig{})

	// The record-less other-project actor, injected directly (never
	// through this runtime's Run, so it genuinely has no record).
	const (
		otherAtespace = "scion-bbbbbbbbbbbb"
		otherActor    = "projB--dev"
	)
	fc.putActor(otherAtespace, otherActor, "uid-other-project")

	// The record-having actor, started for real.
	cfg := scionruntime.RunConfig{
		Name:         "projA--dev",
		ProjectID:    "550e8400-e29b-41d4-a716-446655440020",
		Image:        "us-docker.pkg.dev/proj/repo/scion-agent@sha256:" + strings.Repeat("a", 64),
		UnixUsername: "scion",
		NoAuth:       true,
		Labels:       map[string]string{"scion.name": "dev", "scion.agent": "true"},
	}
	if _, err := rt.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mgr := NewManager(rt)
	defer mgr.Close()

	if _, err := mgr.Delete(context.Background(), "dev", false, "", false); err != nil {
		t.Fatalf(`Delete("dev") error = %v`, err)
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	// AgentManager.Delete calls Runtime.Stop then Runtime.Delete, and for
	// substrate Stop is Delete (same underlying call) — so 2 DeleteActor
	// calls for one mgr.Delete() is expected. What matters is that EVERY
	// one of them names the record-having actor, never the record-less
	// other-project one.
	if len(fc.deleteActorCalls) == 0 {
		t.Fatal(`Delete("dev") called DeleteActor 0 times, want at least 1`)
	}
	for _, call := range fc.deleteActorCalls {
		got := call.GetActor()
		if got.GetName() != cfg.Name {
			t.Errorf("DeleteActor actor = %s/%s, want the record-having %q, not the record-less other-project actor", got.GetAtespace(), got.GetName(), cfg.Name)
		}
	}
	if _, ok := fc.actors[otherAtespace+"/"+otherActor]; !ok {
		t.Error("the record-less other-project actor was removed — it must be untouched")
	}
}

// TestSubstrateAgentManagerDelete_ThreeLevelActorNameNotMisread is C1's
// raw-agent-name collision case: an agent literally named "b--c", started
// in project "a", produces actor name "a--b--c" — indistinguishable, by the
// string alone, from project "a--b" agent "c". A second, legitimate actor
// "a--c" (project "a", agent "c") exists alongside it. Delete("c") must
// resolve only to "a--c" and never touch "a--b--c".
func TestSubstrateAgentManagerDelete_ThreeLevelActorNameNotMisread(t *testing.T) {
	fc := newFakeSubstrateControlClient()
	const atespace = "scion-proj"
	fc.putActor(atespace, "a--b--c", "uid-abc")
	fc.putActor(atespace, "a--c", "uid-ac")

	rt := scionruntime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient("http://unused"), nil, config.V1SubstrateConfig{})
	mgr := NewManager(rt)
	defer mgr.Close()

	if _, err := mgr.Delete(context.Background(), "c", false, "", false); err != nil {
		t.Fatalf(`Delete("c") error = %v`, err)
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()
	// AgentManager.Delete calls Runtime.Stop then Runtime.Delete, and for
	// substrate Stop is Delete (same underlying call) — so 2 DeleteActor
	// calls for one mgr.Delete() is expected; every one of them must name
	// "a--c", never "a--b--c".
	if len(fc.deleteActorCalls) == 0 {
		t.Fatal(`Delete("c") called DeleteActor 0 times, want at least 1`)
	}
	for _, call := range fc.deleteActorCalls {
		if got := call.GetActor().GetName(); got != "a--c" {
			t.Errorf(`DeleteActor actor name = %q, want "a--c" ("a--b--c" must never match a lookup for "c")`, got)
		}
	}
	if _, ok := fc.actors[atespace+"/a--b--c"]; !ok {
		t.Error(`"a--b--c" was removed — it must be untouched by Delete("c")`)
	}
}

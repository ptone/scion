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

package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
)

// -----------------------------------------------------------------------
// callRecorder: shared call-order tracker for both the fake ControlClient
// and the fake actor HTTP server.
// -----------------------------------------------------------------------

type callRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (c *callRecorder) record(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
}

func (c *callRecorder) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// -----------------------------------------------------------------------
// fakeControlClient: interface-backed fake for ateapipb.ControlClient.
// -----------------------------------------------------------------------

const fakeActorUID = "actor-uid-1"

type fakeControlClient struct {
	rec *callRecorder

	createAtespace          func(*ateapipb.CreateAtespaceRequest) (*ateapipb.Atespace, error)
	getActorTemplate        func(*ateapipb.GetActorTemplateRequest) (*ateapipb.ActorTemplate, error)
	createActorTemplate     func(*ateapipb.CreateActorTemplateRequest) (*ateapipb.ActorTemplate, error)
	createActor             func(*ateapipb.CreateActorRequest) (*ateapipb.Actor, error)
	createActorEgressPolicy func(*ateapipb.CreateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error)
	deleteActorEgressPolicy func(*ateapipb.DeleteActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error)
	resumeActor             func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error)
	getActor                func(*ateapipb.GetActorRequest) (*ateapipb.Actor, error)
	deleteActor             func(*ateapipb.DeleteActorRequest) (*ateapipb.Actor, error)
	listActors              func(*ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error)
}

// newFakeControlClient returns a fake wired for the Run happy path: a
// fresh template (GetActorTemplate NotFound, then CreateActorTemplate
// returns a ready one), CreateActor succeeds, the actor is immediately
// RUNNING with a worker assignment, and every other call succeeds.
// Individual tests override only the field(s) they care about.
func newFakeControlClient(rec *callRecorder) *fakeControlClient {
	return &fakeControlClient{
		rec: rec,
		createAtespace: func(*ateapipb.CreateAtespaceRequest) (*ateapipb.Atespace, error) {
			return &ateapipb.Atespace{}, nil
		},
		getActorTemplate: func(*ateapipb.GetActorTemplateRequest) (*ateapipb.ActorTemplate, error) {
			return nil, status.Error(codes.NotFound, "not found")
		},
		createActorTemplate: func(req *ateapipb.CreateActorTemplateRequest) (*ateapipb.ActorTemplate, error) {
			tmpl := req.GetActorTemplate()
			tmpl.Status = &ateapipb.ActorTemplateStatus{
				GoldenSnapshotStatus: &ateapipb.GoldenSnapshotStatus{
					GoldenTag: &ateapipb.ObjectRef{Atespace: "ate-golden", Name: "golden"},
				},
			}
			return tmpl, nil
		},
		createActor: func(req *ateapipb.CreateActorRequest) (*ateapipb.Actor, error) {
			return &ateapipb.Actor{
				Metadata: &ateapipb.ResourceMetadata{
					Atespace: req.GetActor().GetMetadata().GetAtespace(),
					Name:     req.GetActor().GetMetadata().GetName(),
					Uid:      fakeActorUID,
				},
			}, nil
		},
		createActorEgressPolicy: func(*ateapipb.CreateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
			return &ateapipb.EgressPolicy{}, nil
		},
		deleteActorEgressPolicy: func(*ateapipb.DeleteActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
			return &ateapipb.EgressPolicy{}, nil
		},
		resumeActor: func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
			return &ateapipb.ResumeActorResponse{}, nil
		},
		getActor: func(req *ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
			return &ateapipb.Actor{
				Metadata: &ateapipb.ResourceMetadata{
					Atespace: req.GetActor().GetAtespace(),
					Name:     req.GetActor().GetName(),
					Uid:      fakeActorUID,
				},
				Status: &ateapipb.ActorStatus{
					State: ateapipb.ActorState_ACTOR_STATE_RUNNING,
					WorkerAssignment: &ateapipb.WorkerAssignment{
						WorkerPod:       "worker-pod-1",
						WorkerNamespace: "ate-workers",
					},
				},
			}, nil
		},
		deleteActor: func(*ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
			return &ateapipb.Actor{}, nil
		},
		listActors: func(*ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
			return &ateapipb.ListActorsResponse{}, nil
		},
	}
}

func (f *fakeControlClient) GetActor(ctx context.Context, in *ateapipb.GetActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error) {
	f.rec.record("GetActor")
	return f.getActor(in)
}
func (f *fakeControlClient) CreateActor(ctx context.Context, in *ateapipb.CreateActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error) {
	f.rec.record("CreateActor")
	return f.createActor(in)
}
func (f *fakeControlClient) UpdateActor(ctx context.Context, in *ateapipb.UpdateActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error) {
	f.rec.record("UpdateActor")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) SuspendActor(ctx context.Context, in *ateapipb.SuspendActorRequest, opts ...grpc.CallOption) (*ateapipb.SuspendActorResponse, error) {
	f.rec.record("SuspendActor")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) PauseActor(ctx context.Context, in *ateapipb.PauseActorRequest, opts ...grpc.CallOption) (*ateapipb.PauseActorResponse, error) {
	f.rec.record("PauseActor")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) ResumeActor(ctx context.Context, in *ateapipb.ResumeActorRequest, opts ...grpc.CallOption) (*ateapipb.ResumeActorResponse, error) {
	f.rec.record("ResumeActor")
	return f.resumeActor(in)
}
func (f *fakeControlClient) RevertActor(ctx context.Context, in *ateapipb.RevertActorRequest, opts ...grpc.CallOption) (*ateapipb.RevertActorResponse, error) {
	f.rec.record("RevertActor")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) DeleteActor(ctx context.Context, in *ateapipb.DeleteActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error) {
	f.rec.record("DeleteActor")
	return f.deleteActor(in)
}
func (f *fakeControlClient) GetActorEgressPolicy(ctx context.Context, in *ateapipb.GetActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error) {
	f.rec.record("GetActorEgressPolicy")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) CreateActorEgressPolicy(ctx context.Context, in *ateapipb.CreateActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error) {
	f.rec.record("CreateActorEgressPolicy")
	return f.createActorEgressPolicy(in)
}
func (f *fakeControlClient) UpdateActorEgressPolicy(ctx context.Context, in *ateapipb.UpdateActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error) {
	f.rec.record("UpdateActorEgressPolicy")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) DeleteActorEgressPolicy(ctx context.Context, in *ateapipb.DeleteActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error) {
	f.rec.record("DeleteActorEgressPolicy")
	return f.deleteActorEgressPolicy(in)
}
func (f *fakeControlClient) MintActorJWT(ctx context.Context, in *ateapipb.MintActorJWTRequest, opts ...grpc.CallOption) (*ateapipb.MintActorJWTResponse, error) {
	f.rec.record("MintActorJWT")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) MintActorCertificate(ctx context.Context, in *ateapipb.MintActorCertificateRequest, opts ...grpc.CallOption) (*ateapipb.MintActorCertificateResponse, error) {
	f.rec.record("MintActorCertificate")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) CreateTag(ctx context.Context, in *ateapipb.CreateTagRequest, opts ...grpc.CallOption) (*ateapipb.Tag, error) {
	f.rec.record("CreateTag")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) GetTag(ctx context.Context, in *ateapipb.GetTagRequest, opts ...grpc.CallOption) (*ateapipb.Tag, error) {
	f.rec.record("GetTag")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) ListTags(ctx context.Context, in *ateapipb.ListTagsRequest, opts ...grpc.CallOption) (*ateapipb.ListTagsResponse, error) {
	f.rec.record("ListTags")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) UpdateTag(ctx context.Context, in *ateapipb.UpdateTagRequest, opts ...grpc.CallOption) (*ateapipb.Tag, error) {
	f.rec.record("UpdateTag")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) DeleteTag(ctx context.Context, in *ateapipb.DeleteTagRequest, opts ...grpc.CallOption) (*ateapipb.Tag, error) {
	f.rec.record("DeleteTag")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) ListWorkers(ctx context.Context, in *ateapipb.ListWorkersRequest, opts ...grpc.CallOption) (*ateapipb.ListWorkersResponse, error) {
	f.rec.record("ListWorkers")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) GetWorker(ctx context.Context, in *ateapipb.GetWorkerRequest, opts ...grpc.CallOption) (*ateapipb.Worker, error) {
	f.rec.record("GetWorker")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) CreateWorker(ctx context.Context, in *ateapipb.CreateWorkerRequest, opts ...grpc.CallOption) (*ateapipb.Worker, error) {
	f.rec.record("CreateWorker")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) UpdateWorker(ctx context.Context, in *ateapipb.UpdateWorkerRequest, opts ...grpc.CallOption) (*ateapipb.Worker, error) {
	f.rec.record("UpdateWorker")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) DeleteWorker(ctx context.Context, in *ateapipb.DeleteWorkerRequest, opts ...grpc.CallOption) (*ateapipb.Worker, error) {
	f.rec.record("DeleteWorker")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) DrainWorker(ctx context.Context, in *ateapipb.DrainWorkerRequest, opts ...grpc.CallOption) (*ateapipb.Worker, error) {
	f.rec.record("DrainWorker")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) ListWorkerActorAssignments(ctx context.Context, in *ateapipb.ListWorkerActorAssignmentsRequest, opts ...grpc.CallOption) (*ateapipb.ListWorkerActorAssignmentsResponse, error) {
	f.rec.record("ListWorkerActorAssignments")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) ListActors(ctx context.Context, in *ateapipb.ListActorsRequest, opts ...grpc.CallOption) (*ateapipb.ListActorsResponse, error) {
	f.rec.record("ListActors")
	return f.listActors(in)
}
func (f *fakeControlClient) CreateAtespace(ctx context.Context, in *ateapipb.CreateAtespaceRequest, opts ...grpc.CallOption) (*ateapipb.Atespace, error) {
	f.rec.record("CreateAtespace")
	return f.createAtespace(in)
}
func (f *fakeControlClient) GetAtespace(ctx context.Context, in *ateapipb.GetAtespaceRequest, opts ...grpc.CallOption) (*ateapipb.Atespace, error) {
	f.rec.record("GetAtespace")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) ListAtespaces(ctx context.Context, in *ateapipb.ListAtespacesRequest, opts ...grpc.CallOption) (*ateapipb.ListAtespacesResponse, error) {
	f.rec.record("ListAtespaces")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) DeleteAtespace(ctx context.Context, in *ateapipb.DeleteAtespaceRequest, opts ...grpc.CallOption) (*ateapipb.Atespace, error) {
	f.rec.record("DeleteAtespace")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) CreateActorTemplate(ctx context.Context, in *ateapipb.CreateActorTemplateRequest, opts ...grpc.CallOption) (*ateapipb.ActorTemplate, error) {
	f.rec.record("CreateActorTemplate")
	return f.createActorTemplate(in)
}
func (f *fakeControlClient) GetActorTemplate(ctx context.Context, in *ateapipb.GetActorTemplateRequest, opts ...grpc.CallOption) (*ateapipb.ActorTemplate, error) {
	f.rec.record("GetActorTemplate")
	return f.getActorTemplate(in)
}
func (f *fakeControlClient) ListActorTemplates(ctx context.Context, in *ateapipb.ListActorTemplatesRequest, opts ...grpc.CallOption) (*ateapipb.ListActorTemplatesResponse, error) {
	f.rec.record("ListActorTemplates")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}
func (f *fakeControlClient) DeleteActorTemplate(ctx context.Context, in *ateapipb.DeleteActorTemplateRequest, opts ...grpc.CallOption) (*ateapipb.ActorTemplate, error) {
	f.rec.record("DeleteActorTemplate")
	return nil, status.Error(codes.Unimplemented, "not used in this fake")
}

// -----------------------------------------------------------------------
// fakeActorServer: httptest-backed stand-in for sciontool substrate-serve,
// reached through a substrate.RouterClient exactly like the real router.
// -----------------------------------------------------------------------

type fakeActorServer struct {
	rec *callRecorder

	mu              sync.Mutex
	healthzState    string
	bootstrapStatus int
	lastBootstrap   *bootstrapRequest
	execStatus      int
	execResp        execResponse
}

func newFakeActorServer(rec *callRecorder) *fakeActorServer {
	return &fakeActorServer{rec: rec, healthzState: healthzAwaitingBootstrap}
}

func (s *fakeActorServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(substrateHealthzPath, func(w http.ResponseWriter, r *http.Request) {
		s.rec.record("healthz")
		s.mu.Lock()
		state := s.healthzState
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(healthzResponse{State: state})
	})
	mux.HandleFunc(substrateBootstrapPath, func(w http.ResponseWriter, r *http.Request) {
		s.rec.record("bootstrap")
		var req bootstrapRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.lastBootstrap = &req
		code := s.bootstrapStatus
		s.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
	})
	mux.HandleFunc(substrateExecPath, func(w http.ResponseWriter, r *http.Request) {
		s.rec.record("exec")
		s.mu.Lock()
		code := s.execStatus
		resp := s.execResp
		s.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	})
	return mux
}

// -----------------------------------------------------------------------
// Test fixtures
// -----------------------------------------------------------------------

func testSubstrateRunConfig() RunConfig {
	return RunConfig{
		Name:         "test-agent",
		ProjectID:    "550e8400-e29b-41d4-a716-446655440000",
		Image:        "us-docker.pkg.dev/proj/repo/scion-agent@sha256:" + strings.Repeat("a", 64),
		UnixUsername: "scion",
		Harness:      &mockHarness{command: []string{"claude", "--dangerously-skip-permissions"}},
		Env:          []string{"SCION_AGENT_ID=agent-1", "SCION_HUB_ENDPOINT=https://hub.example.com"},
		Labels:       map[string]string{"scion.agent_id": "agent-1"},
	}
}

// newTestSubstrateHarness resets the process-wide agent-state registry
// (substrateControlTokens/substrateAgentRecords — process-wide package vars,
// see their doc comment) so each test starts clean and can't leak state
// into, or pick up state left by, any other test that also uses this
// helper.
func newTestSubstrateHarness(t *testing.T, rec *callRecorder) (*SubstrateRuntime, *fakeControlClient, *fakeActorServer, func()) {
	t.Helper()
	resetSubstrateAgentStateForTest(t)

	fc := newFakeControlClient(rec)
	fa := newFakeActorServer(rec)
	server := httptest.NewServer(fa.handler())
	rt := newSubstrateRuntimeForTest(fc, substrate.NewRouterClient(server.URL), nil, config.V1SubstrateConfig{
		SnapshotStorage:   "gs://bucket/prefix/",
		SandboxConfigName: "gvisor-default",
	})
	return rt, fc, fa, server.Close
}

// resetSubstrateAgentStateForTest clears the process-wide
// substrateControlTokens/substrateAgentRecords maps for the duration of a
// test, restoring their previous contents afterward — the same save/restore
// pattern as resetSubstrateRuntimeRegistryForTest, for the same reason
// (these are now shared by every SubstrateRuntime instance, so tests must
// not leak state through them).
func resetSubstrateAgentStateForTest(t *testing.T) {
	t.Helper()
	substrateAgentStateMu.Lock()
	oldTokens := substrateControlTokens
	oldRecords := substrateAgentRecords
	substrateControlTokens = make(map[string]string)
	substrateAgentRecords = make(map[string]*substrateAgentRecord)
	substrateAgentStateMu.Unlock()
	t.Cleanup(func() {
		substrateAgentStateMu.Lock()
		substrateControlTokens = oldTokens
		substrateAgentRecords = oldRecords
		substrateAgentStateMu.Unlock()
	})
}

// -----------------------------------------------------------------------
// Run: happy path
// -----------------------------------------------------------------------

func TestSubstrateRun_HappyPath(t *testing.T) {
	rec := &callRecorder{}
	rt, _, fa, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	id, err := rt.Run(context.Background(), testSubstrateRunConfig())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantID := "scion-550e8400-e29/test-agent"
	if id != wantID {
		t.Errorf("Run() id = %q, want %q", id, wantID)
	}

	gotCalls := rec.list()
	wantOrder := []string{
		"CreateAtespace",
		"GetActorTemplate",
		"CreateActorTemplate",
		"CreateActor",
		"CreateActorEgressPolicy",
		"ResumeActor",
		"GetActor",
		"healthz",
		"bootstrap",
	}
	if strings.Join(gotCalls, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("call order = %v, want %v", gotCalls, wantOrder)
	}

	if fa.lastBootstrap == nil {
		t.Fatal("bootstrap was not called")
	}
	if fa.lastBootstrap.StartCmd == "" {
		t.Error("bootstrap start_cmd is empty")
	}
	if !strings.Contains(fa.lastBootstrap.StartCmd, "tmux new-session") {
		t.Errorf("bootstrap start_cmd = %q, want a tmux new-session invocation", fa.lastBootstrap.StartCmd)
	}
	if fa.lastBootstrap.ControlToken == "" {
		t.Error("bootstrap control_token is empty")
	}
	if fa.lastBootstrap.Env["SCION_RUNTIME"] != "substrate" {
		t.Errorf("bootstrap env SCION_RUNTIME = %q, want substrate", fa.lastBootstrap.Env["SCION_RUNTIME"])
	}
	if fa.lastBootstrap.Env["SCION_AGENT_ID"] != "agent-1" {
		t.Errorf("bootstrap env did not carry cfg.Env through: %v", fa.lastBootstrap.Env)
	}

	substrateAgentStateMu.Lock()
	_, hasToken := substrateControlTokens[id]
	substrateAgentStateMu.Unlock()
	if !hasToken {
		t.Error("control token was not cached for the returned id")
	}
}

// -----------------------------------------------------------------------
// Run: non-digest image error
// -----------------------------------------------------------------------

func TestSubstrateRun_NonDigestImageError(t *testing.T) {
	rec := &callRecorder{}
	rt, _, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	cfg := testSubstrateRunConfig()
	cfg.Image = "us-docker.pkg.dev/proj/repo/scion-agent:latest"

	_, err := rt.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run() expected an error for a non-digest-pinned image, got nil")
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Errorf("error = %v, want it to mention the digest requirement", err)
	}

	// Must fail before touching the control plane at all (step 2 precedes
	// CreateAtespace's caller-visible effect... actually atespace is step 1;
	// the image check is step 2, so CreateAtespace WILL have been called).
	// What must NOT happen is anything past the image check.
	for _, c := range rec.list() {
		if c == "CreateActor" || c == "CreateActorTemplate" {
			t.Errorf("Run() called %s before failing the digest check", c)
		}
	}
}

// -----------------------------------------------------------------------
// Run: cleanup on failure at each step after CreateActor
// -----------------------------------------------------------------------

func TestSubstrateRun_CleanupOnFailure(t *testing.T) {
	// bootstrapHijackSentinel proves the 409 path's error is genuinely
	// secret-free, not just free of the specific strings the other
	// assertions happen to check: the 409 case below puts this in cfg.Env
	// and asserts it is ABSENT from the error, guarding against a future
	// change that wraps errBootstrapHijacked with cfg-derived context and
	// forgets to route it through r.redact.
	const bootstrapHijackSentinel = "FAKE-KEY-SENTINEL-bootstrap-hijack-not-a-real-credential"

	cases := []struct {
		name       string
		inject     func(fc *fakeControlClient, fa *fakeActorServer)
		setup      func(rt *SubstrateRuntime) // optional; runs after newTestSubstrateHarness, before Run
		cfg        func(cfg RunConfig) RunConfig
		wantErr    string
		wantAbsent string // optional; asserts this string does NOT appear in the error
	}{
		{
			name: "CreateActorEgressPolicy fails",
			inject: func(fc *fakeControlClient, fa *fakeActorServer) {
				fc.createActorEgressPolicy = func(*ateapipb.CreateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
					return nil, status.Error(codes.Internal, "egress boom")
				}
			},
			wantErr: "egress boom",
		},
		{
			name: "ResumeActor fails",
			inject: func(fc *fakeControlClient, fa *fakeActorServer) {
				fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
					return nil, status.Error(codes.Internal, "resume boom")
				}
			},
			wantErr: "resume boom",
		},
		{
			name: "actor crashes while starting",
			inject: func(fc *fakeControlClient, fa *fakeActorServer) {
				fc.getActor = func(req *ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
					return &ateapipb.Actor{
						Metadata: &ateapipb.ResourceMetadata{Atespace: req.GetActor().GetAtespace(), Name: req.GetActor().GetName(), Uid: fakeActorUID},
						Status:   &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_CRASHED},
					}, nil
				}
			},
			wantErr: "crashed",
		},
		{
			name: "healthz never reaches awaiting-bootstrap (timeout)",
			inject: func(fc *fakeControlClient, fa *fakeActorServer) {
				fa.healthzState = healthzRunning
			},
			setup: func(rt *SubstrateRuntime) {
				// Real time.Now()-based deadline (waitForHealthz doesn't use
				// r.now), so keep this short enough to actually finish.
				rt.healthzTimeout = 20 * time.Millisecond
			},
			wantErr: "did not reach healthz state",
		},
		{
			name: "buildBootstrapFiles read error",
			cfg: func(cfg RunConfig) RunConfig {
				cfg.ResolvedAuth = &api.ResolvedAuth{
					Files: []api.FileMapping{
						{SourcePath: "/nonexistent/does-not-exist", ContainerPath: "~/.creds/token"},
					},
				}
				return cfg
			},
			wantErr: "read auth file",
		},
		{
			name: "bootstrap fails",
			inject: func(fc *fakeControlClient, fa *fakeActorServer) {
				fa.bootstrapStatus = http.StatusInternalServerError
			},
			wantErr: "bootstrap",
		},
		{
			name: "bootstrap hijacked (409)",
			inject: func(fc *fakeControlClient, fa *fakeActorServer) {
				fa.bootstrapStatus = http.StatusConflict
			},
			cfg: func(cfg RunConfig) RunConfig {
				cfg.Env = append(cfg.Env, "BOOTSTRAP_HIJACK_SENTINEL_KEY="+bootstrapHijackSentinel)
				return cfg
			},
			wantErr:    "bootstrapped by another caller",
			wantAbsent: bootstrapHijackSentinel,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, fa, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			if tc.inject != nil {
				tc.inject(fc, fa)
			}
			if tc.setup != nil {
				tc.setup(rt)
			}

			runCfg := testSubstrateRunConfig()
			if tc.cfg != nil {
				runCfg = tc.cfg(runCfg)
			}

			_, err := rt.Run(context.Background(), runCfg)
			if err == nil {
				t.Fatal("Run() expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if tc.wantAbsent != "" && strings.Contains(err.Error(), tc.wantAbsent) {
				t.Errorf("CREDENTIAL LEAK: error = %v, want it to NOT contain %q", err, tc.wantAbsent)
			}

			calls := rec.list()
			if !containsCall(calls, "DeleteActor") {
				t.Errorf("Run() failure did not clean up with DeleteActor; calls = %v", calls)
			}
			if !containsCall(calls, "DeleteActorEgressPolicy") {
				t.Errorf("Run() failure did not clean up with DeleteActorEgressPolicy; calls = %v", calls)
			}
		})
	}
}

func containsCall(calls []string, name string) bool {
	for _, c := range calls {
		if c == name {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------
// Template name stability
// -----------------------------------------------------------------------

func TestSubstrateTemplateName_Stable(t *testing.T) {
	resources := &api.ResourceSpec{Limits: api.ResourceList{CPU: "2", Memory: "4Gi"}}
	image := "repo/image@sha256:" + strings.Repeat("b", 64)
	baseCfg := config.V1SubstrateConfig{
		SandboxClass:      "gvisor",
		SandboxConfigName: "gvisor-default",
		SnapshotStorage:   "gs://bucket/prefix/",
	}

	n1 := substrateTemplateName(image, baseCfg, resources)
	n2 := substrateTemplateName(image, baseCfg, resources)
	if n1 != n2 {
		t.Errorf("substrateTemplateName() not stable: %q != %q", n1, n2)
	}
	if !strings.HasPrefix(n1, "scion-") {
		t.Errorf("substrateTemplateName() = %q, want scion- prefix", n1)
	}

	microVMCfg := baseCfg
	microVMCfg.SandboxClass = "microvm"
	n3 := substrateTemplateName(image, microVMCfg, resources)
	if n1 == n3 {
		t.Error("substrateTemplateName() did not change with sandbox class")
	}

	n4 := substrateTemplateName(image, baseCfg, &api.ResourceSpec{Limits: api.ResourceList{CPU: "4"}})
	if n1 == n4 {
		t.Error("substrateTemplateName() did not change with resources")
	}

	n5 := substrateTemplateName(image, baseCfg, nil)
	n6 := substrateTemplateName(image, baseCfg, config.BuiltinDefaultResources())
	if n5 != n6 {
		t.Error("substrateTemplateName() with nil resources did not match the resolved default resources (buildActorTemplate substitutes BuiltinDefaultResources for nil)")
	}

	configNameCfg := baseCfg
	configNameCfg.SandboxConfigName = "other-config"
	n7 := substrateTemplateName(image, configNameCfg, resources)
	if n1 == n7 {
		t.Error("substrateTemplateName() did not change with sandbox_config_name")
	}

	workerSelectorCfg := baseCfg
	workerSelectorCfg.WorkerSelector = map[string]string{"pool": "scion-agents"}
	n8 := substrateTemplateName(image, workerSelectorCfg, resources)
	if n1 == n8 {
		t.Error("substrateTemplateName() did not change with worker_selector")
	}

	snapshotCfg := baseCfg
	snapshotCfg.SnapshotStorage = "gs://other-bucket/prefix/"
	n9 := substrateTemplateName(image, snapshotCfg, resources)
	if n1 == n9 {
		t.Error("substrateTemplateName() did not change with snapshot_storage")
	}
}

// -----------------------------------------------------------------------
// List: mapping and label filter
// -----------------------------------------------------------------------

func TestSubstrateList_MappingAndLabelFilter(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	// Seed the in-memory record List needs, as Run() would.
	substrateAgentStateMu.Lock()
	substrateAgentRecords["uid-a"] = &substrateAgentRecord{
		Labels:  map[string]string{"scion.agent_id": "agent-a"},
		Project: "proj-a",
	}
	substrateAgentRecords["uid-b"] = &substrateAgentRecord{
		Labels:  map[string]string{"scion.agent_id": "agent-b"},
		Project: "proj-b",
	}
	substrateAgentStateMu.Unlock()

	fc.listActors = func(*ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
		return &ateapipb.ListActorsResponse{
			Actors: []*ateapipb.Actor{
				{
					Metadata: &ateapipb.ResourceMetadata{Atespace: "scion-proj", Name: "agent-a", Uid: "uid-a"},
					Status:   &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING},
				},
				{
					Metadata: &ateapipb.ResourceMetadata{Atespace: "scion-proj", Name: "agent-b", Uid: "uid-b"},
					Status:   &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_SUSPENDED},
				},
				{
					// No agentRecords entry — should still be listed, with
					// empty synthesised fields.
					Metadata: &ateapipb.ResourceMetadata{Atespace: "scion-proj", Name: "agent-c", Uid: "uid-c"},
					Status:   &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_CRASHED},
				},
			},
		}, nil
	}

	all, err := rt.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List() returned %d agents, want 3", len(all))
	}

	byName := map[string]api.AgentInfo{}
	for _, a := range all {
		byName[a.Name] = a
	}

	if byName["agent-a"].Phase != "running" {
		t.Errorf("agent-a phase = %q, want running", byName["agent-a"].Phase)
	}
	if byName["agent-b"].Phase != "stopped" {
		t.Errorf("agent-b phase = %q, want stopped", byName["agent-b"].Phase)
	}
	if byName["agent-c"].Phase != "error" {
		t.Errorf("agent-c phase = %q, want error", byName["agent-c"].Phase)
	}
	if byName["agent-a"].ContainerID != "scion-proj/agent-a" {
		t.Errorf("agent-a ContainerID = %q, want scion-proj/agent-a", byName["agent-a"].ContainerID)
	}
	if byName["agent-a"].Runtime != "substrate" {
		t.Errorf("agent-a Runtime = %q, want substrate", byName["agent-a"].Runtime)
	}

	// Label filter: only agent-a matches project=proj-a.
	filtered, err := rt.List(context.Background(), map[string]string{"scion.agent_id": "agent-a"})
	if err != nil {
		t.Fatalf("List() with filter error = %v", err)
	}
	if len(filtered) != 1 || filtered[0].Name != "agent-a" {
		t.Errorf("List() with label filter = %v, want only agent-a", filtered)
	}
}

// -----------------------------------------------------------------------
// Exec: error mapping
// -----------------------------------------------------------------------

func TestSubstrateExec_ErrorMapping(t *testing.T) {
	rec := &callRecorder{}
	rt, _, fa, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	id := "scion-proj/agent-a"
	substrateAgentStateMu.Lock()
	substrateControlTokens[id] = "tok-123"
	substrateAgentStateMu.Unlock()

	fa.execResp = execResponse{Stdout: "partial output", Stderr: "command not found", ExitCode: 127}

	_, err := rt.Exec(context.Background(), id, []string{"nope"})
	if err == nil {
		t.Fatal("Exec() expected an error for a non-zero exit code, got nil")
	}
	if !strings.Contains(err.Error(), "command not found") {
		t.Errorf("Exec() error = %v, want it to include stderr", err)
	}
	if !strings.Contains(err.Error(), "127") {
		t.Errorf("Exec() error = %v, want it to include the exit code", err)
	}
}

func TestSubstrateExec_NoCachedToken(t *testing.T) {
	rec := &callRecorder{}
	rt, _, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	_, err := rt.Exec(context.Background(), "scion-proj/agent-unknown", []string{"true"})
	if err == nil {
		t.Fatal("Exec() expected an error when no control token is cached, got nil")
	}
}

func TestSubstrateExec_Success(t *testing.T) {
	rec := &callRecorder{}
	rt, _, fa, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	id := "scion-proj/agent-a"
	substrateAgentStateMu.Lock()
	substrateControlTokens[id] = "tok-123"
	substrateAgentStateMu.Unlock()

	fa.execResp = execResponse{Stdout: "hello", ExitCode: 0}

	out, err := rt.Exec(context.Background(), id, []string{"echo", "hello"})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if out != "hello" {
		t.Errorf("Exec() = %q, want hello", out)
	}
}

// -----------------------------------------------------------------------
// Redaction: no secret env values leak into a Run error
// -----------------------------------------------------------------------

func TestSubstrateRun_ErrorDoesNotLeakEnvValues(t *testing.T) {
	const (
		envSentinel          = "FAKE-KEY-SENTINEL-cfg-env-not-a-real-credential"
		resolvedAuthSentinel = "FAKE-KEY-SENTINEL-resolved-auth-not-a-real-credential"
		authFileSentinel     = "FAKE-KEY-SENTINEL-resolved-auth-file-not-a-real-credential"
		envSecretSentinel    = "FAKE-KEY-SENTINEL-env-secret-not-a-real-credential"
		fileSecretSentinel   = "FAKE-KEY-SENTINEL-file-secret-not-a-real-credential"
		// collidingSentinel is the value of a cfg.Env entry AND a file-type
		// ResolvedSecret that share the same name ("COLLIDING_KEY"). With a
		// bare map key, both would be stored under the same key, so adding
		// the second would silently drop the first from the redaction set
		// while its value stayed in the request.
		collidingEnvSentinel    = "FAKE-KEY-SENTINEL-colliding-env-not-a-real-credential"
		collidingSecretSentinel = "FAKE-KEY-SENTINEL-colliding-secret-not-a-real-credential"
	)

	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	authFilePath := filepath.Join(t.TempDir(), "auth-file")
	if err := os.WriteFile(authFilePath, []byte(authFileSentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	// Fail a step whose error text, in a naive implementation, might
	// interpolate the whole request — resumeActor's error text stands in
	// for a hypothetical verbose upstream error that echoes back
	// everything the runtime sent it, regardless of source.
	fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
		return nil, status.Error(codes.Internal, "resume boom, env dump: "+
			"CFG_ENV_KEY="+envSentinel+" "+
			"RESOLVED_AUTH_KEY="+resolvedAuthSentinel+" "+
			"RESOLVED_AUTH_FILE="+authFileSentinel+" "+
			"ENV_SECRET_KEY="+envSecretSentinel+" "+
			"FILE_SECRET_KEY="+fileSecretSentinel+" "+
			"COLLIDING_KEY(env)="+collidingEnvSentinel+" "+
			"COLLIDING_KEY(secret)="+collidingSecretSentinel)
	}

	cfg := testSubstrateRunConfig()
	cfg.Env = append(cfg.Env,
		"CFG_ENV_KEY="+envSentinel,
		"COLLIDING_KEY="+collidingEnvSentinel,
	)
	cfg.ResolvedAuth = &api.ResolvedAuth{
		EnvVars: map[string]string{"RESOLVED_AUTH_KEY": resolvedAuthSentinel},
		Files:   []api.FileMapping{{SourcePath: authFilePath, ContainerPath: "~/.creds/auth-file"}},
	}
	cfg.ResolvedSecrets = []api.ResolvedSecret{
		{Name: "ENV_SECRET_KEY", Type: "environment", Target: "ENV_SECRET_KEY", Value: envSecretSentinel},
		{Name: "FILE_SECRET_KEY", Type: "file", Target: "~/.creds/file-secret", Value: fileSecretSentinel},
		// Same Name as the cfg.Env entry above, deliberately.
		{Name: "COLLIDING_KEY", Type: "file", Target: "~/.creds/colliding", Value: collidingSecretSentinel},
	}

	_, err := rt.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run() expected an error, got nil")
	}
	got := err.Error()

	for name, sentinel := range map[string]string{
		"cfg.Env":                    envSentinel,
		"ResolvedAuth.EnvVars":       resolvedAuthSentinel,
		"ResolvedAuth.Files content": authFileSentinel,
		"ResolvedSecrets (env)":      envSecretSentinel,
		"ResolvedSecrets (file)":     fileSecretSentinel,
		"colliding cfg.Env":          collidingEnvSentinel,
		"colliding ResolvedSecret":   collidingSecretSentinel,
	} {
		if strings.Contains(got, sentinel) {
			t.Errorf("CREDENTIAL LEAK: Run() error contains the %s secret value.\nerror: %s", name, got)
		}
	}
	for _, key := range []string{"CFG_ENV_KEY", "RESOLVED_AUTH_KEY", "ENV_SECRET_KEY", "FILE_SECRET_KEY", "COLLIDING_KEY"} {
		if !strings.Contains(got, key) {
			t.Errorf("redacted error does not name the redacted key %q.\nerror: %s", key, got)
		}
	}
	if !strings.Contains(got, "resume boom") {
		t.Errorf("error is no longer useful: diagnostic text was also removed.\nerror: %s", got)
	}
}

// -----------------------------------------------------------------------
// List: pagination and cross-tenant atespace filtering
// -----------------------------------------------------------------------

func TestSubstrateList_Pagination(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	var pageTokensSeen []string
	fc.listActors = func(req *ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
		pageTokensSeen = append(pageTokensSeen, req.GetPageToken())
		switch req.GetPageToken() {
		case "":
			return &ateapipb.ListActorsResponse{
				Actors: []*ateapipb.Actor{
					{Metadata: &ateapipb.ResourceMetadata{Atespace: "scion-proj", Name: "agent-a", Uid: "uid-a"},
						Status: &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING}},
				},
				NextPageToken: "page-2",
			}, nil
		case "page-2":
			return &ateapipb.ListActorsResponse{
				Actors: []*ateapipb.Actor{
					{Metadata: &ateapipb.ResourceMetadata{Atespace: "scion-proj", Name: "agent-b", Uid: "uid-b"},
						Status: &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING}},
				},
				NextPageToken: "",
			}, nil
		default:
			t.Fatalf("unexpected page token %q", req.GetPageToken())
			return nil, nil
		}
	}

	agents, err := rt.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("List() returned %d agents, want 2 (one per page); got %v", len(agents), agents)
	}
	if len(pageTokensSeen) != 2 || pageTokensSeen[0] != "" || pageTokensSeen[1] != "page-2" {
		t.Errorf("ListActors called with page tokens %v, want [\"\", \"page-2\"]", pageTokensSeen)
	}
}

func TestSubstrateList_SkipsNonScionAtespaces(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	fc.listActors = func(*ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
		return &ateapipb.ListActorsResponse{
			Actors: []*ateapipb.Actor{
				{Metadata: &ateapipb.ResourceMetadata{Atespace: "scion-proj", Name: "agent-a", Uid: "uid-a"},
					Status: &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING}},
				{Metadata: &ateapipb.ResourceMetadata{Atespace: "other-tenant", Name: "not-ours", Uid: "uid-x"},
					Status: &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING}},
			},
		}, nil
	}

	agents, err := rt.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "agent-a" {
		t.Errorf("List() = %v, want only agent-a (the other actor's atespace doesn't start with \"scion-\")", agents)
	}
}

func TestSubstrateList_SynthesisesNameAndAgentLabelsWithoutRecord(t *testing.T) {
	// No agentRecords entry at all for this actor — e.g. it was created by
	// a different SubstrateRuntime instance, or this instance just
	// restarted. List must still expose "scion.name"/"scion.agent" so the
	// broker's own by-name lookup succeeds.
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()

	fc.listActors = func(*ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
		return &ateapipb.ListActorsResponse{
			Actors: []*ateapipb.Actor{
				{Metadata: &ateapipb.ResourceMetadata{Atespace: "scion-proj", Name: "orphaned-agent", Uid: "uid-orphan"},
					Status: &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING}},
			},
		}, nil
	}

	agents, err := rt.List(context.Background(), map[string]string{"scion.name": "orphaned-agent"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("List() with scion.name filter = %v, want exactly the orphaned actor (no record needed)", agents)
	}
	if agents[0].Labels["scion.agent"] != "true" {
		t.Errorf("agent Labels[scion.agent] = %q, want \"true\"", agents[0].Labels["scion.agent"])
	}
}

// -----------------------------------------------------------------------
// NewSubstrateRuntime: process-wide memoization
// -----------------------------------------------------------------------

// resetSubstrateRuntimeRegistryForTest clears the process-wide
// substrateRuntimes registry for the duration of a test, restoring it
// afterward so this test can't leak state into (or pick up state left by)
// any other test.
func resetSubstrateRuntimeRegistryForTest(t *testing.T) {
	t.Helper()
	substrateRuntimesMu.Lock()
	old := substrateRuntimes
	substrateRuntimes = make(map[string]*SubstrateRuntime)
	substrateRuntimesMu.Unlock()
	t.Cleanup(func() {
		substrateRuntimesMu.Lock()
		substrateRuntimes = old
		substrateRuntimesMu.Unlock()
	})
}

func TestNewSubstrateRuntime_MemoizedAcrossCalls(t *testing.T) {
	resetSubstrateRuntimeRegistryForTest(t)

	rec := &callRecorder{}
	fc := newFakeControlClient(rec)
	fa := newFakeActorServer(rec)
	server := httptest.NewServer(fa.handler())
	defer server.Close()

	built := 0
	origBuilder := substrateRuntimeBuilder
	substrateRuntimeBuilder = func(cfg config.V1SubstrateConfig) (*SubstrateRuntime, error) {
		built++
		return newSubstrateRuntimeForTest(fc, substrate.NewRouterClient(server.URL), nil, cfg), nil
	}
	t.Cleanup(func() { substrateRuntimeBuilder = origBuilder })

	cfg := &config.V1SubstrateConfig{
		APIEndpoint:    "api.ate-system.svc:443",
		RouterEndpoint: server.URL,
	}

	// Simulates the broker's first `scion start`: GetRuntime resolves a
	// fresh substrate runtime and Run()s an agent on it.
	rt1, err := NewSubstrateRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSubstrateRuntime() error = %v", err)
	}
	id, err := rt1.Run(context.Background(), testSubstrateRunConfig())
	if err != nil {
		t.Fatalf("rt1.Run() error = %v", err)
	}

	// newFakeControlClient's default listActors doesn't track what
	// createActor "created" — it's a canned response, not an in-memory
	// store. Point it at the actor Run() just created so List can find it;
	// this only stands in for what a real ateapi ListActors would already
	// return.
	atespace, actorName, err := splitSubstrateID(id)
	if err != nil {
		t.Fatalf("splitSubstrateID(%q) error = %v", id, err)
	}
	fc.listActors = func(*ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
		return &ateapipb.ListActorsResponse{
			Actors: []*ateapipb.Actor{
				{
					Metadata: &ateapipb.ResourceMetadata{Atespace: atespace, Name: actorName, Uid: fakeActorUID},
					Status:   &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING},
				},
			},
		}, nil
	}

	// Simulates a second `scion start`: the broker resolves substrate again
	// (findings review: it's an auxiliary runtime, rebuilt on every start
	// that isn't the default profile).
	rt2, err := NewSubstrateRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSubstrateRuntime() (second call) error = %v", err)
	}

	if rt1 != rt2 {
		t.Fatal("NewSubstrateRuntime() returned different instances for the same config — state (control tokens, agent records, the gRPC conn) is not shared")
	}
	if built != 1 {
		t.Errorf("substrateRuntimeBuilder called %d times, want 1 (memoized)", built)
	}

	// The critical behavioural proof: an operation against the "new"
	// resolved instance (rt2, as the broker would use for e.g. `scion
	// message`/`scion delete` on the first agent) must still see the agent
	// rt1 created — this is exactly what broke before memoization.
	agents, err := rt2.List(context.Background(), map[string]string{"scion.name": "test-agent"})
	if err != nil {
		t.Fatalf("rt2.List() error = %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("rt2.List({scion.name: test-agent}) = %v, want to find the agent rt1 created", agents)
	}

	if _, err := rt2.Exec(context.Background(), id, []string{"true"}); err != nil {
		t.Errorf("rt2.Exec() on rt1's agent error = %v, want the control token cached by rt1 to still work", err)
	}
}

func TestNewSubstrateRuntime_DifferentConfigsGetDifferentInstances(t *testing.T) {
	resetSubstrateRuntimeRegistryForTest(t)

	built := 0
	origBuilder := substrateRuntimeBuilder
	substrateRuntimeBuilder = func(cfg config.V1SubstrateConfig) (*SubstrateRuntime, error) {
		built++
		return newSubstrateRuntimeForTest(newFakeControlClient(&callRecorder{}), substrate.NewRouterClient("http://unused"), nil, cfg), nil
	}
	t.Cleanup(func() { substrateRuntimeBuilder = origBuilder })

	cfgA := &config.V1SubstrateConfig{APIEndpoint: "a.example:443", RouterEndpoint: "http://a"}
	cfgB := &config.V1SubstrateConfig{APIEndpoint: "b.example:443", RouterEndpoint: "http://b"}

	rtA, err := NewSubstrateRuntime(cfgA)
	if err != nil {
		t.Fatalf("NewSubstrateRuntime(cfgA) error = %v", err)
	}
	rtB, err := NewSubstrateRuntime(cfgB)
	if err != nil {
		t.Fatalf("NewSubstrateRuntime(cfgB) error = %v", err)
	}
	if rtA == rtB {
		t.Error("NewSubstrateRuntime() returned the same instance for two different configs")
	}
	if built != 2 {
		t.Errorf("substrateRuntimeBuilder called %d times, want 2 (one per distinct config)", built)
	}
}

// TestSubstrateAgentState_SharedAcrossConfigChange confirms control tokens
// and agent records are shared process-wide, not per SubstrateRuntime
// instance, because the broker resolves a genuinely new instance whenever
// settings change (or a second substrate profile has different settings) —
// even though List's synthesised scion.name/scion.agent labels mean the
// agent stays visible, Exec (and any label lookup beyond those two) would
// break for every pre-existing agent the moment the config changed, without
// this.
func TestSubstrateAgentState_SharedAcrossConfigChange(t *testing.T) {
	resetSubstrateRuntimeRegistryForTest(t)
	resetSubstrateAgentStateForTest(t)

	rec := &callRecorder{}
	fc := newFakeControlClient(rec)
	fa := newFakeActorServer(rec)
	server := httptest.NewServer(fa.handler())
	defer server.Close()

	origBuilder := substrateRuntimeBuilder
	// Both configs' instances talk to the same underlying fake ateapi/
	// router, exactly as two SubstrateRuntime instances for the same real
	// cluster would in production — they differ only in
	// V1SubstrateConfig's Go value (egress_allow), which is what makes
	// NewSubstrateRuntime treat them as separate connection-registry
	// entries.
	substrateRuntimeBuilder = func(cfg config.V1SubstrateConfig) (*SubstrateRuntime, error) {
		return newSubstrateRuntimeForTest(fc, substrate.NewRouterClient(server.URL), nil, cfg), nil
	}
	t.Cleanup(func() { substrateRuntimeBuilder = origBuilder })

	cfgA := &config.V1SubstrateConfig{APIEndpoint: "api.example:443", RouterEndpoint: server.URL}
	cfgB := &config.V1SubstrateConfig{APIEndpoint: "api.example:443", RouterEndpoint: server.URL, EgressAllow: []string{"api.example.com"}}

	rtA, err := NewSubstrateRuntime(cfgA)
	if err != nil {
		t.Fatalf("NewSubstrateRuntime(cfgA) error = %v", err)
	}

	runCfg := testSubstrateRunConfig()
	runCfg.Project = "proj-a"
	id, err := rtA.Run(context.Background(), runCfg)
	if err != nil {
		t.Fatalf("rtA.Run() error = %v", err)
	}

	atespace, actorName, err := splitSubstrateID(id)
	if err != nil {
		t.Fatalf("splitSubstrateID(%q) error = %v", id, err)
	}
	fc.listActors = func(*ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
		return &ateapipb.ListActorsResponse{
			Actors: []*ateapipb.Actor{
				{
					Metadata: &ateapipb.ResourceMetadata{Atespace: atespace, Name: actorName, Uid: fakeActorUID},
					Status:   &ateapipb.ActorStatus{State: ateapipb.ActorState_ACTOR_STATE_RUNNING},
				},
			},
		}, nil
	}

	// Simulates the broker resolving substrate again after an
	// egress_allow edit (or a second profile with different settings) —
	// a genuinely different SubstrateRuntime instance.
	rtB, err := NewSubstrateRuntime(cfgB)
	if err != nil {
		t.Fatalf("NewSubstrateRuntime(cfgB) error = %v", err)
	}
	if rtA == rtB {
		t.Fatal("NewSubstrateRuntime(cfgB) returned the same instance as cfgA — this test requires distinct instances to prove state is shared, not per-instance")
	}

	if _, err := rtB.Exec(context.Background(), id, []string{"true"}); err != nil {
		t.Errorf("rtB.Exec() on rtA's agent error = %v, want the control token rtA cached to still work from rtB", err)
	}

	agents, err := rtB.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("rtB.List() error = %v", err)
	}
	if len(agents) != 1 || agents[0].Project != "proj-a" {
		t.Errorf("rtB.List() = %v, want the project label from rtA's record (\"proj-a\")", agents)
	}
}

// TestGetRuntime_Substrate_SettingsBased_Memoized:
// TestNewSubstrateRuntime_MemoizedAcrossCalls calls NewSubstrateRuntime
// directly, which wouldn't catch a future factory.go change that bypasses
// it (e.g. calling newSubstrateRuntimeFromConfig directly). This drives the
// same assertion through GetRuntime/
// config.LoadEffectiveSettings, the actual path pkg/runtimebroker exercises,
// with substrateRuntimeBuilder stubbed so it needs no real cluster/network.
func TestGetRuntime_Substrate_SettingsBased_Memoized(t *testing.T) {
	resetSubstrateRuntimeRegistryForTest(t)

	built := 0
	origBuilder := substrateRuntimeBuilder
	substrateRuntimeBuilder = func(cfg config.V1SubstrateConfig) (*SubstrateRuntime, error) {
		built++
		return newSubstrateRuntimeForTest(newFakeControlClient(&callRecorder{}), substrate.NewRouterClient("http://unused"), nil, cfg), nil
	}
	t.Cleanup(func() { substrateRuntimeBuilder = origBuilder })

	t.Setenv("PATH", "")
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	globalDir := filepath.Join(tmpHome, ".scion")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}

	settings := `{
		"schema_version": "1",
		"active_profile": "substrate",
		"runtimes": {
			"substrate-prod": {
				"type": "substrate",
				"substrate": {
					"api_endpoint": "api.ate-system.svc:443",
					"router_endpoint": "http://atenet-router.ate-system.svc:80"
				}
			}
		},
		"profiles": {
			"substrate": {
				"runtime": "substrate-prod"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(globalDir, "settings.json"), []byte(settings), 0644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	tmpWd := t.TempDir()
	if err := os.Chdir(tmpWd); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	r1 := GetRuntime("", "")
	rt1, ok := r1.(*SubstrateRuntime)
	if !ok {
		t.Fatalf("GetRuntime() = %T, want *SubstrateRuntime", r1)
	}

	r2 := GetRuntime("", "")
	rt2, ok := r2.(*SubstrateRuntime)
	if !ok {
		t.Fatalf("GetRuntime() (second call) = %T, want *SubstrateRuntime", r2)
	}

	if rt1 != rt2 {
		t.Error("GetRuntime() returned different SubstrateRuntime instances for the same settings-resolved config — memoization is bypassed somewhere on the factory path")
	}
	if built != 1 {
		t.Errorf("substrateRuntimeBuilder called %d times via GetRuntime, want 1 (memoized)", built)
	}
}

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
	"strings"
	"sync"
	"testing"

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

func newTestSubstrateHarness(rec *callRecorder) (*SubstrateRuntime, *fakeControlClient, *fakeActorServer, func()) {
	fc := newFakeControlClient(rec)
	fa := newFakeActorServer(rec)
	server := httptest.NewServer(fa.handler())
	rt := newSubstrateRuntimeForTest(fc, substrate.NewRouterClient(server.URL), nil, config.V1SubstrateConfig{
		SnapshotStorage:   "gs://bucket/prefix/",
		SandboxConfigName: "gvisor-default",
	})
	return rt, fc, fa, server.Close
}

// -----------------------------------------------------------------------
// Run: happy path
// -----------------------------------------------------------------------

func TestSubstrateRun_HappyPath(t *testing.T) {
	rec := &callRecorder{}
	rt, _, fa, closeServer := newTestSubstrateHarness(rec)
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

	rt.mu.Lock()
	_, hasToken := rt.controlTokens[id]
	rt.mu.Unlock()
	if !hasToken {
		t.Error("control token was not cached for the returned id")
	}
}

// -----------------------------------------------------------------------
// Run: non-digest image error
// -----------------------------------------------------------------------

func TestSubstrateRun_NonDigestImageError(t *testing.T) {
	rec := &callRecorder{}
	rt, _, _, closeServer := newTestSubstrateHarness(rec)
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
	cases := []struct {
		name    string
		inject  func(fc *fakeControlClient, fa *fakeActorServer)
		wantErr string
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
			name: "bootstrap fails",
			inject: func(fc *fakeControlClient, fa *fakeActorServer) {
				fa.bootstrapStatus = http.StatusInternalServerError
			},
			wantErr: "bootstrap",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, fa, closeServer := newTestSubstrateHarness(rec)
			defer closeServer()
			tc.inject(fc, fa)

			_, err := rt.Run(context.Background(), testSubstrateRunConfig())
			if err == nil {
				t.Fatal("Run() expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
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

	n1 := substrateTemplateName(image, "gvisor", resources)
	n2 := substrateTemplateName(image, "gvisor", resources)
	if n1 != n2 {
		t.Errorf("substrateTemplateName() not stable: %q != %q", n1, n2)
	}
	if !strings.HasPrefix(n1, "scion-") {
		t.Errorf("substrateTemplateName() = %q, want scion- prefix", n1)
	}

	n3 := substrateTemplateName(image, "microvm", resources)
	if n1 == n3 {
		t.Error("substrateTemplateName() did not change with sandbox class")
	}

	n4 := substrateTemplateName(image, "gvisor", &api.ResourceSpec{Limits: api.ResourceList{CPU: "4"}})
	if n1 == n4 {
		t.Error("substrateTemplateName() did not change with resources")
	}
}

// -----------------------------------------------------------------------
// List: mapping and label filter
// -----------------------------------------------------------------------

func TestSubstrateList_MappingAndLabelFilter(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(rec)
	defer closeServer()

	// Seed the in-memory record List needs, as Run() would.
	rt.mu.Lock()
	rt.agentRecords["uid-a"] = &substrateAgentRecord{
		Labels:  map[string]string{"scion.agent_id": "agent-a"},
		Project: "proj-a",
	}
	rt.agentRecords["uid-b"] = &substrateAgentRecord{
		Labels:  map[string]string{"scion.agent_id": "agent-b"},
		Project: "proj-b",
	}
	rt.mu.Unlock()

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
	rt, _, fa, closeServer := newTestSubstrateHarness(rec)
	defer closeServer()

	id := "scion-proj/agent-a"
	rt.mu.Lock()
	rt.controlTokens[id] = "tok-123"
	rt.mu.Unlock()

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
	rt, _, _, closeServer := newTestSubstrateHarness(rec)
	defer closeServer()

	_, err := rt.Exec(context.Background(), "scion-proj/agent-unknown", []string{"true"})
	if err == nil {
		t.Fatal("Exec() expected an error when no control token is cached, got nil")
	}
}

func TestSubstrateExec_Success(t *testing.T) {
	rec := &callRecorder{}
	rt, _, fa, closeServer := newTestSubstrateHarness(rec)
	defer closeServer()

	id := "scion-proj/agent-a"
	rt.mu.Lock()
	rt.controlTokens[id] = "tok-123"
	rt.mu.Unlock()

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
	const sentinel = "FAKE-KEY-SENTINEL-not-a-real-credential"

	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(rec)
	defer closeServer()

	// Fail a step whose error text, in a naive implementation, might
	// interpolate the whole request — resumeActor's error text stands in
	// for a hypothetical verbose upstream error.
	fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
		return nil, status.Error(codes.Internal, "resume boom, env dump: ANTHROPIC_API_KEY="+sentinel)
	}

	cfg := testSubstrateRunConfig()
	cfg.Env = append(cfg.Env, "ANTHROPIC_API_KEY="+sentinel)

	_, err := rt.Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run() expected an error, got nil")
	}
	got := err.Error()
	if strings.Contains(got, sentinel) {
		t.Errorf("CREDENTIAL LEAK: Run() error contains the secret value.\nerror: %s", got)
	}
	if !strings.Contains(got, "ANTHROPIC_API_KEY") {
		t.Errorf("redacted error does not name the redacted key.\nerror: %s", got)
	}
	if !strings.Contains(got, "resume boom") {
		t.Errorf("error is no longer useful: diagnostic text was also removed.\nerror: %s", got)
	}
}

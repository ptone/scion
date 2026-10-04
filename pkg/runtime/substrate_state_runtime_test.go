// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	"github.com/GoogleCloudPlatform/scion/third_party/ateapipb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Tests for SubstrateRuntime over the durable agent state store: the
// write-ahead protocol in Run, the deleting mark in Delete, and the List /
// RecordlessActors joins — above all, that an agent survives a broker
// restart (an emptied in-memory cache) fully manageable.

// stateTestSecretValue is an exec secret carried in the run config's Env;
// stateTestHarnessSecret one returned by the harness's GetEnv. Neither may
// ever appear in an error, log line or listing.
const (
	stateTestSecretValue   = "sk-env-secret-0123456789"
	stateTestHarnessSecret = "sk-harness-secret-9876543210"
)

// stateTestRunConfig is testSubstrateRunConfig with project identity and
// secret-bearing env, so a test can assert both survive persistence and
// that the secrets never leak.
func stateTestRunConfig(projectID, name string) RunConfig {
	cfg := testSubstrateRunConfig()
	cfg.Name = name
	cfg.ProjectID = projectID
	cfg.Project = "my-project"
	cfg.Template = "claude-template"
	cfg.Env = append(cfg.Env, "API_KEY="+stateTestSecretValue)
	cfg.Harness = &mockHarness{
		command: []string{"claude"},
		env:     map[string]string{"HARNESS_TOKEN": stateTestHarnessSecret},
	}
	cfg.Labels = map[string]string{
		"scion.agent_id":           "agent-1",
		"scion.name":               name,
		projectkeys.LabelProject:   "my-project",
		projectkeys.LabelProjectID: projectID,
		"scion.harness_config":     "claude",
	}
	cfg.Annotations = map[string]string{projectkeys.LabelProjectPath: "/srv/my-project"}
	return cfg
}

const stateTestProjectID = "550e8400-e29b-41d4-a716-446655440000"

// serveCreatedActors makes the fake ateapi's ListActors report every actor
// CreateActor made (scoped to the request's atespace when set) and its
// DeleteActor remove the actor, so List/Delete see a coherent cluster.
func serveCreatedActors(fc *fakeControlClient) {
	fc.listActors = func(req *ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		var out []*ateapipb.Actor
		for _, a := range fc.createdActors {
			if req.GetAtespace() != "" && a.GetMetadata().GetAtespace() != req.GetAtespace() {
				continue
			}
			out = append(out, a)
		}
		slices.SortFunc(out, func(a, b *ateapipb.Actor) int {
			return strings.Compare(a.GetMetadata().GetName(), b.GetMetadata().GetName())
		})
		return &ateapipb.ListActorsResponse{Actors: out}, nil
	}
	fc.deleteActor = func(req *ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
		key := req.GetActor().GetAtespace() + "/" + req.GetActor().GetName()
		fc.mu.Lock()
		defer fc.mu.Unlock()
		a, ok := fc.createdActors[key]
		if !ok {
			return nil, status.Error(codes.NotFound, "actor not found")
		}
		delete(fc.createdActors, key)
		return a, nil
	}
}

// stateHarness is newTestSubstrateHarness with its state clientset exposed.
type stateHarness struct {
	rt     *SubstrateRuntime
	fc     *fakeControlClient
	fa     *fakeActorServer
	rec    *callRecorder
	cs     *k8sfake.Clientset
	store  *k8sSecretStateStore
	server *httptest.Server
	cfg    config.V1SubstrateConfig
}

func newStateHarness(t *testing.T) *stateHarness {
	t.Helper()
	resetSubstrateAgentStateForTest(t)
	rec := &callRecorder{}
	fc := newFakeControlClient(rec)
	serveCreatedActors(fc)
	fa := newFakeActorServer(rec)
	server := httptest.NewServer(fa.handler())
	t.Cleanup(server.Close)
	cs := newStateFakeClientset()
	cfg := config.V1SubstrateConfig{
		SnapshotStorage:   "gs://bucket/prefix/",
		SandboxConfigName: "gvisor-default",
		StateNamespace:    testStateNamespace,
	}
	return &stateHarness{
		rt:     NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient(server.URL), cs, cfg),
		fc:     fc,
		fa:     fa,
		rec:    rec,
		cs:     cs,
		store:  newK8sSecretStateStore(cs, testStateNamespace),
		server: server,
		cfg:    cfg,
	}
}

// secondRuntime is a fresh SubstrateRuntime over the same cluster and the
// same state namespace: what a restarted broker process builds.
func (h *stateHarness) secondRuntime() *SubstrateRuntime {
	return NewSubstrateRuntimeForTest(h.fc, substrate.NewRouterClient(h.server.URL), h.cs, h.cfg)
}

func (h *stateHarness) calls(name string) int {
	n := 0
	for _, c := range h.rec.list() {
		if c == name {
			n++
		}
	}
	return n
}

// failSecrets makes every `verb` on secrets fail with an Internal error
// whose message embeds the given text (to prove the store never echoes a
// client-go error's message).
func failSecrets(cs *k8sfake.Clientset, verb, embedded string) {
	cs.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("backend failure near %s", embedded))
	})
}

// assertAgentManageable is the restart contract: rt (a process whose cache
// is empty) lists id with its persisted project identity, does not report
// it record-less, and deletes it — actor and state object both.
func assertAgentManageable(t *testing.T, h *stateHarness, rt *SubstrateRuntime, id, name string) {
	t.Helper()
	ctx := context.Background()

	agents, err := rt.List(ctx, map[string]string{"scion.name": name, projectkeys.LabelProjectID: stateTestProjectID})
	if err != nil {
		t.Fatalf("List() after restart error = %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("List() after restart = %d agents, want 1 (the persisted agent): %+v", len(agents), agents)
	}
	got := agents[0]
	if got.ContainerID != id || got.Name != name {
		t.Errorf("List() agent = {ContainerID:%q Name:%q}, want {%q %q}", got.ContainerID, got.Name, id, name)
	}
	if got.ProjectID != stateTestProjectID || got.Project != "my-project" ||
		got.Labels[projectkeys.LabelProjectID] != stateTestProjectID || got.Labels[projectkeys.LabelProject] != "my-project" {
		t.Errorf("List() agent project identity = {Project:%q ProjectID:%q labels:%v}, want the persisted project labels", got.Project, got.ProjectID, got.Labels)
	}
	if got.Template != "claude-template" || got.HarnessConfig != "claude" || got.ProjectPath != "/srv/my-project" || got.Image == "" {
		t.Errorf("List() agent record = {Template:%q HarnessConfig:%q ProjectPath:%q Image:%q}, want the persisted record", got.Template, got.HarnessConfig, got.ProjectPath, got.Image)
	}

	_, recordless, err := rt.RecordlessActors(ctx, stateTestProjectID)
	if err != nil {
		t.Fatalf("RecordlessActors() error = %v", err)
	}
	if len(recordless) != 0 {
		t.Errorf("RecordlessActors() after restart = %+v, want none (the agent's record is persisted)", recordless)
	}

	if err := rt.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() after restart error = %v", err)
	}
	if _, err := h.store.Get(ctx, id); !errors.Is(err, errStateNotFound) {
		t.Errorf("state object after Delete: Get error = %v, want errStateNotFound", err)
	}
	h.fc.mu.Lock()
	_, stillThere := h.fc.createdActors[id]
	h.fc.mu.Unlock()
	if stillThere {
		t.Error("actor still exists after Delete")
	}
}

// TestSubstrateRestart_PersistedAgentSurvivesCacheWipe is the headline
// restart contract: an agent Run by this broker is still listed with its
// project identity and still deletable after the in-memory cache is gone.
func TestSubstrateRestart_PersistedAgentSurvivesCacheWipe(t *testing.T) {
	h := newStateHarness(t)
	id, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "survivor"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	t.Cleanup(WipeSubstrateAgentStateForTest())

	assertAgentManageable(t, h, h.rt, id, "survivor")
}

// TestSubstrateRestart_PersistedAgentManageableFromNewRuntimeInstance is
// the same contract from a second SubstrateRuntime over the same state
// namespace — a restarted broker process.
func TestSubstrateRestart_PersistedAgentManageableFromNewRuntimeInstance(t *testing.T) {
	h := newStateHarness(t)
	id, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "survivor"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	t.Cleanup(WipeSubstrateAgentStateForTest())

	assertAgentManageable(t, h, h.secondRuntime(), id, "survivor")
}

// TestSubstrateRun_PersistsEveryFieldBeforeCreateActor pins the
// write-ahead: when CreateActor is called the pending object already holds
// the record, token and exec secrets; after Run it is committed with the
// actor UID.
func TestSubstrateRun_PersistsEveryFieldBeforeCreateActor(t *testing.T) {
	h := newStateHarness(t)
	ctx := context.Background()
	cfg := stateTestRunConfig(stateTestProjectID, "wal")
	wantID := substrateAtespaceName(stateTestProjectID) + "/wal"

	defaultCreate := h.fc.createActor
	var atCreate *substrateAgentState
	h.fc.createActor = func(req *ateapipb.CreateActorRequest) (*ateapipb.Actor, error) {
		st, err := h.store.Get(ctx, wantID)
		if err != nil {
			t.Errorf("state object at CreateActor: Get error = %v, want a pending object", err)
		}
		atCreate = st
		return defaultCreate(req)
	}

	id, err := h.rt.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if id != wantID {
		t.Fatalf("Run() id = %q, want %q", id, wantID)
	}
	if atCreate == nil {
		t.Fatal("no state object observed at CreateActor")
	}
	if atCreate.Phase != substrateStatePending || atCreate.ActorUID != "" {
		t.Errorf("state at CreateActor = {Phase:%q ActorUID:%q}, want {pending, \"\"}", atCreate.Phase, atCreate.ActorUID)
	}
	if atCreate.ControlToken == "" {
		t.Error("state at CreateActor has no control token")
	}
	wantSecrets := substrateSecretCandidates(cfg)
	if fmt.Sprint(atCreate.ExecSecrets) != fmt.Sprint(wantSecrets) {
		t.Errorf("state at CreateActor exec secrets = %d entries, want the %d run-config secret candidates", len(atCreate.ExecSecrets), len(wantSecrets))
	}
	wantRecord := newSubstrateAgentRecord(cfg)
	if fmt.Sprint(atCreate.Record) != fmt.Sprint(wantRecord) {
		t.Errorf("state at CreateActor record = %+v, want %+v", atCreate.Record, wantRecord)
	}

	final, err := h.store.Get(ctx, id)
	if err != nil {
		t.Fatalf("state after Run: Get error = %v", err)
	}
	if final.Phase != substrateStateCommitted || final.ActorUID != fakeActorUID {
		t.Errorf("state after Run = {Phase:%q ActorUID:%q}, want {committed, %q}", final.Phase, final.ActorUID, fakeActorUID)
	}
	if final.ControlToken != atCreate.ControlToken {
		t.Error("committed control token differs from the one written ahead of CreateActor")
	}
	substrateAgentStateMu.Lock()
	cachedToken := substrateControlTokens[id]
	substrateAgentStateMu.Unlock()
	if cachedToken != final.ControlToken {
		t.Error("cached control token differs from the persisted one")
	}
}

// TestSubstrateRun_ExistingCommittedIDRefusedBeforeCreateActor: a second
// Run of a persisted agent's id is refused at the state claim with the
// container-name-in-use error, without reaching CreateActor and without
// touching the existing agent's state.
func TestSubstrateRun_ExistingCommittedIDRefusedBeforeCreateActor(t *testing.T) {
	h := newStateHarness(t)
	ctx := context.Background()
	cfg := stateTestRunConfig(stateTestProjectID, "dup")
	id, err := h.rt.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	before, _ := h.store.Get(ctx, id)
	creates := h.calls("CreateActor")

	_, err = h.secondRuntime().Run(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second Run() error = %v, want a container-name-in-use error", err)
	}
	if got := h.calls("CreateActor"); got != creates {
		t.Errorf("CreateActor calls = %d after the refused Run, want %d (refused at the state claim)", got, creates)
	}
	after, err := h.store.Get(ctx, id)
	if err != nil || after.Phase != substrateStateCommitted || after.ControlToken != before.ControlToken {
		t.Errorf("existing agent's state after the refused Run = %+v (err %v), want it unchanged", after, err)
	}
}

// TestSubstrateRun_DeletingIDRefusedWithRetry: an id whose Delete is still
// pending is refused with a retryable error.
func TestSubstrateRun_DeletingIDRefusedWithRetry(t *testing.T) {
	h := newStateHarness(t)
	ctx := context.Background()
	cfg := stateTestRunConfig(stateTestProjectID, "dying")
	id := substrateAtespaceName(stateTestProjectID) + "/dying"
	st := testState(substrateAtespaceName(stateTestProjectID), "dying")
	st.Phase = substrateStateDeleting
	if err := h.store.Create(ctx, st); err != nil {
		t.Fatal(err)
	}

	_, err := h.rt.Run(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "being deleted; retry") {
		t.Fatalf("Run() error = %v, want a 'being deleted; retry' error", err)
	}
	if h.calls("CreateActor") != 0 {
		t.Error("CreateActor was called for an id being deleted")
	}
	if got, _ := h.store.Get(ctx, id); got == nil || got.Phase != substrateStateDeleting {
		t.Errorf("deleting state object after the refused Run = %+v, want it untouched", got)
	}
}

// TestSubstrateRun_StoreCreateFailureFailsBeforeCreateActor: if the state
// cannot be written ahead, nothing is created on the cluster.
func TestSubstrateRun_StoreCreateFailureFailsBeforeCreateActor(t *testing.T) {
	h := newStateHarness(t)
	failSecrets(h.cs, "create", "x")

	_, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "nostore"))
	if err == nil || !strings.Contains(err.Error(), "persist agent state") {
		t.Fatalf("Run() error = %v, want a persist-agent-state error", err)
	}
	if h.calls("CreateActor") != 0 {
		t.Error("CreateActor was called although the state write-ahead failed")
	}
}

// TestSubstrateRun_NilStoreRefuses: a runtime with no state store never
// starts an agent it could not manage after a restart.
func TestSubstrateRun_NilStoreRefuses(t *testing.T) {
	resetSubstrateAgentStateForTest(t)
	rec := &callRecorder{}
	fc := newFakeControlClient(rec)
	rt := NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient("http://unused"), nil, config.V1SubstrateConfig{StateNamespace: testStateNamespace})

	_, err := rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "nostate"))
	if err == nil || !strings.Contains(err.Error(), "state store is not configured") {
		t.Fatalf("Run() error = %v, want a state-store-not-configured refusal", err)
	}
	if slices.Contains(rec.list(), "CreateActor") {
		t.Error("CreateActor was called without a state store")
	}
}

// TestSubstrateRun_FailureAfterCreateActorDropsState: a start that fails
// after CreateActor deletes the actor and its own state object.
func TestSubstrateRun_FailureAfterCreateActorDropsState(t *testing.T) {
	h := newStateHarness(t)
	h.fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
		return nil, status.Error(codes.Unavailable, "resume failed")
	}
	id := substrateAtespaceName(stateTestProjectID) + "/fails"

	if _, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "fails")); err == nil {
		t.Fatal("Run() error = nil, want the resume failure")
	}
	if _, err := h.store.Get(context.Background(), id); !errors.Is(err, errStateNotFound) {
		t.Errorf("state object after the failed Run: Get error = %v, want errStateNotFound", err)
	}
	if h.calls("DeleteActor") == 0 {
		t.Error("actor was not deleted after the failed Run")
	}
}

// TestSubstrateRun_CreateActorFailureDropsState: a CreateActor failure
// releases the claim, so the id is immediately reusable.
func TestSubstrateRun_CreateActorFailureDropsState(t *testing.T) {
	h := newStateHarness(t)
	h.fc.createActor = func(*ateapipb.CreateActorRequest) (*ateapipb.Actor, error) {
		return nil, status.Error(codes.Unavailable, "create failed")
	}
	id := substrateAtespaceName(stateTestProjectID) + "/nocreate"
	if _, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "nocreate")); err == nil {
		t.Fatal("Run() error = nil, want the CreateActor failure")
	}
	if _, err := h.store.Get(context.Background(), id); !errors.Is(err, errStateNotFound) {
		t.Errorf("state object after the failed CreateActor: Get error = %v, want errStateNotFound", err)
	}
}

// markDeletingBehindRun simulates a concurrent Delete claiming the id
// while Run is in flight.
func markDeletingBehindRun(t *testing.T, store *k8sSecretStateStore, id string) {
	t.Helper()
	st, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("concurrent Delete: Get error = %v", err)
	}
	st.Phase = substrateStateDeleting
	if err := store.Update(context.Background(), st); err != nil {
		t.Fatalf("concurrent Delete: Update error = %v", err)
	}
}

// TestSubstrateRun_ConcurrentDeleteBeforeUIDRecordAborts: a Delete that
// claims the id between the write-ahead and the UID record makes Run's CAS
// fail; Run deletes the actor it created and leaves the deleting object to
// the Delete that owns it.
func TestSubstrateRun_ConcurrentDeleteBeforeUIDRecordAborts(t *testing.T) {
	h := newStateHarness(t)
	id := substrateAtespaceName(stateTestProjectID) + "/raced"
	defaultCreate := h.fc.createActor
	h.fc.createActor = func(req *ateapipb.CreateActorRequest) (*ateapipb.Actor, error) {
		markDeletingBehindRun(t, h.store, id)
		return defaultCreate(req)
	}

	_, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "raced"))
	if err == nil || !strings.Contains(err.Error(), "deleted while starting") {
		t.Fatalf("Run() error = %v, want 'deleted while starting'", err)
	}
	if h.calls("DeleteActor") == 0 {
		t.Error("Run did not delete its actor after losing the id to a Delete")
	}
	if h.calls("ResumeActor") != 0 {
		t.Error("Run kept starting the actor after losing the id to a Delete")
	}
	st, err := h.store.Get(context.Background(), id)
	if err != nil || st.Phase != substrateStateDeleting {
		t.Errorf("state object = %+v (err %v), want it left deleting for its Delete", st, err)
	}
}

// TestSubstrateRun_ConcurrentDeleteBeforeCommitAborts: the same race,
// landing after the bootstrap path started but before the commit.
func TestSubstrateRun_ConcurrentDeleteBeforeCommitAborts(t *testing.T) {
	h := newStateHarness(t)
	id := substrateAtespaceName(stateTestProjectID) + "/raced"
	h.fc.resumeActor = func(*ateapipb.ResumeActorRequest) (*ateapipb.ResumeActorResponse, error) {
		markDeletingBehindRun(t, h.store, id)
		return &ateapipb.ResumeActorResponse{}, nil
	}

	_, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "raced"))
	if err == nil || !strings.Contains(err.Error(), "deleted while starting") {
		t.Fatalf("Run() error = %v, want 'deleted while starting'", err)
	}
	if h.calls("DeleteActor") == 0 {
		t.Error("Run did not delete its actor after losing the id to a Delete")
	}
	substrateAgentStateMu.Lock()
	_, cached := substrateControlTokens[id]
	substrateAgentStateMu.Unlock()
	if cached {
		t.Error("Run cached a control token for an agent it lost to a Delete")
	}
	st, err := h.store.Get(context.Background(), id)
	if err != nil || st.Phase != substrateStateDeleting {
		t.Errorf("state object = %+v (err %v), want it left deleting for its Delete", st, err)
	}
}

// TestSubstrateDelete_MarksDeletingBeforeDeleteActor pins Delete's order:
// the object is "deleting" while DeleteActor runs, and gone afterwards.
func TestSubstrateDelete_MarksDeletingBeforeDeleteActor(t *testing.T) {
	h := newStateHarness(t)
	ctx := context.Background()
	id, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "ordered"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	served := h.fc.deleteActor
	var phaseAtDelete substrateStatePhase
	h.fc.deleteActor = func(req *ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
		if st, err := h.store.Get(ctx, id); err == nil {
			phaseAtDelete = st.Phase
		}
		return served(req)
	}

	if err := h.rt.Delete(ctx, id); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if phaseAtDelete != substrateStateDeleting {
		t.Errorf("state phase during DeleteActor = %q, want %q", phaseAtDelete, substrateStateDeleting)
	}
	if _, err := h.store.Get(ctx, id); !errors.Is(err, errStateNotFound) {
		t.Errorf("state object after Delete: Get error = %v, want errStateNotFound", err)
	}
}

// TestSubstrateDelete_DeleteActorFailureLeavesDeletingForRetry: a failed
// DeleteActor keeps the object "deleting" (a Run of the id is refused) and
// a retried Delete finishes the job.
func TestSubstrateDelete_DeleteActorFailureLeavesDeletingForRetry(t *testing.T) {
	h := newStateHarness(t)
	ctx := context.Background()
	cfg := stateTestRunConfig(stateTestProjectID, "stubborn")
	id, err := h.rt.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	served := h.fc.deleteActor
	h.fc.deleteActor = func(*ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
		return nil, status.Error(codes.Unavailable, "control plane down")
	}

	if err := h.rt.Delete(ctx, id); err == nil {
		t.Fatal("Delete() error = nil, want the DeleteActor failure")
	}
	st, err := h.store.Get(ctx, id)
	if err != nil || st.Phase != substrateStateDeleting {
		t.Fatalf("state object after a failed Delete = %+v (err %v), want it deleting", st, err)
	}
	if _, err := h.rt.Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "being deleted; retry") {
		t.Errorf("Run() during a pending Delete error = %v, want 'being deleted; retry'", err)
	}

	h.fc.deleteActor = served
	if err := h.secondRuntime().Delete(ctx, id); err != nil {
		t.Fatalf("retried Delete() error = %v", err)
	}
	if _, err := h.store.Get(ctx, id); !errors.Is(err, errStateNotFound) {
		t.Errorf("state object after the retried Delete: Get error = %v, want errStateNotFound", err)
	}
}

// TestSubstrateDelete_StoreReadFailureTouchesNothing: Delete fails before
// any cluster call when the state cannot be read.
func TestSubstrateDelete_StoreReadFailureTouchesNothing(t *testing.T) {
	h := newStateHarness(t)
	ctx := context.Background()
	id, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "unread"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	failSecrets(h.cs, "get", "x")

	if err := h.rt.Delete(ctx, id); err == nil {
		t.Fatal("Delete() error = nil, want the state read failure")
	}
	if h.calls("DeleteActor") != 0 || h.calls("DeleteActorEgressPolicy") != 0 {
		t.Error("Delete touched the cluster although the state could not be read")
	}
}

// TestSubstrateDelete_LegacyActorWithoutState: an actor with no state
// object is deleted exactly as before.
func TestSubstrateDelete_LegacyActorWithoutState(t *testing.T) {
	h := newStateHarness(t)
	id := seedLegacyActor(h.fc, stateTestProjectID, "legacy", "legacy-uid")
	if err := h.rt.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete() of a legacy actor error = %v", err)
	}
	if h.calls("DeleteActor") != 1 {
		t.Errorf("DeleteActor calls = %d, want 1", h.calls("DeleteActor"))
	}
}

// TestSubstrateList_StoreFailureFailsList: List never silently reports
// persisted agents as record-less when the store is unreadable.
func TestSubstrateList_StoreFailureFailsList(t *testing.T) {
	h := newStateHarness(t)
	if _, err := h.rt.Run(context.Background(), stateTestRunConfig(stateTestProjectID, "listed")); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	failSecrets(h.cs, "list", "x")
	if _, err := h.rt.List(context.Background(), nil); err == nil {
		t.Fatal("List() error = nil, want the state list failure")
	}
	if _, _, err := h.rt.RecordlessActors(context.Background(), stateTestProjectID); err == nil {
		t.Fatal("RecordlessActors() error = nil, want the state list failure")
	}
}

// TestSubstrateRecordlessActors_PendingStateCountsAsRecordless: an actor
// whose start never committed has no usable record yet.
func TestSubstrateRecordlessActors_PendingStateCountsAsRecordless(t *testing.T) {
	h := newStateHarness(t)
	ctx := context.Background()
	atespace := substrateAtespaceName(stateTestProjectID)
	seedLegacyActor(h.fc, stateTestProjectID, "half-started", "uid-half")
	st := testState(atespace, "half-started")
	st.ActorUID = "uid-half"
	if err := h.store.Create(ctx, st); err != nil {
		t.Fatal(err)
	}
	_, recordless, err := h.rt.RecordlessActors(ctx, stateTestProjectID)
	if err != nil {
		t.Fatalf("RecordlessActors() error = %v", err)
	}
	if len(recordless) != 1 {
		t.Fatalf("RecordlessActors() = %+v, want the pending actor", recordless)
	}
}

// TestSubstrateState_NoCredentialLeak drives the store-backed Run, Delete
// and List through success and failure paths and asserts neither the
// control token nor any exec secret appears in a returned error, a log
// line or a listing. Store failures embed a secret in the client-go error
// message to prove the store never echoes it.
func TestSubstrateState_NoCredentialLeak(t *testing.T) {
	logs := captureRuntimeLog(t)
	h := newStateHarness(t)
	ctx := context.Background()

	var surfaced []string
	note := func(err error) {
		if err != nil {
			surfaced = append(surfaced, err.Error())
		}
	}

	// Success path.
	id, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "secretive"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	st, err := h.store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	token := st.ControlToken
	secrets := []string{token, stateTestSecretValue, stateTestHarnessSecret}

	t.Cleanup(WipeSubstrateAgentStateForTest())
	agents, err := h.secondRuntime().List(ctx, nil)
	note(err)
	surfaced = append(surfaced, fmt.Sprintf("%+v", agents))
	_, rl, err := h.rt.RecordlessActors(ctx, stateTestProjectID)
	note(err)
	surfaced = append(surfaced, fmt.Sprintf("%+v", rl))
	note(h.rt.Delete(ctx, id))

	// Failure paths, each with the backend error message carrying a secret.
	type step struct {
		verb string
		run  func() error
	}
	runAgain := func() error {
		_, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "secretive-2"))
		return err
	}
	for _, s := range []step{
		{"create", runAgain},
		{"update", runAgain},
		{"list", func() error { _, err := h.rt.List(ctx, nil); return err }},
	} {
		cs := h.cs
		cs.PrependReactor(s.verb, "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("backend failure near %s %s %s", token, stateTestSecretValue, stateTestHarnessSecret))
		})
		err := s.run()
		if err == nil {
			t.Errorf("%s failure: operation succeeded, want an error", s.verb)
		}
		note(err)
		cs.ReactionChain = cs.ReactionChain[1:]
	}

	// Delete failing at the store read, and at the store delete.
	id2, err := h.rt.Run(ctx, stateTestRunConfig(stateTestProjectID, "secretive-3"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, verb := range []string{"get", "delete"} {
		h.cs.PrependReactor(verb, "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("backend failure near %s %s", token, stateTestSecretValue))
		})
		err := h.rt.Delete(ctx, id2)
		if err == nil {
			t.Errorf("Delete with a failing store %s: error = nil, want an error", verb)
		}
		note(err)
		h.cs.ReactionChain = h.cs.ReactionChain[1:]
	}

	all := strings.Join(surfaced, "\n") + "\n" + logs.String()
	for _, s := range secrets {
		if s == "" {
			t.Fatal("test setup: empty secret value")
		}
		if strings.Contains(all, s) {
			t.Errorf("credential material leaked into an error, log line or listing:\n%s", all)
		}
	}
}

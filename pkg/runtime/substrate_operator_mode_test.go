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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Operator mode (substrate v1 design §3.3, §3.7; AC 12): verify-or-fail
// ensureAtespace and the per-atespace List.

// atespaceRequestLog records the atespace of every ListActors request.
type atespaceRequestLog struct {
	mu        sync.Mutex
	atespaces []string
}

func (l *atespaceRequestLog) add(a string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.atespaces = append(l.atespaces, a)
}

func (l *atespaceRequestLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.atespaces...)
}

// runningActor is one RUNNING actor in atespace.
func runningActor(atespace, name, uid string) *ateapi.Actor {
	return &ateapi.Actor{
		Metadata: &ateapi.ResourceMetadata{Atespace: atespace, Name: name, Uid: uid},
		Status:   &ateapi.ActorStatus{State: ateapi.ActorState_ACTOR_STATE_RUNNING},
	}
}

// listActorsByAtespace serves ListActors from byAtespace (one page each),
// failing an atespace listed in errs with its error, and logs every
// requested atespace.
func listActorsByAtespace(log *atespaceRequestLog, byAtespace map[string][]*ateapi.Actor, errs map[string]error) func(*ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
	return func(req *ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
		log.add(req.GetAtespace())
		if err := errs[req.GetAtespace()]; err != nil {
			return nil, err
		}
		return &ateapi.ListActorsResponse{Actors: byAtespace[req.GetAtespace()]}, nil
	}
}

func TestSubstrateEnsureAtespace_VerifyOrFail(t *testing.T) {
	const atespace = "scion-0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name      string
		err       error
		wantTyped bool
		wantCode  codes.Code
	}{
		{"exists", nil, false, codes.OK},
		{"NotFound", status.Error(codes.NotFound, "no such atespace"), true, codes.NotFound},
		{"PermissionDenied", status.Error(codes.PermissionDenied, "denied"), true, codes.PermissionDenied},
		{"Unavailable", status.Error(codes.Unavailable, "down"), false, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			var got *ateapi.GetAtespaceRequest
			fc.getAtespace = func(req *ateapi.GetAtespaceRequest) (*ateapi.Atespace, error) {
				got = req
				if tc.err != nil {
					return nil, tc.err
				}
				return &ateapi.Atespace{}, nil
			}

			err := rt.ensureAtespace(context.Background(), atespace)

			if got.GetAtespace().GetName() != atespace || got.GetAtespace().GetAtespace() != "" {
				t.Errorf("GetAtespace request = %v, want ObjectRef{Name: %q}", got, atespace)
			}
			if calls := rec.list(); !reflect.DeepEqual(calls, []string{"GetAtespace"}) {
				t.Errorf("calls = %v, want only GetAtespace (never CreateAtespace)", calls)
			}
			if tc.err == nil {
				if err != nil {
					t.Fatalf("ensureAtespace() = %v, want nil", err)
				}
				return
			}
			var perr *SubstrateAtespaceProvisioningError
			isTyped := errors.As(err, &perr)
			if isTyped != tc.wantTyped || errors.Is(err, ErrSubstrateAtespaceNotProvisioned) != tc.wantTyped {
				t.Fatalf("ensureAtespace() = %v; typed = %v, want %v", err, isTyped, tc.wantTyped)
			}
			if !tc.wantTyped {
				if status.Code(errors.Unwrap(err)) != tc.wantCode {
					t.Errorf("ensureAtespace() = %v, want it to wrap %v", err, tc.wantCode)
				}
				return
			}
			if perr.Atespace != atespace || perr.Code != tc.wantCode {
				t.Errorf("provisioning error = %+v, want atespace %q code %v", perr, atespace, tc.wantCode)
			}
			want := "ask your operator to provision atespace \"" + atespace + "\" and grant the broker editor on it"
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err, want)
			}
		})
	}
}

// TestSubstrateRun_UnprovisionedAtespaceIsTypedErrorAndStops: Run's step 1
// returns the typed provisioning error as is (not flattened by redact) and
// makes no further control-plane call.
func TestSubstrateRun_UnprovisionedAtespaceIsTypedErrorAndStops(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			fc.getAtespace = func(*ateapi.GetAtespaceRequest) (*ateapi.Atespace, error) {
				return nil, status.Error(code, "x")
			}
			cfg := testSubstrateRunConfig()
			_, err := rt.Run(context.Background(), cfg)
			var perr *SubstrateAtespaceProvisioningError
			if !errors.As(err, &perr) || perr.Atespace != substrateAtespaceName(cfg.ProjectID) {
				t.Fatalf("Run() = %v, want *SubstrateAtespaceProvisioningError for the project's atespace", err)
			}
			if calls := rec.list(); !reflect.DeepEqual(calls, []string{"GetAtespace"}) {
				t.Errorf("calls = %v, want only GetAtespace", calls)
			}
		})
	}
}

// TestSubstrateList_PerAtespaceFromProjectSet: an unfiltered List reads the
// atespace of each served project, in ProjectSet order, and never the
// empty atespace.
func TestSubstrateList_PerAtespaceFromProjectSet(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	ps := newFakeProjectSet(testProjAID, testProjBID)
	rt.SetProjectSetForTest(ps)
	log := &atespaceRequestLog{}
	fc.listActors = listActorsByAtespace(log, map[string][]*ateapi.Actor{
		testAtespaceA: {runningActor(testAtespaceA, "projA--dev", "uid-a")},
		testAtespaceB: {runningActor(testAtespaceB, "projB--dev", "uid-b")},
	}, nil)

	agents, err := rt.List(context.Background(), map[string]string{"scion.agent": "true"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := log.get(), []string{testAtespaceA, testAtespaceB}; !reflect.DeepEqual(got, want) {
		t.Errorf("ListActors atespaces = %v, want %v (one per served project, none empty)", got, want)
	}
	var ids []string
	for _, a := range agents {
		ids = append(ids, a.ContainerID)
	}
	if want := []string{testAtespaceA + "/projA--dev", testAtespaceB + "/projB--dev"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("List() = %v, want %v", ids, want)
	}
	if ps.calls != 1 {
		t.Errorf("ServedProjects calls = %d, want 1", ps.calls)
	}
}

// TestSubstrateList_SingleProjectFromLabelFilter: a project ID in
// labelFilter lists that project's atespace only, without consulting the
// ProjectSet.
func TestSubstrateList_SingleProjectFromLabelFilter(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	ps := newFakeProjectSet(testProjAID, testProjBID)
	rt.SetProjectSetForTest(ps)
	log := &atespaceRequestLog{}
	fc.listActors = listActorsByAtespace(log, map[string][]*ateapi.Actor{
		testAtespaceA: {runningActor(testAtespaceA, "projA--dev", "uid-a")},
		testAtespaceB: {runningActor(testAtespaceB, "projB--dev", "uid-b")},
	}, nil)

	if _, err := rt.List(context.Background(), map[string]string{projectkeys.LabelProjectID: testProjBID}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := log.get(), []string{testAtespaceB}; !reflect.DeepEqual(got, want) {
		t.Errorf("ListActors atespaces = %v, want %v", got, want)
	}
	if ps.calls != 0 {
		t.Errorf("ServedProjects calls = %d, want 0 for a project-scoped List", ps.calls)
	}
}

// TestSubstrateList_RestartKeepsServedRecordlessActorVisible is the restart
// AC (design §3.3 "Post-restart outcome", AC 12): after a simulated broker
// restart with no in-memory agent records, an unfiltered List still
// returns a served project's record-less actor.
func TestSubstrateList_RestartKeepsServedRecordlessActorVisible(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	cfg := testSubstrateRunConfig()
	rt.SetProjectSetForTest(newFakeProjectSet(cfg.ProjectID))
	// ListActors serves what CreateActor created, per atespace.
	fc.listActors = func(req *ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		var out []*ateapi.Actor
		for _, a := range fc.createdActors {
			if a.GetMetadata().GetAtespace() == req.GetAtespace() {
				out = append(out, runningActor(a.GetMetadata().GetAtespace(), a.GetMetadata().GetName(), a.GetMetadata().GetUid()))
			}
		}
		return &ateapi.ListActorsResponse{Actors: out}, nil
	}

	id, err := rt.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// The restart: every in-memory record and control token is gone; the
	// actor stays in the cluster.
	wipeSubstrateAgentStateForRestart(t)

	for _, filter := range []map[string]string{nil, {"scion.agent": "true"}} {
		agents, err := rt.List(context.Background(), filter)
		if err != nil {
			t.Fatalf("List(%v) after restart error = %v", filter, err)
		}
		if len(agents) != 1 || agents[0].ContainerID != id {
			t.Fatalf("List(%v) after restart = %v, want the record-less actor %q", filter, agents, id)
		}
		if agents[0].ProjectID != "" {
			t.Errorf("record-less actor ProjectID = %q, want empty (no record)", agents[0].ProjectID)
		}
	}
}

// captureSlog sends slog's default logger to a buffer for the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestSubstrateList_UnprovisionedServedProjectSkippedAndCounted: in an
// unfiltered List, a served project whose atespace is NotFound or
// PermissionDenied is skipped with a logged, counted note; the others are
// listed.
func TestSubstrateList_UnprovisionedServedProjectSkippedAndCounted(t *testing.T) {
	const projCID = "cccccccccccc"
	atespaceC := substrateAtespaceName(projCID)
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			rt.SetProjectSetForTest(newFakeProjectSet(testProjAID, testProjBID, projCID))
			log := &atespaceRequestLog{}
			fc.listActors = listActorsByAtespace(log, map[string][]*ateapi.Actor{
				testAtespaceA: {runningActor(testAtespaceA, "projA--dev", "uid-a")},
				atespaceC:     {runningActor(atespaceC, "projC--dev", "uid-c")},
			}, map[string]error{testAtespaceB: status.Error(code, "x")})

			actors, skipped, err := rt.listOperatorModeActors(context.Background(), nil)
			if err != nil {
				t.Fatalf("listOperatorModeActors() error = %v", err)
			}
			if want := []skippedAtespace{{Atespace: testAtespaceB, Code: code}}; !reflect.DeepEqual(skipped, want) {
				t.Errorf("skipped = %v, want %v", skipped, want)
			}
			if len(actors) != 2 {
				t.Errorf("actors = %v, want projA's and projC's", actors)
			}

			buf := captureSlog(t)
			agents, err := rt.List(context.Background(), nil)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(agents) != 2 {
				t.Errorf("List() = %v, want 2 agents (A and C)", agents)
			}
			out := buf.String()
			if !strings.Contains(out, "skipped=1") || !strings.Contains(out, testAtespaceB) {
				t.Errorf("log = %q, want a note counting 1 skipped atespace and naming it", out)
			}
		})
	}
}

// TestSubstrateList_UnfilteredOtherErrorFailsList: any error other than
// NotFound or PermissionDenied on one atespace fails the unfiltered List.
func TestSubstrateList_UnfilteredOtherErrorFailsList(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	rt.SetProjectSetForTest(newFakeProjectSet(testProjAID, testProjBID))
	log := &atespaceRequestLog{}
	fc.listActors = listActorsByAtespace(log, map[string][]*ateapi.Actor{
		testAtespaceA: {runningActor(testAtespaceA, "projA--dev", "uid-a")},
	}, map[string]error{testAtespaceB: status.Error(codes.Unavailable, "down")})

	agents, err := rt.List(context.Background(), nil)
	if err == nil || errors.Is(err, ErrSubstrateAtespaceNotProvisioned) {
		t.Fatalf("List() err = %v, want a plain failure", err)
	}
	if agents != nil {
		t.Errorf("List() agents = %v, want nil alongside the error", agents)
	}
}

// TestSubstrateList_ProjectSetColdOrUnavailableIsError: an unfiltered List
// with no ProjectSet, or one that cannot answer, returns an error, never an
// empty result, and makes no ListActors call.
func TestSubstrateList_ProjectSetColdOrUnavailableIsError(t *testing.T) {
	cold := errors.New("served projects not loaded yet")
	for _, tc := range []struct {
		name string
		ps   ProjectSet
		want error
	}{
		{"unset", nil, errSubstrateProjectSetUnset},
		{"cold", &fakeProjectSet{err: cold}, cold},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &callRecorder{}
			rt, _, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			rt.SetProjectSetForTest(tc.ps)

			agents, err := rt.List(context.Background(), map[string]string{"scion.agent": "true"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("List() err = %v, want %v", err, tc.want)
			}
			if agents != nil {
				t.Errorf("List() agents = %v, want nil", agents)
			}
			if calls := rec.list(); len(calls) != 0 {
				t.Errorf("calls = %v, want none", calls)
			}
		})
	}
}

// TestSubstrateList_SingleProjectUnprovisionedIsTypedError: a project-scoped
// List for a project with no provisioned (or no readable) atespace returns
// the typed provisioning error.
func TestSubstrateList_SingleProjectUnprovisionedIsTypedError(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			log := &atespaceRequestLog{}
			fc.listActors = listActorsByAtespace(log, nil, map[string]error{testAtespaceA: status.Error(code, "x")})

			agents, err := rt.List(context.Background(), map[string]string{projectkeys.LabelProjectID: testProjAID})
			var perr *SubstrateAtespaceProvisioningError
			if !errors.As(err, &perr) || perr.Atespace != testAtespaceA || perr.Code != code {
				t.Fatalf("List() err = %v, want *SubstrateAtespaceProvisioningError{%s, %v}", err, testAtespaceA, code)
			}
			if agents != nil {
				t.Errorf("List() agents = %v, want nil", agents)
			}
		})
	}
}

// TestSubstrateList_MaxPagesAppliesPerAtespace: the page cap bounds each
// atespace, not the whole List.
func TestSubstrateList_MaxPagesAppliesPerAtespace(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	origCap := maxActorListPages
	maxActorListPages = 3
	t.Cleanup(func() { maxActorListPages = origCap })
	rt.SetProjectSetForTest(newFakeProjectSet(testProjAID, testProjBID))
	var mu sync.Mutex
	calls := 0
	// Each atespace answers exactly maxActorListPages pages.
	fc.listActors = func(req *ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		next := map[string]string{"": "p2", "p2": "p3", "p3": ""}[req.GetOptions().GetPageToken()]
		return &ateapi.ListActorsResponse{NextPageToken: next}, nil
	}

	if _, err := rt.List(context.Background(), nil); err != nil {
		t.Fatalf("List() error = %v, want none (3 pages per atespace is within the per-atespace cap)", err)
	}
	if calls != 6 {
		t.Errorf("ListActors calls = %d, want 6 (3 pages x 2 atespaces)", calls)
	}
}

// TestSubstrateRecordlessActors_FailsOnAnyError: unlike an unfiltered List,
// RecordlessActors never skips: NotFound and PermissionDenied fail it too.
func TestSubstrateRecordlessActors_FailsOnAnyError(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			fc.listActors = func(*ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
				return nil, status.Error(code, "x")
			}
			if _, actors, err := rt.RecordlessActors(context.Background(), testProjAID); err == nil || actors != nil {
				t.Fatalf("RecordlessActors() = %v, %v; want an error and no actors", actors, err)
			}
		})
	}
}

// TestSubstrateAtespaceProvisioningError_NeverCarriesServerText: the typed
// error skips redact in Run, so it must carry only the atespace name and
// the gRPC code. A distinctive server message planted in the NotFound or
// PermissionDenied status must not appear in the error text from
// ensureAtespace, Run, or a project-scoped List.
func TestSubstrateAtespaceProvisioningError_NeverCarriesServerText(t *testing.T) {
	const planted = "PLANTED-SERVER-TEXT-7f3a9c internal://ate-api.example:8443 principal=broker-sa"
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			fc.getAtespace = func(*ateapi.GetAtespaceRequest) (*ateapi.Atespace, error) {
				return nil, status.Error(code, planted)
			}
			fc.listActors = func(*ateapi.ListActorsRequest) (*ateapi.ListActorsResponse, error) {
				return nil, status.Error(code, planted)
			}
			_, runErr := rt.Run(context.Background(), testSubstrateRunConfig())
			_, listErr := rt.List(context.Background(), map[string]string{projectkeys.LabelProjectID: testProjAID})
			for name, err := range map[string]error{
				"ensureAtespace": rt.ensureAtespace(context.Background(), testAtespaceA),
				"Run":            runErr,
				"List":           listErr,
			} {
				if !errors.Is(err, ErrSubstrateAtespaceNotProvisioned) {
					t.Fatalf("%s: err = %v, want the typed provisioning error", name, err)
				}
				// The text, the unwrapped chain and the gRPC status a caller
				// can extract (errors.As / status.FromError) carry the code
				// only, never the server's message.
				st, ok := status.FromError(err)
				if !ok || st.Code() != code {
					t.Errorf("%s: status.FromError = %v, %v; want code %v", name, st, ok, code)
				}
				var texts []string
				texts = append(texts, err.Error(), st.Message())
				for e := errors.Unwrap(err); e != nil; e = errors.Unwrap(e) {
					texts = append(texts, e.Error())
				}
				var se interface{ GRPCStatus() *status.Status }
				if errors.As(err, &se) {
					texts = append(texts, se.GRPCStatus().Message())
				}
				for _, text := range texts {
					for _, part := range []string{"PLANTED", "7f3a9c", "ate-api.example", "principal="} {
						if strings.Contains(text, part) {
							t.Errorf("%s: %q carries server text %q", name, text, part)
						}
					}
				}
			}
		})
	}
}

// TestSubstrateList_NeverReturnsActorReportingAnotherAtespace: each
// atespace's listing keeps only actors that report that atespace, through
// List itself, unfiltered and project-scoped.
//
// Every atespace's response also carries actors that report another
// atespace. Unfiltered, each actor is listed once, from its own atespace,
// and an actor of an unserved atespace is never listed. Project-scoped (a
// project ID in labelFilter), List returns (a) no actor that reports
// another atespace, even one whose record names the requested project, and
// (b) no record-less actor, because a record-less actor carries no project
// label and never matches a project filter (List's doc comment).
func TestSubstrateList_NeverReturnsActorReportingAnotherAtespace(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	rt.SetProjectSetForTest(newFakeProjectSet(testProjAID, testProjBID))
	notServed := substrateAtespaceName("not-served")
	// ownA has a record for project A; orphanA and ownB have none;
	// strayInA reports atespace B but has a record for project A; unserved
	// is in an atespace no served project maps to.
	ownA := runningActor(testAtespaceA, "projA--dev", "uid-a")
	orphanA := runningActor(testAtespaceA, "projA--orphan", "uid-o")
	ownB := runningActor(testAtespaceB, "projB--dev", "uid-b")
	strayInA := runningActor(testAtespaceB, "projA--stray", "uid-s")
	unserved := runningActor(notServed, "other--dev", "uid-x")
	substrateAgentStateMu.Lock()
	substrateAgentRecords["uid-a"] = &substrateAgentRecord{Labels: map[string]string{"scion.name": "dev"}, Project: "projA", ProjectID: testProjAID}
	substrateAgentRecords["uid-s"] = &substrateAgentRecord{Labels: map[string]string{"scion.name": "stray"}, Project: "projA", ProjectID: testProjAID}
	substrateAgentStateMu.Unlock()
	log := &atespaceRequestLog{}
	fc.listActors = listActorsByAtespace(log, map[string][]*ateapi.Actor{
		testAtespaceA: {ownA, orphanA, strayInA, ownB, unserved},
		testAtespaceB: {ownB, ownA, unserved},
	}, nil)

	containerIDs := func(filter map[string]string) []string {
		t.Helper()
		agents, err := rt.List(context.Background(), filter)
		if err != nil {
			t.Fatalf("List(%v) error = %v", filter, err)
		}
		var ids []string
		for _, a := range agents {
			ids = append(ids, a.ContainerID)
		}
		return ids
	}

	// Unfiltered: each actor once, from its own atespace; strayInA is not
	// in atespace B's response, so it is not listed; the unserved one never.
	if got, want := containerIDs(nil), []string{testAtespaceA + "/projA--dev", testAtespaceA + "/projA--orphan", testAtespaceB + "/projB--dev"}; !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %v, want %v", got, want)
	}

	// Project-scoped, through List: only project A's record-having actor
	// from atespace A. (a) strayInA reports atespace B, so it is dropped
	// although its record names project A. (b) orphanA is record-less, so
	// the project filter does not match it.
	if got, want := containerIDs(map[string]string{projectkeys.LabelProjectID: testProjAID}), []string{testAtespaceA + "/projA--dev"}; !reflect.DeepEqual(got, want) {
		t.Errorf("List(project A) = %v, want %v", got, want)
	}

	// The raw actors read for project A: only those reporting atespace A.
	actors, skipped, err := rt.listOperatorModeActors(context.Background(), map[string]string{projectkeys.LabelProjectID: testProjAID})
	if err != nil || len(skipped) != 0 {
		t.Fatalf("listOperatorModeActors(project A) = %v, skipped %v, err %v", actors, skipped, err)
	}
	var raw []string
	for _, a := range actors {
		raw = append(raw, a.GetMetadata().GetAtespace()+"/"+a.GetMetadata().GetUid())
	}
	if want := []string{testAtespaceA + "/uid-a", testAtespaceA + "/uid-o"}; !reflect.DeepEqual(raw, want) {
		t.Errorf("listOperatorModeActors(project A) = %v, want %v", raw, want)
	}
}

// TestSubstrateList_UnreadableAtespaceMarksListIncomplete: an unfiltered
// List that skips a PermissionDenied atespace marks the caller's
// ListCompleteness incomplete (the atespace may hold live actors) and still
// returns the readable atespaces' actors. A NotFound skip hides nothing
// and leaves it complete, and so does a List with nothing skipped.
func TestSubstrateList_UnreadableAtespaceMarksListIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name           string
		errs           map[string]error
		wantIncomplete bool
	}{
		{"PermissionDenied", map[string]error{testAtespaceB: status.Error(codes.PermissionDenied, "x")}, true},
		{"NotFound", map[string]error{testAtespaceB: status.Error(codes.NotFound, "x")}, false},
		{"none skipped", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &callRecorder{}
			rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
			defer closeServer()
			rt.SetProjectSetForTest(newFakeProjectSet(testProjAID, testProjBID))
			log := &atespaceRequestLog{}
			fc.listActors = listActorsByAtespace(log, map[string][]*ateapi.Actor{
				testAtespaceA: {runningActor(testAtespaceA, "projA--dev", "uid-a")},
			}, tc.errs)

			c := &ListCompleteness{}
			agents, err := rt.List(WithListCompleteness(context.Background(), c), nil)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(agents) != 1 || agents[0].ContainerID != testAtespaceA+"/projA--dev" {
				t.Errorf("List() = %v, want projA's actor", agents)
			}
			if got, reason := c.Incomplete(); got != tc.wantIncomplete {
				t.Errorf("Incomplete() = %v (%q), want %v", got, reason, tc.wantIncomplete)
			}
		})
	}
}

// TestSubstrateInstallProjectSet_RefusesAnotherOwner: InstallProjectSet
// accepts a set on an empty runtime and the same set again, refuses a
// different set with ErrSubstrateRuntimeShared and keeps the first, and
// treats uncomparable values as different without panicking.
func TestSubstrateInstallProjectSet_RefusesAnotherOwner(t *testing.T) {
	rt := NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	first, second := newFakeProjectSet("p1"), newFakeProjectSet("p1")
	if err := rt.InstallProjectSet(first); err != nil {
		t.Fatalf("first install error = %v", err)
	}
	if err := rt.InstallProjectSet(first); err != nil {
		t.Fatalf("same-set re-install error = %v, want nil", err)
	}
	if err := rt.InstallProjectSet(second); !errors.Is(err, ErrSubstrateRuntimeShared) {
		t.Fatalf("other-set install error = %v, want ErrSubstrateRuntimeShared", err)
	}
	if got := rt.ProjectSetForTest(); got != ProjectSet(first) {
		t.Errorf("ProjectSet = %v, want the first set kept", got)
	}

	rt2 := NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	if err := rt2.InstallProjectSet(sliceProjectSet{"p1"}); err != nil {
		t.Fatalf("uncomparable first install error = %v", err)
	}
	if err := rt2.InstallProjectSet(sliceProjectSet{"p1"}); !errors.Is(err, ErrSubstrateRuntimeShared) {
		t.Errorf("uncomparable second install error = %v, want ErrSubstrateRuntimeShared", err)
	}
}

// sliceProjectSet is an uncomparable ProjectSet (a slice type).
type sliceProjectSet []string

func (s sliceProjectSet) ServedProjects(context.Context) ([]string, error) { return s, nil }

// ifaceFieldProjectSet is a comparable ProjectSet type (a non-pointer
// struct) whose interface field can hold an uncomparable value.
type ifaceFieldProjectSet struct {
	v any
}

func (ifaceFieldProjectSet) ServedProjects(context.Context) ([]string, error) { return nil, nil }

// TestSubstrateSameProjectSet_UncomparableValuesOfSameTypeRefused: two
// values of one comparable struct type whose interface fields both hold a
// slice. Interface == on them would reach the slices and panic, so
// sameProjectSet counts them as different without comparing, and
// InstallProjectSet refuses the second.
func TestSubstrateSameProjectSet_UncomparableValuesOfSameTypeRefused(t *testing.T) {
	first := ifaceFieldProjectSet{v: []string{"p1"}}
	second := ifaceFieldProjectSet{v: []string{"p1"}}
	noPanic := func(what string, f func()) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s panicked: %v", what, r)
			}
		}()
		f()
	}

	noPanic("sameProjectSet", func() {
		if sameProjectSet(first, second) {
			t.Error("sameProjectSet(two slice-holding values) = true, want false")
		}
	})

	rt := NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	if err := rt.InstallProjectSet(first); err != nil {
		t.Fatalf("first install error = %v", err)
	}
	noPanic("InstallProjectSet", func() {
		if err := rt.InstallProjectSet(second); !errors.Is(err, ErrSubstrateRuntimeShared) {
			t.Errorf("second install error = %v, want ErrSubstrateRuntimeShared", err)
		}
	})
}

// TestSubstrateList_SkipNoteLoggedOnChange: the skip note is logged at Warn
// when the skipped set changes, not on every List (the heartbeat lists
// every interval), and at Info once when it clears. A project-scoped List
// between two unfiltered Lists with the same skip does not reset it.
func TestSubstrateList_SkipNoteLoggedOnChange(t *testing.T) {
	rec := &callRecorder{}
	rt, fc, _, closeServer := newTestSubstrateHarness(t, rec)
	defer closeServer()
	rt.SetProjectSetForTest(newFakeProjectSet(testProjAID, testProjBID))
	errs := map[string]error{testAtespaceB: status.Error(codes.NotFound, "x")}
	log := &atespaceRequestLog{}
	fc.listActors = listActorsByAtespace(log, nil, errs)
	buf := captureSlog(t)
	warns := func() int { return strings.Count(buf.String(), "level=WARN") }

	for i := 0; i < 3; i++ {
		if _, err := rt.List(context.Background(), nil); err != nil {
			t.Fatalf("List() error = %v", err)
		}
	}
	if n := warns(); n != 1 {
		t.Fatalf("Warn lines after 3 Lists with the same skip = %d, want 1", n)
	}
	// Interleaved: a project-scoped List, then an unfiltered one with the
	// same skip. Neither logs: still 1 Warn and no Info.
	if _, err := rt.List(context.Background(), map[string]string{projectkeys.LabelProjectID: testProjAID}); err != nil {
		t.Fatalf("List(project A) error = %v", err)
	}
	if _, err := rt.List(context.Background(), nil); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if n, infos := warns(), strings.Count(buf.String(), "level=INFO"); n != 1 || infos != 0 {
		t.Fatalf("after an interleaved project-scoped List: Warn lines = %d, Info lines = %d; want 1 and 0", n, infos)
	}
	errs[testAtespaceA] = status.Error(codes.PermissionDenied, "x")
	if _, err := rt.List(context.Background(), nil); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if n := warns(); n != 2 {
		t.Errorf("Warn lines after the skipped set changed = %d, want 2", n)
	}
	delete(errs, testAtespaceA)
	delete(errs, testAtespaceB)
	if _, err := rt.List(context.Background(), nil); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !strings.Contains(buf.String(), "level=INFO") {
		t.Errorf("no Info line when the skips cleared: %q", buf.String())
	}
}

// TestSubstrateReleaseProjectSet_CompareAndClear: ReleaseProjectSet clears
// the installed set only when it is the given set, so a stopping broker
// instance never clears another instance's set; after a release another
// set can be installed.
func TestSubstrateReleaseProjectSet_CompareAndClear(t *testing.T) {
	rt := NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	owner, other := newFakeProjectSet("p1"), newFakeProjectSet("p2")
	if err := rt.InstallProjectSet(owner); err != nil {
		t.Fatalf("install error = %v", err)
	}
	rt.ReleaseProjectSet(other)
	if got := rt.ProjectSetForTest(); got != ProjectSet(owner) {
		t.Fatalf("after a release by a non-owner, ProjectSet = %v, want the owner's set kept", got)
	}
	rt.ReleaseProjectSet(owner)
	if got := rt.ProjectSetForTest(); got != nil {
		t.Fatalf("after the owner's release, ProjectSet = %v, want nil", got)
	}
	if err := rt.InstallProjectSet(other); err != nil {
		t.Fatalf("install after release error = %v, want nil", err)
	}
	rt.ReleaseProjectSet(sliceProjectSet{"p1"})
	if got := rt.ProjectSetForTest(); got != ProjectSet(other) {
		t.Errorf("after a release with an uncomparable set, ProjectSet = %v, want the installed set kept", got)
	}
}

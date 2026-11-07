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
	rt.SetProjectSet(ps)
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
	rt.SetProjectSet(ps)
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
	rt.SetProjectSet(newFakeProjectSet(cfg.ProjectID))
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
			rt.SetProjectSet(newFakeProjectSet(testProjAID, testProjBID, projCID))
			log := &atespaceRequestLog{}
			fc.listActors = listActorsByAtespace(log, map[string][]*ateapi.Actor{
				testAtespaceA: {runningActor(testAtespaceA, "projA--dev", "uid-a")},
				atespaceC:     {runningActor(atespaceC, "projC--dev", "uid-c")},
			}, map[string]error{testAtespaceB: status.Error(code, "x")})

			actors, skipped, err := rt.listOperatorModeActors(context.Background(), nil)
			if err != nil {
				t.Fatalf("listOperatorModeActors() error = %v", err)
			}
			if !reflect.DeepEqual(skipped, []string{testAtespaceB}) {
				t.Errorf("skipped = %v, want [%s]", skipped, testAtespaceB)
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
	rt.SetProjectSet(newFakeProjectSet(testProjAID, testProjBID))
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
			rt.SetProjectSet(tc.ps)

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
	rt.SetProjectSet(newFakeProjectSet(testProjAID, testProjBID))
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

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
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// fakeProjectLister is a hubclient RuntimeBrokers().ListProjects stand-in.
// Each call is recorded and announced on calls (buffered) so tests wait on
// the refresher goroutine without sleeping.
type fakeProjectLister struct {
	mu       sync.Mutex
	projects []string
	err      error
	brokerID []string
	calls    chan struct{}
}

func newFakeProjectLister(projects ...string) *fakeProjectLister {
	return &fakeProjectLister{projects: projects, calls: make(chan struct{}, 64)}
}

func (f *fakeProjectLister) ListProjects(_ context.Context, brokerID string) (*hubclient.ListBrokerProjectsResponse, error) {
	f.mu.Lock()
	f.brokerID = append(f.brokerID, brokerID)
	err := f.err
	var resp *hubclient.ListBrokerProjectsResponse
	if err == nil {
		resp = &hubclient.ListBrokerProjectsResponse{}
		for _, p := range f.projects {
			resp.Projects = append(resp.Projects, hubclient.BrokerProjectInfo{ProjectID: p})
		}
	}
	f.mu.Unlock()
	f.calls <- struct{}{}
	return resp, err
}

func (f *fakeProjectLister) set(err error, projects ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
	f.projects = projects
}

func (f *fakeProjectLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.brokerID)
}

// waitCall fails the test unless one ListProjects call arrives. The
// deadline only bounds a broken test; passing runs never wait on it.
func (f *fakeProjectLister) waitCall(t *testing.T, what string) {
	t.Helper()
	select {
	case <-f.calls:
	case <-time.After(10 * time.Second):
		t.Fatalf("no ListProjects call for %s", what)
	}
}

// testServedProjects returns a servedProjects over the given sources with
// a manual clock and a manual ticker channel.
func testServedProjects(sources ...servedProjectsSource) (*servedProjects, chan time.Time, *time.Time) {
	p := newServedProjects(func() []servedProjectsSource { return sources }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	tick := make(chan time.Time)
	p.newTicker = func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} }
	return p, tick, &now
}

func TestServedProjects_ColdBeforeFirstLoad(t *testing.T) {
	f := newFakeProjectLister("p1")
	p, _, _ := testServedProjects(servedProjectsSource{name: "hub", brokerID: "b1", lister: f})
	ids, err := p.ServedProjects(context.Background())
	if !errors.Is(err, errServedProjectsCold) {
		t.Fatalf("cold set: err = %v, want errServedProjectsCold", err)
	}
	if ids != nil {
		t.Fatalf("cold set returned %v, want nil", ids)
	}
}

func TestServedProjects_NoHubConnectionIsCold(t *testing.T) {
	p, _, _ := testServedProjects()
	if _, err := p.ServedProjects(context.Background()); !errors.Is(err, errServedProjectsCold) {
		t.Fatalf("no sources: err = %v, want errServedProjectsCold", err)
	}
}

func TestServedProjects_HubUnreachableWithNoPriorLoadIsCold(t *testing.T) {
	f := newFakeProjectLister()
	f.set(errors.New("hub unreachable"))
	p, _, _ := testServedProjects(servedProjectsSource{name: "hub", brokerID: "b1", lister: f})
	p.reload(context.Background())
	if _, err := p.ServedProjects(context.Background()); !errors.Is(err, errServedProjectsCold) {
		t.Fatalf("unreachable, no prior load: err = %v, want errServedProjectsCold", err)
	}
}

// TestServedProjects_LoadsOnStartReconnectAndEveryFiveMinutes covers the
// three refresh triggers of design §3.3 through run: the load at start, a
// reload request (the hub reconnect hook), and the 5-minute tick.
func TestServedProjects_LoadsOnStartReconnectAndEveryFiveMinutes(t *testing.T) {
	f := newFakeProjectLister("p1")
	p, tick, _ := testServedProjects(servedProjectsSource{name: "hub", brokerID: "b1", lister: f})
	var interval time.Duration
	p.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		interval = d
		return tick, func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()

	f.waitCall(t, "start")
	got, err := p.ServedProjects(ctx)
	if err != nil || !reflect.DeepEqual(got, []string{"p1"}) {
		t.Fatalf("after start: %v, %v; want [p1], nil", got, err)
	}

	f.set(nil, "p1", "p2")
	p.requestReload()
	f.waitCall(t, "reconnect")
	tick <- time.Time{} // blocks until run is back in its select, so the reload above has stored
	f.waitCall(t, "tick")
	if interval != 5*time.Minute {
		t.Fatalf("refresh interval = %v, want 5m", interval)
	}
	f.set(nil, "p3")
	tick <- time.Time{}
	f.waitCall(t, "second tick")
	cancel()
	<-done
	got, err = p.ServedProjects(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"p3"}) {
		t.Fatalf("after ticks: %v, %v; want [p3], nil", got, err)
	}
	if n := f.callCount(); n != 4 {
		t.Fatalf("ListProjects calls = %d, want 4 (start, reconnect, 2 ticks)", n)
	}
	for _, id := range f.brokerID {
		if id != "b1" {
			t.Fatalf("ListProjects broker ID = %q, want b1", id)
		}
	}
}

func TestServedProjects_FailedRefreshKeepsLastGood(t *testing.T) {
	f := newFakeProjectLister("p2", "p1")
	p, _, now := testServedProjects(servedProjectsSource{name: "hub", brokerID: "b1", lister: f})
	var logs bytes.Buffer
	p.log = slog.New(slog.NewTextHandler(&logs, nil))
	p.reload(context.Background())
	f.set(errors.New("hub unreachable"))
	*now = now.Add(12 * time.Minute)
	p.reload(context.Background())
	got, err := p.ServedProjects(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"p1", "p2"}) {
		t.Fatalf("last good: %v, %v; want [p1 p2], nil", got, err)
	}
	snap := p.snaps[servedProjectsSource{name: "hub", brokerID: "b1"}.key()]
	if snap.lastErr == nil || now.Sub(snap.loadedAt) != 12*time.Minute {
		t.Fatalf("snapshot = %+v, want lastErr set and age 12m", snap)
	}
	if out := logs.String(); !strings.Contains(out, "age=12m0s") || !strings.Contains(out, "hub=hub") {
		t.Errorf("log = %q, want the last-good warning with hub=hub and age=12m0s", out)
	}
	// A second call while the source keeps failing does not warn again;
	// a successful load ends the failing stretch with one Info line.
	lastGood := func() int { return strings.Count(logs.String(), "Using the last good served-project set") }
	if _, err := p.ServedProjects(context.Background()); err != nil {
		t.Fatalf("second ServedProjects() error = %v", err)
	}
	if n := lastGood(); n != 1 {
		t.Errorf("last-good Warn lines after 2 consecutive calls while failing = %d, want 1", n)
	}
	f.set(nil, "p3")
	p.reload(context.Background())
	if !strings.Contains(logs.String(), "Served projects loaded from the hub again") {
		t.Errorf("no recovery Info line after a successful load: %q", logs.String())
	}
}

func TestServedProjects_UnionAndColdAcrossHubConnections(t *testing.T) {
	a := newFakeProjectLister("p1", "p2")
	b := newFakeProjectLister("p2", "p3")
	b.set(errors.New("hub b unreachable"))
	p, _, _ := testServedProjects(
		servedProjectsSource{name: "a", brokerID: "ba", lister: a},
		servedProjectsSource{name: "b", brokerID: "bb", lister: b},
	)
	p.reload(context.Background())
	if _, err := p.ServedProjects(context.Background()); !errors.Is(err, errServedProjectsCold) {
		t.Fatalf("one connection never loaded: err = %v, want errServedProjectsCold", err)
	}
	b.set(nil, "p2", "p3")
	p.reload(context.Background())
	got, err := p.ServedProjects(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"p1", "p2", "p3"}) {
		t.Fatalf("union: %v, %v; want [p1 p2 p3], nil", got, err)
	}
	// Each connection is read with its own broker ID and keeps its own
	// snapshot: one connection's load never writes another's.
	for _, c := range []struct {
		f    *fakeProjectLister
		id   string
		want []string
	}{{a, "ba", []string{"p1", "p2"}}, {b, "bb", []string{"p2", "p3"}}} {
		for _, id := range c.f.brokerID {
			if id != c.id {
				t.Errorf("ListProjects broker ID = %q, want %q", id, c.id)
			}
		}
	}
	if snap := p.snaps[servedProjectsSource{name: "a", brokerID: "ba"}.key()]; !reflect.DeepEqual(snap.projectIDs, []string{"p1", "p2"}) {
		t.Errorf("connection a snapshot = %v, want [p1 p2]", snap.projectIDs)
	}
	if snap := p.snaps[servedProjectsSource{name: "b", brokerID: "bb"}.key()]; !reflect.DeepEqual(snap.projectIDs, []string{"p2", "p3"}) {
		t.Errorf("connection b snapshot = %v, want [p2 p3]", snap.projectIDs)
	}
}

// TestServedProjects_RemovedConnectionLeavesUnionAndColdCheck: a hub
// connection that is removed or stopped is not a source, so it neither
// blocks List as cold nor contributes its last projects: a deconfigured
// hub must not hold the set cold forever.
func TestServedProjects_RemovedConnectionLeavesUnionAndColdCheck(t *testing.T) {
	a := newFakeProjectLister("p1")
	b := newFakeProjectLister("p9")
	srcA := servedProjectsSource{name: "a", brokerID: "ba", lister: a}
	srcB := servedProjectsSource{name: "b", brokerID: "bb", lister: b}
	current := []servedProjectsSource{srcA, srcB}
	p, _, _ := testServedProjects()
	p.sources = func() []servedProjectsSource { return current }

	b.set(errors.New("hub b unreachable"))
	p.reload(context.Background())
	if _, err := p.ServedProjects(context.Background()); !errors.Is(err, errServedProjectsCold) {
		t.Fatalf("b never loaded: err = %v, want errServedProjectsCold", err)
	}
	current = []servedProjectsSource{srcA} // b removed or stopped
	got, err := p.ServedProjects(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"p1"}) {
		t.Fatalf("b removed: %v, %v; want [p1], nil", got, err)
	}

	b.set(nil, "p9")
	current = []servedProjectsSource{srcA, srcB}
	p.reload(context.Background())
	current = []servedProjectsSource{srcA} // b loaded once, then removed
	got, err = p.ServedProjects(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"p1"}) {
		t.Fatalf("loaded b removed: %v, %v; want [p1] without p9, nil", got, err)
	}
}

// TestServedProjects_ColdErrorNamesConnectionOnly: the cold error names the
// hub connection only, and carries nothing from the failed load (no URL,
// credential or token).
func TestServedProjects_ColdErrorNamesConnectionOnly(t *testing.T) {
	f := newFakeProjectLister()
	f.set(errors.New("dial https://hub.example.invalid: refused"))
	p, _, _ := testServedProjects(servedProjectsSource{name: "prod", brokerID: "b1", lister: f})
	p.reload(context.Background())
	_, err := p.ServedProjects(context.Background())
	if err == nil {
		t.Fatal("want a cold error")
	}
	if got, want := err.Error(), `served projects not loaded from the hub yet (hub "prod")`; got != want {
		t.Fatalf("cold error = %q, want %q", got, want)
	}
}

// sourcesHubClient is a hubclient.Client whose only used method is
// RuntimeBrokers; any other call panics on the nil embedded interface.
type sourcesHubClient struct {
	hubclient.Client
	rb hubclient.RuntimeBrokerService
}

func (c sourcesHubClient) RuntimeBrokers() hubclient.RuntimeBrokerService { return c.rb }

// TestServedProjectsSources_OnlyStartedOpenConnections: a hub connection is
// a source only while it is started (heartbeat or control channel running)
// and not closed, with a hub client and a broker ID; a removed or stopped
// connection drops out of the union and the cold check.
func TestServedProjectsSources_OnlyStartedOpenConnections(t *testing.T) {
	rt := runtime.NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	mgr := agent.NewManager(rt)
	t.Cleanup(mgr.Close)
	srv := New(testServerConfig(), mgr, rt, config.RuntimeBuildRecord{})
	client := sourcesHubClient{}
	add := func(name, brokerID string, started, closed bool, c hubclient.Client) {
		hc := &HubConnection{Name: name, BrokerID: brokerID, HubClient: c, closed: closed}
		if started {
			hc.Heartbeat = &HeartbeatService{}
		}
		srv.hubMu.Lock()
		srv.hubConnections[name] = hc
		srv.hubMu.Unlock()
	}
	add("b-started", "bb", true, false, client)
	add("a-started", "ba", true, false, client)
	add("not-started", "bn", false, false, client)
	add("closed", "bc", true, true, client)
	add("no-client", "bx", true, false, nil)
	add("no-broker-id", "", true, false, client)

	var got []string
	for _, s := range srv.servedProjectsSources() {
		got = append(got, s.name+"/"+s.brokerID)
	}
	if want := []string{"a-started/ba", "b-started/bb"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}

// TestServedProjects_LastGoodWarnPrunedWithRemovedConnection: a reload
// drops the last-good warning state of a connection that is no longer a
// source, so the connection added again starts a fresh failing stretch:
// its first successful load logs no recovery Info, and its next failure
// warns again.
func TestServedProjects_LastGoodWarnPrunedWithRemovedConnection(t *testing.T) {
	f := newFakeProjectLister("p1")
	src := servedProjectsSource{name: "hub", brokerID: "b1", lister: f}
	current := []servedProjectsSource{src}
	p, _, _ := testServedProjects()
	p.sources = func() []servedProjectsSource { return current }
	var logs bytes.Buffer
	p.log = slog.New(slog.NewTextHandler(&logs, nil))
	count := func(msg string) int { return strings.Count(logs.String(), msg) }
	const lastGood = "Using the last good served-project set"
	const recovered = "Served projects loaded from the hub again"

	p.reload(context.Background())
	f.set(errors.New("hub unreachable"))
	p.reload(context.Background())
	if _, err := p.ServedProjects(context.Background()); err != nil {
		t.Fatalf("ServedProjects() while failing error = %v", err)
	}
	if n := count(lastGood); n != 1 {
		t.Fatalf("last-good Warn lines after the first failure = %d, want 1", n)
	}

	current = nil
	p.reload(context.Background())
	if _, ok := p.lastGoodWarned[src.key()]; ok {
		t.Fatal("removed connection's last-good warning state kept after reload")
	}

	current = []servedProjectsSource{src}
	f.set(nil, "p1")
	p.reload(context.Background())
	if n := count(recovered); n != 0 {
		t.Errorf("recovery Info lines for a re-added connection's first load = %d, want 0", n)
	}
	f.set(errors.New("hub unreachable"))
	p.reload(context.Background())
	if _, err := p.ServedProjects(context.Background()); err != nil {
		t.Fatalf("ServedProjects() while failing again error = %v", err)
	}
	if n := count(lastGood); n != 2 {
		t.Errorf("last-good Warn lines = %d, want 2 (one per failing stretch)", n)
	}
}

// ifaceFieldRuntime is a comparable receiver type whose interface field can
// hold an uncomparable value. The embedded Runtime is never called.
type ifaceFieldRuntime struct {
	runtime.Runtime
	v any
}

func (ifaceFieldRuntime) InstallProjectSet(runtime.ProjectSet) error { return nil }
func (ifaceFieldRuntime) ReleaseProjectSet(runtime.ProjectSet)       {}

// TestServedProjects_RecordReceiverNeverPanicsOnUncomparableValue: the
// record-once check never compares a receiver holding an uncomparable value.
// Interface == panics when both operands hold an uncomparable value of the
// same dynamic type (two slice holders meet); holders of different dynamic
// types compare false. A receiver holding an uncomparable value is recorded
// on every install, and a comparable receiver installed twice is recorded
// once.
func TestServedProjects_RecordReceiverNeverPanicsOnUncomparableValue(t *testing.T) {
	withSlice := ifaceFieldRuntime{v: []int{1}}
	withSlice2 := ifaceFieldRuntime{v: []int{2}}
	withInt := ifaceFieldRuntime{v: 1}
	for _, tc := range []struct {
		name  string
		order []ifaceFieldRuntime
		want  int
	}{
		{"slice then int", []ifaceFieldRuntime{withSlice, withInt, withInt}, 2},
		{"int then slice", []ifaceFieldRuntime{withInt, withSlice, withInt}, 2},
		{"slice then slice", []ifaceFieldRuntime{withSlice, withSlice2, withInt, withInt}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _ := testServedProjects()
			s := &Server{servedProjects: p}
			for i, rt := range tc.order {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("install %d panicked: %v", i, r)
						}
					}()
					if err := s.installProjectSet(rt); err != nil {
						t.Fatalf("install %d error = %v", i, err)
					}
				}()
			}
			if n := len(s.projectSetInstalled); n != tc.want {
				t.Fatalf("recorded receivers = %d, want %d (each slice-holder install, the int holder once)", n, tc.want)
			}
		})
	}
}

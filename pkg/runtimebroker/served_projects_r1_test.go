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
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/runtime/substrate"
	ateapi "github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestHeartbeat_UnreadableAtespaceMarksTargetIncomplete: a served atespace
// that answers PermissionDenied may hold live actors, so the heartbeat
// reports the substrate target incomplete (the hub then does not reconcile
// that project's agents as missing), while the actors of the readable
// atespaces are still reported. A NotFound atespace hides nothing and the
// target stays complete.
func TestHeartbeat_UnreadableAtespaceMarksTargetIncomplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	atespaceA := runtime.SubstrateAtespaceNameForTest("aaaaaaaaaaaa")
	atespaceB := runtime.SubstrateAtespaceNameForTest("bbbbbbbbbbbb")
	for _, tc := range []struct {
		code         codes.Code
		wantComplete bool
	}{
		{codes.PermissionDenied, false},
		{codes.NotFound, true},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			t.Cleanup(runtime.WipeSubstrateAgentStateForTest())
			fc := newFakeSubstrateControlClient(&substrateEgressRecorder{})
			fc.putActor(atespaceA, "projA--dev", "uid-hb-a")
			code := tc.code
			fc.listActorsErrFor = func(in *ateapi.ListActorsRequest) error {
				if in.GetAtespace() == atespaceB {
					return status.Error(code, "x")
				}
				return nil
			}
			rt := runtime.NewSubstrateRuntimeForTest(fc, substrate.NewRouterClient(""), nil, config.V1SubstrateConfig{})
			rt.SetProjectSetForTest(staticProjectSet{"aaaaaaaaaaaa", "bbbbbbbbbbbb"})
			mgr := agent.NewManager(rt)
			t.Cleanup(mgr.Close)
			hb := NewHeartbeatService(nil, "test-broker-id", time.Minute, mgr, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

			projects, inv := hb.gatherProjectAgents(context.Background(), nil)

			if len(inv.Targets) != 1 || inv.Targets[0].Complete != tc.wantComplete {
				t.Fatalf("inventory targets = %+v, want one target with Complete=%v", inv.Targets, tc.wantComplete)
			}
			found := false
			for _, p := range projects {
				for _, a := range p.Agents {
					if a.Slug == "projA--dev" {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("heartbeat projects = %+v, want the readable atespace's actor projA--dev reported", projects)
			}
		})
	}
}

// TestServedProjects_SharedSubstrateRuntimeRefusedForSecondInstance: two
// broker instances in one process that resolve the same substrate config
// share one memoized runtime. The second instance's install is refused with
// the typed error, it refuses to start, and it cannot register the runtime
// either; the first keeps its set. The first instance installing its own
// set again is accepted.
func TestServedProjects_SharedSubstrateRuntimeRefusedForSecondInstance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := runtime.NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	mgr1 := agent.NewManager(rt)
	t.Cleanup(mgr1.Close)
	cfg1 := testServerConfig()
	cfg1.BrokerID = "broker-one"
	srv1 := New(cfg1, mgr1, rt, config.RuntimeBuildRecord{})

	mgr2 := agent.NewManager(rt)
	t.Cleanup(mgr2.Close)
	cfg2 := testServerConfig()
	cfg2.BrokerID = "broker-two"
	srv2 := New(cfg2, mgr2, rt, config.RuntimeBuildRecord{})

	if srv1.projectSetSetupErr != nil {
		t.Fatalf("first instance setup error = %v, want nil", srv1.projectSetSetupErr)
	}
	if !errors.Is(srv2.projectSetSetupErr, runtime.ErrSubstrateRuntimeShared) {
		t.Fatalf("second instance setup error = %v, want ErrSubstrateRuntimeShared", srv2.projectSetSetupErr)
	}
	if got := rt.ProjectSetForTest(); got != srv1.servedProjects {
		t.Errorf("runtime ProjectSet = %v, want the first instance's set (kept)", got)
	}
	if err := srv2.StartServices(context.Background()); !errors.Is(err, runtime.ErrSubstrateRuntimeShared) {
		t.Errorf("second instance StartServices() = %v, want ErrSubstrateRuntimeShared (refuses to start)", err)
	}
	if _, err := srv2.newAuxiliaryRuntime(rt, mgr2, config.RuntimeBuildRecord{}); !errors.Is(err, runtime.ErrSubstrateRuntimeShared) {
		t.Errorf("second instance newAuxiliaryRuntime() = %v, want ErrSubstrateRuntimeShared", err)
	}
	if _, err := srv1.newAuxiliaryRuntime(rt, mgr1, config.RuntimeBuildRecord{}); err != nil {
		t.Errorf("first instance re-install error = %v, want nil", err)
	}
	if got := rt.ProjectSetForTest(); got != srv1.servedProjects {
		t.Errorf("runtime ProjectSet after the refused installs = %v, want the first instance's set", got)
	}
}

// fakeRuntimeBrokers serves ListProjects from a fakeProjectLister; any other
// RuntimeBrokerService call panics on the nil embedded interface.
type fakeRuntimeBrokers struct {
	hubclient.RuntimeBrokerService
	lister *fakeProjectLister
}

func (f fakeRuntimeBrokers) ListProjects(ctx context.Context, brokerID string) (*hubclient.ListBrokerProjectsResponse, error) {
	return f.lister.ListProjects(ctx, brokerID)
}

// TestHubConnection_ControlChannelConnectReloadsServedProjects: the control
// channel's connect callback (onControlChannelState, the production
// callback) causes a ListProjects for the broker's served-project set; a
// disconnect requests nothing.
func TestHubConnection_ControlChannelConnectReloadsServedProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := runtime.NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	mgr := agent.NewManager(rt)
	t.Cleanup(mgr.Close)
	srv := New(testServerConfig(), mgr, rt, config.RuntimeBuildRecord{})
	lister := newFakeProjectLister("p1")
	hc := &HubConnection{Name: "hub", BrokerID: "b1", HubClient: sourcesHubClient{rb: fakeRuntimeBrokers{lister: lister}}, Heartbeat: &HeartbeatService{}}
	srv.hubMu.Lock()
	srv.hubConnections["hub"] = hc
	srv.hubMu.Unlock()
	tick := make(chan time.Time)
	srv.servedProjects.newTicker = func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} }
	srv.startServedProjectsRefresh(context.Background())
	t.Cleanup(srv.stopServedProjectsRefresh)

	lister.waitCall(t, "start")
	hc.onControlChannelState(srv, true)
	lister.waitCall(t, "connect")
	hc.mu.RLock()
	st := hc.Status
	hc.mu.RUnlock()
	if st != ConnectionStatusConnected {
		t.Errorf("status after connect = %q, want %q", st, ConnectionStatusConnected)
	}

	hc.onControlChannelState(srv, false)
	hc.mu.RLock()
	st = hc.Status
	hc.mu.RUnlock()
	if st != ConnectionStatusReconnecting {
		t.Errorf("status after disconnect = %q, want %q", st, ConnectionStatusReconnecting)
	}
	if n := len(srv.servedProjects.reloadCh); n != 0 {
		t.Errorf("reload requests pending after disconnect = %d, want 0", n)
	}
}

// TestServedProjects_ShutdownStopsRefresher: stopServedProjectsRefresh (run
// by Shutdown) makes the refresher goroutine return, so a refused or
// stopped instance leaks no goroutine or ticker.
func TestServedProjects_ShutdownStopsRefresher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := runtime.NewSubstrateRuntimeForTest(nil, nil, nil, config.V1SubstrateConfig{})
	mgr := agent.NewManager(rt)
	t.Cleanup(mgr.Close)
	srv := New(testServerConfig(), mgr, rt, config.RuntimeBuildRecord{})
	tick := make(chan time.Time)
	srv.servedProjects.newTicker = func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} }
	srv.startServedProjectsRefresh(context.Background())

	stopped := make(chan struct{})
	go func() {
		srv.stopServedProjectsRefresh()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stopServedProjectsRefresh did not return")
	}
	srv.mu.RLock()
	done := srv.servedProjectsDone
	srv.mu.RUnlock()
	select {
	case <-done:
	default:
		t.Fatal("refresher still running after stop")
	}
}

// funcCalls returns the names of the methods and functions called inside
// the named function (and receiver type, when recv is set) of file.
func funcCalls(t *testing.T, file, recv, name string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", file), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	calls := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != name {
			continue
		}
		if recv != "" {
			if fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); !ok || id.Name != recv {
				continue
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				switch f := c.Fun.(type) {
				case *ast.SelectorExpr:
					calls[f.Sel.Name] = true
				case *ast.Ident:
					calls[f.Name] = true
				}
			}
			return true
		})
	}
	return calls
}

// TestServedProjects_RefreshWiredIntoStartReconnectAndShutdown guards the
// production wiring the unit tests cannot reach: startServices starts the
// refresher, the hub connection's control-channel callback goes through
// onControlChannelState (which requests a reload), and Shutdown stops it.
func TestServedProjects_RefreshWiredIntoStartReconnectAndShutdown(t *testing.T) {
	if !funcCalls(t, "server.go", "Server", "startServices")["startServedProjectsRefresh"] {
		t.Error("startServices does not call startServedProjectsRefresh")
	}
	if !funcCalls(t, "server.go", "Server", "Shutdown")["stopServedProjectsRefresh"] {
		t.Error("Shutdown does not call stopServedProjectsRefresh")
	}
	if !funcCalls(t, "hub_connection.go", "HubConnection", "start")["onControlChannelState"] {
		t.Error("HubConnection.start does not route the control channel state through onControlChannelState")
	}
	if !funcCalls(t, "hub_connection.go", "HubConnection", "onControlChannelState")["requestReload"] {
		t.Error("onControlChannelState does not request a served-project reload")
	}
}

// TestServedProjects_ReloadPrunesSnapshotsOfRemovedConnections: a reload
// drops the snapshot of a connection that is no longer a source, so a
// connection removed and added again starts cold instead of answering from
// its old snapshot.
func TestServedProjects_ReloadPrunesSnapshotsOfRemovedConnections(t *testing.T) {
	a := newFakeProjectLister("p1")
	b := newFakeProjectLister("p2")
	srcA := servedProjectsSource{name: "a", brokerID: "ba", lister: a}
	srcB := servedProjectsSource{name: "b", brokerID: "bb", lister: b}
	current := []servedProjectsSource{srcA, srcB}
	p, _, _ := testServedProjects()
	p.sources = func() []servedProjectsSource { return current }

	p.reload(context.Background())
	current = []servedProjectsSource{srcA}
	p.reload(context.Background())
	if _, ok := p.snaps[srcB.key()]; ok {
		t.Fatal("removed connection's snapshot kept after reload")
	}

	b.set(errors.New("hub b unreachable"))
	current = []servedProjectsSource{srcA, srcB}
	p.reload(context.Background())
	if _, err := p.ServedProjects(context.Background()); !errors.Is(err, errServedProjectsCold) {
		t.Fatalf("re-added connection that has not loaded: err = %v, want errServedProjectsCold (no stale snapshot)", err)
	}
}

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
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/brokercontrol"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	scionrt "github.com/GoogleCloudPlatform/scion/pkg/runtime"
)

// controlPathTestHome points $HOME at a temporary home with a Scion global
// directory and returns the global directory and an unrelated directory.
func controlPathTestHome(t *testing.T) (global, outside string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	global = filepath.Join(home, ".scion")
	outside = filepath.Join(root, "data")
	for _, d := range []string{global, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return global, outside
}

func decodeErrorCode(t *testing.T, body io.Reader) string {
	t.Helper()
	var resp ErrorResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return resp.Error.Code
}

func TestCreateAgent_RefusesVolumesReachingControlPaths(t *testing.T) {
	global, _ := controlPathTestHome(t)
	for _, body := range []string{
		`{"name": "a1", "config": {"volumes": [{"source": "~/.scion", "target": "/x", "read_only": true}]}}`,
		`{"name": "a1", "inlineConfig": {"volumes": [{"source": "` + filepath.ToSlash(filepath.Join(global, "settings.yaml")) + `", "target": "/x"}]}}`,
	} {
		srv := newTestServer(t)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		if code := decodeErrorCode(t, w.Body); code != brokercontrol.ErrorCode {
			t.Fatalf("code = %q, want %q", code, brokercontrol.ErrorCode)
		}
	}
}

func TestStartAgent_RefusesInlineVolumesReachingControlPaths(t *testing.T) {
	controlPathTestHome(t)
	mgr := &mockManager{agents: []api.AgentInfo{{ID: "container-1", Name: "test-agent-1", Phase: "stopped"}}}
	srv := newTestServerWithManager(t, mgr)
	body := `{"inlineConfig": {"volumes": [{"source": "~", "target": "/x"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/test-agent-1/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if code := decodeErrorCode(t, w.Body); code != brokercontrol.ErrorCode {
		t.Fatalf("code = %q, want %q", code, brokercontrol.ErrorCode)
	}
	if mgr.startCalls != 0 {
		t.Fatalf("Start was called %d times after a refused volume", mgr.startCalls)
	}
}

func TestSqliteDatabaseFile(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "hub.db")
	cases := []struct {
		db   config.DatabaseConfig
		want string
	}{
		{config.DatabaseConfig{Driver: "sqlite", URL: abs}, abs},
		{config.DatabaseConfig{Driver: "sqlite", URL: "file:" + abs + "?_busy_timeout=5000"}, abs},
		{config.DatabaseConfig{Driver: "sqlite", URL: ":memory:"}, ""},
		{config.DatabaseConfig{Driver: "sqlite", URL: "file::memory:?cache=shared"}, ""},
		{config.DatabaseConfig{Driver: "postgres", URL: "postgres://h/db"}, ""},
	}
	for _, tc := range cases {
		if got := sqliteDatabaseFile(tc.db); got != tc.want {
			t.Errorf("sqliteDatabaseFile(%+v) = %q, want %q", tc.db, got, tc.want)
		}
	}
}

func TestRegisterControlPaths_StateDirIsProtected(t *testing.T) {
	_, outside := controlPathTestHome(t)
	state := filepath.Join(outside, "state", "b1")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	registerControlPaths("test-broker", state, "")
	t.Cleanup(func() { brokercontrol.SetExtraPaths(controlPathsOwner("test-broker"), nil) })
	err := checkRequestVolumes([]api.VolumeMount{{Source: filepath.Join(outside, "state"), Target: "/x"}})
	if !errors.Is(err, brokercontrol.ErrControlPathMount) {
		t.Fatalf("err = %v, want ErrControlPathMount", err)
	}
}

// sweepRuntime is a mock runtime with scripted agents and inspect results.
type sweepRuntime struct {
	*scionrt.MockRuntime
	agents  []api.AgentInfo
	inspect map[string][]sweepInspect
	calls   map[string]int
	stopped []string
}

type sweepInspect struct {
	binds []scionrt.BindMount
	err   error
}

func newSweepRuntime(agents []api.AgentInfo, inspect map[string][]sweepInspect) *sweepRuntime {
	r := &sweepRuntime{agents: agents, inspect: inspect, calls: map[string]int{}}
	r.MockRuntime = &scionrt.MockRuntime{
		ListFunc: func(context.Context, map[string]string) ([]api.AgentInfo, error) { return r.agents, nil },
		StopFunc: func(_ context.Context, ref scionrt.RunRef) error {
			r.stopped = append(r.stopped, ref.ID)
			for i := range r.agents {
				if r.agents[i].ContainerID == ref.ID {
					r.agents[i].Phase = "stopped"
				}
			}
			return nil
		},
	}
	return r
}

func (r *sweepRuntime) InspectBindMounts(_ context.Context, id string) ([]scionrt.BindMount, error) {
	res := r.inspect[id]
	i := min(r.calls[id], len(res)-1)
	r.calls[id]++
	return res[i].binds, res[i].err
}

func newTestSweeper(rt scionrt.Runtime) (*controlPathSweeper, *[]string) {
	sw := newControlPathSweeper(rt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var marked []string
	sw.markError = func(a agentRef) { marked = append(marked, a.name) }
	return sw, &marked
}

func TestControlPathSweep(t *testing.T) {
	global, outside := controlPathTestHome(t)
	scion := map[string]string{"scion.name": "x"}
	agents := []api.AgentInfo{
		{ContainerID: "clean", Name: "clean", Phase: "running", Labels: scion},
		{ContainerID: "bad", Name: "bad", Phase: "running", Labels: scion},
		{ContainerID: "flaky", Name: "flaky", Phase: "running", Labels: scion},
		{ContainerID: "broken", Name: "broken", Phase: "running", Labels: scion},
		{ContainerID: "stopped", Name: "stopped", Phase: "stopped", Labels: scion},
		{ContainerID: "other", Name: "other-workload", Phase: "running"},
	}
	ok := sweepInspect{binds: []scionrt.BindMount{{Source: outside, Destination: "/x"}}}
	bad := sweepInspect{binds: []scionrt.BindMount{{Source: global, Destination: "/g"}}}
	fail := sweepInspect{err: scionrt.ErrMountsUnverifiable}
	rt := newSweepRuntime(agents, map[string][]sweepInspect{
		"clean":   {ok},
		"bad":     {bad},
		"flaky":   {fail, ok},
		"broken":  {fail, fail},
		"stopped": {bad},
		"other":   {bad},
	})
	sw, marked := newTestSweeper(rt)
	now := time.Now()

	sw.tick(context.Background(), now)
	if strings.Join(rt.stopped, ",") != "bad" {
		t.Fatalf("after the first sweep, stopped = %v, want [bad] (a violation stops at once; an inspect failure waits a tick)", rt.stopped)
	}
	if rt.calls["stopped"] != 0 || rt.calls["other"] != 0 {
		t.Fatalf("swept a stopped agent or a non-Scion container: %v", rt.calls)
	}

	// The next tick is due because inspects failed: flaky now inspects
	// clean, broken fails a second time and is stopped.
	sw.tick(context.Background(), now.Add(controlPathSweepTick))
	if strings.Join(rt.stopped, ",") != "bad,broken" {
		t.Fatalf("after the retry tick, stopped = %v, want [bad broken]", rt.stopped)
	}
	if strings.Join(*marked, ",") != "bad,broken" {
		t.Fatalf("marked = %v", *marked)
	}
	if len(sw.failures) != 0 {
		t.Fatalf("failures not cleared: %v", sw.failures)
	}

	// Nothing failed and the control paths did not move: not due.
	before := rt.calls["clean"]
	sw.tick(context.Background(), now.Add(2*controlPathSweepTick))
	if rt.calls["clean"] != before {
		t.Fatal("swept before the interval with no change")
	}
	// The full interval elapsed: due.
	sw.tick(context.Background(), now.Add(controlPathSweepInterval+2*controlPathSweepTick))
	if rt.calls["clean"] != before+1 {
		t.Fatal("did not sweep after the interval")
	}
}

func TestControlPathSweep_ControlPathsMoved(t *testing.T) {
	_, outside := controlPathTestHome(t)
	scion := map[string]string{"scion.name": "x"}
	ws := filepath.Join(outside, "ws")
	rt := newSweepRuntime(
		[]api.AgentInfo{{ContainerID: "a", Name: "a", Phase: "running", Labels: scion}},
		map[string][]sweepInspect{"a": {{binds: []scionrt.BindMount{{Source: outside, Destination: "/x"}}}}},
	)
	sw, _ := newTestSweeper(rt)
	now := time.Now()
	sw.tick(context.Background(), now)
	if len(rt.stopped) != 0 {
		t.Fatalf("stopped = %v", rt.stopped)
	}
	// A control path is now registered inside the agent's bind: the next
	// tick sees the set change and stops the agent without waiting for the
	// interval.
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	brokercontrol.SetExtraPaths("sweep-test", []brokercontrol.Entry{{Path: ws, Kind: brokercontrol.Dir, Class: brokercontrol.ClassHostState}})
	t.Cleanup(func() { brokercontrol.SetExtraPaths("sweep-test", nil) })
	sw.tick(context.Background(), now.Add(controlPathSweepTick))
	if strings.Join(rt.stopped, ",") != "a" {
		t.Fatalf("stopped = %v, want [a]", rt.stopped)
	}
}

func TestControlPathSweep_RuntimeWithoutInspection(t *testing.T) {
	rt := &scionrt.MockRuntime{}
	sw, _ := newTestSweeper(rt)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sw.run(ctx) // returns at once: nothing to sweep
}

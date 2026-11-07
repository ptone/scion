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

package commands

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	core "github.com/GoogleCloudPlatform/scion/pkg/conduit"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/conduit"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/control"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
	conduitv1 "github.com/GoogleCloudPlatform/scion/proto/conduit/v1"
)

// Fixtures in the exact shapes the runtimes emit. The tmux start command
// is buildTmuxStartCmd's format (pkg/runtime/common.go:280-293) around
// tmuxAgentWindowCmd's wrapper (common.go:244-246).
const (
	fixtureAgentWindow = `sh -c 'claude --no-chrome; echo $? > /tmp/scion-harness-exit-code'`
	fixtureTmuxPrefix  = "tmux new-session -d -s scion -n agent " + fixtureAgentWindow +
		` \; set-option -g window-size latest \; new-window -t scion -n shell \; select-window -t scion:agent`
	// tmuxAttachSession (docker, podman, k8s).
	fixtureTmuxAttach = fixtureTmuxPrefix + ` \; attach-session -t scion`
	// tmuxPollSession (Cloud Run, Cloud Run sandbox, substrate).
	fixtureTmuxPoll = fixtureTmuxPrefix + "; while tmux has-session -t scion 2>/dev/null; do sleep 2; done"
	// Docker/Podman with FUSE mounts (common.go:1050-1052).
	fixtureFUSEScript = `gcsfuse --implicit-dirs bucket /mnt/data && exec sh -c "$SCION_START_CMD"`
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// TestHarnessInScionSession_RuntimeShapes: each runtime's child command
// is recognised (design r3 §1.5 condition 3).
func TestHarnessInScionSession_RuntimeShapes(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		env  map[string]string
	}{
		// common.go:1054-1055.
		{"docker/podman", []string{"sh", "-c", fixtureTmuxAttach}, nil},
		// common.go:1037-1052: the start command is in the environment.
		{"docker/podman with FUSE mounts", []string{"sh", "-c", fixtureFUSEScript}, map[string]string{envStartCmd: fixtureTmuxAttach}},
		// k8s_runtime.go:2185-2186: the gate shell expands
		// "$SCION_START_CMD" before it execs init, so init sees the plain
		// shape.
		{"kubernetes", []string{"sh", "-c", fixtureTmuxAttach}, nil},
		// cloudrun_runtime.go:900-901.
		{"cloud run", []string{"/bin/sh", "-c", fixtureTmuxPoll}, nil},
		// cloudrun_sandbox_runtime.go:1609.
		{"cloud run sandbox", []string{"/bin/sh", "-c", fixtureTmuxPoll}, nil},
		// substrate_runtime.go:1423.
		{"substrate", []string{"/bin/sh", "-c", fixtureTmuxPoll}, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if !harnessInScionSession(tt.argv, envOf(tt.env)) {
				t.Fatalf("not recognised: %q", tt.argv)
			}
		})
	}
}

// TestHarnessInScionSession_FailsClosed: anything but the exact shapes
// leaves the route absent.
func TestHarnessInScionSession_FailsClosed(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		env  map[string]string
	}{
		{"no -s", []string{"sh", "-c", "tmux new-session -d -n agent claude"}, nil},
		{"another session", []string{"sh", "-c", "tmux new-session -d -s main -n agent claude"}, nil},
		{"-s as the last token", []string{"sh", "-c", "tmux new-session -d -s"}, nil},
		{"first token not tmux", []string{"sh", "-c", "screen new-session -d -s scion -n agent claude"}, nil},
		{"tmux without new-session", []string{"sh", "-c", "tmux attach-session -t scion"}, nil},
		{"argv of 2", []string{"sh", "-c"}, nil},
		{"argv of 4", []string{"sh", "-c", fixtureTmuxAttach, "x"}, nil},
		{"argv[0] tmux", []string{"tmux", "new-session", "-d -s scion -n agent claude"}, nil},
		{"argv[0] bash", []string{"bash", "-c", fixtureTmuxAttach}, nil},
		{"argv[1] not -c", []string{"sh", "-e", fixtureTmuxAttach}, nil},
		{"FUSE shape, variable unset", []string{"sh", "-c", fixtureFUSEScript}, nil},
		{"FUSE shape, variable empty", []string{"sh", "-c", fixtureFUSEScript}, map[string]string{envStartCmd: ""}},
		{"FUSE suffix off by one character", []string{"sh", "-c", `gcsfuse b /mnt/d && exec sh -c "$SCION_START_CMDS"`}, map[string]string{envStartCmd: fixtureTmuxAttach}},
		{"unexpanded variable literal", []string{"sh", "-c", `"$SCION_START_CMD"`}, map[string]string{envStartCmd: fixtureTmuxAttach}},
		{"empty argv", nil, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if harnessInScionSession(tt.argv, envOf(tt.env)) {
				t.Fatalf("recognised: %q", tt.argv)
			}
		})
	}
}

// TestDeliverRouteOptions_Conditions: the route is offered only when the
// experiment is listed, tmux and sh resolve in the fixed tool path and
// the harness runs in the scion session; the runner runs as the PTY user
// and execs the resolved absolute paths (design r3 §1.5).
func TestDeliverRouteOptions_Conditions(t *testing.T) {
	resolveAll := func(name string) (string, error) { return "/usr/bin/" + name, nil }
	missing := func(miss string) func(string) (string, error) {
		return func(name string) (string, error) {
			if name == miss {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		}
	}
	argv := []string{"sh", "-c", fixtureTmuxAttach}
	flags := map[string]string{conduit.EnvHubExperiments: conduit.ExperimentConduit + ", " + experiments.ConduitDeliver}
	user := conduit.PTYUser{UID: 1000, GID: 1000, Username: "scion", RequirePrivilegeDrop: true}

	opts := deliverRouteOptions(user, argv, envOf(flags), resolveAll)
	if opts == nil {
		t.Fatal("route not offered with all conditions met")
	}
	want := control.LocalRunner{UID: 1000, GID: 1000, Username: "scion", RequirePrivilegeDrop: true,
		TmuxPath: "/usr/bin/tmux", ShPath: "/usr/bin/sh"}
	if got, ok := opts.Runner.(control.LocalRunner); !ok || got != want {
		t.Fatalf("runner %#v, want %#v", opts.Runner, want)
	}

	cases := []struct {
		name    string
		env     map[string]string
		resolve func(string) (string, error)
		argv    []string
	}{
		{"flag off", map[string]string{conduit.EnvHubExperiments: conduit.ExperimentConduit}, resolveAll, argv},
		{"no experiments", nil, resolveAll, argv},
		{"similar name only", map[string]string{conduit.EnvHubExperiments: experiments.ConduitDeliver + "x"}, resolveAll, argv},
		{"no tmux in the fixed tool path", flags, missing("tmux"), argv},
		{"no sh in the fixed tool path", flags, missing("sh"), argv},
		{"not in the scion session", flags, resolveAll, []string{"sh", "-c", "claude"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if deliverRouteOptions(user, tt.argv, envOf(tt.env), tt.resolve) != nil {
				t.Fatal("route offered")
			}
		})
	}
}

// TestPortForwardingConduitDeliverRoute: with the experiment listed, the
// agent session advertises deliver-message in Hello.capabilities.rpc and
// a hub-side call reaches the route (a malformed body is answered 400);
// without it the route is neither advertised nor served (design r3 §1.5,
// §6.3 route exposure).
func TestPortForwardingConduitDeliverRoute(t *testing.T) {
	orig := deliverResolveTool
	deliverResolveTool = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	t.Cleanup(func() { deliverResolveTool = orig })

	for _, tt := range []struct {
		name     string
		flags    string
		wantRPC  []string
		wantCall int32
	}{
		{"flag on", conduit.ExperimentConduit + "," + experiments.ConduitDeliver,
			[]string{control.RouteRotateToken, control.RouteDeliverMessage}, 400},
		{"flag off", conduit.ExperimentConduit, []string{control.RouteRotateToken}, 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(hub.SetTokenHome(t.TempDir()))
			h := newPFHub(t, true)
			client := hub.NewClientWithConfig(h.srv.URL, "token-1", "agent-1")
			env := map[string]string{
				conduit.EnvHubExperiments: tt.flags,
				projectkeys.EnvProjectID:  "project-1",
				conduit.EnvLaunchID:       "launch-1",
			}
			ctx, cancel := context.WithCancel(context.Background())
			p := newPortForwarding(client, false, conduit.PTYUser{}, nil,
				[]string{"sh", "-c", fixtureTmuxAttach}, func(k string) string { return env[k] })
			done := make(chan struct{})
			go func() { p.run(ctx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })

			var hello *conduitv1.Hello
			select {
			case hello = <-h.hellos:
			case <-time.After(pfWait):
				t.Fatal("no conduit Hello")
			}
			if got := hello.GetCapabilities().GetRpc(); !slices.Equal(got, tt.wantRPC) {
				t.Fatalf("Hello rpc = %v, want %v", got, tt.wantRPC)
			}
			var s core.Session
			select {
			case s = <-h.sessions:
			case <-time.After(pfWait):
				t.Fatal("no conduit session")
			}
			resp, err := s.Call(ctx, &conduitv1.RpcRequest{Method: "POST", Path: control.Prefix + control.RouteDeliverMessage,
				Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetStatus() != tt.wantCall {
				t.Fatalf("deliver-message call: status %d, want %d", resp.GetStatus(), tt.wantCall)
			}
		})
	}
}

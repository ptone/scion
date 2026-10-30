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

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
)

func TestParseFlags(t *testing.T) {
	base := []string{"-hub", "http://127.0.0.1:1/", "-dev-token-file", "t", "-port", "8080", "-state-dir", "s", "-ready-file", "r"}

	o, err := parseFlags(base)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if o.hubURL != "http://127.0.0.1:1" {
		t.Errorf("hubURL = %q, want the trailing slash trimmed", o.hubURL)
	}
	if strings.Join(o.transports, ",") != "conduit,legacy" {
		t.Errorf("transports = %v, want conduit,legacy by default", o.transports)
	}

	o, err = parseFlags(append(append([]string{}, base...), "-transports", "legacy"))
	if err != nil || strings.Join(o.transports, ",") != "legacy" {
		t.Errorf("-transports legacy: %v, %v", o.transports, err)
	}

	for name, extra := range map[string][]string{
		"unknown transport": {"-transports", "ssh"},
		"duplicate":         {"-transports", "legacy,legacy"},
		"bad port":          {"-port", "0"},
	} {
		if _, err := parseFlags(append(append([]string{}, base...), extra...)); err == nil {
			t.Errorf("%s: parseFlags accepted %v", name, extra)
		}
	}
	for i := 0; i < len(base); i += 2 {
		missing := append(append([]string{}, base[:i]...), base[i+2:]...)
		if base[i] == "-port" {
			continue // covered by "bad port"
		}
		if _, err := parseFlags(missing); err == nil {
			t.Errorf("parseFlags accepted a missing %s", base[i])
		}
	}
}

func TestTransportForAgent(t *testing.T) {
	for name, want := range map[string]string{
		"port-proxy-conduit": transportConduit,
		"port-proxy-legacy":  transportLegacy,
	} {
		if got, ok := transportForAgent(name); !ok || got != want {
			t.Errorf("transportForAgent(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"port-proxy-ssh", "legacy", "other-legacy", ""} {
		if got, ok := transportForAgent(name); ok {
			t.Errorf("transportForAgent(%q) = %q, want no transport", name, got)
		}
	}
}

func TestErrorKeyDropsTheRequestSuffix(t *testing.T) {
	a := errors.New("forbidden: no (status: 403, request: a)")
	b := errors.New("forbidden: no (status: 403, request: b)")
	if errorKey(a) != errorKey(b) || errorKey(a) != "forbidden: no" {
		t.Errorf("errorKey: %q, %q", errorKey(a), errorKey(b))
	}
}

func TestRetryable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("connection refused"), true},
		{&apiclient.APIError{StatusCode: 503, Code: "unavailable"}, true},
		{&apiclient.APIError{StatusCode: 500, Code: "internal_error"}, true},
		{&apiclient.APIError{StatusCode: 412, Code: "unsupported_capability"}, false},
		{&apiclient.APIError{StatusCode: 409, Code: "conflict"}, false},
		{fmt.Errorf("wrapped: %w", &apiclient.APIError{StatusCode: 403, Code: "forbidden"}), false},
	} {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("retryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestStartRefusesAnIncompleteEnvironment(t *testing.T) {
	s := newAgentSet(8080)
	full := map[string]string{
		"SCION_HUB_ENDPOINT": "http://127.0.0.1:1",
		"SCION_AGENT_ID":     "agent-1",
	}
	for _, key := range []string{"SCION_HUB_ENDPOINT", "SCION_AGENT_ID"} {
		env := map[string]string{}
		for k, v := range full {
			if k != key {
				env[k] = v
			}
		}
		if _, err := s.start(api.StartOptions{Name: "port-proxy-legacy", Env: env, HubIssuedToken: "token"}); err == nil {
			t.Errorf("start without %s succeeded", key)
		}
	}
	if _, err := s.start(api.StartOptions{Name: "port-proxy-legacy", Env: full}); err == nil {
		t.Error("start without a hub-issued token succeeded")
	}
	// A SCION_AUTH_TOKEN in the start environment is not the hub-issued
	// token: the runtime broker never puts the agent's token there.
	withEnvToken := map[string]string{"SCION_AUTH_TOKEN": "token"}
	for k, v := range full {
		withEnvToken[k] = v
	}
	if _, err := s.start(api.StartOptions{Name: "port-proxy-legacy", Env: withEnvToken}); err == nil {
		t.Error("start with the token only in the environment succeeded")
	}
	if _, err := s.start(api.StartOptions{Name: "worker", Env: full, HubIssuedToken: "token"}); err == nil {
		t.Error("start of an agent whose name names no transport succeeded")
	}
	if got := s.list(nil); len(got) != 0 {
		t.Errorf("refused starts left agents behind: %v", got)
	}
}

func TestListFiltersByLabel(t *testing.T) {
	s := newAgentSet(8080)
	s.agents["port-proxy-legacy"] = &fakeAgent{info: api.AgentInfo{
		Name:   "port-proxy-legacy",
		Labels: map[string]string{"scion.name": "port-proxy-legacy", "agent_id": "a1"},
	}}
	if got := s.list(map[string]string{"agent_id": "a1"}); len(got) != 1 {
		t.Errorf("list(agent_id=a1) = %v, want the agent", got)
	}
	if got := s.list(map[string]string{"agent_id": "a2"}); len(got) != 0 {
		t.Errorf("list(agent_id=a2) = %v, want none", got)
	}
}

func TestStartCarriesTheRunID(t *testing.T) {
	s := newAgentSet(8080)
	defer s.stopAll()
	info, err := s.start(api.StartOptions{
		Name:           "port-proxy-legacy",
		RunID:          "run-1",
		HubIssuedToken: "token",
		Env: map[string]string{
			// Nothing listens here: the agent keeps retrying the port
			// registration until stopAll.
			"SCION_HUB_ENDPOINT": "http://127.0.0.1:1",
			"SCION_AGENT_ID":     "agent-1",
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if info.RunID != "run-1" || info.Labels[api.LabelRunID] != "run-1" {
		t.Errorf("start info run = %q, label %q; want run-1", info.RunID, info.Labels[api.LabelRunID])
	}
	if got := s.list(map[string]string{api.LabelRunID: "run-1"}); len(got) != 1 {
		t.Errorf("list by run label = %v, want the agent", got)
	}
}

func TestWaitConnected(t *testing.T) {
	newAgent := func() *fakeAgent {
		return &fakeAgent{done: make(chan struct{}), connected: make(chan struct{})}
	}
	wait := func(s *agentSet, name string, d time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		return s.waitConnected(ctx, name)
	}

	s := newAgentSet(8080)
	stopped := newAgent()
	close(stopped.done)
	s.agents["port-proxy-legacy"] = stopped
	if err := wait(s, "port-proxy-legacy", 5*time.Second); err == nil || !strings.Contains(err.Error(), "stopped before") {
		t.Errorf("an agent that stopped before connecting: err = %v, want the stopped error", err)
	}

	connected := newAgent()
	connected.markConnected()
	s.agents["port-proxy-conduit"] = connected
	if err := wait(s, "port-proxy-conduit", 5*time.Second); err != nil {
		t.Errorf("a connected agent: err = %v, want nil", err)
	}

	if err := wait(s, "port-proxy-missing", 300*time.Millisecond); err == nil {
		t.Error("an agent the hub never started: want an error at the deadline")
	}
}

// fakeLifecycle is the hub's stop and start for stopAndResume: stop ends
// the agent here (as the broker's stop reaches the runtime), start runs
// startFn (the broker's start handler calling the fake manager).
type fakeLifecycle struct {
	agents   *agentSet
	name     string
	noStop   bool
	stopErr  error
	startFn  func() error
	stops    int
	starts   int
	sequence []string
}

func (f *fakeLifecycle) Stop(_ context.Context, _ string) (*hubclient.LifecycleResponse, error) {
	f.stops++
	f.sequence = append(f.sequence, "stop")
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	if !f.noStop {
		f.agents.stop(f.name)
	}
	return &hubclient.LifecycleResponse{}, nil
}

func (f *fakeLifecycle) Start(_ context.Context, _ string) (*hubclient.LifecycleResponse, error) {
	f.starts++
	f.sequence = append(f.sequence, "start")
	if err := f.startFn(); err != nil {
		return nil, err
	}
	return &hubclient.LifecycleResponse{}, nil
}

func TestStopAndResume(t *testing.T) {
	const name = "port-proxy-legacy"
	env := map[string]string{
		// Nothing listens here: the agent keeps retrying the port
		// registration until it is stopped.
		"SCION_HUB_ENDPOINT": "http://127.0.0.1:1",
		"SCION_AGENT_ID":     "agent-1",
	}
	startWith := func(s *agentSet, token, runID string) func() error {
		return func() error {
			_, err := s.start(api.StartOptions{Name: name, Env: env, HubIssuedToken: token, RunID: runID})
			return err
		}
	}
	running := func(t *testing.T) *agentSet {
		t.Helper()
		s := newAgentSet(8080)
		t.Cleanup(s.stopAll)
		if err := startWith(s, "token", "run-1")(); err != nil {
			t.Fatalf("first start: %v", err)
		}
		return s
	}
	call := func(lc *fakeLifecycle) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return stopAndResume(ctx, lc, lc.agents, name, "agent-1")
	}

	t.Run("resumes a new instance", func(t *testing.T) {
		s := running(t)
		old := s.current(name)
		lc := &fakeLifecycle{agents: s, name: name}
		lc.startFn = startWith(s, "token-2", "run-2")
		if err := call(lc); err != nil {
			t.Fatalf("stopAndResume: %v", err)
		}
		if strings.Join(lc.sequence, ",") != "stop,start" {
			t.Errorf("hub calls = %v, want stop then start", lc.sequence)
		}
		cur := s.current(name)
		if cur == nil || cur == old || cur.info.RunID != "run-2" {
			t.Errorf("after resume the running instance is %+v, want a new one on run-2", cur)
		}
	})

	t.Run("a resume without a hub-issued token fails", func(t *testing.T) {
		// What #6663 did: the broker dropped the credential on start, so
		// the agent manager refused the resume and the hub answered an
		// error.
		s := running(t)
		lc := &fakeLifecycle{agents: s, name: name}
		lc.startFn = func() error {
			if err := startWith(s, "", "run-2")(); err != nil {
				return &apiclient.APIError{StatusCode: 502, Message: err.Error()}
			}
			return nil
		}
		err := call(lc)
		if err == nil || !strings.Contains(err.Error(), "resume agent "+name) {
			t.Fatalf("err = %v, want the resume failure", err)
		}
		if s.current(name) != nil {
			t.Error("a refused resume left an agent running")
		}
	})

	t.Run("a hub start that starts nothing fails", func(t *testing.T) {
		s := running(t)
		lc := &fakeLifecycle{agents: s, name: name, startFn: func() error { return nil }}
		if err := call(lc); err == nil || !strings.Contains(err.Error(), "started no new agent") {
			t.Fatalf("err = %v, want the no-new-agent failure", err)
		}
	})

	t.Run("a stop that does not end the agent fails before the start", func(t *testing.T) {
		s := running(t)
		lc := &fakeLifecycle{agents: s, name: name, noStop: true, startFn: func() error { return nil }}
		if err := call(lc); err == nil || !strings.Contains(err.Error(), "did not end") {
			t.Fatalf("err = %v, want the stop failure", err)
		}
		if lc.starts != 0 {
			t.Errorf("start called %d times after a failed stop, want 0", lc.starts)
		}
	})

	t.Run("a stop error is final", func(t *testing.T) {
		s := running(t)
		lc := &fakeLifecycle{agents: s, name: name, stopErr: errors.New("boom"), startFn: func() error { return nil }}
		if err := call(lc); err == nil || !strings.Contains(err.Error(), "stop agent") {
			t.Fatalf("err = %v, want the stop error", err)
		}
		if lc.starts != 0 {
			t.Errorf("start called %d times after a stop error, want 0", lc.starts)
		}
	})

	t.Run("an agent that is not running fails", func(t *testing.T) {
		s := newAgentSet(8080)
		lc := &fakeLifecycle{agents: s, name: name, startFn: func() error { return nil }}
		if err := call(lc); err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("err = %v, want the not-running error", err)
		}
		if lc.stops != 0 {
			t.Errorf("stop called %d times for an agent that is not running, want 0", lc.stops)
		}
	})
}

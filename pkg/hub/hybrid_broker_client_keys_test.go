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

//go:build !no_sqlite

package hub

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agentkeys"
)

// fakeKeysHTTPClient embeds the full mockRuntimeBrokerClient stub (defined in
// httpdispatcher_test.go) so it satisfies RuntimeBrokerClient for free, and
// adds ExecuteKeys so it also satisfies agentkeys.BrokerClient — the same
// "optional interface, type-asserted at the call site" pattern
// HybridBrokerClient already uses for brokerImageClient.
type fakeKeysHTTPClient struct {
	*mockRuntimeBrokerClient
	calls  int
	result agentkeys.BrokerResult
	err    error
}

func (f *fakeKeysHTTPClient) ExecuteKeys(ctx context.Context, brokerID, brokerEndpoint, agentSlug string, req agentkeys.BrokerRequest) (agentkeys.BrokerResult, error) {
	f.calls++
	if f.err != nil {
		return agentkeys.BrokerResult{}, f.err
	}
	return f.result, nil
}

// TestHybridBrokerClient_ExecuteKeys_RouteLocal proves a locally connected
// broker is dispatched via the control channel, never HTTP.
func TestHybridBrokerClient_ExecuteKeys_RouteLocal(t *testing.T) {
	const localBroker = "broker-local"
	mgr := NewControlChannelManager(DefaultControlChannelConfig(), slog.Default())
	mgr.mu.Lock()
	mgr.connections[localBroker] = &BrokerConnection{brokerID: localBroker, sessionID: "s1"}
	mgr.mu.Unlock()

	httpClient := &fakeKeysHTTPClient{mockRuntimeBrokerClient: &mockRuntimeBrokerClient{}}
	c := NewHybridBrokerClient(mgr, httpClient, nil, false)

	got := c.route(context.Background(), localBroker, "")
	if got != routeLocal {
		t.Fatalf("route = %v, want routeLocal", got)
	}
	// The control channel's tunnel has no real transport wired in this test;
	// what matters here is that the HTTP client is never reached for a
	// locally connected broker.
	if httpClient.calls != 0 {
		t.Fatalf("HTTP client must not be called when routeLocal, got %d calls", httpClient.calls)
	}
}

// TestHybridBrokerClient_ExecuteKeys_RouteHTTP proves a broker with no local
// socket and a direct endpoint is dispatched via the HTTP keys client.
func TestHybridBrokerClient_ExecuteKeys_RouteHTTP(t *testing.T) {
	mgr := NewControlChannelManager(DefaultControlChannelConfig(), slog.Default())
	httpClient := &fakeKeysHTTPClient{
		mockRuntimeBrokerClient: &mockRuntimeBrokerClient{},
		result:                  agentkeys.BrokerResult{OperationID: "op-1", Outcome: agentkeys.OutcomeDispatched},
	}
	c := NewHybridBrokerClient(mgr, httpClient, nil, false)
	c.SetAffinityLookup(func(context.Context, string) (string, bool) { return "", false })

	result, err := c.ExecuteKeys(context.Background(), "broker-remote", "http://endpoint", "agent-1", agentkeys.BrokerRequest{ExecuteBefore: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("ExecuteKeys failed: %v", err)
	}
	if result.Outcome != agentkeys.OutcomeDispatched {
		t.Fatalf("outcome = %q, want dispatched", result.Outcome)
	}
	if httpClient.calls != 1 {
		t.Fatalf("expected exactly one HTTP call, got %d", httpClient.calls)
	}
}

// TestHybridBrokerClient_ExecuteKeys_RouteForwardAndUndeliverable prove that,
// unlike MessageAgent (which defers to a durable queue via
// ErrMessageDeferred), keys has no durable queue: both routeForward (another
// node believed to own the broker) and routeUndeliverable (no owner, no
// endpoint) must return agentkeys.ErrNotDispatched directly, and must never
// call the HTTP client.
func TestHybridBrokerClient_ExecuteKeys_RouteForwardAndUndeliverable(t *testing.T) {
	mgr := NewControlChannelManager(DefaultControlChannelConfig(), slog.Default())
	httpClient := &fakeKeysHTTPClient{mockRuntimeBrokerClient: &mockRuntimeBrokerClient{}}
	c := NewHybridBrokerClient(mgr, httpClient, nil, false)

	t.Run("routeForward", func(t *testing.T) {
		httpClient.calls = 0
		c.SetAffinityLookup(func(context.Context, string) (string, bool) { return "hubA", true })
		_, err := c.ExecuteKeys(context.Background(), "broker-remote", "", "agent-1", agentkeys.BrokerRequest{ExecuteBefore: time.Now().Add(time.Minute)})
		if !errors.Is(err, agentkeys.ErrNotDispatched) {
			t.Fatalf("expected agentkeys.ErrNotDispatched, got %v", err)
		}
		if got := agentkeys.ClassifyDispatchError(err); got != agentkeys.OutcomeKeysUnavailable {
			t.Fatalf("ClassifyDispatchError = %q, want %q", got, agentkeys.OutcomeKeysUnavailable)
		}
		if httpClient.calls != 0 {
			t.Fatalf("HTTP client must not be called for routeForward, got %d calls", httpClient.calls)
		}
	})

	t.Run("routeUndeliverable", func(t *testing.T) {
		httpClient.calls = 0
		c.SetAffinityLookup(func(context.Context, string) (string, bool) { return "", false })
		_, err := c.ExecuteKeys(context.Background(), "broker-remote", "", "agent-1", agentkeys.BrokerRequest{ExecuteBefore: time.Now().Add(time.Minute)})
		if !errors.Is(err, agentkeys.ErrNotDispatched) {
			t.Fatalf("expected agentkeys.ErrNotDispatched, got %v", err)
		}
		if httpClient.calls != 0 {
			t.Fatalf("HTTP client must not be called for routeUndeliverable, got %d calls", httpClient.calls)
		}
	})
}

// TestHybridBrokerClient_ExecuteKeys_HTTPClientMissingSupport proves a
// non-keys-aware httpClient (implementing only RuntimeBrokerClient) produces
// a plain error rather than a panic or a false success.
func TestHybridBrokerClient_ExecuteKeys_HTTPClientMissingSupport(t *testing.T) {
	mgr := NewControlChannelManager(DefaultControlChannelConfig(), slog.Default())
	c := NewHybridBrokerClient(mgr, &mockRuntimeBrokerClient{}, nil, false)
	c.SetAffinityLookup(func(context.Context, string) (string, bool) { return "", false })

	_, err := c.ExecuteKeys(context.Background(), "broker-remote", "http://endpoint", "agent-1", agentkeys.BrokerRequest{ExecuteBefore: time.Now().Add(time.Minute)})
	if err == nil {
		t.Fatal("expected an error when the HTTP client does not support keys dispatch")
	}
}

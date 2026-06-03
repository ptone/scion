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

package hub

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlChannelManager_OnDisconnectCallback(t *testing.T) {
	mgr := NewControlChannelManager(DefaultControlChannelConfig(), slog.Default())

	var mu sync.Mutex
	var receivedBrokerID string
	done := make(chan struct{})

	mgr.SetOnDisconnect(func(brokerID string) {
		mu.Lock()
		defer mu.Unlock()
		receivedBrokerID = brokerID
		close(done)
	})

	// Manually add a connection entry so removeConnection has something to remove
	conn := &BrokerConnection{brokerID: "broker-1"}
	mgr.mu.Lock()
	mgr.connections["broker-1"] = conn
	mgr.mu.Unlock()

	mgr.removeConnection("broker-1", conn)

	// Wait for async callback
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for onDisconnect callback")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "broker-1", receivedBrokerID)

	// Verify connection was removed
	require.False(t, mgr.IsConnected("broker-1"))
}

func TestControlChannelManager_OnDisconnectCallback_NilSafe(t *testing.T) {
	mgr := NewControlChannelManager(DefaultControlChannelConfig(), slog.Default())

	// Don't set any callback - verify removeConnection doesn't panic
	conn := &BrokerConnection{brokerID: "broker-2"}
	mgr.mu.Lock()
	mgr.connections["broker-2"] = conn
	mgr.mu.Unlock()

	// This should not panic
	mgr.removeConnection("broker-2", conn)

	require.False(t, mgr.IsConnected("broker-2"))
}

func TestControlChannelManager_ReconnectDoesNotTriggerDisconnect(t *testing.T) {
	// Reproduces the bug from issue #131: when a broker reconnects quickly
	// (disconnect + reconnect in the same second), the old connection's
	// deferred removeConnection was removing the NEW connection and firing
	// onDisconnect, leaving the broker stuck offline.
	mgr := NewControlChannelManager(DefaultControlChannelConfig(), slog.Default())

	disconnectCount := 0
	var mu sync.Mutex
	mgr.SetOnDisconnect(func(brokerID string) {
		mu.Lock()
		defer mu.Unlock()
		disconnectCount++
	})

	// Simulate the initial connection
	oldConn := &BrokerConnection{brokerID: "broker-reconnect", sessionID: "session-old"}
	mgr.mu.Lock()
	mgr.connections["broker-reconnect"] = oldConn
	mgr.mu.Unlock()

	// Simulate reconnect: a new connection replaces the old one in the map
	// (this is what HandleUpgrade does when a broker reconnects)
	newConn := &BrokerConnection{brokerID: "broker-reconnect", sessionID: "session-new"}
	mgr.mu.Lock()
	mgr.connections["broker-reconnect"] = newConn
	mgr.mu.Unlock()

	// Now the old goroutine's defer fires removeConnection with the OLD connection.
	// This must NOT remove the new connection or fire onDisconnect.
	mgr.removeConnection("broker-reconnect", oldConn)

	// Give the async callback time to fire (it shouldn't)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, 0, disconnectCount, "onDisconnect must not fire for a superseded connection")
	mu.Unlock()

	// The new connection must still be in the map
	require.True(t, mgr.IsConnected("broker-reconnect"), "new connection must remain active")
	got := mgr.GetConnection("broker-reconnect")
	assert.Equal(t, "session-new", got.GetSessionID(), "active connection must be the new one")
}

func TestControlChannelManager_StaleRemoveAfterReconnect_ThenRealDisconnect(t *testing.T) {
	// Verifies the full lifecycle: stale remove is skipped, but a later
	// real disconnect of the current connection fires the callback.
	mgr := NewControlChannelManager(DefaultControlChannelConfig(), slog.Default())

	var mu sync.Mutex
	var disconnectedBrokers []string
	mgr.SetOnDisconnect(func(brokerID string) {
		mu.Lock()
		defer mu.Unlock()
		disconnectedBrokers = append(disconnectedBrokers, brokerID)
	})

	oldConn := &BrokerConnection{brokerID: "broker-full", sessionID: "old"}
	mgr.mu.Lock()
	mgr.connections["broker-full"] = oldConn
	mgr.mu.Unlock()

	// Reconnect: new connection replaces old
	newConn := &BrokerConnection{brokerID: "broker-full", sessionID: "new"}
	mgr.mu.Lock()
	mgr.connections["broker-full"] = newConn
	mgr.mu.Unlock()

	// Stale remove from old goroutine — should be a no-op
	mgr.removeConnection("broker-full", oldConn)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	assert.Empty(t, disconnectedBrokers, "stale remove must not fire callback")
	mu.Unlock()

	// Real disconnect of the current connection
	done := make(chan struct{})
	mgr.SetOnDisconnect(func(brokerID string) {
		mu.Lock()
		defer mu.Unlock()
		disconnectedBrokers = append(disconnectedBrokers, brokerID)
		close(done)
	})

	mgr.removeConnection("broker-full", newConn)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for real disconnect callback")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"broker-full"}, disconnectedBrokers, "only the real disconnect should fire")
	require.False(t, mgr.IsConnected("broker-full"))
}

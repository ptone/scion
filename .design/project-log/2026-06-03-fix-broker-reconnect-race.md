# Fix: Broker control channel reconnect race (issue #131)

**Date**: 2026-06-03
**PR**: #133
**Files changed**: `pkg/hub/controlchannel.go`, `pkg/hub/controlchannel_test.go`

## Problem

When a runtime broker's control channel disconnects and reconnects rapidly (within the same second due to TCP keepalive timeout or load balancer reset), the hub internal state was not updated on reconnect. `onlineProviders` stayed at 0, leaving the broker permanently offline despite a live WebSocket session.

## Root cause

In `ControlChannelManager.HandleUpgrade`, a reconnecting broker gets a new `*BrokerConnection` installed in the map (replacing the old one), and `markBrokerOnline` is called. However, the old connection's `handleConnection` goroutine has a deferred `removeConnection(brokerID)` that races — it deletes the NEW connection from the map (since lookup is by `brokerID` string alone) and fires `onDisconnect`, which marks the broker offline.

## Fix

Changed `removeConnection` to accept a `*BrokerConnection` pointer in addition to the `brokerID` string. It now compares the passed connection pointer against the current map entry: if they don't match (meaning a reconnect already replaced the old connection), the removal and disconnect callback are both skipped.

## Observations

- The disconnect callback (`onDisconnect`) in `server.go` does significant work: updates broker heartbeat, updates all provider statuses, publishes a disconnected event. Having it fire for a stale connection was the direct cause of the state corruption.
- The `ControlChannelManager` has `SetOnDisconnect` but no corresponding `SetOnConnect` — the connect-side work happens externally in `server.go` via `markBrokerOnline`. This asymmetry is worth noting but wasn't addressed in this fix.

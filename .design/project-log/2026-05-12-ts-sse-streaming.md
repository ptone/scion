# TypeScript SDK: SSE Streaming Support

**Date:** 2026-05-12  
**Task:** Step 11 — Implement SSE streaming for the TypeScript SDK  
**Branch:** scion/manage-sdk

## Summary

Added Server-Sent Events (SSE) streaming support to the TypeScript SDK, enabling real-time consumption of agent lifecycle events and cloud log entries.

## What Was Implemented

### New Files

1. **`sdk/typescript/src/types/streaming.ts`** — Streaming type definitions:
   - `StreamEvent` — base SSE event shape
   - `AgentEvent` — agent lifecycle/status events with typed subject parsing
   - `AgentDetail` — freeform detail payload for agent status events
   - `LogEntry` / `SourceLocation` — Cloud Logging structured log entries
   - `StreamOptions` — reconnect, backoff, timeout, signal, query config
   - `StreamCallbackOptions<T>` — callback-style consumption interface

2. **`sdk/typescript/src/streaming.ts`** — SSE stream parser and high-level wrapper:
   - `createLineSplitter()` — TransformStream splitting text chunks into lines
   - `createSSEParser()` — TransformStream parsing SSE lines into StreamEvent objects
   - `ScionStream<T>` — main stream class implementing `AsyncIterable<T>` and `subscribe()` callback API
   - Features: AbortSignal cancellation, auto-reconnect with exponential backoff + jitter, Last-Event-ID tracking, clean teardown

3. **`sdk/typescript/tests/streaming.test.ts`** — 36 unit tests covering:
   - Line splitter: \n, \r\n handling, cross-chunk buffering
   - SSE parser: data events, named events, IDs, multi-line data, heartbeats, comments, empty values, flush on close
   - Full pipeline: raw byte stream → text → lines → parsed SSE events, chunked delivery
   - ScionStream: AsyncIterable consumption, null filtering, callback subscription
   - AbortSignal: mid-stream abort, pre-aborted signal
   - Error handling: non-retryable errors, callback onError
   - Heartbeat handling: heartbeat filtering
   - Reconnection: server-close reconnect, maxReconnectAttempts exhaustion
   - AgentsResource integration: streamEvents (global and project-scoped), streamCloudLogs, malformed JSON handling, callback style

### Modified Files

4. **`sdk/typescript/src/resources/agents.ts`** — Added streaming methods:
   - `streamEvents(agentId, opts?)` — subscribe to agent lifecycle SSE events
   - `streamCloudLogs(agentId, opts?)` — subscribe to cloud log SSE stream
   - Both support AsyncIterable and callback overloads
   - Project-scoped subject patterns when client has projectId

5. **`sdk/typescript/src/types/index.ts`** — Added streaming type re-exports

6. **`sdk/typescript/src/index.ts`** — Added `ScionStream`, `createSSEParser`, `createLineSplitter` exports and all streaming types

## Design Decisions

- **TransformStream pipeline** rather than manual string parsing — composes cleanly and handles backpressure natively
- **Generic `ScionStream<T>`** with pluggable parse function — same stream machinery powers both agent events and log entries
- **Dual API** (AsyncIterable + callbacks) — AsyncIterable is primary for idiomatic `for await...of`, callbacks support existing event-driven patterns
- **Reconnection in ScionStream**, not transport — SSE reconnection is stream-level concern (Last-Event-ID, event dedup) distinct from HTTP retry
- **400/404 errors throw immediately** through transport (no retry), while stream-level reconnection handles transient disconnects

## Verification

- `npm run typecheck` — passes
- `npm run build` — ESM + CJS + DTS all build successfully
- `npm test` — 174 tests pass (36 streaming + 138 existing)

# Wake Flag Design Doc — Project Log

**Date:** 2026-05-12
**Agent:** design-wake
**Task:** Write design document for `--wake` flag on `scion message` (Issue #26)

## What was done

- Read and analyzed issue #26 thoroughly
- Traced the full message delivery path in both Hub and local modes:
  - `cmd/message.go` → `sendMessageViaHub()` → Hub `handleAgentMessage()` → `dispatcher.DispatchAgentMessage()` → broker `sendMessage()` → `AgentManager.Message()`
  - Local mode: `cmd/message.go` → `AgentManager.Message()` directly
- Traced the start/resume path to understand where suspend→resume detection happens:
  - Broker start handler (`runtimebroker/handlers.go:1044-1049`) checks `GetSavedPhase()` and sets `opts.Resume = true`
  - Hub `handleAgentLifecycle()` dispatches start via `DispatchAgentStart()`
- Identified that the message path has no phase awareness — it assumes the agent is running
- Wrote comprehensive design doc covering Hub mode, local mode, API changes, race conditions, edge cases
- Committed to `.design/wake-flag-design.md` on branch `scion/design-wake`
- Sent open questions to ptone@google.com for review

## Key findings

1. **Hub handler is the right location** for wake logic — it has phase information from the store and access to the dispatcher for start
2. **Broker doesn't need changes** — the broker's message handler only needs to deal with running agents; the Hub orchestrates the resume-then-message sequence
3. **Race conditions are manageable** — `DispatchAgentStart` is idempotent for already-running agents, so concurrent wake requests are safe
4. **Post-resume readiness gap** exists between container running and harness ready; proposed a 2-second fixed delay as a simple v1 solution
5. **Interface change required** — `SendStructuredMessage` needs a `wake` parameter, touching ~5 call sites

## Awaiting

Approval from ptone@google.com before starting implementation.

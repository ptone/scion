# Wake Flag Implementation — Phases 1-4

**Date**: 2026-05-12
**Branch**: `scion/implement-wake`
**Author**: dev-integrate-wake

## Summary

Implemented the complete `--wake` flag feature for `scion message`, allowing users to resume a suspended agent and deliver a message in a single command. The implementation spans 4 phases, each in its own commit.

## Changes

### Phase 1: Interface Plumbing
- Added `wake bool` parameter to `AgentService.SendStructuredMessage` interface and implementation in `pkg/hubclient/agents.go`
- Added `Wake` field to the request body struct with `json:"wake,omitempty"` tag
- Updated all 8 call sites across 5 files to pass `false` for the new parameter:
  - `cmd/message.go` (3 sites: project broadcast, global broadcast, single agent)
  - `extras/scion-a2a-bridge/internal/bridge/bridge.go` (3 sites: non-blocking send, blocking send, cancel interrupt)
  - `extras/scion-a2a-bridge/internal/bridge/stream.go` (1 site: streaming send)
  - `extras/scion-chat-app/internal/chatapp/commands.go` (1 site: chat message)

### Phase 2: CLI Flag + Validation
- Added `--wake` / `-w` boolean flag to the `message` command
- Added validation rules:
  - Cannot combine with `--broadcast` or `--all` (wake targets a single agent)
  - Cannot combine with `--in` or `--at` (scheduled messages)
  - Cannot combine with `--raw` (raw send-keys mode)
  - Cannot use with user recipients
- Returns clear error in local mode ("--wake requires Hub mode")
- Updated `sendMessageViaHub` to accept and pass `wake` to the single-agent path; broadcast fan-out paths keep passing `false`

### Phase 3: Hub Handler Wake Logic
- Added `Wake bool` field to `MessageRequest` struct in `pkg/hub/handlers.go`
- In `handleAgentMessage`, when `req.Wake` is true:
  - **Suspended**: resumes via `DispatchAgentStart`, updates phase to running, publishes status event, waits for agent readiness
  - **Running**: no-op (message delivered normally)
  - **Stopped**: returns 400 with guidance to use `scion start`
  - **Error**: returns 400 with guidance to use `scion start`
  - **Other phases**: returns 400 with current phase info

### Phase 4: Readiness Polling Helper
- Created `pkg/hub/wake.go` with `waitForAgentReady` method
- Polls agent store every 500ms until agent's `Activity` field is non-empty (harness initialized) or timeout expires
- Returns error if agent enters unexpected phase during polling
- Uses context-based timeout (15 seconds from the handler)

## Design Decisions

- **Did not modify `pkg/brokerclient/agents.go`** — that's a separate interface for broker-to-agent communication, not CLI-to-Hub
- **Wake only in single-agent path** — broadcast/all paths pass `false` since waking all agents simultaneously is not a supported use case
- **Activity-based readiness check** — uses the existing `Activity` field (set by the harness on init) rather than adding new state fields

## Verification

- `go build ./...` passes with zero errors after all phases

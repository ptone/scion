# Phase 5: Tests for --wake Feature

**Date:** 2026-05-12
**Branch:** `scion/implement-wake`
**Author:** dev-phase5-tests agent

## Summary

Added comprehensive tests for the `--wake` flag feature across the CLI and hub handler layers.

## Changes Made

### 1. Fixed Existing Test Compilation Errors (`cmd/message_test.go`)
- Updated all 9 existing `sendMessageViaHub` call sites to include the new `wake bool` parameter (added `false` as the 8th argument)
- Verified with `go vet ./cmd/...`

### 2. New CLI Validation Tests (`cmd/message_test.go`)
- **`TestWakeFlagValidation`** — 6 sub-tests covering all `--wake` incompatibility checks:
  - `--wake` + `--broadcast` → error
  - `--wake` + `--all` → error
  - `--wake` + `--in` → error
  - `--wake` + `--at` → error
  - `--wake` + `--raw` → error
  - `--wake` + `user:` recipient → error
- **`TestSendMessageViaHub_WakePassedThrough`** — verifies `wake=true` propagates through the HTTP request body to the mock server

### 3. Hub Handler Wake Tests (`pkg/hub/wake_test.go`, new file)
- Shared test fixture helper `createWakeTestFixtures` for DRY test setup
- **`TestHandleAgentMessage_WakeStopped`** — 400 with "Agent is stopped" message
- **`TestHandleAgentMessage_WakeError`** — 400 with "Agent is in error state" message
- **`TestHandleAgentMessage_WakeRunning`** — no-op, message delivered normally (uses `recordingDispatcher`)
- **`TestHandleAgentMessage_WakeUnknownPhase`** — 400 with "Agent is not yet running" for `provisioning` phase
- **`TestWaitForAgentReady_Timeout`** — returns timeout error when activity never reported (100ms timeout)
- **`TestWaitForAgentReady_ActivityReported`** — succeeds when activity becomes non-empty via goroutine update
- **`TestWaitForAgentReady_UnexpectedPhase`** — errors when phase transitions to `stopped` during polling

## Test Results

All new tests pass:
- `go test ./cmd/ -run "TestWakeFlagValidation|TestSendMessageViaHub_WakePassedThrough"` → PASS
- `go test ./pkg/hub/ -run "TestHandleAgentMessage_Wake|TestWaitForAgentReady"` → PASS (7 tests)

## Observations

- Some pre-existing `cmd/` test failures exist (e.g., `TestSendMessageViaHub_SingleAgent`) related to mock URL routing using `groves/` prefix instead of `projects/`. These predate this work and are unrelated to the wake feature.
- The `recordingDispatcher` mock from `notifications_test.go` is shared across the hub test package, which made the running-agent wake test straightforward.
- The `waitForAgentReady` tests use goroutines with short delays to simulate async agent status updates — a well-established pattern in the codebase.

# Design: `--wake` Flag for `scion message` Command

**Issue:** [#26](https://github.com/ptone/scion/issues/26)
**Status:** Approved (with revisions — see Decisions below)
**Author:** design-wake agent
**Date:** 2026-05-12

## Summary

Add a `--wake` flag to `scion message` that atomically resumes a suspended agent and delivers a message, combining what currently requires two separate commands (`scion start <agent>` + `scion message <agent> "..."`).

```
scion message --wake my-agent "Here's your next task"
```

## Motivation

When orchestrating agents, a parent agent frequently parks children via `scion suspend` and later needs to wake them with new work. Today this requires two commands with a timing gap between them. The `--wake` flag captures the user's intent ("wake this agent and tell it something") as a single atomic operation, eliminating race conditions and simplifying orchestration logic.

## Current Behavior

### Hub Mode (primary path)
1. CLI calls `sendMessageViaHub()` → Hub client's `SendStructuredMessage()` → `POST /api/v1/agents/{id}/message`
2. Hub's `handleAgentMessage()` (`pkg/hub/handlers.go:2214`) does **not** check agent phase before dispatching
3. It calls `checkBrokerAvailability()`, then forwards to the runtime broker via `dispatcher.DispatchAgentMessage()`
4. Broker's `sendMessage()` (`pkg/runtimebroker/handlers.go:1250`) resolves the agent manager and calls `mgr.Message()` or `mgr.MessageRaw()`
5. `AgentManager.Message()` (`pkg/agent/manager.go`) lists running containers → fails with `"agent '<name>' not found or not running"` because the suspended agent's container is stopped

**Key detail:** The auto-resume logic for suspended agents exists **only** in the broker's start handler (`runtimebroker/handlers.go:1044-1049`):
```go
if savedPhase == string(state.PhaseSuspended) {
    opts.Resume = true
}
```
This logic is absent from the message path entirely.

### Local Mode
1. CLI directly creates `AgentManager` and calls `mgr.Message()`
2. `AgentManager.Message()` lists running containers → fails with `"agent '<name>' not found or not running"`

**Result:** Messaging a suspended agent fails in both modes.

## Proposed Behavior

```
scion message --wake <agent> "message"
```

| Agent Phase | Behavior |
|---|---|
| `suspended` | Resume the agent, wait for `running` phase, deliver the message |
| `running` | Deliver the message normally (`--wake` is a no-op) |
| `stopped` | Return error: `"agent is stopped, not suspended — use 'scion start' to start a fresh session"` |
| `error` | Return error: `"agent is in error state — use 'scion start' to restart"` |
| `created`/`provisioning`/`starting` | Return error: `"agent is not yet running (phase: <phase>) — wait for it to reach running state"` |

## Implementation Plan

### 1. CLI Layer (`cmd/message.go`)

#### New flag variable and registration

```go
var msgWake bool

// In init():
messageCmd.Flags().BoolVarP(&msgWake, "wake", "w", false,
    "Resume a suspended agent before delivering the message")
```

#### Flag validation rules

Add to the existing validation block at the top of RunE:

```go
if msgWake {
    if msgBroadcast || msgAll {
        return fmt.Errorf("--wake cannot be combined with --broadcast or --all")
    }
    if msgIn != "" || msgAt != "" {
        return fmt.Errorf("--wake cannot be combined with --in or --at")
    }
    if msgRaw {
        return fmt.Errorf("--wake cannot be combined with --raw")
    }
    if userRecipient != "" {
        return fmt.Errorf("--wake cannot be used with user recipients")
    }
}
```

**Rationale for each exclusion:**
- `--broadcast`/`--all`: Waking multiple agents atomically is a different and riskier operation; future extension if needed.
- `--in`/`--at`: Ambiguous semantics — wake at schedule time or fire time? Excluded for simplicity.
- `--raw`: Raw messages need a running tmux session with specific send-keys behavior; the resume handshake doesn't compose well with raw byte delivery.
- User recipients: `--wake` is specifically for agent lifecycle; users don't have a suspended/running phase.

#### Hub mode path

Pass `msgWake` through to `sendMessageViaHub()`:

```go
func sendMessageViaHub(hubCtx *HubContext, agentName string, message string,
    interrupt bool, broadcast bool, all bool, notify bool, wake bool) error {
    // ... existing code ...

    // Single agent path — add wake to the request
    msg := buildStructuredMessage(sender, "agent:"+agentName, message)
    if err := agentSvc.SendStructuredMessage(ctx, agentName, msg, interrupt, notify, wake); err != nil {
        // ...
    }
}
```

#### Local mode path — not supported

`--wake` is Hub/API-only. In the local code path, validate early and return an error:

```go
// Local mode — --wake requires Hub
if msgWake {
    return fmt.Errorf("--wake requires Hub mode (use 'scion hub enable' first)")
}
```

### 2. API Layer Changes

#### Hub MessageRequest (`pkg/hub/handlers.go`)

Add a `Wake` field to `MessageRequest`:

```go
type MessageRequest struct {
    Message           string                       `json:"message,omitempty"`
    StructuredMessage *messages.StructuredMessage   `json:"structured_message,omitempty"`
    Interrupt         bool                         `json:"interrupt,omitempty"`
    Notify            bool                         `json:"notify,omitempty"`
    Wake              bool                         `json:"wake,omitempty"` // NEW
}
```

#### Hub Client (`pkg/hubclient/agents.go`)

Update `SendStructuredMessage` to accept and forward the wake flag:

```go
func (s *agentService) SendStructuredMessage(ctx context.Context, agentID string,
    msg *messages.StructuredMessage, interrupt bool, notify bool, wake bool) error {
    body := struct {
        StructuredMessage *messages.StructuredMessage `json:"structured_message"`
        Interrupt         bool                        `json:"interrupt,omitempty"`
        Notify            bool                        `json:"notify,omitempty"`
        Wake              bool                        `json:"wake,omitempty"`
    }{
        StructuredMessage: msg,
        Interrupt:         interrupt,
        Notify:            notify,
        Wake:              wake,
    }
    resp, err := s.c.post(ctx, s.agentPath(agentID)+"/message", body, nil)
    if err != nil {
        return err
    }
    return apiclient.CheckResponse(resp)
}
```

The `AgentService` interface also needs updating:

```go
SendStructuredMessage(ctx context.Context, agentID string,
    msg *messages.StructuredMessage, interrupt bool, notify bool, wake bool) error
```

**Note:** All existing callers of `SendStructuredMessage` need a `false` added for the new `wake` parameter. There are ~5 call sites (broadcast fan-out, notification delivery, etc.).

### 3. Hub Handler (`pkg/hub/handlers.go` — `handleAgentMessage`)

This is the core logic. Insert wake handling **after** the agent is loaded and **before** broker dispatch:

```go
func (s *Server) handleAgentMessage(w http.ResponseWriter, r *http.Request, id string) {
    ctx := r.Context()

    var req MessageRequest
    // ... existing JSON decode and message construction ...

    agent, err := s.store.GetAgent(ctx, id)
    if err != nil {
        writeErrorFromErr(w, err, "")
        return
    }

    // === NEW: Wake handling ===
    if req.Wake {
        switch state.Phase(agent.Phase) {
        case state.PhaseSuspended:
            // Resume the agent first, then deliver the message
            if !s.checkBrokerAvailability(w, r, agent) {
                return
            }
            dispatcher := s.GetDispatcher()
            if dispatcher == nil {
                ServiceNotReady(w, "Dispatch not available — server may still be starting up")
                return
            }
            if agent.RuntimeBrokerID == "" {
                ServiceNotReady(w, "Agent has no runtime broker assigned")
                return
            }

            // Dispatch start (reuses the same path as handleAgentLifecycle's start case)
            if err := dispatcher.DispatchAgentStart(ctx, agent, ""); err != nil {
                RuntimeError(w, "Failed to wake agent: "+err.Error())
                return
            }

            // Update Hub state to running
            statusUpdate := store.AgentStatusUpdate{
                Phase:           string(state.PhaseRunning),
                ContainerStatus: agent.ContainerStatus, // from broker response
            }
            if err := s.store.UpdateAgentStatus(ctx, id, statusUpdate); err != nil {
                writeErrorFromErr(w, err, "")
                return
            }
            agent.Phase = string(state.PhaseRunning)
            s.events.PublishAgentStatus(ctx, agent)

            // Wait for the agent to become ready for messages.
            // Poll the agent's state until the harness reports readiness
            // (activity transitions from empty/starting to idle/thinking/etc.)
            if err := s.waitForAgentReady(ctx, id, 15*time.Second); err != nil {
                RuntimeError(w, "Agent resumed but did not become ready: "+err.Error())
                return
            }

        case state.PhaseRunning:
            // Already running — wake is a no-op, proceed to message delivery

        case state.PhaseStopped:
            writeError(w, http.StatusBadRequest, ErrCodeValidationError,
                "Agent is stopped, not suspended — use 'scion start' to start a fresh session", nil)
            return

        case state.PhaseError:
            writeError(w, http.StatusBadRequest, ErrCodeValidationError,
                "Agent is in error state — use 'scion start' to restart", nil)
            return

        default:
            writeError(w, http.StatusBadRequest, ErrCodeValidationError,
                fmt.Sprintf("Agent is not yet running (phase: %s) — wait for it to reach running state", agent.Phase), nil)
            return
        }
    }
    // === END Wake handling ===

    // ... existing broker availability check, dispatch, and notification logic ...
}
```

**Why the Hub handler is the right place:**
- Phase information is readily available from the store
- The dispatcher infrastructure for `DispatchAgentStart` is already accessible
- The Hub handler already manages state transitions (see `handleAgentLifecycle`)
- Keeps the broker's `sendMessage` handler simple — it only needs to deliver messages to running agents

### 4. Race Condition Handling

#### Resume ↔ Message delivery gap

After `DispatchAgentStart` returns, the container is running but the harness (Claude Code, Gemini CLI) inside the tmux session may not yet be ready to receive messages. There is a timing gap:

```
DispatchAgentStart returns → container running → tmux session restored → harness ready
                              ~0s                  ~1-2s                  ~2-5s
```

**Approach: State-based readiness inference.** After dispatching the start, the Hub should poll the agent's reported state until the harness indicates readiness. The sciontool hooks report activity transitions (STARTING → IDLE/THINKING/WAITING_FOR_INPUT) — the Hub can wait for the agent's activity to move beyond the initial startup phase.

Concretely, after `DispatchAgentStart`:
1. Poll `store.GetAgent()` at short intervals (e.g., 500ms)
2. Wait until `agent.Phase == "running"` AND `agent.Activity` is set to a non-empty value (indicating the harness has initialized and reported its first status)
3. Apply a timeout (e.g., 15 seconds) — if the agent doesn't reach ready state within the timeout, return an error

If the current state infrastructure doesn't expose enough granularity to determine harness readiness (e.g., the activity field isn't populated quickly enough after resume), this should be filed as a sub-issue rather than worked around with a fixed `time.Sleep`.

**Do NOT use a fixed timing delay.** Timing-based approaches are fragile across different hardware, container runtimes, and harness implementations.

#### Concurrent wake requests

If two callers simultaneously send `--wake` messages to the same suspended agent:
1. Both read `phase == suspended` from the store
2. Both dispatch `DispatchAgentStart`
3. One succeeds; the other gets a "container already running" response from the broker (the broker's start handler is idempotent for already-running agents — see `run.go:87-98`)
4. Both proceed to message delivery

This is safe because:
- `DispatchAgentStart` is idempotent for running agents
- The message buffer in `AgentManager` handles concurrent message delivery
- The Hub updates phase to `running` after start — the second caller may redundantly update phase, which is a no-op

#### Agent crashes during wake

If the agent's container fails to start (e.g., OOM, corrupt image):
- `DispatchAgentStart` returns an error
- The Hub handler returns an HTTP error to the CLI
- The message is not delivered
- The agent remains in `suspended` state (the start failure doesn't change Hub state)

### 5. Timeout Handling

The existing 30-second context timeout in `sendMessageViaHub()` covers the entire operation including wake:

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
```

A resume typically completes in 3-5 seconds (no provisioning, no image pull — the container is checkpointed). The readiness poll has its own 15-second sub-timeout within this overall 30-second budget. This leaves ~12 seconds for the actual message delivery.

If the resume or readiness check exceeds the context timeout, the CLI reports a timeout error. The agent may still resume successfully on the broker side — a subsequent `scion message` (without `--wake`) would then succeed.

### 6. CLI Modes (`cmd/cli_mode.go`)

The `message` command is already available in both `assistant` and `agent` CLI modes. The `--wake` flag doesn't change the command's availability — it's a new option on an existing command.

### 7. Web UI Considerations

The web UI currently sends messages via the Hub API. Adding `wake: true` to the message request body is straightforward. The UI could:
- Show a "Wake & Message" button for suspended agents
- Automatically set `wake: true` when sending a message to a suspended agent

This is out of scope for the initial implementation but the API design supports it.

## Files to Modify

| File | Change |
|---|---|
| `cmd/message.go` | Add `--wake` flag, validation rules, pass through to Hub path; error in local mode |
| `pkg/hub/handlers.go` | Add `Wake` field to `MessageRequest`, wake logic + `waitForAgentReady()` in `handleAgentMessage` |
| `pkg/hubclient/agents.go` | Add `wake` parameter to `SendStructuredMessage` interface + implementation |
| `pkg/runtimebroker/types.go` | No changes needed (broker doesn't need wake awareness) |
| `pkg/agent/manager.go` | No changes needed (manager handles running agents only) |

**Callers of `SendStructuredMessage` that need the new parameter added (as `false`):**
- `cmd/message.go` — broadcast fan-out paths (~2 sites)
- `pkg/hub/notifications.go` — notification delivery
- `pkg/hub/messagebroker.go` — message broker fan-out
- `pkg/hub/handlers_broker_inbound.go` — broker inbound relay

## Implementation Phases

### Phase 1: Interface plumbing — `SendStructuredMessage` signature change
**Covers:** Add `wake bool` parameter to the `AgentService` interface in `pkg/hubclient/agents.go`, update the implementation, and update **all** existing call sites to pass `false`.
**Files:**
- `pkg/hubclient/agents.go` — interface definition + implementation
- `cmd/message.go` — 3 call sites (two broadcast fan-outs + single-agent path)
- `extras/scion-a2a-bridge/internal/bridge/bridge.go` — 3 call sites
- `extras/scion-a2a-bridge/internal/bridge/stream.go` — 1 call site
- `extras/scion-chat-app/internal/chatapp/commands.go` — 1 call site

**Complexity:** Low. Mechanical signature change — add a parameter, pass `false` at every existing call site. The risk is missing a call site and breaking compilation.
**Dependencies:** None. Can start immediately.
**Parallelizable:** Yes — this phase has no dependency on other phases, though Phases 2–4 depend on it.

---

### Phase 2: CLI flag and validation
**Covers:** Add `--wake` / `-w` flag to `cmd/message.go`, all flag incompatibility validation rules, Hub-mode requirement check, and passing `wake` through to `sendMessageViaHub()`.
**Files:**
- `cmd/message.go` — flag variable, `init()` registration, validation block in `RunE`, `sendMessageViaHub()` signature change, local-mode error gate

**Complexity:** Low. Follows the established pattern of other flag additions (`--notify`, `--raw`, `--plain`). The validation rules are straightforward boolean checks.
**Dependencies:** Phase 1 (needs the updated `SendStructuredMessage` signature to pass `wake` through).
**Parallelizable:** Can be done in parallel with Phase 3 (Hub handler) if the Phase 1 interface is landed first. In practice, since both touch `cmd/message.go`, serial is cleaner.

---

### Phase 3: Hub handler — wake logic in `handleAgentMessage`
**Covers:** Add `Wake` field to Hub's `MessageRequest` struct. Implement the phase-switching wake logic in `handleAgentMessage`: check agent phase, dispatch start for suspended agents, update Hub state, gate on readiness, then fall through to existing message dispatch.
**Files:**
- `pkg/hub/handlers.go` — `MessageRequest` struct, `handleAgentMessage()` function

**Complexity:** Medium. This is the core logic. Requires careful ordering (start dispatch → state update → readiness check → message dispatch) and correct error handling for each phase. The readiness polling helper (`waitForAgentReady`) is new code.
**Dependencies:** Phase 1 (for the API contract). Phase 2 is not strictly required since the Hub handler reads `req.Wake` from the HTTP request body, independent of the CLI flag.
**Parallelizable:** Can be done in parallel with Phase 2 after Phase 1 is complete.

---

### Phase 4: Readiness polling — `waitForAgentReady` helper
**Covers:** Implement the `waitForAgentReady(ctx, agentID, timeout)` method on the Hub server. Polls `store.GetAgent()` at 500ms intervals until the agent's `Activity` field is non-empty (indicating the harness has reported its first status after resume), or until timeout.
**Files:**
- `pkg/hub/handlers.go` (or a new `pkg/hub/wake.go` if the method is large enough to warrant its own file)

**Complexity:** Medium. The polling logic itself is simple, but the correctness depends on understanding the sciontool status reporting lifecycle. Need to verify that resumed agents reliably report an activity update within the timeout window. If they don't, this phase produces a sub-issue rather than a workaround.
**Dependencies:** None for the implementation, but it's called by Phase 3. Can be implemented and tested independently.
**Parallelizable:** Yes — fully parallel with Phases 2 and 3. The method is self-contained and only needs the store interface.

---

### Phase 5: Tests
**Covers:** Unit tests for all new code: flag validation (Phase 2), Hub handler wake logic (Phase 3), readiness polling (Phase 4), and the interface change (Phase 1).
**Files:**
- `cmd/message_test.go` (or new test file) — flag validation tests
- `pkg/hub/handlers_test.go` (or new `pkg/hub/wake_test.go`) — Hub handler wake tests with mocked store/dispatcher
- Integration-level test for the end-to-end wake-then-message flow (if test infrastructure supports it)

**Complexity:** Medium. Requires mocking the store (to control agent phase) and the dispatcher (to verify start dispatch). The readiness polling test needs to simulate activity updates arriving asynchronously.
**Dependencies:** Phases 1–4 (tests exercise the code written in those phases).
**Parallelizable:** Test scaffolding (mock setup, test helpers) can be written in parallel with implementation phases. Test bodies must be written after the code they test.

---

### Phase Summary

```
Phase 1: Interface plumbing          [Low]     ──┐
                                                  ├── Phase 2: CLI flag + validation  [Low]
Phase 4: Readiness polling helper    [Medium]  ──┤
                                                  └── Phase 3: Hub handler wake logic [Medium]
                                                                                        │
                                                                      Phase 5: Tests  [Medium]
```

| Phase | Complexity | Serial/Parallel | Depends on |
|-------|-----------|-----------------|------------|
| 1. Interface plumbing | Low | Start immediately | — |
| 2. CLI flag + validation | Low | After Phase 1 | Phase 1 |
| 3. Hub handler wake logic | Medium | After Phase 1; parallel with Phase 2 | Phase 1, Phase 4 |
| 4. Readiness polling helper | Medium | Parallel with all others | — |
| 5. Tests | Medium | After Phases 1–4 | Phases 1–4 |

**Critical path:** Phase 1 → Phase 3 → Phase 5
**Parallelizable work:** Phase 4 can proceed independently from the start. Phase 2 and Phase 3 can run in parallel once Phase 1 is done.

## Testing Strategy

### Unit tests
- Flag validation: `--wake` + `--broadcast`, `--wake` + `--raw`, etc. → expect errors
- Hub handler: mock store and dispatcher, test wake-then-message for each phase
- Idempotency: `--wake` on already-running agent → message delivered normally

### Integration tests
- Suspend an agent → `scion message --wake` → verify agent resumes and receives message
- `scion message --wake` on a stopped agent → verify error message
- Concurrent `--wake` messages → verify both messages delivered

## Resolved Decisions (from review feedback)

1. **Stopped agents:** `--wake` targets suspended (paused) agents only. Stopped agents return an error directing the user to `scion start`. ✅ Confirmed.

2. **Post-resume readiness:** Do NOT use a fixed timing delay (e.g. `time.Sleep`). Instead, infer readiness from agent state. The Hub should wait until the agent's phase/activity indicates it is ready to receive messages (e.g., the harness has reported `PhaseRunning` + a non-starting activity via sciontool status hooks). If the current state infrastructure doesn't support inferring readiness precisely enough, file a sub-issue to address that gap — but do not ship a timing-based workaround.

3. **No local-only mode:** The `--wake` feature is Hub/API-only. The local-mode code path in `cmd/message.go` does not need wake support. If `--wake` is used without Hub mode, return an error: `"--wake requires Hub mode"`.

4. **Interface change approach:** Add `wake` as a parameter to the existing `SendStructuredMessage` function in the `AgentService` interface. Do not create a separate method. All existing call sites must be updated to pass `false` for the new parameter. Proper testing is required for the interface change.

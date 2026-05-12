# Design: `--wake` Flag for `scion message` Command

**Issue:** [#26](https://github.com/ptone/scion/issues/26)
**Status:** Draft — Awaiting Approval
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

#### Local mode path

Before attempting message delivery, check the agent's phase and resume if needed:

```go
// Local mode — handle --wake for suspended agents
if msgWake && !msgBroadcast && !msgAll {
    projectDir, _ := config.GetResolvedProjectDir(projectPath)
    if projectDir != "" {
        savedPhase := agent.GetSavedPhase(agentName, projectPath)
        switch savedPhase {
        case string(state.PhaseSuspended):
            fmt.Printf("Waking suspended agent '%s'...\n", agentName)
            if err := RunAgent(cmd, []string{agentName}, true); err != nil {
                return fmt.Errorf("failed to wake agent '%s': %w", agentName, err)
            }
            // Wait briefly for the container to be ready for messages
            // (tmux session initialization after resume)
            time.Sleep(2 * time.Second)
        case string(state.PhaseStopped):
            return fmt.Errorf("agent '%s' is stopped, not suspended — use 'scion start' to start a fresh session", agentName)
        case string(state.PhaseError):
            return fmt.Errorf("agent '%s' is in error state — use 'scion start' to restart", agentName)
        case string(state.PhaseRunning), "":
            // Already running or unknown — proceed to message delivery
        default:
            return fmt.Errorf("agent '%s' is not yet running (phase: %s) — wait for it to reach running state", agentName, savedPhase)
        }
    }
}
```

**Note on local-mode limitation:** `RunAgent()` handles both Hub and local start paths and already contains the suspend→resume detection logic. However, `RunAgent()` may call `startAgentViaHub()` if Hub mode is available, in which case the resume goes through the Hub. The local-only path provisions and starts the container, and the agent manager's `Start()` handles the `opts.Resume = true` case. A more robust implementation would call `mgr.Start()` directly rather than going through `RunAgent()`, but `RunAgent()` captures all the necessary flag resolution and config loading. **Open question: should we extract a lower-level resume helper?**

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

            // Brief pause for the harness to initialize after container resume.
            // The broker's start handler returns once the container is running,
            // but the tmux session inside needs a moment to become responsive.
            time.Sleep(2 * time.Second)

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

**Approach:** Insert a fixed 2-second sleep after the start dispatch returns. This matches the empirical startup time for resumed containers (which skip provisioning and image pull).

**Why not polling?** The Hub doesn't have a direct readiness probe for the harness inside the container. The status reporting mechanism (sciontool hooks) operates asynchronously and the `PhaseRunning` state is set by the broker before the harness sends its first status update. A fixed delay is simpler and sufficient for resume (as opposed to cold start, where provisioning time is unpredictable).

**Future improvement:** The broker could expose a `/readiness` probe that checks tmux session state. This is out of scope for v1.

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

A resume typically completes in 3-5 seconds (no provisioning, no image pull — the container is checkpointed). The 2-second post-resume delay plus message delivery fits well within 30 seconds.

If the resume takes longer than expected (e.g., slow disk I/O on restoring the container checkpoint), the context timeout will cancel the request and the CLI will report a timeout error. The agent may still resume successfully on the broker side — a subsequent `scion message` (without `--wake`) would then succeed.

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
| `cmd/message.go` | Add `--wake` flag, validation rules, pass through to Hub/local paths |
| `pkg/hub/handlers.go` | Add `Wake` field to `MessageRequest`, wake logic in `handleAgentMessage` |
| `pkg/hubclient/agents.go` | Add `wake` parameter to `SendStructuredMessage` |
| `pkg/runtimebroker/types.go` | No changes needed (broker doesn't need wake awareness) |
| `pkg/agent/manager.go` | No changes needed (manager handles running agents only) |

**Callers of `SendStructuredMessage` that need the new parameter added (as `false`):**
- `cmd/message.go` — broadcast fan-out paths (~2 sites)
- `pkg/hub/notifications.go` — notification delivery
- `pkg/hub/messagebroker.go` — message broker fan-out
- `pkg/hub/handlers_broker_inbound.go` — broker inbound relay

## Testing Strategy

### Unit tests
- Flag validation: `--wake` + `--broadcast`, `--wake` + `--raw`, etc. → expect errors
- Hub handler: mock store and dispatcher, test wake-then-message for each phase
- Idempotency: `--wake` on already-running agent → message delivered normally

### Integration tests
- Suspend an agent → `scion message --wake` → verify agent resumes and receives message
- `scion message --wake` on a stopped agent → verify error message
- Concurrent `--wake` messages → verify both messages delivered

## Open Questions

1. **Should `--wake` work for stopped agents?** The issue recommends erroring. An alternative is to start a fresh session and deliver the message, but this conflates "resume preserved state" with "start new session." Recommend: error with a clear message pointing to `scion start`.

2. **Sleep vs. polling for post-resume readiness:** A 2-second fixed delay is proposed. Should we implement a readiness check (e.g., poll the broker for container tmux status)? The fixed delay is simpler but may be too short on slow systems or too long on fast ones.

3. **Should the web UI auto-wake?** When a user types a message to a suspended agent in the web UI, should it automatically set `wake: true`? This is a UX question that can be decided separately.

4. **Local-mode implementation depth:** Should we extract a lower-level `resumeAgent()` helper from `RunAgent()` for local mode, or is calling `RunAgent()` directly acceptable? The current approach reuses `RunAgent()` which handles all flag resolution, but it may also trigger Hub-mode paths if Hub is configured.

# Remote Environment Variable Gathering

## Status
**Proposed**

## 1. Overview

When agents are started locally, environment variables defined in templates/settings are projected from the CLI's current environment into the container. For remote agents started via a Runtime Broker, this flow must be replicated while coordinating between three components: the CLI (user's machine), the Hub (coordinator and env store), and the Runtime Broker (execution host).

This document proposes a multi-phase dispatch flow where:
1. The Hub first resolves what environment variables it can provide from its global and grove-scoped storage.
2. The Broker determines what it can satisfy from its local environment (taking precedence over Hub values).
3. Only if variables remain unsatisfied does the flow return to the CLI for gathering.

## 2. Goals

*   **Parity with Local Mode:** Remote agents should receive the same environment variables they would if started locally.
*   **Layered Resolution:** Environment variables are resolved through a hierarchy: Broker (highest server-side priority) → Hub (grove-scoped, then global) → CLI (fallback).
*   **Hub-First for Server-Side:** The Hub proactively provides its stored env vars to the Broker, minimizing round-trips.
*   **CLI as Last Resort:** The CLI is only prompted for variables that neither the Hub nor Broker can satisfy.
*   **Transparency:** The CLI should clearly show the user what variables are needed, where each was sourced from, and what is missing.
*   **Security:** Environment variable values should not be logged or displayed; only keys are shown during the gathering process.
*   **Minimal Round-Trips:** The flow should be optimized to avoid excessive back-and-forth between components.

## 3. Non-Goals

*   Modifying the local agent start flow.
*   Automatic secret injection from external secret managers (future work).

## 4. Proposed Flow

### 4.1. Sequence Diagram

```
CLI                          Hub                         Broker
 |                            |                            |
 |-- POST /agents ----------->|                            |
 |   (with gather_env=true)   |                            |
 |                            |                            |
 |                            | [Resolve grove + global    |
 |                            |  env, deduplicate]         |
 |                            |                            |
 |                            |-- WS: CreateAgent -------->|
 |                            |   (mode=gather_env,        |
 |                            |    hub_env={...})          |
 |                            |                            |
 |                            |   [Broker checks local env,|
 |                            |    replaces hub values     |
 |                            |    with its own]           |
 |                            |                            |
 |                            |<-- EnvRequirements --------|
 |                            |    {required, hub_has,     |
 |                            |     broker_has, needs}     |
 |                            |                            |
 |   [If needs is empty]      |                            |
 |<-- 200 AgentCreated -------|-- WS: FinalizeAgent ------>|
 |                            |                            |
 |   [If needs is not empty]  |                            |
 |<-- 202 EnvGatherRequest ---|                            |
 |    {required, hub_has,     |                            |
 |     broker_has, needs}     |                            |
 |                            |                            |
 |   [CLI gathers from env]   |                            |
 |   [CLI prints summary]     |                            |
 |                            |                            |
 |-- POST /agents/{id}/env -->|                            |
 |   {gathered_env}           |                            |
 |                            |-- WS: FinalizeAgent ------>|
 |                            |   (env=merged)             |
 |                            |                            |
 |                            |<-- AgentStarted -----------|
 |<-- 200 AgentCreated -------|                            |
```

### 4.2. Step-by-Step Flow

1. **User initiates remote agent start:**
   ```bash
   scion start fooAgent --broker remote "hello world"
   ```

2. **CLI sends creation request to Hub:**
   - Request includes `gather_env: true` flag to trigger the multi-phase flow.
   - Hub does NOT immediately dispatch to the Broker for execution.

3. **Hub resolves its stored environment variables:**
   - Queries global-scoped env vars.
   - Queries grove-scoped env vars for the target grove.
   - Deduplicates: grove-scoped values take precedence over global.
   - Prepares a `hub_env` map of resolved key-value pairs.

4. **Hub dispatches "gather" request to Broker:**
   - Hub sends a `CreateAgent` command with `mode: gather_env` and `hub_env` payload.
   - Broker does NOT start the agent container yet.

5. **Broker determines env requirements:**
   - Loads the template configuration.
   - Extracts all environment variable keys from `scionCfg.Env`.
   - For each required key:
     - If Broker has it locally → use Broker's value (highest priority).
     - Else if Hub provided it → use Hub's value.
     - Else → mark as "needs" (must come from CLI).
   - Returns to Hub:
     - `required`: All env var keys the template needs.
     - `hub_has`: Keys satisfied by Hub-provided values.
     - `broker_has`: Keys the Broker is providing from its local environment.
     - `needs`: Keys that neither Hub nor Broker can satisfy.

6. **Hub evaluates response:**
   - **If `needs` is empty:** All variables are satisfied server-side. Hub immediately sends `FinalizeAgent` to Broker and returns `200 AgentCreated` to CLI.
   - **If `needs` is not empty:** Hub returns HTTP `202 EnvGatherRequest` to CLI with the full breakdown.

7. **CLI gathers from local environment (if needed):**
   - For each key in `needs`, checks `os.Getenv()`.
   - Prints a summary (keys only, not values):
     ```
     Environment variables for agent 'fooAgent':
       Broker provides: ANTHROPIC_API_KEY
       Hub provides:    GITHUB_TOKEN (grove), DATADOG_API_KEY (global)
       Found locally:   AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
       Missing:         SLACK_WEBHOOK_URL

     Warning: 1 required variable is missing. Agent may fail to start.

     Tip: Use 'scion hub env set SLACK_WEBHOOK_URL --grove' to store this
          variable in the Hub for future agent starts.

     Continue? [Y/n]:
     ```

8. **CLI submits gathered env to Hub:**
   - Sends `POST /agents/{id}/env` with key-value pairs.
   - Hub stores these temporarily or forwards immediately.

9. **Hub dispatches final agent creation:**
   - Merges: Hub-stored env + Broker-provided env + CLI-gathered env.
   - Sends `FinalizeAgent` command to Broker with complete env map.

10. **Broker starts the agent:**
    - Uses the merged environment to start the container.
    - Reports agent status back to Hub.

## 5. API Changes

### 5.1. Hub API

#### Create Agent Request (Modified)
```go
type CreateAgentRequest struct {
    // ... existing fields ...

    // GatherEnv triggers the multi-phase env gathering flow.
    // If true, Hub may return 202 with env requirements if CLI input is needed.
    GatherEnv bool `json:"gather_env,omitempty"`
}
```

#### Env Gather Response (New)
```go
type EnvGatherResponse struct {
    // AgentID is the pending agent's ID, used for the follow-up request.
    AgentID string `json:"agent_id"`

    // Required is the full list of env var keys the template needs.
    Required []string `json:"required"`

    // HubHas are keys the Hub is providing from its storage.
    // Each entry includes scope information (global or grove).
    HubHas []EnvSource `json:"hub_has"`

    // BrokerHas are keys the Broker can provide from its environment.
    BrokerHas []string `json:"broker_has"`

    // Needs are keys the CLI should provide (not satisfied by Hub or Broker).
    Needs []string `json:"needs"`
}

type EnvSource struct {
    Key   string `json:"key"`
    Scope string `json:"scope"` // "global" or "grove"
}
```

#### Submit Gathered Env (New Endpoint)
```
POST /api/v1/agents/{id}/env
```
```go
type SubmitEnvRequest struct {
    // Env is the key-value map gathered from the CLI's environment.
    Env map[string]string `json:"env"`

    // Confirm proceeds even if some variables are missing.
    Confirm bool `json:"confirm,omitempty"`
}
```

### 5.2. Broker API (WebSocket Commands)

#### CreateAgent Command (Modified)
```go
type CreateAgentCommand struct {
    // ... existing fields ...

    // Mode can be "execute" (default) or "gather_env".
    Mode string `json:"mode,omitempty"`

    // HubEnv contains environment variables provided by the Hub.
    // The Broker may override these with its own local values.
    HubEnv map[string]string `json:"hub_env,omitempty"`
}
```

#### EnvRequirements Response (New)
```go
type EnvRequirementsResponse struct {
    Required  []string `json:"required"`
    HubHas    []string `json:"hub_has"`    // Keys where Hub value is used
    BrokerHas []string `json:"broker_has"` // Keys where Broker overrides/provides
    Needs     []string `json:"needs"`      // Keys still needed from CLI
}
```

#### FinalizeAgent Command (New)
```go
type FinalizeAgentCommand struct {
    AgentID string            `json:"agent_id"`
    Env     map[string]string `json:"env"`
}
```

### 5.3. CLI Changes

#### New Flag
```bash
scion start <agent> --broker <name> [--no-env-gather] "task"
```
- `--no-env-gather`: Skip the multi-phase flow; use only Hub/Broker-scoped env vars.

#### Environment Gathering Logic
Located in `cmd/start.go`:
1. Detect Hub mode + remote broker.
2. Send request with `gather_env: true`.
3. Handle response:
   - `200`: Agent started immediately (all env satisfied server-side).
   - `202`: Print summary, gather from local env, prompt user if missing vars.
4. Submit gathered env and complete flow.

#### Hub Storage Hint
When variables are missing, CLI should display a helpful message:
```
Tip: Use 'scion hub env set <KEY> --grove' to store this variable
     in the Hub for future agent starts.
```

## 6. Implementation Details

### 6.1. Hub-Side Env Resolution

Before dispatching to the Broker, the Hub must:
1. Query global-scoped env vars from storage.
2. Query grove-scoped env vars for the target grove.
3. Merge with grove-scoped taking precedence over global.
4. Track the scope of each resolved variable for reporting back to CLI.

### 6.2. Broker-Side Template Loading

The Broker must load and parse the template to determine required env vars without starting the container. This requires:
1. Template hydration (already exists for normal flow).
2. Parsing `scionCfg.Env` to extract variable references.
3. Expanding variable names (e.g., `${API_KEY}` → `API_KEY`).
4. Checking Hub-provided values and local environment for each key.

### 6.3. Pending Agent State

When `gather_env: true` and CLI input is needed, the Hub creates an agent record with status `PENDING_ENV`. This agent cannot be started until env is submitted. The record should include a TTL to prevent orphaned pending agents.

### 6.4. Merge Priority

Final environment merge order (highest priority last):
1. Hub global-scoped env
2. Hub grove-scoped env
3. Broker's local environment
4. CLI-gathered environment
5. Agent config overrides

Note: The Broker effectively "wins" over Hub for keys it has locally, but CLI-gathered values take final precedence to allow user overrides.

### 6.5. Fast Path (No CLI Needed)

When the Hub and Broker together satisfy all required variables:
- Hub returns `200` immediately with the created agent.
- No second round-trip to CLI.
- This is the expected common case for well-configured environments.

### 6.6. Non-Interactive Mode

For CI/CD or scripted usage:
```bash
scion start agent --broker remote --no-env-gather "task"
```
Falls back to existing behavior: Broker uses its own env, CLI-provided `--env` flags, and Hub-scoped variables only.

## 7. Security Considerations

### 7.1. Value Transmission

*   Env values are transmitted over HTTPS/WSS (already encrypted in transit).
*   Values are NOT logged by Hub or Broker.
*   CLI displays only keys, never values.

### 7.2. Pending Agent Cleanup

*   Pending agents with status `PENDING_ENV` should expire after a configurable TTL (default: 5 minutes).
*   Expired pending agents are garbage collected.

### 7.3. Sensitive Variable Handling

*   Variables marked as `sensitive: true` in Hub env storage remain write-only.
*   CLI-gathered env is treated as ephemeral and not persisted in Hub storage.

### 7.4. Hub Env Transmission to Broker

*   Hub sends actual values to Broker over the secure WS channel.
*   Broker does not persist or log these values.
*   Values are used only for the current agent start operation.

## 8. User Experience Examples

### 8.1. Happy Path (Fully Satisfied Server-Side)
```bash
$ scion start analyzer --broker prod-k8s "analyze the auth module"

Environment variables for agent 'analyzer':
  Broker provides: ANTHROPIC_API_KEY
  Hub provides:    GITHUB_TOKEN (grove)

Starting agent 'analyzer' on broker 'prod-k8s'...
Agent started successfully.
```

### 8.2. Happy Path (CLI Provides Remaining)
```bash
$ scion start analyzer --broker prod-k8s "analyze the auth module"

Environment variables for agent 'analyzer':
  Broker provides: ANTHROPIC_API_KEY
  Hub provides:    DATADOG_API_KEY (global)
  Found locally:   GITHUB_TOKEN

Starting agent 'analyzer' on broker 'prod-k8s'...
Agent started successfully.
```

### 8.3. Missing Variables (Interactive)
```bash
$ scion start analyzer --broker prod-k8s "analyze the auth module"

Environment variables for agent 'analyzer':
  Broker provides: ANTHROPIC_API_KEY
  Hub provides:    (none)
  Found locally:   (none)
  Missing:         GITHUB_TOKEN

Warning: 1 required variable is missing.

Tip: Use 'scion hub env set GITHUB_TOKEN --grove' to store this
     variable in the Hub for future agent starts.

Continue anyway? [y/N]: n
Aborted.
```

### 8.4. Missing Variables (Forced)
```bash
$ scion start analyzer --broker prod-k8s --force "analyze the auth module"

Environment variables for agent 'analyzer':
  Broker provides: ANTHROPIC_API_KEY
  Hub provides:    (none)
  Found locally:   (none)
  Missing:         GITHUB_TOKEN

Warning: 1 required variable is missing.

Tip: Use 'scion hub env set GITHUB_TOKEN --grove' to store this
     variable in the Hub for future agent starts.

Starting agent anyway (--force specified)...
Agent started successfully.
```

---

## 9. Open Questions

### 9.1. Template Parsing Location

**Question:** Should env var extraction happen on the Broker or the Hub?

*   **Broker:** Has access to template files, knows its local env.
*   **Hub:** Could cache template metadata, reducing Broker round-trip.
*   **Hybrid:** Hub parses template requirements, Broker only reports what it has.

**Recommendation:** Broker parses (it already hydrates templates). Hub should not duplicate template storage.

### 9.2. Variable Reference Expansion

**Question:** How do we handle complex variable references?

Templates may have:
```yaml
env:
  API_KEY: "${ANTHROPIC_API_KEY}"
  FULL_URL: "https://${API_HOST}:${API_PORT}/v1"
```

*   Do we report `API_KEY`, `ANTHROPIC_API_KEY`, or both?
*   For `FULL_URL`, do we report `API_HOST` and `API_PORT` separately?

**Recommendation:** Report the referenced variables (the `${...}` contents), not the target keys.

### 9.3. Partial Availability Across Sources

**Question:** What if Hub has one variable and Broker has another for a multi-reference variable?

Example: Hub has `API_HOST`, Broker has `API_PORT`.

*   Both sources contribute to satisfying `FULL_URL`.
*   The merge logic handles this naturally.

**Recommendation:** Report at the granular level (individual referenced vars), let the system merge.

### 9.4. Timeout and Retry

**Question:** What happens if the CLI never submits the gathered env?

*   **TTL:** Pending agents expire after N minutes.
*   **Resume:** Should CLI be able to resume a pending env gather?

**Recommendation:** Implement TTL with cleanup. Consider a `scion env submit <agent-id>` command for recovery.

### 9.5. Hub Env Caching on Broker

**Question:** Should the Broker cache Hub-provided env vars?

*   Could speed up repeated starts of similar agents.
*   Risk: Cached values become stale.

**Recommendation:** No caching. Hub provides fresh values on each request. This ensures consistency with Hub storage changes.

### 9.6. WebSocket vs HTTP for Gather Phase

**Question:** Should the gather phase use the existing WS control channel or a dedicated HTTP endpoint on the Broker?

*   **WS:** Consistent with existing command dispatch, works through NAT.
*   **HTTP:** Simpler request/response semantics, but requires Broker to be directly reachable.

**Recommendation:** Use WS control channel for consistency and NAT traversal.

### 9.7. Distinction Between "Not Set" and "Empty String"

**Question:** How do we handle env vars that are set to an empty string?

*   `export FOO=""` is different from `unset FOO`.
*   Should both be treated as "doesn't have it"?

**Recommendation:** Treat empty string as "has value" (empty is a valid value). Only report as missing if truly unset.

### 9.8. CLI-Only Variables

**Question:** What about variables that should *only* come from the CLI, never the Hub or Broker?

Example: User-specific credentials that shouldn't be in shared storage.

*   Add a `source: cli_only` annotation to template env definitions?
*   Always prefer CLI value over server-side values for certain keys?

**Recommendation:** Defer to future work. Current merge priority (CLI > Broker > Hub) handles most cases.

### 9.9. Backwards Compatibility

**Question:** How do we handle older CLIs that don't support the gather flow?

*   Older CLIs won't send `gather_env: true`.
*   Hub falls back to existing behavior (immediate dispatch).

**Recommendation:** No breaking changes. The gather flow is opt-in via the flag.

### 9.10. Multi-Broker Coordination

**Question:** If a grove has multiple brokers, should we gather env requirements from all potential brokers before selection?

*   User might choose a broker based on what env vars it already has.
*   Adds complexity and latency.

**Recommendation:** Gather from selected broker only. User selects broker first (via existing multi-broker flow), then env is gathered.

### 9.11. Hub Env Visibility in Gather Response

**Question:** Should the CLI see which scope (global vs grove) each Hub-provided variable came from?

*   Helpful for debugging and understanding configuration.
*   Adds complexity to the response structure.

**Recommendation:** Include scope information. This helps users understand their configuration hierarchy and make informed decisions about where to store new variables.

### 9.12. Broker Override Reporting

**Question:** When the Broker overrides a Hub-provided value, should this be reported to the CLI?

*   Could help debug unexpected behavior.
*   May be confusing if users don't understand the precedence.

**Recommendation:** Report both `hub_has` and `broker_has` separately. If a key appears in `broker_has`, the Broker's value is used regardless of whether Hub also had it. Consider adding a `broker_overrides` list for clarity in future iterations.

# Configurable Lifecycle Hooks for Scion

## Status
**Draft** | May 2026

## Problem

Scion's current lifecycle hook system is powerful but internal: it handles container-level events (pre-start, post-start, pre-stop, session-end) via shell scripts and Go handler functions, all executing inside the agent's container. There is no mechanism for template authors to declaratively attach custom actions to agent lifecycle events — particularly actions that call external APIs, send webhooks, or run scripts with structured configuration.

This gap matters because organizations need to integrate Scion agents into broader infrastructure:

- **Service registries**: Register agents on startup and deregister on shutdown (e.g., Google Cloud Agent Registry, Consul, internal catalogs).
- **Observability**: Notify external monitoring systems when agents transition states.
- **Access management**: Provision or revoke credentials, service accounts, or IAM bindings tied to an agent's lifetime.
- **Audit**: Record lifecycle events to compliance systems outside the Scion Hub.
- **Orchestration**: Trigger downstream workflows when agents complete tasks or reach error states.

Today, achieving any of these requires writing custom shell scripts, baking them into container images, or modifying Scion internals. Template authors cannot express "when this agent starts, call this API" in `scion-agent.yaml`.

### Motivating Example: Google Cloud Agent Registry

The [Google Cloud Agent Registry API](https://cloud.google.com/agent-registry/reference/rest/v1alpha/projects.locations.services) provides a centralized catalog of agent services. A typical integration would:

1. **On agent start** (`post-start`): Call `POST /v1alpha/{parent}/services` to register the agent, providing its name, capabilities, endpoint, and metadata.
2. **On agent stop** (`pre-stop`): Call `DELETE /v1alpha/{name}` to deregister the agent.
3. **On activity change** (`activity-change`): Call `PATCH /v1alpha/{name}` to update the agent's status and availability in the registry.

This requires: authenticated HTTP calls with GCP credentials, structured JSON bodies templated with agent metadata, error handling that doesn't block shutdown, and configuration that lives in the template — not in a custom Docker image.

---

## Current Architecture

### Lifecycle Events (Container-Level)

The `LifecycleManager` (`pkg/sciontool/hooks/lifecycle.go`) manages four container-level events:

| Event | When | Context |
|-------|------|---------|
| `pre-start` | After container setup, before child process starts | Provisioning, secrets resolution |
| `post-start` | Child process confirmed running | Hub reporting begins, heartbeat starts |
| `pre-stop` | SIGTERM/SIGINT received, before graceful shutdown | Time-bounded by grace period |
| `session-end` | After child exits, during cleanup | Final state reporting |

### Harness Events (Runtime)

The `HarnessProcessor` (`pkg/sciontool/hooks/harness.go`) normalizes harness-specific events into a common set:

| Event | Description |
|-------|-------------|
| `session-start` / `session-end` | Harness session lifecycle |
| `agent-start` / `agent-end` | Agent turn lifecycle |
| `tool-start` / `tool-end` | Tool execution |
| `prompt-submit` / `response-complete` | User interaction |
| `model-start` / `model-end` | LLM API calls |
| `notification` | Harness notifications |

### Execution Model

Hook scripts are discovered from ordered directories (`/etc/scion/hooks`, `$HOME/.scion/hooks`) and executed sequentially. Go handlers are registered programmatically. Both receive an `Event` struct with normalized data.

### Phase/Activity State Model

Agents have a layered state: **Phase** (infrastructure lifecycle: `created` → `provisioning` → `starting` → `running` → `stopping` → `stopped` → `error`) and **Activity** (runtime behavior: `idle`, `thinking`, `executing`, `waiting_for_input`, `blocked`, `completed`, `limits_exceeded`, `stalled`, `offline`). Activity is only meaningful when phase is `running`.

---

## Proposal: Template-Defined Lifecycle Hooks

### Design Principles

1. **Declarative over imperative**: Hooks are defined in `scion-agent.yaml`, not as scripts baked into images.
2. **External-first**: The primary use case is calling external systems, not running arbitrary code.
3. **Non-blocking by default**: Hooks should not delay agent lifecycle transitions unless explicitly configured to do so.
4. **Auth-aware**: Hooks inherit the agent's resolved credentials or can specify their own.
5. **Fail-safe**: Hook failures are logged but do not kill the agent unless the template author opts in.
6. **Composable**: Multiple hooks can attach to the same event; they execute in declaration order.

### Hook Definition Format

Add a `lifecycle_hooks` field to `scion-agent.yaml`:

```yaml
harness: claude
image: scion-claude:latest

lifecycle_hooks:
  - name: register-agent
    on: [post-start]
    action:
      type: http
      method: POST
      url: "https://agentregistry.googleapis.com/v1alpha/projects/${GCP_PROJECT}/locations/${GCP_REGION}/services"
      headers:
        Content-Type: application/json
      body: |
        {
          "displayName": "${AGENT_NAME}",
          "description": "Scion agent: ${TEMPLATE_NAME}",
          "labels": {
            "scion-grove": "${GROVE_NAME}",
            "scion-template": "${TEMPLATE_NAME}"
          }
        }
      auth: gcp-default
    on_error: log
    timeout: 10s

  - name: deregister-agent
    on: [pre-stop]
    action:
      type: http
      method: DELETE
      url: "https://agentregistry.googleapis.com/v1alpha/projects/${GCP_PROJECT}/locations/${GCP_REGION}/services/${AGENT_SLUG}"
      auth: gcp-default
    on_error: log
    timeout: 5s

  - name: update-registry-status
    on: [activity-change]
    action:
      type: http
      method: PATCH
      url: "https://agentregistry.googleapis.com/v1alpha/projects/${GCP_PROJECT}/locations/${GCP_REGION}/services/${AGENT_SLUG}"
      headers:
        Content-Type: application/json
      body: |
        {
          "annotations": {
            "scion-activity": "${ACTIVITY}",
            "scion-phase": "${PHASE}"
          }
        }
      auth: gcp-default
    on_error: log
    timeout: 5s
    debounce: 10s

  - name: notify-slack
    on: [session-end]
    action:
      type: webhook
      url: "${SLACK_WEBHOOK_URL}"
      body: |
        {
          "text": "Agent ${AGENT_NAME} session ended (exit: ${EXIT_CODE})"
        }
    on_error: log
    timeout: 5s

  - name: cleanup-credentials
    on: [pre-stop]
    action:
      type: script
      command: ["/usr/local/bin/revoke-sa-keys.sh"]
      env:
        SERVICE_ACCOUNT: "${GCP_SERVICE_ACCOUNT}"
    on_error: log
    timeout: 30s
    blocking: true
```

### Schema Definition

```go
// LifecycleHookSpec defines a single lifecycle hook in scion-agent.yaml.
type LifecycleHookSpec struct {
    Name     string           `json:"name" yaml:"name"`
    On       []string         `json:"on" yaml:"on"`
    Action   HookAction       `json:"action" yaml:"action"`
    OnError  HookErrorPolicy  `json:"on_error,omitempty" yaml:"on_error,omitempty"`
    Timeout  string           `json:"timeout,omitempty" yaml:"timeout,omitempty"`
    Blocking bool             `json:"blocking,omitempty" yaml:"blocking,omitempty"`
    Debounce string           `json:"debounce,omitempty" yaml:"debounce,omitempty"`
    Condition string          `json:"condition,omitempty" yaml:"condition,omitempty"`
}

// HookAction defines what a hook does when triggered.
type HookAction struct {
    Type    HookActionType    `json:"type" yaml:"type"`
    // HTTP/Webhook fields
    Method  string            `json:"method,omitempty" yaml:"method,omitempty"`
    URL     string            `json:"url,omitempty" yaml:"url,omitempty"`
    Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
    Body    string            `json:"body,omitempty" yaml:"body,omitempty"`
    Auth    string            `json:"auth,omitempty" yaml:"auth,omitempty"`
    // Script fields
    Command []string          `json:"command,omitempty" yaml:"command,omitempty"`
    Env     map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
}

type HookActionType string
const (
    HookActionHTTP    HookActionType = "http"
    HookActionWebhook HookActionType = "webhook"
    HookActionScript  HookActionType = "script"
)

type HookErrorPolicy string
const (
    HookErrorLog   HookErrorPolicy = "log"   // Log and continue (default)
    HookErrorFail  HookErrorPolicy = "fail"  // Abort lifecycle transition
    HookErrorRetry HookErrorPolicy = "retry" // Retry with backoff (max 3)
)
```

### Supported Lifecycle Events

Template-defined hooks can attach to events from both layers:

**Container Lifecycle Events** (Phase transitions):

| Event | Description | Available Context |
|-------|-------------|-------------------|
| `pre-start` | Before child process starts | Agent name, template, image, grove |
| `post-start` | Child process running | All above + container ID, phase=running |
| `pre-stop` | Shutdown signal received | All above + current activity |
| `session-end` | After child exits | All above + exit code, final activity |
| `phase-change` | Any phase transition | Previous phase, new phase |

**Runtime Events** (Activity transitions):

| Event | Description | Available Context |
|-------|-------------|-------------------|
| `activity-change` | Agent activity changed | Previous activity, new activity, tool name |
| `task-completed` | Agent reports task done | Task summary, assistant text |
| `limits-exceeded` | Agent hit max turns/calls/duration | Which limit, current count |
| `error` | Unrecoverable error | Error message, phase at failure |

### Variable Substitution

Hook URLs, bodies, and headers support variable substitution via `${VAR_NAME}`. Variables are resolved from multiple sources in priority order:

1. **Event context** (highest priority):
   - `${PHASE}`, `${ACTIVITY}`, `${PREVIOUS_PHASE}`, `${PREVIOUS_ACTIVITY}`
   - `${TOOL_NAME}`, `${EXIT_CODE}`, `${ERROR_MESSAGE}`
   - `${TASK_SUMMARY}`, `${ASSISTANT_TEXT}`

2. **Agent metadata**:
   - `${AGENT_NAME}`, `${AGENT_SLUG}`, `${AGENT_ID}`
   - `${TEMPLATE_NAME}`, `${HARNESS_NAME}`, `${HARNESS_CONFIG}`
   - `${GROVE_NAME}`, `${GROVE_ID}`, `${GROVE_PATH}`
   - `${CONTAINER_ID}`, `${IMAGE}`

3. **Environment variables** (from template env, runtime env):
   - `${GCP_PROJECT}`, `${SLACK_WEBHOOK_URL}`, etc.
   - Any key from `scion-agent.yaml`'s `env:` block
   - Container environment variables

Unresolved variables expand to empty string and emit a warning log.

---

## Execution Mechanism

### Architecture

Lifecycle hooks execute inside the agent's container, managed by `sciontool init`. This is the natural location because:

- Hooks run in the same security context as the agent (same credentials, network, filesystem).
- The `LifecycleManager` already orchestrates lifecycle events here.
- No external orchestration is needed — the container manages itself.

```
sciontool init
  ├── LifecycleManager (existing)
  │   ├── Script hooks (/etc/scion/hooks, $HOME/.scion/hooks)
  │   └── Go handlers (status, logging, telemetry, hub)
  │
  └── TemplateHookExecutor (new)
      ├── Reads lifecycle_hooks from scion-agent.yaml
      ├── Registers itself with LifecycleManager for each event
      ├── On event:
      │   ├── Resolve variables from event context + agent metadata
      │   ├── Dispatch action (HTTP client, script exec)
      │   ├── Apply timeout, retry, debounce policies
      │   └── Report result (log, fail, or retry)
      └── HTTP client with auth plugin system
```

### HTTP Action Execution

```
1. Resolve URL, headers, body variables
2. Acquire auth token (see Auth Handling below)
3. Execute HTTP request with configured timeout
4. Check response status:
   - 2xx: Success, log response
   - 4xx: Permanent failure, apply on_error policy
   - 5xx: Transient failure, apply on_error policy (retry if configured)
5. If on_error=retry: exponential backoff (1s, 2s, 4s), max 3 attempts
```

### Webhook Action Execution

`webhook` is a convenience alias for `http` with `method: POST` and no auth. It's for simple webhook endpoints (Slack, PagerDuty, etc.) that authenticate via URL token.

### Script Action Execution

Script hooks run as the agent's user (not root), with the configured environment overlay. They inherit the same environment as harness pre-start scripts but execute at the specified lifecycle event. Script stdout/stderr is captured to the agent's hook log file.

### Blocking vs Non-Blocking

By default, hooks are **non-blocking**: they fire asynchronously and do not delay the lifecycle transition. When `blocking: true`:

- The lifecycle transition waits for the hook to complete (or timeout).
- This is appropriate for `pre-stop` hooks that need to deregister before shutdown.
- `post-start` blocking hooks delay the "running" status report to the Hub.
- **Caution**: Blocking hooks on `pre-stop` consume time from the supervisor's grace period. A 30s blocking hook with a 10s grace period will be killed after 10s.

### Debounce

The `debounce` field is relevant for high-frequency events like `activity-change`. When set, rapid-fire events are coalesced: only the last event in the debounce window triggers the hook. This prevents flooding external APIs with status updates during fast activity transitions (e.g., `idle` → `thinking` → `executing` within 100ms).

---

## Auth Handling

### Auth Strategies

Hooks can specify an `auth` field that selects a credential strategy:

| Auth Value | Description | Credential Source |
|------------|-------------|-------------------|
| `gcp-default` | GCP Application Default Credentials | ADC file, metadata server, or Workload Identity |
| `gcp-service-account` | GCP service account key | Secret mounted as file |
| `bearer-token` | Static bearer token | Environment variable |
| `api-key-header` | API key in header | Environment variable |
| `none` (default) | No auth | N/A |

### GCP Auth Resolution

For `gcp-default`, the hook executor reuses the existing auth resolution chain from the harness system (`pkg/api/types.go:AuthConfig`). The resolution order:

1. **Workload Identity** (Kubernetes): Service account token from pod metadata.
2. **GCP Metadata Server**: When `GCPMetadataMode=assign`, fetch token from `http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token`.
3. **ADC File**: Read `~/.config/gcloud/application_default_credentials.json` and exchange for access token.
4. **Service Account Key**: Read key file and sign JWT for token exchange.

The hook executor maintains a token cache with automatic refresh (tokens are refreshed 5 minutes before expiry). This is implemented as a thin wrapper around `golang.org/x/oauth2/google`.

### Auth in Template Config

```yaml
lifecycle_hooks:
  - name: register-agent
    on: [post-start]
    action:
      type: http
      url: "https://agentregistry.googleapis.com/v1alpha/..."
      auth: gcp-default  # Uses resolved GCP credentials

  - name: notify-monitoring
    on: [session-end]
    action:
      type: http
      url: "https://monitoring.internal/api/events"
      auth: bearer-token  # Uses $MONITORING_BEARER_TOKEN env var
      headers:
        Authorization: "Bearer ${MONITORING_BEARER_TOKEN}"

  - name: ping-webhook
    on: [post-start]
    action:
      type: webhook  # No auth needed — URL contains the secret
      url: "${SLACK_WEBHOOK_URL}"
```

### Secret Access

Hook auth credentials follow the existing secret resolution chain:

1. Template-level `secrets:` block in `scion-agent.yaml` (projected as env vars or files).
2. Agent-level environment variables from `env:` block.
3. Runtime environment from container launch.
4. GCP metadata server (when available).

Hooks should never log credential values. The executor redacts `Authorization` headers and request bodies containing known secret patterns in all log output.

---

## Error Semantics

### Error Policies

Each hook specifies an `on_error` policy:

| Policy | Behavior | Use Case |
|--------|----------|----------|
| `log` (default) | Log error, continue lifecycle transition | Non-critical notifications, status updates |
| `fail` | Abort the lifecycle transition, set agent to error phase | Critical registration that must succeed |
| `retry` | Retry with exponential backoff (1s, 2s, 4s), then fall back to `log` | Transient failures against reliable APIs |

### Timeout Handling

- Default timeout: 10s for HTTP actions, 30s for script actions.
- Maximum timeout: 120s (enforced at validation time).
- Timeouts on `pre-stop` hooks are additionally bounded by the supervisor grace period.
- Timeout expiry triggers the `on_error` policy (not a separate error path).

### Error Reporting

Hook execution results are reported through:

1. **Agent log** (`$HOME/agent.log`): All hook executions, successes, and failures.
2. **Telemetry** (OpenTelemetry spans): `agent.lifecycle_hook` span with hook name, event, duration, status.
3. **Hub status** (via existing `HubHandler`): When `on_error=fail` triggers error phase, the hub receives the phase change.

### Idempotency

Template authors are responsible for ensuring hook actions are idempotent. The system makes this easier by:

- Providing a stable `${AGENT_ID}` for use as an idempotency key.
- Including a `X-Scion-Hook-ID: ${AGENT_ID}:${HOOK_NAME}:${EVENT}:${TIMESTAMP}` header on HTTP requests.
- Documenting that `retry` policy may cause duplicate calls.

---

## Validation

`scion-agent.yaml` validation (at template load time) enforces:

```go
func ValidateLifecycleHooks(hooks []LifecycleHookSpec) error {
    // 1. Name uniqueness
    // 2. At least one event in 'on'
    // 3. All events are valid (from supported events list)
    // 4. Action type is valid (http, webhook, script)
    // 5. HTTP actions require url; script actions require command
    // 6. Timeout parses as valid duration, <= 120s
    // 7. Debounce parses as valid duration (only for activity-change, phase-change)
    // 8. on_error is valid (log, fail, retry)
    // 9. Webhook type does not specify method (always POST)
    // 10. Auth value is a recognized strategy
}
```

---

## Interaction with Existing Systems

### Relationship to Script Hooks

Template-defined lifecycle hooks complement, not replace, the existing script hook system:

| Aspect | Script Hooks | Template Lifecycle Hooks |
|--------|-------------|------------------------|
| Definition | Files in `/etc/scion/hooks/`, `$HOME/.scion/hooks/` | `lifecycle_hooks:` in `scion-agent.yaml` |
| Scope | System-wide or per-agent (via image) | Per-template (travels with template) |
| Execution | Sequential, blocking | Non-blocking by default, configurable |
| Capabilities | Arbitrary code | HTTP calls, webhooks, scripts |
| Auth | Manual (env vars) | Declarative (auth strategies) |
| Error handling | Exit code → fatal | Configurable (log, fail, retry) |

**Execution order**: Existing script hooks run first (they are part of the LifecycleManager's script discovery). Template lifecycle hooks run after, via a registered Go handler.

### Relationship to Hub Reporting

The Hub already receives phase and activity changes. Template hooks are a separate channel for external integrations that are not part of the Hub's purview. They do not replace Hub reporting — they augment it with template-author-defined side effects.

### Relationship to Services

Services (`scion-agent.yaml` `services:` block) are long-running sidecar processes. Lifecycle hooks are event-driven, short-lived actions. They are orthogonal concerns. However, a hook might interact with a service (e.g., "on post-start, tell the sidecar MCP server to reload config").

---

## Concrete Example: Agent Registry Integration

### Template Configuration

```yaml
# scion-agent.yaml
harness: claude
image: scion-claude:latest
default_harness_config: claude-web

env:
  GCP_PROJECT: my-project
  GCP_REGION: us-central1
  AGENT_REGISTRY_PARENT: "projects/my-project/locations/us-central1"

secrets:
  - key: GOOGLE_APPLICATION_CREDENTIALS
    description: "GCP service account for Agent Registry"
    type: file

lifecycle_hooks:
  - name: register-with-agent-registry
    on: [post-start]
    action:
      type: http
      method: POST
      url: "https://agentregistry.googleapis.com/v1alpha/${AGENT_REGISTRY_PARENT}/services"
      headers:
        Content-Type: application/json
      body: |
        {
          "serviceId": "${AGENT_SLUG}",
          "service": {
            "displayName": "${AGENT_NAME}",
            "description": "Scion agent running template ${TEMPLATE_NAME}",
            "serviceEndpoint": {
              "uri": "scion://${GROVE_NAME}/${AGENT_SLUG}"
            },
            "labels": {
              "managed-by": "scion",
              "grove": "${GROVE_NAME}",
              "template": "${TEMPLATE_NAME}",
              "harness": "${HARNESS_NAME}"
            },
            "annotations": {
              "scion.dev/agent-id": "${AGENT_ID}",
              "scion.dev/container-id": "${CONTAINER_ID}",
              "scion.dev/image": "${IMAGE}"
            }
          }
        }
      auth: gcp-default
    on_error: retry
    timeout: 15s
    blocking: true

  - name: update-registry-activity
    on: [activity-change]
    action:
      type: http
      method: PATCH
      url: "https://agentregistry.googleapis.com/v1alpha/${AGENT_REGISTRY_PARENT}/services/${AGENT_SLUG}"
      headers:
        Content-Type: application/json
      body: |
        {
          "service": {
            "annotations": {
              "scion.dev/activity": "${ACTIVITY}",
              "scion.dev/phase": "${PHASE}",
              "scion.dev/last-updated": "${TIMESTAMP}"
            }
          },
          "updateMask": "annotations"
        }
      auth: gcp-default
    on_error: log
    timeout: 5s
    debounce: 15s

  - name: deregister-from-agent-registry
    on: [pre-stop]
    action:
      type: http
      method: DELETE
      url: "https://agentregistry.googleapis.com/v1alpha/${AGENT_REGISTRY_PARENT}/services/${AGENT_SLUG}"
      auth: gcp-default
    on_error: log
    timeout: 10s
    blocking: true
```

### Execution Flow

```
Container starts
  │
  ├─ sciontool init
  │   ├─ Load scion-agent.yaml → parse lifecycle_hooks
  │   ├─ Create TemplateHookExecutor
  │   ├─ Register executor with LifecycleManager for: post-start, activity-change, pre-stop
  │   │
  │   ├─ RunPreStart() [existing hooks only]
  │   ├─ Start child process (claude)
  │   ├─ RunPostStart()
  │   │   ├─ Existing script hooks...
  │   │   └─ TemplateHookExecutor: "register-with-agent-registry"
  │   │       ├─ Resolve variables: ${AGENT_SLUG}=my-agent, ${GROVE_NAME}=my-grove, ...
  │   │       ├─ Acquire GCP access token via ADC
  │   │       ├─ POST https://agentregistry.googleapis.com/v1alpha/.../services
  │   │       ├─ Response 200 → log success
  │   │       └─ (blocking=true, so post-start waits for completion)
  │   │
  │   ├─ Report "running" to Hub
  │   ├─ Start heartbeat loop
  │   │
  │   ├─ [Agent runs, activity changes fire...]
  │   │   └─ TemplateHookExecutor: "update-registry-activity" (debounced 15s)
  │   │       ├─ Coalesce rapid transitions
  │   │       ├─ PATCH .../services/my-agent with latest activity
  │   │       └─ (non-blocking, fire-and-forget)
  │   │
  │   ├─ [SIGTERM received]
  │   ├─ RunPreStop()
  │   │   ├─ Existing script hooks...
  │   │   └─ TemplateHookExecutor: "deregister-from-agent-registry"
  │   │       ├─ DELETE .../services/my-agent
  │   │       ├─ Response 200 → log success
  │   │       └─ (blocking=true, waits before proceeding to shutdown)
  │   │
  │   ├─ Supervisor: SIGTERM → child → grace period → SIGKILL
  │   ├─ RunSessionEnd()
  │   └─ Report "stopped" to Hub
```

---

## Implementation Plan (High-Level)

### Phase 1: Core Infrastructure

1. **Add `LifecycleHookSpec` to `pkg/api/types.go`** alongside existing `ServiceSpec`, `MCPServerConfig`.
2. **Add `lifecycle_hooks` field to `ScionConfig`** with YAML/JSON tags.
3. **Add validation** in `ValidateLifecycleHooks()` following the pattern of `ValidateServices()` and `ValidateMCPServers()`.
4. **Implement `TemplateHookExecutor`** in `pkg/sciontool/hooks/template_hooks.go`:
   - Variable resolution engine.
   - HTTP client with timeout and retry.
   - Script executor (reuse existing `executeScript` pattern).
5. **Register executor in `sciontool init`** (`cmd/sciontool/commands/init.go`): parse hooks from loaded config, register with `LifecycleManager`.

### Phase 2: Auth and Advanced Features

6. **Auth plugin system** in `pkg/sciontool/hooks/auth/`: GCP default, bearer token, API key.
7. **Debounce support** for high-frequency events.
8. **Telemetry integration**: Emit `agent.lifecycle_hook` spans via existing telemetry handler pattern.
9. **Hub handler integration**: Register template hooks with the `HarnessProcessor` for runtime events (activity-change, task-completed).

### Phase 3: UX and Observability

10. **CLI support**: `scion hooks list <agent>` to show configured hooks and recent execution history.
11. **Web UI**: Hook execution log viewer in the agent detail panel.
12. **Dry-run mode**: `scion hooks test <agent> <event>` to preview variable resolution and show what would be called.

---

## Trade-offs and Alternatives Considered

### 1. Hooks Inside Container vs. Outside (Hub-Side)

**Chosen: Inside container.**

| Aspect | Inside Container | Hub-Side |
|--------|-----------------|----------|
| Auth access | Direct (same credentials, ADC, metadata server) | Requires credential forwarding |
| Network | Agent's network (VPC, service mesh) | Hub's network (may not reach internal APIs) |
| Latency | Low (co-located) | Higher (Hub → external API) |
| Reliability | Tied to container lifetime | Independent of container |
| Orphan cleanup | No deregister if container killed | Hub can deregister on container death |
| Complexity | Lower (extends existing LifecycleManager) | Higher (new Hub subsystem) |

**Risk**: If the container is force-killed (SIGKILL, OOM), `pre-stop` hooks don't run. Mitigation: the Hub can run a reconciliation loop that detects stale registrations and cleans them up. This is documented as a known limitation and addressed in Phase 3 with a Hub-side "orphan reaper" for registered services.

### 2. YAML DSL vs. Script-Only Hooks

**Chosen: YAML DSL with script escape hatch.**

A pure script approach (just add more script hooks) is simpler but:
- Requires baking scripts into images or mounting them.
- Doesn't support auth natively — every script reimplements token acquisition.
- Doesn't support debounce, retry, or non-blocking execution.
- Doesn't travel with the template.

The YAML DSL handles the common case (HTTP calls) declaratively while allowing scripts for complex logic.

### 3. Variable Substitution: Simple `${VAR}` vs. Full Templating (Go templates, CEL)

**Chosen: Simple `${VAR}` substitution.**

Go templates or CEL expressions would be more powerful but:
- Increase the attack surface (template injection, resource exhaustion).
- Add cognitive load for template authors.
- Are harder to validate statically.
- Are rarely needed — most hooks just need agent metadata interpolated into URLs and JSON bodies.

If conditional logic is needed, the `condition` field (Phase 2) supports simple equality checks: `condition: "ACTIVITY == completed"`. Complex conditions should use a script action.

### 4. Blocking Semantics

**Chosen: Non-blocking by default, opt-in blocking.**

Making all hooks blocking would be safest (no orphan registrations) but would slow every lifecycle transition. Non-blocking by default with opt-in blocking gives template authors control:
- `post-start` + `blocking: true` for registration that must complete before the agent is considered "running."
- `pre-stop` + `blocking: true` for deregistration that must complete before shutdown.
- `activity-change` + `blocking: false` (always) for status updates that are best-effort.

### 5. Event Granularity: Phase-Only vs. Phase + Activity

**Chosen: Both phase and activity events.**

Phase-only events would be simpler but miss the Agent Registry status-update use case. Activity events (especially `activity-change`) enable real-time status synchronization with external systems. The debounce mechanism prevents these high-frequency events from overwhelming external APIs.

### 6. Retry Semantics

**Chosen: Simple fixed-attempt retry (max 3) with exponential backoff.**

More sophisticated retry (configurable count, jitter, circuit breaker) adds complexity that's rarely needed for lifecycle hooks. If an API is consistently failing, 3 retries with backoff (1s, 2s, 4s = 7s total) is enough to handle transient blips without delaying lifecycle transitions.

---

## Open Questions

1. **Hub-side orphan reaper**: Should the Hub automatically detect agents that registered with external services but were killed without running `pre-stop`? This requires the Hub to understand hook semantics, which violates the "hooks are template-defined" principle. Alternative: a separate `scion reconcile` command that template authors run periodically.

2. **Hook ordering across templates**: If a template inherits from a parent template that also defines lifecycle hooks, should hooks be merged (parent first, child second) or should the child replace the parent's hooks? Recommendation: merge with override-by-name (child hook with same name replaces parent).

3. **Cross-agent hooks**: Should a hook on Agent A be able to trigger actions on Agent B (e.g., "when this agent completes, start another agent")? This is orchestration territory and may be better served by a separate mechanism (parent agent, Hub automation rules). Recommendation: out of scope for v1.

4. **Response capture**: Should HTTP hook responses be captured and made available as variables for subsequent hooks? This would enable chains like "register → capture service ID → use ID in status updates." Recommendation: yes for Phase 2, capture response body in `${HOOK_<name>_RESPONSE}` variable.

5. **Rate limiting**: Should the system enforce global rate limits on hook HTTP calls to protect external APIs? Or is per-hook debounce sufficient? Recommendation: per-hook debounce is sufficient for v1; global rate limiting is a Phase 3 concern.

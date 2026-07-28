---
title: Agent Configuration (scion-agent.yaml)
description: Reference for Scion agent templates and configuration.
---

The `scion-agent.yaml` file acts as the blueprint for an agent. It defines the environment, resources, and harness configuration required to run the agent.

## File Locations

- **Templates**: `.scion/templates/<template-name>/scion-agent.yaml`
- **Active Agents**: `.scion/agents/<agent-name>/scion-agent.yaml`

:::note[Format Change]
Previous versions of Scion used `scion-agent.json`. The new versioned settings system uses `scion-agent.yaml`, though JSON is still supported as valid YAML.
:::

## Configuration Fields

### Core Fields

| Field | Type | Description |
| :--- | :--- | :--- |
| `schema_version` | string | Should be `"1"`. |
| `default_harness_config` | string | The name of the default harness config to use (e.g., `gemini`, `claude`). |
| `agent_instructions` | string | Role-specific instructions for the agent (harness-agnostic). |
| `system_prompt` | string | The system prompt to use for the agent (harness-agnostic). |
| `image` | string | Override the container image defined in the harness config. |
| `env` | map | Environment variables to inject into the container. |
| `volumes` | list | Additional volume mounts. |
| `detached` | bool | Run in background (default `true`). |
| `command_args` | list | Additional arguments passed to the harness entrypoint. |
| `task_flag` | string | CLI flag name for passing the task (e.g., `--input`). When set, the task is delivered as a flag value instead of a positional argument. |
| `model` | string | LLM model identifier override. |

:::caution[Harness Field Deprecated]
The `harness` field is no longer supported in `scion-agent.yaml`. Templates must be harness-agnostic. Use `default_harness_config` to specify a preferred harness, which can be overridden by users at runtime.
:::

### Platform and Workspace Skills

Scion automatically injects contextual platform skills into the agent's environment during provisioning:

- **`git-sandbox`**: Injected if the agent is running in a Git-backed workspace. Provides operational context for git worktree and branch management.
- **Platform Skills**: Core instructions (such as status signaling, messaging, and command operations) are dynamically injected to guide the agent in interacting with the system.

These platform skills are managed by Scion and do not need to be manually included in your template definition.

### Limits & Resources

| Field | Type | Description |
| :--- | :--- | :--- |
| `max_turns` | int | Maximum number of LLM turns before the agent stops. Exceeding this triggers a `LIMITS_EXCEEDED` state and termination. |
| `max_duration` | string | Maximum runtime duration (e.g., `"2h"`, `"30m"`). Exceeding this triggers a `LIMITS_EXCEEDED` state and termination. |
| `resources` | object | Container resource requests/limits (see below). |

### Resource Specification

```yaml
resources:
  requests:
    cpu: "500m"
    memory: "512Mi"
  limits:
    cpu: "2"
    memory: "2Gi"
  disk: "10Gi"
```

### Sidecar Services (`services`)

Define auxiliary containers to run alongside the agent (e.g., a headless browser).

```yaml
services:
  - name: browser
    command: ["chromium", "--headless"]
    env:
      DISPLAY: ":99"
    ready_check:
      type: tcp
      target: "localhost:9222"
  - name: delayed-job
    command: ["./worker.sh"]
    ready_check:
      type: delay
      target: "5s"
```

| Field | Type | Description |
| :--- | :--- | :--- |
| `name` | string | **Required**. Service name. |
| `command` | list | **Required**. Entrypoint and arguments. |
| `restart` | string | Restart policy: `no`, `always`, or `on-failure`. |
| `env` | map | Environment variables for the service. |
| `ready_check` | object | Health check to determine if the service is ready. |

#### Ready Check (`ready_check`)

| Field | Type | Description |
| :--- | :--- | :--- |
| `type` | string | `tcp`, `http`, or `delay`. |
| `target` | string | Host:port (tcp/http) or duration (delay). |
| `timeout` | string | Maximum wait time. |

### Hub Override (`hub`)

Specify a different Hub endpoint for this agent.

```yaml
hub:
  endpoint: "https://hub.example.com"
```

### Required Secrets (`secrets`)

Define secrets required by the agent. These follow the same schema as [Orchestrator Settings Secrets](/scion/reference/orchestrator-settings/#required-secrets).

### Gemini Settings (`gemini`)

Harness-specific settings for Gemini.

```yaml
gemini:
  auth_selectedType: "vertex-ai"
```

### Telemetry (`telemetry`)

Override telemetry settings for this template or agent. These merge on top of any telemetry configuration defined in `settings.yaml` (global or project scope), using last-write-wins semantics.

```yaml
telemetry:
  enabled: true
  cloud:
    endpoint: "monitoring.googleapis.com:443"
  filter:
    events:
      exclude:
        - "agent.user.prompt"
  resource:
    service.name: "my-specialized-agent"
```

See the [Orchestrator Settings Reference](/scion/reference/orchestrator-settings/#telemetry-configuration-telemetry) for the full field reference and the [Metrics guide](/scion/hosted/single-node/metrics/#configuration-hierarchy) for how telemetry settings merge across scopes.

### Kubernetes Specifics (`kubernetes`)

Overrides for Kubernetes runtimes.

```yaml
kubernetes:
  namespace: "custom-ns"
  serviceAccountName: "workload-identity-sa"
  runtimeClassName: "gvisor"
```

## Resolution Logic

When an agent starts, Scion loads `scion-agent.yaml` from the selected template,
resolves each setting through the precedence chain below, and writes the result to
the agent's runtime directory.

### Resolution precedence

Every agent setting that can be specified in more than one place — `harness_config`,
`model`, `max_turns`, `max_model_calls`, `max_duration` and `resources` — is
resolved with the same ordering, highest priority first:

1.  **Explicit agent-create request** — the value passed to `scion agent create`
    (or the equivalent API field / web form field).
2.  **Project setting** — the project's default, set in Project Settings and
    stored as a `scion.io/default-*` annotation on the project.
3.  **Template** — the value in the selected template's `scion-agent.yaml`.
4.  **Harness config** — the selected harness config's own defaults.
5.  **Profile** — the active profile in `settings.yaml`.
6.  **Hub default** — the hub's agent defaults, set in the admin server config.
    These are currently resolved broker-side, from the settings the hub writes to
    `settings.yaml`, so they apply on deployments where the broker reads that file.
7.  **Compiled default** — the value built into Scion.

A project setting is an *override*: it takes precedence over the template a project
uses. Leave a project setting blank to let the template (and then the rest of the
chain) decide.

:::note[How the ordering is enforced, and where it can bite]
Levels 1–3 are resolved by the hub, which stamps the winning value onto the agent
before dispatch. The broker receives that value in its *highest-priority* slot, which
is what makes it outrank levels 4–7 end-to-end. Levels 4–7 are resolved broker-side
from `settings.yaml`.

The consequence worth knowing: **any value the hub stamps outranks levels 4–6,
regardless of which tier it came from.** The ordering above holds only because the
hub stamps values from levels 1–3, which already sit above them. It is not a
general-purpose guarantee that a lower level can never overtake a higher one — read
it as the intended ordering for the fields listed, not as a description of every
transport path. Per-field deviations exist; `resources` in particular can be
overridden by a profile's harness overrides.
:::

:::caution[Behaviour change]
Before this change, `harness_config` departed from the ordering above at levels 2
and 3: the template was resolved ahead of the project's
`scion.io/default-harness-config` annotation, so the template always won. It now
follows the documented ordering, as the other project settings already did. This only affects projects that set **both** a default harness config
**and** a template naming a *different* harness config; new agents in those projects
receive the project's harness config. Projects that set only one of the two are
unaffected, and already-created agents keep the configuration they were created with.

Because the project setting is now load-bearing, check that it names a harness config
that actually exists. The value is not validated when you save Project Settings, and a
stale or misspelled name used to be harmless — the template supplied a working value
instead. It now displaces that template value, and the agent is dispatched without a
harness-config ID, leaving a remote broker unable to fetch it from Hub storage. The hub
logs a `harness config not found in project or global scope` warning when this happens.
:::

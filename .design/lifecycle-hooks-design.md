# Configurable Agent Lifecycle Hooks — Design

## Status
**Draft (revised approach)** | June 2026

This supersedes the earlier template-defined draft (on branch `scion/lifecycle-design`,
`.design/lifecycle-hooks-design.md`). That draft proposed hooks declared in
`scion-agent.yaml` and executed inside the agent container. A design discussion
(issue #35) reworked the approach: hooks are **stored in the Hub database**,
authored by **hub administrators**, evaluated **Hub-side** at authoritative phase
transitions, and executed with a **project integration service account**. This
document records the revised design and the reasoning that led to it.

---

## Problem

Organizations need to integrate Scion agents into external infrastructure tied to
the agent's lifetime:

- **Service registries** — register an agent on start, deregister on stop (e.g.
  Google Cloud Agent Registry, Consul, internal catalogs).
- **Observability** — notify external systems on lifecycle transitions.
- **Access management** — provision/revoke credentials or IAM bindings.
- **Audit** — record lifecycle events to external compliance systems.

The motivating case: **a hub administrator wants every agent on the hub registered
in an external registry**, where the hub (not the agent) is the only authorized
caller of the registry API. The agent itself has no business holding that
credential.

---

## Why not the original (template-defined, in-container) approach

The discussion surfaced three problems with shipping hooks in templates and running
them in-container. Each pushed the design toward the revised model.

### 1. Two execution models, not one
Container lifecycle events (`pre-start`/`post-start`/`pre-stop`/`session-end`) run
in the long-lived `sciontool init` process (`pkg/sciontool/hooks/lifecycle.go`).
Runtime events (activity changes, tool events) are fired as **separate, short-lived
`sciontool hook` invocations** (`cmd/sciontool/commands/hook.go`). The original
draft's debounce, token caching, and async/non-blocking semantics only hold for the
first; they silently break for the second. Treating both uniformly was incorrect.

### 2. State is multi-tier and split across execution domains
Agent state has two tiers (`pkg/agent/state/state.go`): **Phase** (infrastructure
lifecycle, platform-owned) and **Activity** (runtime behavior, only meaningful while
`running`). Critically, most phase transitions are authored **Hub/broker-side, on
the host** — not in the container. Only the running-window transitions
(post-start → running, pre-stop → stopping) happen in-container. A hook on a generic
phase transition therefore cannot be served from inside the container, which is
where the draft placed the executor. **The tier that owns a transition dictates
where a hook can run and what identity it has.**

### 3. Three conflated concerns + a provenance gap
A single template `lifecycle_hooks:` block conflated three independent decisions:

| Axis | Question |
|------|----------|
| **Authorship** | Who writes the logic (what API, event, body)? |
| **Authorization / enablement** | Who decides it applies, and binds it to a credential? |
| **Execution identity** | Whose credentials does it run with? |

The guiding principle: **privilege flows from the enabler, never the author.** An
author may only *declare intent*; turning intent into privileged action requires a
grant from someone with authority. If hooks ship inside template content, binding
authorization to them is hard: a hook *name* (`register`) is not provenance — two
templates can ship `register` (collision), and an author can edit a hook's body
after approval (rug-pull/TOCTOU). Defending this needs content-digest pinning with
run-time re-verification — substantial machinery.

---

## The revised model: hooks as Hub database records

**Hooks are not shipped with templates. They are first-class Hub database records,
created by hub administrators through the Hub API.**

This dissolves the problems above:

- **Authorship and authorization collapse into one governed act.** A hook is a row
  created by a principal the Hub's authorization layer already governs. "Who wrote
  it" is `created_by` plus the RBAC check at creation. There is no untrusted YAML in
  transit, so there is nothing to pin.
- **No collision, no rug-pull.** A hook has a stable row ID; edits are governed
  mutations with audit and optimistic locking (`StateVersion` already exists in the
  store). Content-digest pinning drops from load-bearing to an optional integrity
  nicety.
- **Execution identity is a field on the row,** set at creation by someone
  authorized to grant it. Creation *is* the binding — no separate "declare a
  reference, bind it later" step.
- **Execution converges Hub-side,** which is exactly where the privileged registry
  use case needs to run, and where the authoritative phase transitions are owned.

This is the **admission/policy-webhook pattern**: the platform injects behavior at
lifecycle points based on a rule stored as data, independent of any workload's own
configuration.

---

## Architecture

```
Agent phase transition (authoritative, Hub-side)
        │
        ▼
  Hub lifecycle-hook evaluator
        │  selects enabled hooks whose scope + selector match the agent
        ▼
  For each matching hook:
        ├─ Resolve execution identity (project integration SA)
        ├─ Render action (URL/headers/body) with trusted + untrusted vars
        ├─ Execute HTTP/webhook with timeout
        ├─ Apply on_error policy (log | retry)
        └─ Record result to the audit log
```

- **Source of truth:** the Hub database.
- **Trigger source:** authoritative phase transitions the Hub already owns.
- **Execution location:** Hub-side (v1). No container changes, no agent involvement.
- **Identity:** a per-project GCP service account the Hub already mints and manages
  (`pkg/hub/handlers_gcp_identity.go`).

---

## Data model

A new `LifecycleHook` entity, sibling to `AccessPolicy`
(`pkg/ent/schema/`). Sketch:

| Field | Type | Notes |
|-------|------|-------|
| `id` | UUID | Primary key |
| `name` | string | Human label (not an identity) |
| `scope_type` | enum | `hub` (v1). `project` reserved for later. |
| `scope_id` | string | Empty for hub scope |
| `selector` | JSON | Which agents this applies to (see below) |
| `trigger` | enum | Authoritative phase transition (see below) |
| `action` | JSON | HTTP/webhook spec (method, url, headers, body, on_error, timeout) |
| `execution_identity` | string | Reference to a project integration SA |
| `enabled` | bool | |
| `created` / `created_by` / `updated` | — | Authorship + audit |
| `state_version` | int64 | Optimistic locking (existing pattern) |

### Selector (v1)
Matches on attributes **actually persisted** on the agent (`pkg/ent/schema/agent.go`):

- `project_id`
- `template`

> **Note:** agents have **no persisted labels** today. `Labels` exists on the Go
> struct in `pkg/store/models.go` but is not a column in the `Agent` ent schema.
> Label-based selection is a future enhancement (it depends on first adding agent
> labels). The selector schema is designed to accept a label matcher later without
> a breaking change.

### Triggers (v1)
Only **authoritative phase transitions** owned Hub-side. **Resolved set (v1):**

- `running` — agent confirmed running → register.
- `suspended` — agent suspended (no longer serving) → deregister / mark-unavailable
  (re-register on return to `running`).
- `stopped` — agent stopped → deregister.
- `error` — agent entered error phase → deregister / alert.

**Excluded from v1:** `stopping` (overlaps `stopped` and is the less-reliable edge;
`stopped` is the authoritative terminal signal) and the early lifecycle phases
(`created`/`provisioning`/`cloning`/`starting` — no current use case).

Running-window **activity** transitions are deferred (they live in the other
execution domain and add the debounce/ephemeral-process complexity discussed above).

### Action types (v1)
- `http` — full request (method, URL, headers, body), authenticated via the
  execution identity.
- `webhook` — convenience alias for an unauthenticated `POST` (URL carries its own
  token).

`script` actions are **deferred** — they require a provisioning + per-file-hash
trust model to be safe, and Hub-side script execution is a different concern.

---

## Execution identity

Hooks run as a **project integration service account**, referenced by the hook row
and resolvable Hub-side. The Hub already mints, lists, verifies, and deletes
per-project GCP service accounts (`pkg/hub/handlers_gcp_identity.go`), so this is an
existing capability, not new machinery.

**Reference shape (resolved):** the hook row stores the **managed-SA record ID
(UUID)** from the GCP SA store (`store.GCPServiceAccount.ID`), not a raw email.
At hook-creation time, validate that the SA exists, is in the hook's scope, and is
`Verified`; at execution time, resolve ID→email and reuse the existing impersonation
path (token generator / `VerifyImpersonation`). This gives an integrity check at both
creation and execution and clean audit attribution, since this identity is the
privileged caller.

> **Association model — start simple (v1):** the SA is referenced **per hook** (one
> `execution_identity` field on each row). How operators *choose and associate* an SA
> for hooks (per-hook vs. a hub-wide default for all hooks) may be refined later; v1
> keeps it explicit per-hook.

Rejected alternatives and why:
- **Agent identity, in-container:** the agent often lacks rights to the target API,
  and putting privileged integration credentials next to the LLM/harness is exactly
  the wrong place — privilege wants *distance* from the agent.
- **Raw Hub service account for any hook:** maximal blast radius and poor
  attribution. A scoped project SA is auditable and least-privilege.

---

## Variable substitution and the untrusted-variable guard

Hook URLs, headers, and bodies support `${VAR}` substitution. Variables come from
two trust classes:

- **Trusted** (admin/platform-fixed): hook config, project metadata, the resolved
  agent identity fields the Hub controls.
- **Untrusted** (agent/runtime-derived): `${AGENT_NAME}`, `${TASK_SUMMARY}`, and any
  value influenced by the agent or its LLM.

**Even a fully-trusted, admin-authored hook running as a privileged SA is a confused
deputy if untrusted *data* flows into a sensitive position.** Rules enforced at
render time:

- Untrusted variables are **never** allowed in the URL **path/host**, in any **auth
  field/header**, or in unescaped positions.
- Untrusted variables are allowed only in **allow-listed body fields**, always
  **strictly encoded** (JSON-encoded on insertion; percent-encoded for any query
  position).
- A clear split between trusted and untrusted substitution sources, enforced by
  *where* each may appear.

This prevents path manipulation/SSRF and field/annotation injection performed with
the Hub's privilege.

---

## Failure, retry, timeout

- `on_error`: `log` (default — record and continue) or `retry` (exponential
  backoff, fixed max attempts, then fall back to `log`). `fail` is **deferred**
  (aborting an authoritative transition is high-impact and needs care).
- `timeout`: per-action, with a validated maximum.
- Results (success/failure/latency) are recorded to the **audit log** with the
  hook id, trigger, agent id, and execution identity for attribution.

**Response capture (resolved, v1):** record **status code, latency, and a coarse
error class only** — **not** the HTTP response body. This keeps attacker/third-party-
controlled content out of the audit store and avoids a redaction pipeline. Request
metadata (method, host, hook id) is recorded; rendered auth headers and secret body
fields are **never** stored. If gnarly debugging later demands it, emit a customized,
opt-in debug log at that point — out of scope for v1.

### Reliability note
Because hooks fire off **authoritative Hub-side** transitions rather than from
inside a possibly-dead container, deregister-on-stop is far more reliable than the
in-container `pre-stop` approach (which is lost on SIGKILL/OOM). Residual gaps
(e.g. Hub downtime during a transition) can be handled later by a reconciliation
sweep; out of scope for v1.

**Hook executors should be idempotent.** The evaluator de-duplicates by tracking
previous phase, but edge cases (hub restart, race conditions) may cause a hook to
fire more than once for the same logical transition. Executors (and the external
endpoints they call) must tolerate duplicate invocations safely.

---

## Authorization and audit

- **Creation:** hub-admin only (v1), enforced via the existing authz layer
  (`pkg/hub/authz.go`). Project-owner-created, project-scoped hooks are a deferred
  enhancement.
- **Audit:** creation, edits, enable/disable, and every execution are recorded
  (`pkg/hub/audit.go`).
- **Change control** is RBAC + audit, not template review: editing a hook takes
  effect on the next matching transition.

---

## Explicitly deferred (post-v1)

- **Synchronous, blocking in-container lifecycle hooks** — paired pre-/post-start and
  pre-/post-stop hooks that run *inside* the container (the long-lived `sciontool init`
  process, `pkg/sciontool/hooks/lifecycle.go`), execute with container/agent identity,
  block the lifecycle on each edge, and may abort the transition on the pre- edge.
  These serve container-local setup / flush / sync needs and are a genuinely distinct
  execution model from the Hub-side observed-state hooks this design covers (different
  authorship, identity, and blocking/reliability semantics). Explicitly **out of scope
  for this design**, recorded here as a future extension point.
- Template-shipped hooks (and the content-digest provenance machinery they'd need).
- In-container / agent-identity execution (recoverable later by projecting matching
  hook rows into the container at start, the way config/secrets already flow down).
- Activity-change (running-window) triggers + debounce.
- `script` actions (need provisioning + per-file-hash trust model).
- Signing / publisher trust for any externally-sourced hook content.
- Project-owner-created, project-scoped hooks.
- Agent labels as a selector dimension (needs agent labels added first).
- Hub-side reconciliation/orphan sweep.

---

## Implementation sketch (high-level)

1. **Entity + store:** add `LifecycleHook` ent schema (`pkg/ent/schema/`) and store
   methods (`pkg/store/`), mirroring `AccessPolicy`/`PolicyBinding` patterns,
   including `StateVersion` optimistic locking.
2. **Hub API:** admin-gated CRUD handlers (mirroring `admin_settings.go` /
   `project_settings_handlers.go`), authz via `authz.go`, audit via `audit.go`.
3. **Evaluator:** at authoritative phase transitions, select enabled hooks whose
   scope + selector match the transitioning agent.
4. **Executor:** render action with the trusted/untrusted-variable guard, acquire
   the project integration SA token (reusing `golang.org/x/oauth2/google`, already a
   dependency), execute with timeout, apply `on_error`, write the audit record.
5. **Validation:** trigger ∈ supported phase transitions; action well-formed; no
   untrusted variables in disallowed positions; timeout within bounds.

No changes to `scion-agent.yaml` / `ScionConfig`. No container/`sciontool` changes.

---

## Open questions — RESOLVED (issue #35 discussion, June 2026)

1. **Trigger set (v1):** `running`, `suspended`, `stopped`, `error`. `stopping` and
   the early lifecycle phases are excluded. *(See [Triggers (v1)](#triggers-v1).)*
2. **`execution_identity` reference:** the **managed-SA record ID (UUID)** from the
   GCP SA store, validated at creation (exists / in-scope / verified). SA *association*
   model (per-hook vs. hub-wide default) may be refined later; v1 is per-hook.
   *(See [Execution identity](#execution-identity).)*
3. **Response-body capture:** **No** — record status code, latency, and error class
   only; no response bodies. An opt-in debug log can be added later if needed.
   *(See [Failure, retry, timeout](#failure-retry-timeout).)*

### Additional scope decision
- **Synchronous, blocking in-container lifecycle hooks** (pre-/post-start,
  pre-/post-stop) are a distinct execution model and are **out of scope for this
  design**, recorded as a future extension point. *(See [Explicitly deferred](#explicitly-deferred-post-v1).)*

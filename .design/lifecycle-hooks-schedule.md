# Lifecycle Hooks (issue #35) — Development Schedule

**Scope:** Hub-side, DB-stored, admin-authored observed-state lifecycle hooks (v1).
Companion to `.design/lifecycle-hooks-design.md`. In-container synchronous/blocking
hooks are **out of scope** (future extension point).

## Resolved decisions feeding this plan
- **Triggers (v1):** `running`, `suspended`, `stopped`, `error`.
- **Execution identity:** managed-SA **record ID (UUID)**, validated at creation
  (exists / in-scope / verified); per-hook reference.
- **Audit:** status code + latency + error class only; **no** response bodies.
- **Actions:** `http` and `webhook`. `script` and `fail`/abort deferred.

---

## Milestones & sequencing

### M1 — Data model & store  *(foundation; nothing else starts without it)*
- `LifecycleHook` ent schema in `pkg/ent/schema/` (sibling to `policy.go` /
  `policybinding.go`): fields per design doc, incl. `state_version` optimistic locking.
- Generate ent code; add store CRUD methods mirroring AccessPolicy/PolicyBinding.
- Selector + action stored as validated JSON.
- **Exit:** create/get/list/update/delete with optimistic locking; unit tests.
- **Depends on:** —

### M2 — Validation library  *(can start alongside M1 once schema fields agreed)*
- Trigger ∈ {running, suspended, stopped, error}.
- Action well-formedness (method, URL, headers, body, timeout ≤ max).
- **Untrusted-variable guard** (the security-critical piece): enforce that untrusted
  vars never appear in URL path/host, auth fields/headers, or unescaped positions;
  allow only in allow-listed, strictly-encoded body fields.
- `execution_identity` resolves to an existing, in-scope, **verified** managed SA.
- **Exit:** table-driven tests incl. SSRF/path-injection/field-injection cases.
- **Depends on:** M1 (field shapes).

### M3 — Hub API (admin-gated CRUD)
- Handlers mirroring `admin_settings.go` / `project_settings_handlers.go`.
- Authz: hub-admin only (`pkg/hub/authz.go`).
- Audit on create / edit / enable-disable / delete (`pkg/hub/audit.go`).
- Wire M2 validation into create/update.
- **Exit:** API tests incl. authz-denied and validation-rejection paths.
- **Depends on:** M1, M2.

### M4 — Evaluator (trigger → matching hooks)
- Hook into the **authoritative phase-transition** path Hub-side; on a v1 trigger,
  select enabled hooks whose scope + selector (`project_id`, `template`) match.
- **Exit:** unit tests for selector matching + enabled filtering; no-match = no-op.
- **Depends on:** M1. (Identify the exact Hub-side transition call site — see Risks.)

### M5 — Executor (render → identity → call → audit)
- Render action with the M2 trusted/untrusted guard.
- Acquire project-SA token via existing impersonation path (`gcp_token*.go`,
  `golang.org/x/oauth2/google`); resolve identity ID→email.
- Execute HTTP with per-action timeout; apply `on_error` (`log` default | `retry`
  w/ bounded exponential backoff → fall back to `log`).
- Write audit record (status, latency, error class; **no body**).
- **Exit:** executor tests w/ mock endpoint: success, timeout, retry-exhaustion,
  4xx/5xx; confirm no secrets/bodies persisted.
- **Depends on:** M2, M4.

### M6 — Integration, docs, hardening
- End-to-end: phase transition → evaluator → executor → audit, against a fake
  registry endpoint. Validate the motivating case (register on `running`,
  deregister on `stopped`/`suspended`/`error`).
- Admin docs (authoring a hook, SA prerequisites, variable trust rules).
- **Exit:** green e2e; issue #35 acceptance reviewed with Preston.
- **Depends on:** M3, M5.

---

## Critical path
`M1 → M2 → {M3, M4} → M5 → M6`  (M2 overlaps M1's tail; M3 and M4 parallelize after M2.)

## Top risks / watch-items
1. **Untrusted-variable guard (M2)** is the highest-value security surface — review it
   hardest; it's where a privileged-SA confused-deputy bug would live.
2. **Evaluator hook point (M4):** must attach to the *authoritative* Hub-side
   transition, not a derived/display status. Needs a short spike to pin the exact
   call site and confirm `suspended`→`running` re-register fires as expected.
3. **Identity resolution failure modes (M5):** SA deleted/unverified between hook
   creation and firing — define behavior (skip + audit, don't crash the transition).
4. **No transition should be blocked or fail** because a hook failed (v1 is
   non-blocking; `fail` is deferred). Assert this in tests.

## Out of scope (recorded, not built)
Activity-change triggers; `script` actions; `fail`/abort; in-container blocking
hooks; template-shipped hooks; project-scoped/project-owner hooks; agent labels as a
selector; reconciliation sweep; response-body capture.

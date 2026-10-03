# Substrate restart durability — Phase 1: durable agent state store

**Date:** 2026-10-03
**Branch:** scion/substrate-restart-durability
**Issue:** ptone/scion#2818 (Phase 1)

## Problem

The substrate runtime kept every agent's record (labels, project identity),
control token and exec-redaction secrets only in process memory. After a
broker restart every agent it had started became record-less: listed under
its actor name with no project labels, and every delete/stop of it in its
project answered `409 agent_identity_unknown` ("operator cleanup required").

## Solution

- **`AgentStateStore`** (`pkg/runtime/substrate_state.go`): one Opaque
  Kubernetes Secret per agent in a dedicated, operator-configured state
  namespace. Name `scion-sb-` + first 40 hex chars of sha256(`<atespace>/<actor>`);
  labels managed-by / state-version `"1"` / atespace; annotations phase /
  actor-uid; data `id`, `record.json`, `control_token`, `exec_secrets`.
  Optimistic concurrency via resourceVersion. Errors carry operation +
  object name only (never data or the client-go message); unreadable,
  unknown-version or id-mismatched objects are skipped with a name-only log.
- **Run** writes ahead: claim `pending` (record, token, exec secrets) before
  CreateActor → record the actor UID (CAS) → bootstrap → `committed` (CAS) →
  fill the in-memory cache. A CAS conflict means a concurrent Delete owns the
  id: Run deletes its actor and leaves the state to that Delete. Any other
  failure after the claim deletes the actor and the state object.
- **Delete/Stop**: mark `deleting` (CAS) → DeleteActor → delete the state
  object → evict the cache. A failed DeleteActor leaves `deleting`, which
  makes a Run of the same id fail with a retryable error until a retried
  Delete completes.
- **List / RecordlessActors** join the persisted records (committed/deleting
  by UID; pending by id for List only), then the cache. A store failure fails
  the call rather than silently reporting persisted agents as record-less.
- **Config**: `runtimes.<name>.substrate.state_namespace` is required (DNS-1123
  label); missing/invalid refuses broker startup via the existing
  `ErrSubstrateProfileInvalid` path. It is covered by the operator-only
  profile check automatically.
- **Deploy**: `deploy/substrate/broker.yaml` gains the `${STATE_NAMESPACE}`
  Namespace, a Role there with exactly get/list/create/update/delete on
  secrets, and a RoleBinding to `scion-substrate-broker`. No new permission in
  the broker namespace.

Legacy actors (created by a broker without state persistence) keep today's
record-less behaviour and the 409 path.

## Tests

Store unit tests on client-go's fake with a resourceVersion-enforcing
reactor; Run/Delete write-ahead and CAS-race tests; a no-credential-leak test
over success and failure paths; the headline restart tests in `pkg/runtime`
(List with project labels → Delete after a cache wipe, and from a new runtime
instance) and `pkg/runtimebroker` (delete 204 / stop 202 instead of 409);
config refusal + positive control in `cmd`; and a manifest RBAC test. The
restart tests were shown to fail (0 agents listed; 409 `agent_identity_unknown`)
with the persisted-record join disabled.

## Out of scope (later phases)

Exec/Message/reset-auth read-through of the token from the store and 401
handling (Phase 2); the reconciler, `state_reconcile_interval` and per-id
serialisation (Phase 3); docs and `settings.example.yaml` (Phase 4).

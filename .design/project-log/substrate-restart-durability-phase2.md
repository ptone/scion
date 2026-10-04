# Substrate restart durability — Phase 2: exec / message / reset-auth after restart

**Date:** 2026-10-04
**Branch:** scion/substrate-restart-durability-p2
**Issue:** ptone/scion#2818 (Phase 2)

## Problem

After Phase 1 the agent record, control token and exec secrets survive a
broker restart in the state store, but Exec / ExecWithStdin still read the
control token and exec-redaction secrets only from the in-memory cache. After a
restart every exec-backed operation (exec, message delivery, reset-auth)
failed with "no control token cached".

## Solution

- **Read-through** (`controlCredentials` in `pkg/runtime/substrate_runtime.go`):
  a cache hit returns immediately. On a miss the persisted state is read. A
  `committed` state with a non-empty token fills the token, exec secrets and
  record caches under the cache lock (an entry filled concurrently wins) and is
  used. Not found, `pending`, `deleting` or an empty token keep the existing
  "no control token cached" error. Other store errors return an opaque
  `read agent state` error. Message and reset-auth both run through
  Exec/ExecWithStdin, so they get this too.
- **401 handling** (`doExec` in `substrate_bootstrap.go`): an actor 401
  returns a wrapped `errControlCredentialRejected` ("control credential
  rejected"), does not echo the body and is not retried. The ExecWithStdin
  probe forwards it instead of reporting version skew.
- **Redaction**: the exec redaction set is now exec secrets plus the control
  token. It applies to successful stdout as well as error text.
  `redactedExecError` unwraps only to a sentinel, never to the unredacted
  original error.

No change to the §3.2 schema or the store interface.

## Tests

- `pkg/runtime/substrate_exec_restart_test.go`:
  - exec / exec-with-stdin after a cache wipe, on the same runtime and on a new runtime instance (auth is the persisted token, output and errors redacted, cache filled);
  - unusable states;
  - opaque store failure;
  - 401 makes exactly one call;
  - no-leak test across success and failure paths;
  - bootstrap-body token equals the persisted `control_token`.
- `pkg/runtimebroker/substrate_restart_exec_test.go`:
  - exec, message and reset-auth over HTTP after a real restart;
  - opaque 401;
  - broker-wide no-leak test over exec / message / reset-auth / list / run / delete success and error paths, covering HTTP bodies and logs.
- Non-vacuity: these tests fail with the read-through disabled, persisted exec secrets dropped, 401 detection disabled, or a 401 retry added.

## Out of scope (Phase 3)

Run stale takeover, per-id serialisation (a read-through interleaving with a
concurrent Delete can re-cache a token until the next restart or Delete), and
the reconciler.

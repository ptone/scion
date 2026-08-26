## Summary

Three handlers under `/api/v1/runtime-brokers/{id}/…` act on the broker ID taken from
the URL path without checking that the authenticated caller *is* that broker. The
codebase already has the correct pattern for this check and uses it elsewhere in the
same area; these three handlers simply don't call it.

Requests do reach these handlers authenticated — `UnifiedAuthMiddleware` and
`BrokerAuthMiddleware` are applied globally in `applyMiddleware` — so this is a
cross-tenant authorization gap, **not** an unauthenticated one.

## The affected handlers

All in `pkg/hub/handlers_runtime_brokers.go` (line numbers as of `4b68362`):

| Handler | Line | Authorization present |
|---|---|---|
| `handleBrokerHeartbeat` | 623 | none |
| `getRuntimeBroker` | 300 | none |
| `getBrokerProjects` | 853 | none |

For contrast, in the same file `updateRuntimeBroker` and `deleteRuntimeBroker` both
call `s.authorize(...)`, and `handleBrokerSecrets` / `handleBrokerEnvVars`
(`pkg/hub/handlers_env_secrets.go:2136, 2226, 2394, 2456`) do the broker-identity
check properly.

## The part most worth attention: a guard that reads as protective but checks the wrong pair

Inside `handleBrokerHeartbeat`, at line 649:

```go
// Security check: ensure the agent belongs to this broker
if agent.RuntimeBrokerID != id {
    slog.Warn("Broker attempted to update agent owned by different broker", …)
    continue
}
```

`id` here is the **path** parameter, not the caller's identity. The comparison is
therefore *path-vs-agent*, never *caller-vs-path*. It correctly prevents a heartbeat
addressed to broker B from touching broker C's agents — but it does nothing to
establish that the sender is B.

The consequence is that the loop's protection does not hold in the case it appears to
cover: a request addressed to another broker's heartbeat endpoint, carrying that
broker's own agents, passes the check for every agent.

This is worth calling out separately from the missing-check list, because a reader
auditing this handler is likely to see the comment, see a `continue`, and move on.

## Impact

For an authenticated caller addressing a broker ID that is not its own:

- **Write** — agent `Phase`, `Activity`, `Message` and container status for every
  agent belonging to the targeted broker, via the loop above.
- **Write** — the targeted broker's own liveness/status row, via
  `s.store.UpdateRuntimeBrokerHeartbeat(ctx, id, heartbeat.Status)` at line 633.
- **Read** — the targeted broker's record (`getRuntimeBroker`) and its project list
  (`getBrokerProjects`).

Severity depends on deployment: on a hub where all brokers belong to one operator this
is latent; on a hub with mutually distrusting broker owners it is live cross-tenant
access. I don't have visibility into which describes the community hub.

Deliberately not including a reproduction request here.

## Suggested fix

Apply the existing pattern. From `handlers_env_secrets.go`:

```go
// Authorize access: broker self-access or user CheckAccess
if brokerIdent := GetBrokerIdentityFromContext(ctx); brokerIdent != nil && brokerIdent.BrokerID() == brokerID {
    // Broker accessing its own resource — allowed
} else {
    identity := GetIdentityFromContext(ctx)
    if identity == nil { Unauthorized(w); return }
    if userIdent, ok := identity.(UserIdentity); ok {
        decision := s.authzService.CheckAccess(ctx, userIdent,
            Resource{Type: "runtime_broker", ID: brokerID}, ActionRead)
        if !decision.Allowed { Forbidden(w); return }
    } else {
        Forbidden(w); return
    }
}
```

Notes for whoever picks this up:

1. **`handleBrokerHeartbeat` needs a write-shaped action**, not `ActionRead` — the
   snippet above is from a read path.
2. **Fix the misleading comment at line 649 while you're there.** Once the caller
   check exists the `agent.RuntimeBrokerID != id` test is still worth keeping as
   defence in depth, but the comment should say what it actually verifies.
3. **Check the heartbeat path for a rejection-vs-skip decision.** Today an
   unauthorized agent update is silently `continue`d. A caller mismatch at the
   handler level should almost certainly be a `403`, not a silent no-op, or brokers
   will fail invisibly during any future identity migration.
4. There is a second, unauthenticated-looking route into the same handler:
   `handleRuntimeBrokerByID` (marked `//nolint:unused // Kept for legacy route
   compatibility`) also dispatches to `handleBrokerHeartbeat`. Worth confirming it is
   genuinely unreachable rather than assuming.

## Provenance

Found while auditing `handlers_runtime_brokers.go` for an unrelated design
(single-node hosted tier on Cloud Run Instances), and re-verified against `4b68362`
before filing. Not the product of a security review, so the surrounding handlers have
not been audited to the same depth — the three above are what I looked at, not
necessarily all there is.

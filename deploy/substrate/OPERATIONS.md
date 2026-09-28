# Operating the Substrate broker

Day-2 operations for a deployed `deploy/substrate/broker.yaml` broker: warming
a template before real traffic, what a broker restart does and doesn't
recover, cleaning up actors a restarted broker can no longer identify, and
where the runtime's other known limits are tracked. See `README.md` for
install and `cluster/README.md` for the cluster-level prerequisites these
procedures assume are already in place.

## Warm the template before first use

Creating the first agent on a given template builds its golden `ActorTemplate`
snapshot, which can take on the order of a minute or more — dominated by the
golden build itself, not broker or hub overhead. The hub's control-channel
dispatch has a fixed timeout; if the golden build plus actor bootstrap
doesn't finish inside it, the hub gives up and sends a rollback delete that
races the still-in-progress create. The broker's create finishes anyway,
leaving a **running, unbootstrapped actor the hub doesn't know about**,
holding a worker indefinitely. A cold template build is the easiest way to
hit this, but it is a general dispatch-timeout gap, not specific to this
runtime.

Warm a golden for a brand-new project by creating one throwaway agent first —
the first `CreateActorTemplate` for a project needs that project's atespace
to exist, and an ordinary `scion start` creates it as a side effect:

```sh
# Start a throwaway agent on the profile/template you're about to use for
# real. The first create against a new template builds the golden snapshot
# and can take well over a minute — expected here.
scion start --profile substrate warm-template-check "echo warm"

# Wait for it to actually reach `running` before treating the template as
# warm — don't just wait for the command to return.
scion list | grep warm-template-check

# Clean up the throwaway agent. The template and its golden snapshot are
# NOT deleted with it (there is no template GC yet) — that's the point: the
# next real create against the same template reuses the now-ready golden
# and takes the fast (sub-second broker dispatch) path.
scion delete warm-template-check
```

Re-warm before pointing real traffic at anything that changes the template's
content-addressed name — a new image digest, resource shape, sandbox class,
`worker_selector`, or `egress_trust_bundle`.

## After a broker restart

The broker keeps two things only in memory, never persisted: the per-agent
record used to report an agent's project/slug on `list`, and the control
token minted at bootstrap for exec/message. A broker restart — a pod
reschedule, a rolling update, an OOM kill, anything that starts a new
process — drops both for every actor a *previous* process created. The
actors themselves, their egress policies, and their workers are untouched on
the cluster; only this broker's memory of them is gone.

**The invariant: no delete or stop reports success while the actor exists.**
Concretely:

- **Delete/stop of a pre-restart agent by its slug returns HTTP 409**
  (`substrate_agent_identity_unknown`) instead of the ordinary 404/202,
  naming the atespace and how many unidentified actors it holds. This is
  intentional: a 404 is treated as an idempotent completed delete, and a 202
  as a completed stop — either would let the actor, its egress policy, and
  its worker leak with no further signal. An explicit conflict, requiring an
  operator, is the safer failure.
- **If the underlying check itself can't run**, delete/stop return an
  explicit 5xx, never the idempotent success a failed check would otherwise
  fall back to.
- **Exec** on a pre-restart agent fails permanently for that actor — the
  bootstrap endpoint is one-shot, so a new broker process has no way to
  re-mint or recover a token an old process already used.
- **Message** is accepted and reported as delivered by the hub, but never
  actually delivered — a general gap in how a dispatched message ID reaches
  the broker, not specific to this runtime.
- Any agent created **after** the restart is unaffected.

### Cleaning up a record-less actor

The 409 response body only proves at least one unidentified actor exists in
the atespace — it never names one. Identify the actor from the broker's own
log before acting on anything:

```sh
# 1. Find the actor name. The broker logs unidentified-actor names at WARN
#    on every such 409 (log only, never in the HTTP response). A rolling
#    update can run more than one broker pod at once, so match the SPECIFIC
#    pod that logged the line you care about:
kubectl -n "${BROKER_NAMESPACE}" logs --prefix --tail=-1 -l app=scion-substrate-broker \
  | grep -i "agent identity unknown"

# The actor this delete/stop is for is named "<project-slug>--<agent-slug>".
# Act ONLY on that one name, and only after confirming both of the following
# -- the WARN line lists every unidentified actor in the atespace, which
# routinely includes other agents that are still running and must not be
# touched here:
#   (a) its creation time predates the broker CONTAINER's current start in
#       that pod (not the pod's start time, which does not move on an
#       in-place container restart):
kubectl ate get actor -a <atespace> <actor> -o yaml   # read .metadata.createTime
kubectl -n "${BROKER_NAMESPACE}" get pod <pod> \
  -o jsonpath='{.status.containerStatuses[?(@.name=="scion-substrate-broker")].state.running.startedAt}'
#   (b) it matches an agent you actually expect in the hub's own agent list
#       for that project.

# 2. Delete the actor's egress policy FIRST -- order matters, since an
#    orphaned policy left behind after deleting the actor is invisible to
#    this whole mechanism. There is no CLI for this; call the ateapi
#    Control service's DeleteActorEgressPolicy RPC directly (grpcurl or
#    equivalent), authenticated with a short-lived token, TLS verification
#    ON. Confirm the policy is actually gone (expect NotFound on a
#    follow-up Get) before proceeding.

# 3. Delete the actor itself, any-state so a non-RUNNING actor isn't
#    rejected:
kubectl ate delete actor -a <atespace> <actor> --any-state

# 4. Force-delete the hub's own agent record so it stops dispatching to an
#    actor that no longer exists (the hub has no --force on the client;
#    call its DELETE .../agents/<id>?force=true endpoint directly, with the
#    caller's own hub token). Run this LAST -- run it before steps 1-3
#    finish and the actor, egress policy, and worker are orphaned with no
#    further signal.
```

`force=true` still dispatches the delete to the broker exactly as a normal
delete would; it only changes what happens when that dispatch fails — the
hub deletes its own agent record regardless of what the broker did or didn't
manage to clean up. Run it only after the actor and its egress policy are
confirmed gone.

### A pre-restart actor stuck in `DELETING`

An actor already mid-delete when the broker restarts (for example, one this
same broker had started deleting, whose deletion never finished before a
crash) is **excluded** from the record-less-actor count above — its egress
policy is already gone, so counting it would turn an ordinary
stop-then-delete race into a false 409. The consequence: a delete of its
slug returns the ordinary idempotent 404 and the hub drops its record, while
the actor itself may still be sitting on the cluster, stuck in `DELETING`.
It can't be found by attempting a delete/stop — a `DELETING` actor never
produces the 409 above. Locate and clear it instead:

```sh
# 1. List the atespace's actors and note every one in ACTOR_STATE_DELETING
#    (there is no --state filter):
kubectl ate get actors -a <atespace>

# 2. For each candidate, compare its creation time against every currently
#    running broker pod's CONTAINER start (not pod start -- see above). Only
#    a candidate that predates every one of those timestamps is stuck; a
#    DELETING actor created after any of them may just be that pod's own,
#    ordinary, in-flight delete.

# 3. Only for a confirmed candidate: delete its egress policy exactly as in
#    step 2 above (tolerate NotFound if it's already gone), then:
kubectl ate delete actor -a <atespace> <NAME> --any-state
```

## Known limits

The runtime's behavior in areas outside this broker's control — logging,
suspend semantics, egress validation, platform quirks — is out of scope for
this operations doc. The full list of verified defects, limitations, and
feature requests filed against the upstream runtime is tracked by ID
(`SB-D*` defects, `SB-L*` limitations, `SB-F*` friction, `SB-R*` requests) in
the project's feedback record; the ones most likely to affect day-2
operation of this broker are:

- Stuck `DELETING` actors that hold a worker indefinitely until force-cleaned
  (SB-D4), and force-clean itself leaking the actor's host-side directory
  (SB-D6) — see "A pre-restart actor stuck in `DELETING`" above.
- Slow first creates against a cold template, and a template's golden build
  competing with real actors for pool workers (SB-L6) — see "Warm the
  template before first use" above.
- gVisor snapshots only restoring on a matching CPU platform (SB-L13) — see
  `cluster/README.md`, "gVisor node pool".
- NetworkPolicy enforcement requiring an enforcing CNI with no built-in check
  (SB-F3), and router ingress having no authorization of its own (SB-L11) —
  see `cluster/README.md`, "Enabling NetworkPolicy enforcement", and
  `README.md`, "Known Phase 1 limitations".
- `worker_selector` matching the wrong label silently reporting "no free
  workers" instead of a selector-mismatch error (SB-F6) — see
  `cluster/README.md`, "`worker_selector`".

## TODO (not automated yet)

- The record-less-actor and stuck-`DELETING` cleanup above are manual runbook
  steps. Nothing in this folder scripts them.
- No golden-template garbage collection: a warmed-but-unused template's
  snapshot is never automatically reclaimed.

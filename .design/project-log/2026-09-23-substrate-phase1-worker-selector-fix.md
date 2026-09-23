# Substrate Phase 1 — fix worker_selector: matches WorkerPool labels, not Pod labels

Integration bug from the live smoke test, relayed by sb-em: `CreateActor`
returned "no free workers" because `worker_selector`'s checked-in value
matched against the wrong object's labels entirely.

## Root cause, confirmed against upstream

`worker_selector` (`V1SubstrateConfig.WorkerSelector`) is copied into an
`ActorTemplate`'s `workerSelector.matchLabels`
(`pkg/runtime/substrate_template.go:180`). I had it set to
`{ate.dev/worker-pool: scion-agents}` — the label
`cmd/atecontroller/internal/controllers/workerpool_apply.go` stamps onto
the **generated worker Pods** (confirmed correct for that purpose in round
3, when it was used for the router-adjacent `NetworkPolicy` verification
steps, which are unaffected by this fix). But `workerSelector` on an
`ActorTemplate` is matched against a **different object's labels
entirely**: the `WorkerPool` custom resource's own `metadata.labels`, not
any Pod label at all.

Confirmed by fetching an upstream example rather than trusting the
distinction abstractly:
`demos/multi-template/multi-template.yaml.tmpl`'s `WorkerPool` carries
`metadata.labels: {workload: multi-template-shared}`, and
`demos/multi-template/counter-template.yaml.tmpl`'s `ActorTemplate` selects
it with `workerSelector.matchLabels: {workload: multi-template-shared}` —
the exact same key/value, on the pool's own metadata. The comment in the
multi-template README states the invariant directly: "workerSelector, a
label selector matched against the pool's labels ... no atespace or
namespace scoping involved: pool selection is cluster-wide."

`infra/cluster.md`'s WorkerPool table for `substrate-scion-test` already
recorded the two label sets separately without me connecting them
correctly the first time: "**Worker Pod Labels**: `ate.dev/worker-pool=
scion-agents`, `workload=scion-agents`" (the Pod-level labels — irrelevant
here) vs. "**Pool Label**: `pool: scion-agents` (on WorkerPool metadata)"
(the one that actually matters for `worker_selector`). The failure mode is
exactly what makes this class of bug dangerous: nothing about applying the
manifest signals the error — `kubectl apply` succeeds, the broker starts,
the ConfigMap parses fine — it only surfaces later, at actor creation, as
"no free workers," with no NetworkPolicy or RBAC error to point at the
real cause.

## Fix

- `deploy/substrate/broker.yaml`: `worker_selector` is now templated
  (`${WORKER_SELECTOR_KEY}: "${WORKER_SELECTOR_VALUE}"`) instead of a
  checked-in literal requiring a post-render hand-edit. The hand-edit step
  itself — introduced in round 1 specifically because a map doesn't
  template as cleanly as a scalar — is what let the wrong value ship
  unnoticed for this long; a single required key/value pair templates
  fine with `${VAR}` and now shows up in the same `export` block as every
  other required value, rather than as an easy-to-skip separate step.
  Added a comment on the field itself stating the WorkerPool-labels vs.
  Pod-labels distinction, so a future reader hits the explanation exactly
  where the mistake would be made again.
- `deploy/substrate/README.md`:
  - Added `WORKER_SELECTOR_KEY`/`WORKER_SELECTOR_VALUE` to the placeholder
    table (`pool` / `scion-agents` for `substrate-scion-test`).
  - Replaced the old "hand-edit the ConfigMap" paragraph with an
    explanation of the actual bug (not just the correct value): which
    object each label set belongs to, the upstream evidence, and why the
    failure is silent until `CreateActor` runs.
  - Added a `kubectl get workerpool -n <ns> -o yaml` command as the
    general procedure for reading the correct value off any cluster's
    actual `WorkerPool` object, not just trusting a copied value.
  - Updated the "Apply order" `export` block to include the two new
    variables and removed the now-obsolete hand-edit instruction.

## Grep sweep for other occurrences

Searched the whole tracked repo (`git grep`) for `worker_selector`,
`WorkerSelector`, and `ate.dev/worker-pool`:
- `pkg/config/settings_v1.go`, `pkg/config/schemas/settings-v1.schema.json`:
  generic field descriptions, no example value asserting the wrong label —
  no fix needed.
- `pkg/runtime/substrate_runtime_test.go:687`: already uses
  `{"pool": "scion-agents"}` as its test fixture — sb-dev's code/tests
  never carried the wrong assumption; only this deploy manifest did.
- Remaining `ate.dev/worker-pool` occurrences in `deploy/substrate/`
  (`broker.yaml`'s router-`NetworkPolicy` comment,
  `README.md`'s NetworkPolicy verification steps (c)/(d)/(e)) are correct
  as-is — that label genuinely is the Pod-level one
  `atecontroller`'s `NetworkPolicyReconciler` uses for its generated
  `NetworkPolicy`'s `podSelector`, an entirely separate, correct usage
  from `worker_selector`. Left untouched.
- Older project-log entries (`2026-09-23-substrate-phase1-round1-review-fixes.md`,
  `-round3-remove-worker-netpol.md`) record the wrong value as historical
  fact of what was done at the time. Left unedited deliberately — project
  logs are an append-only record of what happened, not a live doc; this
  entry is the correction, not a rewrite of what came before it.

## Validation

- `kubeconform -strict` on both the raw placeholder file and a
  `substrate-scion-test`-values render (now including
  `WORKER_SELECTOR_KEY`/`VALUE`): 11/11 resources valid, 0 errors — the
  rendered `worker_selector: {pool: "scion-agents"}` was inspected
  directly in the render output, not just schema-validated.
- No Go changes this round.

## Not in scope

Whether other `V1SubstrateConfig` fields have similar "looks like it
should reference a label but doesn't specify which object's" ambiguity
(`sandbox_config_name`, `cluster_trust_bundle`) — both of those name a
CRD/object by its own `metadata.name`, not by a label match, so the
specific confusion here (two different label sets on two different
objects, easy to conflate) doesn't apply to them the same way. Not
re-verified against upstream this round; flagging only because this bug
came from exactly that kind of unstated assumption.

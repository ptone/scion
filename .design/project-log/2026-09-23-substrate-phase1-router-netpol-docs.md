# Substrate Phase 1 — document router NetworkPolicy namespace binding + Calico enablement cost

Small doc-only follow-up requested by substrate-lead via sb-em.

## Router NetworkPolicy namespace binding

Checked: `atenet-router-restrict-ingress`'s `namespaceSelector` in
`deploy/substrate/broker.yaml` already templates on `${BROKER_NAMESPACE}`
— it was never hardcoded, so no manifest change was needed. What was
missing was the README saying so explicitly, and spelling out the
consequence: `${BROKER_NAMESPACE}` resolves once at `envsubst`/apply time,
so if the broker's namespace is later renamed or the broker is redeployed
into a different one, the `NetworkPolicy` object keeps pointing at the old
namespace until it's re-rendered and re-applied — the broker's Deployment
moving does not update it, and the symptom (bootstrap/exec timing out
against the router while the broker pod itself looks healthy) is not
obviously a NetworkPolicy problem unless someone knows to check. Added a
callout in the `## Placeholders` section right after the `BROKER_NAMESPACE`
table row, and referenced it from the row itself.

## Operational prerequisite: enabling NetworkPolicy enforcement on a live cluster

Recorded a new `## Operational prerequisites` section (didn't exist before)
with a fact confirmed on `substrate-scion-test` and relayed by
substrate-lead: turning on NetworkPolicy enforcement (Calico or GKE
Dataplane V2) on an existing Substrate cluster that already has running
workers is not a same-day, apply-and-go change — it requires
rolling-restarting every worker pod and recreating every golden
`ActorTemplate` snapshot. Pre-enablement snapshots fail `runsc restore`
with exit 128 once enforcement is live. This is distinct from (and in
addition to) the existing "Known Phase 1 limitations" bullet about whether
enforcement is enabled at all — that one is about the initial state of the
cluster; this one is about the cost of *changing* it later.

## Validation

- `kubeconform -summary` on `broker.yaml`: 11/11 resources still valid
  (README-only change, no manifest edit).
- No Go changes.

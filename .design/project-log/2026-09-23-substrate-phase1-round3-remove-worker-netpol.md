# Substrate Phase 1 — remove the hand-written worker NetworkPolicy

New information from the cluster (relayed by sb-em, substrate-lead)
changes the worker-namespace half of the §5 fallback's protection:
Substrate's own `atecontroller` **already** creates a NetworkPolicy per
`WorkerPool` that closes exactly the gap `scion-worker-restrict-ingress`
(added in round 2) was trying to close — more precisely than the
hand-written one did. Keeping both would have made things *worse*, not
redundant-but-safe, because NetworkPolicies targeting the same pods are
OR'd together.

## Confirmed against upstream source

Read `cmd/atecontroller/internal/controllers/networkpolicy_controller.go`
(`github.com/agent-substrate/substrate`) directly rather than take the
claim at face value. `NetworkPolicyReconciler.buildNetworkPolicyApplyConfig`
generates, per `WorkerPool`, owned by that `WorkerPool` (`OwnerReference`,
GC'd with it):

- `podSelector`: `matchLabels: {ate.dev/worker-pool: <pool-name>}` — this
  also confirms the label `infra/cluster.md` names for `worker_selector`
  (`ate.dev/worker-pool: scion-agents`) is the *same* label the controller
  uses to select worker pods for this policy, not a coincidence.
- `ingress[0].from`: **one peer** with both a `namespaceSelector`
  (`kubernetes.io/metadata.name: <SystemNamespace>`, i.e. `ate-system`) and
  a `podSelector` (`app: atenet-router`) — a single peer with both
  selectors set is an **AND**: only pods matching *both* conditions (router
  pods specifically, not "anything in ate-system"). All ports (no `ports:`
  stanza in the generated spec — egress is left unmanaged, per the code's
  own comment, pending future Egress API work).

This is strictly narrower than what `scion-worker-restrict-ingress`
granted (all of `ate-system`, not just `atenet-router` pods within it), and
it's applied automatically the moment a `WorkerPool` exists — no manifest
of ours needs to create or maintain it.

## Why the hand-written policy had to go, not just "could reasonably stay"

NetworkPolicy semantics: when multiple policies select the same pods,
traffic is allowed if **any** applicable policy's rules permit it (they are
not ANDed together, and there is no "most restrictive wins"). With both
policies live, `atelet`/`ateapi`/anything else in `ate-system` — not just
`atenet-router` — would still reach worker pods via
`scion-worker-restrict-ingress`, even though Substrate's own tighter policy
would otherwise have refused it. The broader policy wasn't a harmless
belt-and-suspenders duplicate; it was actively undoing the precision of the
one Substrate ships. Sb-em's evidence that this holds in practice: the
direct-:443/:8443-denied check and a suspend/resume test both already
passed against Substrate's controller-generated policy alone on the
`substrate-scion-test` cluster — confirming atelet doesn't need (and
therefore shouldn't have) ingress to worker pods either.

## Changes

### `deploy/substrate/broker.yaml`
- Removed the entire `scion-worker-restrict-ingress` `NetworkPolicy` object
  (11 resources now, was 12).
- Added a comment block after the router `NetworkPolicy` explaining why
  there is deliberately no second one here: what Substrate's own policy
  does, how it's more precise, the OR-semantics reasoning above, and a
  pointer to the exact upstream function
  (`networkpolicy_controller.go`'s `buildNetworkPolicyApplyConfig`) so a
  future reader doesn't mistake the absence for an oversight.

### `deploy/substrate/README.md`
- Replaced the worker-namespace verification block: instead of applying
  and testing our own policy, steps (c)–(f) now (c) confirm the
  controller-generated policy exists (`kubectl get networkpolicy -l
  ate.dev/worker-pool`, since the exact generated name
  — `internal/resources.NetworkPolicyName`, a
  `substrate-<truncated-pool-name>-<hash>` pattern — isn't worth
  reproducing when the label is stable), (d)/(f) confirm direct pod-IP
  access is refused both from a plain namespace and from ate-system without
  the router's label, and (e) confirms a pod carrying the router's own
  label *is* let through — pinning that the peer selector is a real AND,
  not "namespace alone."
- Rewrote the "Bootstrap auth is the §5 fallback" limitation: the router
  policy is ours, the worker policy is Substrate's, explicitly, with the
  OR-semantics reasoning for why a redundant hand-written one would have
  weakened rather than reinforced it.
- Replaced the now-obsolete "worker-namespace pod selector is unscoped"
  limitation (that concern doesn't apply to a policy we no longer own) with
  one naming the real standing dependency: this manifest can verify but not
  control Substrate's worker policy, and has no way to detect if
  `atecontroller` stops enforcing it.
- Updated the placeholder table, "Files" section, and the stale kubeconform
  resource count (12 → 11) to match.

## Validation

- `kubeconform -strict` on both the raw placeholder file and a
  `substrate-scion-test`-values render: 11/11 resources valid, 0 errors
  (back to the pre-round-2 count, as expected — removing one policy, not
  changing any other resource's shape).
- No Go changes this round — manifest/README only.

## Not in scope

Whether `atecontroller`'s generated policy name is worth pinning exactly
(vs. matching by label) — left as a label match in the README since the
generated name includes a content hash that would need to be recomputed by
the operator, and the label is both stable and sufficient to confirm
presence.

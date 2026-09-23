# Substrate Phase 1 — round 2 correction: worker NetworkPolicy port scope

sb-em/substrate-lead caught a real bug in round 1's worker-namespace
`NetworkPolicy` (added in
`2026-09-23-substrate-phase1-round1-review-fixes.md`) before it reached a
real cluster: it scoped ingress to TCP `:80` only, on the assumption that
the actor control server's port is what's exposed at the worker pod's
network level. Their catch: that assumption would have both broken
substrate's real traffic and failed to close the gap it was meant to close,
**and the round 1 verification demo (probing `:80` from another namespace)
wouldn't have caught either problem**, since it never exercised real
router→worker traffic.

## What was actually wrong, confirmed against upstream source

Read `cmd/atecontroller/internal/controllers/workerpool_apply.go`
(`github.com/agent-substrate/substrate`, the code that builds the worker
pod's `Deployment` apply-configuration) rather than continue guessing from
the install manifests alone. The worker pod's `ateom` container — not the
sandboxed actor process — is what actually listens at the pod-network
level:

- `:443` — `--atunnel-listen-address=:443`. This is the **real** inbound
  port: `atenet-router.yaml`'s envoy container comment confirms its
  `ORIGINAL_DST` actor cluster dials "the actor's atunnel ingress server on
  :443". atunnel then bridges into the sandbox, where the actual
  `sciontool substrate-serve` listens on :80 — a port that lives *inside*
  the sandbox network namespace atunnel bridges into, not one directly
  addressable at the pod-network level that Kubernetes NetworkPolicy
  reasons about at all.
- `:8443` — `--atunnel-connect-listen-address=8443`, CONNECT-tunneled
  traffic, mirroring the router's own 8081/8444 pair.
- `:8080` (name `readyz`) — the `ateom` container's own `readinessProbe`
  target, checked by kubelet.

So round 1's `port: 80` restriction would have blocked the real :443/:8443
atunnel traffic (breaking substrate) while restricting a port
(:80) that was never reachable at the pod-network level to begin with
(providing zero actual protection) — the worst of both outcomes, and the
verification step (curling `:80` from outside `ate-system`) would have
reported "refused" either way, whether or not the policy was doing anything
useful.

## Fix

Removed the `ports:` stanza entirely: the `scion-worker-restrict-ingress`
`NetworkPolicy` now allows all ports from `ATE_SYSTEM_NAMESPACE`, nothing
from anywhere else. This is what actually matches the property the §5
fallback depends on (no caller outside `ate-system` can reach a worker pod
at all — not "no caller can reach one specific, possibly-wrong port"), and
it doesn't require tracking atunnel's exact port set or guessing at
atelet's own node-to-pod management traffic (upstream doesn't document a
fixed port list for that either).

Checked, before finalizing, whether worker pods need any *non*-ate-system
ingress at all: grepped the full upstream `manifests/` tree for a
`PodMonitoring` or similar targeting worker/`ateom` pods the way
`atenet-router-monitoring.yaml` targets the router — none exists in the
core install. Nothing else in the upstream manifests names a worker pod as
an ingress target from outside `ate-system`.

## Kubelet probe exemption: confirmed and documented, not assumed

sb-em asked to confirm/document that kubelet's own health-check traffic
doesn't need an explicit allow (since it's node-originated, not
namespace-scoped). This is a documented Kubernetes/GKE property, true for
both NetworkPolicy enforcement backends GKE offers: the legacy Calico-based
add-on and Dataplane V2 (Cilium) — kubelet's HTTP/TCP probes source from the
node, not a pod, and neither backend treats them as ingress from any
namespace a `NetworkPolicy` `from:` clause could match or block. Documented
this explicitly in the README (a dedicated bullet, not folded into the
metrics-scraping one as round 1 had it) and added a verification step (e):
after applying the policy, check that the `ateom` container's readiness
status is still `Ready` — a real check, not just trusting the
documentation.

## Changes

- `deploy/substrate/broker.yaml`: `scion-worker-restrict-ingress` — dropped
  the `ports: [{protocol: TCP, port: 80}]` stanza. Rewrote the policy's
  comment block to record the atunnel :443 finding and why "no ports
  stanza" is the correct fix, not a weakening.
- `deploy/substrate/README.md`: worker-namespace verification steps (c)/(d)
  now probe `:8080/readyz` (a real, plain-HTTP port on the `ateom`
  container) instead of `:80/scion/v1/healthz` (never reachable there to
  begin with); added step (e) checking kubelet's own probe still passes.
  Rewrote the "Bootstrap auth is the §5 fallback" limitation and replaced
  the probe-exemption sentence that had been folded into the
  metrics-scraping bullet with its own bullet, naming both GKE enforcement
  backends explicitly.

## Validation

- `kubeconform -strict` on both the raw placeholder file and a
  `substrate-scion-test`-values render: 12/12 resources valid, 0 errors
  (same count as round 1 — removing a `ports:` stanza doesn't change
  resource count, only that resource's shape, which is still valid `[]` vs.
  the OpenAPI schema for an unset optional field).
- No Go changes this round — manifest/README only.

## Not in scope

Whether atelet's own node-to-worker-pod management traffic is pod-network
traffic at all (vs. same-node local calls) remains undetermined from the
manifests alone; allowing all of `ate-system` covers it either way without
needing the answer.

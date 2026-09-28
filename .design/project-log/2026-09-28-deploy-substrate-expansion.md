# deploy/substrate: cluster prerequisites, day-2 operations, install sequence

Date: 2026-09-28
Agent: developer

## Overview

Expanded `deploy/substrate/` so an operator can go from an empty GKE cluster
to a running scion agent on the Substrate runtime using only that folder.
Previously the folder had a broker runbook (`README.md`) and the in-cluster
broker manifest (`broker.yaml`) only; it had no cluster-level prerequisites,
no day-2 operations doc, and no standalone settings example.

## Changes

### New files
- `deploy/substrate/cluster/README.md` — cluster prerequisites: GKE version
  constraints, node pool shape (system pool + gVisor pool), the gVisor node
  taint/toleration, single-CPU-platform requirement for gVisor
  checkpoint/restore, NetworkPolicy enforcement enablement (Calico/Dataplane
  V2), the `worker_selector` label-matching pitfall, and install order.
- `deploy/substrate/cluster/workerpool.yaml` — a `WorkerPool` (Substrate CRD
  `ate.dev/v1alpha1`) dedicated to scion actors: sized for a Claude
  Code-class workload, tolerating the gVisor taint, node-selected onto the
  gVisor pool. Real manifest, not illustrative — its shape and every field
  used (`sandboxClass`, `template.resources`, `template.tolerations`,
  `template.nodeSelector`) come from the upstream Substrate CRD schema
  (`ate.dev_workerpools.yaml`) and from a WorkerPool actually created and
  exercised on the reference cluster.
- `deploy/substrate/cluster/networkpolicy.yaml` — two `NetworkPolicy`
  objects: the router ingress restriction (relocated from `broker.yaml`,
  unchanged semantics — it's a cluster/`ate-system` prerequisite, not a
  broker-specific resource) and a new default-deny ingress baseline for the
  worker namespace. The default-deny is additive-safe under NetworkPolicy
  union semantics (a rule-less deny adds no allow, so it cannot widen what
  Substrate's own per-`WorkerPool` controller-generated policy already
  permits) — the file's own comments and `cluster/README.md` spell this out,
  since a hand-written *allow* policy in the same spot would have widened
  access instead.
- `deploy/substrate/settings.example.yaml` — the same `runtimes`/`profiles`
  shape `broker.yaml`'s ConfigMap renders, standalone, for a non-Kubernetes
  broker or for diffing against a deployed cluster's live settings.
- `deploy/substrate/OPERATIONS.md` — day-2 operations: warming a template
  before real traffic, what a broker restart does and doesn't recover, the
  409 "agent identity unknown" cleanup procedure, recovering an actor stuck
  in `DELETING`, and a pointer to the known-limitations list by ID.

### Modified files
- `deploy/substrate/README.md` — restructured around an explicit install
  sequence (cluster prerequisites → Substrate → cluster-level scion objects
  → broker → first agent), added a "First agent" section closing the gap
  between "broker is up" and "an agent is running," and updated the
  NetworkPolicy discussion to match the relocation below. All of the
  existing broker runbook content (image pinning, placeholders, egress
  validation, secret creation, apply order, validation, bootstrap file
  delivery, verification commands, known limitations) is preserved, not
  discarded — the broker-restart/stuck-actor operational content moved to
  `OPERATIONS.md` instead of staying inline.
- `deploy/substrate/broker.yaml` — the `atenet-router-restrict-ingress`
  NetworkPolicy moved out to `cluster/networkpolicy.yaml` (a pointer comment
  is left in its place); no other functional change.

## Validation

- `kubeconform -strict -summary -kubernetes-version 1.31.0` against every
  rendered manifest (`broker.yaml`, `cluster/workerpool.yaml`,
  `cluster/networkpolicy.yaml`), resolved first with `envsubst` using
  placeholder sample values. All resources valid, including `WorkerPool`
  against a JSON schema generated from the real upstream CRD
  (`ate.dev_workerpools.yaml`, converted with kubeconform's own
  `openapi2jsonschema-go` tool) rather than skipped.
- Every manifest checked for stray unresolved `${...}` placeholders after
  `envsubst`.
- Own diff grepped for project IDs, cluster names, IPs, image digests, and
  secret-shaped strings before committing; none found.

Full command output and the per-manifest applied/new marking are in the
handoff report accompanying this branch.

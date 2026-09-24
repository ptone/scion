# Hybrid Deployment Tier — Phase 3a: deploy.sh GKE attach and NFS firewall rules

Branch `scion/hybrid-tier-p3`, based on the rebased `scion/hybrid-tier-p2` head. Fork PR on
`ptone/scion`, stacked on the Phase 2 PR.

## Overview

`scripts/single-node-vm/deploy.sh` can now optionally attach an existing GKE cluster as a
second, Kubernetes-based runtime alongside the single-node VM, so agents can run in either
place while sharing project scratchpads over the NFS export the hub VM serves. This slice
covers the tier's config, cluster discovery, and the two firewall rules the VM needs to serve
NFS to that cluster's nodes, end to end through creation and teardown. Everything else the
tier will eventually need — the NFS export itself, the squash uid, PV/PVC wiring, and
settings.yaml changes — is out of scope here and follows in a later slice.

The tier is entirely opt-in: a config file (or wizard run) that never sets `gke_target.name`
gets byte-for-byte the same `deploy.sh` behavior as before this change.

## Config

A new optional `gke_target` block (`name`, `location`, `project`) in
`deploy-config.example.json`, read the same way as every other field (`config_get`, with a
wizard prompt when running fully interactively and no config file is given). The cluster is
always an attach-only, pre-existing prerequisite — `deploy.sh` never creates or deletes one.
Phase 3a supports only a cluster in the hub's own GCP project; a different `gke_target.project`
fails with an actionable message before anything else runs.

## Discovery

Before creating anything, `deploy.sh` confirms the cluster exists and is on the same VPC
network as the hub VM (`default`, today), then discovers the cluster's node network tag with
read-only `gcloud` calls bound to the cluster itself, not to guessing at instance names: it
reads the cluster's node pools' managed instance groups, then the network tags on each group's
instance template — present even when a pool currently has zero running instances, such as an
idle Autopilot pool. Among those tags it selects the one matching the pattern GKE itself uses
for a node's firewall-purpose tag, `gke-<suffix>-node`, which is the same pattern for both a
Standard and an Autopilot cluster. Zero or more than one distinct match refuses to guess and
fails, naming the cluster and listing every tag it found. A cluster that can't be found or a
network mismatch also fail, before any resource is created.

## Firewall rules and VM tag

Two firewall rules, both named from the hub and both carrying the exact description token
`scion-deployment=<hub_name>` as an ownership marker, checked client-side as an exact string,
never a substring or prefix match:

- `scion-hub-<hub_name>-nfs-allow` — INGRESS, ALLOW, tcp:2049, source is the discovered node
  tag, target is `scion-hub-<hub_name>-nfs`, priority 900.
- `scion-hub-<hub_name>-nfs-deny` — INGRESS, DENY, tcp:2049, source `0.0.0.0/0`, same target
  tag, priority 950.

The hub VM gets the `scion-hub-<hub_name>-nfs` target tag at creation time when the tier is
enabled, or via an idempotent `add-tags` call on an existing VM. If a rule already exists under
one of these names without the exact marker, `deploy.sh` refuses to touch it rather than
adopting it — this applies only to these two rules; the VM, Cloud Run proxy, router, NAT, and
service account keep today's unmarked adopt-by-name behavior unchanged. Re-running the script
against a hub that predates the hybrid tier works: the existing base resources are adopted as
always, and the two firewall rules (plus the VM tag) are created fresh, with markers.

A pre-existing, already-marked rule is also checked against the tier's full expected spec
(direction, action, ports, priority, source, target tag, network) before being reused. Any
mismatch fails the run, lists exactly which fields differ, and prints the commands to fix it —
always a delete (so the next `deploy.sh` run recreates the rule correctly), plus a direct
`update` command when the drift is limited to fields that command can change in place. Nothing
is ever auto-corrected: a hand-edited rule under this deployment's marker is exactly what an
operator should see reported back, since the rule's meaning comes from those fields.

## Teardown (`--delete`)

The existing static deletion-list block gains one more check, run before anything is printed or
deleted: it looks up the two hybrid firewall rules by name and classifies each as **found,
marked** (queued for deletion), **found, unmarked** (listed as SKIPPED), or not found. An
unmarked name match fails the whole teardown run, not just the two hybrid rules — a naming
collision on those two names means the hub name can no longer be trusted to identify only
resources this deployment owns, so nothing else proceeds safely from that point either. The
cluster itself is never deleted, under any circumstance.

## Tests

`scripts/single-node-vm/hybrid-tier.sh` is a self-contained module (functions:
`hybrid_read_config`, `hybrid_discover`, `hybrid_ensure_firewall_rules`, `hybrid_vm_tag`,
`hybrid_apply_vm_tag`, `hybrid_teardown_check`, `hybrid_teardown_delete`), sourced by
`deploy.sh` but independently testable. `scripts/single-node-vm/tests/run.sh` exercises it
against a stubbed `gcloud` on `PATH` that records every invocation and serves fixtures — no
test ever contacts GCP. Coverage includes: rule names, the marker on every created rule
(including that the marker check is an exact match, not a substring, so a prefix-colliding hub
name can't be mistaken for a match), refusal on an existing unmarked rule, the target tag on
every created rule, the allow/deny priorities/ports/source-tag/deny-all shape, discovery for
both a Standard and an Autopilot node-pool/instance-group/instance-template fixture shape
(including a pair of prefix-colliding cluster names, a multi-tag template, a zero-instance
pool, and the zero- and multiple-candidate refusals), the network-mismatch refusal, the
pre-existing non-hybrid-hub re-run case, an idempotent create-then-reuse round trip, each
spec-drift scenario together with its remediation output, teardown's
marked/SKIPPED/fail-the-run classification, and the tier-off case (config absent, zero
hybrid-related `gcloud` calls).

No CI workflow currently runs anything under `scripts/single-node-vm/`; the repository's
blanket `shellcheck` job lints any `*.sh` file with no path filter, so the new scripts are
covered by that but not by any dedicated test-execution job. Every new or changed script here
(including `tests/lib/gcloud`, which has no `.sh` extension) is shellcheck-clean.

## Docs

A new "Hybrid Tier (Optional)" section in `docs/deploy/agent-runbook-single-node-vm.md`
documents the config fields, the discovery method, the firewall rules and VM tag, and the
teardown behavior, and states plainly that base-resource adoption and teardown are unchanged by
this tier. The full docs pass, including `docs/deploy/single-node-vm.md`, is a later slice.

## Verifying this locally

1. `bash -n scripts/single-node-vm/deploy.sh scripts/single-node-vm/hybrid-tier.sh`
2. `scripts/single-node-vm/tests/run.sh`
3. Manually: run `deploy.sh --config <file>` with a `gke_target` block naming a real cluster in
   the same project as the hub, and confirm the two firewall rules and the VM tag appear; then
   `deploy.sh --delete` and confirm both rules are deleted and reported.

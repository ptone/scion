# Hybrid Deployment Tier — Phase 3b: pod CIDR, hub-allow rule, static internal IP

Branch `scion/hybrid-tier-p3`, same fork PR as the earlier Phase 3a and 3b slices.

## Overview

Provisions the infrastructure for GKE agent pods to reach the hub directly over the VPC at
`http://<VM internal IP>:8080`: pod CIDR discovery, a third firewall rule scoped to that CIDR, and
a static internal IP reservation for the hub VM so that address is stable across VM recreates.
Also verifies (and did not need to change) that the hub's auth layer accepts an agent token over a
plain, non-localhost URL. Delivering a `gke`-profile-scoped hub endpoint that actually points GKE
agents at the reserved address is a separate, hub-side code change and is out of scope here — see
Known limits below.

## Pod CIDR discovery

`hybrid_discover` now also reads the cluster's pod CIDR from the same cluster-describe call
already used for the node subnet: `clusterIpv4Cidr`, cross-checked against
`ipAllocationPolicy.clusterIpv4CidrBlock`. Discovery fails if either field is missing, if the two
disagree, or if the agreed value doesn't pass the same IPv4/`/8`-or-narrower validation already
used for the node subnet — never guessing between two different answers, and never handing a
dangerously broad range to the firewall rule below.

## Hub-allow firewall rule

A third rule, `scion-hub-<hub>-hub-allow` (`INGRESS ALLOW tcp:8080` from the discovered pod CIDR,
priority 900), joins the existing NFS allow/deny pair under the same target tag and marker
convention, with no paired deny. This is what lets GKE agent pods reach the hub directly, and it
opens tcp:8080 to every pod in the cluster (all namespaces), not only Scion agents; requests are
still authenticated by the hub itself (an agent token, or the IAP assertion for browser users),
and a small set of endpoints (health checks, login/token flows, OIDC discovery, public settings)
answer without credentials, same as for any other caller. Pod-to-hub traffic on this path is plain
HTTP inside the VPC, so agent tokens travel as bearer credentials over it -- anything able to
observe VPC or node traffic can capture them, so this cluster's workloads need to stay trusted.
Only the cluster's default pod range is admitted; a node pool with a separate pod CIDR isn't
covered and fails closed (blocked, not open). `hybrid_teardown_check` was extended to classify and
report on this third rule the same way as the NFS pair; the underlying preflight/delete functions
needed no changes, since they already iterate generically over whatever names are classified.

## Static internal IP reservation

`scion-hub-<hub>-internal-ip` reserves the hub VM's internal address so it survives a VM recreate,
marked with an exact `scion-deployment=<hub>` description -- the same token format the firewall
rules, router, and service account use for their own markers, but unlike those base resources,
this marker is enforced: an address with this name that lacks it is refused on create and blocks
teardown. On a fresh VM, a free address is reserved (or an existing marked one reused) and the VM is created
with `--private-network-ip` pinned to it. On an existing VM, its current internal IP is promoted
into a reservation of the same name (`addresses create --addresses=<current-ip>`), and the VM is
re-described afterward to confirm the IP didn't change. A same-name reservation that's unmarked, or
marked but pointing at a different IP than the VM's actual one, fails the run with the mismatch and
remediation — never auto-corrected, matching the existing firewall-rule drift behavior.

Teardown checks this reservation the same way as the firewall rules: found-and-marked queues it for
delete, found-and-unmarked aborts the whole teardown before anything is deleted, and an
inconclusive check (a `list` failure) is never read as "gone." The reservation is deleted only once
the VM is positively confirmed gone project-wide — it's still attached to the VM's network
interface until then, so an earlier delete would fail regardless — and is reported as kept, with
the reason, rather than silently dropped from the summary when the VM's fate isn't confirmed.
`deploy.sh`'s VM-deletion branch was widened so an internal-IP reservation queued for delete
triggers the same strict, project-wide "confirmed gone" check the firewall rules already required,
not just a bare `describe` in a possibly-wrong zone.

## Hub URL guard

The pre-create half of the guard is structural: pod-CIDR discovery and the address reservation
already `exit 1` on their own failures before deploy.sh does anything else, so nothing downstream
of them runs on bad inputs. `hybrid_hub_url_guard_verify`, called right after VM creation, is the
post-create half: it re-describes the address reservation and the hub-allow rule and fails, naming
whichever is missing, rather than letting deploy.sh proceed with settings.yaml or the rest of
Phase 3 on an incomplete guarantee.

## Auth verification (no code change)

Confirmed that the hub's `UnifiedAuthMiddleware` accepts a GKE agent's own agent token before any
IAP or proxy/localhost-specific logic runs, so a GKE agent dialing the hub directly at
`http://<internal-ip>:8080` — bypassing the IAP-fronted Cloud Run proxy entirely — authenticates
the same way it would through the proxy. No code change was needed or made; this was a read-only
trace to confirm the internal-IP approach doesn't need a new auth path.

## Known limits: interim, pending a hub-side change

Nothing yet points the `gke` runtime's agents at the reserved internal IP; a hub-side change is
required before this tier ships. This is provisioning ahead of that change, not a finished feature.

`docs/deploy/agent-runbook-single-node-vm.md` and `docs/deploy/hybrid-tier.md` document the
discovery step, the three firewall rules, the reservation, the widened teardown table and
ownership-check wording, and this limitation, in the same style as the existing NFS/Kubernetes
sections.

## Hardening pass

A follow-up pass tightened several areas beyond the initial slice:

- **NFS server posture**: the `nfs.conf.d` drop-in now also disables NFSv4.0; `nfs-server` is
  restarted (not just enabled) after writing it, since a package install can already have started
  the service with stock config; the rpcbind mask is verified rather than assumed.
- **Loop filesystem**: image reservation uses `fallocate` (fails clearly, and removes the partial
  file, on insufficient disk) instead of a sparse `truncate`; a stale `/etc/fstab` line for the
  image with different options is refused rather than silently trusted; the mount is verified to
  actually be backed by the image's loop device, not just present at the export path; a
  `scion-hub.service` drop-in adds `RequiresMountsFor` on the export root. The `nofail` rationale in
  both the code comment and commit history now states plainly that `x-systemd.required-by=` alone
  already keeps a failed mount from blocking boot (verified against `systemd.mount(5)`); `nofail` is
  kept only as a defensive redundancy.
- **Teardown clarity**: the internal IP reservation appears in the pre-delete confirmation list; the
  summary distinguishes "SKIPPED" (delete never attempted, with why) from "Kept" (delete attempted
  and failed); every base resource's "not found" case is now reported separately from "Kept" rather
  than collapsed into it.
- **Static IP / guard hardening**: the reused reservation's address type and subnet are checked
  against expectations on both the new-VM and existing-VM paths; a reservation create that succeeds
  but can't be read back is an explicit, named error instead of a silent `set -e` exit; the
  existing-VM promote path validates the current IP looks like IPv4 before using it; the hub URL
  guard now also checks the reservation's marker, that its address matches the VM's resolved
  internal IP, and that the hub-allow rule's source range matches the discovered pod CIDR (not just
  that both resources exist).
- **Test harness**: the suite's own `TMPDIR` usage is now bounded to a single run-scoped directory,
  removed in one shot on exit, with a self-check that it's empty afterward; the create-mode wiring
  tests wait on a sentinel with a large timeout and fail loudly if it's never reached, instead of a
  fixed short grace period; substantial new coverage closes several gaps in the existing test
  suite, including: the NFS squash-identity script executed (not just its rendered text inspected); a
  host-bits-set CIDR rejection; each PV drift field (reclaim policy, `claimRef.name`,
  `claimRef.namespace`, path) checked individually in both the pre-VM and post-VM object checks; an
  unknown (not just absent) check result treated as a hard failure at every namespace/PV/PVC lookup
  site, including in teardown; a realistic kubectl permission-denied message that also matches the
  literal not-found shape; the Cloud Run proxy's marker label actually reaching the `run deploy`
  call; and every field of the hub-allow firewall rule's drift check (not just its source range).

## Test harness

`tests/lib/harness.sh` gained `seed_pod_cidr`/`seed_pod_cidr_mismatch`/`seed_pod_cidr_missing`,
`seed_address`/`seed_address_unmarked`, and `set_address_delete_will_fail`/
`set_address_list_will_fail`; `seed_cluster`'s generated fixture now includes a default,
mutually-agreeing pod CIDR so existing tests didn't need to change. `tests/lib/gcloud` gained
`compute addresses describe/create/delete/list` handlers. New coverage in `test_hybrid_tier.sh`
spans pod CIDR discovery, all four static-IP paths (new-VM reserve/reuse/refuse/list-error,
existing-VM promote/reuse/drift/refuse, teardown check/delete), and the hub URL guard; new
`test_deploy_wiring.sh` cases cover the actual deploy.sh-level wiring for the reservation's
teardown delete (unmarked-aborts, deleted-when-gone, kept-when-not-gone, ordering after the VM
delete), mirroring the existing pattern for the NFS firewall rules.

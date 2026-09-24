# Hybrid Deployment Tier — Phase 3b: pod CIDR, hub-allow rule, static internal IP

Branch `scion/hybrid-tier-p3`, same fork PR as the earlier Phase 3a and 3b slices.

## Overview

Provisions the infrastructure for GKE agent pods to reach the hub directly over the VPC at
`http://<VM internal IP>:8080`: pod CIDR discovery, a third firewall rule scoped to that CIDR, and
a static internal IP reservation for the hub VM so that address is stable across VM recreates.
Also verifies (and did not need to change) that the hub's auth layer accepts an agent token over a
plain, non-localhost URL. Delivering a `gke`-profile-scoped hub endpoint that actually points GKE
agents at the reserved address is a separate, hub-side code change and is out of scope here — see
Known limits below and the "not implemented" section at the end of this entry.

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
convention. It has no paired deny: port 8080 is already reachable VPC-internally as part of this
tier's existing base posture (the single-node VM's own IAP proxy setup), so this rule only narrows
how pods specifically reach it rather than opening anything new. `hybrid_teardown_check` was
extended to classify and report on this third rule the same way as the NFS pair; the underlying
preflight/delete functions needed no changes, since they already iterate generically over whatever
names are classified.

## Static internal IP reservation

`scion-hub-<hub>-internal-ip` reserves the hub VM's internal address so it survives a VM recreate,
marked the same way as the other base resources (an exact `scion-deployment=<hub>` description).
On a fresh VM, a free address is reserved (or an existing marked one reused) and the VM is created
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

## Known limits: the reservation and rule are provisioned but not yet consumed

Nothing in this tier today points the `gke` runtime's agents at the reserved internal IP. The hub
endpoint GKE agent pods receive is still whatever the hub process resolves for every runtime alike
(the same value used for admin invite links and the OIDC issuer default). Three ways to deliver a
`gke`-profile-scoped override were traced and rejected, each written up with file:line evidence:
pointing the general-purpose `server.hub.public_url` at the internal IP breaks admin invite links,
chat-bridge links, IAP-audience derivation, and (if configured) OIDC/`cloudrun_invoker` audiences;
the one genuinely profile-scoped settings key
(`profiles.<name>.harness_overrides.<harness>.env`) is unconditionally overwritten by the hub
dispatcher and the runtime broker before it can ever apply; and no third, hub-wide dispatch
endpoint distinct from `public_url` exists at all — every code path that could set it converges on
the identical field. Delivering this requires a hub code change and is not part of this tier.

`docs/deploy/agent-runbook-single-node-vm.md` and `docs/deploy/hybrid-tier.md` document the
discovery step, the three firewall rules, the reservation, the widened teardown table and
ownership-check wording, and this limitation, in the same style as the existing NFS/Kubernetes
sections.

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

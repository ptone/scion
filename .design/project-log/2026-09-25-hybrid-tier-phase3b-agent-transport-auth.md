# Hybrid Deployment Tier — Phase 3b: agent transport auth, replacing hub-allow

Branch `scion/hybrid-tier-p3`, same fork PR as the earlier Phase 3a and 3b slices.

## Overview

GKE agent pods now reach the hub through its existing public IAP URL — the same URL browser users
use — instead of the private VPC path (`http://<VM internal IP>:8080`) the earlier hub-allow rule
provisioned for. The private path is removed outright: hub-allow is deleted, and a new hub-deny
rule (`INGRESS DENY tcp:8080` from the discovered pod CIDR, priority 950, the same scheme as
nfs-deny) makes it explicit that nothing in the cluster's pod range can reach the hub VM directly.
The static internal IP reservation is kept, since the shared NFS PV's server field still needs a
stable address — that is now its only purpose.

## Cloud Run egress / hub-deny overlap

Before hub-deny (or either of the other two hybrid firewall rules) is created, a check reads the
Cloud Run IAP proxy's own egress subnet (`gcloud compute networks subnets describe`, the same
"default" subnet its direct-VPC-egress configuration already hardcodes) and refuses to continue if
that subnet's primary range could overlap the discovered pod CIDR — hub-deny would otherwise also
deny the proxy's own traffic to the hub, breaking every deploy regardless of whether the hybrid
tier is in use. A describe failure fails the same way; an unknown range is never treated as safe.

## Agent transport auth

A dedicated `scion-hub-<hub>-transport` service account is created or adopted (marked in its
description — service accounts have no labels — the same convention as every other hybrid-tier
resource; a same-name SA without the marker is refused, never adopted). Setup:

1. The project's IAP OAuth client ID is read (`gcloud iap settings get --resource-type=iap_web`)
   and used verbatim as the transport token's audience. An API error or an empty client ID fails
   closed.
2. The hub's own runtime service account is granted `roles/iam.serviceAccountOpenIdTokenCreator`
   on the transport SA specifically, not project-wide, and not the broader
   `serviceAccountTokenCreator`, which this never needs: `pkg/hub/transport_token.go`'s
   `gcpTransportMinter` calls the IAM Credentials API's `GenerateIdToken` directly
   (`transport_token.go:106`), and that call needs only the ID-token permission this role grants.
3. Once the Cloud Run proxy exists and has IAP enabled (Phase 4), the transport SA is granted
   `roles/iap.httpsResourceAccessor` on it — the same call and role the human operator's own access
   already uses.
4. `server.auth.transport` (`mode: iap`, `oidc_audience`, `platform_auth_sa`) is written into both
   the dev-mode and proxy-mode `settings.yaml` writes, gated on the hybrid tier: only
   GKE-dispatched agents leave the hub VM to reach it over IAP, so only they need a transport
   token.

IAM changes here can take on the order of a minute to propagate. `deploy.sh` itself never mints or
uses a transport token, so nothing in it blocks on that propagation; the very first agent dispatch
right after a deploy may see a transient authentication failure that a retry resolves, and this is
documented in the runbook rather than covered with a blind sleep.

Teardown removes the transport SA's Cloud Run IAP binding (best-effort, since deleting the Cloud
Run service already does this when that delete succeeds) and then the SA itself, marked only,
never touching an unmarked same-name SA — the same discipline as every other hybrid-tier resource.

## Docs and PR body

`docs/deploy/agent-runbook-single-node-vm.md` and `docs/deploy/hybrid-tier.md` are updated: the
firewall-rule bullet describes hub-deny instead of hub-allow, a new step documents agent transport
auth, the resource table lists hub-deny and the transport service account, and the interim
known-limit about nothing pointing GKE agents at the reserved internal IP is removed (resolved by
this change, not just superseded).

## Tests

Function-level coverage in `test_hybrid_tier.sh`: hub-deny's creation shape; the drift battery's
hub-allow cases rewritten for hub-deny's actual spec; the overlap check (an overlapping range, a
subnet-describe failure, and a disjoint range); a repo-wide grep confirming no hub-allow artifact
remains in `scripts/single-node-vm` or `docs`; and the transport-auth functions (client-ID
discovery, the transport SA's name truncation and create/adopt/refuse-unmarked behavior, the
token-creator grant's role and member, the Cloud Run accessor grant's resource type/service/role/
member, the rendered `settings.yaml` block, and teardown's marked/unmarked/absent/failure cases).
`test_deploy_wiring.sh`'s hub-allow-vs-nfs-deny delete-ordering test is rewritten for hub-deny, and
the guard wiring assertion checks only the internal-IP reservation's post-create re-describe call.

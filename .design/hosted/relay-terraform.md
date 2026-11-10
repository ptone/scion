# Design: production Terraform for separate conduit relays (ptone/scion#7420)

Status: **DRAFT r3.3** (OQ1, OQ3 resolved; Secret Manager long-term direction added) (tf-lead, 2026-11-05). Upstream base 4828ec7022. Nothing is built until ptone says yes to this plan.
r3 = r2 + an independent design review: 3 blockers, 5 majors, minors and nits, all addressed (see the changelog at the end).
Inputs: separate-relay doc; server-config reference (relay, conduit and database sections); experiments doc; the relay code (cmd/server_relay.go, pkg/conduit/relayrole/relayserver/server.go, pkg/conduit/relay/singleton.go, pkg/hub/conduit_relay_admit.go, pkg/store/relaydb); the hub Terraform modules; the scion-hub chart incl. `_helpers.tpl`; terraform-ci; conduit-lead's brief and answers (13:45Z, 13:54Z); Stage 1d/1d-b live-test material (examples only).
Conventions held: no stubs, no local-exec, additive `_iam_member` only, no imports, ordering through `boot_prerequisites` (never module-level depends_on), every module gets offline `tftest.hcl` tests.

---

## 1. Problem & Goals

Upstream can run conduit relays as separate processes (`scion server start --role relay`, hub `relay_mode: external`), but nothing deployable exists. There is no Terraform module, no chart, and no hub-side wiring for `relay_mode`, `peer_auth`, `peer_secret` or a hub internal Service. The only documentation is configuration prose.

Goals:
- **G1. HA relays you can deploy:** N ≥ 2 relay replicas next to a Terraform-managed GKE hub, from `terraform apply`, apart from the admin steps named in §5.
- **G2. Correct transport by construction:** every internal hop is https with a verified certificate, peer auth is HMAC, nothing plaintext crosses a non-loopback hop, and database traffic goes through the Cloud SQL Auth Proxy. The relay public listener is not reachable from outside the VPC.
- **G3. Safe upgrades:** hubs always roll before relays within one apply, and a failed relay start never displaces serving relays.
- **G4. Tested and in CI from phase 1:** module tests run offline (mock providers), chart render tests run in chart-ci, and terraform-ci needs no workflow edits.
- **G5. Least privilege:**
  - the relay has its own GCP identity, with metric write plus Cloud SQL client and instance-user only;
  - its own IAM database user, with no password anywhere and exactly the documented table rights;
  - it reads no hub secret apart from the shared peer secret.

Success = a hub-gke stack serves conduit sessions through 2 separate relays. Killing a relay pod sends its sessions back through the other, and an image bump rolls the hub then the relays with no manual sequencing.

**Stated limitation:** hub-gke runs one hub replica, rolled with Recreate (chart values L30-35). The hub stays a single point of failure, and every hub roll is a short conduit outage while relays have no hub to admit through. Relays make the *relay tier* HA and offload sessions from the hub process; they do not make the hub HA. Multi-replica hubs are separate work (the chart warns that >1 replica breaks pod-local state).

## 2. Non-Goals

- **A Cloud Run hub paired with relays.** hub-cloudrun exposes one container port, so it cannot serve the `internal_listen` relays need, and HA refuses run.app advertise. External-relay mode is hub-gke only here.
- **Relays on a GCE MIG** (§4 Alt B): deferred.
- **Direct dials to a relay from outside the VPC.** conduit-lead (13:54Z): not a v1 path; proxy ingress is the production path. An opt-in front is Phase 6.
- **`ingress: redirect`:** unsupported upstream; the hub refuses to start with it.
- **Turning on the `hub.conduit` experiment from Terraform.** It is a runtime admin setting stored in the database (Admin → Server Config → Experiments, or `scion hub experiments enable hub.conduit`), and is a named rollout step (§5).
- **Product code changes.** Hub-managed relay grants are a separate product companion (OQ1), not part of this module.
- **Multi-replica hubs,** and live hubs not managed by upstream Terraform (the community and integration VMs).

## 3. Proposed Design

### 3.1 Topology

```
                  clients / brokers / agents
                            │  https/wss (existing hub-lb: managed cert, IAP, timeout 86400s)
                            ▼
   ┌──────────── GKE Autopilot cluster, namespace <hub>-system ──────────────────┐
   │ hub pod (scion-hub chart, release <hub>, fullname F=<hub>-scion-hub)         │
   │   relay_mode: external, ingress: proxy, peer_auth: hmac                      │
   │   web/API :8080 ◄── NEG/LB      internal :9443 TLS (POD_IP advertise)        │
   │   cloud-sql-proxy sidecar (loopback)                                         │
   │        ▲  Service S = "<F>-relay" :9443 (NEW, ClusterIP) = hub_internal_url  │
   │        │                                                                     │
   │ relay pods ×N (NEW scion-relay chart, release <hub>-scion-relay)             │
   │   --role relay; public :9810 (/healthz only reachable use; VPC-only)         │
   │   internal :9443 TLS, advertise https://POD_IP:9443 ◄── hub proxied dials    │
   │   cloud-sql-proxy sidecar --auto-iam-authn (loopback)                        │
   │                                                                              │
   │ Secrets: <hub>-conduit-peer (peer secret); <hub>-conduit-tls-hub and         │
   │   <hub>-conduit-tls-relay (two leaves, same SAN, one CA)                     │
   └──────────────────────────────┬───────────────────────────────────────────────┘
                                  │ Cloud SQL Auth Proxy (TLS, IAM)
                     Cloud SQL (hub role owns schema; relay = IAM SA user)
```

Facts this rests on, verified in the code:
- The relay public listener (`server.hub.host:port`, default `0.0.0.0:9810`) serves only `/api/v1/conduit` and `/healthz`. Direct dials go through an arrival check; `/healthz` always returns 200 with no credential, for probes (relayserver/server.go:421-424). There is no native TLS on it.
- The internal listener is TLS unless it binds loopback, and advertises `https://POD_IP:<port>`. Relays must be addressable one by one, and a mismatch costs a `409 relay_target_mismatch` retry.
- `server.relay.public_url` is required (server_relay.go:84). It is used only for host binding of direct dials (conduit_relay_admit.go:250-259).
- **Startup failures are immediate:**
  - with `hub.conduit` off, the relay fails at once (the 60 s retry applies only when the setting cannot be *read*, server.go:235-252);
  - with a missing schema column it fails at registration ("upgrade the hubs first").
- The relay needs `database.driver: postgres` (the default is `sqlite`, server-config L502). It needs `server.mode: hosted` for the HA startup self-check (`RequireSelfCheck = hostedMode && isHADeployment`, server_relay.go:210).
- Shutdown deadline: 30 s with `shutdown_grace` unset; grace + 10 s when it is set (server_relay.go:246-252, 325-335).

### 3.2 Hub-side changes (scion-hub chart + hub-gke)

New chart values under `conduit:`. All default to today's behaviour, so the rendered output is unchanged when they are unset:

```yaml
conduit:
  relayMode: in_process        # in_process | external
  peerAuth: ""                 # "" (binary default auto) | hmac | oidc. external => must be hmac (guard)
  peerSecret:
    existingSecret: ""         # REQUIRED when external. Opaque Secret, delivered as env
    key: peer-secret           #   SCION_SERVER_HUB_CONDUIT_PEERSECRET (secretKeyRef)
  internalService:
    enabled: false             # REQUIRED true when external
    name: ""                   # REQUIRED when enabled; ClusterIP, port internalTls.port -> container port relay-tls
  internalTls: { ...existing; serverName set explicitly by hub-gke... }
```

- **Render guards:**
  - external without `peerSecret.existingSecret`;
  - external with internal TLS off (it must be on even at replicaCount 1);
  - external with `peerAuth` ≠ `hmac`;
  - external without `internalService`;
  - `internalService` with TLS off;
  - `internalService.name` empty.
- **Peer secret checksum annotation:** a pod annotation carrying a hash of the Secret's resourceVersion, supplied by Terraform as a value, so rotating the secret restarts the pods.
- The existing chart comment ("keep relay-tls out of any Service, Ingress or Gateway") is amended: not reachable from outside the cluster, and a ClusterIP Service only when `internalService.enabled`.
- **Peer secret as env, not file:** the setting has no `_file` variant. The chart's settings Secret is rendered from values, which would put the value into the helm values and so into state twice. hub-gke already delivers the session secret as env from a Secret. Hub and relay pods spawn no agent processes. (The docs prefer file-mounted settings, so this deviation is recorded as OQ6.)

**hub-gke:** one optional object variable. When it is null (the default), the chart values are byte-identical to today, and a test asserts it.

```hcl
variable "conduit" {
  type = object({
    internal_tls_secret = string     # resource attribute from module.conduit_pki (ordering: hub waits for the Secret)
    peer_secret_secret  = string     # resource attribute from module.conduit_pki
    peer_secret_version = string     # resource_version, for the checksum annotation
    internal_port       = optional(number, 9443)
  })
  default = null
}
locals {
  fullname    = strcontains(var.hub_name, "scion-hub") ? var.hub_name : "${var.hub_name}-scion-hub"   # mirrors _helpers.tpl L23-34
  server_name = "${local.fullname}-relay.${local.namespace}.svc"
  # ONE value feeds: conduit.internalTls.serverName, conduit.internalService.name ("<fullname>-relay"),
  # output conduit_hub_internal_url, and conduit-pki SANs. A test asserts all four agree.
}
output "conduit_server_name"      {}
output "conduit_hub_internal_url" {}   # https://<server_name>:<port>, null when conduit is null
output "hub_release_revision"     {}   # helm_release.hub revision (helm provider pinned; 2.x metadata[0].revision)
```

Note: `server_name` is needed by conduit-pki *before* hub-gke's release exists. It is computed from inputs in a tiny shared expression (`modules/conduit-pki` takes `fullname` and `namespace`, and hub-gke takes the pki outputs). There is no cycle, because conduit-pki depends only on the namespace name, which is derived from `hub_name`, not on the namespace resource. The Secrets are created by conduit-pki in that namespace, so conduit-pki waits on `module.hub_gke.namespace` (a resource attribute output).

### 3.3 New module `modules/conduit-pki`: shared secret material in the hub namespace

- **Peer secret:** `random_password` (48, no special characters; ≥32 octets and ≥16 distinct by construction) → `kubernetes_secret_v1 "<hub>-conduit-peer"`. A `rotation` keeper input exists. Rotation restarts hubs and relays via the checksum annotations, and there is an HMAC-mismatch window between the hub and relay rolls (no dual-secret support upstream), which the runbook documents.
- **Internal TLS**, `mode = "terraform"` (default):
  - one CA: `tls_private_key` ECDSA P-256 + `tls_self_signed_cert`, 10 y;
  - **two leaves, hub and relay**, each its own key, `tls_locally_signed_cert` 365 d, `early_renewal_hours = 1440`, both with SAN = `server_name` (+ `.cluster.local`), server+client auth. Every node verifies the same server name, so both leaves need the same SAN; separate keys only shrink the damage from one compromised key, but they cost nothing;
  - `kubernetes.io/tls` Secrets `<hub>-conduit-tls-hub` and `<hub>-conduit-tls-relay`, each with `ca.crt`.
  - Cert and key are re-read on change (≤1/min), so leaf renewal needs no restart. The CA is read at startup only.
- **Internal TLS**, `mode = "existing_secret"`: the operator names two existing TLS Secrets (cert-manager or Google CAS), and no key material is created.
- **Check:** warn when a leaf is within 30 days of expiry.
- **Outputs:** `peer_secret_name`, `peer_secret_version`, `hub_tls_secret_name`, `relay_tls_secret_name`, `server_name` (all resource attributes).

Terraform state holds the CA key, the leaf keys and the peer secret. That is the same class as the session secret and DB password state already holds, and it is OQ3. There is no relay DB password at all (§3.7).

### 3.4 New chart `deploy/helm/scion-relay`

A separate chart and helm release, so Terraform orders "hub rollout complete" before "relay rollout starts" (§3.6). Release `<hub>-scion-relay`, so fullname = release.

**Deployment:**
- `replicaCount` default 2;
- `topologySpreadConstraints` across zone and hostname;
- `RollingUpdate maxUnavailable 0, maxSurge 1`;
- env `POD_IP` and `POD_NAME` (`POD_NAME` is the default `instance_id`, unique per pod);
- args `server start --role relay --config /etc/scion-relay`.

**Shutdown:** `shutdown_grace` is set explicitly (default `20s`). `terminationGracePeriodSeconds` = grace + 10 s (relay deadline) + 5 s margin = 35 by default, and a guard enforces TGPS ≥ grace + 15.

**Probes:** readiness, liveness and startup probes are all `GET /healthz` on 9810. Startup failures exit before listening, so readiness never passes and the rollout stalls with the old pods serving. The startupProbe budget is 90 s, which covers the self-check plus registration.

**Cloud SQL:**
- a `cloud-sql-proxy` native sidecar (initContainer with `restartPolicy: Always`, as the hub chart does), flags `--private-ip --auto-iam-authn`, listening on 127.0.0.1:5432;
- DSN `postgres://<iam-user>@127.0.0.1:5432/<db>?sslmode=disable`, where plaintext is loopback only and the proxy carries TLS to the instance;
- there is no password, so the DSN is not secret and is rendered into settings.

**settings Secret** `settings.yaml` (no secret values):
- `server.mode: hosted`;
- `server.trusted_proxies: []`;
- `server.relay.{hub_internal_url, public_url}`;
- `server.hub.{host: 0.0.0.0, port: 9810}`;
- `server.hub.conduit.{internal_listen: 0.0.0.0:9443, internal_tls_cert_file, internal_tls_key_file, internal_tls_ca_file, internal_tls_server_name, peer_auth: hmac, lifetime_cap, shutdown_grace}`;
- `database.{driver: postgres, url, max_open_conns}`;
- observability/`gcp_project_id` for metrics.
- `peer_secret` arrives as env from the peer Secret.

**`public_url`:** `https://<release>-public.<ns>.svc`. It is a stable, non-colliding internal name that nothing dials in proxy mode. Phase 6 replaces it if a public front is ever added.

**Other templates:**
- **Volumes:** the relay TLS Secret, mounted as whole directories, as in the hub chart.
- **KSA** with the Workload Identity annotation → the relay GSA.
- **PDB** `minAvailable: 1` when replicas ≥ 2.
- **Checksum annotation** for the peer secret.
- **NetworkPolicy, default OFF in phase 1** (Phase 2, §3.8).

**Render guards:**
- non-loopback internal listener without TLS;
- missing peer Secret, TLS Secret or `hubInternalUrl`;
- `hubInternalUrl` not `https://`;
- `driver` ≠ postgres;
- `server.mode` ≠ hosted;
- `replicaCount` < 1;
- TGPS < grace + 15;
- an image older than the first release with the relay role (the hub chart's `relayImageCheck` pattern).

### 3.5 New module `modules/relay-gke`

```hcl
module "relay" {
  source              = "../../modules/relay-gke"
  project_id          = var.project_id
  hub_name            = var.hub_name
  namespace           = module.hub_gke.namespace
  image_repository    = var.image_repository
  image_digest        = coalesce(var.relay_image_digest, var.image_digest)   # check: equal unless allow_version_skew
  replicas            = var.conduit_relays.replicas                          # default 2
  deploy_release      = var.conduit_relays.grants_ready                     # B3 gate, see §3.7/§5
  hub_internal_url    = module.hub_gke.conduit_hub_internal_url
  server_name         = module.conduit_pki.server_name
  peer_secret_name    = module.conduit_pki.peer_secret_name
  peer_secret_version = module.conduit_pki.peer_secret_version
  tls_secret_name     = module.conduit_pki.relay_tls_secret_name
  sql_connection_name = module.shared_lookup.shared.sql.connection_name
  sql_instance_name   = module.shared_lookup.shared.sql.instance_name
  db_name             = module.cloudsql_database.db_name
  boot_prerequisites  = { hub_release_revision = module.hub_gke.hub_release_revision }
}
```

**Resources:**
- **Relay GSA** `<hub>-relay`, granted `roles/monitoring.metricWriter`, `roles/cloudsql.client` and `roles/cloudsql.instanceUser` (`google_project_iam_member`; conditions are optional and can be added later). Plus `google_service_account_iam_member` workloadIdentityUser for the `<ns>/<hub>-relay` KSA. No logWriter: GKE ships stdout. No Secret Manager or token-creator grants.
- **Relay DB user:** `google_sql_user` of type `CLOUD_IAM_SERVICE_ACCOUNT` for the relay GSA (name = GSA email without `.gserviceaccount.com`). IAM users get no table privileges by default, are not `cloudsqlsuperuser` members, and have no password (verified on the target instance in phase 1, §8.6).
- **`helm_release.relay`**, created only when `deploy_release = true`. It uses `wait = true`, values via `yamlencode`, and ordering through `terraform_data.boot_prerequisites`.
- **Checks:**
  - `relay_image_matches_hub`;
  - `replicas_ha` (warn when < 2);
  - `lifetime_cap_below_lb_timeout`;
  - `connection_budget`: (hub pool + N × (relay `max_open_conns` + 1 LISTEN)) ≤ the instance connection budget, extending the hub-cloudrun pattern.
- **Outputs:** `relay_gsa_email`, `relay_db_user`, `grant_sql` (the exact §3.7 block rendered for this user and database), `release_revision`.

The composition lives in `configurations/hub-gke` (an existing root, so no terraform-ci list edit). It is gated by `var.conduit_relays = null | { replicas = 2, grants_ready = false, … }`. When null, nothing relay-related exists, and a test asserts it. The root tests add `mock_provider "tls"` and `mock_provider "random"`.

### 3.6 Upgrade ordering (G3)

- `helm_release.relay` depends, via `boot_prerequisites`, on the hub release revision. In one apply that bumps the image, the hub release is updated first. `wait = true` returns when the hub pod is Ready, and the hub runs AutoMigrate before reporting Ready (confirmed by the review). Only then is the relay release updated.
- Relays roll `maxUnavailable 0`. A relay that fails startup never becomes Ready, the old relays keep serving, and helm's wait times out, failing the apply loudly.
- The hub roll itself (Recreate, 1 replica) is a short conduit outage. The relays stay up but cannot admit or re-check until the hub returns. This is the stated limitation in §1.
- The image digest is shared by default. Skew only with `allow_version_skew`.
- After any upgrade that changes the relay tables, the grants must be re-checked (§3.7). Until OQ1's product companion lands, the upgrade runbook includes re-running `grant_sql`. That block is idempotent, and the relay refuses to start on a missing privilege, so a missed re-grant fails loudly with old relays still serving.

### 3.7 Relay database role

Exact grants, proven in the Stage 1d-b live test (column-level; the hub role owns the schema and must issue them):

```
GRANT CONNECT ON DATABASE <db> TO "<iam-user>"; GRANT USAGE ON SCHEMA public TO "<iam-user>";
relay_instances:          SELECT, INSERT; UPDATE (generation, internal_endpoint, public_endpoint, started_at, last_seen, draining, features)
conduit_sessions:         SELECT, INSERT, DELETE; UPDATE (last_seen, draining)
conduit_principal_epochs: SELECT; INSERT (principal_kind, principal_id, epoch); UPDATE (epoch)
hub_settings:             SELECT
sequence of conduit_principal_epochs.id: USAGE
```

LISTEN on `scion_ev_conduit` needs no grant. The live test had to add `features` mid-run when the schema gained it. **The grants track the schema**, so a static script drifts on upgrade.

Terraform cannot issue these: local-exec is barred, and a PostgreSQL provider cannot reach the private-IP instance from CI.
- **v1 (pending OQ1 = A):**
  - the module outputs `grant_sql`;
  - an operator runs it once as the hub role (the existing `gcloud sql connect` path) after the first hub migration, then sets `grants_ready = true`, which creates the relay release;
  - the upgrade runbook re-runs it after schema changes.
- **Target:** a product companion where the hub, which owns migrations, grants exactly these rights to a configured relay role (for example `server.hub.conduit.relay_db_role`) at every migration. Then `grants_ready` defaults to true, and the manual step disappears from docs and acceptance.

### 3.8 Networking

- Same cluster and namespace: no VPC firewall rules. Clients keep dialing the hub's `/api/v1/conduit` through the existing hub-lb, whose 86400 s timeout exceeds `lifetime_cap` (3500 s); a check enforces it.
- **NetworkPolicy (Phase 2, default off):** it selects **relay pods only**:
  - 9443 ingress from hub-pod and relay-pod labels;
  - 9810 with no peer rules beyond node-originated probes. Dataplane V2 admits kubelet probes, which is verified on the target cluster.
- **Hub pods are not policed:** any policy selecting them would default-deny 8080 from the GFE and health-check ranges, because the NEGs reach pods directly, and it would also deny the proxy health port and hub-to-hub 9443. Policing hub pods needs its own design.
- Live-test lesson: a firewall admitting only the broker tag locked out legitimate dialers. There are no tag rules here, and the lesson binds Phase 6.

### 3.9 Cloud Run relay profile (conditional, pending OQ4)

conduit-lead: the singleton has never run live on Cloud Run (all stages ran on GCE VMs). Pairing is undecided: on 10-14 ptone floated a Cloud Run worker pool with Direct VPC for Cloud Run hub replicas.

The only singleton shape the code allows:
- an ingress sidecar on `$PORT` → relay internal listener on `127.0.0.1:9443`;
- `internal_advertise = https://<svc>.run.app`;
- `singleton: true`, min = max = 1, `cpu_idle = false`, `lifetime_cap` < the 3600 s timeout;
- ingress INTERNAL with unauthenticated invoke (HMAC is the gate);
- Direct VPC egress.

Phase 5 is conditional on ptone's decision, and its first step is a live probe.

### 3.10 Live-test lessons carried in (Stage 1d / 1d-b, GCE VMs, upstream bf7b8588: routing, load spread, key rotate/revoke and access revoke all passed, with 0 product failures)

- `relay_mode: external`, `ingress: proxy` and `peer_auth: hmac` must be explicit (`auto` turns the ingress off on GCP). The §3.2 guards enforce it.
- The relays used a load-balanced hub internal URL, so no relay depends on one hub. The §3.2 Service is the in-cluster equivalent, though with one hub replica it has one endpoint.
- The listener must accept on the address traffic is delivered to: `0.0.0.0:9443` plus the `POD_IP` advertise.
- TCP health checks stayed green with a dead backend behind a TLS front, so probes here are HTTP `/healthz` on the process.
- Bring-up order: database, then hubs (migrate), then grants, then relays. That is §3.6/§3.7/§5.
- **Test-only settings, never defaults here:** `grant_key_activation: 1m`, `stream_authz_max.user: 2m`, `class: test`, `--test-mode`, `--dev-auth`, throwaway CAs, `sslmode=disable` on a network hop (loopback to the proxy only).
- The test explicitly set `database.driver: postgres` and `server.mode: hosted`; §3.4 does too.

## 4. Alternatives Considered

**Alt A: relay template inside the scion-hub chart.**
- Against: one release rolls hubs and relays together, so there's no hub-first ordering. Rejected for v1.

**Alt B: HA relays on a regional GCE MIG.**
- Against:
  - the first tag-scoped firewall rules in the repo;
  - per-VM secret and cert delivery;
  - an instance-group LB shape hub-lb lacks;
  - a second rollout system.
- With the hub on GKE it adds cost for no availability gain. Deferred.

**Alt C: internal TLS via cert-manager or CAS.**
- For: no private keys in state.
- Against: new cluster add-ons and a paid CA pool. Offered as `conduit-pki mode = existing_secret`; the default is pending OQ3.

**Alt D: write-only or ephemeral secret arguments.**
- Against: needs Terraform ≥ 1.11, and CI pins 1.9.8. Revisit when CI moves.

**Alt E: the relay reuses the hub DB user.**
- Against: it breaks least privilege, since the relay could read token and secret tables. Rejected.

**Alt F: a separate `configurations/relay` root.**
- Against: three terraform-ci edits, a remote-state read, and it loses the in-apply ordering edge. Rejected.

**Alt G: a BUILT_IN (password) relay DB user via `google_sql_user`.**
- Against:
  - API-created users join `cloudsqlsuperuser`, which has CREATEROLE. On PostgreSQL ≤ 15 that can grant itself membership in the hub role, which defeats G5;
  - the password lands in state.
- Rejected in favour of the IAM SA user (§3.5). A fallback, if IAM auth is unavailable, would be `CREATE ROLE … LOGIN NOCREATEROLE NOINHERIT` inside the operator SQL block.

**Alt H: KSA principal IAM (direct Workload Identity Federation, no GSA).**
- For: fewer resources.
- Against: Cloud SQL IAM DB auth needs a service-account principal for `CLOUD_IAM_SERVICE_ACCOUNT` users, so a GSA stays. Rejected for v1.

## 5. Migration / Rollout

All additive and default-off: `conduit` and `conduit_relays` are null, and a test asserts the existing hub-gke plan is unchanged.
1. **Prerequisites (admin):**
   - the `hub.conduit` experiment is on;
   - targets meet the minimum version the separate-relay docs gate on.
2. **Apply 0** (`conduit_relays = { grants_ready = false }`):
   - conduit-pki Secrets, the relay GSA and IAM grants, and the IAM DB user;
   - **no relay release, hub still in_process.**
   - Then the operator runs the `grant_sql` output as the hub role.
3. **Apply 1** (`grants_ready = true`), the single cut-over:
   - the hub flips to `relay_mode: external` with internal TLS and the internal Service (Recreate, a short conduit outage);
   - then the relay release rolls out with N replicas.
   - Clients reconnect through the relays.
   - (Pre-staging relays while the hub is in_process is dropped as a default: the hub has no `peer_secret` then, so nodes would sign with different HMAC keys. OQ7 can make it opt-in later.)
4. **Verify:**
   - `scion.hub.conduit.eligible_relays` = N with no `cause`;
   - relay logs show sessions;
   - `scion.relay.*` metrics are exported.
5. **Rollback:** set `conduit_relays = null` and `conduit = null` and apply. The hub returns to in_process and the relays and Secrets are removed. Deleting these resources needs a per-resource ack, per the standing rule. Alternatively keep the relays with only the hub flipped.

Later upgrades are a single apply (§3.6), plus `grant_sql` re-run until the product companion.

## 6. Open Questions

1. ~~Relay DB grants~~ **RESOLVED: ptone chose A (22:23Z 11-05)**, "so long as the manual step is temporary and not long term design... should be clear in comments and docs". **Binding requirements:** (a) every place the step appears (relay-gke `grant_sql` output description, the `grants_ready` variable description, module README, docs page, upgrade runbook) labels it TEMPORARY and names the product companion issue that removes it; (b) the product companion is filed before Phase 1 merges (DONE: ptone/scion#7438, owner tf-lead, filed 22:24Z 11-05); (c) Phase 4 is not optional: it removes the step and defaults `grants_ready` to true.
2. ~~Direct dials~~ **RESOLVED** (conduit-lead 13:54Z): not v1; Phase 6 opt-in.
3. ~~Key material in Terraform state~~ **RESOLVED: ptone 04:26Z 11-07 "proceed as recommended" (stand-up reply to c5b77a4a) = A:** conduit-pki `mode = "terraform"` is the default (CA key, 2 leaf keys and the peer secret in state, like the session secret and DB password), and `existing_secret` mode is built as the supported alternative (Phase 3). **ptone 04:35Z 11-07 (msg 8465e773): "ok to hold secrets in state - long term will want ref secret manager"**, so the long-term target is a `secret_manager` mode: the peer secret and TLS material live in Secret Manager, Terraform holds only references, and pods read them via the GKE Secret Manager add-on (CSI) mounted as files. That is Phase 3b, below.
4. **(ptone, via conduit-lead) Cloud Run relay profile:** singleton vs worker pool vs none. It decides Phase 5. conduit-lead is asking.
5. ~~cloudsqlsuperuser~~ **RESOLVED by design:** the IAM SA DB user (Alt G); phase 1 verifies no superuser membership.
6. **(sec-review-3 / conduit-lead) Peer secret as env** (the session-secret precedent) vs. file: acceptable?
7. **(conduit-lead, later) Opt-in pre-staging:** could relays share a peer secret with an in_process hub, to make the cut-over seamless?
8. **(ptone) Phase-1 target and cost:** the tfgk5-h1 GKE stack (ours, live) or a throwaway hub-gke stack, and in which project? Two relay pods, the small Autopilot cost.

## 7. Implementation Phases

**Phase 1: vertical slice, 2 relays end to end** (needs ptone's yes + OQ1/OQ3/OQ8):
- scion-hub chart: `conduit.relayMode`, `peerAuth`, `peerSecret`, `internalService` + guards + checksum annotation + render tests.
- scion-relay chart: Deployment (2), settings, proxy sidecar, probes, TGPS, TLS mounts, KSA, PDB, guards + render tests. **chart-ci entries for scion-relay land in this phase.**
- `modules/conduit-pki` (terraform mode) + tests.
- `modules/relay-gke` (GSA, IAM grants, IAM DB user, gated helm release, checks, `grant_sql`) + tests.
- hub-gke `conduit` variable, the `server_name` local and outputs + a "null = unchanged" test + a "four names agree" test.
- `configurations/hub-gke` composition behind `conduit_relays` (+ mock tls/random).
- **Live:** Apply 0 → `grant_sql` → Apply 1 on the phase-1 target, then AC4–AC7.
- **Gate:** the slice proven end to end. Interface fixes land here before any fan-out.

**Phase 2 (conditional on Phase 1):** NetworkPolicy for relay pods (on by default once verified on Autopilot); rolling-upgrade drill (image bump: hub then relays in one apply); `connection_budget` tuning.

**Phase 3 (conditional on Phase 1):** `conduit-pki existing_secret` mode; peer-secret and leaf rotation runbooks; docs page "Deploying separate relays with Terraform" (hosted/ha), with the grant step and the stated hub single-point-of-failure limitation.

**Phase 3b (ptone long-term direction, after Phase 1):** a `conduit-pki mode = "secret_manager"`. Terraform creates the Secret Manager secrets and their accessor `_iam_member` for the hub and relay GSAs. Versions are added by the operator, or the CA is replaced by CAS. Pods mount them via the GKE Secret Manager add-on, so Terraform state holds only references. Open design point: the peer secret is env-only today, so it needs either a settings file assembled from mounted parts or a product `peer_secret_file`. It may get its own small plan.

**Phase 4 (conditional on OQ1):** product companion for hub-managed relay grants; then `grants_ready` defaults to true and the manual step is removed.

**Phase 5 (conditional on OQ4 + a live probe):** Cloud Run relay module.

**Phase 6 (opt-in, on request):** public direct-dial front.

## 8. Acceptance Criteria

1. With `conduit`/`conduit_relays` null, `terraform plan` of an existing hub-gke stack shows **no changes**, the chart renders byte-identically, and tests assert both.
2. `terraform test` passes offline for conduit-pki, relay-gke, hub-gke and configurations/hub-gke. terraform-ci picks up the new modules with no workflow edit, and chart-ci renders scion-relay and the new scion-hub values (phase 1).
3. Every render guard in §3.2/§3.4 fires for its invalid combination, with one test each. The "four names agree" test passes.
4. **Live (phase 1):**
   - `eligible_relays` = 2;
   - a conduit session (agent attach or port tunnel) is held by a relay pod, shown in its logs;
   - deleting that pod moves the session's reconnect to the other relay, with no hub restart.
5. **Live (phase 1 + phase 2 drill):** an image-bump apply updates the hub release before the relay release (apply log order and revisions), and no relay pod crash-loops on schema.
6. **Live:**
   - the relay GSA holds only metricWriter + cloudsql.client + cloudsql.instanceUser;
   - the relay DB user holds exactly the §3.7 grants and is not a member of `cloudsqlsuperuser` (an `information_schema`/`pg_auth_members` readback);
   - the relay pod environment holds no hub session, keyring or DB-owner secret.
7. **Live:** a plaintext dial to `http://<relay-pod>:9810/api/v1/conduit` from inside the namespace returns `403 transport_not_confidential`. With Phase 2 NetworkPolicy on, a pod outside the allowed labels cannot reach relay 9443, and probes still pass.
8. Apply 0 with `grants_ready = false` creates no relay release, and its plan test asserts that.
9. Docs state the prerequisites, the grant step (until Phase 4), the stated hub single-point-of-failure limitation, and the rollback.

---

### Changelog r2 → r3 (design review)
- **B1:** `database.driver: postgres` and `server.mode: hosted` added, with guards.
- **B2:** relay DB path = Cloud SQL Auth Proxy sidecar + IAM SA DB user (no password).
- **B3:** `grants_ready` gate and Apply 0.
- **M1:** a single `server_name` from the chart fullname, plus a test.
- **M2:** IAM user replaces BUILT_IN, and Alt G was added.
- **M3:** NetworkPolicy covers relay pods only, default-off until Phase 2; hub pods not policed.
- **M4:** single cut-over apply; the hub single point of failure is stated; pre-staging dropped.
- **M5:** explicit `shutdown_grace` + the TGPS formula.
- **Minors:** startup-failure facts corrected; `public_url` renamed; two leaves; checksum annotations; chart-ci moved to phase 1; phase 1 runs 2 replicas; connection budget check; Secret names passed as resource attributes.
- **Nits:** logWriter dropped; `trusted_proxies: []`; helm provider pin; mock tls; Alt H added.

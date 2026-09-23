# Substrate in-cluster broker (Phase 1)

Deploys the scion runtime broker as an in-cluster workload on the Substrate
GKE cluster, connected to the `scion-integration` hub, with the
`substrate` runtime profile it needs to run agents as Substrate actors.
This is Phase 1 scaffolding (substrate-integration `phase1-spec.md` §2.4):
a minimal manifest for testing the end-to-end slice, not the polished Helm
chart (that's Phase 2).

`broker.yaml` is a **plain manifest**, not a chart. It uses `${VAR}`-style
placeholders resolved with `envsubst` (or an equivalent templating pass)
before `kubectl apply`.

## Why an in-cluster broker

Substrate has no authorization on its control API or inbound router — see
`findings.md` §6. The broker needs both `api.ate-system.svc` (ateapi) and
`atenet-router` reachability, and D1 requires it run in-cluster rather than
reach in over a LoadBalancer/Ingress that would expose those unauthenticated
surfaces beyond the cluster boundary.

## Placeholders

Resolve every one of these before applying. None are secret; secrets are
handled separately (see "Secret creation" below).

| Placeholder | Meaning | `substrate-scion-test` value (from `infra/cluster.md`) |
|---|---|---|
| `BROKER_NAMESPACE` | Namespace the broker Deployment and its RBAC live in | `scion-substrate-broker` (suggested — not cluster-specific) |
| `BROKER_IMAGE` | Branch-built image containing the `scion` binary (see "Building the broker image") | `us-docker.pkg.dev/<project>/scion/broker@sha256:...` (build it yourself, see below) |
| `ATE_SYSTEM_NAMESPACE` | Namespace hosting ateapi (`api.<ns>.svc:443`) and `atenet-router` | `ate-system` (upstream default, confirmed for this cluster) |
| `SUBSTRATE_WORKER_NAMESPACE` | Namespace the actor **worker pods** run in, for the `pods`/`pods/log` RBAC (`GetLogs`) and the worker-namespace `NetworkPolicy` | `scion-agents` (confirmed via `infra/cluster.md`; not `ate-system` — see the note this superseded, kept below for history) |
| `HUB_ENDPOINT` | The `scion-integration` hub's URL | from `infra/cluster.md` / your hub deployment — not a Substrate-specific value |
| `HUB_BROKER_ID` | The broker's stable UUID from `scion runtime-broker register` (not secret — see below) | UUID printed by `register`; there is no fixed value until you actually register |
| `HUB_CONNECTION_NAME` | The `--name` used at `register` time; also the credentials JSON filename | `scion-integration` (suggested) |
| `CLUSTER_TRUST_BUNDLE_NAME` | The `ClusterTrustBundle` object verifying ateapi/router TLS | `servicedns.podcert.ate.dev:identity:primary-bundle` |
| `SANDBOX_CONFIG_NAME` | The `SandboxConfig` CRD instance actor templates use | `gvisor-default` |
| `SNAPSHOT_STORAGE_URI` | Bucket/prefix for actor snapshots | `gs://snapshot-substrate-scion-test` |

`worker_selector` and `egress_allow` are left as empty (`{}` / `[]`) in the
ConfigMap rather than templated — edit `broker.yaml` directly if the cluster
needs a `WorkerPool` pin or extra egress hosts. For `substrate-scion-test`,
`infra/cluster.md` names a worker pool pin:

```yaml
worker_selector:
  ate.dev/worker-pool: scion-agents
```

Replace the ConfigMap's `worker_selector: {}` line with the above before
applying to this cluster. (This is left as a hand-edit rather than an
envsubst placeholder because it's a map, not a scalar — templating a
variable-shaped map with plain `${VAR}` substitution doesn't generalize to
"no pin needed" the way an empty string does for the scalar placeholders
above.)

### `server.broker.broker_id` in the ConfigMap

`HUB_BROKER_ID` fills `server.broker.broker_id`
(`pkg/config/settings_v1.go` `V1BrokerConfig`), populated by the same
`scion runtime-broker register` run that produces the credentials Secret
(`cmd/broker.go`'s `runBrokerRegister` writes it to the registering
machine's global `settings.yaml`). It's a stable UUID, not a credential, so
it's fine in a ConfigMap. This is confirmed as the *current* (non-deprecated)
location: `settings_v1.go`'s legacy-key migration explicitly maps the old
flat `hub.brokerId` to `server.broker.broker_id`
("hub.brokerId is deprecated; moved to server.broker.broker_id").

The same struct also has `broker_token` — that field **is** the secret (the
older single-shared-secret auth mode) and must never go in this ConfigMap.
This deployment authenticates with the newer, per-connection
`hub-credentials/<name>.json` file instead (see "Secret creation"), so
`broker_token` is deliberately absent from `broker.yaml`.

## Building the broker image

The broker is the same `scion` binary the Hub uses (see `Dockerfile.hub`,
which already builds `/usr/local/bin/scion` from `./cmd/scion/` with
`ENTRYPOINT ["/usr/local/bin/scion"]`). Build and push a branch image from
this repo's `scion/substrate-integration` branch, e.g.:

```sh
docker build -f Dockerfile.hub -t "${BROKER_IMAGE}" .
docker push "${BROKER_IMAGE}"
```

Nothing broker-specific is baked into the image — `broker.yaml`'s `args`
select broker-only mode (`--enable-runtime-broker` without `--enable-hub`).

## Secret creation (out-of-band, never in this manifest)

The broker connects **outward** to the `scion-integration` hub using HMAC
credentials obtained by registering, not by anything baked into the image or
the ConfigMap.

1. From a machine with hub access (not the cluster), register:

   ```sh
   scion runtime-broker start --foreground &   # or as a background daemon
   scion runtime-broker register --name "${HUB_CONNECTION_NAME}" \
     --hub "${HUB_ENDPOINT}"
   ```

   This writes `~/.scion/hub-credentials/${HUB_CONNECTION_NAME}.json`
   (`BrokerCredentials`: `name`, `brokerId`, `secretKey`, `hubEndpoint`,
   `authMode`, ...) and prints the broker's `brokerId` — that's
   `HUB_BROKER_ID` above.

2. Create the Secret directly from that file — never paste its contents into
   a committed manifest:

   ```sh
   kubectl create secret generic scion-substrate-broker-hub-credentials \
     --namespace "${BROKER_NAMESPACE}" \
     --from-file="${HUB_CONNECTION_NAME}.json=${HOME}/.scion/hub-credentials/${HUB_CONNECTION_NAME}.json"
   ```

   `broker.yaml` mounts this Secret read-only (`defaultMode: 0400`) at
   `/home/scion/.scion/hub-credentials/${HUB_CONNECTION_NAME}.json` inside
   the broker container.

3. Rotation: re-run `register --force` on the registering machine, then
   `kubectl create secret ... --dry-run=client -o yaml | kubectl apply -f -`
   (or `kubectl delete secret` + recreate) with the refreshed file. There is
   no in-cluster rotation mechanism in Phase 1.

## Apply order

```sh
# 1. Resolve placeholders (envsubst reads ${VAR} from the environment).
# Values below are the substrate-scion-test example from infra/cluster.md;
# substitute your own cluster's values elsewhere.
export BROKER_NAMESPACE=scion-substrate-broker
export BROKER_IMAGE=...
export ATE_SYSTEM_NAMESPACE=ate-system
export SUBSTRATE_WORKER_NAMESPACE=scion-agents
export HUB_ENDPOINT=...
export HUB_BROKER_ID=...
export HUB_CONNECTION_NAME=scion-integration
export CLUSTER_TRUST_BUNDLE_NAME='servicedns.podcert.ate.dev:identity:primary-bundle'
export SANDBOX_CONFIG_NAME=gvisor-default
export SNAPSHOT_STORAGE_URI=gs://snapshot-substrate-scion-test

envsubst < deploy/substrate/broker.yaml > /tmp/broker.rendered.yaml
# Then hand-edit /tmp/broker.rendered.yaml's ConfigMap worker_selector: {}
# to `{ate.dev/worker-pool: scion-agents}` — see the placeholder table above.

# 2. Validate before touching the cluster (see "Validation" below).

# 3. Namespace + RBAC + ConfigMap first, so the Secret step below has
#    somewhere to land and the Deployment has everything it needs the
#    moment it starts.
kubectl apply -f /tmp/broker.rendered.yaml   # includes the NetworkPolicy too

# 4. Create the Secret (see "Secret creation" above) — do this AFTER step 3
#    creates the namespace, before the Deployment's pod actually starts
#    scheduling, or the pod will CrashLoopBackOff on a missing volume source
#    until the Secret exists.
kubectl create secret generic scion-substrate-broker-hub-credentials ...
```

`envsubst` isn't preinstalled everywhere; on Debian/Ubuntu it's in the
`gettext-base` package. If you don't have it, any templating pass that
resolves `${VAR}` (kustomize's `configMapGenerator` + patches, `sed`, etc.)
works equally well — the manifest itself has no envsubst-specific syntax.

## Validation

`kubectl apply --dry-run=client` needs a reachable API server for discovery
even in client-only mode, so it's not usable from a machine with no cluster
access at all. Two options that don't require one:

```sh
# Static schema validation (used to validate this manifest before any
# cluster existed): https://github.com/yannh/kubeconform
go install github.com/yannh/kubeconform/cmd/kubeconform@latest
kubeconform -strict -summary -kubernetes-version 1.31.0 /tmp/broker.rendered.yaml
# => Summary: 12 resources found in 1 file - Valid: 12, Invalid: 0, Errors: 0, Skipped: 0

# Once you *do* have cluster access:
kubectl apply --dry-run=client -f /tmp/broker.rendered.yaml
kubectl apply --dry-run=server -f /tmp/broker.rendered.yaml   # catches RBAC/CRD-shape issues client-side can't
```

## Verification commands (once applied to a real cluster)

```sh
# Broker pod is up and its own health endpoint is happy.
kubectl -n "${BROKER_NAMESPACE}" get pods -l app=scion-substrate-broker
kubectl -n "${BROKER_NAMESPACE}" exec deploy/scion-substrate-broker -- \
  wget -qO- http://localhost:9800/healthz

# Broker registered and heartbeating (from a machine with hub access):
scion runtime-broker status --broker "${HUB_BROKER_ID}"

# TokenRequest RBAC works (no permission error in logs / the runtime
# actually reaches ateapi — GetActor or ListActors calls should not fail
# with a 401/PermissionDenied from ateapi's own auth):
kubectl -n "${BROKER_NAMESPACE}" logs deploy/scion-substrate-broker | grep -i "substrate: mint ateapi token"

# ---- Router NetworkPolicy: substrate-lead requires this actually verified
# on the cluster, not just applied. Confirm the policy blocks an
# out-of-namespace caller and allows an in-namespace one: ----

# (a) From a throwaway pod OUTSIDE the broker namespace: must be refused.
kubectl run netpol-probe --rm -it --restart=Never \
  --namespace default \
  --image=curlimages/curl -- \
  curl -sS --max-time 5 "http://atenet-router.${ATE_SYSTEM_NAMESPACE}.svc/scion/v1/healthz"
# Expect: a timeout/connection error (dropped by the NetworkPolicy), not a
# 401/404 from the router itself — a response of any HTTP status means the
# policy did NOT block the request.

# (b) From a throwaway pod INSIDE the broker namespace: must connect
# (whatever HTTP response comes back — even a 404 — proves the packet
# reached the router; only a 200 with a real actor target would prove more
# than connectivity).
kubectl run netpol-probe --rm -it --restart=Never \
  --namespace "${BROKER_NAMESPACE}" \
  --image=curlimages/curl -- \
  curl -sS --max-time 5 -o /dev/null -w '%{http_code}\n' \
  "http://atenet-router.${ATE_SYSTEM_NAMESPACE}.svc/scion/v1/healthz"

# ---- Worker-namespace NetworkPolicy: the router policy above is only
# meaningful if a worker/actor pod's :80 (sciontool substrate-serve) is NOT
# also reachable directly, bypassing the router entirely. Verify that too,
# with a real actor pod IP (start at least one agent on this profile
# first): ----

WORKER_POD_IP=$(kubectl -n "${SUBSTRATE_WORKER_NAMESPACE}" get pods \
  -o jsonpath='{.items[0].status.podIP}')

# (c) From a throwaway pod OUTSIDE ate-system: must be refused.
kubectl run netpol-probe --rm -it --restart=Never \
  --namespace default \
  --image=curlimages/curl -- \
  curl -sS --max-time 5 "http://${WORKER_POD_IP}:80/scion/v1/healthz"
# Expect: a timeout/connection error. Any HTTP response (even a 4xx/5xx from
# substrate-serve) means the packet reached the pod and the policy did NOT
# block it.

# (d) From a throwaway pod INSIDE ate-system: must connect (any HTTP
# response, including a substrate-serve auth error, proves reachability).
kubectl run netpol-probe --rm -it --restart=Never \
  --namespace "${ATE_SYSTEM_NAMESPACE}" \
  --image=curlimages/curl -- \
  curl -sS --max-time 5 -o /dev/null -w '%{http_code}\n' \
  "http://${WORKER_POD_IP}:80/scion/v1/healthz"
```

## Known Phase 1 limitations

- **Bootstrap auth is the §5 fallback, not the identity-derived nonce.**
  `sciontool substrate-serve` (this branch) defaults to
  `FirstBootstrapWinsVerifier`: any bearer token is accepted, and only the
  single-shot "first bootstrap wins" check plus the two NetworkPolicies
  (router ingress restricted to the broker namespace, worker ingress
  restricted to `ate-system`) prevent an unauthorized bootstrap. The two
  policies are not redundant — the router policy is worthless on its own if
  a worker pod's :80 is *also* reachable directly by pod IP from outside
  `ate-system`, bypassing the router (and the single-shot check with it)
  entirely; the worker-namespace policy is what actually closes that path
  (round 1 review FYI, sb-rev). If either policy fails to apply, is
  misconfigured, or GKE Dataplane V2 / Network Policy enforcement isn't
  enabled on the cluster, **any pod that can reach a worker pod's IP, not
  just the router, can bootstrap an actor before the broker does.** This is
  why both verification steps above are not optional — an
  applied-but-unverified policy is not a control.
- **NetworkPolicy enforcement requires GKE Dataplane V2 (or another
  NetworkPolicy-enforcing CNI) to be enabled on the cluster.** A GKE cluster
  created without Dataplane V2 and without the legacy Calico add-on silently
  accepts NetworkPolicy objects and enforces none of them — `kubectl apply`
  succeeds either way, which is exactly why the verification commands above
  check actual traffic, not just object presence. Confirm via
  `gcloud container clusters describe <cluster> --format='value(networkConfig.datapathProvider)'`
  (expect `ADVANCED_DATAPATH`) before relying on this policy.
- **Metrics/monitoring scraping is not accounted for.** The NetworkPolicy
  only opens the router's client-facing ports (8080/8443/8081/8444) to the
  broker namespace. It does not add an explicit allow for Google Managed
  Prometheus scraping the Envoy sidecar's admin port (9901,
  `atenet-router-monitoring.yaml` upstream) or for kubelet's own
  readiness/liveness probes (port 9090). Most CNIs including GKE Dataplane V2
  exempt node-originated kubelet probes from NetworkPolicy, so those should
  keep working; GMP's collector traffic path is cluster-config-dependent —
  if router metrics go missing after applying this policy, that's the first
  thing to check, and this file's NetworkPolicy comment block has the
  specifics.
- **Worker-namespace pod selector is unscoped (`podSelector: {}`).** The
  `scion-worker-restrict-ingress` NetworkPolicy applies to every pod in
  `SUBSTRATE_WORKER_NAMESPACE`, not just actor/worker pods specifically —
  Phase 1 has no confirmed, stable label to narrow it further. If that
  namespace ever hosts non-actor workloads that legitimately need broader
  ingress, this policy will also restrict those; revisit once a real
  actor-pod label is confirmed.
- **No Helm chart, no template GC, no doctor integration.** This is
  Phase 1's minimal fixture (`phase1-spec.md` §2.4 explicitly scopes it this
  way); the polished chart is Phase 2.
- **No in-cluster credential rotation.** See "Secret creation" step 3.

## Files

- `broker.yaml` — Namespace, ServiceAccount, RBAC (TokenRequest-on-self,
  ClusterTrustBundle read, worker-namespace pod/log read), ConfigMap
  (substrate runtime profile), Deployment, and the two NetworkPolicies
  (router ingress restricted to the broker namespace; worker-namespace
  ingress restricted to `ate-system`).
- This README.

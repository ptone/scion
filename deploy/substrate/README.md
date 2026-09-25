# Substrate in-cluster broker (Phase 1)

Deploys the scion runtime broker as an in-cluster workload on the Substrate
GKE cluster, connected to the `scion-integration` hub, with the
`substrate` runtime profile it needs to run agents as Substrate actors.
This is Phase 1 scaffolding (`.design/kubernetes/substrate-runtime.md` §1):
a minimal manifest for testing the end-to-end slice, not the polished Helm
chart (that's Phase 2).

`broker.yaml` is a **plain manifest**, not a chart. It uses `${VAR}`-style
placeholders resolved with `envsubst` (or an equivalent templating pass)
before `kubectl apply`.

## Why an in-cluster broker

Substrate has no authorization on its control API or inbound router — see
`.design/kubernetes/substrate-runtime.md` §1. The broker needs both
`api.ate-system.svc` (ateapi) and `atenet-router` reachability, and must run
in-cluster rather than reach in over a LoadBalancer/Ingress that would
expose those unauthenticated surfaces beyond the cluster boundary.

## Operational prerequisites

- **Enabling NetworkPolicy enforcement (Calico or GKE Dataplane V2) on an
  existing Substrate cluster that wasn't created with it is not a
  no-downtime, apply-and-go change.** On `substrate-scion-test`
  (`infra/cluster.md`, "NetworkPolicy Enforcement (Calico)"), which
  enforces via the **Calico** add-on, not Dataplane V2, enabling it
  required:
  1. Enable the add-on and enforcement:
     `gcloud container clusters update <cluster> --update-addons=NetworkPolicy=ENABLED`,
     then `--enable-network-policy`.
  2. **Label existing nodes** `projectcalico.org/ds-ready=true` — the
     rolling update that installs Calico does **not** auto-label nodes that
     already existed, so `calico-node` never schedules onto them without
     this step.
  3. **Rolling-restart every pod that must be subject to policy**, not just
     "the workers": at minimum `atenet-router` in `${ATE_SYSTEM_NAMESPACE}`,
     the worker pods, and the `atelet` DaemonSet. Pods created before Calico
     keep GKE's PTP CNI (no `cali*` interface) and get **no enforcement** —
     **a router pod that was never restarted leaves
     `atenet-router-restrict-ingress` completely unenforced**, silently,
     while `kubectl get networkpolicy` still shows the object as applied.
     This is the one control the §5 first-bootstrap-wins fallback depends
     on; skipping the router in this restart is the failure mode that
     matters most here, not an edge case.
  4. **Recreate every golden `ActorTemplate` snapshot** — snapshots taken
     before enforcement was on fail `runsc restore` with **exit 128** once
     enforcement is live, because the snapshotted network namespace state
     is incompatible with Calico's CNI.
  Plan this as a maintenance window with a template rebuild, not as a
  same-day toggle, on any cluster where actors are already running. See
  "Known Phase 1 limitations" below for the separate question of whether
  enforcement is enabled at all, and check `infra/cluster.md` for this
  cluster's exact, already-executed procedure before repeating any of it.

## Placeholders

Resolve every one of these before applying. None are secret; secrets are
handled separately (see "Secret creation" below).

| Placeholder | Meaning | `substrate-scion-test` value (from `infra/cluster.md`) |
|---|---|---|
| `BROKER_NAMESPACE` | Namespace the broker Deployment and its RBAC live in. **Also the value the router NetworkPolicy's `namespaceSelector` is pinned to** (`atenet-router-restrict-ingress`, templated as `${BROKER_NAMESPACE}`, not hardcoded) — see the callout below the table. | `scion-substrate-broker` (suggested — not cluster-specific) |
| `BROKER_IMAGE` | Branch-built image containing the `scion` binary (see "Building the broker image") | `us-docker.pkg.dev/<project>/scion/broker@sha256:...` (build it yourself, see below) |
| `ATE_SYSTEM_NAMESPACE` | Namespace hosting ateapi (`api.<ns>.svc:443`) and `atenet-router` | `ate-system` (upstream default) |
| `SUBSTRATE_WORKER_NAMESPACE` | Namespace the actor **worker pods** run in, for the `pods`/`pods/log` RBAC (`GetLogs`) and for locating Substrate's own controller-generated worker `NetworkPolicy` (verification only — this manifest doesn't create it) | `scion-agents` (per `infra/cluster.md`) |
| `HUB_ENDPOINT` | The `scion-integration` hub's URL | from `infra/cluster.md` / your hub deployment — not a Substrate-specific value |
| `HUB_BROKER_ID` | The broker's stable UUID from `scion runtime-broker register` (not secret — see below) | UUID printed by `register`; there is no fixed value until you actually register |
| `HUB_CONNECTION_NAME` | The `--name` used at `register` time; also the credentials JSON filename | `scion-integration` (suggested) |
| `CLUSTER_TRUST_BUNDLE_NAME` | The `ClusterTrustBundle` object verifying ateapi/router TLS | `servicedns.podcert.ate.dev:identity:primary-bundle` |
| `SANDBOX_CONFIG_NAME` | The `SandboxConfig` CRD instance actor templates use | `gvisor-default` |
| `WORKER_SELECTOR_KEY` / `WORKER_SELECTOR_VALUE` | One label key/value pinning actors to a `WorkerPool` — matched against **the `WorkerPool` object's own `metadata.labels`**, not any Pod label (see the callout below the table) | `pool` / `scion-agents` |
| `SNAPSHOT_STORAGE_URI` | Bucket/prefix for actor snapshots | `gs://snapshot-substrate-scion-test` |

**`BROKER_NAMESPACE` is baked into the router NetworkPolicy at apply time,
not read live.** `atenet-router-restrict-ingress`'s `namespaceSelector`
matches on `${BROKER_NAMESPACE}`'s resolved value — a literal namespace
name in the rendered manifest, the same way every other placeholder here
resolves once at `envsubst` time (see broker.yaml, the router
`NetworkPolicy`'s `ingress[0].from[0].namespaceSelector`). If you later
rename the broker's namespace, or redeploy the broker into a different one,
that `NetworkPolicy` object still points at the *old* namespace until you
re-render and re-apply it with the new value — the broker's Deployment
moving does not update it. The symptom is exec/bootstrap timing out talking
to the router (`atenet-router.${ATE_SYSTEM_NAMESPACE}.svc`) even though the
broker pod itself looks healthy, because the router now refuses it. There
is no live binding here to break in the other direction: nothing but this
one `NetworkPolicy` object needs updating, but it does need updating,
explicitly, as part of any namespace move.

**`worker_selector` must match the target `WorkerPool`'s own registered
labels, not any Kubernetes Pod label.** `workerSelector.matchLabels` on an
`ActorTemplate` is matched against a **`WorkerPool` custom resource's own
`metadata.labels`** — not the label `atecontroller` separately stamps onto
the *generated worker Pods* (`ate.dev/worker-pool=<pool-name>`, the one the
router `NetworkPolicy` verification steps below use, correctly, for a
different purpose). These are two different label sets on two different
objects. See upstream `demos/multi-template/*.yaml.tmpl` for a worked
example: the demo's `WorkerPool` carries
`metadata.labels: {workload: multi-template-shared}`, and the
`ActorTemplate`s that select it use exactly that as
`workerSelector.matchLabels`. Getting this wrong doesn't fail to apply —
the manifest renders and `kubectl apply` succeeds — it fails at runtime:
`CreateActor` returns "no free workers" because zero `WorkerPool`s match
the selector, even though a pool with capacity exists.

**To find the correct value for your cluster:**
```sh
kubectl get workerpool -n "${SUBSTRATE_WORKER_NAMESPACE}" --show-labels

# Or, to extract just name + labels for scripting:
kubectl get workerpool -n "${SUBSTRATE_WORKER_NAMESPACE}" \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.labels}{"\n"}{end}'
```
Use one of the labels under that `WorkerPool` object's own `metadata`, not
anything you find by inspecting the worker Pods it created. Example output:
```
NAME           ...   LABELS
scion-agents   ...   pool=scion-agents,workload=scion-agents
```
`pool=scion-agents` is what `WORKER_SELECTOR_KEY`/`WORKER_SELECTOR_VALUE`
are set to in the placeholder table above, and what `broker.yaml`'s
ConfigMap templates `worker_selector` from directly. If a cluster genuinely
needs *no* pool pin, delete the `worker_selector` block from the rendered
manifest by hand instead.

**`egress_allow` entries are validated, and rejected entries block the
broker from starting an agent.** An actor's own `EgressPolicy` must never
let it reach the router or other in-cluster services — that would defeat
the NetworkPolicy above, which is what keeps the Phase 1 bootstrap-nonce
fallback (§5) safe. `pkg/config.V1SubstrateConfig.Validate` (checked when
the runtime is constructed, and again in `Run` before the `EgressPolicy` is
created) takes an allowlist-first approach: Phase 1 accepts *only* public
FQDNs (optionally wildcarded as `*.example.com`), and rejects everything
else, including:

- any IP address or CIDR, bare or wildcarded, IPv4 or IPv6 — Phase 1 has no
  IP/CIDR egress support at all, not even for public addresses;
- catch-alls: `all`, `*`, `0.0.0.0/0`, `::/0`, or any bare `*`-style entry;
- a hostname whose top-level domain isn't a real, ICANN-delegated one (this
  also catches Kubernetes-internal-shaped names like `*.default.pod` or
  `10-0-0-1.kube-system.pod`, and reserved zones like `home.arpa`, `.lan`,
  `.corp`, `.local`, `.internal`, `.localhost`);
- a hostname that is itself a public suffix rather than a name beneath one
  — including private/multi-tenant-platform suffixes such as
  `googleapis.com` or `github.io` (`storage.googleapis.com` and
  `foo.github.io` are still accepted; `googleapis.com` and `*.github.io`
  are not);
- hostnames ending in `.svc`, `.cluster.local`, `.internal`, `.local`, or
  `.localhost`.

See `ValidateEgressAllow`'s doc comment (`pkg/config/substrate_egress.go`)
for the exact rule set. None of this protects against a validly-public
hostname later resolving to a private or in-cluster address — DNS
rebinding, or a service like nip.io/sslip.io that does this by design.
Closing that gap requires a post-resolution check by the egress proxy
itself, which Phase 1 does not add.

### `egress_trust_bundle`: only needed under sdsmint

Substrate's plain `atenet-egress` only enforces `egress_allow` for TLS
*passthrough* on ADDRESS rules — every rule `egress_allow` emits is a
HostnameRule, which is enforced for HTTPS only under the **sdsmint** egress
gateway (`hack/install-ate.sh --deploy-atenet --experimental-use-sdsmint`).
Under sdsmint the gateway terminates every TLS connection and re-originates
it with a per-SNI leaf certificate chained to its own CA — not the origin's
— so an actor validating only the public roots rejects it and every HTTPS
request fails.

Set `runtimes.<name>.substrate.egress_trust_bundle: egress-mitm.ate.dev` —
the only trust bundle name the vendored Substrate version (d277088b)
resolves — **if and only if the cluster runs sdsmint and the actor makes any
HTTPS/TLS request.** When set, `buildActorTemplate`
(`pkg/runtime/substrate_template.go`) adds a `system-info` volume that
projects the gateway's CA to `/run/ate/trust-bundle.pem`, and points
`NODE_EXTRA_CA_CERTS`, `GIT_SSL_CAINFO`, `SSL_CERT_FILE`, and
`CURL_CA_BUNDLE` at that file, plus `SSL_CERT_DIR=/run/ate`. See
`docs/egress-trust-bundle.md` in agent-substrate/substrate for the full
guide, including every runtime's own env var.

**`SSL_CERT_DIR=/run/ate` is set deliberately, not left at its default.**
`SSL_CERT_DIR` makes the gateway CA **exclusive for Go and Python `ssl`**
(`crypto/x509`'s `SystemCertPool` and OpenSSL's default-path lookup both
*replace* their default directory list with it, rather than adding to it),
and it is **additive or ignored for curl, git and Node**. What it buys:
sciontool's own Go client trusts only the gateway CA, so a hub status
report succeeding is positive proof for Go, and a path that bypasses the
gateway fails closed with a TLS error rather than silently trusting a
public root. The cost: if the hub is ever reached without the gateway
re-originating the connection, status reports fail TLS.

It does **not** give the same guarantee for curl, git, or Node:

- **curl and git** on Debian (the base image, trixie) are both built
  against libcurl with `--with-ca-path=/etc/ssl/certs` compiled in; libcurl
  passes that CApath to OpenSSL explicitly, so `SSL_CERT_DIR` is never
  consulted and both still trust the full public root set *in addition to*
  the gateway CA. (OpenSSL's CApath lookup also needs certificates under
  subject-hash filenames — `<hash>.0` — so `/run/ate`, holding only
  `trust-bundle.pem`, would contribute nothing through CApath even where
  `SSL_CERT_DIR` is honored.) `CURL_CA_BUNDLE`/`GIT_SSL_CAINFO` still add
  the gateway CA as a trusted anchor for both — the config isn't broken —
  but a `curl`/`git` success on its own is not proof the gateway did the
  validating: a passthrough connection straight to the real origin would
  succeed too. To prove it, check the certificate issuer in `curl -sv`
  output (it should be the gateway CA, not the origin's own), or force curl
  to use only the projected bundle:
  `curl --capath /nonexistent --cacert /run/ate/trust-bundle.pem ...`.
- **Node** keeps its own bundled public roots regardless of `SSL_CERT_DIR`
  (which it doesn't read at all); `NODE_EXTRA_CA_CERTS` is additive on top
  of them, never a replacement.

**Setting this on a plain (non-sdsmint) install breaks every actor.**
Nothing backs the `egress-mitm.ate.dev` `ClusterTrustBundle` on a plain
install, so the actor fails to start entirely rather than merely losing
HTTPS: atelet logs `while populating system-info volume "system-info":
system-info projection "trust-bundle.pem": trust bundle
"egress-mitm.ate.dev": ClusterTrustBundle
"egress-mitm.ate.dev:mitm:primary-bundle" not found` on the node that was
going to host the actor, and the same text surfaces to the caller via
`CreateActor`/`Run`, wrapped by the resume step and gRPC.

Leaving `egress_trust_bundle` empty (the default) is byte-identical to
today: no `system-info` volume, no `/run/ate` mount, no Env — and it also
leaves `substrateTemplateName`'s content-address unchanged, so a plain
install's existing golden templates keep being reused rather than rebuilt.

**When set, the image needs util-linux ≥ 2.35** (`su`'s
`-w`/`--whitelist-environment` flag, `pkg/sciontool/substrate/execuser.go`).
This carries the CA-bundle vars across the `su -` login shell that
`sciontool substrate-serve exec` (the broker exec endpoint, `scion look`,
and `/scion/v1/exec` directly) would otherwise reset. scion's images
satisfy this already (≥ 2.35; Debian trixie ships util-linux 2.41). Plain installs
are unaffected either way: `-w` is only ever added when a CA-bundle var is
actually set, which never happens without `egress_trust_bundle` configured.

### `server.broker.broker_id` in the ConfigMap

`HUB_BROKER_ID` fills `server.broker.broker_id`
(`pkg/config/settings_v1.go` `V1BrokerConfig`), populated by the same
`scion runtime-broker register` run that produces the credentials Secret
(`cmd/broker.go`'s `runBrokerRegister` writes it to the registering
machine's global `settings.yaml`). It's a stable UUID, not a credential, so
it's fine in a ConfigMap. This is the current (non-deprecated) location:
`settings_v1.go`'s legacy-key migration maps the old flat `hub.brokerId` to
`server.broker.broker_id` ("hub.brokerId is deprecated; moved to
server.broker.broker_id").

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
# WORKER_SELECTOR_KEY/VALUE must match the target WorkerPool's own
# metadata.labels, NOT a Pod label — see the placeholder table's callout.
export WORKER_SELECTOR_KEY=pool
export WORKER_SELECTOR_VALUE=scion-agents
export SNAPSHOT_STORAGE_URI=gs://snapshot-substrate-scion-test

envsubst < deploy/substrate/broker.yaml > /tmp/broker.rendered.yaml

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
# => Summary: 11 resources found in 1 file - Valid: 11, Invalid: 0, Errors: 0, Skipped: 0

# Once you *do* have cluster access:
kubectl apply --dry-run=client -f /tmp/broker.rendered.yaml
kubectl apply --dry-run=server -f /tmp/broker.rendered.yaml   # catches RBAC/CRD-shape issues client-side can't
```

## Broker API exposure

The Deployment passes `--host=0.0.0.0` so the broker's own API (port 9800,
used by the `readinessProbe`/`livenessProbe` below and by
`scion runtime-broker status`) binds to all interfaces, not just loopback.
Without it, a standalone broker in `--hosted` mode (no `--enable-hub`)
binds to `127.0.0.1` by default — a safety net
(`cmd/server_foreground.go:915-920`) so a *fresh* broker with no HMAC keys
yet can still be reached locally by `scion runtime-broker register` before
those keys exist. kubelet's probes connect to the **pod IP**, not to
`127.0.0.1` inside the container's own network namespace, so that default
made both probes fail with connection-refused. (`cfg.Hub.Host` is also set
by this flag, but every code path that reads it is gated on
`--enable-hub`/`--enable-web`, both false here, so it has no other effect
in this deployment.)

**This is safe because the broker's own HMAC auth is unconditionally
"strict mode."** `pkg/runtimebroker/server.go` hardcodes
`BrokerAuthEnabled: true, BrokerAuthStrictMode: true` as the default (no
flag or settings key turns strict mode off for this deployment) and, more
importantly, **refuses to start at all** on a non-loopback host unless
valid HMAC keys are already loaded (`validateBrokerAuthStartup`,
`pkg/runtimebroker/server.go`). With `hub_endpoint` set in the ConfigMap,
`HubEnabled` is true, so the branch that actually runs here is the
hub-mode one at `:805-809` ("...in hub mode requires HMAC auth keys");
the general non-loopback check at `:816-819` is the one that would apply
if `HubEnabled` were false. Both refuse to start rather than fall open —
it does not fall open to unauthenticated non-loopback listening under any
configuration this manifest produces. Those keys come from the credentials
Secret (see "Secret creation" above), loaded from the mounted
`hub-credentials/<name>.json` before the broker's HTTP server starts
listening. Practical consequence: if that Secret is missing or empty when
the pod starts, the broker container now fails closed (crashes / restarts)
rather than serving `:9800` unauthenticated — reinforcing, not weakening,
why the Secret must exist before the Deployment's pod actually starts (see
"Apply order" step 4).

**Should there also be an ingress NetworkPolicy on the broker pod,
restricting `:9800` to kubelet only?** Worth doing, but not added here.
HMAC auth already means an unauthenticated caller can't actually perform
broker RPCs, so this wouldn't close a real authorization gap — but binding
to `0.0.0.0` does put the port in reach of every pod in the cluster that
can route to the broker namespace (not just the intended callers: the
node's kubelet, and the Hub reaching in for control-channel RPCs), which is
a larger blast surface for probing, DoS, or a future auth regression than
necessary. The reason it isn't added now: kubelet's own probe traffic is
node-originated, not namespace-scoped (see the "Kubelet health-check
probes are exempt" limitation below), so a policy restricting ingress to
"the broker namespace and nothing else" would need to reason about that
node-origin case explicitly rather than expressing "kubelet" as a
`NetworkPolicy` peer at all — `NetworkPolicy` has no concept of "the
kubelet" as a selector, only pod/namespace/IP block peers, and getting the
IP-block form right (which CIDR actually is the node range, whether GKE
exposes it consistently) needs cluster-specific input this manifest
doesn't have. Track as a Phase 2 hardening item once that's confirmed
rather than guessing at a policy that could break the probes it's supposed
to still allow.

## Bootstrap files: home delivery

`buildBootstrapFiles` (`pkg/runtime/substrate_bootstrap.go`) assembles the
`POST /scion/v1/bootstrap` payload's `files` array from three sources, in
this precedence order: the broker-composed agent home (`RunConfig.HomeDir`
— harness-config `home/`, the template home, and skills), then
`ResolvedAuth.Files`, then file-type `ResolvedSecrets`. When more than one
source targets the same in-container path, the later source wins (auth and
secret files override a same-path home file), and exactly one entry per
path reaches the wire — the dedup happens in the broker, not on
`substrate-serve`.

**This delivery model is deliberately narrower than Docker/Podman or
cloudrun-sandbox, which bind-mount or relocate `HomeDir` directly:**

- **Copy-in, one-way.** Files are read once, at bootstrap time, and written
  into the actor's filesystem. Nothing the actor writes afterwards is ever
  synced back to the broker's `HomeDir` — there is no persistent mount or
  ongoing sync.
- **Additive over the image's home, not a replacement for it.** The actor
  image's own `/home/scion` is not cleared or shadowed first; bootstrap
  only adds or overwrites the specific paths it ships.
- **Only regular files are shipped.** The walk
  (`homeBootstrapFiles`, `filepath.WalkDir`) never follows a symlink, and
  skips — counting but never reading — fifos, sockets and devices. None of
  those is "a file to ship," and reading one could block or behave
  unpredictably.

**No symlink traversal in a target's path.** File-secret and auth targets
must not traverse a symlink anywhere in their path, including system
symlinks such as `/var/run -> /run` or a merged-`/usr` image — such a target
fails bootstrap with HTTP `422` and the stable code `bootstrap_path_symlink`.
This is `substrate-serve`'s `mkdirAllTracked` guard (`pkg/sciontool/substrate/
helpers.go`): every existing path component is checked, not just the ones
under `/home`, so a target under a symlinked system path is rejected exactly
like a symlinked path under home. **Workaround:** use the resolved form of
the target instead of the symlinked one, e.g. `/run/secrets/...` rather than
`/var/run/secrets/...`. A separate stable code, `bootstrap_path_invalid`,
covers every other path-shape rejection (an empty or non-absolute path, or a
path component that exists but is not a directory). Both codes are returned
in the `422` response body alongside the rejected file's own path — never
its content — and the broker's `Run` error and log line for a rejected
bootstrap surface both.

**Size cap.** The total decoded size across every file (home + auth +
secrets combined, after dedup) is capped by
`maxBootstrapFilesTotalBytes` = 16 MiB (`pkg/runtime/substrate_bootstrap.go`).
That number is sized with headroom under the limits actually measured on
this path, not an assumed one:

- The router's ingress listener for this route has no request-body cap of
  its own: no buffer filter or `max_request_bytes` appears anywhere in its
  filter chain, and `per_connection_buffer_limit_bytes` is unset on both the
  listener and the upstream cluster — the 1 MiB default there is a
  flow-control watermark, not a cap. The body streams through uninspected,
  under a 300s route timeout.
- `sciontool substrate-serve`'s own `POST /scion/v1/bootstrap` handler is
  therefore the binding limit: it bounds the raw request body to
  `maxBootstrapBodyBytes` = 64 MiB (`pkg/sciontool/substrate/server.go`) via
  `http.MaxBytesReader`, which fails the read closed (a bounded error, not
  unbounded buffering) once the body exceeds it.

16 MiB decoded is ~21.3 MiB once base64-encoded (the 4/3 expansion), leaving
roughly 3x headroom under the 64 MiB serve-side limit even before the
surrounding JSON envelope (`env`, `start_cmd`, `control_token`, per-file
path/mode/quoting) is added — negligible next to file content for any
realistic env or `start_cmd` size. Exceeding the cap fails the run with an
error naming only the cap and the total size, never a path or file content.

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

# active_profile: substrate (in the ConfigMap) is doing its job: the
# broker's own heartbeat should never fall back to auto-detecting a local
# container runtime. Confirm no "docker ps failed" WARN appears — its
# presence means the broker's primary manager resolved to something other
# than substrate (e.g. active_profile missing or misspelled):
kubectl -n "${BROKER_NAMESPACE}" logs deploy/scion-substrate-broker | grep -i "docker ps failed"
# Expect: no output.

# ---- Router NetworkPolicy: verify this on the cluster, not just that it
# applied. Confirm the policy blocks an out-of-namespace caller and allows
# an in-namespace one: ----

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

# ---- Worker-namespace NetworkPolicy: this manifest does NOT create one.
# Substrate's own `atecontroller` WorkerPool controller auto-creates a
# per-WorkerPool NetworkPolicy (see broker.yaml's comment after the router
# NetworkPolicy for why this manifest does not also define one):
# podSelector `ate.dev/worker-pool: <pool-name>`, ingress only from pods
# labeled `app: atenet-router` *within* ATE_SYSTEM_NAMESPACE, all ports
# (cmd/atecontroller/internal/controllers/networkpolicy_controller.go,
# `buildNetworkPolicyApplyConfig`, upstream). What's left to verify here is
# that it actually exists and does its job — not to create it. ----

# (c) Confirm the controller-generated policy exists for the WorkerPool
# backing SUBSTRATE_WORKER_NAMESPACE. Its name is generated by
# internal/resources.NetworkPolicyName(poolName) upstream — a
# `substrate-<truncated-pool-name>-<5-hex-hash>` pattern — so match on the
# label the controller also sets rather than guessing the exact name:
kubectl -n "${SUBSTRATE_WORKER_NAMESPACE}" get networkpolicy \
  -l 'ate.dev/worker-pool' -o wide
# Expect at least one NetworkPolicy, owned by a WorkerPool (check
# `ownerReferences: kind: WorkerPool` via -o yaml if you need to confirm
# which pool). No result here means either the WorkerPool doesn't exist yet
# (start an agent on this profile first) or atecontroller isn't running —
# in either case, the fallback nonce currently has no worker-side
# protection at all.

# (d) Confirm it actually blocks direct pod-IP access from outside
# ate-system (the property that matters — object presence alone isn't a
# control). Requires a real worker pod IP (start at least one agent first).
# :8080 (readyz) is a real, plain-HTTP port on the worker pod's `ateom`
# container, convenient to probe with curl; the worker pod's actual
# actor-facing port is atunnel on :443, not the sandboxed actor's :80 (see
# broker.yaml's comment on the router policy for why).
WORKER_POD_IP=$(kubectl -n "${SUBSTRATE_WORKER_NAMESPACE}" get pods \
  -o jsonpath='{.items[0].status.podIP}')

kubectl run netpol-probe --rm -it --restart=Never \
  --namespace default \
  --image=curlimages/curl -- \
  curl -sS --max-time 5 "http://${WORKER_POD_IP}:8080/readyz"
# Expect: a timeout/connection error. Any HTTP response (even a 4xx/5xx)
# means the packet reached the pod and no applicable policy blocked it.

# (e) Confirm it still allows the router itself through (a pod labeled
# app: atenet-router in ate-system) — any HTTP response proves reachability:
kubectl run netpol-probe --rm -it --restart=Never \
  --namespace "${ATE_SYSTEM_NAMESPACE}" \
  --overrides='{"metadata":{"labels":{"app":"atenet-router"}}}' \
  --image=curlimages/curl -- \
  curl -sS --max-time 5 -o /dev/null -w '%{http_code}\n' \
  "http://${WORKER_POD_IP}:8080/readyz"

# (f) Confirm a plain pod in ate-system WITHOUT that label is refused, same
# as (d) — proves the controller-generated policy's peer selector is really
# an AND of namespace and pod label, not "namespace ate-system" alone:
kubectl run netpol-probe --rm -it --restart=Never \
  --namespace "${ATE_SYSTEM_NAMESPACE}" \
  --image=curlimages/curl -- \
  curl -sS --max-time 5 "http://${WORKER_POD_IP}:8080/readyz"
# Expect: refused, same as (d).
```

## Known Phase 1 limitations

- **Bootstrap auth is the §5 fallback, not the identity-derived nonce.**
  `sciontool substrate-serve` (this branch) defaults to
  `FirstBootstrapWinsVerifier`: any bearer token is accepted, and only the
  single-shot "first bootstrap wins" check plus two NetworkPolicies from
  *two different owners* prevent an unauthorized bootstrap:
  - the router policy, **created by this manifest**
    (`atenet-router-restrict-ingress`), restricting `atenet-router` ingress
    to the broker namespace;
  - the worker policy, **created by Substrate itself**, not this manifest —
    `atecontroller`'s WorkerPool controller auto-generates one per
    WorkerPool restricting worker-pod ingress to pods labeled
    `app: atenet-router` within `ATE_SYSTEM_NAMESPACE`
    (`cmd/atecontroller/internal/controllers/networkpolicy_controller.go`
    upstream). Do not also define a hand-written `NetworkPolicy` for the
    worker pods here: `NetworkPolicy` objects targeting the same pods are
    OR'd together, so a broader hand-written policy would only *widen*
    access beyond what Substrate's tighter, purpose-built one grants,
    undermining it rather than reinforcing it.

  Neither policy alone is sufficient — the router policy is worthless if a
  worker pod is *also* reachable directly by pod IP from outside
  `ate-system`, bypassing the router (and the single-shot check with it)
  entirely; that's exactly what the worker-side policy closes. If either
  fails to apply, is misconfigured, or GKE Dataplane V2 / Network Policy
  enforcement isn't enabled on the cluster, **any pod that can reach a
  worker pod's IP, not just the router, can bootstrap an actor before the
  broker does.** Because this manifest only owns one of the two policies,
  verifying the *other* one exists and works — not just assuming Substrate
  applies it correctly — is not optional; see verification steps (c)–(e)
  above.
- **NetworkPolicy enforcement requires GKE Dataplane V2 or the Calico
  add-on to be enabled on the cluster.** A GKE cluster created without
  either silently accepts NetworkPolicy objects and enforces none of them —
  `kubectl apply` succeeds either way, which is exactly why the
  verification commands above check actual traffic, not just object
  presence. Checking only `datapathProvider` gives a **false negative on a
  Calico cluster**: `substrate-scion-test` enforces via Calico
  (`infra/cluster.md`, "NetworkPolicy Enforcement (Calico)"), and Calico
  clusters don't report `datapathProvider=ADVANCED_DATAPATH` — that field
  only reflects Dataplane V2. Reading `datapathProvider` alone and
  concluding enforcement is off would be wrong on exactly the reference
  cluster this README is written against. Check both signals:
  ```sh
  gcloud container clusters describe <cluster> --location=<location> --project=<project> \
    --format='value(networkConfig.datapathProvider,networkPolicy.enabled,networkPolicy.provider)'
  ```
  The command prints tab-separated fields in that order. Expect either
  `datapathProvider=ADVANCED_DATAPATH` (Dataplane V2), **or**
  `networkPolicy.enabled=True` and `networkPolicy.provider=CALICO` — not
  both, and not neither. Where available, also confirm
  `addonsConfig.networkPolicyConfig.disabled` is `false` (a cluster can
  have the add-on enabled per the fields above while a subsequent config
  change disables it). `infra/cluster.md` documents that
  `substrate-scion-test` enforces via Calico and how it was enabled;
  capture this specific command's output directly from the cluster to
  confirm current state.
- **Kubelet health-check probes are exempt from NetworkPolicy on GKE, by
  design, on both enforcement backends.** Neither the router's readiness/
  liveness probes (port 9090) nor the worker pod's `readyz` probe (port
  8080, see the worker-namespace verification step's item (e)) need an
  explicit allow rule for this reason: GKE documents that kubelet's own
  HTTP/TCP health checks originate from the node, not from a pod or
  namespace, and are always permitted regardless of NetworkPolicy rules —
  true for both the legacy Calico-based add-on and Dataplane V2 (Cilium).
  This is a documented Kubernetes/GKE networking property, not something
  specific to this manifest, but it's exactly the kind of thing that's easy
  to get backwards when reasoning about "does restricting ingress break our
  own health checks?" — hence verification step (e), rather than trusting
  the documentation alone.
- **Metrics/monitoring scraping is not accounted for.** The router
  NetworkPolicy only opens its client-facing ports (8080/8443/8081/8444) to
  the broker namespace; it does not add an explicit allow for Google Managed
  Prometheus scraping the Envoy sidecar's admin port (9901,
  `atenet-router-monitoring.yaml` upstream). GMP's collector traffic path is
  cluster-config-dependent — if router metrics go missing after applying
  this policy, that's the first thing to check. (No equivalent PodMonitoring
  exists for worker/`ateom` pods in the upstream install, so there's nothing
  analogous to account for on the worker-namespace policy.)
- **The worker-side NetworkPolicy is Substrate's responsibility, not this
  manifest's.** This deployment has no control over it beyond verifying it
  exists (step (c) above) — if `atecontroller` isn't running, is
  misconfigured, or a future Substrate version changes or removes that
  controller's behavior, worker pods lose their ingress restriction with no
  signal from anything in `deploy/substrate/`. There is nothing to
  "revisit" here on our side; this is a standing dependency to be aware of,
  not a gap this manifest could reasonably close by duplicating
  Substrate's own policy (see broker.yaml's comment on the router
  NetworkPolicy for why a hand-written duplicate would weaken, not
  strengthen, this).
- **No Helm chart, no template GC, no doctor integration.** This is
  Phase 1's minimal fixture (`.design/kubernetes/substrate-runtime.md` §1
  scopes it this way); the polished chart is Phase 2.
- **No in-cluster credential rotation.** See "Secret creation" step 3.
- **Bootstrap files — including the composed agent home — cross the
  broker→router hop in plaintext.** The router endpoint in this fixture is
  `http://atenet-router.<namespace>.svc:80`, and the router client has no CA
  configured. The composed home (see "Bootstrap files: home delivery" above)
  can carry a `settings.json` with env values, and is treated as
  secret-grade for hygiene (never logged or put in an error) — but the wire
  exposure is the same plaintext hop auth and secret files already ride, not
  a narrower one. Out of scope to fix in Phase 1 for the same reasons the
  existing plaintext-hop risk is: this is a dedicated test cluster, router
  ingress is restricted to the broker namespace (see the NetworkPolicy
  discussion above), and the single-shot bootstrap check applies. A shared
  cluster needs the router's TLS listener with a CTB CA, or an end-to-end
  sealed payload, before this ships beyond a dedicated test cluster.
- **The dialer's trust material (`ca_file`/`cluster_trust_bundle`) is pinned
  for the broker process's lifetime, not re-read per agent start.**
  `pkg/runtime.NewSubstrateRuntime` memoizes one `*SubstrateRuntime` (and its
  gRPC `ClientConn`, dialed once) per distinct `V1SubstrateConfig` for the
  life of the process — see the "process-wide memoization" comment on
  `substrateRuntimesMu` in `pkg/runtime/substrate_runtime.go`. Rotating the
  CA behind the same `ca_file` path, or re-keying the same
  `ClusterTrustBundle` name, does not take effect until the broker process
  restarts — the existing `ClientConn`'s TLS config was built once, at first
  dial, and is never rebuilt. This is a known Phase 1 gap, not something
  this branch adds code to reload: a proper fix would build the dialer's
  `tls.Config` with `GetConfigForClient` or `VerifyPeerCertificate` so it
  re-reads the CA source per handshake, which is Phase 2 scope. Until then,
  **a CA rotation on this cluster requires
  restarting the broker Deployment** (a rolling restart is sufficient) to
  pick it up.

## Files

- `broker.yaml` — Namespace, ServiceAccount, RBAC (TokenRequest-on-self,
  ClusterTrustBundle read, worker-namespace pod/log read), ConfigMap
  (substrate runtime profile), Deployment, and the router NetworkPolicy
  (`atenet-router-restrict-ingress`, restricting router ingress to the
  broker namespace). The worker-side NetworkPolicy is Substrate's own —
  see "Known Phase 1 limitations" and the comment in `broker.yaml` after
  the router policy.
- This README.

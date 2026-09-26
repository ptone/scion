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

## Pin the agent image by digest (REQUIRED)

Substrate requires a digest-pinned agent image (`.design/kubernetes/substrate-runtime.md`
§3). Without one, `scion start` on a `substrate` profile fails closed
(`pkg/runtime/substrate_runtime.go:339`):

```
substrate: image "<image>" is not pinned by digest (@sha256:...); set a digest image in the agent's template or pass --image (tag resolution is a Phase 2 feature)
```

**Pinning the digest in the substrate profile's `harness_overrides` alone
is NOT sufficient on a default install.** Why:

- The shipped `claude` harness-config sets `image: scion-claude:latest`.
- The broker copies that image into the agent's own persisted config at
  provisioning time (`pkg/agent/provision.go`, the harness-config merge,
  written into the agent's `scion-agent.json`).
- At start, that agent/template config image outranks the profile's
  `harness_overrides` pin (`pkg/agent/run.go`'s image resolution order).

**Recommended:** create a substrate template that sets
`image: <registry>/scion-claude@sha256:<digest>`, and create substrate
agents from that template. A template image overrides the harness-config
image, affects only agents created from that template, and survives
`scion harness-config upgrade --force` (which reseeds the harness-config's
own `image:` key, but doesn't touch the template).

**Per-agent alternative:** pass `--image <registry>/scion-claude@sha256:<digest>`
at `scion start` time.

**Not recommended:** editing `image:` directly in the hub's `claude`
harness-config. That config is shared by every broker and runtime on the
hub:

- removing the key breaks non-substrate profiles, unless settings also set
  `harness_configs.claude.image`;
- existing agents keep whatever image they already resolved and must be
  recreated to pick up a change;
- a non-forced `scion harness-config upgrade` only fills in missing or
  empty keys (`mergeMissingMapValues`, `pkg/config/harness_config_upgrade.go`),
  so a digest pin survives it, but *deleting* the `image:` key instead of
  editing it gets the key re-added as `scion-claude:latest` on the next
  upgrade;
- `scion harness-config upgrade --force` reseeds `image:` from the bundled
  default unconditionally, discarding any pin here regardless of how it was
  set — re-apply it afterward if you go this route.

**Diagnostic:** `scion-agent.json` (the `image` field, in the agent's
directory on the broker) carries the image the agent's persisted config
resolved to, which `start` uses unless `--image` is passed — `--image` is
applied only for that one `start` call (`pkg/agent/run.go:353`) and is
never written back to `scion-agent.json`.

## Warm the template before first use

**Creating the first agent on a given template builds its golden
ActorTemplate snapshot, which can take on the order of a minute or more**
(`.design/kubernetes/substrate-runtime.md` §3) — and the hub's
control-channel dispatch timeout is hard-coded at 120s
(`pkg/hub/server.go`). If the golden build plus actor bootstrap doesn't
finish inside that window, the hub gives up and sends a rollback delete
that races the still-in-progress create: the broker's create finishes
anyway, leaving a **RUNNING, unbootstrapped actor the hub doesn't know
about**, holding a worker indefinitely
(`.design/kubernetes/substrate-runtime.md` §10 — this is a generic
hub/broker dispatch-timeout gap, not substrate-specific, but a cold
template build is the easiest way to hit it on this runtime).

**Warming a golden for a brand-new project by calling `CreateActorTemplate`
directly needs the project's atespace to exist first.** An atespace is
created lazily, on the broker's first `ensureAtespace` call for that project
(normally the project's first agent create) — it does not exist merely
because the project does. An operator pre-warming a golden this way, ahead
of that first create, must create the atespace directly first —
`kubectl ate create atespace <atespace>`, the same call `ensureAtespace`
itself makes — or `CreateActorTemplate` fails closed with
`FailedPrecondition`. (The `scion start` warm-up below creates the atespace
itself.)

**Before pointing real traffic at a new template** (a new image digest,
resource shape, sandbox class, `worker_selector`, or `egress_trust_bundle`
— anything that changes the content-addressed template name, §3), warm it
with a disposable agent first:

```sh
# Start a throwaway agent on the profile/template you're about to use for
# real. The first create against a new template builds the golden snapshot
# and can take well over a minute — this is expected here.
scion start --profile substrate warm-template-check "echo warm"

# Wait for it to actually reach `running` before treating the template as
# warm — don't just wait for the command to return.
scion list | grep warm-template-check

# Clean up the throwaway agent. The template and its golden snapshot are
# NOT deleted with it (template GC is a Phase 2 item) — that's the point:
# the next real create against the same template reuses the now-ready
# golden and takes the fast (sub-second broker dispatch) path.
scion delete warm-template-check
```

Once the golden is `READY`, subsequent creates against that template are
well inside the hub's dispatch timeout.

## Operational prerequisites

- **Enabling NetworkPolicy enforcement (Calico or GKE Dataplane V2) on an
  existing Substrate cluster that wasn't created with it is not a
  no-downtime, apply-and-go change.** On `substrate-scion-test`, which
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
     This is one of the two controls the §5 first-bootstrap-wins fallback
     depends on (see "Known Phase 1 limitations" below — neither policy
     alone is sufficient); skipping the router in this restart is the
     failure mode that matters most here, not an edge case.
  4. **Recreate every golden `ActorTemplate` snapshot** — snapshots taken
     before enforcement was on fail `runsc restore` with **exit 128** once
     enforcement is live, because the snapshotted network namespace state
     is incompatible with Calico's CNI.
  Plan this as a maintenance window with a template rebuild, not as a
  same-day toggle, on any cluster where actors are already running. See
  "Known Phase 1 limitations" below for the separate question of whether
  enforcement is enabled at all, and check your own cluster's change
  history for the exact, already-executed procedure before repeating any
  of it.

## Placeholders

Resolve every one of these before applying. None are secret; secrets are
handled separately (see "Secret creation" below).

| Placeholder | Meaning | `substrate-scion-test` value (example only) |
|---|---|---|
| `BROKER_NAMESPACE` | Namespace the broker Deployment and its RBAC live in. **Also the value the router NetworkPolicy's `namespaceSelector` is pinned to** (`atenet-router-restrict-ingress`, templated as `${BROKER_NAMESPACE}`, not hardcoded) — see the callout below the table. | `scion-substrate-broker` (suggested — not cluster-specific) |
| `BROKER_IMAGE` | Branch-built image containing the `scion` binary (see "Building the broker image") | `us-docker.pkg.dev/<project>/scion/broker@sha256:...` (build it yourself, see below) |
| `ATE_SYSTEM_NAMESPACE` | Namespace hosting ateapi (`api.<ns>.svc:443`) and `atenet-router` | `ate-system` (upstream default) |
| `SUBSTRATE_WORKER_NAMESPACE` | Namespace the actor **worker pods** run in. Not read by anything this manifest applies (agent logs are not supported on the substrate runtime in this phase — see "Logs" below); used only for locating Substrate's own controller-generated worker `NetworkPolicy` and for the verification commands below (verification only — this manifest doesn't create either) | `scion-agents` (the `substrate-scion-test` example) |
| `HUB_ENDPOINT` | The `scion-integration` hub's URL | from your hub deployment — not a Substrate-specific value |
| `HUB_BROKER_ID` | The broker's stable UUID from `scion runtime-broker register` (not secret — see below) | UUID printed by `register`; there is no fixed value until you actually register |
| `HUB_CONNECTION_NAME` | The `--name` used at `register` time; also the credentials JSON filename | `scion-integration` (suggested) |
| `CLUSTER_TRUST_BUNDLE_NAME` | The `ClusterTrustBundle` object verifying ateapi TLS (the router hop is plaintext in Phase 1 — see below) | `servicedns.podcert.ate.dev:identity:primary-bundle` |
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
  `10-0-0-1.kube-system.pod`, and made-up/reserved zones like `.lan`,
  `.corp`, `.local`, `.internal`, `.localhost`);
- a hostname under the special-use `arpa` or `onion` top-level domains
  (`arpa` is itself ICANN-delegated, so it needs its own rejection rule
  rather than the ICANN-delegation check above) — this covers `home.arpa`;
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

- **curl (Debian, the base image, trixie) and git (vendored from
  Chainguard's git image, not Debian's own — see `image-build/core-base/
  Dockerfile`)** are both built against libcurl with
  `--with-ca-path=/etc/ssl/certs` compiled in; libcurl
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
the substrate-serve `/scion/v1/exec` path (the broker exec endpoint,
`scion look`, and `/scion/v1/exec` calls directly) would otherwise reset. scion's images
satisfy this already (≥ 2.35; Debian trixie ships util-linux 2.41). Plain installs
are unaffected either way: `-w` is only ever added when a CA-bundle var is
actually set, which never happens without `egress_trust_bundle` configured.

Note: the harness child and its tmux session start with cwd resolved to
`SCION_WORKSPACE_PATH` (default `/workspace`), falling back to the scion
user's own home directory if that path (or any ancestor) isn't searchable
by the scion uid/gid — never substrate-serve's own root `$HOME`, since the
child's chdir happens after the privilege drop (see
`resolveSubstrateHarnessCwd`, `cmd/sciontool/commands/substrate_serve.go`).
Resolution runs inside `RunInit` after the workspace clone and the
post-pre-start-hook ownership fixup, so a git-clone agent's `/workspace` is
checked once it is scion-owned. Neither candidate usable fails the harness
start rather than ever falling back to `/`. A later exec via `su -` (as
above) still resets cwd to `$HOME` on login, exactly as `docker exec ... su
-` does on every other runtime, so that part is expected and unchanged.

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
# Values below are the substrate-scion-test example values;
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
#    scheduling. A missing (non-optional) Secret volume source leaves the
#    pod in ContainerCreating with a FailedMount event, not CrashLoopBackOff,
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
# => Summary: 9 resources found in 1 file - Valid: 9, Invalid: 0, Errors: 0, Skipped: 0

# Once you *do* have cluster access:
kubectl apply --dry-run=client -f /tmp/broker.rendered.yaml
kubectl apply --dry-run=server -f /tmp/broker.rendered.yaml   # catches RBAC/CRD-shape issues client-side can't
```

## Broker API exposure

The Deployment passes `--host=0.0.0.0` so the broker's own API (port 9800,
used by the `readinessProbe`/`livenessProbe` below and, for
`scion runtime-broker status --broker`, via the Hub API rather than a
direct connection — plain `status` with no `--broker` flag probes the
caller's own local broker instead) binds to all interfaces, not just loopback.
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
"strict mode."** The live `ServerConfig` built for this deployment
(`cmd/server_foreground.go:2536-2537`) hardcodes `BrokerAuthEnabled: true,
BrokerAuthStrictMode: true` (no flag or settings key turns strict mode off
here) and, more importantly, **refuses to start at all** on a non-loopback
host unless valid HMAC keys are already loaded
(`validateBrokerAuthStartup`, `pkg/runtimebroker/server.go`). With
`hub_endpoint` set in the ConfigMap, `HubEnabled` is true, so the branch
that actually runs here is the hub-mode one at `:805-807` ("...in hub mode
requires HMAC auth keys"); the general non-loopback check at `:817-818` is
the one that would apply
if `HubEnabled` were false. Both refuse to start rather than fall open —
it does not fall open to unauthenticated non-loopback listening under any
configuration this manifest produces. Those keys come from the credentials
Secret (see "Secret creation" above), loaded from the mounted
`hub-credentials/<name>.json` before the broker's HTTP server starts
listening. Practical consequence: an **empty or invalid** Secret reaches
the broker's startup, so the container fails closed (`CrashLoopBackOff`,
with the startup-refusal log line) rather than serving `:9800`
unauthenticated. A Secret that is **missing entirely** never reaches that
code at all — the non-optional Secret volume keeps the pod in
`ContainerCreating`, and `kubectl describe pod` shows a `FailedMount`
event, until the Secret exists. Either way this reinforces, not weakens,
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
# Broker pod is up and READY (the readinessProbe already hits /healthz;
# the broker image has no wget/curl, so don't exec a check into it).
kubectl -n "${BROKER_NAMESPACE}" get pods -l app=scion-substrate-broker

# Or hit /healthz from your own workstation:
kubectl -n "${BROKER_NAMESPACE}" port-forward deploy/scion-substrate-broker 9800:9800 &
curl -s localhost:9800/healthz

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

- **`scion stop` destroys the agent on substrate.** Phase 1 has no suspend
  primitive, so `Stop` is `Delete` (`.design/kubernetes/substrate-runtime.md`
  §4's `Stop` row): the actor, its workspace, and its worker slot are all
  gone, not paused. A later `scion start` provisions a brand-new actor from
  the template, not a resumed one — any uncommitted work in the stopped
  actor's workspace is lost. Push before stopping. Suspend/resume (keeping
  the workspace and freeing the worker without discarding either) is a
  Phase 2 item (§11).
- **Bootstrap auth is the §5 fallback, not the identity-derived nonce.**
  `sciontool substrate-serve` (Phase 1) defaults to
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
  applies it correctly — is not optional; see verification steps (c)–(f)
  above.
- **NetworkPolicy enforcement requires GKE Dataplane V2 or the Calico
  add-on to be enabled on the cluster.** A GKE cluster created without
  either silently accepts NetworkPolicy objects and enforces none of them —
  `kubectl apply` succeeds either way, which is exactly why the
  verification commands above check actual traffic, not just object
  presence. Checking only `datapathProvider` gives a **false negative on a
  Calico cluster**: `substrate-scion-test` enforces via Calico, and Calico
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
  change disables it). `substrate-scion-test` enforces via Calico; capture
  this specific command's output directly from your own cluster to confirm
  current state.
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
  own health checks?" None of the verification steps above test this
  specific exemption directly (step (e) tests router-label reachability,
  not kubelet probes) — rely on the GKE documentation for this property.
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
  Phase 1's minimal fixture — template GC and doctor integration are listed
  as Phase 2 items in `.design/kubernetes/substrate-runtime.md` §11 (see
  also §9 for current template/GC behavior); a Helm chart isn't discussed
  in that doc at all. The polished chart is Phase 2.
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
  life of the process — see the memoization comment on `substrateRuntimesMu`
  in `pkg/runtime/substrate_runtime.go`. Rotating the
  CA behind the same `ca_file` path, or re-keying the same
  `ClusterTrustBundle` name, does not take effect until the broker process
  restarts — the existing `ClientConn`'s TLS config was built once, at first
  dial, and is never rebuilt. This is a known Phase 1 gap, not something
  Phase 1 adds code to reload: a proper fix would have the dialer's
  client-side TLS config re-read the CA source per handshake (e.g. via
  `tls.Config.VerifyPeerCertificate`/`VerifyConnection`, or custom gRPC
  transport credentials — `GetConfigForClient` is a server-side callback
  and doesn't apply here), which is Phase 2 scope. Until then,
  **a CA rotation on this cluster requires
  restarting the broker Deployment** (a rolling restart is sufficient) to
  pick it up.
- **Logs.** `scion logs` and the web log view return an explicit
  not-supported error for a substrate agent in this phase
  (`pkg/runtime.ErrLogsNotSupported`, `.design/kubernetes/substrate-runtime.md`
  §4): worker pods are shared across atespaces and users, so an unfiltered
  worker-pod log read would return other tenants' actor output — and any
  worker-level lines naming other atespaces — alongside the caller's own.
  Operators can still read a specific actor's own output directly:
  ```sh
  kubectl logs -n <worker-namespace> <worker-pod> -c ateom | grep <actor-uid>
  ```
  Filter by the actor's UID, not its name — actor names are reused across a
  worker's lifetime, so a name-based filter can pick up another actor's
  lines.

## After a broker restart

The broker keeps two things only in memory, process-wide, never persisted
(`pkg/runtime/substrate_runtime.go`): the per-agent record `List` needs to
report a project-scoped agent's slug and project labels, and the
`control_token` minted at bootstrap for `Exec`/`Message`. A broker restart —
a pod reschedule, a rolling update, an OOM kill, anything that starts a new
process — drops both for every actor a *previous* process created. The
actors themselves, their egress policies, and their workers are untouched on
the cluster; only this broker's memory of them is gone (ptone/scion#1808).

**The invariant: no delete or stop reports success while the actor exists,**
with two exceptions: an actor already in `ACTOR_STATE_DELETING`, including
the pre-restart case in consequence (d) below (the `DELETING` exclusion has
nothing to do with a restart by itself, but can combine with one); and a
pre-restart actor under a second substrate profile on a different ateapi
endpoint, which is not probed until that profile's first `Run` (see "The
record-less-actor probe only reaches..." below; not applicable to this
deployment, which has one substrate profile).
Three exit paths matter, and all three are covered, not just the main one:
- **Delete/stop of a pre-restart agent by its project-scoped slug returns
  HTTP 409 `substrate_agent_identity_unknown`** — whether the slug resolves
  to nothing at all, or resolves to a file-only target because this
  project's directory happens to survive the restart (a workstation broker,
  or any `$HOME` that isn't wiped on restart the way this deployment's
  container filesystem is). The response names the atespace and how many
  record-less actors it holds. This is intentional: a 404 is treated as an
  idempotent completed delete by the hub, and a 202 as a completed stop, and
  a file-only delete actually removes the files — every one of those would
  let the actor, its egress policy, and its worker leak with no further
  signal. An explicit conflict, requiring an operator, is the safe failure
  here.
- **If the check itself cannot run — the runtime listing needed to look for
  record-less actors, or (on stop) the slug lookup that would otherwise
  resolve the target, fails — delete/stop return an explicit 5xx**, never
  the idempotent 404/202 a failed check would otherwise fall back to. An
  unresolved outcome is never treated as a safe one.
- **Exec** on a pre-restart agent fails with an explicit error naming the
  missing control token. This is permanent for that specific actor:
  `sciontool substrate-serve`'s bootstrap endpoint is one-shot
  (`.design/kubernetes/substrate-runtime.md` §5), so there is no way for a
  new broker process to re-mint or recover the token an old process already
  used.
- **Message** is different: the hub accepts it and its API response says the
  message was delivered, but it is never delivered, and the hub's message
  record stays in the dispatched state rather than being marked failed. A
  user-sent message is dispatched to the broker without the message ID the
  broker needs to report a buffered-delivery failure back to the hub, so the
  broker's failure is logged on the broker only. This is a general scion
  gap, not specific to this runtime; tracked as a follow-up.
- **New agents are unaffected.** Any agent this broker process itself
  starts (i.e. anything created after the restart) has a fresh in-memory
  record and control token, and its delete/stop/exec/message all work
  normally.
- **A same-project stop or delete, with no restart at all, is not affected
  either.** Stop is Delete in Phase 1: it drops this process's own
  in-memory record immediately but the actor can stay listed, in
  `ACTOR_STATE_DELETING`, for a while afterward (Delete is fire-and-forget —
  see `.design/kubernetes/substrate-runtime.md` §9). Such an actor is never
  counted as record-less: its egress policy is already gone (Delete deletes
  that first), so there is nothing left to protect, and counting it would
  turn an ordinary stop-then-delete sequence into a false "broker
  restarted" 409.

**Operator cleanup for a record-less actor.** The 409 response body carries
only the atespace and a count, never an actor name or a control token —
identify the actor from the broker's own log or your own records first, not
from the response body:

```sh
# 0. Identify the actor. The 409 response body only proves at least one
#    record-less actor exists in this atespace; it does not name it, and it
#    is never a license to act on every name the broker's log turns up. The
#    broker logs the record-less actor NAMES at WARN on every such 409 (only
#    in its own log, never in the HTTP response). Because a rolling update
#    or `strategy: Recreate` can list more than one broker pod at once, find
#    the SPECIFIC pod that logged the line you care about, not an arbitrary
#    one (`kubectl logs deploy/...` picks one pod for you, which may not be
#    the one that logged this WARN):
kubectl -n "${BROKER_NAMESPACE}" logs --prefix --tail=-1 -l app=scion-substrate-broker \
  | grep -i "agent identity unknown"
#    Each matching line is prefixed "[pod/<pod-name>/scion-substrate-broker]"
#    -- note that pod name (call it POD below) and read the
#    "recordless_actors" field on that same line.
#
#    The actor THIS delete/stop is for is named "<project-slug>--<agent-slug>"
#    (containerName, pkg/agent/run.go) -- act ONLY on that one name, and only
#    if it appears in POD's WARN line and passes both checks below. The WARN
#    line lists every record-less actor in the atespace, which routinely
#    includes OTHER pre-restart agents in the same project that are still
#    running (and, per the atespace-prefix note below, possibly another
#    project's actors sharing the prefix). Those other names are NOT part of
#    this procedure and must not be deleted here: each is cleaned up through
#    its own agent's delete/stop, when that request hits this same 409.
#
#    For the one name that matches <project-slug>--<agent-slug>:
#      (i)  confirm its .metadata.createTime predates the broker CONTAINER's
#           current start in POD -- not POD's .status.startTime, which is
#           when the POD started and does NOT move when the kubelet restarts
#           a crashed or OOM-killed container in place
#           (`restartPolicy: Always`, the Deployment default). An in-pod
#           container restart is exactly the kind of "broker restart" this
#           whole mechanism exists to catch (see "an OOM kill, anything that
#           starts a new process" above, and consequence (d) below), so the
#           pod's start time is the wrong boundary: use the container's own
#           state.running.startedAt instead:
kubectl ate get actor -a <atespace> <actor> -o yaml   # read .metadata.createTime
kubectl -n "${BROKER_NAMESPACE}" get pod "$POD" \
  -o jsonpath='{.status.containerStatuses[?(@.name=="scion-substrate-broker")].state.running.startedAt}'
#           An actor created after that timestamp is not a pre-restart
#           actor, whatever this 409 says -- do not act on it.
#      (ii) cross-check it against the hub's own agent list for this
#           project (its phase/name should match what you expect).
#    If more than one project could plausibly share this atespace (see the
#    atespace-prefix note below), confirm ownership with that project's
#    owner first. Only the one name verified this way -- read from POD's own
#    WARN log line, confirmed pre-restart against POD's own container start,
#    and matched against the hub's agent list -- may be acted on below;
#    never guess from the requested slug alone, and never act on the other
#    names the same WARN line lists.

# 1. Only once the actor is confirmed: delete its egress policy FIRST. This
#    order is mandatory, not a suggestion — deleting the actor before its
#    egress policy leaves the policy behind with nothing left to delete it
#    (RecordlessActors only ever reports actors, so an orphaned policy alone
#    is invisible to this whole mechanism). There is no dedicated CLI for
#    this; call the ateapi Control service's DeleteActorEgressPolicy RPC
#    directly. Keep TLS verification ON: never grpcurl -insecure/-plaintext
#    against a real cluster endpoint.
#
#    Per deployment, fill in: <atespace> and <actor> (from step 0); the
#    CA bundle passed to -cacert, which is the CA that signs the
#    api.${ATE_SYSTEM_NAMESPACE}.svc certificate (cluster-internal, not in a
#    public trust store; with this ConfigMap it is the ClusterTrustBundle
#    named by substrate.cluster_trust_bundle, or the file named by
#    substrate.ca_file if you use that instead); and a host to run grpcurl
#    from that can resolve and reach api.${ATE_SYSTEM_NAMESPACE}.svc:443
#    directly (inside the cluster network) -- it is a headless,
#    cluster-internal service name, so it does not resolve from a
#    workstation (see the workstation form after the in-cluster form below).
#
#    The token is held in an exported environment variable, never written to
#    a file and never put on grpcurl's command line: with -expand-headers,
#    grpcurl itself replaces ${ATE_TOKEN} in the header with the value of
#    that environment variable, and the single quotes stop the shell from
#    expanding it first, so argv only ever contains the literal text
#    '${ATE_TOKEN}'. The export and both calls run in a subshell, so
#    ATE_TOKEN is gone as soon as it exits -- whether or not the calls
#    inside succeed -- with no separate `unset` needed.
kubectl get clustertrustbundle "${CLUSTER_TRUST_BUNDLE_NAME}" \
  -o jsonpath='{.spec.trustBundle}' > ate-ca.pem
(
  export ATE_TOKEN="$(kubectl create token scion-substrate-broker -n "${BROKER_NAMESPACE}" \
    --audience api.${ATE_SYSTEM_NAMESPACE}.svc --duration 600s)"
  grpcurl -cacert ate-ca.pem -expand-headers -H 'Authorization: Bearer ${ATE_TOKEN}' \
    -d '{"actor":{"atespace":"<atespace>","name":"<actor>"}}' \
    api.${ATE_SYSTEM_NAMESPACE}.svc:443 ateapi.Control/DeleteActorEgressPolicy

  #  Verify the policy is actually gone before touching the actor — an
  #  orphaned policy after step 2 is invisible to this whole mechanism, so
  #  this check matters, not just as a courtesy:
  grpcurl -cacert ate-ca.pem -expand-headers -H 'Authorization: Bearer ${ATE_TOKEN}' \
    -d '{"actor":{"atespace":"<atespace>","name":"<actor>"}}' \
    api.${ATE_SYSTEM_NAMESPACE}.svc:443 ateapi.Control/GetActorEgressPolicy
)   # ATE_TOKEN is gone as soon as the subshell exits, whatever happened inside
#    Expect a NotFound gRPC status. Anything else means the policy is still
#    there; do not proceed to step 2 until it reads NotFound.
#
#    From a workstation: the CA bundle is already fetched above by the same
#    command; port-forward instead of reaching the cluster-internal service
#    name directly, and keep TLS verification anchored to the in-cluster
#    name via -authority, which also sets the TLS server name used for
#    verification (the help text for grpcurl's own -authority flag, and its
#    use via grpc.WithAuthority -- cmd/grpcurl/grpcurl.go in
#    fullstorydev/grpcurl). Run the port-forward and both calls in their own
#    subshell, so ATE_TOKEN stays scoped to it just like the in-cluster form
#    above; guard the port-forward with a trap right after capturing its
#    PID, so it is stopped whether the subshell exits normally, is
#    interrupted, or is terminated. A separate `INT TERM` trap that exits is
#    required alongside the `EXIT` one: a trap only runs a handler and
#    resumes the script at the next command, so an `EXIT INT TERM` trap
#    whose handler is just the cleanup would kill the port-forward on
#    Ctrl-C and then keep going into `export ATE_TOKEN=...` and the grpcurl
#    calls below instead of stopping the block — `exit 130` (128+SIGINT) on
#    `INT TERM` makes the interrupt actually stop the block, and that exit
#    is what then fires the `EXIT` trap's cleanup. Wait for the
#    port-forward to actually be listening before the first grpcurl call:
#      (
#        kubectl -n "${ATE_SYSTEM_NAMESPACE}" port-forward svc/api 9555:443 &
#        pf=$!
#        trap 'kill "$pf" 2>/dev/null' EXIT
#        trap 'exit 130' INT TERM
#        sleep 2   # give the port-forward time to start listening
#        export ATE_TOKEN="$(kubectl create token scion-substrate-broker -n "${BROKER_NAMESPACE}" \
#          --audience api.${ATE_SYSTEM_NAMESPACE}.svc --duration 600s)"
#        grpcurl -cacert ate-ca.pem -authority api.${ATE_SYSTEM_NAMESPACE}.svc \
#          -expand-headers -H 'Authorization: Bearer ${ATE_TOKEN}' \
#          -d '{"actor":{"atespace":"<atespace>","name":"<actor>"}}' \
#          localhost:9555 ateapi.Control/DeleteActorEgressPolicy
#        # (same substitution for the GetActorEgressPolicy verification
#        # call above, run inside this same subshell)
#      )
#    Once verification reads NotFound, proceed to step 2.

# 2. Delete the actor itself, any_state so a non-RUNNING actor isn't
#    rejected:
kubectl ate delete actor -a <atespace> <actor> --any-state

# 3. Force-delete the hub's own agent record so it doesn't keep dispatching
#    to an actor that no longer exists. `scion delete --force` does not
#    exist (checked, cmd/delete.go has no --force flag) — call the hub API
#    directly, with the access token from `~/.scion/credentials.json` (mode
#    0600; `pkg/credentials/store.go`) — the same one `scion` itself uses to
#    authenticate; do not create or expect any separate "~/.scion/token"
#    file — nothing in this codebase creates one. Nothing that expands a
#    secret may appear on the command line (shell history, `ps`,
#    `/proc/<pid>/cmdline`) or be left behind if this is interrupted: the
#    whole read/write/curl sequence runs in a subshell, so its EXIT trap
#    fires — removing the header file — as soon as curl returns, not only
#    when the surrounding interactive shell eventually exits. A separate
#    `INT TERM` trap that exits is required alongside the `EXIT` one, the
#    same reasoning as the port-forward subshell above: without it, an
#    interrupt would remove the header file and then fall through to the
#    curl call instead of stopping the block.
(
  read -rs TOKEN          # paste the hub token; not echoed, not in history
  HDR=$(mktemp); trap 'rm -f "$HDR"' EXIT; trap 'exit 130' INT TERM
  printf 'Authorization: Bearer %s\n' "$TOKEN" > "$HDR"; chmod 600 "$HDR"
  unset TOKEN
  curl --fail-with-body -X DELETE \
    "https://<hub-endpoint>/api/v1/agents/<agent-id>?force=true" -H @"$HDR"
)
```

`--fail-with-body` makes a 401/403/404 exit non-zero (with the body still
shown) instead of silently exiting 0, so a rejected request cannot be
mistaken for a completed step.

`force=true` does not skip the broker: the hub (`pkg/hub/handlers_agents_core.go`)
still dispatches the delete to the broker exactly as a normal delete would —
it only changes what happens when that dispatch fails. Without `force`, a
broker error (including this same 409) fails the hub request and leaves the
hub record alone; with `force`, the hub logs the broker error and deletes its
own agent record anyway, regardless of what the broker did or didn't manage
to clean up. Run it only after steps 0–2 succeed: run it early and the actor,
egress policy and worker are orphaned with no further signal (the broker
error that would have surfaced this is now just a log line); run it while
other record-less actors remain in this atespace and the broker still
returns 409, so the project's on-broker files for this agent may also be
left behind.

**Fail-closed consequences worth knowing about, not fixed here:**
- (a) `substrateAtespaceName` truncates a project ID to its first 12
  (sanitized) characters. This collision is not just theoretical: project
  IDs are client-supplied at hub project creation (`pkg/hub/handlers_projects_core.go`),
  so anyone able to create a project who knows another project's ID prefix
  can deliberately create one that maps to the same atespace. What that
  allows is a count disclosure (how many record-less actors project X's
  atespace holds) and the ability to force 409s on project X's genuinely-
  absent-slug deletes/stops — never a wrong-project action: the probe only
  ever turns a would-be success into an error, it never selects a delete
  target across atespace boundaries.
- (b) Once at least one record-less actor exists in a project's atespace,
  *every* delete or stop of an absent slug in that project returns 409
  instead of the usual idempotent 404/202, until every record-less actor in
  that atespace has been cleaned up (above).
- (c) A narrow window between `CreateActor` succeeding and this process
  recording it (spanning `waitRunning`, `healthz`, and bootstrap — tens of
  seconds) makes a new actor look record-less to any concurrent delete/stop
  of an unrelated absent slug in the same project, or of the in-flight agent
  itself: both get a spurious 409 rather than a wrong action, since the
  probe never selects a target. Not hardened further in Phase 1 (see
  `.design/kubernetes/substrate-runtime.md` §9).
- (d) A pre-restart actor already in `ACTOR_STATE_DELETING` when the broker
  restarts — for example, one this same broker began deleting (`Stop` is
  `Delete` in Phase 1) just before whatever crash triggered the restart, and
  whose deletion never finished — is excluded from the record-less-actor
  count above (see "with no restart at all", above, for why: its egress
  policy is already gone, so counting it would just turn an ordinary
  stop-then-delete race into a false 409). The consequence: a delete of its
  slug returns the ordinary idempotent 404, and the hub drops its agent
  record, while the actor itself may still be sitting in the cluster,
  stuck in `DELETING`. This is one of the two exceptions to the invariant
  stated above. A process-local record of which actors this broker process itself
  asked to delete could not distinguish this case either: why the deletion
  never finished is below scion, in ateapi or the cluster it manages, not
  anything this process could have tracked about its own requests.

  Locating and clearing one (it can't be located by attempting a delete/stop
  and reading the error — a `DELETING` actor never produces the 409 above):

  ```sh
  # 1. List the atespace's actors and note every one whose STATE column
  #    reads ACTOR_STATE_DELETING. ListActorsRequest has no state filter, so
  #    this is a visual/scripted filter over the full listing, not a --state
  #    flag:
  kubectl ate get actors -a <atespace>

  # 2. For each DELETING candidate NAME, compare its creation time against
  #    every currently running broker pod's CONTAINER start -- not the pod's
  #    .status.startTime, which does not move on an in-pod crash or OOM
  #    restart (see step 0(i) above for why that distinction matters here).
  #    List every matching pod rather than guessing .items[0] (a rolling
  #    update or `strategy: Recreate` can list more than one at once):
  kubectl ate get actor -a <atespace> <NAME> -o yaml   # read .metadata.createTime
  kubectl -n "${BROKER_NAMESPACE}" get pods -l app=scion-substrate-broker \
    -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[?(@.name=="scion-substrate-broker")].state.running.startedAt}{"\n"}{end}'
  #    Only a NAME that predates every one of those timestamps is a
  #    candidate -- a DELETING actor created after any of them may still be
  #    that pod's own, ordinary, in-flight delete.

  # 3. Only for a NAME confirmed in step 2: delete its egress policy exactly
  #    as in operator cleanup step 1 above (tolerates NotFound if it is
  #    already gone), then:
  kubectl ate delete actor -a <atespace> <NAME> --any-state
  ```

All four are accepted trade-offs of failing closed rather than risking a
false success; see `.design/kubernetes/substrate-runtime.md` §10 for the
durable fix Phase 2 tracks.

**The record-less-actor probe only reaches the substrate profiles this
broker process has already resolved a manager for.** Immediately after a
restart, that is just the default substrate profile — a second substrate
profile pointed at a *different* ateapi endpoint is not probed until this
process resolves it at least once (its first `Run`), so a pre-restart actor
under that second profile's atespace gets the ordinary idempotent 404/202
until then. This deployment only has one substrate profile, so it doesn't
apply here.

**`profiles.local`/`profiles.remote` are repointed at the substrate runtime
in this ConfigMap on purpose** (`broker.yaml`) — not an oversight, and not
free of side effects. The embedded default settings always define these two
profiles against the docker and Kubernetes runtimes; a project or global
settings layer only overwrites the exact keys it sets, so it can't remove
them. Left alone, this broker would discover both as auxiliary runtimes on
every request and their `List` calls would fail here (no docker binary, no
pod-list RBAC), independently masking the exact "not found" vs "can't tell"
ambiguity this section's fix addresses. Repointing both at this broker's own
`substrate-prod` runtime removes that layer of noise regardless of this fix
— **but it also means a dispatch that names profile `local` or `remote`
explicitly now creates a substrate agent on this broker instead of failing**
(the embedded default `active_profile` is `local`, so this is also what an
unqualified start against this broker resolves to). If your deployment ever
names either profile intentionally expecting docker/Kubernetes semantics,
treat this repoint as a one-line thing to revisit; nothing here special-cases
that case.

## Files

- `broker.yaml` — Namespace, ServiceAccount, RBAC (TokenRequest-on-self,
  ClusterTrustBundle read), ConfigMap
  (substrate runtime profile), Deployment, and the router NetworkPolicy
  (`atenet-router-restrict-ingress`, restricting router ingress to the
  broker namespace). The worker-side NetworkPolicy is Substrate's own —
  see "Known Phase 1 limitations" and the comment in `broker.yaml` after
  the router policy.
- This README.

# Substrate Phase 1 — in-cluster broker manifest + router NetworkPolicy

Second task (sb-dev-2), following on from
`2026-09-23-substrate-phase1-substrate-serve.md`. Implements
`phase1-spec.md` §2.4: a plain-manifest (not Helm) deployment of the scion
runtime broker inside the Substrate cluster, connected to the
`scion-integration` hub, plus the router `NetworkPolicy` the §5 fallback
bootstrap-nonce decision requires.

## Decision inputs (from sb-em)

- Nonce: Phase 1 uses the §5 fallback (first-bootstrap-wins).
  `FirstBootstrapWinsVerifier` (already the default from the first task)
  stays; no wiring change needed here. This manifest's job is the
  NetworkPolicy that fallback depends on for its safety, plus documenting it
  as a Phase 2 hardening item.
- Settings shape and RBAC needs relayed from sb-dev (who owns
  `pkg/config`/`pkg/runtime`): the exact `V1SubstrateConfig` field names, the
  `profiles.substrate.runtime` indirection, and the three RBAC
  requirements (TokenRequest-on-self, ClusterTrustBundle read,
  worker-namespace pod/log read). Cross-checked directly against
  `pkg/config/settings_v1.go` and `pkg/runtime/substrate/dialer.go` on the
  branch (both already pushed by sb-dev) — the relayed shape and RBAC list
  match the code exactly.
- Cluster specifics (`infra/cluster.md`) had not landed yet at write time —
  everything cluster-specific is a placeholder, listed below.

## Research: what the substrate router actually needs

`atenet-router`'s upstream install isn't vendored into this repo, so I
fetched it directly from `github.com/agent-substrate/substrate`
(`manifests/ate-install/atenet-router.yaml`, `ate-system-namespace.yaml`,
`atenet-router-monitoring.yaml`, default branch, 2026-09-23) rather than
guess at the NetworkPolicy's shape:

- Router pod label is `app: atenet-router` in namespace `ate-system`
  (parameterized as `ATE_SYSTEM_NAMESPACE`, defaulting to that).
- Its Service exposes: `80→8080` (http), `443→8443` (https), `8081`
  (CONNECT tunneling), `8444` (CONNECT+TLS), `4040` (status). These four
  data-plane ports (8080/8443/8081/8444) are what the NetworkPolicy allows
  from the broker namespace.
- Readiness/liveness probes hit port **9090** on the router container
  itself — kubelet-originated, not namespace-scoped traffic in the way
  NetworkPolicy reasons about; most CNIs (including GKE Dataplane V2) exempt
  node-to-pod probes, but I could not verify this holds on the eventual
  target cluster from here, so it's called out explicitly in the README
  rather than silently assumed.
- Metrics scraping (`atenet-router-monitoring.yaml`) is a
  `monitoring.googleapis.com/v1 PodMonitoring` (Google Managed Prometheus)
  scraping the **Envoy sidecar's** admin port 9901 directly by pod IP — not
  through the Service, and not a port my NetworkPolicy touches at all. GMP's
  collector traffic path is cluster-config-dependent; flagged as a
  known-unaccounted-for gap rather than guessed at.
- Confirmed **ateapi and atelet are never inbound clients of the router** in
  the upstream manifests — the router calls out to ateapi (egress from the
  router's perspective), so nothing needs to be added to an *ingress* policy
  on the router's behalf for either of them.
- Confirmed the upstream install ships **no pre-existing NetworkPolicy** for
  `atenet-router` at all (grepped the full `manifests/ate-install/` tree) —
  this manifest's policy is additive, not a replacement/narrowing of
  something already there.

This is the concrete basis for the "don't break the router's own traffic"
requirement in the brief, rather than a policy written from the spec's
description of the router alone.

## Changes

### `deploy/substrate/broker.yaml` (new)
Single plain manifest (11 objects, one file, `---`-separated), matching the
`deploy/monitoring/` precedent in this repo (plain YAML + README, not a
chart) rather than `deploy/helm/`:
- `Namespace` for the broker.
- `ServiceAccount` (`scion-substrate-broker`) + a `Role`/`RoleBinding`
  granting `create` on `serviceaccounts/token`, **resourceNames-pinned to
  itself** — the TokenRequest RBAC `pkg/runtime/substrate/dialer.go` needs
  to mint its own ateapi-audience token, scoped so it can't mint tokens for
  any other identity.
- `ClusterRole`/`ClusterRoleBinding` granting `get` on
  `certificates.k8s.io/clustertrustbundles` (cluster-scoped resource, so
  this can't be a namespaced Role) — needed only when
  `substrate.cluster_trust_bundle` is set, harmless read-only access
  otherwise.
- `Role`/`RoleBinding` in the **worker** namespace granting `get` on `pods`
  and `pods/log`, bound to the broker's ServiceAccount by namespaced
  subject reference — for `GetLogs` (phase1-spec.md §2.2).
- `ConfigMap` holding `settings.yaml` with the `runtimes.substrate-prod` /
  `profiles.substrate` shape sb-dev specified, `api_endpoint`/
  `router_endpoint`/`token_audience` derived from `ATE_SYSTEM_NAMESPACE` so
  they can't drift from the namespace placeholder, everything else
  (`cluster_trust_bundle`, `sandbox_config_name`, `snapshot_storage`) a
  named placeholder.
- `Deployment` (1 replica): `args` select broker-only mode
  (`server start --foreground --hosted --enable-runtime-broker
  --runtime-broker-port=9800`, mirroring `cmd/broker.go`'s
  `runBrokerStart`'s foreground branch exactly — **not**
  `--enable-hub`, since this broker connects outward to the existing
  `scion-integration` hub rather than running one). `readinessProbe`/
  `livenessProbe` hit `GET /healthz` on 9800
  (`cmd/hub.go`'s `checkLocalBrokerServer`, same endpoint
  `scion runtime-broker status` polls locally). Mounts the settings
  ConfigMap and the (out-of-band) credentials Secret via `subPath` onto
  individual files under `$HOME/.scion/`, `HOME` pinned to `/home/scion` via
  env (`config.GetGlobalDir()` is always `$HOME/.scion` — confirmed in
  `pkg/config/paths.go`, no path env override exists).
- No literal secret values anywhere in the file — a commented-out
  (non-live) `Secret` block documents the expected shape; the real Secret is
  created out-of-band, documented in the README.
- Router `NetworkPolicy` in `ATE_SYSTEM_NAMESPACE`: `podSelector: {app:
  atenet-router}`, ingress allowed only from `BROKER_NAMESPACE` on the four
  client-facing ports.

### `deploy/substrate/README.md` (new)
Placeholder table (10 placeholders — see below), how to build the broker
image (reuses `Dockerfile.hub`'s existing `scion` binary build, no new
Dockerfile needed — the broker is the same binary, different flags), Secret
creation via `scion runtime-broker register` (traced through `cmd/broker.go`
to confirm exactly what file and fields it produces), apply order,
validation commands, and — per substrate-lead's requirement relayed by
sb-em — **verification commands that prove the NetworkPolicy actually
blocks an out-of-namespace caller and allows an in-namespace one**, not just
that `kubectl apply` succeeded. Also documents:
- why `server.broker.broker_id` (not `broker_token`) is the field that
  belongs in the ConfigMap (see "Notable finding" below);
- that NetworkPolicy enforcement itself requires GKE Dataplane V2 (or
  another enforcing CNI) — a cluster without it accepts the object and
  enforces nothing, silently;
- the unconfirmed worker-namespace placeholder;
- the GMP-metrics gap noted above;
- that this is intentionally a minimal Phase 1 fixture, not the Phase 2
  chart.

## Notable finding: settings.yaml's broker identity lives at `server.broker.*`, not top-level `hub.*`

My first draft put the broker's identity at a top-level `hub: {broker_id,
endpoint}` block, extrapolating from the relayed settings shape (which only
covered `runtimes`/`profiles`). Checking `pkg/config/settings_v1.go`
directly before finalizing turned up the real answer: `V1ServerConfig.Broker
*V1BrokerConfig` (`json:"broker"`) holds `broker_id`, `hub_endpoint`, and
`broker_token`, i.e. the path is `server.broker.broker_id` /
`server.broker.hub_endpoint`. This is confirmed as the *current*
(non-deprecated) location by settings_v1.go's own legacy-key migration,
which maps the old flat `hub.brokerId` to `server.broker.broker_id` with the
comment "hub.brokerId is deprecated; moved to server.broker.broker_id".

This also surfaced something worth flagging on its own:
`V1BrokerConfig.BrokerToken` (`broker_token`) is a **secret** field in the
same struct — the legacy single-shared-secret broker auth mode. It would
have been an easy mistake to fold the whole `V1BrokerConfig` block into the
ConfigMap by analogy with `broker_id`. Fixed by including only `broker_id`
and `hub_endpoint` (both non-secret) and documenting explicitly in
`broker.yaml`'s comment and the README why `broker_token` is deliberately
absent — this deployment authenticates via the newer, per-connection
`hub-credentials/<name>.json` Secret instead (see "Secret creation"), so
there's no reason for the older field to appear here at all.

## Validation

No cluster is reachable from this environment (`infra/cluster.md` hasn't
landed), so `kubectl apply --dry-run=client` can't run at all here — it
needs live API-server discovery even in client-only mode, confirmed by
testing (`dial tcp [::1]:8080: connect: connection refused` even with
`--validate=false`). Used **kubeconform** instead, per the brief's own
"or kubeconform if available" allowance:

```
go install github.com/yannh/kubeconform/cmd/kubeconform@latest
kubeconform -strict -summary -kubernetes-version 1.31.0 <rendered-manifest>
# Summary: 11 resources found in 1 file - Valid: 11, Invalid: 0, Errors: 0, Skipped: 0
```

Run against both the raw `${VAR}`-templated file (confirms it's syntactically
valid YAML and that the placeholder tokens don't break document structure)
and a version with all placeholders resolved to representative dummy values
(confirms every object's shape against the OpenAPI schema for Kubernetes
1.31). Both passed with 0 invalid/errors. `kubectl apply --dry-run=client`/
`--dry-run=server` are documented in the README as the next step once
cluster access exists.

## Out of scope (per brief / phase1-spec.md §3)

The Helm chart (Phase 2), template GC, `scion doctor` integration for
Substrate, in-cluster credential rotation, resolving the
`SUBSTRATE_WORKER_NAMESPACE` and other cluster-specific placeholders (blocked
on `infra/cluster.md`), and actually applying/verifying this on a live
cluster (substrate-lead / uat-lead, per phase1-spec.md §7 acceptance
criteria 10–11).

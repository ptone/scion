# Substrate Runtime (Phase 1)

This is the in-repo design reference for the `substrate` runtime backend, as
built. It documents what the code in `pkg/runtime` (the `SubstrateRuntime`),
`pkg/runtime/substrate` (the ateapi dialer and router client),
`pkg/sciontool/substrate` and `cmd/sciontool/commands` (the in-actor
`sciontool substrate-serve` control server), `pkg/substratecaps`,
`pkg/substrateenv`, `pkg/config` (`V1SubstrateConfig`), and
`deploy/substrate/` actually rely on. Section numbers below are stable
citation anchors (`substrate-runtime.md §N`); when a section is split further,
the subsection number (`§N.M`) is also stable. Extend this document by adding
sections, not by renumbering existing ones.

Deployment-manifest specifics (placeholders, RBAC, apply order, verification
commands) live in `deploy/substrate/README.md`, not here; this document is
cited from there for the parts that are actually code behavior rather than
operational procedure.

## 1. Architecture and topology

Substrate is a Kubernetes-hosted actor runtime: **actors** (OCI containers in
gVisor or microVM sandboxes) run on a shared pool of **worker** pods, at most
one actor per worker at a time, and can be suspended to object storage and
resumed onto any free worker. scion maps one agent onto one actor.

```
 hub ──control channel──> scion runtime broker (in-cluster pod)
                              │ gRPC (ServiceAccount token)   │ HTTP, ate-target-actor header
                              ▼                                ▼
                     api.ate-system (ateapi Control)    atenet-router ──> actor: sciontool substrate-serve :80
                                                                            └─ tmux ─ harness (claude, ...)
 actor egress (HTTP(S) only, per-actor EgressPolicy) ──> hub, git host, model API, telemetry
```

- **The broker is the existing scion runtime broker binary**, with the
  `substrate` runtime compiled in, deployed as a Kubernetes Deployment inside
  the Substrate cluster (`deploy/substrate/`). It authenticates to ateapi
  with an in-cluster ServiceAccount token obtained via the Kubernetes
  TokenRequest API (audience `api.ate-system.svc` by default), not a
  projected volume (`pkg/runtime/substrate/dialer.go`).
- **The router is Substrate's own `atenet-router`.** Inbound requests carry
  the header `ate-target-actor: <atespace>/<actor>`
  (`pkg/runtime/substrate/router.go`'s `TargetActorHeader`) to address a
  specific actor. An inbound request to a suspended actor auto-resumes it.
- **The only new code that runs inside the actor is `sciontool substrate-serve`**
  (§5), shipped in the agent image as the template's entrypoint.
- **Why an in-cluster broker, not a remote one:** Substrate has no
  authorization on its control API (`ateapi`) or its inbound router — any
  caller that can reach them controls the whole install. Reaching them from
  outside the cluster through a LoadBalancer/Ingress would expose both
  unauthenticated surfaces beyond the cluster boundary, so the broker (and
  therefore the only holder of ateapi credentials) must run in-cluster. This
  is also why the Phase 1 bootstrap-nonce fallback (§5.2) additionally
  depends on a NetworkPolicy restricting router ingress to the broker
  namespace (`deploy/substrate/README.md`), rather than relying on
  authorization Substrate itself doesn't provide.
- **ateapi client:** `github.com/agent-substrate/substrate/pkg/proto/ateapipb`
  (vendored under `third_party/ateapipb/`, with its license and source SHA
  recorded there, to avoid pulling the upstream module's full transitive
  dependency set into the build).
- **Egress** from the actor is HTTP(S)-only and default-deny without an
  EgressPolicy — see §7. There is no WebSocket egress, so the hub
  port-forward tunnel and `sciontool` autoexpose are both disabled when
  `SCION_RUNTIME=substrate` (`cmd/sciontool/commands/init.go`): starting them
  would just spin retrying against a gateway that always refuses the
  upgrade. This is a Phase 1 limitation (§11), not a permanent one.

## 2. Settings and profile keys

`V1SubstrateConfig` (`pkg/config/settings_v1.go`, under
`V1RuntimeConfig.Substrate`) holds every Substrate-specific setting:

| Key | Meaning |
|---|---|
| `api_endpoint` | ateapi Control gRPC endpoint, e.g. `api.ate-system.svc:443`. |
| `router_endpoint` | atenet-router inbound endpoint, e.g. `http://atenet-router.ate-system.svc:80`. |
| `token_audience` | Audience for the in-cluster ServiceAccount TokenRequest. Defaults to `api.ate-system.svc`. |
| `ca_file` / `cluster_trust_bundle` | Verify the ateapi/router server certificate against a PEM file or a `ClusterTrustBundle`; when both are set, `cluster_trust_bundle` wins. |
| `sandbox_class` | `gvisor` (default) or `microvm`. |
| `sandbox_config_name` | The Substrate `SandboxConfig` CRD instance actor templates reference. |
| `worker_selector` | Copied into the ActorTemplate's `workerSelector`, matched against a `WorkerPool`'s own `metadata.labels` (not a Pod label — see `deploy/substrate/README.md`). |
| `snapshot_storage` | Bucket/prefix for the ActorTemplate's `snapshotsConfig` storage, e.g. `gs://bucket/prefix/`. |
| `egress_allow` | Additional hostnames beyond the hub/git/model/telemetry hosts the runtime always adds (§7). Public FQDNs only — see `ValidateEgressAllow`. |
| `egress_trust_bundle` | Names a Substrate trust bundle to project into the actor for the sdsmint MITM gateway (§7.1). Only `egress-mitm.ate.dev` is accepted. |
| `template_ready_timeout` | Bounds how long `Run` waits for a newly created ActorTemplate to become ready. Go duration string; defaults to 10 minutes. |

**Selection is explicit only.** `pkg/runtime/factory.go`'s `substrate` case
is reachable only when a profile names it; there is no auto-detect branch
(unlike `docker`/`k8s`, which Linux auto-detection can select). A settings
profile is free to name its `substrate` runtime entry anything (e.g.
`substrate-prod`, `substrate-nip`); `SubstrateRuntime.Name()` always reports
the literal `"substrate"` regardless, because that is the value the broker's
substrate-only code paths (e.g. the delete gate in
`pkg/runtimebroker/handlers.go`) key off.

`NewSubstrateRuntime` memoizes one `*SubstrateRuntime` (and its dialed gRPC
`ClientConn`) per distinct `V1SubstrateConfig` for the life of the broker
process — see the "process-wide memoization" comment on
`substrateRuntimesMu` (`pkg/runtime/substrate_runtime.go`). A CA rotation
behind the same `ca_file`/`cluster_trust_bundle` value does not take effect
until the broker process restarts.

## 3. ActorTemplate mapping

Templates are **generic and content-addressed**, never per-agent: per-agent
configuration (env, secrets, home files) is pushed after the actor starts,
via bootstrap (§5), not baked into the template. This keeps secrets out of
Substrate's Postgres and out of golden snapshots, and lets every agent on the
same image/resources/sandbox share one template.

- **Image must be digest-pinned** (`@sha256:...`). `Run` fails closed
  otherwise (`pkg/runtime/substrate_runtime.go:317`):

  > `substrate: image %q is not pinned by digest (@sha256:...); set a
  > digest image in the agent's template or pass --image (tag resolution
  > is a Phase 2 feature)`

  Tag→digest resolution is a Phase 2 feature. A substrate profile's own
  `harness_overrides` image pin is **not** sufficient by itself on a
  default install, because agent/template config
  (`finalScionCfg.Image`, `pkg/agent/run.go`'s image resolution) outranks
  it once the harness-config's own image has been copied into the agent's
  persisted config at provisioning time (`pkg/agent/provision.go`). See
  `deploy/substrate/README.md` for the operator-facing setup step this
  requires.
- **Template name** = `scion-` + the first 12 hex characters of
  `sha256(image digest, sandbox class, sandbox config name, worker_selector,
  snapshot storage, effective resources, snapshot scope, the container's
  added capabilities, substrate-serve entrypoint version)`, plus
  `egress_trust_bundle` appended only when it is non-empty. Same inputs
  always produce the same name, so concurrent `Run`s for the same effective
  template converge on one `CreateActorTemplate` instead of racing to create
  distinct ones. The entrypoint version is folded in so a change to the
  `substrate-serve` wire protocol forces a new template rather than reusing
  golden state built against an older one; resources are resolved against
  `config.BuiltinDefaultResources` before hashing (a nil `*ResourceSpec`
  would otherwise hash as empty and silently reuse a stale golden template
  if the default ever changes). `egress_trust_bundle` is appended as its own
  segment, not a fixed field, precisely so that leaving it empty keeps
  existing golden templates on a plain install byte-identical and reused;
  setting it changes the hash, since `buildActorTemplate`'s output genuinely
  differs (a new `system-info` volume and Env — see §7.1).
- **Contents:** one container running the pinned image, command
  `["sciontool", "substrate-serve"]`, `Env` carrying no secrets and no
  per-agent config ever (only the fixed CA-bundle paths when
  `egress_trust_bundle` is set — see §7.1); cpu/memory limits from
  `cfg.Resources` or runtime defaults; one `durableDir` volume at
  `/workspace`; `snapshotsConfig` (`onPause`/`onCommit` = `DATA`, storage
  from `snapshot_storage`); `sandboxConfig` (the configured sandbox class and
  `SandboxConfig` name); `workerSelector` from config.
- **Creating a template boots a golden actor and snapshots it.**
  `ensureActorTemplate` calls `GetActorTemplate`, and `CreateActorTemplate`
  if missing, then polls `GetActorTemplate` until the golden snapshot is
  ready (bounded by `template_ready_timeout`, default 10 minutes) before
  `Run` proceeds to `CreateActor`. A cold template build can take on the
  order of a minute; see `deploy/substrate/README.md` ("warm the template
  before first use") for why this matters for the hub's dispatch timeout.

## 4. Runtime lifecycle

`SubstrateRuntime` implements `runtime.Runtime`:

| Method | Phase 1 behaviour |
|---|---|
| `Name()` | Always `"substrate"` (§2). |
| `Run(cfg)` | Steps below. |
| `Delete(id)` | See §9. |
| `Stop(id)` | Same as `Delete`, with a `TODO(Phase 2)` marker: `SuspendActor(DATA)` plus the `$HOME` durableDir layout would keep the workspace and free the worker instead, but that lands with Phase 2's suspend/resume work (§11). Faking a "stopped" actor that is still running, or a "durable stop" that discarded the workspace, would both misreport what happened, so Phase 1 keeps `Stop` honest by making it `Delete`. |
| `List(labelFilter)` | `ListActors(atespace)`. Substrate actors carry no labels of their own, so `AgentInfo` labels are synthesised from the broker's in-memory record of what it passed to `Run`, keyed by actor UID. A broker restart loses this record for actors it did not create in the current process lifetime (Phase 2 adds a ConfigMap-backed store); see "known limitations" (§10). Label filtering is applied after synthesis. When a `"scion.name"` filter has no project-scoping key alongside it and more than one record-having actor shares that slug, `List` excludes all of them rather than returning an arbitrary one (the same-slug cross-project hardening — see §10). |
| `GetLogs(id)` | `GetActor` → `status.worker_assignment` → the worker pod's name/namespace → client-go `PodLogs` (tail 2000 lines). |
| `Exec(id, argv)` | `POST /scion/v1/exec` via the router, authorized with the `control_token` minted at bootstrap (§5.3), header `ate-target-actor: <atespace>/<actor>` (§1). Returns stdout; a non-zero exit becomes an error that includes stderr. |
| `Attach`, `Sync`, `GetWorkspacePath` | Return errors naming Phase 2 / the hub workspace API. Substrate has no exec/attach/TTY primitive in Phase 1, so the broker's PTY switch (`pty_handlers.go`) returns a clean "attach not yet supported on substrate" error instead of falling through to docker exec, which would fail confusingly. |
| `ImageExists` / `PullImage` / `ImageID` / `RemoveImage` | `true` / no-op / return the digest / no-op — Substrate pulls the image itself from the template. |
| `ExecUser()` | `"scion"`. |

**`Run(cfg)` steps:**

1. Compute `atespace = "scion-" + first 12 chars of sha256(cfg.ProjectID)`
   (sanitised to a valid Kubernetes short name) and `actor = cfg.Name`
   (already DNS-safe). `CreateAtespace` if missing, treating `AlreadyExists`
   as success.
2. Verify `cfg.Image` is digest-pinned (§3); fail closed otherwise.
3. Ensure the content-addressed ActorTemplate exists and its golden snapshot
   is ready (§3).
4. `CreateActor(atespace/name, template)`. An existing actor with the same
   name surfaces as `agent.ErrContainerNameInUse`.
5. `CreateActorEgressPolicy` (§7).
6. `ResumeActor`, then poll `GetActor` until `RUNNING` (bounded).
7. Poll `GET /scion/v1/healthz` via the router until `awaiting-bootstrap`
   (bounded backoff).
8. `POST /scion/v1/bootstrap` (§5.3): env = `cfg.Env` + `ResolvedAuth.EnvVars`
   + env-type `ResolvedSecrets`; files = the composed home + `ResolvedAuth.Files`
   + file-type secrets (§5.4); `start_cmd` built by the same shared helper the
   docker/k8s runtimes use. Generate a random 32-byte hex `control_token` and
   keep it in memory, keyed by `<atespace>/<actor>`.
9. Return `<atespace>/<actor>` as the runtime ID (the same string
   `splitSubstrateID` parses back into atespace/actor for every later call).

**Cleanup on failure:** if any step after `CreateActor` fails, `Run` deletes
the actor and the egress policy (best effort) before returning the error.

**Error hygiene:** see §6.

## 5. Bootstrap protocol and the in-actor control server

`sciontool substrate-serve` (`cmd/sciontool/commands/substrate_serve.go`,
package `pkg/sciontool/substrate/`) is the ActorTemplate's entrypoint. It
listens on `:80`, the port the router targets by default, and is compiled
into the same `sciontool` binary as every other subcommand.

### 5.1 Endpoints

- `GET /scion/v1/healthz` — no auth. Returns `{"state": "awaiting-bootstrap"}`
  or `{"state": "running"}` only, never data.
- `POST /scion/v1/bootstrap` — accepted once per process lifetime, only in
  `awaiting-bootstrap`. Auth: the nonce (§5.2). Effect: write the files
  (§5.4), set the env, and run the **existing** `sciontool init` path
  in-process (the same `InitRunner` seam the cmd layer already has, reused
  rather than forked). The server keeps serving afterwards. Responses:
  `409` if already bootstrapped, `401` on a bad nonce, `422` for a rejected
  file path (§5.5), `413` over the size cap (§5.4).
- `POST /scion/v1/exec` — auth: `Bearer <control_token>` from bootstrap.
  Body `{"argv":[...], "user":"scion"|"root", "timeout_s":N}`, response
  `{"stdout":"...","stderr":"...","exit_code":N}`. Output is capped at 4 MiB
  per stream (truncated, with a flag on the response). Runs as the requested
  user via the same su/exec-user semantics other runtimes use.

Out of scope for Phase 1: `/pty`, `/rehydrate`, `/tunnel/open`.

### 5.2 Nonce

`bootstrapNonce` is the single call site for the bootstrap request's bearer
value, so the broker only ever has one place to change how it is derived.
Two options exist:

- **Identity-derived** (not used in Phase 1): a bootstrap nonce minted via
  `MintActorJWT` for the actor's own identity, verifiable only by the broker
  and control plane. `StaticNonceVerifier` compares a presented bearer
  against a fixed expected value in constant time, and stands in for this
  once the identity-derived path is wired up.
- **Fallback, used in Phase 1: `FirstBootstrapWinsVerifier`.** The control
  server accepts any bearer token, including an empty one, but the
  single-shot "first bootstrap wins" check (§5.1, `409` on a second attempt)
  is the actual guard. This is acceptable only together with a NetworkPolicy
  restricting router ingress to the broker namespace
  (`deploy/substrate/README.md`) — the race window is otherwise any in-cluster
  caller able to reach the router before the broker's own bootstrap call.
  **A `409` from `/bootstrap` is treated as a compromise:** the broker
  deletes the actor and fails `Run` with a clear error.

Threat model for `egress_allow` validation (§7) ties back to this: Substrate's
egress default-deny plus the actor's own EgressPolicy is what keeps an actor
off the router and other in-cluster services in the first place; an
`egress_allow` entry that reached either would defeat the fallback nonce's
NetworkPolicy assumption, which is why `V1SubstrateConfig.Validate` rejects
CIDRs, catch-alls and internal-shaped hostnames outright.

### 5.3 Bootstrap payload

```jsonc
{
  "env": {"K": "V", ...},
  "files": [{"path": "/home/scion/.claude/...", "mode": 384, "content_b64": "..."}],
  "start_cmd": "tmux new-session ... ",
  "control_token": "<random 32B hex>"
}
```

- `env` = `cfg.Env` + `ResolvedAuth.EnvVars` + env-type `ResolvedSecrets`,
  plus `SCION_RUNTIME=substrate` (which is what lets `sciontool` disable
  autoexpose and the port-forward tunnel — §1, §7).
- `mode` is decimal (384 decimal == 0600 octal) — the only mode used for
  every file, since neither `api.FileMapping` nor `api.ResolvedSecret`
  carries one.
- `files` — see §5.4 for composition and precedence, §5.5 for path safety.
- `start_cmd` — the same tmux command string the k8s runtime's `tmuxCmd`
  builds, through one shared helper (not duplicated a third time).
- `control_token` — generated fresh per bootstrap, used both as the `/exec`
  bearer (§4) and, in the Phase 1 fallback, has no bearing on the nonce
  itself (the nonce is checked before this payload is ever parsed).

`postBootstrap` sends this payload through the router with the nonce as the
bearer. Any non-2xx response is an error; `409` specifically becomes
`errBootstrapHijacked` (§5.2), and `422` becomes a `*bootstrapPathRejectedError`
carrying the same stable code and path the server returned (§5.5).

### 5.4 Home delivery

`buildBootstrapFiles` (`pkg/runtime/substrate_bootstrap.go`) assembles
`files` from three sources, **in precedence order, deduped by final path
with the later source winning**, so exactly one entry per path reaches the
wire:

1. **The broker-composed agent home** (`RunConfig.HomeDir` — harness-config
   `home/`, e.g. `.claude.json`/`.claude/settings.json`, the template home,
   and skills). `homeBootstrapFiles(cfg.HomeDir, containerHome)` walks it
   with `filepath.WalkDir`, **never following a symlink**; regular files
   only (fifos, sockets and devices are skipped and counted, never read);
   mode = the source file's permission bits `& 0o777`. `HomeDir == ""`
   leaves the output unchanged (golden-tested against the pre-existing
   auth+secret-only behavior); a `HomeDir` that doesn't exist is an error.
2. `ResolvedAuth.Files` (read from `SourcePath`).
3. File-type `ResolvedSecrets`.

**This delivery model is deliberately narrower than Docker/Podman or
cloudrun-sandbox, which bind-mount or relocate `HomeDir` directly:**

- **Copy-in, one-way, at bootstrap.** Files are read once and written into
  the actor's filesystem. Nothing the actor writes afterwards is synced back
  to the broker's `HomeDir` — there is no persistent mount or ongoing sync.
- **Additive over the image's home, not a replacement for it.** The actor
  image's own home directory is not cleared or shadowed first; bootstrap
  only adds or overwrites the specific paths it ships.

**Size cap, measured, not assumed.** The router's ingress listener for
`POST /scion/v1/bootstrap` has no request-body cap of its own (no buffer
filter or `max_request_bytes`; the 1 MiB `per_connection_buffer_limit_bytes`
default is a flow-control watermark, not a cap — the body streams through
uninspected, under a 300s route timeout). `sciontool substrate-serve`'s own
handler is therefore the binding limit: `maxBootstrapBodyBytes` = 64 MiB
(`pkg/sciontool/substrate/server.go`), enforced via `http.MaxBytesReader`
(fails the read closed — a bounded error, not unbounded buffering — reported
as `413`). `maxBootstrapFilesTotalBytes` = 16 MiB decoded, across every file
combined after dedup (`pkg/runtime/substrate_bootstrap.go`), is sized with
headroom under that: 16 MiB decoded is ~21.3 MiB once base64-encoded (the
4/3 expansion), leaving roughly 3x headroom under the 64 MiB serve-side
limit even before the surrounding JSON envelope (`env`, `start_cmd`,
`control_token`, per-file path/mode/quoting) is added. The error on overflow
names only the cap and the total size, never a path or content.

**Hygiene:** home file contents (`settings.json` can carry env) are
secret-grade — never logged or put in an error — but ride the same
plaintext broker→router hop auth/secret files already ride (§6, §10); the
exposure is not narrower for home files than for the other two sources.

### 5.5 Path safety: the symlink guard

`writeBootstrapFile` (serve side, `pkg/sciontool/substrate/`) accepts
arbitrary absolute paths, creates parent directories, and chowns what it
created for the scion user.

- `mkdirAllTracked` Lstats **every existing component** of
  `filepath.Clean(dir)`, top-down from the first component to `dir` itself —
  not just the deepest one that happens to exist. (Stopping at the first
  existing ancestor is not sufficient: it never Lstats anything above that
  point, so a symlinked component whose target already contains the
  remaining subpath would pass unnoticed.) The first symlink found anywhere
  in that walk rejects the whole file, naming only its own path. The first
  missing component starts a suffix created top-down with plain `os.Mkdir`
  (never `os.MkdirAll`, which would reopen the same hole).
- This is a plain existence check followed by a separate create, not one
  atomic operation — safe only because nothing writes concurrently during
  bootstrap: `substrate-serve` is the sole writer, and the harness has not
  started yet. It is **not** a general TOCTOU-safe guarantee.
- The leaf file itself is handled separately: `writeFileAtomicMode`'s
  `os.Rename(tmp, path)` replaces whatever directory entry sits at `path`,
  including a pre-existing symlink, rather than following it — a symlink at
  the leaf is safe by construction.
- `writeBootstrapFile` cleans the bootstrap file's `Path` once
  (`filepath.Clean`) and uses that same cleaned value for both the component
  walk and the final write target, so a `..` segment can never resolve
  differently between the two.

**The guard applies to every bootstrap `Path`, not just home files** —
including auth/secret targets that legitimately live outside home. A target
under a *system* symlink (e.g. `/var/run -> /run` on Debian-based images, or
a merged-`/usr` layout) is rejected exactly like a symlinked path under
home. **Workaround:** use the resolved form of the target, e.g.
`/run/secrets/...` instead of `/var/run/secrets/...`. No real target is
known to need an exemption (§11).

**Two stable error codes**, carried by a `*bootstrapPathError`
(`pkg/sciontool/substrate/helpers.go`) and returned as `422`:

- `bootstrap_path_symlink` — a path component is a symlink.
- `bootstrap_path_invalid` — an empty/relative path, or a path component
  that exists but is not a directory.

Both carry only the rejected file's own `path`, never an internal ancestor
or content. `postBootstrap` (broker side) parses a `422` body into a
`*bootstrapPathRejectedError` with the same code and path, and `Run` logs
both explicitly before returning the redacted error — paths are
configuration and safe to log; file content stays secret-grade and is never
part of this error on either side. No new wire fields exist: the body is
still the pre-existing unstructured string `/bootstrap` has always returned
for every rejection, just with a stable, parseable
`"<code>: bootstrap file <quoted path> rejected: <detail>"` shape for this
one class of error.

### 5.6 SIGTERM and eviction

**SIGTERM must not kill the harness.** `substrate-serve` is PID 1 in the
actor. In Phase 1 it logs SIGTERM and keeps running — it does not forward
the signal to the harness and does not exit. This matters because Substrate
propagates SIGTERM into the actor's workload when a worker is drained, with
a 30-minute grace period before SIGKILL; forwarding it would kill the agent
on every eviction instead of surviving it. Full eviction handling —
suspending the actor within that grace period rather than just surviving
the signal — is Phase 2 (§11).

## 6. Error hygiene

No secrets — env values, file contents, or auth/secret material — appear in
ActorTemplates, logs, or error strings, on either side of the bootstrap
hop:

- The ActorTemplate's container `Env` never carries secrets or per-agent
  config (§3); only fixed CA-bundle paths when `egress_trust_bundle` is set.
- Bootstrap file content is never logged or put in an error, on either the
  broker or the serve side (§5.4). Rejected-path errors (§5.5) name only the
  path, never content. The size-cap error (§5.4) names only the cap and the
  total size.
- `redactErr` (`pkg/sciontool/substrate/helpers.go`) is the named seam for
  this invariant on the serve side; any new error path added to the server
  is reviewed against it.
- **Known residual exposure:** the broker→router hop carries the bootstrap
  payload, including home/auth/secret file contents, in plaintext in the
  Phase 1 fixture (`router_endpoint: http://...`). This is out of Phase 1
  scope (§10) and covered by the NetworkPolicy plus first-bootstrap-wins
  nonce (§5.2), not by transport encryption.

## 7. Egress model

`Run` creates a per-actor `EgressPolicy` (step 5, §4) with hostname rules
for:

- the hub endpoint host;
- the git clone host;
- the harness model API hosts, hardcoded for Phase 1: `api.anthropic.com`,
  plus Google auth and Vertex (`oauth2.googleapis.com`, `*.googleapis.com`);
  `*.googleapis.com` also happens to cover the Cloud Trace telemetry default,
  but the actual configured telemetry endpoint is always added as its own
  rule too — coincidence of the default is not a rule;
- the configured telemetry endpoint host;
- `egress_allow` entries from settings (§2).

`egress_allow` is **allowlist-first and validated** (`ValidateEgressAllow`,
`pkg/config/substrate_egress.go`): only public FQDNs (optionally wildcarded
as `*.example.com`) are accepted. Rejected outright: any IP address or CIDR;
catch-alls (`all`, `*`, `0.0.0.0/0`, `::/0`); a hostname whose top-level
domain isn't a real, ICANN-delegated one (also catching
Kubernetes-internal-shaped names and reserved zones like `.local`/`.internal`);
a hostname that is itself a public suffix rather than a name beneath one;
hostnames ending in `.svc`, `.cluster.local`, `.internal`, `.local`, or
`.localhost`. This does not close DNS rebinding or a service like
nip.io/sslip.io resolving a valid public hostname to a private address —
only a post-resolution check by the egress proxy itself could close that,
and Phase 1 does not add one.

### 7.1 `egress_trust_bundle`: the sdsmint MITM gateway

Substrate's plain `atenet-egress` enforces `egress_allow` only for TLS
*passthrough* on address rules — every rule `egress_allow` emits is a
HostnameRule, enforced for HTTPS only under the **sdsmint** gateway, which
terminates every TLS connection and re-originates it with a per-SNI leaf
certificate chained to its own CA. An actor validating only public roots
rejects that certificate and every HTTPS request fails.

`egress_trust_bundle: egress-mitm.ate.dev` (the only name the vendored
Substrate version resolves) is required if and only if the cluster runs
sdsmint and the actor makes any HTTPS/TLS request. When set,
`buildActorTemplate` adds a `system-info` volume projecting the gateway's CA
to `/run/ate/trust-bundle.pem`, and sets `NODE_EXTRA_CA_CERTS`,
`GIT_SSL_CAINFO`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE` (all pointing at that
file) and `SSL_CERT_DIR=/run/ate` — the shared variable names live in
`pkg/substrateenv.TrustBundleVarNames`, so `buildActorTemplate` and the
serve-side `su -w` exec path (§4, `Exec`) never drift apart. `SSL_CERT_DIR`
is exclusive for Go and Python `ssl`, and only additive for curl, git and
Node (see `deploy/substrate/README.md` for the full breakdown). Leaving
`egress_trust_bundle` empty is byte-identical to today and keeps the
template's content-address unchanged (§3); setting it on a plain
(non-sdsmint) install breaks every actor at start, since nothing backs the
named `ClusterTrustBundle`.

When set, the image needs util-linux ≥ 2.35 (`su -w`); this is only
exercised when a CA-bundle var is actually set, so plain installs are
unaffected either way.

## 8. Privilege drop and rootfs

Substrate starts the actor process as **UID 0 with a minimal capability
set** (`AUDIT_WRITE`, `KILL`, `NET_BIND_SERVICE`). `sciontool init`'s
privilege-drop and rootfs assumptions were written against Docker's
defaults, which don't hold here:

- **`/` is `0700` in the actor** (Substrate `bundle_linux.go`).
  `fixupRootfsForScion` (`cmd/sciontool/commands/substrate_rootfs.go`)
  chmods `/` to `0755`. It runs at `substrate-serve` startup (captured in
  the golden snapshot) and again at `/bootstrap`, and also re-chowns
  root-owned entries under the resolved `scion` user's home
  (`chownTreeRootOwned`) — image-layer files can appear owned by UID 0.
- **`checkPrivilegeDropFeasible`** (`cmd/sciontool/commands/init.go`) is
  `/bootstrap`'s synchronous precondition: it verifies every capability in
  `pkg/substratecaps.Required` is actually effective before `/bootstrap`
  ever responds `200`, so a missing capability fails fast and visibly
  instead of deep inside `RunInit` with a silently truncated log (see the
  next point).
- **Capabilities added beyond Substrate's default set**
  (`pkg/substratecaps.Required`, applied on the ActorTemplate's container
  `SecurityContext` by `buildActorTemplate`, and independently checked by
  `checkPrivilegeDropFeasible` — the two must never drift apart, which is
  why both consumers import the single dependency-free `substratecaps`
  package):

  | Capability | Reason |
  |---|---|
  | `SETUID` | `su` (exec-user switching) and the supervisor's own credential drop both need it to leave root. |
  | `SETGID` | Same two consumers, for `setgroups`/`setgid`. |
  | `CHOWN` | `RunInit` unconditionally chowns the log file and the workspace/home tree from root to the scion user once a drop is expected. |
  | `DAC_OVERRIDE` | `RunInit`'s root phase writes into the scion-owned `0700` home (created by the rootfs fixup) and appends to the log after it has already been chowned to the scion user — without this, root is subject to the same permission check as any other non-owning UID, and (a second, independent effect) log writes after the chown fail silently, dropping every later init log line including the one that would have explained the failure. |

  `su` unconditionally drops all of these for the scion process tree once
  the switch happens.
- **Non-fatal, observed warnings:** gVisor `--allow-suid` is disabled;
  `RLIMIT_MEMLOCK` gives `EPERM`.

## 9. Delete semantics

`Delete(id)`: `DeleteActorEgressPolicy` (ignoring `NotFound`), then
`DeleteActor(any_state=true)`, then drop the in-memory `control_token` for
`<atespace>/<actor>`. The ActorTemplate itself is never deleted by `Delete`
— template GC is Phase 2 (§11).

**Delete is fire-and-forget in Phase 1.** The broker counts `DeleteActor`
being accepted (not erroring) as success; it does not confirm the actor
actually leaves `DELETING`. A stuck actor is invisible to the hub and
silently holds a worker (§10). Confirming deletion (poll until the actor
leaves `DELETING`, or report a stuck state) is a Phase 2 item (§11).

**Same-slug, cross-project safety.** Because `List` (§4) can only recover a
best-effort project *name* for a record-less actor (not a trustworthy ID),
`Delete`'s call sites that filter `Runtime.List` by `"scion.name"` alone,
with no project-scoping key, must fail closed rather than guess when more
than one record-having actor shares that slug across different projects
(§4's `List` row). This closes the file-system analogue of the same
ambiguity for local-file deletion fallbacks (`findAgentInHubManagedProjects`)
that already existed for the actor-delete path.

## 10. Known limitations (Phase 1)

- **A create that outlives the hub's dispatch timeout leaks a running,
  unbootstrapped actor** (holding a worker indefinitely). The hub's
  control-channel `RequestTimeout` is hard-coded at 120s
  (`pkg/hub/server.go`); a cold template build (§3) can exceed it. The
  broker's own dispatch context is never cancelled by a hub-side timeout
  (generic, not substrate-specific — `pkg/runtimebroker/controlchannel.go`).
  **Operator mitigation:** warm the template before first use — see
  `deploy/substrate/README.md`.
- **The broker→router hop is plaintext HTTP**, and bootstrap credentials
  (including the composed home, §5.4) cross it. Mitigated in Phase 1 only by
  the NetworkPolicy and first-bootstrap-wins nonce (§5.2), not by transport
  encryption. See `deploy/substrate/README.md` for the fix options.
- **PID-1 exit is not observed by Substrate.** A post-bootstrap init failure
  leaves a `RUNNING` actor holding a worker; `healthz` reports
  `init-failed`, but the hub only shows an error if the direct status report
  succeeds.
- Same-slug cross-project handling is fail-closed, not resolved (§9).
- Stop-then-delete can leak local broker-side files.
- The broker's in-memory `substrateAgentRecords`/`substrateControlTokens`
  are lost on a broker restart for actors it did not create in the current
  process lifetime (§4's `List` row); a Phase 2 ConfigMap-backed store
  replaces this.
- A forced delete can still leak an actor that doesn't confirm `DELETING`
  (§9).
- Non-home image paths remain root-owned after the rootfs fixup (§8), which
  only re-chowns under the resolved home directory.
- **A default install's `substrate` create fails closed with the
  `not pinned by digest` error (§3) against the shipped `claude`
  harness-config's `scion-claude:latest` image, and a substrate profile's
  own `harness_overrides` image pin alone does not fix it.** The
  harness-config's image is copied into the agent's persisted config at
  provisioning (`pkg/agent/provision.go`), and at start that agent/template
  config image outranks the profile override (`pkg/agent/run.go`'s image
  resolution) — see §3 and `deploy/substrate/README.md` for the required
  setup step (a digest-pinned template `image:` or `--image`).
- **The egress hostname-rule model requires the sdsmint MITM gateway**
  (§7.1); the plain gateway enforces only address rules for TLS.
- **The dialer's trust material is pinned for the broker process's
  lifetime** (§2), not re-read per agent start.
- **The every-prefix symlink guard (§5.5) is not a general TOCTOU
  guarantee** — it depends on `substrate-serve` being the sole writer during
  bootstrap.
- Build-bootstrap-files-before-create is not implemented: `buildBootstrapFiles`
  runs after the actor is already `RUNNING` (§4 step 8), so a deterministic
  configuration error (e.g. a missing `HomeDir`, §5.4) costs a full actor
  spin-up and teardown rather than failing before `CreateActor`.
- No exemption exists for a bootstrap target that must traverse a system
  symlink (§5.5); the documented workaround is the resolved path form.

## 11. Phase 2+ recommendations

- Make the default-install image gap (previous section) harder to hit by
  default — e.g. a runtime-aware harness-config seed, or resolving tags to
  digests generically — rather than requiring every substrate install to
  set a digest-pinned template `image:` by hand.
- Identity-derived bootstrap nonce (`MintActorJWT`), replacing the Phase 1
  fallback (§5.2).
- Seal the bootstrap payload end-to-end, or move the router endpoint to its
  TLS listener with a `ClusterTrustBundle` CA, closing the broker→router
  plaintext gap (§6, §10).
- `Suspender` capability (`Suspend`/`Resume`) using Substrate's FULL
  snapshot scope, plus the `$HOME` durableDir layout needed for `Stop`
  to keep the workspace instead of deleting the actor (§4's `Stop` row).
- Credential rehydration after a FULL resume (the hub revokes agent
  credentials on suspend).
- Eviction handling beyond "don't forward SIGTERM" (§5.6): watch for
  `WORKER_STATE_DRAINING`, and have the broker suspend proactively.
- An on-demand hub port-forward tunnel and `sciontool` autoexpose, so
  Substrate agents regain that path once it no longer depends on
  always-on WebSocket egress (§1, §7) — a scion-wide refactor, not
  substrate-specific, that this runtime would opt into once it lands.
- Template GC, and a durable (ConfigMap-backed) label store surviving
  broker restarts (§4's `List` row, §10).
- Confirm actor deletion (poll until `DELETING` clears, or report a stuck
  state) instead of the current fire-and-forget `Delete` (§9).
- Doctor integration for the substrate profile (API reachability, auth,
  atespace create permission, WorkerPool capacity, snapshot bucket, router
  reachability).
- Re-read the dialer's trust material per handshake instead of pinning it
  for the broker process's lifetime (§2).
- Build bootstrap files before `CreateActor`, so a deterministic
  configuration error doesn't cost a full actor spin-up (§10).
- A concrete exemption mechanism for a bootstrap target that must traverse
  a system symlink, if a real case ever needs one (§5.5) — none is known
  today, and an ownership heuristic can't reliably separate a benign
  symlink from a hostile one.
- The sdsmint trust-bundle projection (§7.1) as a supported, tested
  configuration on more than one cluster.

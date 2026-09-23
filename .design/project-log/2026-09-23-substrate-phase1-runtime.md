# Substrate Phase 1 — `substrate` runtime, ateapi client, dialer, settings/factory

Implemented my half (sb-dev) of the substrate-integration Phase 1 brief:
`phase1-spec.md` §2.2 (the `substrate` runtime backend), §2.3 (settings/factory),
the shared tmux start-cmd helper extraction, and the runtime-side `§6` unit tests.
`pkg/sciontool/substrate/` and `cmd/sciontool/commands/substrate_serve.go` (spec
§2.1) are sb-dev-2's half of the same branch; I did not touch `cmd/sciontool/` or
`pkg/sciontool/`. `deploy/substrate/broker.yaml` (spec §2.4) was reassigned to
sb-dev-2 by sb-em mid-task; I did not write it.

## Changes

### Shared start-cmd helper (`pkg/runtime/common.go`)
Extracted `harnessCmdLine`, `tmuxAgentWindowCmd`, and `buildTmuxStartCmd` (with a
`tmuxSessionEnd` parameter: `tmuxAttachSession` vs `tmuxPollSession`) from four
call sites that had each built the tmux "scion" session command independently:
`common.go` (docker/podman), `k8s_runtime.go`, `cloudrun_sandbox_runtime.go`
(`buildEntrypoint`), and `cloudrun_runtime.go` (which also collapsed its
duplicate NoAuth/Harness branches into one, since `harnessCmdLine` already
handles both). The only genuine variation — `attach-session` (Docker/Podman/K8s,
which give PID 1 a TTY) vs a poll loop (Cloud Run/-sandbox, which don't) — is
kept explicit via the `tmuxSessionEnd` parameter rather than normalised away.
Verified byte-identical output: every existing parity/tmux test
(`k8s_runtime_tmux_test.go`, `k8s_parity_test.go`, the cloudrun-sandbox
`buildEntrypoint` tests, `common_test.go`) passes unchanged. The substrate
runtime's `buildSubstrateStartCmd` is the fifth caller, using `tmuxPollSession`
(the control server execs it the same no-TTY way Cloud Run/-sandbox's PID 1
does).

### `third_party/ateapipb/` (new — vendored, not `go get`)
`go get github.com/agent-substrate/substrate/pkg/proto/ateapipb@d277088` (the
brief's provisional SHA — `infra/cluster.md` did not exist yet when I started;
sb-em will relay the final one) pulls substrate's full `go.mod`, which requires
**Go 1.27.0** (scion targets 1.26.1) and forces major-version upgrades of
`k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go`, and roughly a dozen
other shared deps scion already pins. Confirmed by actually running `go get`
and inspecting the diff, then reverting (`git checkout -- go.mod go.sum`) —
this is exactly the "unreasonable transitive dependencies" case phase1-spec.md
§2.2 anticipates. The generated package (`ateapi.pb.go`, `ateapi_grpc.pb.go`,
`ateapi.proto`, `source.go`) only depends on `google.golang.org/protobuf` and
`google.golang.org/grpc`, both already present in scion's `go.mod` at
compatible versions — the vendored files were generated with
`protoc-gen-go v1.36.11-devel`, matching scion's `google.golang.org/protobuf
v1.36.11` exactly. Copied verbatim under `third_party/ateapipb/` with the
source repo's Apache 2.0 `LICENSE` and the SHA recorded in `README.md`.

### `pkg/runtime/substrate/` (new — dialer + router client)
Minimal re-implementation of substrate's `internal/ateclient/builder.go`
(`internal/`, so scion cannot import it), scoped to what the in-cluster broker
needs:
- `dialer.go`/`grpc.go`: `Dial(ctx, k8sClient, DialerConfig)` opens a gRPC
  connection to ateapi Control, authenticated via `grpc.PerRPCCredentials`
  backed by a `tokenSource` that mints (and caches, refreshing 60s before
  expiry) a bearer token for the **calling pod's own ServiceAccount** via the
  `TokenRequest` API (`CoreV1().ServiceAccounts(ns).CreateToken`). TLS is
  verified against a `ClusterTrustBundle` (preferred) or a `CAFile`;
  `InsecureSkipVerify` is never set — a config with neither source configured
  is a hard error, not a fallback to unverified TLS.
- `identity.go`: `currentServiceAccountIdentity` determines the broker's own
  namespace (from the standard projected file) and ServiceAccount name (from
  the `sub` claim of its own mounted token — `system:serviceaccount:<ns>:<name>`
  — decoded but not signature-verified, since it only selects which SA to
  request a token *for*; the TokenRequest API call itself is what's actually
  authorized). Cross-checks the token's namespace claim against the mounted
  namespace file and errors on mismatch rather than silently trusting one
  source.
- `router.go`: `RouterClient.Do` sends HTTP requests through `atenet-router`
  with the `ate-target-actor: <atespace>/<actor>` header.

### `pkg/config/settings_v1.go` + `schemas/settings-v1.schema.json`
Added `V1SubstrateConfig` (`api_endpoint`, `router_endpoint`, `token_audience`,
`ca_file`/`cluster_trust_bundle`, `sandbox_class`, `sandbox_config_name`,
`worker_selector`, `snapshot_storage`, `egress_allow`,
`template_ready_timeout`) under `V1RuntimeConfig.Substrate`, and `"substrate"`
to the schema's runtime-type enum. **No sub-object schema** for it: the schema
doesn't model `cloudrun_instances`/`cloudrun_sandbox` sub-objects either (its
`runtimeConfig` `$def` only has the base fields, `additionalProperties: false`)
— that's a pre-existing gap, not something introduced here, and adding one only
for substrate would be new asymmetric behaviour rather than following the
existing pattern. Confirmed no test exercises schema validation against those
existing sub-objects either, so this isn't a regression.

### `pkg/runtime/factory.go`
Added the `"substrate"` case (calls `NewSubstrateRuntime(rtConfig.Substrate)`,
wraps a construction error in `ErrorRuntime`) and `"substrate"` to the
direct-profile-name fallback list. **No auto-detect branch** — substrate is
never reached from the `"local"`/`"auto"` OS-detection path, only via an
explicit `type: substrate` profile, per phase1-spec.md §2.3.

### `pkg/runtime/substrate_runtime.go` + `substrate_template.go` +
    `substrate_egress.go` + `substrate_bootstrap.go` (new)
`SubstrateRuntime` implements `runtime.Runtime`:
- **`Run`** — the 9 steps in phase1-spec.md §2.2, in order: `CreateAtespace`
  (AlreadyExists tolerated) → digest-pin check on `cfg.Image` → content-
  addressed `ActorTemplate` (`GetActorTemplate`, `CreateActorTemplate` if
  `NotFound`, poll until `GoldenSnapshotStatus.golden_tag` is set or the
  configured `template_ready_timeout` elapses) → `CreateActor`
  (`AlreadyExists` → an error matching `agent.isContainerNameInUseError`'s
  string-match pattern, since `pkg/runtime` cannot import `pkg/agent` — see
  Finding 1) → `CreateActorEgressPolicy` (one rule, all hostnames as
  patterns) → `ResumeActor` + poll `GetActor` until `RUNNING` → poll
  `GET /scion/v1/healthz` through the router until `awaiting-bootstrap` →
  `POST /scion/v1/bootstrap`. Any failure from `CreateActor` onward calls a
  best-effort cleanup (`DeleteActorEgressPolicy` + `DeleteActor(any_state=true)`
  on a fresh 30s context) before returning.
- **`Delete`**/**`Stop`** — `DeleteActorEgressPolicy` (NotFound ignored) +
  `DeleteActor(any_state=true)`; `Stop` is byte-identical to `Delete` in Phase
  1, with a `TODO(Phase 2)` for `SuspendActor(DATA)` — faking a "stopped"
  phase that actually deleted (or actually left running) the actor would be
  dishonest either way, so Phase 1 is explicit about deleting instead.
- **`List`** — `ListActors()` (empty atespace = all atespaces) with
  labels/template/project/image synthesised from an in-memory
  `map[uid]*substrateAgentRecord` populated at `Run` (Substrate actors carry
  no labels at all — confirmed from the proto, `Actor` has no such field).
  Label filtering falls back to the synthesised `project`/`projectID` when a
  filtered key has no literal label, matching the existing
  `CloudRunSandboxRuntime.List` pattern.
- **`GetLogs`** — `GetActor` → `status.worker_assignment.worker_pod`/
  `.worker_namespace` (see Finding 2 below — no `GetWorker` call) → client-go
  `PodLogs`, tail 2000 lines.
- **`Exec`** — `POST /scion/v1/exec` via the router, authorized with the
  `control_token` cached at bootstrap (keyed by `<atespace>/<actor>`, per
  spec). Non-zero exit code becomes an error carrying stderr and the code.
- **`Attach`/`Sync`/`GetWorkspacePath`** — explicit "not supported" errors
  naming Phase 2 / the hub workspace API. `pty_handlers.go`'s PTY switch
  (both `LocalPTYSession.Run` and `StreamPTYHandler.Run`) gets an explicit
  `runtimeCmd == "substrate"` branch returning a clean
  `"attach not yet supported on substrate"` error, instead of falling through
  to `startDockerExec` (which would otherwise run and fail confusingly).
- **`ImageExists`/`PullImage`/`ImageID`/`RemoveImage`** — `true` / no-op /
  digest substring after `@` / no-op, per spec (Substrate pulls the image
  itself).
- **Error hygiene** — every `Run` error path that could carry request/response
  content is passed through `redact` (`SubstrateRuntime.redact`), which reuses
  the existing #1342 redaction helpers (`externalEnvValues`/`redactEnvValues`
  in `argv_redact.go`) against the assembled bootstrap env, so no secret value
  can reach a returned error string.
- **Bootstrap nonce (§5)** — `bootstrapNonce` is the single function the brief
  asked for. It currently returns a broker-generated random value (the
  documented §5 fallback). See Finding 3 and "Decisions" below —
  **sb-em confirmed Phase 1 uses the fallback.**

## Findings

1. **`CreateActor` `AlreadyExists` can't return `agent.ErrContainerNameInUse`
   directly.** `pkg/agent` imports `pkg/runtime`, so the reverse import would
   cycle. `pkg/agent/run.go`'s `classifyLaunchRuntimeError` instead
   pattern-matches any runtime's `Run` error text
   (`isContainerNameInUseError`: contains both "container name" and "already
   in use", or "is already in use by container") and remaps it to
   `agent.ErrContainerNameInUse` generically. `substrate_runtime.go` returns
   `"substrate: container name %q already in use in atespace %q"`, which
   matches that pattern, so the existing broker handling applies without any
   `pkg/agent`/`pkg/runtime` coupling change.
2. **API-shape difference from findings.md/phase1-spec.md (validation
   question 8): `GetLogs` does not call `GetWorker`.** Both docs describe the
   path as "`GetActor` → `status.worker` → `GetWorker` → pod name/namespace".
   The proto shows `ActorStatus.worker_assignment` (`WorkerAssignment`)
   already carries `worker_pod` and `worker_namespace` directly — the proto's
   own comment calls this out as deliberate: "worker_* fields below are a
   denormalized copy kept so that readers on a hot path do not have to fetch
   the Worker at all." `GetLogs` reads those fields straight off the `GetActor`
   response; no `GetWorker` call is made or needed.
3. **Bootstrap nonce: could not confirm the `MintActorJWT` + `systemInfo`
   option is workable from `ateapi.proto` alone.** `SystemInfoVolumeSource` →
   `SystemInfoDataSource` offers `ActorMetadataDataSource` (projects the
   actor's own name/atespace/uid as plain files — no cryptographic proof) or
   `TrustBundleDataSource` (projects a *named* trust bundle, where "supported
   names are allowlisted in atelet" per the proto comment — opaque outside
   that implementation). Verifying a `MintActorJWT`-issued JWT inside the
   actor would need the *issuer's* signing trust bundle to be one of those
   allowlisted names, which isn't discoverable from the proto or from outside
   a running cluster. Reported this to sb-em rather than guessing; **sb-em
   decided Phase 1 uses the documented fallback** (see Decisions). Bonus: this
   turned out to already match what sb-dev-2 built independently on the
   server side (`FirstBootstrapWinsVerifier` defaults to accepting any bearer,
   including empty) — the two sides agree without having coordinated on it
   directly, because both followed the same spec section.
4. **Bootstrap env formula, as literally stated in phase1-spec.md §2.2 step 8
   ("`cfg.Env` + `ResolvedAuth.EnvVars` + env-type `ResolvedSecrets`"), omits
   `cfg.Harness.GetEnv()`/`GetTelemetryEnv()`.** Every other runtime
   (`buildCommonRunArgs`, `KubernetesRuntime.buildPod`,
   `CloudRunSandboxRuntime`) includes harness env in the container/pod env —
   without it, the harness has no model/task config to start with. Implemented
   it with harness env included and flagged the deviation to sb-em rather than
   silently narrowing scope; **sb-em approved keeping it.**

## Decisions (sb-em, this conversation)

- **ateapipb vendoring under `third_party/`**: approved. sb-em will relay the
  final pinned SHA once `infra/cluster.md` lands (currently `d277088`,
  swap-in-place).
- **Bootstrap nonce**: Phase 1 uses the §5 fallback
  (`substrateBootstrapNonce` stays a broker-generated random value, sent
  immediately after `RUNNING`). Kept as the single function per the brief so
  swapping to `MintActorJWT` later is a one-function change.
- **Harness env in the bootstrap payload**: approved, kept, documented as a
  spec deviation in code (see Finding 4).
- **`deploy/substrate/broker.yaml` (spec §2.4)**: reassigned by sb-em to
  sb-dev-2 mid-task (parallel work); not written by sb-dev. Landed as
  `deploy/substrate/broker.yaml` + `deploy/substrate/README.md` in
  `a336d46f2`, using exactly the settings/profile shape I gave sb-em
  (`runtimes.substrate-prod.type: substrate`, the `V1SubstrateConfig` field
  names above, `profiles.substrate.runtime: substrate-prod`).

## Known limitations (Phase 1, by design)

- `Stop` deletes the actor rather than suspending it (no DATA-scope suspend
  yet — Phase 2, findings.md §4.6 / Q5 `$HOME` layout).
- `List`'s label/project record is in-memory only; a broker restart loses it
  for actors that broker didn't create in its current process lifetime
  (explicitly accepted in phase1-spec.md §2.2's List row; Phase 2 adds a
  ConfigMap-backed store).
- `Exec`'s `control_token` cache has the same broker-restart limitation.
- Egress hostname collection (`substrateEgressHostnames`) extracts the hub and
  git hosts from `cfg.Env`/`cfg.GitClone` and hardcodes
  `api.anthropic.com`, `oauth2.googleapis.com`, `*.googleapis.com` (which also
  covers `cloudtrace.googleapis.com`, findings.md §2's telemetry default) plus
  `egress_allow`. This is a best-effort implementation for Phase 1; validation
  questions 1–2 (which hosts are actually needed) can only be answered
  empirically on the real cluster, per phase1-spec.md §4.
- `substrateBootstrapNonce`/`FirstBootstrapWinsVerifier`'s security rests
  entirely on the NetworkPolicy restricting router ingress to the broker
  namespace (now shipped in `deploy/substrate/broker.yaml`) — documented as a
  Phase 2 hardening item in phase1-spec.md §5.

## Tests (§6, my half)

`pkg/runtime/substrate_runtime_test.go` (fake `ateapipb.ControlClient` +
`httptest`-backed actor server, sharing one `callRecorder` so cross-transport
call order — gRPC calls and HTTP calls — is asserted together):
- `TestSubstrateRun_HappyPath` — exact call order
  (`CreateAtespace, GetActorTemplate, CreateActorTemplate, CreateActor,
  CreateActorEgressPolicy, ResumeActor, GetActor, healthz, bootstrap`),
  returned id, bootstrap payload contents (`start_cmd` contains the tmux
  invocation, `control_token` non-empty, `SCION_RUNTIME=substrate` present,
  `cfg.Env` carried through), and that the control token was cached.
- `TestSubstrateRun_NonDigestImageError` — fails before `CreateActor`/
  `CreateActorTemplate`.
- `TestSubstrateRun_CleanupOnFailure` — table test over
  `CreateActorEgressPolicy` failing, `ResumeActor` failing, the actor
  reporting `CRASHED`, and bootstrap returning 500; asserts `DeleteActor` and
  `DeleteActorEgressPolicy` both ran in every case.
- `TestSubstrateTemplateName_Stable` — same inputs → same name; sandbox class
  and resources each change it.
- `TestSubstrateList_MappingAndLabelFilter` — phase mapping (`RUNNING`→
  running, `SUSPENDED`→stopped, `CRASHED`→error), `ContainerID`/`Runtime`
  fields, an actor with no in-memory record still listed, and label filtering.
- `TestSubstrateExec_ErrorMapping`, `TestSubstrateExec_Success`,
  `TestSubstrateExec_NoCachedToken`.
- `TestSubstrateRun_ErrorDoesNotLeakEnvValues` — equivalent to
  `TestCloudRunSandboxRun_ErrorDoesNotLeakEnvValues`: injects a secret value
  into both `cfg.Env` and a fake upstream error message, asserts the returned
  error redacts the value but keeps the diagnostic text and the key name.

`pkg/runtime/substrate/dialer_test.go` + `identity_test.go`:
- TLS config building: `CAFile`, `ClusterTrustBundle`, `ClusterTrustBundle`
  taking precedence over an unreadable `CAFile`, both-missing and
  invalid-PEM-contents errors, `InsecureSkipVerify` asserted false.
- Token caching (`CreateToken` called once across 3 calls within the TTL) and
  refresh (a new token minted once within `tokenRefreshSkew` of the cached
  one's expiry), plus the empty-minted-token error case.
- JWT `sub`-claim extraction: valid, malformed (empty/too few parts/too many),
  missing claim, and the namespace-mismatch / non-ServiceAccount-subject error
  cases in `currentServiceAccountIdentity`.

## Gate results

- `go build ./...` — pass (confirmed `go.mod`/`go.sum` untouched throughout,
  including after reverting the `go get` experiment for Finding/third_party
  above).
- `go vet ./...` — pass, no output.
- `go test ./pkg/runtime/...` — pass except `TestGetRuntime`,
  `TestGetRuntime_CloudRun`, `TestGetRuntime_CloudRun_DirectProfileName`,
  `TestGetRuntime_CloudRunSandbox_SettingsBased` (factory auto-detect tests
  sensitive to the local Docker-daemon/GCP-metadata/OS environment) — confirmed
  identical on an unmodified `c3b6e82` worktree via `git worktree add
  --detach`, so **pre-existing, not introduced by this branch**.
- `go test ./pkg/config/...` and `go test ./pkg/runtimebroker/...` — both have
  pre-existing failures, confirmed identical against the unmodified `c3b6e82`
  base via a detached worktree; root cause is the documented sandbox gotcha
  (leaked `SCION_*` env vars from this very agent's own runtime environment —
  `SCION_HUB_ENDPOINT`, `SCION_PROJECT_ID`, `SCION_GROVE`, etc. — leaking into
  settings/hub tests that don't clear them). Not related to this branch's
  changes.
- `go test ./cmd/...` — 3 pre-existing failures for the same env-leakage
  reason (confirmed against the unmodified base); `./cmd/sciontool/commands/...`
  (sb-dev-2's territory, which I did not touch) passes fully.
- `go test ./pkg/runtime/substrate/...` — all pass.
- `GOGC=40 golangci-lint run --new-from-rev=main --concurrency=1
  ./pkg/runtime/... ./pkg/runtimebroker/... ./pkg/config/... ./third_party/...`
  — 0 issues (fixed two findings along the way: an unchecked `fmt.Fprintf`
  into a `hash.Hash`, and a deprecated `kubefake.NewSimpleClientset` in a test,
  swapped for `NewClientset` to match the convention already used elsewhere in
  the repo).
- `gofmt -l` on every changed/new Go file — clean.

## Out of scope (per brief / reassigned)

`sciontool substrate-serve` and `pkg/sciontool/substrate/` (sb-dev-2, spec
§2.1); `deploy/substrate/broker.yaml` (reassigned to sb-dev-2 mid-task, spec
§2.4); the final nonce-source implementation beyond the Phase 1 fallback
(spec §5 — decided, see Decisions); `Suspender`/DATA-scope `Stop`, `$HOME`
layout, PTY/attach, template GC, tag→digest resolution, `scion doctor` for
substrate (all explicitly Phase 2+ per phase1-spec.md §3).

---

## Update: sb-dev task 2 (egress hardening) + review round 1 fixes

Two follow-on rounds of work, on top of the Phase 1 slice above: a
substrate-lead-directed security task (`briefs/sb-dev-task2.md`), and
`sb-rev`'s review round 1 (`reviews/round-1-sb-rev.md`, verdict REQUEST
CHANGES: 1 Critical, 4 Required, 6 Consider/Nit). The 409 fix is shared
between the two (task2 item 1 and review Required #2 are the same finding,
fixed once).

### Threat rationale (task2 item 3)

The Phase 1 bootstrap nonce (findings.md/phase1-spec.md §5) uses the
documented fallback: the control server accepts the *first* caller to
`POST /scion/v1/bootstrap`, with correctness resting on a NetworkPolicy
restricting router ingress to the broker namespace, not on the nonce value
itself. That means:

1. **A 409 response is the only signal available that the fallback's trust
   assumption was violated** — some caller other than the broker reached
   the actor's bootstrap endpoint first. Treating it as success (the
   pre-review behaviour) would leave the hub reporting a healthy, running
   agent that is actually executing an attacker-supplied `start_cmd`/env as
   root, with a `control_token` the broker doesn't hold. Task 2 item 1 /
   review Required #2 fix this: 409 now deletes the actor and its egress
   policy and fails `Run` with a secret-free error.
2. **Even a legitimately-bootstrapped actor must not be able to reach the
   router or other in-cluster services itself.** If it could, a compromised
   or buggy agent process could call `/scion/v1/bootstrap` on *other*
   actors, or otherwise poke at the control plane, regardless of whether the
   NetworkPolicy holds. Task 2 item 2 / review Consider #10 fix this:
   `egress_allow` (the operator-configurable extra hosts) can no longer be
   a catch-all, a private/in-cluster CIDR, or an in-cluster DNS suffix,
   validated at both settings-construction time and defensively again in
   `Run`.

Neither of these depends on whether the NetworkPolicy in
`deploy/substrate/broker.yaml` turns out to have gaps — they're independent
layers, per the brief's framing.

### Review round 1 findings addressed

- **Critical #1** — `SubstrateRuntime` was rebuilt (fresh `controlTokens`/
  `agentRecords`, a new never-closed gRPC `ClientConn`) on every
  `NewSubstrateRuntime` call, because the broker resolves substrate as an
  *auxiliary* runtime (not the default profile) and re-resolves it from
  settings on every `start`. A second agent would leave the first
  unreachable/unlistable/undeletable. Fixed with process-wide memoization
  (`substrateRuntimes`, keyed on a canonical JSON encoding of
  `V1SubstrateConfig`) plus making `List` always synthesise
  `scion.name`/`scion.agent` labels so a broker lookup by name survives a
  missing in-memory record regardless. `substrateRuntimeBuilder` is a
  package var so the memoization logic is unit-testable without a real
  cluster.
- **Required #2** (= task2 item 1) — bootstrap 409 now deletes the actor and
  fails `Run` (`errBootstrapHijacked`), instead of being treated as
  success.
- **Required #3** — `redact` now covers `ResolvedAuth.EnvVars` and both
  env- and file-type `ResolvedSecrets` (`substrateSecretCandidates`),
  not just `cfg.Env`/harness env (`externalEnvValues`'s coverage, which was
  written for cloudrun-sandbox's argv and was never extended for this
  runtime's actual secret sources).
- **Required #4** — the egress policy now adds a rule for
  `SCION_OTEL_ENDPOINT` / `OTEL_EXPORTER_OTLP_ENDPOINT` /
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, checked independently, instead of
  relying on `*.googleapis.com` to incidentally cover only the Cloud Trace
  default.
- **Required #5** — `List` now paginates (`next_page_token`) and skips
  actors outside a `scion-`-prefixed atespace (an empty-atespace
  `ListActors` call spans the whole cluster).
- **Consider #6** — added the missing cleanup-on-failure cases:
  `waitForHealthz` timeout and a `buildBootstrapFiles` read error (the
  table test now also covers 409 as part of the Required #2 fix).
- **Consider #7** — accepted the deviation from the spec's literal
  hash-input list: `substrateTemplateName` now also hashes
  `sandbox_config_name`, `worker_selector`, `snapshot_storage`, and the
  *effective* resources (nil resolved against
  `config.BuiltinDefaultResources()` before hashing, not hashed as `nil`).
  These are all real `ActorTemplate` content that `buildActorTemplate`
  bakes in; omitting them from the hash meant changing them in settings
  silently reused a stale golden template.
- **Consider #8** — `RouterClient` no longer carries a blanket HTTP
  timeout; `getHealthz`/`postBootstrap`/`doExec` each set their own context
  deadline (10s / 30s / `timeout_s`+10s) instead of racing a flat 30s
  client timeout that was shorter than exec timeouts up to 60s.
- **Consider #10** — added the `substrate` object to
  `settings-v1.schema.json`'s `runtimeConfig` `$def` (verified with an
  ad-hoc test that the broker's own `deploy/substrate/broker.yaml`
  ConfigMap settings now pass `ValidateSettings`). Left
  `cloudrun`/`cloudrun_instances`/`cloudrun_sandbox`'s equivalent
  pre-existing gap alone — out of scope here, per the original decision in
  this log's first section.
- **Consider #11** — `substrateAtespaceName` now sanitises through
  `sanitizeK8sShortNameFragment` (lowercase, replace non-alnum/hyphen with
  `-`, trim leading/trailing `-`) instead of assuming a project ID's first
  12 characters are already a valid Kubernetes short name.
- **Nit #13** — `agentRecords.HarnessConfig` is now populated from
  `cfg.Labels["scion.harness_config"]` (the same label key
  `CloudRunSandboxRuntime.Run` already reads it from, via the existing
  `labelValue` helper), instead of being permanently empty.
- **#9, #12** are sb-dev-2's files (`pkg/sciontool/substrate/server.go`,
  `deploy/substrate/broker.yaml`) — not touched here.
- **FYI items** (reaper/exec race measured clean, `*.googleapis.com`
  breadth, NetworkPolicy port-target confirmation, `RunInit`/keep-id
  interaction) are informational; no action needed from this side.

### Commit note

These fixes land as 8 commits rather than one-per-finding: several findings
(the Critical #1 memoization/List fix, the 409 handling, the redaction
wiring, and the atespace/healthz/HarnessConfig fixes) all touch the same
methods in `pkg/runtime/substrate_runtime.go` and were implemented in one
editing pass, so true per-finding atomicity would have meant hand-splitting
diffs after the fact rather than reviewable, working commits at each step.
Grouped instead by file/theme (router timeout removal;
`substrate_bootstrap.go`'s three fixes; template hash; telemetry egress;
`egress_allow` validation + schema; the `substrate_runtime.go` bundle;
tests), with each commit message enumerating every review finding it
covers.

### Gate results (this update)

- `go build ./...`, `go vet ./...` — pass.
- `go test ./pkg/runtime/...` — pass except the same 4 pre-existing
  `TestGetRuntime*` failures noted above.
- `go test ./pkg/config/...` — pass except the same pre-existing env-leakage
  failures noted above (unchanged set).
- `go test -race ./pkg/runtime/... -run Substrate` and
  `go test -race ./pkg/runtime/substrate/...` — pass (the new
  `substrateRuntimesMu`-guarded registry has no detected race).
- `GOGC=40 golangci-lint run --new-from-rev=main --concurrency=1
  ./pkg/runtime/... ./pkg/config/...` — 0 issues.
- `gofmt -l` on every changed file — clean.
- Ad-hoc test confirmed the broker's `deploy/substrate/broker.yaml`
  ConfigMap settings pass `config.ValidateSettings` against the updated
  schema (Consider #10).

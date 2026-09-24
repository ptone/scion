# sb-dev-mitm: opt-in egress-gateway trust bundle for Substrate actors (option A)

**APPROVED** — ptone chose (A) (substrate-lead, 2026-09-24 21:24Z). Built on
`origin/scion/substrate-integration` at `cf8a1487f`.

## Why

Live diagnosis (`infra/cluster.md`, "Egress diagnosis @ 29d98cf31
(run2-cold-1)") found all actor HTTPS failing: envoy passthrough leg, flags
`UH`, `original_dst` null. Substrate's plain `atenet-egress` honours only
ADDRESS rules for TLS passthrough; hostname rules — which is what every
`egress_allow` entry becomes — apply to HTTPS only under the **sdsmint**
MITM gateway, which terminates every TLS connection and re-originates it
with a per-SNI leaf certificate chained to its own CA, not the origin's. An
actor validating only public roots rejects that certificate and every HTTPS
request fails.

## What changed

1. **`pkg/config/settings_v1.go`**: `V1SubstrateConfig.EgressTrustBundle
   string` (`egress_trust_bundle` in json/yaml/koanf), default off, next to
   `EgressAllow`. Doc comment states the "required iff sdsmint" /
   "breaks a plain install" tradeoff up front.
2. **`pkg/config/substrate_egress.go`**: `ValidateEgressTrustBundle`, wired
   into `V1SubstrateConfig.Validate`. Empty is OK (off); the single
   supported value `egress-mitm.ate.dev` (the only name vendored Substrate
   d277088b resolves) is OK; anything else is rejected with an error naming
   the supported value, so a typo fails at settings-validation time with a
   clear message instead of failing much later and much less clearly at
   actor start.
3. **`pkg/runtime/substrate_template.go`**: `buildActorTemplate`, only when
   `sc.EgressTrustBundle != ""`:
   - a `Volume{Name: "system-info", SystemInfo: {DataSources:
     [{TrustBundle: {Name: sc.EgressTrustBundle, Path:
     "trust-bundle.pem"}}]}}`;
   - a `VolumeMount{Name: "system-info", MountPath: "/run/ate"}` on the
     `scion-agent` container (system-info volumes are inherently read-only
     per the proto's own doc comment — `ateapipb.VolumeMount` has no
     `ReadOnly` field to set);
   - `Env`: `NODE_EXTRA_CA_CERTS`, `GIT_SSL_CAINFO`, `SSL_CERT_FILE`,
     `CURL_CA_BUNDLE` all `/run/ate/trust-bundle.pem`, plus
     `SSL_CERT_DIR=/run/ate`.

   Off is byte-identical to before this field existed: `Env` stays `nil`,
   `Volumes`/`VolumeMounts` stay exactly the pre-change one-element slices
   (verified with a golden `proto.Equal` test built from the pre-change
   source, not from the function under test).

   `substrateTemplateName`'s hash appends `sc.EgressTrustBundle` as its own
   `|`-segment **only when non-empty** — not as an always-present 10th
   `%s` field — so an unset value keeps producing the exact literal name it
   always has (pinned in a test), and a set value always differs (also
   tested).

4. **Env-propagation trace** (container Env → consumers) — no code change
   needed beyond the template itself; every hop already passes the full
   process environment through rather than an allowlist:
   - **`sciontool substrate-serve`** (`cmd/sciontool/commands/substrate_serve.go:156-226`,
     `runSubstrateServe`) is PID 1; the container's `Env` becomes its
     `os.Environ()` directly (Substrate sets container env the same way any
     OCI runtime does).
   - **init's git clone** (`GIT_SSL_CAINFO`): `gitCloneWorkspace`
     (`cmd/sciontool/commands/init.go:2107`) runs every git subprocess
     through `setupGitCmd` → `configureGitCommand`
     (`cmd/sciontool/commands/init.go:2507-2528`), which sets
     `cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")` — a full
     copy of the parent's env, not a filtered subset — before optionally
     attaching a `syscall.Credential` (UID/GID switch via the exec'd
     process's own privilege drop, **not** a login shell, so nothing resets
     the environment here). `GIT_SSL_CAINFO` reaches `git` unchanged when
     present in the parent's env. Test:
     `TestConfigureGitCommand_PropagatesTrustBundleEnv`
     (`cmd/sciontool/commands/init_test.go`).
   - **harness child via the supervisor credential drop**
     (`NODE_EXTRA_CA_CERTS`): `Supervisor.Run`
     (`pkg/sciontool/supervisor/supervisor.go:97-227`). The privilege-drop
     branch (lines 123-136) and the fallback (lines 153-156) both start
     `s.cmd.Env` from `os.Environ()`; the only keys ever rewritten are
     `HOME`/`USER`/`LOGNAME` (`setEnvVar`), and the later harness-env-overlay
     merge (`mergeEnvOverlay` / `hooks.MergeEnvOverlayWithNativeTelemetryPolicy`,
     lines 169-187) is additive-only — it never overwrites a key already
     present, so it cannot mask `NODE_EXTRA_CA_CERTS` etc. either. The
     actual `syscall.Credential` UID/GID switch is a plain `execve`-time
     credential change, not a login shell (`su`), so nothing resets the
     environment. `sciontool init`'s child command for substrate is `tmux
     new-session -A -s main ...`
     (`cmd/sciontool/commands/init.go:82`), exec'd directly by `Supervisor.Run`
     with this env — tmux's server, started fresh on this exec, captures
     that as its own process environment and every pane spawned from it
     (including the harness) inherits it via tmux's normal environment
     seeding. No `update-environment`/`set-environment` config exists in
     this codebase, and none is needed: that tmux option only matters for a
     *second* client re-attaching to an *already-running* server, which
     doesn't happen on a fresh actor start (a config change also forces a
     new template/golden snapshot per point 3 above, so there's no
     stale-server case to worry about here either). Test:
     `TestSupervisor_HarnessChildInheritsCABundleEnv`
     (`pkg/sciontool/supervisor/trust_bundle_env_test.go`).
   - **sciontool's own Go HTTPS to the hub** (`SSL_CERT_FILE`):
     `hub.NewClient()` (`pkg/sciontool/hub/client.go:199-211`) builds
     `&http.Client{Timeout: ...}` with no `Transport` set; `configureOIDCTransport`
     (`pkg/sciontool/hub/oidc.go:45-77`) wraps whatever transport is there
     via `transportauth.Wrap`, which falls back to `http.DefaultTransport`
     when nil (`pkg/transportauth/transportauth.go:237-246`) — no custom
     `TLSClientConfig`/`RootCAs` anywhere in this chain. Go's default
     transport leaves `tls.Config.RootCAs` nil, so `crypto/x509`'s
     `SystemCertPool()` runs at connection time and — on Linux —
     `SSL_CERT_FILE`/`SSL_CERT_DIR` are exactly the env vars it consults.
     Same process as substrate-serve itself, so no exec/credential-drop
     boundary to cross at all.
   - **`sciontool substrate-serve exec`'s `su -` path — checked, found to
     drop these vars, but out of scope for this brief's four required
     consumers.** `runExec` (`pkg/sciontool/substrate/exec.go:48-110`) runs
     `execAsUserCmd(user, cmd)` (`pkg/sciontool/substrate/execuser.go:33-36`):
     `if whoami == user then exec sh -c "$2" else exec su - "$1" -c "$2"`.
     substrate-serve always runs as root (Substrate starts every actor as
     UID 0), so the exec-user branch (`su -`) is the one that actually
     runs. **`su -` is a login shell and discards the inherited
     environment**, keeping only its own minimal login set (`HOME`,
     `LOGNAME`/`USER`, `PATH`, `TERM`, and whatever `/etc/login.defs`
     `ENV_SUPATH`/PAM add) — none of the five CA-bundle vars survive it.
     This does **not** affect any of the four consumers above (none of them
     goes through `/scion/v1/exec`), and it is not a regression this brief
     introduces: any env var beyond that minimal set already failed to
     survive `su -` before `egress_trust_bundle` existed. Flagged, not
     fixed: if a future exec-invoked command (e.g. a message-delivery
     script making its own HTTPS call) needs the trust bundle, it will need
     its own fix — most likely passing the CA-bundle vars through
     `execAsUserCmd`'s wrapper script explicitly (e.g. `env
     SSL_CERT_FILE=... su - ...`) rather than relying on inheritance. Not
     implemented here since nothing in this brief's required scope needs
     it; call this out to sb-em if a future exec-based consumer needs TLS
     under sdsmint.
5. **`deploy/substrate/README.md`**: new `egress_trust_bundle` section
   (mirrors the existing `egress_allow` section's style) covering when to
   set it, what it projects, the `SSL_CERT_DIR`/Node-additive note, and the
   exact failure text on a plain install.

## Tests

- `pkg/config/substrate_egress_test.go`: `TestValidateEgressTrustBundle`,
  `TestV1SubstrateConfig_Validate_EgressTrustBundle` — empty OK, the
  supported name OK, anything else rejected naming the supported value.
- `pkg/runtime/substrate_trust_bundle_test.go`:
  - `TestBuildActorTemplate_EgressTrustBundleUnset_MatchesPreChangeGolden` —
    `proto.Equal` against a hand-built want mirroring the pre-change
    `buildActorTemplate` source exactly (no volume, no mount, `Env: nil`).
  - `TestBuildActorTemplate_EgressTrustBundleSet` — exact volume, mount, and
    five env vars.
  - `TestSubstrateTemplateName_UnchangedWhenEgressTrustBundleUnset` — pins
    the literal `scion-52ec9dfe17f8` for a fixture config, computed from the
    hash formula unchanged since before this field existed.
  - `TestSubstrateTemplateName_ChangesWhenEgressTrustBundleSet` — same
    fixture, name differs once set.
- `cmd/sciontool/commands/init_test.go`:
  `TestConfigureGitCommand_PropagatesTrustBundleEnv`.
- `pkg/sciontool/supervisor/trust_bundle_env_test.go`:
  `TestSupervisor_HarnessChildInheritsCABundleEnv` — runs a real child
  process and reads back its env via a temp file (Supervisor hardcodes
  `Stdout`/`Stderr` to the test binary's own, so output has to be captured
  this way rather than via `cmd.Output()`).

### Mutation table (each reverted after; `git diff` and
`sha256sum /home/scion/agent-info.json` confirmed 0 residual changes)

| Mutation | Killed by |
|---|---|
| Drop the `system-info` volume from `buildActorTemplate` | `TestBuildActorTemplate_EgressTrustBundleSet` (`Volumes = 1, want 2`) |
| Drop `CURL_CA_BUNDLE` from the Env list | `TestBuildActorTemplate_EgressTrustBundleSet` (`Env = 4 entries, want 5`) |
| Remove the `EgressTrustBundle` hash-input segment from `substrateTemplateName` | `TestSubstrateTemplateName_ChangesWhenEgressTrustBundleSet` (name did not change) |

`agent-info.json` sha256 before/after all three mutation runs:
`fd40d02d1d2487a7e6d829345fe9406ee705b1a35dd57ea4125163797ba003ea` (0
changes — the test suite touches no real agent state).

## Gates

`cleanenv` applied before every run (unset `SCION_*` and
`CLAUDE_CODE_ENABLE_TELEMETRY`).

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test -count=1 ./pkg/runtime/... ./pkg/config/... ./cmd/sciontool/...` —
  all packages `ok`. (Without `cleanenv`, `pkg/runtime`'s `TestGetRuntime_*`
  family flakes on ambient `SCION_*` vars leaking from the outer agent
  shell into runtime auto-detection — confirmed this reproduces
  identically on the unmodified `cf8a1487f` base and disappears once
  `cleanenv` is applied; not caused by this change, and not one of the
  gate's listed known-failures since it isn't a real failure once the gate
  is run as specified.)
- `make lint` — clean.
- `golangci-lint run` (scoped to the touched packages) — `0 issues`.

## Declined options

- **A `ReadOnly` field on the `VolumeMount`**: the brief says "read-only
  VolumeMount"; `ateapipb.VolumeMount` has only `Name`/`MountPath` — no such
  field exists in this proto version. `SystemInfoVolumeSource`'s own doc
  comment already states it is "a read-only volume of substrate-generated
  per-actor files", so the mount is read-only by construction; nothing to
  add.
- **Fixing the `su -` exec-path env drop**: researched (see trace above),
  confirmed real, but out of scope — none of this brief's four required
  consumers go through `/scion/v1/exec`. Flagged for sb-em rather than
  fixed preemptively, per the brief's "propose the minimal fix ... BEFORE
  implementing" instruction — there is no fix to propose because nothing
  required here needs one.
- **A bool instead of a string for the setting**: explicitly decided against
  in the brief itself (mirrors Substrate's own `trustBundle.name`; a future
  second name needs only an allowlist entry).

## Commits

- `1b4cab0e` feat(config): add opt-in egress_trust_bundle setting for sdsmint actors
- `d4074c70` feat(runtime): project the sdsmint trust bundle into the actor template
- `a6f3d4e5` test(sciontool): prove the CA-bundle env reaches the git clone and harness child
- `4bfbe085` docs(substrate): document egress_trust_bundle and the SSL_CERT_DIR note

All pushed to `origin/scion/substrate-integration`.

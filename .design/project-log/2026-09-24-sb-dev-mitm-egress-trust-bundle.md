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
     environment. **Correction (round-18 review, FYI-3):** the Substrate
     harness child's actual launch command is `buildSubstrateStartCmd` →
     `tmuxAgentWindowCmd` + `buildTmuxStartCmd`
     (`pkg/runtime/substrate_runtime.go:973-979`,
     `pkg/runtime/common.go:140-178`): `tmux new-session -d -s scion -n
     agent /bin/sh -c '<harness>; echo $? > …'` — not `tmux new-session -A
     -s main` (`cmd/sciontool/commands/init.go:82` is that command's own
     `--help` text, not what substrate-serve actually execs). The
     conclusion is unchanged either way: exec'd directly by
     `Supervisor.Run` with this env, tmux's server (started fresh on this
     exec) captures that as its own process environment and every pane
     spawned from it (including the harness) inherits it via tmux's normal
     environment seeding. No `update-environment`/`set-environment` config
     exists in this codebase, and none is needed: that tmux option only
     matters for a *second* client re-attaching to an *already-running*
     server, which doesn't happen on a fresh actor start (a config change
     also forces a new template/golden snapshot per point 3 above, so
     there's no
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

## Round 18 (sb-dev-mitm-r18): the su - exec fix, plus R-2/R-3/N-1

Base: `76c0609fb`. Full review at
`reviews/round-18-sb-rev-18.md` (sb-rev-18, REQUEST CHANGES: 0 Critical /
3 Required / 1 Optional / 5 FYI).

### Task 1 (R-1, DECIDED by substrate-lead): `su -w` conditional on the CA vars

The round-18 review reproduced trace item 5 from the original report: every
`sciontool substrate-serve exec` call (the broker exec endpoint, `scion
look`, `/scion/v1/exec` directly) runs through `execAsUserCmd`
(`pkg/sciontool/substrate/execuser.go`), which used `su - "$1" -c "$2"`
whenever the exec-as-user differs from the caller — true for every
Substrate exec, since substrate-serve is always root and `ExecUser()` is
`"scion"`. `su -` is a login shell; it discards the whole inherited
environment, so any exec-invoked command doing TLS lost the gateway CA
under sdsmint even though the harness itself trusted it fine.

**Fix, in the Substrate copy only:** `execAsUserCmd` now passes `su -w
<list> - "$1" -c "$2"` when at least one CA-bundle var is set (non-empty)
in the pre-su environment, and the exact pre-fix `su - "$1" -c "$2"`
otherwise — byte-identical script and argv on every plain install, since
none of the vars is ever set there. `<list>` names only the vars that ARE
set, in a fixed order.

**Design choice: build `<list>` in Go, not in the sh wrapper.** The brief
left this open ("pick whichever keeps the plain-path script/argv
byte-identical and is easiest to test, and justify the choice"). Go wins on
both counts here: `execAsUserCmd` already returns a fully-resolved argv
(no shell-side string processing exists to reuse), and building the list
in Go makes every case (plain, all-5, subset, empty-string-counts-as-unset)
a pure function of a `[]string` input — no process spawn, no PATH shim,
no real `su`/`sh` needed for the required tests. The one required test
that's specific to a sh-built list ("(f), if the list is built in sh") is
therefore not required here, but a confidence test that actually runs the
generated script through `/bin/sh` with a PATH-shimmed `su` recorder is
included anyway (`TestExecAsUserCmd_RealShellInvokesSuWithExpectedArgv`)
since it's cheap and closes the "does a real shell parse this the way the
Go string implies" gap.

**Shared source of truth:** the candidate names are
`pkg/substrateenv.TrustBundleVarNames` — a new, dependency-free package
(same shape and rationale as the existing `pkg/substratecaps`) that both
`pkg/runtime`'s `buildActorTemplate` and `pkg/sciontool/substrate`'s
`execAsUserCmd` import directly. `buildActorTemplate` was refactored to
build its `Env` slice by iterating this shared slice (mapping every name to
the bundle file, except `SSL_CERT_DIR` which gets the bundle's mount
directory) instead of a hand-written literal, so the two lists cannot
silently drift apart. Two tests tie them together from each side:
`TestExecAsUserCmd_CandidateNamesMatchTemplateEnvNames`
(`pkg/sciontool/substrate`) and
`TestBuildActorTemplate_EnvNamesMatchSharedTrustBundleVarNames`
(`pkg/runtime`).

`pkg/runtime.ExecAsUserCmd` (every other runtime's copy) is untouched, per
the brief: `-w` is util-linux-specific and this whole mechanism only exists
to counter Substrate's own env-propagation shape.

Also added: a comment in `execuser.go` documenting the intentional
divergence from `pkg/runtime.ExecAsUserCmd` (R-1's second ask), and a
README note (next to `egress_trust_bundle`) that the image needs util-linux
≥ 2.35 for `su -w`, which scion's images (Debian trixie) already satisfy.

#### Tests (pkg/sciontool/substrate/execuser_test.go)

- (a) `TestExecAsUserCmd_PlainEnvIsByteIdentical` — pins the exact pre-fix
  script/argv literal.
- (b) `TestExecAsUserCmd_AllCAVarsSet` — all 5 set → `-w` with exactly
  those 5, in order.
- (c) `TestExecAsUserCmd_SubsetOfCAVarsSet` — 2 of 5 set → `-w` with
  exactly those 2, in the fixed order (not env order).
- (d) `TestExecAsUserCmd_EmptyValueCountsAsUnset` — `SSL_CERT_FILE=` (empty
  value) is treated as unset.
- (e) `TestExecAsUserCmd_CandidateNamesMatchTemplateEnvNames` (this
  package) + `TestBuildActorTemplate_EnvNamesMatchSharedTrustBundleVarNames`
  (`pkg/runtime`) — the tie test, from both sides.
- (f) not required (list built in Go, not sh) but included anyway:
  `TestExecAsUserCmd_RealShellInvokesSuWithExpectedArgv`.

#### Mutation table (Task 1)

Each applied by hand in the real environment, confirmed to fail, then
reverted (`git diff` clean afterward). `agent-info.json` sha256:
`fd40d02d1d2487a7e6d829345fe9406ee705b1a35dd57ea4125163797ba003ea` before
Task 1's mutation runs and after every revert — 0 changes.

| Mutation | Result |
|---|---|
| Removed the `if list != ""` condition, so `-w <list> -` is always passed | KILLED by (a): plain-env literal no longer matched (`su -w  - ...` with an empty list) |
| Dropped `CURL_CA_BUNDLE` from `substrateenv.TrustBundleVarNames` | KILLED by (b): all-5 case only produced 4 names. (e)'s exec-side test still passed, since both sides read the same mutated slice — exactly why the brief allows "(b) or (e)" |
| Removed the `set[name] != ""` presence check in `trustBundleWhitelist`, so every candidate name is always appended | KILLED by (c): subset case (2 set) produced all 5 names instead of 2 |

### Task 2: round-18 findings

| Finding | Resolution | Commit |
|---|---|---|
| R-1 (exec path drops CA vars) | Fixed — see Task 1 above. Divergence comment added to `execuser.go`. | `d40717ac` |
| R-2 (process references in shipped code) | Fixed — reworded every listed location (`init_test.go:921`, `substrate_egress_test.go:197-199`, `substrate_template.go:234`, `substrate_trust_bundle_test.go:31,40,192`, `trust_bundle_env_test.go:16`) to describe behaviour instead of naming an agent/brief/review. Pinned literal `scion-52ec9dfe17f8` kept. Re-ran the hygiene grep on every changed non-log file (Task 1's new files included) — the only hits left are false positives (hex capability bitmasks, a decimal IP literal, and the two legitimately-pinned template-name literals); see the grep output below. | `d40717ac`, `1d8c5e67` |
| R-3 (false "exclusive anchor" claim for curl/git/Node) | Fixed — reworded `deploy/substrate/README.md`'s `egress_trust_bundle` section and the `buildActorTemplate` code comment to the substrate-lead's exact binding wording: SSL_CERT_DIR is exclusive for Go and Python `ssl`, additive-or-ignored for curl/git/Node; states what it buys (a hub status success is positive proof for Go; a bypassed gateway fails closed) and the cost (status reports fail TLS if the hub is ever reached without the gateway re-originating). Added the curl/git compiled-in-CApath explanation and the `curl -sv` issuer check / `--capath /nonexistent --cacert ...` proof method. `SSL_CERT_DIR` itself stays set — not relitigated. | `d40717ac`, `1d8c5e67` |
| N-1 (Optional: table-driven name tests) | Done — `TestSubstrateTemplateName_UnchangedWhenEgressTrustBundleUnset` / `_ChangesWhenEgressTrustBundleSet` are now table-driven over the original fixture plus a worker-selector + nil-resources fixture (`substrateTemplateNameFixtures`). New literal `scion-3b33f56da495` computed and pinned for the second fixture. | `d40717ac` |
| FYI-1 (cleanenv) | No action — already how gates were run. |
| FYI-2 (sciontool's Go client trusts only the gateway CA; a bypass fails closed) | No action — this is exactly the tradeoff R-3's reworded text now states explicitly. |
| FYI-3 (project log's tmux command was wrong) | Fixed — corrected the original entry above: the real launch is `buildSubstrateStartCmd` → `tmuxAgentWindowCmd`/`buildTmuxStartCmd` → `tmux new-session -d -s scion -n agent /bin/sh -c …` (`pkg/runtime/substrate_runtime.go:973-979`, `pkg/runtime/common.go:140-178`), not `tmux new-session -A -s main` (`init.go:82`, that command's own `--help` text). Conclusion unchanged. | (log-only, this entry) |
| FYI-4 (`proto.Equal` nil vs empty repeated field) | No action — equivalent mutant, as the review notes. |
| FYI-5 (rotation reaches only resumed/new actors) | No action — already documented in `docs/egress-trust-bundle.md` upstream. |

#### Hygiene grep (re-run on every changed non-log file, including Task 1's new files)

```
grep -rniE 'round [0-9]|this round|sb-rev|sb-dev|sb-em|substrate-lead|finding #|the review|reviewer|the brief|addendum|another agent|[0-9a-f]{9}' \
  cmd/sciontool/commands/init_test.go deploy/substrate/README.md \
  pkg/config/substrate_egress_test.go pkg/runtime/substrate_template.go \
  pkg/runtime/substrate_trust_bundle_test.go pkg/sciontool/substrate/execuser.go \
  pkg/sciontool/substrate/execuser_test.go pkg/sciontool/supervisor/trust_bundle_env_test.go \
  pkg/substrateenv/substrateenv.go

cmd/sciontool/commands/init_test.go:582:	// /proc/self/uid_map typically shows "0 0 4294967295" or similar.
cmd/sciontool/commands/init_test.go:1048:			input: "Name:\tinit\nCapEff:\t000001ffffffffff\n",
cmd/sciontool/commands/init_test.go:1054:			input: "Name:\tinit\nCapInh:\t0000000000000000\nCapEff:\t00000000000000ff\n",
cmd/sciontool/commands/init_test.go:1060:			input: "Name:\tinit\nCapEff:\t000000000000007f\n",
cmd/sciontool/commands/init_test.go:1065:			input: "Name:\tinit\nCapEff:\t0000000000000000\n",
cmd/sciontool/commands/init_test.go:1070:			input: "Name:\tinit\nCapInh:\t0000000000000000\n",
cmd/sciontool/commands/init_test.go:1086:			input: "CapEff:\t0000000000000080\n",
pkg/config/substrate_egress_test.go:274:		{"decimal IP alias", "2130706433"},
pkg/config/substrate_egress_test.go:559:		{"IP/CIDR: decimal IP alias", "2130706433", reasonIPCIDR},
pkg/runtime/substrate_trust_bundle_test.go:229:			wantUnset: "scion-52ec9dfe17f8",
pkg/runtime/substrate_trust_bundle_test.go:245:			wantUnset: "scion-3b33f56da495",
```

Every remaining hit is a false positive against the pattern (hex capability
bitmasks, a decimal IP literal, or the two pinned template-name literals
the review explicitly said to keep) — no genuine process/agent reference
left.

### Gates (round 18)

`cleanenv` applied first (unset `SCION_*`, `CLAUDE_CODE_ENABLE_TELEMETRY`).

| Gate | Result |
|---|---|
| `go build ./...` | clean |
| `GOOS=darwin go vet ./cmd/sciontool/commands/ ./pkg/sciontool/...` | clean |
| `go vet ./...` | clean |
| `go test -count=1 ./pkg/runtime/... ./pkg/config/... ./pkg/sciontool/... ./cmd/sciontool/...` | all `ok` |
| `go test -count=1 -race ./pkg/sciontool/substrate/...` | clean, no races |
| `make lint` | clean |
| `golangci-lint run --new-from-rev=c3b6e821d ./...` | `0 issues` |

No deviation from the known pre-existing failure list (same as the
original brief's list).

### Commits (round 18)

- `d40717ac` fix(sciontool): keep the CA-bundle env across the su - exec path (R-1)
- `1d8c5e67` fix: reword process references and correct the SSL_CERT_DIR claim (R-2, R-3)
- `472823de` docs(substrate): note the util-linux >= 2.35 requirement for su -w

All pushed to `origin/scion/substrate-integration`.

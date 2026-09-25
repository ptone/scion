# sb-dev-cwd: harness cwd under Substrate (blocker for C(v)(b) / AC1)

Built on `origin/scion/substrate-integration` at `9b5ca72bf`. Branch
`scion/substrate-cwd`.

## Why

Live evidence at 9b5ca72bf (GKE substrate-scion-test): under the substrate
runtime the harness process tree (claude, its sh, tmux) started with
cwd=`/`, because the ateapi Container spec has no `workingDir` field, ateom
does not apply the image's `WorkingDir`, and `sciontool` set no `cmd.Dir` for
the child. claude then showed the folder-trust dialog for `/` (the delivered
`~/.claude.json` only trusts `/workspace`), the agent never made a model
call, and AC1 failed.

## What changed

1. **`pkg/sciontool/supervisor/supervisor.go`**: `Config.WorkingDir string`.
   `Run()` sets `s.cmd.Dir = s.config.WorkingDir` only when non-empty;
   otherwise `cmd.Dir` stays unset (today's behaviour, unconditionally, for
   every existing caller).
2. **`cmd/sciontool/commands/init.go`**: `InitRunOptions.WorkingDir string`,
   threaded straight into `supervisor.Config.WorkingDir` in `RunInit`. Plain
   plumbing — `RunInit` never inspects `SCION_RUNTIME` or anything else to
   decide this; it's a pure function of the field, exactly like the existing
   `RequirePrivilegeDrop`.
3. **`cmd/sciontool/commands/substrate_serve.go`**: new
   `resolveSubstrateHarnessCwd(substrateHarnessCwdDeps) string` (with an
   injectable-deps struct for tests, same pattern as
   `privilegeDropPreconditionDeps`), and `substrateServeInitOptions` now sets
   `WorkingDir: resolveSubstrateHarnessCwd(defaultSubstrateHarnessCwdDeps)`.
   Resolution order: `SCION_WORKSPACE_PATH` (default `/workspace`) if it's a
   directory; else `$HOME` if that's a directory other than `/`; else `""`
   (today's behaviour — never `/`). One `log.Info` line on each fallback,
   path-only via `%q`.
4. **`deploy/substrate/README.md`**: one-line note next to the existing
   `su -`/CA-bundle-env discussion, clarifying the harness/tmux cwd is
   resolved as above, and that a later `su -` exec still resets cwd to
   `$HOME` on login (matching `docker exec ... su -` on every other
   runtime) — left as-is, not a regression.
5. Tests (see below) in `pkg/sciontool/supervisor/supervisor_test.go`,
   `cmd/sciontool/commands/substrate_serve_test.go`, and a new
   `pkg/runtime/workdir_guard_test.go`.

## Launch sites (file:line, at the branch's HEAD after this change)

- Actual OS child spawn: `pkg/sciontool/supervisor/supervisor.go` —
  `exec.Command` around line 103 (unmoved), new `cmd.Dir` assignment
  immediately after `cmd.Stderr = os.Stdout`.
- Substrate's `childArgs` construction: `pkg/sciontool/substrate/server.go`
  `handleBootstrap`, `childArgs := []string{"sh", "-c", req.StartCmd}`
  (unchanged) — `req.StartCmd` is documented on `BootstrapRequest.StartCmd`
  as "the same command string the k8s runtime places in SCION_START_CMD (a
  tmux invocation)".
- `InitRunner` wiring: `cmd/sciontool/commands/substrate_serve.go`
  `newSubstrateServeServer`/`substrateServeInitOptions`.

## Scoping argument: why other runtimes are unaffected

The fix is substrate-only **by code path**, not by sniffing:
`resolveSubstrateHarnessCwd` and its call site live entirely in
`cmd/sciontool/commands/substrate_serve.go`, which only runs under
`sciontool substrate-serve`. `RunInit`/`supervisor.Run` (shared by every
runtime) gained one new, optional `string` field each; both stay a strict
no-op — `cmd.Dir` is left unset exactly as before — for any caller that
doesn't set it, which is every caller except substrate-serve's
`substrateServeInitOptions`. `sciontool init` (the CLI command, used by
Docker/Podman/Apple Container/Cloud Run/-sandbox) never sets
`InitRunOptions.WorkingDir`, so its `InitRunOptions{}` zero value is
byte-identical to before this change.

Docker/Podman/Kubernetes/Cloud Run already get the correct cwd for free from
the image's `WORKDIR`, which they honour unlike Substrate's ateom — nothing
about their launch paths (`pkg/runtime/common.go`'s `buildCommonRunArgs`,
`pkg/runtime/k8s_runtime.go`'s `buildPod`) was touched. Two new guard tests
in `pkg/runtime/workdir_guard_test.go`
(`TestBuildCommonRunArgs_NoWorkspaceCwdFlag`,
`TestKubernetesRuntime_BuildPod_NoWorkspaceCwdFlag`) pin today's tmux
command shape for Docker/Podman and Kubernetes, and the existing parity
suites (`common_test.go`, `k8s_parity_test.go`, `k8s_runtime_tmux_test.go`)
all still pass unmodified.

## Why one mechanism (cmd.Dir) covers both "the child" and "the tmux session"

Substrate's `handleBootstrap` always execs the harness as
`sh -c req.StartCmd`, where `req.StartCmd` is an opaque tmux invocation
string built client-side in `pkg/runtime` (shared with, and parity-tested
against, every other runtime). Setting `cmd.Dir` on that one `sh` process
(via `InitRunOptions.WorkingDir` -> `supervisor.Config.WorkingDir`) is
sufficient for both cases:

- if `req.StartCmd` is a bare command, `cmd.Dir` is its cwd directly;
- if `req.StartCmd` is `tmux new-session ...` (the substrate case, always),
  `tmux new-session` invoked **without** an explicit `-c` flag defaults its
  initial pane's directory to its invoking client's cwd — i.e. exactly
  `cmd.Dir` — so the "agent" window (the harness) and the "shell" window
  `new-window` creates alongside it both land there too, and `scion attach`
  (which reattaches to those same panes) sees the same result.

This was chosen over parsing/rewriting `req.StartCmd` to inject a literal
`tmux new-session -c <dir>` flag, because that string's construction
(`pkg/runtime/common.go`, `pkg/runtime/substrate_runtime.go`) is shared
infrastructure covered by its own parity tests; duplicating tmux's
cwd-construction logic in the substrate-only server code would only add a
second, driftable place that could disagree with it, for no behavioural
gain. `TestSubstrateServeBootstrap_ThreadsWorkingDirToInitRunner` proves
`req.StartCmd`/`childArgs` are untouched while `InitRunOptions.WorkingDir`
carries the resolved directory.

## Fallback: $HOME, never `/`

Per the binding brief amendment, `resolveSubstrateHarnessCwd` never returns
`/`: if `SCION_WORKSPACE_PATH` isn't a directory, it falls back to `$HOME`
(rejecting `$HOME == "/"` explicitly); if neither resolves, it returns `""`
(cmd.Dir stays unset — never `/`). `/` is not added to any trust list
anywhere in this change.

## Tests

- `pkg/sciontool/supervisor/supervisor_test.go`:
  - `TestSupervisor_RunWithWorkingDir` — `Config.WorkingDir` sets `cmd.Dir`
    (verified via a relative-path `touch marker` side effect, since
    `cmd.Stdout` is hardcoded to `os.Stdout`).
  - `TestSupervisor_RunWithoutWorkingDir_LeavesCmdDirUnset` — the
    non-substrate/default path is unchanged.
- `cmd/sciontool/commands/substrate_serve_test.go`:
  - `TestResolveSubstrateHarnessCwd_UsesWorkspaceWhenValid`,
    `_DefaultsWorkspacePath` — normal case + default.
  - `TestResolveSubstrateHarnessCwd_FallsBackToHomeWhenWorkspaceMissing`,
    `_FallsBackToHomeWhenWorkspaceIsAFile` — the fallback rule.
  - `TestResolveSubstrateHarnessCwd_NeverFallsBackToRoot` — table-driven
    guard (`HOME` is `/`, `HOME` unset, `HOME` also missing on disk); asserts
    the result is never `/` in any case.
  - `TestResolveSubstrateHarnessCwd_NoUsableDirLeavesUnset` — final rung
    returns `""`.
  - `TestSubstrateServeInitOptions_SetsWorkingDirFromRealEnv`,
    `_FallsBackToHomeFromRealEnv` — the wiring against real
    `os.Getenv`/`os.Stat`, not just the injected-deps unit tests.
  - `TestSubstrateServeBootstrap_ThreadsWorkingDirToInitRunner` — full HTTP
    `/scion/v1/bootstrap` request through `newSubstrateServeServer`, proving
    both that `InitRunOptions.WorkingDir` is resolved from the bootstrap
    request's own env and that `childArgs` (the tmux invocation) is
    unmodified.
- `pkg/runtime/workdir_guard_test.go` (new file, no production code changed
  in this package):
  - `TestBuildCommonRunArgs_NoWorkspaceCwdFlag`,
    `TestKubernetesRuntime_BuildPod_NoWorkspaceCwdFlag` — guard that
    Docker/Podman and Kubernetes launch args are unchanged.

All of the above pass. Pre-existing, environment-dependent failures
unrelated to this change (confirmed identical on `origin/scion/substrate-integration`
before this branch's commits, via `git stash`):
`TestNativeTelemetryPolicyEffectiveChildEnv/disabled` (this sandbox has
`CLAUDE_CODE_ENABLE_TELEMETRY=1` set in its ambient environment, which the
test doesn't expect); `TestGetRuntime`, `TestGetRuntime_CloudRun*`,
`TestGetRuntime_Substrate_SettingsBased_Memoized` (runtime auto-detection
depends on host binaries — docker/apple-container/gcloud — not present in
this sandbox).

## Verification run

- `go build -buildvcs=false ./...` — clean, whole repo.
- `go vet -buildvcs=false ./cmd/sciontool/... ./pkg/sciontool/... ./pkg/runtime/...` — clean.
- `go test -buildvcs=false ./cmd/sciontool/... ./pkg/sciontool/... ./pkg/runtime/...` — all pass except the pre-existing failures above.
- `gofmt -l` on every changed/added file — clean (one unrelated pre-existing
  misalignment at `init.go:2401`, `lchownFn = os.Lchown`, confirmed present
  on `origin/scion/substrate-integration` before this branch and left
  untouched — out of scope for this change; not part of this diff).
- Full `make ci` was not run: `test-fast` is `go test ./...` across the
  whole monorepo (SQLite, GKE/k8s fake clients, GCP metadata, network calls),
  which is slow and has pre-existing, unrelated failures in this sandbox;
  used the brief's explicitly-permitted narrower alternative instead (go vet
  + go test for the three named package trees, plus a full-repo `go build`
  and per-file `gofmt -l`).

## Open items

- None blocking. The `su -` exec-lands-in-$HOME behavior is unchanged by
  design (see README note) — not a gap, a deliberate non-goal per the brief
  amendment.

## Round 2 (sb-dev-cwd-r2): review findings fixed

`reviews/round-cwd-sb-rev-cwd.md` (sb-rev-cwd2) found the fallback broken:
`resolveSubstrateHarnessCwd` read `d.getenv("HOME")`, which under substrate
is substrate-serve's own (root's) `$HOME` — `/root` or unset — never the
scion user's. Since `supervisor.Run`'s `cmd.Dir` chdir happens through
`SysProcAttr.Credential` *after* the setuid/setgid drop
(`syscall/exec_linux.go`: chdir follows the "User and groups" block), a
candidate a root-only `stat` accepts can still be unusable by the dropped
scion uid — `HOME=/root` (mode 0700) made the harness fail to start
(EACCES); `HOME` unset silently left `cmd.Dir` at substrate-serve's own
cwd, `/`, the exact bug the whole fix exists to avoid. A live substrate-lead
amendment mid-task additionally required the final error (when neither
candidate is usable) to name every candidate tried and the uid checked,
nothing else.

**Fixed**, `cmd/sciontool/commands/substrate_serve.go`:

- `resolveSubstrateHarnessCwd` now returns `(string, error)`. It no longer
  reads `$HOME` at all: the fallback is `lookupUser("scion").HomeDir` (a new
  `substrateHarnessCwdDeps.lookupUser` field, wired to `scionUserLookup` the
  same way `privilegeDropPreconditionDeps.lookupUser` is), the same value
  `supervisor.Run` sets as the child's `HOME`.
- Every candidate (workspace, then scion home) is gated by a new
  `dirUsableForScion` helper: it walks `parentDirs(candidate) + candidate`
  and requires each to stat as a directory searchable by the scion uid/gid
  via the existing `canSearchDir` — never trusting a stat this (root)
  process could make on its own. `candidate == "/"` is refused outright,
  independent of its permissions.
- Both candidates unusable is now a hard failure, not a silent `""`
  (inherited cwd) fallback: `substrateServeInitOptions` propagates the
  error, and `newSubstrateServeServer`'s `WithInitRunner` closure logs it
  and returns a new distinct `exitCodeNoUsableHarnessCwd` (18) instead of
  ever invoking `runInit` — flipping healthz to `StateInitFailed` the same
  way any other init failure does. Error text (substrate-lead amendment):
  `substrate: no usable harness working directory for uid <uid>: tried
  "<path>" (<reason>), "<path>" (<reason>)` — quoted paths and reasons only,
  plus the uid; nothing else.
- "Also take" items done in the same change: a non-absolute
  `SCION_WORKSPACE_PATH` is now rejected (`filepath.IsAbs`) and logged
  rather than stat'd relative to substrate-serve's own cwd;
  `supervisor.Run` now sets `PWD=<dir>` in the child env whenever
  `WorkingDir != ""` (scoped to that case only), so a symlinked workspace's
  logical path stays visible to sh/tmux/`process.cwd()`; the
  `resolveSubstrateHarnessCwd` doc comment is trimmed from ~50 lines to
  ~28.

**R2 (data race + env leak)**, `cmd/sciontool/commands/substrate_serve_test.go`:
`TestSubstrateServeBootstrap_ThreadsWorkingDirToInitRunner` wrote
`gotArgv`/`gotOpts` from `handleBootstrap`'s own goroutine and polled them
unsynchronized from the test goroutine (`go test -race` failure), and never
restored `SCION_WORKSPACE_PATH` after `handleBootstrap`'s real `os.Setenv`.
Fixed: the stub now sends an `initCall{argv, opts}` over a buffered channel,
the test `select`s on it with a 2s timeout, and `t.Setenv("SCION_WORKSPACE_PATH",
"")` runs before the request so `t.Cleanup` restores the prior value
regardless of what the handler sets it to.

**Tests added/changed** (`cmd/sciontool/commands/substrate_serve_test.go`,
`cmd/sciontool/commands/init_privilege_drop_test.go`,
`pkg/sciontool/supervisor/supervisor_test.go`): every
`resolveSubstrateHarnessCwd` fixture now supplies a `lookupUser` fake
instead of an ambient `$HOME` (the old `HOME=/root` fixtures were rewritten
to `TestResolveSubstrateHarnessCwd_IgnoresAmbientHOMEWhenRoot`/`WhenUnset`,
which assert the ambient value is never chosen even when it's independently
stat-able); new coverage for workspace-not-searchable-by-scion-uid,
non-absolute rejection, the never-`/` guard (now via an unusable-fallback
error, not a returned string), the both-unusable error's exact contents
(paths + uid), and a `scion` user lookup failure. Three new
`..._EffectiveCwd_...` tests assert the *effective* cwd — spawning a real
child process (`exec.Command`, `cmd.Dir` set) against real, differently-
permissioned temp directories (mode 0 to simulate "unsearchable by anyone",
skipped when running as root since DAC_OVERRIDE would bypass it) — not just
the string the resolver returns, per the brief's explicit requirement. Two
new `supervisor_test.go` tests cover the `PWD` behavior in-scope
(`WorkingDir != ""`) and out-of-scope (`WorkingDir == ""` must not touch an
inherited `PWD`).

**Gates**: `gofmt`, `go vet`, `go build -buildvcs=false ./...` all clean.
`go test -race -count=3 -run ThreadsWorkingDir` and `go test -race` for
`./cmd/sciontool/commands/` and `./pkg/sciontool/supervisor/` are clean
except the same pre-existing, environment-dependent
`TestNativeTelemetryPolicyEffectiveChildEnv/disabled` failure documented in
round 1 (this sandbox has `CLAUDE_CODE_ENABLE_TELEMETRY=1` ambient).
`go test` for `./pkg/runtime/... ./pkg/sciontool/... ./cmd/sciontool/...`
reproduces exactly the same pre-existing `TestGetRuntime*` auto-detection
failures round 1 and the review both already documented (no docker/apple-
container/gcloud binaries in this sandbox) — nothing new.

## Round 3 (sb-dev-cwd-r3): four hardening items closed

Round 2's review was CLEAN with four non-blocking Optionals; all four are
fixed on this round, head `1c9b3b1d`:

1. **Canonicalisation before the `/` guard.** `dirUsableForScion` now
   `filepath.Clean`s `candidate` at the top, before comparing it to `/` and
   before walking its ancestor chain; the cleaned value is what
   `resolveSubstrateHarnessCwd` returns (via a new `chosen` var set by
   `tryCandidate`). `parentDirs` already cleaned its own output, so an
   uncleaned candidate like `/.`, `//`, or `/tmp/..` previously reached the
   guard already reduced to `/` and slipped past it.
2. **Symlink targets.** `substrateHarnessCwdDeps` gained an `evalSymlinks
   func(string) (string, error)` field (wired to `filepath.EvalSymlinks` in
   `defaultSubstrateHarnessCwdDeps`). `dirUsableForScion` resolves the
   (cleaned) candidate with it and, when the result differs, walks the
   resolved path's own ancestor chain too (extracted into a shared
   `dirsSearchable` helper used for both the lexical and resolved chains).
   An `EvalSymlinks` error makes the candidate unusable outright. The
   *candidate*, never the resolved path, is still what gets returned, so
   `PWD`/`cmd.Dir` stay logical.
3. **Exit-18 Hub report.** New `substrateServeReportCwdFailure(d,
   cause)`, called from `newSubstrateServeServer`'s `WithInitRunner` closure
   right before returning `exitCodeNoUsableHarnessCwd`. It resolves the
   scion user's home (falling back to `$HOME` if that lookup also fails)
   and calls the existing `reportInitFailure`, the same helper
   `requirePrivilegeDropOrFail`'s failure path uses in `RunInit` — so the
   Hub learns the agent failed on this path too, not just the actor log and
   healthz's `StateInitFailed`.
4. **Info log.** `substrateServeInitOptions` now logs
   `substrate-serve: harness working directory %q` once per successful
   resolution, so a later chdir failure (which doesn't itself name the
   directory) is diagnosable.

**Tests added** (`cmd/sciontool/commands/substrate_serve_test.go`):
`TestResolveSubstrateHarnessCwd_CanonicalisesNonCanonicalRootSpellings`
(table: `/.`, `//`, `/tmp/..` as `SCION_WORKSPACE_PATH`),
`_CanonicalisesHomeDirRootSpelling` (`/.` as `HomeDir`),
`_ReturnsCanonicalPath`,
`_FallsBackWhenSymlinkTargetParentUnsearchable` (fake `evalSymlinks`
resolving to a target behind a root-only 0700 directory),
`_EffectiveCwd_SymlinkedWorkspaceReallyEnterable` (real symlink, real
`filepath.EvalSymlinks`, proves no regression on the ordinary case),
`TestSubstrateServeReportCwdFailure_WritesPhaseErrorAndMessage` and
`_FallsBackToHOMEWhenScionUserLookupFails` (direct, cheap unit tests of the
new reporter against a temp `agentHome`/`$HOME`), and
`TestSubstrateServeBootstrap_NoUsableHarnessCwd_ReportsInitFailureToLocalState`
(drives a real bootstrap request through `newSubstrateServeServer` to the
exit-18 path and asserts `agent-info.json` gets written with
`PhaseError`).

**Gates**: `gofmt`, `go vet`, `go build -buildvcs=false ./...` all clean
(env scrubbed of `SCION_*`/`CLAUDE_CODE_ENABLE_TELEMETRY` first).
`go test -race` for `./cmd/sciontool/commands/` and
`./pkg/sciontool/supervisor/` both pass clean this round (no leftover
telemetry env in this sandbox). `go test` for `./pkg/runtime/...` and
`./pkg/sciontool/...` both pass clean.

## Round 4 (sb-dev-cwd-r4): symlink-to-root, non-absolute home, and a test-effectiveness gap

Round 3's review found the canonicalisation tests didn't actually exercise
the `filepath.Clean` fix (the fake `stat` matched paths verbatim, so `/.`,
`//`, and `/tmp/..` looked identically "missing" whether or not `Clean` ran),
and that a candidate whose symlink chain resolves to exactly `/` was still
accepted, since `/` is always searchable and so sails through the
resolved-chain walk unguarded.

1. **Fake `stat` now cleans its argument** (`fakeSubstrateHarnessCwdDeps`,
   `cmd/sciontool/commands/substrate_serve_test.go`), matching `os.Stat`'s
   own lexical resolution of `.`/`..`. Verified by temporarily removing
   both `filepath.Clean` calls in production: only the two canonicalisation
   tests and `_ReturnsCanonicalPath` fail; the fix was then restored and the
   suite re-confirmed green.
2. **A resolved target of exactly `/` is now rejected** in
   `dirUsableForScion` (`substrate_serve.go`), right after `EvalSymlinks`,
   with reason `"resolves to /"`. New fake-deps and real-filesystem
   (`os.Symlink("/", link)`) tests cover both the single-candidate fallback
   and the both-candidates-resolve-to-`/` error case. Confirmed by removing
   the guard: all three new tests fail (one accepts the root-resolving
   symlink outright, two produce no error).
3. **The `EvalSymlinks` error path is now covered**: a fake-deps test
   returns the candidate itself alongside a non-nil error, so a mutant that
   stops checking the error (sees `real == candidate`, skips the resolved
   walk, accepts the candidate) is caught — confirmed by applying that
   exact mutation and watching the test fail.
4. **Self-audit finding, fixed**: `scionUser.HomeDir` was never checked for
   being absolute the way `SCION_WORKSPACE_PATH` explicitly is.
   `filepath.Clean("")` is `.`, not `/`, so an empty (or otherwise relative)
   home directory reached the lexical `candidate == "/"` guard as a
   relative path that guard doesn't match, and would return a relative
   `WorkingDir` — one a real chdir resolves against substrate-serve's own
   process cwd, typically `/` for a container's PID 1. Closed with a
   `!filepath.IsAbs(candidate)` guard in `dirUsableForScion`, ahead of the
   literal-`/` check, with a fake-deps test and a real-filesystem test that
   relocates the test process's own cwd to prove the rejection is about the
   candidate being relative, not about the directory being unusable.
   Confirmed by removing the guard: both new tests fail, returning the
   relative candidate with a nil error.
5. **Remaining residual, not fixed**: an intermediate symlink hop (a
   candidate symlinked to `/p/q`, where `q` is itself a symlink to `/r/s`)
   means `/p`'s own permissions are checked by neither the lexical chain
   (ancestors of the candidate) nor the resolved chain (ancestors of the
   fully-resolved `/r/s`) — the kernel traverses `/p` but this resolver
   never looks at it. This can only cause a false accept that fails at
   actual chdir time (`EACCES`), never a landing in `/`, since the fully
   resolved target is what's checked against `/` and its own ancestors. Not
   fixed, matching the round-3 review's own conclusion on this same point.
6. **The no-usable-cwd wiring test now polls `/healthz`** for
   `StateInitFailed` instead of polling for `agent-info.json`'s mere
   existence: the init-runner goroutine sets `initFailed` under the
   server's mutex only after the InitRunner wrapper (which does the
   `agent-info.json` write) returns, so observing that state gives a real
   happens-before edge and proves the goroutine ran to completion — the
   file's existence alone gave neither. `go test -race -count=30` on this
   test (and its siblings) is clean.

**Gates**: `gofmt`, `go vet`, `go build -buildvcs=false ./...` clean (env
scrubbed). `go test -race` for `./cmd/sciontool/commands/` and
`./pkg/sciontool/supervisor/`, and `go test` for `./pkg/runtime/...` and
`./pkg/sciontool/...`, all pass.

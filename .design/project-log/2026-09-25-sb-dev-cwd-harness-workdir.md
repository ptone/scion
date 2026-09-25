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

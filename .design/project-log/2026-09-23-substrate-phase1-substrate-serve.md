# Substrate Phase 1 — `sciontool substrate-serve` control server

Implemented my half (sb-dev-2) of the substrate-integration Phase 1 brief:
`phase1-spec.md` §2.1 (the `substrate-serve` control server), the autoexpose/
port-forward guard from §3, and the `substrate-serve` unit tests from §6.
`pkg/runtime/substrate/` and the `substrate` runtime backend (spec §2.2–2.4)
are sb-dev's half of the same branch; I did not touch `pkg/runtime/` or
`pkg/config/`.

## Changes

### `cmd/sciontool/commands/init.go` (refactor for reuse)
- Renamed `runInit(args []string) int` to exported `RunInit(args []string,
  opts InitRunOptions) int`. Behaviour is unchanged for the `sciontool init`
  CLI command, which now calls `RunInit(args, InitRunOptions{ForwardTermSignal:
  true})` — same default as before.
- New `InitRunOptions.ForwardTermSignal`: when false, `RunInit` skips
  installing the `supervisor.SignalHandler` that reacts to SIGTERM/SIGINT by
  running pre-stop hooks and shutting down the child. This is the seam
  `substrate-serve` needs — it is PID 1, owns SIGTERM handling itself
  (log-only, no forward, no exit; see below), and must not have `RunInit`
  independently also react to the same signal out from under it.
- Guarded the port-forward tunnel manager and the autoexpose reconciler
  (both WebSocket-based) behind `os.Getenv("SCION_RUNTIME") != "substrate"`.
  Substrate's egress is HTTP(S)-only/default-deny (findings.md §0, §3);
  leaving these running would just spin retrying 403s. This is the one item
  I own from §3 ("Autoexpose") — everything else in §3 is out of scope for
  me per the brief.
- No other behavioural change. `extractChildCommand`, hooks, git clone, hub
  reporting, etc. are untouched.

### `cmd/sciontool/commands/substrate_serve.go` (new)
- New `sciontool substrate-serve` subcommand, `--addr` flag (default `:80`,
  overridable for tests). Registered the normal Cobra way
  (`rootCmd.AddCommand` in `init()`), so it ships in the sciontool binary
  automatically — confirmed no `image-build/` change is needed: both
  `image-build/scion-base/Dockerfile` and `cmd/sciontool/main.go` build/run
  the whole `commands` package, not an enumerated command list.
- `runSubstrateServe(addr)`: starts the zombie reaper (substrate-serve is
  PID 1), constructs a `substrate.Server` with an `InitRunner` that closures
  over `RunInit`, and serves `srv.Handler()` on `addr`.
- SIGTERM handling: `signal.Notify` + a goroutine that only logs. Per
  phase1-spec.md §2.1 and findings.md D3, full eviction handling (reacting
  to the worker's 30-minute grace period with a real suspend) is Phase 2;
  Phase 1's contract is just "don't kill the harness on SIGTERM, don't
  exit". Documented as a known limitation in the command's `Long` help text.

### `pkg/sciontool/substrate/` (new package)
- `types.go`: the wire types shared with sb-dev's runtime client —
  `HealthzResponse`, `BootstrapRequest`/`BootstrapFile`, `ExecRequest`/
  `ExecResponse` — field names and shapes exactly as phase1-spec.md §2.1
  specifies (`state`, `env`/`files`/`start_cmd`/`control_token`,
  `argv`/`user`/`timeout_s`, `stdout`/`stderr`/`exit_code`/`truncated`).
- `server.go`: `Server` with `GET /scion/v1/healthz`, `POST
  /scion/v1/bootstrap`, `POST /scion/v1/exec`. `/pty`, `/rehydrate`,
  `/tunnel/open` are not implemented (out of scope, spec §2.1/§3).
  - Bootstrap claims the single-shot slot (409 after) before running any
    side effects, after nonce auth. Writes files via `mkdirAllTracked`
    (only chowns directories it actually created, never a pre-existing
    ancestor), sets env with `os.Setenv`, then runs `sh -c "$start_cmd"`
    through the injected `InitRunner` in a goroutine with
    `forwardTermSignal=false` — the server keeps serving immediately after,
    per spec.
  - Exec requires `Bearer <control_token>` compared with
    `subtle.ConstantTimeCompare`; rejects until bootstrapped (no token to
    compare against yet); validates `user ∈ {scion, root}` and non-empty
    `argv`.
  - No secret values appear in any log or error path: bootstrap file errors
    log only the path and a structural error (bad base64, mkdir/write
    failure), never `content_b64` or decoded bytes; env values are never
    logged.
- `nonce.go`: `NonceVerifier` interface plus two implementations —
  `FirstBootstrapWinsVerifier` (accepts any token; phase1-spec.md §5's
  documented fallback, correctness resting entirely on the single-shot
  bootstrap check plus an external NetworkPolicy restricting router ingress
  to the broker namespace) and `StaticNonceVerifier` (constant-time compare
  against a fixed expected value — a stand-in for the MintActorJWT-derived
  option until that decision lands). `substrate.NewServer`'s default is
  `FirstBootstrapWinsVerifier`; `WithNonceVerifier` swaps it. **I did not
  block on the nonce-source decision** (spec §5 explicitly allows this) —
  whichever way sb-em decides, it plugs in without touching the handler.
- `exec.go` / `execuser.go`: `runExec` shells out via a local
  `execAsUserCmd` helper implementing the same whoami-skip-su wrapper as
  `pkg/runtime.ExecAsUserCmd` (see "Notable finding" below for why this is
  a deliberate copy, not an import), inside its own process group so a
  timeout or the request context ending can actually kill the whole
  tree (see next paragraph). Output is captured through a `cappedWriter`
  that hard-stops at 4 MiB per stream (`maxOutputBytes`) and flags
  `truncated`, independently for stdout/stderr.
- `helpers.go`: `mkdirAllTracked` (returns exactly the directories `os.MkdirAll`
  created, for precise chown) and a `redactErr` seam documenting the
  no-secrets-in-errors invariant for future bootstrap error paths.

## Notable findings during implementation

- **`pkg/sciontool` must never import `pkg/runtime`.**
  `cmd/sciontool/commands/init_test.go`'s `TestInitProjectDataIsolation` is a
  canary asserting `cmd/sciontool/...` never transitively depends on
  `pkg/config` (in-container code must not do project-path resolution).
  `pkg/runtime` (needed for `ExecAsUserCmd`) transitively imports
  `pkg/config` (confirmed via `go list -deps`), so importing it from
  `pkg/sciontool/substrate` would have broken that invariant the moment
  `cmd/sciontool` pulled the substrate package in. Since `pkg/runtime/` is
  sb-dev's territory for this branch (brief: "do not edit those paths"),
  extracting a shared leaf package for the ~10-line wrapper is a follow-up,
  not something to do unilaterally here. I duplicated
  `execAsUserCmd` verbatim into `pkg/sciontool/substrate/execuser.go` with a
  comment explaining why, and a note to keep it in sync if
  `pkg/runtime.ExecAsUserCmd` ever changes. Flagging this as a real
  candidate for a future shared `pkg/execuser`-style package once both
  branches land.
- **`exec.CommandContext`'s default `Cancel` doesn't reach grandchildren.**
  `execAsUserCmd`'s wrapper re-execs into a fresh `sh -c "$cmd"`; `/bin/sh`
  in the build image is `dash`, and dash does **not** tail-call-exec a
  simple last command like `sleep 30` — it forks a real child with a
  different PID. `exec.CommandContext`'s default cancellation only signals
  the top-level tracked PID, so a timeout would have orphaned the actual
  work and let it run to completion regardless. Fixed by giving the command
  its own process group (`Setpgid: true`) and setting `cmd.Cancel` to
  `syscall.Kill(-pid, SIGKILL)`. Caught by `TestRunExec_TimeoutKillsProcess`,
  which failed (30s instead of ~300ms) before this fix.

## Tests (§6, my half)

`pkg/sciontool/substrate/server_test.go`:
- `TestHealthz_InitiallyAwaitingBootstrap`
- `TestBootstrap_BadNonceRejected`, `TestBootstrap_MissingBearerRejected` (auth failures)
- `TestBootstrap_SingleShot_SecondCallGets409` (409 on second, init runner invoked exactly once)
- `TestBootstrap_WritesFilesWithParentDirsAndEnv`, `TestBootstrap_RejectsRelativePath`
- `TestExec_RequiresControlToken`, `TestExec_WrongTokenRejected` (auth failures)
- `TestExec_SucceedsWithCorrectToken`, `TestExec_RejectsUnknownUser`, `TestExec_NonZeroExitCodePropagated`

`pkg/sciontool/substrate/exec_test.go`:
- `TestCappedWriter_*` (under/exact/over limit, split across writes) — unit-level cap logic
- `TestRunExec_OutputCapsAndFlags`, `TestRunExec_StderrCappedIndependently` — real-subprocess
  end-to-end proof of the 4 MiB/stream cap (each generates >4 MiB via `head -c ... /dev/zero`
  and asserts the response is capped at exactly `maxOutputBytes` with `truncated=true`)
- `TestRunExec_TimeoutKillsProcess` — real `sleep 30` killed within its 300ms timeout
- `TestShellQuote_EscapesSingleQuotes`

`cmd/sciontool/commands/substrate_serve_test.go`:
- `TestSubstrateServeCommand_Help`, `TestSubstrateServeCommand_AddrFlagDefault`
- `TestSubstrateServeCommand_Integration_SIGTERMNotForwarded` — gated like the existing
  `TestInitCommand_Integration` (`testing.Short()` and `SCION_INTEGRATION_TEST` must both
  allow it; skipped by default). Builds the real binary, runs `substrate-serve`, bootstraps
  a long-lived `sleep`, sends the subprocess a real `SIGTERM`, and asserts both the control
  server (`/healthz`) and the harness child (checked via `/exec` + `pgrep`) are still alive
  afterward — end-to-end proof of the "SIGTERM not forwarded" requirement, not just a
  unit-level stand-in.

## Gate results

- `go build ./...` — pass.
- `go vet ./...` — pass, no output.
- `go test ./pkg/sciontool/... ./cmd/sciontool/...` — pass, with one **pre-existing,
  unrelated** failure: `TestNativeTelemetryPolicyEffectiveChildEnv/disabled` in
  `pkg/sciontool/supervisor`. Confirmed via `git stash` + rerun against the branch tip
  (`c3b6e82`, before any of my changes) that it fails identically there — not something I
  touched (I never edited the `supervisor` package) and not introduced by this change.
  Leaving it for whoever owns that test; flagging here rather than silently working around it.
- `go test ./pkg/sciontool/substrate/... ./cmd/sciontool/...` in isolation (my new/changed
  packages) — all pass, including the `TestInitProjectDataIsolation` canary.
- `GOGC=40 golangci-lint run --new-from-rev=origin/scion/substrate-integration --concurrency=1
  ./pkg/sciontool/substrate/... ./cmd/sciontool/...` — 0 issues.
- `gofmt -l` on every changed/new file — clean.
- Confirmed `TestSubstrateServeCommand_Integration_SIGTERMNotForwarded` and the two
  `SCION_INTEGRATION_TEST`-gated tests it shares a pattern with are skipped by default (not
  part of the failure above, not part of standard CI unless that env var is set — consistent
  with the existing `TestInitCommand_Integration` convention).

## Out of scope (per brief)

`/pty`, `/rehydrate`, `/tunnel/open`; the `substrate` runtime backend, ateapi client, and
`pkg/config`/`pkg/runtime` changes (sb-dev, spec §2.2–2.4); `deploy/substrate/broker.yaml`
(spec §2.4); the final nonce-source decision (spec §5 — interface is ready, default is the
documented fallback, pending sb-em).

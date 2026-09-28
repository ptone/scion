# Project Log: Substrate Refactor Fan-Out — R10 (egress validators) + R8 (atomic writer / PATH)

**Date:** 2026-09-28
**Component:** `pkg/config` → `pkg/runtime/substrate` (egress validators), `pkg/sciontool/hooks` (`hardenedRootHookEnv`), `pkg/sciontool/dirfd` / `pkg/sciontool/hub` (atomic file writers — investigated, not merged)
**Branch:** `scion/substrate-refactor-moves`, cut from `scion/substrate-refactor@71ad0ec63e7e9a4e7bcf6c0359a720b9404c9a91` (the accepted R1+R5 slice). Not pushed to `scion/substrate-refactor` itself.

This is the R10+R8 fan-out item from `refactor-core-seams.md`, dispatched by sb-em. Two commits landed (R10, and half of R8); the other half of R8 was stopped rather than forced, per the brief's own escape hatch.

## R10 — egress validators moved from `pkg/config` to `pkg/runtime/substrate`

Moved `pkg/config/substrate_egress.go` (`Validate`, `ValidateEgressAllow`, `NormalizeEgressAllowEntry`, `ValidateEgressTrustBundle`, ~500 lines) and its test file into `pkg/runtime/substrate` as free functions, package `substrate`. `V1SubstrateConfig` and its schema block stayed in `pkg/config`, per the brief and the cloudrun-sandbox config precedent.

The only real design decision: `(s *V1SubstrateConfig) Validate() error` had to become a free function, since a method on a type can't move packages without moving the type. Named it `substrate.Validate(sc *config.V1SubstrateConfig) error` — same behavior (including the nil-receiver-returns-nil case), called from `NewSubstrateRuntime` and `Run` (both already in `pkg/runtime`, already importing both `pkg/config` and `pkg/runtime/substrate`), and from `substrateEgressHostnames` via `substrate.NormalizeEgressAllowEntry` instead of `config.NormalizeEgressAllowEntry`.

**Import cycle check (the brief's named risk):** `pkg/runtime` already imports `pkg/config`, so the question was whether `pkg/runtime/substrate` importing `pkg/config` (for the `V1SubstrateConfig` type in `Validate`'s signature) could cycle back. Checked `pkg/config`'s own non-test imports (`pkg/api`, `pkg/ent`, `pkg/ent/integrationconfig`, `pkg/projectcompat`, `pkg/storage`, `pkg/util`, `pkg/util/logging`, `resources`) — none is `pkg/runtime` or `pkg/runtime/substrate`. The one `pkg/config` file that does import `pkg/runtime` (`sandbox_bin_sync_test.go`) is deliberately in `package config_test` (external test package), with a doc comment explaining exactly why — it predates this change and was already solving the same class of problem. `go build -buildvcs=false ./...` and `go vet ./...` both confirm no cycle, full repo.

Guard grep (`grep -n "^func \|^var \|^const " pkg/config/*.go | grep -i egress`, non-test) is empty; `V1SubstrateConfig` confirmed still at `pkg/config/settings_v1.go:1128`. Full test suites for `pkg/runtime/...`, `pkg/runtimebroker/...`, `pkg/config/...` pass. Commit `9c7976d65`.

## R8 — split into a safe half (landed) and a blocked half (stopped, reported)

### Landed: `hardenedRootHookEnv`'s hardcoded PATH → `rootexec.SearchPath`

Mechanical, no behavior change: `rootexec.SearchPath` is byte-identical to the literal it replaced (`strings.Join(rootexec.SearchPath, ":")` == `"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"`). `pkg/sciontool/hooks` already imports `pkg/sciontool/rootexec` (no new import, no cycle risk — `rootexec`'s non-test files import nothing from `hooks`). Commit `b3f741e29`.

### Stopped: merging `hub.WriteFileNoFollowChown` into `dirfd.WriteFileNoFollow`

The brief's own termination clause says to stop rather than force this if the two writers turn out to have genuinely different guarantees. They do, on the one guarantee that actually matters for a caller's observable behavior:

- **`dirfd.WriteFileNoFollow`** (used today by `pkg/sciontool/hooks/handlers/status.go` and `cmd/sciontool/commands/init.go`'s `configureSharedWorkspaceGit`) never inspects the destination leaf before writing. If the leaf is already a symlink or other non-regular file, the final `renameat` just replaces that directory entry outright — the write always succeeds, the symlink is silently destroyed and replaced with a real file. `init.go:2945-2951`'s own doc comment documents this as the intended behavior for installing `.gitconfig`.
- **`hub.WriteFileNoFollowChown`** (used by the GitHub-token and hub-token write paths) calls `dirfd.RefuseSymlinkOrNonRegularAt` on the leaf *before* creating any temp file, and refuses the entire write — leaving the existing symlink completely untouched — if the leaf is already a symlink or non-regular file. This isn't just documented; it's pinned by an existing test, `TestClient_StartTokenRefresh_RefusesSymlinkAtTokenPath` (`pkg/sciontool/hub/client_test.go`), which plants a symlink at the token path and asserts the chown seam never runs and the symlink survives untouched.

Merging in either direction changes observable behavior for real callers:
- Add the refuse-check to `dirfd.WriteFileNoFollow` (keeping hub's stronger guarantee) → `configureSharedWorkspaceGit`'s gitconfig install would start failing outright (logging "Failed to install ... .gitconfig", no gitconfig written at all) whenever `~/.gitconfig` is a pre-existing symlink — a real scenario the design brief's own R9(b) item names explicitly ("a symlinked ~/.gitconfig (dotfile managers)... Decide: follow it... or warn loudly. It must not be silent" — an open, undecided question, out of scope for this fan-out).
- Drop the refuse-check from the merged function (keeping dirfd's current permissiveness) → the hub token-write paths lose a guarantee that's actively tested (`TestClient_StartTokenRefresh_RefusesSymlinkAtTokenPath` would need to be rewritten to accept silent replacement of a planted symlink, which is exactly the regression that test exists to catch).

Neither direction is "no behavior change." The two non-conflicting differences (hub's `f.Sync()` before rename; the temp-file name's entropy source) were left uninvestigated further once this blocker was found — not worth landing piecemeal ahead of the real question, which is a design-authority call: which failure mode does the canonical writer want (refuse-and-leave-untouched vs. atomic-replace-a-symlink), and does that decision get made before or together with R9(b)'s .gitconfig-symlink question.

**Not forced.** Reported to sb-em in the same message as the branch head SHA, with a pointer to this log entry and the report section in `refactor-core-seams-report.md`.

## Gates run

`go build -buildvcs=false ./...`, `go vet ./...`, `gofmt -l .` (repo-wide) all clean. `go test -count=1` green, no pre-existing failures reproduced in this environment, for: `./pkg/runtime/...`, `./pkg/runtimebroker/...`, `./pkg/config/...`, `./pkg/sciontool/...`, `./cmd/...`. `git diff --stat 71ad0ec63..HEAD -- deploy/` is empty.

One environment note for whoever next runs this suite from a Scion agent shell: the standing env-scrub instruction (`env -u SCION_* -u CLAUDE_CODE_* ...`) needs `env $=VARS` in zsh, not `env $VARS` — zsh doesn't word-split an unquoted parameter expansion by default the way bash does, so `env $VARS go test ...` silently no-ops the scrub (received one mangled argument, unset nothing) rather than erroring. Confirmed by reproducing a `CLAUDE_CODE_ENABLE_TELEMETRY` policy-conflict test failure that disappeared once scrubbing was fixed.

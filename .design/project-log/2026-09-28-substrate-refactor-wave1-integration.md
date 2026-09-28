# Project Log: Wave-1 integration (moves + init + R9) onto the substrate-refactor base

**Date:** 2026-09-28
**Candidate branch:** `scion/substrate-refactor-wave1-candidate`
**Base:** `scion/substrate-refactor@71ad0ec63e7e9a4e7bcf6c0359a720b9404c9a91`
**Inputs (exact approved SHAs, each cut directly from the base):**
- moves: `9a90cab37efa3218d96446b95576423aae9efffa` (`scion/substrate-refactor-moves`) — R10 egress-validator move to `pkg/runtime/substrate` + R8-PATH `rootexec.SearchPath`.
- init: `44c558e9bdd37b127820bdb5e101d9ec91aa35f6` (`scion/substrate-refactor-init`) — R3 `DisablePortForwarding` seam + R4 pure same-package move of the privilege-drop probe.
- R9: `9160ef01b0a8ee61869b83e97bf6a3de659ebf56` (`scion/substrate-refactor-r9`) — R9a fd-anchored symlink walk, R9b refuse+WARN gitconfig symlink handling, R9c reExec doc, R8 `LeafPolicy` writer-merge.

This entry records how the three independently-reviewed-and-APPROVED branches were combined and the two reconciliations the integration brief called for. No third-party behavior beyond what the three branches already carry was introduced.

## How the tree was built

All three branches share the identical merge-base (the integration base itself) — none is an ancestor of another. Built by three sequential `git merge --no-edit` calls onto a new branch cut from the base, in the order moves → init → R9:

1. **moves onto base**: fast-forward. `moves` never touches `cmd/sciontool/commands/init.go` or the guard test, so there was nothing to reconcile yet.
2. **init onto (base+moves)**: a real merge commit, but git's `ort` strategy resolved every file automatically — `init`'s `init.go` changes (the R3 seam + R4 move + the new `substrate_privilege_drop.go` file) don't textually overlap anything `moves` touched.
3. **R9 onto (base+moves+init)**: `init.go` itself auto-merged cleanly (R9's `init.go` hunks — the `reExecWithCleanEnv` doc comment and the gitconfig-refusal WARN/`LeafPolicy` block — sit in different regions than `init`'s R3/R4 changes). The only textual conflict in the whole integration was `pkg/sciontool/rootexec/guard_test.go`'s line-keyed `execSiteAllowlist`, both sides having re-keyed it to their own (mutually exclusive) `init.go` line layout. See reconciliation (b) below.

Net shape: 3 merge commits (one fast-forward, two real merges) on top of the shared base, each preserving its input branch's own commit history intact — no squashing, no rebasing of the input branches.

## Reconciliation (a): LeafPolicy writers — R9 is canonical

`moves` deliberately left `dirfd.WriteFileNoFollow` and `hub.WriteFileNoFollowChown` byte-for-byte unchanged (its own project-log entry says so explicitly), pending R9's ruling on the writer-merge. Since `moves` touches neither file, there was no textual conflict to resolve here — R9's versions carried through the merge as-is. Confirmed the tree matches the brief's exact ruling:

- `dirfd.WriteFileNoFollow(path, data, mode, uid, gid, policy LeafPolicy)` — `policy` is a required, no-usable-zero-value enum (`LeafPolicyUnset` / `ReplaceLeaf` / `RefuseSymlink`); `LeafPolicyUnset` is rejected at runtime by `WriteFileNoFollowWithChown`, not just documented as invalid (`pkg/sciontool/dirfd/safeio.go:214-226`).
- `hub.WriteFileNoFollowChown` is a thin `dirfd.RefuseSymlink` wrapper over the shared fd-anchored core (`pkg/sciontool/hub/client.go:1302-1303`).
- Every caller in the merged tree compiles against the new signature: `cmd/sciontool/commands/init.go:2931` (gitconfig install) and `pkg/sciontool/hooks/handlers/status.go:278` (status-file install) both pass `dirfd.ReplaceLeaf`; `hub.WriteFileNoFollowChown`'s own internal call into `dirfd.WriteFileNoFollowWithChown` passes `dirfd.RefuseSymlink`; the token-path writers (`StartTokenRefresh` et al. in `pkg/sciontool/hub/client.go`) stay on `hub.WriteFileNoFollowChown`, i.e. `RefuseSymlink`, unchanged.

## Reconciliation (b): `guard_test.go` `execSiteAllowlist` re-sync

Both `init` and `R9` re-keyed this line-number-keyed allowlist to their own `init.go` layout; `moves`'s `SearchPath` change lives in `pkg/sciontool/hooks/lifecycle.go`, not `init.go`, so it didn't add a third layout to reconcile. After the moves+init+R9 merge, the combined `init.go` has yet another final line layout, so both conflicting sides' line numbers were stale. Resolved by discarding both sides' literal numbers and recomputing every key against the actual post-merge `init.go` (via `grep -n` for each `exec.Command`/`exec.CommandContext` call site, matched back to its original entry by argument shape), then running `TestNoRootContextExecUsesABareUnresolvedCommandName` (and the rest of `./pkg/sciontool/rootexec/...`) to confirm. No justification text was changed; no invariant was loosened — each entry still maps to the same exec site it always did (groupmod/usermod realignment, the `/etc/passwd`,`/etc/group` sed fallback, the git clone/fetch/checkout/config/ls-remote steps). The four entries whose comments/keys were untouched by either branch (`harness.go:157`, `lifecycle.go:309`, `exec_enforced.go:231`, `supervisor.go:136`, `services/manager.go:373`, `substrate/exec.go:84`) were left exactly as they were — `moves`'s `lifecycle.go` edit is 351 lines below `lifecycle.go:309` and doesn't change that file's line count, so that key needed no change.

Final combined line-number keys (`cmd/sciontool/commands/init.go`), replacing both conflicting sides:

- Host-user realignment (groupmod / usermod), unchanged reason text:
  - `init.go:2127`
  - `init.go:2132`
- Direct `/etc/passwd`,`/etc/group` sed fallback, unchanged reason text:
  - `init.go:2234`
  - `init.go:2243`
- `gitCloneWorkspace`'s dropped git clone-path calls (`configureGitCommand` sets `Credential` when uid>0), unchanged reason text, in the same relative order as before (git init → remote add → fetch → checkout → per-config-entry loop → sanitize remote → credential-helper config → branch checkout → branch fetch → branch track → branch create → ls-remote):
  - `init.go:2425`
  - `init.go:2443`
  - `init.go:2458`
  - `init.go:2514`
  - `init.go:2528`
  - `init.go:2539`
  - `init.go:2558`
  - `init.go:2577`
  - `init.go:2585`
  - `init.go:2589`
  - `init.go:2599`
  - `init.go:3064`

All other allowlist entries (`harness.go:157`, `lifecycle.go:309`, `exec_enforced.go:231`, `supervisor.go:136`, `services/manager.go:373`, `substrate/exec.go:84`) are byte-identical to base — not touched by any of the three branches' `execSiteAllowlist` hunks, so nothing to re-key.

`substrate_rootfs_test.go` confirmed byte-identical to base (`git diff base...HEAD -- '**/substrate_rootfs_test.go'` empty).

## Gates

All run against the merged tree with three leaked sandbox env vars scrubbed (`SCION_AUTO_EXPOSE_PORTS`, `CLAUDE_CODE_ENABLE_TELEMETRY`, `SCION_GROVE_ID`/`SCION_PROJECT_ID` — this container's own ambient agent env, not settings the candidate tree writes; see "Environment notes" below):

- `go build -buildvcs=false ./...` — clean.
- `gofmt -l` on every file touched relative to base — empty.
- `go vet ./...` / `make lint` (`go vet -tags no_sqlite ./...`) — clean.
- `make fmt-check` — clean.
- `make check-custom` (compat-literals, annotation-prefix, authz-guards, conversation-upsert-guard, security-marker-gates, authorization-catalog) — all pass.
- `make build` — clean.
- `go test -count=1` on `./cmd/sciontool/commands/...`, `./pkg/sciontool/...`, `./pkg/config/...`, `./pkg/runtime/...` — all `ok`.
- `make ci`'s `test-fast` (`go test -tags no_sqlite -count=1 ./...`, whole repo) — every package this integration touches is `ok`; the only failure anywhere in the repo is `cmd.TestHubAllOrOneActions` (hub CLI auth test, unrelated file, not part of any of the three input branches), confirmed to fail identically on the unmodified base in a detached worktree — pre-existing, not a regression.
- Named tests, individually, all pass: `TestClient_StartTokenRefresh_RefusesSymlinkAtTokenPath` (confirmed unmodified vs. base by diff), the `LeafPolicy` suite including `TestWriteFileNoFollow_InvalidLeafPolicyRejected` (the zero-value case), `TestNoRootContextExecUsesABareUnresolvedCommandName`, `TestReadUnderRootNoFollow_RefusesEscapingSymlinkAtIntermediateComponent`, the full `TestConfigureSharedWorkspaceGit_*` suite, and the privilege-drop precondition suite (`TestNewSubstrateServeServer_PrivilegeDropPreconditionRejectsBootstrap`, `TestSubstrateServeInitOptions_RequiresPrivilegeDrop`, `TestCheckPrivilegeDropFeasible_*`).
- `git diff --stat 71ad0ec63...HEAD -- deploy/` — empty.

### Environment notes

This sandbox's own ambient agent env leaks three vars that the settings/telemetry/runtime-factory tests read directly, causing failures on a byte-identical *base* checkout too (verified in a detached worktree before concluding anything about the candidate tree): `SCION_AUTO_EXPOSE_PORTS=true` (string) trips `pkg/config`'s `auto_expose_ports` map/struct decode; `CLAUDE_CODE_ENABLE_TELEMETRY=1` (this container running under Claude Code itself) trips the supervisor's native-telemetry-policy conflict check; `SCION_GROVE_ID`/`SCION_PROJECT_ID` get adopted by a legacy-env-precedence test that asserts they must not be. All three are pre-existing sandbox leakage, not settings the candidate branch's code writes or reads differently from base — scrubbed for every gate run above, matching this repo's own documented `env -u SCION_PROJECT …` sandbox gotcha.

## Result

`scion/substrate-refactor-wave1-candidate` pushed to `origin`. `scion/substrate-refactor` itself was not touched, per the brief's boundary — that push remains the eng-manager's integration step.

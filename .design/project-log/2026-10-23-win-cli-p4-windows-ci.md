# win-cli Phase 4: full Windows test allowlist (and release zips, held)

**Date**: 2026-10-22/23
**Issue**: ptone/scion#5514 (design `win-cli-design.md` sha256 7cb5d107…, §5.2 "P4 adds", §7, §8 Phase 4; plan AC6, AC1, A11)
**Branch / PR**: `win-cli/p4ci` → `win-cli/integration`, ptone/scion#5784. Release zips: `win-cli/p4zips` (no PR, held).

## Summary

- **Candidates re-derived at 1904c8826.** `GOOS=windows go list` over `./...` found 156 packages with tests. The F§5 marker scan was re-run over each package's Windows test files (unix syscalls, pty, symlinks, mode asserts, Chmod, POSIX path literals, `exec.Command("sh"|"git"…)`, GOOS checks). Result: F§5's 71 marker-free packages, all still present, plus 18 new marker-free packages (for example pkg/brokerclient, pkg/preflight, pkg/testutil, the hack/ tools). That makes 89 candidates.
- **Triage on windows-latest** (a temporary commit, rewritten out of the branch): the 88 buildable candidates ran in one `go test`. The 89th, `deploy/terraform/tools/settingscheck`, is test-only, and `go build` refuses it.
- **Result.** 75 packages are ON as plain lines, `./pkg/artifacts` is ON as an anchored line, and 14 are OFF with a one-line reason each in `.github/windows-test-packages.txt`. Confirming run 39842612067: 87 packages, 5194 tests passed, 0 skips; job 12m58s (build+vet 6m07s, tests 2m47s). The pkg/hub guard and the workflow are unchanged.
- **One test-side fix:** `pkg/util/otlptls`. The load errors quote paths with `%q`, which doubles the backslashes of a Windows path. The test now accepts the raw or the quoted form.
- **Anchors.** `./cmd` and `./pkg/config` already name every `_windows_test.go` test in those packages, so they are unchanged. `./pkg/artifacts` is anchored to the four reference parse/encode tests, because the allowlisted `message --artifact` uses that code.

## Decisions

- **Packages with any skip stay OFF.** That covers the Postgres-only tests, the Chromium probe and `/proc/net/tcp6`. Hiding a skip with `-skip` or a narrower `-run` would break the zero-skip rule in spirit. The exception is `pkg/artifacts`: the anchored tests are exactly the code a Windows command reaches.
- **OFF packages reachable from a windowsAllowed command:** only `pkg/artifacts`, through `message`, now covered. `pkg/wsclient` is linked into scion.exe, but only `attach` uses it, and `attach` is not allowlisted. The remaining linked OFF packages (harnesses, brokerregistration, authzop, pgprovider, testinfra, autoexpose) are reached only from gated server, broker or agent-mode commands.
- **`pkg/integration/runtime` is ON.** Its test binary links pkg/hub as a non-test dependency only, the same as `cmd`. No allowlisted package compiles pkg/hub's own tests.

## Release zips (item 2): held on `win-cli/p4zips`

- Commit 719849cd1 adds windows/amd64 and windows/arm64 to `build-release.yml`, re-derived from miller79/scion#189 (d3a0af31a). The assets are `scion-windows-amd64.zip` and `scion-windows-arm64.zip`, each containing `scion.exe`. linux and darwin are unchanged, and `docs/release-process.md` has the new table rows.
- Commit 475bca882 is a temporary DRY-RUN commit that must never merge. It adds a pull_request trigger, sets the release and update-manifest jobs to `if: false`, and adds a verify job on windows-latest. Run 39826196336 (head 2f840f297, before the branch split) produced:
  - `scion-windows-amd64.zip`: 105594414 bytes, PE machine 0x8664
  - `scion-windows-arm64.zip`: 96564426 bytes, PE machine 0xAA64
  - each zip contains only `scion.exe`
  - `scion.exe version` printed `scion dev (commit de3dac8a)`
  - the release and LATEST.json jobs were skipped
- Nothing was published, and no tag or release was created. The zips are held until the replacement security clearance (A11). The branch will be rebuilt without the DRY-RUN commit.

## Open issues / follow-ups

- `pkg/wsclient`: the pty reconnect tests fail on Windows. A closed socket returns WSAECONNABORTED (wsarecv) rather than the expected error. This matters if `attach` is ever allowlisted on Windows.
- `pkg/preflight`: two profile-check tests fail on Windows (not diagnosed; hub-side).
- Several repo guards (`hack/checktimeliterals`, `pkg/hub/authzop` catalog, `harnesses` vendored-lib, the `pkg/artifacts` fixture) compare unequal on a Windows checkout, probably because of CRLF line endings or a path difference. They are Linux-only guards.
- D4 (unchanged): full `cmd` and `config` suites on Windows.

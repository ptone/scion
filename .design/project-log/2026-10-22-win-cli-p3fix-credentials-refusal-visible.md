# 2026-10-22 win-cli Phase 3 follow-up: make the credentials refusal visible

Agent: win-cli-dev-p3fix (EM win-cli-em). Branch `win-cli/p3fix`, base
`win-cli/integration` 1904c8826.

## Problem
Design row C1r / decision D3: on Windows a credentials.json that other
users can read is refused, with a fix hint. But most callers dropped the
error: `GetAccessToken` returned "", `IsAuthenticated` false, `getAuthInfo`
(hub status) ignored it, and `Store` (login) silently replaced the file,
dropping other hubs' logins. Only logout and `hub remove --logout` showed
the refusal, so design section 7 ("each refusal says how to fix it") was
not met on the common paths.

## Change (pkg/credentials only)
- `notice.go`: `reportRefusal` prints the refusal (which already carries
  the fix hint) to stderr once per process, only for errors wrapping
  `privatefs.ErrNotPrivate`; `warnReplacingRefused` prints the login
  warning. Writer and once-flag are package vars guarded by `mu`.
- `Load` calls `reportRefusal` on a load error. Every read path goes
  through `Load` (GetAccessToken, IsAuthenticated, cmd getAuthInfo), so no
  cmd change was needed. Return values are unchanged; fail-closed is
  unchanged (the file is never read). Unix is unchanged: its load has no
  privacy check, so `ErrNotPrivate` never occurs there.
- `Store` warns before replacing a refused file.

## Decisions
- Print inside the package rather than at each cmd caller: one site, and
  every current and future read path is covered. stdout/JSON stay clean.
- Shell completion: cobra's bash/zsh scripts drop stderr and the
  PowerShell script keeps only the success stream, so the notice does
  not reach completion output.
- Logout over a refused file: the root pre-run (dev-auth banner calls
  IsAuthenticated) prints the notice, then logout fails with the same
  refusal error. That is a small repeat on a command that fails anyway;
  left as is.

## Tests
- Linux/portable (`notice_test.go`): once-only, and only for ErrNotPrivate,
  for both helpers.
- Windows (`store_windows_test.go`, ./pkg/credentials already runs
  unanchored on the Windows job, so no edit to
  `.github/windows-test-packages.txt`): read paths print the refusal once
  for a Users-readable file (control: private file prints nothing, file
  not modified); login over a refused file warns and drops the old logins
  (control: login over a private file warns nothing).

## Verification (dev time, not gate evidence)
`go test ./pkg/credentials` (linux) and `GOOS=windows go vet
./pkg/credentials` passed under heavy-build LIGHT with a private cache.
No ./cmd build (none needed: cmd is unchanged).

## Item 2: section 7.5 audit findings (win-cli-sec75, verdict CLEAR)
- L1 fixed (option A): `pkg/agent/hoststate_windows.go` returns an error
  wrapping `errors.ErrUnsupported` from openHostStateDir/openFile/writeFile;
  `hoststate_other.go` narrowed to `!unix && !windows`. Callers tolerate
  it (startFailedForDisplay fail-quiet; RecordedRunOwner reports unknown
  owner so files are kept; writers return the error to log; all reachable
  only from gated commands). `hoststate_other.go` left the Windows build,
  so its `hack/winsec-unprotected.txt` line was removed; `hoststate.go`
  stays listed (still in the build, still gated). winsec-guard: ok.
  `TestWindowsHostState_Unsupported` anchored on the Windows job.
- I1, I5: comment-only fixes (filelock dependency 1 made exact, numbering
  kept; artifact containment rests on os.Root, not the Lstat pre-check).
- I2: PENDING a sec-review-lead decision (fix like C10/C11, or decline
  as design C5).
- I3 declined: in-container sciontool doctor diagnostic, out of scope.
- I4 declined: lock_other.go matches agentinfolock's best-effort contract
  on every OS; the design 3.2 table wording is the thing that differs.
- Dev-time checks: GOOS=windows vet of pkg/agent and pkg/util/filelock,
  Linux host-state tests, masked GOOS=windows build of ./cmd/scion
  (allowed by the EM), and winsec-guard all passed.

## I2 update: fixed (sec-review-lead decision, option b)
- `atomicProtection` in `pkg/config/atomic_write_windows.go`: in an
  Inherit-mode write, the existing target is opened with
  `Tree.OpenNoFollow`. If `CheckPrivate` passes on that handle the write
  is Private, else Inherit (`protectionForCheck`). A missing target, a
  reparse point at the target (never followed; win-cli-lead ledger 14),
  or an unopenable Tree keep the caller's protection; any other open
  failure is Private (fail closed).
- It applies in `writeFileAtomicMode`, so it covers `writeFileAtomic` and
  also the explicit-0644 `replaceSettingsFileByName` (settings migration).
- The old assertion in `TestWindowsAtomicWrite_PrivateReplacesTooOpenInheritInherits`
  ("writeFileAtomic over a private file inherits") encoded the behaviour
  this fix changes, so it was removed. Its replacement is
  `TestWindowsAtomicWrite_InheritKeepsOwnerOnlyTarget`. The symlink test
  of row C5 is unchanged; `TestWindowsAtomicWrite_InheritDecisionDoesNotFollowLink`
  is new. Both new tests are anchored.

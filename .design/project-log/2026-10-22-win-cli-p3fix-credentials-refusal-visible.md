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

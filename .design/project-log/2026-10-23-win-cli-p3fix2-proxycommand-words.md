# win-cli p3fix2: Windows ProxyCommand word validation; once-only refusal notice

**Date**: 2026-10-23
**Issue**: ptone/scion#5514 (follow-up to Phase 3; security review record)
**Branch / PR**: `win-cli/p3fix2` → `win-cli/integration`

## Summary

- **`cmd/ssh_config_files.go`.** `sshCmdQuote` (the Windows ProxyCommand form) now validates each word against an allowlist and returns an error; it no longer quotes arbitrary words. After backslashes are turned into forward slashes, a word may contain only:
  - ASCII letters and digits and `@+=:,./_-`
  - non-ASCII letters, digits and combining marks (account names in paths)
  - spaces, which double-quote the word
  Anything else is an error naming the character. That includes `%` and every character that sh or the C runtime's command-line parser treats specially.
- **`sshShellQuote` returns `(string, error)`.** The unix form never errors, and its output is unchanged for every input.
- **New `sshProxyCommand` builds the ProxyCommand for all three callers:** ssh-config host blocks, vscode blocks (`renderSSHHostBlockPinned`) and `scion ssh` (`prepareSSH`). Each word's error names the word: executable path, hub endpoint (redacted with `config.RedactURL`), project, agent, known-hosts path or host key alias.
  - ssh-config and vscode render every block before writing anything.
  - `scion ssh` checks before it makes its temporary directory.
- **`sshEndpointURL`, every OS:** an endpoint may contain only ASCII letters, digits and `-._:/`, plus one `[ ]` pair around an IPv6 host.
- **`pkg/credentials`:** `refusalReported` is an `atomic.Bool` set with `CompareAndSwap`, so the notice is once-only without relying on `mu`. Adds a concurrency test.

## Decisions

- **The endpoint restriction applies on every OS.** Hub endpoints are `scheme://host[:port][/path]`, and no test or documented endpoint uses other characters. One validation means one set of accepted endpoints on every OS. On unix the single-quote form already carries any character; this is tightening only.
- **Brackets stay allowed in the endpoint check** for IPv6 hosts (`http://[::1]:9810` appears in tests). On the Windows form such an endpoint is written double-quoted.
- **Non-ASCII letters, digits and marks are accepted** so that account names such as `José` keep working in executable and known-hosts paths. Non-ASCII punctuation (for example a fullwidth quotation mark) is refused.
- **Quoted-word relaxation (EM decision).** `( ) ~ [ ]` are also accepted, and a word containing any of them is double-quoted; inside double quotes they are literal for sh and for the C runtime's parser. That keeps three cases working: `Program Files (x86)` paths, 8.3 `~` paths and IPv6-literal endpoints. Every other character outside the allowlist is still refused, `%` included (ssh expands % tokens in a ProxyCommand). An executable or home path with `&`, `%`, `'`, `!` or `^` is refused with an error naming the character.

- **`cmd/vscode.go` checks arguments for a `.cmd`/`.bat` launcher (EM decision on the sweep item).** On Windows, VS Code's CLI on PATH is normally `bin\code.cmd`, which runs through cmd.exe, and os/exec does not escape arguments for cmd.exe. `checkVSCodeBatchArgs` therefore validates the user data directory, the remote and `--path` against an allowlist before VS Code is started:
  - allowed: ASCII letters, digits, `@+=:,./_-`, backslash, space, and non-ASCII letters, digits and marks
  - anything else is an error naming the argument, with a hint
  A CLI that is not a `.cmd` or `.bat`, and every other OS, is unaffected.
  Discovery is unchanged. `Code.exe` is not a drop-in replacement for `code.cmd`: the wrapper sets ELECTRON_RUN_AS_NODE and runs `resources\app\out\cli.js`.

## Verification

Dev-time only (LIGHT, private cache), so not evidence:
- the masked `./cmd` run of `SSH|VSCode|HubOnly` tests passed
- `go test ./pkg/credentials` passed
- `GOOS=windows go vet ./pkg/credentials` passed
- `TestNoSkipOnSymlinkPaths` and `hack/winsec-guard.sh` passed

The new tests are added to the Windows job's `./cmd` anchor.

## Open issues

- The sweep of other Windows-reachable strings that a shell or CreateProcess interprets went to the EM as a bucket report. One item, the VS Code CLI launch on Windows, is listed there for a decision.

## Review r1 (APPROVE, win-cli-rev-p3fix2-r1): all four items adopted

1. Both allowlists (the ProxyCommand words and the VS Code .cmd arguments) go through a shared `isNonASCIIWordRune`. At r1 it excluded modifier letters (Unicode Lm), and U+02B9, U+02BA, U+02BC and U+02C6 were added to both refusal tests. That alone did not cover every rune the best-fit tables map to punctuation; see review r2.
2. `checkVSCodeBatchArgs` allows `~` (it is literal for cmd.exe), with an 8.3 control (`C:\Users\FIRSTN~1\...`). `%` stays refused.
3. The .cmd refusal hint now lists exactly what the check allows.
4. `TestPrepareSSH_RefusalMakesNoTempDir`: on a refusal, `scion ssh` makes no temporary directory (TMPDIR/TMP/TEMP redirected to an empty directory). Control: a successful prepare makes one, and its cleanup removes it.

Also fixed after the first Windows run: the new tests use a host-absolute scion path. `sshTestExe` is a unix path, which is not absolute on Windows.

## Review r2 (REQUEST CHANGES, one Required): fixed

- **Required 1.** `isNonASCIIWordRune` now also refuses an explicit set, `sshBestFitPunct`. The set is two letters, U+01C0 (to `|`) and U+01C3 (to `!`), and four combining marks: U+0300 (to a backtick), U+0302 (to `^`), U+0303 (to `~`) and U+030E (to `"`). The comment names the source: Microsoft's WindowsBestFit tables (unicode.org MAPPINGS/VENDORS/MICSFT/WindowsBestFit, code pages 874, 932, 936, 949, 950 and 1250-1258). It also states that Lm and these runes are refused because they map to ASCII punctuation.
  - Coverage is exactly what the reviewer's table check covers: every non-ASCII entry in those tables whose target is an ASCII character the allowlists refuse is Lm, Sk, P, S, Zs, or in this set. Entries mapping to `_` (U+0331, U+0332) are harmless and stay accepted.
  - Both refusal tests add the six runes, with the marks after a base letter. Other combining marks stay accepted; the control is `Jose` + U+0301.
- **Nit.** The `checkVSCodeBatchArgs` doc comment is re-wrapped.
- **Optional, adopted.** `TestPrepareSSH_RefusalMakesNoTempDir` asserts that the error names the refused character.

## Windows CI follow-up (test-only)

The success controls of `prepareSSH` in two new tests failed on windows-latest. The runner's TMP directory carries an inheritable CREATOR OWNER entry, which the private-file check correctly refuses for the per-run known-hosts directory. These controls now point TMP/TEMP at a private directory: `privateHome` on Windows (as in `TestSSHPrepare_SpaceInTempWindows`), and `t.TempDir` elsewhere. No product code changed.

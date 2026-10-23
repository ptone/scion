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

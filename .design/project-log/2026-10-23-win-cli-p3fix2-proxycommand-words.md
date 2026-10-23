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
- **Brackets stay allowed in the endpoint check** for IPv6 hosts (`http://[::1]:9810` appears in tests). The Windows word allowlist still refuses `[ ]`, so on Windows an IPv6-literal hub endpoint is refused by ssh-config, vscode and ssh. A host name works.
- **Non-ASCII letters, digits and marks are accepted** so that account names such as `José` keep working in executable and known-hosts paths. Non-ASCII punctuation (for example a fullwidth quotation mark) is refused.
- **Consequences of the strict set on Windows:**
  - an executable under a directory with `(`, `)`, `~`, `%`, `&` or `'` is refused with an error naming the character, for example `Program Files (x86)` or an 8.3 short name with `~`
  - the same applies to a home directory with those characters (the vscode known-hosts path)
  The error tells the user which word and character.

## Verification

Dev-time only (LIGHT, private cache), so not evidence:
- the masked `./cmd` run of `SSH|VSCode|HubOnly` tests passed
- `go test ./pkg/credentials` passed
- `GOOS=windows go vet ./pkg/credentials` passed
- `TestNoSkipOnSymlinkPaths` and `hack/winsec-guard.sh` passed

The new tests are added to the Windows job's `./cmd` anchor.

## Open issues

- The sweep of other Windows-reachable strings that a shell or CreateProcess interprets went to the EM as a bucket report. One item, the VS Code CLI launch on Windows, is listed there for a decision.

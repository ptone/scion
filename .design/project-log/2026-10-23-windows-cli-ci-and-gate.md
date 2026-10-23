# Windows CLI: CI package list, gated-command hardening, follow-ups

**Date:** 2026-10-23

## Summary

The windows-latest job now runs every package whose tests have no
unix-specific test code, the Windows command gate also covers persistent
hooks and shell completion, and a set of small code-quality follow-ups to the
Windows CLI change landed.

## Windows job package list

`.github/windows-test-packages.txt` lists, besides the anchored packages
(`./cmd`, `./pkg/config`, `./pkg/agent`, `./pkg/brokercredentials`,
`./pkg/artifacts`), the packages without unix-specific test code (no
unix-only syscalls, symlinks, mode assertions, Chmod, hard-coded unix paths
or shell execs in the tests the Windows build compiles). Each runs in full
with zero skips; each package left off has a one-line reason in the file.

- Rule stated in the header: never `./pkg/hub` (or a `./pkg/hub/...`
  wildcard) and never a package whose test binary links pkg/hub. `./cmd` is
  the one exception: it links pkg/hub production code only, as on Linux, and
  runs anchored tests only. Check a candidate with
  `GOOS=windows go list -test -deps <pkg>`.
- `pkg/util/otlptls`'s test accepts the `%q`-quoted path in error messages
  (a quoted Windows path doubles its backslashes).
- `pkg/projectfs/migrate` is left off: every file in it is unix-only.

## Gated commands: persistent hooks and completion

- `gateCommands` (cmd/platform_gate.go) also clears `PersistentPreRun(E)` and
  `PersistentPostRun(E)` on every gated command. Cobra runs only the nearest
  persistent pre-run hook, so a hook left on a gated command would replace
  root's, which returns the refusal. An allowed child of a gated parent
  (`project use` under `project`) falls back to root's hook and keeps
  working.
- `completeGated` answers cobra's shell completion request (`__complete`,
  `__completeNoDesc`) for a gated command, or for anything under a gated
  parent, with only the `ShellCompDirectiveNoFileComp` directive line, before
  cobra runs. The command's `ValidArgsFunction` and the completion functions
  registered for its flags therefore never run. `Execute` calls it right
  after the platform restrictions; off Windows nothing is gated, so it never
  answers. The directive line is printed (and nothing else) because without
  it the completion scripts fall back to file completion.
- PowerShell completion and stderr: cobra's PowerShell script (v1.10.2) runs
  the request as `Invoke-Expression -OutVariable out "$RequestComp" 2>&1 |
  Out-Null`, so stderr from `__complete` (for example the credentials
  refusal notice) is discarded, never shown and never parsed. Cobra itself
  writes "Completion ended with directive" to stderr on every request. No
  suppression is needed.

## Follow-ups

- `cmd/ssh_rulei_windows_test.go` is now `cmd/ssh_links_windows_test.go`;
  test and message names describe what they check.
- The free-floating note on `isUnderHubProjectsDir` in pkg/config/templates.go
  is part of `isHubWorkspaceRootWithoutScion`'s doc comment.
- The Windows `isUnderHubProjectsDir` logs at debug level when it cannot
  resolve a path (or the global directory) and therefore skips the project
  layer; the outcome is unchanged.
- `writeVSCodeSettings` had no production caller; its test writes through
  `writeVSCodeSettingsData` directly.
- The unix ssh link tests use one tree snapshot helper.

## Open items

- The Windows job's first run on this list decides the final ON/OFF state of
  the newly added packages; any that fail get a one-line reason and move to
  the left-off list.

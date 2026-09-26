# Project Log: Closing the Remaining Privilege-Drop Gate Seam Gaps

**Date:** 2026-09-26
**Component:** `cmd/sciontool/commands/init.go`, `cmd/sciontool/commands/init_test.go`, `cmd/sciontool/commands/init_privilege_drop_test.go`

See `2026-09-26-substrate-rootfs-privilege-drop-gate-hardening.md` for the fail-closed privilege-drop gate and the seam-defaults test this entry extends.

## What this closes

`harnessSupervisorConfig` builds the `supervisor.Config` the harness's supervised child process runs under, threading `RequirePrivilegeDrop` straight from `InitRunOptions` regardless of the `rootless` argument it also receives. Nothing previously pinned that independence: a regression that accidentally cleared `RequirePrivilegeDrop` whenever `rootless` was true would silently disable the supervisor's own enforcement for a rootless-but-enforced configuration, with every existing test still green. A new test now drives `harnessSupervisorConfig` directly with `RequirePrivilegeDrop: true` and `rootless: true` and asserts both fields land on the resulting `supervisor.Config` untouched.

`runSetupHostUser` and `runDirectSetUID` are two more test-only package-var seams in the same init/setup path — each already had its own doc comment explaining why production code always leaves it at its default and only a test ever replaces it — but neither was covered by the pin proving that default is in fact still the real function it wraps. Both are now added to that check, alongside the six seams it already covered, bringing the total to eight.

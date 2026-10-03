# Substrate PR3 — round-3 code review

Reviewed the final substrate runtime-integration PR (branch
`scion/substrate-pr3-restack-wip`, tip `644293a2c`, based on current main)
as a fresh round-3 code reviewer. Verdict: **APPROVE**. No Critical, High or
Medium findings; three Low polish items were raised.

## What was checked
- **Async-create fallback.** Runtimes that do not call the async launch hooks
  now get a synchronous create. This goes through a new optional runtime
  capability (`AsyncLaunchUnsupportedRuntime` / `HasAsyncLaunchSupport`),
  following the same "missing capability means supported" pattern as attach.
  The broker asks the runtime resolved for the request, not the broker
  default, and no runtime-specific type assertion leaked into broker code.
- **Hub-default passthrough downgrade (#2323).** Unchanged by this PR.
- **Error redaction ordering.** Exec and bootstrap failure text is redacted
  before it is truncated for embedding in an error, and truncation is
  rune-safe, so a secret straddling the cut cannot leave a prefix behind.
- **Egress allowlist.** Tenant-derived hosts can never widen the policy beyond
  the operator's own allowlist. Operator coverage is the primary control; the
  IP-literal rejection is defense-in-depth.
- **Round-2 fixes.** Spot-checked the follow-through on round-2 dispositions,
  including startup refusal scoped to substrate-construction failures only.

## Low items raised (non-blocking)
- The startup-refusal sentinel's docs describe every substrate construction
  failure as an unrecoverable settings problem. Some construction steps can
  fail transiently at boot. Behaviour is acceptable, since a restart retries,
  but the wording should change.
- A small test-only interface lives in broker production code.
- The egress hostnames summary doc omits the operator-coverage gate; the
  inner comment is correct.

## Process notes
- Build, vet, gofmt and scoped golangci-lint are clean. Scoped tests are green
  apart from one supervisor test that fails because the sandbox exports a
  telemetry env var. It fails identically on main, so it is environmental.
- The full hub test suite was not run, per the hub memory-guard rule. The
  PR's hub delta is comments only.

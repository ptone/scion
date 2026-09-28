# Attach capability: test file naming, message wording, and two coverage gaps

**Date:** 2026-09-28
**Branch:** `scion/substrate-refactor-r2` (base `scion/substrate-refactor@c1cafb0b7`, untouched)
**Component:** `cmd/attach.go`, `cmd/attach_capability_gaps_test.go`, `cmd/broker_registration_profile_attach_test.go` (renamed), `pkg/runtime/capabilities_test.go`, `pkg/runtimebroker/attach_resolver_test.go` (renamed)

Trailing, append-only changes on top of the per-profile attach capability
work: no production logic changed except one error-message edge case. All
changes are test/comment text and two file renames.

## File names

Two test files were named after an internal review sequence rather than
what they exercise. Renamed with `git mv` so history follows:

- `cmd/attach_capability_round3_gaps_test.go` → `cmd/broker_registration_profile_attach_test.go`
  (it exercises `registerGlobalProjectAndBroker` / `buildStoreBrokerProfiles`,
  the embedded broker's registration producer, and the CLI's read of what
  that producer persisted).
- `pkg/runtimebroker/attach_capability_round3_gaps_test.go` → `pkg/runtimebroker/attach_resolver_test.go`
  (it exercises the broker-side live-runtime resolver behind `/info`, the
  heartbeat's reported capability, and the control-channel attach
  pre-check).

A third test — covering the case where every configured profile is
filtered out and the registration producer falls back to a synthesized
default profile — arrived as a new, separately named file; it tests the
exact same producer as the first renamed file above, so it was folded
into `cmd/broker_registration_profile_attach_test.go` instead of adding a
fourth file name to track.

Two comments in `cmd/attach_capability_gaps_test.go` described a fix
relative to an internal review step rather than the behavior itself;
reworded to describe what the test proves and why, with no reference to
that step.

## Message wording

`attachUnsupportedErr` (`cmd/attach.go`) built its rejection message as
`"attach is not supported for agents on the %s runtime"`. When the
caller's agent record carries no runtime name at all (an empty string is
a real, documented value along one of its call paths — see the comment
at its `startAgentViaHub` call site), substituting it produced a message
with a doubled space before "runtime". The empty case now gets its own,
grammatically complete message instead of falling through to the
`%s`-templated one.

## Coverage

Two gaps closed, no production behavior changed by either:

- **`SubstrateRuntime.SupportsAttach()` had no direct test of its own.**
  The CLI's earlier `agentRuntime == "substrate"` string check was
  replaced by capability reads throughout this unit (the broker's
  pre-upgrade gate, its control-channel gate, `/info`, heartbeat, and
  registration all ask the same capability); this one method is now the
  sole place substrate's own lack of an attach primitive is declared.
  Nothing previously asserted it directly — only indirectly, through
  callers that also exercise unrelated logic. Added a direct test.
- **The registration producer's "every profile filtered out" fallback had
  no attach-capability coverage.** `buildStoreBrokerProfiles` synthesizes
  a `"default"` profile of the broker's own type when every configured
  profile is filtered out (e.g. a local-only runtime on a non-local
  default); that synthesized profile is backed by the live default
  runtime the same way a normal profile of that type would be, and needed
  a test proving it actually asks that runtime rather than leaving the
  field unset.

## Gates

`env -i`, re-adding only what the toolchain needs — no hand-listed
`SCION_*` subset. `GOCACHE=/scion-volumes/gocache`, not cleaned.

| Gate | Result |
|---|---|
| `go build -buildvcs=false ./...` | OK |
| `gofmt -l` on every touched file | empty |
| `go vet -tags no_sqlite ./...` | OK |
| `go test -count=1 ./cmd/... ./pkg/runtime/... ./pkg/runtimebroker/... ./pkg/store/... ./pkg/hubclient/...` | all ok |
| `golangci-lint run --new-from-rev=c1cafb0b7` | 0 issues |
| `make ci` (fmt-check, lint, check-custom, test-fast `./...`, build) | CI passed |
| grep proof: `pkg/runtime/capabilities.go`, `pkg/runtimebroker/pty_handlers.go`, `cmd/attach.go`, `pkg/runtimebroker/controlchannel.go` | zero matches, all four |

This entry was appended as a trailing commit on `scion/substrate-refactor-r2`;
no existing commit was changed, and the branch was pushed fast-forward.

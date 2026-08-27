# DEF-25: Rename grove-* fixture project IDs in message_deprecation_test.go

**Date:** 2026-08-27
**Agent:** dev-def25-compat
**Branch:** scion/ca-msg-em6

## Summary

Renamed 7 test fixture project IDs in `cmd/message_deprecation_test.go` from
legacy `grove-*` vocabulary to `proj-*` vocabulary, resolving compat-literals
violations for this file.

## Changes

All changes in `cmd/message_deprecation_test.go`:

| Before | After |
|---|---|
| grove-depr-bcast | proj-depr-bcast |
| grove-depr-in | proj-depr-in |
| grove-depr-at | proj-depr-at |
| grove-depr-bcast-works | proj-depr-bcast-works |
| grove-depr-notify-works | proj-depr-notify-works |
| grove-depr-plain-works | proj-depr-plain-works |
| grove-depr-channel-works | proj-depr-channel-works |

## Verification

- `go test ./cmd/ -run TestDeprecation -count=1` — all tests pass
- No `grove` literals remain in `cmd/message_deprecation_test.go`
- These were internal test fixture IDs with no external dependencies

## Notes

The compat-literals check (`hack/check-project-compat-literals.sh`) still reports
violations in `cmd/broadcast_test.go`, which is outside the scope of this task.

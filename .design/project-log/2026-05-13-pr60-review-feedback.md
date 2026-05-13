# PR #60 Review Feedback — set[] Composite Recipient

**Date:** 2026-05-13
**Branch:** scion/fix-issue-25
**PR:** #60

## Summary

Addressed remaining medium-severity review feedback on PR #60 (set[] composite recipient feature).

## Issues Fixed

### Already fixed (commit c1831853)
Three critical issues had already been addressed in a prior commit:
1. **Silent failure in `sendSetMessageViaHub`** — function always returned nil. Fixed to return errors on full/partial delivery failure.
2. **GroupID correlation broken for CLI-originated messages** — `handleAgentMessage` now extracts `group_id` from structured message metadata and populates `storeMsg.GroupID`.
3. **No-op test in `TestSendSetMessageViaHub_RequiresHub`** — replaced always-true assertion with `require.Error(t, err)`.

### Fixed in this session (commit 3dfbf489)
4. **Persist failures not reflected in delivery results (medium)** — In `handleSetMessage`, when `CreateMessage` fails for either agent or user recipients, the error was logged but the recipient result still showed "delivered". Now persist failures mark the recipient as "failed" with the error message and skip further processing (dispatch/publish) for that entry.

## Observations

- Several low-severity/observation items were noted by the reviewer (dead code in `PublishToSet`, missing index on `group_id`, concurrent `fmt.Printf` without synchronization) — these were intentionally left unfixed per the task scope of only addressing critical/medium issues.

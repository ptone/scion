# DEF-11: Pre-resolved ConversationID leaves ExternalRef empty

**Date:** 2026-08-27
**Author:** dev-def11
**Branch:** `scion/ca-msg-em7` (from `scion/messaging-v2`)

## Problem

When the CLI pre-resolves a `ConversationID` on a `StructuredMessage`, the
handler in `handleAgentMessage` (pkg/hub/handlers_agent_messaging.go) constructed
a `ConversationResult` with only `ConversationID` set, leaving `ExternalRef` as
an empty string. This empty ref was then fed into `ComputeDivergenceMatch`, which
compared e.g. `oldRouting="sender-recipient:A:B"` against `actualExternalRef=""`
and produced a false `routing-type-mismatch: old=sender-recipient:A:B new=` error.

The two routing models actually agreed; the comparison was being fed a blank.

## Fix

Two changes in `pkg/hub/handlers_agent_messaging.go`:

1. **Populate ExternalRef from the store:** After building the `ConversationResult`
   for a pre-resolved `ConversationID` (lines 828-832), call
   `s.store.GetConversation(ctx, structuredMsg.ConversationID)` and set
   `convResult.ExternalRef = conv.ExternalRef` on success. This gives
   `ComputeDivergenceMatch` the real database value instead of an empty string.

2. **Fallback handling for lookup failures:** When `ExternalRef` is still empty
   but `ConversationID` is non-empty (pre-resolved but store lookup failed),
   record a fallback via `DivergenceMetrics.IncFallback()` and log a dedicated
   `conv-lookup-failed` entry. This path skips `ComputeDivergenceMatch` entirely
   to avoid the ambiguous `routing-type-mismatch: ... new=` artifact. The
   fallback does NOT increment the mismatch counter, preserving metric accuracy.

## Tests Added

Four new tests in `pkg/hub/handlers_agent_messaging_test.go`, all using the
existing `testServer(t)` / `doRequest()` harness with delta-based assertions on
the process-global `DivergenceMetrics` singleton:

| Test | Verifies |
|------|----------|
| `TestDEF11_PreResolvedConversation_PopulatesExternalRef` | ExternalRef is populated from store; dm-routing-agreement match occurs |
| `TestDEF11_PreResolvedConversation_DivergenceMatch` | Pre-resolved send produces Matches delta >= 1, Mismatches delta == 0 |
| `TestDEF11_PreResolvedConversation_LookupFailure` | Non-existent ConversationID records Fallbacks delta >= 1, Mismatches delta == 0 |
| `TestDEF11_PreResolvedConversation_GenuineDisagreement` | Wrong ExternalRef produces Mismatches delta >= 1 (comparison is active) |

**Mutation proof:** Removing the `GetConversation` call causes
`TestDEF11_PreResolvedConversation_DivergenceMatch` to fail because ExternalRef
reverts to empty, producing `routing-type-mismatch` instead of
`dm-routing-agreement`.

## Files Changed

- `pkg/hub/handlers_agent_messaging.go` -- the fix + fallback handling
- `pkg/hub/handlers_agent_messaging_test.go` -- four new tests

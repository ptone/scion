# Tranche C File Classification — origin/scion/messaging-v2

**Produced by:** ca-msg-em10  
**Date:** 2026-08-28  
**Merge base:** 6268bac44 (2026-08-27 11:05)  
**V2 branch:** origin/scion/messaging-v2  
**Main tip at measurement:** upstream/main (3c7e14e41)  
**Post-fork main commits touching these files:** 18

## Classification Table

| # | File | Class | What v2 changed (from merge-base) | Post-fork main commits | Re-derivation cost | Confidence |
|---|------|-------|-----------------------------------|----------------------|-------------------|------------|
| 1 | `pkg/hub/handlers_agent_messaging.go` | **SUBSTANTIVE** | Adds ValidateLegacyMessage validation at 3 choke points; Phase 5 dual-write with DeriveConversationKey + ResolveOrCreateConversationByKey (unified API); divergence monitoring; DEF-11 pre-resolved ConversationID handling with store lookup + fallback; Phase 11 surface resolution via UpsertConversationByExternalRef; CheckConversationConsistency; Surface/ExternalRef/ParentRef fields on MessageRequest. 274 lines. | #1353, #1347, #1343, #1322 | Medium. ~80 lines unique (ValidateLegacyMessage, DEF-11, Phase 11 surface, CheckConversationConsistency). Conversation resolution + divergence at same sites already on main (different API surface). | HIGH |
| 2 | `pkg/hub/handlers_agent_messaging_test.go` | **SUBSTANTIVE** | Adds DEF-11 regression suite (3 tests: PreResolvedConversation, DivergenceMatch, LookupFailure); DEF-19 group recipient handler test; divergence detection test; changes TestOutboundMessage_UnknownType to expect 400 (from ValidateLegacyMessage). ~400 lines of new test logic. | #1343 | Medium-hard. ~350 lines of test logic encoding DEF-11/DEF-19 acceptance criteria with divergence metric assertions. Main has none of this. | HIGH |
| 3 | `pkg/hub/handlers_broker_inbound.go` | **SUBSTANTIVE** | Adds Phase 11 fields (Surface, ExternalRef, ParentRef) to inboundMessageRequest; ValidateLegacyMessage choke point; Phase 11 broker edge resolution (surface + external_ref → UpsertConversationByExternalRef); Phase 5 dual-write with divergence logging (ComputeDivergenceMatch, LogDivergence); CheckConversationConsistency. 91 lines. | #1353, #1322 | Medium. ~70 lines unique (Phase 11 surface resolution, validation, divergence, consistency check). Main has only basic conversation resolution at same site, NO divergence logging. | HIGH |
| 4 | `pkg/hub/handlers_chat_v2.go` | **SUBSTANTIVE** | Adds Phase 8 read-switch in handleConversationHistory (ConversationReadSwitch ON → resolve conversation, query by ConversationID with DM key parsing, thread topic lookup, fallback for pre-dual-write data); ValidateLegacyMessage in sendAgentRouted; attachment-only message synthetic body workaround. 54 lines. | #1353, #1347, #1338, #1322 | Hard. ~50 lines of Phase 8 read-switch logic — novel design (conversation-first reads with graceful fallback). Main has no read-switch at all. | HIGH |
| 5 | `pkg/hub/messagebroker.go` | **STALE** | Adds Phase 5 dual-write conversation resolution + divergence logging in deliverToUser and deliverToAgent (75 lines). Uses ResolveOrCreateThreadConversation, ResolveOrCreateDMConversation, ComputeDivergenceMatch, LogDivergence, CheckConversationConsistency. | #1353, #1343 | Trivial residual. ~4 lines (CheckConversationConsistency calls at 2 sites). Conversation resolution + divergence already on main via tranche B; v2's version also lacks B5 security fix (authenticatedSender) and the persistOK→early-return defect fix. | HIGH |
| 6 | `pkg/hub/messagebroker_test.go` | **MECHANICAL** | Updated `newTestStore(":memory:")` to `newTestStore(t, ":memory:")` to match v2's changed test helper signature. 1 line. | #1353, #1343 | Trivial. 1 line, only needed if newTestStore signature change is carried. | HIGH |
| 7 | `pkg/hub/server.go` | **MECHANICAL** | Added 2 route registrations: `/api/v1/admin/messaging/divergence` (guarded) and `/api/v1/conversations/resolve`. 4 lines. | #1348, #1346, #1336, #1328, #1323 | Trivial. 3 lines of HandleFunc boilerplate. | HIGH |
| 8 | `pkg/hub/route_metadata.go` | **MECHANICAL** | Added 2 route metadata entries: `conversations.resolve` (RouteAuthenticated) and `admin.messagingDivergence` (RouteHubAdmin). 8 lines. | #1348, #1346, #1336, #1334, #1333, #1332, #1329, #1327, #1324 | Trivial. 8 lines of formulaic struct literals. | HIGH |
| 9 | `pkg/hub/route_classification_test.go` | **MECHANICAL** | Added 2 entries to routePermissionClassifications map for the new routes. 2 lines. | #1348, #1346, #1336, #1334, #1332, #1327 | Trivial. 2 lines. | HIGH |
| 10 | `pkg/hub/admin_maintenance_test.go` | **MECHANICAL** | Updated `newTestStore(":memory:")` to `newTestStore(t, ":memory:")`. 1 line. | #1332 | Trivial. 1 line, only needed if newTestStore signature change is carried. | HIGH |
| 11 | `pkg/hub/chat_notifications_test.go` | **MECHANICAL** | Updated `newTestStore` call + removed explicit `t.Cleanup`. 2 lines. | #1343 | Trivial. 2 lines. | HIGH |
| 12 | `pkg/messages/types.go` | **STALE** | Added `IsValidChannel()` helper and `ConversationID` field to StructuredMessage. 10 lines. | #1331 | Zero. Main #1331 has byte-identical changes. | HIGH |
| 13 | `pkg/store/models.go` | **STALE** | Added `ConversationID` to Message and MessageFilter; added Conversation, ConversationParticipant, MessageAddressee, ConversationFilter model types. 64 lines. | #1331, #1324, #1323 | Zero. Main #1331 has identical content. | HIGH |
| 14 | `pkg/store/store.go` | **STALE** | Embedded ConversationStore in Store interface; added SetMessageConversationID and CountUnbackfilledMessages to MessageStore; defined full ConversationStore interface (17 methods). 74 lines. | #1349, #1348, #1331, #1323 | Trivial residual. ~3 lines (CountUnbackfilledMessages interface declaration, not on main). | HIGH |
| 15 | `pkg/store/entadapter/composite.go` | **STALE** | Added ConversationStore field + initialization in CompositeStore. 2 lines. | #1331, #1323 | Zero. Main #1331 has identical lines. | HIGH |
| 16 | `pkg/store/entadapter/message_store.go` | **STALE** | Added ConversationID handling in entMessageToStore, CreateMessage, ListMessages; added SetMessageConversationID and CountUnbackfilledMessages methods. 63 lines. | #1331 | Easy residual. ~18 lines (CountUnbackfilledMessages implementation — standard ent query). | HIGH |
| 17 | `pkg/hub/handlers_policies_test.go` | **STALE** | Updated newTestStore call + removed t.Cleanup. 2 lines. | #1334 | Zero. Main #1334 completely rewrote newPolicyTestServer to use testServer(t); the function v2 patched no longer exists. | HIGH |
| 18 | `pkg/hub/skill_registry_handlers_test.go` | **STALE** | Updated newTestStore call + removed t.Cleanup. 2 lines. | #1334 | Zero. Main #1334 completely rewrote newRegistryTestServer; the function v2 patched no longer exists. | HIGH |

## 19th File — Correction

The architect listed 19 files but the set above contains only 18 distinct code files. I verified the list against both the three-dot diff and the post-fork main overlap. No 19th code file was found in the risk set beyond the 18 above. (go.sum and docs-site files were explicitly excluded.)

## Summary

**SUBSTANTIVE count: 4**

Four of the 18 files contain design decisions that require understanding the problem to re-derive:

1. **handlers_agent_messaging.go** — ValidateLegacyMessage integration, DEF-11 pre-resolved ConversationID handling, Phase 11 surface resolution via UpsertConversationByExternalRef. Conversation resolution + divergence are already on main (different API), but ~80 lines of unique logic remain.
2. **handlers_agent_messaging_test.go** — DEF-11 regression suite, DEF-19 handler test, divergence metric assertions. ~350 lines, none on main.
3. **handlers_broker_inbound.go** — Phase 11 broker edge resolution, ValidateLegacyMessage, divergence logging + consistency checks. ~70 lines unique; main has only basic conversation resolution at same site.
4. **handlers_chat_v2.go** — Phase 8 read-switch (conversation-first reads with graceful DM/thread detection and pre-dual-write fallback). ~50 lines, entirely novel, nothing like it on main.

**MECHANICAL count: 6** — Route registration (3 files), newTestStore signature updates (3 files). All trivial bookkeeping.

**STALE count: 8** — Store models/interface/implementation (5 files), superseded test patches (2 files), and messagebroker.go where conversation resolution + divergence already landed via tranche B.

**Re-derivation estimate:** The 4 SUBSTANTIVE files contain ~550 unique lines total. If re-derived from intent on current main, ~80 + ~350 + ~70 + ~50 = ~550 lines of design-aware implementation. The 6 MECHANICAL files add ~15 lines of boilerplate. The 8 STALE files add 0–21 lines of trivial residual (CountUnbackfilledMessages). Total re-derivation: ~580 lines.

**Risk note:** handlers_broker_inbound.go and messagebroker.go are confirmed to contain hunks that would revert main security fixes (B5 in messagebroker.go via #1343, #1322 validation in handlers_broker_inbound.go). A rebase path must preserve those fixes. The re-derivation path inherits them from main automatically.

## Positive Control

To verify the instrument correctly detects SUBSTANTIVE changes, I ran the same classification against `pkg/hub/handlers_chat_v2.go` (expected SUBSTANTIVE) and `pkg/hub/admin_maintenance_test.go` (expected MECHANICAL):

- **handlers_chat_v2.go**: Three-dot diff shows Phase 8 read-switch (ConversationReadSwitch, ResolveDMConversationForRead, ResolveThreadConversationForRead, DivergenceMetrics.IncFallback), ValidateLegacyMessage validation, attachment workaround — 54 lines of design logic not present on main. → **SUBSTANTIVE** ✓
- **admin_maintenance_test.go**: Three-dot diff shows exactly 1 changed line: `newTestStore(":memory:")` → `newTestStore(t, ":memory:")`. No design decisions. → **MECHANICAL** ✓

The instrument distinguishes SUBSTANTIVE from MECHANICAL correctly.

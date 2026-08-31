# Defect Ledger — ca-msg-arch

Extracted from `IMPLEMENTATION-STATE.md` by `ca-msg-ledger`, 2026-08-31.
Cross-checked and reconciled by `ca-msg-arch`, 2026-08-31.

**This file is the tracker. The journal is not.** The journal is append-only
narrative and records a defect's status at the moment of writing; only this
table records it now. When a defect changes state, edit the row here in the
same commit as the journal entry that explains why.

Three rows were already stale when extraction finished, because they changed
while it ran (DEF-12, DEF-83, and the two new ids DEF-89/DEF-90 that did not
exist when the sweep started). That is the argument for maintaining this file
rather than re-deriving it.

## Ledger

| ID | Summary | Status | Last mention | Anchor |
|---|---|---|---|---|
| DEF-1 | Participant-level auth on `conv:<id>` — implemented in S4 handler | CLOSED | 5484 | 5441 |
| DEF-2 | Cross-project addressee validation — conditionally closed, wired by C5 [^1] | CLOSED | 16251 | 5442 |
| DEF-3 | Divergence gate independent source of truth — `CheckConversationConsistency` added | CLOSED | 21725 | 5458 |
| DEF-4 | `pkg/hub` test suite OOM — fixed at `b92926dd` | CLOSED | 5462 | 5460 |
| DEF-5 | `conv:<id>` and `#<thread>` have no CLI delivery policy; routing unbuilt | OPEN | 25223 | 5443 |
| DEF-6 | Scheduled sends cannot address a conversation — specced §2.14, not built | OPEN | 18610 | 5444 |
| DEF-7 | `#<thread>` form can never resolve — design answered, build pending §2.6.3 | OPEN | 5447 | 5447 |
| DEF-8 | Agent DMs as two disjoint rows — partial fix in Tranche A, reconciliation unspecced [^2] | OPEN | 21544 | 5448 |
| DEF-9 | Addressee mechanism unwired — `message_addressees` table never written in production | OPEN | 18610 | 5449 |
| DEF-10 | `@<agent>` DMs project-scoped contradicting Q2 — half-struck, remainder bounded | OPEN | 18610 | 5450 |
| DEF-11 | Divergence board CLI mismatch — artifact struck after re-derivation | CLOSED | 18760 | 5445 |
| DEF-12 | Backfill entry point unreachable — PR #1426 merged to main at `66b5cab7f` | CLOSED | 25177 | 5446 |
| DEF-13 | Conversation-reference forms documented — merged at `edd4e4bd` | CLOSED | 5457 | 5451 |
| DEF-14 | DM key membership check — struck, mechanism replaced | CLOSED | 16714 | 5452 |
| DEF-15 | `dm:`-prefixed ThreadID third DM shape — resolved via §2.15 phase 4 at `14b3ba7c` | CLOSED | 5454 | 5453 |
| DEF-16 | Dual-write validation ordering — both handlers validate-before-write on main | CLOSED | 16714 | 5454 |
| DEF-17 | Integration branch CI gate failures — struck, addressed via tranche workflow | CLOSED | 16714 | 5457 |
| DEF-18 | AC-33 cross-project refusal cannot name refused agents — collected but never read | OPEN | 18610 | 5455 |
| DEF-19 | Phase 7 validation `group[]` breakage — fixed, merged in Tranche C | CLOSED | 11366 | 5456 |
| DEF-20 | Phase 4 topic-lookup guard — fixed and verified, Tranche C merged | CLOSED | 15303 | 3927 |
| DEF-21 | Error/absence conflation in topic lookup — fixed, Tranche C merged | CLOSED | 3964 | 3934 |
| DEF-22 | Dangling `conversation_id` — fixed, Tranche C merged | CLOSED | 3947 | 3947 |
| DEF-23 | Two vacuous ACs on topic-scoped sends — fixed, Tranche C merged | CLOSED | 3954 | 1948 |
| DEF-24 | WITHDRAWN — not a defect; stale cut point already fixed on staging [^3] | CLOSED | 1951 | 1951 |
| DEF-25 | Compat-literal test fixtures — fixed, gate re-verified EXIT=0 | CLOSED | 24268 | 841 |
| DEF-26 | Green placeholder test renamed — PR #1340 merged to main at `7c5a64ae9` | CLOSED | 16714 | 792 |
| DEF-27 | Soft-deleted topic shadow conversation — fixed at `25fad0a2`, Tranche C merged | CLOSED | 13409 | 921 |
| DEF-28 | `UpsertConversationByExternalRef` parent_ref erasure — guarded at `f57e07b6` | CLOSED | 5725 | 1041 |
| DEF-29 | `CreateConversation` keyless direct guard — landed, Tranche A merged | CLOSED | 26935 | 1492 |
| DEF-30 | Stored DM keys format mismatch — closed, no migration needed, no exposure | CLOSED | 5702 | 1941 |
| DEF-31 | Topic `default_agent` cross-project routing — PR #1338 merged at `310126977` | CLOSED | 16714 | 1318 |
| DEF-32 | Federated principals unattributable — no `(issuer,subject) → user_id` link table | OPEN | 25476 | 3117 |
| DEF-33 | `hasAgentReplyAfter` casing mismatch fail-open — concrete fix exists but held | OPEN | 18610 | 12389 |
| DEF-34 | `hasAgentReplyAfter` `Channel:"web"` filter — blocked on external #1259 | OPEN | 25224 | 12547 |
| DEF-35 | Pagination constant coupling — latent, folded into DEF-34 pass | OPEN | 13863 | 13863 |
| DEF-36 | Topic mints never populate listing index — landed with #1380 | CLOSED | 26860 | 14857 |
| DEF-37 | Validation-exemption marker gate — C5 merged, PR #1410 merged | CLOSED | 21074 | 2898 |
| DEF-38 | Marker gate stale expectations for `authorizeAgentMessage` — PR #1376 merged | CLOSED | 16714 | 15505 |
| DEF-39 | Fail-open guard in C1 exemption — DEF-39b verified closed, #1379 merged | CLOSED | 16714 | 15527 |
| DEF-40 | Order-dependent cross-project boundary hole — #1379 merged, explicit bool fix | CLOSED | 16714 | 15764 |
| DEF-41 | `validate_compat.go` fabricated `ConversationID` — #1401 landed | CLOSED | 19638 | 2821 |
| DEF-42 | `webChatStore` race condition — PR #1405 merged to main | CLOSED | 20221 | 2899 |
| DEF-43 | Cloud-logs test ordering — verified green on main after #1399 | CLOSED | 18605 | 2965 |
| DEF-44 | Stale `go.sum` in extras — cleared, C7 merged | CLOSED | 18606 | 17549 |
| DEF-45 | Chat-app identity compile error — cleared | CLOSED | 18606 | 17646 |
| DEF-46 | 4 `send_test.go` tests skip in CI — missing `/scion-volumes/` root | OPEN | 18611 | 18409 |
| DEF-47 | Production `/home/scion` fallback — behaviour change question | OPEN | 18611 | 18444 |
| DEF-48 | Validation-after-resolve orphan row on `@agent` path — #1402 landed | CLOSED | 20118 | 2854 |
| DEF-49 | Caller-supplied `conversation_id` membership check — #1403 merged | CLOSED | 19841 | 3097 |
| DEF-50 | Reachability gate comment-blind grep — fixed, PR #1410 merged | CLOSED | 22118 | 3097 |
| DEF-51 | Orphan row on `@email` path — PR #1407 merged | CLOSED | 20165 | 19244 |
| DEF-52 | CI `-race` flag missing — nightly race job, #1408 merged | CLOSED | 21251 | 19362 |
| DEF-53 | B10 guard skips failure case — Tranche G precondition | OPEN | 23773 | 19755 |
| DEF-54 | Server struct `channelRegistry`/`pluginManager` race — PR #1409 merged | CLOSED | 22073 | 19884 |
| DEF-55 | Orphan row via timeout between resolve and send — filed, not staffed | OPEN | 22073 | 20106 |
| DEF-56 | Reachability gate wired to CI — PR #1410 merged | CLOSED | 21453 | 20484 |
| DEF-57 | Broker inbound auth bypass — rescoped to cleanup, PR #1411 merged | CLOSED | 22119 | 20765 |
| DEF-58 | Negative gate needed: `brokerIdentityImpl` vs `UserIdentity`/`AgentIdentity` | OPEN | 26001 | 21316 |
| DEF-59 | `GetAgent` can't resolve slugs — tests pinned, fix not implemented | OPEN | 22654 | 22075 |
| DEF-60 | DEF-57 cleanup removed wrong explanation without leaving right one | FILED-NOT-STAFFED | 22096 | 22096 |
| DEF-61 | `filter.AgentID` stays raw after resolution — trap for DEF-59 fixer | FILED-NOT-STAFFED | 22655 | 22500 |
| DEF-62 | `parseUUID` inconsistency in same filter struct | FILED-NOT-STAFFED | 22655 | 22517 |
| DEF-63 | Divergence metric asymmetry — closed for `handleMessages` | CLOSED | 22623 | 22534 |
| DEF-64 | Read-switch narrows manager view — Tranche G blocker, pinned not fixed | OPEN | 25608 | 22733 |
| DEF-65 | Explicit `thread_id` silently becomes DM-scoped for project-less agents | FILED-NOT-STAFFED | 22890 | 22890 |
| DEF-66 | Consistency checker conflates routing disagreement with consistency failure | FILED-NOT-STAFFED | 23305 | 23091 |
| DEF-67 | S1 nil `webChatStore` short-circuits — `IncFallback` never called | FILED-NOT-STAFFED | 23842 | 23300 |
| DEF-68 | `divergenceCaveats` is runtime-mutable package-level var | FILED-NOT-STAFFED | 23843 | 23319 |
| DEF-69 | Caveat test drops non-string values | FILED-NOT-STAFFED | 23843 | 23332 |
| DEF-70 | PR (B) tests bypass mux — `guarded()` RouteHubAdmin check never exercised | OPEN | 23844 | 23344 |
| DEF-71 | Missing `canManage` positive control — verified closed | CLOSED | 23845 | 23734 |
| DEF-72 | Dual-outcome assertion on 5-part DM key parse cannot fail | FILED-NOT-STAFFED | 23845 | 23845 |
| DEF-73 | `IncFallback` false positives — divergence counter unreliable | FILED-NOT-STAFFED | 23849 | 23847 |
| DEF-74 | DEF-64 pins exercise admin-bypass only — no membership coverage | FILED-NOT-STAFFED | 24236 | 23907 |
| DEF-75 | Delta assertion accepts 0 and 1 regardless of response code | FILED-NOT-STAFFED | 24103 | 24067 |
| DEF-76 | S2 read-switch silently drops messages outside DM model | FILED-NOT-STAFFED | 24197 | 24106 |
| DEF-77 | Nine fragile `delta==0` assertions — switch-ON indistinguishable from OFF | FILED-NOT-STAFFED | 24737 | 24156 |
| DEF-78 | `handleAgentMessagesStream` has no read-switch block — REST/SSE paths diverge | FILED-NOT-STAFFED | 24177 | 24175 |
| DEF-79 | Characterisation test pins `found==false` — narrowing not proven sole cause | OPEN | 26001 | 24208 |
| DEF-80 | Divergence board caveat omits unbackfilled-history fail-open path | OPEN | 25503 | 24281 |
| DEF-81 | Backfill resume timestamp exclusion — PR #1426 merged | CLOSED | 25493 | 24501 |
| DEF-82 | No multi-project backfill coverage | FILED-NOT-STAFFED | 24707 | 24707 |
| DEF-83 | `Init()` fails on pre-existing DB — PR #1437 merged to main [^4] | CLOSED | 28402 | 26239 |
| DEF-84 | `maintenance_executors.go` relies on fetch-all-refs | FILED-NOT-STAFFED | 26654 | 26471 |
| DEF-85 | `RebuildServerExecutor` overwrites live binary with no stash/verify | FILED-NOT-STAFFED | 27540 | 27096 |
| DEF-86 | Rebuild control served by process it restarts — design limitation | FILED-NOT-STAFFED | 27457 | 27100 |
| DEF-87 | `github_pat_` credential in contrib-repo origin URL | FILED-NOT-STAFFED | 27285 | 27285 |
| DEF-88 | Pre-deploy snapshot incompatible with deploy mechanism — resolved via manual SSH | CLOSED | 27531 | 27429 |
| DEF-89 | New webchat topics are created with no `conversation_id`; backfill decays [^5] | FILED-NOT-STAFFED | 28560 | 28560 |
| DEF-90 | scion-gteam port 22 internet-exposed; continuous external brute-force [^6] | OPEN | 28640 | 28640 |

## Counts by status

| Status | Count |
|---|---|
| CLOSED | 47 |
| OPEN | 22 |
| FILED-NOT-STAFFED | 21 |
| DEFERRED | 0 |
| UNKNOWN | 0 |
| **Total** | **90** |

## DEF-NN ids mentioned but never defined

None. All 90 ids (DEF-1 through DEF-90) have definitions in the journal.

## Notes and edge cases

[^1]: **DEF-2** (CLOSED): The journal says "CLOSED — but conditionally" (line 5442). The condition was that C5 wire the production caller for cross-project addressee validation. C5 was part of Tranche C, which merged. A later mention at line 16251 says "C5 is the FIRST production caller. Until it wires the call, AC-33/DEF-2 is unenforced in production." This was written before C5 completed; C5 subsequently merged as part of Tranche C. Classified CLOSED based on the journal's explicit marking and the merge of the conditioning tranche, but the conditional language warrants attention.

[^2]: **DEF-8** (OPEN): An extraction agent found Tranche A carrying "DEF-8 x4" commits that merged. However, the deferred-item ledger at line 5448 says "Gates the beta" and requires "spec reconciliation." The four Tranche A commits improved the resolver (e.g., populating ExternalRef) but the core issue — agent DMs exist as two disjoint rows requiring reconciliation — is not resolved. The reconciliation is unspecced. Classified OPEN because the structural defect persists.

[^3]: **DEF-24** (CLOSED): The journal says "WITHDRAWN — not a defect." WITHDRAWN is not in the allowed status vocabulary, so this is mapped to CLOSED. The stale cut point referenced was already fixed on the staging branch.

[^4]: **DEF-83** (CLOSED — reclassified by ca-msg-arch after extraction): The extraction correctly read OPEN from the journal as it stood: the fix was on `tranche-g` and deployed to the test VM, with `def83-main` awaiting merge. ptone merged that work as PR #1437 while extraction was in flight (journal §5jf, line 28402), and the merge was verified rather than assumed — the deleted `CREATE UNIQUE INDEX` is still created by the migration on both drivers, so fresh DBs keep the index. Now closed on main.

[^5]: **DEF-89** (FILED-NOT-STAFFED, filed after extraction began): `handlers_chat_v2.go:460-473` builds `WebChatTopic` with no `ConversationID`, so `CreateTopic`'s atomic dual-write branch — which is correct, transactional and tested — is unreachable from its only caller. Every new topic is therefore created with no conversation row. The consequence is that the one-time backfill is a decaying artifact: coverage falls as new topics accumulate, and flipping the read switch would 409 exactly the newest, most-used threads. Scoping decision (whether new topics get a conversation unconditionally) is with ptone.

[^6]: **DEF-90** (OPEN, filed after extraction began): scion-gteam has port 22 open to `0.0.0.0/0` with no fail2ban, no iptables rule and no GCP firewall restriction. Observed: 36 concurrent inbound SSH connections from external scanning ranges, saturating sshd's default `MaxStartups 10:30:100` and causing the intermittent connection failures that have been slowing every VM operation. Reachability is established by the inbound traffic itself, not by an outbound probe. Fix in progress with ptone: a tag-scoped allow/deny rule pair, where adding the tag is the switch and removing it is the rollback.

### Additional observations

- **DEF-55** (OPEN): The journal records it as "Filed, not staffed" at line 20106, which maps literally to the FILED-NOT-STAFFED status. However, it appears in tracked defect lists alongside actively-worked items at line 22073, suggesting ongoing awareness. Classified OPEN as the more conservative status.

- **DEF-12** (CLOSED — reclassified by ca-msg-arch after extraction): This was an extraction error, and an instructive one. The journal states plainly at line 25177, "**DEF-12 and DEF-81 are closed.**" The extraction picked up DEF-81 from that sentence but not DEF-12, because a *later* mention (line 26055) discusses DEF-12 in the present tense as part of the activation sequence. The summary text was then taken from line 20471, which predates closure by five thousand lines. **Last mention is not last status.** Any future refresh of this ledger should search for an explicit closure statement before falling back to the most recent mention.

- **Anchor precision**: Anchors represent the first substantive mention found in the journal. For defects first listed in status tables (e.g., Section 3 or §5de), the anchor may point to the table row rather than a full prose definition.

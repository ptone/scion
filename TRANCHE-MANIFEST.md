# TRANCHE-MANIFEST

**Scope caveat.** Table 1 (new files) covers files present on `origin/scion/messaging-v2`
and absent from `upstream/main`. It is silent on modifications to files that exist on both
trees. A file-existence table cannot see Phase 11 (broker edge), which lands entirely as
modifications, nor any other behavioural change that modifies an existing file. Table 2
(modifications) covers all 18 files that v2 modifies AND that main changed post-fork
(per architect's extension scope). All 18 have been swept for revert risk.

Files on `origin/scion/messaging-v2` (`91c9e314`) absent from `upstream/main` (`31c48801`),
mapped to tranches C–G per the tranche table at IMPLEMENTATION-STATE.md line 1772 (rows 1776–1782).

Instrument: `comm -23` on `git ls-tree -r --name-only` outputs.
Control: `dm_key.go`, `dm_migration.go`, `conversation_store.go` all correctly absent (landed via tranche A).
77 files total: 32 code, 44 `.design/project-log/`, 1 `pkg/messaging/VALIDATION_EXEMPTIONS.md`.

**Tranche table (line 1772):**

| # | Content |
|---|---|
| **C** | Phases 6, 9, 11: envelope types, delivery format, broker edge + 5 adapters |
| **D** | Phase 7: validation choke point |
| **E** | Phase 8: read-switch machinery + divergence board |
| **F** | Phase 10 + S8: CLI subcommands, help grammar, deprecations |
| **G** | Flip `conversation_read_switch` |
| **H** | Conversations resolve endpoint (architect ruling 2026-08-28) |

**NOTE — C is self-contradictory.** Line 1778 defines C as "Phases 6, 9, 11". Line 5322–5323
redefines C as "§2.6.4 phases 1–4 + the DEF-20/21/22/23 chain + DEF-27". §5ca (line 5994) and
§5co (line 6958) compute C entirely from `ca-msg-em9-unify` as unnamed counts ("44 safe adds",
"8 cmd" adds). The three definitions are never reconciled. This manifest uses the **line 1778
definition** (phase-level) as the authoritative source, and flags conflicts below.

**Label disambiguation.** Table 1 uses NONE to mean "no tranche claims this file." Table 2
uses UNCLAIMED to mean "this change is absent from main but no tranche names it." Both indicate
a gap in the tranche plan, but Table 1 NONE is about whole files and Table 2 UNCLAIMED is about
individual hunks within a file that may have other hunks correctly assigned.

---

## Table 1 — New files (32 code + 1 non-code + 44 docs)

| file | tranche | basis | confidence | notes |
|---|---|---|---|---|
| `pkg/messaging/envelope.go` | C | line 1778: "Phase 6: envelope types" | HIGH | Phase 6 envelope type definition |
| `pkg/messaging/envelope_compat.go` | C | line 1778: Phase 6 | HIGH | Envelope compat layer |
| `pkg/messaging/envelope_compat_test.go` | C | line 1778: Phase 6 | HIGH | |
| `pkg/messaging/envelope_test.go` | C | line 1778: Phase 6 | HIGH | |
| `pkg/messaging/delivery.go` | C | line 1778: "Phase 9: delivery format" | HIGH | Phase 9 delivery format |
| `pkg/messaging/delivery_compat.go` | C | line 1778: Phase 9 | HIGH | Delivery compat layer |
| `pkg/messaging/delivery_compat_test.go` | C | line 1778: Phase 9 | HIGH | |
| `pkg/messaging/delivery_test.go` | C | line 1778: Phase 9 | HIGH | |
| `pkg/messaging/validate.go` | D | line 1779: "Phase 7: validation choke point" | HIGH | Phase 7 validation choke |
| `pkg/messaging/validate_compat.go` | D | line 1779: Phase 7 | HIGH | Validation compat layer |
| `pkg/messaging/validate_compat_test.go` | D | line 1779: Phase 7 | HIGH | AC-8 round-2 verified (line 2156) |
| `pkg/messaging/validate_test.go` | D | line 1779: Phase 7 | HIGH | |
| `pkg/hub/handlers_validation_integration_test.go` | D | line 1779: Phase 7 | HIGH | Integration test for the validation choke point |
| `pkg/hub/admin_messaging_divergence.go` | E | line 1780: "Phase 8: divergence board" | HIGH | Admin divergence board handler |
| `pkg/hub/admin_messaging_divergence_test.go` | E | line 1780: Phase 8 | HIGH | |
| `pkg/hub/handlers_read_switch_test.go` | E | line 1780: "Phase 8: read-switch machinery" | HIGH | Read-switch test; flag-gated OFF |
| `cmd/broadcast.go` | F | architect ruling: line 1781 "CLI subcommands" is definitive; C's claim existed only as a directory-level add count (§5ca line 6013), not a feature-level assignment | HIGH | C's count in §5co is overstated by 4. C must not be cut from that number. |
| `cmd/broadcast_test.go` | F | same as `broadcast.go` | HIGH | |
| `cmd/keys.go` | F | architect ruling: same as `broadcast.go` | HIGH | C's count in §5co is overstated by 4. |
| `cmd/keys_test.go` | F | same as `keys.go` | HIGH | |
| `cmd/message_deprecation_test.go` | F | line 3250: "DEF-25's file rides tranche F" | HIGH | **Only file-level tranche claim in the entire document.** DEF-25 closed 21:47Z (line 1913). |
| `cmd/doc_syntax_test.go` | F | line 1781: "Phase 10 + S8: help grammar"; line 2465/4299: doc syntax test in S8/DEF-13 context | MEDIUM | S8 = DEF-13 help text work (line 1781 "S8"). Tests design doc syntax against cobra help. |
| `cmd/message_help_test.go` | F | line 1781: "Phase 10 + S8: help grammar"; line 4184: referenced in DEF-17 format-check context | MEDIUM | Help text test; Phase 10 / S8 territory. |
| `cmd/message_convref_test.go` | F | architect ruling: tests CLI conversation-reference grammar (TestConvRefParsing_AtAgent, _AtEmail, _HashThread); carries convRefMockServer with outboundMessage recorder | HIGH | Carries the test harness for the G-2 fix (conv:/# resolve but deliver nothing). If this file drops silently, the fix for a HIGH finding loses its instrument. |
| `cmd/deploy_instance.go` | NONE | line 1886/10061–10062: "correctly unclaimed — main deleted them in #1325, revert hazard" | HIGH | **DO NOT CARRY — REVERT HAZARD.** Main deleted this file in #1325 (the Cloud Run move). Porting it re-adds 828 lines of deleted deploy tooling. |
| `cmd/deploy_instance_test.go` | NONE | same as `deploy_instance.go` | HIGH | **DO NOT CARRY — REVERT HAZARD.** +736 lines. Same #1325 deletion. |
| `cmd/server_backfill.go` | NONE | line 10059: "DEF-12, no tranche letter"; line 5084: DEF-12 spec | HIGH | **HIGHEST SILENT-DROP RISK** (line 10074). DEF-12 backfill CLI subcommand. No tranche row. The 4-file `server_backfill*` cluster is the only cluster whose feature has no tranche at all. |
| `cmd/server_backfill_test.go` | NONE | never mentioned (line 10063) | HIGH | Part of the `server_backfill*` cluster. |
| `cmd/server_backfill_volume_test.go` | NONE | never mentioned (line 10063) | HIGH | Part of the `server_backfill*` cluster. |
| `cmd/server_foreground_backfill_test.go` | NONE | never mentioned (line 10064) | HIGH | Part of the `server_backfill*` cluster. |
| `pkg/hub/handlers_conversations_resolve.go` | H | architect ruling: new tranche H "the conversations resolve endpoint". **BLOCKED ON G-1**, no carrier, no schedule. G-1 fix (delete `sender_principal_kind`/`sender_principal_id` from request body) must ship with the file or the file does not ship. | HIGH | G-1 (line 4930): endpoint lets caller choose who they are. Landing the endpoint and fixing it afterwards means shipping the vulnerability and chasing it. |
| `pkg/hub/handlers_conversations_resolve_test.go` | H | architect ruling: **REPLACE, not CARRY.** All five existing tests construct a bare `&Server{}` and only reach early returns — zero coverage of sender identity. | HIGH | A suite that reaches only early returns is worse than no suite because it reports coverage it does not have. Must be rewritten before H ships. |

### Non-code file (1)

| file | tranche | basis | confidence | notes |
|---|---|---|---|---|
| `pkg/messaging/VALIDATION_EXEMPTIONS.md` | D | line 2158: AC-8c verified in S3/Phase 7 review | HIGH | Documents three server-generated emitters exempted from the Phase 7 validation choke point. Ships with `validate.go`. |

### Design log files (44)

| file | tranche | basis | confidence | notes |
|---|---|---|---|---|
| `.design/project-log/2026-08-27-def11-preresolved-externalref.md` | DOCS | §5ca line 6014/6067: ".design/ files — noise" | n/a | Project log |
| `.design/project-log/2026-08-27-def13-help-text.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-def15-derive-key-foundation.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-def15-thread-dm-shape.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-def16-dualwrite-validation-order.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-def26-rename-placeholder-test.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-def4-hub-test-fix.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-def8-auth.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-def8-convergence.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-g1-g2-fix.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-i1-warning-fixes.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-i2i3-parsecheck-fixes.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-j1j2-test-floors.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-mutation1-test-rewrite.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-s5-closeout.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws1-phase8-foundation.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws1-skill-update.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws2-docs-messaging.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws2-phase8-readswitch.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws3-cli-reference.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws3-fix-f1-f2.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws3-phase10-cli.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws4-broker-edge.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws4-glossary.md` | DOCS | same | n/a | |
| `.design/project-log/2026-08-27-ws5-doc-parsecheck.md` | DOCS | same | n/a | |
| `.design/project-log/def12-cli-command.md` | DOCS | same | n/a | |
| `.design/project-log/def12-store-detection.md` | DOCS | same | n/a | |
| `.design/project-log/def12-volume-exercise.md` | DOCS | same | n/a | |
| `.design/project-log/def15-phase6-migration-sweep.md` | DOCS | same | n/a | |
| `.design/project-log/def15-phases3-5-key-consolidation.md` | DOCS | same | n/a | |
| `.design/project-log/def25-grove-fixture-rename.md` | DOCS | same | n/a | |
| `.design/project-log/dev-s3-fixes-review-audit.md` | DOCS | same | n/a | |
| `.design/project-log/e1-native-chat-validation.md` | DOCS | same | n/a | |
| `.design/project-log/phase1-schema.md` | DOCS | same | n/a | |
| `.design/project-log/phase2-store.md` | DOCS | same | n/a | |
| `.design/project-log/phase3-resolution.md` | DOCS | same | n/a | |
| `.design/project-log/s2-fix-round3.md` | DOCS | same | n/a | |
| `.design/project-log/s2-foundation-schema.md` | DOCS | same | n/a | |
| `.design/project-log/s2-phase4-backfill.md` | DOCS | same | n/a | |
| `.design/project-log/s215-groupmsg-m1-proof.md` | DOCS | same | n/a | |
| `.design/project-log/s215-validDMKey-coexistence-test.md` | DOCS | same | n/a | |
| `.design/project-log/s3-phase6-envelope-types.md` | DOCS | same | n/a | |
| `.design/project-log/s3-phase7-validate.md` | DOCS | same | n/a | |
| `.design/project-log/s3-phase9-delivery-format.md` | DOCS | same | n/a | |

---

## Table 1 summary by tranche

| Tranche | Code files | Notes |
|---|---|---|
| **C** | 8 | envelope (4) + delivery (4). Phase 11 has zero new files — see Table 2. |
| **D** | 5 + 1 non-code | validate (4) + integration test (1) + `VALIDATION_EXEMPTIONS.md` (1) |
| **E** | 3 | divergence board (2) + read-switch test (1) |
| **F** | 8 | broadcast (2) + keys (2) + deprecation test (1) + doc syntax test (1) + help test (1) + convref test (1, G-2 harness) |
| **H** | 2 | resolve handler (1) + resolve test (1, REPLACE not CARRY). **BLOCKED on G-1.** |
| **NONE** | 6 | deploy_instance (2, REVERT HAZARD) + server_backfill (4, SILENT-DROP RISK) |
| **DOCS** | 44 | `.design/project-log/*` — noise per §5ca |

---

## Table 2 — Modified files (broker edge + hub + CLI scope)

Instrument: `git diff upstream/main origin/scion/messaging-v2 -- <file>`, content-checked
against `upstream/main` @ `31c48801`. Tranche B landed as `b3562fb1` (#1343).
Changes are decomposed per behavioural hunk where a file spans multiple phases.

**REVERT-RISK method.** For each file, ran
`git log --since=2026-08-27T11:05:00 --format='%h %s' upstream/main -- <file>`.
Merge-base is `6268bac4` (2026-08-27 11:05Z). Any commit main gained after that timestamp
on a file v2 also modifies is a revert candidate. Each such commit was read and its hunks
compared to v2's version of the same lines. CONFIRMED = v2 demonstrably predates the change
and would undo it. NONE = no post-fork commits touch this file, or the post-fork changes
are in areas v2 does not modify.

**Post-fork commits found (16 files, 15 distinct commits):**

*Security/correctness PRs (4 commits):*
- `b453a685` (#1322, DM key ownership, 2026-08-27 13:16Z) → `handlers_broker_inbound.go`,
  `handlers_agent_messaging.go`, `handlers_chat_v2.go`
- `b3562fb1` (#1343, tranche B / B5 security, 2026-08-28 07:40Z) → `messagebroker.go`,
  `handlers_agent_messaging.go`, `messagebroker_test.go`, `chat_notifications_test.go`,
  `handlers_agent_messaging_test.go`
- `31012697` (#1338, DEF-31 defaultAgent, 2026-08-28 06:37Z) → `handlers_chat_v2.go`
- `31bedbb3` (#1347, ActionAttach + project authz, 2026-08-28 09:37Z) →
  `handlers_agent_messaging.go`, `handlers_chat_v2.go`
- `e8c7c40a` (#1349, B6/B7/B9 EnsureParticipant, post-fork) → `store.go`

*P2 feature PRs (10 commits):*
- `392e5e14` (P2-B1 #1323) → `server.go`, `store.go`, `models.go`, `composite.go`
- `b09e7f49` (P2-A1 #1324) → `route_metadata.go`, `models.go`
- `943241ad` (P2-A2 #1327) → `route_metadata.go`, `route_classification_test.go`
- `78323b5b` (P2-B2 #1328) → `server.go`
- `cca1f87d` (P2-A3 #1329) → `route_metadata.go`
- `9668909c` (#1331 conv model) → `store.go`, `models.go`, `composite.go`,
  `message_store.go`, `types.go`
- `c600df51` (P2-A4 #1332) → `route_metadata.go`, `route_classification_test.go`,
  `admin_maintenance_test.go`
- `53ec098f` (P2-A5 #1333) → `route_metadata.go`
- `ce9a7993` (P2-A6 #1334) → `route_metadata.go`, `route_classification_test.go`,
  `handlers_policies_test.go`, `skill_registry_handlers_test.go`
- `680193b8` (P2-B3 #1336) → `server.go`, `route_metadata.go`, `route_classification_test.go`
- `137e1763` (P2-D1 #1346) → `server.go`, `route_metadata.go`, `route_classification_test.go`
- `19e32902` (P2-C1 #1348) → `server.go`, `route_metadata.go`, `route_classification_test.go`,
  `store.go`

*No post-fork commits:* all 13 extras adapter files, `cmd/message.go`

### Extras adapters (Phase 11 — all absent from main, all tranche C)

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `extras/scion-discord/internal/discord/broker.go` | +28: adds `Surface`/`ExternalRef`/`ParentRef` to `inboundPayload`; derives fields from `discord_guild_id` metadata in `deliverInbound` | NO | C | HIGH | NONE | Phase 11 broker edge. Comment says "Phase 11" explicitly. Zero post-fork commits. |
| `extras/scion-discord/internal/discord/broker_test.go` | +85: `TestDeliverInbound_ConversationFields` | NO | C | HIGH | NONE | Phase 11 test. Zero post-fork commits. |
| `extras/scion-slack/internal/slack/broker.go` | +36: same pattern — `channelID:threadTS` as `ExternalRef`, channel as `ParentRef` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-slack/internal/slack/broker_test.go` | +118: `TestDeliverInbound_ConversationFields` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-teams/internal/teams/hubclient.go` | +42: adds `teamsConvFields()` helper + wires into `DeliverInbound` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-teams/internal/teams/hubclient_test.go` | +149: `TestTeamsConvFields` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-telegram/internal/telegram/telegram.go` | +38: adds `telegramConvFields()` helper + wires into v1 `deliverInbound` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-telegram/internal/telegram/broker_v2.go` | +18: wires `telegramConvFields` into v2 `deliverInbound` and `deliverInboundWithFeedback` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-telegram/internal/telegram/telegram_test.go` | +129: `TestTelegramConvFields` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-chat-app/internal/chatapp/commands.go` | +33: adds `gchatConvFields()` helper; three call sites switch from `SendStructuredMessage` to `SendStructuredMessageWithConv` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-chat-app/internal/chatapp/commands_test.go` | +52: `TestGchatConvFields` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-chat-app/internal/chatapp/commands_new_test.go` | +6: adds `SendStructuredMessageWithConv` stub to `stubAgentService` | NO | C | HIGH | NONE | Zero post-fork commits. |
| `extras/scion-chat-app/internal/chatapp/sendqueue_test.go` | +4: minor test adjustment | NO | C | LOW | NONE | Zero post-fork commits. Small change; possibly unrelated drift. |

### Hub inbound handler (multi-phase — decomposed)

Post-fork commit: `b453a685` (#1322, 2026-08-27 13:16Z) — "validate DM key ownership at
message ingress — prevent cross-project injection." Added SenderID caching at the permission
check, added `parseDMKeyIDs` DM ownership check, and removed the late SenderID-by-email
resolution. v2 predates this commit and reverses all three changes.

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `…/handlers_broker_inbound.go` — Phase 11 fields | +9: adds `Surface`/`ExternalRef`/`ParentRef` to `inboundMessageRequest` struct | NO | C | HIGH | NONE | Phase 11 broker-edge request schema. #1322 did not touch this area. |
| `…/handlers_broker_inbound.go` — Phase 11 resolution | +50: broker-edge conversation resolution block (`UpsertConversationByExternalRef`, attaches `conversation_id` to message metadata) | NO | C | MEDIUM | NONE | Phase 11 core logic. #1322 did not touch this area. Confidence MEDIUM: depends on store shapes that may drift. |
| `…/handlers_broker_inbound.go` — Phase 7 validation | +4: `messaging.ValidateLegacyMessage(req.Message)` call before dispatch | NO | D | HIGH | NONE | Phase 7 choke point wired to broker inbound path (AC-8). #1322 did not touch this area. |
| `…/handlers_broker_inbound.go` — Phase 5 dual-write | +31: `ResolveOrCreate{Thread,DM}Conversation` + divergence logging + `CheckConversationConsistency` | NO | UNCLAIMED | MEDIUM | NONE | Phase 5 dual-write for broker-inbound. Tranche B landed Phase 5 dual-write for `messagebroker.go` but NOT for this file. No tranche claims this hunk. #1322 did not touch this area. |
| `…/handlers_broker_inbound.go` — SenderID caching removal | −4: removes `req.Message.SenderID = senderUser.ID` and its comment, added by #1322 at lines 142–145 on main | NO | UNCLAIMED | LOW | **CONFIRMED** | **REVERTS #1322.** #1322 added this caching so the downstream DM ownership check could use it. v2 predates #1322 and has no equivalent. Removing the caching without removing the ownership check would break the check; v2 removes both — see next row. **RESOLUTION:** Keep #1322's early SenderID caching. Phase 11 resolution should be added AFTER it. The late `GetUserByEmail` fallback (row below) then drops out as redundant rather than needing to be argued about. |
| `…/handlers_broker_inbound.go` — DM ownership check removal | −14: deletes the `parseDMKeyIDs` / DM ownership check block (lines 160–173 on main) | NO | UNCLAIMED | LOW | **CONFIRMED** | **REVERTS #1322 — SECURITY.** #1322 added this check to prevent cross-project DM injection: a broker plugin supplying a crafted `ThreadID` like `dm:agent:<victimAgent>:user:<attacker>` could inject messages into a DM the sender does not belong to. The check validates that the agent ID and user ID in the DM key match the actual resolved agent and the authenticated sender. v2 has no equivalent — zero grep hits for `parseDMKeyIDs` or `"DM thread_id does not match"`. v2's Phase 11 conversation resolution provides a *different* authorization model (conversation-based rather than ThreadID-based), but carrying the removal without the Phase 11 block leaves the endpoint unprotected. **Must not be carried independently.** Main has not removed this check by any other route. **RESOLUTION:** Keep the DM ownership check (#1322). The ownership check protects ThreadID-based participant identity; Phase 11 resolution protects Surface/ExternalRef routing. They are orthogonal — the two protect different things, making them composable rather than competing. Add Phase 11 resolution after the ownership check. If the user chooses rebase, both can coexist. |
| `…/handlers_broker_inbound.go` — late SenderID resolution | −4/+9: re-adds `GetUserByEmail` fallback for empty SenderID (lines 283–289 on v2) that #1322 removed | NO | UNCLAIMED | LOW | **CONFIRMED** | **REVERTS #1322.** #1322 moved SenderID resolution earlier (into the permission check) and removed this fallback. v2 re-adds it because v2 predates #1322 and never had the early caching. Carrying this hunk alone is not dangerous, but it signals the #1322 revert since all three hunks are coupled. **RESOLUTION:** Drop this hunk. The early SenderID caching from #1322 (row above) makes this fallback redundant. |

### Message broker (multi-phase — decomposed)

Post-fork commit: `b3562fb1` (#1343, tranche B, 2026-08-28 07:40Z).

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `…/messagebroker.go` — signature fix | −2/+2: `ResolveOrCreateDMConversation` drops duplicate `p.store` parameter (v2: `p.store, p.log`; main: `p.store, p.store, p.log`) | NO | UNCLAIMED | MEDIUM | NONE | Later Phase 5 refinement. #1343 landed the double-store signature. v2's version is a forward fix, not a revert — main never had the single-store version. May require coordinated change in function signature. |
| `…/messagebroker.go` — DEF-3 consistency | +4: adds `CheckConversationConsistency` calls in `deliverToUser` and `deliverToAgent` | NO | UNCLAIMED | HIGH | NONE | DEF-3 independent consistency check. Not on main at all — purely additive, no revert possible. |
| `…/messagebroker.go` — B5 REVERT | −27/+4: removes B5/R1 `msg.SenderID == agent.ID` self-skip, R3b empty-SenderID warnings; replaces with pre-B5 `msg.Sender == "agent:"+agent.Slug` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **SECURITY REGRESSION.** v2 predates B5 (#1343). Post-B5 the Sender field can hold a UUID, so `"agent:"+agent.Slug` never matches and the self-skip stops firing — the sending agent receives its own broadcast. Main's explanation at :725: "Sender is a display label that may be in UUID form after the B5 auth-derivation override." Carrying this hunk deletes both the guard and the alarm (#1343 also added R3b warnings for empty SenderID). §5co (line 6991–7001) documents this collision with zero-count proof. |

### CLI message command (multi-phase — decomposed)

Zero post-fork commits on main for `cmd/message.go`.

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `cmd/message.go` — deprecation system | +35: `emitDeprecationWarning`, `deprecationReplacements` table, `emitDeprecationWarnings` | NO | F | HIGH | NONE | Phase 10 + S8 deprecations (line 1781). DEF-25/I-1 fix territory. |
| `cmd/message.go` — help text update | +6: adds `@<agent>`, `@<email>`, `conv:<uuid>`, `#<thread>` to `Long` help | NO | F | HIGH | NONE | S8/DEF-13: "Long now documents @, conv:, #" (line 5089). |
| `cmd/message.go` — conversation reference parsing | +20: `messaging.ParseReference` integration, `RefConversation`/`RefThread` gate | NO | F | HIGH | NONE | Phase 10 CLI split — new reference forms. |
| `cmd/message.go` — `sendMessageViaConversation` | +80: full function: resolve via Hub, send via standard agent path with `conversation_id` | NO | F | HIGH | NONE | Phase 10 CLI delivery for `@<agent>`, `@<email>`. |
| `cmd/message.go` — flag deprecation + hiding | +25: `MarkHidden` on 10 flags, help-string rewording | NO | F | HIGH | NONE | Phase 10 + S8 deprecation UX. |
| `cmd/message.go` — Phase 7 validation | +8: `ValidateLegacyMessage` calls on broadcast and direct-send paths | NO | D | MEDIUM | NONE | Phase 7 choke point wired to CLI. Confidence MEDIUM: depends on `pkg/messaging` types that tranche D's new files define — carrying the calls without the types fails to compile. |
| `cmd/message.go` — visibility flag | +3: `--visibility` flag and `msg.Visibility` assignment | NO | UNCLAIMED | LOW | NONE | No phase reference found. May be Phase 10 CLI work or an independent enhancement. |

### Agent messaging handler (multi-phase — decomposed)

Post-fork commits: `31bedbb3` (#1347, ActionAttach + project authz, 2026-08-28 09:37Z),
`b3562fb1` (#1343, tranche B / B5 security, 2026-08-28 07:40Z),
`b453a685` (#1322, DM key ownership, 2026-08-27 13:16Z).

This file has the highest density of security reverts in the codebase — 10 CONFIRMED hunks
spanning all three post-fork security PRs. v2 adds significant Phase 11 functionality (conversation
resolution, cross-project mention checks, DEF-3 consistency, DEF-11 pre-resolved conv handling)
but every addition is interleaved with security control removals.

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `…/handlers_agent_messaging.go` — #1322 DM ownership check in `handleAgentOutboundMessage` | −11: deletes `parseDMKeyIDs(req.ThreadID)` ownership block (lines 178–187 on main). Check verified DM key agent/user match actual sender/recipient. | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1322 — SECURITY.** Same pattern as `handlers_broker_inbound.go`. v2 has no equivalent check on this path. Phase 11's `DeriveConversationKey` (added by v2 at ~line 267) replaces ThreadID-based routing but does NOT validate participant ownership. |
| `…/handlers_agent_messaging.go` — B5 unconditional auth override in `handleAgentMessage` | −18/+22: changes from ALWAYS overriding `structuredMsg.Sender`/`SenderID` via `authenticatedSender(ctx)` to CONDITIONAL backfill when `Sender == ""` (lines 540–568 v2 vs 540–562 main) | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) — SECURITY.** B5's fix was making the override unconditional: "Client-supplied Sender and SenderID are untrusted inputs that must never be used as conversation key inputs." v2 makes it conditional — a client supplying a spoofed Sender gets it accepted. The SenderID feeds into DM key derivation downstream (dual-write, group message), so a spoofed SenderID means a wrong DM key means a wrong ACL. Three downstream dual-write locations inherit the tainted value. |
| `…/handlers_agent_messaging.go` — Phase 7 validation in `handleAgentMessage` | +5: adds `messaging.ValidateLegacyMessage(structuredMsg)` call | NO | D | HIGH | NONE | Phase 7 choke point (AC-8). Not a revert — purely additive. |
| `…/handlers_agent_messaging.go` — AC-33 cross-project mention check | +24: adds `messaging.ValidateCrossProjectAddressees` before dispatch | NO | UNCLAIMED | HIGH | NONE | Additive check. No post-fork equivalent exists — this is new v2 work. |
| `…/handlers_agent_messaging.go` — Phase 11 fields in `MessageRequest` | +7: adds `Surface`, `ExternalRef`, `ParentRef` fields to struct | NO | C | HIGH | NONE | Phase 11 broker edge request schema. |
| `…/handlers_agent_messaging.go` — #1322 DM ownership check in `handleAgentMessage` | −14/+24: deletes `parseDMKeyIDs` / authenticated-identity ownership check (lines 617–634 on main); replaces with Phase 11 `UpsertConversationByExternalRef` block | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1322 — SECURITY.** Main's check used `authenticatedUserID` (from context) not payload. v2 replaces with conversation resolution that does NOT validate DM ThreadID participant ownership. The replacement is functionally different: it resolves conversations by external_ref, not by validating DM key slots. |
| `…/handlers_agent_messaging.go` — Phase 5 dual-write in `handleAgentMessage` (DeriveConversationKey) | +35/−8: replaces `ResolveOrCreate{Thread,DM}Conversation` pair with unified `DeriveConversationKey` + `ResolveOrCreateConversationByKey` | NO | UNCLAIMED | MEDIUM | NONE | Forward refactoring of Phase 5 dual-write. Not a revert — the old calls are replaced with a single unified path. However, the old calls used `authenticatedSender(ctx)` for the DM case (B5 security); the new calls use `structuredMsg.SenderID` which may be client-supplied (see B5 revert row above). The security regression lives in the Sender population, not in the resolution function. |
| `…/handlers_agent_messaging.go` — DEF-11 pre-resolved conv handling | +18: when `structuredMsg.ConversationID != ""`, uses it directly and looks up `ExternalRef` from store | NO | UNCLAIMED | HIGH | NONE | New v2 work. Purely additive — no post-fork code in this area. |
| `…/handlers_agent_messaging.go` — DEF-3/DEF-11 divergence + consistency | +25/−7: adds `lookupFailed` guard, `conv-lookup-failed` reason, `CheckConversationConsistency` | NO | UNCLAIMED | HIGH | NONE | Forward work (DEF-3, DEF-11). Purely additive — no post-fork code here. |
| `…/handlers_agent_messaging.go` — B5 DM key derivation in `handleGroupMessage` (agent set) | −3/+5: replaces `authenticatedSender(ctx)` with `agentMsg.SenderID` for `ResolveOrCreateDMConversation` input | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) — SECURITY.** B5 comment: "derive sender from authenticated context, never payload." v2 uses `agentMsg.SenderID` — a payload field that may be client-supplied (see conditional backfill revert above). The DM key derived from a spoofed SenderID creates/accesses the wrong conversation. |
| `…/handlers_agent_messaging.go` — B5 DM key derivation in `handleGroupMessage` (user set) | −3/+5: same pattern for user-recipient group messages: `authenticatedSender(ctx)` → `userMsg.SenderID` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) — SECURITY.** Same as agent set above. |
| `…/handlers_agent_messaging.go` — #1347 project authz in `handleProjectBroadcast` | −16: removes `if userIdent != nil { s.authorize(w, r, projectResource(project), ActionAttach) }` block (lines 1245–1259 on main) | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1347 — SECURITY.** #1347 added this check so user callers must have attach access in the target project before broadcasting. v2 removes the entire block — any authenticated user can broadcast to any project. |
| `…/handlers_agent_messaging.go` — B5 unconditional auth override in `handleProjectBroadcast` | −14/+10: same pattern as `handleAgentMessage` — ALWAYS override → CONDITIONAL backfill when `Sender == ""` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) — SECURITY.** Identical vulnerability to `handleAgentMessage`: spoofed Sender/SenderID accepted when client provides them. |
| `…/handlers_agent_messaging.go` — B5 forced `Broadcasted = true` removal | −8: removes `req.StructuredMessage.Broadcasted = true` and its B5 comment | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) — SECURITY.** B5 comment: "force Broadcasted = true server-side. The client must not control whether its message is treated as a broadcast." Without this, a client setting `Broadcasted=false` walks the message through the DM dual-write in `deliverToAgent`, creating a spurious DM conversation per running agent. |
| `…/handlers_agent_messaging.go` — B5 auth self-skip → slug comparison (targeting) | −2/+1: replaces `if authKind == "agent" && agent.ID == authID` with `if req.StructuredMessage.Sender == "agent:"+agent.Slug` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) — SECURITY.** Same pattern as `messagebroker.go` B5 revert. After B5, Sender may contain a UUID, so `"agent:"+agent.Slug` never matches — self-skip fails and the sending agent receives its own broadcast. Main uses authenticated ID comparison which works regardless of Sender format. |
| `…/handlers_agent_messaging.go` — B5 auth self-skip → slug comparison (running) | −2/+1: same pattern in second loop (collecting running agents for fan-out) | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) — SECURITY.** Same as targeting loop above — duplicate self-skip logic, same regression. |
| `…/handlers_agent_messaging.go` — Phase 7 validation in `handleProjectBroadcast` | +5: adds `messaging.ValidateLegacyMessage(req.StructuredMessage)` call | NO | D | HIGH | NONE | Phase 7 choke point (AC-8). Not a revert — purely additive. |
| `…/handlers_agent_messaging.go` — `authenticatedSender` function deletion | −18: deletes the entire function (lines 1596–1613 on main) | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343).** B5 introduced this function as the single point for deriving trusted sender identity. All B5 reverts above are consequences of this deletion — without the function, all call sites must find another source, and v2's choice (payload-supplied values with conditional backfill) is the pre-B5 vulnerable pattern. |
| `…/handlers_agent_messaging.go` — Phase 7 validation in `handleAgentOutboundMessage` | +5: adds `messaging.ValidateLegacyMessage(structuredMsg)` in outbound path | NO | D | HIGH | NONE | Phase 7 choke point wired to agent outbound path (AC-8). |
| `…/handlers_agent_messaging.go` — Phase 5 dual-write in `handleAgentOutboundMessage` (DeriveConversationKey) | +25/−4: replaces `ResolveOrCreate{Thread,DM}Conversation` with `DeriveConversationKey` + `ResolveOrCreateConversationByKey` | NO | UNCLAIMED | MEDIUM | NONE | Same forward refactoring as `handleAgentMessage`. The outbound path uses `agent.ID` and `recipientID` (server-derived) for the key, not client-supplied SenderID, so the B5 regression does NOT apply to this specific path. |
| `…/handlers_agent_messaging.go` — DEF-3 consistency in `handleAgentOutboundMessage` | +2: adds `CheckConversationConsistency` call | NO | UNCLAIMED | HIGH | NONE | Purely additive. |
| `…/handlers_agent_messaging.go` — DEF-3 consistency in `handleGroupMessage` (×2) | +4: adds `CheckConversationConsistency` calls in both agent-set and user-set paths | NO | UNCLAIMED | HIGH | NONE | Purely additive. |

### Chat v2 handler (multi-phase — decomposed)

Post-fork commits: `31bedbb3` (#1347, ActionAttach for chat, 2026-08-28 09:37Z),
`31012697` (#1338, DEF-31 defaultAgent validation, 2026-08-28 06:37Z),
`b453a685` (#1322, DM key ownership, 2026-08-27 13:16Z).

This file carries 8 CONFIRMED revert hunks. Unlike `handlers_agent_messaging.go` (where reverts
are interleaved with Phase 11 additions), the reverts here are clean deletions with no replacement
logic — v2 simply omits the security controls that main added.

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `…/handlers_chat_v2.go` — #1338 `validateDefaultAgent` in `handleCreateThread` | −12: removes `strings.TrimSpace(body.DefaultAgent)` + `validateDefaultAgent` call (lines 448–459 on main) | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1338 (DEF-31).** v2 predates DEF-31. Without the validation, `handleCreateThread` accepts any string (including UUIDs naming agents in other projects or deleted agents) as `defaultAgent`. Not a security fix per se but a correctness/isolation fix. |
| `…/handlers_chat_v2.go` — #1338 `validateDefaultAgent` in `handleTopicPatch` | −11/+1: reverts to `updates.DefaultAgent = body.DefaultAgent` (pre-#1338 pattern), removing trim + validation | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1338 (DEF-31).** Same gap as `handleCreateThread`. |
| `…/handlers_chat_v2.go` — #1338 `validateDefaultAgent` function deletion | −30: deletes the entire `validateDefaultAgent` function (lines 677–706 on main) | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1338 (DEF-31).** The function enforced: length gate (200 runes), slug lookup (project-scoped, excludes soft-deleted), UUID fallback (project + deletion guard). All three safeguards are lost. |
| `…/handlers_chat_v2.go` — #1338 scope guard in `handleConversationSend` | −10: removes `defaultAgent.ProjectID != projectID \|\| !defaultAgent.DeletedAt.IsZero()` guard from `GetAgent` fallback (lines 995–1004 on main) | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1338 (DEF-31).** Without the guard, a UUID naming an agent in another project (or a deleted agent) binds successfully via `GetAgent` bare primary-key fetch. DEF-31 comment: "without this guard a UUID naming an agent in another project (or a deleted agent) would bind successfully." |
| `…/handlers_chat_v2.go` — #1347 `ActionAttach` on primary agent in `sendAgentRouted` | −3/+8: replaces `s.authorize(w, r, agentResource(primaryAgent), ActionAttach)` with `messaging.ValidateLegacyMessage(msg)` | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1347 — SECURITY.** #1347 added authorization (does the user have permission to attach to this agent?). v2 replaces it with message format validation (`ValidateLegacyMessage`) — a completely different check. Format validity does NOT imply authorization. A well-formed message from an unauthorized user passes `ValidateLegacyMessage` and reaches the agent. |
| `…/handlers_chat_v2.go` — #1347 `ActionAttach` on mentioned agents in `sendAgentRouted` | −19: removes entire per-mention `authzService.CheckAccess` block including skip-on-denied, logging, and `MentionResult` status update (lines 1197–1218 on main) | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1347 — SECURITY.** Without this, any user can mention any agent in any project regardless of permissions. The mention fan-out delivers the message to all named agents unconditionally. |
| `…/handlers_chat_v2.go` — #1322 `isDMParticipant` kind-label loosening | −3/+1: replaces `(parts[1] == "user" && parts[2] == userID) \|\| (parts[3] == "user" && parts[4] == userID)` with `parts[2] == userID \|\| parts[4] == userID` | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1322.** #1322 tightened this check so only "user" slots match, preventing an agent principal from satisfying a user-slot check (and vice-versa). v2 loosens it back — any slot matches regardless of kind label. Practical impact: an agent whose UUID collides with a user UUID could pass a user-identity check. |
| `…/handlers_chat_v2.go` — #1322 `parseDMKeyIDs` function deletion | −15: deletes the `parseDMKeyIDs` function (lines 3084–3098 on main) | NO | UNCLAIMED | HIGH | **CONFIRMED** | **REVERTS #1322.** This function is the utility used by DM ownership checks in `handlers_broker_inbound.go` and `handlers_agent_messaging.go`. Its deletion is consistent with v2 predating #1322 entirely. |
| `…/handlers_chat_v2.go` — Phase 8 read-switch in `handleConversationHistory` | +35/−3: adds conversation resolution via `ResolveDMConversationForRead` / `ResolveThreadConversationForRead` gated by `ConversationReadSwitch` | NO | E | HIGH | NONE | Phase 8 read-switch (line 1780). Purely additive new v2 functionality. No post-fork code in this area. |
| `…/handlers_chat_v2.go` — `messaging` import addition | +1: adds `"pkg/messaging"` to import block | NO | (dependency) | HIGH | NONE | Required by Phase 7/8 code. Not independently meaningful. |

### P2 authorization system files (Permissions Phase 2)

The P2 series (PRs #1323–#1348) built a permissions/quota/role system on main after the fork.
v2 predates the entire P2 series and has none of it. All removals below are feature reverts —
v2 does not replace the P2 functionality with anything, it simply lacks it.

**Independent restoration unit: P2 authorization pipeline.** The routeGuard `Decide` path
(route_metadata.go), 45 per-route Permission/Resource/Action metadata entries, and the route
registrations (server.go) form one interconnected unit. Restoring fine-grained admin
permissions requires all three together.

**Independent restoration unit: P2 quota subsystem.** QuotaStore interface (store.go),
quota models (models.go), QuotaStore embed (composite.go), QuotaService (server.go), route
registrations, and limit seeding are one unit.

**Independent restoration unit: P2 role management.** RoleStore extensions
(UpdateRoleDefinition, DeleteRoleDefinition, ListAllRoleBindings, CountAllRoleBindings in
store.go), SystemRoleHubAdmin constant (models.go), and role management routes
(server.go + route_metadata.go) form one unit.

Post-fork commits on these files:
- `server.go`: #1348 (P2-C1), #1346 (P2-D1), #1336 (P2-B3), #1328 (P2-B2), #1323 (P2-B1)
- `route_metadata.go`: #1348, #1346, #1336, #1334 (P2-A6), #1333 (P2-A5), #1332 (P2-A4),
  #1329 (P2-A3), #1327 (P2-A2), #1324 (P2-A1)
- `route_classification_test.go`: #1348, #1346, #1336, #1334, #1332, #1327

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `pkg/hub/server.go` — QuotaService field removal | −4: removes `quotaService *QuotaService` struct field | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B2 (#1328).** v2 has no quota enforcement. |
| `pkg/hub/server.go` — QuotaService init removal | −5: removes `QuotaService` initialization in `New()` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B2 (#1328).** |
| `pkg/hub/server.go` — seedLimitDefinitions removal | −4: removes `seedLimitDefinitions(ctx, s)` call | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B1 (#1323).** |
| `pkg/hub/server.go` — auth/scopes endpoint removal | −1: removes `/api/v1/auth/scopes` route registration | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-D1 (#1346).** UAT scope enumeration endpoint. |
| `pkg/hub/server.go` — quota routes removal | −7: removes 6 quota/usage route registrations (limits, entitlements, usage) | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B3 (#1336).** |
| `pkg/hub/server.go` — role management routes removal | −6: removes 5 role/binding/permissions route registrations | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-C1 (#1348).** |
| `pkg/hub/server.go` — messaging divergence route addition | +1: adds `/api/v1/admin/messaging/divergence` | NO | E | HIGH | NONE | Phase 8 divergence board (line 1780). v2 addition. |
| `pkg/hub/server.go` — conversations resolve route addition | +3: adds `/api/v1/conversations/resolve` | NO | H | HIGH | NONE | v2 addition. Part of tranche H (resolve endpoint). |
| `pkg/hub/route_metadata.go` — P2 Permission metadata removal | −45 Permission/Resource/Action fields across ~30 admin routes | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A1 through P2-A6 (#1324–#1334).** Without Permission metadata, all RouteHubAdmin routes fall back to `requireAdmin` bypass — coarser authorization. |
| `pkg/hub/route_metadata.go` — routeGuard Decide path removal | −40: removes entire Decide-based permission evaluation from RouteHubAdmin case | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A1 (#1324).** The structural backbone of fine-grained admin permissions. Without it, the per-route Permission metadata has no consumer. |
| `pkg/hub/route_metadata.go` — skill-registries classification change | RouteHubAdmin→RoutePolicy, Permission `skill.register`→`skill.read` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A5/A6.** Changes authorization model for skill registry endpoints. |
| `pkg/hub/route_metadata.go` — policy route Permission removal | −2 Permission fields from `/api/v1/policies` and `/api/v1/policies/` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A6 (#1334).** Policy routes lose fine-grained permission checks. |
| `pkg/hub/route_metadata.go` — auth/scopes route metadata removal | −4: removes `/api/v1/auth/scopes` entry | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-D1 (#1346).** |
| `pkg/hub/route_metadata.go` — quota/role route metadata removal | −55: removes all quota and role management route metadata entries | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B3/C1 (#1336/#1348).** |
| `pkg/hub/route_metadata.go` — conversations resolve route addition | +4: adds `/api/v1/conversations/resolve` metadata | NO | H | HIGH | NONE | v2 addition. |
| `pkg/hub/route_classification_test.go` — P2 route expectations removal | −14: removes quota, role, scopes, usage test entries | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B3/C1/D1.** Test expectations match pre-P2 route set. |
| `pkg/hub/route_classification_test.go` — skill-registry classification change | `hub-admin:skill-registry` → `policy:skill-registry` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A5/A6.** Matches route_metadata classification change. |
| `pkg/hub/route_classification_test.go` — routeGuard test simplification | −10/+6: removes Decide-path test comments, changes test routes from permission-gated to admin-only | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A1.** Tests match pre-P2 authorization model. |
| `pkg/hub/route_classification_test.go` — hub-admin UAT test removal | −30: removes `TestHubAdminRoutesRejectScopedAdminUAT` function, replaces `allHubAdminRoutes` with simpler inline loop | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-D1 (#1346).** Test verified scoped-admin tokens are rejected by hub-admin routes. |
| `pkg/hub/route_classification_test.go` — v2 route additions | +2: adds messaging divergence and conversations resolve test entries | NO | E, H | HIGH | NONE | v2 additions. |

### Store interface and model files

Post-fork commits:
- `store.go`: #1349 (B6/B7/B9 EnsureParticipant), #1348 (P2-C1 role management), #1331
  (conversation model), #1323 (P2-B1 quota)
- `models.go`: #1331 (conversation model), #1324 (P2-A1 hub-admin role), #1323 (P2-B1 quota)
- `composite.go`: #1331 (conversation model), #1323 (P2-B1 quota)
- `message_store.go`: #1331 (conversation model) — v2 diff is ONLY additions
- `types.go`: #1331 (conversation model) — v2 diff is cosmetic alignment only

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `pkg/store/store.go` — QuotaStore removal | −3: removes `QuotaStore` from Store interface | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B1 (#1323).** Part of quota subsystem unit. |
| `pkg/store/store.go` — ErrQuotaExceeded removal | −1: removes `ErrQuotaExceeded` sentinel error | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B1 (#1323).** |
| `pkg/store/store.go` — QuotaStore interface deletion | −72: removes entire `QuotaStore` interface (LimitDefinition CRUD, EntitlementBinding CRUD, UsageReservation management) | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B2 (#1328).** |
| `pkg/store/store.go` — RoleStore method removal | −16: removes `UpdateRoleDefinition`, `DeleteRoleDefinition`, `ListAllRoleBindings`, `CountAllRoleBindings` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-C1 (#1348).** Role management CRUD extensions. |
| `pkg/store/store.go` — EnsureParticipant removal | −5: removes `EnsureParticipant` from ConversationStore interface | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS #1349 (B6/B7/B9).** Idempotent participant-add. v2 has only `AddParticipant` (which fails on duplicates). |
| `pkg/store/store.go` — CountUnbackfilledMessages addition | +5: adds `CountUnbackfilledMessages` to MessageStore interface | NO | UNCLAIMED | HIGH | NONE | v2 addition — DEF-12 backfill support. Purely additive, no post-fork code in this area. |
| `pkg/store/models.go` — SystemRoleHubAdmin removal | −1: removes `SystemRoleHubAdmin = "hub-admin"` constant | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A1 (#1324).** Part of P2 role system. |
| `pkg/store/models.go` — quota models deletion | −64: removes `LimitDefinition`, `EntitlementBinding`, `UsageReservation` structs and related constants | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B1 (#1323).** Part of quota subsystem unit. |
| `pkg/store/models.go` — cosmetic alignment | ±38: struct field alignment differences in Message, MessageFilter, Conversation | NO | (cosmetic) | HIGH | NONE | Not functional. gofmt alignment difference. |
| `pkg/store/entadapter/composite.go` — QuotaStore embed removal | −2: removes `*QuotaStore` embed and initialization | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-B1 (#1323).** Part of quota subsystem unit. |
| `pkg/store/entadapter/message_store.go` — CountUnbackfilledMessages addition | +19: adds `CountUnbackfilledMessages` implementation | NO | UNCLAIMED | HIGH | NONE | v2 addition — DEF-12 backfill. Purely additive, #1331 did not change the same area. |
| `pkg/messages/types.go` — cosmetic alignment | ±18: struct field alignment in StructuredMessage | NO | (cosmetic) | HIGH | NONE | Not functional. Same fields, different tab alignment. |

### B5 test files (#1343)

Post-fork commit: `b3562fb1` (#1343, tranche B / B5 security, 2026-08-28 07:40Z).
All three files contain tests added by #1343 that v2 does not have.

**Coupled to authenticatedSender restoration unit.** If the B5 production code is restored,
these tests must also be restored — they verify the security invariants. If v2's test code
is carried instead, the tests will fail against restored B5 production code.

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `pkg/hub/messagebroker_test.go` — B5 SenderID test setup | −4: removes `senderAgent` capture and `msg.SenderID = senderAgent.ID` in `TestMessageBrokerProxy_BroadcastSkipsSender` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) test.** v2's test uses Sender slug for self-skip (pre-B5 pattern). Coupled to `messagebroker.go` B5 revert. |
| `pkg/hub/messagebroker_test.go` — newTestStore signature | `newTestStore(":memory:")` → `newTestStore(t, ":memory:")` | NO | (infrastructure) | LOW | NONE | v2 test helper API change. |
| `pkg/hub/chat_notifications_test.go` — B5 notification tests deletion | −150: removes 3 tests: `TestChatNotifier_DMReceived_ResolvesAgentSlugFromSenderID` (B5/F2), `_DoesNotClobberCallerLabel` (B5/F3/F6), `_FallsBackOnLookupFailure` (B5/F2 fallback) | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) tests.** These verify notification display name resolution after B5's auth-derivation override produces UUID-form Sender fields. |
| `pkg/hub/chat_notifications_test.go` — newTestStore + cleanup | −2/+1: signature change + removes explicit `t.Cleanup` | NO | (infrastructure) | LOW | NONE | v2 test helper API change. |
| `pkg/hub/handlers_agent_messaging_test.go` — B5 security tests deletion | −805: removes 8 test functions: `_B5_SpoofedSenderDoesNotDeriveConversationKey`, `_B5F1_SpoofedSender...`, `_B5F1b_BroadcastedForcedTrue...`, `_R1_BroadcastingAgent...`, `_B5F1a_SenderOverride...`, `_B5F1c_SelfSkipUsesAuth...`, `_R2_FanOutGlobalSelfSkip...`, `_R3b_WarnOnEmptySenderID` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS B5 (#1343) tests.** The most critical test coverage: 8 tests verifying spoofed-sender prevention, forced-Broadcasted, self-skip-by-ID, and empty-SenderID warnings. Without these tests, a future regression in B5 protections has no safety net. |
| `pkg/hub/handlers_agent_messaging_test.go` — v2 test additions | +~300: adds 5 tests: `TestDEF11_PreResolvedConversation_*` (4), `TestDEF19_GroupRecipient_FullHandlerPath` | NO | UNCLAIMED | HIGH | NONE | v2 test code for DEF-11 and DEF-19. These test v2-only functionality. |
| `pkg/hub/handlers_agent_messaging_test.go` — test infrastructure changes | ±50: import changes (removes slog/eventbus/require, adds fmt/sort/messaging), adds `testDirectMessageExternalRef` helper, `newTestStore` signature change | NO | (infrastructure) | MEDIUM | NONE | v2 test helper changes. The `testDirectMessageExternalRef` replicates legacy DM key format for divergence tests. |

### P2-A authorization test files

Post-fork commits: `c600df51` (P2-A4 #1332) for admin_maintenance_test.go,
`ce9a7993` (P2-A6 #1334) for handlers_policies_test.go and skill_registry_handlers_test.go.

All three files have the same pattern: P2-A converted inline `requireAdmin` checks to
routeGuard permission-based checks. The PR removed inline admin tests and replaced them with
routeGuard-level tests (in separate test files like `TestRouteGuardOpsPermissions`). v2
predates P2-A and still has inline admin checks, so it restores the inline tests.

**Coupled to P2 authorization pipeline restoration unit.** If P2-A is restored, these test
reverts are correct (the inline checks don't exist in production code). If P2-A is NOT
restored, these tests correctly verify the inline admin checks that v2 uses.

| file | v2 delta | on main? | tranche | confidence | revert-risk | notes |
|---|---|---|---|---|---|---|
| `pkg/hub/admin_maintenance_test.go` — inline admin tests restored | +60/−20: restores `TestListMaintenanceOperations_NonAdmin`, `TestExecuteMigration_NonAdmin`, `TestExecuteOperation_NonAdmin`, `TestCheckForUpdates_NonAdmin`, `TestHandleAdminRestart_Forbidden`. P2-A4 removed these (authorization moved to routeGuard). | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A4 (#1332) test changes.** v2's handlers still have inline `requireAdmin` checks, so these tests are correct for v2. |
| `pkg/hub/admin_maintenance_test.go` — newTestStore signature | `newTestStore(":memory:")` → `newTestStore(t, ":memory:")` | NO | (infrastructure) | LOW | NONE | v2 test helper API change. |
| `pkg/hub/handlers_policies_test.go` — simplified test server | −8/+6: removes `authzService` init, `seedRoleDefinitions`, creates bare `&Server{store: s}` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A6 (#1334).** v2's policy endpoints use inline admin checks, not Decide-based permission checks. |
| `pkg/hub/handlers_policies_test.go` — inline admin tests expanded | +30/−5: adds GET listPolicies, GET getPolicy, listPolicyBindings, addPolicyBinding test cases. Changes scope from write-only to all operations. | NO | UNCLAIMED | HIGH | NONE | v2's test coverage is broader for inline admin checks. Not a revert — expanded coverage. |
| `pkg/hub/handlers_policies_test.go` — routeGuard test deletion | −60: removes `TestPolicyRouteGuard_SuperAdminOnlyAccess` (verified hub-admin denied, super-admin allowed via Permission-based route guard) | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A6 (#1334).** Test verified Decide-based permission enforcement. |
| `pkg/hub/skill_registry_handlers_test.go` — simplified test server | −5/+5: removes `seedRoleDefinitions`, creates bare `&Server{store: s}` | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A6 (#1334).** Same pattern as policies test. |
| `pkg/hub/skill_registry_handlers_test.go` — routeGuard test replaced with inline test | −70/+20: removes `TestSkillRegistryRouteGuard_PermissionBased` (hub-admin binding + skill.register permission), adds `TestSkillRegistryCRUD_NonAdminRejected` (simpler inline admin check) | **YES — v2 is OLDER** | **DO NOT CARRY** | HIGH | **CONFIRMED** | **REVERTS P2-A6 (#1334).** v2 tests inline admin model; P2-A6 tested routeGuard permission model. |

---

## Table 2 summary

| Tranche | Modified hunks | Revert-risk count | Notes |
|---|---|---|---|
| **C** | 13 adapter files + 2 hunks in `handlers_broker_inbound.go` + 1 hunk in `handlers_agent_messaging.go` | 0 | Phase 11 broker edge — **the entire Phase 11 footprint is here, invisible to Table 1** |
| **D** | 6 hunks (`handlers_broker_inbound.go` + `cmd/message.go` + 3× `handlers_agent_messaging.go`) | 0 | Phase 7 validation wired to broker-inbound, CLI, agent-message, and broadcast paths |
| **E** | 2 hunks (`handlers_chat_v2.go` + `server.go`) | 0 | Phase 8 read-switch + divergence board route |
| **H** | 2 hunks (`server.go` + `route_metadata.go`) | 0 | Conversations resolve route + metadata |
| **F** | 5 hunks in `cmd/message.go` | 0 | Phase 10 + S8: deprecation system, help text, conversation references, flag hiding |
| **UNCLAIMED** | 22 hunks across 6 files | **7** | handlers_broker_inbound.go (3), handlers_agent_messaging.go (9), handlers_chat_v2.go (6), messagebroker.go (2), store.go (1: CountUnbackfilledMessages), message_store.go (1: CountUnbackfilledMessages). Seven hunks are security reverts; fifteen are forward changes. |
| **DO NOT CARRY** | 53 hunks across 13 files | **53** | See "By PR reverted" below. |
| **(cosmetic)** | 3 hunks across `models.go` (1) + `types.go` (1) + test infrastructure changes | 0 | Alignment/whitespace differences. Not functional. |
| **(infrastructure)** | 5 hunks across test files | 0 | `newTestStore` signature changes. v2 test helper API. |

**Total CONFIRMED revert-risk hunks: 60** across 16 files.

**By PR reverted (security/correctness):**
- **#1343 (B5 security)**: 8 production hunks + 12 test hunks = 20 total
  - Production: `messagebroker.go` (1), `handlers_agent_messaging.go` (7)
  - Tests: `messagebroker_test.go` (1), `chat_notifications_test.go` (1), `handlers_agent_messaging_test.go` (1 — covering 8 deleted test functions)
- **#1322 (DM ownership)**: 7 production hunks
  - `handlers_broker_inbound.go` (3), `handlers_agent_messaging.go` (2), `handlers_chat_v2.go` (2)
- **#1347 (ActionAttach)**: 3 production hunks
  - `handlers_agent_messaging.go` (1), `handlers_chat_v2.go` (2)
- **#1338 (DEF-31 defaultAgent)**: 4 production hunks in `handlers_chat_v2.go`
- **#1349 (B6/B7/B9 EnsureParticipant)**: 1 hunk in `store.go`

**By PR reverted (P2 feature system):**
- **P2-A1 through A6 (#1324–#1334)**: 45 Permission metadata fields + routeGuard Decide path + classification changes + 5 test files = ~12 hunks
- **P2-B1 through B3 (#1323–#1336)**: QuotaStore interface + models + routes + service + seeding = ~8 hunks
- **P2-C1 (#1348)**: RoleStore extensions + role routes = ~3 hunks
- **P2-D1 (#1346)**: auth/scopes endpoint + route metadata + test = ~3 hunks

**Independent restoration units (not hunks):**

| Unit | PR(s) | Files | Hunks | Complexity |
|---|---|---|---|---|
| **1. authenticatedSender** | B5 (#1343) | 2 (`handlers_agent_messaging.go`, `messagebroker.go`) | 8 production + 12 test | HIGH — restore function + rewire 7 call sites + restore 8 test functions |
| **2. parseDMKeyIDs + DM ownership checks** | #1322 | 3 (`handlers_chat_v2.go`, `handlers_agent_messaging.go`, `handlers_broker_inbound.go`) | 5 | MEDIUM — restore function + 4 callers across 3 files |
| **3. isDMParticipant kind-label** | #1322 | 1 (`handlers_chat_v2.go`) | 1 | LOW — one-line change |
| **4. validateDefaultAgent** | #1338 | 1 (`handlers_chat_v2.go`) | 4 | LOW — restore function + 3 call sites, all in one file |
| **5. ActionAttach checks** | #1347 | 2 (`handlers_agent_messaging.go`, `handlers_chat_v2.go`) | 3 | LOW — 3 independent authz calls |
| **6. EnsureParticipant** | #1349 | 1 (`store.go`) | 1 | LOW — interface method |
| **7. P2 authorization pipeline** | P2-A1–A6 | 5 (`route_metadata.go`, `route_classification_test.go`, `admin_maintenance_test.go`, `handlers_policies_test.go`, `skill_registry_handlers_test.go`) | ~12 | HIGH — routeGuard Decide path + 45 Permission fields + 5 test file reverts |
| **8. P2 quota subsystem** | P2-B1–B3 | 4 (`store.go`, `models.go`, `composite.go`, `server.go`) | ~8 | HIGH — full feature (interface + models + implementation + routes + seeding) |
| **9. P2 role management** | P2-C1 | 2 (`store.go`, `server.go`) | ~3 | MEDIUM — CRUD extensions + route registration |
| **10. P2 UAT scopes** | P2-D1 | 2 (`server.go`, `route_metadata.go`) | ~3 | LOW — one endpoint + metadata |

## Key findings from Table 2

1. **Phase 11 is entirely modifications.** 13 adapter files + 2 hunks in `handlers_broker_inbound.go` + 1 hunk in `handlers_agent_messaging.go` = the complete Phase 11 footprint. Table 1 has zero Phase 11 rows. Any manifest that counts only new files undercounts tranche C by its entire third phase.

2. **v2 reverts 6 distinct PR families, not one.** The full sweep found 60 CONFIRMED revert-risk hunks across 16 files. These fall into two categories:
   - **Security/correctness reverts** (5 PRs, 24 production hunks + 12 test hunks): B5 (#1343), #1322 (DM ownership), #1347 (ActionAttach), #1338 (DEF-31 defaultAgent), #1349 (EnsureParticipant).
   - **P2 feature reverts** (10 PRs, 24 hunks): The entire Permissions Phase 2 system — authorization pipeline (P2-A1–A6), quota subsystem (P2-B1–B3), role management (P2-C1), UAT scopes (P2-D1).

3. **10 independent restoration units, not 60 independent fixes.** The hunk count overstates the work. The authenticatedSender unit (1 function + 7 call sites + 8 test functions) is one act of restoration, not 20. The P2 quota subsystem (interface + models + store + routes + seeding) is one feature, not 8 hunks. See the restoration unit table for the actual work breakdown.

4. **The authenticatedSender unit is the structural root of B5.** All 8 B5 production hunks + 12 B5 test hunks stem from the deletion of one function. The function existed as the single point for deriving trusted sender identity. Without it, all call sites fall back to client-supplied SenderID — the pre-B5 vulnerability. Restoring B5 means restoring the function, rewiring 7 call sites, and restoring 8 test functions. This is one coordinated change, not a list of small fixes.

5. **The #1322 revert is coupled to Phase 11 but resolvable.** The DM ownership check (ThreadID-based participant identity) and Phase 11 resolution (Surface/ExternalRef routing) protect different things. They are orthogonal and composable: keep #1322's early SenderID caching and ownership check, add Phase 11 resolution after it. The late `GetUserByEmail` fallback drops out as redundant. Remedy documented in RESOLUTION notes on affected rows.

6. **The P2 series is a complete feature revert, not a security patch revert.** v2 predates the entire Permissions Phase 2 by design — P2 was not part of the messaging v2 scope. Carrying v2 would delete the P2 authorization pipeline, quota system, role management, and UAT scopes. The authorization impact is nuanced: without P2, RouteHubAdmin routes fall back to `requireAdmin` (coarser but not weaker — non-admin hub-admin-role users lose access entirely rather than getting scoped access).

7. **Test coverage for security fixes is deleted.** v2 removes 8 B5 test functions from `handlers_agent_messaging_test.go` (805 lines), 3 B5 tests from `chat_notifications_test.go` (150 lines), and 1 B5 test setup from `messagebroker_test.go`. These tests verify spoofed-sender prevention, forced-Broadcasted, self-skip-by-ID, and empty-SenderID warnings. The tranche C prohibition list must include preserving this test coverage — it is the safety net against future B5 regressions.

8. **Multi-phase files total 6.** `handlers_broker_inbound.go` (4 phases), `handlers_agent_messaging.go` (C+D+UNCLAIMED+DO-NOT-CARRY), `handlers_chat_v2.go` (E+UNCLAIMED+DO-NOT-CARRY), `server.go` (E+H+DO-NOT-CARRY), `route_metadata.go` (H+DO-NOT-CARRY), `cmd/message.go` (D+F). All require hunk-level decomposition.

9. **Two cosmetic-only files.** `models.go` and `types.go` show struct field alignment differences that are not functional. These are gofmt artifacts and do not affect behaviour.

10. **Completeness accounting.** 18 files with post-fork commits were identified (architect-corrected count). All 18 have been swept. Of these, 16 have at least one CONFIRMED revert-risk hunk. Two have NONE (`message_store.go` — only v2 additions; `types.go` — cosmetic only).

11. **The prohibition list (for tranche C spec).** Since messaging-v2 is abandoned as a source branch, every CONFIRMED hunk above becomes a prohibition in the tranche C specification: "this implementation must not remove X." The full list of things tranche C must not remove:
    - `authenticatedSender` function and all 7 call sites
    - `parseDMKeyIDs` function and all DM ownership checks (4 callers, 3 files)
    - `isDMParticipant` kind-label tightening
    - `validateDefaultAgent` function and 3 call sites
    - All 3 `ActionAttach` authorization checks (#1347)
    - `EnsureParticipant` store method (#1349)
    - `Broadcasted = true` server-side forcing
    - B5 test coverage (8 test functions in handlers_agent_messaging_test.go, 3 in chat_notifications_test.go, 1 setup in messagebroker_test.go)
    - P2 authorization pipeline (routeGuard Decide path + 45 Permission metadata entries)
    - P2 quota subsystem (QuotaStore + models + routes + seeding)
    - P2 role management (RoleStore extensions + routes)
    - P2 UAT scopes (auth/scopes endpoint)

---

## Architect's four known items — verified

1. **C is self-contradictory.** Confirmed. Line 1778 (Phases 6/9/11), line 5322–5323 (§2.6.4 phases 1–4 + DEF chain), and §5ca/§5co (unnamed file counts from `em9-unify`) are three incompatible definitions. This manifest uses line 1778 and flags every conflict.

2. **`broadcast.go` and `keys.go` overlap C and F.** Resolved by architect ruling: F is definitive. C's claim was a counting method that produced a number nobody mapped back to files. C's count in §5co is overstated by 4.

3. **`cmd/server_backfill*.go` silent-drop risk.** Confirmed. 4 files, `server_backfill.go` mentioned only as DEF-12 (line 10059) with no tranche letter. The other 3 are never mentioned at all (line 10063–10064). This is the only feature cluster with no tranche row.

4. **`cmd/deploy_instance{,_test}.go` is REVERT HAZARD.** Confirmed. Main deleted both in #1325 (line 1886). Marked NONE / DO NOT CARRY.

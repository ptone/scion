# TRANCHE-MANIFEST

**Scope caveat.** Table 1 (new files) covers files present on `origin/scion/messaging-v2`
and absent from `upstream/main`. It is silent on modifications to files that exist on both
trees. A file-existence table cannot see Phase 11 (broker edge), which lands entirely as
modifications, nor any other behavioural change that modifies an existing file. Table 2
(modifications) covers the broker-edge scope and hub/CLI files that Phase 11 touches;
it is not exhaustive across all 156 modified files.

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

**Post-fork commits found (2 files, 3 commits):**
- `pkg/hub/handlers_broker_inbound.go`: `b453a685` (#1322, "validate DM key ownership at
  message ingress — prevent cross-project injection", 2026-08-27 13:16Z)
- `pkg/hub/messagebroker.go`: `b3562fb1` (#1343, tranche B, 2026-08-28 07:40Z)
- No post-fork commits: all 13 extras adapter files, `cmd/message.go`

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
| `…/handlers_broker_inbound.go` — SenderID caching removal | −4: removes `req.Message.SenderID = senderUser.ID` and its comment, added by #1322 at lines 142–145 on main | NO | UNCLAIMED | LOW | **CONFIRMED** | **REVERTS #1322.** #1322 added this caching so the downstream DM ownership check could use it. v2 predates #1322 and has no equivalent. Removing the caching without removing the ownership check would break the check; v2 removes both — see next row. |
| `…/handlers_broker_inbound.go` — DM ownership check removal | −14: deletes the `parseDMKeyIDs` / DM ownership check block (lines 160–173 on main) | NO | UNCLAIMED | LOW | **CONFIRMED** | **REVERTS #1322 — SECURITY.** #1322 added this check to prevent cross-project DM injection: a broker plugin supplying a crafted `ThreadID` like `dm:agent:<victimAgent>:user:<attacker>` could inject messages into a DM the sender does not belong to. The check validates that the agent ID and user ID in the DM key match the actual resolved agent and the authenticated sender. v2 has no equivalent — zero grep hits for `parseDMKeyIDs` or `"DM thread_id does not match"`. v2's Phase 11 conversation resolution provides a *different* authorization model (conversation-based rather than ThreadID-based), but carrying the removal without the Phase 11 block leaves the endpoint unprotected. **Must not be carried independently.** Main has not removed this check by any other route. |
| `…/handlers_broker_inbound.go` — late SenderID resolution | −4/+9: re-adds `GetUserByEmail` fallback for empty SenderID (lines 283–289 on v2) that #1322 removed | NO | UNCLAIMED | LOW | **CONFIRMED** | **REVERTS #1322.** #1322 moved SenderID resolution earlier (into the permission check) and removed this fallback. v2 re-adds it because v2 predates #1322 and never had the early caching. Carrying this hunk alone is not dangerous, but it signals the #1322 revert since all three hunks are coupled. |

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

---

## Table 2 summary

| Tranche | Modified hunks | Revert-risk count | Notes |
|---|---|---|---|
| **C** | 13 files + 2 hunks in `handlers_broker_inbound.go` | 0 | Phase 11 broker edge — **the entire Phase 11 footprint is here, invisible to Table 1** |
| **D** | 2 hunks (`handlers_broker_inbound.go` + `cmd/message.go`) | 0 | Phase 7 validation wired to broker-inbound and CLI paths |
| **F** | 5 hunks in `cmd/message.go` | 0 | Phase 10 + S8: deprecation system, help text, conversation references, flag hiding |
| **UNCLAIMED** | 5 hunks across `handlers_broker_inbound.go` (3) and `messagebroker.go` (2) | **3** | Three hunks CONFIRMED revert of #1322 (DM ownership / cross-project injection). Two hunks are forward changes (signature fix, DEF-3). |
| **DO NOT CARRY** | 1 hunk in `messagebroker.go` | **1** | B5 security revert (#1343). |

**Total CONFIRMED revert-risk hunks: 4** (1 in `messagebroker.go` B5, 3 in `handlers_broker_inbound.go` #1322).

## Key findings from Table 2

1. **Phase 11 is entirely modifications.** 13 adapter files + 2 hunks in `handlers_broker_inbound.go` = the complete Phase 11 footprint. Table 1 has zero Phase 11 rows. Any manifest that counts only new files undercounts tranche C by its entire third phase.

2. **Two security fixes are reverted by v2, not one.** `messagebroker.go` reverts B5 (#1343, broadcast ingress hardening). `handlers_broker_inbound.go` reverts #1322 (DM key ownership validation, cross-project injection prevention). Both are the same shape: v2 predates a security fix that main gained after the fork. The #1322 revert is the more dangerous of the two: B5's self-skip failure causes an agent to see its own broadcast (nuisance), while #1322's ownership check failure allows cross-project DM injection (data-integrity/confidentiality).

3. **The #1322 revert is coupled to Phase 11.** v2 removes the DM ownership check because Phase 11 conversation-based routing replaces ThreadID-based DM ownership. But carrying the removal without Phase 11 leaves the endpoint unprotected. The three #1322 hunks and the Phase 11 hunks in `handlers_broker_inbound.go` are a single atomic unit: carry all or none. Carrying Phase 11 without the #1322 removal might also be safe (double protection), but carrying the removal without Phase 11 is a vulnerability.

4. **`handlers_broker_inbound.go` spans four phases in one file.** Phase 11 (C), Phase 7 (D), Phase 5 dual-write (UNCLAIMED), and #1322 revert (CONFIRMED). A per-file tranche assignment is impossible; the file must be decomposed per hunk when cutting any tranche that touches it.

5. **`cmd/message.go` spans two phases.** Phase 10/S8 (F) and Phase 7 (D). The Phase 7 `ValidateLegacyMessage` calls depend on `pkg/messaging` types that tranche D's new files define — carrying F without D's types would fail to compile.

6. **Two forward changes have no tranche.** The `ResolveOrCreateDMConversation` signature fix and DEF-3 `CheckConversationConsistency` additions in `messagebroker.go` are absent from main, are not reverts (purely additive or a later refinement), and are claimed by no tranche. Both are Phase 5 territory that tranche B did not carry.

---

## Architect's four known items — verified

1. **C is self-contradictory.** Confirmed. Line 1778 (Phases 6/9/11), line 5322–5323 (§2.6.4 phases 1–4 + DEF chain), and §5ca/§5co (unnamed file counts from `em9-unify`) are three incompatible definitions. This manifest uses line 1778 and flags every conflict.

2. **`broadcast.go` and `keys.go` overlap C and F.** Resolved by architect ruling: F is definitive. C's claim was a counting method that produced a number nobody mapped back to files. C's count in §5co is overstated by 4.

3. **`cmd/server_backfill*.go` silent-drop risk.** Confirmed. 4 files, `server_backfill.go` mentioned only as DEF-12 (line 10059) with no tranche letter. The other 3 are never mentioned at all (line 10063–10064). This is the only feature cluster with no tranche row.

4. **`cmd/deploy_instance{,_test}.go` is REVERT HAZARD.** Confirmed. Main deleted both in #1325 (line 1886). Marked NONE / DO NOT CARRY.

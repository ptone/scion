# TRANCHE-MANIFEST

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

**NOTE — C is self-contradictory.** Line 1778 defines C as "Phases 6, 9, 11". Line 5322–5323
redefines C as "§2.6.4 phases 1–4 + the DEF-20/21/22/23 chain + DEF-27". §5ca (line 5994) and
§5co (line 6958) compute C entirely from `ca-msg-em9-unify` as unnamed counts ("44 safe adds",
"8 cmd" adds). The three definitions are never reconciled. This manifest uses the **line 1778
definition** (phase-level) as the authoritative source, and flags conflicts below.

---

## Code files (32)

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
| `cmd/broadcast.go` | C/F | line 1778 (C: "broker edge + 5 adapters") vs line 1781 (F: "CLI subcommands") | LOW | **OVERLAP.** §5ca line 6013 counts it in C's "8 cmd" adds. Line 1781 defines F as "CLI subcommands." `broadcast` is a CLI subcommand but also a broker-edge adapter. Architect already flagged: "tranche F content sitting inside tranche C's count with neither tranche naming it" (line 10070–10072). **Needs architect ruling.** |
| `cmd/broadcast_test.go` | C/F | same as `broadcast.go` | LOW | Follows `broadcast.go` |
| `cmd/keys.go` | C/F | line 1778 (C) vs line 1781 (F) | LOW | **OVERLAP.** Same ambiguity as `broadcast.go`. §5ca line 6013 counts it in C's "8 cmd" adds. Line 1781 captures it as F "CLI subcommands". Architect flagged at line 10070–10072. **Needs architect ruling.** |
| `cmd/keys_test.go` | C/F | same as `keys.go` | LOW | Follows `keys.go` |
| `cmd/message_deprecation_test.go` | F | line 3250: "DEF-25's file rides tranche F" | HIGH | **Only file-level tranche claim in the entire document.** DEF-25 closed 21:47Z (line 1913). |
| `cmd/doc_syntax_test.go` | F | line 1781: "Phase 10 + S8: help grammar"; line 2465/4299: doc syntax test for design doc, in S8/DEF-13 context | MEDIUM | S8 = DEF-13 help text work (line 1781 "S8"). Tests design doc syntax against cobra help. |
| `cmd/message_help_test.go` | F | line 1781: "Phase 10 + S8: help grammar"; line 4184: "cmd/message_help_test.go is dirty" in format-check context | MEDIUM | Help text test; referenced in DEF-17 format check (Phase 10 / S8 territory). |
| `cmd/message_convref_test.go` | NONE | never mentioned (line 10065) | HIGH | No tranche claim exists. The 5 "never mentioned" files include this one. Conversation-reference test; may belong to F (CLI) but no basis in the doc. |
| `cmd/deploy_instance.go` | NONE | line 1886: "re-adds cmd/deploy_instance.go (+828)"; line 10061–10062: "correctly unclaimed — main deleted them in #1325, revert hazard" | HIGH | **DO NOT CARRY — REVERT HAZARD.** Main deleted this file in #1325 (the Cloud Run move). Porting it re-adds 828 lines of deleted deploy tooling. The branch carries it only because its merge-base predates #1325. |
| `cmd/deploy_instance_test.go` | NONE | same as `deploy_instance.go` | HIGH | **DO NOT CARRY — REVERT HAZARD.** +736 lines. Same #1325 deletion. |
| `cmd/server_backfill.go` | NONE | line 10059: "DEF-12, no tranche letter"; line 5084: DEF-12 spec | HIGH | **HIGHEST SILENT-DROP RISK** (line 10074). DEF-12 built the backfill CLI subcommand. No tranche row in the table. The 4-file `server_backfill*` cluster is the only cluster whose feature has no tranche at all. |
| `cmd/server_backfill_test.go` | NONE | never mentioned (line 10063) | HIGH | Part of the `server_backfill*` cluster. No tranche claim. |
| `cmd/server_backfill_volume_test.go` | NONE | never mentioned (line 10063) | HIGH | Part of the `server_backfill*` cluster. |
| `cmd/server_foreground_backfill_test.go` | NONE | never mentioned (line 10064) | HIGH | Part of the `server_backfill*` cluster. |
| `pkg/hub/handlers_conversations_resolve.go` | NONE | line 10060: "live G-1 HIGH finding, sits in the E/F seam claimed by neither"; line 4930: G-1 finding detail | HIGH | **ARCHITECT DECISION REQUIRED.** G-1 (line 4930): the resolve endpoint lets the caller choose who they are — `sender_principal_kind`/`sender_principal_id` read from request body, no verification against authenticated caller. em4's fix (line 4887) deleted the body fields, but this file sits in the E/F seam: not Phase 8 (read-switch) and not Phase 10 (CLI). Neither tranche names it. |
| `pkg/hub/handlers_conversations_resolve_test.go` | NONE | never mentioned (line 10065) | HIGH | Follows `handlers_conversations_resolve.go`. Same E/F seam, same no-tranche gap. |

## Non-code file (1)

| file | tranche | basis | confidence | notes |
|---|---|---|---|---|
| `pkg/messaging/VALIDATION_EXEMPTIONS.md` | D | line 2158: AC-8c verified against this file in S3/Phase 7 review | HIGH | Documents three server-generated emitters exempted from the Phase 7 validation choke point. Ships with `validate.go`. |

## Design log files (44)

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

## Summary by tranche

| Tranche | Code files | Notes |
|---|---|---|
| **C** | 8 | envelope (4) + delivery (4) |
| **C/F** | 4 | `broadcast.go`, `broadcast_test.go`, `keys.go`, `keys_test.go` — **needs ruling** |
| **D** | 5 + 1 non-code | validate (4) + integration test (1) + `VALIDATION_EXEMPTIONS.md` (1) |
| **E** | 3 | divergence board (2) + read-switch test (1) |
| **F** | 3 | `message_deprecation_test.go`, `doc_syntax_test.go`, `message_help_test.go` |
| **NONE** | 9 | deploy_instance (2, REVERT HAZARD) + server_backfill (4, SILENT-DROP RISK) + handlers_conversations_resolve (2, G-1/ARCHITECT DECISION) + message_convref_test (1, never mentioned) |
| **DOCS** | 44 | `.design/project-log/*` — noise per §5ca |

## Architect's four known items — verified

1. **C is self-contradictory.** Confirmed. Line 1778 (Phases 6/9/11), line 5322–5323 (§2.6.4 phases 1–4 + DEF chain), and §5ca/§5co (unnamed file counts from `em9-unify`) are three incompatible definitions. This manifest uses line 1778 and flags every conflict.

2. **`broadcast.go` and `keys.go` overlap C and F.** Confirmed. Both are CLI subcommands (F by line 1781) but counted inside C's "8 cmd" adds (§5ca line 6013). Neither tranche C nor tranche F names them explicitly. Marked C/F with LOW confidence pending ruling.

3. **`cmd/server_backfill*.go` silent-drop risk.** Confirmed. 4 files, `server_backfill.go` mentioned only as DEF-12 (line 10059) with no tranche letter. The other 3 are never mentioned at all (line 10063–10064). This is the only feature cluster with no tranche row.

4. **`cmd/deploy_instance{,_test}.go` is REVERT HAZARD.** Confirmed. Main deleted both in #1325 (line 1886: "re-adds `cmd/deploy_instance.go` (+828) and `deploy_instance_test.go` (+736), deletes `deploy_script_test.go` (−655) and `deploy_script_pin_test.go` (−210)"). The branch carries them only because its merge-base predates #1325. Marked NONE / DO NOT CARRY.

## Additional findings

5. **`handlers_conversations_resolve{,_test}.go` — E/F seam, no tranche.** Per line 10060: "sits in the E/F seam claimed by neither." The handler carries unresolved G-1 HIGH security finding (line 4930). em4's fix (line 4887) deleted the body sender fields. This file is not Phase 8 (read-switch) and not Phase 10 (CLI). **Architect decision required on tranche assignment.** If carried, G-1's fix status must be re-verified on the carrying branch.

6. **`cmd/message_convref_test.go` — never mentioned.** Among the 5 files with zero design-doc references (line 10065). Tests conversation-reference resolution; could logically belong to F (CLI) alongside `message_help_test.go` and `message_deprecation_test.go`, but no basis exists in the document.

7. **No Phase 11 (broker edge) new files.** Tranche C at line 1778 includes Phase 11 "broker edge + 5 adapters." All broker-edge work appears to land as modifications to existing files (`handlers_broker_inbound.go`, `messagebroker.go`) rather than new files. Zero v2-only files map to Phase 11 by name. The "5 adapters" are the extras plugins which are also modifications.

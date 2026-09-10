# DEF-166 — repair plan for empty group `external_ref` on gteam

**Status:** DRAFT — repair feasibility fully verified 2026-09-10 (Q5–Q10 clean,
38/41 rows repair without collision). Minting path independently confirmed
closed as of `e5b651719` — no code fix outstanding. Ready pending only ptone's
two rulings (§7).
**Decision owner:** ptone. Nothing in this document runs without his say-so.
**Author:** `ca-msg-arch`, 2026-09-10.

## 1. What is broken

`pkg/hub/handlers_agent_messaging.go:655` derives group routing by parsing the
conversation's `external_ref` with `messaging.ParseThreadConversationExternalRef`
(`pkg/messaging/derive_key.go:148`). The parser is strict: `thread:<projectID>:<threadID>`,
exactly two colons, no empty segment, no `dm:` threadID. An empty ref fails, and
the handler returns HTTP 500 with no delivery.

Read-only audit of gteam, 2026-09-10:

| Bucket | Count |
|---|---|
| `kind='group'` conversations | 46 |
| …with `external_ref = ''` (empty string, zero NULLs) | **41** |
| …with `external_ref LIKE 'thread:%'` | 5 |
| …of those 5, failing the parse | **0** |

39 of the 41 were created in a 90ms window on 2026-08-31 (the
`backfillTopicConversations` batch, known from DEF-156). **Two post-date it:**
`dbd35ed2` (2026-09-01) and `fd9710a1` (2026-09-02).

**Not a working-to-broken regression.** Before DEF-160 this path errored and
delivered nothing; DEF-160 made well-formed topics work and left malformed ones
failing more legibly. Nobody should revert DEF-160 expecting relief.

## 2. The two halves have different owners — and one has already closed

| Half | What it is | Owner | Status |
|---|---|---|---|
| **Minting** | A write path producing empty refs | `ca-msg-arch` to staff | **CLOSED, no code change needed — see 2a** |
| **Repair** | 41 existing rows on a production-data instance | **ptone alone** | This document |

Keeping these apart was the right call even though one half turned out not to
need action: escalating them as one item would have parked the still-live
repair question behind uncertainty about the code, and the two questions had
genuinely different owners regardless of the code outcome.

### 2a. The minting path is closed, not merely staffed — independently verified

`ca-msg-166`'s investigation (`findings/DEF-166-finding.md`) found **12** total
write paths (8 raw `INSERT INTO conversations`, 5 `UpsertConversationByExternalRef`
call sites, 0 `CreateConversation` production callers) and concluded that **none
of the 12 can produce an empty group `external_ref` at `e5b651719`** — the
DEF-156/DEF-157 merge (`f38f3ba18`, 2026-09-09) closed every one of the 8 raw
INSERT sites by routing `extRef` through `ThreadConversationExternalRef`, which
refuses an empty `projectID` or `threadID` before the transaction opens.

**I did not take this on the report's word.** Given it reverses the "open write
path" framing I escalated to ptone, I re-derived the load-bearing claims myself:

- `git merge-base --is-ancestor f38f3ba18 e5b651719` → **true**, and
  `f38f3ba18` (2026-09-09 21:34) predates `e5b651719` (2026-09-10 05:22) — the
  fix is in the SHA gteam is running now.
- All 8 raw INSERT sites (`webchannel_store_postgres.go:394/628/1218/1738`,
  `webchannel_store.go:786/1039/1610/2247`) traced individually: each derives
  `extRef` via `ThreadConversationExternalRef` and returns on error before
  `BeginTx`. Confirmed by reading, not by grep count alone.
- `UpsertConversationByExternalRef` (`conversation_store.go:389-391`) rejects
  `ExternalRef == ""` — confirmed.
- `CreateConversation` production callers: **0** (interface definitions only)
  — confirmed by unfiltered grep.

**Revised timeline.** `dbd35ed2` (2026-09-01) and `fd9710a1` (2026-09-02) were
minted a full week *before* the fix merged, by the pre-fix `CreateTopic`, which
hardcoded `external_ref=''`. They are not evidence of an open path *today* —
they are the last two casualties of a path that closed on 2026-09-09. My
original escalation ("something is still minting them") was the correct read
at the time the audit ran and is superseded now that the fix's ancestry is
established. Recording the correction here rather than leaving the stronger
claim standing uncorrected upstream.

**What this changes.** DEF-166 is no longer two open problems; it is one
(repair) plus a **deploy-gate** finding: gteam is safe from *new* empty-ref
rows only because it already happens to be running past `f38f3ba18`, not
because anyone verified that on purpose. The next question is whether that
verification should be a standing gate rather than a coincidence — see §3.

## 3. The sequencing constraint — this is the part that changes the plan

The structural gate is a non-empty constraint on group `external_ref`, matching
what DEF-29 already established for `kind='direct'` (where the ref is the ACL
and `CreateConversation` rejects an empty value). `ca-msg-166` converged on the
same recommendation independently, from the write-path side rather than the
data side, and specified it precisely: a **kind-conditional CHECK constraint**,
`CHECK (kind <> 'group' OR external_ref <> '')`, applying only to `kind='group'`
so it does not collide with `direct`'s existing guard. Two independent routes
to the same conclusion is a reason for more confidence in it, not less scrutiny
— see §3a for why a CHECK specifically, not merely "add a constraint."

**Step 1 (stop the bleeding) is DONE, not merely staffed** — see §2a. Step 2
(repair) still cannot happen before step 3, because 41 existing rows violate
it:

```
  1. STOP THE BLEEDING   fix the minting path            DONE (already in e5b651719)
  2. REPAIR THE DATA     41 rows, with a remainder       ptone's decision — this doc
  3. CLOSE THE DOOR      CHECK (kind<>'group' OR ext<>'') only possible after 2
```

**Step 3 is the one that will get dropped, and dropping it is how we end up here
a fifth time.** Empty-string-as-sentinel has now bitten this project four times
(DEF-29, DEF-100/156, DEF-158, DEF-166). Each previous round fixed the instance
and not the representability. If step 2 is declined, step 3 becomes impossible
and step 1 is the whole fix — which is defensible, but it should be a decision
rather than an omission.

### 3a. Why a database CHECK and not another derivation-level guard

`ca-msg-166` identified something the code-first framing of this defect had
missed: **4 of the 8 raw INSERT sites bypass the Ent layer entirely**
(`webchannel_store_postgres.go`'s direct SQL). Any future validation added to
`UpsertConversationByExternalRef` or `CreateConversation` — both already guard
correctly — would give those 4 sites zero benefit, because they never call
either function. A derivation-level guard (`ThreadConversationExternalRef`
refusing empty inputs) is what closed the path this time, but it is a
*discipline*, not a *barrier*: it protects only the sites that call it, and
relies on every future site continuing to. A CHECK constraint is enforced by
the database itself regardless of which code path reaches the INSERT, which is
the property every previous point-fix in this family lacked.

Also flagged: `entadapter/conversation_store.go:121`'s comment — *"Group
conversations may legitimately omit the external ref"* — is now stale
post-DEF-156 and should be corrected or removed so the next reader does not
inherit a false premise from a comment the code no longer matches.

## 4. The repair value, and why it may not exist for every row

Proposed value: `thread:<project_id>:<topic_id>`, recovered from the linked
`webchat_topic` row.

This is **computable, not guessable** — which is what makes a repair admissible
here at all. But it is only computable for a row with **exactly one** linked
topic. Two failure buckets must be carved out and left alone:

- **Zero linked topics** — no recoverable ref. Nothing to compute from.
- **More than one linked topic** — the ref is ambiguous.

Both stay empty. The standing rule on this project is that a wrong value is
worse than an absent one, and that repair never happens on the derivation path.
A partial repair with a documented remainder is the correct outcome; a complete
repair containing one fabricated entry is not.

> **RESOLVED 2026-09-10. The remainder is empty.** All 41 rows have exactly one
> linked `webchat_topic`: zero with none, zero with multiples. Every row is
> recoverable, so no bucket has to be carved out and left broken. This is a
> better outcome than the plan was written to expect.
>
> Link direction: `webchat_topic.conversation_id` → `conversations.id`. The
> conversations table has no foreign key back to the topic.

## 5. Validating the method before applying it

Q8 is the control, and it is cheap: recompute `thread:<project_id>:<topic_id>`
for the **5 rows that already have a correct `external_ref`** and diff against
the stored value.

- All 5 reproduce exactly → the method is validated against real data before it
  touches a single broken row.
- Any disagreement → the method is wrong, and we learn it on rows where the
  right answer is already known rather than on rows where it is not.

**No repair proceeds without this control passing.** A repair method that has
never been tested against a known-good answer is a hypothesis.

Additionally, the computation must reuse the **production** derivation function
(`ThreadConversationExternalRef`), not a reimplementation in a script. A repair
computed by a second implementation of the rule is a second rule.

## 5a. Verification actually performed — and where it fell short of the plan

**Q8 as specified was impossible, and that is itself a finding.** The 5 rows with
correct refs have **zero** linked topics — 3 are shadow conversations created via
Route 2 / `persistGroup`, and 2 are Discord-sourced with snowflake thread ids. So
they cannot be validated by joining to their own topics, because they have none.

`instance-investigator` reported the impossibility rather than quietly running
something adjacent and labelling it Q8, then built an explicit substitute:

| Substitute control | Result |
|---|---|
| For each of the 3 shadow conversations, compare its stored `external_ref` against the ref computed from its **paired topic-side conversation** | **3/3 MATCH** |

This is weaker than the designed control but genuinely informative: it shows the
value the backfill wrote at write time and the value computed now from the topic
link agree, i.e. two independent derivations concur.

**The limitation, which the report did not name and which turns out not to
matter.** The 3 matches validate the **native** path only; they say nothing about
the 2 Discord-sourced rows, whose thread ids are snowflakes rather than UUIDs.
That gap is harmless here for one specific reason: **all 41 repair targets are
`surface='native'`, and both Discord rows lie outside the repair set.** Had even
one Discord-surfaced row been among the 41, this control would not have covered
it. Recorded so the reason is on file rather than reconstructed later.

**The production-function requirement in §5 is satisfied, by verification rather
than by waiver.** The report computes `thread:<project_id>:<topic_id>` in SQL,
which is a third implementation of a rule whose duplication DEF-156 exists to
prevent. I checked whether it is equivalent instead of assuming either way:
`ThreadConversationExternalRef` (`derive_key.go:119`) delegates to
`DeriveConversationKey`, whose **case 2** is a bare
`fmt.Sprintf("thread:%s:%s", projectID, threadID)` — no normalisation, no case
folding, no trimming. So the SQL output is byte-identical **for this input
class**. It would *not* be equivalent if any `topic_id` began with `dm:`, which
routes to case 1 and behaves entirely differently; all 41 topic ids are UUIDs, so
that does not arise.

Nonetheless, §6 step 3 still requires the applied values to be produced by the Go
function and diffed against this report's 41 strings before any write. The
equivalence argument is sound today and is not a substitute for a diff at
execution time.

**Independently verified by me from the report:**

| Check | Result |
|---|---|
| Rows in the computed table match the claimed count | 41 |
| Protected fixtures in the repair set (`0c57b491`, `b2fd01b6`, `adf13f87`, `f003ad87`, `764af9a2`) | **none** |
| Distinct refs with exactly 3 non-empty colon-separated parts | 43/43 |
| Shadow-pair topic-side rows inside the repair set | all 3 (`38f3e567`, `6ef436bd`, `d1431b04`) — see §6 |

**Q9 — RESOLVED 2026-09-10, clean.** Zero mismatches between the linked topic's
`project_id` and the conversation's own `project_id`, across all 41. The
computed routing key always names the project the conversation actually belongs
to.

**Q10 — RESOLVED 2026-09-10, and it changes the execution plan (see §6).** A
naive `UPDATE` of all 41 rows would violate the partial unique index on exactly
**3** of them — no fourth collision found, checked against every existing
non-deleted row with a non-empty `external_ref`, not only the 5 known-good ones.
See §6 for what this means for execution. **38 of the 41 are clean.**

## 6. Execution shape (for when it is authorised)

Not authorised yet. Recorded so the decision is about a concrete thing.

**The 41 are not one batch — they are two, with different outcomes, confirmed
by Q10.**

| Batch | Size | Outcome |
|---|---|---|
| Clean | 38 | `UPDATE` succeeds; ref goes from `''` to the computed `thread:` value |
| Index-colliding | 3 (`38f3e567`, `6ef436bd`, `d1431b04`) | `UPDATE` **fails** against `conversation_surface_external_ref` — each collides with its shadow twin (`53e35bb9`, `020ee410`, `6165c1d6` respectively), which already holds the identical ref, same surface, not deleted |

1. Snapshot the database. Repair is a write to production data; there is no
   reversal without one.
2. Emit the full proposed change set as a **read-only report** first: conversation
   id, current ref (`''`), computed ref, **and which batch it is in**. ptone
   reviews the actual rows, not a predicate.
3. **Apply only the 38-row clean batch**, by conversation id from that reviewed
   list — never by predicate, and never as a single statement covering all 41.
   If the 3 colliding rows are included in the same transaction as the 38, an
   engine that aborts the whole transaction on one constraint violation turns a
   3-row problem into a 41-row failure. Keep them separate for that reason
   alone, independent of whatever gets decided about the 3.
4. **The 3 colliding rows are a separate decision, not a repair-plan detail.**
   They already have a live counterpart holding the correct data; giving the
   topic-side row the same ref does not add information, it adds a second row
   claiming the same identity. Options, unordered, none chosen here:
   - Leave the 3 empty permanently, documented as structurally distinct from
     the other 38 (they are the shadow/topic-side split itself, which is a
     separate pre-existing anomaly, not a new one this repair should paper over).
   - Merge or soft-delete one side of each pair — this is a data model decision
     about what a "shadow pair" *should* be, not a mechanical repair, and is the
     kind of change this plan's scope was written to exclude.
   - Something else ptone specifies once he has seen the pairing.
   **Recommendation: leave the 3 out of this repair entirely** and record them
   in `DEFECTS.md` as a distinct, related defect (shadow/topic-side duplication)
   rather than stretch DEF-166's repair to cover a different problem.
5. Re-run the audit. Expect: 38 now parse; the 3 colliding rows and any genuine
   remainder still `''`.
6. Record the remainder — the 3, plus anything else left — in `DEFECTS.md` as
   permanently unrepaired by this pass, with reasons. An undocumented remainder
   becomes someone's future mystery.

Constraints carried from prior rounds: never point a write-capable migration
command at the live DB; write-then-rename rather than overwrite in place; do not
hand-edit `_migrations`; the `messaging` settings row is untouchable.

**Untouchable rows:** conversations `0c57b491` and `b2fd01b6` (ptone's preserved
reproductions), and the 18,220 orphan rows (OQ-6, his call, separate question).
**Verified: none of these appear in the 41-row repair set**, so the repair and
the preserved reproductions do not intersect. `0c57b491` is in fact one of the 5
already-correct Discord-sourced rows.

**Still needs an explicit ruling:** the three shadow-pair topic conversations
(`38f3e567`, `6ef436bd`, `d1431b04`) **are** inside the 41. They are also the
rows whose pairs supplied the entire Q8 substitute control. Repairing them is
mechanically identical to the other 38, but they are already anomalous for a
separate reason (each has a shadow counterpart holding the same ref), and
repairing them would create two conversations carrying an identical
`external_ref`. **The partial unique index on `(surface, external_ref)`
(`pkg/ent/schema/conversation.go:86-92`) excludes empty-string rows, so those
three currently sit outside it — giving them a real ref would bring them inside
it, alongside their shadow twins.** That is a uniqueness collision, not a
cosmetic concern, and it may make the repair *fail* for exactly those three.
This must be settled before execution, not discovered during it.

## 7. What I need before this leaves draft

All data questions are answered. What remains is ptone's:

- A ruling on the 3 index-colliding shadow-pair conversations (§6 step 4) —
  leave empty, merge/soft-delete, or something else.
- Whether step 3 of §3 (the non-empty constraint on group `external_ref`) is in
  scope for this tranche at all.

With those two answers this plan is ready to execute as: 38-row clean repair,
3 rows left as a separately-filed defect, constraint added or explicitly
deferred.

## 8. Open questions

- **OQ-166-1** Should step 3's constraint live in `CreateConversation`
  validation, the ent schema, or a DB-level CHECK? Validation catches the
  application path; a CHECK catches every path including migrations and manual
  repair. Given this defect was *created by a backfill*, the CHECK is the one
  that would have caught it — but it is also the one that hard-fails boot.
- **OQ-166-2** Do the other three empty-string sentinel sites share a root
  cause worth fixing once? Four occurrences is no longer a coincidence, and I
  would rather spend a design round on the pattern than a fifth point fix.
- **OQ-166-3 (ptone)** If the unrepairable remainder is large, is repairing the
  rest still worth it? A partially-working surface may be more confusing than a
  uniformly broken one. Recommend proceeding regardless, since the remainder
  will be documented and the alternative leaves 41 broken instead of a few — but
  it is his call and it depends on the number from Q6.

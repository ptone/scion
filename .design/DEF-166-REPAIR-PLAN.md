# DEF-166 — repair plan for empty group `external_ref` on gteam

**Status:** DRAFT — awaiting feasibility data (Q5–Q8) from `instance-investigator`.
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

## 2. The two halves have different owners

| Half | What it is | Owner | Status |
|---|---|---|---|
| **Minting** | A live write path still producing empty refs — proven by the two post-backfill rows | `ca-msg-arch` to staff | Brief written (`briefs/DEF-166-EMPTY-REF-MINT.md`), undispatched pending the platform 403 |
| **Repair** | 41 existing rows on a production-data instance | **ptone alone** | This document |

Keeping these apart is the point. Escalating them as one item would have parked
the code fix behind a data decision; fixing them as one item would have meant
touching gteam without asking.

## 3. The sequencing constraint — this is the part that changes the plan

The obvious structural gate is a non-empty constraint on group `external_ref`,
matching what DEF-29 already established for `kind='direct'` (where the ref is
the ACL and `CreateConversation` rejects an empty value).

**That gate cannot be added first.** 41 existing rows violate it. Adding the
constraint before the repair either fails the migration or fails boot. So the
three steps are strictly ordered:

```
  1. STOP THE BLEEDING   fix the minting path            (code — no permission needed)
  2. REPAIR THE DATA     41 rows, with a remainder       (ptone's decision — this doc)
  3. CLOSE THE DOOR      non-empty constraint on group   (only possible after 2)
```

**Step 3 is the one that will get dropped, and dropping it is how we end up here
a fifth time.** Empty-string-as-sentinel has now bitten this project four times
(DEF-29, DEF-100/156, DEF-158, DEF-166). Each previous round fixed the instance
and not the representability. If step 2 is declined, step 3 becomes impossible
and step 1 is the whole fix — which is defensible, but it should be a decision
rather than an omission.

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

> Pending: Q6 asks for exactly these rows. Until it returns, **the size of the
> unrepairable remainder is unknown and this plan is not executable.** I will not
> put a number in front of ptone that I have not seen.

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

## 6. Execution shape (for when it is authorised)

Not authorised yet. Recorded so the decision is about a concrete thing.

1. Snapshot the database. Repair is a write to production data; there is no
   reversal without one.
2. Emit the full proposed change set as a **read-only report** first: conversation
   id, current ref (`''`), computed ref. ptone reviews the actual rows.
3. Apply only to the exactly-one-linked-topic bucket, by conversation id, from
   that reviewed list — never by predicate.
4. Re-run the audit. Expect: 41 − |remainder| now parse; remainder still `''`.
5. Record the remainder in `DEFECTS.md` as permanently unrepaired, with reasons.
   An undocumented remainder becomes someone's future mystery.

Constraints carried from prior rounds: never point a write-capable migration
command at the live DB; write-then-rename rather than overwrite in place; do not
hand-edit `_migrations`; the `messaging` settings row is untouchable.

**Untouchable rows:** conversations `0c57b491` and `b2fd01b6` (ptone's preserved
reproductions), and the 18,220 orphan rows (OQ-6, his call, separate question).
The three known shadow-pair topic conversations (`38f3e567`, `6ef436bd`,
`d1431b04`) are among the 39 batch-created rows and need an explicit ruling
before inclusion — they are already anomalous for a different reason.

## 7. What I need before this leaves draft

- **Q5** — conversation id + linked `webchat_topic` id/project_id for all 41,
  and which direction the link runs.
- **Q6** — the zero-link and multi-link buckets, listed explicitly.
- **Q7** — computed `thread:<project_id>:<topic_id>` per repairable row.
- **Q8** — the 5-row control result.
- **ptone** — a ruling on the three shadow-pair conversations, and on whether
  step 3 (the constraint) is in scope at all.

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

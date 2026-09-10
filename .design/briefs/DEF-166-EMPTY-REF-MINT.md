# DEF-166 — find the code path still minting group conversations with empty `external_ref`

You are an **investigator** agent. Fresh context. **You do not write production
code.** Your deliverable is a written finding with `file:line` and a recommended
fix for me to route. If you find yourself editing a non-test file, stop.

## Read first

1. `/scion-volumes/scratchpad/projects/ca-msg-arch/DEFECTS.md` — search for
   DEF-29, DEF-100, DEF-156, DEF-158. Empty-string-as-sentinel is this project's
   repeat offence and this is the fourth sighting. Read what was concluded the
   first three times before re-deriving it.
2. `/scion-volumes/scratchpad/projects/ca-msg-arch/briefs/_GATE-APPARATUS.md`.

## Base

Read against `scion/tranche-g` @ `e5b651719`. Read-only worktree is fine.

## The established facts — do not re-derive these

A read-only audit of the live gteam database (2026-09-10) found:

- 46 conversations with `kind='group'`.
- **41 have `external_ref = ''`** — empty string, zero NULLs.
- The other 5 have `external_ref LIKE 'thread:%'` and all 5 parse cleanly.
- 39 of the 41 were created in a 90ms window on 2026-08-31 — the boot backfill
  (`backfillTopicConversations`), already understood from DEF-156.
- **Two were created afterwards: one 2026-09-01 13:43:52 (`dbd35ed2`), one
  2026-09-02 19:18:54 (`fd9710a1`).** All 41 are `surface='native'`.

Those two rows are the whole reason you exist. The batch is history; something in
the code is **still minting empty refs**, and that is a live write path.

Why it matters: `pkg/hub/handlers_agent_messaging.go:655` parses this ref with
`messaging.ParseThreadConversationExternalRef`
(`pkg/messaging/derive_key.go:148`) to route a group reply. The parser is strict.
An empty ref yields HTTP 500 and no delivery. So ~89% of gteam's group
conversations currently cannot be replied to by `conv:<uuid>`.

## Your question

**Which write paths can insert a `kind='group'` conversation with an empty
`external_ref`, and which one produced those two rows?**

Known conversation-creation routes, as a starting point and not as a limit:

- Route 1 — `CreateTopic` / `EnsureGeneralTopic` / `backfillTopicConversations`
- Route 2 — `ResolveOrCreateThreadConversation` / `ResolveOrCreateConversationByKey`
- Route 3 — `BackfillService.persistGroup`
- Route 4 — `PromoteDM`

One lead worth checking early, recorded earlier in this project and never
followed up: **`CreateTopic` and `PromoteDM` are byte-identical at the INSERT.**

Enumerate the paths exhaustively rather than stopping at the first plausible one.
A second uncovered write path found later is much more expensive than a thorough
sweep now.

## Constraints that are not negotiable

1. **Do not touch the gteam VM or its database.** You have the audit above;
   that is all the production data you get. If you need another query, ask me and
   I will route it to `instance-investigator`. ptone's standing instruction is
   that nothing on gteam changes without telling him first.
2. **Do not propose repairing the 41 existing rows.** That is a live-data
   decision reserved to ptone and I am writing him a plan separately. Your scope
   is the *code* that mints new ones.
3. **A `kind='direct'` conversation's `external_ref` is its ACL.** Do not
   propose any change that touches the direct path, relaxes
   `CreateConversation`'s rejection of an empty `external_ref` for
   `kind='direct'`, or makes any parser tolerant. Group refs are routing, not
   authorization — keep that distinction explicit in your write-up, because the
   two look alike and the consequences do not.
4. **Do not make the reader tolerant of an empty ref** as your recommendation.
   The standing principle on this project is to fix the value at derivation.
   If you believe tolerance is genuinely right here, you may argue it — but
   argue it, do not assume it.

## Method

- Every symbol you name carries `file:line`.
- Where you claim "there is no other path," show the command and the **full**
  count. Do not pipe a negative-claim grep through `head` — truncation removes
  evidence of presence and biases you toward concluding absence.
- Distinguish clearly between what you **reproduced** and what you **reasoned**.
  Both are welcome; conflating them is not.
- If you can write a failing test that demonstrates the minting path, do — a test
  is the strongest form of this finding. Test files only.
- `GOCACHE=/tmp/gocache-166`. Never run the full `pkg/hub` package (~35 min,
  ends in a timeout panic); use `-run`.

## Deliverable

A written finding to me (`ca-msg-arch`):

1. The exhaustive list of write paths that can produce an empty group
   `external_ref`, each with `file:line`.
2. Which one most likely produced `dbd35ed2` and `fd9710a1`, and your confidence,
   and what evidence would raise it.
3. A recommended fix, as a description — not a patch.
4. Whether a structural gate is possible: something that makes an empty group
   `external_ref` unrepresentable or loudly detectable, rather than a fourth
   point fix. This is the part I most want your judgement on.
5. Anything you found that is not in this brief.

If you write a demonstrating test, push it on a branch `scion/ca-msg-def166`:

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  HEAD:refs/heads/scion/ca-msg-def166
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  scion/ca-msg-def166
```

`origin` in your container points at the wrong repo — use the explicit URL.
Never push to `scion/tranche-g` or `main`.

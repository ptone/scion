# Severity rubric for reviewers

**Every review brief on this project references this file. Read it before you
assign a severity to anything.**

It exists because of a specific, observed failure: a review that found a real
defect at the exact right lines, described its mechanism accurately, and then
filed it under *Nit / Optional* with a remedy that would not have prevented it.
**Detection and severity are separate faculties.** Being good at the first is
not evidence about the second, and a soft grade on a correct finding does more
damage than a miss, because it carries the authority of having been examined.

---

## The primary question: what does the code do to state?

Ask this before anything about logging, style, or observability.

| The path… | Severity floor |
|---|---|
| commits a state change that the user cannot undo | **Critical** |
| commits a state change that an operator can undo with a query | **Required** |
| returns wrong data without changing state | **Required** |
| is merely unobservable, with no state change | Nit / Optional |

**Deletion, and anything that makes a row unreachable, is the purest form of
irreversible.** A row whose registry entry is gone, whose key no longer resolves,
or whose ACL basis has been dropped is not "degraded" — it is lost, and the fact
that the bytes are still on disk is not a mitigation if nothing can find them.

## Reversibility is the hinge, and it is the axis people forget

When you weigh "fix it" against "accept it", you are weighing two costs. Say out
loud which of them is reversible. If one is and the other is not, **that
asymmetry decides it** and the relative sizes barely matter.

The worked example: refusing a promotion on a transient error costs the user a
retry. Proceeding costs them a DM that has left their list and a thread with no
messages in it, with a `200` telling everyone it worked. Those are not two
comparable costs to be traded off. One is an inconvenience and the other is
unrecoverable, and a review that presents them as a balance has dropped the only
term that matters.

**Under-doing an operation is recoverable. Over-doing it is not.** Refusal is
almost always the safe direction; say so explicitly when you recommend it, and
justify it when you do not.

## Do not prescribe the cure for the symptom

If your remedy is *"log a warning"*, stop and check what you are treating.

Silence is a symptom. On a path that destroys state, the defect is that it
**proceeds and commits**. A log makes the loss diagnosable afterwards; it does
not make it not happen, and the user's data is gone either way. Adding
observability to an unsafe path and calling the item closed converts a data-loss
bug into a data-loss bug with a paper trail.

Logging is the right remedy when the behaviour is correct and merely opaque. It
is never the whole remedy when the behaviour is wrong.

## A value in a success response is not a safety control

`messageCount: 0` in a `200` does not make a failure safe, for two reasons, and
both generalise:

1. **It arrives after the irreversible step.** Observability downstream of a
   commit is a post-mortem aid, not a guard.
2. **Nothing is obliged to read it.** A field is a control only if some consumer
   is *required* to check it and refuse. If you cannot name that consumer and
   point at the code that refuses, it is not a control.

The same test applies to log lines, metrics, and response headers offered as
mitigations. Name the reader and the refusal, or it is not a mitigation.

## The table sets floors, not ceilings — and here is the escalator it omits

**Who would notice, and how long would it take them?** A fault that refuses
loudly on a path someone is watching is cheaper than a quieter fault on a
population nobody can see.

Escalate above the table when the affected population is **invisible**: old
deployments, un-migrated data, users on a path the team does not exercise,
anything whose operators would read the symptom as normal breakage rather than
as a regression with a cause. Those are, by construction, the cases least likely
to generate a report, so the defect's lifetime is bounded by nothing.

This clause has already been used once, against this rubric's own table: a
missing test on an error-classification branch is a coverage gap and the table
floors it at *Nit*, but its failure mode was **total refusal for
pre-conversation-model hubs** — the oldest deployments, least watched, whose
operators would read the 503 as "this feature is broken" and never file
anything. It was filed as *Required*.

## Hardening a path creates coverage obligations on branches you did not edit

When you change what an error **means** — narrowing a swallow-all into a
classification, turning a warning into a refusal, making a default strict —
check what was **harmless before and is not now**.

A branch that never mattered can become load-bearing without its line changing,
and the tests will not notice, because nothing about the diff points at it. Ask:
which mistakes about this value were survivable an hour ago and are not
survivable now? Those branches need coverage in **this** commit, not a follow-up.
An inherited hazard can be deferred; a created one cannot.

## "Extremely narrow failure path" is not a severity argument on its own

Rarity multiplies with consequence; it does not replace it. A rare path that
destroys user data on a live endpoint is not a nit. Transient database errors,
context deadlines and pool exhaustion are **not rare** in the sense people mean
when they say narrow — they are the normal weather of a production system, and
a path reachable only by them is a path that will be reached.

If you want to argue rarity, argue it against a bounded consequence, and state
the consequence first.

## Error handling: the three-outcome check

Any `x, err := lookup(...)` on a path that then changes state has **three**
outcomes, not two. Check that the code distinguishes them:

- `err` is the expected sentinel (`ErrNotFound` or similar) → a legitimate,
  designed case. Proceeding may be correct.
- `err` is anything else → something went wrong. Proceeding is a decision that
  must be justified in the code, not assumed.
- `err == nil` **and** the result is nil/zero → frequently unhandled, and it gets
  swept into the branch beside the legitimate case. Either it is impossible and a
  comment says so, or it is a failure and belongs with the failures.

**Grep for the sentinel.** If `ErrNotFound` appears in a file only inside
comments, the code is not distinguishing anything, whatever the comments claim.

## Comments are not evidence, and a confident one is a place to look

A comment that says a path is safe is a claim to be tested, not a finding to be
accepted. On this project a comment justifying a silent error branch cited a
fallback that a measurement had already shown reaches 0.3% of rows — the
consoling clause asserted the precise fact the surrounding change had been
written to refute. **Where a comment disagrees with the code or the data, the
code and the data win, and the disagreement is itself a reportable finding.**

---

## How to file

Use these headings, and put each item under the highest one that applies:

- **Critical** — merge is blocked; irreversible harm is reachable.
- **Required** — merge is blocked; harm is reversible but real.
- **Nit / Optional** — genuinely does not matter if it ships as-is.
- **FYI** — no action, but the next reader should know.

For every item under *Nit / Optional*, write one sentence answering: **what is
the worst state the system can end up in if this ships?** If you cannot answer
it without describing lost or wrong data, it is not a nit.

**If you are unsure between two levels, file the higher one and say you are
unsure.** Over-escalating costs me a paragraph. Under-escalating costs whatever
the defect costs, and it spends the credibility of everything else you approved
in the same report.

# Substrate Phase 1 — clean review-history narrative out of the operator README

Final docs-only pass before this branch's PR goes upstream (substrate-lead
authorised, relayed by sb-em): `deploy/substrate/README.md` had
accumulated a fair amount of review-process narrative across six rounds of
fixes — "an earlier version did X", "confirmed by reading Y, not assumed",
citations to specific review rounds and findings, provenance claims like
"ran this against the live cluster to confirm rather than trust the doc" —
appropriate as an audit trail during development, but not appropriate for
operator-facing documentation shipped upstream. This pass removes that
narrative and keeps only the current, correct facts.

## Primary target: the workerpool-labels command narrative (README:118-130)

Removed:
- "(An earlier version of this command was `... | grep -A5 '^  labels:'`
  — broken, and confirmed broken by actually running it: ...)"
- "Ran the fixed `--show-labels` command directly against
  `substrate-scion-test` (read-only) to confirm rather than trust
  `infra/cluster.md` alone"
- The cluster-snapshot sample output (`AGE 155m`, blank `READY`) — replaced
  with a trimmed, generic example (`NAME ... LABELS` /
  `scion-agents ... pool=scion-agents,workload=scion-agents`).
- "the previous round of this manifest left it as a post-render hand-edit
  ... and that extra step is exactly what let the wrong value ship
  unnoticed"

Kept both commands (`--show-labels` and the `-o jsonpath=...` scripting
alternative), and the operationally useful facts: what object's labels
`worker_selector` actually matches, how to read the correct value off any
cluster, and that a "no pool pin" cluster needs a hand-edit instead of the
placeholder.

## Full-file sweep for the same pattern

Grepped for `round `, `reviewer`, `previously`, `we found`, plus the
narrower patterns this branch's actual history produced (`earlier
version`, `earlier draft`, `live-incident`, `live-deploy bug`,
`unnoticed`, `relayed by`, `for history`, `rather than trust`, `not
assumed`, `confirmed broken`, `tried and reverted`, `was removed once ...
confirmed`) and fixed every instance found:

- **Operational prerequisites**: "Confirmed on `substrate-scion-test` ...,
  relayed by substrate-lead" → stated as a plain fact about the cluster,
  no provenance/attribution framing.
- **Placeholder table**: `SUBSTRATE_WORKER_NAMESPACE`'s "kept below for
  history" and `WORKER_SELECTOR_KEY`/`VALUE`'s "this was a live-incident
  bug" → removed; both rows now just state the correct value.
- **`worker_selector` callout**: rewrote entirely — dropped "getting this
  backwards was a real live-deploy bug", "An earlier version of this
  manifest set `{ate.dev/worker-pool: scion-agents}`", and "Confirmed
  against upstream" framing. Kept the substantive technical distinction
  (which object's labels are matched) and the upstream demo reference as
  a plain "see this example," not "we verified this by checking."
- **`server.broker.broker_id`**: "This is confirmed as the *current*" →
  "This is the current" (dropped the investigative verb, kept the fact).
- **Broker API exposure**: "confirmed by reading `server_foreground.go`,
  not assumed" → dropped the self-justifying clause, kept the fact plainly
  stated.
- **Verification commands**: "substrate-lead requires this actually
  verified" → reworded to a plain imperative ("verify this on the
  cluster"), removing the named-individual attribution. "an earlier draft
  of this manifest did" → removed.
- **Known Phase 1 limitations**: "An earlier draft of this manifest also
  created a hand-written worker policy; it was removed once this was
  confirmed" → rewritten as a forward-looking caution ("do not also define
  a hand-written NetworkPolicy here") without narrating what was tried
  before. "capture it from the cluster directly rather than trust a
  guessed value here" → "capture this specific command's output directly
  ... to confirm current state." "(review round 2, Consider O3)" citation
  removed; "Before that memoization existed, every agent start re-dialed"
  (comparing to pre-fix behavior) removed. "(see broker.yaml's comment on
  why that was tried and reverted)" → reworded without "tried and
  reverted."

Left untouched (didn't match the pattern, and isn't process/review
narrative): the "Should there also be an ingress NetworkPolicy... Worth
doing, but not added here" risk-assessment paragraph (forward-looking
design reasoning, not a history claim), and "hence verification step (e),
rather than trusting the documentation alone" (operational justification
for a step, not a review citation). Did not touch `broker.yaml` — it has
similar comments citing "round 1 review", "round 2 correction", etc., but
this task was scoped to the README; flagged to sb-em in the completion
message as a candidate for the same treatment if the manifest's comments
also need to read clean for the upstream PR.

## Useful content preserved outside the repo

Everything substantive that was removed — the `worker_selector` incident's
full root-cause chain and upstream evidence, the router `NetworkPolicy`'s
three-iteration history (why `:80`-only was wrong, why the hand-written
worker policy was removed), the live cluster verification transcript
(including the `AGE 155m` snapshot dropped from the README's now-generic
example), and cross-references to the still-in-README Phase 1 findings
(`--host=0.0.0.0`/HMAC, CA memoization, Calico enablement cost) — went into
`/scion-volumes/scratchpad/projects/substrate-integration/phase1-report-notes-sb-dev-2.md`
(outside the repo, per instructions), organized for folding into
`phase1-report.md`'s §4 validation-question answers.

## Validation

- `git diff --stat`: `deploy/substrate/README.md` only, no manifest
  change.
- `kubeconform -summary` on `broker.yaml`: 11/11 resources still valid
  (unaffected).
- No Go changes this round.

## Not in scope

`deploy/substrate/broker.yaml`'s own comments (which cite round numbers
and "an earlier version of this policy") were not touched — out of scope
for this task per its explicit README-only framing. Flagged as a follow-up
candidate in the completion message.

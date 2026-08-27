# Brief: commit C on `scion/sn-docpr-upstream` — §5 states both loss events

Author: sn-impl-arch (architect). Date: 2026-08-27. ptone cleared this at 12:54. D7 + D8.2.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If something here contradicts what you find, **stop and message me.**

This is a small task with one large trap in it. The trap is in §3.

---

## 1. What you are doing

**One commit, on the existing branch `scion/sn-docpr-upstream`, changing one section of one file.**

```
git fetch origin scion/sn-docpr-upstream
git checkout -b <local> FETCH_HEAD     # HEAD must be 4a35a3a3
```

File: `.design/hosted/cloud-run-single-node.md`. Section: **§5 only.** Push back to the same branch:
`git push origin HEAD:scion/sn-docpr-upstream`. **Do not force-push.** **Do not rebase.**

Two commits are already on that branch and they are correct. Do not touch them, and do not touch any
other section — §6, §9.1, §10 and the qualified references were all settled and verified this
morning.

## 2. §5 as it stands today

```
## 5. Durability — Tier 0, pure ephemeral

Workspaces and the SQLite control plane live on the Instance's ephemeral filesystem.
A redeploy loses both.

This is a deliberate trade for G5, not an oversight. The tier is aimed at cheap,
fast, disposable deployments. Operators who need durability want the GCE VM baseline
(§7.4) or the HA hosted tier.

The state store the runtime keeps on disk is preview-stage and no state file is
expected to outlive a redeploy, so schema changes in it carry no migration cost.
```

**Every sentence is true.** You are not correcting a false statement.

## 3. THE TRAP — what is actually wrong here

The section names **one** way state is lost: redeploy. There is a **second** way, we measured it
today, and it is the dangerous one: **exceeding the agent ceiling destroys the Instance and
everything on it.**

The two are not the same kind of event:

- **A redeploy is chosen.** The operator picks the moment and can save work first.
- **An overload is not chosen.** It arrives roughly eight seconds after an agent-create request that
  returned **HTTP 201**. There is no memory or CPU instrument on this tier, so nothing warned the
  operator and nothing could have. The service then self-recovers in about 25–30 seconds, healthy and
  completely empty.

**Do not write this as a wording change.** The obvious execution — soften or delete the word
"disposable" — would be worthless. "Disposable" is a symptom. The defect is that a reader who
finishes §5 believes *"if I do not redeploy, my work is safe."* That belief is false, and no
adjective fixes it. **Add the missing fact; do not tune the vocabulary.**

Equally: **do not oversell it.** Do not call the tier unsafe, do not add a warning banner, do not
recommend against the tier. §5's core claim — ephemeral is a deliberate Tier 0 trade for G5 — is
correct and must survive intact. You are completing the section, not reversing it.

## 4. What the new §5 must contain

1. Both loss events named. Redeploy stays. Overload is added.
2. The distinction stated plainly: one is planned, one is not and **cannot currently be
   anticipated** — because there is no instrument (cross-reference §9.1, which now carries the
   observability gap entry added in commit B).
3. A cross-reference to the measured capacity table now in §9.1. **Do not restate the numbers in
   §5** — one authoritative copy, cited from the other place. Two copies of a measured number drift.
4. The existing G5 trade-off framing preserved.
5. The last paragraph (preview-stage state store, no migration cost) preserved — it is unrelated and
   still true.

Keep it proportionate. This is a design doc section, not a runbook: **roughly one added paragraph.**
If your §5 has doubled in length, you have written too much.

## 5. Numbers — do not add any that are not already in the file

`ptone/scion#1303` is the tracking issue for the ceiling failure. Reference it **fully qualified** —
a bare `#1303` in this file resolves upstream to an unrelated PR about a secret-metadata endpoint.

**The reference rule is absolute in this file.** Both prior commits enforced it and the branch is
currently at zero unqualified refs. **Before you commit, grep for `#1[0-9]{3}` not preceded by a repo
slug. The answer must be zero.** Note that fork `#1310` collides with the tier's own upstream PR, so
"it is obvious from context" is never a safe reason to leave one bare.

Do **not** add the agent-count numbers, a per-GiB rule, a recommended maximum, or a safety margin.
Every ceiling figure we have is a single observation with unmeasured repeatability, and §9.1 already
says so in the one place it belongs.

## 6. What you must NOT do

- **Do not open a PR.** ptone opens upstream PRs. Push the branch and stop.
- **Do not force-push or rebase** `scion/sn-docpr-upstream`.
- **Do not touch any section other than §5.**
- **Do not touch branch `scion/sn-buildfix-upstream`.**
- **Do not fix any other defect you notice** — tell me instead.
- **Do not deploy or delete anything.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t` are **do-not-delete**; `sn-ready` is ptone's live
  Instance.

## 7. Report back

Message `sn-impl-arch` with the new commit SHA, the full text of your new §5, confirmation that the
unqualified-ref grep returned zero, and confirmation that `git diff` shows §5 as the only section
changed. Also tell me anything here you think I have got wrong — two developers corrected me today
and both were right.

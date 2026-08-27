# Brief: two observability issues, and a tier label applied retroactively

Author: sn-impl-arch (architect). Date: 2026-08-27. Approved by ptone 12:27. Task #66.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find, **stop and message me** — do not improvise. That rule
has caught several of my own errors this week, including three today.

---

## 1. The reference trap — read before you write a single word

**Fork and upstream issue numbers are completely independent.** The same bare number is a different
thing in each repo. There are **twelve known collisions** and the list can never be complete: both
repos are active and share one number space, so every issue we file is a future collision. The
register is `ptone/scion#1297` — read it, but do not treat it as exhaustive.

**Fully qualify every cross-repo reference.** `ptone/scion#1304`, never a bare `#1304`.

This is not hypothetical. Yesterday a bare number reached a **user-facing doc**: the tutorial linked
`#1274` to the upstream URL, sending readers to an unrelated merged PR. The brief that produced it
had the reference qualified correctly — the prefix was dropped when prose became a link.

Do **not** write `Fixes #N` or `Closes #N`. **File on `ptone/scion`. Issues are fork-only here.**

## 2. Background — what was decided and why

The design doc `.design/hosted/cloud-run-single-node.md` never addressed observability at all
(`grep observ|monitor|metric|stats|utiliz` → zero matches). It says twice that agents share the
Instance budget. Both statements are true and both miss that the budget is also **invisible**.

Five instruments were tried during a stress test and all five were dead:

- Cloud Monitoring's memory/CPU metrics only cover `cloud_run_revision` (Services), not
  `cloud_run_instance`.
- `getStats` returns hardcoded zeros (`ptone/scion#1304`).
- The hub agent list is wrong in **both** directions — reports `running` for dead sandboxes, and
  omits live leaked ones (`ptone/scion#1308`).
- Agent sandboxes are gVisor processes, invisible to Cloud Monitoring.
- SSH does not work; `sshd` is absent from the omni image (`ptone/scion#1305`).

**ptone decided this is a known gap, not a non-goal** — we intend to close it — and asked for it to
be split into two stages.

## 3. Issue 1 — per-agent Cloud Logging support (the near-term stage)

The tractable half. Cloud Logging **does** work on this platform and it survived every failure we
threw at it: it outlived Instance deletion, and it is how both stress ceilings were finally verified
by sandbox name after two agents miscounted their own ladders.

What the issue should establish:

- Logs exist and are queryable, but they are **not organised per agent**. Verifying which sandboxes
  were alive required ad-hoc `grep` over raw JSON payloads for name patterns like
  `retest--w-1`. That is a forensic technique, not an operator feature.
- An operator has no per-agent view: no way to ask "show me this agent's logs" without knowing the
  internal sandbox naming convention.
- **Do not specify the implementation.** State the capability gap and the evidence. Whether this is
  log labels, a structured field, a hub endpoint or a CLI subcommand is a design question and it is
  not yours or mine to settle in a tracker.

## 4. Issue 2 — additional observability for `cloudrun-sandbox` agents (the longer-term stage)

CPU and memory visibility, which is the part with no working instrument at all.

- Reference `ptone/scion#1304` for `getStats`, but **this issue is not a duplicate of it.** `#1304`
  is one function returning zeros. This is the absence of any instrument at the tier level.
- Note that the broker is co-located with the Hub in the main container
  (`cmd/server_dispatcher.go:34`), so it already has instance-scoped `/proc/meminfo` access. That is
  context for a future implementer, **not a recommendation** — do not write it as the chosen fix.
- **The one working instrument needs no new code: agent create latency.** Measured at both Instance
  sizes, it ramps ~24× over four rungs before failure (1.5s → 1.3s → 11s → 15s → 26s → 36s → 503 at
  68s). It shipped as the tutorial's operating guidance. Record it, because it is the fallback until
  something better exists — and record that **with idle agents there is no such warning at all.**

Relate both issues to `ptone/scion#1303` (exceeding the ceiling destroys the Instance). That is why
observability matters here rather than being a nice-to-have: **the operator's only current feedback
is the failure, and the failure is unrecoverable.**

## 5. The label — and the judgement call that matters most

ptone asked for a label to apply retroactively to issues specific to this tier's deploy config.

1. **List the repo's existing labels first** (`gh label list`) and match the established naming
   convention. If there is already a prefix style (`area/`, `tier/`, `deploy/`), follow it. Only if
   there is no convention, use `tier/cloud-run-single-node`. **Tell me what you chose and why.**
2. **Then apply it — selectively.**

**THIS IS THE PART TO GET RIGHT.** Do not label everything this project filed. The test is *specific
to this tier's deploy config*, and several of our issues are **product-wide problems that merely
surfaced here first**:

- `ptone/scion#1304` (`getStats` zeros) is in `runtimebroker`. It returns zeros for **every runtime
  on every tier.** Labelling it as tier-specific would bury a product-wide defect inside one tier's
  backlog. I stopped exactly that mistake when it was filed.
- `ptone/scion#1308` (hub DELETE leaks the sandbox) — likely the same shape. It was *measured* here;
  there is no evidence the cause is tier-specific.
- `ptone/scion#1281`, `ptone/scion#1274` — platform defects, found here.

**When you cannot tell, leave it unlabelled and list it for me.** An unlabelled issue is a small
untidiness. A product-wide defect mislabelled as one tier's problem is how it stops being anyone's.

Report your labelled set, your unlabelled set, and your reasoning for any you found genuinely
ambiguous.

## 6. What you must NOT do

- **Do not fix anything.** This task produces two issues and a label. Nothing else.
- **Do not touch any branch, PR, or code.**
- **Do not open anything upstream.** Fork only.
- **Do not edit `.design/hosted/cloud-run-single-node.md`.** It lives on upstream main; only ptone
  can open that PR. The D5 change is already queued for it.
- **Do not delete any Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t` are **do-not-delete**. `sn-ready` is ptone's live
  Instance.

## 7. Report back

Message `sn-impl-arch` with the two issue numbers as `ptone/scion#NNNN`, the label name and why you
chose it, the issues you labelled, the issues you deliberately did **not** label, and anything here
you think I have got wrong. Two developers corrected me today and both were right.

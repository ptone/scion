# Brief: prepare two branches for ptone to open as upstream PRs

Author: sn-impl-arch (architect). Date: 2026-08-27. Approved by ptone 12:33. Tasks #62, #66, D1/D4/D6/D8.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find, **stop and message me** — do not improvise. That rule
has caught several of my own errors this week, including three today.

**You are preparing branches, not opening PRs.** Agents have fork write access only, by design.
Push both branches to `ptone/scion` and report them. **ptone opens the upstream PRs. You do not.**

---

## 1. Base both branches on CURRENT upstream main — this is not the fork's main

```
git fetch https://github.com/GoogleCloudPlatform/scion.git main
git checkout -b <branch> FETCH_HEAD
```

Upstream main was `3aeb7729` when I wrote this. **It has already moved twice today** — the tier
merged at `f99a8189` and upstream is now well past it. Fetch fresh; do not trust my SHA, and do not
branch from the fork's `main`, which may lag.

Then push to the fork: `git push origin <branch>`. Only remote is `origin` = `ptone/scion`.

## 2. Branch 1 — `scion/sn-docpr-upstream`. ONE file, THREE commits, and one section you must not touch

The only file this branch may modify is `.design/hosted/cloud-run-single-node.md`.

### Commit A — qualify all 18 bare issue references (`ptone/scion#1297`)

**Read `ptone/scion#1297` in full first.** It is accurate; I re-derived its counts independently this
morning and got the identical split. It is your specification.

There are **18 bare `#NNNN` refs** and they belong to **two different repositories**:

| where | count | which |
|---|---|---|
| **fork** (`ptone/scion`) | 13 | `#1273`×4, `#1274`×2, `#1275`×2, `#1276`×4, `#1281`×1 |
| **upstream** (`GoogleCloudPlatform/scion`) | 5 | `#1302` (§4.4), `#1300`, `#1304`, `#1305`, `#1306` |

**THE TRAP — read this twice.** The obvious execution is to prefix every bare ref with
`ptone/scion#`. **That is wrong and it would make the file worse than it is now.**

The file already contains a table where **each row cites a fork issue and an upstream PR side by
side**:

```
| #1273 | resolve implicit `default` template when none is specified | `fc523ecd` (PR #1305) |
| #1275 | skip env-gather when `noAuth` is true            | `6edf6ed0` (PR #1304) |
| #1276 | auth preflight recognises passthrough GCP identity mode | `a30368aa` (PR #1306) |
```

Left column: **fork issues.** Right column: **upstream PRs.** In row one, `#1273` must become
`ptone/scion#1273` and `#1305` must become `GoogleCloudPlatform/scion#1305`. If you blanket-prefix,
that row cites `ptone/scion#1305` — which is the *sshd absent from the omni image* issue, an
entirely different thing, in a row about template resolution. **It would read as plausible.** That
is the whole reason this defect class is dangerous.

Note the asymmetry, because it tells you where the risk is: the file lives upstream, so the **5
upstream refs already resolve correctly today** and the **13 fork refs are the broken ones**. You are
qualifying all 18 anyway — a ref that is correct only because of where the file happens to live
breaks the moment the text is copied, and that is precisely how this bug was born.

**Resolve every single ref individually.** For each one, read the surrounding sentence and decide
which repo it means. Where the text already says "PR" or "landed upstream", that is your signal.
`#1302` at line 247 is **upstream** — it is the Instances runtime PR that merged as `83ee4bd9`; note
that the same file elsewhere already writes `GoogleCloudPlatform/scion#1302` correctly, so the file
contains both a right and a wrong treatment of the same number.

**Verify your own work before committing:** after editing, grep the file for any remaining
`#1[0-9]{3}` not preceded by a repo slug. The answer must be zero.

### Commit B — four design-doc deltas that are settled

- **§6 (D4) — upgrade, not a correction.** §6.1 "One gate, and it must be designed for" was written
  as design intent. It is now measured: the deploy sets `invokerIamDisabled: true`, so the Cloud Run
  invoker check is off and **IAP is the sole perimeter**. Evidence: a six-way header × token ×
  audience matrix run 2026-08-27. Say it is verified and say how. §6 was right, but right without
  evidence, and a security section that cannot show its working invites the next reader to re-derive
  it.
- **§9.1 (D5) — add an observability gap entry.** ptone decided this at 12:27 today: it is a **known
  gap we intend to close**, explicitly **not** a non-goal, so it belongs in §9.1 and **must not** go
  in §2. §2 and §9.1 currently record that agents share the Instance budget. True, and incomplete in
  the same way: **they never notice the budget is also invisible.** Five instruments were tried and
  all five were dead — Cloud Monitoring covers only `cloud_run_revision` (Services) not
  `cloud_run_instance`; `getStats` returns hardcoded zeros; the hub agent list is wrong in both
  directions; sandboxes are gVisor processes invisible to Cloud Monitoring; `sshd` is absent from the
  omni image. Note that work is staged in two parts and that the only working signal today is **agent
  create latency**. Keep it to a short gap entry — the two tracking issues carry the detail, and
  `sn-obs-dev` is filing them right now. **Ask me for their numbers before you write the entry; do
  not guess them and do not omit them.**
- **§10 AC 11 (D6).** The omni image is now produced by the chained build and verified by digest.
  AC 11 moves from "reasoned" to "run".
- **§9.1 / §2 sizing caveat (D8, parts 1 and 3).** §9.1's "No per-agent resource limits" states the
  constraint without its magnitude, which reads milder than it is. Give it the measured numbers:

  | size | idle agents | working agents |
  |---|---|---|
  | 4 CPU / 8 GiB (default) | 20 | 6 |
  | 8 CPU / 32 GiB (maximum) | 51 | 14 |

  **Each is a single observation; repeatability is unmeasured. Write that.** And state that **no
  per-CPU or per-GiB rule is derivable**: 4× memory and 2× CPU bought roughly 3× idle and 2× working.
  Two points, non-linear in both resources. **Do not add, round, interpolate or extrapolate any
  number beyond this table.** A number in a doc becomes true by being printed and outlives every
  caveat attached to it.

### DO NOT TOUCH §5 — it is a third commit that is not yet authorised

§5 is the durability section. It needs a change (it frames loss around redeploy, and the dangerous
loss event is overload). **That change is D7, it is a live question with ptone, and he has not
answered it.** He approved the *grouping* of these PRs, not §5's new wording.

Leave §5 exactly as it is. I will send you the wording as **commit C** on this same branch once he
rules. If you think §5 is wrong while you read it — you are right, and it is still not yours to fix
today.

## 3. Branch 2 — `scion/sn-buildfix-upstream`. Two tiny changes, unrelated to branch 1

Both verified by me against upstream main today.

- **`ptone/scion#1298`** — create an **empty** `image-build/.gitignore`. It does not exist upstream;
  I checked. `gcloud` 582.0.0 fails with `Could not read ignore file .gitignore` when building from
  that directory **even when `--ignore-file` points elsewhere**. Zero bytes. Note that an empty file
  needs `git add -f` only if something ignores it — check that it is actually staged.
- **`ptone/scion#1299`** — `image-build/cloudbuild-omni.yaml` **line 192**, `timeout: 14400s` →
  `2400s`. Measured build time was **641s** (build `9a1b9766-9d43-4e14-85e8-28536ab00a80`, 2026-08-27),
  so 14400s was ~4.5% utilised. **Add a provenance comment** in the style already used on
  `hub`/`scion-base`/`core-base` — date and measurement. The missing comment is the actual root
  cause: a number with no reason invites the next person to copy it forward, which is how 14400s got
  there.

## 4. What you must NOT do

- **Do not open any PR, upstream or fork.** Push branches; report; stop.
- **Do not merge anything.** `#1265`/`#1266` are ptone's gate.
- **Do not rebase or force-push any shared or integration branch.**
- **Do not touch §5** of the design doc. See §2.
- **Do not combine the two branches.** They are two PRs on purpose.
- **Do not fix any other defect you notice.** Tell me instead.
- **Do not deploy or delete anything.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t` are **do-not-delete**; `sn-ready` is ptone's live
  Instance.

## 5. Report back

Message `sn-impl-arch` with:

- Both branch names and commit SHAs, and the upstream base SHA you branched from.
- **Your ref-by-ref accounting for commit A**: how many you qualified to each repo, and confirmation
  that the post-edit grep for unqualified `#NNNN` returned zero.
- Any ref whose target repo you could not determine with confidence. **Say so rather than guessing —
  an unqualified ref I know about is better than a confidently wrong one I do not.**
- Anything in this brief you think I have got wrong. Two developers corrected me today and both were
  right.

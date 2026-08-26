# Upstream merge assessment — single-node hosted tier

**Author:** sn-impl-arch · **Date:** 2026-08-26 16:40 UTC
**Question asked:** what work is sitting where, and what would it take to get all of this merged
upstream as a set of PRs?

Everything below is measured from `origin` at 16:35 today, not recalled.

---

## 1. Where the work actually sits

Four places, and only the first is safe.

### 1.1 Upstream, green, and waiting on one human

| PR | Branch | → base | Size | Checks | Mergeable |
|---|---|---|---|---|---|
| **#1266** | `scion/dev-rebase-1294` | `main` | +7575/−59, 63f, 67 commits | **all 4 SUCCESS** | **CLEAN** |
| **#1265** | `scion/security-fix-p0-s1` | `main` | +259/−0, 6f, 1 commit | **all 3 SUCCESS** | **CLEAN** |

`reviewDecision` on both is **empty** — nobody has reviewed them. They are not blocked on CI, not
blocked on conflicts, and not blocked on me. They are blocked on ptone.

This is the single most important line in this document: **the main body of the tier is already
mergeable and has been sitting green.** The remaining work is not "make it mergeable", it is
"decide to merge it".

### 1.2 Branch-only, deliberately not proposed to main

| PR | Branch | → base | Size |
|---|---|---|---|
| #1268 | `scion/sn-dev-ready` | `scion/dev-rebase-1294` | +465/−17, 6f |
| #1269 | `scion/sn-ws-mount` | `scion/dev-rebase-1294` | +697/−26, 8f |

Both are titled **DO NOT MERGE** and exist to make CI build an image, which was the only way to get
a testable artefact. They are not review-ready as framed, but their *content* is.

**Ancestry (verified, not assumed):**

```
origin/scion/sn-ws-mount  (06bb924a, 76 ahead of main)
  ├── contains scion/sn-dev-ready      → YES
  ├── contains scion/dev-rebase-1294   → YES
  └── contains scion/security-fix-p0-s1 → NO   (independent)
```

So **`scion/sn-ws-mount` is the single tip containing the entire tier**: 67 files, +8248/−61.
There is no work stranded on a side branch. That is worth knowing, because it means the merge can
be sequenced from one lineage rather than reconciled from several.

The nine commits it carries beyond #1266:

```
06bb924a feat: add cloudbuild-omni.yaml for Cloud Build omni image chain
3f99cb79 chore: trigger CI for PR #1269
296cc1de fix: correct stale GetWorkspacePath comment after mount remapping
5800f5a3 Merge origin/scion/sn-dev-ready into scion/sn-ws-mount
e186021d fix: mount workspace at /workspace inside Cloud Run sandbox (#43)
ad30c1aa fix: add SCION_SERVER_MODE=hosted to deploy-instance env vars (#40)
a1165c0e fix: deploy-instance derives SCION_IMAGE_REGISTRY from --image (#38)
a0367c1d style: fix gofmt formatting in provision_home_test.go
e36a3f00 fix: sandbox exit detection — HOME, template home, deploy-instance parsing
```

### 1.3 The scratchpad — **not version controlled, and this is a real risk**

`/scion-volumes/scratchpad/projects/single-node/` is **not a git repository.** It holds:

- `cloudrun-instances-sandboxes.md` — the design doc, including §1 (the yardstick) and 22 sections
  of measured findings
- `implementation-state.md` — the running evidence log; the only record of *why* each fix is shaped
  the way it is
- `review-queue.md` — items accumulated for ptone
- `briefs/*.md` — the agent briefs, which contain measured evidence not repeated anywhere else

If that volume goes away, the code survives and **the entire reasoning record does not.** Several
of the fixes on the branches are one-line changes whose justification took hours to establish
(the `/workspace` mount destination, the `HOME=/root` chain, the seed-path dead end). A future
reader of `e186021d` gets a one-line commit message and no way back to the evidence.

This is the one thing in this assessment I would fix regardless of what happens with the PRs.

### 1.4 The defect register — filed locally, almost none of it upstream

Sixteen defects are tracked in my task list and the scratchpad. **Exactly one has been filed
upstream** (ptone/scion#1270, this afternoon, at ptone's instruction).

Open and unfiled upstream: #15, #32, #35, #37, #39, #41, #42, #43, #44, #45, #46, plus five not yet
written up (`SCION_SERVER_AUTH_DEVMODE=false` clobbered at `server_foreground.go:844`; stale
`containerStatus=running`; unknown create-request fields defaulted rather than rejected;
`drop-to-shell` OAuth retry loop; omni image build provenance).

Several of these are **not specific to the hosted tier** — #45 (WebServer config split-brain), #32
(`relocateToScion` data loss), #35 (session metrics 400) and #42 (`noAuth:true` inversion) are
upstream defects that this project merely happened to walk into first. Merging the tier does not
resolve them and they should not be attached to the tier's PRs.

### 1.5 The live estate

Cloud Run Instances in `ptone-experiments`/`us-east4`: `sn-ready` (yours), `sn-walk` (mine),
`e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`. Not "work", but it is state that exists only
there, and `sn-ready` currently carries a **hand-applied** `SCION_SERVER_HUB_ADMINEMAILS` patch
that no branch reproduces. If it is redeployed from any current branch it loses that.

---

## 2. The number that should drive the decision

An 8,248-line diff reads as a large, risky merge. It is not, and the reason is worth stating
precisely.

| Category | Insertions |
|---|---|
| Files that **do not exist** on main (new runtime, new command, new image, new tests, project logs) | **6,679** |
| Files that **do** exist on main — total | 1,569 |
| …of which are test files | ~823 |
| **Pre-existing non-test code** | **~746 insertions, 61 deletions** |

**81% of the change is in files main has never seen.** New files cannot regress behaviour that does
not exist; the worst they can do is fail to work. The actual regression surface of this entire tier
is roughly **746 added and 61 removed lines**, and that is what a reviewer should spend their time
on. Ranked:

| File | Δ | Why it matters |
|---|---|---|
| `pkg/runtimebroker/pty_handlers.go` | +164 −25 | **Highest risk.** Shared by docker, k8s and Cloud Run. A regression here breaks terminals for every existing user. |
| `pkg/sciontool/metadata/server.go` | +141 −7 | Shared metadata emulator; the link-local binding change (S5) lives here. |
| `image-build/scripts/lib/targets.sh` | +62 −2 | Build chain for all images, not just omni. |
| `pkg/runtimebroker/hubenv.go` | +48 | New env plumbing on a shared path. |
| `pkg/agent/provision.go` | +44 | Template-home provisioning — shared with docker. |
| `web/src/components/pages/diagnostics.ts` | +44 | UI only, additive. |
| `cmd/server_foreground.go` | +34 −4 | Startup wiring. |
| `pkg/runtime/factory.go` | +31 −2 | **The dispatch point.** If the new runtime is selected when it should not be, existing deployments change behaviour. |
| `pkg/hub/project_workspace_handlers.go` | +25 −3 | |
| `pkg/hub/web.go` | +22 | |
| `pkg/hub/handlers_health.go` | +18 −9 | Only file with meaningful deletions. |

Everything else is ≤13 lines or a new file.

Two of these deserve named attention in review because they are the ones that can hurt someone who
has never heard of Cloud Run: **`pty_handlers.go`** and **`factory.go`**. If I were reviewing this
in an hour, I would spend forty minutes on those two and skim the rest.

---

## 3. Recommendation: three PRs, sequenced

### PR-1 — `#1265` security fix, merge now, standalone

+259/−0, one commit, green, independent of everything else (verified: **not** an ancestor of
`dev-rebase-1294`). It refuses dev auth on non-loopback interfaces.

Merge it first and separately. A security fix should not sit behind a 7,500-line feature review,
and because nothing depends on it, merging it costs nothing and can be done in five minutes.

### PR-2 — `#1266` the tier, **as it stands**

Green, CLEAN, 67 commits, mostly new files. Do **not** re-cut it (see §4).

Two things to do to it before merging, both cheap:

1. **Retitle and rewrite the description** around the review surface in §2 above, so the reviewer
   knows the 8k diff has a 750-line regression footprint and where it is. Right now the PR gives no
   guidance and the natural reaction to +7575 is to defer it — which is, empirically, what has
   happened.
2. **State the tier's maturity honestly in the description.** §1 steps 0–5, 5a, 5b and 7 pass on
   live hardware; step 6 does not yet (that is #43, fixed on a branch but unverified). Merging a
   tier with a known-broken step is defensible if it is labelled experimental; it is not defensible
   silently.

### PR-3 — the fixes, re-cut against `main` after PR-2 lands

The nine commits from §1.2, minus the two that are noise (`3f99cb79` "trigger CI", and the merge
commit `5800f5a3`, which disappears on rebase). What remains is coherent and small:

- `e36a3f00` + `a0367c1d` — sandbox exit detection (HOME/USER/LOGNAME, template home)
- `e186021d` + `296cc1de` — **#43**, the `/workspace` mount destination
- `a1165c0e` — **#38**, derive `SCION_IMAGE_REGISTRY` from `--image`
- `ad30c1aa` — **#40**, `SCION_SERVER_MODE=hosted`
- `06bb924a` — `cloudbuild-omni.yaml`

That is +673/−26 over #1266 across 8 files. One PR, retitled off "DO NOT MERGE", base `main`.

**Precondition:** #43's fix is currently *unverified on live hardware* — that is exactly what the
running Cloud Build is for. Do not merge PR-3 until step 6 has actually been walked. It is the one
change in the set whose correctness rests on a code reading rather than a measurement, and this
project has been wrong about code readings five times today.

### Not in any of the three

The defect register (§1.4). File the upstream-general ones (#45 is already #1270, plus #32, #35,
#42) as issues against main on their own merit. They are not this tier's debt and attaching them
would make three clean PRs look like a dumping ground.

---

## 4. Alternatives considered

**A. One mega-PR: fast-forward `main` to `scion/sn-ws-mount`.**
Tempting — it is one command and the tip already contains everything. Rejected: it merges an
unverified fix (#43) and a "trigger CI" commit, and it gives the reviewer a single +8248 wall with
no seam to stop at. The seam between "the tier" and "the fixes" is genuinely useful, because the
tier is green-and-measured and the fixes are not all measured yet.

**B. Re-cut #1266 into six thematic PRs** (runtime / broker / metadata / image-build / deploy
command / hub).
This is what a textbook would say. Rejected on cost-benefit: the 67 commits are a development
narrative (`p0` → `p6` phases), not a clean thematic decomposition, so re-cutting means rewriting
history and re-running CI on six branches — realistically a day of work with a live risk of
breaking a build that is currently green. The benefit it buys is reviewability, and §2 shows that
benefit is largely illusory: the reviewable surface is 750 lines regardless of how the commits are
sliced. **Slicing a PR does not shrink the diff; it only changes how it is presented.** Better to
present it well in one description than to spend a day producing six.

I would revisit this if a reviewer actually bounces #1266 for size. They have not — they have not
looked at it at all, which is a different problem and re-cutting would not solve it.

**C. Hold everything until §1 step 6 passes and merge one complete, verified tier.**
Rejected, but it is the closest call. It has real appeal: nothing lands until the yardstick is met.
Against it: #1265 is a *security fix* and holding it hostage to a feature is wrong; #1266 has been
green and unreviewed for long enough that "wait for more" is how it stays unreviewed indefinitely;
and the tier is useful-with-caveats today. **Merging PR-1 and PR-2 does not commit anyone to
PR-3** — that is the reversibility that makes the split worth having.

---

## 5. What it would actually take

Sequenced, with the honest costs.

| # | Step | Cost | Blocked on |
|---|---|---|---|
| 1 | Review and merge **#1265** | ~15 min | ptone |
| 2 | Rewrite **#1266**'s description around §2's review surface | ~30 min | me |
| 3 | Review and merge **#1266** | 1–2 h of real review, focused on `pty_handlers.go` and `factory.go` | ptone |
| 4 | Verify **§1 step 6** on the Cloud Build image | ~45 min once the image lands | running now |
| 5 | Rebase the nine commits onto `main`, drop the CI-trigger commit, open **PR-3** | ~1 h | step 3 + step 4 |
| 6 | Review and merge **PR-3** | ~30 min | ptone |
| 7 | Commit design doc + defect register somewhere durable | ~1 h | decision on where |
| 8 | File the upstream-general defects as issues | ~1 h | — |

**Critical path is not engineering time.** Steps 1, 3 and 6 are review decisions totalling perhaps
three hours of your attention; everything else is a few hours of mine and can run alongside. If the
review happened today, the whole tier could be on `main` tomorrow with step 6 verified.

The thing that would make this take *weeks* instead is choosing alternative B.

---

## 6. Open questions — for ptone, not resolvable by me

1. **Should the tier land labelled experimental?** It has a known-broken step 6 until #43 is
   verified, and it runs the Instance as the default compute SA with `roles/editor` (§1.5 of the
   defect register, part of #41) — fine for a demo project, wrong for anything an operator runs.
   I would land it behind a clear "experimental / not hardened" note rather than hold it.
2. **Where does the reasoning record live?** The scratchpad is not backed up. `.design/project-log/`
   is established convention upstream (165 files on main), so the mechanism exists — the question is
   only whether this project's log belongs in the public repo.
3. **Do the unfiled defects get filed before or after the merge?** Filing first is more honest and
   makes the tier's known gaps visible at review time; filing after avoids muddying the PRs.

*Not blocking: whether `.design/project-log/*.md` belongs in the PRs. I checked — it is already
convention, 165 such files on main. The 23 new ones stay.*

---

## 7. Acceptance criteria for "this is merged"

- `#1265` and `#1266` merged to `main`; `scion/dev-rebase-1294` deleted.
- PR-3 merged, with §1 step 6 **measured on live hardware** and the evidence recorded before merge.
- `git diff origin/main...origin/scion/sn-ws-mount` is **empty**.
- A fresh `deploy-instance` from `main` produces an Instance on which §1 steps 0–7 pass, with no
  hand-applied patches — in particular no manual `SCION_SERVER_HUB_ADMINEMAILS`.
- Every defect in §1.4 is either fixed, filed upstream, or explicitly written off.
- The design doc and evidence log exist somewhere that survives the scratchpad volume.

---

## 8. Addendum — 2026-08-26 (supersedes §3 ordering)

Written in ASD-STE100 Simplified Technical English, at ptone's instruction.
The full answer to ptone's 2026-08-25 18:35 request is in
`status-for-ptone.md` in this directory. Read that first.

### 8.1 One hard constraint that §3 did not state

**Do not merge PR 1266 alone.** On its own it ships a `deploy-instance` command
that cannot work. Two environment variables are missing on the
`scion/dev-rebase-1294` branch:

- `SCION_SERVER_MODE=hosted` — without it the hub enables dev auth and refuses
  to boot (defect D-40).
- `SCION_IMAGE_REGISTRY` — without it the broker cannot pull agent images
  (defect D-38).

Both fixes exist only on `scion/sn-dev-ready` (PR 1268) and `scion/sn-ws-mount`
(PR 1269). Verified 2026-08-26 by reading `cmd/deploy_instance.go:289` on all
three branches.

Therefore PR 1268 and PR 1269 must merge into `scion/dev-rebase-1294` **before**
PR 1266 goes to `main`. §3's "PR-3 re-cut after PR-2" ordering is wrong on this
point and is superseded.

### 8.2 Corrected PR state, verified 2026-08-26

All numbers are in `ptone/scion`. All checks green.

| PR | Branch | Target | Size |
|---|---|---|---|
| 1265 | `scion/security-fix-p0-s1` | `main` | +259 / 6 files |
| 1266 | `scion/dev-rebase-1294` | `main` | +7575 / 63 files |
| 1268 | `scion/sn-dev-ready` | `scion/dev-rebase-1294` | +465 / 6 files |
| 1269 | `scion/sn-ws-mount` | `scion/dev-rebase-1294` | +806 / 9 files |
| 1272 | `scion/wc-dev` | `main` | +221 / 5 files — fixes D-45, issue 1270 |
| 1264 | `scion/broker-auth-gap` | `main` | +390 / 2 files |

Note: PR 1272 did not exist when §3 was written. It closes D-45 with fix shape A
(an `AccessSettingsProvider` interface). D-44 is downstream of D-45 and must be
re-tested after PR 1272 merges.

### 8.3 A gap §5 did not price

PR 1266 adds **no user documentation**. Its only Markdown files are internal
engineering logs under `.design/project-log/` plus `image-build/README.md`. No
page under `docs-site/`. No reusable script for a third party.

The tutorial and the deploy scripts are specified in `status-for-ptone.md`
Part C.4. They are a new deliverable of about three commits. Target directory
for the scripts: `scripts/single-node/`. Target page:
`docs-site/src/content/docs/hosted/single-node/cloud-run.md`.

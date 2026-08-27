# Brief: file the single-node follow-up register as tracking issues

Author: sn-impl-arch (architect). Date: 2026-08-27. Approved by ptone 04:07.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find, **stop and message me** — do not improvise. That rule
has caught several of my own errors on this project, including the one described in §2 below.

**You will be dispatched for ONE batch of four.** ptone asked for three batches of four to manage
load on the system. Do only your batch. I will dispatch the next one after you report.

---

## 1. What you are doing

`#1310` merged to upstream main at 04:00 (`f99a8189`) — the single-node Cloud Run tier. Twelve
follow-up items were deliberately left out of scope. ptone wants them as tracking issues so they
survive.

**File them on `ptone/scion`. Issues are fork-only in this project — never open an issue upstream.**
You have fork write access by design.

## 2. READ THIS FIRST — the reference trap that caused this brief to be rewritten

**Fork and upstream numbers are completely independent.** The same number is a different thing in
each repo. I verified this the hard way just now:

| bare ref | in `ptone/scion` (fork) | in `GoogleCloudPlatform/scion` (upstream) |
|---|---|---|
| `#1274` | ISSUE: `GitCloneConfig.Depth: 0 documented as full clone but implemented as 1` | PR: `fix(hub): accept text files with unusual control chars` |
| `#1281` | ISSUE: `Session metrics are lost permanently…` | PR: `fix(hub): stop syncBuiltImage from mutating config.yaml` |
| `#1273` | ISSUE: `Hosted hub drops template and harness-config identity…` | PR: `fix(harness): populate file_secret_files…` |

**Completely unrelated content behind identical numbers.**

So: **fully qualify every cross-repo reference you write.** `ptone/scion#1274` or
`GoogleCloudPlatform/scion#1305`. **Never a bare `#1274`.** A bare number resolves against whichever
repo the text is rendered in, which is how this went wrong in the first place.

Do **not** write `Fixes #N` or `Closes #N` anywhere. These are tracking issues, not fixes.

## 3. Two items are ALREADY FILED. Do not duplicate them.

I checked before writing this. Both are **open** and correctly describe the problem:

- **`ptone/scion#1274`** — depth-1 shallow clone, cannot push to any remote but `origin`.
- **`ptone/scion#1281`** — session metrics lost, `exit_code` never persisted.

They stay open as-is. **Reference them where relevant, but do not re-file them.**

I searched the fork for the other ten and found no duplicates. **Search again anyway before filing
each one** — my search was keyword-based and could have missed a differently-worded issue. If you
find a genuine duplicate, do not file; tell me instead.

## 4. Common requirements for every issue you file

- **Title:** states the problem, not the area. `No per-agent resource limits in the single-node
  tier` beats `Resource limits`.
- **Body must contain:**
  - What the limitation or defect is, concretely.
  - **Whether it is by-design or a defect.** Several of these are deliberate non-goals, and an issue
    that reads like a bug report for an intentional decision wastes the next reader's time. Say
    plainly which it is.
  - The consequence for an operator.
  - A pointer to `.design/hosted/cloud-run-single-node.md` and the relevant section — **by path and
    section number, not by issue number.** That file is on upstream main as of `f99a8189`.
- **Labels:** apply existing repo labels if obvious ones exist. Do not invent new labels.
- Keep each body tight. A paragraph or two plus a bullet list. These are trackers, not essays.

## 5. THE BATCHES

### Batch 1 — deliberate limits of the tier as shipped (all by-design, all from design doc §2 / §9.1)

1. **No per-agent resource limits.** All agents share the single Instance's CPU and memory budget.
   There is no way to say "this agent gets 2 CPU". Note this is the item ptone specifically
   remembered, so it is the one most likely to be read.
2. **Ephemeral only — no workspace or control-plane durability.** Workspaces are lost on redeploy.
   Design doc §5 covers the reasoning ("Tier 0, pure ephemeral").
3. **No HA, failover, or multi-node.** One Instance, a single point of failure by design.
4. **Templated Sandboxes are unavailable**, which is what forces the omni image (§4.1). Record what
   changes when they ship — per-agent images return, `ImageExists`/`PullImage` stop being no-ops,
   and §8.3 says this is a one-file change plus the build definition, not a redesign.

### Batch 2 — diagnosability and distribution gaps

5. **Image-pull failure on first deploy is undiagnosable.** The error comes from the Cloud Run
   sandbox launcher rather than Scion, names a cache mirror instead of the requested image, and its
   tag advice is misleading. Real defect.
6. **Sandbox stderr can be lost**, so an agent that dies during provisioning is harder to diagnose
   than it should be. Note the contrast: the general Scion path is well instrumented; this loss is
   specific to this runtime's sandbox handling. Real defect.
7. **Goal G4 is unmet: `deploy-instance` requires `--image` and has no default.** The design doc's
   own goal says "deployment is one command against a GCP project — no registry setup". Closing this
   needs an org owner to create and publish `ghcr.io/googlecloudplatform/scion-omni` as public. Be
   explicit that dropping the CI workflows did **not** cause this — it was already unmet because the
   package push was denied (`installation not allowed to Create organization package`).
8. **A conflicted PR silently loses all `pull_request` CI.** Measured on this very PR: head
   `38ba412e` got 6 check-runs, the prior commit `728d17cd` got 10, and `Build & Test` was absent
   entirely. A conflicted PR has no computable merge commit, so GitHub skips those workflows. The
   surviving `pull_request_target` ones keep the list looking populated, so **an unbuilt commit is
   indistinguishable from a green one at a glance.** Suggest fixing forward: a required check only
   the real CI job can satisfy, or branch protection naming `Build & Test` explicitly rather than
   "all checks passed". ptone deferred this explicitly.

### Batch 3 — correctness and housekeeping

9. **Delete the now-obsolete deploy-time stopgaps.** `ptone/scion#1273` and `ptone/scion#1276` were
   fixed upstream (by `GoogleCloudPlatform/scion` PRs #1305 and #1306), so the workarounds this tier
   carried for them are dead code. They were operator settings rather than code, which is why the
   tier never blocked on them.
10. **Retest the `#1300` access-settings fix on a live deployment.** Upstream PR
    `GoogleCloudPlatform/scion#1300` added `AccessSettingsProvider` so all browser login paths read
    live settings. **This was verified by reading the merged code and has never been exercised on a
    running deployment.** The retest is outstanding. Flag that the untested half is
    security-relevant: `user_access_mode` gates who may log in.
11. **The design doc's §9.2 references are ambiguous and resolve wrongly upstream.** This is my
    error and you should say so neutrally in the issue. `.design/hosted/cloud-run-single-node.md`
    §9.2 uses bare `#1273`/`#1274`/`#1275`/`#1276`/`#1281`, which are **fork issue** numbers — but
    the file now lives in the **upstream** repo, where each resolves to an unrelated PR (see §2 of
    this brief for the table). The same table also cites genuine upstream numbers (`#1300`, `#1304`,
    `#1305`, `#1306`) in the same bare form, so a reader cannot tell which venue any number means.
    The fix is to fully qualify every reference in that section. **File this as an issue; do not fix
    the file yourself** — I want the fix reviewed, not slipped in.
12. **Commit an empty `image-build/.gitignore`.** `gcloud` 582.0.0 errors
    `Could not read ignore file .gitignore` unless one literally exists in `image-build/`, **even
    when `--ignore-file` correctly points elsewhere.** Found by the coordinator running the real
    omni build tonight; worked around with `touch`. Looks like a gcloud CLI quirk. Filing it so the
    next person running this build outside CI does not lose time to it.

## 6. What you must NOT do

- **Do not open an issue on `GoogleCloudPlatform/scion`.** Fork only. An upstream write attempt
  returns 403 anyway.
- **Do not write bare cross-repo references.** See §2. This is the whole reason for the rewrite.
- **Do not re-file `ptone/scion#1274` or `ptone/scion#1281`.** See §3.
- **Do not fix any of these problems.** Every one of these is a tracking issue only. Item 11 and
  item 12 are especially tempting one-line fixes — **do not make them.**
- **Do not touch any branch, any PR, or any code.**
- **Do not delete any Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t` are all do-not-delete.

## 7. Report back

Message `sn-impl-arch` with:

- The issue number and title for each of your four, as `ptone/scion#NNNN`.
- Any duplicate you found and did not file.
- Anything in your batch you think I have described wrongly. Several of these I wrote from a
  register rather than re-deriving from the code, so **if one looks wrong to you, say so** — I would
  rather correct the issue text now than have it mislead someone in three months.

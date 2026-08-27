# Brief: reconcile four `ptone/scion` tracking issues after `GoogleCloudPlatform/scion#1325`

Author: sn-impl-arch (architect). Date: 2026-08-27, 19:35. Task #83 (internal number).

**This is GitHub issue admin. Do not write code. Do not touch any branch.**

---

## 1. What changed

`GoogleCloudPlatform/scion#1325` was squash-merged to upstream `main` at 19:29:22Z as
**`c13d910b74245ff096332f38fa3e618da8c9ac2b`**.

It **deletes the `scion deploy-instance` Go command** and replaces it with
`scripts/single-node/deploy.sh`. I verified this on the merged tree: `scripts/single-node/deploy.sh`
exists, and `deploy-instance` appears nowhere under `cmd/` or `docs-site/`.

Four open issues on **`ptone/scion`** name the deleted command. **They do not all resolve the same
way, and that is the whole point of this task.** Closing them uniformly would silently drop two live
defects.

## 2. THE TRAP — read before you touch anything

An issue that names a deleted artifact is not thereby obsolete. Ask **"is the DEFECT still present
in `scripts/single-node/deploy.sh`?"**, not "does the command still exist?".

- If the defect survives → **retitle only. Do not close.**
- If the remedy is now impossible → recommend closure, **do not close it yourself**.

## 3. The four, one at a time

### 3a. `ptone/scion#1314` — **DO NOT CLOSE. DO NOT COMMENT. Report to me only.**

Authored by **ptone**, not by us. I checked this and I had it wrong the first time.

Substance: it asks for `deploy-instance` to ship in a published release so the tutorial can drop its
build-from-source workaround. `#1325` deletes the command and removes the Go toolchain prerequisite
entirely — the page now needs only `git` and `gcloud`. So its **problem is solved by a route it did
not consider**, and its **remedy is now impossible**.

**Your job: verify that reasoning against the merged tree and tell me if it is wrong.** Nothing else.
I will put the recommendation to ptone.

### 3b. `ptone/scion#1301` — **KEEP OPEN. Add one comment.**

Title concerns `deploy-instance` creating an IAP OAuth client and not printing its ID. That print is
now **step 6 of `scripts/single-node/deploy.sh`**. The issue is still valid; only the artifact moved.

Add a comment saying, in your own words:

- the behaviour now lives in `scripts/single-node/deploy.sh`, step 6;
- **step 6 is now untested.** `#1325` dropped two Go tests that were "does not panic" pins on that
  function, and nothing replaced them;
- so whoever fixes this should add the client-ID output **and** a test, in one change.

**Do not open a separate issue for the coverage gap.** I considered it and rejected it: `#1301` *is*
that print, and splitting one small piece of work across two issues makes it likelier neither half
gets done.

### 3c. `ptone/scion#1293` — **RETITLE. DO NOT CLOSE.**

Defect survives intact: `deploy.sh` still requires `--image`, and there is still no public default
image. Retitle so it names the script rather than the deleted command. Add a one-line comment noting
the artifact moved in `#1325` and the defect did not.

### 3d. `ptone/scion#1291` — **RETITLE. DO NOT CLOSE.**

Defect survives intact: an image-pull failure on first deploy is still undiagnosable. Same treatment
as 3c.

**Read each issue before retitling it.** If what you read contradicts what I wrote above — for
instance if the defect really has been fixed — **stop and tell me.** Do not proceed on my
description. Six people corrected me today and all six were right.

## 4. Also

Sweep `ptone/scion` open issues for any **other** reference to `deploy-instance` as a live command.
I listed four; I am not certain four is all of them. Report anything you find. Do not act on it.

## 5. Rules

- **Do not close any issue.** Not one. Closure recommendations come to me.
- **Do not edit issue bodies.** Titles and new comments only.
- **Do not touch `GoogleCloudPlatform/scion`.** Everything here is on the `ptone/scion` fork.
- **Fully qualify every issue number you write in prose** — `ptone/scion#NNNN` or
  `GoogleCloudPlatform/scion#NNNN`. **48 of 48 numbers in the range `#1270`–`#1320` exist in BOTH
  repositories**, so a bare `#1293` is genuinely ambiguous to a reader. This is measured, not a
  style preference.
- No branch, no PR, no push, no deploy.

## 6. Report

Message `sn-impl-arch` with:

1. `ptone/scion#1314` — does my reasoning hold against the merged tree? Yes or no, and why.
2. The exact old and new title for `#1293` and `#1291`.
3. The comment text you left on `#1301`.
4. Any other issue naming `deploy-instance`.
5. Anything in this brief that is wrong.

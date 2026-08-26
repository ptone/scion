# Brief: repack the single-node tier onto a clean integration branch

Author: sn-impl-arch (architect). Date: 2026-08-26.
Authorised by ptone, 22:21: *"you can dispatch to a developer agent - we can create net new
integration branches if needed."*

You are the developer. I designed this; I do not implement it. Read the whole brief before you
start. If a step contradicts what you find on disk, **stop and message me** — do not improvise
around it.

---

## 1. What you are doing, in one sentence

Assemble the complete single-node Cloud Run tier onto a **new** integration branch cut from
current `main`, minus the internal logs, minus a security fix that belongs to someone else's PR,
minus two unrelated drive-by changes, plus one refined design doc.

## 2. What you must NOT do

These are hard constraints. They are not style preferences.

- **Do not merge anything.** Merging is ptone's gate. That includes 1265, 1266, 1268, 1269.
- **Do not rebase, force-push, or otherwise rewrite** `scion/dev-rebase-1294`,
  `scion/sn-dev-ready`, or `scion/sn-ws-mount`. Those branches back open PRs. Leave them exactly
  as they are. Your work happens on a new branch.
- **Do not open the upstream PR.** See §8 — the venue matters and the timing is ptone's call.
- **Do not delete any Cloud Run Instance or any agent.** Several are load-bearing for other
  tasks. `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control` and `sn-ready` are explicitly
  do-not-delete.

## 3. Facts I verified, so you do not have to re-derive them

All checked against the remote on 2026-08-26 between 22:15 and 22:25.

**`scion/sn-ws-mount` already contains the entire tier.** This is the single most useful fact
here. Compared to `scion/sn-dev-ready` it is `ahead=6, behind=0`, so it is a strict superset. The
three PRs stack like this:

```
main
 └── scion/dev-rebase-1294   (PR 1266, the tier)
      ├── scion/sn-dev-ready (PR 1268, based on dev-rebase-1294)
      └── scion/sn-ws-mount  (PR 1269, based on dev-rebase-1294, and contains sn-dev-ready)
```

So you take content from **one** branch, `scion/sn-ws-mount`. You do not merge three.

- `scion/sn-ws-mount` vs `main`: `ahead=77, behind=2`, **68 files changed**.
- `main` head at time of writing: `1d1e4d76`.
- PR 1265 (`scion/security-fix-p0-s1`) was still **OPEN and unmerged** at 22:21. This matters —
  see §6.

**File breakdown of the 68:**

| Area | Files |
|---|---|
| `pkg/` | 23 |
| `.design/project-log/` | 23 |
| `image-build/` | 9 |
| `cmd/` | 7 |
| `.github/` | 3 |
| `web/` | 2 |
| `scripts/` | 1 |

## 4. The steps

### 4.1 Cut the new branch

From current `main`. Name it `scion/sn-tier` unless that is taken; if it is, ask me rather than
inventing a variant.

Bring in the content of `scion/sn-ws-mount`. How you do this is your call — merge, squash, or
cherry-pick — but the result must be that the tier's code is present and the branch has a sane,
reviewable history. A single squashed commit is acceptable and probably preferable given the
history on the source branches is 77 commits of iteration.

### 4.2 Drop all 23 internal log files

Delete everything under `.design/project-log/`. All of it. It does not land in the repo in any
form.

ptone, 22:14: *"all other project logs do not need to be durably recorded in the repo, and can
move to the scratchpad working project folder."* They are already in the scratchpad at
`/scion-volumes/scratchpad/projects/single-node/`, so nothing is lost. **Do not copy them
anywhere first — they are already safe.**

### 4.3 Add the one refined design doc

Take `.design/hosted/cloud-run-single-node.md` from branch `scion/sn-impl-arch` (my branch). It
is 416 lines. It is the durable design record for this tier and it replaces the 23 log files.

Do not edit it without telling me. If you find something in it that contradicts the code, that is
a real finding and I want to hear it — the doc is meant to describe what is actually there.

### 4.4 Remove the duplicated security fix

PR 1265 is the P0-S1 dev-auth fix: the server must refuse to start when dev auth is bound to a
non-loopback interface. **The tier branch contains a second copy of that same fix.** It should
not. It belongs to 1265.

Four files are *purely* that fix and should be **deleted from your branch entirely**:

| File | Change on the tier branch |
|---|---|
| `pkg/hub/web.go` | +22 −0 |
| `pkg/hub/web_test.go` | +80 −0 |
| `cmd/server_foreground_test.go` | +96 −0 |
| `cmd/server_bridge_test.go` | +89 −0 |

Two more files are **mixed** and must be edited, not deleted:

- `cmd/server_foreground.go` (+34 −4 overall). Roughly 9 lines are the dev-auth guard; the rest
  is genuine tier work. Remove only the guard.
- `scripts/cloudrun/deploy.sh` (+5 −0). Present in both PRs. Work out which lines are 1265's and
  drop only those.

**Verify my claim before acting on it.** I read these as pure-security-fix from the diff stats and
the shape of the change. If `pkg/hub/web.go` turns out to contain tier work too, do not delete it
— tell me.

### 4.5 Move the drive-bys out

Three files are unrelated to this tier and should travel separately, each on its own small branch:

- `web/embed.go` (+1 −1) — a comment fix, `npm run build:client` → `npm run build`. Trivially
  correct and trivially unrelated.
- `pkg/hub/handlers_health.go` (+18 −9) and `web/src/components/pages/diagnostics.ts` (+44 −0) —
  a `deploymentWarnings[]` field on the health response plus the UI that renders it. **The
  mechanism is a general hub feature and worth proposing on its own merits.** Only the Cloud Run
  warning string is ours. Split it: propose the mechanism separately; keep only the string in the
  tier if the tier still needs it.

Create the branches and leave them. Do not open PRs.

### 4.6 Verify

- The branch builds.
- `go test ./...` passes, or fails only in ways you can show are pre-existing on `main`.
- `git diff --stat main...HEAD` shows roughly the numbers in §5.

## 5. The expected result — and a correction to a number I gave earlier

| Stage | Files |
|---|---|
| Full tier today (1266 + 1268 + 1269) | 68 |
| Drop the 23 logs | 45 |
| Add the design doc | 46 |
| Remove the duplicated security fix (4 whole files) | 42 |
| Move the 3 drive-bys out | 39 |

**About 39 files, and roughly 2,000 fewer lines** (1,679 from the logs, 287 from the dedupe, ~63
from the drive-bys).

I previously told ptone "about 32-33 files". **That number was wrong and I am correcting it
here.** It was wrong twice over: it counted PR 1266 alone (63 files) rather than the full tier
including 1268 and 1269 (68 files), and it assumed the dedupe removed 5 whole files when it
removes 4 — `cmd/server_foreground.go` is mixed and stays. If your final count lands near 39,
that is success. Do not chase 32.

## 6. The one ordering dependency you must respect

Your branch will **not** contain the dev-auth security fix, because you removed it in §4.4 and
PR 1265 has not merged yet.

That is correct and intended, but it means **your branch must not land before 1265.** The tier
ships a `deploy-instance` command that stands up a publicly reachable hub; landing it without the
dev-auth guard in place would be a real exposure.

Put this in the branch description or a `DO NOT MERGE BEFORE 1265` note. Once 1265 lands on
`main`, rebase your branch on `main` — **rebasing *your own new* branch is fine**, it is only the
existing shared ones you must not touch.

## 7. What is deliberately NOT in scope

- The four upstream issues (1273, 1274, 1275, 1276). Other teams own those. Do not work around
  them and do not fix them here.
- The tutorial and deploy scripts. Separate task, not yet approved.
- Rewriting PR 1266's description. It becomes moot once the new branch exists.

## 8. When you are done

Message me (`sn-impl-arch`) with the branch name and the actual `git diff --stat`. Do **not** open
the PR yourself.

The reason is a workflow detail that is easy to get wrong: **`ptone/scion` is a fork.** The true
upstream is `GoogleCloudPlatform/scion`. A PR on the fork is only a review venue — work actually
lands via a **separate upstream PR under a different number**, after which the fork PR is closed
unmerged. I verified this pattern on five branches (1272→1300, 1271→1297, 1260→1294, 1262→1295,
1256→1293). Opening the wrong PR in the wrong place wastes a review cycle. ptone decides when and
where.

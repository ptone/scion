# Brief: merge restored main into the tier branch, and get CI to actually run

Author: sn-impl-arch (architect). Date: 2026-08-27. Follows task #56.

**DO NOT START until sn-impl-arch tells you ptone has confirmed.** This brief is written in advance
so it is ready the moment he answers. If you were dispatched with this brief and no explicit
"confirmed" from me, **stop and ask me.**

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find on disk, **stop and message me** — do not improvise
around it. That rule has caught several of my own errors on this project, three of them yesterday.

---

## 1. What you are doing, in one sentence

Merge `GoogleCloudPlatform/scion` `main` into `ptone/scion` `scion/sn-tier` with an ordinary merge
commit, so that `#1310` goes to `behind=0` and its head commit finally gets built.

## 2. Why this is not the routine chore it looks like

Two separate things happened overnight and you need both in your head.

### 2a. Upstream main was regressed, then force-pushed back

Upstream PR **#1301** merged at 03:15:33Z and reverted two already-merged PRs — **#1307** (a P0
security fix) and **#1302** (the Cloud Run Instances runtime). ptone force-pushed `main` back to
**`f876e27b`**, pre-#1301. Fork main is synced to match.

I verified the restoration myself against the API, so you do not need to re-derive it:

| check | value |
|---|---|
| upstream main | `f876e27b` |
| `IsLoopbackHost` / `non-loopback` in `pkg/hub/web.go` | 6 |
| `NewCloudRunRuntimeFromInstances` | `(*CloudRunRuntime, error)` — two-value, the working form |
| `"not yet implemented"` | 2 |
| `#1310` `mergeable` | **true** |

**The `pkg/runtime/factory.go` conflict has already cleared on its own.** You should not have to
resolve anything. **If you hit a merge conflict, stop and tell me** — that would mean something moved
again and my premise is stale.

### 2b. The head commit of #1310 has never been built. This is the real reason for the task.

| commit | pushed | workflows that ran |
|---|---|---|
| `728d17cd` | 03:08:46 | CI, GitHub Actions Scan, Google Admin scan — **10 check-runs** |
| `38ba412e` | 03:35:25 | GitHub Actions Scan only — **6 check-runs** |

`38ba412e` was pushed *after* #1301 landed, while `#1310` was `CONFLICTING`. **A conflicted PR has no
computable merge commit, so GitHub silently skips every `pull_request` workflow.** Only
`GitHub Actions Scan` ran, because it is `pull_request_target` and runs against the base.

So `sn-review-dev`'s R1/R2/R4/R5 work — context threading in `diRESTCall` and `diRunGcloud`, the
`resizeSandboxTerminal` helper, `waitForSandboxLiveness` and its five new tests — **has zero CI
signal.** It was reported as locally green and I have no reason to doubt that, but nothing has been
independently verified.

**Expect the possibility that CI fails, and do not assume a failure is your merge.** If the build or
tests break, the overwhelmingly likely cause is R1/R2/R4/R5 finally being exercised. Diagnose before
you attribute. Tell me what you find either way.

## 3. The change

```
git fetch upstream main
git checkout scion/sn-tier
git merge upstream/main        # ordinary merge commit
git push origin scion/sn-tier  # NO force
```

Use whatever remote names your clone actually has — verify with `git remote -v` rather than
assuming `upstream` exists.

**A plain merge, not a rebase, and this is deliberate:**

1. `#1310` has **six inline bot review comments** attached to specific lines. A force-push detaches
   them and they read as unaddressed.
2. ptone is **squash-merging**, so branch history is collapsed at merge time regardless. A rebase
   buys a tidier history that nobody will ever see.

## 4. Before you push — a cheap local check

The head has never been built, so build it yourself before you hand CI a surprise:

```
go build ./...
go test ./cmd/... ./pkg/runtime/... ./pkg/runtimebroker/...
```

**`go build ./...` does not compile test files.** I made exactly that mistake yesterday and reported
a repair as smaller than it was. A green `go build` is a much weaker signal than it looks — run the
tests.

Known-noisy suites, do not be alarmed and do not "fix" them:

- `internal/fixturegen` fails identically on `main`. Pre-existing.
- `pkg/hub` has a flaky race that passes on retry, and takes ~250s. If it fails, **re-run it once**
  before reporting.

## 5. What you must NOT do

- **Do not rebase. Do not force-push.** See §3.
- **Do not resolve a merge conflict.** There should not be one. If there is, stop and message me.
- **Do not touch `.github/`.** Those files were deliberately removed at ptone's direction and the
  branch must stay at 37 files.
- **Do not touch `image-build/`.** Settled by `sn-cloudbuild-dev`; do not reopen it.
- **Do not open, edit, close or comment on any PR.** `#1310` updates itself when you push. Agents
  have fork write access only, by design — an upstream comment attempt returns 403.
- **Do not try to fix `cla/google`.** It fails on agent-authored commits and is a known non-blocker.
  Merged PR #1304 had the same author and merged with it red.
- **Do not merge `#1310`.** That is ptone's gate, not ours, and not yours.
- **Do not delete any Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t` are all do-not-delete.

## 6. Verify — and count the checks, do not just read them

1. `#1310` shows **`behind: 0`**, `mergeable: true`, and **37 changed files**. The merge must add no
   files. If the count moved off 37, stop and tell me.
2. **Count the check-runs on the new head.** This is the whole point of the task:
   ```
   gh api repos/GoogleCloudPlatform/scion/commits/<newsha>/check-runs --jq '.total_count'
   ```
   **Expect 10.** If you get 6, the `pull_request` workflows were skipped again and the merge did not
   achieve its purpose — stop and tell me rather than reporting success.
3. `CI` must appear and must have actually run. Confirm `Build & Test` is present by name.
4. `cla/google` red is expected. Everything else should be green or `skipped`.
5. `mergeable` may come back **`null`** on your first query. GitHub computes it on demand — the first
   call triggers, the second reads. **Do not report `null` as a conflict.** Query twice.

## 7. Report back

Message `sn-impl-arch` with:

- The merge commit SHA.
- The **count** of check-runs on the new head, and the status of `Build & Test` by name.
- `behind`, `mergeable`, and the file count.
- Your local `go test` result, including whether `pkg/hub` needed a retry.
- If anything failed: your read on whether it is the merge or R1/R2/R4/R5 finally being exercised.

If a premise here turns out to be wrong — especially if there **is** a conflict, or the check count
stays at 6 — **stop and tell me.** I would much rather revise this brief than have you build on a
wrong premise of mine. Several of mine have been wrong on this PR already.

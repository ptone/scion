# Brief: rebase the single-node tier onto upstream main, now that its gate has landed

Author: sn-impl-arch (architect). Date: 2026-08-27.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find on disk, **stop and message me** — do not improvise
around it. That rule has already caught two of my own errors on this project.

---

## 1. What you are doing, in one sentence

Rebase `scion/sn-tier` onto **upstream** `main` — which has just gained the dev-auth guard that was
this branch's blocker — resolve two conflicting files, and confirm the guard is present afterwards.

## 2. What you must NOT do

- **Do not merge anything.** Merging is ptone's gate.
- **Do not open, close, or edit any PR.** Fork PR `ptone/scion#1282` tracks this branch and updates
  itself when you push.
- **Do not touch any other branch.** Not `scion/security-fix-p0-s1`, not `scion/dev-rebase-1294`,
  not `scion/sn-dev-ready`, not `scion/sn-ws-mount`, not either `scion/sn-driveby-*`.
  `scion/sn-tier` only.
- **Do not re-add a copy of the dev-auth guard.** It now lives in `main`. This branch must inherit
  it, not duplicate it. A second copy is the exact thing we removed two hours ago.
- **Do not delete any Cloud Run Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`,
  `q2-control` and `sn-ready` are do-not-delete.

## 3. Critical: rebase onto UPSTREAM main, not fork main

**The fork's `main` is stale.** I checked at 00:29: `ptone/scion` main was 3 commits behind
`GoogleCloudPlatform/scion` main, and upstream has moved again since.

If you rebase onto fork `main` you will produce a branch that looks clean locally and is still
behind the branch it targets. **This already bit the previous rebase on this project tonight.**

Add upstream as a remote and rebase onto `upstream/main`:

```
git remote add upstream https://github.com/GoogleCloudPlatform/scion.git
git fetch upstream main
git rebase upstream/main    # onto UPSTREAM, not origin
```

Push back to `origin` (the fork). Only the fork.

## 4. Facts I verified, so you do not re-derive them

Checked against the remotes at 00:31–00:33 on 2026-08-27.

- **The guard is now in upstream `main`.** `pkg/hub/web.go:439` defines `IsLoopbackHost`; the fatal
  check is at `:462`. It landed as `f22db257` (PR #1307).
- `scion/sn-tier` vs upstream main: **`ahead=6, behind=8`, status `diverged`.**
- **The conflict surface is exactly two files.** I intersected the 11 files the 8 upstream commits
  touch against our 40:

  - `cmd/server_foreground.go`
  - `cmd/server_foreground_test.go`

  Nothing else overlaps.

- **`pkg/runtime/factory.go` does NOT conflict.** I predicted it would, in the PR description and
  in the design doc. **That prediction was wrong and you should not go looking for it.** Our branch
  already registers all three runtimes — `cloudrun`, `cloudrun-instances`, `cloudrun-sandbox` — and
  none of the 8 upstream commits touches `factory.go`.

## 5. How to resolve the two conflicts

Both files are conflicted for the same reason: **our branch edited them as tier work, and upstream
has since added the dev-auth guard and its tests to the same files.**

The resolution is **keep both**:

- Upstream's guard (`initWebServer` returning an error) — **inherited, not reproduced**.
- Our tier's changes to the same file — **preserved**.

They are not alternatives. If you find yourself choosing between them, you have misread the
conflict — **stop and message me**.

Watch for the trap the last rebase hit: **git may auto-merge one of these silently and wrongly.**
On the previous branch it produced a stale call signature that only surfaced at compile time. Build
before you trust the merge.

## 6. Verify — this is the part that matters

1. The branch builds.
2. `go test ./...` passes, or fails only in ways you can **show** are pre-existing on upstream main.
   `internal/fixturegen TestFixtureCoverage` is known pre-existing — verify, do not assume.
3. **The guard is present in the rebased branch.** `grep IsLoopbackHost pkg/hub/web.go` must find
   it. If it is missing, the rebase dropped our whole reason for waiting — that is a stop-and-tell-me.
4. **The guard's tests pass**: `TestInitWebServer_DevAuth_NonLoopback_Rejected`, `TestIsLoopbackHost`,
   `TestNewWebServer_DevAuth_NonLoopback_Rejected`.
5. **There is exactly ONE copy of the guard**, not two. Check that `IsLoopbackHost` is defined once.
6. `git diff --stat upstream/main...HEAD` — expect **~40 files**. A materially different number
   means something was lost or gained; tell me the number either way.
7. Fork PR #1282 should go back to `mergeable=MERGEABLE`.

## 7. Report back

Message `sn-impl-arch` with:

- How you resolved each of the two files, in a sentence each.
- `git diff --stat` against upstream main, and the file count.
- Test results, including the four guard tests by name.
- Confirmation that `IsLoopbackHost` appears exactly once.
- #1282's `mergeable` / `mergeStateStatus` after the push.

If anything required a judgement call, **say so explicitly** rather than burying it. I would much
rather hear "I had to choose here" than find it in review.

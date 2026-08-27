# Brief: drop the three CI workflow files from the tier PR

Author: sn-impl-arch (architect). Date: 2026-08-27. Follows task #56.

**DO NOT START until sn-impl-arch tells you the decision is confirmed.** This brief is written in
advance so it is ready the moment ptone answers. If you were dispatched with this brief and no
explicit "confirmed" from me, **stop and ask me**.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find on disk, **stop and message me** — do not improvise
around it. That rule has caught six of my own errors on this project in the last two days, two of
them on this very PR tonight.

---

## 1. What you are doing, in one sentence

Remove all three `.github/workflows/` changes from `scion/sn-tier`, so that
`GoogleCloudPlatform/scion#1310` stops failing two CI checks.

## 2. Why — and it is not "our workflow is broken"

Two checks fail on head `a9131f1f`. They are **independent**. An earlier report called the second
one downstream of the first; it is not. I checked the annotations.

### Failure 1 — `Build and Push Omni Image`

```
denied: installation not allowed to Create organization package
```

The build **succeeds**. Only the push is refused. `ghcr.io/googlecloudplatform/scion-omni` does not
exist, and the `GITHUB_TOKEN` app installation is not allowed to create an org-level package.

`publish-omni.yml` triggers on `pull_request` with paths `**.go`, `go.mod`, `go.sum`, `web/**`,
`image-build/**`. **So merging it puts a red check on every future Go PR upstream, not just ours.**
That is the reason this is being dropped rather than tolerated.

### Failure 2 — `zizmor-output`

11 unique findings. **10 are pre-existing upstream content**, not ours:

| File | Lines | Ours? |
|---|---|---|
| `build-images.yml` | 78, 81, 85, 94–97 | no — upstream lines 75/78/82/91–94, shifted +3 by our `on:` edit |
| `build-release.yml` | 23, 49, 102 | no |
| `build-release.yml` | 24 (`packages: write`) | **yes, one line** |

`zizmor-output` is `skipped` on any PR that does not touch `.github/`. That is why #1300, #1302,
#1304 and #1307 never saw it. Ours is the first PR in a while to touch workflows, so it is the first
measured against a policy upstream's own files already violate.

**We are not fixing those 10.** Cleaning up upstream's workflow security debt is a real and useful
job, but it is not this PR's job, and doing it here would balloon the review surface of a tier PR.

## 3. The change

Three files, all under `.github/workflows/`:

| File | Our change | What to do |
|---|---|---|
| `publish-omni.yml` | added, +124 | **delete the file** |
| `build-release.yml` | modified, +56 −0 | **revert to upstream `main`'s version** |
| `build-images.yml` | modified, +5 −2 | **revert to upstream `main`'s version** |

Take the two reverted files verbatim from `GoogleCloudPlatform/scion` `main`. Do not hand-edit them
back. Hand-reverting is how you end up one whitespace character away from a diff that still shows.

**Verify after: those three paths must not appear in the compare diff at all.** Not "appear with an
empty patch" — not appear.

## 4. How to land it

- **Add a commit on top. Do not rebase and do not force-push.** #1310 is open and under review; a
  force-push there rewrites what a reviewer may already be looking at.
- #1310 tracks the branch. It updates itself when you push. **Do not touch the PR.**
- Push to `origin scion/sn-tier`.

## 5. What you must NOT do

- **Do not pin actions to hashes** anywhere. See §2. That is upstream's debt and a separate PR.
- **Do not remove only `packages: write`** and leave the rest. It fixes 1 finding of 11. The check
  still fails and we have gained nothing.
- **Do not touch any other file in the branch.** The branch is at 40 files. It must land at 37.
- **Do not open, edit or close any PR.**
- **Do not delete any Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t` are do-not-delete.

## 6. Verify

1. `git diff` against upstream `main` shows **37 files**, down from 40.
2. No path under `.github/` in that diff.
3. Build passes. `go test ./cmd/...` passes.
4. Nothing else in the branch referenced these workflows — I already checked, the count was zero for
   `publish-omni`, `omni-image`, `build-release.yml` and `build-images.yml`. Confirm it still holds
   after your change.
5. On #1310 after the push: `zizmor-output` should go to **`skipped`**, and
   `Build and Push Omni Image` should **disappear entirely**. If either still runs, the revert is
   incomplete — tell me before doing anything else.
6. `cla/google` will still fail. That is expected and is not a blocker. Merged PR #1304 had the same
   agent commit author and merged with it red.

## 7. A thing worth knowing that this brief does not fix

Design-doc goal **G4** says *"Deployment is one command against a GCP project — no registry setup"*.
`deploy-instance` requires `--image` and has no default. So G4 is unmet today, and dropping these
workflows does not make it any less met — it was already unmet, because the push fails.

What closes G4 is an org owner creating `ghcr.io/googlecloudplatform/scion-omni` and making it
public. Then these three files come back as a small follow-up PR. **That is not your task.** I am
telling you so you do not read the deletion as us abandoning CI image publishing.

## 8. Report back

Message `sn-impl-arch` with:

- The commit SHA.
- The file count against upstream main.
- The status of `zizmor-output` and `Build and Push Omni Image` on #1310 after the push — the actual
  values, not "they're fine".

If anything contradicts §2 or §3 — especially if reverting the two files produces a diff you did not
expect — **stop and tell me.** I would much rather revise this brief than have you build on a wrong
premise of mine. Two of my premises on this PR were wrong tonight.

# Brief: sn-omni-ci — give the omni image a CI build and publish path

**Dispatched by** `sn-impl-arch`, 2026-08-26. ptone offline ~7–10 h.

## Why this matters

The omni image is **what a Cloud Run Instance boots** in this tier — it is the
deployment artifact, not a convenience. §1's success criterion is *"an operator runs
one deploy command"*, and a deploy command cannot name an image that nothing publishes.

## Read first

**§4.2a-ci-rev** in `cloudrun-instances-sandboxes.md`. It contains the design and,
importantly, a correction: the original §4.2a-ci diagnosis was **wrong in both
directions**. Read the correction before you start, so you don't re-derive the wrong
problem.

## The actual state, verified 2026-08-26

- `build-images.sh` defaults to `BUILDER="local-docker"`.
- `build-images.yml` runs on `ubuntu-latest` and **does not pass `--builder`** — so the
  GitHub runner is already a working amd64 Docker builder for this.
- ⇒ **omni is not locked out of CI.** The `cloud-build.sh` gate blocks one *backend*.
- The **only** blocker is that `omni` is missing from the `target` choice list in
  `build-images.yml`.
- **And the larger gap:** `build-images.yml` has only `workflow_dispatch` /
  `workflow_call` triggers, and `build-release.yml` **never calls it**. Nothing in this
  repo publishes any image automatically.

## The work

| # | Change |
|---|---|
| 1 | Add `omni` to the `target` enum in `build-images.yml` — **both** the `workflow_dispatch` and `workflow_call` blocks. |
| 2 | Have `build-release.yml` call `build-images.yml` on tag push with `target: omni`. Tag the image with both the release tag and `latest`. |
| 3 | **`platform: linux/amd64` for omni, explicitly.** Cloud Run runs amd64 and the thick base is amd64-only (§4.2b) — an arm64 leg is impossible here, and inheriting the `all` default will fail or waste an hour. |
| 4 | Fix the misleading error in `cloud-build.sh`. It currently reads *"omni target chains harness images and must be built locally"*, which sounds like a property of the artifact; it is a property of one backend, and that wording is what produced the wrong diagnosis. Say instead: the omni chain has no Cloud Build yaml — use `--builder local-docker`, the default, which works in CI. |
| 5 | Document the **registry + tag convention** the deploy command should default to, so `--image` can be optional. Coordinate with `sn-impl-em3`, who is building that command — message them, don't guess. |

## ⚠️ The risk to verify before you commit to change 2

**Disk.** The omni chain is six sequential images over an amd64-only thick base of
~2 GB. A standard `ubuntu-latest` runner has roughly **14 GB** free. This may not fit.

**Verify this empirically before wiring the release trigger** — ptone said fork PRs are
fine for getting CI runs, so use one. If it doesn't fit, the options are: a
free-disk-space step, a larger runner, or reinstating Cloud Build for this target.
Commit `eb3bb709` deleted an orphaned `cloudbuild-omni.yaml`; if we go back to Cloud
Build, recover it from git history as a starting point rather than writing from scratch.

**Report the measured image sizes and peak disk either way** — that number is useful
beyond this task.

## Working rules

- **Integration branch is `scion/dev-rebase-1294`; do not create a fourth branch.**
  ptone asked for branch consolidation tonight. Fork PRs for CI runs are fine and
  explicitly sanctioned.
- **Do not merge anything.** #1265 and #1266 are open; merging is ptone's gate.
- **Do not rebase or force-push the integration branch.**
- If a CI run fails for a reason unrelated to your change, **report it, don't fix it**.

## Reporting

Message `sn-impl-arch` with: what landed, the disk numbers, and whether the release
trigger is wired or blocked. A truthful partial beats an optimistic claim — several
reports in this project today have claimed work that wasn't there, and I check.

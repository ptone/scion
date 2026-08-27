# Brief: make cloudbuild-omni.yaml actually reachable, and actually conventional

Author: sn-impl-arch (architect). Date: 2026-08-27. Follows task #56.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find on disk, **stop and message me** — do not improvise
around it. That rule has caught several of my own errors on this project, two of them yesterday.

**Do not start until `sn-ciscope-dev` has finished pushing to `scion/sn-tier`.** I will tell you.
Two developers pushing to one branch under upstream review is an avoidable collision.

---

## 1. Why this exists

ptone approved dropping the three GitHub Actions workflow files, but the approval was
**conditional**. Verbatim, 02:47 today:

> we can drop the github workflow image builds - our current practice is to manually run our
> cloud-build targets from the image-build dir ... **as long as we have a sound cloudbuild file for
> that which follows the existing conventions** that should be find. For our beta testers we can
> share a pre-built image.

`image-build/cloudbuild-omni.yaml` exists in the branch. I audited it against its seven siblings.
**It does not currently meet that condition.** Five things must change. I verified every one of them
myself against the branch, so you should not need to re-derive them — but check me if you like.

## 2. M1 — the file cannot be reached. This is the embarrassing one.

`image-build/scripts/builders/cloud-build.sh`, lines 52-57 **on our branch**:

```bash
    omni)
      echo "cloud-build: no cloudbuild-*.yaml for target 'omni'." >&2
      echo "The omni chain has no Cloud Build config. Use --builder local-docker" >&2
      echo "(the default), which works both locally and in GitHub Actions CI." >&2
      return 1
      ;;
    thick-prep) file="cloudbuild-thick.yaml" ;;
```

`image-build/README.md` line 136, **same branch**:

```
| `omni` | `cloudbuild-omni.yaml` |
```

So the PR ships a Cloud Build config, documents its mapping, and then hard-refuses to use it with an
error saying it does not exist. This is leftover from an earlier revision that added the refusal
before the YAML was written, and it was never reconciled.

It is also now doubly wrong: the message directs the user to GitHub Actions CI, which is the exact
path ptone has just asked us to delete.

**Fix:** replace the whole `omni)` arm with the one-line form every other target uses:

```bash
    omni) file="cloudbuild-omni.yaml" ;;
```

Match the placement and style of its neighbours.

## 3. M5 — no immutable tag. This is the one that matters most to the stated use case.

Our file, line 157-158:

```yaml
images:
  - '$_REGISTRY/scion-omni:$_TAG'
```

`cloudbuild-hub.yaml` and every other sibling double-tag:

```yaml
      - '-t'
      - '$_REGISTRY/scion-hub:$_SHORT_SHA'
      - '-t'
      - '$_REGISTRY/scion-hub:$_TAG'
```

Confirmed by count: siblings contain `_SHORT_SHA`, ours contains it **zero** times.

ptone's plan is to **share a pre-built image with beta testers**. With only a mutable tag, a tester
can pin nothing — `:latest` moves under them and a bug report cannot be tied to an artifact. Add the
`$_SHORT_SHA` tag to the final `build-scion-omni` step and list **both** coordinates under `images:`.

## 4. M3 — no `verify-registry` pre-flight, in the file where it matters most

All seven siblings open with the identical first step:

```yaml
  - name: 'gcr.io/cloud-builders/gcloud'
    id: 'verify-registry'
    entrypoint: 'bash'
    args: ['image-build/scripts/verify-registry.sh', '$_REGISTRY']
```

Ours has zero occurrences; its first step is `build-thick-prep`. `cloudbuild-core-base.yaml` states
the intent: *"Pre-flight: verify push access to catch permission errors early."*

Ours is an **eight-stage chain that pushes only at the very end** via `images:`. So a permissions
problem is discovered after the entire chain has run rather than before it starts. ptone wants a
**semi-private** registry — a repo whose ACL is the entire point. This is the worst possible file to
drop the permission pre-flight from.

`image-build/scripts/verify-registry.sh` already exists. Add the step, copied from a sibling.

## 5. M4 — `$COMMIT_SHA` should be `$_COMMIT_SHA`, or the version stamp is empty

Ours, lines 58 and 149: `- 'GIT_COMMIT=$COMMIT_SHA'`. `cloudbuild-scion-base.yaml` uses
`GIT_COMMIT=$_COMMIT_SHA`.

Two distinct consequences, both real:

1. `$COMMIT_SHA` is a Cloud Build **built-in**, populated only for trigger-based builds from a
   connected repo. The invocation in our own header comment is `gcloud builds submit` from local
   source, where it is empty.
2. `cloud-build.sh` passes the value only when it sees the *user* substitution:
   ```bash
   if grep -q '_COMMIT_SHA' "${config}"; then
     subs="${subs},_COMMIT_SHA=${commit_sha}"
   fi
   ```
   The string `GIT_COMMIT=$COMMIT_SHA` does **not** contain the substring `_COMMIT_SHA`. So even
   after M1 is fixed, the builder would never pass it.

**Fix:** both lines to `$_COMMIT_SHA`, and declare `_COMMIT_SHA` in `substitutions:` with a default,
exactly as the siblings do.

## 6. M2 — missing Apache license header

Ours starts `# Omni image chain for ...`. All seven siblings start `# Copyright 2026 Google LLC`
followed by the Apache block. Our own `image-build/omni/Dockerfile` **has** the header, so this is a
one-file oversight, not a policy. Copy the header verbatim from `cloudbuild-hub.yaml`.

## 7. Also fix — the `gcloudignore-omni` pattern is unwired

`image-build/gcloudignore-omni` (83 lines) is new and has no sibling. The **need is real**: the root
`.gcloudignore` excludes `web/src/`, `web/*.json`, `web/*.ts`, `web/*.html`, and omni's Dockerfile
does `COPY web/ ./web/` then `npm install && npm run build`. Without the override the build fails
ENOENT on `web/package.json`.

The **wiring is missing**. `cloud-build.sh` submits with no `--ignore-file`:

```bash
  local -a cmd=(
    gcloud builds submit --async
    --project="${project}"
    --substitutions="${subs}"
    --config="${config}"
    "${REPO_ROOT}"
  )
```

So once you fix M1 and `--target omni` starts routing to the YAML, **the build will fail with the
exact error `gcloudignore-omni` was written to prevent.** M1 without this is worse than neither.

**Fix generically, not as a special case:** in `cloud-build.sh`, when a file
`image-build/gcloudignore-<target>` exists, append `--ignore-file` to the submit command. That way
the next target with the same problem inherits the mechanism. Do not hardcode `omni`.

## 8. Also fix — the README row is factually wrong

Line 63 says: *"Hub + all harnesses ... Uses existing `scion-base:<tag>`."* Both halves are false.

- **Not all harnesses.** The chain is `claude, codex, opencode, antigravity, grok-build` — five. The
  upstream catalog has eight; `copilot`, `gemini-cli` and `hermes` are omitted.
- **Not "uses existing scion-base".** The Cloud Build path builds `thick-prep` and then builds
  `scion-base` *on top of thick-prep*, from scratch.

Correct both. Also add a builder-divergence note, in the style the README already uses for
`thick-prep`: under `cloud-build` the omni image is thick-based; under `local-docker` it starts from
whatever `scion-base:<tag>` already exists. Same target name, two lineages — that must be written
down. And mention `gcloudignore-omni` once you have wired it.

## 9. Judgement calls I am leaving to you — tell me what you chose

- **`timeout: 7200s`.** The closest analogue, `cloudbuild-thick.yaml`, sets `14400s` for *less*
  work — ours adds a full web build (`npm install && npm run build`) and uses no buildx cache.
  I think it should be `14400s`. I have not timed a real run and neither has anyone else. If you
  disagree, say why.
- **The five-of-eight harness subset.** Probably deliberate and correct for a single-node image. But
  the list is duplicated in **three** places: `resolve_targets`, `_OMNI_CHAIN` in `targets.sh`, and
  the step sequence in the YAML. `targets.sh` calls itself "the single source of truth for which
  images exist". Reduce the duplication if you can do it cleanly. **If you cannot do it cleanly, do
  not force it** — tell me instead, and leave a comment saying the subset is deliberate.
- **The header comment, lines 10-34,** hardcodes `ptone-experiments`, an impersonation recipe, and
  `gs://ptone-experiments_cloudbuild/source`. No sibling contains operator-specific content. This
  branch is under review **upstream**, so it reads as personal scratch notes. The technical content
  about `CLOUDSDK_BILLING_QUOTA_PROJECT` / `SERVICE_DISABLED` is genuinely useful — generalise the
  placeholders rather than deleting it.

## 10. What you must NOT do

- **Do not rebase and do not force-push.** #1310 is open and under review.
- **Do not touch anything under `.github/`.** `sn-ciscope-dev` owns that.
- **Do not open, edit or close any PR.** #1310 updates itself.
- **Do not restructure the chain.** The `docker build` choice over `buildx`, the amd64-only stance,
  and the absence of `waitFor` are all **correct and I verified them** — each stage must be resident
  in the local daemon for the next stage's `BASE_IMAGE`, and a step with no `waitFor` waits for all
  prior steps, which is what a sequential chain wants. Leave all three alone.
- **Do not delete any Instance or agent.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t` are all do-not-delete.

## 11. One thing I could not verify, which you should

The audit could not confirm that **Node and `npm` are present** in the `thick-prep` → `scion-base`
lineage that omni's `RUN cd web && npm install && npm run build` depends on. If they are not, the
final stage fails regardless of everything above. Check `image-build/thick-prep/Dockerfile` and
`image-build/scion-base/Dockerfile`. **Report what you find either way.**

## 12. Verify

1. `bash -n` on the modified shell script; `shellcheck` clean (CI runs it).
2. The YAML parses.
3. `image-build/scripts/build-images.sh --builder cloud-build --target omni` resolves to
   `cloudbuild-omni.yaml` instead of exiting 1. You do **not** need to run a real build.
4. `grep -c '_SHORT_SHA'`, `grep -c 'verify-registry'`, `grep -c '_COMMIT_SHA'` on
   `cloudbuild-omni.yaml` all return non-zero.
5. No `$COMMIT_SHA` without the leading underscore remains.
6. File count against upstream main is unchanged from whatever `sn-ciscope-dev` leaves it at — you
   add no files and delete none.

## 13. Report back

Message `sn-impl-arch` with the commit SHA, each of §9's three judgement calls and what you chose,
your §11 finding, and #1310's check status after the push.

If a fix turns out to be wrong or to break something, **stop and tell me.** I would much rather
revise this brief than have you build on a wrong premise of mine.

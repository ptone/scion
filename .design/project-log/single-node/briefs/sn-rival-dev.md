# Brief: resolve the #1315 conflict — a rival Cloud Run page landed upstream

Author: sn-impl-arch (architect). Date: 2026-08-27, 15:05. Task #71.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
touch anything.** If what you find contradicts this brief, **stop and message me** — do not
improvise. Three developers corrected me today and all three were right.

**There is a gate in §3 that can invalidate the whole plan. Clear it first.**

---

## 1. What happened

`GoogleCloudPlatform/scion#1315` (our single-node Cloud Run tutorial, branch `scion/sn-docs-dev`,
head `724d8a6d`) was MERGEABLE at 14:30 and is **CONFLICTING** at 15:00.

Upstream `main` moved `3aeb7729` → `06a3130d`, commit *"docs: nightly doc update Aug 27 (Permissions
Foundation, Cloud Run, Helm P2-3) (#1314)"*. That commit **added a second Cloud Run page for our
tier**: `docs-site/src/content/docs/hosted/single-node/hub-setup-cloudrun.md`.

Two conflicting files:

| file | why |
|---|---|
| `docs-site/astro.config.mjs` | both sides added a Cloud Run entry **in the same sidebar slot**, different slugs and different indentation |
| `docs-site/src/content/docs/hosted/single-node/overview.md` | both sides added a Cloud Run section and a link |

## 2. The upstream page is wrong, and that is why we are not simply deferring to it

`hub-setup-cloudrun.md` is 36 lines. I read it. Three defects, in descending order:

1. **Its only instruction is a command that does not exist.** It says
   `make deploy-cloudrun-sandbox`, annotated *"refer to your internal tooling or scripts
   directory"*. **I verified this**: `git grep deploy-cloudrun-sandbox` across upstream `main`
   matches **only that document**, and the upstream `Makefile` contains **no deploy targets at
   all**. A reader fails on the first command. Our command is `scion deploy-instance`.
2. **It implies durability is attainable** — *"lost or reset ... unless a persistent network volume
   is attached"*. Our measured §5 is pure ephemeral Tier 0. It also names only redeploy/scale-down
   and misses the loss event that actually bites: **exceeding the agent ceiling destroys the
   Instance**, ~8s after an HTTP 201, with no instrument to warn anyone.
3. **It never mentions IAP**, which is the entire auth perimeter of this tier.

**Do not treat this as a turf dispute and do not editorialise about it in the diff.** The upstream
page is a generated summary that got ahead of the implementation. Ours is a walked, measured
tutorial. We are replacing content, not scoring a point. **No commit message or doc text may
disparage the other page.**

## 3. THE GATE — settle this before you write anything

**Is `hub-setup-cloudrun.md` machine-generated, and will tonight's nightly job overwrite it?**

The commit that added it is titled *"nightly doc update"*. If a generator recreates that path from a
manifest every night, then putting our tutorial there means **our tutorial is silently reverted
tonight**, and my plan in §4 is wrong.

Find out. Look for the workflow or script behind "nightly doc update" (`.github/workflows/`, any
docs-generation tooling), and for a manifest or page list naming `hub-setup-cloudrun`. Establish
whether the generator **creates only missing files** or **rewrites existing ones**.

- If it does not overwrite existing files, or no generator exists → proceed with §4.
- If it **would** overwrite our page → **stop and message me.** The decision changes and it is mine
  to change, not yours.

Report what you found either way, with the file path and the evidence. "I could not find one" is an
acceptable and useful answer — say it plainly rather than concluding there is none.

## 4. The plan, if the gate is clear: adopt the upstream slug

**Our tutorial content moves to `hub-setup-cloudrun.md`. `cloud-run.md` ceases to exist.**

Rationale, so you can tell if I am wrong: one page and one slug for one tier; upstream's existing
inbound link keeps resolving; and if the generator ever does regenerate that path, it collides with
a file we own and shows up as a **diff we can see**, rather than quietly publishing a second, wrong
page beside ours. **`starlightLinksValidator` is enabled**, so a dangling link is a build failure —
that is protection, not an obstacle, and it is why you must fix every inbound reference.

Concretely, on branch `scion/sn-docs-dev`, merging current upstream `main` in:

1. `git mv` our `cloud-run.md` → `hub-setup-cloudrun.md`, our content winning wholesale. Keep the
   upstream page's **frontmatter `title`** (`Deploy on Cloud Run (Sandbox)`) if ours differs —
   upstream's sidebar label and the HA guide's cross-references were written against it.
2. `astro.config.mjs` — take **upstream's** line for the sidebar entry (`hub-setup-cloudrun`, label
   *"Deploy on Cloud Run (Sandbox)"*) and **upstream's indentation** for that whole block. Ours only
   differed because we added a differently-named page. Resolve to a file that differs from upstream
   `main` by **zero lines** here if you can — check with `git diff`.
3. `overview.md` — resolve by hand. Upstream's line 74 already links
   `/scion/hosted/single-node/hub-setup-cloudrun/`; that link now points at our tutorial and should
   stay. Where the two sides describe the tier in prose, **prefer the measured statement over the
   generated one** — in particular do not let upstream's "unless a persistent network volume is
   attached" survive into the merged text. Keep it to one coherent section, not both sides stapled
   together.
4. Fix every remaining reference to the old path. I found these on our branch:
   `docs-site/astro.config.mjs`, `docs-site/src/content/docs/hosted/single-node/overview.md`,
   `scripts/single-node/README.md`. **Re-grep after your merge — that list is from before it.**

**Merge upstream into the branch. Do not rebase and do not force-push.** `git push origin
HEAD:scion/sn-docs-dev`.

## 5. What must survive the merge unchanged

These are load-bearing and were each fixed for a measured reason today:

- The `PATH` block: **`export PATH="$(go env GOPATH)/bin:$PATH"` — prepend, not append.** A stale
  `scion` earlier on `PATH` otherwise wins and fails with `unknown command "deploy-instance"`
  rather than `command not found`. Both symptoms are named in the text. Keep both.
- The `git clone` + `go build -tags no_embed_web` prerequisite steps.
- The `:::caution[Always specify harnessConfig]` block and the troubleshooting entry
  *"Agent create returns 502: harness-config "antigravity" not found"*. **These stay until
  `ptone/scion#1316` phase 4 lands. Do not remove them as stale — they are current.**
- The `:::caution[Temporary workaround — check before you build]` block.
- The IAP/OIDC wording. A reviewer asserted IAP rejects OAuth access tokens; a six-way matrix
  measured otherwise and the claim was declined. **Do not let the merge reintroduce it.**

## 6. Reference rule — absolute

Every issue or PR number in prose must be **fully qualified** (`ptone/scion#NNNN` or
`GoogleCloudPlatform/scion#NNNN`). We measured this today: sweeping `#1270`–`#1320`, **48 of 48
numbers exist in both repositories**. A bare number is never safe from context. Before committing,
grep for `#1[0-9]{3}` not preceded by a repo slug; the answer must be zero.

I broke this rule myself in a brief this morning, so it is not a rule I am confident you will keep
by intention alone. Grep.

## 7. What you must NOT do

- **Do not open or merge any PR.** ptone opens upstream PRs. Push the branch and stop.
- **Do not rebase or force-push** `scion/sn-docs-dev`, and do not touch `#1265`/`#1266`.
- **Do not touch** branches `scion/sn-docpr-upstream` or `scion/sn-buildfix-upstream`
  (`GoogleCloudPlatform/scion#1317` and `#1316` — both MERGEABLE, leave them that way).
- **Do not delete or edit `hub-setup-gce.md`, `auth.md`, or any other single-node page.** Only the
  three files in §4 plus the renamed tutorial.
- **Do not deploy or delete anything.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk` are **do-not-delete**;
  `sn-ready` is ptone's live Instance.
- **Do not fix any other defect you notice.** Tell me instead.

## 8. Report back

Message `sn-impl-arch` with:

- **The §3 gate answer first**, with evidence.
- The merge commit SHA, and confirmation that `gh pr view 1315` reports `MERGEABLE` again.
- The `astro.config.mjs` diff against upstream `main` (ideally empty).
- Confirmation that the unqualified-ref grep returned zero, and that every §5 item survived —
  quote the `PATH` line back to me.
- **Anything in this brief you think I have got wrong.** Say it plainly; I would rather be corrected
  than agreed with.

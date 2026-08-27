# Review brief: the deploy CLI backout and the image removal

Author: sn-impl-arch (architect). Date: 2026-08-27, 16:45. Task #73.

You are the code reviewer for branch **`scion/sn-backout`** on `ptone/scion`, written by
`sn-backout-dev` against its brief at
`/scion-volumes/scratchpad/projects/single-node/briefs/sn-backout-dev.md`. **Read that brief.** It
is what the developer was told to do; your job includes judging whether the brief itself was wrong.

**Your verdict is the verdict.** I will not re-run your checks or second-guess your conclusion —
that is the process here and I was corrected on it today. If you say this is not ready, it is not
ready. Say so plainly and say what would make it ready.

---

## 1. What the change is, and why

ptone reviewed the published single-node Cloud Run tutorial and asked for three things, verbatim:

> we should only have a script in scripts/ we should NOT be adding to scion cli surface for deploy.
> back that out we should not share the actual ptone-misc image. that is not fully public. share an
> example and include steps to cloud build submit to get your own.

So: `scion deploy-instance` (828 lines of Go) is deleted; `scripts/single-node/deploy.sh` — until
now a 94-line wrapper that just `exec`'d the command — absorbs the logic in bash; every reference
to our `ptone-misc` image comes out of the docs; and the page gains `gcloud builds submit` steps so
a reader builds their own image.

Context you need in order to weigh severity: this tier's tutorial is **published**, at
`https://googlecloudplatform.github.io/scion/hosted/single-node/hub-setup-cloudrun/`. Strangers run
these commands. The tier runs with `invokerIamDisabled: true`, so **IAP is the sole network
perimeter** — there is no IAM invoker check behind it.

## 2. Where to look

```bash
git fetch https://github.com/GoogleCloudPlatform/scion.git main
git fetch https://github.com/ptone/scion.git scion/sn-backout
git diff FETCH_HEAD_MAIN...FETCH_HEAD_BRANCH   # resolve these properly; do not trust my shorthand
```

Baseline is current upstream `main` (`98a9d9c2` when I measured; **fetch fresh**). The Go original
you are comparing the bash against is `cmd/deploy_instance.go` at that baseline — read it before
you read the replacement.

## 3. The single most important check: Gate 2

`diAssertPerimeter` — step 7 of the original, labelled in-source as the **most valuable
deliverable** of the command. It sends an **unauthenticated** request to the new Instance and
requires that it **does not get through**. It is the only thing preventing us from shipping a
script that prints a success banner over a Cloud Run service that is open to the internet.

**The specific way a bash rewrite breaks this:** the natural idiom for "call a URL" is `curl -f`,
which exits **non-zero on 403** — and 403 is exactly the response that means *the gate passed*.
Inverted polarity here yields a script that fails on a correctly-secured deploy, or worse, one
whose error handling swallows the inversion and passes on an open one.

Do not review this by reading. **Exercise it.** At minimum:

1. Confirm on a live deploy that Gate 2 ran and reported a pass, and that the pass came from an
   actual unauthenticated probe returning a rejection — not from an exit code nobody checked.
2. Make Gate 2 fail on purpose. The honest way is to point the probe at something reachable
   without IAP. If the script can be made to report success while the perimeter is open, that is a
   **blocking** finding and I want it immediately, not in a summary at the end.

Gate 1 (`diWaitForIAP`, step 4) matters less but check its timeout and retry behaviour survived.

## 4. The eight steps and the four measured fixes

`runDeployInstance` had eight numbered steps: resolve identity; resolve project number; create the
Instance via `gcloud` **v1** surface (v1 is required for `sandboxLauncher`) and enable IAP via REST
**v2 PATCH**; Gate 1; bind the IAP policy at region level; print effective access; Gate 2; print
the URL. Confirm all eight are present and ordered.

Four lines in the original are not plumbing — each is a fix for a defect we measured live, and each
is the kind of thing that looks like noise during a translation:

| Must survive | What breaks without it |
|---|---|
| `SCION_IMAGE_REGISTRY` | Deploy succeeds, then cannot start a single agent. |
| `SCION_SERVER_MODE=hosted` | Hub enables dev auth and refuses to boot. |
| Stripping `gcloud`'s impersonation warning from captured stdout | The warning text is spliced into the Instance URL and the IAP audience. |
| Admin-email resolution | Nobody is an admin; §1 step 2 cannot complete. |

Also confirm the **nine flag names are byte-identical** to the original: `name`, `project`, `image`,
`region`, `cpu`, `memory`, `admin-email`, `service-account`, `image-registry` — same defaults, same
three required. The published flag table depends on this, and `--image-registry` was documented
upstream today.

## 5. The pin

`cmd/deploy_instance_test.go` pinned the IAP audience format against the hub's own
`isSupportedIAPAudience`, plus a URL/audience agreement test and a `services`-not-`instances` test.
Deleting the Go file deletes that pin, and a wrong audience means IAP login fails — a blocker CI
would otherwise catch.

I asked for a replacement: a Go test that reads the format strings **out of `deploy.sh`** and feeds
them to the same validators. Check that it exists, that all three assertions survive, and — the
part that matters — **that it actually fails when the string in the script is wrong.** A pin that
cannot fail is decoration. The developer was asked to verify this; verify it independently anyway,
because it is two minutes of work and it is the whole point.

Judge the design too. If reading a format string out of a shell script with a regex strikes you as
too brittle to survive a routine edit, say so — that was my call, not the developer's, and I would
rather hear it now.

## 6. The docs

File: `docs-site/src/content/docs/hosted/single-node/hub-setup-cloudrun.md`, plus
`scripts/single-node/README.md`.

- **`git grep ptone-misc` must return zero.** Six references came out, including an
  `@sha256:` digest.
- The example image must be an obvious **placeholder in the reader's own project**. A real-looking
  registry path someone might actually try to pull is a finding.
- The `gcloud builds submit` steps derive from the comment block at the top of
  `image-build/cloudbuild-omni.yaml`. **That documented invocation is ours and carries our quirks:**
  `CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT`, `CLOUDSDK_BILLING_QUOTA_PROJECT`,
  `--gcs-source-staging-dir=gs://<project>_cloudbuild/source`, `--async`. None of that belongs in a
  public tutorial, and `--async` is actively wrong when the next step needs the image to exist.
  If any of it survived into the page, that is a finding.
- The Go toolchain prerequisite (~50 lines: `go build -tags no_embed_web`, the `GOPATH/bin`
  install, the `PATH` **prepend**, the `--help` check, the `unknown command` troubleshooting pair)
  should be gone, **but only if `deploy-instance` really was the sole use of the `scion` binary in
  the page**. I asserted that from a grep. Check it. If any step still needs the binary and the
  block was removed anyway, the tutorial is now broken for every reader.
- These must be **untouched**: the `:::caution[Always specify harnessConfig]` block and the
  `harness-config "antigravity" not found` troubleshooting entry (both stay until
  `ptone/scion#1316` phase 4); the `:::caution[Temporary workaround]` block; all IAP wording
  including the sole-perimeter caution; the durability section naming both loss events; the
  `--image-registry` row.
- **Read the page as a stranger would**, top to bottom, and tell me whether it still works as a
  tutorial. Four commits of surgery on a 491-line page tends to leave seams — a prerequisite that
  refers to a step that no longer exists, a flag table describing a command that is now a script.

## 7. The live walk

Deploy with the rewritten script and walk the tier's acceptance path end to end:

> open the `run.app` URL, log in, create a project, start a Claude agent, attach to its terminal
> from the browser, and watch it commit to a git remote.

- Project `ptone-experiments` (number `721899303052`), region `us-east4`.
- Name your Instance **`sn-backout-t`**. **Delete it when you are finished.**
- For *your test only*, use `us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni:f99a818`. It stays
  out of the docs; it does not stay out of your test.
- Credentials come from the metadata server — **no key file**. Impersonate
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.
- **Do not print access tokens to stdout.** That has happened on this project before.
- **Follow the tutorial's own text**, not the developer's report. If the page and the script
  disagree, the page is wrong and that is a finding.

**Do not delete, restart, or touch:** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
`sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, and above all **`sn-ready`, which is
ptone's live Instance**.

Two known traps that will otherwise cost you time: the hub reports agents as `running` when the
sandbox entrypoint has hung, so do not treat a phase reading as evidence the agent works; and
exceeding the agent ceiling destroys the entire Instance about eight seconds after a `201`, so
create one agent, not several.

## 8. Rules

- **Do not fix what you find.** Report it. If something is a one-line typo, still report it —
  the developer owns the branch.
- **Do not open a PR, rebase, or force-push.** ptone opens upstream PRs.
- Fully qualify every issue number: `ptone/scion#NNNN` or `GoogleCloudPlatform/scion#NNNN`. We
  measured 48 of 48 numbers in `#1270`–`#1320` existing in **both** repositories.

## 9. Report

Message `sn-impl-arch` with a verdict — **ready**, **ready with non-blocking findings**, or
**not ready** — and then:

1. Gate 2: what you observed, and what happened when you tried to make it lie.
2. The pin: does it fail when the script is wrong?
3. The live walk, step by step, with the step that broke if one did.
4. Findings, ordered by severity, each with file and line.
5. **Anything wrong in the developer's brief or in this one.** Both are mine. Four people corrected
   me today and all four were right; you are welcome to be the fifth.

Raise blocking findings the moment you have them. Do not save them for the report.

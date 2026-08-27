# Brief: the script owns the deploy; the CLI command goes; the ptone-misc image goes

Author: sn-impl-arch (architect). Date: 2026-08-27, 16:35. Task #73. Raised by ptone.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
touch anything.** If what you find contradicts this brief, **stop and message me** — do not
improvise. Four developers corrected me today and every one of them was right.

**This is the largest single change on this tier since the tier merged.** There are three traps:
§4 (the two gates), §5 (the pin you will be tempted to silently drop), and §7 (our private
build flags, which must not reach a public page).

---

## 1. What ptone asked for, verbatim

> we should only have a script in scripts/ we should NOT be adding to scion cli surface for deploy.
> back that out we should not share the actual ptone-misc image. that is not fully public. share an
> example and include steps to cloud build submit to get your own.

Three changes. All three are decided — none of them is open for redesign. What is open is *how*,
and §4/§5 are where the how matters.

## 2. Start here

Base off **current upstream `main`**, not the fork's `main`, not any tier branch.

```bash
git fetch https://github.com/GoogleCloudPlatform/scion.git main
git checkout -b scion/sn-backout FETCH_HEAD
```

Main was `98a9d9c2` when I measured. **Fetch fresh; do not trust any SHA I quote.** Push to the
fork: `git push origin scion/sn-backout`. Only remote is `origin` = `ptone/scion`.

**One branch, four commits, in this order.** I am deliberately not splitting this across branches:
the code change and the doc change edit the same region of the same tutorial, and two branches
would conflict with each other. We lost time to exactly that today.

## 3. Commit 1 — `deploy.sh` absorbs the deploy logic

`scripts/single-node/deploy.sh` is 94 lines and its real content is one line:
`exec "$SCION_BIN" deploy-instance "$@"`. It must become the implementation.

The good news, which I verified so you do not have to guess: **`cmd/deploy_instance.go` uses no Go
GCP client library.** Its imports are stdlib plus `cobra`. Every GCP call is either
`exec.Command("gcloud", ...)` (via `diRunGcloud`) or a plain `net/http` REST call (via
`diRESTCall`). So the translation target is `gcloud` plus `curl`, and it is a translation, not a
redesign.

`runDeployInstance` has eight numbered steps. **All eight must survive**, in order:

1. Resolve identity
2. Resolve project number
3. a) Create/update the Instance via `gcloud` (v1 surface — the v1 surface is required for
   `sandboxLauncher`); b) enable IAP via REST v2 `PATCH`
4. **Gate 1** — wait for IAP reconcile
5. Bind the IAP access policy at the region level
6. Read back and print effective access
7. **Gate 2** — assert the perimeter
8. Print the URL

**Flag names must stay byte-identical** to the nine the command takes today: `name`, `project`,
`image`, `region`, `cpu`, `memory`, `admin-email`, `service-account`, `image-registry`. Same
defaults, same three required flags (`name`, `image`, `project`). Two reasons: the tutorial's flag
table then stays true without being rewritten, and `--image-registry` was documented and merged
upstream less than an hour ago. **If you find a flag I have miscounted, stop and tell me.**

Several lines in that file are not general-purpose plumbing — each one is a live-measured defect
fix, and each will look like noise you can drop. **Grep for these and carry every one across:**

- `SCION_IMAGE_REGISTRY` — without it the deploy succeeds and then cannot start a single agent.
- `SCION_SERVER_MODE=hosted` — without it the hub enables dev auth and refuses to boot.
- The handling that strips `gcloud`'s **impersonation warning** out of captured stdout. Without it
  the warning text is spliced into the Instance URL and the IAP audience.
- The admin-email resolution path.

If you cannot tell whether a line is load-bearing, assume it is, and ask me.

## 4. TRAP 1 — the two gates, and why Gate 2 is the point of the whole script

Steps 4 and 7 are the two gates. Read them in the source before you write a line of bash:
`diWaitForIAP` and `diAssertPerimeter`. The source comment on step 7 calls it the **most valuable
deliverable** of the command, and that is not decoration.

**Gate 2 sends an unauthenticated request and requires that it does NOT get through.** It is the
only thing standing between us and a script that prints a cheerful success banner over a Cloud Run
Instance that is open to the internet. This tier runs with `invokerIamDisabled: true` — IAP is the
*sole* network perimeter. There is no second line of defence behind this check.

A bash rewrite makes it very easy to lose, because the natural bash idiom for "call a URL" is
`curl -f`, which exits non-zero on a 403 — the exact response that means **the gate passed**. Get
the polarity right, and make the failure message say plainly that the perimeter is open.

Both gates must keep their current pass/fail conditions, their timeouts, and their retry
behaviour. **If you change any timing value, tell me the old value, the new one, and why.**

Do not use `curl -f` for the probes. Capture the status code explicitly and branch on it.

## 5. TRAP 2 — the pin you must not silently drop

`cmd/deploy_instance_test.go` is not a formality. It contains pinning tests that check the deploy's
own output against the hub's own parser:

- `TestBuildIAPAudienceAcceptedByIsSupportedIAPAudience` — the audience the deploy builds must be
  accepted by `isSupportedIAPAudience`.
- `TestBuildInstanceURLMatchesIapAudienceToCloudRunURL` — the URL and the audience must agree.
- `TestBuildIAPAudienceUsesServicesNotInstances` — IAP's vocabulary is `services`, not `instances`.

The two builders are one line each:

```go
// https://%s-%s.%s.run.app          (name, projectNumber, region)
// /projects/%s/locations/%s/services/%s   (projectNumber, region, name)
```

**If you delete the Go file, you delete the pin.** Bash cannot be unit-tested against a Go
validator, and the failure mode when the audience is wrong is that IAP login fails — a §1 blocker
that CI would no longer catch.

**Commit 2 replaces the pin before commit 3 removes it.** The shape I want: a Go test that *reads
`scripts/single-node/deploy.sh` from disk*, extracts the two format strings, substitutes sample
values, and feeds the results to the same `isSupportedIAPAudience` and `iapAudienceToCloudRunURL`
the current tests use. That keeps one authoritative copy of each string — in the script — and keeps
CI able to catch a bad edit to it. The three assertions above must all still be made.

**Order matters: land the replacement pin before you delete the original.** Do not leave a commit
in between where neither exists.

If you cannot make the replacement work, **stop and message me**. Do not delete the pin and
mention it in your report. That is the single outcome I am most concerned about.

## 6. Commit 3 — remove the CLI surface

Delete `cmd/deploy_instance.go` and `cmd/deploy_instance_test.go`. Remove the registration in
`cmd/root.go` (line ~90, the `case "help", "version", ... "deploy-instance"` list) and
`cmd/cli_mode.go` (line ~111, `"deploy-instance": true`).

`go build ./...` and `go test ./...` must both pass. **`scion deploy-instance --help` must return
`unknown command`** — and note that this is also the symptom of a stale binary, so verify against a
binary you built from your branch, not one on your `PATH`.

Hidden commands, build tags, and a second `main` package are **not** acceptable substitutes. ptone
said "back that out". A command that still ships in the binary has not been backed out.

## 7. Commit 4 — the docs

File: `docs-site/src/content/docs/hosted/single-node/hub-setup-cloudrun.md` (491 lines), plus
`scripts/single-node/README.md`.

### 7a. Replace the command

5 occurrences of `scion deploy-instance` become the script. Check what the correct invocation is
from the repo root and use one form consistently.

### 7b. The Go toolchain prerequisite can go — but verify first

I grepped every `scion`-binary use in the page. **`deploy-instance` is the only one.** So lines
~51–101 — the `go build -tags no_embed_web` block, the `mkdir`/`mv` into `$(go env GOPATH)/bin`,
the `export PATH=...` prepend, the `scion deploy-instance --help` verification, the
`scion: command not found` / `unknown command "deploy-instance"` troubleshooting pair, and the
"no web UI assets" note — all become dead, and the reader no longer needs Go at all.

**Re-grep and confirm this yourself before you delete 50 lines of a published page.** If any other
step still needs the `scion` binary, the block stays and you tell me.

The reader still needs `git` (to clone for the build in §7c) and `gcloud`. Update the CLI-tools
table accordingly: `go` comes out, `git` stays.

This is a genuine improvement, not just compliance — it removes the stale-binary trap that cost us
a troubleshooting entry. Say nothing about that in the docs. Just let the page be shorter.

### 7c. The image — example only, plus build-your-own

Remove all 6 `ptone-misc` references (5 in the tutorial at lines ~142, 145, 174, 184, 362; 1 in
`scripts/single-node/README.md` line ~24). That includes the `@sha256:e3eab113...` digest.

Replace with a **placeholder in the reader's own project**, e.g.
`us-central1-docker.pkg.dev/YOUR_PROJECT/scion/scion-omni:YOUR_TAG`. **Do not invent a
real-looking registry path** that a reader might actually try to pull, and do not substitute
another project of ours. There is no public image to point at; the page must say so plainly and
without apology.

Then add the build-your-own steps. **The source of truth is the comment block at the top of
`image-build/cloudbuild-omni.yaml` (around line 27).** Read it. Do not invent the command.

**The trap:** that documented invocation is *our* invocation, and it carries our project's quirks:
`CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT`, `CLOUDSDK_BILLING_QUOTA_PROJECT`,
`--gcs-source-staging-dir=gs://<your-project>_cloudbuild/source`, `--async`. A reader building in
their own project with their own credentials needs none of the impersonation machinery, and
`--async` is wrong for a tutorial where the next step needs the image to exist. Keep what a generic
reader needs — `--project`, `--config`, `--substitutions`, `--ignore-file` — and drop the rest.
State that the build takes roughly ten minutes.

The substitutions the config requires are `_TAG`, `_SHORT_SHA`, `_COMMIT_SHA`, `_REGISTRY`.
Confirm that list against the file rather than against this brief. Note that only `scion-omni` is
pushed, via the `images:` section — so the reader's `--image` is `$_REGISTRY/scion-omni:$_TAG`.
Mention that the Artifact Registry repository must exist first, and give the one command that
creates it.

Keep it proportionate: this is a prerequisite section, not an image-build guide. If it runs past
about 30 lines you have overwritten it.

### 7d. Do not touch

These are load-bearing and each was fixed for a measured reason:

- The `:::caution[Always specify harnessConfig]` block and the troubleshooting entry for
  `harness-config "antigravity" not found`. **They stay until `ptone/scion#1316` phase 4 lands.**
- The `:::caution[Temporary workaround — check before you build]` block.
- All IAP wording, in particular the caution that the Cloud Run invoker IAM check is disabled and
  IAP is therefore the sole perimeter.
- The durability section naming both loss events.
- The `--image-registry` table row and the two sentences above it, merged upstream today.

Do not retitle the page, do not change the frontmatter, do not reorder sections.

## 8. AMENDED 16:40 — a code reviewer owns verification, not you and not me

**This section replaces an earlier version that asked you to do the live walk. Do not do it.**

A separate `code-reviewer` agent owns the review and the live §1 walk on this change. That is the
process here, and I was wrong to fold it into your task. **Do not deploy anything. Do not create,
restart, or delete any Instance.**

What you still owe on your own work:

- `go build ./...` and `go test ./...` pass on your branch.
- `scion deploy-instance --help` returns `unknown command` on a binary built **from your branch**
  (not one on your `PATH` — that is the stale-binary symptom, not proof).
- `bash -n scripts/single-node/deploy.sh` parses, and `shellcheck` is clean if it is available.
- The new audience pin test passes, and you can show it failing when the format string in
  `deploy.sh` is deliberately broken. **A pin that cannot fail is not a pin** — check that once,
  then restore the string.

That last item is the only extra verification I want from you, and it is cheap. Everything else —
the live deploy, Gate 2 firing for real, the §1 walk — belongs to the reviewer.

Do not chase the reviewer's findings pre-emptively by testing against a live Instance "just to be
sure". That is the duplication we are removing.

## 9. Rules

- **Fully qualify every issue/PR number** you write: `ptone/scion#NNNN` or
  `GoogleCloudPlatform/scion#NNNN`. Sweeping `#1270`–`#1320`, **48 of 48 numbers exist in both
  repositories**. Before committing, grep for `#1[0-9]{3}` not preceded by a repo slug; the answer
  must be zero. I broke this rule myself today, so grep rather than trust care. Internal defect
  numbers like "#38" are not GitHub issues — omit them or say plainly that they are internal.
- **Do not open a PR.** ptone opens upstream PRs. Push the branch and stop.
- **Do not rebase or force-push** anything. Do not touch `#1265`/`#1266`.
- **Do not delete or restart** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-ready`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`. **`sn-ready` is ptone's live Instance** —
  do not touch it at all.
- **Do not fix any other defect you notice.** Tell me instead.

## 10. Report back

Message `sn-impl-arch` with:

1. The four commit SHAs and `git diff --stat` against upstream `main`.
2. **The §5 answer first if it went wrong** — is the audience pin still enforced by CI, and how?
   Include the result of deliberately breaking the format string.
3. Your independent confirmation of §7b: is `deploy-instance` really the only use of the `scion`
   binary in the tutorial? Quote your grep.
4. The §8 self-checks: build, tests, `bash -n`, `unknown command`.
5. **A handover note for the code reviewer**: which of the eight steps you are least confident in,
   what you could not test without a live deploy, and anything you changed from the Go original
   rather than translated. Be candid — the reviewer walking straight at your weakest point is the
   cheapest outcome available to us. Hiding it costs a whole deploy cycle.
6. Confirmation that the unqualified-ref grep returned zero and that `git grep ptone-misc` returns
   zero outside your own notes.
7. **Anything in this brief you think I have got wrong.** Say it plainly. I would rather be
   corrected than agreed with, and today that has paid off four times.

Send the same report to the code reviewer when it is dispatched. I will tell you its name.

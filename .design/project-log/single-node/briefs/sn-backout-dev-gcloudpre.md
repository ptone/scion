# Brief: make `deploy.sh` fail early and clearly on an older gcloud

Author: sn-impl-arch (architect). Date: 2026-08-27, 18:05. Task #79.
Branch: **`scion/sn-backout`** — the one you already own. Do not start a new branch.

You are the developer. Read the whole brief before you write anything. If it contradicts what you
find, **stop and message me** — six people corrected me today and all six were right.

---

## 1. What was measured

`sn-iaplogin-inv` ran your `scripts/single-node/deploy.sh` on **gcloud 575.0.0**. It failed:

```
==> Step 3a: Creating/updating Cloud Run Instance (gcloud, v1 surface)...
    gcloud beta run instances deploy sn-iaplogin-t2 --image
ERROR: (gcloud.beta.run) Invalid choice: 'instances'.
This command is available in one or more alternate release tracks.  Try:
  gcloud alpha run instances
```

At 575.0.0 the `instances` noun exists only under `alpha`. The script uses `beta ... deploy`.

This is **not** a bug in your translation. The Go command had the same dependency. The failure is
real, it is on the published path, and the script does nothing to explain it.

**Your 582.0.0 prerequisite line in the tutorial is now confirmed by measurement.** I accepted that
line rather than softening it. That was right.

## 2. Why this is a defect and not merely a documented prerequisite

The page does say "Cloud SDK 582.0.0 or later". The script does not check, and the error it
produces never mentions a version.

Worse: gcloud's own advice — `Try: gcloud alpha run instances` — **points the reader at a wrong
fix.** The alpha surface uses `create`, not `deploy`, and does not support `--sandbox-launcher`.
Follow it and you get an Instance that comes up and whose scion server crashes on startup.

That is exactly what happened to the investigator, who had read a brief telling it which script to
run. If someone holding the full context falls into this, a stranger following the published page
certainly will.

This is the tier's recurring failure shape — see internal defects #39, #46, #22: an error message
that does not name its cause, plus adjacent advice that leads away from the fix.

## 3. What to build

A **preflight capability check**, run **before step 1**, so nothing is half-created when it fails.

**Do not parse a version number and compare it to 582.**

We know only that the command is absent at 575.0.0 and present at 582.0.0. The first good version
is **unmeasured**. Hardcoding 582 would encode a number we cannot support, and would wrongly reject
anyone on 576–581 if the noun exists there. We have already been burned once this week by writing
down a number nobody measured.

Probe the capability instead: ask gcloud whether `beta run instances deploy` exists, discard its
output, and branch on whether that succeeded. `--help` on the subcommand is the cheap way; if you
find something cheaper or more reliable, use it and tell me what you chose.

On failure, exit non-zero with a message that does five things:

1. names the missing command;
2. says the single-node tier requires it;
3. states honestly what we know: absent at 575.0.0, present at 582.0.0;
4. tells the reader to run `gcloud components update`;
5. **explicitly warns against the alpha surface that gcloud itself suggests** — say that alpha uses
   `create` rather than `deploy`, does not support `--sandbox-launcher`, and yields an Instance
   whose server crashes on startup.

Item 5 is the valuable part of this change. The other four are hygiene. A tool that steers a reader
away from the wrong fix its own dependency recommends is worth more than one that merely fails
politely.

Keep the message short enough to read in a terminal. Do not turn it into a document.

## 4. Constraints

- **`scripts/single-node/deploy.sh` only.** If you believe the tutorial also needs a line, tell me;
  do not write it. Three tasks have already targeted that page today and one of them is frozen
  pending an answer from ptone.
- Keep the file's existing shape: **sourceable functions, main guard, no side effects at file
  scope.** That seam is what keeps the ported tests alive. Your preflight is a function called from
  `di_main`, not a bare command.
- Add a test for it in the shell-function test file, in the style of the ones you already wrote.
  It should cover both branches. If the capability probe is awkward to test without a real gcloud,
  say so rather than writing a test that cannot fail — we have shipped a decorative pin before and
  I would rather have no test than a false one.
- `shellcheck scripts/single-node/deploy.sh` must stay clean.
- **Do not open a PR, rebase, or force-push.** ptone opens upstream PRs. The compare URL for this
  branch is already with him, so **message me the moment you push** — the URL he holds will be
  pointing at an older head until you do.
- Fully qualify every issue number you write: `ptone/scion#NNNN` or `GoogleCloudPlatform/scion#NNNN`.
  48 of 48 numbers in `#1270`–`#1320` exist in both repositories. `#39`, `#46`, `#22` and `#79` in
  this brief are internal task numbers — if you reference one, say so plainly.
- **Do not deploy anything.** You cannot test the failure branch on this container anyway: it ships
  gcloud 575.0.0, which is the broken version. That is convenient — you can exercise the failure
  path for real, and only the failure path.

## 5. Report back

Message `sn-impl-arch` with:

1. the new head SHA;
2. how you chose to probe the capability, and why;
3. the exact text of the failure message;
4. what the preflight does on **this** container (gcloud 575.0.0) — paste the real output;
5. whether the test can actually fail, and how you know;
6. anything in this brief you think is wrong.

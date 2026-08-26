# Brief: spike-oq2 — can a sandbox reach the launcher, and how?

**Dispatched by** `sn-impl-arch`, 2026-08-26. ptone is away ~7–10 h; do not wait on him.

## Why this matters more than it looks

OQ-2 has been carried for weeks as a latency-and-simplicity question. It is not.
It now decides **two** design outcomes:

1. **Whether phase P7 exists at all.** P7 is transport-token refresh, marked *"only
   if hairpinning"*.
2. **Whether P6 needs a transport-auth story.** With IAP as the sole perimeter
   (OQ-17 forced `invokerIamDisabled: true`), *anything* arriving at the Instance
   from outside must carry an IAP-acceptable credential. If agents must reach the
   hub by its public URL, every agent needs an OIDC token minted for the IAP OAuth
   client — a whole subsystem. If agents reach the launcher **locally**, they
   bypass the perimeter entirely and that subsystem never gets built.

So: a cheap test now removes or creates a large amount of work.

## The question, precisely

An agent runs inside a **Cloud Run Sandbox**, created by the launcher container on
a Cloud Run **Instance**. The hub's HTTP server listens **in the launcher
container**. Can the process inside the sandbox reach it, and by what address?

Test each of these and report the result for every one — including the failures,
which are as informative as the successes:

| # | Path to try | Notes |
|---|---|---|
| 1 | `127.0.0.1:<port>` from inside the sandbox | Only works if the sandbox shares the launcher's network namespace. |
| 2 | The launcher's container IP (find it; don't assume) | |
| 3 | `169.254.x.x` / metadata-style link-local | |
| 4 | A **unix socket** bind-mounted into the sandbox | ⚠️ Very likely dead — `spike-uds` found every socket-crossing test fails on a real Sandbox (§4.4/§4.4a). Confirm rather than re-derive, and do not spend long on it. |
| 5 | The Instance's **public `run.app` URL** (the hairpin) | The fallback. If this is the only path, P7 lives. |

**Vary `--allow-egress`.** Sandboxes are created with it today. Test with and
without: if reachability depends on it, that is a design constraint, because
egress also governs whether an agent can reach the public internet.

For whichever paths work, measure **latency** (a hundred sequential small requests,
report median and p95) — the hub is chatty and this figure feeds the design.

## Ground rules — these are not negotiable

- **Test on a real Cloud Run Instance with real Sandboxes.** Not `unshare`, not
  local Docker, not a self-installed `runsc`. This project has already produced one
  confident wrong answer from a substitute mechanism, and a second one would be
  worse than no answer.
- **A negative result is a result.** If nothing but the hairpin works, say so
  plainly. Do not stretch to find a positive.
- Report the **mechanism**, not just the outcome — *why* a path works or fails.
  "It works" is not usable; "the sandbox has its own netns and no route to the
  launcher bridge" is.

## Practical notes

- Project `ptone-experiments`, region `us-east4`. Credentials from the **metadata
  server**; the container SA holds Token Creator on
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`. **Do not
  print access tokens to stdout** — that has happened in this project before.
- **Step 0:** `bash /scion-volumes/scratchpad/update-gcloud.sh` (2–4 min → 582.0.0).
  Containers ship a stale gcloud and missing subcommands look like permission errors.
- Deployment mechanics: `deploy-instance-with-sandbox.md` (read its banner first;
  the REST sections are superseded by `--sandbox-launcher` on 582.0.0).
- **Delete your Instance when finished** — unlike `iap-demo` and `val-delete-2`,
  this one is a spike and should not outlive it.

## Report

Message `sn-impl-arch` with the table filled in, the mechanism for each result, the
latency figures, and a one-line answer to: **does P7 need to exist?** Append your
findings to `ac0-results.md`. Raise a blocker to me immediately if Instance creates
are failing — there was a multi-region 503 outage earlier today; if it has returned,
that is not your fault and I want to know rather than have you grind on it.

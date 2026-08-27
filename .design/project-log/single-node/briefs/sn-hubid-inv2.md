# Brief: what is `hub_id` for, and is the hostname it falls back to stable?

Author: sn-impl-arch (architect). Date: 2026-08-27, 21:05. Task #75 (internal number).
**This is the SECOND run. A previous investigator answered Q1 and was blocked on Q2.**

**Investigation, not implementation. Do not fix anything. Do not change the deploy script.**

---

## 0. Read this first — what happened last time, and what changed

**Q1 is already answered. Do not redo it.** The previous investigator found `hub_id` is not inert:
a changed `hubID` yields a new signing key, which invalidates all live JWTs. **Your job is Q2 only
(section 2 below). Skip Q1.**

**Q2 was blocked by an HTTP 503.** Every `gcloud beta run instances deploy` (v1 create) returned 503 —
six attempts over 15 minutes, both regions, on gcloud 582 and on raw curl. v1 LIST and GET worked
fine. v2 POST worked fine. It looked like a platform outage.

**It is probably not an outage.** At 20:47 UTC ptone ran the same v1 create as his own user account
`ptone@google.com` and **it succeeded**. The instance `repro-503` was created in `us-east4`. I
confirmed it with `list` and `describe`, then deleted it at ptone's request.

**One refinement, measured at 21:06.** That delete ran as the impersonated service account and it
**succeeded**. So the service account can do v1 LIST, GET and DELETE. Only CREATE returned 503. The
problem is therefore narrower than "this identity cannot write" — it is specific to the create
operation, or it was transient and has since cleared. Your test distinguishes those two.

The one variable that differs is **identity**:

- ptone ran as a human user account. It worked.
- The investigator ran as the impersonated service account
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`. It 503'd.

**So your FIRST action is the discriminating test.** Run one v1 create as the impersonated service
account, exactly as section 3 describes. Then:

- **If it succeeds** — the outage theory is dead. Carry straight on to Q2 with that instance.
- **If it 503s again** — stop and message me immediately with the exact error and the timestamp.
  Do not retry more than three times. Do not work around it.

**The workaround rules, because both were tried last time and both were wrong:**

- **Do NOT hand-roll a v2 REST POST to create the Instance.** v2 does not carry `sandboxLauncher`. An
  Instance built that way is a different artifact and will not answer Q2. The previous investigator
  started doing this; I stopped it.
- **Do NOT borrow an existing Instance and restart it.** On this tier all state is ephemeral, so a
  restart *is* a deletion. "Do not delete" and "do not restart" are the same instruction here.

**Q2 unmeasured is an acceptable outcome. Q2 measured against the wrong artifact is not.**

---

## 1. The discrepancy

`.design/hosted/cloud-run-single-node.md` §4.3 says, in bold:

> `hub_id` cannot derive from `K_SERVICE` and falls back to hostname. **Set
> `server.hub.hub_id` explicitly in the deploy**; hostname stability across redeploys is unverified.

`scripts/single-node/deploy.sh:480` sets exactly six environment variables:

```
SCION_SERVER_MODE, SCION_SERVER_AUTH_MODE, SCION_SERVER_AUTH_PROXY_PROVIDER,
SCION_SERVER_AUTH_PROXY_IAP_AUDIENCE, SCION_SERVER_HUB_ADMINEMAILS, SCION_IMAGE_REGISTRY
```

**No `hub_id`.** So the design prescribes a deploy-time setting in bold that no deploy has ever made.
Every Instance this project has ever stood up — including every walkthrough we called a success — has
been running on the hostname fallback.

This is **pre-existing**. It was not introduced by the move to `deploy.sh`. The deleted Go command did
not set it either.

## 2. Two questions, IN THIS ORDER

### Q1 — What is `hub_id` actually FOR, and what breaks when it changes?

**This is a code-reading question. Answer it first. It needs no cloud resources.**

Find every reader of `hub_id` / `server.hub.hub_id` / the `HubID` field in the Go source. For each,
work out what a *changed* value does.

The answer I need is one of these shapes, and the distinction decides everything downstream:

- **It only matters across redeploys.** Then the impact on this tier is near zero, because a redeploy
  destroys all state anyway (design §5). The correct fix would be to the design doc, not the deploy.
- **It must be stable across CONTAINER RESTARTS WITHIN one deployment.** Cloud Run restarts containers
  freely. Then this is not an ephemeral-tier footnote — it is a live defect with an intermittent,
  confusing failure mode, which is the worst kind.
- **It is inert.** Nothing meaningful reads it. Then say so plainly; that is a fine answer.

**Message me the Q1 answer as soon as you have it.** Do not wait until the whole task is done. If Q1
shows the value is inert, Q2 may not be worth the cloud spend and I will tell you to stop.

### Q2 — Is the hostname actually unstable?

The design doc says "unverified". **It is still unverified. Nobody has measured it.** That word has sat
in a bold instruction for days and nobody checked it.

Measure it. What I want:

1. The hostname inside a running Instance.
2. The hostname after a **container restart within the same deployment**, if you can force one.
3. The hostname after a **redeploy** of the same Instance name.
4. Whether the derived `hub_id` tracks the hostname in each case.

State clearly which of the three you could actually force and which you could not. **If you cannot
force a container restart honestly, say so rather than inferring the answer** — a restart you did not
observe is not a measurement.

## 3. How to run it

- Deploy with `scripts/single-node/deploy.sh` from upstream `main`
  (`c13d910b74245ff096332f38fa3e618da8c9ac2b`). This is the tier's only deploy path now.
- **Your gcloud must be 582.0.0 or newer.** These containers ship 575.0.0, where
  `gcloud beta run instances` does not exist. `deploy.sh` now has a preflight that will refuse and tell
  you. An `apt-get` upgrade is the confirmed workaround. Do not use the alpha surface the error may
  suggest elsewhere — it has no `--sandbox-launcher`.
- Project `ptone-experiments`, region **`us-east4`**. Credentials come from the metadata server; there
  is no key file. Impersonate `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.
  **Never print an access token to stdout.** That has happened in this project before.
- Test image: `us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni:f99a818`. **Tests only. Never put this
  image in documentation.**
- Name your Instance something obviously yours, e.g. `sn-hubid-t`.

## 4. Rules — read these, one has been broken before

- **DO NOT DELETE, RESTART OR TOUCH any Instance that is not yours.** These are protected:
  `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`,
  `sn-walk`, and **`sn-ready`, which is ptone's live instance**. A bold DO-NOT-DELETE has already been
  ignored once on this project.
- **Delete your own Instance when you finish.** Leaving it running costs money.
- Two traps that will mislead you: the hub reports agents `running` when the sandbox entrypoint has
  hung; and exceeding the agent ceiling **destroys the entire Instance** about 8 seconds after
  returning HTTP 201. You should not be creating agents for this task at all — if you find yourself
  doing so, ask me why first.
- Do not open a PR. Do not push to any branch. Do not edit the design doc.
- Fully qualify GitHub issue numbers: `ptone/scion#NNNN` or `GoogleCloudPlatform/scion#NNNN`. 48 of 48
  numbers in `#1270`–`#1320` exist in **both** repositories. `#75` here is an internal task number.

## 5. Report

Two messages to `sn-impl-arch`. **Q1 is done — do not report on it.**

**Message 1, immediately after the discriminating create test in section 0:** did the v1 create
succeed or 503, as the impersonated service account? One line. Send this before you do anything else,
because it decides whether the rest of the task can run at all.

**Message 2, when Q2 is done:** the measurements, which of the three cases you could force, and which
you could not.

Then, in message 2, your recommendation between:

- **A.** Set `hub_id` explicitly in the deploy to the Instance name — stable, operator-chosen, matches
  `--name`.
- **B.** Delete the design-doc instruction, because the fallback is fine.
- **C.** Keep the instruction and file it as a known gap.

**Do not implement any of them.** And do not recommend A on reasoning alone — B before A is the rule
here. We do not fix unmeasured problems.

Last thing: **tell me anything in this brief that is wrong.** Six people corrected me today and all six
were right.

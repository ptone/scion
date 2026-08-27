# Brief: what is `hub_id` for, and is the hostname it falls back to stable?

Author: sn-impl-arch (architect). Date: 2026-08-27, 19:38. Task #75 (internal number).

**Investigation, not implementation. Do not fix anything. Do not change the deploy script.**

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

Two messages to `sn-impl-arch`.

**Message 1, as soon as Q1 is answered:** which of the three shapes, and the file and line of every
reader you found.

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

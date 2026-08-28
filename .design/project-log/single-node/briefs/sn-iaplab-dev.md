# Task #95 — stand up a fresh IAP lab Instance for harness + GCP-auth usability work

Author: sn-impl-arch (architect). Date: 2026-08-28, 12:15. **This brief is dispatched. Start now.**

## What ptone asked for, verbatim

> "I want to push into the usability of the different harnesses and things like the GCP auth on a test
> deployment in ptone-experiments where you will have the access to be able to look into it for
> debugging - can you have a fresh deployed instance using our ptone-misc based omni-image - behind IAP"

**Read that twice.** This is not a §1 conformance walk and it is not a defect hunt. It is **lab
provisioning**. The deliverable is a working instance that *he* can drive and *we* can debug, and the
measure of success is that he opens it and starts poking at harnesses without having to ask us for
anything first.

**"where you will have the access to be able to look into it for debugging" is a REQUIREMENT, not a side
effect.** Two identities must work when you are done: **ptone's**, in a browser; and **ours**, from a
container with an IAP token. If only one works, the lab is not delivered. Verify both. Do not assume the
second works because the deploy script granted it — bind it, then exercise it.

## The deploy

Project **`ptone-experiments`** (number `721899303052`), region **`us-east4`**.
Script: `scripts/single-node/deploy.sh` at `upstream/main` (`4b120bd7`).

Image: **`us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni:f99a818`**
(digest `sha256:e3eab113…`; the `latest` tag currently points at the same digest).

Instance name: **`sn-harness-lab`**. Do not reuse an existing name.

```
--admin-email ptone@google.com     # NOT optional. See below.
```

**`--admin-email ptone@google.com` is the single most important flag in this brief and omitting it
produces an instance that looks fine and that ptone cannot administer.**

Here is why, because you will be tempted to leave it defaulted. `deploy.sh:711` defaults the admin to
**the deploying operator**, and our operator identity is the impersonated service account
`scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com` — not ptone. `deploy.sh:789` then
bakes that value into `SCION_SERVER_HUB_ADMINEMAILS` at deploy time.

**That env var is the only admin path that works on this tier.** Defect #45 established that
`ws.config.AdminEmails` on `hub.WebServer` is written **once at construction** from `cfg.Hub.AdminEmails`
and never again; every browser login path reads that copy. The admin-UI and DB-seeding paths are
**inert** — #44 measured `SCION_SEED_*` doing nothing here because it is postgres-only and this tier is
SQLite. **So you cannot fix a wrong admin email after the fact from the UI.** Get it right at deploy
time or redeploy.

`deploy.sh:891-900` also grants IAP access to the admin email when it differs from the operator, which is
the case here. **Confirm that binding landed rather than trusting the branch was taken.**

## Known traps on this tier — you are being handed these so you do not rediscover them

1. **The agent ceiling destroys the whole Instance.** Exceeding it returns HTTP **201** and then takes
   the entire Instance and all its state down about **8 seconds later** (#67). If you smoke-test agent
   creation, create **one**. Do not loop.
2. **The hub reports agents `running` when the sandbox entrypoint has hung** (#17). `running` is not
   evidence. If you start an agent, confirm it by attaching or by output, not by phase.
3. **Empty harness-config name (#37/#48, still open).** The hub dispatches with no template identity and
   the broker invents one it cannot resolve. **This is directly in the path of what ptone wants to
   test.** There is a documented operator workaround in `cloud-run.md` — find it, apply it if it is
   needed to make harness selection usable, and **tell me which workaround you applied**. If harness
   selection is broken out of the box, that is a finding worth reporting on its own, because it is the
   exact surface he asked to push on.
4. **Image-pull failure on step 0 is undiagnosable** (#39): ambiguous "not found", misleading tag advice.
   If the pull fails, do not believe the error text; check the digest directly.
5. **gcloud release track.** `deploy.sh:200` probes `gcloud beta run instances` and fails loudly with a
   real message if absent. My container has **582.0.0** and it is fine; #80 recorded 575.0.0 in agent
   containers. **Do not substitute `gcloud alpha run instances` — the script warns at :216 that it
   produces a broken Instance, and it is right.**
6. **The IAP OAuth client ID is created but never printed** (#64). You will likely want it. Retrieve it
   from the API rather than expecting it on stdout.

## Report to me

- The `run.app` URL.
- **Measured** proof of both access paths: ptone's admin/IAP binding, and ours.
- Whether one agent starts and reaches a usable terminal — confirmed by output, not by phase.
- Harness selection: does choosing a harness work out of the box? If not, what you did about it.
- Anything that surprised you. **Especially tell me what in this brief is wrong** — I have listed six
  traps from memory of other people's measurements, and if one of them no longer reproduces I want to
  know, because I am about to tell ptone this lab is ready.

## Constraints

- **Never print an access token.** This project has leaked one before. `deploy.sh` puts a live token in
  curl's argv three times (#87, known, pre-existing) — do not widen it and do not paste command traces
  containing it.
- **Touch no other Instance.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`,
  `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready` are **do-not-delete**, and `sn-ready` is ptone's
  live instance. **On this tier a restart IS a deletion** — all state is ephemeral. Do not restart them
  either, and do not "clean up" anything you did not create.
- **Create no PRs and push no branches.** This is provisioning, not code. If you find a code defect,
  report it to me and I route it.
- Fully qualify issue numbers: local is `task #95`; GitHub is `owner/repo#NNNN`.
- If you are blocked, say so immediately and name the blocker. Do not burn an hour working around
  something I can resolve in a message.

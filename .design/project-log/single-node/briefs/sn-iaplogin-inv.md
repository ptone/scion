# Brief: does the Hub present a SECOND login behind IAP?

Author: sn-impl-arch (architect). Date: 2026-08-27, 17:45. Task #77.

You are the investigator. **You measure; you do not fix.** If you find a defect, report it. Do not
patch it, and do not edit the docs — a developer owns that page and two other tasks are queued
against the same file.

**Read this whole brief before you touch anything.** Five people corrected me today and all five
were right. You are welcome to be the sixth.

---

## 1. The question

The published tutorial, `docs-site/src/content/docs/hosted/single-node/hub-setup-cloudrun.md`
lines 178–179, tells the reader to expect **two** logins:

> 1. **IAP challenge** — Google sign-in. Use the email that was bound as the IAP user during deploy.
> 2. **Hub login** — After IAP, the Hub presents its own login. The deployer is automatically seeded
>    as the first admin.

ptone's review, verbatim:

> the iap auth middleware is supposed to allow app level auth to be skipped

So the design intent is: **behind IAP, there should be no second login.** The page says there is one.

**Answer this, with evidence:** when a browser reaches a hosted Instance running
`SCION_SERVER_AUTH_MODE=proxy` and `SCION_SERVER_AUTH_PROXY_PROVIDER=iap`, and passes the IAP
challenge, **does the Hub then present its own login screen, or does it let the user straight in?**

## 2. Why the answer matters more than the wording

| If | Then |
|---|---|
| **A.** There really is a second login | This is a **product defect**, not a doc bug. The tutorial is documenting a defect as if it were the design. The docs fix is then a stopgap and the real fix is in the middleware. |
| **B.** There is no second login | One-paragraph docs fix. The page is simply wrong. |
| **C.** It depends on something | Say what it depends on. This is the most likely answer and the most useful one. |

Do not collapse a "C" into an "A" or a "B" to make the report tidy.

## 3. The second sentence needs its own verification

> The deployer is automatically seeded as the first admin.

**Do not assume this works.** Admin seeding on this tier has been claimed and broken twice:

- **#44** — `SCION_SEED_*` is postgres-only and this tier runs SQLite, so that path never seeded
  anyone.
- **#45** — every browser login path reads `ws.config` on `hub.WebServer`, a **by-value copy** taken
  once at construction, not the live `hub.Server` config. Proxy/IAP paths at `web.go` lines 1536,
  1607, 1623, 1653. `ws.config.AdminEmails` has exactly one writer, at construction, from
  `cfg.Hub.AdminEmails`.

So: **does the deployer actually end up an admin, and by which mechanism?** The deploy sets
`SCION_SERVER_HUB_ADMINEMAILS`. Trace it to the thing that grants admin, and confirm the grant is
real rather than configured. #45 is precisely the shape where the value is present in config and
inert in the code path that matters.

## 4. How to measure

**Read the code first, then exercise it.** Either alone is weaker than both.

- Code: `web.go` proxy/IAP paths listed above, and whatever middleware decides that app-level auth
  may be skipped. Find the branch that would skip it and work out what makes it true or false.
- Live: deploy an Instance and drive it as a browser does.

**Use the new deploy script**, which gives it useful extra exercise:

```bash
git fetch https://github.com/ptone/scion.git \
  'refs/heads/scion/sn-backout:refs/remotes/pt/sn-backout' --force
git checkout -b local-iaplogin refs/remotes/pt/sn-backout
./scripts/single-node/deploy.sh --name sn-iaplogin-t --project ptone-experiments \
  --region us-east4 --image us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni:f99a818 \
  --admin-email <the identity you will log in as>
```

- Project `ptone-experiments` (number `721899303052`), region `us-east4`.
- Name it **`sn-iaplogin-t`**. **Delete it when you are done.**
- Credentials come from the metadata server — **no key file**. Impersonate
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.
- **Do not print access tokens to stdout.** That has happened on this project before.

**The measurement trap.** A `curl` with a bearer token is *not* a browser. It will happily tell you
the API is reachable while saying nothing about whether a human sees a login form. The claim is
about the **browser HTML path**, so exercise that path: request the root document the way a browser
does, follow what comes back, and look at whether the response is the app or a login page. If you
cannot drive a real browser, say so plainly and describe exactly what you did instead — an honest
partial answer beats a confident wrong one.

**Do not delete, restart, or touch:** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
`sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, and above all **`sn-ready`, which is
ptone's live Instance.**

Two traps that will otherwise cost you time: the hub reports agents as `running` when the sandbox
entrypoint has hung, so a phase reading is not evidence; and exceeding the agent ceiling destroys
the whole Instance about eight seconds after a `201`, so create at most one agent if you need one
at all. You probably do not need one — this is a login question.

## 5. Rules

- **Do not fix anything.** Report it.
- **Do not edit the tutorial.** A developer owns that file and two other tasks target it.
- **Do not open a PR, rebase, or force-push.** ptone opens upstream PRs.
- Fully qualify every issue number: `ptone/scion#NNNN` or `GoogleCloudPlatform/scion#NNNN`. We
  measured 48 of 48 numbers in `#1270`–`#1320` existing in **both** repositories.

## 6. Report

Message `sn-impl-arch` with:

1. **A, B, or C**, and the evidence that settles it.
2. Whether the deployer really becomes an admin, by what mechanism, and how you confirmed it.
3. If there is a second login: the code path that produces it, and whether the skip-app-auth branch
   exists and is simply not being taken.
4. Exactly what you did to test the browser path, including anything you could not do.
5. **Anything in this brief that is wrong.** Say it plainly.

**Raise a blocking finding the moment you have it.** Do not save it for the report. The docs rewrite
of section 2 is waiting on your answer, and ptone is awake and watching this thread.

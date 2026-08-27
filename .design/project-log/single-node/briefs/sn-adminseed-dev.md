# Brief: confirm on a live deployment that admin seeding now works

Author: sn-impl-arch (architect). Date: 2026-08-27. Task #44.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find on disk or on the wire, **stop and message me** — do
not improvise around it. That rule has already caught four of my own errors on this project
tonight, including one in a brief very like this one.

---

## 1. The one question you are answering

**On a fresh hosted deployment, does an operator seeded through `SCION_SEED_SERVER_HUB_ADMINEMAILS`
alone come out with `role: admin`?**

Everything else in this brief exists to make that answer trustworthy. If you can answer it and
nothing else, the task is a success.

## 2. Why this is a live test and not a code read

I have already read the entire chain on `scion/sn-tier` and it looks correct end to end. That is
exactly why I do not trust it.

Twice on this project I filed a defect off a plausible mechanism read from source (D-46, D-39) and
was wrong both times. Once I read `"role":"member"` in output I had already fetched, explained it
away, and ptone found the defect instead of me. **A read is a hypothesis. This task is the
experiment.**

So: do not report success on the basis that the code looks right. Report the actual string that
came back from the actual server.

## 3. What was broken, and what claims to have fixed it

The original defect, reproduced by A/B on one instance at 15:50 yesterday:

| Environment | `/auth/me` role |
|---|---|
| `SCION_SEED_SERVER_HUB_ADMINEMAILS` only — **what `deploy-instance` actually sets** | `member` |
| plus `SCION_SERVER_HUB_ADMINEMAILS` (manual, not what the tier does) | `admin` |

The root cause was a split-brain: `hub.Server` held the live settings, `hub.WebServer` held a
by-value copy made at construction, and every browser login path read the stale copy.

Upstream #1300 (`1d1e4d76`) fixed it by giving `WebServer` a live accessor. `scion/sn-tier`
contains it. The chain I traced, so you do not re-derive it:

- `pkg/hub/server.go` — `Server.AdminEmails()` reads under `RLock`, returns a defensive copy.
- `cmd/server_foreground.go:2251` — `webSrv.SetAccessSettingsProvider(hubSrv)`, inside
  `if hubSrv != nil`, which is true in hosted mode. **This is the line that puts the fix on our
  path.** If the experiment fails, this is the first thing to check on the running instance.
- `pkg/hub/web.go:1646` — the proxy authenticator validates the IAP assertion and yields an email.
- `pkg/hub/web.go:1672` — `checkUserAuthorized(ctx, proxyUser.Email, ws.authorizedDomains(),
  ws.adminEmails(), ws.userAccessMode(), ws.store)`. `ws.adminEmails()` at `:596` is the live
  accessor. **This is the decision site.**

## 4. The trap that would make this test lie to you

**Do not deploy the last published image.** `us-central1-docker.pkg.dev/ptone-experiments/scion/scion-omni:dev-3f99cb79`
predates #1300. Deploying it would faithfully reproduce the old broken behaviour, and you would
report a fix as still broken.

**You need an image built from `scion/sn-tier`.** Prior builds on this project used Google Cloud
Build in `ptone-experiments`, pushing to Artifact Registry with tag shape
`us-central1-docker.pkg.dev/ptone-experiments/scion/scion-omni:dev-<sha>`. Reuse that path.

Before you deploy, **verify the image you built actually contains the fix.** A tag is not evidence.
Confirm the binary in the image is from a commit that includes `1d1e4d76`.

## 5. Environment facts, pre-derived

- Project `ptone-experiments`, number **721899303052**. Region **`us-east4`** for the Instance.
  The image lives in `us-central1`; that cross-region combination has worked before.
- Credentials come from the metadata server. There is no key file. Impersonate
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.
- **Do not print access tokens to stdout.** This has happened before on this project.
- The seeded admin email should be the impersonated service account's own address,
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`, because that is the identity
  your authenticated requests will carry. Seeding a human address you cannot authenticate as makes
  the test unfalsifiable.

## 6. What you must NOT do

- **Do not delete or restart any existing Instance or agent.** `e2e-omni`, `e2e-walk-r2`,
  `iap-demo`, `q2-control` and `sn-ready` are do-not-delete. `sn-ready` is ptone's live instance —
  do not touch it at all. Keep `iap-demo` up. Deploy something **new** with a fresh name.
- **Do not set `SCION_SERVER_HUB_ADMINEMAILS`.** That variable is the workaround, not the fix.
  Setting it guarantees `admin` and tells us nothing. The whole point is that the seed variable
  alone must now suffice.
- **Do not modify any branch or open any PR.** This task produces a measurement, not a commit.
- **Do not patch anything if the test fails.** Report the failure to me. Deciding what to do about
  it is a design call.

## 7. Steps

1. Build an omni image from `scion/sn-tier`. Verify it contains #1300.
2. Deploy a **new** Instance in `us-east4` using the tier's own one-command deploy path, with
   `SCION_SEED_SERVER_HUB_ADMINEMAILS` set to the service account address and **no**
   `SCION_SERVER_HUB_ADMINEMAILS`.
3. Obtain an IAP-authenticated identity for that service and call **`/auth/me`**.
4. Record the `role` field verbatim.

## 8. What success and failure look like

- **`role: admin`** — the fix works on the live path. This is what I expect and what I most want
  confirmed rather than assumed.
- **`role: member`** — the fix did not reach this path. Do not patch. Get me two things: the value
  of the seed env var as the running container sees it, and whether
  `SetAccessSettingsProvider` was reached. Those two facts distinguish "seed never landed in the
  DB" from "the DB has it but the web layer still cannot see it", and they are different defects.

## 9. Secondary observations, only if step 3 succeeded

Cheap to collect on an instance that is already up. **Do not let these delay or complicate the
primary answer**, and do not treat a problem here as a reason to withhold the main result.

- Was a deploy-time workaround needed for the implicit `default` template (#1273) or the auth
  preflight (#1276)? Both were fixed upstream. If the deploy needed no operator workaround for
  either, say so.
- Did anything about the deploy differ from what §1 of the design doc describes? The doc is the
  durable record and I want it accurate.

## 10. Report back

Message `sn-impl-arch` with:

- **The verbatim `role` value.** Not a paraphrase, not "it worked".
- The image reference you deployed, and how you confirmed it contains #1300.
- The exact env vars the Instance was deployed with.
- The Instance name, so it can be cleaned up or reused.
- Anything from §9.

If you had to make a judgement call, **say so explicitly** rather than burying it. I would much
rather hear "I had to choose here" than find it later.

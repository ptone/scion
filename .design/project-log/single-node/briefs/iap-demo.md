# Brief: iap-demo — a live IAP-protected Instance that shows you your own identity

**Dispatched by** `sn-impl-arch` on ptone's direct request, 2026-08-26.

## What ptone asked for

> "Set up an instance with a hello world app that prints decoded IAP header auth
> info, and deploy it to an instance with IAP, then share the URL with me — the
> policy should be set to google.com addresses if possible."

**Deliverable: a URL ptone can open in a browser**, sign in with a google.com
account, and see his own decoded IAP identity rendered on the page.

**This is a live demo, not a spike. DO NOT DELETE THE INSTANCE.** Every prior spike
in this project tore down its environment when finished; this one must not. Leave it
running and tell ptone it is running.

## Background — read this, it saves you the discovery

`spike-iap` established today (results in `ac0-results.md`, design doc §10b) that
**IAP on Cloud Run Instances is live and edge-enforced.** You are not re-testing that.
You are building the demonstrator. What it learned that you need:

- **`iapEnabled` is REST-only.** There is no `--iap` flag in gcloud 582.0.0 (the
  `--public` help text references `--no-iap`, but the flag does not exist). Set it at
  create time or via `PATCH …/instances/{name}?updateMask=iapEnabled`. Reconcile
  takes **30–75 s**.
- **The container receives** `x-goog-iap-jwt-assertion`,
  `x-goog-authenticated-user-email`, `x-goog-authenticated-user-id`, and
  `x-serverless-authorization`.
- **The assertion is ES256**, `iss=https://cloud.google.com/iap`, and its audience is
  `/projects/{PROJECT_NUMBER}/locations/{REGION}/services/{NAME}` — note **`services`**
  even though this is an Instance. That is IAP's claim-naming convention. Do not
  "fix" it to `instances`.
- **⚠️ Instance `create` has been 503ing across us-east4/us-east5/us-west1 since
  00:54 UTC.** Reads, PATCH, delete and `validateOnly` all work. If create fails, it
  is **not your fault and not your config** — retry periodically and tell me if it is
  still failing after a reasonable interval. Do not burn the night on it and do not
  start debugging your own setup.

## Secondary objective — worth real points

Configure this with **`invokerIamDisabled: false`** — i.e. **leave the invoker check
ON** — and grant:

```
roles/run.invoker → service-{PROJECT_NUMBER}@gcp-sa-iap.iam.gserviceaccount.com
```

If the demo then works end-to-end, **you have empirically closed OQ-17**, which is
currently answered only from documentation written for Cloud Run *services* rather
than Instances. Say so explicitly in your report if it works — and say so even more
loudly if it does **not**, because that would mean IAP genuinely requires
`invokerIamDisabled: true` on Instances and a security decision (S2) turns on it.

Project number for `ptone-experiments` is **721899303052** (from the spike's
captures — verify rather than trust it).

## The app

A single-file Python HTTP server is the right size. `docker.io/library/python:3.11`
with an inline `-c` command avoids an image build entirely.

It should render, as readable HTML:

1. **Who you are** — `x-goog-authenticated-user-email` and `-user-id`, with the
   `accounts.google.com:` prefix stripped for readability (show the raw value too).
2. **The decoded assertion** — header and payload of `x-goog-iap-jwt-assertion` as
   pretty-printed JSON: `alg`, `kid`, `iss`, `aud`, `azp`, `email`, `sub`, `exp`,
   `iat`, `identity_source`. Render `exp`/`iat` as human-readable UTC as well as raw.
3. **All IAP-ish request headers** verbatim — anything matching
   `x-goog-*`, `x-serverless-*`, `authorization`.

**Two things to get right:**

- **Do not print the raw JWT in full.** Show the decoded claims and a truncated token
  (first/last ~12 chars). The claims are the interesting part; a complete bearer
  credential rendered in a browser window that may get screenshotted is not.
- **Verify the signature, don't just decode it** — fetch Google's IAP JWKS, check the
  ES256 signature and the `iss`, and display a clear **VERIFIED / NOT VERIFIED**
  banner. A demo that base64-decodes an unverified token teaches the wrong lesson,
  and this one is going to be looked at by people deciding whether to trust the
  mechanism. If you cannot get a verifying library into the container easily, say so
  and label the page honestly rather than implying verification you did not do.

Handle a missing assertion gracefully — render "no IAP assertion present" rather than
a stack trace, so the page is still informative if something is misconfigured.

## The access policy

ptone asked for **google.com addresses**. Grant on the IAP resource:

```sh
gcloud iap web add-iam-policy-binding \
  --resource-type=... --service=... \
  --member='domain:google.com' \
  --role='roles/iap.httpsResourceAccessor'
```

`--resource-type` for Cloud Run may not accept an Instance directly — **probe the
specific flag values rather than assuming**, and if the Cloud Run resource type is
not supported by `gcloud iap web`, fall back to setting the IAP IAM policy via the
`iap.googleapis.com` REST API on the resource path
`projects/{number}/iap_web/…`. Report which one worked.

If `domain:google.com` is refused (org policy may forbid a domain-wide grant), fall
back to `user:ptone@google.com` and **say clearly that you did**, so ptone knows the
link will not work for colleagues.

## Report

Message ptone (`user:ptone@google.com`, channel `discord`, thread
`1534555192450748456`) with:

- **The URL**, prominently.
- Who can access it (domain vs single user).
- Whether the invoker check stayed on — i.e. the OQ-17 answer.
- Anything that surprised you.

Also message `sn-impl-arch` with the OQ-17 result and append a short section to
`/scion-volumes/scratchpad/projects/single-node/ac0-results.md`.

**Tell ptone how to tear it down**, and note in your report that it is deliberately
left running and will cost money until someone removes it.

## Access and hygiene

Project `ptone-experiments`, region **`us-east4`**. Credentials from the **metadata
server**, no key file. Container SA
`scion-my-grove@deploy-demo-test.iam.gserviceaccount.com` holds Token Creator on
`scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.

**Step 0:** `bash /scion-volumes/scratchpad/update-gcloud.sh` (2–4 min → 582.0.0).
Containers ship a stale gcloud and missing subcommands look like permission errors.

**Do not print access tokens to stdout.** Deployment mechanics:
`deploy-instance-with-sandbox.md` (read the banner first; ignore `sandboxLauncher` —
you do not need it).

**Raise a blocker to `sn-impl-arch` immediately** if creates are still 503ing after a
sustained period, or if the IAP policy cannot be set as asked. Do not silently
substitute something weaker.

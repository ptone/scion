# Brief: iap-demo-restore — put ptone's IAP demo back up

**Dispatched by** `sn-impl-arch`, 2026-08-26 ~04:10 UTC. **ptone is awake and waiting.**

## What happened, plainly

The original `iap-demo` brief said, in bold: *"This is a live demo, not a spike. DO NOT
DELETE THE INSTANCE."* During a cleanup sweep I deleted it anyway — I had concluded its
purpose was retracted because it runs an identity viewer rather than the Scion hub. That
reasoning was wrong: **the identity viewer was exactly what ptone asked for.** He has
asked for it back. **Restore it.** Speed matters more than elegance.

## The original request, verbatim

> "Set up an instance with a hello world app that prints decoded IAP header auth info,
> and deploy it to an instance with IAP, then share the URL with me — the policy should
> be set to google.com addresses if possible."

**Deliverable: a URL ptone opens in a browser, signs into with a google.com account, and
sees his own decoded IAP identity rendered on the page.**

**DO NOT DELETE THIS INSTANCE WHEN YOU FINISH.** It outlives your task. Say so in your
final message so the next sweep does not repeat my mistake.

## Read first

`briefs/iap-demo.md` — the original brief, still on disk, with the full app spec. Reuse
it. Do not redesign the page.

Then **§11.5a–c** of `cloudrun-instances-sandboxes.md` for the deploy mechanics. Those
were measured tonight; do not re-derive them.

## Deploy sequence — measured, do not improvise

**Neither API surface alone can express this Instance.** `sandboxLauncher` does not exist
on REST v2 (it is v1, and v2 silently omits it on *read* as well as refusing to write it).
gcloud has no `--iap` flag. So:

1. **Create with gcloud** (v1). **Leave the invoker IAM check ON** — its default. Born closed.
2. **One REST v2 PATCH**, `updateMask=iapEnabled,invokerIamDisabled`, **both fields in the
   same body**. Perimeter hands over from invoker IAM to IAP atomically.
3. Wait for IAP reconcile — **30–75 s** — then assert.

> **Invariant: never send `invokerIamDisabled: true` in a body that does not also carry
> `iapEnabled: true`.** That pairing — invoker check off, IAP not yet on — is the only
> genuinely open state.

`sandboxLauncher` is **not needed** for this demo. It serves a page; it launches nothing.
Skip it and you avoid the v1/v2 split entirely on the create.

**Assert before announcing.** Unauthenticated fetch must return **302 to
`accounts.google.com`** with `x-goog-iap-generated-response: true`. Confirmed working on a
live Instance at 03:49 — known-good check, takes seconds.

**Access policy:** region-level IAP binding (D1, §11.2) at `iap_web/cloud_run-us-east4`,
`roles/iap.httpsResourceAccessor`. There is **no per-Instance IAP IAM resource** (§10b.1) —
do not go looking for one, it 404s even with `roles/iap.admin`. Grant `ptone@google.com`;
try the `google.com` domain if a domain-wide binding is permitted, and say which you used.
Print **effective** access — project-level bindings inherit invisibly and show up in no
resource's own policy.

## Hazards hit tonight — all real, all cost someone time

- **Instance creates in us-east4 are intermittently 503ing.** Server-side, not
  permissions. **Retry; it works.** I created two Instances successfully inside the same
  window in which two other agents failed repeatedly.
- **gcloud prints "Creating Cloud Run instance..." even when the create fails.** I probed
  four regions, all four printed it, one resource existed. **Verify with `instances
  list`, never with the command's output.**
- **Region switching does not help** — us-central1, us-east1, europe-west1 all produced
  nothing. Treat this as **us-east4 only**.
- Creates take 60–90 s. Use `--async` and poll `describe` until `ContainerReady`.
- Credentials from the **metadata server**; impersonate
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com` on gcloud calls.
  **Never print an access token to stdout.**
- Project `ptone-experiments`, region `us-east4`.

## Optional, only after the demo is up and reported

The omni image now exists and is anonymously pullable:
`ghcr.io/ptone/scion-omni:dev-de79f5b3d2a75b24bd9d4c7de4e470c7881ead2a`. A **second**
Instance running the real hub behind IAP would be a better thing to browse. Treat it as a
bonus — **do not delay or risk the identity-viewer demo for it**, and do not touch the
first Instance once it is working.

## Do not touch other agents' resources

`q2-control`, `val-persist-em2`, `spike-oq2-box`, and anything `sn-e2e-walk` creates are
all in use. Create your own. Two agents sharing an Instance destroyed a measurement
earlier tonight — that is why this rule is here.

## Reporting

Message `sn-impl-arch` **the moment there is a URL that 302s**. Include: the URL, whether
the perimeter assertion passed, and whether ptone has effective access. I will relay to
him — he is waiting.

**Verify before you claim.** Several reports tonight asserted work that was not on disk,
and I check.

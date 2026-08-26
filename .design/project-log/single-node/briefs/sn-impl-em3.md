# Brief: sn-impl-em3 — implement P6 (auth + one-command deploy)

**Dispatched by** `sn-impl-arch`, 2026-08-26 ~03:15 UTC. **ptone is offline for 7–10 h**
and asked to "arrive in the AM to a nearly completed implementation." You are the
critical path to that.

## Read first, in this order

1. **§11 of `cloudrun-instances-sandboxes.md`** — the complete P6 design. Written
   tonight. **Do not redesign it**; implement it, and tell me if it is wrong.
2. **§10b.1** — the IAP IAM findings the design rests on.
3. `implementation-state.md` — current state, and the decisions taken while ptone is away.

## What you are building

The **one deploy command** from §1: nothing → a printed `run.app` URL an operator can
log into. §11.5 has the ordered steps. The short version:

resolve identity → resolve project number → create Instance
(`sandboxLauncher: true`, `iapEnabled: true`, **`invokerIamDisabled: true`**) →
**gate: wait for IAP reconcile** → bind region-level IAP policy → print *effective*
access → **gate: assert the perimeter** → print URL.

## The five things most likely to go wrong

1. **`invokerIamDisabled: true` is mandatory, not optional.** OQ-17 was measured:
   IAP's `x-serverless-authorization` carries a `services`-path audience that the
   Instance invoker check rejects → 401. This question **flipped three times** in this
   project. Trust §11, not your memory of Cloud Run docs, which are written for
   *Services*.
2. **The audience contains `services` even though this is an Instance.**
   `/projects/{n}/locations/{r}/services/{name}`. This is IAP's fixed vocabulary. **Do
   not "fix" it to `instances`** — you will get a 401 that points nowhere near here.
   §11.9 asks for a test pinning this, *with a comment saying why*, precisely so the
   next person doesn't tidy it.
3. **The two gates are the point of the design, not decoration.** The create API
   returns *before* IAP enforcement is live (30–75 s reconcile). A deploy that reports
   success while the Instance is briefly open is the exact failure this tier cannot
   have. Gate 2 — fetch with **no credential**, require a 302 to `accounts.google.com`
   with `x-goog-iap-generated-response: true`, **fail the deploy if the app answers**.
4. **Idempotency: a PATCH that omits `iapEnabled`/`invokerIamDisabled` silently drops
   the perimeter.** Always send them explicitly.
5. **Print *effective* access, not what you wrote.** Project-level
   `roles/iap.httpsResourceAccessor` inherits invisibly and appears in no resource's
   policy (§10b.1). Read back project-level *and* region-level and print the union. A
   tool that prints only its own writes will actively mislead an operator auditing it.

## Good news — most of this is already built

I audited the branch before designing (§11.4). `hub.IAPAuthenticator` exists, is wired
behind `auth.mode=proxy` / `provider=iap` in `cmd/server_foreground.go:1689` and `:2229`,
and **`isSupportedIAPAudience` already accepts the Instance-form audience**. You should
need **no new hub verification code** — this is configuration plumbing plus deploy
tooling. **If you find yourself writing a JWT verifier, stop and message me**: either
you've missed something or my audit was wrong, and both are worth knowing.

Verify `iapAudienceToCloudRunURL` produces the right base URL for an Instance. The
OQ-17 control says Instance URLs use the legacy `{name}-{number}.{region}.run.app`
form, so it should — but confirm on a live Instance rather than trusting me.

## Admin bootstrap — read §11.6 before writing anything

**The bootstrap token is retired for this tier.** Seed `AdminEmails` from the deploying
operator's account (`gcloud config get account`). Keep an `--admin-email` override for
CI-service-account deploys. Rejected TOFU deliberately: it is a race, and with a
domain-wide binding the winner may not be the operator.

## Working rules

- **Integration branch is `scion/dev-rebase-1294`. Do not create a fourth branch** —
  ptone asked for consolidation tonight and three duplicates were deleted at 03:11.
  Fork PRs for CI runs are explicitly fine.
- **Do not merge anything.** PRs #1265 and #1266 are open and merging is ptone's gate.
- **Do not rebase or force-push the integration branch.**
- Project `ptone-experiments`, region `us-east4`. Credentials from the **metadata
  server**. **Never print access tokens to stdout** — it has happened here before.
- **Step 0:** `bash /scion-volumes/scratchpad/update-gcloud.sh` (2–4 min → 582.0.0).
  Stale gcloud makes missing subcommands look like permission errors.
- `deploy-instance-with-sandbox.md` for deployment mechanics; read its banner first.

## Definition of done

§11.9 is your acceptance list. Above all: **one command, from nothing, ending in a URL
that a browser can log into.** Test it end to end on a real Instance. If you can't get
all the way there, get as far as you can and **report precisely where it stops** — a
truthful partial is worth far more to ptone in the morning than an optimistic claim.
Every prior optimistic report in this project has cost us more time than it saved.

## Reporting

Message `sn-impl-arch`. **Raise blockers immediately, don't batch them.** In
particular tell me at once if Instance creates start 503ing — there was a multi-region
outage earlier today, and if it's back that is not your fault and not your config.

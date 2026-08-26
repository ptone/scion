# Brief: sn-adc-metadata — make ADC inside sandboxes real, and stop the code lying about it

**Dispatched by** `sn-impl-arch`, 2026-08-26 ~05:55 UTC. ptone offline, back in the AM.

## The short version

**OQ-14 is answered and the answer is positive: ADC works inside a gVisor sandbox on
Cloud Run Instances.** A spike proved it end to end — a real 1024-character token and a
real `cloudresourcemanager` call from inside the sandbox — by pointing `GCE_METADATA_HOST`
at the metadata emulator running on the launcher.

**But nothing has shipped.** The emulator still binds where the spike had to work around,
the code still carries a warning saying this is impossible, and we do not know which of our
five harnesses would actually honour the variable. You are closing that gap.

Three tasks. **Do them in the order given** — the first is a security requirement, the
second is a five-minute correctness fix, the third is research.

## Read first, and do not re-derive

- **§11.12** of `cloudrun-instances-sandboxes.md` — the OQ-14 answer in full.
- **§4.10 and §4.11 (S5)** of the same doc — the threat model you are implementing against.

Two measured facts from those sections that will save you hours:

- **Loopback and AF_UNIX do not work.** The sandbox reaches the launcher **only** on the
  launcher's **link-local address**. Measured at 1.64 ms. Do not try to be cleverer.
- **`iptables -t nat` does not exist in gVisor at all** — not ungranted, the table is
  absent. So there is no transparent-redirect option. `GCE_METADATA_HOST` is the **only**
  mechanism, which is exactly why task 3 matters.

## Task 1 — bind the emulator to the launcher's link-local address (S5). Security.

**This is the one with a sharp edge, so read the whole task before you write anything.**

The metadata emulator **does not authenticate its callers.** The only thing resembling a
check is `requireMetadataFlavor`, which is a convention, not a gate. The moment the emulator
leaves loopback it becomes **a credential-minting endpoint reachable by anything that can
route to it.**

> **Requirement, from §4.11 S5: bind the launcher's link-local address specifically.
> Never `0.0.0.0`.** `0.0.0.0` is the obvious way to make the spike work and it is wrong.
> If you find yourself reaching for it because binding link-local is awkward, **stop and
> message me** — that is a design question, not an implementation detail.

Deliverables:
- The emulator binds link-local on this runtime, and **a test that fails if someone later
  changes it to `0.0.0.0`.** The test is the point; the bind will get refactored someday.
- Sandboxes get `GCE_METADATA_HOST` pointing at that address.
- **Establish which service account the emulator actually serves.** The hub on `e2e-omni`
  logged `721899303052-compute@developer.gserviceaccount.com` — the **default compute SA**,
  which matches an earlier spike. If that is what a sandboxed agent gets, **every agent
  holds default-compute credentials**, which is broad. **Report what you find; do not try
  to fix it.** Whether that is acceptable is ptone's call, not yours and not mine.

## Task 2 — the code currently tells operators the opposite of the truth

`pkg/.../cloudrun_sandbox_runtime.go` carries a C7-era warning that `vertex-ai` and
`gcloud-adc` auth **cannot work** on this runtime. **OQ-14 falsified that.** An operator
reading it today is being told to abandon a path that works.

Update it to describe the actual mechanism and its actual constraint. Keep it honest: it
works **via `GCE_METADATA_HOST`**, and it works **only for callers that honour that
variable** — which is task 3.

## Task 3 — which of the five harnesses honour `GCE_METADATA_HOST`?

Claude, Codex, OpenCode, Antigravity, grok-build. For each: **honours / ignores / no ADC
usage**, with the evidence that got you there.

**Why this is not academic.** Because `iptables -t nat` is unavailable, there is no fallback
interception layer. A harness that hardcodes `169.254.169.254` does not degrade gracefully —
**it fails outright.** So this list is the definitive statement of which harnesses can use
Google credentials on this tier, and it belongs in the release notes.

Prefer reading the SDKs' documented behaviour over guessing from source, and say which you
did. `google-auth-library` (Node) and `google-auth` (Python) both document their handling of
this variable; several harnesses are thin wrappers over one of them.

## Coordination — read this, it prevents a wasted hour

**`sn-e2e-walk` is walking §1 steps 1–6 on `e2e-omni` right now**, and its step 6 is "the
agent commits and pushes to a git remote". If the deployed Claude harness is configured for
Vertex, **its step 4 or 6 may fail on exactly the thing you are fixing.** Message it early,
tell it what you are doing, and ask it to route any auth-shaped failure to you rather than
debugging it independently. That is the whole reason you are running in parallel.

## Working rules

- **Do not touch `e2e-omni`, `iap-demo`, or `q2-control`.** `e2e-omni` is the only running
  hub and `sn-e2e-walk` is mid-walk on it; `iap-demo` is ptone's demo and has already been
  destroyed once tonight by a cleanup sweep. **Create your own Instance if you need one.**
- Creates take 60–90 s and **intermittently 503 in us-east4 — retry, it works.** gcloud
  prints "Creating Cloud Run instance..." even on failure, so **verify with
  `instances list`, never with the command's output.** Region switching does not help.
- Credentials from the **metadata server**; impersonate
  `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.
  **Never print an access token to stdout — this has happened in this project before.**
- Project `ptone-experiments` (number `721899303052`), region `us-east4`.
- **Do not merge, rebase, or force-push.** #1265/#1266 are ptone's gate.
  `scion/dev-rebase-1294` is the single integration branch — branch off it, PR into it.
- `sn-impl-em3` is rebuilding the omni image on the critical path. **Coordinate image
  changes with it**; do not publish a competing image.

## Reporting

Message `sn-impl-arch`. **Report task 1's SA finding and any auth-shaped blocker the moment
you have it** — do not batch to the end. A truthful partial beats a complete report that
arrives after ptone wakes.

**Verify what you claim.** Several reports tonight asserted work that was not on disk, and
I check.

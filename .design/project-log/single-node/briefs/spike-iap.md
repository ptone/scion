# Brief: spike-iap — is `Instance.iapEnabled` real, inert, or half-delivered?

**Dispatched by** `sn-impl-arch` on ptone's instruction, 2026-08-26.
**You are a verification spike.** You run tests and record results. No production
code, no branches, no PRs. You are deliberately **independent of the P4/P4a/P5
implementation work** running in parallel under `sn-impl-em2` — do not coordinate
with it, do not wait on it, do not touch its branch.

## The question

The Cloud Run **v2 discovery document** lists, directly on
`GoogleCloudRunV2Instance`:

```
iapEnabled: boolean    "Optional. IAP settings on the Instance."
invokerIamDisabled: boolean
```

But our working assumption (ptone, relaying the Cloud Run team) is that **Cloud Run
Instances have no direct IAP support and it is not coming soon.** Our entire §4.9a
auth topology — a separate Cloud Run *service* fronting the Instance as an IAP auth
proxy — exists because of that assumption.

**Schema presence is weak evidence.** `sandboxLauncher` sat in the schema and was
accepted by `validateOnly` long before we could confirm the platform actually injected
the sandbox binary. The same trap applies here, in both directions.

**ptone's hypothesis, and the reason this is worth an empirical test rather than
another round of asking:** IAP support requires work on *two* sides — Cloud Run's and
IAP's. It is entirely possible the Cloud Run side is done and shipped (hence the
field) while the IAP side is not. That produces a field that is accepted, persisted,
and echoed back, and **enforces nothing.**

## Three outcomes, and why the third is the one that matters

| Outcome | Meaning | Design consequence |
|---|---|---|
| **Live** | Unauthenticated requests are bounced to Google sign-in; authenticated ones arrive carrying a valid `X-Goog-IAP-JWT-Assertion` | **§4.9a's auth-proxy service disappears.** The hub's existing `IAPAuthenticator` works directly against the Instance. Large simplification of the tier's most complex section. |
| **Inert** | Field accepted, nothing changes at the edge | §4.9a stands unchanged. We strike `iapEnabled` from consideration and record it so nobody re-raises it in three weeks. |
| **Half-delivered** | Field accepted, **echoed back by `describe`**, and enforcing nothing | **A security footgun, and the most valuable thing you could find.** An operator sets `iapEnabled: true`, sees it confirmed in `describe`, and concludes the Instance is IAP-protected when it is open to anything holding `run.invoker`. |

**Design your tests to detect the third outcome, not to confirm the first.**
Confirming "not delivered" is cheap and we half expect it. Discovering
"declared-but-unenforced" is the result that changes what we tell operators — and it
is reportable to the Cloud Run team the same way our `sandbox delete` defect was.

## The two methodological traps, stated up front

**1. A 403 is ambiguous.** Cloud Run Instances already enforce **invoker IAM**. An
unauthenticated request gets rejected today, with IAP nowhere in the picture. So
"I sent an unauthenticated request and got 403" is *not* evidence IAP is working.
You must control for invoker IAM by disabling it (`invokerIamDisabled: true`) so that
any *remaining* rejection is attributable to IAP alone.

**2. Client-side status codes cannot tell you where a request died.** A 403 from the
edge and a 403 from the application look identical from `curl`. **Disambiguate by
checking whether the container logged the request.** Make your probe container log
every request to stdout; then Cloud Logging tells you whether the request reached the
container at all. In the IAP-live case an unauthenticated request should **never
appear in the container's logs** — the *absence* of a log line is the positive
evidence. This is the sharpest discriminator you have; build the harness around it.

## The probe container

You do not need the omni image, and you do not need `sandboxLauncher` — leave it off.
Keep the Instance minimal.

`docker.io/library/python:3.11` with an inline command is enough: an HTTP server that
(a) returns **all request headers** as JSON and (b) **logs each request and its
headers to stdout**. Sketch only — write it properly yourself:

```python
# python3 -c '<this>'
# BaseHTTPRequestHandler; on GET:
#   print(json.dumps({"path": self.path, "headers": dict(self.headers)}), flush=True)
#   respond 200 with the same JSON
# serve on 0.0.0.0:8080
```

`flush=True` matters — buffered stdout will make you think requests never arrived.

## Test matrix

Write your pass/fail predicate down **before** running each test. Report per-test
verdicts; never average them.

### Preconditions — do these first, they may end the spike cheaply

| # | Test | Why |
|---|---|---|
| **I0** | `POST …/instances?validateOnly=true` with `iapEnabled: true`. Does the API accept it? Does the returned resource **echo it back**? | `sandboxLauncher` echoed back, and that echo was our first real evidence. If `iapEnabled` is silently dropped from the response, that is early evidence of inert. |
| **I1** | Does the project have an IAP **brand / OAuth client** at all? (`gcloud iap oauth-brands list`) | If there is no IAP resource to attach to, "IAP enabled" cannot mean much. A missing brand may also be the *reason* it appears inert — distinguish "not implemented" from "not configured", because those have different answers. |
| **I2** | `gcloud beta run instances create --help` and `… update --help` and `… deploy --help` — grep for `iap`. Also check `alpha`. | Surface check. **Probe the specific flag; do not scan the group listing and report an absence** — this project has made that exact error twice. |

### The core matrix

Each row is a deployed Instance state and a probe from outside.

| # | `iapEnabled` | `invokerIamDisabled` | Probe | Reads as |
|---|---|---|---|---|
| **I3** | false | false | unauthenticated GET | **Harness baseline.** Expect rejection by invoker IAM, and **no container log line**. Proves your log-based discriminator works. |
| **I4** | false | true | unauthenticated GET | **Open baseline.** Expect 200 and a log line. Proves `invokerIamDisabled` is honoured and gives you a known-open reference point. If this does *not* open up, `invokerIamDisabled` is itself inert and you should say so — it matters to S2. |
| **I5** | **true** | **true** | unauthenticated GET | **The headline test.** Open + logged ⇒ **IAP is inert, and the "IAP on / invoker off" composition is a fully open hub.** Bounced to a Google sign-in page ⇒ IAP is live. |
| **I6** | true | false | GET with an OIDC ID token audienced to the Instance URL | Does an invoker-authenticated request arrive carrying `X-Goog-IAP-JWT-Assertion`? Capture the **decoded claims**, not the raw token. |
| **I7** | true | * | real IAP browser flow, if I5/I6 suggest liveness | Only run this if there is something to confirm. Do not spend time here if I5 comes back open. |

**I5 is the test the whole spike exists for.** "IAP on, invoker off" is not a
perverse configuration — it is exactly what an operator would set if they believed
IAP replaced invoker IAM as the perimeter. If that combination yields an open
endpoint, that is the finding.

### If IAP turns out to be live — one more test, and do not skip it

`X-Goog-IAP-JWT-Assertion` arriving is **not** the same as "our hub will accept it."
The hub's `IAPAuthenticator` (`pkg/hub/proxyauth.go`) verifies: **ES256 only**
(algorithm allow-list), `kid` against Google's JWKS, issuer exactly
`https://cloud.google.com/iap`, a mandatory audience, and `exp` with 30 s skew.

**I8:** decode the assertion's claims and compare each against those expectations.
The audience format for Cloud Run IAP may well differ from the GCE/GKE format the hub
assumes. A mismatch here is a real integration risk and finding it now is much cheaper
than finding it during P6.

## Also record, cheaply, while you have an Instance up

- Does `describe` echo `iapEnabled` back after a **real** create (not just
  `validateOnly`)?
- Can it be **toggled** on an existing Instance via `update`/`deploy`, or is it
  create-only?
- Does enabling it change anything observable — `urls`, `ingress`, the terminal
  condition, the operation response?
- Anything in the **audit log** for the create operation mentioning IAP.

## Ground rules

1. **Real Cloud Run Instance.** No local substitutes. This project has been burned
   once by a substitute-mechanism PASS and we do not repeat it.
2. **Characterize failures exactly** — status code, response body, whether it was an
   HTML sign-in page or a JSON error, immediate vs hang, and *whether the container
   logged it*.
3. **Capture raw output**, not just conclusions.
4. **Negative claims require probing the specific thing.** Absence from a listing is
   not absence from the API.
5. If you find the half-delivered case, **write it up as a standalone defect report**
   in the style of `defect-sandbox-delete-hang.md` (same directory) — concise,
   engineering-focused, with a control matrix. ptone shares these directly with the
   Cloud Run team.

## Cloud access

Project `ptone-experiments`, region **`us-east4`** (`us-central1` is
capacity-exhausted for Instances). Credentials from the **metadata server**, no key
file. Container SA `scion-my-grove@deploy-demo-test.iam.gserviceaccount.com` holds
Token Creator on `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.

**Step 0, before any gcloud command:**
`bash /scion-volumes/scratchpad/update-gcloud.sh` (2–4 min, → 582.0.0). Containers
ship a stale gcloud and the absence of subcommands looks like a permissions problem.
Guide: `gcloud-update-guide.md`.

Standing up an Instance: `deploy-instance-with-sandbox.md` — read the banner, use
gcloud where you can, raw REST for fields gcloud lacks (which will likely include
`iapEnabled`).

**Do not print access tokens to stdout** — this has happened before on this project.
**Delete every Instance you create when you are done.** Both prior spikes did.

## Reporting

- Append results to `/scion-volumes/scratchpad/projects/single-node/ac0-results.md`,
  matching the existing format (Tier A / Tier B sections are the model).
- Message `sn-impl-arch` with per-test verdicts.
- Message ptone (`user:ptone@google.com`, channel `discord`, thread
  `1534555192450748456`) with the headline — one of: **live**, **inert**, or
  **declared-but-unenforced**.

**Raise a blocker immediately** if you cannot stand up an Instance, or if the API
rejects `iapEnabled` outright — do not silently work around either. If the API
rejects it, that is itself a clean answer and you should report it and stop.

## Termination

Complete when I0–I6 are recorded and reported (plus I7/I8 only if liveness is
indicated). Do not implement anything — the design consequences are `sn-impl-arch`'s
to write and P6's to build.

# IAP Spike Results — is `Instance.iapEnabled` real, inert, or half-delivered?

**OQ-15 | Agent: `spike-iap` | Date: 2026-08-26**
**Project:** `ptone-experiments`, region `us-east4`
**Instances:** `iap-test-1`, `iap-test-2` (deleted after tests)

---

## Headline

**IAP on Cloud Run Instances is LIVE.** Setting `iapEnabled: true` activates IAP
enforcement at the Google Frontend edge. Unauthenticated requests get 302 to
Google sign-in. Authenticated requests arrive at the container carrying
`X-Goog-IAP-JWT-Assertion` with ES256 signatures and
`iss: https://cloud.google.com/iap`.

This is not inert. This is not half-delivered. The edge enforces it.

---

## Test Matrix and Verdicts

### Preconditions

| # | Test | Verdict | Finding |
|---|------|---------|---------|
| **I0** | `validateOnly` with `iapEnabled: true` | **PASS** | API accepts and echoes back. Same echo pattern as `sandboxLauncher`. |
| **I1** | IAP brand / OAuth client | **EXISTS** | Brand present, `iap.googleapis.com` enabled. "Not configured" ruled out. |
| **I2** | gcloud CLI `--iap` flag | **PARTIAL** | `--public` help references `--no-iap` but the flag doesn't exist in gcloud 582.0.0. REST API only. |

### Core Matrix

| # | `iapEnabled` | `invokerIamDisabled` | Probe | Verdict | Finding |
|---|---|---|---|---|---|
| **I3** | false | false | unauth GET | **PASS** | 403 from invoker IAM. Baseline works. |
| **I4** | false | true | unauth GET | **PASS** | 200, container reached. `invokerIamDisabled` honoured. |
| **I5** | **true** | **true** | unauth GET | **IAP LIVE** | **302 to `accounts.google.com`**, `x-goog-iap-generated-response: true`, server: Google Frontend. Container never reached. |
| **I6** | true | false | OIDC token | **IAP LIVE** | See audience analysis below. |
| **I7** | true | * | browser flow | **SKIPPED** | Liveness confirmed in I5; browser flow unnecessary. |

### I5 — Raw Evidence (the headline test)

```
< HTTP/2 302
< set-cookie: GCP_IAP_XSRF_NONCE_9aUtEeMSKj8TMmzozUzcog=1; ...
< location: https://accounts.google.com/o/oauth2/v2/auth?client_id=721899303052-3aurml9he9hm8p04a3grl7e5tutj0k3t.apps.googleusercontent.com&...
< x-goog-iap-generated-response: true
< server: Google Frontend
```

Body: `Invalid IAP credentials: empty token`

### I6 — Audience Analysis

Three variants tested with OIDC tokens:

| Variant | Token audience | `invokerIamDisabled` | Result |
|---------|---------------|---------------------|--------|
| A | IAP client ID | false | **Passed IAP** (no `x-goog-iap-generated-response`), **rejected by invoker IAM** (401) |
| B | Instance URL | false | **Rejected by IAP** (`x-goog-iap-generated-response: true`, "Invalid JWT audience") |
| C | IAP client ID | true | **200 — reached container with `X-Goog-IAP-JWT-Assertion`** |

IAP client ID: `721899303052-3aurml9he9hm8p04a3grl7e5tutj0k3t.apps.googleusercontent.com`

**Q2 — RESOLVED (empirically confirmed 2026-08-26T01:48Z):**

**`invokerIamDisabled: true` IS required when using IAP on Instances.**

This was tested empirically with all three conditions met:
- `iapEnabled: true`
- `invokerIamDisabled` absent (default false)
- IAP SA (`service-721899303052@gcp-sa-iap.iam.gserviceaccount.com`) granted
  `roles/run.invoker` at both resource and project level

Result: **401** — `www-authenticate: Bearer error="invalid_token"`, no
`x-goog-iap-generated-response` header (meaning IAP passed, invoker check failed).

**Root cause: audience mismatch in `x-serverless-authorization`.**

When `invokerIamDisabled: true` is set, the probe container captures both:
- `x-goog-iap-jwt-assertion` (ES256): `aud: /projects/721899303052/locations/us-east4/services/outage-test-e4`
- `x-serverless-authorization` (RS256): `aud: /projects/721899303052/locations/us-east4/services/outage-test-e4`

Both use **`services`** in the audience path. But the Instance's invoker check
expects the Instance URL (`https://outage-test-e4-721899303052.us-east4.run.app`)
or an `instances` path. The `services` audience in `x-serverless-authorization`
does not match, so the invoker check rejects the token regardless of IAM grants.

**This is an IAP-side integration gap, not a configuration error.** The
[documented procedure](https://cloud.google.com/iap/docs/enabling-cloud-run)
for Cloud Run Services (grant `roles/run.invoker` to the IAP SA) works because
IAP's `x-serverless-authorization` audience matches the Service path. For
Instances, IAP still generates a `services`-path audience, which doesn't match.

**The grant is irrelevant — the token itself cannot pass verification.**

**Design consequence:** When deploying with `iapEnabled: true` on Instances,
always set `invokerIamDisabled: true`. IAP enforcement at the edge is the
perimeter; the invoker check cannot work with IAP on Instances until IAP
generates the correct audience. This is a footgun if operators follow the
Services documentation.

### Q2 Controls (requested by sn-impl-arch, 2026-08-26T01:53–02:00Z)

**Control 1 — Is the audience actually rejected?**
Tested on fresh instance `q2-control` (iapEnabled=false, invokerIamDisabled=false):

| Variant | Token audience | HTTP | Verdict |
|---------|---------------|------|---------|
| 1a | `/projects/721899303052/locations/us-east4/services/q2-control` | **401** | **Rejected** |
| 1b | `https://q2-control-721899303052.us-east4.run.app` | **200** | Accepted |
| 1c | `/projects/721899303052/locations/us-east4/instances/q2-control` | **200** | Accepted |

**The invoker check accepts URL and `instances`-path audiences. It rejects `services`-path.**
IAP generates `services`-path in `x-serverless-authorization`. The mismatch is the mechanism.
1c narrows the defect: if IAP generated `/…/instances/{name}` instead of `/…/services/{name}`, it would work.

**Control 2 — 401 vs 403 distinction:**

| Input | HTTP | Interpretation |
|-------|------|----------------|
| No auth header | **403** | No credential — IAM denial |
| Services-path audience token | **401** | Token presented, verification failed |
| Garbage bearer token | **401** | Token presented, unverifiable |
| Correct audience + authorized | **200** | Success |

401 = token was presented and examined but failed at verification (audience mismatch).
403 = no credential at all. The IAP-on case is 401, confirming a token WAS consulted.

**Control 4 — Does the invoker check read `x-serverless-authorization`?**
(Resolves (A) vs (B) ambiguity from Control 2 note above.)

| Variant | Header | Audience | HTTP | Verdict |
|---------|--------|----------|------|---------|
| 4a | `x-serverless-authorization` | URL | **200** | **Invoker reads this header** |
| 4b | `Authorization` (control) | URL | **200** | Known-good |
| 4c | `x-serverless-authorization` | services-path | **401** | Rejected — audience mismatch |
| 4d | `x-serverless-authorization` | instances-path | **200** | Accepted |

**4a is decisive: (A) confirmed.** The invoker check reads `x-serverless-authorization`,
verifies audience, checks IAM. IAP emits the wrong audience — that is the defect.

Note: 4a–4d were run with IAP off — tokens injected directly by curl.
`x-serverless-authorization` set by an external client is not stripped at the edge.
Not a vulnerability (token is signature- and audience-verified), but a non-obvious
property of the request path.

**Control 3 — Confounders eliminated:**
Fresh instance `q2-control`, not PATCHed from other config:
- IAM grant: 01:54:39Z → 180s explicit propagation wait
- iapEnabled PATCH after IAM wait → full reconciliation (100s)
- 30s additional buffer → probe at 01:59:54Z (5m15s after grant)
- Result: **401** (same error, same response)

---

## I8 — JWT Assertion Decoded

The probe container (`python:3.11` HTTP server echoing request headers as JSON)
captured the full `X-Goog-IAP-JWT-Assertion`:

**JWT Header:**
```json
{"alg": "ES256", "typ": "JWT", "kid": "gO4i_Q"}
```

**JWT Claims:**
```json
{
  "aud": "/projects/721899303052/locations/us-east4/services/iap-test-2",
  "azp": "/projects/721899303052/locations/us-east4/services/iap-test-2",
  "email": "scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com",
  "exp": 1787707273,
  "iat": 1787706673,
  "identity_source": "GOOGLE",
  "iss": "https://cloud.google.com/iap",
  "sub": "accounts.google.com:110532853671892060667"
}
```

### Comparison with hub's `IAPAuthenticator` expectations

| Check | Expected | Actual | Match? |
|-------|----------|--------|--------|
| Algorithm | ES256 only | ES256 | **YES** |
| `kid` against JWKS | Standard Google kid | `gO4i_Q` | **YES** |
| `iss` | `https://cloud.google.com/iap` | `https://cloud.google.com/iap` | **YES** |
| `aud` | Mandatory, configured | `/projects/721899303052/locations/us-east4/services/iap-test-2` | **FORMAT NOTE** |
| `exp` | Present, 30s skew | Present (600s validity) | **YES** |

**Audience format:** `/projects/{number}/locations/{region}/services/{name}`. Uses
**`services`** even though the backend is a Cloud Run Instance, not a Service.
Confirmed by the REST URLs (all used `/instances` path):

```
POST https://run.googleapis.com/v2/projects/ptone-experiments/locations/us-east4/instances?instanceId=iap-test-1
PATCH https://run.googleapis.com/v2/projects/ptone-experiments/locations/us-east4/instances/iap-test-1?updateMask=iapEnabled
```

The hub's `IAPAuthenticator` must construct the expected audience as
`/projects/{number}/locations/{region}/services/{instance_name}`.

### Additional request headers from IAP

```
x-goog-authenticated-user-id: accounts.google.com:110532853671892060667
x-goog-authenticated-user-email: accounts.google.com:scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com
x-serverless-authorization: bearer <RS256 JWT from service-721899303052@gcp-sa-iap.iam.gserviceaccount.com>
```

---

## Supplementary Observations

| Observation | Result |
|-------------|--------|
| `describe` echoes `iapEnabled` after real deploy? | **YES** |
| Can `iapEnabled` be toggled on existing Instance via PATCH? | **YES** |
| Can `iapEnabled` be toggled OFF? | **YES** |
| Does enabling IAP change `urls`? | **NO** — URL format unchanged |
| Does enabling IAP change `ingress`? | **NO** — remains `INGRESS_TRAFFIC_ALL` |
| Does enabling IAP change `terminalCondition`? | **NO** — same `Running` / `CONDITION_SUCCEEDED` |
| Reconciliation time for iapEnabled toggle | ~30-75 seconds |
| gcloud `--public` references `--no-iap`? | **YES** — but `--no-iap` doesn't exist as a flag |
| `invokerIamDisabled` honoured on Instances? | **YES** — tested independently in I4 |

---

## Design Consequences

1. **S4.9a's auth-proxy service can disappear.** IAP works directly on Instances.
   The hub's `IAPAuthenticator` works against the Instance with one config change:
   audience = `/projects/{number}/locations/{region}/services/{name}`.

2. **Audience uses `services` even for Instances.** This is IAP's naming, not Cloud
   Run's. The REST API uses `/instances`, but IAP's JWT claims use `/services`.

3. **`invokerIamDisabled: true` IS required on Instances** (empirically confirmed).
   IAP's `x-serverless-authorization` token uses a `services`-path audience that the
   Instance invoker check cannot verify. Granting `roles/run.invoker` to the IAP SA
   is irrelevant — the token itself fails audience verification. This differs from
   the documented Services flow where the audience matches. **S2 implication:** the
   invoker check cannot provide defense-in-depth behind IAP on Instances; IAP at the
   edge is the sole perimeter.

4. **The `--iap` gcloud flag is not yet exposed** in 582.0.0 but is referenced in
   help text. REST API is the only path today.

5. **I5 falsifies the "half-delivered" hypothesis.** No security footgun.

---

## Methodology Notes

- All instances deployed via REST v2 API (`/v2/.../instances`), confirmed by URL
  captures and `gcloud beta run instances list`.
- Probe container: `python:3.11` HTTP server echoing request headers as JSON.
- `iapEnabled` toggled via PATCH with `updateMask` (REST creates were intermittently
  503 during the test window; PATCHes were reliable).
- Q2 empirical test: first on `outage-test-e4`, then reproduced cleanly on
  `q2-control` with 3-minute IAM propagation wait, full reconciliation, and 30s
  buffer. Same result both times.
- Q2 controls run on `q2-control` — Control 1 (audience acceptance matrix),
  Control 2 (401/403 distinction), Control 3 (confounder elimination).
- `q2-control` held in us-east4 for potential defect report; other instances deleted.

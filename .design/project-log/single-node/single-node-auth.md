# Single-node hosted: login options

**Status:** proposal / RFC
**Date:** 2026-08-15
**Companion to:** `.design/single-node-packaging.md` (expands Track 0.5)

## The question

How does a single-node user server get put behind a login? Username/password is the obvious
answer and the unappealing one.

## Why username/password is the wrong answer

Worth stating concretely rather than as taste. It is the only option on the table that requires
us to build *and then own*:

- a credential store and password hashing choice,
- a reset flow, which means SMTP configuration — a new required prerequisite on a tier whose
  entire goal is removing prerequisites,
- brute-force protection, lockout, and the operational tail that follows,
- a phishing surface for a secret the user has probably reused elsewhere.

And for a genuinely single-user deployment it is *weaker* than a 32-byte random token, which we
already know how to mint (`UATPrefix = "scion_pat_"`, `store/models.go:1461`). It buys nothing
the alternatives don't, and it is the only one that adds a credential database. Recommend
against.

## What already exists

Verified against the tree; this changes what's worth building.

| Mechanism | State |
|---|---|
| OAuth web flow (Google/GitHub) | Works. Redirect URI is `BaseURL + /auth/callback/<provider>` (`web.go:1626`) — **hostname-dependent**, source of the chicken-and-egg |
| **OAuth device grant (RFC 8628)** | **Fully implemented**, `handlers_auth.go:990-1117`, `oauth.go:625-834`. CLI uses it today via `--no-browser` / headless detect (`hub_auth.go:138`) |
| `ProxyAuthenticator` | Clean 2-method interface (`proxyauth.go:31-45`). One impl: IAP. Reusable `jwksCache` (`:232-355`) |
| UATs / `scion_pat_*` | SHA-256 hashed, scoped, revocable, 90d default, full CRUD UI at `/profile/tokens`. **API-only — cannot establish a browser session** |
| Dev auth | Token + a middleware that auto-admins any cookieless browser (`web.go:1253`) |
| Generic OIDC | **Does not exist.** Nine hardcoded `switch provider` sites + struct-shaped config + frontend buttons. (`ha-oidc.md` is about *outbound* tokens and does not help; `auth-proxy-mode.md:18`'s claim of a "partial custom OIDC provider" is stale.) |
| Passkeys / WebAuthn | Nothing. Zero matches tree-wide |
| First-admin bootstrap | Nothing. `AdminEmails` static config is the only durable admin |

Two findings that constrain everything below:

**Device grant is the only human login path in the repo with no dependency on a
publicly-resolvable hub URL.** No `redirect_uri` is ever sent (`oauth.go:644`, `:687`). That is
exactly the property the single-node tier needs.

**`AdminEmails` is re-applied as source of truth on every login** (`determineUserRole`,
`handlers_auth.go:1372`). Anyone promoted out-of-band via `PATCH /users/{id}` is **silently
demoted at next login**. Any new auth method needs a durable admin story that isn't a static
config list — otherwise the bootstrap admin evaporates on second login.

---

## Option 0 — SSH port-forward (zero code, available today)

Bind to loopback; the user reaches it with `ssh -L 8080:localhost:8080 vm`. On GCP,
`gcloud compute ssh --tunnel-through-iap` needs no public IP at all.

The login *is* the VM's SSH auth. And unlike today's `--host 0.0.0.0` footgun, the loopback
assumption that dev-auth is built on becomes actually true rather than nominally true.

**Pros:** costs nothing, works now, no open ports, no DNS, no certs, no OAuth.
**Cons:** no mobile, no sharing, re-tunnel every session. Not a product answer — but it is the
honest "day one" answer and should be documented as the minimal path.

## Option 1 — Delegate to a front door (recommended default)

The hub runs no auth code; a proxy authenticates first and passes a *verifiable* assertion.
This is what `auth.mode: proxy` already is — it is just IAP-only today.

**Cloudflare Tunnel + Access** is the strongest fit: no public IP, no inbound ports, no DNS
records, no certificates, free tier, and identity from Google/GitHub/email-OTP. It collapses
Track 0.4 (TLS/DNS) and this question into one setup step.

Implementation is genuinely small: a `CloudflareAccessAuthenticator` reading
`Cf-Access-Jwt-Assertion`, JWKS at `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`.
The existing `jwksCache` is directly reusable. Note you cannot reuse `IAPAuthenticator` — it
pins ES256 (`proxyauth.go:101`) and Cloudflare signs RS256.

Two pieces of cleanup this forces, both wanted anyway:
- the provider `switch` is **duplicated** in `server_foreground.go:1468` and `:1968`, and the
  web copy has no `case "header"` — so `provider: header` passes hub init and then hard-errors
  in web init. Today's `header` provider is a no-op stub and `TrustedProxies` is never populated
  from any config, so that whole path is dead code.
- web-side proxy provisioning is an inline reimplementation (`web.go:1420-1469`) that bypasses
  `provisionUser` and skips invite-audit logging. A new provider inherits that drift for free.

**Also in this family:** Pomerium, oauth2-proxy, Authelia — all self-hostable, same interface.

### Option 1b — Tailscale (network-as-auth)

Arguably the most progressive: the hub is *not reachable from the internet at all*. Auth is
tailnet membership; `ts.net` gives TLS; your laptop's CLI just works because it's on the tailnet.
One dependency solves auth + TLS + DNS + networking.

**Sharp edge, verified:** `Tailscale-User-Login` is an **unauthenticated header**. The
`ProxyAuthenticator` interface carries no network-trust signal — `isTrustedProxy`
(`auth.go:415`) is wired only to the dead legacy branch. Dropping a header-trusting Tailscale
provider behind this interface as-is would make the hub spoofable by anything that can reach the
listener. The correct implementation calls `tailscale whois` on the peer address via the local
API, grounding identity in WireGuard rather than a header — or binds the listener to the
Tailscale interface only.

**Tradeoff vs. Cloudflare:** Tailscale is better for solo; Cloudflare Access is better the
moment you want to show a colleague, since it needs nothing installed on their device.

## Option 2 — Device flow as a first-class login (self-contained fallback)

For users who won't take a third-party front door. Already built, and uniquely
hostname-independent.

The move that makes this compelling: **ship a pre-registered public device client**, the way
`gcloud` and `gh` do. User-side OAuth registration then drops to *zero* — no client to create,
and critically no hostname needed in order to create it. Shipping a device-client secret in a
distributed binary is standard practice for public clients.

Work required: device flow currently mints only `ClientTypeCLI` tokens (`handlers_auth.go:1163`)
and there is no path from it to a browser session. Needs either a browser-side device
prompt or a "paste your CLI token to sign in" exchange.

Caveats: still depends on Google/GitHub being acceptable IdPs; Google requires a
"TV & Limited Input" client type for the device grant, and there is no code fallback from
`oauth.device.*` to `oauth.cli.*` (`oauth.go:126-137`).

## Option 3 — Passkeys / WebAuthn (first-party, later)

The modern answer if a real first-party login page is wanted: phishing-resistant, no password
store, no reset flow. Enrollment pairs naturally with a bootstrap token — first boot prints a
token, you enroll a passkey, that's your login forever.

Ranked below the others for three reasons: it is the most work (library, credential storage,
enrollment/recovery UX, all from zero); **the RP ID is bound to the hostname**, which fights
directly with the `sslip.io`/IP-derived-hostname simplification proposed in Track 0.4 — change
IP, lose your passkeys; and it is browser-only, so CLI still needs UATs or device flow. It is a
good *complement*, not a complete answer.

## Option 4 — Generic OIDC

"Bring your own issuer" — Authentik, Keycloak, Okta, Auth0. Worth having eventually for
self-hosters, but it is real work: nine hardcoded `switch provider` sites, struct-shaped config
(`OAuthClientConfig{Google, GitHub}`) that must become map-shaped, plus frontend provider
buttons. Does not solve the chicken-and-egg on its own — an OIDC client still needs a redirect
URI, so it still needs the hostname. Lower priority than it first appears.

## Option 5 — Bootstrap token (not optional; every option needs it)

Not a login system — the bridge into one. First boot prints a one-time token to
stdout/journal/serial console; exchange it in the browser for an admin session; configure the
real auth method from there, now that the hub knows its own hostname.

Mostly assembled already: the dev-token generate-and-persist pattern (`devauth.go:47-88`) and the
session-establishing five-key block. The missing piece is that there are **four independent
writers** of that block (OAuth callback `web.go:1766`, proxy `:1486`, dev-auth `:1275`,
test-login `handlers_test_login.go:165`) and **no function to call**. Extracting
`ws.establishSession(w, r, *store.User)` is a ~20-line refactor and is the natural insertion
point for every option above.

Must also fix the `AdminEmails` demotion behaviour, or the bootstrap admin loses admin on second
login.

---

## Recommendation

1. **Option 5 (bootstrap token) + the `establishSession` extraction** — prerequisite for
   everything, small, and unblocks the hosted onboarding wizard from the packaging doc.
2. **Option 1 (front door) as the recommended default.** Finishing `proxy` auth from IAP-only to
   a real provider set is mostly-built infrastructure, and it is the only answer where we write
   no auth code, hold no credentials, and the port is never open to the internet. Cloudflare
   Tunnel + Access simultaneously deletes the DNS and TLS prerequisites from the packaging doc.
3. **Option 2 (device flow + shipped public client)** as the self-contained path, because it is
   already built and is the only login flow that doesn't need to know its own hostname.
4. **Option 0 documented now** as the zero-setup path.
5. **Option 3 (passkeys)** if and when a first-party login page is wanted — with the hostname
   coupling understood.
6. **Skip username/password.**

Independent of all of this: dev-auth still auto-admins any cookieless browser and is not guarded
against a non-loopback bind (`server.go:123` advertises `--host 0.0.0.0`). That must be fixed
before cloud deployment gets easy, whichever option lands.

---

## Addendum: Cloudflare on a no-public-IP GCE VM, vs. Cloud Run + IAP

**Pricing figures below are from memory and could not be verified** — `cloud.google.com/nat/pricing`
redirect-looped on fetch and web search is blocked by org policy in this environment. Treat them
as order-of-magnitude and confirm with the pricing calculator. The structural argument does not
depend on them.

### Does Cloudflare Tunnel work on a VM with no external IP?

Yes — that is `cloudflared`'s canonical use case. It dials **outbound only** to the Cloudflare
edge, so there is no inbound port, no public IP, and no firewall rule to open. The
`scion-<name>-allow-http-https` rule in `gce-demo-provision.sh` disappears entirely.

**The catch worth planning around:** a GCE VM with no external IP has **no internet egress at
all** by default. You need Cloud NAT (plus a Cloud Router). So "no public IP" is not "no
networking setup" — you trade a firewall rule for a NAT gateway.

Two things make that better than it sounds:

- **Private Google Access** lets a no-external-IP VM reach Google APIs — Artifact Registry, GCS,
  Cloud Logging, Cloud Trace, Secret Manager — *without* traversing NAT and without NAT data
  processing charges. Since agent image pulls and all telemetry are Google-bound, that removes
  the bulk of the byte volume from the meter. What still needs NAT: `github.com` clones,
  npm/pip/apt, LLM API calls, and the `cloudflared` tunnel itself.
- **The fixed cost actually inverts.** An in-use external IPv4 is ~$3.65/mo; Cloud NAT is ~$1/mo
  per VM plus ~$0.045/GB processed. No-public-IP is *cheaper* on fixed cost, and stays cheaper
  until roughly 50-60 GB/mo of non-Google egress.

**Correction to what I said earlier:** I claimed Cloudflare "deletes the DNS prerequisite." That
was overstated. It deletes certificates, the public IP, and inbound ports, and it turns DNS into
a one-time nameserver change instead of ongoing certbot + `dns.admin`. But **you still need a
domain on Cloudflare**. Worth noting because IAP does not need one (below).

### Cloud Run + IAP as an auth proxy in front of the VM (the actual proposal)

**This is the strongest front-door option, and I initially analysed the wrong thing** — I read it
as "host the hub on Cloud Run" and answered that instead. The proposal is a scale-to-zero Cloud
Run service acting purely as an authenticating reverse proxy to a no-public-IP VM. That is a
different and better design.

Topology: browser → Cloud Run (IAP-protected, `run.app` hostname) → Direct VPC egress → VM
private IP → hub on :8080. The VM has no external IP and no inbound firewall rule; its only
ingress is from the Cloud Run VPC range.

**Why it beats Cloudflare for a GCP-centric deployment:**

1. **Zero new hub code.** `IAPAuthenticator` already exists, is wired, and is production-tested
   on the HA path. IAP injects `X-Goog-IAP-JWT-Assertion`, the proxy forwards it, the hub
   validates it with code we ship today. Cloudflare Access needs a brand-new RS256
   `ProxyAuthenticator`, a config struct, and wiring into both switches. This is the only
   front-door option that works with the hub as-is.
2. **No domain at all.** IAP protects the `run.app` hostname directly. Cloudflare requires a
   domain on their DNS. On the friction axis this is a clear win.
3. **No third-party dependency**, IAM-integrated, org-policy friendly.
4. **Genuinely scales to zero.** Unlike the hub, a proxy really can idle at $0 — my
   scale-to-zero objections applied to hosting the hub, not to a front door.
5. **The CLI story is already designed.** `auth.transport.mode: cloudrun_invoker` plus
   `GCPTransportMinter` (`server_foreground.go:1487-1503`) exists precisely so clients can
   traverse an IAP/Cloud Run front door with minted OIDC tokens — and `.design/ha-oidc.md` is
   exactly that design. **I was wrong to dismiss `ha-oidc.md` earlier**; it is irrelevant to
   inbound *login*, but it is the missing CLI piece for *this* topology.

**The one real integration problem — the agent endpoint split.** Verified in
`cmd/server_foreground.go:1195-1250`, the agent-facing hub endpoint resolves in this order:
project/broker settings → `--base-url` → **`SCION_SERVER_BASE_URL`** → settings-level
`SCION_HUB_ENDPOINT` → IAP-audience-derived Cloud Run URL → `localhost` fallback.

So `SCION_SERVER_BASE_URL` — which you must set to the `run.app` URL for browser cookies
(`web.go:482` derives the `Secure` flag from it) — *also* becomes the endpoint injected into
agent containers. Agents on the VM would then try to reach the hub by going out through NAT to
Cloud Run and back in, hitting IAP with no user credential. Absurd routing, and it fails.

Agents must talk to the hub locally and never traverse the proxy. There is already a knob for
this — `broker.container_hub_endpoint` (`settings_v1.go:466`) — but its guard is backwards for
this case: `applyContainerBridgeOverride` (`hubenv.go:111`) only fires when the resolved endpoint
**is** localhost, because it was built for "hub on loopback, container needs
`host.docker.internal`". Here the resolved endpoint is a public URL and we want to override it
*downward* to a local one.

Fix is small and well-scoped: let `container_hub_endpoint` override unconditionally, or add an
explicit agent-facing endpoint that sits above `SCION_SERVER_BASE_URL` in the chain. Worth doing
regardless — the current precedence conflates three distinct roles for one URL (browser origin,
OAuth redirect base, agent dial target).

*(Note the OAuth-redirect role vanishes in this topology, since IAP replaces OAuth. That leaves
`BaseURL` used only for the cookie `Secure` flag and a frontend-exposed field — so the conflation
is easier to unpick here than it looks.)*

**Other things to get right:**

- **Use Direct VPC egress, not the legacy Serverless VPC Access connector.** The connector bills
  always-on instances, which would destroy the scale-to-zero economics that motivate the design.
- **Cloud Run caps requests at 60 minutes**, which will cut long-lived SSE streams. Verify the
  dashboard's SSE client reconnects cleanly across that boundary.
- **SSE keeps the proxy warm.** Idle is genuinely $0, but an open dashboard means a continuously
  active instance. Small for a 0.5 vCPU proxy, not nothing.
- **You still need Cloud NAT** for agent egress (git, npm, LLM APIs) — the proxy handles ingress
  only.
- **You must build and deploy a proxy artifact** (a small nginx/Caddy container). Tiny and
  stable, but it is a second thing to version.
- **Cold start** on first request after idle — fine for a dashboard, another reason agents must
  stay local.

**Revised recommendation:** for a GCP-native single-node deployment this is the better default,
chiefly because it needs **no new hub code and no domain**. Cloudflare Tunnel remains the right
answer for cloud-neutrality, non-Google identity, or running outside GCP entirely. IAP TCP
forwarding stays the zero-artifact option when a browser from an unmanaged device isn't needed.

### Aside: why *hosting* the hub on Cloud Run doesn't work

Verified: **IAP on Cloud Run needs no load balancer.** Google's docs call direct-on-Cloud-Run the
recommended path, and the repo already assumes it — `isCloudRunIAPAudience`
(`server_foreground.go:966`) requires the native
`/projects/<number>/locations/<region>/services/<service>` form, and HA preflight *rejects* an
LB-style `backendServices` audience (`:940`). So the ~$18/mo forwarding-rule objection I would
otherwise have raised does not apply. IAP itself is free, and it protects the `run.app` hostname
directly — **no domain required at all**, which is genuinely lower friction than Cloudflare on
the DNS axis.

The problem is not cost. It is that Cloud Run cannot host this tier:

1. **Agents cannot run there.** `CloudRunRuntime.Run` still returns
   `"cloudrun: Run not yet implemented"` (`cloudrun_runtime.go:77`, re-verified at HEAD 52cf5a9).
   A Cloud Run hub needs GKE or a broker VM for agents — and once you are paying for a VM, you
   have paid for the VM *and* Cloud Run.
2. **The hub cannot scale to zero.** Scheduler, notification dispatcher, agent dispatcher, broker
   heartbeat (30s), and image-status rechecking are background goroutines. At zero instances,
   scheduled agent tasks silently do not fire — a functional break, not a latency tradeoff. So
   `min-instances=1`, roughly $7/mo idle, and the "scale to zero" premise is already gone.
3. **SSE is hostile to Cloud Run billing.** The dashboard holds long-lived SSE connections for
   real-time events; Cloud Run bills *active* CPU for a request's full duration. A dashboard left
   open ~8h/day is on the order of $20/mo of active CPU on top of idle. Scale-to-zero economics
   assume short bursty requests; this is the opposite shape.
4. **Storage settles it.** Single-node hosted is *defined* by embedded SQLite, and Cloud Run's
   filesystem is ephemeral. Filestore is ~$160-200/mo minimum and dominates every other line
   item. GCS FUSE is cheap but SQLite over it is a correctness hazard — no real POSIX locking.
   The remaining option is Postgres, at which point you are on the HA tier by definition, not
   single-node.

Cloud Run + IAP is the right shape for **HA hosted** — which is exactly where the repo already
uses it (`scripts/cloudrun/`: Postgres + GCS + Filestore). It is the wrong shape for single-node.

### Where the money actually is

| | Fixed monthly |
|---|---|
| VM (e2-standard-4, "10s of agents") | ~$98 (~$62 w/ 1yr CUD, ~$29 spot) |
| VM (e2-medium, light single-user) | ~$25 |
| 200 GB pd-balanced (current default) | ~$20 — **shrink to 50 GB for ~$5** |
| External IP | ~$3.65 |
| Cloud NAT | ~$1 + ~$0.045/GB |
| Cloudflare Tunnel + Access (≤50 users) | $0 (+ domain ~$1/mo) |
| IAP | $0 |

The networking-and-auth delta between every option here is **±$3/mo — noise** against a
$25-100/mo VM. The oversized 200 GB default disk costs more than the entire access-control
decision.

**So: choose on posture and friction, not price.**
- **IAP** — no domain, no third party, Google identity + IAM. Best if the user is already all-in
  on GCP.
- **Cloudflare Access** — needs a domain, but no GCP coupling, works for non-Google identities,
  and gives the no-inbound-port posture on any cloud or a home box.
- **IAP TCP forwarding** (`gcloud compute start-iap-tunnel`) is the sleeper option for a
  no-public-IP VM: authenticated, zero inbound exposure, no domain, no LB, free. It is Option 0
  with real identity behind it. Costs nothing to document and works today; the limitation is
  needing `gcloud` on the client, so no mobile browser.

## Open questions

1. Do we ship a public device OAuth client under a Google-owned project, and who owns rotation?
2. Is "single-user, bootstrap-token-only, no IdP ever" a supported end state, or strictly a
   waypoint to a real login?
3. Does admin need to become durable state (a DB flag set at bootstrap) rather than derived from
   `AdminEmails` on every login? Recommend yes.

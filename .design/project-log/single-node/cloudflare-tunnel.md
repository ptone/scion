# Cloudflare Tunnel + Cloudflare Access support

**Status:** design / implementation plan — no code
**Date:** 2026-08-17
**Author:** cf-tunnel-arch
**Verified against:** HEAD `066eeba` (all `file:line` below re-checked at this commit)
**Drift check 2026-08-25 (`origin/main` = `b1d9075c`, 81 commits later):** every structural
claim re-verified and holding — both duplicated provider switches still present (now
`server_foreground.go:1684-…` / `:2209-…`, ~+89 lines), `proxyauth.go` cites unchanged
(`:42`, `:101`, `:232`), `web.go:484` unchanged, OAuth redirect URIs now `:1820`/`:1884`
(+20, from #1246 which touches agent *port*-proxy routes only — no auth impact). Two
adjacent-area commits worth knowing at implementation time: #1253 encrypts the local secret
backend at rest (§5.6 unaffected — we still store nothing) and #1191 adds an IAP-audience
placeholder warning near the hub-side wiring switch. Implementer should still re-verify line
numbers at their HEAD; structure is stable.
**Context:** `/scion-volumes/scratchpad/projects/single-node/single-node-auth.md` (Option 1 +
Addendum) and `single-node-packaging.md`. This feature is standalone — it does not depend on
the bootstrap-token / `establishSession()` work, nor on the release-binary packaging track.

---

## 1. Summary & value proposition

A hub behind a Cloudflare Tunnel needs **no public IP, no inbound firewall rule, no TLS
certificate, no certbot, and no DNS delegation to Cloud DNS**. `cloudflared` dials outbound
only; Cloudflare Access (free ≤50 users) provides login with Google/GitHub/email-OTP identity.
For the single-node hosted tier this collapses most of today's deployment friction — OAuth
client registration, the hostname chicken-and-egg, certificates — into one Cloudflare setup.

**Honest scoping of the win** (per the Addendum correction): Cloudflare does **not** remove the
domain requirement. You still need a domain on Cloudflare DNS; what disappears is certs, the
public IP, inbound ports, and ongoing DNS automation (one nameserver change, once). For
GCP-native deployments, Cloud Run + IAP is arguably the lower-friction sibling (no domain, no
new hub code); Cloudflare is the right answer for cloud-neutrality, non-Google identity, and
anything outside GCP — including a home box.

Two independently useful halves:

1. **Auth**: `CloudflareAccessAuthenticator` implementing the existing `ProxyAuthenticator`
   interface (`pkg/hub/proxyauth.go:42-45`). Anyone already running Cloudflare Access in front
   of a hub — with their own tunnel, their own topology — benefits with nothing but config.
2. **Convenience UI**: a "Cloudflare Tunnel" section in the **Hub Server** pane of the admin
   server-config page (`web/src/components/pages/admin-server-config.ts:3286`,
   `renderHubServerTab`) that wraps a host-installed `cloudflared`: detection, status, and
   token-based setup of a systemd-managed tunnel.

The auth half has no dependency on the UI half. The UI half *configures* what the auth half
consumes, but each ships and works alone.

## 2. Non-goals

- Installing `cloudflared` for the user (detection + instructions only — see §5.5).
- Driving the Cloudflare account API (creating tunnels, DNS records, or Access applications on
  the user's behalf). The user does that once in the Zero Trust dashboard.
- The Cloud Run + IAP auth-proxy sibling design.
- Generic OIDC, passkeys, device-flow login, bootstrap tokens.
- Fixing dev-auth-on-`0.0.0.0` (`.design/single-node-packaging.md` item 0.7) — referenced, not
  absorbed.
- Machine-identity mapping for Access **service tokens** (deliberately rejected in v1, §3.2).

## 3. Architecture

### 3.1 Verified Cloudflare facts (from Cloudflare docs, 2026-08)

| Item | Value |
|---|---|
| Assertion header | `Cf-Access-Jwt-Assertion` (browsers also carry a `CF_Authorization` cookie; the header is canonical) |
| Signing algorithm | **RS256** (`IAPAuthenticator` pins ES256 at `proxyauth.go:101` — cannot be reused) |
| JWKS | `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs` (serves current + previous keys) |
| Issuer | `https://<team>.cloudflareaccess.com` |
| Audience | The Access application's **AUD tag** (dashboard: Application → Additional settings) |
| Identity claims | `sub` (stable user ID), `email`, `exp`, `iat`, `nbf`, `identity_nonce`, `country`, `type` |
| Service-token JWTs | `sub: ""`, no `email`, `common_name: <client-id>` — distinguishable and must be handled explicitly |
| Headless tunnel setup | **Remotely-managed tunnels**: created in the dashboard (on the user's laptop), yielding a *tunnel token* (`eyJ…`). On the VM: `sudo cloudflared service install <TOKEN>` — installs a systemd unit; **no browser, no `cloudflared tunnel login`, ever, on the server**. Tokens are rotatable from the dashboard |
| Quick tunnels (`trycloudflare.com`) | No account needed, but **no SSE support** and no Access — useless for a hub dashboard (SSE-driven). Not part of this design |

### 3.2 Auth half

**New authenticator** — `pkg/hub/proxyauth_cloudflare.go`:

```go
// CloudflareAccessAuthenticator verifies Cloudflare Access assertions
// (Cf-Access-Jwt-Assertion). Implements ProxyAuthenticator.
type CloudflareAccessAuthenticator struct {
    TeamDomain string        // "myteam" or "myteam.cloudflareaccess.com" (normalized)
    Audience   string        // Access application AUD tag — MANDATORY
    Issuer     string        // override for tests; default derived from TeamDomain
    JWKSURL    string        // override for tests; default derived from TeamDomain
    HTTPClient *http.Client  // default: shared 10s-timeout client

    jwksCache *jwksCache     // REUSED as-is from proxyauth.go:232-376
    initOnce  sync.Once
}

func (a *CloudflareAccessAuthenticator) Name() string { return "cloudflare" }

func (a *CloudflareAccessAuthenticator) Authenticate(r *http.Request) (*ProxyUserInfo, error) {
    // 1. assertion := r.Header.Get("Cf-Access-Jwt-Assertion"); "" -> (nil, nil)
    // 2. jwt.ParseSigned(assertion, []jose.SignatureAlgorithm{jose.RS256})
    // 3. kid -> a.jwksCache.GetKey (identical flow to IAP :107-118)
    // 4. claims: iss == https://<team>.cloudflareaccess.com, aud contains Audience,
    //    exp/iat/nbf with 30s skew (reuse iapClockSkew or a shared const)
    // 5. Service-token assertions (sub == "" or email == ""): return a distinct,
    //    diagnosable error — "cloudflare: service-token assertion not supported for
    //    human login". See CLI note below for why this is correct, not a gap.
    // 6. return &ProxyUserInfo{Subject: sub, Email: lower(email), DisplayName: "", Domain: ""}
}
```

Notes:
- `jwksCache` is package-internal to `hub` and needs **zero changes** — lazy fetch, 1h refresh,
  on-miss refresh, 5s debounce, last-good serving, 1MB cap all verified at
  `proxyauth.go:232-376`.
- Access JWTs carry no display name. `DisplayName` stays empty (IAP behaves the same,
  `proxyauth.go:138`). Fetching full identity via `identity_nonce` + `/cdn-cgi/access/get-identity`
  is possible but adds a per-login network call for cosmetics — rejected for v1.
- No IdP-prefix stripping (that is an IAP-ism, `proxyauth.go:224-226`).

**Config** — extend the existing proxy config chain end-to-end:

- `pkg/config/hub_config.go` — `ProxyAuthConfig` (`:313-321`) gains
  `Cloudflare *CloudflareAccessAuthConfig`; new struct `{ TeamDomain, Audience, Issuer, JWKSURL }`
  mirroring `IAPAuthConfig` (`:323-331`). `Provider` doc comment becomes `"iap" | "cloudflare"`.
- `pkg/config/settings_v1.go` — `V1ProxyConfig` (`:601-609`) gains a matching
  `Cloudflare *V1CloudflareConfig`; conversion at `:1554-1566` extended.
- Provider string: **`cloudflare`** (short, matches `Name()`; "cloudflare_access" is redundant —
  there is no other Cloudflare auth product we would front with).

**Wiring — kill the duplicated switch instead of triple-pasting it.** The provider switch
exists twice: hub side `cmd/server_foreground.go:1595-1614`, web side `:2127-2147`. The web copy
already lacks the `header` case, so the two have *already* diverged once. Replace both with one
constructor in `pkg/hub` (which already imports `pkg/config` — see `admin_settings.go:27`):

```go
// pkg/hub/proxyauth.go (or a new proxyauth_config.go)
// NewProxyAuthenticator builds the configured ProxyAuthenticator.
// Returns (nil, nil) when authMode != "proxy". Error on unknown/incomplete provider config.
func NewProxyAuthenticator(authMode string, p *config.ProxyAuthConfig) (ProxyAuthenticator, error)
```

Both call sites reduce to one call. This is the "keep the abstraction clean" ask from the
strategy thread: the next provider (Pomerium, oauth2-proxy, a corrected Tailscale whois-based
one) is a new `case` in exactly one place.

**Dead code decision (the `header` provider and `extractProxyUser`).** Verified dead:
`case "header"` is a no-op stub (`server_foreground.go:1608-1610`), web init rejects it
(`:2144-2145`), `ServerConfig.TrustedProxies` (`pkg/hub/server.go:114-115`) is never populated
from any config or flag, so the legacy path (`auth.go:276-288`, `extractProxyUser`,
`parseTrustedProxies :468`, `isTrustedProxy :494`) is unreachable. Additionally
`settings.yaml.example:141-146` documents a proxy shape (`enabled/header/trusted_proxies`) that
does not even match the real `V1ProxyConfig` struct. **Recommendation: remove it in this work**,
as a separate commit inside Phase 1: (a) it is provably unreachable; (b) an unauthenticated
header-trust provider is exactly the spoofing trap the single-node-auth doc warns about for
Tailscale — keeping a stub invites someone to "finish" it; (c) the shared-constructor refactor
touches every one of these lines anyway, so removal is cheapest now. If reviewers prefer
caution, the fallback is `default: error` listing valid providers and deleting only the stub
case — but keeping `extractProxyUser` has no identifiable customer.

**Consumption sites need no changes.** API middleware (`pkg/hub/auth.go:233-293`) and web
middleware (`pkg/hub/web.go:1449-1685`) operate on the interface. One drift to flag, not fix:
the web middleware re-implements find-or-create inline (`web.go:1593-1640`), bypassing
`provisionUser` and its invite-audit logging (`handlers_auth.go:1281` emits
`InviteAuditUserActivated`; the API path routes through `cfg.ProxyUserProvisioner`,
`auth.go:247`). The Cloudflare provider inherits this drift exactly as IAP does today — invited
users activated through the web proxy path skip the audit trail. Scoped out (pre-existing,
provider-agnostic), recommended as an immediate follow-up: extract the shared provisioning into
`provisionUser` and call it from `web.go`.

**CLI / API clients through Access (important non-change).** The hub's API middleware tries the
proxy authenticator **only when no bearer token is present** (`auth.go:231-234`). A `scion` CLI
request carrying a UAT/JWT authenticates at the hub exactly as today; Cloudflare is only a
reachability problem for it. The documented answer is an Access **service token**: the client
sends `CF-Access-Client-Id` / `CF-Access-Client-Secret` headers, Access lets the request
through (Service Auth policy) and injects a service-token JWT, which the hub *ignores* because
the bearer path wins. This is why the authenticator rejecting service-token assertions for
*human login* (no email → no user mapping) is correct. Gap to close in a small optional phase:
`pkg/apiclient/transport.go` has no extra-headers option (verified — `TransportOption`s cover
client/timeout/retry/UA/auth only), so the CLI cannot send the two headers yet.

### 3.3 Convenience-UI half

**Where the endpoints live.** Not under `/api/v1/system/*` — that entire surface is
`requireWorkstation`-gated (404s in hosted mode, `pkg/hub/server.go:3590-3597`) and mostly
`assertLoopback`ed. The right precedent is the **admin maintenance surface**:
`/api/v1/admin/maintenance/operations/{key}/run` (`server.go:3479-3480`) is gated on
`role == "admin"` only (`admin_maintenance.go:40-44`), works in hosted mode, and already has
async execution with captured logs and run history (`admin_maintenance.go:190-249`).

Design: a small dedicated status/config surface plus maintenance executors for the mutating
staged operations.

```
GET  /api/v1/admin/tunnel/status        (new file pkg/hub/admin_tunnel.go; admin-gated)
  -> {
       "cloudflared": { "detected": bool, "path": str, "version": str },
       "service":     { "installed": bool, "active": bool, "unit": "cloudflared.service" },
       "platform":    { "os": "linux", "systemd": bool },        // degrades on darwin/no-systemd
       "auth":        { "mode": str, "provider": str, "configured": bool,
                        "verify": { "seen_valid_assertion": bool, "last_seen": ts,
                                     "last_error": str } },       // §5.1 shadow verification
       "base_url":    { "value": str, "https": bool }             // §3.5 — UI flags stale/missing
     }

POST /api/v1/admin/maintenance/operations/tunnel-install/run     (existing framework)
  params: { "token": "<tunnel token>" }   // pass-through secret, §5.6
POST /api/v1/admin/maintenance/operations/tunnel-uninstall/run
POST /api/v1/admin/maintenance/operations/tunnel-restart/run
```

Detection mirrors `scionruntime.DetectContainerRuntime()` (`pkg/runtime/container.go:24-34` —
plain `exec.LookPath`): `exec.LookPath("cloudflared")` + `cloudflared --version` +
`systemctl is-active cloudflared` / `is-enabled`, each with short timeouts, all read-only.

**Executors** (new `TunnelInstallExecutor` etc. in `pkg/hub/maintenance_executors.go`, wired in
`resolveMaintenanceExecutor`, `admin_maintenance.go:252-309`), following `PullImagesExecutor`
(`:166-233`) for staged progress and `RebuildServerExecutor` (`:243+`) for the
sudo-with-narrow-sudoers pattern.

**One wrinkle the maintenance framework must absorb: secret params.** Operation params are
persisted into the run record (`op.Result`, params logged). The tunnel token must be marked
transient — either a `secretParams` allowlist the executor framework strips before persisting,
or the executor reads the token from a one-shot in-memory handoff. Smallest change: strip
`params["token"]` (replace with `"<redacted>"`) before `UpdateMaintenanceOperation`. This is a
small, explicit change to `executeOperation` and is called out in the file list.

**Frontend** — a new "Cloudflare Tunnel" section in `renderHubServerTab()`
(`admin-server-config.ts:3286`), alongside the existing sections, with three progressive states:

1. `cloudflared` not detected → short instructions + link to Cloudflare's install docs +
   copy-paste commands; a "re-check" button. Nothing else rendered.
2. Detected, no service → setup form: tunnel-token paste field (password-style input, never
   echoed back by any GET), "Install & start tunnel" button → runs `tunnel-install`, streams the
   run log (existing maintenance-run UI pattern).
3. Service installed → status chips (active/inactive, version), restart/uninstall buttons, and
   the **auth enablement flow** (§5.1): provider config fields (team domain, AUD tag) which are
   ordinary settings writes (`server.auth.proxy.*` via the existing PUT
   `/api/v1/admin/server-config`, `admin_settings.go:127-157`), the shadow-verification status,
   and the mode-flip button that stays disabled until verification passes.

The provider *auth* fields also appear in the Authentication tab (`renderAuthTab`) where auth
config natively lives; the Hub Server pane section links/embeds them for the guided flow. (If
reviewers want zero duplication, keep auth fields only in the Authentication tab and have the
tunnel section deep-link — cosmetic either way.)

**Settings-schema debt this exposes (must fix in Phase 2):** the settings JSON schema's
`serverAuth` def (`pkg/config/schemas/settings-v1.schema.json`, `$defs/serverAuth`) has **no
`mode` and no `proxy` properties at all** today, despite `V1AuthConfig` carrying both
(`settings_v1.go:574-589`). UI-driven proxy config cannot validate against the schema until the
schema learns `auth.mode` and `auth.proxy.{provider, iap, cloudflare}`. Also fix the stale
`settings.yaml.example:141-146` block, which documents a proxy shape that no longer exists.

### 3.4 Topology (what the docs must state)

```
browser ──► Cloudflare edge (Access login: Google/GitHub/OTP)
                 │  adds Cf-Access-Jwt-Assertion (RS256, aud=AUD tag)
                 ▼
        cloudflared (systemd, on the VM; OUTBOUND-only connection to edge)
                 │  plain HTTP to origin
                 ▼
        hub web/API on 127.0.0.1:<port>   ◄── scion CLI w/ UAT (+ Access service token headers)
                 ▲
        agents on the VM / containers via host-gateway (never traverse the tunnel)
```

Agents are unaffected: they already resolve a local hub endpoint
(`broker.container_hub_endpoint`, bridge override in `pkg/runtimebroker/hubenv.go:111-140`).
Nothing in this design routes agent traffic through Cloudflare.

### 3.5 Base URL and external scheme (gap found in review — load-bearing)

Putting the hub behind a tunnel changes its public URL, and the hub cannot discover that at
runtime. Verified at HEAD:

- **Cookie `Secure` flag derives from BaseURL**: `web.go:484` —
  `Secure: strings.HasPrefix(cfg.BaseURL, "https://")`. Behind cloudflared the origin hop is
  plain HTTP; if BaseURL is not updated to the `https://` tunnel hostname, session cookies are
  issued **without `Secure`** even though browser→edge is TLS.
- **OAuth redirect URIs are built from BaseURL**: `web.go:1800` and `:1864`
  (`ws.config.BaseURL + "/auth/callback/" + provider`). Stale BaseURL = broken OAuth at the
  tunnel hostname. (Moot after the flip — proxy mode disables OAuth — but live during any
  transition where OAuth remains the login.)
- **No `X-Forwarded-Proto` handling exists in the web/session path.** The only forwarded-header
  handling in the tree is `storage_helpers.go:327-336`, scoped to storage URL construction. So
  BaseURL is the single source of truth for external scheme/host — this is configuration, not
  something middleware can infer.
- **BaseURL is not a settings key.** It resolves from the `--base-url` flag, then
  `SCION_SERVER_BASE_URL`, then defaults to `http://localhost:<webPort>`
  (`server_foreground.go:2102-2108`; it also feeds agent hub-endpoint resolution at
  `:1323-1331`). Neither `hub_config.go` nor `settings_v1.go` has a `base_url` field. The
  nearby `server.hub.public_url` settings key is a **different role** — it maps to
  `gc.Hub.Endpoint`, the agent-facing endpoint (`settings_v1.go:1404-1405`) — exactly the
  three-roles-in-one-URL conflation the single-node-auth Addendum flagged (browser origin,
  OAuth redirect base, agent dial target).

**Consequence for this design:** the guided flow (Phase 4) can write `auth.proxy.*` through the
settings API but **cannot set the hostname**. Until/unless BaseURL becomes a settings key
(open question Q6), setting `SCION_SERVER_BASE_URL=https://hub.example.com` is a manual
env/systemd edit + restart, and the docs and the UI checklist must say so explicitly. The
tunnel status endpoint (§3.3) additionally reports `base_url` and whether it matches the
`https://` scheme, so the UI can flag the missing/stale value instead of letting the admin
discover it as a non-`Secure` cookie.

**The "Public URL" trap (found in review; a pre-existing bug this feature would funnel users
into).** The settable, similarly-named key `server.hub.public_url` is rendered as a field
labeled **"Public URL"** in the *same* Hub Server pane this design adds its section to
(`admin-server-config.ts:3322-3335`, label map `:387`; the hint text does say "Endpoint URL for
agents to call back to the Hub", but the label is what people read). An admin mid-tunnel-setup
who needs to "set the public URL" will find this field first. Setting it to the tunnel
hostname does two bad things at once, and **neither symptom appears near the field**:
agents and CLI are repointed at the Cloudflare edge (`Hub.PublicURL → gc.Hub.Endpoint`,
`settings_v1.go:1404-1405`; consumed agent/client-side at `pkg/agent/run.go:672-674`,
`pkg/hubsync/resolve.go:95`, `hubsync/sync.go:1312`) where Access blocks them — they hold no
service token — while web BaseURL is *unchanged*, so the `Secure` flag and OAuth redirects stay
wrong. This bites anyone putting the hub behind **any** authenticating reverse proxy today;
Cloudflare merely makes the path well-trodden. Mitigations, in escalating cost:
1. *Mandatory regardless (Phase 2 docs + Phase 4 UI copy):* the tunnel section and docs state
   explicitly that "Public URL" is the agent-facing endpoint and must **not** be set to the
   tunnel hostname.
2. *Nearly free, recommended regardless of Q6 (Phase 3, pure frontend):* relabel the field to
   "Agent Callback URL" (or "Agent/CLI Hub Endpoint") **and change its placeholder** — today it
   is `placeholder="https://hub.example.com"` (`admin-server-config.ts:3329-3333`), which is
   literally the shape of the tunnel hostname a Cloudflare user is about to paste; label and
   placeholder both pull toward the web-hostname reading and the one-line hint loses to both.
   Replace with an unmistakably internal shape (e.g. `http://10.0.0.5:8080`). The mislabel
   predates and outlives Cloudflare.
3. *Q6 proper:* a real `server.base_url` settings key, so the two roles are visibly distinct
   fields rather than one absent and one misnamed.
Additionally, the Phase 4 guided flow collects the tunnel's public hostname anyway (for the
verification instruction and the base-URL check), so the status endpoint cross-checks it:
`server.hub.public_url` host == tunnel hostname ⇒ explicit warning ("agents would dial through
Access and be blocked").

## 4. Alternatives considered

**A. Reuse/parameterize `IAPAuthenticator` (one generic JWT authenticator).** Rejected: the
deltas are not just the algorithm (ES256 vs RS256) but header name, prefix-stripping, claim
sets, service-token semantics, and issuer derivation. A parameter-soup "GenericJWTAuthenticator"
would have 6+ knobs and encode Cloudflare/IAP conditionals anyway. Two small concrete types
sharing `jwksCache` is simpler and matches the existing file's structure. (A third provider can
motivate extraction later; the shared constructor keeps that door open.)

**B. Hub-managed `cloudflared` child process** (hub spawns and supervises the tunnel). Rejected
for lifecycle reasons — see §5.4. Summary: the tunnel must outlive hub restarts, because in
proxy mode the tunnel *is* the admin's path to the hub; tying its lifetime to the hub process
turns every hub restart/crash into a potential lockout, and turns the hub into a process
supervisor (restart/backoff/log-rotation) that systemd already is.

**C. Cloudflare API-token automation** (UI takes a CF API token; hub creates the tunnel, DNS
record, and Access app via API). Genuinely nicer UX; rejected for v1 scope: it puts a powerful
account credential in the hub, triples the API surface (three CF resource types + error
handling + drift reconciliation), and the manual dashboard flow is a one-time, well-documented
~10-minute task. The design leaves room: the tunnel-install executor takes a token regardless
of who minted it. Recorded as future work.

**D. Put cloudflared endpoints on `/api/v1/system/*`.** Rejected: `requireWorkstation` 404s the
whole surface in hosted mode (`server.go:3590-3597`) and the loopback assertions are
structurally wrong for a hosted admin acting through the tunnel itself. Loosening those gates
for one feature would weaken guarantees the workstation onboarding path relies on.

**E. Provider string `"cloudflare_access"`.** Rejected (cosmetic): `Name()` strings elsewhere
are short (`"iap"`), and config enums read better as `provider: cloudflare`.

## 5. The six design tensions — resolutions

### 5.1 Lockout risk → shadow verification gate + documented SSH recovery; no auto-revert

The failure: admin flips `auth.mode: proxy` with a broken tunnel/Access config → OAuth handlers
off, proxy assertions never arrive → nobody can log in.

**Recommendation — staged enablement with a verification handshake:**

1. Admin fills in team domain + AUD while `auth.mode` is unchanged. Saving these is harmless.
2. Hub runs the authenticator in **shadow mode**: a middleware-adjacent hook (web server only)
   that, when `auth.mode != "proxy"` but a Cloudflare provider is fully configured, inspects
   incoming requests for `Cf-Access-Jwt-Assertion` and records verify-outcome metadata
   (last-seen time, last error, email seen) — *without ever authenticating anyone*. Cheap: the
   authenticator already returns `(nil, nil)` on absence; shadow mode just calls
   `Authenticate()` and stores the outcome in memory, surfaced by `GET /admin/tunnel/status`.
   **The hook must run pre-auth and on all routes, including public ones** — this matters, see
   the ordering note below.
3. The UI's "Switch to Cloudflare Access login" button is disabled until shadow state shows a
   valid assertion **for the requesting admin's own email**, observed through the tunnel.
   The guided flow tells the admin: "open https://hub.example.com through Cloudflare now."
4. Only then does the UI write `auth.mode: proxy` + `provider: cloudflare` (restart-required,
   like all Layer-0 auth settings — `reloadSettings` already reports `requires_restart`,
   `admin_settings.go:359-393`).

**What the gate does and does not require (bootstrap ordering — must be explicit in docs).**
The verification request does **not** require a hub login at the tunnel hostname: Access
authenticates at the Cloudflare edge, so a request that reaches the hub through the tunnel
carries a valid assertion even if the hub itself would 401 it or serve the login page — which
is why step 2 observes pre-auth on all routes. The email match in step 3 compares the
assertion's email against the admin's *existing* session on whatever URL they are driving the
UI from. That session is the real constraint: at the tunnel hostname, OAuth login presupposes
an updated BaseURL *and* the tunnel hostname registered as a redirect URI (§3.5) — and the
target user is adopting Cloudflare precisely to avoid OAuth registration. Bootstrap tokens are
out of scope here, so the **supported bootstrap path on a fresh single-node hub is dev-auth
over an SSH port-forward** (`ssh -L`), from which the admin runs the entire guided flow:
configure tunnel + provider → set `SCION_SERVER_BASE_URL` to the tunnel hostname (manual until
Q6 lands) → open the tunnel URL in another tab to satisfy the shadow gate → flip mode →
restart. The Phase 2 docs page writes this ordering out step by step rather than leaving it
implicit; hubs that already have working OAuth at an existing hostname can drive the flow from
there instead. (When the bootstrap-token work from `single-node-auth.md` Option 5 lands, it
replaces the SSH step verbatim — the rest of the flow is unchanged.)

**Escape hatch (documented, not built):** on single-node, `settings.yaml` is root/SSH-editable;
recovery is `ssh vm`, set `auth.mode: oauth` (or `dev` + loopback), `systemctl restart
scion-hub`. The docs page for this feature gets an explicit "locked out?" box.

**Auto-revert: recommended against.** Because the mode flip is restart-required, auto-revert
means persisted revert state across restarts + a second automatic restart + a timer racing the
admin's first login — three new failure modes to guard one, and a mis-fire reverts a *working*
config (e.g., admin flips mode then goes to lunch before logging in through the tunnel). The
shadow gate prevents the misconfiguration class up front; SSH recovers the residual class.
If a reviewer still wants belt-and-braces, the cheap variant is a **warning banner** ("proxy
mode active but no valid assertion seen in 24h") rather than automatic state mutation.

### 5.2 Direct-access bypass → explicit requirement; warn, don't enforce

Access identity is only meaningful if the hub is unreachable except through the tunnel. With
cloudflared on the same host, origin traffic arrives via loopback, so **binding the hub/web
listener to `127.0.0.1` is the natural posture** — but two verified realities argue for *warn*
rather than *enforce*:

- A loopback-only bind breaks colocated agent containers: the bridge override rewrites
  localhost endpoints to `host.docker.internal` (`hubenv.go:106-140`), and the container then
  dials the host-gateway IP — which a `127.0.0.1`-bound listener does not serve. On a
  no-public-IP VM (the recommended shape, per the Addendum), binding `0.0.0.0` is safe: there
  is no inbound path at all.
- The authenticator must also serve IAP-style topologies where the proxy is remote and the bind
  is legitimately non-loopback. `Authenticate()` has no business knowing the bind address.

**Resolution:** (a) docs state the requirement bluntly — *"the hub must not be reachable except
via the tunnel: bind loopback, or run a VM with no public IP / default-deny inbound"*; (b) at
startup, when `provider == "cloudflare"` and the web/hub listeners bind a non-loopback address,
log a prominent warning naming the risk; (c) the tunnel status endpoint reports the bind so the
UI section can show the same warning persistently. Hard-fail is rejected: the server cannot see
its own firewall posture, so a hard gate would both false-positive (no-public-IP VMs) and
false-negative (loopback bind but cloudflared on another box). Defense-in-depth option recorded:
`RequireTrustedProxyIP` already exists in config (`hub_config.go:319-320`) and could later gate
assertion acceptance to loopback/RFC1918 peers for the colocated case.

### 5.3 Headless setup → remotely-managed tunnel token; no `cloudflared tunnel login`, ever

`cloudflared tunnel login` (browser, account cert) is the *locally-managed* flow and is simply
not used. The design standardizes on **remotely-managed tunnels**:

1. User (laptop, browser): Zero Trust dashboard → create tunnel → set public hostname →
   protect it with an Access application (also captures the AUD tag) → copy the tunnel token.
2. VM (via the hub UI, §3.3): paste token → hub runs the install executor →
   `cloudflared service install <token>` (via wrapper, §5.6) → systemd unit up, tunnel
   connects outbound.

The only browser steps happen on the user's own machine against Cloudflare, which is
unavoidable (it's their account) and one-time. Token rotation: dashboard-side rotate, paste new
token, re-run install — the same executor is idempotent (`service install` over an existing
service updates the token; executor should `systemctl restart` after).

### 5.4 Process lifecycle → systemd owns the process; the hub only observes and requests

**Owner: systemd**, via `cloudflared service install`. Not a hub-managed child. Reasons:

- **Restart decoupling.** In proxy mode the tunnel is the admin's only path in. A hub-owned
  child dies with the hub — every `rebuild-server` restart (`maintenance_executors.go:243+`)
  would sever the connection the admin is watching it through. Systemd keeps the tunnel up
  across hub restarts, crashes, and upgrades.
- **Crash handling for free.** The generated unit restarts cloudflared on failure; the hub
  would otherwise need supervise/backoff/log-plumbing code.
- **Precedent.** The starter-hub already generates a systemd unit and narrowly-scoped sudoers
  for exactly this shape of privileged host operation (`gce-start-hub.sh:176-208`, `:337-357`).
  This design extends that pattern rather than inventing a second lifecycle model.

Hub restart → no effect on tunnel. Tunnel crash → systemd restarts it; hub status endpoint
surfaces `active: false` in the interim. Hub uninstalled → tunnel keeps running until
explicitly uninstalled (correct: it may front other services; the uninstall executor exists for
the deliberate case).

Non-Linux / no-systemd hosts: the status endpoint reports `platform.systemd: false` and the UI
degrades to detection + documentation (macOS `cloudflared service install` uses launchd — out
of scope to manage, fine to document).

### 5.5 Scope of "if available" → detect and degrade; never install

The line: **everything after the binary exists is in scope; acquiring the binary is not.**
In: `exec.LookPath` detection, version/service status, token-based service install/uninstall/
restart, status in UI, copy-paste install instructions when missing. Out: package installation
(`apt/curl|sh` as root from the hub process — a supply-chain and ownership liability), binary
self-download, version management, auto-update (cloudflared updates itself / via package
manager). Rationale beyond taste: install method is distro-specific, requires judgment about
repos/keys the user should consciously exercise once, and a failed half-install by the hub is
strictly worse than instructions. Precedent: `DetectContainerRuntime` detects Docker; nothing
in the repo installs Docker.

### 5.6 Secrets → the tunnel token is pass-through; nothing new is stored

Inventory: **tunnel token** (secret; grants "run this tunnel"), **team domain + AUD tag** (not
secrets — the AUD is an identifier, present in every JWT `aud`), **Access service-token
client secret** (client-side credential; never touches the hub).

The tunnel token's steady-state home is **root-owned systemd/cloudflared config on the host**,
written by `service install`. The hub does not need it after that, so: **do not persist it —
not in the secret backend, not in settings, not in run logs.** Handling rules: accept only in
the POST body of the install operation; redact from the persisted maintenance-run params
(§3.3's `executeOperation` change); never echo in any GET; pass to the wrapper via **stdin**,
not argv (argv is `ps`-visible and lands in the sudo log line). Concretely the sudoers surface
is one wrapper script installed alongside the unit (starter-hub and future installer):

```
# /usr/local/bin/scion-tunnel-helper  (root-owned, 0755; reads token from stdin)
#   install   — read token from stdin; exec cloudflared service install "$TOKEN"
#              (or write /etc/cloudflared/token env-file + templated unit, avoiding argv entirely)
#   uninstall — cloudflared service uninstall
#   restart|start|stop|status — systemctl <verb> cloudflared
# sudoers: scion ALL=(root) NOPASSWD: /usr/local/bin/scion-tunnel-helper *
```

The `pkg/secret` backend (`backend.go:25-49`, `local`/`gcpsm`) is **not used** in v1. It becomes
relevant only if a future iteration wants the hub to re-materialize the tunnel (e.g., container
packaging where the host filesystem is ephemeral) — noted as future work, decision reversible.

## 6. Phased plan (each phase lands alone; smallest useful increment first)

**Phase 1 — Authenticator + config + wiring (the standalone auth win).**
Anyone with their own tunnel/Access can use the hub behind Cloudflare after this phase, via
settings.yaml alone. Includes the shared-constructor refactor (prevents the third copy-paste)
and, as a separable commit, dead-code removal (`header` stub + legacy trusted-proxy path).

**Phase 2 — Settings schema, example, docs.**
Schema gains `auth.mode` + `auth.proxy.{provider,iap,cloudflare}` (fixing the pre-existing
schema/struct drift, §3.3); `settings.yaml.example` proxy block rewritten to the real shape;
a docs page: dashboard walkthrough (tunnel + Access app + AUD), topology requirement (§5.2),
**the bootstrap ordering incl. dev-auth-over-SSH-forward and the `SCION_SERVER_BASE_URL`
step (§3.5/§5.1)**, lockout recovery box (§5.1), CLI-via-service-token recipe (§3.2).

**Phase 3 — Detection + status (read-only UI).**
`GET /api/v1/admin/tunnel/status`, shadow verification (§5.1), Hub Server pane section states
1 and 3-read-only (detected/not; service status; bind warning). No mutations yet — this phase
is safely shippable and already useful for "is my tunnel up?".

**Phase 4 — UI-driven setup (mutating).**
Wrapper script + sudoers in starter-hub, executors (`tunnel-install`/`uninstall`/`restart`),
secret-param redaction in `executeOperation`, token paste form, guided enable flow with the
shadow-gated mode flip. **Base-URL handling:** the flow surfaces the current BaseURL (via the
status endpoint) and blocks the mode flip with instructions when it is missing or non-`https`;
if Q6 is answered yes, the flow *sets* it instead (settings key + restart), which is the only
way this stays UI-driven end to end (§3.5).

**Phase 5 (optional, small) — CLI service-token passthrough.**
`WithExtraHeaders` TransportOption in `pkg/apiclient/transport.go` + client config
(`CF-Access-Client-Id/Secret` from env/config). Unblocks remote CLI without weakening Access.

Ordering justification vs. the brief's (a)–(d): identical except schema/docs pulled into their
own phase 2 (the schema drift fix is riskier than it looks — `additionalProperties: false`
means an unpatched schema *rejects* configs Phase 1 makes meaningful) and the CLI passthrough
appended as the only piece touching client code.

## 7. File-level change list

**Phase 1**
| File | Change |
|---|---|
| `pkg/hub/proxyauth_cloudflare.go` | **new** — `CloudflareAccessAuthenticator` (~150 LoC incl. claims struct) |
| `pkg/hub/proxyauth_cloudflare_test.go` | **new** — see §9 |
| `pkg/hub/proxyauth.go` | add `NewProxyAuthenticator(mode, *config.ProxyAuthConfig)`; hoist shared consts (clock skew) |
| `pkg/config/hub_config.go` | `ProxyAuthConfig.Cloudflare`, new `CloudflareAccessAuthConfig` (`:313-331` vicinity) |
| `pkg/config/settings_v1.go` | `V1ProxyConfig.Cloudflare`, `V1CloudflareConfig`; conversion `:1554-1566` |
| `cmd/server_foreground.go` | replace both switches (`:1595-1614`, `:2127-2147`) with constructor calls |
| *(separable commit)* `pkg/hub/auth.go`, `pkg/hub/server.go` | remove legacy trusted-proxy path (`auth.go:276-288`, `:468-…`), `ServerConfig.TrustedProxies` (`server.go:114-115`, `:1373`), header stub |

**Phase 2**
| File | Change |
|---|---|
| `pkg/config/schemas/settings-v1.schema.json` | `serverAuth` gains `mode`, `proxy` (provider enum `iap|cloudflare`, `iap`, `cloudflare` objects) |
| `settings.yaml.example` | rewrite `:141-146` proxy block to real shape + cloudflare example |
| `docs/…` (site docs dir) | new page: Cloudflare Tunnel + Access setup |

**Phase 3**
| File | Change |
|---|---|
| `pkg/hub/admin_tunnel.go` (+`_test`) | **new** — status handler, detection, shadow-verify state |
| `pkg/hub/server.go` | route `/api/v1/admin/tunnel/status` (near `:3479`) |
| `pkg/hub/web.go` | shadow-mode hook when provider configured but mode != proxy (§5.1) |
| `web/src/components/pages/admin-server-config.ts` | tunnel section in `renderHubServerTab()` (`:3286`), states 1 & 3-RO; relabel "Public URL" → "Agent Callback URL" **and** replace its `https://hub.example.com` placeholder with an internal-looking one (`:3322`, `:3329-3333`, `:387` — §3.5 mitigation 2) |

**Phase 4**
| File | Change |
|---|---|
| `pkg/hub/maintenance_executors.go` | `TunnelInstallExecutor`, `TunnelUninstallExecutor`, `TunnelRestartExecutor` |
| `pkg/hub/admin_maintenance.go` | executor cases in `resolveMaintenanceExecutor` (`:252-309`); **redact secret params** in `executeOperation` (`:190-249`) |
| `scripts/starter-hub/gce-start-hub.sh` | install `scion-tunnel-helper` + sudoers stanza (pattern at `:337-357`) |
| `scripts/starter-hub/scion-tunnel-helper.sh` | **new** — wrapper (§5.6) |
| `web/src/components/pages/admin-server-config.ts` | token form, run-log streaming, guided enable flow, base-URL check/step (§3.5) |
| *(if Q6 = yes)* `pkg/config/settings_v1.go`, `hub_config.go`, schema, `cmd/server_foreground.go:2102-2108` | `server.base_url` settings key, lowest precedence below flag/env |

**Phase 5**
| File | Change |
|---|---|
| `pkg/apiclient/transport.go` | `WithExtraHeaders` option |
| CLI config plumbing (`pkg/config` client section + hub client init) | source the two CF headers |

## 8. Migration / rollout

Purely additive; no behavior change for any existing deployment:
- New provider is opt-in via `auth.proxy.provider: cloudflare`; `iap` configs are untouched
  (constructor produces identical `IAPAuthenticator`). No stored-data migration.
- The dead-code removal deletes only unreachable paths (no config key that anyone can be using
  — `trusted_proxies` never mapped to the live structs). Release-notes line regardless.
- Schema additions are backward-compatible (new optional properties); existing settings.yaml
  files validate as before. The UI tunnel section renders nothing but a doc link when
  `cloudflared` is absent, so non-Cloudflare hubs see one new collapsed section at most.
- Rollback = don't set the provider; the feature has no hooks outside proxy mode + admin UI.

## 9. Testing approach

**No live Cloudflare account needed for any CI test.** The pattern already exists in the IAP
tests: mint keys locally, serve JWKS from `httptest`, sign assertions in-test.

- **Unit — authenticator:** generate an RSA key; serve `{keys: [...]}` from `httptest.Server`;
  construct `CloudflareAccessAuthenticator{TeamDomain, Audience, JWKSURL: ts.URL, Issuer: …}`;
  table-test: valid token → correct `ProxyUserInfo` (email lowercased); no header → `(nil,nil)`;
  wrong aud / wrong iss / expired / future-iat / ES256-signed / garbage → errors; unknown kid →
  triggers on-miss refresh (serve rotated JWKS); **service-token shape** (`sub:""`,
  `common_name`) → distinct error. Clock-skew boundaries via injected `now` or ±29s/±31s tokens.
- **Unit — constructor:** mode/provider matrix incl. incomplete config (missing AUD, missing
  team domain) → errors naming the missing key; `iap` parity with the pre-refactor behavior.
- **Integration — middleware:** existing proxy-mode middleware tests
  (`handlers_auth_test.go` provisioning suite) re-run with the Cloudflare authenticator behind
  the same `httptest` JWKS: web session creation (`web.go:1449`), API request path
  (`auth.go:233`), invalid-assertion 401s, bearer-token-wins-over-assertion ordering.
- **Unit — detection/status:** fake `cloudflared` on `PATH` in a temp dir (shell stub printing
  a version); systemd probing behind a small `commandRunner` interface so tests inject outputs;
  status JSON contract tests.
- **Executor tests:** follow `maintenance_executors_test.go` — stub the helper binary, assert
  argv/stdin (token arrives on stdin, never argv), assert run-record redaction of `token`.
- **Shadow verification:** request with valid assertion while `mode=oauth` → status flips to
  verified, *no session created*; invalid assertion → `last_error` recorded; assertion on a
  **public route** (e.g. the login page) is still observed — the pre-auth/all-routes property
  the bootstrap ordering depends on (§5.1).
- **Manual/E2E checklist (once, with a real account):** full dashboard walkthrough per the docs
  page; SSE through the tunnel (dashboard live updates); WebSocket if used; UAT CLI call with
  service-token headers; token rotation; VM reboot (tunnel auto-starts, hub auto-starts,
  ordering-independent).

## 10. Risks & open questions

**Risks**
1. *Cloudflare contract drift* (header name, certs path, claim shapes) — mitigated by
   config-overridable issuer/JWKS and by the service-token test pinning today's shapes.
2. *Sudoers/wrapper on non-starter-hub installs*: UI-driven install (Phase 4) silently lacks
   privileges on hand-rolled hosts. Executor must detect missing helper and return actionable
   instructions (install helper + sudoers snippet printed in the run log).
3. *SSE through Cloudflare*: named tunnels support SSE (unlike quick tunnels) but Cloudflare
   applies ~100s idle-timeout behavior at the edge; the dashboard's SSE client must reconnect
   cleanly. Believed true already (same concern as Cloud Run in the Addendum); verify in E2E.
4. *Session-cache window*: an existing web session survives Access policy revocation until
   cookie expiry (`web.go:1467-1558` treats the session as a cache). Same posture as IAP today;
   document, don't fix here.
5. *Schema `additionalProperties: false`* interactions — adding `auth.proxy` must be checked
   against settings-DB seeded-section validation (postgres mode) as well as file mode.
6. *Stale BaseURL behind the tunnel* → session cookies issued without `Secure`
   (`web.go:484` derives it from the BaseURL prefix; there is no `X-Forwarded-Proto` handling
   in the session path — §3.5). Mitigated by the Phase 4 flip-blocker and the status-endpoint
   check; residual risk for people who wire the tunnel by hand and skip the docs.
7. *The "Public URL" trap* (§3.5): admins set `server.hub.public_url` to the tunnel hostname
   and silently break agent/CLI hub connectivity while fixing nothing web-side. Mitigations 1-2
   are in-scope (docs warning + relabel); the field predates this feature, so residual risk is
   pre-existing, not introduced.

**Open questions** (will be raised serially with ptone)
1. Dead-code removal in-scope as recommended (§3.2), or deferred to a separate cleanup PR?
2. Is the Phase 5 CLI passthrough wanted now, or is "remote CLI unsupported behind Access,
   use SSH/UAT-over-tunnel-with-service-token later" acceptable for v1?
3. Should the guided flow's auth fields live only in the Authentication tab (linked from the
   tunnel section), or duplicated inline as drafted (§3.3)?
4. Provider naming: `cloudflare` confirmed?
5. Does single-node-on-SQLite remain the only target for Phase 4 (file-mode settings), or must
   the guided flow also work on postgres/HA hubs (where settings live in DB sections and
   multiple replicas make a host-local tunnel nonsensical)? Drafted assumption: Phase 4 UI is
   hidden when `IsPostgres()`.
6. Should `server.base_url` become a real settings key (settable via the admin UI,
   restart-required, precedence below `--base-url`/`SCION_SERVER_BASE_URL`)? Today the web
   BaseURL is flag/env-only (`server_foreground.go:2102-2108`) while the superficially similar
   `server.hub.public_url` key configures the *agent-facing* endpoint instead
   (`settings_v1.go:1404-1405`) — and is rendered as a field labeled "Public URL" in the same
   admin pane, which is an active trap (§3.5). This is less a UX improvement than a
   correctness item: the two URL roles need to be visibly distinct. Note for scoping: the
   mislabel and the missing web key are a **pre-existing bug this feature exposes** — it bites
   any reverse-proxy deployment today — so it can be scoped independently of the tunnel work
   (mitigations 1-2 in §3.5 are in-scope here regardless of the answer). (Raised by
   s-node-strat in review; he has sent the same finding to ptone separately.)

## 11. Acceptance criteria

**Phase 1**: hub configured with `auth.mode: proxy`, `provider: cloudflare`, team domain + AUD
accepts a valid RS256 assertion signed by a test JWKS (browser session + API request), rejects
invalid/expired/wrong-aud/service-token assertions with 401 and no session; `provider: iap`
behaves byte-identically to before the refactor; `provider: header` is rejected at startup (or
absent entirely, per Q1); both init paths (hub + web) built from the single constructor —
verified by grep: exactly one `switch` over proxy providers in the tree.
**Phase 2**: a settings.yaml using the new keys passes schema validation; the example file's
proxy block round-trips through the loader; docs page walks a fresh Cloudflare account to a
working login and includes the lockout-recovery box.
**Phase 3**: status endpoint 403s non-admins; reports correct detection on a host with and
without `cloudflared`; shadow verification transitions on a valid assertion without creating a
session; UI section renders all three states; non-loopback bind shows the §5.2 warning; the
"Public URL" field is relabeled **and its placeholder changed** per §3.5 mitigation 2.
**Phase 4**: token paste → running systemd service, with the token absent from: run records,
GET responses, hub logs, and process argv (checked in executor test); uninstall/restart work;
missing-helper failure mode prints actionable instructions; mode-flip button provably gated on
shadow verification (UI test or handler test); flow warns when `server.hub.public_url` host
equals the tunnel hostname and blocks the flip when BaseURL is missing/non-`https` (§3.5).
**Phase 5**: CLI with the two CF headers configured reaches a hub whose Access app has a
Service Auth policy; UAT auth at the hub unchanged.

---
*Cross-references: `.design/auth-proxy-mode.md` (original proxy-mode design), single-node docs
cited in the header, `pkg/hub/proxyauth.go` for the extension point.*

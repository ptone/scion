# GCP Identity for Agents via Metadata Server Emulation

## Status: Approved

## Problem Statement

Scion agents running in containers may need to interact with Google Cloud APIs (GCS, BigQuery, Vertex AI, Cloud Build, etc.). Today, credentials are injected via environment variables or mounted credential files. This works for the LLM harness itself (e.g., `GOOGLE_APPLICATION_CREDENTIALS` for Vertex AI auth), but does not provide a general-purpose GCP identity to the agent's code execution environment. An agent writing a Cloud Function, querying BigQuery, or deploying infrastructure has no transparent way to authenticate.

We want agents to have seamless, transparent GCP identity so that any code using standard GCP client libraries (Go, Python, Node.js, Java) works without explicit credential management.

## Design Overview

Emulate the [GCE compute metadata server](https://cloud.google.com/compute/docs/metadata/overview) inside each agent container via a sciontool sidecar service. GCP client libraries follow a well-defined Application Default Credentials (ADC) discovery chain, and one of the standard steps is querying the metadata server at `169.254.169.254`. By intercepting this and serving tokens brokered through the Hub, we give agents a GCP identity that is:

- **Transparent**: No agent code changes required; standard client libraries just work.
- **Centrally managed**: Service account assignments are managed at the grove level via the Hub.
- **Auditable**: All token requests flow through the Hub, enabling logging and policy enforcement.
- **Secure**: No service account key files are distributed to agents or brokers.

### High-Level Flow

```
Agent Container                    Broker (sciontool)              Scion Hub                  GCP IAM
┌─────────────────┐               ┌──────────────────┐           ┌──────────────┐           ┌─────────┐
│ GCP client lib  │               │ metadata-svc     │           │              │           │         │
│ GET /token      │──────────────>│ (sidecar service) │           │              │           │         │
│                 │  HTTP to      │                  │           │              │           │         │
│                 │  GCE_METADATA │  validate request │           │              │           │         │
│                 │  _HOST        │  + agent identity │           │              │           │         │
│                 │               │                  │──────────>│ POST         │           │         │
│                 │               │                  │  SCION_   │ /agent/      │           │         │
│                 │               │                  │  AUTH_    │  gcp-token   │──────────>│ generate│
│                 │               │                  │  TOKEN    │              │  SA       │ access  │
│                 │               │                  │           │              │  imperson │ token   │
│                 │<──────────────│  return token     │<──────────│  return      │<──────────│         │
│ {access_token,  │               │                  │           │  {token,exp} │           │         │
│  expires_in}    │               │                  │           │              │           │         │
└─────────────────┘               └──────────────────┘           └──────────────┘           └─────────┘
```

### Metadata Interception Strategy

The primary mechanism uses the `GCE_METADATA_HOST` environment variable, which is supported by all major GCP client libraries (Go, Python, Java, Node.js). Setting `GCE_METADATA_HOST=localhost:18380` causes all metadata requests to go to `http://localhost:18380` instead of `169.254.169.254`.

For the Docker runtime, iptables rules will also be configured to redirect traffic destined for `169.254.169.254` to the local sidecar. This ensures that tools which hardcode the metadata IP (e.g., `curl`) are also intercepted, which is important for `block` mode security — without iptables, an agent could use `curl` to bypass the block and reach the host's real metadata server. The env var approach remains the fallback for runtimes where iptables configuration is difficult or impossible.

## Prior Art & Reference

### GCE Metadata Server

The [GCE compute metadata server](https://cloud.google.com/compute/docs/metadata/overview) provides instance and project metadata to VMs running on Google Compute Engine. Key characteristics:

- **Endpoint**: `http://169.254.169.254` or `http://metadata.google.internal`
- **Required header**: `Metadata-Flavor: Google` (prevents SSRF)
- **Token endpoint**: `GET /computeMetadata/v1/instance/service-accounts/{account}/token`
  - Returns: `{"access_token": "...", "expires_in": 3599, "token_type": "Bearer"}`
  - `{account}` is typically `default` or a service account email
- **Other key endpoints**:
  - `/computeMetadata/v1/project/project-id`
  - `/computeMetadata/v1/instance/service-accounts/` (list)
  - `/computeMetadata/v1/instance/service-accounts/{account}/email`
  - `/computeMetadata/v1/instance/service-accounts/{account}/scopes`
  - `/computeMetadata/v1/instance/service-accounts/{account}/identity` (OIDC)

### Client Library Discovery

GCP client libraries implement ADC with this precedence:

1. `GOOGLE_APPLICATION_CREDENTIALS` env var (explicit key file)
2. User credentials from `gcloud auth application-default login`
3. Attached service account via metadata server
4. (Workload Identity Federation, etc.)

### salrashid123/gce_metadata_server

[This project](https://github.com/salrashid123/gce_metadata_server) provides a Go implementation of a metadata server emulator that can:
- Serve access tokens from a service account key file
- Support service account impersonation
- Serve project/instance metadata from a config file
- Bind to a local port or Unix domain socket

This is a useful reference, though our implementation differs significantly because we broker tokens through the Hub rather than holding key files locally.

## Detailed Design

### 1. Service Account Registration (Hub Resource)

Service accounts are a new resource type. They can be scoped at the hub, grove, or user level, consistent with how secrets are scoped.

**Why a new resource type (not a secret)?**

A service account assignment is not a secret — it is a *mapping* of a GCP service account email to a scope, with associated metadata. No key material is stored. The Hub's own GCP identity is used to impersonate the service account at token-generation time.

#### Data Model

```go
// GCPServiceAccount represents a GCP service account registered for use by agents.
type GCPServiceAccount struct {
    ID               string    `json:"id"`                // UUID
    Scope            string    `json:"scope"`             // "hub", "grove", "user"
    ScopeID          string    `json:"scope_id"`          // ID of the hub/grove/user
    Email            string    `json:"email"`             // e.g. "agent-worker@project.iam.gserviceaccount.com"
    ProjectID        string    `json:"project_id"`        // GCP project containing the SA
    DisplayName      string    `json:"display_name"`      // Human-friendly label
    DefaultScopes    []string  `json:"default_scopes,omitempty"` // OAuth scopes (default: cloud-platform)
    Verified         bool      `json:"verified"`          // Hub confirmed it can impersonate this SA
    VerifiedAt       time.Time `json:"verified_at,omitempty"`
    CreatedBy        string    `json:"created_by"`        // User who registered it
    CreatedAt        time.Time `json:"created_at"`
}
```

#### Hub API Endpoints

```
POST   /api/v1/groves/{groveId}/gcp-service-accounts       # Register SA
GET    /api/v1/groves/{groveId}/gcp-service-accounts       # List SAs
GET    /api/v1/groves/{groveId}/gcp-service-accounts/{id}  # Get SA details
DELETE /api/v1/groves/{groveId}/gcp-service-accounts/{id}  # Remove SA
POST   /api/v1/groves/{groveId}/gcp-service-accounts/{id}/verify  # Verify impersonation
```

#### Verification Flow

On registration (or explicit verify), the Hub attempts to generate a test token for the service account using its own credentials:

```go
// Hub calls IAM credentials API to verify it can act as the SA
iamService.GenerateAccessToken(ctx, &credentialspb.GenerateAccessTokenRequest{
    Name:      "projects/-/serviceAccounts/" + sa.Email,
    Scope:     []string{"https://www.googleapis.com/auth/cloud-platform"},
    Lifetime:  durationpb.New(300 * time.Second),
})
```

If successful, the SA is marked `Verified=true`. This confirms the Hub's own service account has `roles/iam.serviceAccountTokenCreator` on the target SA.

#### Authorization Requirements

- **Registering a service account**: Requires grove admin role (or equivalent at the scoped level).
- **Assigning a service account to an agent**: Requires `assign` permission on the specific service account resource, in addition to agent creation permission. This prevents an agent creator from assigning arbitrary service accounts they don't have permission to use.
- **Using tokens at runtime**: The agent's JWT must include a scope `grove:gcp:token:<service-account-id>` identifying the specific service account (see Section 5).

### 2. Agent GCP Identity Assignment

When creating an agent, the user can optionally assign a GCP identity:

```json
{
  "name": "my-agent",
  "template": "default",
  "gcp_identity": {
    "service_account_id": "uuid-of-registered-sa",
    "metadata_mode": "assign"
  }
}
```

#### Metadata Modes

The `metadata_mode` field on agent creation controls how the metadata server behaves:

| Mode | Behavior | Use Case |
|------|----------|----------|
| `block` | Metadata sidecar returns 403 for all token and identity-token requests. Other metadata (project-id, zone) still served. All unmodified metadata items are passed through. | Prevent agents from inheriting broker/host GCP identity. Security-hardened agents. |
| `passthrough` | No metadata sidecar started. Metadata requests reach the real metadata server (if running on GCE) or fail naturally. | Local development, agents on GCE that should use the VM's identity. |
| `assign` | Metadata sidecar serves access tokens and identity tokens for the assigned service account via Hub brokering. All other metadata items not explicitly handled are passed through unchanged. | Production agents needing GCP API access. |

**Default**: `block` in hosted mode. In local/solo mode, metadata interception is disabled (equivalent to `passthrough`).

#### Modified vs. Pass-Through Metadata Items

The metadata sidecar explicitly handles the following endpoints:

| Endpoint Category | `assign` mode | `block` mode |
|---|---|---|
| `/instance/service-accounts/*/token` | Served via Hub brokering | 403 Forbidden |
| `/instance/service-accounts/*/identity` | Served via Hub brokering | 403 Forbidden |
| `/instance/service-accounts/*/email` | Returns assigned SA email | 403 Forbidden |
| `/instance/service-accounts/*/scopes` | Returns configured scopes | 403 Forbidden |
| `/instance/service-accounts/` (list) | Returns assigned SA listing | 403 Forbidden |
| All other metadata items | Passed through unchanged | Passed through unchanged |

#### Agent Model Extension

```go
// In the Agent or AgentConfig model:
type GCPIdentityConfig struct {
    MetadataMode       string `json:"metadata_mode"`                  // "block", "passthrough", "assign"
    ServiceAccountID   string `json:"service_account_id,omitempty"`   // FK to GCPServiceAccount (required for "assign")
    ServiceAccountEmail string `json:"service_account_email,omitempty"` // Denormalized for runtime use
    ProjectID          string `json:"project_id,omitempty"`            // Denormalized
}
```

### 3. Metadata Server Sidecar (sciontool)

The metadata emulator runs as part of the already-running sciontool process inside the agent container. Rather than launching a separate process, the metadata server is started as a goroutine within sciontool, gated by environment variables that configure its mode and service account details. This avoids the overhead of managing an additional process.

#### Configuration (Environment Variables)

When `metadata_mode` is `block` or `assign`, the provisioning pipeline sets the following environment variables for sciontool:

```
SCION_METADATA_MODE=assign
SCION_METADATA_PORT=18380
SCION_METADATA_SA_EMAIL=agent-worker@project.iam.gserviceaccount.com
SCION_METADATA_PROJECT_ID=my-project
```

On startup, sciontool checks for `SCION_METADATA_MODE`. If set to `assign` or `block`, it starts the metadata HTTP server on the configured port as part of its normal initialization.

#### Environment Variable Injection (Agent Container)

The provisioning pipeline also sets:

```
GCE_METADATA_HOST=localhost:18380
```

This redirects all GCP client library metadata lookups to the sidecar.

#### Endpoints Implemented

**Minimum viable set:**

| Endpoint | Response |
|----------|----------|
| `GET /` | `OK` (health) |
| `GET /computeMetadata/v1/` | Metadata root (recursive listing) |
| `GET /computeMetadata/v1/project/project-id` | Project ID string |
| `GET /computeMetadata/v1/project/numeric-project-id` | Numeric project ID (or empty) |
| `GET /computeMetadata/v1/instance/service-accounts/` | List: `default/\n{email}/\n` |
| `GET /computeMetadata/v1/instance/service-accounts/default/email` | SA email |
| `GET /computeMetadata/v1/instance/service-accounts/default/token` | Access token JSON |
| `GET /computeMetadata/v1/instance/service-accounts/default/scopes` | Scope list |
| `GET /computeMetadata/v1/instance/service-accounts/default/identity` | OIDC identity token |
| `GET /computeMetadata/v1/instance/service-accounts/{email}/token` | Access token JSON |
| `GET /computeMetadata/v1/instance/service-accounts/{email}/email` | SA email |
| `GET /computeMetadata/v1/instance/service-accounts/{email}/identity` | OIDC identity token |

**Access token response format** (matches GCE exactly):

```json
{
  "access_token": "ya29.c.ElpSB...",
  "expires_in": 3599,
  "token_type": "Bearer"
}
```

**Identity token response format:**

The `/identity` endpoint accepts an `audience` query parameter and returns a raw JWT string (matching GCE behavior).

**Header validation:**
- All requests must include `Metadata-Flavor: Google` header.
- Requests without this header receive `403 Forbidden` with body: `Missing Metadata-Flavor:Google header.`

#### Token Acquisition Flow (assign mode)

```
1. Client library requests GET /computeMetadata/v1/instance/service-accounts/default/token
2. Metadata sidecar validates Metadata-Flavor header
3. Sidecar checks token cache:
   a. If cached token exists and has >60s remaining → return cached token
   b. Otherwise → request fresh token from Hub
4. Sidecar calls Hub: POST /api/v1/agent/gcp-token
   - Authorization: Bearer <SCION_AUTH_TOKEN>
   - Body: {"scopes": ["https://www.googleapis.com/auth/cloud-platform"]}
5. Hub validates agent JWT, checks scope grove:gcp:token:<sa-id>
6. Hub resolves the service account from the agent's GCP identity assignment
7. Hub calls GCP IAM Credentials API (generateAccessToken) using its own identity
8. Hub returns token to sidecar
9. Sidecar caches token and returns to client
```

#### Token Caching & Proactive Refresh

The sidecar caches tokens locally to minimize Hub round-trips:

- Cache key: service account email + scopes (or audience for identity tokens)
- The first token request triggers a synchronous fetch from the Hub
- Once a token is cached, a background goroutine (using a Go timer) proactively refreshes it before expiry — when `expires_in` drops below 300 seconds, the sidecar pre-emptively fetches a new token from the Hub
- This ensures subsequent agent requests receive cached tokens with near-zero latency, avoiding synchronous Hub round-trips during active use
- Concurrent requests for the same token coalesce (singleflight pattern)

#### Block Mode Behavior

In `block` mode, the sidecar:
- Returns `403` for all `/token` and `/identity` endpoints
- Returns `403` for `/email`, `/scopes`, and service account listing endpoints
- Passes through all other metadata items (project-id, zone, etc.) unchanged
- Logs blocked requests for observability

### 4. Hub Token Brokering Endpoint

#### New Hub Endpoints

```
POST /api/v1/agent/gcp-token
POST /api/v1/agent/gcp-identity-token
```

**Authentication**: Agent JWT (Bearer token with `grove:gcp:token:<sa-id>` scope)

**Access Token Request (`/gcp-token`):**
```json
{
  "scopes": ["https://www.googleapis.com/auth/cloud-platform"]
}
```

Note: The service account email is **not** in the request body. The Hub resolves it from the agent's GCP identity assignment. This prevents an agent from requesting tokens for arbitrary service accounts.

**Identity Token Request (`/gcp-identity-token`):**
```json
{
  "audience": "https://my-cloud-run-service.run.app"
}
```

**Response (200) — Access Token:**
```json
{
  "access_token": "ya29.c.ElpSB...",
  "expires_in": 3599,
  "token_type": "Bearer"
}
```

**Response (200) — Identity Token:**
```json
{
  "token": "eyJhbGciOiJSUzI1NiIs..."
}
```

**Error Responses:**
- `401`: Invalid or expired agent token
- `403`: Agent does not have `grove:gcp:token:<sa-id>` scope, or no GCP identity assigned
- `502`: Hub failed to generate token from GCP (impersonation failed)
- `503`: GCP IAM service unavailable

#### Hub Implementation

```go
func (s *Server) handleAgentGCPToken(w http.ResponseWriter, r *http.Request) {
    // 1. Extract agent identity from context (set by auth middleware)
    agent := GetAgentIdentityFromContext(r.Context())
    if agent == nil {
        http.Error(w, "forbidden", http.StatusForbidden)
        return
    }

    // 2. Look up agent's GCP identity assignment
    agentRecord, _ := s.store.GetAgent(r.Context(), agent.ID())
    if agentRecord.GCPIdentity == nil || agentRecord.GCPIdentity.MetadataMode != "assign" {
        http.Error(w, "no GCP identity assigned", http.StatusForbidden)
        return
    }

    // 3. Verify the agent's JWT scope matches the assigned SA
    requiredScope := fmt.Sprintf("grove:gcp:token:%s", agentRecord.GCPIdentity.ServiceAccountID)
    if !agent.HasScope(requiredScope) {
        http.Error(w, "forbidden", http.StatusForbidden)
        return
    }

    // 4. Parse requested scopes (or default)
    var req gcpTokenRequest
    json.NewDecoder(r.Body).Decode(&req)
    scopes := req.Scopes
    if len(scopes) == 0 {
        scopes = []string{"https://www.googleapis.com/auth/cloud-platform"}
    }

    // 5. Generate access token via IAM Credentials API
    token, err := s.gcpTokenGenerator.GenerateAccessToken(r.Context(),
        agentRecord.GCPIdentity.ServiceAccountEmail, scopes)
    if err != nil {
        http.Error(w, "token generation failed", http.StatusBadGateway)
        return
    }

    // 6. Log token generation event
    s.auditLog.LogGCPTokenGeneration(r.Context(), agent.ID(), agentRecord.GroveID,
        agentRecord.GCPIdentity.ServiceAccountEmail)

    // 7. Return token
    json.NewEncoder(w).Encode(token)
}
```

#### GCP Token Generator

```go
type GCPTokenGenerator interface {
    GenerateAccessToken(ctx context.Context, serviceAccountEmail string, scopes []string) (*GCPAccessToken, error)
    GenerateIDToken(ctx context.Context, serviceAccountEmail string, audience string) (*GCPIDToken, error)
    VerifyImpersonation(ctx context.Context, serviceAccountEmail string) error
}

type GCPAccessToken struct {
    AccessToken string `json:"access_token"`
    ExpiresIn   int    `json:"expires_in"`
    TokenType   string `json:"token_type"`
}

type GCPIDToken struct {
    Token string `json:"token"`
}
```

The default implementation uses the [IAM Credentials API](https://cloud.google.com/iam/docs/reference/credentials/rest/v1/projects.serviceAccounts/generateAccessToken):

```go
type iamTokenGenerator struct {
    client *credentials.IamCredentialsClient
}

func (g *iamTokenGenerator) GenerateAccessToken(ctx context.Context, email string, scopes []string) (*GCPAccessToken, error) {
    resp, err := g.client.GenerateAccessToken(ctx, &credentialspb.GenerateAccessTokenRequest{
        Name:     fmt.Sprintf("projects/-/serviceAccounts/%s", email),
        Scope:    scopes,
        Lifetime: durationpb.New(3600 * time.Second),
    })
    if err != nil {
        return nil, fmt.Errorf("IAM generateAccessToken failed: %w", err)
    }
    return &GCPAccessToken{
        AccessToken: resp.AccessToken,
        ExpiresIn:   int(time.Until(resp.ExpireTime.AsTime()).Seconds()),
        TokenType:   "Bearer",
    }, nil
}

func (g *iamTokenGenerator) GenerateIDToken(ctx context.Context, email string, audience string) (*GCPIDToken, error) {
    resp, err := g.client.GenerateIdToken(ctx, &credentialspb.GenerateIdTokenRequest{
        Name:     fmt.Sprintf("projects/-/serviceAccounts/%s", email),
        Audience: audience,
    })
    if err != nil {
        return nil, fmt.Errorf("IAM generateIdToken failed: %w", err)
    }
    return &GCPIDToken{Token: resp.Token}, nil
}
```

### 5. New Agent Token Scope

Add a new scope pattern to `AgentTokenScope`:

```go
const (
    // existing scopes...
    // AgentTokenScopeGCPToken is a prefix — the full scope is "grove:gcp:token:<sa-id>"
    AgentTokenScopeGCPTokenPrefix = "grove:gcp:token:"
)
```

The scope is parameterized with the specific service account ID (e.g., `grove:gcp:token:uuid-of-sa`). This ensures an agent can only request tokens for its specifically assigned service account, not any arbitrary service account registered to the grove.

This scope is automatically added to the agent's JWT when provisioned with `metadata_mode: assign`.

### 6. Provisioning Pipeline Changes

During agent provisioning (in `pkg/agent/provision.go` and `pkg/runtimebroker/start_context.go`):

1. **Resolve GCP identity**: If the agent has a GCP identity assignment, fetch the service account details.
2. **Configure metadata server**: Set `SCION_METADATA_MODE`, `SCION_METADATA_PORT`, `SCION_METADATA_SA_EMAIL`, and `SCION_METADATA_PROJECT_ID` environment variables for sciontool.
3. **Set agent environment**: Add `GCE_METADATA_HOST=localhost:18380` to the agent's environment.
4. **Configure iptables (Docker runtime)**: When using Docker, add iptables rules to redirect `169.254.169.254` traffic to the local metadata sidecar port.
5. **Suppress conflicting auth**: When metadata mode is `assign` or `block`, ensure `GOOGLE_APPLICATION_CREDENTIALS` is **not** set (it takes higher precedence in ADC than the metadata server). If a user has explicitly set GAC via secrets, that should still win — the metadata server acts as a fallback.
6. **Add JWT scope**: Include `grove:gcp:token:<sa-id>` in the agent's JWT scopes.

## Alternatives Considered

### A. Mount Service Account Key Files Directly

**Approach**: Store SA key files as grove secrets, mount into containers as files.

**Pros**: Simple; no metadata server needed; works offline.

**Cons**:
- Key files are long-lived credentials that can be exfiltrated by malicious agents.
- No centralized revocation — revoking requires rotating the key in GCP and updating all secrets.
- Violates Google's recommendation to avoid downloaded key files.
- No audit trail of token usage through the Hub.

**Verdict**: Rejected for production use. May be acceptable as a degenerate "bring your own key" fallback for local/solo mode.

### B. Direct Impersonation (No Metadata Server)

**Approach**: Use `GOOGLE_APPLICATION_CREDENTIALS` with a workload identity federation config that points to the Hub as a token source.

**Pros**: No metadata server sidecar; uses GCP's native WIF mechanism.

**Cons**:
- Requires generating and mounting a WIF credential config file per agent.
- WIF config format is complex and brittle.
- Not all client libraries support all WIF source types uniformly.
- The metadata server approach is more universally compatible.

**Verdict**: Rejected. Metadata server emulation is more robust and universally supported.

### C. Network-Level Interception (iptables redirect)

**Approach**: Use iptables/nftables to redirect traffic destined for `169.254.169.254` to the local sidecar, instead of setting `GCE_METADATA_HOST`.

**Pros**: Transparent even to tools that don't respect `GCE_METADATA_HOST`; matches real GCE behavior exactly.

**Cons**:
- Requires `NET_ADMIN` capability in the container (security concern).
- More complex to set up and debug.
- May conflict with container networking (especially on Kubernetes where a real metadata server exists).

**Verdict**: Medium priority. Will be implemented for the Docker runtime as a complement to `GCE_METADATA_HOST`. Without iptables interception, an agent in `block` mode could use `curl` directly against `169.254.169.254` to bypass the block and gain access to the host's service account identity. The env var approach remains the primary mechanism and sole mechanism for runtimes where iptables is impractical (e.g., Kubernetes).

### D. Hub-Side Token Caching

**Approach**: Cache tokens at the Hub level, returning cached tokens for repeat requests.

**Pros**: Reduces IAM API calls across all agents sharing the same SA.

**Cons**:
- Tokens are bearer tokens — caching at the Hub means a Hub compromise exposes all cached tokens.
- GCP access tokens are already valid for ~1 hour; sidecar-level caching is sufficient.
- Adds complexity to the Hub.

**Verdict**: Defer. Sidecar-level caching with proactive refresh is adequate. Hub-level caching can be added later if IAM API rate limits become a concern.

## Resolved Questions

### Q1: Default metadata mode — `block`

Default to `block` in hosted mode. In local/solo mode, metadata interception is disabled (passthrough behavior).

### Q2: Required role for SA registration — Grove admin

Grove admins can manage service accounts, consistent with their existing ability to manage grove configuration.

### Q3: Per-request scope restrictions — Not supported

Start with `cloud-platform` as the only supported scope. Fine-grained access control is handled via IAM roles on the service account itself, not via OAuth scope restrictions at the metadata layer.

### Q4: OIDC identity tokens — In scope (Phase 1)

Identity token support via the `/identity` endpoint and `generateIdToken` IAM API is included in the MVP. This covers Cloud Run service-to-service auth and other OIDC flows.

### Q5: Multiple service accounts per agent — Single

Start with one SA per agent (served as both `default` and by email). Multiple SAs can be added later if needed.

### Q6: Token lifetime and refresh strategy

3600s lifetime (GCP default). The sidecar uses proactive background refresh: after the first synchronous fetch, a Go timer pre-emptively requests a new token from the Hub before the current one expires (~300s before expiry). This prevents agent code from triggering synchronous Hub round-trips during active use.

### Q7: Interaction with harness auth

If an agent has both a Gemini API key (for the LLM) and a GCP identity (for API access), they coexist naturally. If harness auth uses `vertex-ai` and relies on ADC, the metadata server identity is used — the assigned SA needs Vertex AI permissions. No code changes needed; ADC precedence handles it.

### Q8: Audit logging

Log token generation events (not sidecar cache hits) with agent ID, grove ID, SA email, and timestamp. This provides an audit trail without excessive volume.

## Implementation Sketch

### Phase 1: Foundation (MVP) ✅ Complete

**Goal**: End-to-end token flow for a single assigned SA, including both access tokens and identity tokens.

1. ✅ **Store layer**: Add `GCPServiceAccount` model and store interface methods.
2. ✅ **Hub endpoints**: CRUD for grove service accounts + verify endpoint.
3. ✅ **Hub token endpoints**: `POST /api/v1/agent/gcp-token` and `POST /api/v1/agent/gcp-identity-token` with IAM Credentials integration.
4. ✅ **Agent token scope**: Add `grove:gcp:token:<sa-id>` scoped scope.
5. ✅ **sciontool metadata server**: In-process HTTP server within sciontool implementing token, identity token, and project-id endpoints, gated by `SCION_METADATA_MODE` env var.
6. ✅ **Provisioning changes**: Set metadata server env vars and `GCE_METADATA_HOST`.
7. ✅ **Agent model extension**: Add `GCPIdentityConfig` to agent creation/config.
8. **SA assignment permission**: Deferred to Phase 2 — permission check on service account resources.

**Files to create/modify**:

| File | Change |
|------|--------|
| `pkg/store/models.go` | Add `GCPServiceAccount` model |
| `pkg/store/store.go` | Add `GCPServiceAccountStore` interface |
| `pkg/store/sqlite.go` | Implement store (if using SQLite) |
| `pkg/hub/handlers_gcp_identity.go` | New: SA CRUD + verify handlers |
| `pkg/hub/gcp_token.go` | New: Token generation (access + ID) + Hub endpoint handlers |
| `pkg/hub/server.go` | Register new routes |
| `pkg/hub/agenttoken.go` | Add `grove:gcp:token:<sa-id>` scope |
| `pkg/sciontool/metadata/server.go` | New: Metadata HTTP server (in-process) |
| `pkg/agent/provision.go` | Set metadata env vars |
| `pkg/runtimebroker/start_context.go` | Pass GCP identity config to provisioning |
| `pkg/api/types.go` | Add `GCPIdentityConfig` to relevant types |

### Phase 2: Hardening ✅ Complete

1. ✅ **Block mode**: Implemented in Phase 1 — metadata sidecar returns 403 for all token/identity/email/scopes requests in block mode.
2. ✅ **iptables interception**: Docker runtime adds `NET_ADMIN` capability; metadata server sets up iptables DNAT rule redirecting `169.254.169.254:80` to local sidecar port. Non-fatal fallback if iptables unavailable.
3. ✅ **Audit logging**: GCP token generation events logged via `AuditLogger` with agent ID, grove ID, SA email, success/failure status.
4. ✅ **CLI commands**: `scion grove service-accounts add/list/remove/verify` with hub client integration.
5. **Web UI**: Deferred — SA management and agent identity assignment via web interface.
6. ✅ **Rate limiting**: Per-agent token bucket rate limiter (1 req/sec avg, burst of 10) on Hub GCP token endpoints.
7. ✅ **Metrics**: `GCPTokenMetrics` tracking access/identity token requests, successes, failures, rate limit rejections, and IAM API latency percentiles. Exposed via `/metrics` endpoint.

**Files created/modified**:

| File | Change |
|------|--------|
| `pkg/hub/gcp_ratelimit.go` | New: Per-agent token bucket rate limiter |
| `pkg/hub/gcp_metrics.go` | New: GCP token metrics (counters + latency percentiles) |
| `pkg/hub/audit.go` | Extended: GCP token event types and audit logging |
| `pkg/hub/handlers_gcp_identity.go` | Modified: Rate limiting, metrics, audit logging integration |
| `pkg/hub/server.go` | Modified: Initialize rate limiter and metrics |
| `pkg/hub/handlers.go` | Modified: Include GCP metrics in `/metrics` response |
| `pkg/sciontool/metadata/iptables.go` | New: iptables redirect setup/cleanup |
| `pkg/sciontool/metadata/server.go` | Modified: iptables setup on Start, cleanup on Stop |
| `pkg/runtime/interface.go` | Modified: `MetadataInterception` field on `RunConfig` |
| `pkg/runtime/common.go` | Modified: `--cap-add NET_ADMIN` when metadata interception enabled |
| `pkg/agent/run.go` | Modified: Detect metadata mode and set `MetadataInterception` |
| `cmd/grove_service_accounts.go` | New: CLI commands for SA management |
| `pkg/hubclient/gcp_service_accounts.go` | New: Hub client for GCP SA CRUD + verify |
| `pkg/hubclient/client.go` | Modified: Add `GCPServiceAccounts()` to Client interface |

### Phase 3: Extensions

- Multiple service accounts per agent
- Scope restrictions per SA registration
- Hub-level token caching (if IAM API rate limits are hit)
- Support for Workload Identity Federation as an alternative backend
- iptables-based interception for non-Docker runtimes

## Security Considerations

### Threat Model

1. **Malicious agent requests token for wrong SA**: Prevented — Hub resolves SA from agent's assignment, not from request body. Agent JWT scope is parameterized to the specific SA ID.
2. **Agent exfiltrates access token**: Mitigated — tokens are short-lived (1 hour). No long-lived key material in the container.
3. **Agent bypasses metadata sidecar**: In `block` mode with `GCE_METADATA_HOST` only, if the env var is unset/overridden, requests go to `169.254.169.254`. On non-GCE hosts this fails naturally. On GCE, the agent would get the broker's identity. The iptables variant (Phase 2, Docker runtime) closes this gap by intercepting at the network level.
4. **Hub compromise exposes IAM credentials**: The Hub uses its own managed identity (GCE SA or Workload Identity) — no key files stored. Compromise of the Hub process allows token generation, but revoking the Hub's `serviceAccountTokenCreator` role immediately cuts off all agent tokens.
5. **Broker compromise**: Broker never holds SA credentials. It only holds the agent JWT, which is scoped and short-lived.
6. **Agent requests token for SA in another grove**: Prevented — JWT scope is bound to specific SA ID, and Hub verifies the SA is assigned to the requesting agent.

### Principle of Least Privilege

- Agents only get tokens for their assigned SA (enforced by parameterized JWT scope).
- The Hub's own SA only needs `roles/iam.serviceAccountTokenCreator` on target SAs — not broad IAM permissions.
- Agent JWTs require explicit `grove:gcp:token:<sa-id>` scope.
- Default metadata mode is `block`, requiring explicit opt-in.
- SA assignment requires explicit permission on the service account resource.

## Dependencies

- **GCP IAM Credentials API**: `google.golang.org/api/iamcredentials/v1` or the gRPC client `cloud.google.com/go/iam/credentials/apiv1`
- **Hub must run with a GCP identity** that has `roles/iam.serviceAccountTokenCreator` on target SAs.
- **Existing scion infrastructure**: ServiceManager (sidecar services), agent token scopes, Hub auth middleware, store layer.

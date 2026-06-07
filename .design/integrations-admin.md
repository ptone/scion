# Integrations Architecture & Admin

**Status:** Draft  
**Date:** 2026-06-07  
**Issue:** #115  
**Branch:** scion/integrationsadmin  
**Related:** [scion-plugins.md](scion-plugins.md), [message-broker-plugins.md](message-broker-plugins.md), [postgres-strategy.md](postgres-strategy.md), [hosted/web-realtime.md](hosted/web-realtime.md), [hosted/hosted-architecture.md](hosted/hosted-architecture.md), [workstation-onboarding.md](workstation-onboarding.md) (on `workstation-improvements` branch)

---

## 1. Problem

Scion's integrations (Telegram, Google Chat, GitHub App, notification channels) have two distinct problems:

### 1.1 Operational Pain (Admin)

Configuring integrations today requires editing config files on disk, building/installing separate binaries, patching Caddy routes, and restarting services in the correct order — with race conditions between hub and self-managed plugins. There is no unified admin experience.

### 1.2 Architectural Coupling (HA)

Integrations are tightly coupled to a single hub instance:

- **Managed plugins** (Telegram) run as hub subprocesses — they die and restart with the hub. In a multi-hub HA setup, each hub instance would spawn its own Telegram plugin, causing duplicate webhook registrations, duplicate message delivery, and conflicting state.
- **Self-managed plugins** (Google Chat) connect to a specific hub's RPC listener on localhost. They cannot failover to another hub instance.
- **Configuration** lives in `settings.yaml` on each hub's local filesystem, not in shared state.

As Scion moves toward multi-node HA with Postgres (shared database, LISTEN/NOTIFY for event coordination), integrations must be decoupled from individual hub instances and treated as independent services that coordinate through shared state.

---

## 2. Goals

- Integrations run as **independent services** that connect to the hub API (not RPC to a specific hub process), supporting both co-located single-node and distributed multi-node deployments.
- **Co-located mode** remains the default: on a single hub or workstation, integrations can still run as subprocesses for simplicity — no separate systemd units required for simple setups.
- Integration **configuration is stored in the database** (not just settings.yaml), enabling shared state across hub instances and API-driven management.
- Administrators can manage all integrations from a unified `/admin/integrations` web page.
- The hub orchestrates restart sequencing and health verification, eliminating race conditions.
- Setup wizards guide administrators through external prerequisites.
- Integration cards are reusable in onboarding flows.

## 3. Non-Goals

- Third-party/community plugin admin UI (future — extend via schema-driven forms when needed).
- Automating external service creation (BotFather, GitHub App) — manual steps required.
- Full Kubernetes operator for integration deployment (defer to K8s milestone).

---

## 4. Integration Architecture

### 4.1 Deployment Modes

Scion defines four canonical deployment modes. Integrations must work across all of them. The modes differ in compute substrate, persistence, and how services are exposed — but the integration code is the same, only the transport and infrastructure layer changes.

#### Glossary of Deployment Modes

| Mode | Substrate | Persistence | Reverse Proxy | Integration Model | When to Use |
|------|-----------|-------------|---------------|-------------------|-------------|
| **Workstation** | Local machine (macOS/Linux) | SQLite | None | Co-located subprocess | Local development, personal use |
| **Single VM** | GCE VM (or similar) | SQLite or Postgres | Caddy + systemd | Co-located or independent services | Small team, current production model |
| **Cloud Solo** | Cloud Run (single instance) | SQLite (persistent volume) | Cloud Run ingress | Co-located subprocess | Cloud-deployed dev/personal server, single user |
| **HA Cluster** | VM instance group, Cloud Run service, or K8s | Postgres (shared) | LB + Caddy/Ingress | Independent services | Production, multi-user, high availability |

#### Mode Details

```
WORKSTATION
───────────
┌─────────────────────────────────────┐
│         scion server                │
│  ┌─────┐ ┌─────────┐ ┌──────────┐ │
│  │ Hub │ │ Broker  │ │  Web UI  │ │
│  └──┬──┘ └─────────┘ └──────────┘ │
│     │                               │
│  ┌──┴──────────────────────────┐   │
│  │ Telegram Plugin (subprocess)│   │
│  └─────────────────────────────┘   │
│  SQLite │ No Caddy │ No systemd    │
└─────────────────────────────────────┘

- Single binary, everything in-process or subprocess
- Telegram plugin as managed subprocess (polling mode, no public URL)
- SQLite database, local filesystem
- Simplest possible setup


SINGLE VM
─────────
┌──────────────────────────────────────────────┐
│                  GCE VM                       │
│                                               │
│  ┌────────────┐        ┌───────────────────┐ │
│  │  Caddy     │───────►│  Hub (scion-hub)  │ │
│  │  :443      │        │  :8080            │ │
│  └──────┬─────┘        └────────┬──────────┘ │
│         │                       │             │
│         │              ┌────────┴──────────┐ │
│         │              │  Plugin Manager   │ │
│         │              │  (go-plugin RPC)  │ │
│         │              └────────┬──────────┘ │
│         │                       │             │
│    ┌────┴────┐          ┌───────┴─────────┐  │
│    │Telegram │          │  Google Chat    │  │
│    │webhook  │          │  (self-managed) │  │
│    │:9094    │          │  :8443 + :9090  │  │
│    └─────────┘          └─────────────────┘  │
│                                               │
│  SQLite or Postgres │ Caddy │ systemd         │
└──────────────────────────────────────────────┘

- Hub manages Telegram as subprocess or independent systemd service
- Chat-app as separate systemd service
- Both connect via localhost RPC (co-located) or hub API
- Caddy handles TLS termination and webhook routing


CLOUD SOLO
──────────
┌─────────────────────────────────────────────┐
│            Cloud Run Service                 │
│                                              │
│  ┌─────────────────────────────────────┐    │
│  │  Hub (single container instance)    │    │
│  │  :8080                              │    │
│  │  ┌──────────────────────────────┐   │    │
│  │  │ Telegram Plugin (subprocess) │   │    │
│  │  └──────────────────────────────┘   │    │
│  └─────────────────────────────────────┘    │
│                                              │
│  Cloud Run ingress (TLS, routing)            │
│  SQLite (persistent volume)                  │
│  No Caddy │ No systemd                       │
└─────────────────────────────────────────────┘

- Workstation mode deployed to Cloud Run
- Single container instance, single binary (same as workstation)
- SQLite on a persistent volume (Cloud Run volume mount)
- Telegram as managed subprocess, polling mode (same as workstation)
- Cloud Run behind IAP — all requests require authentication,
  so external webhooks (Telegram, GitHub) cannot reach the service.
  Integrations must use long-polling or outbound connections only.
- Cloud Run handles TLS/ingress (no Caddy)
- No systemd — process lifecycle managed by Cloud Run
- Config in settings.yaml (baked into image or mounted)


HA CLUSTER
──────────
┌──────────────┐  ┌──────────────┐
│   Hub Node 1 │  │   Hub Node 2 │   (behind LB)
│   :8080      │  │   :8080      │
└──────┬───────┘  └──────┬───────┘
       │                  │
       └────────┬─────────┘
                │
       ┌────────┴────────┐
       │    Postgres      │   (shared state)
       │  LISTEN/NOTIFY   │   (event coordination)
       └────────┬────────┘
                │
  ┌─────────────┼─────────────┐
  │             │             │
┌─┴──────┐ ┌───┴────┐ ┌──────┴──────┐
│Telegram│ │ Google  │ │ Notification│
│Service │ │ Chat   │ │ Service     │
│        │ │ Service│ │             │
└────────┘ └────────┘ └─────────────┘

Substrate options:
  - VM instance group (GCE MIG + Caddy per node)
  - Cloud Run service (multiple instances, auto-scaling)
  - Kubernetes (Deployments + Services + Ingress)

- Hub nodes are stateless (shared Postgres)
- Integrations are independent services (own deployment unit)
- Connect to hub via API (any node, through LB)
- Coordinate via Postgres (config, state, advisory locks)
- No RPC coupling to specific hub instance
- Single instance of each integration (advisory lock guarantee)
```

#### Capability Matrix

| Capability | Workstation | Single VM | Cloud Solo | HA Cluster |
|-----------|-------------|-----------|------------|------------|
| Managed plugin (subprocess) | Yes | Yes | Yes | No (use independent) |
| Self-managed plugin (localhost RPC) | No | Yes | No | No |
| Independent service (API transport) | No | Optional | No | Required |
| Caddy reverse proxy | No | Yes | No (Cloud Run ingress) | Depends on substrate |
| systemd units | No | Yes | No | Depends on substrate |
| SQLite | Yes | Yes | Yes | No |
| Postgres | No | Optional | No | Required |
| Advisory locks (single-instance) | N/A | N/A | N/A | Required |
| Webhook (public URL) | No (polling) | Yes | No (IAP, polling) | Yes |
| Config in settings.yaml | Yes | Yes | Yes | Bootstrap only |
| Config in database | Optional | Optional | Optional | Required |

### 4.2 Integration Lifecycle Journey (Telegram Example)

The design must support a user evolving through deployment modes without re-architecting their integrations. Here's how Telegram works at each stage and what changes at each transition.

#### Day 1: Workstation

- User creates bot in BotFather, gets token
- Configures via admin UI (or settings.yaml): `bot_token`, `inbound_mode: polling`
- Hub starts, plugin manager launches `scion-plugin-telegram` as subprocess
- Plugin polls Telegram `getUpdates` API (outbound, no public URL needed)
- User adds bot to a Telegram group, runs `/setup` to link to a project
- **State:** settings.yaml (config), telegram_v2.db (group links, user mappings), hub SQLite (agents, projects)

#### Transition: Workstation → Cloud Solo

**What the user does:**
1. Builds container image including scion + scion-plugin-telegram binaries
2. Includes settings.yaml (baked or Secret Manager)
3. Deploys to Cloud Run with persistent volume for SQLite DBs

**What changes:** Nothing architecturally. Same subprocess, same polling, same SQLite, same settings.yaml. The persistent volume carries both hub DB and telegram_v2.db.

**What stays the same:** Transport (RPC), polling mode (IAP blocks webhooks), config format, all state.

**Migration effort:** Near zero — deploy and go.

#### Transition: Cloud Solo → Single VM

**What the user does:**
1. Provisions a VM, runs starter-hub setup
2. Copies settings.yaml config and telegram_v2.db
3. Hub discovers and launches Telegram as managed subprocess (or independent systemd service)
4. Switches from polling to webhook mode (VM has public URL via Caddy)
5. Registers webhook URL with Telegram API

**What changes:** Inbound mode (polling → webhook), Caddy route added, optionally independent systemd service.

**What stays the same:** Bot token, group links (telegram_v2.db), transport can stay RPC or switch to API.

#### Transition: Single VM → HA Cluster

**What the user does:**
1. Sets up Postgres, migrates hub DB (per postgres-strategy.md)
2. Deploys Telegram as independent service with APITransport
3. Migrates telegram_v2.db to the independent service's persistent storage
4. Configures integration service token for hub API auth
5. Advisory lock ensures single Telegram instance across the cluster

**What changes:** Transport (RPC → API), process model (subprocess → independent service), hub config moves to DB, health reporting via heartbeat API.

**What stays the same:** Bot token, telegram_v2.db schema (stays as the plugin's own SQLite — not migrated to hub DB), webhook mode, Telegram API interactions.

**Note:** telegram_v2.db remains a separate SQLite database owned by the Telegram plugin, even in HA mode. The plugin carries its own state. Only hub-level integration config (enabled, transport settings) moves to Postgres. This keeps the plugin self-contained and avoids coupling plugin internals to the hub's database schema.

#### Key Design Principle

Each transition should require changing **at most two things**: the transport mode and the inbound mode. Config format, plugin state, and bot identity stay the same across all modes.

### 4.3 Integration Communication Model

The key architectural change: integrations communicate with the hub through the **Hub API** rather than go-plugin RPC. This decouples them from specific hub instances.

#### Current Model (RPC-Coupled)

```
Integration ←──go-plugin RPC──→ Hub Process (specific instance)
  - Publish(): RPC call to hub
  - Subscribe(): RPC call from hub
  - Inbound: POST /api/v1/broker/inbound (already uses API)
  - Config: map[string]string via Configure() RPC
```

#### Target Model (API-Driven)

```
Integration ←──Hub API (any instance)──→ Hub (behind LB)
  - Publish(): POST /api/v1/broker/publish  (new)
  - Subscribe(): Postgres LISTEN or polling  (new)
  - Inbound: POST /api/v1/broker/inbound    (existing)
  - Config: GET /api/v1/integrations/{type}/config  (new, from DB)
  - Health: POST /api/v1/integrations/{type}/heartbeat  (new)
```

#### Compatibility Layer

To support both topologies without rewriting every integration, introduce an `IntegrationTransport` abstraction:

```go
// IntegrationTransport abstracts how an integration communicates with the hub.
// Implementations exist for co-located (RPC) and distributed (API) modes.
type IntegrationTransport interface {
    // PublishMessage sends an inbound message to the hub for routing.
    PublishMessage(ctx context.Context, topic string, msg *messages.StructuredMessage) error

    // SubscribeMessages registers interest in outbound messages.
    // The handler is called for each matching message.
    SubscribeMessages(pattern string, handler MessageHandler) (Subscription, error)

    // FetchConfig retrieves the integration's current configuration.
    FetchConfig(ctx context.Context) (map[string]string, error)

    // ReportHealth sends a health status update to the hub.
    ReportHealth(ctx context.Context, status *HealthStatus) error

    // Close cleanly disconnects.
    Close() error
}

// RPCTransport implements IntegrationTransport using go-plugin RPC.
// Used in co-located mode (Topology 1 & 2).
type RPCTransport struct { /* wraps existing go-plugin client */ }

// APITransport implements IntegrationTransport using Hub HTTP API.
// Used in distributed mode (Topology 3).
type APITransport struct {
    hubURL    string  // hub API endpoint (or LB address)
    authToken string  // integration service token
    client    *http.Client
}

// PostgresTransport extends APITransport with Postgres LISTEN/NOTIFY
// for efficient message subscription without polling.
type PostgresTransport struct {
    APITransport
    pgPool *pgxpool.Pool  // direct Postgres connection for LISTEN
}
```

Integration binaries select transport based on deployment mode:

```yaml
# Workstation / Cloud Solo / Single VM co-located (default, backward compatible)
transport: rpc

# HA Cluster — independent service via Hub API
transport: api
hub_url: https://hub.example.com
auth_token: scion_integ_xxx

# HA Cluster with Postgres subscription (efficient, no polling)
transport: postgres
hub_url: https://hub.example.com
auth_token: scion_integ_xxx
postgres_url: postgres://user:pass@db:5432/scion
```

| Transport | Deployment Modes | Characteristics |
|-----------|-----------------|-----------------|
| `rpc` | Workstation, Cloud Solo, Single VM | go-plugin RPC, subprocess or localhost, lowest latency |
| `api` | Single VM (optional), HA Cluster | HTTP to hub API (through LB), works anywhere, stateless |
| `postgres` | HA Cluster | API + Postgres LISTEN for efficient message subscription |

### 4.3 Configuration Storage

Move integration configuration from `settings.yaml` files to the database, while keeping settings.yaml as a bootstrap/override mechanism.

#### Database Schema

```sql
CREATE TABLE integrations (
    id          TEXT PRIMARY KEY,         -- e.g., "telegram", "googlechat"
    type        TEXT NOT NULL,            -- category: "broker-plugin", "notification", "github-app"
    enabled     BOOLEAN DEFAULT false,
    config      JSONB NOT NULL DEFAULT '{}',  -- encrypted sensitive fields
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    created_by  TEXT                      -- admin user who configured it
);

CREATE TABLE integration_status (
    integration_id  TEXT PRIMARY KEY REFERENCES integrations(id),
    health          TEXT DEFAULT 'unknown',  -- healthy, degraded, unhealthy, unknown
    message         TEXT,
    details         JSONB DEFAULT '{}',
    last_heartbeat  TIMESTAMPTZ,
    node_id         TEXT,                    -- which node the integration is running on
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE integration_operations (
    id              TEXT PRIMARY KEY,
    integration_id  TEXT REFERENCES integrations(id),
    action          TEXT NOT NULL,          -- install, configure, enable, disable, restart, uninstall
    status          TEXT DEFAULT 'pending', -- pending, running, completed, failed
    config_snapshot JSONB,                 -- config at time of operation
    log             TEXT,
    started_by      TEXT,                  -- admin user
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ
);
```

#### Bootstrap Flow

On hub startup:
1. Read `settings.yaml` for any integration config (backward compatibility)
2. If integration exists in DB, DB config takes precedence
3. If integration exists only in settings.yaml (migration case), import to DB
4. Settings.yaml entries can be marked `managed: false` to prevent DB override (escape hatch)

This means existing deployments keep working — their settings.yaml config is imported on first run with the new code.

### 4.4 Integration Lifecycle in HA

#### Single Instance Guarantee

In multi-hub HA, only one instance of each integration should run. Use Postgres advisory locks:

```go
func (s *IntegrationService) AcquireLock(ctx context.Context, integrationID string) (bool, error) {
    // pg_try_advisory_lock with a hash of the integration ID
    // Returns true if this instance acquired the lock
    // Lock released on disconnect (process exit)
}
```

On startup, each integration service attempts to acquire its advisory lock. If another instance holds it, the new instance enters standby mode (monitoring the lock, ready to take over if the primary fails).

#### Health Reporting

Integrations report health via heartbeat:

```
POST /api/v1/integrations/{type}/heartbeat
Authorization: Bearer scion_integ_xxx
Body: {
    "health": "healthy",
    "message": "3 groups linked, last message 2m ago",
    "details": { "webhook_registered": "true", "groups": "3" },
    "node_id": "integ-node-1"
}
```

The hub records this in `integration_status`. If heartbeats stop (configurable timeout, default 60s), the hub marks the integration as unhealthy. In HA mode, a standby instance can detect the stale heartbeat and attempt to take over the advisory lock.

#### Message Routing with Postgres

When an integration needs to deliver an inbound message in HA mode:

```
Option A: Via Hub API (simple, current pattern)
  Integration → POST /api/v1/broker/inbound → Hub → DispatchAgentMessage()
  Works with any number of hub nodes (LB distributes)

Option B: Via Postgres NOTIFY (efficient, no HTTP overhead)
  Integration → pg_notify('scion.messages', payload) → Hub (listening) → dispatch
  Any hub node picks up the NOTIFY (Postgres broadcasts to all listeners)
  Requires dedup: integration includes a message ID, hub checks before dispatching
```

**Recommendation:** Start with Option A (Hub API). It already works, requires no new infrastructure, and scales well enough for current message volumes. Add Option B as a performance optimization when Postgres LISTEN/NOTIFY is implemented for the EventPublisher.

### 4.5 Lightweight Mode Considerations (Workstation & Cloud Solo)

#### Workstation

- **Telegram in polling mode:** No webhook needed (no public URL). Plugin polls Telegram's `getUpdates` API. Already supported via `inbound_mode: polling` config.
- **No Caddy, no systemd:** Plugin runs as a hub subprocess (managed plugin, current model).
- **SQLite is fine:** Single-node, no need for Postgres advisory locks.
- **Config in settings.yaml:** Database config storage is optional.
- **Transport:** `RPCTransport` (subprocess, go-plugin).

#### Cloud Solo

Cloud Solo is workstation mode deployed to Cloud Run — same single-binary, single-instance model:

- **Persistent volume for SQLite:** Cloud Run volume mount provides persistent storage for the SQLite database and settings.yaml. Same data model as workstation.
- **No Caddy, no systemd:** Cloud Run handles TLS/ingress and process lifecycle.
- **IAP blocks webhooks:** Cloud Solo runs behind IAP (Identity-Aware Proxy), which requires authentication on every inbound request. External services (Telegram, GitHub) cannot send unauthenticated webhooks. Integrations must use **polling mode** (outbound connections only), same as workstation.
- **Telegram as subprocess, polling mode:** Same as workstation — managed subprocess, `inbound_mode: polling`. The plugin polls Telegram's `getUpdates` API on an outbound connection that passes through IAP without issue.
- **GitHub App webhooks:** Cannot receive webhook events behind IAP. GitHub App features that depend on webhooks (push notifications, PR events) are unavailable in Cloud Solo. The GitHub App can still be configured for API-only use (installation token grants, repository access).
- **Config in settings.yaml:** Same as workstation. Settings can be baked into the container image or mounted via Cloud Run volume/Secret Manager.
- **Transport:** `RPCTransport` (co-located subprocess, same as workstation).

The `IntegrationTransport` abstraction handles both: the integration binary is the same, only the transport config changes per deployment mode.

---

## 5. Admin UI & API

### 5.1 API Design

New API endpoints under `/api/v1/admin/integrations`, admin-only.

```
GET    /api/v1/admin/integrations              — list all with status
GET    /api/v1/admin/integrations/{type}        — detail + config schema
PUT    /api/v1/admin/integrations/{type}/config — update configuration (writes to DB)
POST   /api/v1/admin/integrations/{type}/install   — run install operation
POST   /api/v1/admin/integrations/{type}/enable    — activate
POST   /api/v1/admin/integrations/{type}/disable   — deactivate
POST   /api/v1/admin/integrations/{type}/test      — test connectivity
POST   /api/v1/admin/integrations/{type}/restart   — restart with health check
DELETE /api/v1/admin/integrations/{type}           — uninstall
GET    /api/v1/admin/integrations/operations/{id}  — poll operation status
```

Integration-facing endpoints (authenticated with integration service tokens):

```
POST   /api/v1/integrations/{type}/heartbeat    — health reporting
GET    /api/v1/integrations/{type}/config       — fetch config from DB
POST   /api/v1/broker/inbound                   — inbound message delivery (existing)
```

### 5.2 Web UI

New page: `/admin/integrations` — integration cards grouped by category.

Each card shows: status indicator, config summary, action buttons (Configure / Enable / Disable / Restart / Set Up).

Setup wizards guide external prerequisites per integration (BotFather for Telegram, GCP console for Google Chat, GitHub for GitHub App).

Cards show deployment mode indicator:
- **Co-located** (subprocess) — shown in workstation/single-hub mode
- **Independent service** — shown in HA mode, with node ID and heartbeat status

Navigation: add `{ path: '/admin/integrations', label: 'Integrations', icon: 'plug-fill' }` to ADMIN_SECTION in `web/src/components/shared/nav.ts`.

### 5.3 Onboarding Alignment

The `workstation-improvements` branch implements a 6-step browser-based onboarding wizard for first-run setup (identity → system check → runtime → harness selection → image pull → workspace creation). Integration setup layers on top of this:

**In workstation/Cloud Solo mode:**
- After onboarding step 6 (workspace created), offer an optional "Connect an integration" step
- Telegram setup wizard: paste BotFather token → configure polling → done
- Reuse the same integration card components from `/admin/integrations`
- Workstation-mode gate: integration setup endpoints follow the same pattern as `/api/v1/system/*` (loopback-asserted, 404 in production for workstation-only features)

**In Single VM / HA mode:**
- Integration setup lives on `/admin/integrations` (admin-only, full capabilities)
- First-run detection: if no integrations configured, dashboard shows "Connect your first integration" prompt

**Shared components:**
- Integration card components (`integration-card.ts`, `integration-setup-wizard.ts`) work in both onboarding and admin contexts
- Config schemas and setup step definitions are the same regardless of where they render

### 5.4 Installation Executors

Purpose-built executors per integration, using shared infrastructure helpers (CaddyManager, SystemdManager, SettingsPatcher). Executors adapt their behavior based on deployment topology:

**Workstation** (local dev):
- Build binary, run as hub subprocess
- Config in settings.yaml, no privileged operations
- Polling mode for webhooks (no public URL)

**Single VM** (current production):
- Build binary, install to /usr/local/bin/, patch Caddy, update settings.yaml + DB, restart hub
- Same as current install.sh flow, but automated from web UI

**Cloud Solo** (cloud workstation):
- Same as workstation — subprocess, SQLite, settings.yaml, polling mode
- Integration binary baked into container image
- No Caddy/systemd — Cloud Run handles routing and lifecycle
- IAP blocks inbound webhooks — polling only (same as workstation)

**HA Cluster** (multi-node):
- Write config to Postgres (shared across hub nodes)
- Deploy integration as independent service (systemd unit, Cloud Run service, or K8s deployment)
- Integration picks up config from DB via API
- No hub restart needed — integration connects independently

---

## 6. Security Considerations

- **Admin-only management:** All `/api/v1/admin/integrations` endpoints require admin role.
- **Integration service tokens:** Independent integrations authenticate with dedicated tokens (scoped to message delivery and config fetch only, not admin operations). Tokens stored in the integrations DB table.
- **Secret handling:** Sensitive config (bot tokens, private keys) encrypted at rest in DB. Redacted in API responses.
- **Advisory lock scope:** Postgres advisory locks use integration-specific lock IDs — no cross-integration interference.
- **Audit logging:** All operations logged with admin identity.

---

## 7. Implementation Workstreams

This is a major undertaking that spans multiple workstreams. Each workstream is independently valuable and can be staffed in parallel where dependencies allow.

### Workstream A: Foundation (Integration Status + Admin UI Shell)

**Goal:** Visibility into what's configured and healthy, before adding lifecycle management.

- A1: `integrations`, `integration_status`, `integration_operations` DB tables
- A2: Config import from settings.yaml to DB on startup (backward compatible)
- A3: HealthMonitor polling existing plugin HealthCheck() RPC
- A4: API: GET /api/v1/admin/integrations (list with status)
- A5: Web UI: `/admin/integrations` page with status cards (read-only initially)
- A6: Nav entry in admin sidebar

**Dependencies:** None. Can start immediately.

### Workstream B: IntegrationTransport Abstraction

**Goal:** Decouple integrations from hub process, enabling both co-located and independent modes.

- B1: `IntegrationTransport` interface with RPCTransport and APITransport
- B2: Integration service token system (for APITransport auth)
- B3: Heartbeat API: POST /api/v1/integrations/{type}/heartbeat
- B4: Config fetch API: GET /api/v1/integrations/{type}/config
- B5: Refactor Telegram plugin to use IntegrationTransport (RPC default, API opt-in)
- B6: Refactor Google Chat plugin to use IntegrationTransport

**Dependencies:** A1-A2 (DB tables for config storage).

### Workstream C: Per-Integration Admin (Executors + Setup Wizards)

**Goal:** Web-based lifecycle management for each integration type.

- C1: Shared infra helpers (CaddyManager, SystemdManager, SettingsPatcher)
- C2: NotificationChannelExecutor + UI (simplest, validates the stack)
- C3: TelegramExecutor + setup wizard (BotFather → configure → install)
- C4: ChatAppExecutor + setup wizard (GCP prerequisites → install)
- C5: GitHubAppExecutor + setup wizard
- C6: Inbound mode switching (polling ↔ webhook) as admin UI operation

**Dependencies:** A1-A5 (admin UI shell), partially B1 (transport abstraction for mode switching).

### Workstream D: Onboarding Integration

**Goal:** Integration setup as part of the first-run experience, building on `workstation-improvements` branch.

- D1: Optional "Connect an integration" step after onboarding wizard step 6
- D2: Telegram setup wizard in onboarding context (workstation: polling mode, paste token)
- D3: First-run detection on dashboard ("Connect your first integration" prompt)
- D4: Shared card/wizard components usable in both onboarding and admin contexts

**Dependencies:** A5 (integration cards), C3 (Telegram executor), `workstation-improvements` branch merged.

### Workstream E: HA Support

**Goal:** Integrations work correctly in multi-node deployments.

- E1: Postgres advisory locks for single-instance guarantee
- E2: PostgresTransport (API + LISTEN/NOTIFY for message subscription)
- E3: Standby/failover logic for integration services
- E4: Multi-hub integration testing (two hub nodes, one integration instance)
- E5: Container/deployment tooling for independent integration services

**Dependencies:** B1-B6 (transport abstraction), Postgres migration (postgres-strategy.md).

### Workstream Dependency Graph

```
A: Foundation ──────────┬──► C: Per-Integration Admin ──► D: Onboarding
                        │                                      │
                        ├──► B: Transport Abstraction ──────► E: HA Support
                        │         │
                        │         └──► C6: Mode switching
                        │
                  (workstation-improvements) ──────────────► D: Onboarding
```

### Suggested Sequencing

**Near-term (can start now):**
- A1-A6: Foundation — gives immediate visibility, no architectural changes
- C1: Shared infra helpers — reusable building blocks

**Medium-term (after foundation):**
- B1-B4: Transport abstraction — architectural enabler for HA
- C2: Notification channels — validate executor + UI stack on simplest case
- C3: Telegram executor — most valuable single integration

**After workstation-improvements merges:**
- D1-D4: Onboarding integration

**After Postgres migration:**
- E1-E5: HA support

---

## 8. Open Questions

1. **Postgres dependency timing:** The IntegrationTransport abstraction and DB config storage don't require Postgres (SQLite works for single-node). But HA features (advisory locks, LISTEN/NOTIFY) do. Should we gate Phase 7 on the Postgres migration, or design for both?
   **Recommendation:** Design for both. DB config and API transport work on SQLite. Advisory locks and PostgresTransport are Postgres-only features that unlock HA. Phase 7 is explicitly gated on Postgres availability.

2. **Integration service tokens vs existing auth:** Should integrations use the existing UAT (User Access Token) system or a new token type?
   **Recommendation:** New token type — `integration_token` — scoped specifically to integration operations (heartbeat, config fetch, message publish). UATs are user-scoped; integration tokens are service-scoped. Simpler access control.

3. **Backward compatibility window:** How long do we support the old settings.yaml-only config path?
   **Recommendation:** Indefinitely for workstation mode (settings.yaml is simpler for local dev). For production, settings.yaml config is auto-imported to DB on upgrade, then DB is authoritative. Settings.yaml can override with `managed: false` as an escape hatch.

4. **Binary distribution:** Build from source vs pre-built releases?
   **Recommendation:** Support both. Build from source in dev (extras/ available). Download pre-built from GCS/GitHub releases in production. Admin UI offers both options in the setup wizard.

5. **Integration discovery in HA:** How do hub nodes discover running integrations?
   **Recommendation:** Integration heartbeats write to `integration_status` table with `node_id`. Hub reads this table. No separate service discovery needed — the DB is the source of truth.

---

## 9. Migration Path

### From Current State → Phase 1

Existing deployments continue to work unchanged:
- Settings.yaml config is imported to DB on first run
- Managed plugins (Telegram) continue as hub subprocesses via RPCTransport
- Self-managed plugins (chat-app) continue with localhost RPC
- Admin UI provides new visibility but doesn't change behavior

### From Phase 1 → HA-Ready

Incremental steps, each backward compatible:
1. Add IntegrationTransport abstraction (Phase 3) — RPCTransport is default, no behavior change
2. Add APITransport option — integrations can optionally run independently
3. Move config to DB — settings.yaml still read, DB authoritative
4. When Postgres is available (Phase 7) — add advisory locks and PostgresTransport
5. Deploy integrations as independent services — full HA

No big-bang migration. Each phase is independently shippable and backward compatible.

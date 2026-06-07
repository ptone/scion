# Integrations Architecture

**Status:** Draft  
**Date:** 2026-06-07  
**Issue:** #115  
**Branch:** scion/integrationsadmin  
**Related:** [scion-plugins.md](scion-plugins.md), [message-broker-plugins.md](message-broker-plugins.md), [postgres-strategy.md](postgres-strategy.md), [hosted/web-realtime.md](hosted/web-realtime.md), [hosted/hosted-architecture.md](hosted/hosted-architecture.md), [workstation-onboarding.md](workstation-onboarding.md) (on `workstation-improvements` branch)

---

## 1. Problem

Scion's integrations (Telegram, Google Chat, GitHub App, notification channels) are architecturally coupled to a single hub instance. As users grow from local development to cloud deployment to production HA, integrations must evolve with them — but today each transition requires re-architecting how integrations are deployed, configured, and managed.

Specifically:

- **Managed plugins** (Telegram) run as hub subprocesses — they can't survive hub restarts independently, and in multi-hub HA they'd duplicate.
- **Self-managed plugins** (Google Chat) connect to a specific hub's localhost RPC — they can't failover to another hub instance.
- **Configuration** lives in `settings.yaml` on each hub's local filesystem — not in shared state.
- **Operational setup** requires editing config files, building binaries, patching Caddy, and restarting services in the correct order with no guided flow.

The core challenge is designing an integration architecture where the same integration code works across all deployment modes, with transitions requiring minimal reconfiguration.

---

## 2. Goals

- A user can evolve from Workstation → Cloud Solo → Single VM → HA Cluster, changing **at most two things** per transition: the transport mode and the inbound mode.
- Integrations run as co-located subprocesses in simple modes and as independent services in HA — same binary, different transport config.
- Integration configuration is stored in the database for HA modes, with settings.yaml as bootstrap/override for simple modes.
- An admin UI provides visibility, setup wizards, and lifecycle management — but is one facet of the broader architecture, not the primary deliverable.
- Each implementation workstream is independently valuable and backward compatible.

## 3. Non-Goals

- Third-party/community plugin admin UI (future — extend via schema-driven forms when needed).
- Automating external service creation (BotFather, GitHub App) — manual steps required.
- Full Kubernetes operator for integration deployment (defer to K8s milestone).
- Moving Telegram plugin state (telegram_v2.db) into the hub database — the plugin keeps its own SQLite DB across all modes.

---

## 4. Deployment Modes

Scion defines four canonical deployment modes. Integrations must work across all of them.

| Mode | Substrate | Persistence | Reverse Proxy | Integration Model | When to Use |
|------|-----------|-------------|---------------|-------------------|-------------|
| **Workstation** | Local machine (macOS/Linux) | SQLite | None | Co-located subprocess | Local development, personal use |
| **Single VM** | GCE VM (or similar) | SQLite or Postgres | Caddy + systemd | Co-located or independent services | Small team, current production model |
| **Cloud Solo** | Cloud Run (single instance) | SQLite (persistent volume) | Cloud Run ingress (behind IAP) | Co-located subprocess | Cloud-deployed dev/personal server |
| **HA Cluster** | VM instance group, Cloud Run service, or K8s | Postgres (shared) | LB + Caddy/Ingress | Independent services | Production, multi-user, high availability |

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

---

## 5. The Lifecycle Journey

This section traces Telegram — the most complex integration — through each deployment mode transition. This journey is the primary design validation: if Telegram works smoothly across all transitions, the architecture is sound.

### 5.1 Stage 1: Workstation (Day 1)

**Setup:**
- User creates bot in BotFather, gets token, disables privacy mode
- Configures via admin UI or settings.yaml: `bot_token`, `inbound_mode: polling`
- Hub starts, plugin manager launches `scion-plugin-telegram` as subprocess
- Plugin polls Telegram `getUpdates` API (outbound, no public URL needed)
- User adds bot to a Telegram group, runs `/setup` to link to a project

**State created:**
- `settings.yaml`: bot_token, webhook_secret, db_path, inbound_mode
- `telegram_v2.db` (plugin-owned SQLite): group→project links, user→scion mappings
- Hub SQLite: agents, projects, integration status

**Message flow:**
```
Inbound:  Telegram API ←─poll─ Plugin (subprocess) → broker.Publish() → Hub → Agent
Outbound: Agent → Hub → broker.Publish() → Plugin → Telegram sendMessage API
```

**What makes this work:** RPC transport (go-plugin), polling mode, settings.yaml config, SQLite. Simplest possible setup.

### 5.2 Transition: Workstation → Cloud Solo

**What the user does:**
1. Builds container image including scion + scion-plugin-telegram binaries
2. Includes settings.yaml (baked in image or via Secret Manager)
3. Deploys to Cloud Run with persistent volume for SQLite DBs

**What changes:** Nothing architecturally. Same subprocess, same polling (IAP blocks webhooks), same SQLite, same settings.yaml.

**What stays the same:** Transport (RPC), inbound mode (polling), config format, all state.

**Migration effort:** Near zero — deploy and go.

### 5.3 Transition: Cloud Solo → Single VM

**What the user does:**
1. Provisions a VM, runs starter-hub setup
2. Copies settings.yaml config and telegram_v2.db
3. Hub discovers and launches Telegram as managed subprocess (or independent systemd service)
4. Switches from polling to webhook mode (VM has public URL via Caddy)
5. Registers webhook URL with Telegram API

**What changes:** Inbound mode (polling → webhook), Caddy route added for `/telegram/webhook*`, optionally runs as independent systemd service instead of subprocess.

**What stays the same:** Bot token, group links (telegram_v2.db), transport can stay RPC.

### 5.4 Transition: Single VM → HA Cluster

This is the significant transition.

**What the user does:**
1. Sets up Postgres, migrates hub DB (per postgres-strategy.md)
2. Deploys Telegram as independent service with APITransport (separate Cloud Run service, VM, or K8s pod)
3. Carries telegram_v2.db with the independent service (plugin keeps its own state)
4. Configures integration service token for hub API auth
5. Advisory lock ensures single Telegram instance across the cluster

**What changes:**
- Transport: RPC → APITransport (HTTP to hub API through LB)
- Process model: hub subprocess → independent service with its own lifecycle
- Hub-level config: settings.yaml → Postgres (integration_config table)
- Health reporting: HealthCheck() RPC → heartbeat POST to hub API

**What stays the same:**
- Bot token (same bot, same token)
- `telegram_v2.db` (plugin carries its own state, same schema, same SQLite file)
- Webhook mode (public URL via LB)
- Telegram API interactions
- Message delivery path (POST /api/v1/broker/inbound — already the pattern)

### 5.5 Key Design Principle

Each transition changes at most two things: **transport mode** and **inbound mode**. Plugin state, config schema, and bot identity stay the same across all deployment modes. The integration binary is the same — only the transport configuration changes.

---

## 6. Architecture

### 6.1 IntegrationTransport Abstraction

The core architectural change: integrations communicate with the hub through a transport abstraction that supports both co-located (RPC) and distributed (API) modes.

```go
type IntegrationTransport interface {
    PublishMessage(ctx context.Context, topic string, msg *messages.StructuredMessage) error
    SubscribeMessages(pattern string, handler MessageHandler) (Subscription, error)
    FetchConfig(ctx context.Context) (map[string]string, error)
    ReportHealth(ctx context.Context, status *HealthStatus) error
    Close() error
}
```

Three implementations:

| Transport | Deployment Modes | Characteristics |
|-----------|-----------------|-----------------|
| `RPCTransport` | Workstation, Cloud Solo, Single VM | go-plugin RPC, subprocess or localhost, lowest latency |
| `APITransport` | Single VM (optional), HA Cluster | HTTP to hub API (through LB), works anywhere, stateless |
| `PostgresTransport` | HA Cluster | APITransport + Postgres LISTEN for efficient subscription |

Selection via integration config:
```yaml
# Co-located (default)
transport: rpc

# Independent service
transport: api
hub_url: https://hub.example.com
auth_token: scion_integ_xxx

# Independent + Postgres subscription
transport: postgres
hub_url: https://hub.example.com
auth_token: scion_integ_xxx
postgres_url: postgres://...
```

### 6.2 Configuration Storage

Integration configuration moves from settings.yaml to the database for HA modes, while settings.yaml remains the bootstrap/override mechanism for simple modes.

```sql
CREATE TABLE integrations (
    id          TEXT PRIMARY KEY,
    type        TEXT NOT NULL,
    enabled     BOOLEAN DEFAULT false,
    config      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    created_by  TEXT
);

CREATE TABLE integration_status (
    integration_id  TEXT PRIMARY KEY REFERENCES integrations(id),
    health          TEXT DEFAULT 'unknown',
    message         TEXT,
    details         JSONB DEFAULT '{}',
    last_heartbeat  TIMESTAMPTZ,
    node_id         TEXT,
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE integration_operations (
    id              TEXT PRIMARY KEY,
    integration_id  TEXT REFERENCES integrations(id),
    action          TEXT NOT NULL,
    status          TEXT DEFAULT 'pending',
    config_snapshot JSONB,
    log             TEXT,
    started_by      TEXT,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ
);
```

**Bootstrap flow** on hub startup:
1. Read settings.yaml for any integration config (backward compatibility)
2. If integration exists in DB, DB config takes precedence
3. If integration exists only in settings.yaml, import to DB
4. Settings.yaml entries with `managed: false` are not overridden by DB

### 6.3 HA: Single Instance Guarantee

In multi-hub HA, only one instance of each integration should run. Postgres advisory locks:

```go
func (s *IntegrationService) AcquireLock(ctx context.Context, integrationID string) (bool, error) {
    // pg_try_advisory_lock with a hash of the integration ID
    // Lock released on disconnect (process exit)
}
```

Standby instances monitor the lock and take over if the primary fails.

### 6.4 HA: Health Reporting

Independent integrations report health via heartbeat:

```
POST /api/v1/integrations/{type}/heartbeat
Authorization: Bearer scion_integ_xxx
Body: { "health": "healthy", "message": "...", "details": {...}, "node_id": "..." }
```

If heartbeats stop (timeout 60s), the hub marks the integration unhealthy. Standby instances can detect and take over.

### 6.5 HA: Message Routing

Inbound messages use the Hub API (existing pattern):
```
Integration → POST /api/v1/broker/inbound → Hub → DispatchAgentMessage()
```
This works through any LB — no changes needed. Postgres NOTIFY is a future optimization.

### 6.6 Lightweight Modes (Workstation & Cloud Solo)

**Workstation:** Telegram polls via `getUpdates` (no public URL). Managed subprocess, SQLite, settings.yaml, `RPCTransport`.

**Cloud Solo:** Same as workstation but deployed to Cloud Run. IAP blocks all unauthenticated inbound requests — webhooks from Telegram/GitHub cannot reach the service. Integrations must use polling mode. SQLite on persistent volume. GitHub App is API-only (no webhook events).

---

## 7. Admin UI & Onboarding

The admin UI is one facet of the integration architecture — it provides visibility, setup wizards, and lifecycle management, but the core value is in the transport abstraction and deployment mode support.

### 7.1 API Endpoints

Admin endpoints (admin role required):
```
GET    /api/v1/admin/integrations              — list all with status
GET    /api/v1/admin/integrations/{type}        — detail + config schema
PUT    /api/v1/admin/integrations/{type}/config — update configuration
POST   /api/v1/admin/integrations/{type}/install
POST   /api/v1/admin/integrations/{type}/enable
POST   /api/v1/admin/integrations/{type}/disable
POST   /api/v1/admin/integrations/{type}/test
POST   /api/v1/admin/integrations/{type}/restart
DELETE /api/v1/admin/integrations/{type}
GET    /api/v1/admin/integrations/operations/{id}
```

Integration-facing endpoints (integration service tokens):
```
POST   /api/v1/integrations/{type}/heartbeat
GET    /api/v1/integrations/{type}/config
POST   /api/v1/broker/inbound                  (existing)
```

### 7.2 Web UI

`/admin/integrations` page with integration cards grouped by category. Each card shows status, config summary, and actions (Configure / Enable / Disable / Restart / Set Up). Setup wizards guide external prerequisites per integration.

Cards indicate deployment mode: co-located (subprocess) vs independent service (with node ID and heartbeat status).

### 7.3 Installation Executors

Purpose-built executors per integration using shared infra helpers (CaddyManager, SystemdManager, SettingsPatcher). Executors adapt to deployment mode:
- **Workstation/Cloud Solo:** Config in settings.yaml, no privileged ops, polling mode
- **Single VM:** Build/install binary, patch Caddy, update settings + DB, restart hub
- **HA Cluster:** Write config to Postgres, deploy as independent service, no hub restart

### 7.4 Onboarding Alignment

The `workstation-improvements` branch adds a 6-step onboarding wizard (identity → system check → runtime → harness → images → workspace). Integration setup layers on top:

- **Workstation/Cloud Solo:** After onboarding, optional "Connect an integration" step. Telegram wizard: paste BotFather token → configure polling → done. Shares card/wizard components with admin page.
- **Single VM / HA:** Integration setup on `/admin/integrations`. First-run detection: dashboard prompts "Connect your first integration" if none configured.

---

## 8. Security

- **Admin-only management:** All admin integration endpoints require admin role.
- **Integration service tokens:** Independent integrations authenticate with dedicated `integration_token` type — scoped to heartbeat, config fetch, and message publish. Not user-scoped.
- **Secret handling:** Sensitive config encrypted at rest in DB, redacted in API responses.
- **Advisory lock scope:** Per-integration lock IDs, no cross-integration interference.
- **Audit logging:** All operations logged with admin identity.

---

## 9. Implementation Workstreams

This is a major undertaking spanning multiple workstreams. Each is independently valuable and can be staffed in parallel.

### Workstream A: Foundation

**Goal:** Visibility into what's configured and healthy.

- A1: `integrations`, `integration_status`, `integration_operations` DB tables
- A2: Config import from settings.yaml to DB on startup
- A3: HealthMonitor polling existing plugin HealthCheck() RPC
- A4: API: GET /api/v1/admin/integrations (list with status)
- A5: Web UI: `/admin/integrations` page with status cards (read-only)
- A6: Nav entry in admin sidebar

**Dependencies:** None. Can start immediately.

### Workstream B: IntegrationTransport

**Goal:** Decouple integrations from hub process for HA readiness.

- B1: `IntegrationTransport` interface with RPCTransport and APITransport
- B2: Integration service token system
- B3: Heartbeat API
- B4: Config fetch API
- B5: Refactor Telegram plugin to use IntegrationTransport
- B6: Refactor Google Chat plugin to use IntegrationTransport

**Dependencies:** A1-A2 (DB tables).

### Workstream C: Per-Integration Admin

**Goal:** Web-based lifecycle management for each integration.

- C1: Shared infra helpers (CaddyManager, SystemdManager, SettingsPatcher)
- C2: NotificationChannelExecutor + UI (simplest, validates the stack)
- C3: TelegramExecutor + setup wizard
- C4: ChatAppExecutor + setup wizard
- C5: GitHubAppExecutor + setup wizard
- C6: Inbound mode switching (polling ↔ webhook) as admin operation

**Dependencies:** A1-A5 (admin UI shell), partially B1 (transport for mode switching).

### Workstream D: Onboarding

**Goal:** Integration setup as part of first-run experience.

- D1: Optional "Connect an integration" step after onboarding wizard
- D2: Telegram setup wizard in onboarding context
- D3: First-run detection on dashboard
- D4: Shared components for onboarding and admin

**Dependencies:** A5, C3, `workstation-improvements` branch merged.

### Workstream E: HA Support

**Goal:** Integrations work in multi-node deployments.

- E1: Postgres advisory locks for single-instance guarantee
- E2: PostgresTransport
- E3: Standby/failover logic
- E4: Multi-hub integration testing
- E5: Container/deployment tooling for independent services

**Dependencies:** B1-B6, Postgres migration.

### Dependency Graph

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

- **Now:** A (Foundation) + C1 (shared infra helpers)
- **After foundation:** B (Transport) + C2-C3 (notification channels, Telegram)
- **After workstation-improvements merges:** D (Onboarding)
- **After Postgres migration:** E (HA support)

---

## 10. Decisions Made

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Hybrid approach: per-integration executors + shared infra helpers | Best UX per integration, shared helpers avoid duplication, practical to build incrementally |
| 2 | Four deployment modes: Workstation, Single VM, Cloud Solo, HA Cluster | Covers the full user journey from local dev to production HA |
| 3 | Cloud Solo = workstation in Cloud Run | SQLite, subprocess, settings.yaml — same model, different substrate |
| 4 | Cloud Solo behind IAP = polling only | IAP requires auth on all inbound requests; webhooks can't reach the service |
| 5 | Telegram DB (telegram_v2.db) stays separate | Plugin owns its state, avoids coupling plugin internals to hub DB schema |
| 6 | IntegrationTransport abstraction (RPC/API/Postgres) | Same binary across all modes, only transport config changes |
| 7 | Config in DB for HA, settings.yaml for bootstrap | Shared state for multi-node, backward compatible for simple modes |
| 8 | HA substrate-agnostic | VM instance group, Cloud Run service, or K8s — architecture doesn't prescribe |
| 9 | Hub API for inbound messages (not Postgres NOTIFY) | Already works through LB, Postgres NOTIFY is a future optimization |
| 10 | Integration service tokens (not UATs) | Service-scoped, not user-scoped; simpler access control |
| 11 | Onboarding alignment with workstation-improvements | Integration setup layers on the existing onboarding wizard |

---

## 11. Open Questions

1. **Postgres dependency timing:** DB config and API transport work on SQLite. Advisory locks and PostgresTransport require Postgres. Workstream E is gated on Postgres availability.

2. **Backward compatibility window:** Settings.yaml supported indefinitely in workstation mode. For production, auto-imported to DB on upgrade, then DB authoritative. `managed: false` as escape hatch.

3. **Binary distribution:** Support both build-from-source (dev, extras/ available) and pre-built releases (production, GCS/GitHub releases).

4. **Integration discovery in HA:** Heartbeats write to `integration_status` with `node_id`. Hub reads this table. DB is the source of truth — no separate service discovery.

---

## 12. Migration Path

**Current state → Foundation (Workstream A):**
Existing deployments unchanged. Settings.yaml imported to DB on first run. Managed/self-managed plugins continue as-is. Admin UI provides new visibility.

**Foundation → HA-ready:**
1. IntegrationTransport abstraction (B) — RPCTransport default, no behavior change
2. APITransport option (B) — integrations can optionally run independently
3. Config in DB (A) — settings.yaml still read, DB authoritative
4. Postgres available (E) — add advisory locks and PostgresTransport
5. Independent services (E) — full HA

No big-bang migration. Each step is independently shippable and backward compatible.

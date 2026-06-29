# Chat Integration Admin — Architecture Design

**Date:** 2026-06-28 (updated 2026-06-29)
**Status:** Design complete — all questions resolved, ready for implementation planning
**Issues:** #361 (Discord HA), #362 (Telegram HA)

## 1. Problem Statement

Managing Telegram, Discord, and Google Chat integrations is entirely shell-driven today: editing YAML config files, building binaries from source, running install scripts, restarting services. The hub admin needs a web UI and API surface to:

1. **View and edit** integration configuration without hand-editing YAML or SSH
2. **Trigger updates** to pull latest code, rebuild, and restart (modes 1-2) or deploy new container revisions (mode 3)
3. **Monitor status** — connection health, version, deployment mode

**Target persona:** Hub admin.

## 2. Deployment Modes

Three deployment modes, all supported from day one:

| Mode | Description | Update Mechanism | Config Storage | Platforms |
|------|-------------|-----------------|----------------|-----------|
| **1. Single-node, long-poll** | Plugin as hub subprocess, outbound-only connections. Works behind NAT. | Hub: git pull → go build → replace binary → restart spoke | YAML file + hub secrets | Telegram, Discord (Gateway WS) |
| **2. Single-node, webhook/gateway** | Plugin as hub subprocess with inbound connection (webhook URL or Gateway WS). | Same as Mode 1 | YAML file + hub secrets | Telegram, Discord, Google Chat |
| **3. HA / Separate service** | Integration runs as standalone service on separate compute. Postgres-backed state. | Container revision deployment (Cloud Run, k8s). Hub triggers or observes. | PostgreSQL tables + hub secrets | Telegram (future #362), Discord (future #361), Google Chat (future) |

### NAT Compatibility

| Transport | Direction | Behind NAT? |
|-----------|-----------|-------------|
| Telegram long-poll | Outbound HTTP | ✅ Yes |
| Discord Gateway | Outbound WebSocket | ✅ Yes |
| Telegram webhook | Inbound HTTP | ❌ Needs public URL |
| Google Chat events | Inbound HTTP | ❌ Needs public URL |

**Key insight:** Discord's Gateway WebSocket is outbound-initiated, so it works behind NAT just like Telegram's long-poll mode. Both Telegram and Discord are viable integrations for home/workstation deployments without public URLs.

## 3. Config Architecture

### 3.1 Design Principle: Integration Owns Its Config

Each integration has its own configuration domain. The hub's `settings.yaml` contains only what the hub needs to know about the integration (existence, connection mode, address). The integration's own settings live separately.

**Resolved decisions:**
- Config files live flat in `~/.scion/` (e.g., `~/.scion/scion-telegram.yaml`)
- Config changes require integration restart (no hot reload for v1)
- Sensitive values (bot tokens, webhook secrets, signing keys) stored in **hub secret backend** — not in YAML files
- Non-sensitive config (inbound_mode, listen_address, db_path, guild_id, etc.) in per-integration YAML files

**Today:**
- Telegram: config embedded in `settings.yaml` → `server.plugins.broker.telegram.config`
- Discord: config embedded in `settings.yaml` → `server.plugins.broker.discord.config`
- Google Chat: own file `scion-chat-app.yaml` ← already separate

**Target:**
- Each integration has its own config file: `~/.scion/scion-telegram.yaml`, `~/.scion/scion-discord.yaml`, `~/.scion/scion-chat-app.yaml`
- Sensitive values in hub secret backend (per GitHub App pattern)
- Hub `settings.yaml` retains only a reference entry per integration:

```yaml
server:
  plugins:
    broker:
      telegram:
        mode: plugin        # plugin | self-managed | grpc
        path: /usr/local/bin/scion-plugin-telegram  # for managed mode
        # address: host:port  # for self-managed/grpc mode
        config_file: /home/scion/.scion/scion-telegram.yaml
      discord:
        mode: plugin
        path: /usr/local/bin/scion-plugin-discord
        config_file: /home/scion/.scion/scion-discord.yaml
      googlechat:
        self_managed: true
        address: localhost:9090
        config_file: /home/scion/.scion/scion-chat-app.yaml
```

### 3.2 Secrets: Hub Secret Backend Pattern

Following the GitHub App integration pattern (`pkg/hub/handlers_github_app.go`):

**Well-known secret keys per integration:**
```go
// Telegram
const (
    SecretTelegramBotToken    = "TELEGRAM_BOT_TOKEN"
    SecretTelegramWebhookKey  = "TELEGRAM_WEBHOOK_SECRET"
)

// Discord
const (
    SecretDiscordBotToken     = "DISCORD_BOT_TOKEN"
    SecretDiscordPublicKey    = "DISCORD_PUBLIC_KEY"
)

// Google Chat
const (
    SecretGChatSigningKey     = "GCHAT_SIGNING_KEY"
)
```

**Write path (admin saves config):**
- Non-sensitive fields → write to per-integration YAML file
- Sensitive fields → `secretBackend.Set()` at hub scope with well-known key
- Integration restart triggered after save

**Read path (plugin startup):**
- Plugin receives non-sensitive config via `Configure()` call (hub reads YAML file)
- Plugin retrieves secrets from hub secret backend via hub API (or hub injects them via `Configure()`)
- Fallback chain (like GitHub App): in-memory → file path → secret backend

**API response pattern:**
- GET endpoint returns `has_bot_token: true`, `has_webhook_secret: false` — never actual values
- Mirrors `handleGetGitHubApp` pattern exactly

### 3.3 IntegrationConfigProvider Interface

A config abstraction that supports both file-backed and DB-backed storage:

```go
// IntegrationConfigProvider reads and writes integration-specific configuration.
// Non-sensitive settings only — secrets go through SecretBackend separately.
// For non-HA (modes 1-2): backed by a per-integration YAML file.
// For HA (mode 3): backed by a PostgreSQL table.
type IntegrationConfigProvider interface {
    // GetConfig returns the current non-sensitive configuration.
    GetConfig(ctx context.Context, integrationName string) (*IntegrationConfig, error)

    // UpdateConfig writes updated non-sensitive configuration.
    // Config changes require integration restart (no hot reload).
    UpdateConfig(ctx context.Context, integrationName string, config *IntegrationConfig) error

    // ListIntegrations returns all registered integrations and their deployment mode.
    ListIntegrations(ctx context.Context) ([]IntegrationSummary, error)

    // GetStatus returns health/version/connection status for an integration.
    GetStatus(ctx context.Context, integrationName string) (*IntegrationStatus, error)
}

type IntegrationConfig struct {
    Name           string         `json:"name"`
    Platform       string         `json:"platform"`       // telegram, discord, googlechat
    DeploymentMode string         `json:"deployment_mode"` // single-poll, single-webhook, ha
    Settings       map[string]any `json:"settings"`        // platform-specific non-sensitive config
    // Secrets reported as has_* booleans, never values
    HasSecrets     map[string]bool `json:"has_secrets"`    // e.g., {"bot_token": true, "webhook_secret": false}
}

type IntegrationStatus struct {
    Name           string `json:"name"`
    Connected      bool   `json:"connected"`
    Version        string `json:"version"`
    MinScionVer    string `json:"min_scion_version"`
    GitCommit      string `json:"git_commit,omitempty"`
    BuildTime      string `json:"build_time,omitempty"`
    Uptime         string `json:"uptime"`
    LastError      string `json:"last_error,omitempty"`
    DeploymentMode string `json:"deployment_mode"`
    UpdateAvail    bool   `json:"update_available,omitempty"`
}
```

### 3.4 Implementations

```
IntegrationConfigProvider
├── YAMLConfigProvider      // reads/writes per-integration YAML files in ~/.scion/
│                           // used in modes 1-2 (single-node)
└── PostgresConfigProvider  // reads/writes integration_config table
                            // used in mode 3 (HA)
```

Both implementations use the same `SecretBackend` for sensitive values. The provider handles only non-sensitive settings; the admin API handler coordinates both.

## 4. Update Architecture

### 4.1 Modes 1-2: Hub-Mediated Source Build

**Source location:** Convention-based. Hub uses `SCION_REPO_PATH` env var (defaults to auto-detection from hub binary location or `/home/scion/scion/`). Plugin source is at `{SCION_REPO_PATH}/extras/{plugin-dir}/`.

**Update flow:**
```
Admin UI → POST /api/v1/admin/integrations/{name}/update
    → Hub checks current version vs available (git fetch --dry-run or local comparison)
    → Hub runs: git pull (in repo root)
    → Hub runs: go build -o {binary} ./cmd/{binary} (in extras/{plugin-dir})
    → Hub stops the plugin spoke in FanOutEventBus
    → Hub replaces the binary (atomic rename)
    → Hub starts the new plugin spoke
    → Hub re-subscribes all active message patterns
    → Returns result to admin UI
```

**Prerequisite changes to FanOutEventBus:**
- Add `ReplaceSpoke(name string, newBus NamedEventBus) error` method
- Add `RemoveSpoke(name string) error` method
- These need to handle subscription migration (tear down old, rebuild on new)

**Prerequisite changes to plugin Manager:**
- Add `UpdatePlugin(name string) error` — stop, replace binary, relaunch, reconfigure
- The existing `Reconnect()` method handles self-managed plugin reconnection already

### 4.2 Mode 3: Container Revision Deployment

For integrations running as separate services (Cloud Run, k8s):

```
Admin UI → POST /api/v1/admin/integrations/{name}/update
    → Hub signals the integration to update:
        Write to integration_updates table in Postgres
        Integration watches via LISTEN/NOTIFY
    → Integration triggers its own redeployment (new container revision)
    → Hub detects reconnection via reconnectingBrokerAdapter
    → Returns result to admin UI
```

**Database signal approach** chosen for consistency with the Postgres-backed HA model. The hub writes an update request; the integration watches for it and triggers its own redeployment. This avoids the hub needing deployment credentials for every target platform.

### 4.3 Version Tracking

The existing `PluginInfo` struct already carries `Version` and `MinScionVersion`. Extend with:

```go
type PluginInfo struct {
    Name            string `json:"name"`
    Version         string `json:"version"`
    MinScionVersion string `json:"min_scion_version"`
    // New fields:
    GitCommit       string `json:"git_commit,omitempty"`
    BuildTime       string `json:"build_time,omitempty"`
    ConfigFile      string `json:"config_file,omitempty"`
    DeploymentMode  string `json:"deployment_mode,omitempty"`
}
```

## 5. Admin API Endpoints

New endpoints under `/api/v1/admin/integrations/`:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/admin/integrations` | List all registered integrations with status |
| GET | `/api/v1/admin/integrations/{name}` | Get config (non-sensitive) + has_secrets + status |
| PUT | `/api/v1/admin/integrations/{name}/config` | Update config (non-sensitive fields to YAML/DB, sensitive to secret backend) |
| POST | `/api/v1/admin/integrations/{name}/update` | Trigger binary/container update |
| POST | `/api/v1/admin/integrations/{name}/restart` | Restart the integration (config reload, no update) |
| GET | `/api/v1/admin/integrations/{name}/health` | Detailed health check |

All endpoints require admin role (same as existing `/api/v1/admin/*` endpoints).

**Config endpoint response pattern** (mirrors GitHub App):
```json
{
  "name": "telegram",
  "platform": "telegram",
  "deployment_mode": "single-webhook",
  "settings": {
    "inbound_mode": "webhook",
    "webhook_listen": ":9094",
    "db_path": "/var/lib/scion/telegram_v2.db",
    "send_queue_size": 100,
    "send_min_delay": "50ms",
    "agent_cache_ttl": "5m"
  },
  "has_secrets": {
    "bot_token": true,
    "webhook_secret": true
  },
  "status": {
    "connected": true,
    "version": "v0.8.2",
    "git_commit": "b4a682a",
    "uptime": "3d 12h",
    "deployment_mode": "single-webhook"
  }
}
```

## 6. Admin UI

### 6.1 New Page: `/admin/integrations`

A dedicated admin page (not a tab in server-config) since integrations are extensions, not core server settings.

**List View:**
```
┌──────────────────────────────────────────────────────┐
│  Chat Integrations                                    │
│                                                       │
│  ┌─────────────┬──────────┬─────────┬───────────────┐ │
│  │ Integration │ Status   │ Mode    │ Version       │ │
│  ├─────────────┼──────────┼─────────┼───────────────┤ │
│  │ 🟢 Telegram │ Connected│ Plugin  │ v0.8.2 (b4a6) │ │
│  │ 🟢 Discord  │ Connected│ Plugin  │ v0.5.0 (c3f1) │ │
│  │ 🔴 GChat    │ Error    │ Ext.Svc │ v0.6.1 (a2b3) │ │
│  └─────────────┴──────────┴─────────┴───────────────┘ │
│                                                       │
│  [+ Add Integration]                                  │
└──────────────────────────────────────────────────────┘
```

**Detail View** (click into an integration):
- **Status card**: connected/disconnected, uptime, last error, version + git commit
- **Config form**: platform-specific settings, sensitive fields show "Set" / "Not set" with option to update (following GitHub App UI pattern)
- **Actions**: Update (pull + build + restart), Restart, View Logs
- **Deployment mode selector**: Plugin (managed) / External Service (self-managed) / HA (separate + Postgres)

### 6.2 Web Component

New Lit component: `web/src/components/pages/admin-integrations.ts`

Follows existing patterns from `admin-server-config.ts` and GitHub App admin:
- Fetch config from API on load
- Form fields hydrated from response
- Sensitive fields show has_* status, not values
- Save sends PUT to update endpoint (non-sensitive to YAML, sensitive to secret backend)
- Config save triggers automatic integration restart

## 7. Config File Refactor Scope

### Telegram (extras/scion-telegram/)

**Current:** Config received as `map[string]string` via `Configure()` call from hub, which reads from `settings.yaml`. Bot token stored in plaintext in settings.yaml.

**Change:**
1. Define `TelegramConfig` struct with typed YAML fields in Telegram plugin code
2. Plugin reads non-sensitive config from its own YAML file (path passed via `Configure()`)
3. Plugin retrieves bot_token and webhook_secret from hub secret backend
4. Hub's `settings.yaml` entry reduced to: `mode`, `path`, `config_file`
5. Backward compatibility: if no `config_file` specified, fall back to inline `config` map (existing behavior)

### Discord (extras/scion-discord/)

**Same pattern as Telegram:**
1. Define `DiscordConfig` struct
2. Non-sensitive config (guild_id, db_path, mention_routing, tuning params) in YAML file
3. bot_token, public_key from hub secret backend
4. Hub entry reduced to reference only

### Google Chat (extras/scion-chat-app/)

**Already separate** — has `scion-chat-app.yaml`. Changes:
1. Hub's `settings.yaml` entry adds `config_file` field pointing to `scion-chat-app.yaml`
2. Migrate signing key to hub secret backend (already supports Secret Manager fallback — now also supports hub secret backend)
3. Admin API reads the external file for the config UI

## 8. Implementation Phases

### Phase 1: Config File Refactor + Secrets Migration (Foundation)
- Move Telegram/Discord config to standalone YAML files in `~/.scion/`
- Migrate sensitive values (bot tokens, webhook secrets) to hub secret backend
- Add `config_file` field to `V1PluginEntry`
- Implement `YAMLConfigProvider`
- Plugin startup: read YAML for non-sensitive, hub secrets for sensitive
- Backward compatibility with inline config map

### Phase 2: Admin API
- Add admin API endpoints (GET/PUT config, GET status, POST restart)
- Follow GitHub App handler pattern for secret has_*/set flow
- Add `IntegrationStatus` to plugin Manager (wraps existing `HealthCheck` + `GetInfo`)
- Wire config updates: non-sensitive → YAML file, sensitive → secret backend, then restart

### Phase 3: Admin UI
- New `admin-integrations.ts` Lit component
- List view with status indicators
- Detail view with config form + secret status indicators
- Restart button, status card
- Wire to Phase 2 API endpoints

### Phase 4: Hub-Mediated Updates (Modes 1-2)
- FanOutEventBus spoke replacement (add `ReplaceSpoke` method)
- Plugin Manager `UpdatePlugin` method (stop → build → replace → restart)
- Admin API update endpoint
- UI update button + progress indicator
- Convention-based source path (`SCION_REPO_PATH`)

### Phase 5: HA Support (Mode 3)
- `PostgresConfigProvider` implementation
- Database signal mechanism for remote updates (LISTEN/NOTIFY)
- Container revision deployment integration
- Depends on #361 (Discord HA) and #362 (Telegram HA) for the integration-side work

## 9. Resolved Design Decisions

| Question | Decision | Rationale |
|----------|----------|-----------|
| Config file location | Flat in `~/.scion/` | Matches existing chat-app.yaml location, simple |
| Config reload behavior | Restart always | Simpler, predictable, avoids hot-reload bugs; restart is fast (seconds) |
| Update source location | Convention-based (`SCION_REPO_PATH`) | Starter-hub already has repo at known path; single env var keeps it simple |
| Secrets management | Hub secret backend (GitHub App pattern) | Existing infrastructure, works with both Local and GCP backends, never exposes values via API |

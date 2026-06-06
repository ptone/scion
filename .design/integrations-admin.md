# Integrations Admin

**Status:** Draft  
**Date:** 2026-06-06  
**Issue:** #115  
**Branch:** scion/integrationsadmin  
**Related:** [scion-plugins.md](scion-plugins.md), [message-broker-plugins.md](message-broker-plugins.md), [integration-env-design.md](integration-env-design.md), [admin-settings-ui-qa.md](admin-settings-ui-qa.md)

---

## 1. Problem

Scion's external integrations — Telegram, Google Chat, GitHub App, notification channels (Slack, Discord, Email, Webhooks) — are currently configured through a patchwork of manual processes:

1. **Editing config files on disk** — `settings.yaml`, per-integration YAML files, `.env` files
2. **Building and installing binaries** — extras/ modules are separate Go builds that must be compiled, installed to `/usr/local/bin/`, and discovered by the plugin manager
3. **Patching infrastructure** — Caddy reverse-proxy routes for webhook endpoints, systemd units for self-managed services
4. **Restart orchestration** — hub and integration services must restart in the correct order; a race condition exists where self-managed plugins (chat-app) attempt to connect before the hub's RPC listener is ready
5. **External service setup** — creating bots (BotFather), GitHub Apps, GCP service accounts, with no guided flow

There is no unified admin experience. An administrator who wants to set up Slack notifications, connect Telegram, and configure a GitHub App must navigate the Server Config page (raw YAML), the filesystem, and external service consoles.

The existing maintenance page (`/admin/maintenance`) proves that privileged server-side operations can be safely orchestrated from the web UI via executors with sudoers rules. This same pattern can solve the integration lifecycle problem.

### Current Installation Surface Per Integration

| Layer | Telegram | Google Chat | GitHub App | Notification Channels |
|-------|----------|-------------|------------|----------------------|
| **Binary** | `scion-plugin-telegram` (extras/) | `scion-chat-app` (extras/) | — | — |
| **Config files** | settings.yaml plugin entry | settings.yaml + scion-chat-app.yaml + chat-app.env | settings.yaml github_app block | settings.yaml notification_channels array |
| **Caddy route** | `/telegram/webhook*` → `:9094` | `/chat/*` → `:8443` | — (built into hub) | — |
| **Systemd unit** | — (hub-managed subprocess) | `scion-chat-app.service` | — | — |
| **Restart order** | Restart hub (plugin auto-launches) | Restart hub THEN chat-app (ordering critical) | Restart hub | Restart hub |
| **External setup** | BotFather: create bot, disable privacy mode | GCP: enable Chat API, create SA with chat.bot scope | GitHub: create App, set webhook URL, download PEM | Create webhook URLs, SMTP creds, etc. |
| **Post-install** | Register webhook with Telegram API | — | — | — |
| **Health check** | HealthCheck() RPC | HealthCheck() RPC (via reconnecting adapter) | Webhook signature validation | Delivery attempt success/failure |

---

## 2. Goals

- Administrators can discover, configure, activate, monitor, and manage all integrations from a single `/admin/integrations` web page — no file editing, no manual binary builds, no manual service restarts.
- The hub orchestrates restart sequencing and health verification, eliminating race conditions.
- Integration health and connectivity status is visible at a glance.
- Setup wizards guide administrators through external service prerequisites (BotFather, GitHub App creation, GCP console).
- Integration card components are reusable in onboarding flows.
- The architecture supports the current GCE/systemd/Caddy deployment topology and can be adapted for Kubernetes.

## 3. Non-Goals

- Third-party/community plugin admin UI (future work — when needed, extend via schema-driven forms or plugin-provided web components).
- Replacing the plugin system architecture itself — this builds on top of the existing `pkg/plugin` system.
- Automating external service creation (BotFather, GitHub App) — these require manual steps in external consoles.
- Multi-tenant integration scoping (per-project integrations) — this is admin-only, hub-level.

---

## 4. Design

### 4.1 Approach: Custom Executors + Shared Infrastructure

Each integration gets a purpose-built executor (following the `MaintenanceExecutor` pattern from `pkg/hub/maintenance_executors.go`), backed by shared infrastructure helpers for common operations.

```
┌───────────────────────────────────────────────────────────────────┐
│                     /admin/integrations                           │
│                                                                   │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐           │
│  │  Telegram    │  │ Google Chat  │  │  GitHub App  │  ...      │
│  │  Card        │  │  Card        │  │  Card        │           │
│  │  ┌────────┐  │  │  ┌────────┐  │  │  ┌────────┐  │           │
│  │  │ Status │  │  │  │ Status │  │  │  │ Status │  │           │
│  │  │ Config │  │  │  │ Config │  │  │  │ Config │  │           │
│  │  │Actions │  │  │  │Actions │  │  │  │Actions │  │           │
│  │  └────────┘  │  │  └────────┘  │  │  └────────┘  │           │
│  └──────────────┘  └──────────────┘  └──────────────┘           │
└───────────────┬───────────────────────────────────────────────────┘
                │
                ▼
┌───────────────────────────────────────────────────────────────────┐
│              Hub API: /api/v1/admin/integrations                 │
│                                                                   │
│  GET    /                        — list all with status           │
│  GET    /{type}                  — single integration detail      │
│  PUT    /{type}/config           — update configuration           │
│  POST   /{type}/install          — run install executor           │
│  POST   /{type}/enable           — activate (start plugin)        │
│  POST   /{type}/disable          — deactivate (stop plugin)       │
│  POST   /{type}/test             — test connectivity              │
│  POST   /{type}/restart          — restart with health check      │
│  DELETE /{type}                  — uninstall                       │
└───────────────┬───────────────────────────────────────────────────┘
                │
                ▼
┌───────────────────────────────────────────────────────────────────┐
│              Integration Executors (per integration)              │
│                                                                   │
│  TelegramExecutor     │  ChatAppExecutor    │  GitHubAppExecutor │
│  NotificationExecutor │                     │                     │
│                       │                     │                     │
│  Each uses shared infrastructure helpers:                         │
│  ┌─────────────┐ ┌──────────────┐ ┌───────────────┐             │
│  │CaddyManager │ │SystemdManager│ │SettingsPatcher│             │
│  │             │ │              │ │               │             │
│  │AddRoute()   │ │CreateUnit()  │ │Get()          │             │
│  │RemoveRoute()│ │StartService()│ │Set()          │             │
│  │Reload()     │ │StopService() │ │AppendTo()     │             │
│  │HasRoute()   │ │RestartSvc()  │ │Remove()       │             │
│  └─────────────┘ │Status()     │ │Write()        │             │
│                   └──────────────┘ └───────────────┘             │
│  ┌──────────────────────────────────────────────────┐            │
│  │              HealthMonitor                        │            │
│  │  Polls HealthCheck() RPC for each active plugin  │            │
│  │  Aggregates status across all integrations        │            │
│  │  Exposes via API for UI polling                   │            │
│  └──────────────────────────────────────────────────┘            │
└───────────────────────────────────────────────────────────────────┘
```

### 4.2 Shared Infrastructure Helpers

These helpers encapsulate the privileged operations that integration executors need, using the established sudoers pattern.

#### CaddyManager (`pkg/hub/infra/caddy.go`)

Manages Caddy reverse-proxy routes for integration webhook endpoints.

```go
type CaddyManager struct {
    caddyfilePath string // default: /etc/caddy/Caddyfile
    logger        *slog.Logger
}

func (m *CaddyManager) HasRoute(pathMatch string) (bool, error)
func (m *CaddyManager) AddRoute(pathMatch, upstream string) error
func (m *CaddyManager) RemoveRoute(pathMatch string) error
func (m *CaddyManager) Reload() error  // sudo systemctl reload caddy
```

Caddyfile modification is idempotent (same pattern as `install.sh`). Routes are added as `handle` blocks before the catch-all hub proxy. The manager parses the Caddyfile to check for existing routes before modifying.

Sudoers rule: `scion ALL=(root) NOPASSWD: /bin/systemctl reload caddy`

#### SystemdManager (`pkg/hub/infra/systemd.go`)

Manages systemd service units for self-managed integrations.

```go
type SystemdManager struct {
    logger *slog.Logger
}

func (m *SystemdManager) CreateUnit(name string, unit *UnitConfig) error
func (m *SystemdManager) RemoveUnit(name string) error
func (m *SystemdManager) StartService(name string) error
func (m *SystemdManager) StopService(name string) error
func (m *SystemdManager) RestartService(name string) error
func (m *SystemdManager) Status(name string) (ServiceStatus, error)
func (m *SystemdManager) DaemonReload() error

type UnitConfig struct {
    Description string
    ExecStart   string
    User        string
    Group       string
    EnvFile     string
    RestartSec  int
}

type ServiceStatus struct {
    Active    string // "active", "inactive", "failed"
    SubState  string // "running", "dead", "failed"
    Since     time.Time
    MainPID   int
}
```

Sudoers rules (per integration service):
```
scion ALL=(root) NOPASSWD: /bin/systemctl start scion-chat-app
scion ALL=(root) NOPASSWD: /bin/systemctl stop scion-chat-app
scion ALL=(root) NOPASSWD: /bin/systemctl restart scion-chat-app
scion ALL=(root) NOPASSWD: /bin/systemctl status scion-chat-app
scion ALL=(root) NOPASSWD: /bin/systemctl daemon-reload
```

Unit files are written to a staging directory and installed via `sudo install -m 644`.

#### SettingsPatcher (`pkg/hub/infra/settings.go`)

Reads, modifies, and writes `settings.yaml` sections without losing other content.

```go
type SettingsPatcher struct {
    settingsPath string // default: ~/.scion/settings.yaml
    logger       *slog.Logger
}

func (p *SettingsPatcher) Get(path string) (interface{}, error)
func (p *SettingsPatcher) Set(path string, value interface{}) error
func (p *SettingsPatcher) AppendTo(path string, value interface{}) error
func (p *SettingsPatcher) Remove(path string) error
func (p *SettingsPatcher) Write() error
```

Uses the existing koanf-based settings infrastructure (`pkg/config`). Reads the current file, applies modifications in memory, and writes back atomically (write to temp, rename). The `UpdateSetting()` function in `pkg/config` already handles this for scalar values; the patcher extends it to support nested objects and arrays.

#### HealthMonitor (`pkg/hub/infra/health.go`)

Periodically polls plugin health and aggregates status for the API.

```go
type HealthMonitor struct {
    pluginManager *plugin.Manager
    statuses      map[string]*IntegrationStatus
    mu            sync.RWMutex
    interval      time.Duration // default: 30s
    stopCh        chan struct{}
}

type IntegrationStatus struct {
    Type           string              // "telegram", "googlechat", etc.
    Category       string              // "broker-plugin", "notification", "github-app"
    Installed      bool
    Enabled        bool
    Connected      bool
    Health         *plugin.HealthStatus // from HealthCheck() RPC
    LastSeen       time.Time
    LastError      string
    ConfigSummary  map[string]string   // safe-to-display config (secrets redacted)
    BinaryVersion  string              // from GetInfo()
    SystemdStatus  *ServiceStatus      // for self-managed only
}

func (m *HealthMonitor) Start()
func (m *HealthMonitor) Stop()
func (m *HealthMonitor) GetAll() map[string]*IntegrationStatus
func (m *HealthMonitor) Get(integrationType string) *IntegrationStatus
```

Health is determined by combining:
- Plugin RPC `HealthCheck()` result (healthy/degraded/unhealthy)
- Systemd service status (for self-managed plugins)
- RPC connection state (connected/disconnected from plugin manager)
- Last successful message publish/receive timestamp

### 4.3 Integration Executors

Each integration has a purpose-built executor that uses the shared helpers. Executors implement a common interface but have integration-specific logic.

```go
type IntegrationExecutor interface {
    // Type returns the integration identifier (e.g., "telegram").
    Type() string

    // Category returns the integration category.
    Category() string // "broker-plugin", "notification", "github-app"

    // ExternalSetupSteps returns the manual steps the user must complete
    // in external services before installation can proceed.
    ExternalSetupSteps() []SetupStep

    // ConfigSchema returns the configuration fields for this integration,
    // used by the API to validate input and by the UI to render forms.
    ConfigSchema() []ConfigField

    // Install performs the full installation: build binary (if applicable),
    // install to path, patch Caddy, create systemd unit, update settings,
    // restart services in correct order, verify health.
    Install(ctx context.Context, config map[string]string, logger io.Writer) error

    // Configure updates the integration's configuration without reinstalling.
    Configure(ctx context.Context, config map[string]string, logger io.Writer) error

    // Enable activates a configured-but-disabled integration.
    Enable(ctx context.Context, logger io.Writer) error

    // Disable deactivates the integration without removing config.
    Disable(ctx context.Context, logger io.Writer) error

    // Uninstall removes the integration completely.
    Uninstall(ctx context.Context, logger io.Writer) error

    // Test validates connectivity without making persistent changes.
    Test(ctx context.Context, config map[string]string) (*TestResult, error)

    // Restart stops and starts the integration with health verification.
    Restart(ctx context.Context, logger io.Writer) error
}

type SetupStep struct {
    Title       string
    Description string
    Link        string // external URL (e.g., "https://t.me/BotFather")
    InputFields []ConfigField // fields the user provides after this step
}

type ConfigField struct {
    Key          string
    Label        string
    Type         string // "string", "secret", "url", "number", "boolean", "select"
    Required     bool
    Default      string
    Placeholder  string
    HelpText     string
    Options      []string // for "select" type
    AutoGenerate bool     // generate a random value (e.g., webhook_secret)
    Validate     string   // regex pattern for validation
}

type TestResult struct {
    Success bool
    Message string
    Details map[string]string
}
```

#### TelegramExecutor

```go
type TelegramExecutor struct {
    caddy    *CaddyManager
    settings *SettingsPatcher
    health   *HealthMonitor
    plugins  *plugin.Manager
    repoPath string // path to scion source checkout (for building)
}

func (e *TelegramExecutor) Install(ctx context.Context, config map[string]string, logger io.Writer) error {
    // 1. Validate config (bot_token required)
    // 2. Auto-generate webhook_secret if not provided
    // 3. Derive webhook_url from SCION_SERVER_BASE_URL if not provided
    // 4. Build binary: go build -o scion-plugin-telegram ./cmd/scion-plugin-telegram
    //    (from repoPath/extras/scion-telegram/)
    // 5. Install binary: sudo install -m 755 ... /usr/local/bin/scion-plugin-telegram
    // 6. Patch settings.yaml:
    //    - Set server.plugins.broker.telegram.config with all config values
    //    - Append "telegram" to server.message_broker.types
    //    - Set server.message_broker.enabled = true
    // 7. Add Caddy route: /telegram/webhook* → localhost:{webhook_listen_port}
    // 8. Reload Caddy
    // 9. Restart hub (fire-and-forget, same as RebuildServerExecutor)
    //    — plugin auto-launches as managed subprocess
    // 10. Wait for health (poll HealthCheck RPC with backoff)
    // 11. Register webhook with Telegram API:
    //     POST https://api.telegram.org/bot{token}/setWebhook
    //     ?url={webhook_url}&secret_token={webhook_secret}
}

func (e *TelegramExecutor) ExternalSetupSteps() []SetupStep {
    return []SetupStep{
        {
            Title:       "Create Telegram Bot",
            Description: "Open @BotFather in Telegram, send /newbot, and follow the prompts. Copy the bot token.",
            Link:        "https://t.me/BotFather",
            InputFields: []ConfigField{
                {Key: "bot_token", Label: "Bot Token", Type: "secret", Required: true,
                 Placeholder: "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"},
            },
        },
        {
            Title:       "Disable Privacy Mode",
            Description: "In BotFather, send /mybots → select your bot → Bot Settings → Group Privacy → Turn off. This allows the bot to see @mentions in group chats.",
        },
    }
}

func (e *TelegramExecutor) ConfigSchema() []ConfigField {
    return []ConfigField{
        {Key: "bot_token", Label: "Bot Token", Type: "secret", Required: true},
        {Key: "webhook_url", Label: "Webhook URL", Type: "url", Required: true,
         Default: "${SCION_SERVER_BASE_URL}/telegram/webhook",
         HelpText: "Public URL where Telegram sends updates"},
        {Key: "webhook_secret", Label: "Webhook Secret", Type: "secret",
         AutoGenerate: true, HelpText: "HMAC secret for webhook verification"},
        {Key: "webhook_listen", Label: "Listen Address", Type: "string",
         Default: ":9094", HelpText: "Local address for the webhook HTTP server"},
        {Key: "db_path", Label: "Database Path", Type: "string",
         Default: "/var/lib/scion/telegram_v2.db"},
        {Key: "inbound_mode", Label: "Inbound Mode", Type: "select",
         Default: "webhook", Options: []string{"webhook", "polling"}},
    }
}
```

#### ChatAppExecutor

```go
func (e *ChatAppExecutor) Install(ctx context.Context, config map[string]string, logger io.Writer) error {
    // 1. Validate config (project_id, service_account_email required)
    // 2. Build binary from extras/scion-chat-app/
    // 3. Install binary to /usr/local/bin/scion-chat-app
    // 4. Generate scion-chat-app.yaml config file
    // 5. Create systemd unit scion-chat-app.service
    // 6. Patch settings.yaml:
    //    - Set server.plugins.broker.googlechat (self_managed: true, address: localhost:9090)
    //    - Append "googlechat" to server.message_broker.types
    // 7. Add Caddy route: /chat/* → localhost:8443
    // 8. Reload Caddy
    // 9. Restart hub (the critical ordering step):
    //    a. Stop chat-app service first (if running)
    //    b. Restart hub
    //    c. Wait for hub health (GET /api/v1/health)
    //    d. Start chat-app service
    //    e. Wait for chat-app RPC connection (plugin manager reconnect)
    //    f. Verify health via HealthCheck() RPC
}
```

The restart ordering in step 9 is the key improvement over today's manual process. The executor ensures the hub's RPC listener is ready before starting the chat-app, eliminating the race condition.

#### GitHubAppExecutor

```go
func (e *GitHubAppExecutor) Install(ctx context.Context, config map[string]string, logger io.Writer) error {
    // 1. Validate config (app_id, private_key or private_key_path, webhook_secret)
    // 2. Write PEM key to secure location if provided as string
    // 3. Patch settings.yaml with server.github_app block
    // 4. Restart hub
    // 5. Verify: GET /api/v1/github-app returns valid config
}
```

#### NotificationChannelExecutor

```go
func (e *NotificationChannelExecutor) Install(ctx context.Context, config map[string]string, logger io.Writer) error {
    // 1. Validate config based on channel type (slack: webhook_url, email: smtp_host, etc.)
    // 2. Append to server.notification_channels array in settings.yaml
    // 3. Test delivery (send a test notification)
    // 4. Restart hub (or hot-reload if supported)
}
```

Notification channels are the simplest case — no binaries, no Caddy, no systemd. Config-only with a test endpoint.

### 4.4 Hub API Design

New API endpoints under `/api/v1/admin/integrations`, admin-only (same auth as other `/admin/` endpoints).

#### List All Integrations

```
GET /api/v1/admin/integrations

Response:
{
  "integrations": [
    {
      "type": "telegram",
      "category": "broker-plugin",
      "installed": true,
      "enabled": true,
      "status": {
        "connected": true,
        "health": "healthy",
        "message": "Connected, 142 messages delivered",
        "last_seen": "2026-06-06T12:00:00Z",
        "details": {
          "webhook_registered": "true",
          "groups_linked": "3",
          "users_linked": "5"
        }
      },
      "config_summary": {
        "webhook_url": "https://hub.example.com/telegram/webhook",
        "inbound_mode": "webhook",
        "bot_token": "***REDACTED***"
      },
      "binary_version": "0.2.1",
      "systemd_status": null
    },
    {
      "type": "googlechat",
      "category": "broker-plugin",
      "installed": true,
      "enabled": true,
      "status": {
        "connected": true,
        "health": "healthy",
        "message": "RPC connected to localhost:9090"
      },
      "systemd_status": {
        "active": "active",
        "sub_state": "running",
        "since": "2026-06-06T08:00:00Z",
        "main_pid": 12345
      }
    },
    {
      "type": "github-app",
      "category": "github-app",
      "installed": true,
      "enabled": true,
      "status": {
        "connected": true,
        "health": "healthy",
        "message": "App ID 12345, 3 installations"
      }
    },
    {
      "type": "slack",
      "category": "notification",
      "installed": true,
      "enabled": true,
      "status": {
        "connected": true,
        "health": "healthy",
        "message": "Last delivery: 2m ago"
      }
    }
  ],
  "available": ["telegram", "googlechat", "github-app", "slack", "discord", "email", "webhook"]
}
```

#### Get Integration Detail

```
GET /api/v1/admin/integrations/{type}

Response: Single integration object (same structure as above) plus:
{
  ...
  "config": { /* full config, secrets redacted */ },
  "config_schema": [ /* ConfigField array for form rendering */ ],
  "external_setup_steps": [ /* SetupStep array */ ],
  "install_log": "last install/update log output"
}
```

#### Update Configuration

```
PUT /api/v1/admin/integrations/{type}/config
Body: { "config": { "bot_token": "...", "webhook_url": "..." } }

Response: { "success": true, "restart_required": true }
```

Config is validated by the executor's `ConfigSchema()`. If the change requires a restart (most do), `restart_required: true` tells the UI to offer a restart button.

#### Install / Enable / Disable / Uninstall / Test / Restart

```
POST /api/v1/admin/integrations/{type}/install
POST /api/v1/admin/integrations/{type}/enable
POST /api/v1/admin/integrations/{type}/disable
POST /api/v1/admin/integrations/{type}/test
POST /api/v1/admin/integrations/{type}/restart
DELETE /api/v1/admin/integrations/{type}

Body (install): { "config": { ... } }
Body (test): { "config": { ... } }

Response: { 
  "success": true, 
  "log": "step-by-step execution log",
  "status": { /* updated IntegrationStatus */ }
}
```

Install and restart are long-running operations. The API returns immediately with an operation ID, and the UI polls for completion (same pattern as maintenance operations).

```
POST /api/v1/admin/integrations/{type}/install
Response: { "operation_id": "integ-op-abc123" }

GET /api/v1/admin/integrations/operations/{id}
Response: { 
  "id": "integ-op-abc123",
  "type": "telegram",
  "action": "install",
  "status": "running",  // "pending", "running", "completed", "failed"
  "log": "=> Building binary\n=> Installing to /usr/local/bin/\n...",
  "started_at": "...",
  "completed_at": null
}
```

### 4.5 Web UI Design

New page: `/admin/integrations` — `web/src/components/pages/admin-integrations.ts`

#### Page Layout

```
┌──────────────────────────────────────────────────────────┐
│  🔌 Integrations                                         │
│                                                          │
│  ┌─ Chat Integrations ─────────────────────────────────┐ │
│  │                                                      │ │
│  │  ┌────────────────┐  ┌────────────────┐             │ │
│  │  │ 🤖 Telegram    │  │ 💬 Google Chat │             │ │
│  │  │ ● Connected    │  │ ● Connected    │             │ │
│  │  │ 3 groups       │  │ 2 spaces       │             │ │
│  │  │ [Configure]    │  │ [Configure]    │             │ │
│  │  └────────────────┘  └────────────────┘             │ │
│  │                                                      │ │
│  └──────────────────────────────────────────────────────┘ │
│                                                          │
│  ┌─ Code Integration ──────────────────────────────────┐ │
│  │                                                      │ │
│  │  ┌────────────────┐                                  │ │
│  │  │ 🐙 GitHub App  │                                  │ │
│  │  │ ● Connected    │                                  │ │
│  │  │ App ID: 12345  │                                  │ │
│  │  │ [Configure]    │                                  │ │
│  │  └────────────────┘                                  │ │
│  │                                                      │ │
│  └──────────────────────────────────────────────────────┘ │
│                                                          │
│  ┌─ Notifications ─────────────────────────────────────┐ │
│  │                                                      │ │
│  │  ┌────────────────┐  ┌────────────────┐             │ │
│  │  │ 🔔 Slack       │  │ 🎮 Discord     │             │ │
│  │  │ ● Active       │  │ ○ Not config'd │             │ │
│  │  │ [Configure]    │  │ [Set Up]       │             │ │
│  │  └────────────────┘  └────────────────┘             │ │
│  │  ┌────────────────┐  ┌────────────────┐             │ │
│  │  │ 📧 Email       │  │ 🔗 Webhook     │             │ │
│  │  │ ○ Not config'd │  │ ● Active       │             │ │
│  │  │ [Set Up]       │  │ [Configure]    │             │ │
│  │  └────────────────┘  └────────────────┘             │ │
│  │                                                      │ │
│  └──────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

#### Integration Card States

Each card shows one of:

- **Not installed** — "Set Up" button, grayed out, shows what the integration does
- **Installing** — progress indicator, step-by-step log output
- **Configured but disabled** — "Enable" button, config summary visible
- **Connected/Healthy** — green indicator, config summary, "Configure" / "Disable" buttons
- **Degraded** — yellow indicator, health message, "Restart" button
- **Error/Disconnected** — red indicator, error message, "Restart" / "Reconfigure" buttons

#### Setup Wizard Flow (Example: Telegram)

Clicking "Set Up" on an unconfigured Telegram card opens a multi-step dialog:

```
Step 1 of 3: Create Telegram Bot
─────────────────────────────────
Open @BotFather in Telegram and create a new bot.
Then disable Privacy Mode in Bot Settings → Group Privacy.

[Open BotFather ↗]

Paste your bot token:
┌─────────────────────────────────────────┐
│ 123456:ABC-DEF1234ghIkl-zyx57W2v1u123  │
└─────────────────────────────────────────┘

                              [Cancel] [Next →]
```

```
Step 2 of 3: Configure Options
─────────────────────────────────
Webhook URL:   [https://hub.example.com/telegram/webhook  ]
               (auto-derived from hub URL)

Listen Port:   [9094        ]
Database Path: [/var/lib/scion/telegram_v2.db             ]

Webhook Secret: [auto-generated ●●●●●●●●  ] [Regenerate]

                              [← Back] [Next →]
```

```
Step 3 of 3: Install
─────────────────────────────────
Ready to install Telegram integration.

This will:
  ✓ Build the Telegram plugin binary
  ✓ Install to /usr/local/bin/
  ✓ Add Caddy webhook route
  ✓ Update hub configuration
  ✓ Restart the hub server
  ✓ Register webhook with Telegram

                              [← Back] [Install]
```

```
Installing...
─────────────────────────────────
=> Building scion-plugin-telegram binary
   go build -o scion-plugin-telegram ./cmd/scion-plugin-telegram
=> Installing binary to /usr/local/bin/
=> Adding Caddy route: /telegram/webhook* → localhost:9094
=> Reloading Caddy
=> Updating hub settings
=> Restarting hub server
=> Waiting for hub to become healthy... ✓
=> Registering webhook with Telegram API... ✓
=> Verifying plugin health... ✓

✓ Telegram integration installed successfully!

                                        [Done]
```

#### Component Architecture

```
web/src/components/pages/admin-integrations.ts
  └── Renders integration cards grouped by category
  └── Handles API polling for status updates

web/src/components/integrations/
  ├── integration-card.ts          — reusable card component
  ├── integration-setup-wizard.ts  — multi-step setup dialog
  ├── integration-config-form.ts   — form rendered from ConfigSchema
  ├── integration-status-badge.ts  — status indicator (connected/error/etc.)
  ├── integration-operation-log.ts — log viewer for install/restart output
  ├── telegram-config.ts           — Telegram-specific config panel
  ├── googlechat-config.ts         — Google Chat-specific config panel
  ├── github-app-config.ts         — GitHub App config panel
  └── notification-config.ts       — Notification channel config panel
```

The `integration-card.ts` component is designed to be reusable in onboarding flows — it can be embedded in a setup wizard page that guides first-time admins through connecting their first integration.

#### Navigation

Add to the Admin section in `web/src/components/shared/nav.ts`:

```typescript
{ path: '/admin/integrations', label: 'Integrations', icon: 'plug-fill' }
```

Position it after "Hub Settings" and before "Server Config" — integrations are a more common admin task than raw server configuration.

### 4.6 Restart Orchestration

The most critical improvement. Today's race condition between hub and self-managed plugins is eliminated by having the executor manage the full sequence.

#### For Hub-Managed Plugins (Telegram)

Simple: restart the hub. The plugin manager discovers and launches the plugin binary as a subprocess during hub startup. The executor uses fire-and-forget `systemctl restart scion-hub` (same as `RebuildServerExecutor`), then the UI polls for the hub to come back healthy.

#### For Self-Managed Plugins (Google Chat)

The executor runs the steps in sequence:

```
1. Stop chat-app:     sudo systemctl stop scion-chat-app
2. Restart hub:       sudo systemctl restart scion-hub  (fire-and-forget)
3. Wait for hub:      Poll GET /api/v1/health until 200 (with timeout)
4. Start chat-app:    sudo systemctl start scion-chat-app
5. Wait for connect:  Poll plugin manager until RPC connection established
6. Verify health:     Call HealthCheck() RPC
```

Since step 2 terminates the current process (the hub is restarting itself), the executor must persist its state to disk so the operation can resume after restart. The operation status is stored in the hub database (same `maintenance_operations` / `maintenance_runs` tables or a parallel `integration_operations` table), and the hub checks for pending operations on startup.

**Restart resume mechanism:**

```go
type PendingOperation struct {
    ID              string
    IntegrationType string
    Action          string    // "install", "restart", etc.
    CurrentStep     int
    Config          map[string]string
    StartedAt       time.Time
}
```

On hub startup, check for pending operations. If found, resume from the current step (e.g., step 4: start chat-app).

#### For Config-Only Integrations (Notification Channels, GitHub App)

Restart the hub. No other services involved.

### 4.7 Sudoers Rules

Extend the existing sudoers file (`/etc/sudoers.d/scion-rebuild-server` or create `/etc/sudoers.d/scion-integrations`):

```sudoers
# Scion integration management privileges.
# Binary installation (staging path → /usr/local/bin/)
scion ALL=(root) NOPASSWD: /usr/bin/install -m 755 /home/scion/*/scion-plugin-telegram /usr/local/bin/scion-plugin-telegram
scion ALL=(root) NOPASSWD: /usr/bin/install -m 755 /home/scion/*/scion-chat-app /usr/local/bin/scion-chat-app

# Systemd service management
scion ALL=(root) NOPASSWD: /bin/systemctl start scion-chat-app
scion ALL=(root) NOPASSWD: /bin/systemctl stop scion-chat-app
scion ALL=(root) NOPASSWD: /bin/systemctl restart scion-chat-app
scion ALL=(root) NOPASSWD: /bin/systemctl status scion-chat-app
scion ALL=(root) NOPASSWD: /bin/systemctl daemon-reload

# Caddy management
scion ALL=(root) NOPASSWD: /bin/systemctl reload caddy

# Systemd unit file installation
scion ALL=(root) NOPASSWD: /usr/bin/install -m 644 /tmp/scion-*.service /etc/systemd/system/
```

These rules are provisioned by the starter-hub script (`scripts/starter-hub/gce-start-hub.sh`), extending the existing pattern.

### 4.8 Onboarding Integration

The integration card components are designed to work in two contexts:

1. **Admin page** (`/admin/integrations`) — full management for all integrations
2. **Onboarding wizard** (`/setup/integrations` or embedded in first-run flow) — guided setup for initial configuration

First-run detection: if no broker plugins are configured and no notification channels exist, the dashboard shows a prompt: "Connect your first integration" linking to the setup wizard.

The setup wizard presents integrations in priority order:
1. GitHub App (code integration — most fundamental)
2. Chat integration (Telegram or Google Chat — enables conversational agent interaction)
3. Notification channels (alerts and monitoring)

---

## 5. Security Considerations

- **Admin-only access:** All `/api/v1/admin/integrations` endpoints require admin role (same as existing admin endpoints).
- **Secret handling:** Sensitive config values (bot tokens, webhook secrets, private keys) are stored in `settings.yaml` (same as today) and redacted in API responses. The UI never displays raw secrets after initial entry.
- **Sudoers scope:** Each privileged operation is narrowly scoped to specific binaries and paths. No wildcard sudo.
- **Config validation:** Executors validate all config before applying. Invalid config is rejected before any changes are made.
- **Audit logging:** All integration operations (install, configure, enable, disable, restart) are logged via the hub's request logger with the admin user's identity.
- **Staging pattern:** Binary installation uses a staging path with `sudo install`, avoiding direct writes to `/usr/local/bin/` (same pattern as `RebuildServerExecutor`).

---

## 6. Implementation Phases

### Phase 1: Shared Infrastructure + API Foundation (1-2 weeks)

**Deliverables:**
- `pkg/hub/infra/` package with CaddyManager, SystemdManager, SettingsPatcher
- HealthMonitor that polls plugin HealthCheck() and aggregates status
- API endpoints: `GET /api/v1/admin/integrations` (list with status), `GET /{type}` (detail)
- Integration status data model (combining plugin health, systemd status, config state)
- Extended sudoers provisioning in `gce-start-hub.sh`

**Verification:**
- API returns accurate status for currently-configured integrations
- Health monitor detects plugin disconnect/reconnect

### Phase 2: Notification Channel Admin (1 week)

**Deliverables:**
- NotificationChannelExecutor (simplest case — config-only, no binaries/Caddy/systemd)
- API endpoints: PUT config, POST test, POST install (add channel), DELETE (remove channel)
- Web UI: notification channel cards with configure/test/add/remove
- Test endpoint sends a test notification and reports success/failure

**Rationale:** Start with the simplest integration type to validate the full stack (API → executor → UI) before tackling the complex plugin-based integrations.

### Phase 3: Telegram Admin (1-2 weeks)

**Deliverables:**
- TelegramExecutor with full lifecycle (build, install, Caddy, settings, restart, webhook registration)
- Setup wizard UI (3-step flow: BotFather → configure → install)
- Status card with health, connected groups, linked users
- Test endpoint validates webhook connectivity
- Restart with hub-managed plugin lifecycle

### Phase 4: Google Chat Admin (1-2 weeks)

**Deliverables:**
- ChatAppExecutor with self-managed plugin lifecycle
- Restart orchestration (stop chat-app → restart hub → wait → start chat-app)
- Restart resume mechanism (persist operation state across hub restart)
- Setup wizard with GCP prerequisites (Chat API, service account)
- Systemd unit management (create, start, stop, status)

### Phase 5: GitHub App Admin (1 week)

**Deliverables:**
- GitHubAppExecutor (config + PEM key management)
- Setup wizard with GitHub App creation guide
- Status card showing installations, webhook activity

### Phase 6: Onboarding Integration (1 week)

**Deliverables:**
- First-run detection (no integrations configured → show setup prompt)
- Setup wizard page (`/setup/integrations`) with prioritized integration flow
- Integration cards embedded in onboarding context

---

## 7. Open Questions

1. **Hot-reload vs restart:** Should config changes trigger a hub restart or a hot-reload of the plugin? Hot-reload is more user-friendly but requires new plugin manager capabilities (Reconfigure RPC). Recommendation: start with restart (proven pattern), add hot-reload as a follow-up.

2. **Binary distribution:** Should integration binaries be built from source (requires Go toolchain on the server) or downloaded as pre-built releases? Recommendation: support both — build from source if `extras/` is available (development), download from GCS/GitHub releases for production.

3. **Kubernetes adaptation:** On Kubernetes, there are no systemd units or Caddyfile. How should the infrastructure helpers adapt? Recommendation: make SystemdManager and CaddyManager interface-based with pluggable backends (systemd vs Kubernetes Deployment/Service, Caddy vs Ingress). Defer Kubernetes implementation to the K8s milestone.

4. **Operation state persistence:** Should integration operations use the existing `maintenance_runs` table or a separate table? Recommendation: separate `integration_operations` table — different schema needs (config snapshots, resume state), and keeps maintenance and integration concerns decoupled.

5. **Notification channel hot-reload:** Notification channels don't require binary or infra changes — can the hub reload them without a full restart? Recommendation: yes, implement channel hot-reload in Phase 2 as it's low-risk and high-value (the NotificationDispatcher can be reinitialized with new channel configs).

---

## 8. Future Extensions

- **Schema-driven forms for third-party plugins:** Extend `PluginInfo` with a `ConfigSchema` field. The admin page renders generic forms from the schema for plugins that don't have purpose-built UI.
- **Plugin-provided web components:** Plugins can optionally register a web component URL via `GetInfo()`. The admin page lazy-loads it for advanced admin features.
- **Integration marketplace:** Discovery of available integration plugins from a registry.
- **Per-project integration scoping:** Allow project owners to configure integrations scoped to their project (e.g., project-specific Slack channels).
- **Delivery logs:** Audit trail of messages sent/received per integration, viewable in the admin UI.

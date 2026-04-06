# Chat App Integration for Scion

**Created:** 2026-04-05
**Status:** Draft
**Related:** `.design/hosted-architecture.md`, `.design/agent-progeny-secret-access.md`, `.design/message-broker-plugin-evolution.md`

---

## Overview

This design describes a standalone chat application that bridges enterprise chat platforms (Google Chat, Slack) with the Scion Hub. It acts as both a **message broker plugin** for real-time agent communication and an **API proxy** for operational commands — enabling users to interact with agents and manage groves directly from their chat workspace.

The first implementation targets **Google Chat** using the `cloud.google.com/go/chat/apiv1` SDK. A second implementation will target **Slack**. Both share a common abstraction layer.

### Goals

- Enable bidirectional messaging between chat users and Scion agents
- Support operational commands (start, stop, delete agents) via slash commands and @mentions
- Map chat users to Hub users for authorized impersonation
- Link chat spaces to groves for scoped interaction
- Handle `ask_user` / `WAITING_FOR_INPUT` agent states via chat dialogs
- Run as a standalone process using the `hubclient` library

### Non-Goals (MVP)

- Full CLI parity (only a subset of commands)
- Complex agent creation workflows (template browsing, multi-step wizards)
- File/attachment relay to agents
- Multi-hub federation

---

## Architecture

### Module Structure

The chat app ships as a **separate Go module** under the `extras/` directory:

```
extras/scion-chat-app/
├── go.mod                    # github.com/GoogleCloudPlatform/scion/extras/scion-chat-app
├── go.sum
├── cmd/
│   └── scion-chat-app/
│       └── main.go           # Binary entrypoint
├── internal/
│   ├── chatapp/              # Core engine, event router, command router
│   ├── googlechat/           # Google Chat adapter (Messenger impl)
│   ├── slack/                # Slack adapter (future, Messenger impl)
│   ├── identity/             # User mapping, impersonation, UAT minting
│   ├── state/                # SQLite state management
│   └── cards/                # Platform-agnostic card model and rendering
├── Dockerfile
└── cloudbuild.yaml
```

This keeps the Google Chat SDK (`cloud.google.com/go/chat/apiv1`) and future Slack SDK out of the main module's dependency tree. The chat app imports from the main module for shared packages:

```go
import (
    "github.com/GoogleCloudPlatform/scion/pkg/hubclient"
    "github.com/GoogleCloudPlatform/scion/pkg/plugin"
    "github.com/GoogleCloudPlatform/scion/pkg/secret"
    "github.com/GoogleCloudPlatform/scion/pkg/messages"
)
```

For local development, a `replace` directive in `go.mod` points to the repo root:

```
replace github.com/GoogleCloudPlatform/scion => ../../
```

### System Diagram

```
┌─────────────────────────────────────────────────────────┐
│                    Chat Platform                         │
│              (Google Chat / Slack)                        │
└──────────┬──────────────────────────────┬───────────────┘
           │  Events (webhooks/WS)        │  API calls
           ▼                              ▲
┌──────────────────────────────────────────────────────────┐
│                  scion-chat-app                           │
│            (extras/scion-chat-app module)                 │
│                                                           │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────┐  │
│  │  Platform     │  │  Command     │  │  Notification  │  │
│  │  Adapter      │  │  Router      │  │  Relay         │  │
│  │  (Messenger)  │  │              │  │                │  │
│  └──────┬───────┘  └──────┬───────┘  └───────┬────────┘  │
│         │                 │                   │           │
│  ┌──────┴─────────────────┴───────────────────┴────────┐  │
│  │                  Core Engine                         │  │
│  │  - User mapping    - Space-grove links               │  │
│  │  - Auth (hubclient) - Card/Block rendering           │  │
│  └──────┬─────────────────────────────────────┬────────┘  │
│         │                                     │           │
│  ┌──────┴──────────────────────────────┐  ┌───┴────────┐  │
│  │         hubclient.Client            │  │  Broker    │  │
│  │  AgentService, GroveService,        │  │  Plugin    │  │
│  │  NotificationService,              │  │  (RPC)     │  │
│  │  MessageService, SubscriptionService│  │            │  │
│  └──────────────┬──────────────────────┘  └───┬────────┘  │
└─────────────────┼─────────────────────────────┼──────────┘
                  │  HTTPS                      │  RPC
                  ▼                             ▼
           ┌─────────────────────────────────────────┐
           │              Scion Hub                   │
           │  ┌─────────────────┐ ┌────────────────┐  │
           │  │  HTTP Handlers  │ │ Plugin Manager │  │
           │  │                 │ │ (self-managed) │  │
           │  └─────────────────┘ └────────────────┘  │
           └─────────────────────────────────────────┘
```

### Process Model

The chat app runs as a **standalone long-lived process** (`scion-chat-app`), typically deployed alongside the Hub. It operates under three distinct identity contexts:

1. **Hub admin user** — A configured Hub user (typically an admin) used for system-level Hub API operations such as notification subscription management, grove lookups, and other administrative calls that are not on behalf of a specific chat user.
2. **Operational environment account** — The GCP service account the chat app process runs as. This identity is used for accessing infrastructure resources like GCP Secret Manager (to read signing key material for minting UATs) and for structured logging. This is not a Hub identity — it is the machine-level credential.
3. **Impersonated chat users** — For user-initiated commands and actions, the chat app impersonates the linked Hub user. It mints short-lived scoped UATs using the signing key accessed via the operational environment account, then makes Hub API calls as that user.

The process:

1. Starts its RPC server for the `MessageBrokerPluginInterface`
2. Authenticates to the Hub as the configured admin user for system-level operations
3. The Hub's plugin manager connects to the app's RPC server (self-managed plugin mode) and calls `Configure()`
4. Uses the operational environment account (GCP service account) for secret backend access and logging
5. Maintains a persistent SSE connection for real-time agent events
6. Receives chat platform events via HTTP webhooks (Google Chat) or WebSocket (Slack)
7. Receives agent messages and notifications via the broker plugin's `Publish()` method
8. Translates between chat-native formats and Hub API calls, impersonating linked Hub users for user-initiated actions

### Relationship to Message Broker Plugin

The chat app is a **superset** of a message broker plugin. It implements the `MessageBrokerPluginInterface` for real-time message transport while also running its own HTTP server, user identity mapping, command routing, and rich UI rendering — capabilities that are outside the plugin interface scope.

This follows the **single-binary model** (Option C from `.design/chat-plugin-tradeoffs.md`): the plugin IS the app. The core plugin machinery supports this via three evolutions implemented in `.design/message-broker-plugin-evolution.md`:

1. **Self-managed plugin lifecycle** (Evolution 3) — The chat app manages its own process. The Hub connects to the app's RPC server at a configured address rather than starting it as a child process. The plugin manager calls `Configure()` and `Close()` but does not own the process.

2. **Notification routing through broker** (Evolution 2) — When the broker plugin is active, the `NotificationDispatcher` routes user-targeted notifications through the broker's `Publish()` path. This avoids double-delivery and enables the chat app to render notifications as rich interactive cards.

3. **Plugin-initiated subscriptions** (Evolution 1) — The chat app uses the `HostCallbacks` interface to dynamically subscribe to grove topics as spaces are linked. When a user runs `/scion link production`, the app calls `RequestSubscription()` to start receiving messages for that grove immediately.

The plugin interface remains a narrow transport contract (`Configure`, `Publish`, `Subscribe`, `Unsubscribe`, `Close`, `GetInfo`, `HealthCheck`). Everything beyond transport — card rendering, identity mapping, command parsing, dialog state — is application logic in the chat app binary, not extensions to the plugin API.

---

## Core Abstraction Layer

### Messenger Interface

The platform abstraction that decouples business logic from chat APIs:

```go
package chatapp

import "context"

// Messenger abstracts chat platform operations.
type Messenger interface {
    // SendMessage posts a text or card message to a chat space/channel.
    SendMessage(ctx context.Context, req SendMessageRequest) (string, error)

    // SendCard posts a structured card (agent status, notifications).
    SendCard(ctx context.Context, spaceID string, card Card) (string, error)

    // UpdateMessage edits an existing message (for status updates).
    UpdateMessage(ctx context.Context, messageID string, req SendMessageRequest) error

    // OpenDialog presents an interactive dialog (for ask_user, command input).
    OpenDialog(ctx context.Context, triggerID string, dialog Dialog) error

    // UpdateDialog updates an open dialog's content.
    UpdateDialog(ctx context.Context, triggerID string, dialog Dialog) error

    // GetUser retrieves chat platform user info for identity mapping.
    GetUser(ctx context.Context, userID string) (*ChatUser, error)

    // SetAgentIdentity configures how agent messages appear.
    // Google Chat: sets card header. Slack: overrides username/avatar.
    SetAgentIdentity(ctx context.Context, agent AgentIdentity) error
}

type SendMessageRequest struct {
    SpaceID    string
    ThreadID   string // Native platform thread ID; replies to this thread if set
    Text       string
    Card       *Card  // Rich card (optional)
    AgentID    string // For per-agent identity (Slack only)
}

type AgentIdentity struct {
    Slug     string
    Name     string
    IconURL  string
    GroveID  string
}

type ChatUser struct {
    PlatformID  string // Google Chat user ID or Slack user ID
    DisplayName string
    Email       string
}
```

### Card Model

A platform-agnostic card representation that maps to Google Chat Cards V2 and Slack Block Kit:

```go
// Card is a platform-agnostic rich message.
type Card struct {
    Header   CardHeader
    Sections []CardSection
    Actions  []CardAction
}

type CardHeader struct {
    Title    string
    Subtitle string
    IconURL  string
}

type CardSection struct {
    Header  string
    Widgets []Widget
}

// Widget types map to both Google Chat widgets and Slack blocks.
type Widget struct {
    Type       WidgetType // Text, KeyValue, Button, Divider, Image, Input, Checkbox
    Label      string
    Content    string
    ActionID   string     // For interactive widgets
    ActionData string     // Opaque payload for callbacks
    Options    []SelectOption // For Checkbox and Select widgets
}

type WidgetType string

const (
    WidgetText     WidgetType = "text"
    WidgetKeyValue WidgetType = "key_value"
    WidgetButton   WidgetType = "button"
    WidgetDivider  WidgetType = "divider"
    WidgetImage    WidgetType = "image"
    WidgetInput    WidgetType = "input"
    WidgetCheckbox WidgetType = "checkbox"
)

type CardAction struct {
    Label    string
    ActionID string
    Style    string // "primary", "danger", ""
}

// Dialog is an interactive modal for user input.
type Dialog struct {
    Title   string
    Fields  []DialogField
    Submit  CardAction
    Cancel  CardAction
}

type DialogField struct {
    ID          string
    Label       string
    Type        string // "text", "textarea", "select", "checkbox"
    Placeholder string
    Required    bool
    Options     []SelectOption // For "select" and "checkbox" types
}

type SelectOption struct {
    Label string
    Value string
}
```

### Event Router

Incoming events from the chat platform are normalized before dispatch:

```go
// ChatEvent represents an inbound event from any chat platform.
type ChatEvent struct {
    Type       ChatEventType
    Platform   string          // "google_chat", "slack"
    SpaceID    string
    ThreadID   string
    UserID     string
    Text       string          // Raw text (with mention stripped)
    Command    string          // Slash command name (if applicable)
    Args       string          // Command arguments
    ActionID   string          // Interactive element callback
    ActionData string          // Callback payload
    DialogData map[string]string // Submitted dialog fields
}

type ChatEventType string

const (
    EventMessage       ChatEventType = "message"        // @mention or DM
    EventCommand       ChatEventType = "command"         // /slash command
    EventAction        ChatEventType = "action"          // Button click
    EventDialogSubmit  ChatEventType = "dialog_submit"   // Dialog form submission
    EventSpaceJoin     ChatEventType = "space_join"      // Bot added to space
    EventSpaceRemove   ChatEventType = "space_remove"    // Bot removed from space
)
```

---

## User Identity Mapping

### Registering Chat Users to Hub Users

The chat app maintains a mapping between chat platform user IDs and Hub user accounts. This enables impersonation — the chat app makes Hub API calls on behalf of the mapped Hub user.

```go
type UserMapping struct {
    PlatformUserID string    // e.g., "users/12345" (Google) or "U0123ABC" (Slack)
    Platform       string    // "google_chat" or "slack"
    HubUserID      string    // Scion Hub user ID
    HubUserEmail   string    // For display/verification
    RegisteredAt   time.Time
    RegisteredBy   string    // "auto" (email match) or "manual" (explicit registration)
}
```

**Registration flow:**

1. **Auto-register by email**: When a chat user first interacts, the app retrieves their email from the chat platform and looks up the Hub user by email. If found, the mapping is created automatically.
2. **Manual registration**: Users can run `/scion register` to initiate explicit registration:
   - The command first checks if the user's chat platform email matches an existing Hub user. If a match is found, the mapping is created immediately (short-circuit) and the user is notified — no device auth flow needed.
   - If no email match is found, the command falls back to a device authorization flow, similar to CLI login. The Hub issues a one-time code; the user confirms via the Hub UI.
3. **Unregister**: `/scion unregister` removes the mapping.

### Impersonation

The chat app's operational environment account (GCP service account) has IAM permissions to access the Hub's signing key material in GCP Secret Manager. The chat app uses this to create short-lived, scoped tokens for each mapped user when making API calls on their behalf. This follows the same pattern as the existing `ScopedUserIdentity` / UAT system.

For each request:
1. Look up `UserMapping` for the chat user
2. Mint a short-lived UAT scoped to the target grove + required actions
3. Create a `hubclient.Client` with that token
4. Execute the API call

**Security considerations:**
- Tokens are never stored; minted per-request with short TTL (60s)
- Scopes are minimized to the specific operation
- Signing authority is derived from the operational environment account (GCP service account) having IAM access to the signing key in GCP Secret Manager
- All impersonated actions are logged with both the chat user ID and Hub user ID

---

## Space-Grove Linking

Chat spaces (Google Chat spaces, Slack channels) are linked to Scion groves. This scopes all interactions within a space to a specific grove.

```go
type SpaceLink struct {
    SpaceID    string    // Platform space/channel ID
    Platform   string
    GroveID    string
    GroveSlug  string
    LinkedBy   string    // Hub user ID of the admin who linked
    LinkedAt   time.Time
}
```

**Linking:**
- `/scion link <grove-slug>` — links the current space to a grove
- Requires the user to be a grove admin or owner
- One space maps to one grove (1:1)
- A grove can be linked to multiple spaces (across platforms)

**Unlinking:**
- `/scion unlink` — removes the link
- Only grove admins/owners can unlink

---

## Command System

### Slash Commands

| Command | Description | Hub API Call |
|---------|-------------|-------------|
| `/scion list` | List agents in linked grove | `AgentService.List()` |
| `/scion create <agent>` | Create an agent (basic) | `AgentService.Create()` |
| `/scion start <agent>` | Start an agent | `AgentService.Start()` |
| `/scion stop <agent>` | Stop an agent | `AgentService.Stop()` |
| `/scion delete <agent>` | Delete an agent | `AgentService.Delete()` |
| `/scion status <agent>` | Show agent status card | `AgentService.Get()` |
| `/scion logs <agent>` | Show recent agent logs | `AgentService.Logs()` |
| `/scion register` | Register chat user to Hub account | Device auth flow |
| `/scion unregister` | Remove user mapping | Local state |
| `/scion link <slug>` | Link space to grove | `GroveService.Get()` + local state |
| `/scion unlink` | Unlink space from grove | Local state |
| `/scion subscribe <agent>` | Subscribe to agent notifications | Local state |
| `/scion unsubscribe <agent>` | Unsubscribe from agent notifications | Local state |
| `/scion message <agent> <text>` | Send a message to an agent | `AgentService.Message()` |
| `/scion help` | Show available commands | Local |

The `message` command accepts a `--thread <thread-id>` flag for replying within a specific thread. This flag should only be used when replying to a message that contains a thread ID (e.g., from a notification card or a previous agent response). The thread ID is included in the `StructuredMessage` payload so the agent can maintain conversational context.

Commands mirror CLI sub-command syntax. The command router parses identically to the CLI's Cobra-based parser where applicable.

### @Mention Messaging

When a user @mentions the bot followed by text, the chat app routes the message to an agent:

```
@Scion tell deploy-agent to check the staging cluster
```

**Routing logic:**
1. Parse the mention text for an agent slug reference
2. If an agent slug is found: send as a direct message via `AgentService.Message()`
3. If no agent slug found:
   - **Google Chat**: Present a dialog to choose a target agent, broadcast, or cancel
   - **Slack**: Agents can be directly @mentioned by slug (e.g., `@deploy-agent`)
4. If the space has only one running agent: send directly to that agent

### Interactive Elements

Buttons and actions on cards trigger callbacks:

| Action ID | Behavior |
|-----------|----------|
| `agent.start.<id>` | Start the named agent |
| `agent.stop.<id>` | Stop the named agent |
| `agent.respond.<id>` | Submit inline response to `ask_user` |
| `agent.logs.<id>` | Fetch and display recent logs |
| `notification.ack.<id>` | Acknowledge notification |

---

## Notification Relay

The chat app subscribes to Hub notifications for linked groves and relays them to the appropriate chat spaces.

### Subscription Strategy

Notifications are delivered to the chat app through two complementary paths:

**Broker path (primary):** When the chat app is connected as a self-managed broker plugin, the Hub's `NotificationDispatcher` routes user-targeted notifications through the broker's `Publish()` method (Evolution 2). The chat app receives these as `StructuredMessage` values and renders them as platform-appropriate cards. This is the preferred path because it avoids double-delivery and gives the chat app full control over rendering.

**SSE path (supplementary):** The chat app also maintains an SSE connection for real-time agent events that are not covered by the notification system (e.g., agent output streaming, status transitions that don't trigger notifications).

On startup and whenever a space-grove link is created, the chat app:

1. Calls `HostCallbacks.RequestSubscription()` to subscribe to the grove's message topics via the broker plugin path (Evolution 1)
2. Creates a `NotificationSubscription` via `SubscriptionService.Create()` scoped to the grove for the SSE supplementary path
3. Subscribes to trigger activities: `COMPLETED`, `WAITING_FOR_INPUT`, `LIMITS_EXCEEDED`, `STALLED`, `ERROR`, `DELETED`
4. On receiving a notification (via broker `Publish()`) or event (via SSE), maps it to the linked space(s) and renders a platform-appropriate card
5. Includes @mentions for all users subscribed to the originating agent (see below)

When a space is unlinked (`/scion unlink`), the chat app calls `HostCallbacks.CancelSubscription()` to stop receiving messages for that grove.

### User Agent Subscriptions

Users can subscribe to notifications for specific agents via `/scion subscribe <agent>`. By default, all grove notifications are delivered to the linked space. Subscriptions control **who gets @mentioned** in notification cards, not whether the card appears.

```go
type AgentSubscription struct {
    PlatformUserID string    // Chat platform user ID
    Platform       string
    AgentID        string    // Scion agent ID
    GroveID        string
    Activities     []string  // Filtered activity types (e.g., ["ERROR", "WAITING_FOR_INPUT"]); empty = all
    SubscribedAt   time.Time
}
```

When a notification fires, the relay:
1. Looks up all users subscribed to that agent
2. Renders the notification card with @mentions for each subscriber
3. Users with no subscriptions receive cards without being mentioned

This can be enabled or disabled per-user via `/scion subscribe` and `/scion unsubscribe`.

When subscribing, the chat app presents a dialog with checkboxes allowing the user to select which activity types they want to be @mentioned for (e.g., `ERROR`, `WAITING_FOR_INPUT`, `COMPLETED`, `STALLED`, `LIMITS_EXCEEDED`). If no activities are selected, all activity types are included by default. Users can re-run `/scion subscribe <agent>` to update their activity filter.

### Notification Cards

Each notification type renders with a distinct card style:

| Activity | Card Style | Actions |
|----------|-----------|---------|
| `COMPLETED` | Success (green header) | View logs |
| `WAITING_FOR_INPUT` | Attention (yellow header) | Inline response field, View logs |
| `ERROR` | Error (red header) | View logs, Restart |
| `STALLED` | Warning (orange header) | View logs, Restart, Stop |
| `LIMITS_EXCEEDED` | Warning (orange header) | View logs, Stop |
| `DELETED` | Info (gray header) | — |

### ask_user Dialog Flow

When an agent enters `WAITING_FOR_INPUT`:

1. Notification relay receives the event
2. Renders a card in the linked space showing the agent's question with an **inline response text field** and submit button — no intermediate "Respond" click required
3. User types response and submits directly from the card
4. Chat app sends the response via `AgentService.Message()` with the response text
5. Agent resumes; follow-up status card is posted

---

## Google Chat Implementation

### Authentication

- Uses a GCP service account (JSON key or Workload Identity)
- The service account must be granted the Chat API scope
- The Chat app is registered in the GCP Console under the Hub's project

### Event Delivery

Google Chat supports two modes:
- **HTTP endpoint** (recommended for production): Google sends POST requests to a registered URL
- **Pub/Sub** (alternative): Events delivered via a Cloud Pub/Sub topic

The implementation uses the HTTP endpoint model:

```go
// internal/googlechat/adapter.go

type GoogleChatAdapter struct {
    chatClient *chat.SpacesMessagesClient
    httpServer *http.Server
    projectID  string
    messenger  Messenger // self-reference for the interface
}

func (g *GoogleChatAdapter) HandleEvent(w http.ResponseWriter, r *http.Request) {
    // 1. Verify request authenticity (Google-signed JWT in Authorization header)
    // 2. Parse event payload
    // 3. Normalize to ChatEvent
    // 4. Dispatch to command router or message handler
}
```

### Card Rendering

Google Chat uses Cards V2 JSON format. The adapter translates the generic `Card` model:

```go
func (g *GoogleChatAdapter) renderCard(card Card) *chatpb.CardWithId {
    // Map CardHeader → chatpb.Card_CardHeader
    // Map CardSection → chatpb.Card_Section
    // Map Widget types → chatpb.Widget (DecoratedText, ButtonList, etc.)
    // Map CardAction → chatpb.Card_CardAction
}
```

### Multi-Agent Identity

Google Chat fixes the bot name/avatar at the GCP Console level. To distinguish agents:

- **Card header**: Each message from an agent includes a `CardHeader` with the agent name, slug, and a generated icon

### Threading Strategy

Both Google Chat and Slack generate an implicit thread ID for every message posted to a space/channel without an explicit thread reference — effectively, every top-level message is the root of a new thread. The chat app leverages this native platform threading directly:

- **Every notification card or agent message** is posted as a new thread root in the linked space
- **User replies** to a notification card or agent message are posted in that message's thread (using Google Chat thread keys or Slack `thread_ts`)
- **Follow-up messages from the same agent conversation** are threaded under the original root message using the platform's native thread ID
- The thread ID is stored as part of the `StructuredMessage` type, enabling agents to reply within a specific thread via the `scion message --thread <thread-id>` flag

This approach uses platform-native threading rather than a custom embedding scheme. It relies on the chat platform to handle thread grouping and UI — no best-effort heuristics needed. If threading behavior needs refinement for specific workflows, it can be tuned without changing the underlying mechanism.

```
┌─────────────────────────────────────┐
│ 🤖 deploy-agent                     │
│ Grove: production │ Running          │
├─────────────────────────────────────┤
│ Deployment to staging complete.     │
│ All health checks passing.          │
│                                     │
│ [View Logs]  [Stop Agent]           │
└─────────────────────────────────────┘
```

### Slash Command Registration

Google Chat slash commands are registered in the GCP Console (Chat API configuration). The app registers:

| Command ID | Command | Description |
|------------|---------|-------------|
| 1 | `/scion` | Scion agent management |

All subcommands are parsed from the `argumentText` field of the slash command event. This avoids needing to register each subcommand separately.

---

## Slack Implementation (Future)

### Key Differences from Google Chat

| Aspect | Google Chat | Slack |
|--------|------------|-------|
| **Identity per message** | Fixed (card header workaround) | Dynamic (`username`, `icon_url` overrides) |
| **Event delivery** | HTTP POST with JWT verification | HTTP POST with signing secret verification |
| **UI framework** | Cards V2 (protobuf) | Block Kit (JSON) |
| **Interactive elements** | Action callbacks to same endpoint | Interactivity URL (separate endpoint) |
| **Dialogs/Modals** | Dialogs via Cards | Modals via `views.open` API |
| **Auth** | Service account (GCP IAM) | Bot token (`xoxb-`) via OAuth |
| **Threading** | Thread keys (named) | Thread timestamps (`thread_ts`) |

### Slack Advantages

- Dynamic bot identity enables true per-agent personas (name + avatar)
- Block Kit is more flexible for complex layouts
- App Home tab can serve as a grove dashboard

### Implementation

The Slack adapter implements the same `Messenger` interface. Key mappings:

```go
type SlackAdapter struct {
    client     *slack.Client
    signingKey string
    httpServer *http.Server
}

func (s *SlackAdapter) SendMessage(ctx context.Context, req SendMessageRequest) (string, error) {
    opts := []slack.MsgOption{
        slack.MsgOptionText(req.Text, false),
    }
    if req.AgentID != "" {
        // Dynamic identity - unique to Slack
        opts = append(opts,
            slack.MsgOptionUsername(req.AgentID),
            slack.MsgOptionIconURL(agentIconURL(req.AgentID)),
        )
    }
    if req.ThreadID != "" {
        opts = append(opts, slack.MsgOptionTS(req.ThreadID))
    }
    _, ts, err := s.client.PostMessageContext(ctx, req.SpaceID, opts...)
    return ts, err
}
```

---

## State Management

The chat app needs to persist:

| Data | Storage | Notes |
|------|---------|-------|
| User mappings | SQLite (local) | Chat user → Hub user registrations |
| Space-grove links | SQLite (local) | Space → grove associations |
| Agent subscriptions | SQLite (local) | User → agent notification subscriptions |
| Pending dialogs | In-memory (with TTL) | Track open ask_user dialogs |
| Notification subscriptions | Hub API (via SubscriptionService) | Managed subscriptions |
| SSE cursor | In-memory | Last-event-id for reconnection |

Chat-specific state is kept local to the chat app in SQLite. The Hub API is not extended with chat-specific resources.

### Schema

```sql
CREATE TABLE user_mappings (
    platform_user_id TEXT NOT NULL,
    platform         TEXT NOT NULL,
    hub_user_id      TEXT NOT NULL,
    hub_user_email   TEXT NOT NULL,
    registered_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    registered_by    TEXT NOT NULL DEFAULT 'auto',
    PRIMARY KEY (platform_user_id, platform)
);

CREATE TABLE space_links (
    space_id    TEXT NOT NULL,
    platform    TEXT NOT NULL,
    grove_id    TEXT NOT NULL,
    grove_slug  TEXT NOT NULL,
    linked_by   TEXT NOT NULL,
    linked_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (space_id, platform)
);

CREATE TABLE agent_subscriptions (
    platform_user_id TEXT NOT NULL,
    platform         TEXT NOT NULL,
    agent_id         TEXT NOT NULL,
    grove_id         TEXT NOT NULL,
    activities       TEXT NOT NULL DEFAULT '',  -- Comma-separated activity types; empty = all
    subscribed_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (platform_user_id, platform, agent_id)
);
```

---

## Configuration

The chat app is configured via a YAML file:

```yaml
# scion-chat-app.yaml

hub:
  endpoint: "https://hub.example.com"
  # The Hub user the chat app operates as (for non-impersonated operations)
  user: "chat-app@example.com"
  # Credentials for Hub authentication
  credentials: "/path/to/hub-credentials.json"
  # Or: use GOOGLE_APPLICATION_CREDENTIALS for auto-detection

# Broker plugin RPC server settings.
# The Hub connects to this address as a self-managed plugin.
plugin:
  # RPC listen address for the MessageBrokerPluginInterface
  listen_address: "localhost:9090"

platforms:
  google_chat:
    enabled: true
    project_id: "my-gcp-project"
    # Service account for Google Chat API
    credentials: "/path/to/chat-sa-key.json"
    # HTTP endpoint for receiving events
    listen_address: ":8443"
    # Verification audience (from GCP Console)
    audience: "1234567890"

  slack:
    enabled: false
    bot_token: "${SLACK_BOT_TOKEN}"
    signing_secret: "${SLACK_SIGNING_SECRET}"
    listen_address: ":8444"

state:
  # Local SQLite for user mappings and space links
  database: "/var/lib/scion-chat-app/state.db"

notifications:
  # Which agent activities to relay to chat
  trigger_activities:
    - COMPLETED
    - WAITING_FOR_INPUT
    - ERROR
    - STALLED
    - LIMITS_EXCEEDED

logging:
  level: "info"
  format: "json"
```

The corresponding Hub-side plugin configuration (in the Hub's settings) references the chat app as a self-managed plugin:

```yaml
# In Hub settings
plugins:
  broker:
    googlechat:
      self_managed: true
      address: "localhost:9090"
      config:
        hub_endpoint: "https://hub.example.com"
        project_id: "my-gcp-project"
```

---

## Process Lifecycle

### Startup

1. Load configuration
2. Initialize SQLite state database
3. Start the broker plugin RPC server (exposes `MessageBrokerPluginInterface`)
4. Create `hubclient.Client` authenticated as the configured Hub user
5. Initialize platform adapter(s) (Google Chat, Slack)
6. Load existing space-grove links from state DB
7. Wait for Hub plugin manager to connect and call `Configure()` (self-managed plugin handshake)
8. For each linked grove: call `HostCallbacks.RequestSubscription()` and create/verify notification subscriptions via Hub API
9. Open SSE connection to Hub for supplementary real-time events
10. Start HTTP server(s) for platform webhook endpoints
11. Begin serving

### Shutdown

1. Receive SIGTERM/SIGINT
2. Stop accepting new webhook events
3. Drain in-flight requests (30s timeout)
4. Cancel all plugin-initiated subscriptions via `HostCallbacks.CancelSubscription()`
5. Close broker plugin RPC server (Hub plugin manager receives disconnect)
6. Close SSE connection
7. Close platform API clients
8. Close state database

### Health Check

Expose `/healthz` endpoint that checks:
- Hub API reachability
- Broker plugin RPC connection status (Hub plugin manager connected)
- SSE connection status
- Platform API connectivity
- State database accessibility

---

## Security

### Hub Access

The chat app operates under three identity contexts (see Process Model):

- **Hub admin user** needs:
  - Read access to all groves that may be linked
  - Notification subscription management
  - Agent messaging permissions
  - Non-impersonated operations (e.g., notification subscriptions, SSE connections) run as this user directly
- **Operational environment account** (GCP service account) needs:
  - IAM access to the signing key in GCP Secret Manager (for minting scoped UATs)
  - Permissions for structured logging and any other infrastructure-level operations
- **Impersonated chat users**: Most user-initiated API calls are made as the linked Hub user via short-lived scoped UATs

### Chat Platform Verification

- **Google Chat**: Verify the `Authorization: Bearer <jwt>` header on incoming requests using Google's public keys. Validate the audience claim matches the configured project.
- **Slack**: Verify `X-Slack-Signature` header using the app's signing secret and timestamp.

### User Impersonation Audit

All impersonated API calls are logged with:
- Chat platform user ID
- Mapped Hub user ID
- Action performed
- Target resource (agent ID, grove ID)
- Timestamp

---

## Implementation Plan

### Phase 1: Core Framework & Google Chat MVP

- [x] Project scaffolding (`extras/scion-chat-app/` module with `cmd/`, `internal/` layout)
- [x] Broker plugin RPC server implementing `MessageBrokerPluginInterface`
- [x] Self-managed plugin handshake (accept Hub plugin manager connection, `Configure()`)
- [x] `HostCallbacks` integration for plugin-initiated subscriptions
- [x] `Messenger` interface and `ChatEvent` types
- [x] Google Chat adapter (webhook receiver, message sending, card rendering)
- [x] Command router with basic parsing
- [x] State management (SQLite for user mappings, space links, agent subscriptions)
- [x] User identity auto-registration (email match)
- [x] Space-grove linking (`/scion link`) with `RequestSubscription()` calls
- [x] Basic commands: `list`, `status`, `start`, `stop`, `create`
- [x] Notification relay via broker `Publish()` path (primary) and SSE (supplementary)
- [x] User agent subscription (`/scion subscribe`, `/scion unsubscribe`)

### Phase 2: Interactive Features & Threading

- [x] Native platform threading (thread ID tracking, `--thread` flag on `scion message`)
- [x] `ask_user` inline response flow (notification card with embedded response field)
- [x] Agent log viewing
- [x] Interactive card buttons (start, stop, acknowledge)
- [x] Manual user registration (device auth fallback when email match fails)
- [x] `delete` command with confirmation dialog
- [x] Agent-to-user message relay (toggleable per user)
- [x] Subscription activity filtering dialog (checkbox-based)

### Phase 3: Slack Adapter

- [ ] Slack adapter implementing `Messenger` interface
- [ ] Block Kit card rendering
- [ ] Slack event API webhook handler
- [ ] Dynamic agent identity (username/avatar per agent)
- [ ] Slack-specific modal flow for dialogs
- [ ] App Home tab as grove dashboard

### Phase 4: Production Hardening

- [ ] Metrics (OpenTelemetry integration)
- [ ] Rate limiting per user/space
- [ ] Graceful reconnection (SSE, platform APIs)
- [ ] Admin commands (list links, force unlink, diagnostics)
- [ ] Helm chart / deployment configuration
- [ ] Documentation and runbook

---

## Resolved Decisions

1. **Plugin architecture**: The chat app is a **single binary** that implements the `MessageBrokerPluginInterface` for transport while also running its own HTTP server and application logic (Option C). It connects to the Hub as a **self-managed plugin** — the Hub plugin manager connects to the app's RPC server rather than starting it as a child process.

2. **Hub-side state vs. local state**: Chat-specific state (user mappings, space links, agent subscriptions) is kept **local** in the chat app's SQLite database. The Hub API is not extended with chat-specific resources.

3. **Multi-instance / HA**: Deferred. The MVP runs as a single instance. HA design will be addressed in a future iteration.

4. **Agent creation from chat**: **Yes** — basic agent creation is supported via `/scion create <agent>`, but with limited complexity (no template browsing wizards or multi-step configuration dialogs).

5. **Broadcast semantics**: Platform-dependent. In **Google Chat**, if no agent slug is found in the mention text, a dialog is presented to choose a target agent, broadcast, or cancel. In **Slack**, agents can be directly @mentioned by slug.

6. **Message relay direction**: **Yes** — agent-to-user messages (non-notification) are relayed to chat. This is **toggleable per user** via a command (e.g., `/scion subscribe`/`/scion unsubscribe`).

7. **Identity model**: The chat app operates under three distinct identity contexts: a Hub admin user for system-level operations, a GCP service account (operational environment account) for secret backend access and logging, and impersonated Hub users for user-initiated actions.

8. **Threading**: Native platform threading is used directly. Both Google Chat and Slack generate implicit thread IDs for every top-level message. The chat app uses these native thread IDs rather than a custom embedding scheme. The `scion message` command supports a `--thread` flag for replying within a specific thread. Thread IDs are included in the `StructuredMessage` type.

9. **Notification delivery**: Notifications are routed through the broker plugin's `Publish()` path when the plugin is active (Evolution 2). The `ChannelRegistry` fallback is only used in deployments without a broker plugin. SSE is maintained as a supplementary path for events not covered by the notification system.

10. **Dynamic grove subscriptions**: The chat app uses `HostCallbacks.RequestSubscription()` / `CancelSubscription()` (Evolution 1) to dynamically manage grove topic subscriptions as spaces are linked and unlinked. This is preferred over restarting the plugin to pick up changes.

11. **Notification activity filtering**: Per-activity-type filtering is supported. When subscribing, users are presented a dialog with checkboxes to select which activity types trigger @mentions. Empty selection defaults to all activities.

12. **`/scion register` short-circuit**: The register command checks for email match first and short-circuits to auto-registration if found, only falling back to device auth flow when no match exists.

13. **Module location**: The chat app ships as a **separate Go module** under `extras/scion-chat-app/`, following the existing extras pattern (`docs-agent`, `fs-watcher-tool`, `agent-viz`). This keeps platform-specific SDKs (Google Chat, Slack) out of the main module's dependency tree. The module imports shared packages (`hubclient`, `plugin`, `secret`, `messages`) from the main `github.com/GoogleCloudPlatform/scion` module, using a `replace` directive for local development.

## Open Questions

_None at this time._

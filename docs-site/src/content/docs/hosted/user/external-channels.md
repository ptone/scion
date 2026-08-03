---
title: External Channels
description: Connect Scion to Telegram, Discord, and A2A for external messaging and notifications.
---

## Overview

Scion can relay agent messages and notifications to external platforms, extending communication beyond the CLI and Web Dashboard. Three channels are available: **Telegram** (bidirectional group chat), **Discord** (outbound webhook notifications), and **A2A protocol** (expose agents as A2A endpoints for programmatic interaction).

## Telegram

The Telegram integration provides **bidirectional messaging** — users can message agents from Telegram groups and receive replies directly in the chat.

### How It Works

- A Telegram bot (created via [@BotFather](https://core.telegram.org/bots#botfather)) acts as the bridge between Telegram groups and the Scion Hub.
- The bot runs as a Hub plugin (`scion-plugin-telegram`). Homebrew installs it automatically; you configure it from the Hub admin UI (or, for a from-source install, in the Hub's `settings.yaml`).
- **Group linking:** Use the `/setup` bot command in a Telegram group to link it to a Scion project.
- **Identity linking:** Use `/register` to associate your Telegram account with your Scion Hub identity.

:::tip[Workstation quick start]
New to Telegram in Workstation mode? Follow the step-by-step
[Setting Up Telegram](/scion/getting-started/telegram/) tutorial — bot creation, plugin
configuration from the web UI, registration, and your first message.
:::

### Routing & Commands

- **@-mention routing:** Mention a specific agent (e.g., `@mybot agent-name message`) to route a message to that agent.
- **Default agent:** Set a default agent with `/default` so untagged messages route automatically.
- Available bot commands: `/agents` (list agents), `/default` (set default), `/settings` (configure group), `/notifications` (toggle notification types).

### Group Settings

Each linked group can be configured via `/settings`:

- **Observer mode (`a2a`):** Show agent-to-agent messages in the group, so you can watch how agents coordinate.
- **Commentary:** Show agent reply messages (responses to other agents) in the group.
- **Group notifications (`grp`):** Post agent state change notifications (completed, error, waiting for input) in the group chat.

For a guided Workstation walkthrough, see [Setting Up Telegram](/scion/getting-started/telegram/). For advanced deployment (webhook mode, HA/standalone, `settings.yaml` reference), see [extras/scion-telegram/README.md](https://github.com/GoogleCloudPlatform/scion/tree/main/extras/scion-telegram).

## Discord

Scion supports two forms of Discord integration depending on your infrastructure requirements:

1. **Simple Outbound Webhooks**: Push-only notifications and agent message forwarding to a Discord channel.
2. **Full Discord Bot Integration**: A bidirectional gateway bot supporting real-time conversation, agent selection, and interactive slash commands.

---

### Option 1: Simple Outbound Webhooks

This provides a lightweight, push-only pipeline. Agents can broadcast status updates, alerts, and `ask_user` notifications to Discord, but users cannot reply or send commands back to agents.

- **Severity-based color coding:** Messages are color-coded by severity (info, warning, error, urgent).
- **@mentions:** Urgent messages and explicit `ask_user` requests can trigger `@user` or `@role` mentions.

#### Configuration

Set the webhook URL in one of two ways:

- **settings.yaml:** Set `server.discord_webhook_url` in the Hub configuration.
- **Environment variable:** Set `SCION_DISCORD_WEBHOOK_URL`.

For more details, see [Hub Setup — Discord Integration](/scion/hosted/single-node/hub-server/#discord-integration).

---

### Option 2: Full Discord Bot Integration (Bidirectional)

The `scion-plugin-discord` (run either as a Hub plugin or a standalone gRPC service) provides a complete bidirectional bridge. Users can join conversations, list agents, route messages via mentions, and run interactive commands.

#### Real-Time Chat & Agent Persona

- **Gateway-Driven Chat**: The bot connects directly to the Discord WebSocket Gateway to process messages instantly.
- **Webhook-Based Identity**: If the bot is granted the **Manage Webhooks** permission, agent replies are sent using per-agent usernames and RoboHash avatars instead of the main bot user identity.
- **Message Routing**:
  - Direct mentions (e.g., `@agent-name hello`) route directly to the targeted agent.
  - Plain messages route to the channel's designated default agent.
  - `@all message` broadcasts to all agents in the channel's project.

#### Interactive Slash Commands

All slash commands are grouped under the `/scion` namespace:

| Command | Description |
| :--- | :--- |
| `/scion setup` | Link the current Discord channel to a Scion project. |
| `/scion unlink` | Unlink the channel from its project. |
| `/scion agents` | List agents in the linked project with real-time state. |
| `/scion default [agent]` | Set, change, or show the channel's default agent. |
| `/scion status <agent>` | Show high-density status and metrics for an agent. |
| `/scion send <path>` | Retrieve and send a file from the shared scratchpad (supports absolute path or partial-name search with a button picker). |
| `/scion register` | Initiate the 6-character OAuth profile link flow. |
| `/scion unregister` | Disassociate your Discord user from your Scion Hub identity. |
| `/scion info` | Show your current user registration status. |
| `/scion settings` | Configure channel-specific notifications and observer modes. |
| `/scion help` | List all available commands and options. |

#### File Retrieval with `/scion send`

The `/scion send <path>` command allows developers and users to inspect or retrieve files directly from the project's **shared scratchpad volume** (e.g., log files, test results, code artifacts generated by agents):

- **Fuzzy Search**: Provide a full absolute path, or a partial file/folder name. If multiple files match, the bot presents an interactive button picker to select the desired file.
- **Path Confinement**: For security, file retrieval is strictly confined to `/scion-volumes/` paths. The plugin employs robust symlink traversal protection to prevent access outside allowed shared directories.

## A2A Protocol Bridge

The A2A (Agent-to-Agent protocol) bridge exposes Scion agents as **standard A2A endpoints**, allowing external A2A clients to discover and interact with them programmatically.

- **Discovery:** External clients can query available agents and their capabilities via the A2A protocol.
- **Interaction modes:** Supports blocking (synchronous), SSE streaming, and push notification delivery.
- **Standalone service:** Runs as a separate bridge process alongside the Hub (see `extras/scion-a2a-bridge`).

This is useful for integrating Scion agents into larger multi-agent systems or exposing them to third-party A2A-compatible clients.

For setup and configuration, see [extras/scion-a2a-bridge/README.md](https://github.com/GoogleCloudPlatform/scion/tree/main/extras/scion-a2a-bridge).

### Desktop App Federation (Claude Desktop, Codex Desktop)

Desktop A2A clients (such as Claude Desktop and Codex Desktop) can interact with Scion agents using per-user authentication. Each user presents their own Scion User Access Token (UAT), and the bridge propagates their identity to the Hub for audit logging and access control.

#### Prerequisites

- The bridge operator has deployed the A2A bridge with `auth.scheme: hubUAT`.
- Your Scion Hub account has access to the target project.

#### Step 1: Create a UAT

Create a Scion UAT scoped to your project with the required permissions:

```bash
scion token create --name "claude-desktop" --project <project-slug> \
  --scope agent:message,agent:read --expires 365d
```

This returns a `scion_pat_...` token. Copy it securely — it will not be shown again.

#### Step 2: Configure Your Desktop App

In your desktop A2A client's provider settings:

- **Endpoint:** `https://<bridge-host>/projects/<project-slug>/agents/<agent-slug>`
- **Auth type:** Bearer token
- **Token:** Paste your `scion_pat_...` token

To discover available agents, query the bridge's agent card:

```bash
curl https://<bridge-host>/.well-known/agent-card.json
```

Or for a specific agent:

```bash
curl https://<bridge-host>/projects/<project-slug>/agents/<agent-slug>/.well-known/agent-card.json
```

#### Step 3: Test the Connection

Verify end-to-end connectivity with a `message/send` call:

```bash
curl -X POST https://<bridge-host>/projects/<project-slug>/agents/<agent-slug>/jsonrpc \
  -H "Authorization: Bearer scion_pat_..." \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": "1",
    "method": "message/send",
    "params": {
      "message": {
        "role": "user",
        "parts": [{"type": "text", "text": "Hello from desktop!"}]
      }
    }
  }'
```

#### Required Scopes

| Scope | Purpose |
|-------|---------|
| `agent:message` | Send messages to agents |
| `agent:read` | List agents and read task status |

#### Per-User Isolation

When the bridge uses `hubUAT` or `hubJWT` auth, each user's tasks are isolated:

- You can only see and cancel tasks you created.
- The Hub's audit logs reflect your identity, not the bridge admin's.
- If your UAT is revoked, access stops within 60 seconds (the bridge's cache TTL).

:::note[Bridge operator note]
To enable per-user auth, set `auth.scheme: hubUAT` in `scion-a2a-bridge.yaml`.
The `auth.api_key` field is not needed for this scheme. See the
[sample config](https://github.com/GoogleCloudPlatform/scion/tree/main/extras/scion-a2a-bridge/scion-a2a-bridge.yaml.sample)
for details.
:::

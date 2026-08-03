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

Scion supports Discord integration in two distinct modes: the **Interactive Discord Bot** (bidirectional messaging) and **Outbound-Only Webhook Notifications** (simple status alerts).

---

### 1. Interactive Discord Bot (Recommended)

The Interactive Discord Bot (powered by the `scion-plugin-discord` plugin) provides **bidirectional messaging** — allowing you to list, start, stop, and message Scion agents, as well as create threaded conversations with new agents directly within Discord.

When communicating, the bot automatically manages Discord webhooks on a per-agent basis. This allows each agent to appear with its own distinct persona (name and avatar) rather than as a generic bot.

#### Setup & Connection

1. **Invite the Bot**: Ensure your server administrator has invited the Scion Discord bot using an OAuth2 invite URL with the necessary permissions (specifically `Manage Webhooks` for per-agent personas, and `Use Application Commands` for slash commands).
2. **Link a Channel**: In the Discord channel where you want to interact with your agents, run:
   ```text
   /scion setup
   ```
   Select your Scion project from the resulting list to bind the channel.
3. **Register Your Identity**: To authenticate your requests, you must associate your Discord account with your Scion Hub account. Run:
   ```text
   /scion register
   ```
   Click the button/link returned by the bot to log into your Hub's profile page and complete the link.

#### Available Bot Commands

All bot interactions are handled via `/scion` slash commands:

| Command | Description |
| :--- | :--- |
| `/scion setup` | Link the current channel to a Scion project. |
| `/scion unlink` | Unlink the channel from its Scion project. |
| `/scion agents` | List all agents in the project with live statuses (💤 idle, ⚙️ executing, 💭 thinking, ✅ completed, etc.). |
| `/scion status <agent>` | Show the detailed status of a specific agent. |
| `/scion start <agent>` | Start a stopped agent. |
| `/scion stop <agent>` | Stop a running agent. |
| `/scion msg <agent> <text>` | Send a message to a specific agent. |
| `/scion logs <agent>` | Stream/view recent logs for an agent. |
| `/scion default [agent]` | Set or clear the default agent for the channel or thread (so unaddressed text routes automatically). |
| `/scion send <path>` | Send a file from your workspace by path or search for files. |
| `/scion thread <title> [template]` | Create a Discord thread and a Scion agent in one atomic step (see below). |
| `/scion register` | Link your Discord account to your Scion Hub identity. |
| `/scion unregister` | Unlink your Discord account from Scion Hub. |
| `/scion settings` | Configure channel-specific notification settings. |
| `/scion info` | Display your linked Scion Hub registration info. |
| `/scion help` | Show the help menu. Includes the plugin's build version and git commit hash (injected via build-time `ldflags`). |

#### One-Step Thread & Agent Creation (`/scion thread`)

The `/scion thread <title> [template]` command allows you to spin up a new conversation and a corresponding agent simultaneously. 

##### How It Works:
1. **Validation & Naming**: The bot validates that the current channel is linked and that you are registered. It automatically "slugifies" your thread title into a valid Scion agent name and checks that it is unique.
2. **Template Selection**: You can specify an optional template. The command provides auto-complete to help you pick from available templates, or defaults to the project default.
3. **Concurrent Orchestration**: The bot starts a coordinated background process:
   - **In Discord**: It creates a new thread (or a forum post in forum-style channels) named after your title.
   - **On the Hub**: It triggers agent creation and starts it up.
4. **Delegated Identity (`X-Scion-On-Behalf-Of`)**: To ensure proper ownership, the bot propagates your identity to the Hub using a secure, HMAC-signed delegated identity header (`X-Scion-On-Behalf-Of` with `user:email` format). This ensures the newly created agent is attributed to your Scion user rather than being left ownerless.
5. **In-Thread Progress & Interaction**: While the agent provisions, the bot posts real-time progress updates inside the newly created thread (e.g., `"⏳ Creating agent <slug>..."`). Once successful, the bot sets the thread-wide default agent to the new agent, letting you immediately start chatting!

---

### 2. Outbound-Only Webhook Notifications

If you do not need bidirectional chat or command interaction, you can configure standard outbound-only webhook notifications. In this mode, Scion simply posts status alerts (completed, error, waiting for input) directly to a Discord channel.

- **Severity-based color coding:** Messages are color-coded by severity (info, warning, error, urgent).
- **@mentions:** Urgent messages and explicit `ask_user` requests can trigger `@user` or `@role` mentions.

#### Configuration

Set the webhook URL in one of two ways:

- **settings.yaml:** Set `server.discord_webhook_url` in the Hub configuration.
- **Environment variable:** Set `SCION_DISCORD_WEBHOOK_URL`.

For more details, see [Hub Setup — Discord Integration](/scion/hosted/single-node/hub-server/#discord-integration).

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

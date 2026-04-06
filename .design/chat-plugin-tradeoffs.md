# Chat App Integration: Plugin API Tradeoffs

**Created:** 2026-04-05
**Status:** Revised (incorporates feedback from broker-tradeoff-feedback.md)
**Related:** `.design/google-chat.md`, `pkg/plugin/broker_plugin.go`, `pkg/hub/channels.go`

---

## Context

The [chat app design](google-chat.md) proposes a standalone process that consumes the Hub API directly via `hubclient`. It dismisses the existing `MessageBrokerPluginInterface` as too narrow. This document revisits that decision and explores a hybrid approach: evolving the plugin API to handle the message-transport portion of the chat app while keeping the rest (commands, dialogs, identity, state) in the standalone process.

The plugin API is new and unused. This is both the concern (it hasn't been validated against a real consumer) and the opportunity (we can shape it around a real use case without breaking existing users).

---

## The Three Integration Surfaces

The chat app touches three distinct surfaces in the Hub. Each has different requirements:

| Surface | Direction | What flows | Current mechanism |
|---------|-----------|-----------|-------------------|
| **Message transport** | Bidirectional | `StructuredMessage` to/from agents | `MessageBroker` interface + `MessageBrokerProxy` |
| **Notifications** | Hub → Chat | Agent lifecycle events | `NotificationChannel` interface + `ChannelRegistry` |
| **API operations** | Chat → Hub | CRUD commands (start, stop, list, create) | `hubclient.Client` HTTP calls |

The key insight is that these three surfaces have very different characteristics. The plugin API is a natural fit for some but not others.

---

## Surface 1: Message Transport

### Current plugin model

The `MessageBrokerPluginInterface` (`pkg/plugin/broker_plugin.go`) defines:

```go
type MessageBrokerPluginInterface interface {
    Configure(config map[string]string) error
    Publish(ctx context.Context, topic string, msg *messages.StructuredMessage) error
    Subscribe(pattern string) error
    Unsubscribe(pattern string) error
    Close() error
    GetInfo() (*PluginInfo, error)
    HealthCheck() (*HealthStatus, error)
}
```

The `BrokerPluginAdapter` wraps this as a `broker.MessageBroker`, with a critical design note in the code:

> Subscribe's MessageHandler callback is not forwarded to the plugin — inbound messages arrive via the hub API instead (see broker-plugins.md design doc).

This means the plugin model already assumes an asymmetric flow:
- **Outbound** (Hub → external): Plugin receives `Publish()` calls with topic + `StructuredMessage`
- **Inbound** (external → Hub): Plugin POSTs to the Hub's `POST /api/v1/broker/inbound` endpoint

### Fit for chat apps

This maps well to the chat message transport use case:

| Chat flow | Plugin mechanism |
|-----------|-----------------|
| Agent sends message to user | Hub calls `Publish()` → plugin delivers to chat platform |
| User sends @mention to agent | Plugin receives from chat platform → POSTs to Hub inbound API |
| Grove broadcast to chat | Hub calls `Publish()` on broadcast topic → plugin delivers |

The topic-based routing is natural: a chat plugin subscribes to `scion.grove.<id>.agent.*.messages` and `scion.grove.<id>.user.*.messages` for linked groves. When an agent message arrives, the plugin translates it to a chat-platform card and sends it to the linked space.

### What's missing

The current plugin API has gaps that prevent it from handling this cleanly:

**Gap 1: No delivery context beyond topic + message**

When the plugin receives a `Publish()` call, it gets a topic string and a `StructuredMessage`. To render a chat card, it also needs:
- Agent metadata (name, slug, grove slug) — partially available from `Sender`/`SenderID` but not reliably
- Space routing info (which chat space is linked to this grove) — plugin has no way to query this
- Rendering hints (is this a notification vs. a conversational reply)

> **Revised (feedback):** Space routing is the chat app's responsibility, not the plugin's. The chat app (which is the same binary as the plugin — see Option C below) maintains the grove→space mapping in its own SQLite state. The plugin side of that binary has direct access to this mapping. This gap is resolved by the single-binary architecture.

**Gap 2: No inbound message metadata**

The Hub's inbound API (`POST /api/v1/broker/inbound`) accepts a `StructuredMessage`, but chat-originated messages need to carry the impersonated user identity. The current message format has `Sender`/`SenderID` but no field for "acting on behalf of user X."

> **Revised (feedback):** The `Sender`/`SenderID` fields can represent the impersonated user directly in the expected format. No new field is required — the chat app sets the sender to the mapped Hub user identity when constructing the inbound message.

**Gap 3: No lifecycle hooks**

The plugin has no way to be notified when:
- A grove is linked/unlinked to a space (plugin-specific state)
- The plugin should adjust subscriptions (currently the Hub manages subscriptions via `MessageBrokerProxy`, not the plugin)

**Gap 4: No rich delivery — only StructuredMessage**

`StructuredMessage` is a flat text message with type/urgency flags. Chat apps want to deliver rich cards. The plugin would need to do its own rendering, which means it needs more context (see Gap 1).

> **Revised (feedback):** Rich delivery is outside the scope of the plugin interface. It is part of what makes the chat app a superset of capabilities beyond a typical message broker plugin. The plugin interface should remain focused on `StructuredMessage` transport; the chat app binary (which contains the plugin) handles rendering internally.

### Proposed plugin API extensions

> **Revised (feedback):** The `PublishWithContext` / `DeliveryMeta` extension is **not recommended** for the core plugin API. There is no strong reason the chat app can't call the Hub API (e.g., the agent/job API) to get required enrichment metadata on its own. `PublishWithContext` would push rendering concerns into the plugin interface, which should remain a narrow message transport contract. The chat app — running as the same binary as the plugin (Option C) — can enrich messages internally by querying the Hub API via `hubclient` when it needs agent metadata, grove info, or rendering hints.
>
> The core plugin API should stay focused on `Publish(topic, msg)` with `StructuredMessage`. Enrichment is a chat-app-layer concern, not a broker plugin concern.

### Verdict: Message transport **should use the plugin**

The plugin model is the right abstraction for message transport. It:
- Keeps the Hub as the authoritative message router (subscriptions, fan-out, persistence, audit)
- Uses the established inbound API for chat→agent messages
- Gets validated against a real consumer, proving the API

The plugin interface does not need `PublishWithContext` — the chat app binary (which embeds the plugin) can enrich messages by calling the Hub API directly via `hubclient`. The core plugin API stays narrow and general-purpose.

---

## Surface 2: Notifications

### Current model

Notifications flow through two independent mechanisms:

1. **NotificationDispatcher** → agent subscribers (via `DispatchAgentMessage`) and user subscribers (via SSE + `ChannelRegistry`)
2. **ChannelRegistry** → fire-and-forget delivery to external channels (`NotificationChannel` interface)

The existing `SlackChannel` (`channels_slack.go`) is a notification channel — it formats a `StructuredMessage` as text and POSTs to a Slack incoming webhook. It's one-way, fire-and-forget, with no interactive elements.

### Overlap with message transport

If the chat plugin handles message transport (Surface 1), notifications are partially redundant. Agent status changes already produce `StructuredMessage` events with type `state-change` that flow through the broker. The plugin would receive these via `Publish()` and render them as cards.

However, there's a distinction:

| Mechanism | Trigger | Scope | Rendering |
|-----------|---------|-------|-----------|
| Message broker | Any message published to agent/user topics | Running agents only | Generic (plugin renders) |
| Notification dispatcher | Agent activity changes matching subscriptions | Subscription-scoped | Pre-formatted text |

The notification system adds value beyond the broker:
- **Subscription filtering** — only fires for specific activities (COMPLETED, ERROR, etc.)
- **Deduplication** — prevents duplicate notifications for the same status
- **Persistence** — notification records in the store for acknowledgment tracking
- **Stale event suppression** — skips events older than the subscription

### Proposed approach: Merge notification delivery into the plugin path

Rather than having both a `NotificationChannel` (Slack webhook) AND a broker plugin (Slack chat) delivering to the same platform, the notification dispatcher should route through the broker when a broker plugin is present for that platform:

```
Agent status change
  → NotificationDispatcher (dedup, filter, persist)
    → subscriber is user?
      → Is there a broker plugin subscribed to user message topics?
        YES → Publish notification as StructuredMessage to broker (plugin renders it)
        NO  → Fall back to ChannelRegistry (webhook/email)
      → Always: SSE event for browser clients
```

This avoids double-delivery and lets the plugin control rendering. The existing `NotificationChannel` implementations (webhook, Slack incoming webhook, email) remain as fallbacks for deployments without a chat plugin.

### Verdict: Notifications **should flow through the plugin** when present

> **Confirmed (feedback):** Strongly agreed. This is the correct approach.

The `ChannelRegistry` remains for simple one-way integrations (webhook, email). But when a broker plugin provides a richer chat integration, notification delivery should be routed through the broker's `Publish()` path instead of the channel registry, to avoid duplication and enable interactive notification cards.

---

## Surface 3: API Operations

### The problem

When a user types `/scion start my-agent` in chat, something needs to call `AgentService.Start()` on the Hub. This requires:

1. **User identity resolution** — map the chat user to a Hub user
2. **Authorization** — make the API call with the mapped user's permissions
3. **Response rendering** — format the API response as a chat card

None of this fits the plugin model. The plugin interface is about message transport — `Publish`/`Subscribe` on topics with `StructuredMessage`. It has no concept of:
- Authenticated API calls
- Request/response patterns (it's fire-and-forget)
- User identity mapping
- Interactive dialogs (command input, confirmation)

### Why not extend the plugin further?

We could theoretically add RPC methods to the plugin for API operations:

```go
// Hypothetical — NOT recommended
type ChatPluginInterface interface {
    MessageBrokerPluginInterface
    HandleCommand(ctx context.Context, cmd Command) (*CommandResponse, error)
    HandleAction(ctx context.Context, action ActionCallback) (*ActionResponse, error)
    ResolveUser(ctx context.Context, platformUserID string) (*UserMapping, error)
}
```

This would be a mistake:

1. **Identity & auth don't belong in plugins.** The plugin runs as a separate process. Giving it the ability to impersonate users and make arbitrary API calls means giving it access to signing keys and broad Hub permissions. The security surface becomes much harder to reason about.

2. **The plugin process model is wrong for this.** Plugins are managed by the Hub's plugin manager — they're child processes that the Hub starts and stops. But command handling requires the chat app to be an HTTP server (receiving webhooks from Google/Slack). The Hub shouldn't own the lifecycle of an HTTP server it doesn't control the routing for.

3. **It conflates transport with application logic.** The plugin API is purposefully narrow: move messages between the Hub's topic system and an external transport. Command parsing, dialog state machines, and user mapping are application logic that belongs in the chat app process.

### Verdict: API operations **stay in the standalone process**

The standalone chat app handles:
- Webhook reception from chat platforms
- Command parsing and routing
- User identity mapping and impersonation
- Dialog/modal lifecycle management
- Direct Hub API calls via `hubclient`

---

## The Hybrid Architecture (Revised: Single Binary)

Combining the three verdicts with Option C (single binary):

```
┌──────────────────────────────────────────────────────────────┐
│                    Chat Platform (Google Chat / Slack)         │
└───────┬───────────────────────────────────┬──────────────────┘
        │ Webhooks (commands, actions,      │ API (send messages,
        │ dialogs, @mentions with intent)   │ update cards)
        ▼                                   ▲
┌───────────────────────────────────────────────────────────────┐
│              scion-chat-app (standalone process)              │
│                                                               │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────────────┐ │
│  │ Command      │  │ User Identity │  │ Dialog/Modal        │ │
│  │ Router       │  │ Mapper        │  │ State Machine       │ │
│  └──────┬──────┘  └──────┬───────┘  └──────────┬───────────┘ │
│         │                │                      │             │
│         └────────────────┼──────────────────────┘             │
│                          │  hubclient.Client                  │
│                          │  (API operations)                  │
└──────────────────────────┼────────────────────────────────────┘
                           │
                           │ HTTPS
                           ▼
┌──────────────────────────────────────────────────────────────┐
│                        Scion Hub                              │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐     │
│  │ MessageBrokerProxy                                    │     │
│  │  subscribes to topics → calls Publish() on plugin     │     │
│  └──────────┬───────────────────────────────────────────┘     │
│             │                                                 │
│  ┌──────────▼───────────────────────────────────────────┐     │
│  │ scion-plugin-googlechat (broker plugin)               │     │
│  │                                                       │     │
│  │  Publish(topic, msg) → render card → Chat API send    │     │
│  │  Inbound chat msg   → POST /api/v1/broker/inbound     │     │
│  └───────────────────────────────────────────────────────┘     │
│                                                               │
│  ┌───────────────────────────────────────────────────────┐    │
│  │ NotificationDispatcher                                 │    │
│  │  agent status → dedup/filter → Publish to broker       │    │
│  │  (falls back to ChannelRegistry if no plugin)          │    │
│  └────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

### Single binary, clear responsibilities

> **Revised (feedback):** The original two-process model is replaced with a single binary.

| Concern | Handled by | Mechanism |
|---------|-----------|-----------|
| Message transport (outbound) | Plugin interface (`Publish`) | Hub calls plugin RPC → binary renders card → Chat API send |
| Message transport (inbound) | Chat app side | Webhook receive → POST to Hub inbound API |
| Commands, identity, dialogs | Chat app side | Webhook receive → `hubclient` API calls → Chat API response |
| Space-grove mapping, state | Chat app side (SQLite) | In-process access, no coordination needed |
| Enrichment (agent metadata) | Chat app side | `hubclient` API calls for metadata, in-process rendering |

### How the flows work

1. **Chat → Agent message**: Chat platform webhook → binary receives it → recognizes it as a message (not a command) → POSTs to Hub inbound API → Hub routes through broker → delivered to agent
2. **Agent → Chat message**: Agent sends message → Hub publishes to broker topic → binary's `Publish()` handler → looks up space mapping (in-process SQLite) → enriches via `hubclient` → renders card → Chat API send
3. **Chat → Command**: Chat platform webhook → binary receives it → parses command → calls Hub API via `hubclient` (e.g., start agent) → renders response card → Chat API send
4. **Notification → Chat**: Agent status change → NotificationDispatcher → publishes to broker → binary's `Publish()` handler → renders notification card → Chat API send

All flows are handled by a single process. No credential sharing, no inter-process coordination.

**Option C: Single process — the plugin IS the app.**
Reject the two-process split. One binary does everything. It's loaded as a plugin for the broker interface but also runs its own HTTP server for webhooks. This requires plugin lifecycle changes (the plugin manager would need to support long-running plugins with their own listeners).

> **Revised (feedback):** Option C was the original intent and is the chosen approach. The single binary acts as both the broker plugin (for message transport) and the chat app (for commands, identity, dialogs). It needs to run outside the plugin manager's standard lifecycle — the plugin manager should support this mode. This eliminates the two-process coordination concern entirely: space routing, enrichment, and state are all in-process.
>
> ~~**Recommendation: Option B** for the MVP.~~ **Recommendation: Option C.** Single binary that implements the broker plugin interface for message transport while also running its own HTTP server for chat platform webhooks and owning command routing, identity mapping, and dialog state. The plugin manager needs a lifecycle extension to support long-running plugins with their own listeners (see `.design/message-broker-plugin-evolution.md`).

---

## What Changes in the Plugin API

> **Revised (feedback):** `PublishWithContext` / `DeliveryMeta` are removed from the required changes. The chat app handles enrichment internally. The focus shifts to core plugin machinery evolution.

### Required changes (small, backward-compatible)

1. ~~**`PublishWithContext` method**~~ — **Removed.** Not needed; the chat app binary can call the Hub API for enrichment metadata when it needs it. The core plugin interface stays narrow.

2. **Inbound API enhancement** — `POST /api/v1/broker/inbound` uses `Sender`/`SenderID` fields on `StructuredMessage` to represent the impersonated user identity. No new fields required.

3. **Plugin-initiated subscriptions** — ~~(was optional, now required)~~ Currently the `MessageBrokerProxy` manages subscriptions for the plugin. The plugin should be able to request subscriptions directly (e.g., "subscribe me to all user-message topics in grove X"). This is important for the chat app's requirements — when a space-grove link is created, the plugin side of the binary needs to tell the Hub to start routing messages for that grove. See `.design/message-broker-plugin-evolution.md` for details.

4. **Extended plugin lifecycle** — the plugin manager must support long-running plugins that run outside its standard start/stop lifecycle. The chat app binary needs to run its own HTTP server for webhooks while also serving as a broker plugin. See `.design/message-broker-plugin-evolution.md` for details.

### Future considerations

5. **Bidirectional RPC** — the current plugin model is host→plugin only (the host calls methods on the plugin). Adding plugin→host callbacks would let the plugin push inbound messages directly instead of POSTing to the HTTP API. This is a bigger change to the go-plugin integration. Lower priority given the single-binary model where the HTTP POST path is simple.

---

## What Changes in Notification Delivery

The `NotificationDispatcher.dispatchToChannels()` method currently sends to all channels in the `ChannelRegistry`. With a broker plugin present:

```go
// In storeAndDispatch, after creating the notification record:
case store.SubscriberTypeUser:
    nd.events.PublishNotification(ctx, notif)  // SSE (unchanged)

    // NEW: if a broker plugin is active, publish notification through
    // the broker so the plugin can render rich cards.
    if nd.brokerProxy != nil {
        notifMsg := buildNotificationMessage(sub, notif, agent)
        nd.brokerProxy.PublishUserMessage(ctx, sub.GroveID, sub.SubscriberID, notifMsg)
    }

    // CHANGED: only fall back to channel registry if no broker plugin
    if nd.brokerProxy == nil {
        nd.dispatchToChannels(ctx, sub, notif, agent.ID, agent.Slug)
    }
```

This is a small change to `notifications.go` (lines 285-297). The `ChannelRegistry` (webhook, email, existing Slack incoming webhook) continues to work for deployments without a chat broker plugin.

---

## Comparison Summary

| Aspect | Pure standalone (google-chat.md) | Pure plugin | Hybrid (recommended) |
|--------|----------------------------------|-------------|---------------------|
| Message transport | SSE polling + hubclient | Plugin Publish/Subscribe | Plugin Publish/Subscribe |
| Notification delivery | SSE polling + re-render | Plugin via ChannelRegistry | Plugin via broker Publish |
| Command handling | hubclient API calls | Would need new plugin RPC | hubclient API calls |
| User identity | App-managed | Would need plugin auth | App-managed |
| Dialog/modal state | App-managed | Would need plugin RPC | App-managed |
| Chat webhook receiver | App HTTP server | Plugin HTTP server (lifecycle issue) | App HTTP server |
| Deployment | One process | One process (but wrong lifecycle) | Two processes |
| Plugin API validation | None (API unused) | Fully exercised | Exercised for transport |
| Latency (commands) | Low (direct API→render) | Low | Low (direct API→render) |
| Latency (agent messages) | Medium (SSE poll) | Low (push via Publish) | Low (push via Publish) |
| Hub owns message routing | No (app polls) | Yes | Yes |
| Audit trail | App must log | Hub logs via MessageBrokerProxy | Hub logs via MessageBrokerProxy |

---

## Implementation Impact

### On the chat app (`scion-chat-app`)

- Simpler than the pure-standalone design: no SSE subscription for agent messages, no notification relay logic
- Still owns: webhook handling, command router, identity mapper, dialog state, space-grove linking
- Sends user→agent messages via Hub API (which routes through broker)
- Receives command results directly from Hub API and renders responses

### On the plugin (`scion-plugin-googlechat`)

- Implements `MessageBrokerPluginInterface` with `PublishWithContext` support
- Contains all Google Chat API rendering logic (Cards V2)
- Subscribes (via Hub) to agent message and user message topics for linked groves
- For inbound messages (non-command @mentions), POSTs to Hub inbound API

### On the Hub

- `PublishWithContext` extension to plugin API (~30 lines in `broker_plugin.go`)
- `DeliveryMeta` type definition (~20 lines in `plugin/` or `messages/`)
- Notification dispatcher tweak to route through broker when plugin present (~10 lines in `notifications.go`)
- Optional: `acting_as` field on inbound API (~15 lines in `handlers_broker_inbound.go`)

### On the Slack implementation (future)

The same split applies. `scion-plugin-slack` handles Block Kit rendering and Slack API delivery. `scion-chat-app` already has the platform adapter abstraction for webhook handling, so the Slack adapter slots in alongside the Google Chat adapter in the same standalone process.

---

## Open Questions

> **Resolved (feedback):**

1. **Plugin discovery for chat plugins**: We may need a different lifecycle mechanism for processes that offer a hybrid superset of capabilities (plugin + app). We are not trying to expose the superset as a significant expansion of the message broker plugin API. The plugin manager should support loading these hybrid binaries outside its standard child-process lifecycle. See `.design/message-broker-plugin-evolution.md`.

2. **Space-grove link propagation**: ~~Resolved.~~ Not an issue when plugin and chat app are one binary. The binary maintains space-grove links in its local SQLite database and both the plugin and app sides have in-process access.

3. **Single binary option**: ~~Resolved.~~ Yes. This is the chosen architecture (Option C). The single binary acts as plugin+chat app. It can persist state in SQLite as needed.

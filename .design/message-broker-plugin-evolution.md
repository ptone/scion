# Message Broker Plugin Evolution

**Created:** 2026-04-05
**Status:** Draft
**Related:** `.design/chat-plugin-tradeoffs.md`, `pkg/plugin/broker_plugin.go`, `pkg/plugin/manager.go`, `pkg/hub/messagebroker.go`, `pkg/hub/notifications.go`

---

## Context

The `MessageBrokerPluginInterface` was designed as a narrow transport abstraction: move `StructuredMessage` values between the Hub's topic system and an external message transport. It has not yet been exercised by a real consumer.

The upcoming chat app integration (`.design/chat-plugin-tradeoffs.md`) will be the first real consumer. Feedback on the tradeoffs analysis confirmed that:

- The core plugin interface should remain narrow — no `PublishWithContext`, no `DeliveryMeta`, no chat-specific extensions
- The chat app runs as a single binary that implements the broker plugin interface for transport while also running its own HTTP server and application logic (Option C)
- Plugin-initiated subscriptions are important for the chat app and should be part of the core plugin machinery
- The plugin manager needs a lifecycle mechanism for hybrid binaries that are more than simple child processes

This document focuses on evolving the **core plugin machinery** to support these needs. It deliberately excludes chat-app-specific concerns (rendering, identity mapping, command routing, dialog state) — those are superset capabilities of the chat app binary, not extensions to the plugin API.

---

## Current State

### Plugin Interface (`pkg/plugin/broker_plugin.go`)

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

### Plugin Manager (`pkg/plugin/manager.go`)

- Discovers plugins by binary name (`scion-plugin-<name>`) or config
- Starts plugin as a child process via `hashicorp/go-plugin`
- Calls `Configure()` immediately after loading
- Dispenses `BrokerRPCClient` wrapped in `BrokerPluginAdapter`
- `Shutdown()` kills all plugin processes

### Message Broker Proxy (`pkg/hub/messagebroker.go`)

- Bridges `broker.MessageBroker` with the Hub's agent lifecycle
- **Manages all subscriptions on behalf of the plugin** — the plugin does not control which topics it receives
- Subscribes to agent lifecycle events and dynamically creates per-agent, per-grove, and global broadcast subscriptions
- Routes inbound messages to agents via `DispatchAgentMessage()`

### Notification Dispatcher (`pkg/hub/notifications.go`)

- Listens for agent status events, matches subscriptions, stores notifications
- Dispatches to agents (via dispatcher), users (via SSE), and external channels (via `ChannelRegistry`)
- `ChannelRegistry` delivers to webhooks, Slack incoming webhooks, and email — fire-and-forget, one-way
- No integration with the broker plugin path

---

## Evolution 1: Plugin-Initiated Subscriptions

### Problem

Currently, the `MessageBrokerProxy` manages all subscriptions. When a new agent is created, the proxy subscribes to that agent's message topic on the broker. The plugin has no say in which topics it receives.

This works for the simple case where the Hub knows everything about what the plugin needs. But a chat app plugin needs to subscribe to topics based on its own state — specifically, which groves are linked to chat spaces. When a user runs `/scion link production` in a chat space, the plugin needs to start receiving messages for the `production` grove immediately.

The proxy doesn't know about space-grove links (that's chat-app state), so it can't manage these subscriptions.

### Proposed Change

Add a `RequestSubscription` / `CancelSubscription` RPC path from plugin to host. This is a new capability that plugins can advertise and use.

#### Plugin Side

The plugin calls a host-provided RPC endpoint to request subscriptions:

```go
// HostCallbacks is an interface the plugin can call back to the host.
// Provided to the plugin via Configure or a new initialization method.
type HostCallbacks interface {
    // RequestSubscription asks the host to start routing messages
    // matching the given pattern to this plugin's Publish() method.
    RequestSubscription(pattern string) error

    // CancelSubscription asks the host to stop routing messages
    // matching the given pattern.
    CancelSubscription(pattern string) error
}
```

#### Host Side

The `MessageBrokerProxy` handles subscription requests from the plugin by creating broker subscriptions that route back to the plugin's `Publish()`:

```go
// In MessageBrokerProxy:
func (p *MessageBrokerProxy) HandlePluginSubscriptionRequest(pattern string) error {
    sub, err := p.broker.Subscribe(pattern, func(ctx context.Context, topic string, msg *messages.StructuredMessage) {
        // Route the message back to the plugin for delivery
        if err := p.broker.Publish(ctx, topic, msg); err != nil {
            p.log.Error("Failed to deliver plugin-requested message", "pattern", pattern, "error", err)
        }
    })
    if err != nil {
        return err
    }
    p.mu.Lock()
    p.pluginSubscriptions[pattern] = sub
    p.mu.Unlock()
    return nil
}
```

#### Interaction with Existing Subscriptions

Plugin-initiated subscriptions coexist with proxy-managed subscriptions:

- **Proxy-managed**: Agent lifecycle-driven (agent created → subscribe, agent deleted → unsubscribe). These continue unchanged.
- **Plugin-initiated**: Plugin-driven (space linked → subscribe, space unlinked → unsubscribe). These are additive.

The proxy deduplicates: if a proxy-managed subscription already covers the requested pattern, the plugin request is a no-op (but tracked so `CancelSubscription` works correctly).

#### Implementation Approach

The `hashicorp/go-plugin` framework supports bidirectional RPC via `MuxBroker`. The host can expose an RPC server that the plugin calls:

1. During `Configure()`, pass a `host_callback_port` or similar connection info in the config map
2. The plugin connects back to the host's callback RPC server
3. Calls `RequestSubscription` / `CancelSubscription` as needed

Alternatively, for the single-binary architecture (Option C), the plugin and host share the same process, so callbacks can be direct function calls with no RPC overhead.

#### Changes Required

| File | Change | Size |
|------|--------|------|
| `pkg/plugin/broker_plugin.go` | Add `HostCallbacks` interface and RPC types | ~40 lines |
| `pkg/plugin/broker_plugin.go` | Add `HostCallbackServer` / `HostCallbackClient` RPC wrappers | ~60 lines |
| `pkg/hub/messagebroker.go` | Add `HandlePluginSubscriptionRequest` / `HandlePluginCancelSubscription` | ~40 lines |
| `pkg/hub/messagebroker.go` | Track plugin-initiated subscriptions separately | ~20 lines |
| `pkg/plugin/manager.go` | Pass host callback connection info during `loadPlugin` | ~15 lines |

---

## Evolution 2: Notification Routing Through Broker

### Problem

The `NotificationDispatcher` and `ChannelRegistry` deliver notifications to external systems independently of the broker plugin. When a broker plugin is present (e.g., the chat app), notifications should flow through the broker so the plugin can render them as rich interactive cards.

Currently, a deployment with both a chat broker plugin and a Slack incoming webhook would double-deliver notifications to Slack — once via the plugin's `Publish()` (for agent messages) and once via the `ChannelRegistry` (for notifications).

### Proposed Change

When a broker plugin is active, the `NotificationDispatcher` routes user-targeted notifications through the broker's `Publish()` path instead of the `ChannelRegistry`. The `ChannelRegistry` becomes a fallback for deployments without a broker plugin.

#### In `notifications.go`, `storeAndDispatch()`:

```go
case store.SubscriberTypeUser:
    nd.events.PublishNotification(ctx, notif)  // SSE (always)

    if nd.brokerProxy != nil {
        // Broker plugin present — publish notification as a StructuredMessage
        // through the broker so the plugin can render it.
        notifMsg := buildNotificationMessage(sub, notif, agent)
        nd.brokerProxy.PublishUserMessage(ctx, sub.GroveID, sub.SubscriberID, notifMsg)
    } else {
        // No broker plugin — fall back to channel registry (webhook/email)
        nd.dispatchToChannels(ctx, sub, notif, agent.ID, agent.Slug)
    }
```

#### Wiring

The `NotificationDispatcher` needs a reference to `MessageBrokerProxy` (currently it has none). Add an optional field:

```go
type NotificationDispatcher struct {
    // ... existing fields ...
    brokerProxy *MessageBrokerProxy // nil = no broker plugin, use ChannelRegistry
}
```

Set via a new `SetBrokerProxy()` method, called from `Server.StartMessageBroker()` when a broker is configured.

#### Changes Required

| File | Change | Size |
|------|--------|------|
| `pkg/hub/notifications.go` | Add `brokerProxy` field, `SetBrokerProxy()` method | ~10 lines |
| `pkg/hub/notifications.go` | Modify `storeAndDispatch()` to route through broker when available | ~15 lines |
| `pkg/hub/server.go` | Wire broker proxy to notification dispatcher in `StartMessageBroker()` | ~5 lines |

---

## Evolution 3: Extended Plugin Lifecycle

### Problem

The current plugin manager treats plugins as child processes with a simple lifecycle: start → configure → serve RPC → kill. The manager owns the process.

A chat app plugin needs to:
- Run its own HTTP server (for receiving chat platform webhooks)
- Manage its own long-running connections (to the Hub API via `hubclient`)
- Persist state in SQLite
- Potentially outlive the plugin manager's standard lifecycle

This doesn't fit the child-process model where the Hub starts and stops the plugin.

### Proposed Change: Self-Managed Plugin Mode

Add a lifecycle mode where the plugin binary manages its own process lifecycle. The Hub connects to an already-running plugin rather than starting it.

#### Plugin Manager Extension

```go
type PluginEntry struct {
    // ... existing fields ...

    // SelfManaged indicates the plugin manages its own lifecycle.
    // The Hub connects to the plugin's RPC server rather than starting it.
    // The plugin is responsible for its own startup/shutdown.
    SelfManaged bool              `json:"self_managed,omitempty" yaml:"self_managed,omitempty"`

    // Address is the RPC address for self-managed plugins.
    // Required when SelfManaged is true.
    Address     string            `json:"address,omitempty" yaml:"address,omitempty"`
}
```

#### Loading Flow for Self-Managed Plugins

Instead of `exec.Command(dp.Path)` → `goplugin.NewClient(...)`, the manager:

1. Connects to the plugin's existing RPC server at the configured address
2. Dispenses the plugin interface as usual
3. Calls `Configure()` as usual
4. Does **not** own the process — `Shutdown()` calls `Close()` but does not kill the process

The `hashicorp/go-plugin` library supports this via `goplugin.ClientConfig.Reattach` or by using a custom net/rpc client directly.

#### Configuration Example

```yaml
plugins:
  broker:
    googlechat:
      self_managed: true
      address: "localhost:9090"
      config:
        hub_endpoint: "https://hub.example.com"
        project_id: "my-gcp-project"
```

#### Changes Required

| File | Change | Size |
|------|--------|------|
| `pkg/plugin/config.go` | Add `SelfManaged`, `Address` fields to `PluginEntry` | ~5 lines |
| `pkg/plugin/manager.go` | Add self-managed loading path in `loadPlugin()` | ~30 lines |
| `pkg/plugin/manager.go` | Skip `Kill()` for self-managed plugins in `Shutdown()` | ~10 lines |

---

## Evolution Summary

| Evolution | What | Why | Core Plugin API Change? |
|-----------|------|-----|------------------------|
| **1. Plugin-initiated subscriptions** | Plugin can request/cancel subscriptions on the host | Chat app needs to subscribe to groves dynamically as spaces are linked | Yes — new `HostCallbacks` interface |
| **2. Notification routing** | Notifications flow through broker when plugin is present | Avoid double-delivery, enable rich notification rendering | No — Hub-internal wiring change |
| **3. Extended lifecycle** | Self-managed plugin mode in the plugin manager | Chat app binary manages its own process, HTTP server, state | No — plugin manager config/loading change |

All three evolutions are backward-compatible. Existing plugins (if any) continue to work unchanged. The `MessageBrokerPluginInterface` itself gains no new methods — Evolution 1 adds a separate `HostCallbacks` interface that plugins can optionally use.

---

## What This Document Does NOT Cover

The following are chat-app superset concerns, not core plugin machinery:

- **Rich card rendering** — the chat app binary handles platform-specific UI rendering internally
- **User identity mapping** — the chat app maintains user registrations and issues impersonated API calls via `hubclient`
- **Command routing and parsing** — application logic in the chat app, not the plugin interface
- **Dialog/modal state machines** — chat platform interaction patterns managed by the app
- **Space-grove link persistence** — SQLite state in the chat app binary
- **Message enrichment** — the chat app calls the Hub API (agent metadata, grove info) to enrich messages before rendering; this is not pushed into the plugin interface via `PublishWithContext` or similar

These capabilities make the chat app a superset of a basic message broker plugin. The plugin interface remains a narrow transport contract: `Configure`, `Publish`, `Subscribe`, `Unsubscribe`, `Close`, `GetInfo`, `HealthCheck`.

---

## Implementation Order

1. **Evolution 2 (Notification routing)** — smallest change, immediately useful, no plugin API impact
2. **Evolution 3 (Extended lifecycle)** — enables the chat app binary to connect to the Hub as a self-managed plugin
3. **Evolution 1 (Plugin-initiated subscriptions)** — most complex, enables dynamic grove subscription management

Evolutions 2 and 3 are independent and can be implemented in parallel. Evolution 1 can be deferred if the initial chat app uses `Configure()` to pass an initial set of grove subscriptions and restarts to pick up changes.

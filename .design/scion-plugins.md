# Scion Plugin System Design

## Motivation

Scion currently hard-codes all message broker implementations (in-process only) and harness implementations (claude, gemini, opencode, codex, generic) directly into the binary. As we add external message brokers (NATS, Redis, etc.) and potentially new harnesses, this approach does not scale:

- Every new implementation increases binary size and dependency surface
- Users cannot add custom integrations without forking the project
- The hub/broker server carries code for harnesses it may never use

We want a **plugin system** that allows scion to load additional message broker and harness implementations at runtime from external binaries.

## Technology: hashicorp/go-plugin

[hashicorp/go-plugin](https://github.com/hashicorp/go-plugin) provides the foundation:

- **Subprocess model**: Each plugin runs as a separate OS process, communicating via go-plugin's RPC layer (net/rpc or gRPC)
- **Crash isolation**: A plugin crash does not bring down the host
- **Language agnostic**: gRPC plugins can be written in any language
- **Versioning**: Protocol version negotiation between host and plugin
- **Security**: Magic cookie handshake prevents accidental plugin execution
- **Health checking**: Built-in gRPC health service

### Key go-plugin Lifecycle

1. Host calls `plugin.NewClient()` with the path to a plugin binary
2. Host calls `client.Client()` then `raw.Dispense("pluginName")` to get a typed interface
3. The plugin subprocess starts and stays running for the lifetime of the `Client`
4. Host calls methods on the dispensed interface; these become RPC calls
5. `client.Kill()` terminates the subprocess (graceful then force after 2s)

### Long-Running vs Per-Use

go-plugin is designed for **long-lived subprocesses**. The client starts the process once and reuses it for all calls. Per-invocation usage (start, call, kill) is technically possible but adds process-spawn overhead on every call.

**Implications for scion:**

| Plugin Type | Lifecycle | Rationale |
|---|---|---|
| Message Broker | Long-running | Brokers maintain connections, subscriptions, state. Must persist for the hub/broker server lifetime. |
| Harness | Per-agent-lifecycle | Harness methods are called during agent create/start/provision. Could be long-running (shared across agents) or per-use. |

**Recommendation**: Use long-running plugin processes for both types. For harnesses, one plugin process serves all agents using that harness - the overhead of keeping it alive is negligible vs. respawning per agent operation.

### RPC Layer: net/rpc vs gRPC

go-plugin supports two RPC transports: Go's built-in `net/rpc` and gRPC. Since we have zero external broker implementations today, we have freedom to choose the simplest option.

**Decision: Use go-plugin's `net/rpc` for Go plugins; support gRPC only for non-Go plugin authors.**

Rationale:
- `net/rpc` is simpler for Go-to-Go communication — no protobuf code generation, no `.proto` files to maintain
- The `MessageBroker` interface is small (3 methods) and maps directly to Go RPC
- If a plugin needs to talk to a gRPC-based backend (e.g., an external NATS or OpenClaw gateway), **that is internal to the plugin** — the plugin's external protocol does not dictate the host↔plugin protocol
- gRPC support can be added later for polyglot plugins without breaking existing Go plugins

## Plugin Types

### Type 1: Message Broker (`broker`)

Implements the `broker.MessageBroker` interface across the plugin boundary:

```go
// Plugin-side interface
type MessageBrokerPlugin interface {
    Configure(config map[string]string) error
    Publish(ctx context.Context, topic string, msg *messages.StructuredMessage) error
    Subscribe(pattern string) error
    Unsubscribe(pattern string) error
    Close() error
}
```

**Key considerations:**
- The in-process `Subscribe()` uses a callback-based `MessageHandler`, which cannot cross process boundaries directly. Instead of polling or reverse RPC, the plugin delivers inbound messages via the hub's existing API endpoints (see "Subscription Delivery via Hub API" in Decisions)
- The plugin maintains the external connection (NATS, Redis, etc.) internally
- Configuration (connection URLs, auth, hub API endpoint) passed via `Configure(map[string]string)` at startup
- Plugin must handle reconnection to the backing service internally
- The host-side adapter wraps the plugin RPC client to satisfy the existing `broker.MessageBroker` interface; for the subscribe path, the adapter simply calls `Subscribe()` on the plugin — actual message delivery happens out-of-band via the hub API

### Type 2: Harness (`harness`)

Implements the `api.Harness` interface over RPC. The current interface has ~15 methods, most of which are simple getters or file operations.

**Key considerations:**
- `GetHarnessEmbedsFS()` returns an `embed.FS` — cannot cross process boundaries. Plugin harnesses should instead write their embedded files directly during `Provision()`, since the plugin has filesystem access to the same paths. This is the closest analog to how built-in harnesses work.
- `Provision()` operates on the local filesystem (agent home dir). The plugin process must have filesystem access to the same paths.
- Some methods are pure data (`Name()`, `GetEnv()`, `GetCommand()`) and could be batched into a single `GetMetadata()` call to reduce round-trips.
- Optional interfaces (`AuthSettingsApplier`, `TelemetrySettingsApplier`) need capability advertisement.

## Plugin Discovery and Loading

### Filesystem Layout

```
~/.scion/plugins/
  broker/
    scion-plugin-nats        # Message broker plugin
    scion-plugin-redis       # Message broker plugin
  harness/
    scion-plugin-cursor      # Harness plugin
    scion-plugin-aider       # Harness plugin
```

Plugin binaries follow a naming convention: `scion-plugin-<name>`.

### Settings Configuration

Add a `plugins` section to settings:

```yaml
plugins:
  broker:
    nats:
      path: ~/.scion/plugins/broker/scion-plugin-nats  # optional, auto-discovered if omitted
      config:
        url: "nats://localhost:4222"
        credentials_file: "/path/to/creds"
  harness:
    cursor:
      path: ~/.scion/plugins/harness/scion-plugin-cursor
      config:
        image: "cursor-agent:latest"
        user: "cursor"
```

**Discovery order:**
1. Explicit `path` in settings
2. Scan `~/.scion/plugins/<type>/` directory
3. Search `$PATH` for `scion-plugin-<name>` (lower priority, optional)

### Active Plugin Selection

For message brokers, the active broker is selected in server config:

```yaml
# In hub/broker server config
message_broker: nats   # selects the "nats" plugin (or "inprocess" for built-in)
```

The design should accommodate future multi-broker configurations (see "Multiple Active Brokers" in Decisions), so internally the broker selection should resolve through a registry that can hold multiple loaded broker plugins. A future routing layer would manage multiple active brokers in a gateway pattern.

For harnesses, plugin harnesses are available alongside built-in ones. The harness factory (`harness.New()`) checks plugins after built-in types:

```go
func New(harnessName string) api.Harness {
    switch harnessName {
    case "claude": return &ClaudeCode{}
    // ... built-in harnesses
    default:
        if plugin, ok := pluginRegistry.GetHarness(harnessName); ok {
            return plugin
        }
        return &Generic{}
    }
}
```

## Plugin Registration

### Static Registration (Settings-based)

Plugins are declared in settings and loaded at startup. This is sufficient for the initial implementation:

- CLI reads settings, loads relevant plugins when needed
- Hub/broker server loads all configured plugins at startup
- No runtime registration needed

Dynamic self-registration via a hub API endpoint is deferred as a future enhancement. The static approach is simpler, debuggable, and covers the primary use cases.

## Local Mode Support

**Should plugins work in local (non-hub) mode?**

| Plugin Type | Local Mode? | Rationale |
|---|---|---|
| Message Broker | No (initially) | Messaging is a hub/broker feature. Local mode uses the CLI directly - no pub/sub needed. |
| Harness | Yes | A user may want to use a custom harness (e.g., Cursor, Aider) in local mode. The harness interface is used for agent create/start regardless of hub vs local. |

For harness plugins in local mode:
- Plugin process is started on-demand when an agent using that harness is created/started
- Plugin process is kept alive for the duration of the CLI command
- Cleaned up on CLI exit (go-plugin handles this via `CleanupClients()`)

## Implementation Architecture

### Core Package: `pkg/plugin`

```
pkg/plugin/
  manager.go          # Plugin lifecycle management (load, start, stop, health)
  registry.go         # Type-safe plugin registry
  discovery.go        # Filesystem scanning and settings-based discovery
  config.go           # Plugin configuration types
  broker_plugin.go    # RPC client/server wrapper for MessageBroker plugins
  harness_plugin.go   # RPC client/server wrapper for Harness plugins
```

Note: With `net/rpc`, no `.proto` files are needed. The RPC interface is defined in Go code using go-plugin's `plugin.Plugin` interface pattern. If gRPC support is added later for polyglot plugins, proto files would be added at that time.

### Plugin Manager

Central component that owns plugin lifecycle:

```go
type Manager struct {
    clients  map[string]*plugin.Client  // "type:name" -> client
    mu       sync.RWMutex
}

func (m *Manager) LoadAll(cfg PluginsConfig) error     // Load from settings
func (m *Manager) Get(pluginType, name string) (interface{}, error)
func (m *Manager) GetBroker(name string) (broker.MessageBroker, error)
func (m *Manager) GetHarness(name string) (api.Harness, error)
func (m *Manager) Shutdown()                            // Kill all plugins
```

### Plugin Lifecycle Tied to Server Lifecycle

Plugin processes are started when the hub/broker server starts and stopped when it stops. The plugin manager's `Shutdown()` is called as part of the server's graceful shutdown sequence. On `scion server restart` or `scion broker restart`, all plugin processes are killed and restarted with the new server instance.

### Integration Points

**Hub Server** (`pkg/hub/server.go`):
- `Server` receives a `*plugin.Manager` at construction
- If `message_broker` setting names a plugin, dispense broker from manager
- Plugin broker replaces the in-process broker in `MessageBrokerProxy`

**Runtime Broker** (`pkg/runtimebroker/server.go`):
- Similar to hub - receives plugin manager for harness plugins
- When creating agents with a plugin harness, dispense from manager

**CLI** (`cmd/`):
- For local harness plugins: create a temporary manager, load needed plugin, use, cleanup
- No broker plugins in local mode

**Harness Factory** (`pkg/harness/harness.go`):
- Accept optional `*plugin.Manager` parameter
- Fall through to plugin lookup before defaulting to `Generic`

## RPC Interface Design Considerations

### Broker Plugin

The `broker.MessageBroker` interface is small and maps well to RPC. The main challenge — `Subscribe()` uses a callback-based `MessageHandler` that cannot cross process boundaries — is solved by having the plugin deliver inbound messages via the hub API (see "Subscription Delivery via Hub API" in Decisions).

**Host-side adapter:**

```go
type brokerPluginClient struct {
    client *rpc.Client
}

func (b *brokerPluginClient) Publish(ctx context.Context, topic string, msg *StructuredMessage) error {
    return b.client.Call("Plugin.Publish", &PublishArgs{Topic: topic, Msg: msg}, nil)
}

func (b *brokerPluginClient) Subscribe(pattern string, handler MessageHandler) (Subscription, error) {
    // handler is not forwarded to the plugin — inbound delivery happens via hub API
    err := b.client.Call("Plugin.Subscribe", pattern, nil)
    // Return a Subscription that calls Plugin.Unsubscribe on cancel
}
```

The adapter's `Subscribe()` tells the plugin to start listening on the external broker. The `MessageHandler` callback is not used for plugin brokers — messages arrive via the hub API instead, where the hub dispatches them through its existing `DispatchAgentMessage()` path.

### Harness Plugin

The harness interface has several methods that don't translate directly:

| Method | Challenge | Solution |
|---|---|---|
| `GetHarnessEmbedsFS()` | Returns `embed.FS` | Plugin writes its own embedded files during `Provision()` directly to the agent home directory. `GetHarnessEmbedsFS()` returns nil or empty for plugin harnesses. |
| `Provision()` | Writes to local filesystem | Plugin has filesystem access to the same paths; pass paths and let plugin write |
| `InjectAgentInstructions()` | Writes to local filesystem | Same as Provision |
| `ResolveAuth()` | Complex types | Serialize as JSON over RPC (Go's `encoding/gob` handles this natively for `net/rpc`) |

**Capability advertisement**: Plugin responds to a `GetCapabilities()` call indicating which optional interfaces it supports (auth settings, telemetry settings).

## Decisions

| Topic | Decision | Rationale |
|---|---|---|
| Host↔Plugin RPC | Use `net/rpc` for Go plugins | Simpler than gRPC; no proto files. Plugin handles external protocols internally. gRPC option deferred for polyglot support. |
| Harness embed files | Plugin writes files during `Provision()` (option c) | Closest to built-in behavior. Plugin has filesystem access, so it can write directly. |
| Plugin config schema | Opaque `map[string]string` validated by plugin (option b) | Keep it simple for v1. Plugin returns clear errors for invalid config. |
| Security model | Simple trust — user-installed binaries, magic cookie handshake | No signature verification or mTLS for now. Same trust model as any user-installed binary. |
| Dynamic registration | Deferred | Static settings-based registration covers primary use cases. |
| Hot reload | Deferred | Plugin lifecycle tied to server start/stop/restart. No watch-and-reload. |
| Plugin distribution | Deferred | Manual install to `~/.scion/plugins/<type>/`. Future `scion plugin install` command possible. |
| Subscription delivery | Hub API callback (see below) | Plugin delivers inbound messages via the hub's existing authenticated API, avoiding polling/reverse-RPC complexity. |
| Plugin versioning | Strict version check; reject incompatible | go-plugin protocol version negotiation with hard rejection on mismatch. No graceful degradation. |
| Multiple brokers | Deferred; design accommodates | Registry supports multiple loaded plugins. Future work: multiple active brokers with a routing layer. |
| Harness `GetHarnessEmbedsFS()` | Return nil for plugin harnesses | `Provision()` flow handles nil gracefully; plugin writes files directly during provisioning. |

### Subscription Delivery via Hub API

The original design proposed polling or reverse RPC for delivering messages from a broker plugin back to the host. A better approach exists: **the broker plugin delivers inbound messages through the hub's existing API**.

The hub already exposes authenticated endpoints for message delivery:
- `POST /api/v1/agents/{agentId}/message` — deliver to a specific agent
- `POST /api/v1/groves/{groveId}/broadcast` — broadcast to a grove

The broker plugin can use these endpoints directly, authenticating via the existing broker HMAC mechanism or a dedicated plugin credential. This eliminates the need for polling loops or a reverse RPC server entirely.

**Message flow with hub API delivery:**

```
Outbound (hub → external):
  Hub → broker.Publish() → [RPC] → Plugin → NATS/Redis

Inbound (external → hub):
  NATS/Redis → Plugin → hub API (POST /api/v1/agents/{id}/message) → Hub dispatches to agent
```

This is the preferred approach because:
- Reuses existing authenticated infrastructure — no new transport to build
- The hub API already handles agent dispatch, fan-out, and audit logging
- No streaming or polling required over the RPC boundary
- The plugin's RPC interface becomes simpler: only `Publish()`, `Subscribe()`, `Unsubscribe()`, and `Close()` — no `ReceiveMessages()` needed

**Implications for the RPC interface:**

The plugin-side broker interface simplifies to:

```go
type MessageBrokerPlugin interface {
    Configure(config map[string]string) error
    Publish(ctx context.Context, topic string, msg *messages.StructuredMessage) error
    Subscribe(pattern string) error       // Plugin starts delivering via hub API
    Unsubscribe(pattern string) error
    Close() error
}
```

The `Subscribe()` call tells the plugin to start listening on the external broker for the given pattern. When messages arrive, the plugin delivers them to the hub API autonomously — no host-side polling needed.

### Plugin Versioning

Scion uses go-plugin's protocol version negotiation with **strict matching**:
- Each plugin type (broker, harness) has a protocol version number (starting at 1)
- On `plugin.NewClient()`, scion specifies the expected protocol version
- go-plugin rejects plugins that report a different version — the plugin process is killed and an error is returned
- Any change to the RPC method signatures, argument types, or semantics constitutes a breaking change requiring a version bump
- Plugins can report their minimum compatible scion version via a `GetInfo()` RPC call; scion logs a warning if the plugin targets a newer scion version

### Multiple Active Brokers

The current design loads one active message broker. Future support for multiple active brokers would follow a gateway/router pattern:

- Multiple broker plugins loaded and active simultaneously (e.g., NATS for inter-agent messaging, Redis for notifications)
- A routing layer determines which broker handles each `Publish()` and `Subscribe()` call based on topic patterns, message types, or explicit configuration
- The plugin manager's registry (keyed by `type:name`) already supports loading multiple broker plugins — the missing piece is the routing logic

This is deferred but the registry and plugin lifecycle design intentionally accommodate it. The routing layer design will be specified when a concrete multi-broker use case is prioritized.

## Open Questions

### 1. Plugin Authentication for Hub API Callbacks

When a broker plugin delivers inbound messages via the hub API, it needs to authenticate. Options:

- **Broker HMAC auth**: The plugin is provisioned with the same HMAC credentials as a runtime broker. This is natural since broker plugins run on the same host as the hub/broker server, but conflates "plugin" identity with "runtime broker" identity.
- **Dedicated plugin credentials**: A new auth type (e.g., `X-Scion-Plugin-Token`) issued to the plugin at startup via the `Configure()` call. Cleaner separation of concerns but adds a new auth path.
- **Inherit host credentials**: Since the plugin runs as a subprocess of the hub server, it could receive the hub's own internal auth token. Simplest, but blurs security boundaries.

**Recommendation**: Start with broker HMAC auth since the infrastructure already exists. The plugin receives broker credentials as part of its `Configure()` config map. Revisit if the identity conflation causes issues.

### 2. Circular Message Delivery Prevention

When a broker plugin delivers an inbound message via the hub API, the hub must not re-publish that message through the broker plugin (creating an infinite loop). The hub's `MessageBrokerProxy` currently subscribes to broker topics and dispatches via `DispatchAgentMessage()` — if inbound messages arrive via the hub API instead, the proxy's subscription-based dispatch is bypassed for those messages.

This needs clear delineation:
- **Outbound path**: Hub → `MessageBrokerProxy.PublishMessage()` → `broker.Publish()` → plugin RPC → external system
- **Inbound path**: External system → plugin → hub API → `DispatchAgentMessage()` directly (bypasses broker)

The risk is that an outbound publish to the external broker might echo back as an inbound message. The plugin must either:
- Filter out messages that originated from the hub (e.g., by sender metadata or a message ID seen-set)
- Use separate external broker topics/channels for inbound vs outbound to prevent echo

### 3. Plugin Topic-to-Agent Mapping

When the broker plugin receives a message from the external system (NATS/Redis), it needs to know which hub API endpoint to call — specifically, which `agentId` or `groveId` the message targets. Options:

- **Topic convention**: The external broker uses the same topic hierarchy as scion internally (`scion.grove.<groveId>.agent.<agentSlug>.messages`). The plugin parses the topic to extract agent/grove identifiers.
- **Subscription metadata**: When the host calls `Subscribe(pattern)`, it includes metadata mapping patterns to agent/grove IDs. The plugin uses this mapping for delivery.
- **Pass-through**: The plugin delivers all inbound messages to a single hub API endpoint (e.g., a new `/api/v1/broker/inbound` route) with the original topic, and the hub handles routing internally.

**Recommendation**: The pass-through approach is cleanest — it keeps routing logic in the hub where it belongs and avoids duplicating topic-parsing logic in every plugin. A new internal API endpoint for plugin message delivery would accept `{topic, message}` and route accordingly.

### 4. net/rpc Suitability for Broker Plugins

With hub API callbacks handling inbound message delivery, the `net/rpc` streaming limitation is largely mitigated — the plugin RPC interface becomes request/response only (Publish, Subscribe, Unsubscribe, Close). However, if future requirements add RPC methods that benefit from streaming (e.g., bulk operations, health telemetry), switching broker plugins to gRPC may be warranted. Monitor during NATS plugin implementation.

### 5. Plugin Behavior During Hub Unavailability

If the hub API is temporarily unavailable when the broker plugin tries to deliver an inbound message:
- Should the plugin buffer messages and retry? If so, what are the buffer limits?
- Should it drop messages and log warnings?
- Should it report health degradation back to the host via the RPC channel?

This matters for production reliability but can be deferred to Phase 2 (NATS plugin implementation) when real failure modes are encountered.

## Phased Implementation Plan

### Phase 1: Plugin Infrastructure
- `pkg/plugin/` package with Manager, Registry, Discovery
- `net/rpc` interface definitions for broker plugin
- Settings schema additions for `plugins` section
- Integration with hub/broker server lifecycle (start/stop/restart)

### Phase 2: Message Broker Plugins
- NATS broker plugin (first external implementation)
- Host-side adapter wrapping plugin RPC client as `broker.MessageBroker`
- Hub API inbound endpoint for plugin message delivery (or reuse existing endpoints)
- Plugin authentication and circular delivery prevention
- Test the full lifecycle: discovery, loading, configuration, publish, subscribe, inbound delivery, shutdown

### Phase 3: Harness Plugins
- `net/rpc` interface definitions for harness plugin
- Refactor `GetHarnessEmbedsFS()` to be nil-safe for plugin harnesses
- Integration with harness factory and local mode
- Example harness plugin

### Phase 4: Polish
- `scion plugin list` command showing discovered/loaded plugins
- Health status reporting
- Documentation and plugin authoring guide
- Optional: gRPC plugin support for non-Go plugin authors

## Related Design Documents

- [Message Broker](hosted/hub-messaging.md) - Current messaging architecture
- [Hosted Architecture](hosted/hosted-architecture.md) - Hub/broker separation
- [Server Implementation](hosted/server-implementation-design.md) - Unified server command
- [Settings Schema](settings-schema.md) - Settings configuration format

# Grove Settings CLI Design

## Status: Draft

## Problem

Grove-level Hub settings (e.g., `DefaultHarnessConfig`, `DefaultGCPIdentityMode`, `DefaultGCPIdentityServiceAccountID`, agent limits, resource defaults) can only be managed via the Hub API or web UI. There is no CLI path for reading or writing these settings.

The Hub stores grove settings as grove annotations (see `grove_settings_handlers.go`). These are distinct from the local `settings.yaml` file — they live on the Hub and influence agent provisioning when the Hub applies grove-level defaults (via `applyGroveDefaults`). Today, users working from the CLI must either use `curl` against the Hub API or navigate to the web UI to change these values.

## Goal

Expose grove-level Hub settings through the Scion CLI so users can read and update them without leaving the terminal. The design should feel natural alongside existing CLI patterns and minimize cognitive overhead.

## Current Architecture

### Settings Layers

There are two distinct settings systems that coexist:

1. **Local settings** (`settings.yaml`): Managed by `scion config set/get`. Lives on disk at grove or global scope. Read by the broker during agent provisioning. Keys like `default_harness_config`, `hub.endpoint`, `grove_id`.

2. **Hub grove settings** (annotations): Managed via `PUT /api/v1/groves/:id/settings`. Stored as grove annotations on the Hub (`scion.io/default-harness-config`, etc.). Applied by the Hub during agent creation via `applyGroveDefaults()`.

These are **not** the same thing. A local `default_harness_config` in `settings.yaml` is read by the local broker. A Hub `DefaultHarnessConfig` grove setting is a grove annotation that the Hub applies as a default when provisioning agents through the Hub API.

### Existing CLI Patterns

- **`scion config set <key> <value>`** (`cmd/config.go`): Writes to local `settings.yaml`. Scoped to grove (default) or global (`--global`). Pure local file operation — no Hub interaction.

- **`scion hub env set`** (`cmd/hub_env.go`): Manages Hub-stored environment variables. Uses `--grove` flag with optional value for scope inference. Calls Hub API. This is the closest precedent for "CLI command that writes grove-scoped data to the Hub."

- **`scion hub link`** (`cmd/hub.go`): Establishes the grove-Hub relationship. Stores `hub.groveId`, `hub.linked`, `hub.enabled` in local settings. The link state is the prerequisite for any grove settings operation.

### Hub Grove Settings API

```
GET  /api/v1/groves/:id/settings  → GroveSettings
PUT  /api/v1/groves/:id/settings  → GroveSettings
```

`GroveSettings` struct (`pkg/hubclient/types.go:118`):
```go
type GroveSettings struct {
    ActiveProfile                      string
    DefaultTemplate                    string
    DefaultHarnessConfig               string
    TelemetryEnabled                   *bool
    DefaultMaxTurns                    int
    DefaultMaxModelCalls               int
    DefaultMaxDuration                 string
    DefaultResources                   *GroveResourceSpec
    DefaultGCPIdentityMode             string    // "block", "passthrough", "assign"
    DefaultGCPIdentityServiceAccountID string    // required when mode is "assign"
    Bucket                             *BucketConfig
    Runtimes                           map[string]interface{}
    Harnesses                          map[string]interface{}
    Profiles                           map[string]interface{}
}
```

These are persisted as individual grove annotations (`pkg/hub/grove_settings_handlers.go:27-48`), e.g.:
- `scion.io/default-harness-config`
- `scion.io/default-gcp-identity-mode`
- `scion.io/default-gcp-identity-service-account-id`
- `scion.io/default-max-turns`

## Design Options

### Option A: Extend `scion config set` with `--hub` flag

Make `scion config set` Hub-aware: when invoked with a `--hub` flag (or when the grove is linked and the key is a recognized Hub setting), the command writes to the Hub API instead of or in addition to the local file.

```
scion config set default_harness_config claude-sonnet --hub
scion config get default_harness_config --hub
scion config list --hub
```

**Pros:**
- Single command surface — users don't need to learn a new subcommand tree
- Feels natural: "I'm setting a config value, it just happens to go to the Hub"
- Discoverability — users already know `config set`

**Cons:**
- **Conflation of two different systems.** Local `settings.yaml` keys and Hub grove annotation keys are different namespaces with different semantics. `default_harness_config` in `settings.yaml` means "this broker's default." `DefaultHarnessConfig` on the Hub means "this grove's default across all brokers." Merging them into one command creates ambiguity: did I set it locally, on the Hub, or both?
- **Key mapping complexity.** Local keys use `snake_case` (`default_harness_config`). Hub keys use `camelCase` (`defaultHarnessConfig`). Annotation keys use a third format (`scion.io/default-harness-config`). The command would need a translation layer and users would need to know which format to use.
- **Flag proliferation.** `config set` already has `--global`. Adding `--hub` creates a 3-way scope (local grove, global, hub grove) that's hard to reason about. What does `--hub --global` mean?
- **Error surface.** `config set` currently never fails on network issues — it's a local file write. Adding Hub calls introduces timeouts, auth failures, and connectivity errors into a command that was previously reliable.
- **Incomplete overlap.** Many Hub grove settings have no local equivalent (e.g., `DefaultGCPIdentityMode`, `DefaultMaxTurns`, `DefaultResources`). And many local settings have no Hub equivalent (e.g., `hub.endpoint`, `workspace_path`). The "unified" surface would actually be two disjoint key sets accessed through one confusingly overloaded command.

### Option B: New `scion hub grove settings` subcommand

Add a dedicated subcommand tree under `scion hub` for managing grove settings on the Hub.

```
scion hub grove settings get [key]
scion hub grove settings set <key> <value>
scion hub grove settings list
```

**Pros:**
- **Clear separation of concerns.** `scion config` = local files. `scion hub grove settings` = Hub API. No ambiguity about what's being modified.
- **Follows the `hub env` precedent.** `scion hub env set` already established the pattern of "Hub subcommand for managing Hub-stored grove data." Grove settings are the same category.
- **Independent key namespace.** Hub setting keys can use their natural names without conflicting with local setting keys.
- **Network-aware by design.** Users expect `scion hub *` commands to talk to the Hub. Auth failures, timeouts, and connectivity issues are unsurprising in this context.
- **Grove resolution is already solved.** The `--grove` flag pattern from `hub env` (with inference from local settings) applies directly.

**Cons:**
- Users must learn a new command
- Longer command path (`scion hub grove settings set` vs `scion config set --hub`)
- Two places to look for "settings"

### Option C: Hybrid — `scion config set` auto-pushes recognized keys to Hub

When a user runs `scion config set <key> <value>` in a linked grove, and the key is one of the recognized Hub grove settings, the command writes locally *and* pushes to the Hub automatically.

**Pros:**
- Single gesture for "set this everywhere"

**Cons:**
- **Surprising side effects.** A seemingly local command silently makes network calls and modifies remote state. Users may not realize they're changing Hub settings.
- **Partial failure modes.** What if the local write succeeds but the Hub push fails? Now local and Hub settings are inconsistent.
- **Wrong mental model.** Local and Hub settings serve different purposes. Setting `default_harness_config` locally tells your broker what to use. Setting it on the Hub tells the Hub what to apply as a grove-wide default. These are not the same operation and shouldn't be silently coupled.
- All the key-mapping and namespace issues from Option A

## Recommendation: Option B

**`scion hub grove settings`** is the right approach for the following reasons:

1. **Clean separation** between local config and Hub state matches the actual architecture. The two systems are genuinely different — the CLI should reflect that rather than paper over it.

2. **Precedent alignment** with `scion hub env` — the pattern already exists and works well.

3. **No surprises** — `scion config set` remains a reliable local operation. Hub operations are explicitly in the `hub` namespace where users expect network calls.

4. **Extensibility** — new Hub grove settings can be added without touching the `config` command or worrying about key collisions.

While Option A might feel more "natural" at first glance, the underlying systems are different enough that merging them creates more confusion than convenience. Users who work with both local and Hub settings will appreciate the clarity.

## Detailed Design

### Command Structure

```
scion hub grove settings                    # alias: scion hub grove setting
├── list                                    # List all grove settings
├── get <key>                               # Get a specific setting
└── set <key> <value>                       # Set a specific setting
```

### Command Registration

Add to `cmd/hub.go` (or a new `cmd/hub_grove_settings.go`):

```go
var hubGroveSettingsCmd = &cobra.Command{
    Use:     "settings",
    Aliases: []string{"setting"},
    Short:   "Manage grove settings on the Hub",
    Long:    `View and modify grove-level default settings stored on the Hub.`,
}
```

Register under `hubGrovesCmd`:
```go
hubGrovesCmd.AddCommand(hubGroveSettingsCmd)
hubGroveSettingsCmd.AddCommand(hubGroveSettingsListCmd)
hubGroveSettingsCmd.AddCommand(hubGroveSettingsGetCmd)
hubGroveSettingsCmd.AddCommand(hubGroveSettingsSetCmd)
```

### Grove Resolution

Follow the same pattern as `hub env` commands:

1. If `--grove=<name-or-id>` is provided, resolve it via `resolveGroveByNameOrID`
2. If `--grove` is provided with no value (bare flag), infer from local settings (`hub.groveId` → `grove_id`)
3. If no `--grove` flag, infer from the current grove context (same as bare `--grove`)

For grove settings, defaulting to the current grove context (no flag required) makes sense — you almost always want to configure the grove you're standing in. The `--grove` flag is only needed when targeting a different grove.

```go
func resolveGroveForSettings(cmd *cobra.Command, settings *config.Settings) (string, error) {
    // If --grove flag provided with explicit value
    if cmd.Flags().Changed("grove") && groveFlag != scopeInferSentinel {
        return groveFlag, nil
    }
    // Infer from local hub link
    if settings.Hub != nil && settings.Hub.GroveID != "" {
        return settings.Hub.GroveID, nil
    }
    if settings.GroveID != "" {
        return settings.GroveID, nil
    }
    return "", fmt.Errorf("cannot determine grove: not linked to Hub. Run 'scion hub link' or use --grove=<name>")
}
```

### Setting Key Format

Use the `camelCase` JSON field names from `GroveSettings` as the CLI key namespace. These are the most user-friendly and match the API request/response format:

| CLI Key | GroveSettings Field | Annotation |
|---------|-------------------|------------|
| `defaultHarnessConfig` | `DefaultHarnessConfig` | `scion.io/default-harness-config` |
| `defaultTemplate` | `DefaultTemplate` | `scion.io/default-template` |
| `activeProfile` | `ActiveProfile` | `scion.io/active-profile` |
| `telemetryEnabled` | `TelemetryEnabled` | `scion.io/telemetry-enabled` |
| `defaultMaxTurns` | `DefaultMaxTurns` | `scion.io/default-max-turns` |
| `defaultMaxModelCalls` | `DefaultMaxModelCalls` | `scion.io/default-max-model-calls` |
| `defaultMaxDuration` | `DefaultMaxDuration` | `scion.io/default-max-duration` |
| `defaultGCPIdentityMode` | `DefaultGCPIdentityMode` | `scion.io/default-gcp-identity-mode` |
| `defaultGCPIdentityServiceAccountID` | `DefaultGCPIdentityServiceAccountID` | `scion.io/default-gcp-identity-service-account-id` |

For compound settings like `defaultResources`, support dot-notation:
```
scion hub grove settings set defaultResources.requests.cpu 2
scion hub grove settings set defaultResources.requests.memory 4Gi
scion hub grove settings set defaultResources.disk 20Gi
```

### Implementation: `set`

```go
var hubGroveSettingsSetCmd = &cobra.Command{
    Use:   "set <key> <value>",
    Short: "Set a grove setting on the Hub",
    Args:  cobra.ExactArgs(2),
    RunE: func(cmd *cobra.Command, args []string) error {
        key, value := args[0], args[1]

        resolvedPath, _, err := config.ResolveGrovePath(grovePath)
        if err != nil {
            return fmt.Errorf("failed to resolve grove path: %w", err)
        }
        settings, err := config.LoadSettings(resolvedPath)
        if err != nil {
            return fmt.Errorf("failed to load settings: %w", err)
        }
        client, err := getHubClient(settings)
        if err != nil {
            return err
        }

        groveID, err := resolveGroveForSettings(cmd, settings)
        if err != nil {
            return err
        }

        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()

        // Resolve grove name/slug to UUID if needed
        groveID, err = resolveScopeID(ctx, client, "grove", groveID)
        if err != nil {
            return err
        }

        // Fetch current settings (read-modify-write)
        current, err := client.Groves().GetSettings(ctx, groveID)
        if err != nil {
            return fmt.Errorf("failed to get current settings: %w", err)
        }

        // Apply the key-value update
        if err := applySettingUpdate(current, key, value); err != nil {
            return err
        }

        // Push back
        updated, err := client.Groves().UpdateSettings(ctx, groveID, current)
        if err != nil {
            return fmt.Errorf("failed to update settings: %w", err)
        }

        // Output
        if isJSONOutput() {
            return outputJSON(ActionResult{
                Status:  "success",
                Command: "hub grove settings set",
                Message: fmt.Sprintf("Updated grove setting '%s' to '%s'", key, value),
                Details: map[string]interface{}{"key": key, "value": value, "groveId": groveID},
            })
        }
        fmt.Printf("Updated grove setting '%s' to '%s'\n", key, value)
        return nil
    },
}
```

The `applySettingUpdate` function maps CLI keys to `GroveSettings` struct fields:

```go
func applySettingUpdate(s *hubclient.GroveSettings, key, value string) error {
    switch key {
    case "defaultHarnessConfig":
        s.DefaultHarnessConfig = value
    case "defaultTemplate":
        s.DefaultTemplate = value
    case "activeProfile":
        s.ActiveProfile = value
    case "telemetryEnabled":
        b, err := strconv.ParseBool(value)
        if err != nil {
            return fmt.Errorf("invalid boolean value for telemetryEnabled: %s", value)
        }
        s.TelemetryEnabled = &b
    case "defaultMaxTurns":
        n, err := strconv.Atoi(value)
        if err != nil {
            return fmt.Errorf("invalid integer value for defaultMaxTurns: %s", value)
        }
        s.DefaultMaxTurns = n
    case "defaultMaxModelCalls":
        n, err := strconv.Atoi(value)
        if err != nil {
            return fmt.Errorf("invalid integer value for defaultMaxModelCalls: %s", value)
        }
        s.DefaultMaxModelCalls = n
    case "defaultMaxDuration":
        s.DefaultMaxDuration = value
    case "defaultGCPIdentityMode":
        if value != "block" && value != "passthrough" && value != "assign" {
            return fmt.Errorf("invalid GCP identity mode: %s (must be block, passthrough, or assign)", value)
        }
        s.DefaultGCPIdentityMode = value
    case "defaultGCPIdentityServiceAccountID":
        s.DefaultGCPIdentityServiceAccountID = value
    // Dot-notation for resources
    case "defaultResources.requests.cpu", "defaultResources.requests.memory",
         "defaultResources.limits.cpu", "defaultResources.limits.memory",
         "defaultResources.disk":
        applyResourceUpdate(s, key, value)
    default:
        return fmt.Errorf("unknown grove setting: %s\n\nAvailable settings:\n  %s",
            key, strings.Join(listAvailableKeys(), "\n  "))
    }
    return nil
}
```

### Implementation: `get`

```go
var hubGroveSettingsGetCmd = &cobra.Command{
    Use:   "get [key]",
    Short: "Get a grove setting from the Hub",
    Args:  cobra.MaximumNArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        // ... resolve grove, create client ...

        settings, err := client.Groves().GetSettings(ctx, groveID)
        if err != nil {
            return err
        }

        if len(args) == 0 {
            // No key — list all (same as `list`)
            return outputGroveSettings(settings)
        }

        key := args[0]
        value, err := getSettingValue(settings, key)
        if err != nil {
            return err
        }

        if isJSONOutput() {
            return outputJSON(map[string]string{"key": key, "value": value})
        }
        fmt.Println(value)
        return nil
    },
}
```

### Implementation: `list`

```go
var hubGroveSettingsListCmd = &cobra.Command{
    Use:   "list",
    Short: "List all grove settings from the Hub",
    RunE: func(cmd *cobra.Command, args []string) error {
        // ... resolve grove, create client ...

        settings, err := client.Groves().GetSettings(ctx, groveID)
        if err != nil {
            return err
        }

        if isJSONOutput() {
            return outputJSON(settings)
        }

        return printGroveSettings(settings)
    },
}
```

Human-friendly output format:
```
Grove Settings (my-project):
  defaultHarnessConfig:               claude-sonnet
  defaultTemplate:                    standard
  defaultGCPIdentityMode:             assign
  defaultGCPIdentityServiceAccountID: sa@project.iam.gserviceaccount.com
  defaultMaxTurns:                    50
  defaultMaxDuration:                 1h
  defaultResources.requests.cpu:      2
  defaultResources.requests.memory:   4Gi
  defaultResources.disk:              20Gi
```

### Clearing/Unsetting Values

To clear a setting, use an empty string:
```
scion hub grove settings set defaultHarnessConfig ""
```

This causes `setOrDelete` on the Hub side to delete the annotation, effectively unsetting the grove-level override. Document this behavior in the command help.

### Flags

```go
var groveSettingsGroveFlag string

func init() {
    // All settings subcommands get --grove and --json
    for _, cmd := range []*cobra.Command{
        hubGroveSettingsListCmd,
        hubGroveSettingsGetCmd,
        hubGroveSettingsSetCmd,
    } {
        cmd.Flags().StringVar(&groveSettingsGroveFlag, "grove", "",
            "Target grove (name, slug, or ID). Defaults to current grove.")
        cmd.Flags().BoolVar(&hubOutputJSON, "json", false, "Output in JSON format")
    }
}
```

### File Organization

Create a new file `cmd/hub_grove_settings.go` to keep the command definitions isolated, following the pattern of `cmd/hub_env.go` and `cmd/hub_secret.go`.

## UX Examples

```bash
# List all settings for the current grove
$ scion hub grove settings list
Grove Settings (my-project):
  defaultHarnessConfig:  claude-sonnet
  defaultMaxTurns:       50
  (other settings not set)

# Get a specific setting
$ scion hub grove settings get defaultHarnessConfig
claude-sonnet

# Set a setting
$ scion hub grove settings set defaultGCPIdentityMode assign
Updated grove setting 'defaultGCPIdentityMode' to 'assign'

$ scion hub grove settings set defaultGCPIdentityServiceAccountID sa@project.iam.gserviceaccount.com
Updated grove setting 'defaultGCPIdentityServiceAccountID' to 'sa@project.iam.gserviceaccount.com'

# Set resource defaults
$ scion hub grove settings set defaultResources.requests.cpu 2
Updated grove setting 'defaultResources.requests.cpu' to '2'

# Clear a setting
$ scion hub grove settings set defaultHarnessConfig ""
Cleared grove setting 'defaultHarnessConfig'

# Target a different grove
$ scion hub grove settings list --grove=other-project
Grove Settings (other-project):
  ...

# JSON output for scripting
$ scion hub grove settings get defaultHarnessConfig --json
{"key":"defaultHarnessConfig","value":"claude-sonnet"}
```

## Migration Path from Option A

If users later ask for a more integrated experience, we can add a `scion config set --push` flag that writes locally *and* calls `hub grove settings set` under the hood — but only as an explicit opt-in convenience, not the default behavior. The underlying `hub grove settings` command remains the source of truth for Hub-side operations.

## Implementation Plan

1. **Create `cmd/hub_grove_settings.go`** with the three subcommands (`list`, `get`, `set`)
2. **Add `applySettingUpdate` / `getSettingValue` helpers** with the key mapping table and type validation
3. **Register commands** under `hubGrovesCmd` in `init()`
4. **Add `--grove` flag** with inference from current grove context
5. **Write tests** covering key mapping, type validation, grove resolution, and error cases
6. **Update CLI help text** to reference the new commands from `scion hub link` output

## Open Questions

1. **Should `set` do read-modify-write or full replace?** The current Hub API is a `PUT` (full replace). To support setting individual keys, the CLI must fetch current settings, modify, and push back. This is the read-modify-write pattern. Risk: concurrent modifications could cause overwrites. For now this is acceptable — grove settings change infrequently and race conditions are unlikely.

2. **Should we validate setting values against available options?** For example, should `set defaultHarnessConfig foo` verify that `foo` is a valid harness config name? For now, no — the Hub already accepts any string and the broker validates at provisioning time. Strict CLI validation would require fetching harness configs from the broker, adding latency and complexity.

3. **Bulk set from file?** A `scion hub grove settings apply -f settings.yaml` could be useful for automation. Defer to a later iteration — the single-key `set` command covers the immediate need.

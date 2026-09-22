# Herdr-Scion Integration Plugin

A [herdr](https://github.com/herdrdev/herdr) plugin that integrates Scion agents as managed terminal panes. See all your running agents in one window with live state indicators.

## Features

- **Auto-discovery** — On herdr startup, running Scion agents are discovered and opened as labeled panes.
- **Interactive attach** — Pick a running agent from a list and open it in a new pane.
- **Start and attach** — Create a new agent from a Scion template and open it in one step.
- **State bridge** — Agent states (working, blocked, idle, done) are polled from Scion and reported to herdr's status indicators.
- **Dashboard** — Create a tiled layout with all running agents in a single action.
- **Reconnection** — If a session disconnects (e.g., inner tmux detach), the wrapper auto-reconnects after 10 seconds.
- **Hub support** — Works identically for local and Hub-connected Scion agents.

## Requirements

- [herdr](https://github.com/herdrdev/herdr) v0.6.10 or later
- [scion](https://github.com/scion-ai/scion) CLI on PATH
- `jq` for JSON processing
- `fzf` (optional) for fuzzy interactive selection; falls back to a numbered menu

## Installation

```bash
# Link the plugin into herdr
herdr plugin link /path/to/scion/extras/herdr-scion

# Verify installation
herdr plugin list
# => scion.herdr-integration v0.1.0
```

## Usage

### Automatic Discovery

On herdr startup, the plugin runs `scion-discover.sh` which:
1. Queries `scion list` for running agents
2. Creates a herdr pane for each agent not already represented
3. Starts the state bridge in the background

### Actions

All actions are available in herdr's action palette:

| Action | Description |
|--------|-------------|
| **Attach to Scion Agent** | Pick a running agent and open it in a new pane |
| **Start Scion Agent** | Choose a template, name the agent, and start+attach in one step |
| **Refresh Scion Agents** | Re-run discovery to pick up newly started agents |
| **Scion Agent Dashboard** | Create a tiled layout with all running agents |

### Manual Invocation

```bash
# Invoke actions directly
herdr plugin action invoke scion.herdr-integration:attach-agent
herdr plugin action invoke scion.herdr-integration:start-agent
herdr plugin action invoke scion.herdr-integration:refresh-agents
herdr plugin action invoke scion.herdr-integration:dashboard
```

## How It Works

### Pane Management

Each Scion agent pane is created via `herdr pane split` with:
- **Label**: `scion:<agent-slug>` (used for duplicate detection)
- **Command**: `scion-attach-wrapper.sh <slug>` (handles reconnection)
- **Environment**: `SCION_AGENT=<slug>`

### Attach Wrapper

The `scion-attach-wrapper.sh` script wraps `scion attach` with:

1. **Idempotent start-or-attach**: Checks `scion list` before deciding whether to attach or start. This handles the Hub-mode gap where `scion start -a` may not be idempotent for already-running agents.
2. **Reconnection loop**: If `scion attach` exits while the agent is still running (e.g., the user pressed Ctrl-b d inside the inner tmux), the wrapper waits 10 seconds then reconnects. Press Enter to reconnect immediately, or Ctrl-C to close the pane.
3. **Graceful exit**: When the agent stops running, the wrapper prints a status message and waits for the user to dismiss.

### State Bridge

The `scion-state-bridge.sh` daemon polls `scion list` every 4 seconds and reports agent states to herdr:

| Scion Activity | Herdr State |
|----------------|-------------|
| `executing`, `working`, `thinking` | Working |
| `blocked`, `waiting_for_input` | Blocked |
| `completed` | Done |
| `stalled`, `idle`, other | Idle |
| Agent phase != `running` | Done |

The bridge enforces singleton behavior via a PID file — only one instance runs per herdr session.

### Dashboard Layout

The dashboard builds a balanced binary split tree:
- 1 agent: single pane
- 2 agents: side-by-side (horizontal split)
- 3+ agents: grid layout (alternating horizontal and vertical splits)

## Configuration

Environment variables for tuning:

| Variable | Default | Description |
|----------|---------|-------------|
| `SCION_BRIDGE_INTERVAL` | `4` | State bridge poll interval in seconds |
| `SCION_BRIDGE_PIDFILE` | `$HERDR_PLUGIN_STATE_DIR/scion-state-bridge.pid` | PID file path for singleton enforcement |

## Architecture

```
herdr pane (PTY)
  -> scion-attach-wrapper.sh
    -> scion attach <slug>
      -> docker exec / WebSocket (Hub)
        -> tmux attach -t scion
          -> harness (Claude Code, Gemini CLI, etc.)
```

Terminal resize propagates through the full chain. Herdr's detach (herdr keybind) leaves the inner session running. The inner tmux detach (Ctrl-b d) triggers the wrapper's reconnection logic.

## Troubleshooting

### No agents discovered on startup
- Check that `scion list -r` shows running agents
- Check that `scion` is on PATH when herdr starts
- Run the refresh action to re-discover

### State indicators show "Unknown"
- Verify the state bridge is running: check for a `scion-state-bridge.sh` process
- Check the PID file at `$HERDR_PLUGIN_STATE_DIR/scion-state-bridge.pid`
- Try removing the PID file and running refresh to restart the bridge

### Pane shows "Disconnected" message
- The inner `scion attach` session ended (tmux detach or network issue)
- Press Enter to reconnect, or wait 10 seconds for auto-reconnect
- If the agent stopped, the pane will show the agent's final phase

### Duplicate panes
- The plugin tracks panes by label (`scion:<slug>`); duplicates should not occur
- If they do, close the extra pane and run refresh

### Keybinding conflicts
- Herdr and the inner tmux may both use Ctrl-b as a prefix
- Configure Scion's inner tmux to use a different prefix (e.g., Ctrl-a), or adjust herdr's keybinding in `~/.config/herdr/config.toml`

## Uninstalling

```bash
herdr plugin unlink scion.herdr-integration
```

This removes the plugin registration. Existing panes remain open but will not be recreated on next startup. The state bridge process will exit when it can no longer reach herdr.

## Files

| File | Purpose |
|------|---------|
| `herdr-plugin.toml` | Plugin manifest (startup hook, actions) |
| `scion-discover.sh` | Auto-discover agents and create panes |
| `scion-attach-wrapper.sh` | Attach with reconnection and idempotent start |
| `scion-pick-agent.sh` | Interactive agent picker |
| `scion-start-agent.sh` | Template picker and agent launcher |
| `scion-state-bridge.sh` | State synchronization daemon |
| `scion-dashboard.sh` | Tiled layout dashboard |

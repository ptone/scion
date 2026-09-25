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
- One of `socat`, `nc` (with `-U` for Unix sockets), or `python3` — used only
  by the dashboard action to talk to herdr's socket API for `layout.apply`,
  which has no CLI equivalent

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
herdr plugin action invoke attach-agent --plugin scion.herdr-integration
herdr plugin action invoke start-agent --plugin scion.herdr-integration
herdr plugin action invoke refresh-agents --plugin scion.herdr-integration
herdr plugin action invoke dashboard --plugin scion.herdr-integration
```

## How It Works

### Pane Management

`herdr pane split` creates a bare pane — it does not accept a command to run.
Each Scion agent pane is set up in three steps:
1. `herdr pane split --direction right --env SCION_AGENT=<name>` — split, and
   read the new pane's ID from `.result.pane.pane_id`.
2. `herdr pane rename <pane_id> "scion:<project>/<name>"` — set the pane's
   label immediately, before the wrapper starts. This is what duplicate
   detection and the state bridge track panes by. The label includes the
   project because scion agent names are only unique within a project —
   discovery and the picker list agents across every project (`-a`), so two
   different projects can have same-named agents.
3. `herdr pane run <pane_id> "bash scion-attach-wrapper.sh <name> --project
   <project> [--project-path <path>] ..."` — type the wrapper command,
   including the agent's project scope, into the pane's shell and press
   Enter.

### Project Scoping

`scion attach`/`scion start` resolve their target project from `-g`
(`--project`) or the CWD, then fall back to the `global` project. Herdr
panes and actions don't run from the project's own directory, so without an
explicit scope, every call would fail with `agent '<name>' not found in
project 'global'` whenever the agent actually lives in a non-global project.

To fix this, every script that discovers or creates an agent pane carries
the agent's `project` and `projectPath` (from `scion list`'s JSON output)
through to `scion-attach-wrapper.sh` via `--project`/`--project-path`. The
wrapper then:
- `cd`s into `--project-path` when given (belt-and-suspenders alongside the
  next point — scion also resolves project from CWD).
- Passes `-g <projectPath>` (or `-g <project>` when there's no local path —
  Hub mode) to every `scion attach`/`scion start`/`scion list` call it
  makes, so `is_running`/`agent_phase` checks and the actual attach/start
  are scoped to the agent's real project, not the pane's CWD.

`--project-path` is passed through exactly as `scion list` reports it — it
isn't normalized between a project's root directory and its `.scion`
subdirectory, which needs verifying against `-g`'s actual expectations.

**Start Scion Agent** additionally has to pick a project *before* listing
templates or starting anything: it offers the distinct `project`/
`projectPath` pairs seen across every known agent (`scion list -a`, any
phase), plus the invoking pane's own `workspace_cwd`/`focused_pane_cwd` if
it looks like a scion project directory (contains `.scion`). The picker is
skipped when there's only one known project.

### Interactive Actions (Popup Panes)

`scion-pick-agent.sh` (fzf) and `scion-start-agent.sh` (`read -rp` prompts)
are interactive. Herdr plugin actions run with piped stdout/stderr and no
controlling terminal, so an interactive script can't run as an action
directly. Instead:

- `scion-pick-agent.sh` and `scion-start-agent.sh` are declared as `[[panes]]`
  entrypoints (`pick` and `start`) in `herdr-plugin.toml`, not as actions.
- The **Attach to Scion Agent** and **Start Scion Agent** actions run
  `scion-open-pane.sh`, a small launcher that calls `herdr plugin pane open
  --plugin scion.herdr-integration --entrypoint <pick|start> --placement
  overlay --env SCION_TARGET_PANE="$HERDR_PANE_ID"`. This opens the real
  script in a genuine terminal (an overlay pane), which closes automatically
  when the script exits.
- The picker/start scripts split the new agent pane off of
  `$SCION_TARGET_PANE` (the pane that triggered the action) rather than off
  the overlay itself, so the agent pane lands next to the user's other panes.
  Falls back to herdr's default split target if `SCION_TARGET_PANE` is unset.

### Attach Wrapper

The `scion-attach-wrapper.sh` script wraps `scion attach` with:

1. **Idempotent start-or-attach**: Checks `scion list` before deciding whether to attach or start. This handles the Hub-mode gap where `scion start -a` may not be idempotent for already-running agents.
2. **Reconnection loop**: If `scion attach` exits while the agent is still running (e.g., the user pressed Ctrl-b d inside the inner tmux), the wrapper waits 10 seconds then reconnects. Press Enter to reconnect immediately, or Ctrl-C to close the pane.
3. **Graceful exit**: When the agent stops running, the wrapper prints a status message and waits for the user to dismiss.

### State Bridge

The `scion-state-bridge.sh` daemon polls `scion list` every 4 seconds and reports agent states to herdr via `herdr pane report-agent`, matching panes to agents by the pane's `scion:<project>/<name>` label — split back into project and name so agents are matched on both, not name alone (which would be ambiguous across projects):

| Scion Activity | Herdr State |
|----------------|-------------|
| `executing`, `working`, `thinking` | Working |
| `blocked`, `waiting_for_input` | Blocked |
| `completed`, `stalled`, `idle`, other | Idle |
| Agent phase != `running` | Idle |

`herdr pane report-agent --state` only accepts `idle`, `working`, `blocked`, or `unknown` — `done` is not a settable state. Herdr derives its own Done indicator from an idle agent plus its internal "seen" tracking, so completed and stopped agents are reported as idle rather than done.

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
- The plugin tracks panes by label (`scion:<project>/<name>`); duplicates should not occur
- If they do, close the extra pane and run refresh

### "agent '<name>' not found in project 'global'"
- The wrapper wasn't given `--project`/`--project-path`, or scion's `-g`
  flag doesn't accept the `projectPath` value the way it was passed
  (project root vs. `.scion` subdirectory — see Project Scoping above)
- Check the pane's label: it should be `scion:<project>/<name>`, not just
  `scion:<name>` — a bare name means the identifier wasn't built with a
  project

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
| `herdr-plugin.toml` | Plugin manifest (startup hook, actions, panes) |
| `scion-discover.sh` | Auto-discover agents and create panes |
| `scion-attach-wrapper.sh` | Attach with reconnection and idempotent start |
| `scion-open-pane.sh` | Launcher: opens the pick/start overlay panes |
| `scion-pick-agent.sh` | Interactive agent picker (runs as an overlay pane) |
| `scion-start-agent.sh` | Template picker and agent launcher (runs as an overlay pane) |
| `scion-state-bridge.sh` | State synchronization daemon |
| `scion-dashboard.sh` | Tiled layout dashboard |
| `scion-common.sh` | Shared helpers (pane tracking, socket API access) |

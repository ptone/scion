# Herdr-Scion Integration Plugin

A [herdr](https://github.com/herdrdev/herdr) plugin that integrates Scion agents as managed terminal panes. See all your running agents in one window with live state indicators.

## Features

- **Follows your scion project** — Acts on the project scion itself would pick: the herdr workspace's directory if it is inside a scion project, otherwise the project selected with `scion use`. The pickers show which project and why.
- **Auto-discovery** — On herdr startup, the project's running Scion agents are discovered and opened as labeled panes.
- **Interactive attach** — Pick a running agent from a list and open it in a new pane.
- **Start and attach** — Create a new agent from a Scion template and open it in one step.
- **State bridge** — Agent states (working, blocked, idle, done) are polled from Scion and reported to herdr's status indicators.
- **Dashboard** — Create a tiled layout with all running agents in a single action.
- **Reconnection** — If a session disconnects (e.g., inner tmux detach), the wrapper auto-reconnects after 10 seconds, and stops after 3 short-lived sessions in a row.
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
1. Resolves the scion project (see [Project Scope](#project-scope))
2. Queries `scion list` for that project's running agents
3. Creates a herdr pane for each agent not already represented
4. Starts the state bridge in the background

If no project is resolved, the action fails with `No scion project selected
… run 'scion use <project>'` in `herdr plugin log`.

### Actions

All actions are available in herdr's action palette:

| Action | Description |
|--------|-------------|
| **Attach to Scion Agent** | Pick one of the project's running agents and open it in a new pane |
| **Start Scion Agent** | Choose one of the project's templates, name the agent, and start+attach in one step |
| **Refresh Scion Agents** | Re-run discovery to pick up newly started agents |
| **Scion Agent Dashboard** | Create a tiled layout (tab `scion: <project>`) with the project's running agents |

### Manual Invocation

```bash
# Invoke actions directly
herdr plugin action invoke attach-agent --plugin scion.herdr-integration
herdr plugin action invoke start-agent --plugin scion.herdr-integration
herdr plugin action invoke refresh-agents --plugin scion.herdr-integration
herdr plugin action invoke dashboard --plugin scion.herdr-integration
```

## How It Works

### Project Scope

Each action resolves the project once, the way scion would for you, then
pins it explicitly for every scion call it makes. Precedence is scion's
own: flags → `SCION_*` env → directory project → `scion use` context →
global settings. The plugin doesn't reimplement this. It runs `scion use
--format json`:
- from the herdr workspace's directory (`workspace_cwd`), if it exists, so
  a workspace inside a scion project targets that project, as any scion
  command there would;
- otherwise from an empty temp dir, so the context applies.

Herdr runs plugin commands from the plugin's own directory, which is
inside the scion checkout (and that has its own `.scion`). Every scion call
runs from an empty temp dir, so the plugin's directory can never select a
project.

How the project is pinned depends on where it came from:

| Source | scion calls | Checked before each call (attach wrapper, state bridge) |
|--------|-------------|------------------------------------------|
| A project directory (or `global`) | `scion -g <project-root> …` | The hub endpoint is unchanged |
| The `scion use` context (a hub project) | `scion …` with no `-g`, so scion applies its context | The context still names the same project UUID and hub |

Context projects are not passed as `-g <uuid>`: scion lets a hub project
skip its `hub.enabled` check only when it took the project from the context
itself. An explicit `-g <uuid>` fails for users who ran `scion use` but never
`scion hub enable`. `--hub` is never passed either: an explicit `--hub` can
link or register a project on that hub, where the context's hub refuses to.

A pane is bound to the project it was opened for. If you run `scion use`
for something else, an open pane doesn't attach to the other project. On
its next connect, it stops and says:

```
scion context is now other-proj on https://hub…; this pane is for my-proj on https://hub….
Run 'scion use my-proj' or close this pane.
```

Press Enter to check again. The next **Refresh** shows the new project's
agents in new panes.

The overlay pickers show the project and where it came from, e.g.
`project: my-proj (from context)` or `project: my-proj (from directory)`.

### All Projects (`SCION_HERDR_SCOPE=all`)

To see agents from every project at once (the plugin's earlier behaviour),
set `SCION_HERDR_SCOPE=all`. Either:
- export it in the environment herdr starts with; or
- write `SCION_HERDR_SCOPE=all` to the file `config` in the plugin's config
  directory (`herdr plugin config-dir scion.herdr-integration`).

The environment wins. In this mode, discovery, the picker and the dashboard
use `scion list -a`. Each pane is pinned to its agent's project path (or
project name) as `scion list -a` reports it, with no context check. **Start
Scion Agent** asks for the project first.

### Pane Management

`herdr pane split` creates a bare pane — it does not accept a command to run.
Each Scion agent pane is set up in three steps (`open_agent_pane` in
`scion-common.sh`):
1. `herdr pane split --direction right --env SCION_AGENT=<name>` — split, and
   read the new pane's ID from `.result.pane.pane_id`.
2. `herdr pane rename <pane_id> "scion:<project-slug>/<name>"` — set the pane's
   label immediately, before the wrapper starts. This is what duplicate
   detection and the state bridge track panes by. The label includes the
   project because scion agent names are only unique within a project, and
   panes from different projects can share a workspace.
3. `herdr pane run <pane_id> "bash scion-attach-wrapper.sh <name> --mode
   pin|context --project <…> --label <slug> [--hub <endpoint>] …"` — type the
   wrapper command, with the pinned scope, into the pane's shell and press
   Enter.

The plugin also records each project's scope in its state directory
(`$HERDR_PLUGIN_STATE_DIR/scopes/<project-slug>`), so the state bridge can
poll each project the same way its panes do.

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

1. **Scope check**: Before each scion call it checks that the pinned project still matches (see [Project Scope](#project-scope)). If it doesn't, it calls nothing and waits.
2. **Idempotent start-or-attach**: Checks `scion list` before deciding whether to attach or start. This handles the Hub-mode gap where `scion start -a` may not be idempotent for already-running agents.
3. **Reconnection loop**: If `scion attach` exits while the agent is still running (e.g., the user pressed Ctrl-b d inside the inner tmux), the wrapper waits 10 seconds then reconnects. Press Enter to reconnect immediately, or Ctrl-C to close the pane. This covers deliberate detaches and local runtimes, which `scion attach`'s own reconnect doesn't.
4. **Reconnect cap**: After 3 sessions in a row that each lasted under 60 seconds, the wrapper stops reconnecting by itself and waits for Enter. `scion attach` already retries a dropped connection itself, so repeated short sessions mean that has given up too.
5. **Graceful exit**: When the agent stops running, the wrapper prints a status message and waits for the user to dismiss.

### State Bridge

The `scion-state-bridge.sh` daemon polls every 4 seconds and reports agent states to herdr via `herdr pane report-agent`. It matches panes to agents by the pane's `scion:<project>/<name>` label, then polls each project with that project's recorded scope (`scion list`, never `-a`), so agents are matched on project and name. If a project's scope no longer matches the scion context, or it has no record (e.g. a pane from an older plugin version), its panes are reported as `unknown`.

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
| `SCION_HERDR_SCOPE` | `project` | `all` shows agents from every project (also settable in the plugin config file, see above) |
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
- Check `herdr plugin log`: discovery says which project it used and why (`project: <slug> (from <source>)`), or that none was resolved
- From the herdr workspace's directory, check that `scion use` names the project you expect, and that `scion list -r` shows running agents
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

### Pane says "scion context is now …; this pane is for …"
- You ran `scion use` for another project (or hub) after the pane was opened
- Run the `scion use …` command the message shows and press Enter, or close the pane and run refresh to open the current project's agents

### Picker shows the wrong project
- The header says where scion got the project from. `(from directory)` means the herdr workspace's directory is inside that scion project, which wins over `scion use`, as it does for any scion command run there
- Open the herdr workspace somewhere else, or set `SCION_HERDR_SCOPE=all`

### "hub project references … require hub mode to be enabled"
- Seen only in `SCION_HERDR_SCOPE=all` mode, when an agent's project has no local path and the plugin pins it by name. Run `scion hub enable`, or use the default mode

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
| `scion-common.sh` | Shared helpers (project scope, pane creation and tracking, socket API access) |

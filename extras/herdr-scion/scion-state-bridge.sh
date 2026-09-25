#!/usr/bin/env bash
# scion-state-bridge.sh — Poll Scion agent states and report to herdr.
#
# Maps Scion activity states to herdr AgentStatus values and pushes updates
# via `herdr pane report-agent`. Runs as a background daemon, started from
# scion-discover.sh or manually.
#
# State mapping:
#   Scion                  -> Herdr
#   executing, working     -> working
#   thinking               -> working
#   blocked                -> blocked
#   waiting_for_input      -> blocked
#   completed              -> idle
#   stalled                -> idle
#   idle / null / other    -> idle
#   (phase != running)     -> idle
#
# `herdr pane report-agent --state` only accepts idle|working|blocked|unknown
# ("done" is not a settable state — herdr derives its own Done badge from
# idle plus its internal "seen" tracking). Completed/stopped agents are
# therefore reported as idle.
#
# Environment:
#   SCION_BRIDGE_INTERVAL  Poll interval in seconds (default: 4)
#   SCION_BRIDGE_PIDFILE   Path to PID file for singleton enforcement
#
# Requires: scion, herdr, jq on PATH.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scion-common.sh
source "${SCRIPT_DIR}/scion-common.sh"

POLL_INTERVAL="${SCION_BRIDGE_INTERVAL:-4}"
PIDFILE="${SCION_BRIDGE_PIDFILE:-${HERDR_PLUGIN_STATE_DIR:-/tmp}/scion-state-bridge.pid}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-state-bridge] $*" >&2; }

# Map a scion activity string to a herdr agent state.
map_state() {
  local activity="$1"
  local phase="$2"

  # If the agent is no longer running, report idle — herdr shows Done once
  # it observes the pane at rest; "done" itself is not a settable state.
  if [[ "$phase" != "running" ]]; then
    echo "idle"
    return
  fi

  case "$activity" in
    executing|working|thinking)
      echo "working"
      ;;
    blocked|waiting_for_input)
      echo "blocked"
      ;;
    *)
      # completed, stalled, idle, null, empty, unknown — all map to idle
      echo "idle"
      ;;
  esac
}

# Enforce singleton: only one state bridge per herdr session.
ensure_singleton() {
  # Create the directory for the PID file if it doesn't exist.
  local piddir
  piddir="$(dirname "$PIDFILE")"
  mkdir -p "$piddir" 2>/dev/null || true

  if [[ -f "$PIDFILE" ]]; then
    local old_pid
    old_pid="$(cat "$PIDFILE" 2>/dev/null)"
    if [[ -n "$old_pid" ]] && kill -0 "$old_pid" 2>/dev/null; then
      log "Another state bridge is already running (PID $old_pid). Exiting."
      exit 0
    fi
    # Stale PID file — previous bridge exited without cleanup.
    log "Removing stale PID file (PID $old_pid no longer running)."
  fi

  echo $$ > "$PIDFILE"
}

# Clean up the PID file on exit.
cleanup() {
  rm -f "$PIDFILE" 2>/dev/null
}

# ---------------------------------------------------------------------------
# Main poll loop
# ---------------------------------------------------------------------------

poll_once() {
  # Get all herdr panes belonging to this plugin.
  # Scion panes are identified by their .label, set via `herdr pane rename`
  # right after split, with the format "scion:<identifier>".
  local panes_json
  panes_json="$("$HERDR_BIN" pane list)" || return 1

  # Extract pane_id + agent identifier from scion-managed panes.
  local pane_entries
  pane_entries="$(echo "$panes_json" \
    | jq -r '.result.panes[] | select(.label != null and (.label | startswith("scion:")))
             | "\(.pane_id)\t\(.label | ltrimstr("scion:"))"' 2>/dev/null)"

  [[ -z "$pane_entries" ]] && return 0

  # Get current scion agent states in one call.
  local agents_json
  agents_json="$(scion list -a --format json 2>/dev/null)" || return 1

  while IFS=$'\t' read -r pane_id identifier; do
    [[ -z "$pane_id" || -z "$identifier" ]] && continue

    # Look up this agent in the scion list output.
    # Uses .slug // .name because slug is omitempty in local/podman mode.
    local agent_info
    agent_info="$(echo "$agents_json" \
      | jq -r ".[] | select((.slug // .name) == \"$identifier\")" 2>/dev/null)"

    local activity phase herdr_state

    if [[ -z "$agent_info" ]]; then
      # Agent no longer in scion list at all — report idle (see note above:
      # "done" is not a settable report-agent state).
      herdr_state="idle"
    else
      activity="$(echo "$agent_info" | jq -r '.activity // "idle"' 2>/dev/null)"
      phase="$(echo "$agent_info" | jq -r '.phase // "unknown"' 2>/dev/null)"
      herdr_state="$(map_state "$activity" "$phase")"
    fi

    # Report to herdr (state only — identity/tracking is via pane .label).
    "$HERDR_BIN" pane report-agent "$pane_id" \
      --source "scion:integration" \
      --agent "scion/${identifier}" \
      --state "$herdr_state"

  done <<< "$pane_entries"
}

check_deps() {
  local missing=()
  for cmd in scion jq; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  command -v "$HERDR_BIN" >/dev/null 2>&1 || missing+=("$HERDR_BIN")
  if [[ ${#missing[@]} -gt 0 ]]; then
    log "Missing required commands: ${missing[*]}"
    exit 1
  fi
}

main() {
  check_deps
  resolve_project_cwd
  log "Starting state bridge (poll interval: ${POLL_INTERVAL}s)."
  ensure_singleton
  trap cleanup EXIT INT TERM

  while true; do
    poll_once || log "Poll cycle failed — will retry."
    sleep "$POLL_INTERVAL"
  done
}

main "$@"

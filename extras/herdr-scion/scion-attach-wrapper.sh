#!/usr/bin/env bash
# scion-attach-wrapper.sh — Wrap `scion attach` with reconnection and
# idempotent start-or-attach logic.
#
# Usage:
#   scion-attach-wrapper.sh <slug>                 # attach to existing agent
#   scion-attach-wrapper.sh <slug> --start <tmpl>  # start if needed, then attach
#
# Handles:
#   - Reconnection after inner tmux detach (Ctrl-b d inside scion session).
#   - Auto-reconnect with a 10-second timeout or on Enter.
#   - Graceful exit when the agent stops running.
#   - Idempotent start: if the agent is already running, attaches instead of
#     starting a duplicate. This works around the Hub-mode idempotency gap
#     where `scion start -a` may not handle already-running agents.
#
# Requires: scion, jq on PATH.

set -uo pipefail

SLUG="${1:?Usage: scion-attach-wrapper.sh <slug> [--start <template>]}"
shift

TEMPLATE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --start)
      TEMPLATE="${2:?--start requires a template name}"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-attach] $*" >&2; }

check_deps() {
  local missing=()
  for cmd in scion jq; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    log "Missing required commands: ${missing[*]}"
    exit 1
  fi
}

# Check whether the agent is currently running.
# Uses .slug // .name because slug is omitempty in local/podman mode.
is_running() {
  scion list -a -r --format json 2>/dev/null \
    | jq -e ".[] | select((.slug // .name) == \"$1\" and .phase == \"running\")" \
    >/dev/null 2>&1
}

# Get the agent's current lifecycle phase (or "unknown").
agent_phase() {
  scion list -a --format json 2>/dev/null \
    | jq -r ".[] | select((.slug // .name) == \"$1\") | .phase // \"unknown\"" 2>/dev/null \
    | head -1
}

# Register this pane with herdr so it can be tracked by the state bridge
# and duplicate detection. Uses HERDR_PANE_ID set by herdr for child processes.
register_pane() {
  if [[ -n "${HERDR_PANE_ID:-}" ]]; then
    "${HERDR_BIN_PATH:-herdr}" pane report-agent "$HERDR_PANE_ID" \
      --source "scion:integration" \
      --agent "scion/${SLUG}" \
      --state idle
  fi
}

# ---------------------------------------------------------------------------
# Initial connect — start if necessary
# ---------------------------------------------------------------------------

initial_connect() {
  if is_running "$SLUG"; then
    log "Agent $SLUG is running — attaching."
    scion attach "$SLUG"
    return $?
  fi

  if [[ -n "$TEMPLATE" ]]; then
    log "Agent $SLUG is not running — starting from template '$TEMPLATE'."
    scion start "$SLUG" -t "$TEMPLATE" -a
    return $?
  fi

  # No template and agent not running — try attach anyway so the user gets
  # scion's own error message.
  log "Agent $SLUG is not running and no template provided — attempting attach."
  scion attach "$SLUG"
  return $?
}

# ---------------------------------------------------------------------------
# Reconnection loop
# ---------------------------------------------------------------------------

reconnect_loop() {
  while true; do
    initial_connect
    local exit_code=$?

    # Check if the agent is still running.
    local phase
    phase="$(agent_phase "$SLUG")"

    if [[ "$phase" != "running" ]]; then
      echo ""
      echo "Agent $SLUG is no longer running (phase: ${phase})."
      echo "Press Enter to close this pane, or Ctrl-C."
      read -r || true
      return "$exit_code"
    fi

    echo ""
    echo "Disconnected from $SLUG. Press Enter to reconnect, or Ctrl-C to close."
    # Auto-reconnect after 10 seconds if the user doesn't press anything.
    read -r -t 10 || true
  done
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

check_deps
register_pane
reconnect_loop

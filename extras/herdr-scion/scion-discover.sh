#!/usr/bin/env bash
# scion-discover.sh — Discover running Scion agents and create herdr panes.
#
# Called as a startup hook and as the "Refresh Scion Agents" action.
# For each running agent not already represented by a herdr pane, creates
# a new pane running scion-attach-wrapper.sh.
#
# Requires: scion, herdr, jq on PATH.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scion-common.sh
source "${SCRIPT_DIR}/scion-common.sh"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-discover] $*" >&2; }

# Collect identifiers of running scion agents (one per line).
# Uses .slug // .name because slug is omitempty in local/podman mode.
running_agents() {
  scion list -r --format json 2>/dev/null \
    | jq -r '.[] | select(.phase == "running") | .slug // .name' 2>/dev/null
}

# Collect agent identifiers from existing herdr panes that belong to this plugin.
# Panes are tracked by the .agent field set via herdr pane report-agent,
# with the format "scion/<identifier>".
existing_pane_agents() {
  herdr pane list --json 2>/dev/null \
    | jq -r '.[] | select(.agent != null) | .agent' 2>/dev/null \
    | grep '^scion/' \
    | sed 's|^scion/||' || true
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

check_deps() {
  local missing=()
  for cmd in scion herdr jq; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    log "Missing required commands: ${missing[*]}"
    exit 1
  fi
}

main() {
  check_deps
  resolve_project_cwd
  log "Discovering running Scion agents..."

  local agents
  agents="$(running_agents)"

  if [[ -z "$agents" ]]; then
    log "No running Scion agents found."
    return 0
  fi

  local existing
  existing="$(existing_pane_agents)"

  local created=0

  while IFS= read -r identifier; do
    [[ -z "$identifier" ]] && continue

    # Skip if a pane already exists for this agent.
    if echo "$existing" | grep -qxF "$identifier"; then
      log "Pane already exists for $identifier — skipping."
      continue
    fi

    log "Creating pane for agent: $identifier"
    herdr pane split --direction right \
      --env "SCION_AGENT=${identifier}" \
      -- bash "${SCRIPT_DIR}/scion-attach-wrapper.sh" "$identifier"

    created=$((created + 1))
  done <<< "$agents"

  log "Discovery complete. Created $created new pane(s)."

  # Start the state bridge in the background if it's not already running.
  bash "${SCRIPT_DIR}/scion-state-bridge.sh" &
  disown
}

main "$@"

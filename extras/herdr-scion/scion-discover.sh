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

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-discover] $*" >&2; }

# Collect slugs of running scion agents.
# Returns one slug per line.
running_agents() {
  scion list -r --format json 2>/dev/null \
    | jq -r '.[] | select(.phase == "running") | .slug' 2>/dev/null
}

# Collect labels of existing herdr panes that belong to this plugin.
# We tag every pane we create with label "scion:<slug>".
existing_pane_labels() {
  herdr pane list --json 2>/dev/null \
    | jq -r '.[].label // empty' 2>/dev/null \
    | grep '^scion:' || true
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

main() {
  log "Discovering running Scion agents..."

  local agents
  agents="$(running_agents)"

  if [[ -z "$agents" ]]; then
    log "No running Scion agents found."
    return 0
  fi

  local existing
  existing="$(existing_pane_labels)"

  local created=0

  while IFS= read -r slug; do
    [[ -z "$slug" ]] && continue

    # Skip if a pane already exists for this agent.
    if echo "$existing" | grep -qxF "scion:${slug}"; then
      log "Pane already exists for $slug — skipping."
      continue
    fi

    log "Creating pane for agent: $slug"
    herdr pane split --direction right \
      --label "scion:${slug}" \
      --env "SCION_AGENT=${slug}" \
      -- bash "${SCRIPT_DIR}/scion-attach-wrapper.sh" "$slug"

    created=$((created + 1))
  done <<< "$agents"

  log "Discovery complete. Created $created new pane(s)."

  # Start the state bridge in the background if it's not already running.
  bash "${SCRIPT_DIR}/scion-state-bridge.sh" &
  disown
}

main "$@"

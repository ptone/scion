#!/usr/bin/env bash
# scion-dashboard.sh — Create a tiled herdr layout with all running Scion agents.
#
# Uses `herdr api layout.apply` to build a balanced split layout where each
# pane runs scion-attach-wrapper.sh for one agent. The layout adapts to the
# number of agents: 1 agent = single pane, 2 = side-by-side, 3+ = grid.
#
# Requires: scion, herdr, jq on PATH.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scion-common.sh
source "${SCRIPT_DIR}/scion-common.sh"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-dashboard] $*" >&2; }

# Build a layout pane node for a single agent.
pane_node() {
  local slug="$1"
  jq -n --arg slug "$slug" --arg script "${SCRIPT_DIR}/scion-attach-wrapper.sh" '{
    type: "pane",
    label: ("scion:" + $slug),
    command: ["bash", $script, $slug],
    env: { SCION_AGENT: $slug }
  }'
}

# Recursively build a balanced binary split tree from an array of pane nodes.
# Alternates split direction by depth (even=right, odd=down) for a grid layout.
#   $1 — JSON array of pane nodes
#   $2 — current depth (default 0)
build_tree() {
  local nodes="$1"
  local depth="${2:-0}"
  local count
  count="$(echo "$nodes" | jq 'length')"

  if [[ "$count" -eq 0 ]]; then
    echo 'null'
    return
  fi

  if [[ "$count" -eq 1 ]]; then
    echo "$nodes" | jq '.[0]'
    return
  fi

  local mid=$(( count / 2 ))
  local left_nodes right_nodes
  left_nodes="$(echo "$nodes" | jq ".[0:$mid]")"
  right_nodes="$(echo "$nodes" | jq ".[$mid:]")"

  local next_depth=$(( depth + 1 ))
  local left_tree right_tree
  left_tree="$(build_tree "$left_nodes" "$next_depth")"
  right_tree="$(build_tree "$right_nodes" "$next_depth")"

  # Alternate direction: even depths split horizontally, odd split vertically.
  local direction
  if (( depth % 2 == 0 )); then
    direction="right"
  else
    direction="down"
  fi

  jq -n --arg dir "$direction" \
    --argjson first "$left_tree" \
    --argjson second "$right_tree" '{
    type: "split",
    direction: $dir,
    ratio: 0.5,
    first: $first,
    second: $second
  }'
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
  log "Building agent dashboard..."

  local agents
  agents="$(scion list -r --format json 2>/dev/null \
    | jq -r '.[] | select(.phase == "running") | .slug // .name' 2>/dev/null)"

  if [[ -z "$agents" ]]; then
    echo "No running Scion agents found." >&2
    exit 0
  fi

  # Build pane nodes array.
  local pane_nodes="[]"
  while IFS= read -r slug; do
    [[ -z "$slug" ]] && continue
    local node
    node="$(pane_node "$slug")"
    pane_nodes="$(echo "$pane_nodes" | jq --argjson n "$node" '. + [$n]')"
  done <<< "$agents"

  local count
  count="$(echo "$pane_nodes" | jq 'length')"
  log "Found $count running agent(s). Building layout..."

  # Build the layout tree.
  local layout_tree
  layout_tree="$(build_tree "$pane_nodes")"

  # Apply via herdr API.
  local request_id
  request_id="scion-dashboard-$(date +%s)"

  local api_request
  api_request="$(jq -n --arg id "$request_id" --argjson root "$layout_tree" '{
    id: $id,
    method: "layout.apply",
    params: { root: $root }
  }')"

  log "Applying layout..."
  herdr api "$api_request" 2>/dev/null

  log "Dashboard created with $count agent pane(s)."

  # Start the state bridge in the background.
  bash "${SCRIPT_DIR}/scion-state-bridge.sh" &
  disown
}

main "$@"

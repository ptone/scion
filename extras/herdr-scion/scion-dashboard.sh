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
# Input: JSON array of pane nodes on stdin.
# Output: a single LayoutNode (pane or split).
build_tree() {
  local nodes="$1"
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

  # Split roughly in half. First half goes left (or top), second goes right
  # (or bottom). Alternate split direction by depth for a grid feel —
  # but herdr layout.apply just needs the tree, the actual direction is set
  # per split node. We alternate: even depths split right, odd split down.
  local mid=$(( count / 2 ))
  local left_nodes right_nodes
  left_nodes="$(echo "$nodes" | jq ".[0:$mid]")"
  right_nodes="$(echo "$nodes" | jq ".[$mid:]")"

  local left_tree right_tree
  left_tree="$(build_tree "$left_nodes")"
  right_tree="$(build_tree "$right_nodes")"

  # Decide split direction: if count > 2, use "right" for first split level
  # (horizontal), then the recursion will alternate naturally.
  local direction="right"
  if [[ "$count" -le 2 ]]; then
    direction="right"
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

main() {
  log "Building agent dashboard..."

  local agents
  agents="$(scion list -r --format json 2>/dev/null \
    | jq -r '.[] | select(.phase == "running") | .slug' 2>/dev/null)"

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

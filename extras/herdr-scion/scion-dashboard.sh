#!/usr/bin/env bash
# scion-dashboard.sh — Create a tiled herdr layout with all running Scion agents.
#
# Sends a layout.apply request straight to herdr's socket API ($HERDR_SOCKET_PATH)
# to build a balanced split layout where each pane runs scion-attach-wrapper.sh
# for one agent — herdr's CLI has no subcommand for layout.apply. The layout
# adapts to the number of agents: 1 agent = single pane, 2 = side-by-side,
# 3+ = grid.
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

# Build a layout pane node for agent $1 in the current scope (SCOPE_*), and
# record the scope for the state bridge.
pane_node() {
  local name="$1"
  local identifier="${SCOPE_LABEL}/${name}"
  local project="$SCOPE_LABEL"

  scope_record_write || log "Warning: could not write the scope record for ${SCOPE_LABEL}"
  scope_wrapper_argv
  local -a cmd=(bash "${SCRIPT_DIR}/scion-attach-wrapper.sh" "$name" "${SCOPE_WRAPPER_ARGV[@]}")

  local cmd_json
  cmd_json="$(printf '%s\n' "${cmd[@]}" | jq -R . | jq -s .)"

  # Note: the --arg name can't be "label" — jq's parser treats a variable
  # reference $label as its `label $out | ...` control-flow keyword, not a
  # bound --arg, and fails with "unexpected label, expecting IDENT" even
  # though the *object key* `label:` on its own is fine.
  jq -n \
    --arg pane_label "scion:${identifier}" \
    --arg name "$name" \
    --arg project "$project" \
    --argjson cmd "$cmd_json" '{
    type: "pane",
    label: $pane_label,
    command: $cmd,
    env: { SCION_AGENT: $name, SCION_PROJECT: $project }
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
  local all_mode=0 list_json agents
  if [[ "$(herdr_scope_mode)" == "all" ]]; then
    all_mode=1
    scope_dir_init || exit 1
    log "Building agent dashboard for all projects (SCION_HERDR_SCOPE=all)..."
    list_json="$(cd "$SCOPE_DIR" && scion list -a -r --format json)" || { log "scion list failed."; exit 1; }
    agents="$(jq -r '.[] | select(.phase == "running")
              | [(.slug // .name), (.project // "global"), (.projectPath // "")] | @tsv' <<<"$list_json")"
  else
    if ! resolve_scope; then
      log "Dashboard aborted: no scion project resolved."
      exit 1
    fi
    log "Building agent dashboard — $(scope_header)..."
    list_json="$(scion_scoped list -r --format json)" || { log "scion list failed."; exit 1; }
    agents="$(jq -r '.[] | select(.phase == "running") | (.slug // .name)' <<<"$list_json")"
  fi

  if [[ -z "$agents" ]]; then
    echo "No running Scion agents found." >&2
    exit 0
  fi

  # Build pane nodes array.
  local pane_nodes="[]" name project project_path
  while IFS=$'\t' read -r name project project_path; do
    [[ -z "$name" ]] && continue
    if [[ "$all_mode" == "1" ]]; then
      scope_set pin "${project_path:-$project}" "" "$project" 0 all
    fi
    local node
    node="$(pane_node "$name")"
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

  # -c (compact) is required: the socket protocol frames one JSON request
  # per line, so the request must not contain embedded newlines.
  local tab_label="scion: ${SCOPE_LABEL}"
  [[ "$all_mode" == "1" ]] && tab_label="scion: all projects"
  local api_request
  api_request="$(jq -nc --arg id "$request_id" --arg tab "$tab_label" --argjson root "$layout_tree" '{
    id: $id,
    method: "layout.apply",
    params: { root: $root, tab_label: $tab }
  }')"

  # herdr has no CLI command for layout.apply — send the request straight to
  # its socket API (newline-terminated JSON), same mechanism herdr's own
  # bundled integrations use.
  log "Applying layout..."
  local api_response
  if ! api_response="$(herdr_socket_request "$api_request")"; then
    log "Failed to apply layout via herdr socket API."
    exit 1
  fi
  if echo "$api_response" | jq -e '.error' >/dev/null 2>&1; then
    log "herdr layout.apply returned an error: $api_response"
    exit 1
  fi

  log "Dashboard created with $count agent pane(s)."

  # Start the state bridge in the background. Redirect its stdio away from
  # ours: herdr's plugin runtime waits for this script's stdout/stderr pipes
  # to close before marking the action finished, and a background child that
  # inherits them holds them open for as long as it runs (forever, here).
  bash "${SCRIPT_DIR}/scion-state-bridge.sh" </dev/null >/dev/null 2>&1 &
  disown
}

main "$@"

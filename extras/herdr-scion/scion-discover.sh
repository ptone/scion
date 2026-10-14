#!/usr/bin/env bash
# scion-discover.sh — Discover running Scion agents and create herdr panes.
#
# Called as a startup hook and as the "Refresh Scion Agents" action.
# Lists running agents in the effective scion project (see resolve_scope in
# scion-common.sh; every project with SCION_HERDR_SCOPE=all), and for each
# one not already represented by a herdr pane, creates a new pane running
# scion-attach-wrapper.sh.
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

# Running agents as TSV rows: name, then (all mode only) project and
# projectPath. Uses .slug // .name because slug is omitempty in local/podman
# mode. Default mode lists the scoped project; all mode lists every project.
running_agents() {
  local list_json
  if [[ "$SCOPE_MODE_ALL" == "1" ]]; then
    list_json="$(cd "$SCOPE_DIR" && scion list -a -r --format json)" || return 1
    jq -r '.[] | select(.phase == "running")
             | [(.slug // .name), (.project // "global"), (.projectPath // "")] | @tsv' <<<"$list_json"
  else
    list_json="$(scion_scoped list -r --format json)" || return 1
    jq -r '.[] | select(.phase == "running") | (.slug // .name)' <<<"$list_json"
  fi
}

# Collect agent identifiers from existing herdr panes that belong to this plugin.
# Panes are tracked by their .label, set via `herdr pane rename` right after
# split, with the format "scion:<identifier>".
existing_pane_agents() {
  local panes_json
  if ! panes_json="$("$HERDR_BIN" pane list)"; then
    log "Warning: herdr pane list failed — assuming no existing panes."
    return 0
  fi
  echo "$panes_json" \
    | jq -r '.result.panes[] | select(.label != null) | .label' \
    | grep '^scion:' \
    | sed 's|^scion:||' || true
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

  SCOPE_MODE_ALL=0
  if [[ "$(herdr_scope_mode)" == "all" ]]; then
    SCOPE_MODE_ALL=1
    scope_dir_init || exit 1
    log "Discovering running Scion agents in all projects (SCION_HERDR_SCOPE=all)..."
  else
    if ! resolve_scope; then
      log "Discovery aborted: no scion project resolved."
      exit 1
    fi
    log "Discovering running Scion agents — $(scope_header) [mode ${SCOPE_MODE}: ${SCOPE_PROJECT}${SCOPE_HUB:+, hub ${SCOPE_HUB}}]..."
  fi

  local agents
  if ! agents="$(running_agents)"; then
    log "scion list failed — see the error above."
    exit 1
  fi

  if [[ -z "$agents" ]]; then
    log "No running Scion agents found."
    start_state_bridge
    return 0
  fi

  local existing
  existing="$(existing_pane_agents)"

  local created=0
  local name project project_path
  while IFS=$'\t' read -r name project project_path; do
    [[ -z "$name" ]] && continue

    if [[ "$SCOPE_MODE_ALL" == "1" ]]; then
      # Each agent carries its own project; pin it as `scion list -a`
      # reported it (path when known, else the project name).
      scope_set pin "${project_path:-$project}" "" "$project" 0 all
    fi

    # Identifier is project/name: scion agent names are only unique within a
    # project, and panes from other projects (earlier refreshes under
    # another `scion use`, or all mode) share the workspace.
    local identifier="${SCOPE_LABEL}/${name}"

    if grep -qxF "$identifier" <<<"$existing"; then
      log "Pane already exists for $identifier — skipping."
      continue
    fi

    log "Creating pane for agent: $identifier"
    if ! open_agent_pane "$name" >/dev/null; then
      log "Could not open a pane for $identifier — skipping."
      continue
    fi
    created=$((created + 1))
  done <<< "$agents"

  log "Discovery complete. Created $created new pane(s)."
  start_state_bridge
}

# Start the state bridge in the background (it exits at once if one is
# already running). Redirect its stdio away from ours: herdr's plugin
# runtime waits for this script's stdout/stderr pipes to close before
# marking the action/startup hook finished, and a background child that
# inherits them holds them open for as long as it runs (forever).
start_state_bridge() {
  bash "${SCRIPT_DIR}/scion-state-bridge.sh" </dev/null >/dev/null 2>&1 &
  disown
}

main "$@"

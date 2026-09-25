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

# Collect running scion agents as TSV rows: name, project, projectPath (one
# row per agent). Uses .slug // .name because slug is omitempty in
# local/podman mode. project falls back to "global" (scion's own fallback
# project name) so every row has a usable, comparable project value.
# projectPath may be empty (Hub mode, or an older scion without the field).
running_agents() {
  scion list -a -r --format json 2>/dev/null \
    | jq -r '.[] | select(.phase == "running")
              | [(.slug // .name), (.project // "global"), (.projectPath // "")] | @tsv' 2>/dev/null
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

  while IFS=$'\t' read -r name project project_path; do
    [[ -z "$name" ]] && continue

    # Identifier is project/name, not just name: scion agent names are only
    # unique within a project, and discovery runs across all projects (-a).
    # Also what the pane's label is built from, so cross-project name
    # collisions don't get treated as the same pane.
    local identifier="${project}/${name}"

    # Skip if a pane already exists for this agent.
    if echo "$existing" | grep -qxF "$identifier"; then
      log "Pane already exists for $identifier — skipping."
      continue
    fi

    log "Creating pane for agent: $identifier"

    local split_json pane_id
    if ! split_json="$("$HERDR_BIN" pane split --direction right \
      --env "SCION_AGENT=${name}" --env "SCION_PROJECT=${project}")"; then
      log "Failed to split a pane for $identifier — skipping."
      continue
    fi

    pane_id="$(echo "$split_json" | jq -r '.result.pane.pane_id // empty')"
    if [[ -z "$pane_id" ]]; then
      log "herdr pane split returned no pane_id for $identifier: $split_json"
      continue
    fi

    "$HERDR_BIN" pane rename "$pane_id" "scion:${identifier}"

    # scion resolves its target project from -g or CWD, then falls back to
    # "global" — the wrapper needs the project (and, when known, its
    # filesystem path) to attach the right agent rather than hitting
    # "agent '<name>' not found in project 'global'".
    local run_cmd
    run_cmd="bash '${SCRIPT_DIR}/scion-attach-wrapper.sh' '${name}' --project '${project}'"
    if [[ -n "$project_path" ]]; then
      run_cmd+=" --project-path '${project_path}'"
    fi
    "$HERDR_BIN" pane run "$pane_id" "$run_cmd"

    created=$((created + 1))
  done <<< "$agents"

  log "Discovery complete. Created $created new pane(s)."

  # Start the state bridge in the background if it's not already running.
  # Redirect its stdio away from ours: herdr's plugin runtime waits for this
  # script's stdout/stderr pipes to close before marking the action/startup
  # hook finished, and a background child that inherits them holds them open
  # for as long as it runs (which, for the bridge, is forever).
  bash "${SCRIPT_DIR}/scion-state-bridge.sh" </dev/null >/dev/null 2>&1 &
  disown
}

main "$@"

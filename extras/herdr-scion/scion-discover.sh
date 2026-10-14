#!/usr/bin/env bash
# scion-discover.sh — Discover running Scion agents and create herdr panes.
#
# Called as a startup hook and as the "Refresh Scion Agents" action.
# Lists running agents in the effective scion project (see resolve_scope in
# scion-common.sh), and for each one not already represented by a herdr pane, creates
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

# Collect the names of running agents in the scoped project (one per line).
# Uses .slug // .name because slug is omitempty in local/podman mode.
running_agents() {
  local list_json
  list_json="$(scion_scoped list -r --format json)" || return 1
  jq -r '.[] | select(.phase == "running") | (.slug // .name)' <<<"$list_json"
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
  if ! resolve_scope; then
    log "Discovery aborted: no scion project resolved."
    exit 1
  fi
  log "Discovering running Scion agents in project ${SCOPE_LABEL} (from ${SCOPE_SOURCE}: -g ${SCOPE_PROJECT}${SCOPE_HUB:+ --hub ${SCOPE_HUB}})..."

  local agents
  if ! agents="$(running_agents)"; then
    log "scion list failed for project ${SCOPE_LABEL} — see the error above."
    exit 1
  fi

  if [[ -z "$agents" ]]; then
    log "No running Scion agents found."
    return 0
  fi

  local existing
  existing="$(existing_pane_agents)"

  local created=0

  while IFS= read -r name; do
    [[ -z "$name" ]] && continue

    # Identifier is project/name, not just name: scion agent names are only
    # unique within a project, and panes from different projects (opened by
    # earlier refreshes under another `scion use`) share the workspace.
    # Also what the pane's label is built from.
    local identifier="${SCOPE_LABEL}/${name}"

    # Skip if a pane already exists for this agent.
    if echo "$existing" | grep -qxF "$identifier"; then
      log "Pane already exists for $identifier — skipping."
      continue
    fi

    log "Creating pane for agent: $identifier"

    local split_json pane_id
    if ! split_json="$("$HERDR_BIN" pane split --direction right \
      --env "SCION_AGENT=${name}" --env "SCION_PROJECT=${SCOPE_LABEL}")"; then
      log "Failed to split a pane for $identifier — skipping."
      continue
    fi

    pane_id="$(echo "$split_json" | jq -r '.result.pane.pane_id // empty')"
    if [[ -z "$pane_id" ]]; then
      log "herdr pane split returned no pane_id for $identifier: $split_json"
      continue
    fi

    "$HERDR_BIN" pane rename "$pane_id" "scion:${identifier}"

    # Pin the pane to the project resolved now: the wrapper gets the same
    # -g/--hub values, so a later `scion use` elsewhere doesn't re-target
    # an open pane on reconnect.
    local run_cmd
    run_cmd="bash $(printf '%q' "${SCRIPT_DIR}/scion-attach-wrapper.sh") $(printf '%q' "$name")"
    run_cmd+=" --project $(printf '%q' "$SCOPE_PROJECT") --label $(printf '%q' "$SCOPE_LABEL")"
    if [[ -n "$SCOPE_HUB" ]]; then
      run_cmd+=" --hub $(printf '%q' "$SCOPE_HUB")"
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

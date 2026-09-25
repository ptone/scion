#!/usr/bin/env bash
# scion-pick-agent.sh — Interactive agent picker for the "Attach to Scion Agent"
# herdr action.
#
# Lists running scion agents, lets the user pick one (via fzf if available,
# falling back to a numbered menu), then creates a herdr pane attached to
# that agent.
#
# Requires: scion, herdr, jq on PATH. fzf is optional.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scion-common.sh
source "${SCRIPT_DIR}/scion-common.sh"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-pick] $*" >&2; }

# Build a display row for each agent: display, template, activity, project,
# name, projectPath. Only the first three columns are shown (see
# --with-nth below); the rest travel through unseen so the caller can
# recover the agent's actual project after a selection. display is
# "project/name" — scion agent names are only unique within a project, and
# this lists across all projects (-a), so the display (and later, the pane
# label) needs the project to disambiguate. Uses .slug // .name because
# slug is omitempty in local/podman mode; project falls back to "global".
agent_display_lines() {
  scion list -a -r --format json 2>/dev/null \
    | jq -r '.[] | select(.phase == "running")
              | [((.project // "global") + "/" + (.slug // .name)),
                 (.template // "-"),
                 (.activity // "idle"),
                 (.project // "global"),
                 (.slug // .name),
                 (.projectPath // "")] | @tsv' 2>/dev/null
}

# Pick using fzf if available, otherwise a basic numbered menu on
# stderr/stdin. Returns the full selected row (all 6 tab-separated fields),
# not just the display column — fzf's --with-nth only changes what's shown
# and matched against, not what's printed on selection.
pick_agent() {
  local lines="$1"

  if command -v fzf >/dev/null 2>&1; then
    echo "$lines" \
      | fzf --prompt="Select Scion agent> " \
            --delimiter=$'\t' \
            --with-nth=1,2,3 \
            --header="AGENT  TEMPLATE  ACTIVITY"
    return
  fi

  # Fallback: numbered menu. Keep each full row so project/name/projectPath
  # survive a numeric pick too.
  local -a rows=()
  local i=1
  echo "" >&2
  echo "Running Scion agents:" >&2
  while IFS= read -r line; do
    local display tmpl activity
    IFS=$'\t' read -r display tmpl activity _ _ _ <<< "$line"
    printf "  %d) %-32s  %-16s  %s\n" "$i" "$display" "$tmpl" "$activity" >&2
    rows+=("$line")
    i=$((i + 1))
  done <<< "$lines"

  echo "" >&2
  read -rp "Select agent [1-${#rows[@]}]: " choice
  if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#rows[@]} )); then
    echo "${rows[$((choice - 1))]}"
  fi
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

  local lines
  lines="$(agent_display_lines)"

  if [[ -z "$lines" ]]; then
    echo "No running Scion agents found." >&2
    exit 0
  fi

  local selected_row
  selected_row="$(pick_agent "$lines")"

  if [[ -z "$selected_row" ]]; then
    echo "No agent selected." >&2
    exit 0
  fi

  local display template activity project name project_path
  IFS=$'\t' read -r display template activity project name project_path <<< "$selected_row"
  local identifier="${project}/${name}"

  # Check if a pane already exists for this agent (tracked via pane .label).
  if pane_exists_for_agent "$identifier"; then
    log "Pane already exists for $identifier — focusing it."
    local pane_id
    pane_id="$(pane_id_for_agent "$identifier")"
    if [[ -n "$pane_id" ]]; then
      # Agent commands accept a pane_id directly (herdr resolves it to the
      # agent currently hosted in that pane).
      "$HERDR_BIN" agent focus "$pane_id"
    fi
    exit 0
  fi

  log "Creating pane for agent: $identifier"

  # Split off the pane that opened us (passed via SCION_TARGET_PANE by
  # scion-open-pane.sh), so the new agent pane lands next to the user's
  # pane rather than off this overlay. Falls back to herdr's default target
  # (calling pane, then focused pane) if unset.
  local -a split_args=(--direction right --env "SCION_AGENT=${name}" --env "SCION_PROJECT=${project}")
  if [[ -n "${SCION_TARGET_PANE:-}" ]]; then
    split_args=(--pane "$SCION_TARGET_PANE" "${split_args[@]}")
  fi

  local split_json pane_id
  split_json="$("$HERDR_BIN" pane split "${split_args[@]}")"
  pane_id="$(echo "$split_json" | jq -r '.result.pane.pane_id // empty')"
  if [[ -z "$pane_id" ]]; then
    log "herdr pane split returned no pane_id for $identifier: $split_json"
    exit 1
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
}

main "$@"

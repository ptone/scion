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

# Build a display line for each agent: "identifier  template  activity"
# Uses .slug // .name because slug is omitempty in local/podman mode.
agent_display_lines() {
  scion list -a -r --format json 2>/dev/null \
    | jq -r '.[] | select(.phase == "running")
              | "\(.slug // .name)\t\(.template // "-")\t\(.activity // "idle")"' 2>/dev/null
}

# Pick using fzf if available, otherwise a basic numbered menu on stderr/stdin.
pick_agent() {
  local lines="$1"

  if command -v fzf >/dev/null 2>&1; then
    echo "$lines" \
      | fzf --prompt="Select Scion agent> " \
            --delimiter=$'\t' \
            --with-nth=1,2,3 \
            --header="AGENT  TEMPLATE  ACTIVITY" \
      | cut -f1
    return
  fi

  # Fallback: numbered menu.
  local -a slugs=()
  local i=1
  echo "" >&2
  echo "Running Scion agents:" >&2
  while IFS=$'\t' read -r slug tmpl activity; do
    printf "  %d) %-24s  %-16s  %s\n" "$i" "$slug" "$tmpl" "$activity" >&2
    slugs+=("$slug")
    i=$((i + 1))
  done <<< "$lines"

  echo "" >&2
  read -rp "Select agent [1-${#slugs[@]}]: " choice
  if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#slugs[@]} )); then
    echo "${slugs[$((choice - 1))]}"
  fi
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

  local lines
  lines="$(agent_display_lines)"

  if [[ -z "$lines" ]]; then
    echo "No running Scion agents found." >&2
    exit 0
  fi

  local selected
  selected="$(pick_agent "$lines")"

  if [[ -z "$selected" ]]; then
    echo "No agent selected." >&2
    exit 0
  fi

  # Check if a pane already exists for this agent (tracked via pane .label).
  if pane_exists_for_agent "$selected"; then
    log "Pane already exists for $selected — focusing it."
    local pane_id
    pane_id="$(pane_id_for_agent "$selected")"
    if [[ -n "$pane_id" ]]; then
      # Agent commands accept a pane_id directly (herdr resolves it to the
      # agent currently hosted in that pane).
      herdr agent focus "$pane_id"
    fi
    exit 0
  fi

  log "Creating pane for agent: $selected"

  local split_json pane_id
  split_json="$(herdr pane split --direction right \
    --env "SCION_AGENT=${selected}")"
  pane_id="$(echo "$split_json" | jq -r '.result.pane.pane_id // empty')"
  if [[ -z "$pane_id" ]]; then
    log "herdr pane split returned no pane_id for $selected: $split_json"
    exit 1
  fi

  herdr pane rename "$pane_id" "scion:${selected}"
  herdr pane run "$pane_id" "bash '${SCRIPT_DIR}/scion-attach-wrapper.sh' '${selected}'"
}

main "$@"

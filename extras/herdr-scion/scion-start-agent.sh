#!/usr/bin/env bash
# scion-start-agent.sh — Start a new Scion agent from a template and open it
# in a herdr pane.
#
# Shows available templates, lets the user pick one, prompts for an agent name,
# then creates a herdr pane running scion-attach-wrapper.sh in --start mode.
# If the named agent is already running, attaches to it instead.
#
# Requires: scion, herdr, jq on PATH. fzf is optional.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scion-common.sh
source "${SCRIPT_DIR}/scion-common.sh"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-start] $*" >&2; }

# Parse template names from `scion template list` text output.
# The command doesn't support JSON reliably, so we extract names from the
# tabular output. Template names appear as the first column after leading
# whitespace, under both "Local Templates" and "Hub Templates" headings.
list_templates() {
  scion template list 2>/dev/null \
    | awk '
      # Skip header/section lines, blank lines, and lines starting with NAME
      /^[[:space:]]+[a-zA-Z]/ && !/NAME/ && !/Project:/ && !/Global:/ && !/Local Templates:/ && !/Hub Templates:/ {
        # First non-whitespace word is the template name
        gsub(/^[[:space:]]+/, "")
        split($0, fields, /[[:space:]]+/)
        if (fields[1] != "") print fields[1]
      }
    ' | sort -u
}

# Pick using fzf if available, otherwise a numbered menu.
pick_template() {
  local templates="$1"

  if command -v fzf >/dev/null 2>&1; then
    echo "$templates" \
      | fzf --prompt="Select template> " \
            --header="Available Scion Templates"
    return
  fi

  # Fallback: numbered menu.
  local -a items=()
  local i=1
  echo "" >&2
  echo "Available Scion templates:" >&2
  while IFS= read -r tmpl; do
    printf "  %d) %s\n" "$i" "$tmpl" >&2
    items+=("$tmpl")
    i=$((i + 1))
  done <<< "$templates"

  echo "" >&2
  read -rp "Select template [1-${#items[@]}]: " choice
  if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#items[@]} )); then
    echo "${items[$((choice - 1))]}"
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

  # Step 1: Pick a template.
  local templates
  templates="$(list_templates)"

  if [[ -z "$templates" ]]; then
    echo "No Scion templates found." >&2
    exit 1
  fi

  local selected_template
  selected_template="$(pick_template "$templates")"

  if [[ -z "$selected_template" ]]; then
    echo "No template selected." >&2
    exit 0
  fi

  # Step 2: Prompt for agent name.
  echo "" >&2
  read -rp "Agent name: " agent_name

  if [[ -z "$agent_name" ]]; then
    echo "No agent name provided." >&2
    exit 0
  fi

  # Sanitise: scion slugifies names, but we pass through as-is and let scion
  # handle validation.

  # Step 3: Check for duplicate pane (tracked via .agent field).
  if pane_exists_for_agent "$agent_name"; then
    log "Pane already exists for $agent_name — focusing it."
    local pane_id
    pane_id="$(pane_id_for_agent "$agent_name")"
    if [[ -n "$pane_id" ]]; then
      herdr agent focus "$pane_id" 2>/dev/null || true
    fi
    exit 0
  fi

  # Step 4: Create pane with the attach wrapper in --start mode.
  log "Starting agent '$agent_name' from template '$selected_template'..."
  herdr pane split --direction right \
    --env "SCION_AGENT=${agent_name}" \
    -- bash "${SCRIPT_DIR}/scion-attach-wrapper.sh" "$agent_name" --start "$selected_template"
}

main "$@"

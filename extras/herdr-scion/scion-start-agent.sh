#!/usr/bin/env bash
# scion-start-agent.sh — Start a new Scion agent from a template and open it
# in a herdr pane.
#
# Starts the agent in the effective scion project (see resolve_scope in
# scion-common.sh; the header names it and where scion got it from). With
# SCION_HERDR_SCOPE=all it asks for the project first. Shows the project's
# templates, lets the user pick one, prompts for an agent name, then creates
# a herdr pane running scion-attach-wrapper.sh in --start mode. If the named
# agent is already running, attaches to it instead.
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
# Scoped to the current project (scion_scoped).
list_templates() {
  scion_scoped template list 2>/dev/null \
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

# All mode only. Collect distinct known projects as "project\tprojectPath"
# rows, from every agent `scion list -a` knows about (any phase, not just running — a project
# can be a valid place to start a new agent even with none currently
# running). projectPath may be empty (Hub mode). Also offers the invoking
# pane's own cwd as a candidate if it looks like a scion project directory
# (contains .scion) and isn't already covered.
distinct_projects() {
  local from_agents
  from_agents="$( (cd "$SCOPE_DIR" && scion list -a --format json 2>/dev/null) \
    | jq -r '[.[] | {project: (.project // "global"), projectPath: (.projectPath // "")}]
              | unique_by([.project, .projectPath])
              | .[] | [.project, .projectPath] | @tsv' 2>/dev/null)"

  local cwd_row=""
  if [[ -n "${HERDR_PLUGIN_CONTEXT_JSON:-}" ]]; then
    local cwd
    cwd="$(echo "$HERDR_PLUGIN_CONTEXT_JSON" \
      | jq -r '.workspace_cwd // .focused_pane_cwd // empty' 2>/dev/null)"
    if [[ -n "$cwd" && -d "${cwd}/.scion" ]] \
      && ! printf '%s\n' "$from_agents" | cut -f2 | grep -qxF "$cwd"; then
      cwd_row="$(printf '%s\t%s' "$(basename "$cwd")" "$cwd")"
    fi
  fi

  { [[ -n "$from_agents" ]] && printf '%s\n' "$from_agents"
    [[ -n "$cwd_row" ]] && printf '%s\n' "$cwd_row"; } | grep -v '^[[:space:]]*$' || true
}

# Pick a project using fzf if available, otherwise a numbered menu. Returns
# the full selected "project\tprojectPath" row.
pick_project() {
  local lines="$1"

  if command -v fzf >/dev/null 2>&1; then
    echo "$lines" \
      | fzf --prompt="Select project> " \
            --delimiter=$'\t' \
            --with-nth=1,2 \
            --header="PROJECT  PATH"
    return
  fi

  local -a rows=()
  local i=1
  echo "" >&2
  echo "Available Scion projects:" >&2
  while IFS= read -r line; do
    local proj path
    IFS=$'\t' read -r proj path <<< "$line"
    printf "  %d) %-24s  %s\n" "$i" "$proj" "$path" >&2
    rows+=("$line")
    i=$((i + 1))
  done <<< "$lines"

  echo "" >&2
  read -rp "Select project [1-${#rows[@]}]: " choice
  if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#rows[@]} )); then
    echo "${rows[$((choice - 1))]}"
  fi
}

# Pick using fzf if available, otherwise a numbered menu.
pick_template() {
  local templates="$1"

  if command -v fzf >/dev/null 2>&1; then
    echo "$templates" \
      | fzf --prompt="Select template> " \
            --header="$(scope_header)"$'\n'"Available Scion Templates"
    return
  fi

  # Fallback: numbered menu.
  local -a items=()
  local i=1
  echo "" >&2
  scope_header >&2
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
  for cmd in scion jq; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  command -v "$HERDR_BIN" >/dev/null 2>&1 || missing+=("$HERDR_BIN")
  if [[ ${#missing[@]} -gt 0 ]]; then
    log "Missing required commands: ${missing[*]}"
    exit 1
  fi
}

# Keep the overlay open on an error long enough to read it.
fail() {
  echo "$*" >&2
  read -rp "Press Enter to close." _ || true
  exit 1
}

# All mode: choose the project to start the agent in, and set the scope to
# it. The picker is skipped when there's only one known project.
choose_project_all_mode() {
  scope_set pin global "" all 0 all
  local projects project_count chosen_row chosen_project chosen_path
  projects="$(distinct_projects)"
  project_count="$(printf '%s\n' "$projects" | grep -c . || true)"
  if [[ "$project_count" -eq 0 ]]; then
    fail "No scion projects known (SCION_HERDR_SCOPE=all lists projects that have agents)."
  elif [[ "$project_count" -eq 1 ]]; then
    chosen_row="$projects"
  else
    chosen_row="$(pick_project "$projects")"
    if [[ -z "$chosen_row" ]]; then
      echo "No project selected." >&2
      exit 0
    fi
  fi
  IFS=$'\t' read -r chosen_project chosen_path <<< "$chosen_row"
  scope_set pin "${chosen_path:-$chosen_project}" "" "$chosen_project" 0 all
}

main() {
  check_deps

  if [[ "$(herdr_scope_mode)" == "all" ]]; then
    choose_project_all_mode
  elif ! resolve_scope; then
    fail "Cannot start an agent: no scion project resolved."
  fi

  # Step 1: Pick a template from the project.
  local templates
  templates="$(list_templates)"

  if [[ -z "$templates" ]]; then
    scope_header >&2
    fail "No Scion templates found."
  fi

  local selected_template
  selected_template="$(pick_template "$templates")"

  if [[ -z "$selected_template" ]]; then
    echo "No template selected." >&2
    exit 0
  fi

  # Step 2: Prompt for agent name.
  echo "" >&2
  scope_header >&2
  read -rp "Agent name: " agent_name

  if [[ -z "$agent_name" ]]; then
    echo "No agent name provided." >&2
    exit 0
  fi

  # Sanitise: scion slugifies names, but we pass through as-is and let scion
  # handle validation.

  # Identifier is project/name: scion agent names are only unique within a
  # project. This is also what the pane's label is built from.
  local identifier="${SCOPE_LABEL}/${agent_name}"

  # Step 3: Check for duplicate pane (tracked via pane .label).
  if pane_exists_for_agent "$identifier"; then
    log "Pane already exists for $identifier — focusing it."
    local pane_id
    pane_id="$(pane_id_for_agent "$identifier")"
    if [[ -n "$pane_id" ]]; then
      # `agent focus` takes a pane id when an agent is reported in it (the
      # wrapper reports one); 0.9.1's `pane focus` is directional only.
      "$HERDR_BIN" agent focus "$pane_id" >/dev/null
    fi
    exit 0
  fi

  # Step 4: Create pane with the attach wrapper in --start mode.
  log "Starting agent '$identifier' from template '$selected_template'..."
  open_agent_pane "$agent_name" --start "$selected_template" >/dev/null \
    || fail "Could not open a pane for $identifier."
}

main "$@"

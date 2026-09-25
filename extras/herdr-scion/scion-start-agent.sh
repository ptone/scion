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
# Extra args (e.g. -g <project>) are passed straight through to scion, so
# the listing can be scoped to the chosen project.
list_templates() {
  scion "$@" template list 2>/dev/null \
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

# Collect distinct known projects as "project\tprojectPath" rows, from every
# agent `scion list -a` knows about (any phase, not just running — a project
# can be a valid place to start a new agent even with none currently
# running). projectPath may be empty (Hub mode). Also offers the invoking
# pane's own cwd as a candidate if it looks like a scion project directory
# (contains .scion) and isn't already covered.
distinct_projects() {
  local from_agents
  from_agents="$(scion list -a --format json 2>/dev/null \
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

  # Step 0: choose the project to start the agent in. scion resolves its
  # target project from -g or CWD, then falls back to "global" — without an
  # explicit choice, template listing and `scion start` would silently
  # target the wrong project (or "global").
  local projects project_count chosen_project chosen_path
  projects="$(distinct_projects)"
  project_count="$(printf '%s\n' "$projects" | grep -c . || true)"

  local chosen_row
  if [[ "$project_count" -le 1 ]]; then
    chosen_row="$projects"
  else
    chosen_row="$(pick_project "$projects")"
    if [[ -z "$chosen_row" ]]; then
      echo "No project selected." >&2
      exit 0
    fi
  fi
  IFS=$'\t' read -r chosen_project chosen_path <<< "$chosen_row"

  local -a g_args=()
  if [[ -n "$chosen_path" ]]; then
    g_args=(-g "$chosen_path")
  elif [[ -n "$chosen_project" ]]; then
    g_args=(-g "$chosen_project")
  fi

  # Step 1: Pick a template, scoped to the chosen project.
  local templates
  templates="$(list_templates "${g_args[@]}")"

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

  # Identifier is project/name, not just name: scion agent names are only
  # unique within a project. This is also what the pane's label is built
  # from, so cross-project name collisions don't get treated as one pane.
  local identifier="${chosen_project}/${agent_name}"

  # Step 3: Check for duplicate pane (tracked via pane .label).
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

  # Step 4: Create pane with the attach wrapper in --start mode.
  log "Starting agent '$identifier' from template '$selected_template'..."

  # Split off the pane that opened us (passed via SCION_TARGET_PANE by
  # scion-open-pane.sh), so the new agent pane lands next to the user's
  # pane rather than off this overlay. Falls back to herdr's default target
  # (calling pane, then focused pane) if unset.
  local -a split_args=(--direction right --env "SCION_AGENT=${agent_name}" --env "SCION_PROJECT=${chosen_project}")
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

  local run_cmd
  run_cmd="bash '${SCRIPT_DIR}/scion-attach-wrapper.sh' '${agent_name}'"
  if [[ -n "$chosen_project" ]]; then
    run_cmd+=" --project '${chosen_project}'"
  fi
  if [[ -n "$chosen_path" ]]; then
    run_cmd+=" --project-path '${chosen_path}'"
  fi
  run_cmd+=" --start '${selected_template}'"
  "$HERDR_BIN" pane run "$pane_id" "$run_cmd"
}

main "$@"

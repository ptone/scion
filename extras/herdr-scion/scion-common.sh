#!/usr/bin/env bash
# scion-common.sh — Shared helpers for the herdr-scion integration plugin.
#
# Sourced by all plugin scripts. Do not execute directly.

# Resolve the project CWD from herdr's plugin context.
#
# Herdr runs plugin commands with CWD set to the plugin root directory.
# Scion resolves its project from CWD, so if the plugin root is inside the
# scion repo checkout (not the user's project), scion finds the wrong project
# or no agents. This function extracts the user's workspace CWD from herdr's
# plugin context and changes to it.
#
# Falls back to: workspace_cwd -> focused_pane_cwd -> current directory (no-op)
resolve_project_cwd() {
  local cwd=""
  if [[ -n "${HERDR_PLUGIN_CONTEXT_JSON:-}" ]]; then
    cwd="$(echo "$HERDR_PLUGIN_CONTEXT_JSON" \
      | jq -r '.workspace_cwd // .focused_pane_cwd // empty' 2>/dev/null)"
  fi
  if [[ -n "$cwd" && -d "$cwd" ]]; then
    cd "$cwd"
  fi
}

# Extract the agent identifier from scion list JSON.
# Uses .slug when present, falls back to .name (slug is omitempty in local mode).
# Usage: scion list --format json | jq -r '<filter> | .slug // .name'
#
# This is a documentation-only helper — the actual jq expression
# '.slug // .name' must be inlined in each jq call.

# Check whether a herdr pane already exists for a given scion agent.
# We track scion panes by the .agent field (set via herdr pane report-agent),
# with the format "scion/<identifier>".
#
# Returns 0 (true) if a pane exists, 1 (false) otherwise.
pane_exists_for_agent() {
  local identifier="$1"
  herdr pane list --json 2>/dev/null \
    | jq -e ".[] | select(.agent == \"scion/${identifier}\")" \
    >/dev/null 2>&1
}

# Get the pane_id of an existing scion agent pane.
# Returns the pane_id or empty string.
pane_id_for_agent() {
  local identifier="$1"
  herdr pane list --json 2>/dev/null \
    | jq -r ".[] | select(.agent == \"scion/${identifier}\") | .pane_id" 2>/dev/null \
    | head -1
}

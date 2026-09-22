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

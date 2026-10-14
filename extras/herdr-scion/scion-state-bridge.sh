#!/usr/bin/env bash
# scion-state-bridge.sh — Poll Scion agent states and report to herdr.
#
# Maps Scion activity states to herdr AgentStatus values and pushes updates
# via `herdr pane report-agent`. Runs as a background daemon, started from
# scion-discover.sh or manually.
#
# Panes are matched by their "scion:<project-slug>/<name>" label. Each
# project is polled with the scope the pane was opened with (a record
# written by scion-common.sh's open_agent_pane / the dashboard), never with
# `scion list -a`. A project whose scope no longer matches the scion
# context (scope_check), or that has no record, is reported as unknown.
#
# State mapping:
#   Scion                  -> Herdr
#   executing, working     -> working
#   thinking               -> working
#   blocked                -> blocked
#   waiting_for_input      -> blocked
#   completed              -> idle
#   stalled                -> idle
#   idle / null / other    -> idle
#   (phase != running)     -> idle
#
# `herdr pane report-agent --state` only accepts idle|working|blocked|unknown
# ("done" is not a settable state — herdr derives its own Done badge from
# idle plus its internal "seen" tracking). Completed/stopped agents are
# therefore reported as idle.
#
# Environment:
#   SCION_BRIDGE_INTERVAL  Poll interval in seconds (default: 4)
#   SCION_BRIDGE_PIDFILE   Path to PID file for singleton enforcement
#
# Requires: scion, herdr, jq on PATH.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scion-common.sh
source "${SCRIPT_DIR}/scion-common.sh"

POLL_INTERVAL="${SCION_BRIDGE_INTERVAL:-4}"
PIDFILE="${SCION_BRIDGE_PIDFILE:-${HERDR_PLUGIN_STATE_DIR:-/tmp}/scion-state-bridge.pid}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-state-bridge] $*" >&2; }

# Map a scion activity string to a herdr agent state.
map_state() {
  local activity="$1"
  local phase="$2"

  # If the agent is no longer running, report idle — herdr shows Done once
  # it observes the pane at rest; "done" itself is not a settable state.
  if [[ "$phase" != "running" ]]; then
    echo "idle"
    return
  fi

  case "$activity" in
    executing|working|thinking)
      echo "working"
      ;;
    blocked|waiting_for_input)
      echo "blocked"
      ;;
    *)
      # completed, stalled, idle, null, empty, unknown — all map to idle
      echo "idle"
      ;;
  esac
}

# Enforce singleton: only one state bridge per herdr session.
ensure_singleton() {
  # Create the directory for the PID file if it doesn't exist.
  local piddir
  piddir="$(dirname "$PIDFILE")"
  mkdir -p "$piddir" 2>/dev/null || true

  if [[ -f "$PIDFILE" ]]; then
    local old_pid
    old_pid="$(cat "$PIDFILE" 2>/dev/null)"
    if [[ -n "$old_pid" ]] && kill -0 "$old_pid" 2>/dev/null; then
      log "Another state bridge is already running (PID $old_pid). Exiting."
      exit 0
    fi
    # Stale PID file — previous bridge exited without cleanup.
    log "Removing stale PID file (PID $old_pid no longer running)."
  fi

  echo $$ > "$PIDFILE"
}

# Clean up the PID file on exit.
cleanup() {
  rm -f "$PIDFILE" 2>/dev/null
}

# ---------------------------------------------------------------------------
# Main poll loop
# ---------------------------------------------------------------------------

# Report herdr state for every pane of project <label>.
#   $1 — project label, $2 — "pane_id<TAB>name" lines for that project
poll_project() {
  local label="$1" entries="$2"
  local agents_json="" scoped=1

  if ! scope_record_load "$label"; then
    log "No scope record for project '$label' — reporting its panes as unknown."
    scoped=0
  elif ! scope_check; then
    log "Project '$label': $SCOPE_MISMATCH_MSG"
    scoped=0
  elif ! agents_json="$(scion_scoped list --format json 2>/dev/null)"; then
    log "scion list failed for project '$label'."
    scoped=0
  fi

  local pane_id name agent_info activity phase herdr_state
  while IFS=$'\t' read -r pane_id name; do
    [[ -z "$pane_id" || -z "$name" ]] && continue

    if [[ "$scoped" == "0" ]]; then
      herdr_state="unknown"
    else
      # Uses .slug // .name because slug is omitempty in local/podman mode.
      agent_info="$(jq -c --arg name "$name" \
        '[.[] | select((.slug // .name) == $name)] | first // empty' <<<"$agents_json" 2>/dev/null)"
      if [[ -z "$agent_info" ]]; then
        # Agent no longer in scion list at all — report idle (see note
        # above: "done" is not a settable report-agent state).
        herdr_state="idle"
      else
        activity="$(jq -r '.activity // "idle"' <<<"$agent_info")"
        phase="$(jq -r '.phase // "unknown"' <<<"$agent_info")"
        herdr_state="$(map_state "$activity" "$phase")"
      fi
    fi

    # Report to herdr (state only — identity/tracking is via pane .label).
    "$HERDR_BIN" pane report-agent "$pane_id" \
      --source "scion:integration" \
      --agent "scion/${label}/${name}" \
      --state "$herdr_state" >/dev/null
  done <<< "$entries"
}

poll_once() {
  local panes_json
  panes_json="$("$HERDR_BIN" pane list)" || return 1

  # "project<TAB>pane_id<TAB>name" for each scion pane.
  local pane_entries
  pane_entries="$(jq -r '.result.panes[] | select(.label != null and (.label | startswith("scion:")))
      | (.label | ltrimstr("scion:")) as $id
      | select($id | contains("/"))
      | [($id | split("/")[0]), .pane_id, ($id | split("/")[1:] | join("/"))] | @tsv' \
      <<<"$panes_json" 2>/dev/null)"

  [[ -z "$pane_entries" ]] && return 0

  local label
  while IFS= read -r label; do
    [[ -z "$label" ]] && continue
    poll_project "$label" "$(awk -F'\t' -v l="$label" '$1 == l { print $2 "\t" $3 }' <<<"$pane_entries")"
  done < <(cut -f1 <<<"$pane_entries" | sort -u)
}

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
  log "Starting state bridge (poll interval: ${POLL_INTERVAL}s)."
  ensure_singleton
  on_exit cleanup
  trap 'exit 0' INT TERM

  while true; do
    poll_once || log "Poll cycle failed — will retry."
    sleep "$POLL_INTERVAL"
  done
}

main "$@"

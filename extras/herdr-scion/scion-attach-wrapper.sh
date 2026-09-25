#!/usr/bin/env bash
# scion-attach-wrapper.sh — Wrap `scion attach` with reconnection and
# idempotent start-or-attach logic, scoped to the agent's own project.
#
# Usage:
#   scion-attach-wrapper.sh <name> --project <project> [--project-path <path>] [--start <template>]
#
# `scion attach`/`scion start` resolve their target project from -g/--global
# or the CWD, then fall back to the "global" project. Herdr panes don't run
# from the project's directory, so without an explicit scope every call
# fails with "agent '<name>' not found in project 'global'" whenever the
# agent actually lives in a non-global project. --project / --project-path
# carry that scope through from discovery so every scion call here targets
# the agent's real project, not whatever the pane's CWD happens to resolve
# to. --project-path is the project's filesystem path (local/podman mode);
# it may be empty in Hub mode, in which case --project (the project
# name/slug) is used as the -g value instead.
#
# Handles:
#   - Reconnection after inner tmux detach (Ctrl-b d inside scion session).
#   - Auto-reconnect with a 10-second timeout or on Enter.
#   - Graceful exit when the agent stops running.
#   - Idempotent start: if the agent is already running, attaches instead of
#     starting a duplicate. This works around the Hub-mode idempotency gap
#     where `scion start -a` may not handle already-running agents.
#
# Requires: scion, jq on PATH.

set -uo pipefail

NAME="${1:?Usage: scion-attach-wrapper.sh <name> --project <project> [--project-path <path>] [--start <template>]}"
shift

PROJECT=""
PROJECT_PATH=""
TEMPLATE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --project)
      PROJECT="${2:?--project requires a value}"
      shift 2
      ;;
    --project-path)
      PROJECT_PATH="${2:?--project-path requires a value}"
      shift 2
      ;;
    --start)
      TEMPLATE="${2:?--start requires a template name}"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

# ---------------------------------------------------------------------------
# Project scoping
# ---------------------------------------------------------------------------

# The value scion's -g/--global flag expects: the project's filesystem path
# in local/podman mode, or its name/slug when there's no local path (Hub
# mode). Passed through exactly as received from `scion list` — not
# normalized against project-root-vs-.scion-dir, which needs verifying
# against real project data.
G_VALUE=""
if [[ -n "$PROJECT_PATH" ]]; then
  G_VALUE="$PROJECT_PATH"
elif [[ -n "$PROJECT" ]]; then
  G_VALUE="$PROJECT"
fi

# `-a`/`--all` (list all agents across all projects) is a `scion list`-only
# flag — it isn't valid on `attach`/`start`. So: `list` calls fall back to
# `-a` when we have no project scope (old cross-project-safe behavior);
# `attach`/`start` calls have no such fallback and just omit the flag,
# accepting scion's own CWD/global resolution (the original bug, but with
# no worse an outcome than before this fix when we truly don't know the
# project).
if [[ -n "$G_VALUE" ]]; then
  LIST_ARGS=(-g "$G_VALUE")
  TARGET_ARGS=(-g "$G_VALUE")
else
  LIST_ARGS=(-a)
  TARGET_ARGS=()
fi

# scion also resolves its project from CWD as a secondary signal — cd into
# the project directory too, as a belt-and-suspenders alongside -g.
if [[ -n "$PROJECT_PATH" ]]; then
  cd "$PROJECT_PATH" 2>/dev/null \
    || echo "[scion-attach] Warning: could not cd to project path $PROJECT_PATH" >&2
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

log() { echo "[scion-attach] $*" >&2; }

check_deps() {
  local missing=()
  for cmd in scion jq; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    log "Missing required commands: ${missing[*]}"
    exit 1
  fi
}

# Check whether the agent is currently running. Scoped to the agent's own
# project via LIST_ARGS, so matching on name alone is unambiguous even when
# another project has an agent with the same name.
is_running() {
  scion "${LIST_ARGS[@]}" list -r --format json 2>/dev/null \
    | jq -e ".[] | select((.slug // .name) == \"$NAME\" and .phase == \"running\")" \
    >/dev/null 2>&1
}

# Get the agent's current lifecycle phase (or "unknown"). Same project scope
# as is_running().
agent_phase() {
  scion "${LIST_ARGS[@]}" list --format json 2>/dev/null \
    | jq -r ".[] | select((.slug // .name) == \"$NAME\") | .phase // \"unknown\"" 2>/dev/null \
    | head -1
}

# Register this pane with herdr so it can be tracked by the state bridge
# and duplicate detection. Uses HERDR_PANE_ID set by herdr for child processes.
register_pane() {
  if [[ -n "${HERDR_PANE_ID:-}" ]]; then
    "${HERDR_BIN_PATH:-herdr}" pane report-agent "$HERDR_PANE_ID" \
      --source "scion:integration" \
      --agent "scion/${PROJECT:-global}/${NAME}" \
      --state idle
  fi
}

# ---------------------------------------------------------------------------
# Initial connect — start if necessary
# ---------------------------------------------------------------------------

initial_connect() {
  if is_running; then
    log "Agent $NAME is running — attaching."
    scion "${TARGET_ARGS[@]}" attach "$NAME"
    return $?
  fi

  if [[ -n "$TEMPLATE" ]]; then
    log "Agent $NAME is not running — starting from template '$TEMPLATE'."
    scion "${TARGET_ARGS[@]}" start "$NAME" -t "$TEMPLATE" -a
    return $?
  fi

  # No template and agent not running — try attach anyway so the user gets
  # scion's own error message.
  log "Agent $NAME is not running and no template provided — attempting attach."
  scion "${TARGET_ARGS[@]}" attach "$NAME"
  return $?
}

# ---------------------------------------------------------------------------
# Reconnection loop
# ---------------------------------------------------------------------------

reconnect_loop() {
  while true; do
    initial_connect
    local exit_code=$?

    # Check if the agent is still running.
    local phase
    phase="$(agent_phase)"

    if [[ "$phase" != "running" ]]; then
      echo ""
      echo "Agent $NAME is no longer running (phase: ${phase})."
      echo "Press Enter to close this pane, or Ctrl-C."
      read -r || true
      return "$exit_code"
    fi

    echo ""
    echo "Disconnected from $NAME. Press Enter to reconnect, or Ctrl-C to close."
    # Auto-reconnect after 10 seconds if the user doesn't press anything.
    read -r -t 10 || true
  done
}

# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

check_deps
register_pane
reconnect_loop

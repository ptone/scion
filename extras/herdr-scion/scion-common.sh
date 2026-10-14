#!/usr/bin/env bash
# scion-common.sh — Shared helpers for the herdr-scion integration plugin.
#
# Sourced by all plugin scripts. Do not execute directly.

# Herdr sets HERDR_BIN_PATH to the exact binary running the current
# server/client for plugin and pane processes. Prefer it over relying on
# PATH resolution; fall back to "herdr" on PATH when it isn't set (e.g.
# running a script by hand for testing).
HERDR_BIN="${HERDR_BIN_PATH:-herdr}"

# The plugin directory (where this file lives).
SCION_HERDR_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# Exit cleanup
# ---------------------------------------------------------------------------

# Scripts share one EXIT trap; register cleanup commands with on_exit
# instead of calling `trap ... EXIT` (which would replace earlier ones).
_SCION_HERDR_ON_EXIT=()
_scion_herdr_run_exit() {
  local c
  for c in "${_SCION_HERDR_ON_EXIT[@]}"; do eval "$c"; done
}
on_exit() {
  _SCION_HERDR_ON_EXIT+=("$1")
  trap _scion_herdr_run_exit EXIT
}

# ---------------------------------------------------------------------------
# Plugin configuration
# ---------------------------------------------------------------------------

# Which agents the plugin shows: "project" (default; the effective scion
# project, see resolve_scope) or "all" (every project, the old cross-project
# view). Set SCION_HERDR_SCOPE in the environment herdr runs with, or put a
# line `SCION_HERDR_SCOPE=all` in "$(herdr plugin config-dir
# scion.herdr-integration)/config". The environment wins.
herdr_scope_mode() {
  local mode="${SCION_HERDR_SCOPE:-}"
  local cfg="${HERDR_PLUGIN_CONFIG_DIR:-}/config"
  if [[ -z "$mode" && -n "${HERDR_PLUGIN_CONFIG_DIR:-}" && -f "$cfg" ]]; then
    mode="$(sed -n 's/^[[:space:]]*SCION_HERDR_SCOPE[[:space:]]*=[[:space:]]*//p' "$cfg" \
      | tail -1 | tr -d "\"' \r")"
  fi
  case "$mode" in
    all) echo "all" ;;
    *) echo "project" ;;
  esac
}

# ---------------------------------------------------------------------------
# Project scope
# ---------------------------------------------------------------------------
#
# A scope says which scion project (and hub) a scion call targets. It is
# resolved once per action (resolve_scope) and then carried explicitly —
# to the attach wrapper as arguments, to the state bridge as a record file —
# so a pane stays on the project it was opened for.
#
#   SCOPE_MODE     pin: run `scion -g "$SCOPE_PROJECT" ...` (a filesystem
#                       path or "global"; or, in "all" mode, whatever
#                       `scion list -a` reported)
#                  context: run scion with no -g, so scion applies its own
#                       `scion use` context. scion only exempts a hub project
#                       from the hub.enabled check when it injected the
#                       project itself; an explicit `-g <uuid>` is checked
#                       and fails for users who never ran `scion hub enable`.
#   SCOPE_PROJECT  the -g value (pin), or the hub project UUID (context)
#   SCOPE_HUB      the hub endpoint scion reported, empty without a hub
#   SCOPE_LABEL    project slug for pane labels and the UI
#   SCOPE_SOURCE   where scion got the project (directory|context|flag|env|
#                  global|...); "all" for scopes built from `scion list -a`
#   SCOPE_VERIFY   1: scope_check compares `scion use` with the scope before
#                  scion calls (pin: hub; context: hub and project id); 0: no
#                  check ("all" mode)
#   SCOPE_ARGS     the scion global flags for this scope
#   SCOPE_DIR      empty temp dir every scion call runs from
#
# --hub is never passed. An explicit --hub can link or register a project on
# that hub, where the hub from `scion use` would refuse to (see HubFromContext
# in pkg/hubsync), so a hub change is caught by scope_check instead.

# Create the empty temp dir scion calls run from (once per process). Running
# from it means neither the plugin root (inside the scion checkout, which
# has its own .scion) nor a pane's shell CWD can change what scion resolves.
scope_dir_init() {
  [[ -n "${SCOPE_DIR:-}" && -d "$SCOPE_DIR" ]] && return 0
  SCOPE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scion-herdr.XXXXXX")" || {
    echo "Could not create a temp dir for scion calls." >&2
    return 1
  }
  on_exit "rm -rf '${SCOPE_DIR}'"
}

# Set SCOPE_ARGS from SCOPE_MODE/SCOPE_PROJECT.
_scope_update_args() {
  if [[ "$SCOPE_MODE" == "pin" ]]; then
    SCOPE_ARGS=(-g "$SCOPE_PROJECT")
  else
    SCOPE_ARGS=()
  fi
}

# Set the scope explicitly. Usage: scope_set <mode> <project> <hub> <label> <verify> [source]
scope_set() {
  SCOPE_MODE="$1" SCOPE_PROJECT="$2" SCOPE_HUB="$3" SCOPE_LABEL="$4" SCOPE_VERIFY="$5"
  SCOPE_SOURCE="${6:-${SCOPE_SOURCE:-}}"
  _scope_update_args
  scope_dir_init
}

# Resolve the scope this action acts on, exactly as scion would for the user.
#
# Precedence is scion's own (flags → env → directory project → `scion use`
# context → global settings): we ask `scion use --format json` rather than
# re-implementing it. It runs from herdr's workspace_cwd when that is a
# directory (so a workspace inside a scion project targets that project, as
# any scion command there would), otherwise from the empty temp dir.
#
# Returns 1, with the reason on stderr (the herdr plugin log, or the
# overlay), when no project is resolved.
resolve_scope() {
  scope_dir_init || return 1

  local probe_dir="$SCOPE_DIR" ws=""
  if [[ -n "${HERDR_PLUGIN_CONTEXT_JSON:-}" ]]; then
    ws="$(jq -r '.workspace_cwd // empty' <<<"$HERDR_PLUGIN_CONTEXT_JSON" 2>/dev/null)"
  fi
  if [[ -n "$ws" && -d "$ws" ]]; then
    probe_dir="$ws"
  fi

  local use_json
  if ! use_json="$(cd "$probe_dir" && scion use --format json)"; then
    echo "'scion use --format json' failed (run from ${probe_dir})." >&2
    return 1
  fi

  local path id source hub label
  path="$(jq -r '.effective.project.path // empty' <<<"$use_json")"
  id="$(jq -r '.effective.project.id // empty' <<<"$use_json")"
  source="$(jq -r '.effective.project.source // "none"' <<<"$use_json")"
  hub="$(jq -r '.effective.hub.endpoint // empty' <<<"$use_json")"
  label="$(jq -r '.effective.project.slug // .effective.project.name // empty' <<<"$use_json")"

  # scion shows the path with the home directory abbreviated to "~".
  # shellcheck disable=SC2088  # matching a literal "~" is the point
  case "$path" in
    "~") path="$HOME" ;;
    "~/"*) path="${HOME}/${path#"~/"}" ;;
  esac

  local mode project
  if [[ "$source" == "none" ]]; then
    echo "No scion project selected (checked from ${probe_dir}) — run 'scion use <project>' or open the herdr workspace in a scion project directory." >&2
    return 1
  elif [[ "$source" == "global" ]]; then
    mode=pin project=global
  elif [[ "$source" == "context" && -n "$id" ]]; then
    mode=context project="$id"
  elif [[ -n "$path" ]]; then
    mode=pin project="$path"
  elif [[ -n "$id" ]]; then
    # A hub project named by a flag or env var outside the context: pin it
    # as given, so scion applies the same rules it would to the user.
    mode=pin project="$id"
  else
    echo "scion resolved a project (source ${source}) with neither a path nor an id — run 'scion use <project>'." >&2
    return 1
  fi

  [[ -n "$label" ]] || label="$(basename "$project")"
  scope_set "$mode" "$project" "$hub" "$label" 1 "$source"
}

# One line naming the scope, for picker headers and logs.
scope_header() {
  if [[ "${SCOPE_SOURCE:-}" == "all" ]]; then
    echo "project: all projects (SCION_HERDR_SCOPE=all)"
  else
    echo "project: ${SCOPE_LABEL} (from ${SCOPE_SOURCE:-unknown})"
  fi
}

_scope_norm_endpoint() { local e="${1%/}"; echo "$e"; }

# Check that scion still resolves this scope the way it did when it was
# pinned. Returns 0 when it does (or SCOPE_VERIFY is 0). Returns 1 on a
# mismatch and 2 when `scion use` fails, with the message in
# SCOPE_MISMATCH_MSG. Don't call scion for this scope while it fails.
# shellcheck disable=SC2034  # SCOPE_MISMATCH_MSG is read by callers
scope_check() {
  SCOPE_MISMATCH_MSG=""
  [[ "${SCOPE_VERIFY:-1}" == "1" ]] || return 0

  local use_json err
  if ! use_json="$(cd "$SCOPE_DIR" && scion ${SCOPE_ARGS[@]+"${SCOPE_ARGS[@]}"} use --format json 2>/dev/null)"; then
    err="$(cd "$SCOPE_DIR" && scion ${SCOPE_ARGS[@]+"${SCOPE_ARGS[@]}"} use --format json 2>&1 >/dev/null)"
    SCOPE_MISMATCH_MSG="Could not check the scion context ('scion use --format json' failed): ${err}"
    return 2
  fi

  local hub id slug
  hub="$(jq -r '.effective.hub.endpoint // empty' <<<"$use_json")"
  id="$(jq -r '.effective.project.id // empty' <<<"$use_json")"
  slug="$(jq -r '.effective.project.slug // .effective.project.id // "none"' <<<"$use_json")"

  if [[ "$(_scope_norm_endpoint "$hub")" == "$(_scope_norm_endpoint "$SCOPE_HUB")" ]] \
    && { [[ "$SCOPE_MODE" != "context" ]] || [[ "$id" == "$SCOPE_PROJECT" ]]; }; then
    return 0
  fi

  if [[ "$SCOPE_MODE" == "context" ]]; then
    SCOPE_MISMATCH_MSG="scion context is now ${slug} on ${hub:-no hub}; this pane is for ${SCOPE_LABEL} on ${SCOPE_HUB:-no hub}. Run 'scion use ${SCOPE_LABEL}' or close this pane."
  elif [[ -n "$SCOPE_HUB" ]]; then
    # A pinned path only changes hub (the context hub, or the project's own).
    SCOPE_MISMATCH_MSG="scion hub is now ${hub:-none}; this pane is for ${SCOPE_LABEL} on hub ${SCOPE_HUB}. Run 'scion use --hub ${SCOPE_HUB}' or close this pane."
  else
    SCOPE_MISMATCH_MSG="scion hub is now ${hub}; this pane is for ${SCOPE_LABEL} with no hub. Switch scion back to no hub or close this pane."
  fi
  return 1
}

# Run scion for the current scope, from the empty temp dir.
scion_scoped() {
  (cd "$SCOPE_DIR" && scion ${SCOPE_ARGS[@]+"${SCOPE_ARGS[@]}"} "$@")
}

# The scope as attach-wrapper arguments, shell-quoted for `herdr pane run`.
scope_wrapper_args() {
  local out
  out="--mode $(printf '%q' "$SCOPE_MODE") --project $(printf '%q' "$SCOPE_PROJECT")"
  out+=" --label $(printf '%q' "$SCOPE_LABEL")"
  [[ -n "$SCOPE_HUB" ]] && out+=" --hub $(printf '%q' "$SCOPE_HUB")"
  [[ "$SCOPE_VERIFY" == "1" ]] || out+=" --no-verify"
  echo "$out"
}

# The same, as an array (SCOPE_WRAPPER_ARGV) for commands built as JSON.
scope_wrapper_argv() {
  SCOPE_WRAPPER_ARGV=(--mode "$SCOPE_MODE" --project "$SCOPE_PROJECT" --label "$SCOPE_LABEL")
  [[ -n "$SCOPE_HUB" ]] && SCOPE_WRAPPER_ARGV+=(--hub "$SCOPE_HUB")
  [[ "$SCOPE_VERIFY" == "1" ]] || SCOPE_WRAPPER_ARGV+=(--no-verify)
  return 0
}

# Scope records let the state bridge (one per herdr server, shared by panes
# of several projects) poll each pane's project with the scope the pane was
# opened with. One file per label, in the plugin's state dir.
_scope_record_path() {
  local dir="${HERDR_PLUGIN_STATE_DIR:-${TMPDIR:-/tmp}/scion-herdr-state}/scopes"
  mkdir -p "$dir" || return 1
  echo "${dir}/${1//\//_}"
}

scope_record_write() {
  local f
  f="$(_scope_record_path "$SCOPE_LABEL")" || return 1
  printf '%s\n' "$SCOPE_MODE" "$SCOPE_PROJECT" "$SCOPE_HUB" "$SCOPE_VERIFY" "$SCOPE_SOURCE" >"$f"
}

# Load the record for <label> into SCOPE_*. Returns 1 if there is none.
scope_record_load() {
  local label="$1" f mode project hub verify source
  f="$(_scope_record_path "$label")" || return 1
  [[ -f "$f" ]] || return 1
  { read -r mode; read -r project; read -r hub; read -r verify; read -r source; } <"$f"
  scope_set "$mode" "$project" "$hub" "$label" "${verify:-1}" "$source"
}

# Extract the agent identifier from scion list JSON.
# Uses .slug when present, falls back to .name (slug is omitempty in local mode).
# Usage: scion list --format json | jq -r '<filter> | .slug // .name'
#
# This is a documentation-only helper — the actual jq expression
# '.slug // .name' must be inlined in each jq call.

# Check whether a herdr pane already exists for a given scion agent.
# We track scion panes by the pane .label, set via `herdr pane rename`
# immediately after the pane is split — format "scion:<identifier>".
# (report-agent's .agent field is used for state reporting only, not
# identity tracking, since it isn't set until the wrapper starts running.)
#
# Returns 0 (true) if a pane exists, 1 (false) otherwise.
pane_exists_for_agent() {
  local identifier="$1"
  local panes_json
  panes_json="$("$HERDR_BIN" pane list)" || return 1
  echo "$panes_json" \
    | jq -e ".result.panes[] | select(.label == \"scion:${identifier}\")" \
    >/dev/null
}

# Get the pane_id of an existing scion agent pane.
# Returns the pane_id or empty string.
pane_id_for_agent() {
  local identifier="$1"
  local panes_json
  panes_json="$("$HERDR_BIN" pane list)" || return 1
  echo "$panes_json" \
    | jq -r ".result.panes[] | select(.label == \"scion:${identifier}\") | .pane_id" \
    | head -1
}

# Open a herdr pane attached to agent <name> in the current scope: split
# (off $SCION_TARGET_PANE when set, i.e. the pane that opened an overlay),
# label it "scion:<project-slug>/<name>" right away (duplicate detection and
# the state bridge track panes by label), record the scope for the bridge,
# then type the wrapper command into the pane's shell.
# Usage: open_agent_pane <name> [extra wrapper args, e.g. --start <template>]
# Prints the new pane id.
open_agent_pane() {
  local name="$1"; shift
  local identifier="${SCOPE_LABEL}/${name}"

  local -a split_args=(--direction right --env "SCION_AGENT=${name}" --env "SCION_PROJECT=${SCOPE_LABEL}")
  if [[ -n "${SCION_TARGET_PANE:-}" ]]; then
    split_args=(--pane "$SCION_TARGET_PANE" "${split_args[@]}")
  fi

  local split_json pane_id
  split_json="$("$HERDR_BIN" pane split "${split_args[@]}")" || {
    echo "herdr pane split failed for ${identifier}" >&2
    return 1
  }
  pane_id="$(jq -r '.result.pane.pane_id // empty' <<<"$split_json")"
  if [[ -z "$pane_id" ]]; then
    echo "herdr pane split returned no pane_id for ${identifier}: ${split_json}" >&2
    return 1
  fi

  "$HERDR_BIN" pane rename "$pane_id" "scion:${identifier}" >/dev/null || return 1
  scope_record_write || echo "Warning: could not write the scope record for ${SCOPE_LABEL}" >&2

  local run_cmd extra
  run_cmd="bash $(printf '%q' "${SCION_HERDR_ROOT}/scion-attach-wrapper.sh") $(printf '%q' "$name") $(scope_wrapper_args)"
  for extra in "$@"; do run_cmd+=" $(printf '%q' "$extra")"; done
  "$HERDR_BIN" pane run "$pane_id" "$run_cmd" >/dev/null || return 1
  echo "$pane_id"
}

# Resolve the running herdr server's API socket path. $HERDR_SOCKET_PATH is
# set in the common cases we've verified (pane processes, and plugin actions
# invoked via `herdr plugin action invoke`), but we don't rely on it holding
# for every invocation path (e.g. startup hooks) or herdr version. `herdr
# status server --json` reports the same socket path (.socket) that the CLI
# itself talks to, and works regardless of how this script was invoked.
resolve_herdr_socket_path() {
  if [[ -n "${HERDR_SOCKET_PATH:-}" ]]; then
    echo "$HERDR_SOCKET_PATH"
    return 0
  fi

  local sock
  sock="$("$HERDR_BIN" status server --json 2>/dev/null | jq -r '.socket // empty')"
  if [[ -z "$sock" ]]; then
    return 1
  fi
  echo "$sock"
}

# Send a newline-terminated JSON request directly to herdr's socket API
# (used for methods with no CLI equivalent, e.g. layout.apply).
# Prints the raw JSON response line to stdout.
herdr_socket_request() {
  local request_json="$1"

  local socket_path
  if ! socket_path="$(resolve_herdr_socket_path)"; then
    echo "herdr_socket_request: could not resolve herdr's socket path (\$HERDR_SOCKET_PATH is unset and '$HERDR_BIN status server --json' returned none)" >&2
    return 1
  fi

  # The socket protocol frames one JSON request per line — compact the
  # request defensively in case the caller built it with pretty-printed jq.
  request_json="$(echo "$request_json" | jq -c '.')" || {
    echo "herdr_socket_request: request is not valid JSON" >&2
    return 1
  }

  if command -v socat >/dev/null 2>&1; then
    printf '%s\n' "$request_json" | socat -T 10 - "UNIX-CONNECT:${socket_path}"
    return $?
  fi

  if command -v nc >/dev/null 2>&1 && nc -h 2>&1 | grep -q -- '-U'; then
    printf '%s\n' "$request_json" | nc -U "${socket_path}"
    return $?
  fi

  if command -v python3 >/dev/null 2>&1; then
    python3 - "$socket_path" "$request_json" <<'PYEOF'
import socket, sys
sock_path, request_json = sys.argv[1], sys.argv[2]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(10)
s.connect(sock_path)
s.sendall((request_json + "\n").encode())
chunks = []
try:
    while True:
        chunk = s.recv(65536)
        if not chunk:
            break
        chunks.append(chunk)
        if b"\n" in chunk:
            break
except socket.timeout:
    pass
sys.stdout.write(b"".join(chunks).decode(errors="replace"))
s.close()
PYEOF
    return $?
  fi

  echo "herdr_socket_request: none of socat, nc -U, python3 are available" >&2
  return 1
}

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
# We track scion panes by the pane .label, set via `herdr pane rename`
# immediately after the pane is split — format "scion:<identifier>".
# (report-agent's .agent field is used for state reporting only, not
# identity tracking, since it isn't set until the wrapper starts running.)
#
# Returns 0 (true) if a pane exists, 1 (false) otherwise.
pane_exists_for_agent() {
  local identifier="$1"
  local panes_json
  panes_json="$(herdr pane list)" || return 1
  echo "$panes_json" \
    | jq -e ".result.panes[] | select(.label == \"scion:${identifier}\")" \
    >/dev/null
}

# Get the pane_id of an existing scion agent pane.
# Returns the pane_id or empty string.
pane_id_for_agent() {
  local identifier="$1"
  local panes_json
  panes_json="$(herdr pane list)" || return 1
  echo "$panes_json" \
    | jq -r ".result.panes[] | select(.label == \"scion:${identifier}\") | .pane_id" \
    | head -1
}

# Send a newline-terminated JSON request directly to herdr's socket API
# (used for methods with no CLI equivalent, e.g. layout.apply). Requires
# $HERDR_SOCKET_PATH, which herdr sets for plugin and pane processes.
# Prints the raw JSON response line to stdout.
herdr_socket_request() {
  local request_json="$1"

  if [[ -z "${HERDR_SOCKET_PATH:-}" ]]; then
    echo "herdr_socket_request: HERDR_SOCKET_PATH is not set" >&2
    return 1
  fi

  # The socket protocol frames one JSON request per line — compact the
  # request defensively in case the caller built it with pretty-printed jq.
  request_json="$(echo "$request_json" | jq -c '.')" || {
    echo "herdr_socket_request: request is not valid JSON" >&2
    return 1
  }

  if command -v socat >/dev/null 2>&1; then
    printf '%s\n' "$request_json" | socat -T 10 - "UNIX-CONNECT:${HERDR_SOCKET_PATH}"
    return $?
  fi

  if command -v nc >/dev/null 2>&1 && nc -h 2>&1 | grep -q -- '-U'; then
    printf '%s\n' "$request_json" | nc -U "${HERDR_SOCKET_PATH}"
    return $?
  fi

  if command -v python3 >/dev/null 2>&1; then
    python3 - "$HERDR_SOCKET_PATH" "$request_json" <<'PYEOF'
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

#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# antigravity-ui harness command (runs in the agent's tmux window).
#
# Process model:
#   - the Antigravity app (headless Electron) and nginx each run in their own
#     session, so keys sent to this tmux pane never reach them;
#   - this script ignores SIGINT, so a Scion interrupt stops nothing;
#   - pasted messages are read by an input sink and answered with a notice;
#     the app's stdin is /dev/null;
#   - when the app or nginx exits, the other is stopped and the wrapper exits
#     (with the app's exit code, or 1 if nginx died first).
#
# Tunables (environment):
#   JETSKI_LS_PORT       language server HTTPS port, loopback only (8443)
#   AGUI_PROXY_PORT      nginx port, the one exposed port (8090)
#   AGUI_ORIGIN_HEADER   on|off: rewrite Origin for the upstream (on)
#   AGUI_CSRF            off|inject: add the language server CSRF header (off)
#   AGUI_PATH_REWRITE    none|html: rewrite root-relative URLs in HTML (none)
#   AGUI_LS_TIMEOUT      seconds to wait for the language server (90)
#   AGUI_PROXY_TIMEOUT   seconds to wait for nginx (30)

set -euo pipefail

: "${JETSKI_LS_PORT:=8443}"
: "${AGUI_PROXY_PORT:=8090}"
: "${AGUI_ORIGIN_HEADER:=on}"
: "${AGUI_CSRF:=off}"
: "${AGUI_PATH_REWRITE:=none}"
: "${AGUI_LS_TIMEOUT:=90}"
: "${AGUI_PROXY_TIMEOUT:=30}"
: "${AGUI_APP_BIN:=/opt/Antigravity/antigravity}"
: "${AGUI_SHARE_DIR:=/usr/local/share/antigravity-ui}"
export ELECTRON_OZONE_PLATFORM_HINT=headless JETSKI_LS_PORT

STATE="${HOME}/.local/state/antigravity-ui"
HELPER="${AGUI_SHARE_DIR}/agui_helper.py"
APP_PID=""
NGX_PID=""
SINK_PID=""

log() { printf 'antigravity-ui: %s\n' "$*"; }

# Interrupts never stop anything (inherited by the input sink).
trap '' INT

# Print the tail of a log with anything that looks like a CSRF token masked.
log_tail() {
  local file="$1"
  [ -f "$file" ] || return 0
  log "last lines of ${file}:"
  tail -n 20 "$file" | sed -E 's/(csrf[_-]?token[=: "]+)[^ "&,]+/\1<redacted>/Ig'
}

kill_group() {
  local pid="$1"
  [ -n "$pid" ] || return 0
  kill -TERM -- "-${pid}" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
}

cleanup() {
  kill_group "$NGX_PID"
  kill_group "$APP_PID"
  if [ -n "$SINK_PID" ]; then kill "$SINK_PID" 2>/dev/null || true; fi
}

trap 'cleanup; exit 143' TERM HUP

fail() {
  log "ERROR: $1"
  shift
  for f in "$@"; do log_tail "$f"; done
  cleanup
  exit 1
}

# wait_until <timeout-seconds> <pid-that-must-stay-alive> <command...>
# Returns 0 when the command succeeds, 1 on timeout, 2 if the pid exits.
wait_until() {
  local timeout="$1" pid="$2" i=0
  shift 2
  until "$@" >/dev/null 2>&1; do
    kill -0 "$pid" 2>/dev/null || return 2
    i=$((i + 1))
    [ "$i" -lt "$timeout" ] || return 1
    sleep 1
  done
}

ls_up() { curl -sk -o /dev/null --max-time 2 "https://127.0.0.1:${JETSKI_LS_PORT}/"; }
proxy_up() { curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${AGUI_PROXY_PORT}/"; }

input_sink() {
  local line
  while IFS= read -r line || [ -n "$line" ]; do
    log "message not delivered: this harness is driven from the Antigravity UI."
  done
}

banner() {
  local agent="${SCION_AGENT_NAME:-<agent>}"
  echo
  echo "=================================================================="
  echo " Antigravity UI (experimental) is running."
  case "$1" in
    link)
      echo " Open the \"Antigravity\" port from this agent's page in Scion."
      ;;
    tunnel)
      echo " The port link is sandboxed on this hub, so the UI does not work"
      echo " through it. Connect with a tunnel instead:"
      echo "   scion tunnel ${agent} --port ${AGUI_PROXY_PORT} --local-port ${AGUI_PROXY_PORT}"
      echo " then open http://127.0.0.1:${AGUI_PROXY_PORT}/"
      ;;
    *)
      echo " The port could not be registered with the Hub (see"
      echo " ${STATE}/expose.log). If it is exposed, connect with:"
      echo "   scion tunnel ${agent} --port ${AGUI_PROXY_PORT} --local-port ${AGUI_PROXY_PORT}"
      ;;
  esac
  echo
  echo " Messages and tasks are not delivered; use the UI."
  echo " Logs: ${STATE}/ (use the shell window)."
  echo "=================================================================="
  echo
}

mkdir -p "$STATE"
chmod 700 "$STATE"

python3 -I "$HELPER" seed-update-off || log "warning: could not disable the app's update check"

log "starting the Antigravity app (headless)"
setsid "$AGUI_APP_BIN" --ozone-platform=headless --headless --disable-gpu --no-sandbox \
  </dev/null >>"${STATE}/app.log" 2>&1 &
APP_PID=$!

rc=0
wait_until "$AGUI_LS_TIMEOUT" "$APP_PID" ls_up || rc=$?
case "$rc" in
  0) ;;
  1) fail "the language server did not answer on 127.0.0.1:${JETSKI_LS_PORT} within ${AGUI_LS_TIMEOUT}s" "${STATE}/app.log" ;;
  *) fail "the app exited during start-up" "${STATE}/app.log" ;;
esac
log "language server is up on 127.0.0.1:${JETSKI_LS_PORT}"

python3 -I "$HELPER" render-nginx \
  --template "${AGUI_SHARE_DIR}/nginx.conf.tmpl" \
  --out "${STATE}/nginx.conf" \
  --state "$STATE" \
  --proxy-port "$AGUI_PROXY_PORT" \
  --ls-port "$JETSKI_LS_PORT" \
  --origin "$AGUI_ORIGIN_HEADER" \
  --csrf "$AGUI_CSRF" \
  --path-rewrite "$AGUI_PATH_REWRITE" \
  || fail "could not render the nginx config"

setsid nginx -p "$STATE" -e "${STATE}/nginx.err" -c "${STATE}/nginx.conf" \
  </dev/null >>"${STATE}/nginx.log" 2>&1 &
NGX_PID=$!

rc=0
wait_until "$AGUI_PROXY_TIMEOUT" "$NGX_PID" proxy_up || rc=$?
case "$rc" in
  0) ;;
  1) fail "nginx did not answer on 127.0.0.1:${AGUI_PROXY_PORT} within ${AGUI_PROXY_TIMEOUT}s" "${STATE}/nginx.err" ;;
  *) fail "nginx exited during start-up" "${STATE}/nginx.err" "${STATE}/nginx.log" ;;
esac
log "proxy is up on 127.0.0.1:${AGUI_PROXY_PORT}"

MODE=none
if command -v scion >/dev/null 2>&1; then
  if scion expose "$AGUI_PROXY_PORT" --label "Antigravity" --app-mode >>"${STATE}/expose.log" 2>&1; then
    MODE=link
  elif scion expose "$AGUI_PROXY_PORT" --label "Antigravity (use scion tunnel)" >>"${STATE}/expose.log" 2>&1; then
    MODE=tunnel
  fi
else
  log "warning: scion CLI not found; port not registered"
fi
banner "$MODE"

exec 3<&0
input_sink <&3 &
SINK_PID=$!
exec 3<&-

rc=0
who=""
wait -n -p who "$APP_PID" "$NGX_PID" || rc=$?
if [ "$who" = "$APP_PID" ]; then
  log "the app exited (code ${rc}); stopping nginx"
  APP_PID=""
  cleanup
  exit "$rc"
fi
log "nginx exited (code ${rc}); stopping the app"
log_tail "${STATE}/nginx.err"
NGX_PID=""
cleanup
exit 1

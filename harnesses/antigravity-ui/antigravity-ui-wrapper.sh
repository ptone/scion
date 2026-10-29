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
#   - the app's stdin is a FIFO this script holds open, so the app never sees
#     EOF. Lines typed or pasted into this pane get a "not delivered" notice;
#   - without working credentials the app waits for an interactive sign-in
#     code (its log shows "Enter the authorization code"). The wrapper then
#     stops the app and exits 1 with a message saying how to provide
#     credentials, during start-up or later;
#   - when the app or nginx exits, the other is stopped and the wrapper exits
#     (with the app's exit code, or 1 if nginx died first).
#
# Tunables (environment):
#   JETSKI_LS_PORT       preferred language server HTTPS port, loopback (8443)
#   AGUI_PROXY_PORT      preferred nginx port, the one exposed port (8090)
#                        A preferred port that is taken (agents on host
#                        networking share the loopback) is replaced by a
#                        free one.
#   AGUI_PATH_REWRITE    auto|none: path shim that lets the UI run under the
#                        Hub port proxy path (auto: on when the running app
#                        matches the shim's patch points)
#   AGUI_LS_TIMEOUT      seconds to wait for the language server (90)
#   AGUI_PROXY_TIMEOUT   seconds to wait for nginx (30)

set -euo pipefail

: "${JETSKI_LS_PORT:=8443}"
: "${AGUI_PROXY_PORT:=8090}"
: "${AGUI_PATH_REWRITE:=auto}"
: "${AGUI_LS_TIMEOUT:=90}"
: "${AGUI_PROXY_TIMEOUT:=30}"
: "${AGUI_APP_BIN:=/opt/Antigravity/antigravity}"
: "${AGUI_SHARE_DIR:=/usr/local/share/antigravity-ui}"
export ELECTRON_OZONE_PLATFORM_HINT=headless

STATE="${HOME}/.local/state/antigravity-ui"
HELPER="${AGUI_SHARE_DIR}/agui_helper.py"
APP_LOG="${STATE}/app.log"
APP_IN="${STATE}/app.stdin"
NO_AUTH_MARK="${STATE}/no-auth"
PROMPT_RE='Enter the authorization code'
NO_AUTH_MSG="the app is not signed in and is waiting for an interactive sign-in code. Assign a dedicated GCP service account to the agent, or stage a gcloud-adc file (Application Default Credentials); see the bundle README."
APP_PID=""
NGX_PID=""
SINK_PID=""
WATCH_PID=""

log() { printf 'antigravity-ui: %s\n' "$*"; }

# Interrupts never stop anything (inherited by the input sink).
trap '' INT

# Masks CSRF tokens and sign-in URLs in log lines shown in the pane.
mask() {
  sed -E -e 's/(csrf[_-]?token[=: "]+)[^ "&,]+/\1<redacted>/Ig' \
    -e 's#https://accounts\.google\.com/[^[:space:]"]+#<sign-in URL hidden>#g'
}

# Print the tail of a log, masked.
log_tail() {
  local file="$1"
  [ -f "$file" ] || return 0
  log "last lines of ${file}:"
  tail -n 20 "$file" | mask
}

kill_group() {
  local pid="$1"
  [ -n "$pid" ] || return 0
  kill -TERM -- "-${pid}" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
}

cleanup() {
  kill_group "$NGX_PID"
  kill_group "$APP_PID"
  local p
  for p in "$SINK_PID" "$WATCH_PID"; do
    if [ -n "$p" ]; then kill "$p" 2>/dev/null || true; fi
  done
}

trap 'cleanup; exit 143' TERM HUP

fail() {
  log "ERROR: $1"
  shift
  for f in "$@"; do log_tail "$f"; done
  cleanup
  exit 1
}

# Whether the app has asked for an interactive sign-in code.
sign_in_prompted() { grep -q -- "$PROMPT_RE" "$APP_LOG" 2>/dev/null; }

ls_up() { curl -sk -o /dev/null --max-time 2 "https://127.0.0.1:${JETSKI_LS_PORT}/"; }
proxy_up() { curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${AGUI_PROXY_PORT}/"; }

# Reads lines from this pane and answers each with a notice.
input_sink() {
  local line
  while IFS= read -r line || [ -n "$line" ]; do
    log "message not delivered: this harness is driven from the Antigravity UI."
  done
}

# After start-up: if the app asks for a sign-in code (for example when an
# assigned service account stops working), stop it; the main loop then
# exits 1 with the no-auth message.
sign_in_watcher() {
  while :; do
    if sign_in_prompted; then
      : >"$NO_AUTH_MARK"
      kill_group "$APP_PID"
      return
    fi
    sleep 2
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
      if [ "$SHIM" = shim ]; then
        echo " The port link is sandboxed on this hub, so the UI does not work"
        echo " through it. Connect with a tunnel instead:"
      else
        echo " The port link cannot show the UI (${SHIM_REASON})."
        echo " Connect with a tunnel instead:"
      fi
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

for var in JETSKI_LS_PORT AGUI_PROXY_PORT; do
  want="${!var}"
  got="$(python3 -I "$HELPER" free-port --preferred "$want")" || fail "invalid ${var}: ${want}"
  if [ "$got" != "$want" ]; then
    log "port ${want} is in use; using ${got} for ${var}"
  fi
  printf -v "$var" '%s' "$got"
done
if [ "$JETSKI_LS_PORT" = "$AGUI_PROXY_PORT" ]; then
  fail "JETSKI_LS_PORT and AGUI_PROXY_PORT must differ"
fi
export JETSKI_LS_PORT

# The app's stdin: a FIFO held open read-write on fd 4, so the app never
# sees EOF on stdin, whatever it does with it.
rm -f "$APP_IN"
mkfifo -m 600 "$APP_IN"
exec 4<>"$APP_IN"
rm -f "$NO_AUTH_MARK"
: >"$APP_LOG"

log "starting the Antigravity app (headless)"
setsid "$AGUI_APP_BIN" --ozone-platform=headless --headless --disable-gpu --no-sandbox \
  <"$APP_IN" >>"$APP_LOG" 2>&1 &
APP_PID=$!

exec 3<&0
input_sink <&3 &
SINK_PID=$!
exec 3<&-
# Wait for the language server. A sign-in prompt ends the start (no log
# tail: the log holds the sign-in URL).
waited=0
until ls_up; do
  sign_in_prompted && fail "$NO_AUTH_MSG"
  kill -0 "$APP_PID" 2>/dev/null || fail "the app exited during start-up" "$APP_LOG"
  waited=$((waited + 1))
  [ "$waited" -lt "$AGUI_LS_TIMEOUT" ] \
    || fail "the language server did not answer on 127.0.0.1:${JETSKI_LS_PORT} within ${AGUI_LS_TIMEOUT}s" "$APP_LOG"
  sleep 1
done
log "language server is up on 127.0.0.1:${JETSKI_LS_PORT}"
sign_in_watcher &
WATCH_PID=$!

# Path shim: only for the app version whose patch points it knows.
SHIM=none
SHIM_REASON="path shim turned off"
case "$AGUI_PATH_REWRITE" in
  auto)
    check="$(python3 -I "$HELPER" shim-check --ls-port "$JETSKI_LS_PORT")" || check="none: shim check failed"
    if [ "$check" = shim ]; then
      SHIM=shim
    else
      SHIM_REASON="${check#none: }"
      log "path shim off: ${SHIM_REASON}"
    fi
    ;;
  none) ;;
  *) fail "AGUI_PATH_REWRITE must be auto or none, got ${AGUI_PATH_REWRITE}" ;;
esac

python3 -I "$HELPER" render-nginx \
  --template "${AGUI_SHARE_DIR}/nginx.conf.tmpl" \
  --out "${STATE}/nginx.conf" \
  --state "$STATE" \
  --proxy-port "$AGUI_PROXY_PORT" \
  --ls-port "$JETSKI_LS_PORT" \
  --path-rewrite "$SHIM" \
  || fail "could not render the nginx config"

setsid nginx -p "$STATE" -e "${STATE}/nginx.err" -c "${STATE}/nginx.conf" \
  </dev/null >>"${STATE}/nginx.log" 2>&1 &
NGX_PID=$!

waited=0
until proxy_up; do
  kill -0 "$NGX_PID" 2>/dev/null || fail "nginx exited during start-up" "${STATE}/nginx.err" "${STATE}/nginx.log"
  waited=$((waited + 1))
  [ "$waited" -lt "$AGUI_PROXY_TIMEOUT" ] \
    || fail "nginx did not answer on 127.0.0.1:${AGUI_PROXY_PORT} within ${AGUI_PROXY_TIMEOUT}s" "${STATE}/nginx.err"
  sleep 1
done
log "proxy is up on 127.0.0.1:${AGUI_PROXY_PORT}"

MODE=none
if command -v scion >/dev/null 2>&1; then
  if [ "$SHIM" = shim ] \
    && scion expose "$AGUI_PROXY_PORT" --label "Antigravity" --app-mode >>"${STATE}/expose.log" 2>&1; then
    MODE=link
  elif scion expose "$AGUI_PROXY_PORT" --label "Antigravity (use scion tunnel)" >>"${STATE}/expose.log" 2>&1; then
    MODE=tunnel
  fi
else
  log "warning: scion CLI not found; port not registered"
fi
banner "$MODE"

rc=0
who=""
wait -n -p who "$APP_PID" "$NGX_PID" || rc=$?
if [ "$who" = "$APP_PID" ]; then
  if [ -f "$NO_AUTH_MARK" ]; then
    APP_PID=""
    fail "$NO_AUTH_MSG"
  fi
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

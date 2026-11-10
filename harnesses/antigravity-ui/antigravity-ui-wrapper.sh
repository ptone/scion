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
#   AGUI_SHIM_FORCE_MISMATCH  1: test hook, make the shim version check fail
#   AGUI_LS_TIMEOUT      seconds to wait for the language server (90)
#   AGUI_PROXY_TIMEOUT   seconds to wait for nginx (30)

set -euo pipefail

: "${JETSKI_LS_PORT:=8443}"
: "${AGUI_PROXY_PORT:=8090}"
: "${AGUI_PATH_REWRITE:=auto}"
: "${AGUI_SHIM_FORCE_MISMATCH:=0}"
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
# Under plain GCP identity passthrough (the host's own identity, no assigned
# service account) the identity is only used on the operator's opt-in.
if [ "${SCION_METADATA_MODE:-}" = "passthrough" ] && [ -z "${SCION_METADATA_SA_EMAIL:-}" ] \
  && [ "${AGY_ADC_AUTH:-}" != "true" ]; then
  NO_AUTH_MSG="the app is not signed in and is waiting for an interactive sign-in code. With GCP identity passthrough the host identity is not used by default. Assign a dedicated GCP service account to the agent, stage a gcloud-adc file, or set AGY_ADC_AUTH=true in the harness-config env (only on a throwaway VM whose service account is minimal); see the bundle README."
fi
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

# fd 4 is the app's stdin FIFO (opened below); only the app needs it, so
# every other child gets it closed.
helper() { python3 -I "$HELPER" "$@" 4>&-; }
ls_up() { curl -sk -o /dev/null --max-time 2 "https://127.0.0.1:${JETSKI_LS_PORT}/" 4>&-; }
proxy_up() { curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${AGUI_PROXY_PORT}/" 4>&-; }

# Reads lines from this pane and answers each with a notice.
input_sink() {
  local line
  while IFS= read -r line || [ -n "$line" ]; do
    log "message not delivered: this harness is driven from the Antigravity UI."
  done
}

# After start-up: if the app asks for a sign-in code (for example when an
# assigned service account stops working), stop it; the main loop then
# exits 1 with the no-auth message. Each pass reads only what the log gained
# since the last one (plus a small overlap, so a prompt split across two
# reads is still seen), so the cost does not grow with the agent's uptime.
sign_in_watcher() {
  local offset=0 size from n
  while :; do
    size="$(stat -c %s "$APP_LOG" 2>/dev/null || echo 0)"
    if [ "$size" -lt "$offset" ]; then offset=0; fi
    if [ "$size" -gt "$offset" ]; then
      from=$(( offset > 256 ? offset - 256 : 0 ))
      n="$(tail -c +"$((from + 1))" "$APP_LOG" | head -c "$((size - from))" | grep -c -- "$PROMPT_RE" || true)"
      if [ "${n:-0}" -gt 0 ]; then
        : >"$NO_AUTH_MARK"
        kill_group "$APP_PID"
        return
      fi
      offset="$size"
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
        echo " App mode is not available here (see ${STATE}/expose.log), so the"
        echo " port link cannot show the UI. Connect with a tunnel instead:"
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

# Ports. Agents on host networking share the loopback, so a port that is
# free now can be taken before our process binds it. Each start attempt
# therefore checks, after the probe answers, that the listener belongs to
# our own process tree, and otherwise retries on another port.
ATTEMPTS=3
WANT_LS="$JETSKI_LS_PORT"
WANT_PROXY="$AGUI_PROXY_PORT"

# pick_port <preferred> <attempt>: the preferred port on the first attempt
# if it is free, otherwise any free loopback port.
pick_port() {
  if [ "$2" -eq 1 ]; then
    helper free-port --preferred "$1"
  else
    helper free-port --any
  fi
}

# port_owner <port> <pid>: "ours", "foreign" or "none" for the listener on
# port, relative to pid's process tree (or session).
port_owner() {
  helper port-owner --port "$1" --pid "$2" 2>/dev/null || echo unknown
}

# owner_ok <port> <pid>: the listener on port is held by pid's tree.
owner_ok() { [ "$(port_owner "$1" "$2")" = ours ]; }

stop_and_reap() {
  kill_group "$1"
  wait "$1" 2>/dev/null || true
}

# The app's stdin: a FIFO held open read-write on fd 4, so the app never
# sees EOF on stdin, whatever it does with it.
rm -f "$APP_IN"
mkfifo -m 600 "$APP_IN"
exec 4<>"$APP_IN"
rm -f "$NO_AUTH_MARK"
: >"$APP_LOG"

exec 3<&0
input_sink <&3 &
SINK_PID=$!
exec 3<&-

attempt=0
while :; do
  attempt=$((attempt + 1))
  JETSKI_LS_PORT="$(pick_port "$WANT_LS" "$attempt")" || fail "invalid JETSKI_LS_PORT: ${WANT_LS}"
  if [ "$JETSKI_LS_PORT" != "$WANT_LS" ]; then
    log "port ${WANT_LS} is not available; using ${JETSKI_LS_PORT} for the language server"
  fi
  export JETSKI_LS_PORT

  log "starting the Antigravity app (headless)"
  setsid "$AGUI_APP_BIN" --ozone-platform=headless --headless --disable-gpu --no-sandbox \
    <"$APP_IN" >>"$APP_LOG" 2>&1 &
  APP_PID=$!

  # Wait for the language server. A sign-in prompt ends the start (no log
  # tail: the log holds the sign-in URL).
  waited=0
  lost_port=""
  until ls_up; do
    sign_in_prompted && fail "$NO_AUTH_MSG"
    if ! kill -0 "$APP_PID" 2>/dev/null; then
      # The app may have exited because another process took its port.
      if [ "$attempt" -lt "$ATTEMPTS" ] && [ "$(port_owner "$JETSKI_LS_PORT" "$APP_PID")" = foreign ]; then
        lost_port=1
        break
      fi
      fail "the app exited during start-up" "$APP_LOG"
    fi
    waited=$((waited + 1))
    [ "$waited" -lt "$AGUI_LS_TIMEOUT" ] \
      || fail "the language server did not answer on 127.0.0.1:${JETSKI_LS_PORT} within ${AGUI_LS_TIMEOUT}s" "$APP_LOG"
    sleep 1
  done
  if [ -z "$lost_port" ] && kill -0 "$APP_PID" 2>/dev/null && owner_ok "$JETSKI_LS_PORT" "$APP_PID"; then
    break
  fi
  log "port ${JETSKI_LS_PORT} is answered by a process outside this agent; trying another port"
  stop_and_reap "$APP_PID"
  APP_PID=""
  [ "$attempt" -lt "$ATTEMPTS" ] \
    || fail "could not start the language server on a port of its own in ${ATTEMPTS} attempts"
done
# The UI has no login of its own: the language server must listen on
# loopback only.
addr="$(helper listen-addr --port "$JETSKI_LS_PORT" --pid "$APP_PID" 2>/dev/null || echo unknown)"
if [ "$addr" != "loopback" ]; then
  fail "the language server's listener on port ${JETSKI_LS_PORT} is not loopback-only (${addr})"
fi
log "language server is up on 127.0.0.1:${JETSKI_LS_PORT}"
sign_in_watcher &
WATCH_PID=$!

# Path shim: only for the app version whose patch points it knows.
SHIM=none
SHIM_REASON="path shim turned off"
case "$AGUI_PATH_REWRITE" in
  auto)
    force=()
    if [ "$AGUI_SHIM_FORCE_MISMATCH" = "1" ]; then force=(--force-mismatch); fi
    check="$(helper shim-check --ls-port "$JETSKI_LS_PORT" "${force[@]}")" || check="none: shim check failed"
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

attempt=0
while :; do
  attempt=$((attempt + 1))
  AGUI_PROXY_PORT="$(pick_port "$WANT_PROXY" "$attempt")" || fail "invalid AGUI_PROXY_PORT: ${WANT_PROXY}"
  if [ "$AGUI_PROXY_PORT" != "$WANT_PROXY" ]; then
    log "port ${WANT_PROXY} is not available; using ${AGUI_PROXY_PORT} for the proxy"
  fi

  helper render-nginx \
    --template "${AGUI_SHARE_DIR}/nginx.conf.tmpl" \
    --out "${STATE}/nginx.conf" \
    --state "$STATE" \
    --proxy-port "$AGUI_PROXY_PORT" \
    --ls-port "$JETSKI_LS_PORT" \
    --path-rewrite "$SHIM" \
    || fail "could not render the nginx config"

  : >"${STATE}/nginx.err"
  setsid nginx -p "$STATE" -e "${STATE}/nginx.err" -c "${STATE}/nginx.conf" \
    </dev/null >>"${STATE}/nginx.log" 2>&1 4>&- &
  NGX_PID=$!

  waited=0
  lost_port=""
  until proxy_up; do
    if ! kill -0 "$NGX_PID" 2>/dev/null; then
      # nginx may have exited because another process took its port.
      if [ "$attempt" -lt "$ATTEMPTS" ] \
        && { grep -q "Address already in use" "${STATE}/nginx.err" 2>/dev/null \
             || [ "$(port_owner "$AGUI_PROXY_PORT" "$NGX_PID")" = foreign ]; }; then
        lost_port=1
        break
      fi
      fail "nginx exited during start-up" "${STATE}/nginx.err" "${STATE}/nginx.log"
    fi
    waited=$((waited + 1))
    [ "$waited" -lt "$AGUI_PROXY_TIMEOUT" ] \
      || fail "nginx did not answer on 127.0.0.1:${AGUI_PROXY_PORT} within ${AGUI_PROXY_TIMEOUT}s" "${STATE}/nginx.err"
    sleep 1
  done
  if [ -z "$lost_port" ] && kill -0 "$NGX_PID" 2>/dev/null && owner_ok "$AGUI_PROXY_PORT" "$NGX_PID"; then
    break
  fi
  log "port ${AGUI_PROXY_PORT} is answered by a process outside this agent; trying another port"
  stop_and_reap "$NGX_PID"
  NGX_PID=""
  [ "$attempt" -lt "$ATTEMPTS" ] \
    || fail "could not start the proxy on a port of its own in ${ATTEMPTS} attempts"
done
addr="$(helper listen-addr --port "$AGUI_PROXY_PORT" --pid "$NGX_PID" 2>/dev/null || echo unknown)"
if [ "$addr" != "loopback" ]; then
  fail "the proxy's listener on port ${AGUI_PROXY_PORT} is not loopback-only (${addr})"
fi
log "proxy is up on 127.0.0.1:${AGUI_PROXY_PORT}"

MODE=none
if command -v scion >/dev/null 2>&1; then
  if [ "$SHIM" = shim ] \
    && scion expose "$AGUI_PROXY_PORT" --label "Antigravity" --app-mode >>"${STATE}/expose.log" 2>&1 4>&-; then
    MODE=link
  elif scion expose "$AGUI_PROXY_PORT" --label "Antigravity (use scion tunnel)" >>"${STATE}/expose.log" 2>&1 4>&-; then
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

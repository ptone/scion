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

# workstation-smoke.sh - end-to-end check of the workstation hub path.
#
# In a temporary HOME and a temporary git repo, with no hub configured:
#   1. scion init                  creates the project (only where a container
#                                  runtime is installed; else the global
#                                  project is used)
#   2. scion hub link              starts the local server automatically and links
#   3. scion hub status            reports the project as linked
#   4. scion list                  returns an empty list through the hub
#   5. scion server stop           stops the server
# Also checks that SCION_HUB_AUTO_START=0 makes 'hub link' fail without
# starting anything. No container runtime is needed.
#
# Usage: hack/workstation-smoke.sh [path-to-scion-binary]
# Without an argument it builds ./cmd/scion with the default build tags
# (SQLite on). The server uses its default ports (web 8080, broker 9800), so
# they must be free.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/scion-ws-smoke.XXXXXX")"
SCION="${1:-}"

log() { printf '==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
  local rc=$?
  if [ -n "${SCION:-}" ] && [ -x "$SCION" ]; then
    "$SCION" server stop >/dev/null 2>&1 || true
  fi
  if [ $rc -ne 0 ] && [ -f "$WORK/home/.scion/server.log" ]; then
    echo "--- server log (last 50 lines) ---" >&2
    tail -n 50 "$WORK/home/.scion/server.log" >&2 || true
  fi
  rm -rf "$WORK"
  exit $rc
}
trap cleanup EXIT

if [ -z "$SCION" ]; then
  log "building scion (default tags)"
  (cd "$REPO_ROOT" && go build -buildvcs=false -o "$WORK/scion" ./cmd/scion)
  SCION="$WORK/scion"
fi
SCION="$(cd "$(dirname "$SCION")" && pwd)/$(basename "$SCION")"
[ -x "$SCION" ] || fail "scion binary not found or not executable: $SCION"

# Isolate from the caller: fresh HOME, and no inherited SCION_* settings
# (inside an agent container these point at another hub).
while IFS='=' read -r name _; do
  case "$name" in SCION_*) unset "$name" ;; esac
done < <(env)
export HOME="$WORK/home"
mkdir -p "$HOME"
export SCION_NO_BROWSER=1
# The workstation broker refuses to start without an image registry. No
# image is pulled here, so a placeholder is enough.
export SCION_IMAGE_REGISTRY=registry.invalid/scion-smoke
export GIT_CONFIG_NOSYSTEM=1
git config --global user.name "Scion Smoke"
git config --global user.email "smoke@example.com"
git config --global init.defaultBranch main

REPO="$WORK/repo"
mkdir -p "$REPO"
cd "$REPO"
git init -q
git commit -q --allow-empty -m "initial commit"

# 'scion init' needs a container runtime (it probes podman/docker). Where
# one is available, link the repo's own project; otherwise skip init and
# target the global project with --global (commands run outside a project
# require it). --format json skips the interactive image-registry question.
TARGET=()
if command -v docker >/dev/null 2>&1 || command -v podman >/dev/null 2>&1; then
  log "scion init --machine && scion init"
  "$SCION" init --machine --format json >"$WORK/init-machine.out" 2>&1 || {
    cat "$WORK/init-machine.out" >&2; fail "scion init --machine"; }
  "$SCION" init --format json >"$WORK/init.out" 2>&1 || { cat "$WORK/init.out" >&2; fail "scion init"; }
else
  log "no container runtime found: skipping scion init, using the global project"
  TARGET=(--global)
fi

log "SCION_HUB_AUTO_START=0 scion hub link fails and starts nothing"
if SCION_HUB_AUTO_START=0 "$SCION" "${TARGET[@]}" -y hub link >"$WORK/off.out" 2>&1; then
  cat "$WORK/off.out" >&2
  fail "hub link succeeded with auto-start off and no endpoint"
fi
grep -q "this command needs a hub" "$WORK/off.out" || { cat "$WORK/off.out" >&2; fail "missing not-configured error"; }
if "$SCION" server status 2>/dev/null | grep -qi "running" && ! "$SCION" server status 2>/dev/null | grep -qi "not running"; then
  fail "a server is running after hub link with auto-start off"
fi

log "scion hub link (auto-starts the local server)"
"$SCION" "${TARGET[@]}" -y hub link >"$WORK/link.out" 2>&1 || { cat "$WORK/link.out" >&2; fail "scion hub link"; }
cat "$WORK/link.out"
grep -q "starting the local scion server" "$WORK/link.out" || fail "hub link did not start the local server"
grep -q "is now linked to the Hub" "$WORK/link.out" || fail "hub link did not link the project"

log "scion hub status shows the project linked"
"$SCION" "${TARGET[@]}" hub status --format json >"$WORK/status.json" 2>"$WORK/status.err" || {
  cat "$WORK/status.json" "$WORK/status.err" >&2; fail "scion hub status"; }
grep -Eq '"linked"[[:space:]]*:[[:space:]]*true' "$WORK/status.json" || {
  cat "$WORK/status.json" >&2; fail "hub status does not report the project as linked"; }

log "scion list returns an empty list through the hub"
"$SCION" "${TARGET[@]}" list >"$WORK/list.out" 2>"$WORK/list.err" || {
  cat "$WORK/list.out" "$WORK/list.err" >&2; fail "scion list"; }
grep -q "Using hub: http://127.0.0.1:" "$WORK/list.err" || {
  cat "$WORK/list.out" "$WORK/list.err" >&2; fail "scion list did not go through the local hub"; }
"$SCION" "${TARGET[@]}" list --format json >"$WORK/list.json" 2>"$WORK/list.err" || {
  cat "$WORK/list.json" "$WORK/list.err" >&2; fail "scion list --format json"; }
tr -d '[:space:]' <"$WORK/list.json" | grep -Eq '^(\[\]|null)$' || {
  cat "$WORK/list.json" "$WORK/list.err" >&2; fail "scion list did not return an empty list"; }

log "scion server stop"
"$SCION" server stop >"$WORK/stop.out" 2>&1 || { cat "$WORK/stop.out" >&2; fail "scion server stop"; }

log "workstation smoke test passed"

#!/usr/bin/env bash
# scion-state-bridge.sh — Poll Scion agent states and report to herdr.
#
# Maps Scion activity states to herdr AgentStatus values and pushes updates
# via `herdr pane report-agent`. Runs as a background loop, typically started
# from scion-discover.sh.
#
# State mapping:
#   Scion                -> Herdr
#   executing, working   -> working
#   blocked              -> blocked
#   waiting_for_input    -> blocked
#   completed            -> done
#   stalled              -> idle
#   idle / null / other  -> idle
#
# Requires: scion, herdr, jq on PATH.
#
# Phase 2 — not yet implemented. This is a placeholder.

set -euo pipefail

log() { echo "[scion-state-bridge] $*" >&2; }

log "State bridge not yet implemented (Phase 2). Exiting."
exit 0

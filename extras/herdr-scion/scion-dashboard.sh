#!/usr/bin/env bash
# scion-dashboard.sh — Create a tiled herdr layout with all running Scion agents.
#
# Uses `herdr api layout.apply` to build a split layout where each pane
# runs scion-attach-wrapper.sh for one agent.
#
# Requires: scion, herdr, jq on PATH.
#
# Phase 3 — not yet implemented. This is a placeholder.

set -euo pipefail

log() { echo "[scion-dashboard] $*" >&2; }

log "Dashboard not yet implemented (Phase 3). Exiting."
exit 0

#!/usr/bin/env bash
#
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
#
# Regenerates every demo dataset.
#
# The generator is seeded and the digest builder is deterministic, so this
# reproduces byte-identical files on any machine. That is the whole reason most
# of these are not committed: regenerating costs a few seconds, and a 2.5MB
# JSON blob in git history costs forever. Only sample/ is committed, because it
# doubles as the frontend test fixture and as a readable reference for the
# input and digest formats.
#
# Usage:
#   ./demo/regenerate.sh            # regenerate everything
#   ./demo/regenerate.sh sample     # regenerate just the committed sample
#
# Run from anywhere; paths are resolved relative to this script.

set -euo pipefail

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$DEMO_DIR")"
BIN="$ROOT/seq-viz"

SAMPLE="$DEMO_DIR/sample"
GENERATED="$DEMO_DIR/generated"

# The frontend integration test reads the committed sample directly, so there
# is exactly one copy of it and it can never drift from the demo.
FIXTURE_NOTE="$ROOT/web-seq/src/seq/core/integration.test.ts"

only="${1:-all}"

echo "==> building seq-viz"
(cd "$ROOT" && go build -o seq-viz ./cmd/seq-viz/)

mkdir -p "$SAMPLE" "$GENERATED"

# ---------------------------------------------------------------------------
# sample/ -- committed. Small enough to read, big enough to be interesting.
#
# 14 agents over 6 minutes. Both the raw Cloud Logging export and the digest
# built from it are kept: the log is the real input format and is what
# exercises the parser, the digest is what the frontend actually consumes.
# ---------------------------------------------------------------------------
echo "==> sample: 14 agents / 6 min"
"$BIN" --synth-agents 14 --synth-duration 6m --synth-seed 1 \
  --dump-log "$SAMPLE/run.log.json"

# Built from the log file rather than straight from the synthesizer, so the
# committed digest is provably the output of the real parse path.
"$BIN" --log-file "$SAMPLE/run.log.json" \
  --dump-digest "$SAMPLE/run.digest.json"

if [[ "$only" == "sample" ]]; then
  echo
  echo "Done. Committed sample regenerated."
  echo "The frontend fixture reads $SAMPLE/run.digest.json directly (see $(basename "$FIXTURE_NOTE"))."
  exit 0
fi

# ---------------------------------------------------------------------------
# generated/ -- gitignored. Each scenario stresses a different axis.
# ---------------------------------------------------------------------------

# The headline demo, and the shape the design targets: 8-12 agents of interest
# on screen at once, drawn from a much larger population over the run.
echo "==> default: 60 agents / 45 min"
"$BIN" --synth-agents 60 --synth-duration 45m --synth-seed 1 \
  --dump-digest "$GENERATED/default.digest.json"

# Column pressure. ~35 concurrent is more than fits on screen, which is the
# only condition under which collapse, auto-fold and solo actually matter.
echo "==> wide: 90 agents / 30 min, ~35 concurrent"
"$BIN" --synth-agents 90 --synth-duration 30m --synth-seed 4 --synth-concurrency 34 \
  --dump-digest "$GENERATED/wide.digest.json"

# Volume. Thousands of intervals and ~11k warp knots: checks that culling and
# the binary-search frame build hold up, and that the payload stays sane.
echo "==> stress: 120 agents / 3 h"
"$BIN" --synth-agents 120 --synth-duration 3h --synth-seed 2 \
  --dump-digest "$GENERATED/stress.digest.json"

# Time pressure. Few agents, long run, mostly idle -- the express lane has to
# cross large empty stretches without the view feeling broken.
echo "==> idle: 20 agents / 2 h, sparse activity"
"$BIN" --synth-agents 20 --synth-duration 2h --synth-seed 5 --synth-concurrency 5 \
  --dump-digest "$GENERATED/idle.digest.json"

echo
echo "Done."
du -h "$SAMPLE"/*.json "$GENERATED"/*.json | sort -k2
echo
echo "Serve one with:"
echo "  ./seq-viz --digest-file demo/generated/wide.digest.json"

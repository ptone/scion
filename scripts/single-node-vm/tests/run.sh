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

# scripts/single-node-vm/tests/run.sh — runs the hybrid-tier test suite.
#
# This never contacts GCP: it puts tests/lib (containing a stub `gcloud`)
# at the front of PATH, sources hybrid-tier.sh directly (not deploy.sh, so
# no VM/Cloud Run/IAP flow runs at all), and calls each test_* function
# found in test_hybrid_tier.sh.
#
# Usage:
#   ./run.sh

# Deliberately no -e: assertions and helpers here routinely run commands
# (e.g. `grep -c` on a log with zero matches) whose non-zero exit is
# expected and meaningful, not a script error. -e would abort the whole
# suite on the first one instead of reporting a normal pass/fail count.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TIER_DIR="$(dirname "$SCRIPT_DIR")"

# The stub `gcloud` must resolve before any real one on the operator's
# machine.
export PATH="${SCRIPT_DIR}/lib:${PATH}"

# shellcheck source=scripts/single-node-vm/tests/lib/harness.sh
source "${SCRIPT_DIR}/lib/harness.sh"
# shellcheck source=scripts/single-node-vm/hybrid-tier.sh
source "${TIER_DIR}/hybrid-tier.sh"
# shellcheck source=scripts/single-node-vm/tests/test_hybrid_tier.sh
source "${SCRIPT_DIR}/test_hybrid_tier.sh"

TEST_NAMES=($(declare -F | awk '{print $3}' | grep '^test_' | sort))

for CURRENT_TEST in "${TEST_NAMES[@]}"; do
  "$CURRENT_TEST"
done

echo ""
echo "hybrid-tier tests: ${PASS_COUNT} passed, ${FAIL_COUNT} failed (of $((PASS_COUNT + FAIL_COUNT)) assertions across ${#TEST_NAMES[@]} tests)."

if [[ "$FAIL_COUNT" -gt 0 ]]; then
  exit 1
fi

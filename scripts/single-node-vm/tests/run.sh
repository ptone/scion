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
# at the front of PATH, sources hybrid-tier.sh directly, and calls each
# test_* function found in test_hybrid_tier.sh (function-level tests) and
# test_deploy_wiring.sh (tests that run deploy.sh itself as a real
# subprocess against the same stub, to cover the wiring between the two
# files that function-level tests can't reach).
#
# Requires bash >= 4 (uses `mapfile` and associative arrays). This is a
# dev-only test runner, not a deployment artifact: deploy.sh and
# hybrid-tier.sh themselves target bash 3.2+ (macOS's shipped /bin/bash),
# but this runner does not need to, and is not the vehicle for verifying
# that support -- see the bash-3.2 note in hybrid-tier.sh's header.
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
export TIER_DIR

# A sentinel, deliberately-bogus KUBECONFIG, exported globally so every
# test in this suite -- not just ones that explicitly set it -- proves it
# never reads or writes the ambient/operator KUBECONFIG. Every kubectl
# call in hybrid-tier.sh must set KUBECONFIG="$HYBRID_KUBECONFIG"
# explicitly; if any code path ever fell back to an ambient value, this
# path pointing at nothing would fail loudly instead of silently working
# against whatever the operator running these tests happens to have
# configured.
export KUBECONFIG="/nonexistent/should-never-be-read-or-written-kubeconfig"

# shellcheck source=scripts/single-node-vm/tests/lib/harness.sh
source "${SCRIPT_DIR}/lib/harness.sh"
# shellcheck source=scripts/single-node-vm/hybrid-tier.sh
source "${TIER_DIR}/hybrid-tier.sh"
# shellcheck source=scripts/single-node-vm/tests/test_hybrid_tier.sh
source "${SCRIPT_DIR}/test_hybrid_tier.sh"
# shellcheck source=scripts/single-node-vm/tests/test_deploy_wiring.sh
source "${SCRIPT_DIR}/test_deploy_wiring.sh"

mapfile -t TEST_NAMES < <(declare -F | awk '{print $3}' | grep '^test_' | sort)

TOTAL_PASS=0
TOTAL_FAIL=0

# Each test runs in its own subshell: a test_* function that unexpectedly
# hits hybrid-tier.sh's own `exit` (a bug, since only run_expect_fail
# cases are supposed to do that) then only ends that one subshell, not
# the whole run.sh process, so the remaining tests still get a chance to
# run and the suite still reports a final pass/fail count instead of
# dying silently partway through.
for CURRENT_TEST in "${TEST_NAMES[@]}"; do
  RESULT_FILE="$(mktemp)"
  (
    PASS_COUNT=0
    FAIL_COUNT=0
    "$CURRENT_TEST"
    {
      echo "PASS_COUNT=${PASS_COUNT}"
      echo "FAIL_COUNT=${FAIL_COUNT}"
    } > "$RESULT_FILE"
  )
  SUBSHELL_RC=$?
  if [[ -s "$RESULT_FILE" ]]; then
    # shellcheck disable=SC1090
    source "$RESULT_FILE"
  else
    echo "CRASH [${CURRENT_TEST}]: test subshell exited with status ${SUBSHELL_RC} before reporting any result"
    PASS_COUNT=0
    FAIL_COUNT=1
  fi
  TOTAL_PASS=$((TOTAL_PASS + PASS_COUNT))
  TOTAL_FAIL=$((TOTAL_FAIL + FAIL_COUNT))
  rm -f "$RESULT_FILE"
done

echo ""
echo "hybrid-tier tests: ${TOTAL_PASS} passed, ${TOTAL_FAIL} failed (of $((TOTAL_PASS + TOTAL_FAIL)) assertions across ${#TEST_NAMES[@]} tests)."

if [[ "$TOTAL_FAIL" -gt 0 ]]; then
  exit 1
fi

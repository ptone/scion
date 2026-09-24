# scripts/single-node-vm/tests/lib/harness.sh — shared helpers for the
# hybrid-tier test harness. Sourced by run.sh, never executed directly.
#
# Provides: stub info/warn/err/config_get/config_prompt (the functions
# hybrid-tier.sh expects its caller, deploy.sh, to already have defined),
# a fresh-per-test gcloud stub state dir on PATH, and small assertion
# helpers. No test here ever contacts GCP; the only `gcloud` on PATH is
# tests/lib/gcloud.

HARNESS_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_TESTS_DIR="$(dirname "$HARNESS_LIB_DIR")"
HARNESS_FIXTURES_DIR="${HARNESS_TESTS_DIR}/fixtures"

PASS_COUNT=0
FAIL_COUNT=0

# --- Stub versions of deploy.sh's own helpers, since hybrid-tier.sh is
# written to use its caller's info/warn/err rather than define its own. ---
info() { :; }
warn() { echo "WARN: $*" >&2; }
err()  { echo "ERR: $*" >&2; }

# --- Config stub -------------------------------------------------------
# Tests populate the CONFIG associative array (declare -A CONFIG=(...))
# before calling a hybrid_* function; config_get reads from it instead of
# parsing any real JSON file.
declare -A CONFIG
config_get() {
  local key="$1" default="${2:-}"
  if [[ -n "${CONFIG[$key]+set}" ]]; then
    printf '%s' "${CONFIG[$key]}"
  else
    printf '%s' "$default"
  fi
}
config_prompt() {
  # Not expected to be called in any test: every test drives hybrid-tier.sh
  # as if a config file were present (CONFIG_FILE is always set to a
  # non-empty placeholder below), which is the only case deploy.sh itself
  # runs non-interactively. If a test hits this, its CONFIG fixture is
  # missing a key it should have set instead.
  err "config_prompt called unexpectedly with: $2"
  exit 1
}
CONFIG_FILE="/dev/null/hybrid-tier-tests-placeholder"

# --- gcloud stub setup ---------------------------------------------------
# fresh_gcloud_state resets CONFIG, the invocation log, and the stub's
# on-disk fixture state dir for the next test.
fresh_gcloud_state() {
  CONFIG=()
  GCLOUD_STUB_STATE_DIR="$(mktemp -d)"
  GCLOUD_STUB_LOG="$(mktemp)"
  export GCLOUD_STUB_STATE_DIR GCLOUD_STUB_LOG
}

# seed_firewall_rule NAME DESCRIPTION — simulates a pre-existing rule.
seed_firewall_rule() {
  mkdir -p "${GCLOUD_STUB_STATE_DIR}/firewall-rules"
  printf '%s' "$2" > "${GCLOUD_STUB_STATE_DIR}/firewall-rules/$1"
}

# seed_cluster_network NETWORK — the fixture cluster's reported network.
seed_cluster_network() {
  printf '%s' "$1" > "${GCLOUD_STUB_STATE_DIR}/cluster-network"
}

# seed_instances FIXTURE_FILE — Standard or Autopilot node-instance fixture.
seed_instances() {
  cp "${HARNESS_FIXTURES_DIR}/$1" "${GCLOUD_STUB_STATE_DIR}/instances.tsv"
}

gcloud_log() {
  [[ -f "$GCLOUD_STUB_LOG" ]] && cat "$GCLOUD_STUB_LOG"
}

gcloud_call_count() {
  gcloud_log | grep -c . || true
}

# --- Assertions ----------------------------------------------------------
# Each prints PASS/FAIL with the current test name and bumps the counters.
# CURRENT_TEST is set by run.sh before invoking each test_* function.

assert_eq() {
  local expected="$1" actual="$2" msg="${3:-}"
  if [[ "$expected" == "$actual" ]]; then
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "FAIL [${CURRENT_TEST}]: ${msg} (expected '${expected}', got '${actual}')"
  fi
}

assert_contains() {
  local haystack="$1" needle="$2" msg="${3:-}"
  if [[ "$haystack" == *"$needle"* ]]; then
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "FAIL [${CURRENT_TEST}]: ${msg} (expected to find '${needle}')"
    echo "  --- haystack ---"
    echo "$haystack" | sed 's/^/  /'
  fi
}

assert_not_contains() {
  local haystack="$1" needle="$2" msg="${3:-}"
  if [[ "$haystack" != *"$needle"* ]]; then
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "FAIL [${CURRENT_TEST}]: ${msg} (did not expect to find '${needle}')"
  fi
}

assert_true() {
  local cond="$1" msg="${2:-}"
  if [[ "$cond" == "true" ]]; then
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "FAIL [${CURRENT_TEST}]: ${msg} (expected true, got '${cond}')"
  fi
}

# run_expect_fail CMD... — runs a command that is expected to call `exit`
# with a non-zero status (hybrid-tier.sh's fatal-error functions call exit
# directly, not return). Runs in a subshell so the test process survives.
# Sets RUN_EXIT_CODE and RUN_OUTPUT.
run_expect_fail() {
  local out
  set +e
  out="$("$@" 2>&1)"
  RUN_EXIT_CODE=$?
  set -e
  RUN_OUTPUT="$out"
}

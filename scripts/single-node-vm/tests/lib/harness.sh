# scripts/single-node-vm/tests/lib/harness.sh — shared helpers for the
# hybrid-tier test harness. Sourced by run.sh, never executed directly.
#
# Provides: stub info/warn/err/config_get/config_prompt (the functions
# hybrid-tier.sh expects its caller, deploy.sh, to already have defined),
# a fresh-per-test gcloud stub state dir on PATH, and small assertion
# helpers. No test here ever contacts GCP; the only `gcloud` on PATH is
# tests/lib/gcloud.
#
# This file has no shebang -- it is always `source`d, never executed --
# so shellcheck needs an explicit shell directive to know its dialect.
# shellcheck shell=bash

HARNESS_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PYTHON="${PYTHON:-python3}"

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
# shellcheck disable=SC2034 # read by hybrid_read_config in hybrid-tier.sh
CONFIG_FILE="/dev/null/hybrid-tier-tests-placeholder"

# --- gcloud stub setup ---------------------------------------------------
# fresh_gcloud_state resets CONFIG, the invocation log, and the stub's
# on-disk fixture state dir for the next test.
fresh_gcloud_state() {
  CONFIG=()
  GCLOUD_STUB_STATE_DIR="$(mktemp -d)"
  GCLOUD_STUB_LOG="$(mktemp)"
  mkdir -p "${GCLOUD_STUB_STATE_DIR}/firewall-rules" "${GCLOUD_STUB_STATE_DIR}/clusters" \
    "${GCLOUD_STUB_STATE_DIR}/migs" "${GCLOUD_STUB_STATE_DIR}/templates"
  export GCLOUD_STUB_STATE_DIR GCLOUD_STUB_LOG
}

# seed_firewall_rule_desc_only NAME DESCRIPTION — simulates a pre-existing
# rule that carries an arbitrary (possibly non-marker) description, with no
# other fields set. Only useful for the "unmarked, refuse to adopt" tests,
# where _hybrid_ensure_firewall_rule fails on the marker check before ever
# looking at the rest of the spec.
seed_firewall_rule_desc_only() {
  "$PYTHON" "${HARNESS_LIB_DIR}/firewall-rule-json.py" \
    "$2" "default" "INGRESS" "ALLOW" "tcp" "2049" "" "" "" "900" \
    > "${GCLOUD_STUB_STATE_DIR}/firewall-rules/$1.json"
}

# seed_firewall_rule_json NAME DESC NETWORK DIRECTION ACTION PROTO PORTS \
#   SOURCE_TAGS SOURCE_RANGES TARGET_TAGS PRIORITY
#
# Simulates a pre-existing rule with a fully specified spec, for the
# marker-plus-spec-verification tests (matching reuse, and drift).
seed_firewall_rule_json() {
  local name="$1"
  shift
  "$PYTHON" "${HARNESS_LIB_DIR}/firewall-rule-json.py" "$@" \
    > "${GCLOUD_STUB_STATE_DIR}/firewall-rules/${name}.json"
}

# seed_cluster NAME NETWORK MIG... — the fixture cluster's reported
# network and the instanceGroupUrls of its (single) node pool. With no
# MIG arguments, the cluster has zero node pools.
seed_cluster() {
  local name="$1" network="$2"
  shift 2
  "$PYTHON" -c "
import json, sys
network = sys.argv[1]
migs = sys.argv[2:]
body = {'network': network, 'nodePools': [{'instanceGroupUrls': migs}] if migs else []}
print(json.dumps(body))
" "$network" "$@" > "${GCLOUD_STUB_STATE_DIR}/clusters/${name}.json"
}

# seed_cluster_no_pools NAME NETWORK — a cluster with zero node pools
# (nodePools entirely empty), for the "no managed instance groups" case.
seed_cluster_no_pools() {
  seed_cluster "$1" "$2"
}

# seed_mig KEY TEMPLATE_REF — KEY is the last path segment of whatever MIG
# URL a test seeds into a cluster's instanceGroupUrls; TEMPLATE_REF is
# whatever hybrid-tier.sh should then pass on to `instance-templates
# describe` (a bare name or another fixture URL, looked up by its own last
# path segment in turn).
seed_mig() {
  printf '%s' "$2" > "${GCLOUD_STUB_STATE_DIR}/migs/$1.txt"
}

# seed_template KEY TAGS_CSV — TAGS_CSV is a comma-separated tag list;
# stored the way `--format=value(properties.tags.items)` actually renders
# a repeated field: semicolon-joined.
seed_template() {
  local key="$1" tags_csv="$2"
  printf '%s' "${tags_csv//,/;}" > "${GCLOUD_STUB_STATE_DIR}/templates/${key}.txt"
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
    local line
    while IFS= read -r line; do
      echo "  ${line}"
    done <<< "$haystack"
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
# Sets RUN_EXIT_CODE and RUN_OUTPUT, both read back by the caller in
# test_hybrid_tier.sh.
run_expect_fail() {
  local out
  set +e
  out="$("$@" 2>&1)"
  # shellcheck disable=SC2034 # read by callers in test_hybrid_tier.sh
  RUN_EXIT_CODE=$?
  set -e
  # shellcheck disable=SC2034 # read by callers in test_hybrid_tier.sh
  RUN_OUTPUT="$out"
}

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
  # Each call makes 4 new mktemp entries (two dirs, two files). Every
  # test in the suite calls this at least once, so leaving the previous
  # call's entries in place -- as this used to do -- accumulates roughly
  # one set per test for the life of the whole run: ~750 entries in
  # $TMPDIR for a run of this suite's size. Removing the previous call's
  # entries before making new ones keeps the suite's own footprint
  # bounded to "the current test's state" at any point in time, not
  # "every test's state so far". This still leaves the very last test's
  # entries in place when the run ends, which run.sh's own per-run
  # TMPDIR + EXIT trap is what actually cleans up.
  rm -rf "${GCLOUD_STUB_STATE_DIR:-}" "${KUBECTL_STUB_STATE_DIR:-}"
  rm -f "${GCLOUD_STUB_LOG:-}" "${KUBECTL_STUB_LOG:-}"
  CONFIG=()
  GCLOUD_STUB_STATE_DIR="$(mktemp -d)"
  GCLOUD_STUB_LOG="$(mktemp)"
  mkdir -p "${GCLOUD_STUB_STATE_DIR}/firewall-rules" "${GCLOUD_STUB_STATE_DIR}/clusters" \
    "${GCLOUD_STUB_STATE_DIR}/migs" "${GCLOUD_STUB_STATE_DIR}/templates" \
    "${GCLOUD_STUB_STATE_DIR}/instances" "${GCLOUD_STUB_STATE_DIR}/subnets" "${GCLOUD_STUB_STATE_DIR}/run-services" \
    "${GCLOUD_STUB_STATE_DIR}/addresses"
  export GCLOUD_STUB_STATE_DIR GCLOUD_STUB_LOG
  KUBECTL_STUB_STATE_DIR="$(mktemp -d)"
  KUBECTL_STUB_LOG="$(mktemp)"
  mkdir -p "${KUBECTL_STUB_STATE_DIR}/pv" "${KUBECTL_STUB_STATE_DIR}/pvc" "${KUBECTL_STUB_STATE_DIR}/namespace"
  export KUBECTL_STUB_STATE_DIR KUBECTL_STUB_LOG
}

kubectl_log() {
  [[ -f "$KUBECTL_STUB_LOG" ]] && cat "$KUBECTL_STUB_LOG"
}

# seed_k8s_pv NAME HUB_NAME SERVER PATH NAMESPACE PVC_NAME [RECLAIM] —
# simulates a pre-existing, marked PV with the given identity fields.
seed_k8s_pv() {
  local name="$1" hub="$2" server="$3" path="$4" ns="$5" pvc="$6" reclaim="${7:-Retain}"
  "$PYTHON" -c "
import json, sys
name, hub, server, path, ns, pvc, reclaim = sys.argv[1:8]
print(json.dumps({
    'kind': 'PersistentVolume',
    'metadata': {'name': name, 'labels': {'scion-deployment': hub}},
    'spec': {
        'nfs': {'server': server, 'path': path},
        'claimRef': {'namespace': ns, 'name': pvc},
        'persistentVolumeReclaimPolicy': reclaim,
    },
}))
" "$name" "$hub" "$server" "$path" "$ns" "$pvc" "$reclaim" > "${KUBECTL_STUB_STATE_DIR}/pv/${name}.json"
}

# seed_k8s_pv_unmarked NAME — a pre-existing PV with the target name and
# no marker label at all, for the marker-refusal tests.
seed_k8s_pv_unmarked() {
  printf '{"kind": "PersistentVolume", "metadata": {"name": "%s", "labels": {}}, "spec": {}}' "$1" \
    > "${KUBECTL_STUB_STATE_DIR}/pv/$1.json"
}

# seed_k8s_pvc NAME NAMESPACE HUB_NAME VOLUME_NAME — a pre-existing,
# marked PVC bound to VOLUME_NAME.
seed_k8s_pvc() {
  local name="$1" ns="$2" hub="$3" volume_name="$4"
  "$PYTHON" -c "
import json, sys
name, ns, hub, volume_name = sys.argv[1:5]
print(json.dumps({
    'kind': 'PersistentVolumeClaim',
    'metadata': {'name': name, 'namespace': ns, 'labels': {'scion-deployment': hub}},
    'spec': {'volumeName': volume_name},
}))
" "$name" "$ns" "$hub" "$volume_name" > "${KUBECTL_STUB_STATE_DIR}/pvc/${ns}__${name}.json"
}

# seed_k8s_pvc_unmarked NAME NAMESPACE — a pre-existing PVC with the
# target name/namespace and no marker label, for the marker-refusal tests.
seed_k8s_pvc_unmarked() {
  local name="$1" ns="$2"
  printf '{"kind": "PersistentVolumeClaim", "metadata": {"name": "%s", "namespace": "%s", "labels": {}}, "spec": {}}' \
    "$name" "$ns" > "${KUBECTL_STUB_STATE_DIR}/pvc/${ns}__${name}.json"
}

# seed_k8s_namespace NAME HUB_NAME — a pre-existing, marked namespace.
seed_k8s_namespace() {
  local name="$1" hub="$2"
  printf '{"kind": "Namespace", "metadata": {"name": "%s", "labels": {"scion-deployment": "%s"}}, "spec": {}}' \
    "$name" "$hub" > "${KUBECTL_STUB_STATE_DIR}/namespace/${name}.json"
}

# seed_k8s_namespace_unmarked NAME — a pre-existing namespace with no
# marker label, for the "used but not adopted" case.
seed_k8s_namespace_unmarked() {
  printf '{"kind": "Namespace", "metadata": {"name": "%s", "labels": {}}, "spec": {}}' "$1" \
    > "${KUBECTL_STUB_STATE_DIR}/namespace/$1.json"
}

# set_k8s_get_error KIND NAME — the next `kubectl get KIND NAME` call
# fails with an error that isn't a "not found" (a permissions or
# connectivity problem, say), so "unknown, not gone/absent" handling can
# be tested directly. KIND is pv, pvc, or namespace; for pvc, NAME must
# be "namespace__pvcname" to match the stub's own key.
set_k8s_get_error() {
  touch "${KUBECTL_STUB_STATE_DIR}/$1/$2.json.get-error"
}

# set_k8s_get_permission_masked KIND NAME — like set_k8s_get_error, but
# with a realistic permission-denied message that also happens to
# contain the words "not found" (some APIs word it that way deliberately
# to avoid confirming a resource's existence to an unauthorized caller).
# Must NOT be treated as "gone".
set_k8s_get_permission_masked() {
  printf 'Error from server: %s "%s" not found or permission denied' "$1" "$2" \
    > "${KUBECTL_STUB_STATE_DIR}/$1/$2.json.get-error"
}

# set_k8s_delete_will_fail KIND NAME — the next `kubectl delete KIND
# NAME` call fails instead of succeeding.
set_k8s_delete_will_fail() {
  touch "${KUBECTL_STUB_STATE_DIR}/$1/$2.json.delete-fail"
}

# seed_k8s_pod_using_pvc NAMESPACE POD_NAME PVC_NAME — a pod in NAMESPACE
# whose spec mounts PVC_NAME, for the teardown pod preflight ("stop
# agents first") to find.
seed_k8s_pod_using_pvc() {
  local namespace="$1" pod="$2" pvc="$3"
  mkdir -p "${KUBECTL_STUB_STATE_DIR}/pods"
  printf '{"items": [{"metadata": {"name": "%s"}, "spec": {"volumes": [{"name": "v", "persistentVolumeClaim": {"claimName": "%s"}}]}}]}' \
    "$pod" "$pvc" > "${KUBECTL_STUB_STATE_DIR}/pods/${namespace}.json"
}

# set_k8s_pods_get_error NAMESPACE — the next `kubectl get pods -n
# NAMESPACE` call fails, for the "could not check, unknown is never
# treated as gone" path.
set_k8s_pods_get_error() {
  mkdir -p "${KUBECTL_STUB_STATE_DIR}/pods"
  touch "${KUBECTL_STUB_STATE_DIR}/pods/$1.json.get-error"
}

# set_cluster_describe_error NAME — the next `container clusters
# describe` call for this cluster fails with an error that isn't
# NOT_FOUND (distinct from the cluster simply never having been seeded,
# which the stub already reports as NOT_FOUND).
set_cluster_describe_error() {
  touch "${GCLOUD_STUB_STATE_DIR}/clusters/$1.json.describe-error"
}

# set_cluster_describe_permission_masked NAME — like
# set_cluster_describe_error, but with a realistic permission-denied
# message that also happens to contain the words "not found". Must NOT
# be treated as "gone".
set_cluster_describe_permission_masked() {
  printf 'gcloud-stub: PERMISSION_DENIED: Cluster %s not found or permission denied' "$1" \
    > "${GCLOUD_STUB_STATE_DIR}/clusters/$1.json.describe-error"
}

# set_get_credentials_will_fail — the next `container clusters
# get-credentials` call fails.
set_get_credentials_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/get-credentials-should-fail"
}

# seed_instance NAME ZONE — simulates a pre-existing GCE VM.
seed_instance() {
  printf '%s' "$2" > "${GCLOUD_STUB_STATE_DIR}/instances/$1"
}

# set_instance_delete_will_fail NAME — the next `instances delete` call
# for this instance fails instead of succeeding (the instance stays
# "present" in the stub's state, matching a real failed delete).
set_instance_delete_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/instances/$1.delete-fail"
}

# set_firewall_list_will_fail — the next `firewall-rules list` call (used
# by hybrid_teardown_check) fails instead of returning a rule list.
set_firewall_list_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/firewall-rules-list-should-fail"
}

# set_instances_list_will_fail — every `instances list` call fails
# (simulating a transient API error), so the VM-gone check can't tell
# whether the VM is still there.
set_instances_list_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/instances-list-should-fail"
}

# set_instances_list_zone_unreachable — every `instances list` call
# simulates a real AggregatedList with one UNREACHABLE zone: exit 0 with
# the VM missing from stdout and a warning on stderr, unless the caller
# set CLOUDSDK_COMPUTE_ALLOW_PARTIAL_ERROR=false, in which case it's a
# hard error (exit 1) instead -- matching real gcloud's own behavior.
set_instances_list_zone_unreachable() {
  touch "${GCLOUD_STUB_STATE_DIR}/instances-list-zone-unreachable"
}

# seed_enabled_apis API... — overrides the stub's "everything is already
# enabled" default for `services list --enabled` with an explicit set, so
# a test can make some (or all) required APIs come back as missing.
seed_enabled_apis() {
  printf '%s\n' "$@" > "${GCLOUD_STUB_STATE_DIR}/enabled-apis.txt"
}

# set_services_list_will_fail — the next `services list` call fails
# (simulating a runner without serviceusage.services.list), so the API
# check can't tell what's already enabled.
set_services_list_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/services-list-should-fail"
}

# set_router_exists / set_service_account_exists — simulate a
# pre-existing base router or service account, so its create-only marker
# can be asserted absent on the adopt path.
set_router_exists() {
  touch "${GCLOUD_STUB_STATE_DIR}/router-exists"
}
set_service_account_exists() {
  touch "${GCLOUD_STUB_STATE_DIR}/service-account-exists"
}

# seed_run_service_exists NAME — the next `run services describe` call
# for this service succeeds (simulating a redeploy of an existing
# service). Without this, the stub reports NOT_FOUND.
seed_run_service_exists() {
  mkdir -p "${GCLOUD_STUB_STATE_DIR}/run-services"
  touch "${GCLOUD_STUB_STATE_DIR}/run-services/$1.exists"
}

# set_run_service_describe_error NAME — the next `run services describe`
# call for this service fails (a permissions problem, for example). On
# its own this tells hybrid_cloud_run_label_args nothing -- it no longer
# trusts describe's error text at all -- so pair this with either
# seed_run_service_exists (the follow-up `list` still finds it: no
# label) or set_run_service_list_error (list also fails: no label,
# fail-safe) to exercise the two ways a describe failure can resolve.
set_run_service_describe_error() {
  mkdir -p "${GCLOUD_STUB_STATE_DIR}/run-services"
  touch "${GCLOUD_STUB_STATE_DIR}/run-services/$1.describe-error"
}

# set_run_service_list_error — the next `run services list` call fails
# outright, simulating "truly cannot tell whether it exists". The
# positive-absence check must fail safe (no label) here, never read a
# failed list as confirmation of absence.
set_run_service_list_error() {
  mkdir -p "${GCLOUD_STUB_STATE_DIR}/run-services"
  touch "${GCLOUD_STUB_STATE_DIR}/run-services/list-error"
}

# set_firewall_delete_will_fail NAME — the next `firewall-rules delete`
# call for this rule fails instead of succeeding (the rule's JSON stays
# present in stub state, matching a real failed delete rather than one
# that succeeded or found nothing).
set_firewall_delete_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/firewall-rules/$1.json.delete-fail"
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

# seed_address NAME ADDRESS DESC — a pre-existing static internal address
# reservation with the given address and description.
seed_address() {
  local name="$1" address="$2" desc="$3" address_type="${4:-INTERNAL}" subnet="${5:-default}"
  "$PYTHON" -c "
import json, sys
addr, desc, address_type, subnet = sys.argv[1:5]
print(json.dumps({
    'address': addr,
    'description': desc,
    'addressType': address_type,
    'subnetwork': 'https://www.googleapis.com/compute/v1/projects/demo-project/regions/us-central1/subnetworks/' + subnet,
}))
" "$address" "$desc" "$address_type" "$subnet" > "${GCLOUD_STUB_STATE_DIR}/addresses/${name}.json"
}

# seed_address_unmarked NAME ADDRESS — a pre-existing reservation with no
# marker description, for the "refuse to adopt" test.
seed_address_unmarked() {
  seed_address "$1" "$2" "some other unrelated reservation"
}

# set_address_delete_will_fail NAME — the next `compute addresses delete`
# call for this reservation fails instead of succeeding.
set_address_delete_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/addresses/$1.json.delete-fail"
}

# set_address_list_will_fail NAME — the next `compute addresses list`
# call filtered to this name fails instead of returning a result.
set_address_list_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/addresses/$1.json.list-error"
}

# set_address_list_will_fail_after_create NAME — the address doesn't
# exist yet, so the next `compute addresses list` call (checking
# absence) still succeeds as empty; but once a `compute addresses
# create` call for NAME has actually run, every list call after that
# fails, simulating list eventual-consistency lag right after create.
set_address_list_will_fail_after_create() {
  touch "${GCLOUD_STUB_STATE_DIR}/addresses/$1.json.list-error-after-create"
}

# seed_firewall_rule_json NAME DESC NETWORK DIRECTION ACTION PROTO PORTS \
#   SOURCE_TAGS SOURCE_RANGES TARGET_TAGS PRIORITY \
#   [SOURCE_SAS [TARGET_SAS [DEST_RANGES [DISABLED [EXTRA_PROTO [EXTRA_PORTS]]]]]]
#
# Simulates a pre-existing rule with a fully specified spec, for the
# marker-plus-spec-verification tests (matching reuse, and drift). The
# trailing fields are optional and default to empty/false; see
# firewall-rule-json.py for what each one builds.
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
body = {
    'network': network,
    'subnetwork': 'default-subnet',
    'nodePools': [{'instanceGroupUrls': migs}] if migs else [],
    # A default pod CIDR, agreeing between both fields hybrid_discover
    # cross-checks, so existing fixtures that don't care about the
    # actual pod CIDR value don't all need updating -- same rationale as
    # seed_subnet's own implicit default below.
    'clusterIpv4Cidr': '10.52.0.0/14',
    'ipAllocationPolicy': {'clusterIpv4CidrBlock': '10.52.0.0/14'},
}
print(json.dumps(body))
" "$network" "$@" > "${GCLOUD_STUB_STATE_DIR}/clusters/${name}.json"
}

# seed_pod_cidr NAME CIDR — overrides both clusterIpv4Cidr and
# ipAllocationPolicy.clusterIpv4CidrBlock on an already-seeded cluster
# fixture to the same explicit value, for tests that care about the
# actual pod CIDR hybrid_discover reads.
seed_pod_cidr() {
  local name="$1" cidr="$2"
  "$PYTHON" -c "
import json, sys
p = sys.argv[1]
cidr = sys.argv[2]
d = json.load(open(p))
d['clusterIpv4Cidr'] = cidr
d['ipAllocationPolicy'] = {'clusterIpv4CidrBlock': cidr}
json.dump(d, open(p, 'w'))
" "${GCLOUD_STUB_STATE_DIR}/clusters/${name}.json" "$cidr"
}

# seed_pod_cidr_mismatch NAME CIDR ALT_CIDR — sets clusterIpv4Cidr and
# ipAllocationPolicy.clusterIpv4CidrBlock to two DIFFERENT values, for
# the "the two disagree" refusal test.
seed_pod_cidr_mismatch() {
  local name="$1" cidr="$2" alt_cidr="$3"
  "$PYTHON" -c "
import json, sys
p, cidr, alt = sys.argv[1:4]
d = json.load(open(p))
d['clusterIpv4Cidr'] = cidr
d['ipAllocationPolicy'] = {'clusterIpv4CidrBlock': alt}
json.dump(d, open(p, 'w'))
" "${GCLOUD_STUB_STATE_DIR}/clusters/${name}.json" "$cidr" "$alt_cidr"
}

# seed_pod_cidr_missing NAME — removes both pod-CIDR fields entirely,
# for the "absent" refusal test.
seed_pod_cidr_missing() {
  local name="$1"
  "$PYTHON" -c "
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d.pop('clusterIpv4Cidr', None)
d.pop('ipAllocationPolicy', None)
json.dump(d, open(p, 'w'))
" "${GCLOUD_STUB_STATE_DIR}/clusters/${name}.json"
}

# seed_cluster_no_pools NAME NETWORK — a cluster with zero node pools
# (nodePools entirely empty), for the "no managed instance groups" case.
seed_cluster_no_pools() {
  seed_cluster "$1" "$2"
}

# seed_subnet NAME CIDR — overrides the stub's default node-subnet
# fixture ("default-subnet" / 10.128.0.0/20, seeded implicitly so
# existing cluster fixtures don't all need updating) with an explicit
# name and primary IP range, for tests that care about the actual CIDR
# value.
seed_subnet() {
  local name="$1" cidr="$2"
  printf '{"ipCidrRange": "%s"}' "$cidr" > "${GCLOUD_STUB_STATE_DIR}/subnets/${name}.json"
}

# set_subnet_describe_will_fail NAME — the next `networks subnets
# describe` call for this subnet fails instead of returning a range.
set_subnet_describe_will_fail() {
  touch "${GCLOUD_STUB_STATE_DIR}/subnets/$1.json.describe-fail"
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

# assert_false COND MSG — COND must be "" or "false" (the two shapes
# `$([[ x ]] && echo true)` and `$([[ x ]] && echo true || echo false)`
# both produce when x is false).
assert_false() {
  local cond="$1" msg="${2:-}"
  if [[ -z "$cond" || "$cond" == "false" ]]; then
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "FAIL [${CURRENT_TEST}]: ${msg} (expected false/empty, got '${cond}')"
  fi
}

# run_expect_fail CMD... — runs a command that is expected to call `exit`
# with a non-zero status (hybrid-tier.sh's fatal-error functions call exit
# directly, not return). The command substitution below already runs it
# in a subshell, so its `exit` can never end the calling test (each test
# additionally runs in its own subshell at the run.sh level, so this is
# defense in depth, not the only thing preventing that). Restores
# errexit to whatever it was before the call, rather than unconditionally
# turning it on, so this never changes the caller's shell options as a
# side effect. Sets RUN_EXIT_CODE and RUN_OUTPUT, both read back by the
# caller in test_hybrid_tier.sh.
run_expect_fail() {
  local out had_errexit=false before_tmp after_tmp
  case "$-" in *e*) had_errexit=true ;; esac
  set +e
  before_tmp="$(find "${TMPDIR:-/tmp}" -mindepth 1 2>/dev/null | sort)"
  out="$("$@" 2>&1)"
  # shellcheck disable=SC2034 # read by callers in test_hybrid_tier.sh
  RUN_EXIT_CODE=$?
  [[ "$had_errexit" == "true" ]] && set -e
  # shellcheck disable=SC2034 # read by callers in test_hybrid_tier.sh
  RUN_OUTPUT="$out"
  # "$@" above runs inside a command-substitution subshell, so a mktemp
  # file/dir it creates (HYBRID_KUBECONFIG, set by
  # hybrid_k8s_setup_kubeconfig, is the recurring one) is invisible here
  # once the subshell exits: there is no variable left to rm -f, only the
  # file on disk. Diffing TMPDIR before/after and removing whatever
  # appeared catches this generically, for any function this wraps, not
  # just the ones a test author remembered to audit.
  after_tmp="$(find "${TMPDIR:-/tmp}" -mindepth 1 2>/dev/null | sort)"
  if [[ "$after_tmp" != "$before_tmp" ]]; then
    comm -13 <(printf '%s\n' "$before_tmp") <(printf '%s\n' "$after_tmp") | while IFS= read -r _new_tmp_entry; do
      [[ -n "$_new_tmp_entry" ]] && rm -rf "$_new_tmp_entry"
    done
  fi
}

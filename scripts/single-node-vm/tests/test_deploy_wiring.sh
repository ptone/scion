# scripts/single-node-vm/tests/test_deploy_wiring.sh — tests that run
# deploy.sh itself as a real subprocess against the stub `gcloud` on
# PATH, to cover the wiring between deploy.sh and hybrid-tier.sh that a
# function-level test (test_hybrid_tier.sh) cannot reach: whether
# deploy.sh actually calls hybrid_discover before its first create, gates
# --tags/add-tags on HYBRID_ENABLED, and puts the ownership check before
# any delete in --delete mode. No test here contacts GCP.
#
# This file has no shebang -- it is always `source`d, never executed --
# so shellcheck needs an explicit shell directive to know its dialect.
# shellcheck shell=bash
#
# Driving deploy.sh: each test writes a JSON config file and runs
# `bash deploy.sh --config FILE [--delete]` as a real subprocess, with
# PATH already pointing at the stub gcloud (run.sh exports it) and stdin
# redirected from /dev/null so deploy.sh's own `[[ ! -t 0 ]]` checks take
# the non-interactive branch. --delete-mode tests run to completion in
# well under a second, since nothing in that path sleeps or retries.
#
# create-mode tests only need the log up through the VM-exists check
# (deploy.sh's branch point between creating a new VM and tagging an
# existing one) -- not a full simulated deploy, which would need to get
# past an SSH-readiness retry loop with real sleeps between attempts. So
# run_deploy_create runs deploy.sh in the background and polls for a
# sentinel file the stub's `compute instances describe`/`create` handlers
# touch at exactly that point, then kills it -- typically well under a
# second, instead of waiting out a fixed timeout. The timeout passed in
# is only a safety net for a test whose assertions turn out to need
# something the sentinel doesn't cover.

DEPLOY_SH="${TIER_DIR}/deploy.sh"
HUB="demohub"
INSTANCE_NAME="scion-hub-${HUB}"

# base_config_json HUB [GKE_TARGET_JSON_FRAGMENT]
#
# GKE_TARGET_JSON_FRAGMENT, if given, must be a leading-comma JSON
# fragment like `, "gke_target": {...}` to splice in before the closing
# brace.
base_config_json() {
  local hub="$1" extra="${2:-}"
  cat <<EOF
{
  "hub_name": "${hub}",
  "project_id": "demo-project",
  "region": "us-central1",
  "machine_size": "small",
  "disk_size_gb": 200,
  "chat_plugins": [],
  "container_images": {"source": "build", "registry": "", "force_rebuild": false},
  "admin_email": "admin@example.com",
  "update_policy": "auto",
  "release_channel": "nightly"${extra}
}
EOF
}

hybrid_config_fragment() {
  echo ", \"gke_target\": {\"name\": \"mycluster\", \"location\": \"us-central1\", \"project\": \"demo-project\"}"
}

# run_deploy_delete CONFIG_JSON — runs `deploy.sh --delete` to completion
# (nothing in that path sleeps or retries). Sets DEPLOY_RC and DEPLOY_LOG.
# Never touches the caller's shell options: the subprocess's own exit
# status is captured through `$?` right after the command substitution,
# with no `set +e`/`set -e` pair of our own that could leak -- run.sh
# deliberately runs every test without -e, and a stray `set -e` left
# behind here would turn it on for the rest of the test, so a later
# command that fails as an ordinary, expected non-zero result (a `grep`
# with no match, say) would kill the test's subshell before any
# assertion ran.
run_deploy_delete() {
  local config_json="$1" config_file
  config_file="$(mktemp)"
  printf '%s' "$config_json" > "$config_file"
  DEPLOY_LOG="$(bash "$DEPLOY_SH" --delete --config "$config_file" < /dev/null 2>&1)"
  DEPLOY_RC=$?
  rm -f "$config_file"
}

# run_deploy_create CONFIG_JSON — runs `deploy.sh` (create mode) in the
# background and stops it shortly after the VM-exists sentinel appears
# (see the file header), falling back to a 10s wait if it never does (the
# safety margin used before the sentinel existed). Sets DEPLOY_RC
# (typically 143, from the TERM this sends once it's done reading the
# log -- these tests never assert on it) and DEPLOY_LOG. Same
# no-stray-set-e discipline as run_deploy_delete above.
run_deploy_create() {
  local config_json="$1" config_file
  config_file="$(mktemp)"
  printf '%s' "$config_json" > "$config_file"
  local sentinel="${GCLOUD_STUB_STATE_DIR}/vm-create-happened"
  rm -f "$sentinel"
  local log_file
  log_file="$(mktemp)"
  bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null > "$log_file" 2>&1 &
  local pid=$!
  local waited_ms=0
  while [[ ! -f "$sentinel" && "$waited_ms" -lt 10000 ]]; do
    sleep 0.1
    waited_ms=$((waited_ms + 100))
  done
  # A brief grace period past the sentinel for the triggering call's own
  # log line, and (on the existing-VM branch) the add-tags call right
  # after it, to actually be written before the log is read.
  sleep 0.2
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null
  DEPLOY_RC=$?
  DEPLOY_LOG="$(cat "$log_file")"
  rm -f "$config_file" "$log_file"
}

# line_number PATTERN LOG — the 1-based line number of the first log line
# containing PATTERN, or empty if none matches.
line_number() {
  echo "$2" | grep -n -F -- "$1" | head -1 | cut -d: -f1
}

# =====================================================================
# --delete wiring
# =====================================================================

test_deploy_delete_invalid_hub_name_refused() {
  fresh_gcloud_state
  run_deploy_delete "$(base_config_json "Not_A_Valid_Hub_Name")"
  assert_eq "1" "$DEPLOY_RC" "an invalid hub name must be refused on the --delete path too"
  assert_eq "0" "$(gcloud_log | grep -c . || true)" "an invalid hub name must be refused before any gcloud call"
}

test_deploy_delete_tier_off_no_rules_exits_zero() {
  fresh_gcloud_state
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "0" "$DEPLOY_RC" "tier-off teardown with no existing hybrid rules should exit 0"
}

test_deploy_delete_unmarked_exits_nonzero_before_any_delete() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "scion-hub-${HUB}-nfs-allow" "unrelated-rule-not-ours"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "an unmarked hybrid rule name match must fail the teardown run"
  assert_eq "0" "$(gcloud_log | grep -c ' delete' || true)" \
    "no delete call of any kind should be logged before the abort"
}

test_deploy_delete_hybrid_list_before_any_delete() {
  fresh_gcloud_state
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  run_deploy_delete "$(base_config_json "$HUB")"
  local log list_line delete_line
  log="$(gcloud_log)"
  list_line="$(line_number 'firewall-rules list' "$log")"
  delete_line="$(line_number ' delete' "$log")"
  assert_true "$([[ -n "$list_line" && -n "$delete_line" && "$list_line" -lt "$delete_line" ]] && echo true || echo false)" \
    "the hybrid ownership list call must precede the first delete call"
}

test_deploy_delete_hybrid_rules_deleted_after_vm() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  run_deploy_delete "$(base_config_json "$HUB")"
  local log vm_line hybrid_line
  log="$(gcloud_log)"
  vm_line="$(line_number "compute instances delete ${INSTANCE_NAME}" "$log")"
  hybrid_line="$(line_number "firewall-rules delete scion-hub-${HUB}-nfs-" "$log")"
  assert_true "$([[ -n "$vm_line" && -n "$hybrid_line" && "$vm_line" -lt "$hybrid_line" ]] && echo true || echo false)" \
    "the VM delete must precede the hybrid firewall-rule deletes"
}

test_deploy_delete_hybrid_order_allow_before_deny() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  run_deploy_delete "$(base_config_json "$HUB")"
  local log allow_line deny_line
  log="$(gcloud_log)"
  allow_line="$(line_number "firewall-rules delete scion-hub-${HUB}-nfs-allow" "$log")"
  deny_line="$(line_number "firewall-rules delete scion-hub-${HUB}-nfs-deny" "$log")"
  assert_true "$([[ -n "$allow_line" && -n "$deny_line" && "$allow_line" -lt "$deny_line" ]] && echo true || echo false)" \
    "the allow rule must be deleted before the deny rule (an allow must never briefly exist unpaired)"
}

test_deploy_delete_vm_failure_keeps_hybrid_rules() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  set_instance_delete_will_fail "$INSTANCE_NAME"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "a VM delete failure must make teardown exit non-zero"
  assert_eq "0" "$(gcloud_log | grep -c "firewall-rules delete scion-hub-${HUB}-nfs-" || true)" \
    "the hybrid rules must not be deleted when the VM delete failed"
  assert_contains "$DEPLOY_LOG" "still exists" "deploy.sh should report why the rules were kept"
}

test_deploy_delete_list_failure_aborts_before_any_delete() {
  fresh_gcloud_state
  set_firewall_list_will_fail
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "a failed ownership list call must abort teardown"
  assert_eq "0" "$(gcloud_log | grep -c ' delete' || true)" \
    "no delete call of any kind should be logged when the list call itself failed"
}

# =====================================================================
# Create-mode wiring
# =====================================================================

test_deploy_create_tier_off_no_tags_no_container_calls() {
  fresh_gcloud_state
  run_deploy_create "$(base_config_json "$HUB")"
  local log create_line
  log="$(gcloud_log)"
  assert_eq "0" "$(echo "$log" | grep -c 'container clusters' || true)" \
    "tier-off create must make zero container/cluster gcloud calls"
  create_line="$(echo "$log" | grep 'compute instances create' | head -1)"
  assert_true "$([[ -n "$create_line" ]] && echo true || echo false)" \
    "the VM create call should have been logged before the run stopped"
  assert_not_contains "$create_line" "--tags=" "tier-off VM create must not include --tags"
  assert_contains "$create_line" "--machine-type=e2-standard-4" "tier-off create keeps its base machine-type flag"
  assert_contains "$create_line" "--no-address" "tier-off create keeps its base --no-address flag"
  assert_contains "$create_line" "--boot-disk-size=200GB" "tier-off create keeps its base disk-size flag"
}

test_deploy_create_tier_on_tags_new_vm() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default" "mig-a"
  seed_mig "mig-a" "template-a"
  seed_template "template-a" "gke-mycluster-abc123-node"
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  local log create_line
  log="$(gcloud_log)"
  create_line="$(echo "$log" | grep 'compute instances create' | head -1)"
  assert_contains "$create_line" "--tags=scion-hub-${HUB}-nfs" \
    "tier-on VM create should carry the hybrid-tier network tag"
  local allow_create_line
  allow_create_line="$(echo "$log" | grep 'firewall-rules create.*nfs-allow' | head -1)"
  assert_contains "$allow_create_line" "--network=default" \
    "the hybrid firewall rule must be created on the hub's actual network, not a hardcoded or wrong one"
}

test_deploy_create_tier_on_existing_vm_gets_add_tags() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_cluster "mycluster" "default" "mig-a"
  seed_mig "mig-a" "template-a"
  seed_template "template-a" "gke-mycluster-abc123-node"
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  local log
  log="$(gcloud_log)"
  assert_eq "0" "$(echo "$log" | grep -c 'compute instances create' || true)" \
    "an existing VM must not be recreated"
  local add_tags_line
  add_tags_line="$(echo "$log" | grep 'instances add-tags' | head -1)"
  assert_contains "$add_tags_line" "add-tags ${INSTANCE_NAME}" \
    "an existing VM should get the hybrid-tier tag via add-tags"
  assert_contains "$add_tags_line" "--tags=scion-hub-${HUB}-nfs" "add-tags should carry the hybrid-tier network tag"
  assert_contains "$add_tags_line" "--zone=" "add-tags must target a specific zone, not the ambient default"
}

test_deploy_create_discovery_before_first_create() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default" "mig-a"
  seed_mig "mig-a" "template-a"
  seed_template "template-a" "gke-mycluster-abc123-node"
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  local log discover_line first_create_line
  log="$(gcloud_log)"
  discover_line="$(line_number 'container clusters describe' "$log")"
  first_create_line="$(echo "$log" | grep -n ' create ' | head -1 | cut -d: -f1)"
  assert_true "$([[ -n "$discover_line" && -n "$first_create_line" && "$discover_line" -lt "$first_create_line" ]] && echo true || echo false)" \
    "cluster discovery must happen before the first create call of any kind"
}

# =====================================================================
# VM-gone must be a positive, project-wide answer when
# hybrid rules are queued, and tier-off teardown keeps warning and
# continuing on a VM delete failure exactly as it always has.
# =====================================================================

test_deploy_delete_vm_uncertain_after_transient_list_error_keeps_rules() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  set_instance_delete_will_fail "$INSTANCE_NAME"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  set_instances_list_will_fail
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "an unconfirmable VM state must fail the run"
  assert_eq "0" "$(gcloud_log | grep -c "firewall-rules delete scion-hub-${HUB}-nfs-" || true)" \
    "the hybrid rules must not be deleted when the VM's fate can't be confirmed"
  assert_contains "$DEPLOY_LOG" "could not confirm" "should explain that the VM's state is unknown, not assumed gone"
}

test_deploy_delete_tier_off_vm_failure_warns_and_continues() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  set_instance_delete_will_fail "$INSTANCE_NAME"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "0" "$DEPLOY_RC" \
    "with no hybrid rules queued, a VM delete failure must warn and continue exactly as before this tier existed"
}

# =====================================================================
# A tier-off redeploy against an existing VM makes no hybrid
# mutating calls at all -- not even add-tags.
# =====================================================================

test_deploy_create_tier_off_existing_vm_untouched() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  run_deploy_create "$(base_config_json "$HUB")"
  local log
  log="$(gcloud_log)"
  assert_eq "0" "$(echo "$log" | grep -c 'compute instances create' || true)" \
    "an existing VM must not be recreated"
  assert_eq "0" "$(echo "$log" | grep -c 'instances add-tags' || true)" \
    "tier-off redeploy on an existing VM must not call add-tags"
  assert_eq "0" "$(echo "$log" | grep -c 'firewall-rules.*nfs-' || true)" \
    "tier-off redeploy must make no hybrid firewall-rule calls"
  assert_eq "0" "$(echo "$log" | grep -c 'container clusters' || true)" \
    "tier-off redeploy must make no cluster calls"
}

# =====================================================================
# The deploy-level teardown result (exit code, summary) is pinned
# for a hybrid rule delete failure specifically, distinct from a VM
# delete failure.
# =====================================================================

test_deploy_delete_hybrid_rule_delete_failure_exits_nonzero_excluded_from_summary() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  set_firewall_delete_will_fail "scion-hub-${HUB}-nfs-allow"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "a hybrid rule delete failure must fail the run (distinct from a VM delete failure)"
  assert_not_contains "$DEPLOY_LOG" "Deleted firewall rule:     scion-hub-${HUB}-nfs-allow" \
    "the summary must not claim the failed rule was deleted"
}

# The "no python needed when nothing matched" case is pinned at the
# function level instead of here: test_teardown_check_no_python_needed_
# when_nothing_matches in test_hybrid_tier.sh. deploy.sh itself requires
# $PYTHON unconditionally to parse --config (deploy.sh:209, pre-existing
# and unrelated to the hybrid tier), so a full subprocess run with
# PYTHON=/nonexistent can never reach the teardown ownership check at
# all -- it fails at config parsing first, every time, tier or no tier.
# run_deploy_delete's PYTHON_OVERRIDE parameter exists for this case, but
# there is no exit-0 scenario for it to demonstrate at this layer.

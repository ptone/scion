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

# run_deploy_delete_interactive PYTHON_OVERRIDE STDIN_TEXT — runs
# `deploy.sh --delete` with no --config at all, piping STDIN_TEXT to
# answer its interactive prompts (hub name, region, the confirmation).
# Unlike run_deploy_delete, deploy.sh never touches $PYTHON on this path
# before the teardown ownership check: --config is what triggers the
# unconditional Python preflight (deploy.sh:208) and config_get's own use
# of $PYTHON, and neither runs here. PYTHON_OVERRIDE, if non-empty, is
# exported for deploy.sh's own process only, to simulate a missing
# interpreter; it does not affect the stub, which reads
# GCLOUD_STUB_PYTHON instead (see tests/lib/gcloud) for exactly this
# reason. Same no-stray-set-e discipline as run_deploy_delete above.
run_deploy_delete_interactive() {
  local python_override="$1" stdin_text="$2"
  if [[ -n "$python_override" ]]; then
    DEPLOY_LOG="$(PYTHON="$python_override" bash "$DEPLOY_SH" --delete <<<"$stdin_text" 2>&1)"
  else
    DEPLOY_LOG="$(bash "$DEPLOY_SH" --delete <<<"$stdin_text" 2>&1)"
  fi
  DEPLOY_RC=$?
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

# Re-running teardown after the VM is already gone (no instance seeded,
# so `instances delete` fails as not-found and the project-wide list must
# positively confirm "gone") must actually delete both hybrid rules and
# succeed -- this is the exact recovery path deploy.sh's own "Keeping the
# hybrid-tier NFS firewall rules... Re-run teardown after the VM is
# deleted" message tells the operator to use.
test_deploy_delete_vm_already_gone_rules_deleted() {
  fresh_gcloud_state
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "0" "$DEPLOY_RC" "re-running teardown after the VM is already gone must succeed"
  assert_eq "2" "$(gcloud_log | grep -c "firewall-rules delete scion-hub-${HUB}-nfs-" || true)" \
    "both hybrid rules must be deleted once the VM is positively confirmed gone"
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
# API check: enable only what's missing, never the whole list when
# nothing needs it, and add container.googleapis.com when the tier is
# on. This is the second deliberate tier-off behavior change (the first
# is base markers on create, below): a validation runner without
# serviceusage.services.enable must never see an enable call for an API
# that's already on.
# =====================================================================

test_deploy_create_api_check_all_enabled_no_enable_call() {
  fresh_gcloud_state
  seed_enabled_apis compute.googleapis.com run.googleapis.com iap.googleapis.com \
    cloudbuild.googleapis.com artifactregistry.googleapis.com
  run_deploy_create "$(base_config_json "$HUB")"
  assert_eq "0" "$(gcloud_log | grep -c 'services enable' || true)" \
    "nothing missing must mean no enable call at all, not an enable call with zero APIs"
}

test_deploy_create_api_check_missing_enables_exact_set() {
  fresh_gcloud_state
  seed_enabled_apis compute.googleapis.com iap.googleapis.com artifactregistry.googleapis.com
  run_deploy_create "$(base_config_json "$HUB")"
  local enable_line
  enable_line="$(gcloud_log | grep 'services enable' | head -1)"
  assert_eq "1" "$(gcloud_log | grep -c 'services enable' || true)" "exactly one enable call for the missing set"
  assert_contains "$enable_line" "run.googleapis.com" "the enable call must include a missing API"
  assert_contains "$enable_line" "cloudbuild.googleapis.com" "the enable call must include the other missing API"
  assert_not_contains "$enable_line" "compute.googleapis.com" "an already-enabled API must not be re-enabled"
  assert_not_contains "$enable_line" "artifactregistry.googleapis.com" "an already-enabled API must not be re-enabled"
}

test_deploy_create_api_check_tier_on_adds_container() {
  fresh_gcloud_state
  seed_enabled_apis compute.googleapis.com run.googleapis.com iap.googleapis.com \
    cloudbuild.googleapis.com artifactregistry.googleapis.com
  seed_cluster "mycluster" "default" "mig-a"
  seed_mig "mig-a" "template-a"
  seed_template "template-a" "gke-mycluster-abc123-node"
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  local enable_line
  enable_line="$(gcloud_log | grep 'services enable' | head -1)"
  assert_eq "1" "$(gcloud_log | grep -c 'services enable' || true)" "exactly one enable call, for container only"
  assert_contains "$enable_line" "container.googleapis.com" \
    "the hybrid tier needs container.googleapis.com enabled"
  assert_not_contains "$enable_line" "compute.googleapis.com" \
    "the base APIs are already enabled and must not be re-enabled"
}

test_deploy_create_api_check_list_failure_tier_off_falls_back_to_unconditional_enable() {
  fresh_gcloud_state
  set_services_list_will_fail
  run_deploy_create "$(base_config_json "$HUB")"
  local enable_line
  enable_line="$(gcloud_log | grep 'services enable' | head -1)"
  assert_contains "$enable_line" "compute.googleapis.com" \
    "tier-off must fall back to enabling the whole base list when it can't list what's already enabled"
  assert_contains "$enable_line" "artifactregistry.googleapis.com" "the fallback enable call must include every base API"
  local create_line
  create_line="$(gcloud_log | grep 'compute instances create' | head -1)"
  assert_true "$([[ -n "$create_line" ]] && echo true || echo false)" \
    "the run must still reach VM creation despite the list failure, tier off"
}

test_deploy_create_api_check_list_failure_tier_on_fails_actionably() {
  fresh_gcloud_state
  set_services_list_will_fail
  local config_file
  config_file="$(mktemp)"
  printf '%s' "$(base_config_json "$HUB" "$(hybrid_config_fragment)")" > "$config_file"
  local log rc
  log="$(timeout 10 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
  rc=$?
  rm -f "$config_file"
  assert_true "$([[ "$rc" -ne 0 && "$rc" -ne 124 ]] && echo true || echo false)" \
    "the tier-on run must fail fast on a list failure, not hang until the create-mode timeout"
  assert_eq "0" "$(gcloud_log | grep -c 'compute instances create' || true)" \
    "nothing should be created when the API check can't tell what's missing, tier on"
  assert_contains "$log" "serviceusage" "the message should explain why an unknown API state is refused, not just fail silently"
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

# A zone being UNREACHABLE during the VM-gone check must not read as "the
# VM is gone": real gcloud downgrades that to a warning and exit 0 with
# the VM silently missing from the list, unless
# CLOUDSDK_COMPUTE_ALLOW_PARTIAL_ERROR=false is set on the call, which the
# stub enforces (see set_instances_list_zone_unreachable).
test_deploy_delete_vm_zone_unreachable_partial_list_keeps_rules() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  set_instance_delete_will_fail "$INSTANCE_NAME"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  set_instances_list_zone_unreachable
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" \
    "a partial AggregatedList result from an unreachable zone must not be read as the VM being gone"
  assert_eq "0" "$(gcloud_log | grep -c "firewall-rules delete scion-hub-${HUB}-nfs-" || true)" \
    "the hybrid rules must not be deleted when a zone is unreachable and the VM's fate can't be confirmed"
  assert_contains "$DEPLOY_LOG" "could not confirm" \
    "should explain that the VM's state is unknown, not assumed gone, for the unreachable-zone case too"
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
# function level (test_teardown_check_no_python_needed_when_nothing_
# matches in test_hybrid_tier.sh) and, here, at the deploy.sh level too,
# via the interactive (no --config) path: without --config, deploy.sh
# never touches $PYTHON before the teardown ownership check runs, so a
# missing interpreter is not fatal as long as there is nothing for the
# check to actually parse.
test_deploy_delete_interactive_no_python_no_rules_exits_zero() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  run_deploy_delete_interactive "/nonexistent/python3" "$(printf '%s\n' "$HUB" "us-central1" "y")"
  assert_eq "0" "$DEPLOY_RC" \
    "an interactive --delete with no config and no hybrid rules must not need python at all"
  assert_contains "$DEPLOY_LOG" "Deleted: ${INSTANCE_NAME}" "the base VM delete must still complete"
  assert_eq "1" "$(gcloud_log | grep -c "compute firewall-rules list" || true)" \
    "the teardown ownership check must actually run on the interactive path too, not be skipped"
}

# An unmarked same-name rule must still abort the teardown before any
# delete on the interactive (no --config) path, exactly as it does with
# --config -- the ownership check runs the same way regardless of how the
# run is driven.
test_deploy_delete_interactive_unmarked_exits_nonzero_before_any_delete() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_firewall_rule_desc_only "scion-hub-${HUB}-nfs-allow" "unrelated-rule-not-ours"
  run_deploy_delete_interactive "" "$(printf '%s\n' "$HUB" "us-central1" "y")"
  assert_eq "1" "$DEPLOY_RC" "an unmarked hybrid rule name match must fail the interactive teardown too"
  assert_eq "0" "$(gcloud_log | grep -c ' delete' || true)" \
    "no delete call of any kind should be logged before the abort"
}

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
# IMAGE_SOURCE/IMAGE_REGISTRY default to "build"/"" (the pre-existing
# fixture shape); tier-on create tests must override to "registry" plus
# a real-looking path, since the tier now refuses source=build.
base_config_json() {
  local hub="$1" extra="${2:-}" image_source="${3:-build}" image_registry="${4:-}"
  cat <<EOF
{
  "hub_name": "${hub}",
  "project_id": "demo-project",
  "region": "us-central1",
  "machine_size": "small",
  "disk_size_gb": 200,
  "chat_plugins": [],
  "container_images": {"source": "${image_source}", "registry": "${image_registry}", "force_rebuild": false},
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
# background and stops it once the VM-exists sentinel appears (see the
# file header) AND the log has gone quiet, rather than a single fixed
# grace period after the sentinel's first appearance: Part B added more
# gcloud calls (the internal-IP ensure functions, `add-tags`) between the
# sentinel's original firing point and the point these tests actually
# assert on, and a fixed 0.2s grace was measured to be too short for
# those under load. The wait for the sentinel itself uses a 30s budget --
# "large" on purpose, since the review that flagged this measured normal
# runs taking up to 9.1s under moderate load, some 90% of the previous
# 10s budget -- and fails the whole call loudly (returns 1, with the
# partial log annotated) if the sentinel is never reached at all, rather
# than silently letting the caller assert against a partial log as if
# nothing were wrong. Sets DEPLOY_RC (typically 143, from the TERM this
# sends once it's done reading the log -- these tests never assert on it)
# and DEPLOY_LOG. Same no-stray-set-e discipline as run_deploy_delete
# above.
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
  while [[ ! -f "$sentinel" && "$waited_ms" -lt 30000 ]]; do
    sleep 0.1
    waited_ms=$((waited_ms + 100))
  done
  if [[ ! -f "$sentinel" ]]; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null
    DEPLOY_RC=1
    DEPLOY_LOG="$(cat "$log_file")
FATAL: run_deploy_create: sentinel '${sentinel}' was never reached within 30000ms -- deploy.sh may be stuck, or this test's expectations no longer match its actual call sequence"
    rm -f "$config_file" "$log_file"
    return 1
  fi
  _wait_for_deploy_log_quiescence "$log_file"
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null
  DEPLOY_RC=$?
  DEPLOY_LOG="$(cat "$log_file")"
  rm -f "$config_file" "$log_file"
}

# _wait_for_deploy_log_quiescence LOG_FILE — polls LOG_FILE's line count
# until it stops growing for 3 consecutive polls (300ms), instead of a
# single fixed sleep after the sentinel first appears. deploy.sh's own
# stdout/stderr grows with every call it completes, so "stopped growing"
# is a direct signal that whatever ran right after the sentinel (the
# triggering call's own log line, add-tags, the internal-IP calls) has
# actually been written, not a guess at how long that should take.
_wait_for_deploy_log_quiescence() {
  local log_file="$1" last_lines=-1 cur_lines quiet_polls=0
  while [[ "$quiet_polls" -lt 3 ]]; do
    cur_lines="$(wc -l < "$log_file" 2>/dev/null || echo 0)"
    if [[ "$cur_lines" == "$last_lines" ]]; then
      quiet_polls=$((quiet_polls + 1))
    else
      quiet_polls=0
      last_lines="$cur_lines"
    fi
    sleep 0.1
  done
}

# run_deploy_create_to_settings_yaml CONFIG_JSON — like run_deploy_create,
# but opts the stub `gcloud` into actually succeeding on `compute ssh`/
# `compute scp` (GCLOUD_STUB_SSH_SUCCEEDS=true) and polls for a LATER
# sentinel: the settings.yaml dev-mode write in Phase 3, which is the
# earliest point past VM creation where deploy.sh has already made the
# hybrid-tier's NFS squash-identity and export SSH calls (if the tier is
# on) and computed HYBRID_SHARED_DIR_STORAGE_YAML, but before Phase 3b's
# image build/push work (which this stub does not simulate at all).
# Every "compute ssh"/"compute scp" call the stub actually receives is
# already in $GCLOUD_STUB_LOG (gcloud_log), one call per line including
# its full --command= text, so assertions on the rendered NFS/export
# scripts and the settings.yaml heredoc read that log directly rather
# than needing a separate one.
run_deploy_create_to_settings_yaml() {
  local config_json="$1" config_file
  config_file="$(mktemp)"
  printf '%s' "$config_json" > "$config_file"
  local sentinel="${GCLOUD_STUB_STATE_DIR}/settings-yaml-dev-mode-written"
  rm -f "$sentinel"
  local log_file
  log_file="$(mktemp)"
  GCLOUD_STUB_SSH_SUCCEEDS=true bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test \
    < /dev/null > "$log_file" 2>&1 &
  local pid=$!
  local waited_ms=0
  while [[ ! -f "$sentinel" && "$waited_ms" -lt 30000 ]]; do
    sleep 0.1
    waited_ms=$((waited_ms + 100))
  done
  if [[ -f "$sentinel" ]]; then
    _wait_for_deploy_log_quiescence "$log_file"
  fi
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null
  DEPLOY_RC=$?
  DEPLOY_LOG="$(cat "$log_file")"
  DEPLOY_REACHED_SETTINGS_YAML="$([[ -f "$sentinel" ]] && echo true || echo false)"
  rm -f "$config_file" "$log_file"
}

# run_deploy_create_to_cloud_run_deploy CONFIG_JSON — extends past the
# settings.yaml stopping point above, all the way into Phase 4's Cloud
# Run IAP proxy deploy: `gcloud run deploy` itself is unhandled by the
# stub (it falls through to the "unhandled invocation" catch-all, exit
# 1), so deploy.sh's own `set -e` ends the run immediately after that
# call is issued -- but the stub logs every invocation unconditionally
# before dispatching on it (see tests/lib/gcloud's header), so the call,
# including its full argv (label args and all), is already in
# $GCLOUD_STUB_LOG by the time this returns. That makes the log itself
# the sentinel: no separate touched file is needed, and nothing past
# this point (Artifact Registry, image build/push, the deploy call) is
# actually reachable any other way without a much larger stub.
run_deploy_create_to_cloud_run_deploy() {
  local config_json="$1" config_file
  config_file="$(mktemp)"
  printf '%s' "$config_json" > "$config_file"
  local log_file
  log_file="$(mktemp)"
  GCLOUD_STUB_SSH_SUCCEEDS=true bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test \
    < /dev/null > "$log_file" 2>&1 &
  local pid=$!
  local waited_ms=0
  while ! grep -q '^run deploy ' "$GCLOUD_STUB_LOG" 2>/dev/null && [[ "$waited_ms" -lt 30000 ]]; do
    sleep 0.1
    waited_ms=$((waited_ms + 100))
  done
  if ! grep -q '^run deploy ' "$GCLOUD_STUB_LOG" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null
    DEPLOY_RC=1
    DEPLOY_LOG="$(cat "$log_file")
FATAL: run_deploy_create_to_cloud_run_deploy: 'run deploy' was never logged within 30000ms"
    rm -f "$config_file" "$log_file"
    return 1
  fi
  _wait_for_deploy_log_quiescence "$log_file"
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

test_deploy_delete_hub_allow_deleted_before_deny() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  seed_firewall_rule_json "scion-hub-${HUB}-hub-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "8080" "" "10.52.0.0/14" "scion-hub-${HUB}-nfs" "900"
  run_deploy_delete "$(base_config_json "$HUB")"
  local log hub_allow_line deny_line
  log="$(gcloud_log)"
  hub_allow_line="$(line_number "firewall-rules delete scion-hub-${HUB}-hub-allow" "$log")"
  deny_line="$(line_number "firewall-rules delete scion-hub-${HUB}-nfs-deny" "$log")"
  assert_true "$([[ -n "$hub_allow_line" && -n "$deny_line" && "$hub_allow_line" -lt "$deny_line" ]] && echo true || echo false)" \
    "the hub-allow rule must be deleted before the deny rule, the same as the nfs-allow rule"
  assert_eq "1" "$(gcloud_log | grep -c "firewall-rules delete scion-hub-${HUB}-hub-allow" || true)" \
    "the hub-allow rule must actually be deleted during teardown, not just left in place"
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

# Internal IP reservation teardown wiring: the deploy.sh-level equivalent
# of the hybrid_internal_ip_teardown_check/_delete function-level tests
# in test_hybrid_tier.sh, covering the wiring those tests can't reach --
# whether deploy.sh actually calls the check before any delete, gates the
# reservation's delete on VM_GONE, and reports it in the final summary.
test_deploy_delete_internal_ip_unmarked_aborts_before_any_delete() {
  fresh_gcloud_state
  seed_address_unmarked "scion-hub-${HUB}-internal-ip" "10.128.0.42"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "an unmarked internal-IP reservation name match must fail the teardown run"
  assert_eq "0" "$(gcloud_log | grep -c ' delete' || true)" \
    "no delete call of any kind should be logged before the abort"
}

test_deploy_delete_internal_ip_deleted_when_vm_already_gone() {
  fresh_gcloud_state
  seed_address "scion-hub-${HUB}-internal-ip" "10.128.0.42" "scion-deployment=${HUB}"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "0" "$DEPLOY_RC" "teardown must succeed once the VM is confirmed gone"
  assert_eq "1" "$(gcloud_log | grep -c "addresses delete scion-hub-${HUB}-internal-ip" || true)" \
    "the internal IP reservation must be deleted once the VM is positively confirmed gone"
  assert_contains "$DEPLOY_LOG" "Deleted internal IP:       scion-hub-${HUB}-internal-ip" \
    "the summary must report the reservation as deleted"
}

test_deploy_delete_internal_ip_kept_when_vm_not_gone() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  set_instance_delete_will_fail "$INSTANCE_NAME"
  seed_address "scion-hub-${HUB}-internal-ip" "10.128.0.42" "scion-deployment=${HUB}"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "a VM delete failure must make teardown exit non-zero"
  assert_eq "0" "$(gcloud_log | grep -c "addresses delete scion-hub-${HUB}-internal-ip" || true)" \
    "the internal IP reservation must not be deleted when the VM delete failed"
  assert_contains "$DEPLOY_LOG" "isn't confirmed yet" \
    "deploy.sh should report why the reservation was kept"
  assert_contains "$DEPLOY_LOG" "SKIPPED internal IP:       scion-hub-${HUB}-internal-ip (VM not confirmed gone)" \
    "the summary must report the reservation as SKIPPED, distinct from an attempted-and-failed delete"
}

test_deploy_delete_internal_ip_kept_when_firewall_rule_delete_failed() {
  fresh_gcloud_state
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  set_firewall_delete_will_fail "scion-hub-${HUB}-nfs-allow"
  seed_address "scion-hub-${HUB}-internal-ip" "10.128.0.42" "scion-deployment=${HUB}"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "a hybrid firewall rule delete failure must make teardown exit non-zero"
  assert_eq "0" "$(gcloud_log | grep -c "addresses delete scion-hub-${HUB}-internal-ip" || true)" \
    "the internal IP reservation must not be deleted when a hybrid firewall rule failed to delete"
  assert_contains "$DEPLOY_LOG" "SKIPPED internal IP:       scion-hub-${HUB}-internal-ip (a hybrid-tier firewall rule failed to delete)" \
    "the summary must report the reservation as SKIPPED, with the reason it was never attempted"
}

test_deploy_delete_internal_ip_order_after_vm() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_address "scion-hub-${HUB}-internal-ip" "10.128.0.42" "scion-deployment=${HUB}"
  run_deploy_delete "$(base_config_json "$HUB")"
  local log vm_line addr_line
  log="$(gcloud_log)"
  vm_line="$(line_number "compute instances delete ${INSTANCE_NAME}" "$log")"
  addr_line="$(line_number "addresses delete scion-hub-${HUB}-internal-ip" "$log")"
  assert_true "$([[ -n "$vm_line" && -n "$addr_line" && "$vm_line" -lt "$addr_line" ]] && echo true || echo false)" \
    "the VM delete must precede the internal IP reservation delete"
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
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
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
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
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
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  local log discover_line first_create_line
  log="$(gcloud_log)"
  discover_line="$(line_number 'container clusters describe' "$log")"
  first_create_line="$(echo "$log" | grep -n ' create ' | head -1 | cut -d: -f1)"
  assert_true "$([[ -n "$discover_line" && -n "$first_create_line" && "$discover_line" -lt "$first_create_line" ]] && echo true || echo false)" \
    "cluster discovery must happen before the first create call of any kind"
}

test_deploy_create_removes_temp_kubeconfig_on_exit() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default" "mig-a"
  seed_mig "mig-a" "template-a"
  seed_template "template-a" "gke-mycluster-abc123-node"
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  local kubeconfig_path
  kubeconfig_path="$(kubectl_log | head -1 | sed -n 's/^KUBECONFIG=\([^ ]*\) .*/\1/p')"
  assert_true "$([[ -n "$kubeconfig_path" ]] && echo true || echo false)" \
    "the k8s preflight must have actually used a task-private KUBECONFIG, or this test can't check it was cleaned up"
  assert_false "$([[ -n "$kubeconfig_path" && -f "$kubeconfig_path" ]] && echo true)" \
    "the temporary kubeconfig deploy.sh's own EXIT trap creates must be removed once deploy.sh exits, not left behind in \$TMPDIR"
}

# =====================================================================
# Base markers on create: additive, create-only, never checked or
# adopted on. This is the first deliberate tier-off behavior change (the
# second is the API check, above): markers apply on every fresh deploy
# regardless of whether the hybrid tier is on, and never on redeploys of
# an existing resource. Base teardown is unaffected either way.
# =====================================================================

test_deploy_create_base_markers_present_on_fresh_create() {
  fresh_gcloud_state
  run_deploy_create "$(base_config_json "$HUB")"
  local log
  log="$(gcloud_log)"
  local instance_line sa_line router_line fw_line
  instance_line="$(echo "$log" | grep 'compute instances create' | head -1)"
  sa_line="$(echo "$log" | grep 'service-accounts create' | head -1)"
  router_line="$(echo "$log" | grep 'compute routers create' | head -1)"
  fw_line="$(echo "$log" | grep 'firewall-rules create.*allow-iap-ssh' | head -1)"
  assert_contains "$instance_line" "--labels=scion-deployment=${HUB}" "the VM must carry the marker label on create"
  assert_contains "$sa_line" "--description=scion-deployment=${HUB}" "the service account must carry the marker description on create"
  assert_contains "$router_line" "--description=scion-deployment=${HUB}" "the router must carry the marker description on create"
  assert_contains "$fw_line" "scion-deployment=${HUB}" \
    "the IAP SSH firewall rule's description must include the marker token alongside its existing text"
  assert_contains "$fw_line" "Allow SSH via IAP tunneling" \
    "the IAP SSH firewall rule's existing description text must be preserved, not replaced by the marker"
}

test_deploy_create_base_markers_absent_when_tier_off_no_tags() {
  # Same fixture as test_deploy_create_tier_off_no_tags_no_container_calls,
  # but checking the marker is present regardless -- this is the
  # deliberate tier-off exception, not something gated on the tier.
  fresh_gcloud_state
  run_deploy_create "$(base_config_json "$HUB")"
  assert_eq "1" "$(gcloud_log | grep -c -- "--labels=scion-deployment=${HUB}" || true)" \
    "the base marker must be present even with the hybrid tier off"
}

# Markers must never appear on an adopt/existing-resource path: this
# reuses the existing-VM fixture (no hybrid tier involved) to prove the
# instance marker is create-only.
test_deploy_create_base_markers_absent_on_existing_vm() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  run_deploy_create "$(base_config_json "$HUB")"
  assert_eq "0" "$(gcloud_log | grep -c 'compute instances create' || true)" \
    "an existing VM must not be recreated (and so never gets the create-only marker call)"
}

test_deploy_create_base_markers_absent_on_existing_router_and_sa() {
  fresh_gcloud_state
  set_router_exists
  set_service_account_exists
  run_deploy_create "$(base_config_json "$HUB")"
  assert_eq "0" "$(gcloud_log | grep -c 'compute routers create' || true)" \
    "an existing router must not be recreated (and so never gets the create-only marker call)"
  assert_eq "0" "$(gcloud_log | grep -c 'service-accounts create' || true)" \
    "an existing service account must not be recreated (and so never gets the create-only marker call)"
  assert_eq "0" "$(gcloud_log | grep -c 'service-accounts update' || true)" \
    "adopting an existing service account must not re-describe/update it -- create-only, like every other base marker"
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
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
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
  printf '%s' "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")" > "$config_file"
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
# GKE nodes can't pull from the VM's local Docker store: tier-on refuses
# container_images.source=build (and any localhost/ registry) before any
# create. The refusal check itself doesn't distinguish config-mode from
# interactive-mode input -- both set the same IMAGE_SOURCE/IMAGE_REGISTRY
# variables the check reads -- so this is covered once, in config mode,
# where it can run fast and deterministically; the interactive image
# prompt (Question 6) feeds the identical two variables.
# =====================================================================

test_deploy_create_tier_on_source_build_refused() {
  fresh_gcloud_state
  local config_file
  config_file="$(mktemp)"
  printf '%s' "$(base_config_json "$HUB" "$(hybrid_config_fragment)")" > "$config_file"
  local log rc
  log="$(timeout 10 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
  rc=$?
  rm -f "$config_file"
  assert_true "$([[ "$rc" -ne 0 && "$rc" -ne 124 ]] && echo true || echo false)" \
    "tier-on with source=build must be refused fast, not hang until the create-mode timeout"
  assert_eq "0" "$(gcloud_log | grep -c 'compute instances create' || true)" "nothing should be created"
  assert_contains "$log" "cannot pull images" "the message should explain why"
}

test_deploy_create_tier_on_loopback_registry_refused_with_correct_message() {
  fresh_gcloud_state
  local config_file
  config_file="$(mktemp)"
  printf '%s' "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "localhost:5000/scion")" > "$config_file"
  local log rc
  log="$(timeout 10 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
  rc=$?
  rm -f "$config_file"
  assert_true "$([[ "$rc" -ne 0 && "$rc" -ne 124 ]] && echo true || echo false)" \
    "tier-on with a loopback registry must be refused fast, not hang until the create-mode timeout"
  assert_eq "0" "$(gcloud_log | grep -c 'compute instances create' || true)" "nothing should be created"
  assert_contains "$log" "names this VM itself" "the message must explain the loopback problem, not claim source is 'build'"
  assert_not_contains "$log" "source is 'build'" \
    "the loopback-registry branch must not use the source=build error message (container_images.source is 'registry' here)"
}

test_deploy_create_tier_off_source_build_unaffected() {
  fresh_gcloud_state
  run_deploy_create "$(base_config_json "$HUB")"
  assert_true "$([[ -f "${GCLOUD_STUB_STATE_DIR}/vm-create-happened" ]] && echo true || echo false)" \
    "tier-off must still reach VM creation with the default source=build config, unaffected by this refusal"
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

# =====================================================================
# Kubernetes objects (marker-scoped teardown wiring): tier-off inertness, the
# cluster-gone/cluster-error distinction, and the abort-before-any-
# delete rule for an unmarked PV/PVC. Create-mode wiring for these
# objects is exercised only at the function level
# (test_k8s_ensure_* in test_hybrid_tier.sh): hybrid_k8s_ensure_objects
# runs in Phase 4, well past the point create-mode wiring tests
# intentionally stop (the VM-exists sentinel), and further downstream
# than even the NFS/squash step -- deep into image-build territory the
# stub doesn't simulate at all.
# =====================================================================

K8S_NS_D="scion-hub-${HUB}"
K8S_PVC_D="scion-hub-${HUB}-shared"
K8S_PV_D="scion-hub-${HUB}-shared"

# hybrid_k8s_preflight itself (unlike hybrid_k8s_ensure_objects) runs in
# Phase 2, right after discovery and well before the VM, NFS, or firewall
# rules exist -- so its own create-side placement, unlike the rest of the
# k8s object lifecycle, IS reachable from a deploy-mode wiring test: an
# unmarked PV must abort the whole run before any of those Phase 3+
# resources are ever created.
test_deploy_create_k8s_preflight_unmarked_pv_aborts_before_any_create() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default" "mig-a"
  seed_mig "mig-a" "template-a"
  seed_template "template-a" "gke-mycluster-abc123-node"
  seed_k8s_pv_unmarked "$K8S_PV_D"
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_true "$([[ "$DEPLOY_RC" -ne 0 ]] && echo true || echo false)" \
    "an unmarked PV must fail the whole run, not just the eventual k8s object creation"
  assert_eq "0" "$(gcloud_log | grep -c 'compute instances create' || true)" \
    "the VM must never be created when the k8s preflight refuses"
  assert_eq "0" "$(gcloud_log | grep -c 'compute addresses create' || true)" \
    "the internal IP must never be reserved when the k8s preflight refuses"
  assert_eq "0" "$(gcloud_log | grep -c 'firewall-rules create' || true)" \
    "no hybrid firewall rule may be created when the k8s preflight refuses"
}

test_deploy_delete_k8s_tier_off_zero_kubectl_calls() {
  fresh_gcloud_state
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "0" "$DEPLOY_RC" "tier-off teardown must succeed as before"
  assert_eq "0" "$(kubectl_log | grep -c . || true)" "tier-off teardown must make zero kubectl calls"
}

test_deploy_delete_k8s_cluster_not_found_continues() {
  fresh_gcloud_state
  run_deploy_delete "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  assert_eq "0" "$DEPLOY_RC" "a genuinely-gone cluster must not fail the teardown"
  assert_eq "0" "$(kubectl_log | grep -c . || true)" \
    "a NOT_FOUND cluster means its k8s objects went with it -- no kubectl call should even be attempted"
  assert_contains "$DEPLOY_LOG" "went with it" "should explain why nothing was checked"
}

test_deploy_delete_k8s_cluster_describe_error_aborts_before_any_delete() {
  fresh_gcloud_state
  set_cluster_describe_error "mycluster"
  run_deploy_delete "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  assert_eq "1" "$DEPLOY_RC" "an unconfirmable cluster state must fail the teardown"
  assert_eq "0" "$(gcloud_log | grep -c ' delete' || true)" "no delete call of any kind should happen before the abort"
}

test_deploy_delete_k8s_cluster_permission_masked_not_read_as_gone() {
  fresh_gcloud_state
  set_cluster_describe_permission_masked "mycluster"
  run_deploy_delete "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  assert_eq "1" "$DEPLOY_RC" \
    "a permission-denied cluster-describe error that also says 'not found' must not be read as the cluster being gone"
  assert_eq "0" "$(gcloud_log | grep -c ' delete' || true)" "no delete call of any kind should happen before the abort"
}

test_deploy_delete_k8s_all_marked_deletes_all_three() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default" "mig-a"
  seed_k8s_pvc "$K8S_PVC_D" "$K8S_NS_D" "$HUB" "$K8S_PV_D"
  seed_k8s_pv "$K8S_PV_D" "$HUB" "10.128.0.5" "/srv/scion-shared" "$K8S_NS_D" "$K8S_PVC_D"
  seed_k8s_namespace "$K8S_NS_D" "$HUB"
  run_deploy_delete "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  assert_eq "0" "$DEPLOY_RC" "an all-marked k8s teardown must succeed"
  assert_eq "1" "$(kubectl_log | grep -c 'delete pvc' || true)" "the PVC must be deleted"
  assert_eq "1" "$(kubectl_log | grep -c 'delete pv ' || true)" "the PV must be deleted"
  assert_eq "1" "$(kubectl_log | grep -c 'delete namespace' || true)" "the namespace must be deleted"
}

test_deploy_delete_k8s_delete_failure_stops_all_downstream_deletes() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_cluster "mycluster" "default" "mig-a"
  seed_k8s_pvc "$K8S_PVC_D" "$K8S_NS_D" "$HUB" "$K8S_PV_D"
  seed_k8s_pv "$K8S_PV_D" "$HUB" "10.128.0.5" "/srv/scion-shared" "$K8S_NS_D" "$K8S_PVC_D"
  seed_k8s_namespace "$K8S_NS_D" "$HUB"
  set_k8s_delete_will_fail "pvc" "${K8S_NS_D}__${K8S_PVC_D}"
  run_deploy_delete "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  assert_eq "1" "$DEPLOY_RC" "a kubectl delete failure must fail the whole teardown"
  assert_eq "0" "$(kubectl_log | grep -c 'delete pv ' || true)" \
    "the PV must not be deleted once the PVC delete ahead of it failed"
  assert_eq "0" "$(kubectl_log | grep -c 'delete namespace' || true)" \
    "the namespace must not be deleted once an earlier k8s delete failed"
  assert_eq "0" "$(gcloud_log | grep -c 'instances delete' || true)" \
    "the VM must not be deleted when a hybrid-tier Kubernetes object failed to delete"
  assert_eq "0" "$(gcloud_log | grep -c 'run services delete' || true)" \
    "Cloud Run must not be deleted when a hybrid-tier Kubernetes object failed to delete"
  assert_eq "0" "$(gcloud_log | grep -c 'firewall-rules delete' || true)" \
    "no firewall rule may be deleted when a hybrid-tier Kubernetes object failed to delete"
}

test_deploy_delete_k8s_deletes_precede_vm_delete() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_cluster "mycluster" "default" "mig-a"
  seed_k8s_pvc "$K8S_PVC_D" "$K8S_NS_D" "$HUB" "$K8S_PV_D"
  seed_k8s_pv "$K8S_PV_D" "$HUB" "10.128.0.5" "/srv/scion-shared" "$K8S_NS_D" "$K8S_PVC_D"
  seed_k8s_namespace "$K8S_NS_D" "$HUB"
  run_deploy_delete "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  assert_eq "0" "$DEPLOY_RC" "an all-marked k8s teardown followed by the VM must succeed"
  local k8s_line vm_line
  k8s_line="$(line_number "Deleting hybrid-tier Kubernetes objects" "$DEPLOY_LOG")"
  vm_line="$(line_number "Deleting GCE VM" "$DEPLOY_LOG")"
  assert_true "$([[ -n "$k8s_line" && -n "$vm_line" && "$k8s_line" -lt "$vm_line" ]] && echo true || echo false)" \
    "the hybrid-tier Kubernetes deletes must run, and be logged, before the VM delete starts"
}

test_deploy_delete_k8s_unmarked_pv_aborts_before_any_delete() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default" "mig-a"
  seed_k8s_pv_unmarked "$K8S_PV_D"
  run_deploy_delete "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  assert_eq "1" "$DEPLOY_RC" "an unmarked PV must abort the whole teardown"
  assert_eq "0" "$(gcloud_log | grep -c ' delete' || true)" "no delete call of any kind should happen before the abort"
  assert_eq "0" "$(kubectl_log | grep -c 'delete' || true)" "no kubectl delete call should happen before the abort either"
}

# =====================================================================
# Past VM create, into Phase 3: extends coverage past the point every
# other create-mode wiring test above intentionally stops (the VM-exists
# sentinel), using GCLOUD_STUB_SSH_SUCCEEDS so the stub actually answers
# `compute ssh`/`compute scp` instead of failing immediately. Stops at
# the settings.yaml dev-mode write -- the earliest point where the
# hybrid tier's NFS squash-identity/export SSH calls (if the tier is on)
# and the HYBRID_SHARED_DIR_STORAGE_YAML computation have both already
# happened, but before Phase 3b's image build/push work, which this
# stub does not simulate.
# =====================================================================

test_deploy_create_tier_off_reaches_settings_yaml_with_no_hybrid_ssh_calls() {
  fresh_gcloud_state
  run_deploy_create_to_settings_yaml "$(base_config_json "$HUB")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-off create must reach the settings.yaml write"
  local log
  log="$(gcloud_log)"
  assert_eq "0" "$(echo "$log" | grep -c 'useradd -r -M -N -g scion' || true)" \
    "tier off must never run the NFS squash-identity script"
  assert_eq "0" "$(echo "$log" | grep -c 'mkfs.ext4' || true)" \
    "tier off must never run the NFS export script"
  assert_not_contains "$log" "shared_dir_storage" \
    "the tier-off settings.yaml write must not carry the shared_dir_storage block"
  assert_eq "0" "$(kubectl_log | grep -c . || true)" "tier off must make zero kubectl calls"
}

test_deploy_create_tier_on_reaches_settings_yaml_with_correct_nfs_and_block() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default" "mig-a"
  seed_mig "mig-a" "template-a"
  seed_template "template-a" "gke-mycluster-abc123-node"
  run_deploy_create_to_settings_yaml \
    "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-on create must reach the settings.yaml write"
  local log
  log="$(gcloud_log)"

  assert_contains "$log" "useradd -r -M -N -g scion -s /usr/sbin/nologin scion-nfs" \
    "the squash-identity script must actually be sent over SSH when the tier is on"

  # The default node-subnet CIDR the gcloud stub serves when a test
  # doesn't override it via seed_subnet -- see tests/lib/harness.sh.
  assert_contains "$log" "10.128.0.0/20" \
    "the export line's CIDR must be the discovered node subnet, wired through correctly from hybrid_discover"
  assert_contains "$log" "anonuid=997,anongid=1001" \
    "the export line's anonuid/anongid must be exactly what the squash script returned, wired through unchanged"

  assert_contains "$log" "shared_dir_storage:" \
    "the tier-on settings.yaml write must carry the shared_dir_storage block"
  assert_contains "$log" 'mount_root: "/srv"' \
    "mount_root must be the export root's parent, not the full export root (the pre-fix bug)"
  assert_contains "$log" 'id: "scion-shared"' \
    "the share id must be the export root's base name"
  assert_contains "$log" 'pv_name: "scion-hub-demohub-shared"' \
    "pv_name must carry the resolved PVC name"

  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/namespace/scion-hub-${HUB}.json" ]] && echo true || echo false)" \
    "hybrid_k8s_preflight must have created the namespace in Phase 2, well before this Phase 3 stopping point"

  # Part B: the reserved internal IP -- not the VM's ephemeral describe
  # IP -- must be what the VM is actually created with, and the same
  # value the settings.yaml write and the PV would use, so GKE agents,
  # the Docker broker, and the reserved address all agree on one number.
  # The stub's `addresses create` defaults to 10.128.0.9, distinct from
  # `instances describe`'s own default of 10.128.0.5, so the two are
  # never accidentally indistinguishable in this assertion.
  local create_line
  create_line="$(echo "$log" | grep 'compute instances create' | head -1)"
  assert_contains "$create_line" "--private-network-ip=10.128.0.9" \
    "the new VM must be created pinned to the reserved internal IP, not left to get an ephemeral one"
  assert_contains "$log" 'server: "10.128.0.9"' \
    "the settings.yaml shared_dir_storage server field must be the reserved internal IP"

  # The hub URL guard's post-create half re-describes the reservation and
  # the hub-allow rule; both calls must actually have happened by this
  # point, proving deploy.sh calls the guard at all (nothing else in this
  # flow describes the internal-IP address after it's created).
  assert_contains "$log" "compute addresses describe scion-hub-${HUB}-internal-ip" \
    "the hub URL guard must re-describe the internal IP reservation after create"
  assert_contains "$log" "compute firewall-rules describe scion-hub-${HUB}-hub-allow" \
    "the hub URL guard must re-describe the hub-allow rule after create"
}

test_deploy_create_cloud_run_deploy_carries_marker_label_on_fresh_create() {
  fresh_gcloud_state
  run_deploy_create_to_cloud_run_deploy "$(base_config_json "$HUB" "" "registry" "us-docker.pkg.dev/demo-project/scion")"
  local run_deploy_line
  run_deploy_line="$(gcloud_log | grep '^run deploy ' | head -1)"
  assert_contains "$run_deploy_line" "--labels=scion-deployment=${HUB}" \
    "the Cloud Run IAP proxy must carry the marker label on its first deploy -- PROXY_SERVICE_LABEL_ARGS must actually reach the run deploy call"
}

test_deploy_create_cloud_run_deploy_no_label_when_service_already_exists() {
  fresh_gcloud_state
  seed_run_service_exists "${INSTANCE_NAME}-iap-proxy"
  run_deploy_create_to_cloud_run_deploy "$(base_config_json "$HUB" "" "registry" "us-docker.pkg.dev/demo-project/scion")"
  local run_deploy_line
  run_deploy_line="$(gcloud_log | grep '^run deploy ' | head -1)"
  assert_not_contains "$run_deploy_line" "--labels=" \
    "redeploying an existing Cloud Run service must not add the create-only marker label"
}

test_deploy_create_tier_on_existing_vm_promotes_current_ip() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_cluster "mycluster" "default" "mig-a"
  seed_mig "mig-a" "template-a"
  seed_template "template-a" "gke-mycluster-abc123-node"
  run_deploy_create "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  local log
  log="$(gcloud_log)"
  assert_contains "$log" "addresses create scion-hub-${HUB}-internal-ip" \
    "an existing VM's current IP must be promoted to a static reservation"
  assert_contains "$log" "--addresses=10.128.0.5" \
    "must promote the VM's actual current IP (the stub's instances-describe default), not a fresh/different one"
}

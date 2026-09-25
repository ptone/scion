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
# create-mode tests only need the log through the end of deploy.sh's
# Phase 2 (VM create/tag plus, when the tier is on, the hybrid-tier
# internal-IP calls and their guard) -- not a full simulated
# deploy, which would need to get past an SSH-readiness retry loop with
# real sleeps between attempts. So run_deploy_create runs deploy.sh in
# the background and polls for the `phase2-complete` sentinel the stub's
# `compute ssh` handler touches on its first invocation (the first thing
# Phase 3 does), then kills it -- typically well under a second, instead
# of waiting out a fixed timeout. The 30s budget is only a safety net for
# a test whose assertions turn out to need something the sentinel doesn't
# cover.

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

# _start_deploy_bg CONFIG_FILE LOG_FILE [ENV_ASSIGNMENT...] -- starts
# deploy.sh (create mode) in the background, in its own process group
# (job control on for this call only), and sets the global _DEPLOY_BG_PID
# to its PID. `set -m` makes bash give a backgrounded job its own process
# group, with the PGID equal to the job's own PID -- that's what lets
# _stop_deploy_bg below signal every process deploy.sh has spawned, not
# just deploy.sh itself. _DEPLOY_BG_PID is a global, not an echoed return
# value: a caller doing `pid="$(_start_deploy_bg ...)"` would run this
# whole function in a command-substitution subshell, and a subshell that
# enabled job control sends SIGHUP to its own background jobs the moment
# it exits -- which happens immediately here, right after backgrounding,
# killing deploy.sh before it ever gets going. A plain function call has
# no such subshell, so callers must call this directly, then read
# _DEPLOY_BG_PID afterward, not wrap the call in `$(...)`.
#
# Also snapshots $TMPDIR's own top-level entries to LOG_FILE.before,
# before deploy.sh ever runs, so _stop_deploy_bg below can tell what it
# left behind directly in $TMPDIR -- deploy.sh does its own mktemp-based
# error-file bookkeeping internally (SQUASH_SSH_ERR and friends), each a
# bare file alongside (not inside) $GCLOUD_STUB_STATE_DIR, with cleanup
# later in its own linear flow; killed mid-flight, whichever of those it
# was between creating and removing at that moment stays on disk. Scoped
# to top-level entries only (not a recursive find): the stub's own state
# directories legitimately gain new files while deploy.sh runs (sentinels,
# JSON fixtures) that the calling test still needs to read after this
# returns, so descending into them and diffing would delete state the
# test hasn't finished using yet, not just orphaned garbage. Same
# snapshot-diff technique run_expect_fail uses for the analogous
# command-substitution-subshell case; written to a file rather than a
# variable for the same subshell-visibility reason _DEPLOY_BG_PID is a
# global.
_start_deploy_bg() {
  local config_file="$1" log_file="$2"
  shift 2
  PATH="$HARNESS_SAFE_PATH" find "${TMPDIR:-/tmp}" -mindepth 1 -maxdepth 1 2>/dev/null \
    | PATH="$HARNESS_SAFE_PATH" sort > "${log_file}.before"
  set -m
  env "$@" bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test \
    < /dev/null > "$log_file" 2>&1 &
  _DEPLOY_BG_PID=$!
  set +m
}

# _stop_deploy_bg PID LOG_FILE -- sends SIGTERM to PID's entire process
# group (not just PID itself), so an in-flight child the stub gcloud
# spawns dies alongside deploy.sh instead of continuing to run -- and
# possibly write into this test's just-removed state directory -- after
# this function returns. Reaps PID via `wait` (bash can only wait on its
# own direct children) and sets DEPLOY_RC to its exit status, then polls
# `kill -0` on the group until every member is actually gone, up to a
# bounded safety timeout, since SIGTERM delivery and process teardown
# aren't instantaneous even once sent. Finally sweeps $TMPDIR for
# anything new since _start_deploy_bg's snapshot (LOG_FILE.before) and
# removes it -- deploy.sh's own temp files, not just a stub subprocess's.
_stop_deploy_bg() {
  local pid="$1" log_file="$2"
  kill -TERM -- "-${pid}" 2>/dev/null || true
  wait "$pid" 2>/dev/null
  DEPLOY_RC=$?
  local waited_ms=0
  while kill -0 -- "-${pid}" 2>/dev/null && [[ "$waited_ms" -lt 5000 ]]; do
    sleep 0.05
    waited_ms=$((waited_ms + 50))
  done
  local before_tmp after_tmp
  before_tmp="$(cat "${log_file}.before" 2>/dev/null || true)"
  after_tmp="$(PATH="$HARNESS_SAFE_PATH" find "${TMPDIR:-/tmp}" -mindepth 1 -maxdepth 1 2>/dev/null | PATH="$HARNESS_SAFE_PATH" sort)"
  rm -f "${log_file}.before"
  if [[ "$after_tmp" != "$before_tmp" ]]; then
    PATH="$HARNESS_SAFE_PATH" comm -13 <(printf '%s\n' "$before_tmp") <(printf '%s\n' "$after_tmp") | while IFS= read -r _new_tmp_entry; do
      [[ -n "$_new_tmp_entry" ]] && rm -rf "$_new_tmp_entry"
    done
  fi
}

# run_deploy_create CONFIG_JSON — runs `deploy.sh` (create mode) in the
# background and stops it once the `phase2-complete` sentinel appears
# (touched by the stub's `compute ssh` handler on its first invocation --
# see tests/lib/gcloud): by that point deploy.sh's entire Phase 2 has run
# to completion, including VM create/tag, the hybrid-tier internal-IP
# calls and their guard, in every tier/VM-state combination, since
# deploy.sh runs strictly sequentially. The wait uses a 30s budget --
# large enough to absorb realistic per-call latency on a loaded host --
# and fails the whole call loudly (returns 1, with the partial log
# annotated) if the sentinel is never reached at all, rather than
# silently letting the caller assert against a partial log as if nothing
# were wrong. A test that needs to know one specific, later call has
# completed (past Phase 2, e.g. into the settings.yaml writes) should use
# run_deploy_create_wait_for below instead, which waits on a
# call-specific sentinel. Sets DEPLOY_RC (typically 143, from the TERM
# this sends once it's done reading the log -- these tests never assert
# on it) and DEPLOY_LOG. Same no-stray-set-e discipline as
# run_deploy_delete above.
run_deploy_create() {
  local config_json="$1" config_file
  config_file="$(mktemp)"
  printf '%s' "$config_json" > "$config_file"
  local sentinel="${GCLOUD_STUB_STATE_DIR}/phase2-complete"
  rm -f "$sentinel"
  local log_file
  log_file="$(mktemp)"
  local pid
  _start_deploy_bg "$config_file" "$log_file"
  pid="$_DEPLOY_BG_PID"
  local waited_ms=0
  while [[ ! -f "$sentinel" && "$waited_ms" -lt 30000 ]]; do
    sleep 0.1
    waited_ms=$((waited_ms + 100))
  done
  if [[ ! -f "$sentinel" ]]; then
    _stop_deploy_bg "$pid" "$log_file"
    DEPLOY_RC=1
    DEPLOY_LOG="$(cat "$log_file")
FATAL: run_deploy_create: sentinel '${sentinel}' was never reached within 30000ms -- deploy.sh may be stuck, or this test's expectations no longer match its actual call sequence"
    rm -f "$config_file" "$log_file"
    return 1
  fi
  _stop_deploy_bg "$pid" "$log_file"
  DEPLOY_LOG="$(cat "$log_file")"
  rm -f "$config_file" "$log_file"
}

# run_deploy_create_wait_for CONFIG_JSON SENTINEL_FILENAME — like
# run_deploy_create, but for the handful of tests that need to know one
# specific, later gcloud call has actually completed (not just that
# deploy.sh has reached the general create-or-tag branch point the
# default sentinel above fires at): waits only on SENTINEL_FILENAME
# (relative to $GCLOUD_STUB_STATE_DIR), with the same 30s fail-loud
# budget, and does NOT layer the quiescence poll on top -- the whole
# point of a call-specific sentinel is that nothing after it needs
# guessing at. Existing tests that use the default, earlier-firing
# sentinel plus quiescence are unaffected; this is an alternate entry
# point, not a replacement.
run_deploy_create_wait_for() {
  local config_json="$1" sentinel_filename="$2" config_file
  config_file="$(mktemp)"
  printf '%s' "$config_json" > "$config_file"
  local sentinel="${GCLOUD_STUB_STATE_DIR}/${sentinel_filename}"
  rm -f "$sentinel"
  local log_file
  log_file="$(mktemp)"
  local pid
  _start_deploy_bg "$config_file" "$log_file"
  pid="$_DEPLOY_BG_PID"
  local waited_ms=0
  while [[ ! -f "$sentinel" && "$waited_ms" -lt 30000 ]]; do
    sleep 0.1
    waited_ms=$((waited_ms + 100))
  done
  if [[ ! -f "$sentinel" ]]; then
    _stop_deploy_bg "$pid" "$log_file"
    DEPLOY_RC=1
    DEPLOY_LOG="$(cat "$log_file")
FATAL: run_deploy_create_wait_for: sentinel '${sentinel}' was never reached within 30000ms -- deploy.sh may be stuck, or this test's expectations no longer match its actual call sequence"
    rm -f "$config_file" "$log_file"
    return 1
  fi
  _stop_deploy_bg "$pid" "$log_file"
  DEPLOY_LOG="$(cat "$log_file")"
  rm -f "$config_file" "$log_file"
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
  _run_deploy_create_to_settings_yaml_impl "$1" "dev"
}

# run_deploy_create_to_proxy_settings_yaml CONFIG_JSON — like
# run_deploy_create_to_settings_yaml, but runs all the way through
# Cloud Run deploy and IAP enablement to the Phase-5 proxy-mode
# settings.yaml (re)write instead of stopping at the earlier dev-mode
# one. IAP_ENFORCEMENT_WAIT_SECS is overridden to 0 so this doesn't
# block on deploy.sh's real 60s wait for IAP enforcement to activate.
run_deploy_create_to_proxy_settings_yaml() {
  _run_deploy_create_to_settings_yaml_impl "$1" "proxy"
}

# _run_deploy_create_to_settings_yaml_impl CONFIG_JSON MODE ("dev" or
# "proxy") — shared implementation. Sets DEPLOY_RC, DEPLOY_LOG, and
# DEPLOY_REACHED_SETTINGS_YAML.
_run_deploy_create_to_settings_yaml_impl() {
  local config_json="$1" mode="$2" config_file
  config_file="$(mktemp)"
  printf '%s' "$config_json" > "$config_file"
  local sentinel
  if [[ "$mode" == "proxy" ]]; then
    sentinel="${GCLOUD_STUB_STATE_DIR}/settings-yaml-proxy-mode-written"
  else
    sentinel="${GCLOUD_STUB_STATE_DIR}/settings-yaml-dev-mode-written"
  fi
  rm -f "$sentinel"
  local log_file
  log_file="$(mktemp)"
  local pid
  _start_deploy_bg "$config_file" "$log_file" \
    GCLOUD_STUB_SSH_SUCCEEDS=true IAP_ENFORCEMENT_WAIT_SECS=0
  pid="$_DEPLOY_BG_PID"
  local waited_ms=0
  while [[ ! -f "$sentinel" && "$waited_ms" -lt 30000 ]]; do
    sleep 0.1
    waited_ms=$((waited_ms + 100))
  done
  # No quiescence poll: the settings.yaml sentinel fires only once the
  # full --command= string (the whole heredoc) has already been logged
  # by the SSH dispatcher, so there's nothing later this call needs to
  # wait for -- stopping here also keeps a dev-mode run from ever
  # reaching the later proxy-mode write, which is the only thing that
  # made the two writes distinguishable by log content rather than by
  # which one this call actually asked for.
  _stop_deploy_bg "$pid" "$log_file"
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
  local pid
  _start_deploy_bg "$config_file" "$log_file" GCLOUD_STUB_SSH_SUCCEEDS=true
  pid="$_DEPLOY_BG_PID"
  local waited_ms=0
  while ! grep -q '^run deploy ' "$GCLOUD_STUB_LOG" 2>/dev/null && [[ "$waited_ms" -lt 30000 ]]; do
    sleep 0.1
    waited_ms=$((waited_ms + 100))
  done
  if ! grep -q '^run deploy ' "$GCLOUD_STUB_LOG" 2>/dev/null; then
    _stop_deploy_bg "$pid" "$log_file"
    DEPLOY_RC=1
    DEPLOY_LOG="$(cat "$log_file")
FATAL: run_deploy_create_to_cloud_run_deploy: 'run deploy' was never logged within 30000ms"
    rm -f "$config_file" "$log_file"
    return 1
  fi
  # No quiescence poll: deploy.sh's own `set -e` ends the run immediately
  # once the unhandled `run deploy` call fails, so nothing further gets
  # logged after this point to wait for.
  _stop_deploy_bg "$pid" "$log_file"
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

test_deploy_delete_hub_deny_deleted_before_nfs_deny() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  seed_firewall_rule_json "scion-hub-${HUB}-hub-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "all" "" "" "10.52.0.0/14" "scion-hub-${HUB}-nfs" "950"
  run_deploy_delete "$(base_config_json "$HUB")"
  local log hub_deny_line deny_line
  log="$(gcloud_log)"
  hub_deny_line="$(line_number "firewall-rules delete scion-hub-${HUB}-hub-deny" "$log")"
  deny_line="$(line_number "firewall-rules delete scion-hub-${HUB}-nfs-deny" "$log")"
  assert_true "$([[ -n "$hub_deny_line" && -n "$deny_line" && "$hub_deny_line" -lt "$deny_line" ]] && echo true || echo false)" \
    "the hub-deny rule must be deleted before the nfs-deny rule, the same as the nfs-allow rule"
  assert_eq "1" "$(gcloud_log | grep -c "firewall-rules delete scion-hub-${HUB}-hub-deny" || true)" \
    "the hub-deny rule must actually be deleted during teardown, not just left in place"
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

test_deploy_delete_internal_ip_list_failure_aborts_before_any_delete() {
  fresh_gcloud_state
  set_address_list_will_fail "scion-hub-${HUB}-internal-ip"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" \
    "an unconfirmable internal-IP reservation state must fail the teardown run, not be treated as absent"
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

test_deploy_delete_internal_ip_delete_failure_reflected_in_exit_code() {
  fresh_gcloud_state
  seed_address "scion-hub-${HUB}-internal-ip" "10.128.0.42" "scion-deployment=${HUB}"
  set_address_delete_will_fail "scion-hub-${HUB}-internal-ip"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" \
    "an internal-IP delete failure, with the VM confirmed gone, must fail the whole teardown's exit code"
  assert_contains "$DEPLOY_LOG" "Kept internal IP:          scion-hub-${HUB}-internal-ip (delete failed:" \
    "the summary must report the reservation as Kept, with the delete-failed reason"
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
  seed_cluster "mycluster" "default"
  run_deploy_create_wait_for "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")" \
    "instances-create-completed"
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
  seed_cluster "mycluster" "default"
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
  seed_cluster "mycluster" "default"
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
  seed_cluster "mycluster" "default"
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
# adopted on. This tier-off behavior is intentional: markers apply on
# every fresh deploy regardless of whether the hybrid tier is on, and
# never on redeploys of an existing resource. Base teardown is
# unaffected either way.
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
# on. This tier-off behavior is intentional: a validation runner
# without serviceusage.services.enable must never see an enable call
# for an API that's already on.
# =====================================================================

test_deploy_create_api_check_all_enabled_no_enable_call() {
  fresh_gcloud_state
  seed_enabled_apis compute.googleapis.com run.googleapis.com iap.googleapis.com \
    cloudbuild.googleapis.com artifactregistry.googleapis.com aiplatform.googleapis.com
  run_deploy_create "$(base_config_json "$HUB")"
  assert_eq "0" "$(gcloud_log | grep -c 'services enable' || true)" \
    "nothing missing must mean no enable call at all, not an enable call with zero APIs"
}

test_deploy_create_api_check_missing_enables_exact_set() {
  fresh_gcloud_state
  seed_enabled_apis compute.googleapis.com iap.googleapis.com artifactregistry.googleapis.com \
    aiplatform.googleapis.com
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
    cloudbuild.googleapis.com artifactregistry.googleapis.com aiplatform.googleapis.com
  seed_cluster "mycluster" "default"
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
  log="$(timeout 60 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
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
  log="$(timeout 60 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
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
  log="$(timeout 60 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
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
  # The stub's generic "simulated delete failure" text carries none of
  # _hybrid_gcloud_not_found's required tokens, so this must be classified
  # as an unconfirmed failure (Kept), not misreported as a positive
  # not-found -- the fake VM is still "present" in the stub's own state.
  assert_contains "$DEPLOY_LOG" "  Kept GCE VM:                ${INSTANCE_NAME}" \
    "a VM delete failure that isn't a confirmed not-found must be reported as Kept, not Not found"
  assert_not_contains "$DEPLOY_LOG" "Not found GCE VM" \
    "must never claim the VM is gone from an ambiguous delete error alone"
}

test_deploy_delete_tier_off_vm_not_found_error_reports_not_found() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  set_instance_delete_will_fail "$INSTANCE_NAME"
  set_instance_delete_error_text "$INSTANCE_NAME" \
    "ERROR: (gcloud.compute.instances.delete) Could not fetch resource: - The resource 'projects/x/zones/us-central1-b/instances/${INSTANCE_NAME}' was not found (code=404)"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "0" "$DEPLOY_RC" "a confirmed not-found VM delete error must still warn and continue"
  assert_contains "$DEPLOY_LOG" "Not found GCE VM:           ${INSTANCE_NAME}" \
    "a delete error that is a positive not-found must still be reported as Not found"
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
  seed_cluster "mycluster" "default"
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
  seed_cluster "mycluster" "default"
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
  seed_cluster "mycluster" "default"
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

test_deploy_delete_k8s_failure_reports_hybrid_resources_skipped_not_kept() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_cluster "mycluster" "default"
  seed_k8s_pvc "$K8S_PVC_D" "$K8S_NS_D" "$HUB" "$K8S_PV_D"
  seed_k8s_pv "$K8S_PV_D" "$HUB" "10.128.0.5" "/srv/scion-shared" "$K8S_NS_D" "$K8S_PVC_D"
  seed_k8s_namespace "$K8S_NS_D" "$HUB"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  seed_address "scion-hub-${HUB}-internal-ip" "10.128.0.42" "scion-deployment=${HUB}"
  set_k8s_delete_will_fail "pvc" "${K8S_NS_D}__${K8S_PVC_D}"
  run_deploy_delete "$(base_config_json "$HUB" "$(hybrid_config_fragment)")"
  assert_eq "1" "$DEPLOY_RC" "a kubectl delete failure must fail the whole teardown"
  assert_contains "$DEPLOY_LOG" "SKIPPED firewall rule:      scion-hub-${HUB}-nfs-allow (a hybrid-tier Kubernetes object failed to delete)" \
    "a hybrid firewall rule never attempted because k8s failed first must be reported SKIPPED, not Kept"
  assert_contains "$DEPLOY_LOG" "SKIPPED firewall rule:      scion-hub-${HUB}-nfs-deny (a hybrid-tier Kubernetes object failed to delete)" \
    "the deny rule must be reported the same way"
  assert_contains "$DEPLOY_LOG" "SKIPPED internal IP:       scion-hub-${HUB}-internal-ip (a hybrid-tier Kubernetes object failed to delete)" \
    "the internal IP reservation must be reported SKIPPED, not Kept (delete failed: unknown error), when it was never attempted"
  assert_not_contains "$DEPLOY_LOG" "Kept internal IP" \
    "must never claim a delete was attempted and failed when it was never attempted at all"
}

test_deploy_delete_vm_not_gone_reports_hybrid_rules_skipped() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  set_instance_delete_will_fail "$INSTANCE_NAME"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "a VM delete failure must fail the whole teardown"
  assert_contains "$DEPLOY_LOG" "SKIPPED firewall rule:      scion-hub-${HUB}-nfs-allow (VM not confirmed gone)" \
    "a hybrid rule never attempted because the VM isn't confirmed gone must be reported SKIPPED, not Kept"
  assert_contains "$DEPLOY_LOG" "SKIPPED firewall rule:      scion-hub-${HUB}-nfs-deny (VM not confirmed gone)" \
    "the deny rule must be reported the same way"
}

test_deploy_delete_hybrid_rule_delete_failure_reports_downstream_rule_kept() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}" \
    "default" "INGRESS" "ALLOW" "tcp" "2049" "gke-x-node" "" "scion-hub-${HUB}-nfs" "900"
  seed_firewall_rule_json "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}" \
    "default" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "scion-hub-${HUB}-nfs" "950"
  set_firewall_delete_will_fail "scion-hub-${HUB}-nfs-allow"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "1" "$DEPLOY_RC" "a hybrid firewall rule delete failure must fail the whole teardown"
  assert_contains "$DEPLOY_LOG" "Kept firewall rule:         scion-hub-${HUB}-nfs-allow (delete failed, or not attempted after an earlier rule's delete failed)" \
    "the rule whose delete actually failed must be reported Kept"
  assert_contains "$DEPLOY_LOG" "Kept firewall rule:         scion-hub-${HUB}-nfs-deny (delete failed, or not attempted after an earlier rule's delete failed)" \
    "the deny rule, never attempted because the allow rule ahead of it failed, must also be reported Kept, not silently dropped from the summary"
}

test_deploy_delete_k8s_deletes_precede_vm_delete() {
  fresh_gcloud_state
  seed_instance "$INSTANCE_NAME" "us-central1-b"
  seed_cluster "mycluster" "default"
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
  seed_cluster "mycluster" "default"
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

test_deploy_create_squash_script_non_numeric_output_fails_before_any_write() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  local config_file
  config_file="$(mktemp)"
  printf '%s' "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")" > "$config_file"
  local log rc
  log="$(GCLOUD_STUB_SSH_SUCCEEDS=true GCLOUD_STUB_SSH_SQUASH_IDS="abc" \
    timeout 60 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
  rc=$?
  rm -f "$config_file"
  assert_true "$([[ "$rc" -ne 0 && "$rc" -ne 124 ]] && echo true || echo false)" \
    "non-numeric output from the squash-identity script must fail deploy.sh, not hang or be silently accepted"
  assert_contains "$log" "Unexpected output from the NFS squash identity script" "the message should explain why"
  assert_not_contains "$log" "schema_version" "settings.yaml must never be written after this refusal"
  assert_eq "0" "$(echo "$log" | grep -c 'exports.d' || true)" \
    "the NFS export must never be written after this refusal"
}

test_deploy_create_squash_script_uid_zero_fails_before_any_write() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  local config_file
  config_file="$(mktemp)"
  printf '%s' "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")" > "$config_file"
  local log rc
  log="$(GCLOUD_STUB_SSH_SUCCEEDS=true GCLOUD_STUB_SSH_SQUASH_IDS="0:1001" \
    timeout 60 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
  rc=$?
  rm -f "$config_file"
  assert_true "$([[ "$rc" -ne 0 && "$rc" -ne 124 ]] && echo true || echo false)" \
    "a squash uid of 0 must fail deploy.sh, not be accepted as the anonymous NFS uid"
  assert_contains "$log" "refusing to export with root as the anonymous uid" "the message should explain why"
  assert_not_contains "$log" "schema_version" "settings.yaml must never be written after this refusal"
  assert_eq "0" "$(echo "$log" | grep -c 'exports.d' || true)" \
    "the NFS export must never be written after this refusal"
}

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

# The test above only checks that a couple of hybrid-specific strings are
# absent; it says nothing about whether the rest of the rendered
# settings.yaml is exactly right, or whether the ${HYBRID_SHARED_DIR_
# STORAGE_YAML:+...} splice leaves any stray blank line or indentation
# artifact behind when it expands to nothing. This extracts the actual
# heredoc body deploy.sh sent over SSH (between the `<< 'SETTINGSEOF'`
# and closing `SETTINGSEOF` lines in the real, captured --command= text,
# not a reimplementation of deploy.sh's own splice logic) and compares it
# byte-for-byte against the exact content a tier-off run must produce.
test_deploy_create_tier_off_settings_yaml_byte_identical() {
  fresh_gcloud_state
  run_deploy_create_to_settings_yaml "$(base_config_json "$HUB")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-off create must reach the settings.yaml write"
  local log actual expected
  log="$(gcloud_log)"
  # Command substitution strips ALL trailing newlines, which would hide
  # a stray trailing blank line the splice mechanism might leave behind.
  # Appending a sentinel character and stripping only that (not "$(...)"
  # itself) preserves any real trailing newline in both actual and
  # expected.
  actual="$(echo "$log" | awk "/<< 'SETTINGSEOF'/{flag=1; next} /^SETTINGSEOF\$/{flag=0} flag"; echo x)"
  actual="${actual%x}"
  expected="$(cat <<'EXPECTED'
schema_version: "1"
image_registry: "localhost/scion"
harness_configs:
  antigravity:
    harness: antigravity
    env:
      GOOGLE_CLOUD_PROJECT: "demo-project"
      GOOGLE_CLOUD_LOCATION: "global"
server:
  hub:
    name: "demohub"
    admin_emails:
      - "admin@example.com"
  maintenance:
    deployment_tier: "binary"
    release_channel: "nightly"
    update_policy: "auto"
  storage:
    local_path: /home/scion/.scion/workspace-storage
  secrets:
    backend: local
  auth:
    mode: dev
  listen_port: 8080
EXPECTED
echo x)"
  expected="${expected%x}"
  assert_eq "$expected" "$actual" \
    "a tier-off settings.yaml must be byte-for-byte identical to a render with no hybrid-tier splice at all"
}

# The dev-mode write above is what deploy.sh's own Phase 3 uses on every
# create; production auth is proxy mode (IAP), written later in Phase 5
# once the Cloud Run proxy exists -- an entirely separate heredoc in
# deploy.sh, unpinned by the dev-mode test above.
test_deploy_create_tier_off_proxy_settings_yaml_byte_identical() {
  fresh_gcloud_state
  run_deploy_create_to_proxy_settings_yaml \
    "$(base_config_json "$HUB" "" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-off create must reach the Phase-5 proxy-mode settings.yaml write"
  local log actual expected
  log="$(gcloud_log)"
  # Two settings.yaml writes are logged by this point (Phase 3 dev-mode,
  # then Phase 5 proxy-mode); take the LAST one.
  actual="$(echo "$log" | awk "/<< 'SETTINGSEOF'/{flag=1; buf=\"\"; next} /^SETTINGSEOF\$/{flag=0; last=buf} flag{buf=buf \$0 ORS} END{printf \"%s\", last}"; echo x)"
  actual="${actual%x}"
  expected="$(cat <<'EXPECTED'
schema_version: "1"
image_registry: "us-docker.pkg.dev/demo-project/scion"
harness_configs:
  antigravity:
    harness: antigravity
    env:
      GOOGLE_CLOUD_PROJECT: "demo-project"
      GOOGLE_CLOUD_LOCATION: "global"
server:
  hub:
    name: "demohub"
    admin_emails:
      - "admin@example.com"
  maintenance:
    deployment_tier: "binary"
    release_channel: "nightly"
    update_policy: "auto"
  storage:
    local_path: /home/scion/.scion/workspace-storage
  secrets:
    backend: local
  auth:
    mode: proxy
    proxy:
      provider: iap
      iap:
        audience: "/projects/123456789012/locations/us-central1/services/scion-hub-demohub-iap-proxy"
  listen_port: 8080
EXPECTED
echo x)"
  expected="${expected%x}"
  assert_eq "$expected" "$actual" \
    "a tier-off proxy-mode settings.yaml must be byte-for-byte identical to a render with no hybrid-tier splice at all"
}

test_deploy_create_tier_on_proxy_settings_yaml_has_shared_dir_storage_block() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  run_deploy_create_to_proxy_settings_yaml \
    "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-on create must reach the Phase-5 proxy-mode settings.yaml write"
  local log proxy_heredoc
  log="$(gcloud_log)"
  # The log also contains Phase 3's dev-mode write, which carries its own
  # shared_dir_storage block -- extracting only the LAST (proxy-mode)
  # heredoc keeps this test from passing merely because the earlier,
  # unrelated dev-mode write still has the block.
  proxy_heredoc="$(echo "$log" | awk "/<< 'SETTINGSEOF'/{flag=1; buf=\"\"; next} /^SETTINGSEOF\$/{flag=0; last=buf} flag{buf=buf \$0 ORS} END{printf \"%s\", last}"; echo x)"
  proxy_heredoc="${proxy_heredoc%x}"
  assert_contains "$proxy_heredoc" "mode: proxy" "the proxy-mode write must actually carry proxy auth, not dev"
  assert_contains "$proxy_heredoc" "shared_dir_storage:" \
    "the tier-on proxy-mode settings.yaml write must carry the shared_dir_storage block -- an IAP-mode hub must not silently lose it"
  assert_contains "$proxy_heredoc" 'server: "10.128.0.9"' \
    "the proxy-mode shared_dir_storage server field must be the reserved internal IP"
  # hybrid_k8s_ensure_objects (Phase 4, after VM_IP is final) is what
  # actually creates the PV/PVC a tier-on deploy depends on -- nothing
  # before this point in the flow asserts that it was ever called at all.
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/pv/scion-hub-${HUB}-shared.json" ]] && echo true || echo false)" \
    "hybrid_k8s_ensure_objects must actually create the PV when the tier is on"
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/pvc/scion-hub-${HUB}__scion-hub-${HUB}-shared.json" ]] && echo true || echo false)" \
    "hybrid_k8s_ensure_objects must actually create the PVC when the tier is on"
}

test_deploy_create_tier_on_reaches_settings_yaml_with_correct_nfs_and_block() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  run_deploy_create_to_settings_yaml \
    "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-on create must reach the settings.yaml write"
  local log dev_heredoc
  log="$(gcloud_log)"
  # Extracted the same way the proxy-mode test extracts its own heredoc
  # (see test_deploy_create_tier_on_proxy_settings_yaml_has_shared_dir_storage_block),
  # but taking the FIRST occurrence rather than the last: this call stops
  # right at the dev-mode sentinel, so in practice the log holds only
  # this one heredoc, but asserting against the specifically-extracted
  # dev-mode block (not the whole log) keeps that true by construction
  # rather than by this call happening to stop early enough.
  dev_heredoc="$(echo "$log" | awk "/<< 'SETTINGSEOF'/{flag=1; buf=\"\"; next} /^SETTINGSEOF\$/{flag=0; if (!seen) {first=buf; seen=1}} flag{buf=buf \$0 ORS} END{printf \"%s\", first}"; echo x)"
  dev_heredoc="${dev_heredoc%x}"

  assert_contains "$log" "useradd -r -M -N -g scion -s /usr/sbin/nologin scion-nfs" \
    "the squash-identity script must actually be sent over SSH when the tier is on"

  # The default node-subnet CIDR the gcloud stub serves when a test
  # doesn't override it via seed_subnet -- see tests/lib/harness.sh.
  assert_contains "$log" "10.128.0.0/20" \
    "the export line's CIDR must be the discovered node subnet, wired through correctly from hybrid_discover"
  assert_contains "$log" "anonuid=997,anongid=1001" \
    "the export line's anonuid/anongid must be exactly what the squash script returned, wired through unchanged"

  assert_contains "$dev_heredoc" "shared_dir_storage:" \
    "the tier-on settings.yaml write must carry the shared_dir_storage block"
  assert_contains "$dev_heredoc" 'mount_root: "/srv"' \
    "mount_root must be the export root's parent, not the full export root"
  assert_contains "$dev_heredoc" 'id: "scion-shared"' \
    "the share id must be the export root's base name"
  assert_contains "$dev_heredoc" 'pv_name: "scion-hub-demohub-shared"' \
    "pv_name must carry the resolved PVC name"

  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/namespace/scion-hub-${HUB}.json" ]] && echo true || echo false)" \
    "hybrid_k8s_preflight must have created the namespace in Phase 2, well before this Phase 3 stopping point"

  # The reserved internal IP -- not the VM's ephemeral describe
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
  assert_contains "$dev_heredoc" 'server: "10.128.0.9"' \
    "the settings.yaml shared_dir_storage server field must be the reserved internal IP"

  # The internal IP guard's post-create half re-describes the
  # reservation; this call must actually have happened by this point,
  # proving deploy.sh calls the guard at all (nothing else in this flow
  # describes the internal-IP address after it's created).
  assert_contains "$log" "compute addresses describe scion-hub-${HUB}-internal-ip" \
    "the internal IP guard must re-describe the internal IP reservation after create"
}

test_deploy_create_tier_on_wires_configured_image_size_to_export_script() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  # A non-default size: 20 is also what every other fixture happens to
  # produce (the config default), so it can't distinguish "the configured
  # value was used" from "a hard-coded value was used".
  run_deploy_create_to_settings_yaml \
    "$(base_config_json "$HUB" ", \"gke_target\": {\"name\": \"mycluster\", \"location\": \"us-central1\", \"project\": \"demo-project\", \"shared_dir_image_size_gb\": 37}" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-on create must reach the settings.yaml write"
  assert_contains "$(gcloud_log)" "fallocate -l 37G" \
    "deploy.sh must pass the configured gke_target.shared_dir_image_size_gb through to the export script, not a fixed default"
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
  seed_cluster "mycluster" "default"
  run_deploy_create_wait_for "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")" \
    "addresses-create-completed"
  local log
  log="$(gcloud_log)"
  assert_contains "$log" "addresses create scion-hub-${HUB}-internal-ip" \
    "an existing VM's current IP must be promoted to a static reservation"
  assert_contains "$log" "--addresses=10.128.0.5" \
    "must promote the VM's actual current IP (the stub's instances-describe default), not a fresh/different one"
}

# =====================================================================
# Settings writes parsed as YAML, and the agent transport wiring.
# =====================================================================
# The settings tests above compare substrings or bytes. These parse the
# captured settings.yaml writes with the koanf YAML parser the hub uses
# to load its settings (tests/lib/settings-yaml-to-json.go, run through
# the repository's own Go module), so a splice that produces invalid or
# mis-nested YAML fails here even when every expected substring is
# present.

# _settings_heredoc_nth LOG N — the body of the Nth (1-based)
# settings.yaml heredoc in LOG.
_settings_heredoc_nth() {
  echo "$1" | awk -v want="$2" "/<< 'SETTINGSEOF'/{flag=1; n++; buf=\"\"; next} /^SETTINGSEOF\$/{flag=0; if (n == want) printf \"%s\", buf} flag{buf=buf \$0 ORS}"
}

# _settings_yaml_json YAML — prints YAML parsed by the hub's settings
# parser, as JSON. Non-zero, with the parser's error on stderr, if YAML
# does not parse.
_settings_yaml_json() {
  local go_bin repo_root
  go_bin="$(command -v go || true)"
  if [[ -z "$go_bin" && -x /usr/local/go/bin/go ]]; then
    go_bin=/usr/local/go/bin/go
  fi
  if [[ -z "$go_bin" ]]; then
    echo "settings YAML parse: no Go toolchain found (needed to run tests/lib/settings-yaml-to-json.go)" >&2
    return 2
  fi
  repo_root="$(cd "${TIER_DIR}/../.." && pwd)"
  printf '%s' "$1" | (cd "$repo_root" && "$go_bin" run -buildvcs=false \
    scripts/single-node-vm/tests/lib/settings-yaml-to-json.go)
}

# _json_get JSON PATH — the value at dotted PATH (list indexes as
# numbers, e.g. server.hub.admin_emails.0), or "<missing>".
_json_get() {
  printf '%s' "$1" | "$PYTHON" -c '
import json, sys
node = json.load(sys.stdin)
for part in sys.argv[1].split("."):
    if isinstance(node, list) and part.isdigit() and int(part) < len(node):
        node = node[int(part)]
    elif isinstance(node, dict) and part in node:
        node = node[part]
    else:
        print("<missing>")
        sys.exit(0)
print(node if isinstance(node, str) else json.dumps(node))
' "$2"
}

TRANSPORT_TEST_CLIENT_ID="999999999-Verbatim_Client-ID.apps.googleusercontent.com"

test_deploy_create_tier_on_settings_writes_parse_as_yaml() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  set_iap_client_id "$TRANSPORT_TEST_CLIENT_ID"
  run_deploy_create_to_proxy_settings_yaml \
    "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-on create must reach the Phase-5 proxy-mode settings.yaml write"
  local log transport_sa n yaml json mode
  log="$(gcloud_log)"
  transport_sa="$(hybrid_transport_sa_name "$HUB")@demo-project.iam.gserviceaccount.com"
  assert_eq "2" "$(echo "$log" | grep -c "<< 'SETTINGSEOF'" || true)" \
    "a create through Phase 5 must write settings.yaml twice (dev mode, then proxy mode)"
  for n in 1 2; do
    if [[ "$n" == 1 ]]; then mode="dev"; else mode="proxy"; fi
    yaml="$(_settings_heredoc_nth "$log" "$n")"
    json="$(_settings_yaml_json "$yaml")"
    assert_eq "0" "$?" "the ${mode}-mode settings.yaml write must parse as YAML"
    assert_eq "$mode" "$(_json_get "$json" server.auth.mode)" "${mode}-mode write: server.auth.mode"
    assert_eq "iap" "$(_json_get "$json" server.auth.transport.mode)" "${mode}-mode write: server.auth.transport.mode"
    assert_eq "$TRANSPORT_TEST_CLIENT_ID" "$(_json_get "$json" server.auth.transport.oidc_audience)" \
      "${mode}-mode write: server.auth.transport.oidc_audience must be the discovered client ID, verbatim"
    assert_eq "$transport_sa" "$(_json_get "$json" server.auth.transport.platform_auth_sa)" \
      "${mode}-mode write: server.auth.transport.platform_auth_sa must be the transport SA"
    assert_eq "invite_only" "$(_json_get "$json" server.auth.user_access_mode)" \
      "${mode}-mode write: server.auth.user_access_mode defaults to invite_only with the tier on"
    assert_eq "<missing>" "$(_json_get "$json" server.auth.authorized_domains)" \
      "${mode}-mode write: no authorized_domains unless the config sets them"
    assert_eq "admin@example.com" "$(_json_get "$json" server.hub.admin_emails.0)" \
      "${mode}-mode write: the admin stays in admin_emails"
    assert_eq "nfs" "$(_json_get "$json" server.shared_dir_storage.backend)" \
      "${mode}-mode write: server.shared_dir_storage.backend"
    assert_eq "10.128.0.9" "$(_json_get "$json" server.shared_dir_storage.nfs.shares.0.server)" \
      "${mode}-mode write: the shared_dir_storage share server is the reserved internal IP"
    assert_eq "8080" "$(_json_get "$json" server.listen_port)" \
      "${mode}-mode write: server.listen_port stays a server-level key after the splices"
  done
}

test_deploy_create_tier_off_settings_writes_parse_as_yaml() {
  fresh_gcloud_state
  run_deploy_create_to_proxy_settings_yaml \
    "$(base_config_json "$HUB" "" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-off create must reach the Phase-5 proxy-mode settings.yaml write"
  local log n yaml json mode
  log="$(gcloud_log)"
  for n in 1 2; do
    if [[ "$n" == 1 ]]; then mode="dev"; else mode="proxy"; fi
    yaml="$(_settings_heredoc_nth "$log" "$n")"
    json="$(_settings_yaml_json "$yaml")"
    assert_eq "0" "$?" "the tier-off ${mode}-mode settings.yaml write must parse as YAML"
    assert_eq "$mode" "$(_json_get "$json" server.auth.mode)" "tier-off ${mode}-mode write: server.auth.mode"
    assert_eq "<missing>" "$(_json_get "$json" server.auth.transport)" \
      "tier-off ${mode}-mode write: no transport block"
    assert_eq "<missing>" "$(_json_get "$json" server.auth.user_access_mode)" \
      "tier-off ${mode}-mode write: no user_access_mode"
    assert_eq "<missing>" "$(_json_get "$json" server.shared_dir_storage)" \
      "tier-off ${mode}-mode write: no shared_dir_storage block"
    assert_eq "8080" "$(_json_get "$json" server.listen_port)" "tier-off ${mode}-mode write: server.listen_port"
  done
}

test_deploy_create_tier_on_grants_transport_sa_roles() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  set_iap_client_id "$TRANSPORT_TEST_CLIENT_ID"
  run_deploy_create_to_proxy_settings_yaml \
    "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-on create must reach the Phase-5 proxy-mode settings.yaml write"
  local log transport_sa token_line iap_line
  log="$(gcloud_log)"
  transport_sa="$(hybrid_transport_sa_name "$HUB")@demo-project.iam.gserviceaccount.com"
  token_line="$(echo "$log" | grep "^iam service-accounts add-iam-policy-binding ${transport_sa} " || true)"
  assert_eq "1" "$(printf '%s' "$token_line" | grep -c . || true)" \
    "deploy.sh must grant exactly one role binding on the transport SA"
  assert_contains "$token_line" "--role=roles/iam.serviceAccountOpenIdTokenCreator" \
    "the transport SA binding must use the ID-token-only role"
  assert_contains "$token_line" "--member=serviceAccount:scion-hub-${HUB}@demo-project.iam.gserviceaccount.com" \
    "the transport SA binding's member must be the hub VM's runtime SA"
  iap_line="$(echo "$log" | grep '^iap web add-iam-policy-binding ' | grep -F -- "--member=serviceAccount:${transport_sa}" || true)"
  assert_eq "1" "$(printf '%s' "$iap_line" | grep -c . || true)" \
    "Phase 4 must grant the transport SA IAP access exactly once"
  assert_contains "$iap_line" "--role=roles/iap.httpsResourceAccessor" \
    "the transport SA's IAP grant must use the accessor role"
  assert_contains "$iap_line" "--service=scion-hub-${HUB}-iap-proxy" \
    "the transport SA's IAP grant must be on the hub's own Cloud Run service"
  assert_contains "$log" "iap settings get --project=demo-project --resource-type=iap_web" \
    "the client ID must be read from the project-level IAP settings"
}

test_deploy_create_tier_off_no_transport_calls() {
  fresh_gcloud_state
  run_deploy_create_to_proxy_settings_yaml \
    "$(base_config_json "$HUB" "" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-off create must reach the Phase-5 proxy-mode settings.yaml write"
  local log
  log="$(gcloud_log)"
  assert_eq "0" "$(echo "$log" | grep -c '^iap settings get' || true)" "tier off must not read IAP settings"
  assert_eq "0" "$(echo "$log" | grep -c 'scion-tp-' || true)" "tier off must not create or grant a transport SA"
}

# _run_deploy_create_expect_refusal CONFIG_JSON — runs a create that is
# expected to stop at validation, with a timeout so a missed refusal
# fails fast. Sets DEPLOY_RC and DEPLOY_LOG.
_run_deploy_create_expect_refusal() {
  local config_file
  config_file="$(mktemp)"
  printf '%s' "$1" > "$config_file"
  DEPLOY_LOG="$(timeout 60 bash "$DEPLOY_SH" --config "$config_file" --version v1.0.0-test < /dev/null 2>&1)"
  DEPLOY_RC=$?
  rm -f "$config_file"
}

test_deploy_create_tier_on_service_account_admin_email_refused() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  _run_deploy_create_expect_refusal \
    "$(base_config_json "$HUB" "$(hybrid_config_fragment)" "registry" "us-docker.pkg.dev/demo-project/scion" \
      | sed 's/admin@example.com/robot@demo-project.iam.gserviceaccount.com/')"
  assert_true "$([[ "$DEPLOY_RC" -ne 0 && "$DEPLOY_RC" -ne 124 ]] && echo true || echo false)" \
    "tier on with a service-account admin_email must be refused before any create"
  assert_contains "$DEPLOY_LOG" "is a service account" "the message should say what is wrong with admin_email"
  assert_eq "0" "$(gcloud_log | grep -c ' create ' || true)" "nothing should be created"
}

test_deploy_create_tier_on_open_user_access_mode_refused() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  _run_deploy_create_expect_refusal \
    "$(base_config_json "$HUB" "$(hybrid_config_fragment), \"user_access_mode\": \"open\"" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_true "$([[ "$DEPLOY_RC" -ne 0 && "$DEPLOY_RC" -ne 124 ]] && echo true || echo false)" \
    "tier on with user_access_mode open must be refused before any create"
  assert_contains "$DEPLOY_LOG" "user_access_mode 'open' is not supported" "the message should name the mode"
  assert_eq "0" "$(gcloud_log | grep -c ' create ' || true)" "nothing should be created"
}

test_deploy_create_tier_on_domain_restricted_written_to_settings() {
  fresh_gcloud_state
  seed_cluster "mycluster" "default"
  run_deploy_create_to_proxy_settings_yaml \
    "$(base_config_json "$HUB" "$(hybrid_config_fragment), \"user_access_mode\": \"domain_restricted\", \"authorized_domains\": [\"Example.com\"]" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-on create must reach the Phase-5 proxy-mode settings.yaml write"
  local log json
  log="$(gcloud_log)"
  json="$(_settings_yaml_json "$(_settings_heredoc_nth "$log" 2)")"
  assert_eq "0" "$?" "the proxy-mode settings.yaml write must parse as YAML"
  assert_eq "domain_restricted" "$(_json_get "$json" server.auth.user_access_mode)" \
    "a configured domain_restricted mode reaches settings.yaml"
  assert_eq '["example.com"]' "$(_json_get "$json" server.auth.authorized_domains)" \
    "the configured domains reach settings.yaml, lowercased"
  assert_eq "iap" "$(_json_get "$json" server.auth.transport.mode)" "the transport block is still written alongside"
  assert_contains "$DEPLOY_LOG" "User access:  domain_restricted" "the resolved mode is shown in the pre-deploy summary"
}

test_deploy_create_tier_off_ignores_user_access_config() {
  fresh_gcloud_state
  run_deploy_create_to_proxy_settings_yaml \
    "$(base_config_json "$HUB" ", \"user_access_mode\": \"open\"" "registry" "us-docker.pkg.dev/demo-project/scion")"
  assert_eq "true" "$DEPLOY_REACHED_SETTINGS_YAML" "a tier-off create with user access keys must still run"
  assert_contains "$DEPLOY_LOG" "applied only when the hybrid tier is on; ignoring them" \
    "tier off must say the user access keys are not applied"
  assert_not_contains "$(gcloud_log)" "user_access_mode" "tier off must not write user_access_mode"
}

# =====================================================================
# Teardown of the agent transport service account, as deploy.sh
# reports it.
# =====================================================================

test_deploy_delete_transport_sa_deleted_and_reported() {
  fresh_gcloud_state
  local email
  email="$(hybrid_transport_sa_name "$HUB")@demo-project.iam.gserviceaccount.com"
  seed_service_account "$email" "$MARKER"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "0" "$DEPLOY_RC" "a clean teardown with a marked transport SA must exit 0"
  assert_contains "$(gcloud_log)" "iam service-accounts delete ${email} " "the marked transport SA must be deleted"
  assert_contains "$DEPLOY_LOG" "Deleted transport SA:       ${email}" "the summary must list the deleted transport SA"
  assert_contains "$DEPLOY_LOG" "No transport SA IAP access left on: scion-hub-${HUB}-iap-proxy" \
    "the summary must say no IAP access is left once the Cloud Run service is gone"
}

test_deploy_delete_transport_sa_absent_reported_not_found() {
  fresh_gcloud_state
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_eq "0" "$DEPLOY_RC" "an absent transport SA must not fail teardown"
  assert_contains "$DEPLOY_LOG" "Not found transport SA:     $(hybrid_transport_sa_name "$HUB")@demo-project.iam.gserviceaccount.com" \
    "the summary must list the transport SA as not found"
}

test_deploy_delete_transport_sa_delete_failure_exits_nonzero() {
  fresh_gcloud_state
  local email
  email="$(hybrid_transport_sa_name "$HUB")@demo-project.iam.gserviceaccount.com"
  seed_service_account "$email" "$MARKER"
  set_service_account_delete_will_fail "$email"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_true "$([[ "$DEPLOY_RC" -ne 0 ]] && echo true || echo false)" \
    "a failed transport SA delete must make teardown exit non-zero"
  assert_contains "$DEPLOY_LOG" "Kept transport SA:          ${email} (delete failed)" \
    "the summary must list the transport SA as kept, with the reason"
}

test_deploy_delete_transport_sa_describe_error_exits_nonzero() {
  fresh_gcloud_state
  local email
  email="$(hybrid_transport_sa_name "$HUB")@demo-project.iam.gserviceaccount.com"
  set_service_account_describe_error "$email"
  run_deploy_delete "$(base_config_json "$HUB")"
  assert_true "$([[ "$DEPLOY_RC" -ne 0 ]] && echo true || echo false)" \
    "a transport SA whose state cannot be read must make teardown exit non-zero"
  assert_contains "$DEPLOY_LOG" "Kept transport SA:          ${email} (state unknown: describe failed)" \
    "the summary must list the transport SA as kept because its state is unknown"
  assert_eq "0" "$(gcloud_log | grep -c "iam service-accounts delete ${email}" || true)" \
    "nothing must be deleted when the transport SA's state is unknown"
}

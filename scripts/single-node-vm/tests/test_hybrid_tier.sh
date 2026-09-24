# scripts/single-node-vm/tests/test_hybrid_tier.sh — tests for
# hybrid-tier.sh. Sourced by run.sh, which also sources lib/harness.sh
# first and puts lib/ (containing the stub `gcloud`) at the front of PATH.
# No test here contacts GCP.

HUB="demohub"
PROJECT="demo-project"
NETWORK="default"

# --- tier-off: absent config means zero hybrid gcloud calls ---------------
test_tier_off_no_gcloud_calls() {
  fresh_gcloud_state
  CONFIG[gke_target.name]=""
  hybrid_read_config "$PROJECT"
  assert_eq "false" "$HYBRID_ENABLED" "tier should be off when gke_target.name is absent"
  assert_eq "0" "$(gcloud_call_count)" "no gcloud call should happen reading config alone"
}

# --- config: project mismatch is refused actionably ------------------------
test_config_project_mismatch_refused() {
  fresh_gcloud_state
  CONFIG[gke_target.name]="mycluster"
  CONFIG[gke_target.location]="us-central1"
  CONFIG[gke_target.project]="other-project"
  run_expect_fail hybrid_read_config "$PROJECT"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "mismatched gke_target.project must fail the run"
  assert_contains "$RUN_OUTPUT" "other-project" "error should name the offending project"
}

# --- config: missing location is refused actionably -------------------------
test_config_missing_location_refused() {
  fresh_gcloud_state
  CONFIG[gke_target.name]="mycluster"
  run_expect_fail hybrid_read_config "$PROJECT"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "missing gke_target.location must fail the run"
  assert_contains "$RUN_OUTPUT" "location" "error should mention the missing field"
}

# --- config: same-project cluster enables the tier --------------------------
test_config_enables_tier() {
  fresh_gcloud_state
  CONFIG[gke_target.name]="mycluster"
  CONFIG[gke_target.location]="us-central1"
  hybrid_read_config "$PROJECT"
  assert_eq "true" "$HYBRID_ENABLED" "tier should be on when name+location are set"
  assert_eq "mycluster" "$GKE_NAME" "GKE_NAME should come from config"
  assert_eq "$PROJECT" "$GKE_PROJECT" "GKE_PROJECT should default to the hub's project"
}

# --- discovery: Standard cluster shape (gke-<cluster>- node prefix) --------
test_discover_standard_shape() {
  fresh_gcloud_state
  GKE_NAME="democluster"
  GKE_PROJECT="$PROJECT"
  GKE_LOCATION="us-central1"
  seed_cluster_network "$NETWORK"
  seed_instances instances-standard.tsv
  hybrid_discover "$NETWORK"
  assert_eq "gke-democluster-default-pool" "$GKE_NODE_TAG" "should discover the Standard node tag"
}

# --- discovery: Autopilot cluster shape (gk3-<cluster>- node prefix) ------
test_discover_autopilot_shape() {
  fresh_gcloud_state
  GKE_NAME="democluster"
  GKE_PROJECT="$PROJECT"
  GKE_LOCATION="us-central1"
  seed_cluster_network "$NETWORK"
  seed_instances instances-autopilot.tsv
  hybrid_discover "$NETWORK"
  assert_eq "gk3-democluster-nap-e2standard4-pool" "$GKE_NODE_TAG" "should discover the Autopilot node tag"
}

# --- discovery: network mismatch is refused actionably ----------------------
test_discover_network_mismatch_refused() {
  fresh_gcloud_state
  GKE_NAME="democluster"
  GKE_PROJECT="$PROJECT"
  GKE_LOCATION="us-central1"
  seed_cluster_network "other-vpc"
  seed_instances instances-standard.tsv
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "network mismatch must fail the run"
  assert_contains "$RUN_OUTPUT" "other-vpc" "error should name the cluster's actual network"
}

# --- discovery: cluster not found fails before creating anything -----------
test_discover_cluster_not_found_refused() {
  fresh_gcloud_state
  GKE_NAME="ghostcluster"
  GKE_PROJECT="$PROJECT"
  GKE_LOCATION="us-central1"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unfound cluster must fail the run"
  assert_eq "0" "$(gcloud_log | grep -c 'firewall-rules create' || true)" "nothing should be created after a failed discovery"
}

# --- firewall rules: names, marker, target tag, ports/priorities/sources ---
test_firewall_rules_created_with_names_marker_tag_and_shape() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-default-pool"
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"

  assert_eq "scion-hub-${HUB}-nfs-allow" "$HYBRID_ALLOW_NAME" "allow rule name"
  assert_eq "scion-hub-${HUB}-nfs-deny" "$HYBRID_DENY_NAME" "deny rule name"

  local log allow_line deny_line
  log="$(gcloud_log)"
  allow_line="$(echo "$log" | grep 'firewall-rules create scion-hub-demohub-nfs-allow')"
  deny_line="$(echo "$log" | grep 'firewall-rules create scion-hub-demohub-nfs-deny')"

  assert_contains "$allow_line" "--description=scion-deployment=${HUB}" "allow rule carries the exact ownership marker"
  assert_contains "$deny_line" "--description=scion-deployment=${HUB}" "deny rule carries the exact ownership marker"

  assert_contains "$allow_line" "--target-tags=scion-hub-${HUB}-nfs" "allow rule has the deployment-specific target tag"
  assert_contains "$deny_line" "--target-tags=scion-hub-${HUB}-nfs" "deny rule has the deployment-specific target tag"

  assert_contains "$allow_line" "--direction=INGRESS" "allow rule is ingress"
  assert_contains "$allow_line" "--action=ALLOW" "allow rule action"
  assert_contains "$allow_line" "--rules=tcp:2049" "allow rule port"
  assert_contains "$allow_line" "--source-tags=${GKE_NODE_TAG}" "allow rule sources from the discovered node tag"
  assert_contains "$allow_line" "--priority=900" "allow rule priority"

  assert_contains "$deny_line" "--direction=INGRESS" "deny rule is ingress"
  assert_contains "$deny_line" "--action=DENY" "deny rule action"
  assert_contains "$deny_line" "--rules=tcp:2049" "deny rule port"
  assert_contains "$deny_line" "--source-ranges=0.0.0.0/0" "deny rule denies the world"
  assert_contains "$deny_line" "--priority=950" "deny rule priority"
}

# --- firewall rules: reuse a rule that already carries the marker ----------
test_firewall_rule_reused_when_marked() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-default-pool"
  seed_firewall_rule "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}"
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_eq "0" "$(gcloud_log | grep -c "firewall-rules create scion-hub-${HUB}-nfs-allow" || true)" \
    "an already-marked rule must not be recreated"
}

# --- firewall rules: refusal on an existing unmarked rule ---------------
test_firewall_rule_refused_when_unmarked() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-default-pool"
  seed_firewall_rule "scion-hub-${HUB}-nfs-allow" "some other unrelated rule"
  run_expect_fail hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unmarked name collision must fail the run"
  assert_contains "$RUN_OUTPUT" "does not carry this deployment's marker" "error should explain the refusal"
}

# --- firewall rules: an unmarked prefix collision (e.g. hub vs hub2) is not
# treated as a match -- the marker check is exact-token, not substring. -----
test_firewall_marker_check_is_exact_not_substring() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-default-pool"
  # A rule actually owned by a different, prefix-colliding hub name.
  seed_firewall_rule "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}2"
  run_expect_fail hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" \
    "a marker for a different, prefix-colliding hub must not be treated as a match"
}

# --- VM tag ------------------------------------------------------------
test_vm_tag_value() {
  assert_eq "scion-hub-${HUB}-nfs" "$(hybrid_vm_tag "$HUB")" "VM tag derivation"
}

test_apply_vm_tag_idempotent() {
  fresh_gcloud_state
  hybrid_apply_vm_tag "scion-hub-${HUB}" "us-central1-b" "$PROJECT" "$HUB"
  hybrid_apply_vm_tag "scion-hub-${HUB}" "us-central1-b" "$PROJECT" "$HUB"
  local log
  log="$(gcloud_log)"
  assert_eq "2" "$(echo "$log" | grep -c 'instances add-tags')" "add-tags should be callable repeatedly without error"
  assert_contains "$log" "--tags=scion-hub-${HUB}-nfs" "add-tags should use the derived VM tag"
}

# --- pre-existing non-hybrid hub, re-run with the tier newly enabled -------
test_reenable_on_preexisting_nonhybrid_hub() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-default-pool"
  # No pre-existing firewall-rules state: this hub predates the hybrid tier,
  # so the two rules don't exist yet even though the VM/hub itself does.
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  hybrid_apply_vm_tag "scion-hub-${HUB}" "us-central1-b" "$PROJECT" "$HUB"
  assert_eq "1" "$(gcloud_log | grep -c 'firewall-rules create scion-hub-demohub-nfs-allow' || true)" \
    "hybrid rules should be created fresh on an existing, previously non-hybrid hub"
  assert_eq "1" "$(gcloud_log | grep -c 'instances add-tags' || true)" \
    "the existing VM should get the tag idempotently"
}

# --- teardown: marked rules deleted, unmarked SKIPPED, run fails first ----
test_teardown_check_all_marked() {
  fresh_gcloud_state
  seed_firewall_rule "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}"
  seed_firewall_rule "scion-hub-${HUB}-nfs-deny" "scion-deployment=${HUB}"
  hybrid_teardown_check "$HUB" "$PROJECT"
  assert_eq "false" "$HYBRID_TEARDOWN_FAILED" "an all-marked pair should not fail teardown"
  assert_eq "2" "${#HYBRID_TEARDOWN_DELETE[@]}" "both marked rules should be queued for deletion"
  assert_eq "0" "${#HYBRID_TEARDOWN_SKIP[@]}" "nothing should be skipped"

  hybrid_teardown_delete "$PROJECT"
  local log
  log="$(gcloud_log)"
  assert_eq "1" "$(echo "$log" | grep -c "firewall-rules delete scion-hub-${HUB}-nfs-allow" || true)" \
    "the allow rule should be deleted"
  assert_eq "1" "$(echo "$log" | grep -c "firewall-rules delete scion-hub-${HUB}-nfs-deny" || true)" \
    "the deny rule should be deleted"
}

test_teardown_check_none_found() {
  fresh_gcloud_state
  hybrid_teardown_check "$HUB" "$PROJECT"
  assert_eq "false" "$HYBRID_TEARDOWN_FAILED" "no rules present is not a failure"
  assert_eq "0" "${#HYBRID_TEARDOWN_DELETE[@]}" "nothing to delete"
  assert_eq "0" "${#HYBRID_TEARDOWN_SKIP[@]}" "nothing to skip"
}

test_teardown_check_unmarked_fails_and_skips() {
  fresh_gcloud_state
  seed_firewall_rule "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}"
  seed_firewall_rule "scion-hub-${HUB}-nfs-deny" "unrelated-rule-not-ours"
  hybrid_teardown_check "$HUB" "$PROJECT"
  assert_eq "true" "$HYBRID_TEARDOWN_FAILED" "an unmarked name match must fail the teardown run"
  assert_eq "1" "${#HYBRID_TEARDOWN_DELETE[@]}" "the marked rule is still queued"
  assert_eq "1" "${#HYBRID_TEARDOWN_SKIP[@]}" "the unmarked rule is listed as SKIPPED"
  assert_eq "scion-hub-${HUB}-nfs-deny" "${HYBRID_TEARDOWN_SKIP[0]}" "the deny rule is the one skipped"
}

test_teardown_delete_only_deletes_queued() {
  fresh_gcloud_state
  seed_firewall_rule "scion-hub-${HUB}-nfs-allow" "scion-deployment=${HUB}"
  hybrid_teardown_check "$HUB" "$PROJECT"
  hybrid_teardown_delete "$PROJECT"
  assert_true "$([[ -f "${GCLOUD_STUB_STATE_DIR}/firewall-rules/scion-hub-${HUB}-nfs-allow" ]] && echo false || echo true)" \
    "the marked rule should actually be deleted from stub state"
}

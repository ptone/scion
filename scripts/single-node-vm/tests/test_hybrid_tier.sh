# scripts/single-node-vm/tests/test_hybrid_tier.sh — tests for
# hybrid-tier.sh. Sourced by run.sh, which also sources lib/harness.sh
# first and puts lib/ (containing the stub `gcloud`) at the front of PATH.
# No test here contacts GCP.
#
# This file has no shebang -- it is always `source`d, never executed --
# so shellcheck needs an explicit shell directive to know its dialect.
# shellcheck shell=bash

HUB="demohub"
PROJECT="demo-project"
NETWORK="default"
ALLOW_NAME="scion-hub-${HUB}-nfs-allow"
DENY_NAME="scion-hub-${HUB}-nfs-deny"
TARGET_TAG="scion-hub-${HUB}-nfs"
MARKER="scion-deployment=${HUB}"

# --- tier-off: absent config means zero hybrid gcloud calls ---------------
test_tier_off_no_gcloud_calls() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]=""
  hybrid_read_config "$PROJECT"
  assert_eq "false" "$HYBRID_ENABLED" "tier should be off when gke_target.name is absent"
  assert_eq "0" "$(gcloud_call_count)" "no gcloud call should happen reading config alone"
}

# --- config: project mismatch is refused actionably ------------------------
test_config_project_mismatch_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  CONFIG["gke_target.location"]="us-central1"
  CONFIG["gke_target.project"]="other-project"
  run_expect_fail hybrid_read_config "$PROJECT"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "mismatched gke_target.project must fail the run"
  assert_contains "$RUN_OUTPUT" "other-project" "error should name the offending project"
}

# --- config: missing location is refused actionably -------------------------
test_config_missing_location_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  run_expect_fail hybrid_read_config "$PROJECT"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "missing gke_target.location must fail the run"
  assert_contains "$RUN_OUTPUT" "location" "error should mention the missing field"
}

# --- config: same-project cluster enables the tier --------------------------
test_config_enables_tier() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  # shellcheck disable=SC2034 # read by config_get (harness.sh) via hybrid_read_config
  CONFIG["gke_target.location"]="us-central1"
  hybrid_read_config "$PROJECT"
  assert_eq "true" "$HYBRID_ENABLED" "tier should be on when name+location are set"
  assert_eq "mycluster" "$GKE_NAME" "GKE_NAME should come from config"
  assert_eq "$PROJECT" "$GKE_PROJECT" "GKE_PROJECT should default to the hub's project"
}

# =====================================================================
# Discovery: bound to the cluster's own node pools / instance groups /
# instance templates, never to instance-name string matching.
# =====================================================================

test_discover_standard_shape() {
  fresh_gcloud_state
  GKE_NAME="democluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "democluster" "$NETWORK" \
    "https://www.googleapis.com/compute/v1/projects/${PROJECT}/zones/us-central1-a/instanceGroupManagers/gke-democluster-default-pool-abc12345-grp"
  seed_mig "gke-democluster-default-pool-abc12345-grp" \
    "https://www.googleapis.com/compute/v1/projects/${PROJECT}/global/instanceTemplates/gke-democluster-default-pool-abc12345"
  seed_template "gke-democluster-default-pool-abc12345" "gke-democluster-abc12345-node,http-server"

  hybrid_discover "$NETWORK"
  assert_eq "gke-democluster-abc12345-node" "$GKE_NODE_TAG" "should discover the Standard node tag via its instance template"
}

test_discover_autopilot_shape() {
  fresh_gcloud_state
  GKE_NAME="aplcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "aplcluster" "$NETWORK" \
    "https://www.googleapis.com/compute/v1/projects/${PROJECT}/zones/us-central1-a/instanceGroupManagers/gk3-aplcluster-nap-abcdef-grp"
  seed_mig "gk3-aplcluster-nap-abcdef-grp" "gk3-aplcluster-nap-abcdef-template"
  seed_template "gk3-aplcluster-nap-abcdef-template" "gke-aplcluster-abcdef-node"

  hybrid_discover "$NETWORK"
  assert_eq "gke-aplcluster-abcdef-node" "$GKE_NODE_TAG" \
    "should discover the Autopilot node tag via its instance template even though node instances are gk3-prefixed"
}

# A cluster whose managed instance group currently has zero running
# instances (e.g. an Autopilot pool scaled to zero) still has a template
# with tags, so discovery must not depend on any live instance existing.
test_discover_works_with_zero_instances() {
  fresh_gcloud_state
  GKE_NAME="idlecluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "idlecluster" "$NETWORK" "mig-idle"
  seed_mig "mig-idle" "template-idle"
  seed_template "template-idle" "gke-idlecluster-zz9999-node"
  hybrid_discover "$NETWORK"
  assert_eq "gke-idlecluster-zz9999-node" "$GKE_NODE_TAG" \
    "discovery reads the template, not live instances, so zero current replicas is fine"
}

# Two clusters whose names prefix-collide ("demo" / "demo-cluster") must
# never cross-contaminate: discovery is bound to the exact cluster's own
# describe response and its own instance groups, never to instance-name
# string matching.
test_discover_prefix_collision_does_not_cross_contaminate() {
  fresh_gcloud_state
  seed_cluster "demo" "$NETWORK" "mig-demo"
  seed_mig "mig-demo" "template-demo"
  seed_template "template-demo" "gke-demo-aaa111-node"

  seed_cluster "demo-cluster" "$NETWORK" "mig-democluster"
  seed_mig "mig-democluster" "template-democluster"
  seed_template "template-democluster" "gke-demo-cluster-bbb222-node"

  GKE_NAME="demo"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_discover "$NETWORK"
  assert_eq "gke-demo-aaa111-node" "$GKE_NODE_TAG" \
    "the prefix-colliding cluster's tag must never be picked for a different cluster"
}

# A template listing a user-added tag before the real GKE node tag must
# not change which one is picked -- filtering is by pattern, not position.
test_discover_multi_tag_template_user_tag_first() {
  fresh_gcloud_state
  GKE_NAME="multitest"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "multitest" "$NETWORK" "mig-multi"
  seed_mig "mig-multi" "template-multi"
  seed_template "template-multi" "custom-user-tag,gke-multitest-xyz-node"
  hybrid_discover "$NETWORK"
  assert_eq "gke-multitest-xyz-node" "$GKE_NODE_TAG" \
    "the real node tag must be picked regardless of tag order in the template"
}

test_discover_zero_candidates_refused_with_evidence() {
  fresh_gcloud_state
  GKE_NAME="notagcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "notagcluster" "$NETWORK" "mig-notag"
  seed_mig "mig-notag" "template-notag"
  seed_template "template-notag" "custom-tag,another-tag"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "no matching tag must fail the run"
  assert_contains "$RUN_OUTPUT" "notagcluster" "error should name the cluster"
  assert_contains "$RUN_OUTPUT" "custom-tag" "error should list a tag that was found"
  assert_contains "$RUN_OUTPUT" "another-tag" "error should list the other tag that was found"
}

test_discover_ambiguous_candidates_refused_with_evidence() {
  fresh_gcloud_state
  GKE_NAME="ambigcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "ambigcluster" "$NETWORK" "mig-a" "mig-b"
  seed_mig "mig-a" "template-a"
  seed_mig "mig-b" "template-b"
  seed_template "template-a" "gke-ambigcluster-aaa-node"
  seed_template "template-b" "gke-ambigcluster-bbb-node"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "more than one candidate must fail the run, never guess"
  assert_contains "$RUN_OUTPUT" "ambigcluster" "error should name the cluster"
  assert_contains "$RUN_OUTPUT" "gke-ambigcluster-aaa-node" "error should list the first candidate"
  assert_contains "$RUN_OUTPUT" "gke-ambigcluster-bbb-node" "error should list the second candidate"
}

test_discover_no_node_pools_refused() {
  fresh_gcloud_state
  GKE_NAME="emptycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "emptycluster" "$NETWORK"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a cluster with no node pools must fail discovery"
  assert_contains "$RUN_OUTPUT" "emptycluster" "error should name the cluster"
}

test_discover_network_mismatch_refused() {
  fresh_gcloud_state
  GKE_NAME="netmismatch"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "netmismatch" "other-vpc" "mig-x"
  seed_mig "mig-x" "template-x"
  seed_template "template-x" "gke-netmismatch-x-node"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "network mismatch must fail the run"
  assert_contains "$RUN_OUTPUT" "other-vpc" "error should name the cluster's actual network"
}

test_discover_cluster_not_found_refused() {
  fresh_gcloud_state
  GKE_NAME="ghostcluster"
  GKE_PROJECT="$PROJECT"
  # shellcheck disable=SC2034 # read by hybrid_discover (hybrid-tier.sh)
  GKE_LOCATION="us-central1"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unfound cluster must fail the run"
  assert_eq "0" "$(gcloud_log | grep -c 'firewall-rules create' || true)" "nothing should be created after a failed discovery"
}

# =====================================================================
# Firewall rules: names, marker, target tag, shape, reuse, and
# spec-drift verification on reuse.
# =====================================================================

test_firewall_rules_created_with_names_marker_tag_and_shape() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"

  assert_eq "$ALLOW_NAME" "$HYBRID_ALLOW_NAME" "allow rule name"
  assert_eq "$DENY_NAME" "$HYBRID_DENY_NAME" "deny rule name"

  local log allow_line deny_line
  log="$(gcloud_log)"
  allow_line="$(echo "$log" | grep "firewall-rules create ${ALLOW_NAME} ")"
  deny_line="$(echo "$log" | grep "firewall-rules create ${DENY_NAME} ")"

  assert_contains "$allow_line" "--description=${MARKER}" "allow rule carries the exact ownership marker"
  assert_contains "$deny_line" "--description=${MARKER}" "deny rule carries the exact ownership marker"

  assert_contains "$allow_line" "--target-tags=${TARGET_TAG}" "allow rule has the deployment-specific target tag"
  assert_contains "$deny_line" "--target-tags=${TARGET_TAG}" "deny rule has the deployment-specific target tag"

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

# A rule this tier just created must itself pass its own reuse check on
# the very next run -- otherwise every real deploy.sh re-run would fail
# immediately after its own first-time creation.
test_firewall_rules_idempotent_across_two_runs() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  local log
  log="$(gcloud_log)"
  assert_eq "1" "$(echo "$log" | grep -c "firewall-rules create ${ALLOW_NAME} ")" \
    "the allow rule must be created exactly once across two runs"
  assert_eq "1" "$(echo "$log" | grep -c "firewall-rules create ${DENY_NAME} ")" \
    "the deny rule must be created exactly once across two runs"
}

test_firewall_rule_reused_when_marked_and_matching() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  seed_firewall_rule_json "$ALLOW_NAME" "$MARKER" "$NETWORK" "INGRESS" "ALLOW" "tcp" "2049" \
    "$GKE_NODE_TAG" "" "$TARGET_TAG" "900"
  seed_firewall_rule_json "$DENY_NAME" "$MARKER" "$NETWORK" "INGRESS" "DENY" "tcp" "2049" \
    "" "0.0.0.0/0" "$TARGET_TAG" "950"
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_eq "0" "$(gcloud_log | grep -c 'firewall-rules create' || true)" \
    "an already-marked, matching-spec rule pair must not be recreated"
}

test_firewall_rule_refused_when_unmarked() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  seed_firewall_rule_desc_only "$ALLOW_NAME" "some other unrelated rule"
  run_expect_fail hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unmarked name collision must fail the run"
  assert_contains "$RUN_OUTPUT" "does not carry this deployment's marker" "error should explain the refusal"
}

# The marker check is an exact match, not a substring: a rule owned by a
# different, prefix-colliding hub name must not be treated as a match.
test_firewall_marker_check_is_exact_not_substring() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  seed_firewall_rule_desc_only "$ALLOW_NAME" "scion-deployment=${HUB}2"
  run_expect_fail hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" \
    "a marker for a different, prefix-colliding hub must not be treated as a match"
  # Specifically the marker check, not merely "failed for some reason" (a
  # substring-match regression here would fall through to the spec-drift
  # check instead and fail with different text, which must not count as a
  # pass for this test).
  assert_contains "$RUN_OUTPUT" "does not carry this deployment's marker" \
    "must fail at the marker check itself, not some other check"
}

# --- Spec drift on an already-marked rule: fail, list the differing
# fields, and print a remediation command. Never auto-correct. ------------

test_drift_widened_allow_source_fails_with_remediation() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  # Drifted: source is some other tag than the currently discovered one --
  # the realistic case is a recreated cluster whose new node tag no longer
  # matches what the rule was created with.
  seed_firewall_rule_json "$ALLOW_NAME" "$MARKER" "$NETWORK" "INGRESS" "ALLOW" "tcp" "2049" \
    "some-stale-tag" "" "$TARGET_TAG" "900"
  seed_firewall_rule_json "$DENY_NAME" "$MARKER" "$NETWORK" "INGRESS" "DENY" "tcp" "2049" \
    "" "0.0.0.0/0" "$TARGET_TAG" "950"
  run_expect_fail hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a widened/changed source must fail the run"
  assert_contains "$RUN_OUTPUT" "$ALLOW_NAME" "drift output should name the drifted rule"
  assert_contains "$RUN_OUTPUT" "some-stale-tag" "drift output should show the actual (drifted) source"
  assert_contains "$RUN_OUTPUT" "${GKE_NODE_TAG}" "drift output should show the expected source"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "drift output should include a runnable delete remediation"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "a source-only drift should also offer an in-place update remediation"
  assert_contains "$RUN_OUTPUT" "--source-tags=${GKE_NODE_TAG}" "the update remediation should restore the expected source"
}

test_drift_lower_deny_priority_fails_with_remediation() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  seed_firewall_rule_json "$ALLOW_NAME" "$MARKER" "$NETWORK" "INGRESS" "ALLOW" "tcp" "2049" \
    "$GKE_NODE_TAG" "" "$TARGET_TAG" "900"
  # Drifted: priority lowered from 950.
  seed_firewall_rule_json "$DENY_NAME" "$MARKER" "$NETWORK" "INGRESS" "DENY" "tcp" "2049" \
    "" "0.0.0.0/0" "$TARGET_TAG" "100"
  run_expect_fail hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a lowered priority must fail the run"
  assert_contains "$RUN_OUTPUT" "$DENY_NAME" "drift output should name the drifted rule"
  assert_contains "$RUN_OUTPUT" "priority" "drift output should mention the drifted field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${DENY_NAME}" "drift output should include a runnable delete remediation"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${DENY_NAME}" "a priority-only drift should also offer an in-place update remediation"
  assert_contains "$RUN_OUTPUT" "--priority=950" "the update remediation should restore the expected priority"
}

test_drift_action_change_offers_delete_but_not_update() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  # Drifted: someone flipped the allow rule to DENY. `update` cannot
  # change action, so only the delete remediation should be offered.
  seed_firewall_rule_json "$ALLOW_NAME" "$MARKER" "$NETWORK" "INGRESS" "DENY" "tcp" "2049" \
    "" "0.0.0.0/0" "$TARGET_TAG" "900"
  seed_firewall_rule_json "$DENY_NAME" "$MARKER" "$NETWORK" "INGRESS" "DENY" "tcp" "2049" \
    "" "0.0.0.0/0" "$TARGET_TAG" "950"
  run_expect_fail hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an action change must fail the run"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "an action drift should still offer the delete remediation"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "an action drift must not offer an in-place update (unsupported by update)"
}

# --- VM tag ------------------------------------------------------------
test_vm_tag_value() {
  assert_eq "$TARGET_TAG" "$(hybrid_vm_tag "$HUB")" "VM tag derivation"
}

test_apply_vm_tag_idempotent() {
  fresh_gcloud_state
  hybrid_apply_vm_tag "scion-hub-${HUB}" "us-central1-b" "$PROJECT" "$HUB"
  hybrid_apply_vm_tag "scion-hub-${HUB}" "us-central1-b" "$PROJECT" "$HUB"
  local log
  log="$(gcloud_log)"
  assert_eq "2" "$(echo "$log" | grep -c 'instances add-tags')" "add-tags should be callable repeatedly without error"
  assert_contains "$log" "--tags=${TARGET_TAG}" "add-tags should use the derived VM tag"
}

# --- pre-existing non-hybrid hub, re-run with the tier newly enabled -------
test_reenable_on_preexisting_nonhybrid_hub() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  # No pre-existing firewall-rules state: this hub predates the hybrid tier,
  # so the two rules don't exist yet even though the VM/hub itself does.
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  hybrid_apply_vm_tag "scion-hub-${HUB}" "us-central1-b" "$PROJECT" "$HUB"
  assert_eq "1" "$(gcloud_log | grep -c "firewall-rules create ${ALLOW_NAME} " || true)" \
    "hybrid rules should be created fresh on an existing, previously non-hybrid hub"
  assert_eq "1" "$(gcloud_log | grep -c 'instances add-tags' || true)" \
    "the existing VM should get the tag idempotently"
}

# =====================================================================
# Teardown
# =====================================================================

test_teardown_check_all_marked() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  seed_firewall_rule_desc_only "$DENY_NAME" "$MARKER"
  hybrid_teardown_check "$HUB" "$PROJECT"
  assert_eq "false" "$HYBRID_TEARDOWN_FAILED" "an all-marked pair should not fail teardown"
  assert_eq "2" "${#HYBRID_TEARDOWN_DELETE[@]}" "both marked rules should be queued for deletion"
  assert_eq "0" "${#HYBRID_TEARDOWN_SKIP[@]}" "nothing should be skipped"

  hybrid_teardown_delete "$PROJECT"
  local log
  log="$(gcloud_log)"
  assert_eq "1" "$(echo "$log" | grep -c "firewall-rules delete ${ALLOW_NAME}" || true)" \
    "the allow rule should be deleted"
  assert_eq "1" "$(echo "$log" | grep -c "firewall-rules delete ${DENY_NAME}" || true)" \
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
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  seed_firewall_rule_desc_only "$DENY_NAME" "unrelated-rule-not-ours"
  hybrid_teardown_check "$HUB" "$PROJECT"
  assert_eq "true" "$HYBRID_TEARDOWN_FAILED" "an unmarked name match must fail the teardown run"
  assert_eq "1" "${#HYBRID_TEARDOWN_DELETE[@]}" "the marked rule is still queued"
  assert_eq "1" "${#HYBRID_TEARDOWN_SKIP[@]}" "the unmarked rule is listed as SKIPPED"
  assert_eq "$DENY_NAME" "${HYBRID_TEARDOWN_SKIP[0]}" "the deny rule is the one skipped"
}

test_teardown_delete_only_deletes_queued() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  hybrid_teardown_check "$HUB" "$PROJECT"
  hybrid_teardown_delete "$PROJECT"
  assert_true "$([[ -f "${GCLOUD_STUB_STATE_DIR}/firewall-rules/${ALLOW_NAME}.json" ]] && echo false || echo true)" \
    "the marked rule should actually be deleted from stub state"
}

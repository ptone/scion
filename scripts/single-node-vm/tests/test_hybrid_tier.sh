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
  hybrid_read_config "$PROJECT" "$HUB"
  assert_eq "false" "$HYBRID_ENABLED" "tier should be off when gke_target.name is absent"
  assert_eq "0" "$(gcloud_call_count)" "no gcloud call should happen reading config alone"
}

# --- config: project mismatch is refused actionably ------------------------
test_config_project_mismatch_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  CONFIG["gke_target.location"]="us-central1"
  CONFIG["gke_target.project"]="other-project"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "mismatched gke_target.project must fail the run"
  assert_contains "$RUN_OUTPUT" "other-project" "error should name the offending project"
}

# --- config: missing location is refused actionably -------------------------
test_config_missing_location_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "missing gke_target.location must fail the run"
  # Specifically the "is required" check, not merely that the later regex
  # validation also happens to reject an empty string with a message that
  # happens to also contain the word "location" (which would still pass a
  # bare substring check without actually pinning this specific check).
  assert_contains "$RUN_OUTPUT" "is required when gke_target.name is set" \
    "error should be the missing-field message, not some other check that also mentions location"
}

# --- config: same-project cluster enables the tier --------------------------
test_config_enables_tier() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  # shellcheck disable=SC2034 # read by config_get (harness.sh) via hybrid_read_config
  CONFIG["gke_target.location"]="us-central1"
  hybrid_read_config "$PROJECT" "$HUB"
  assert_eq "true" "$HYBRID_ENABLED" "tier should be on when name+location are set"
  assert_eq "mycluster" "$GKE_NAME" "GKE_NAME should come from config"
  assert_eq "$PROJECT" "$GKE_PROJECT" "GKE_PROJECT should default to the hub's project"
  assert_eq "scion-hub-${HUB}" "$GKE_NAMESPACE" "GKE_NAMESPACE should default to scion-hub-<hub> when not set in config"
  assert_eq "scion-hub-${HUB}-shared" "$GKE_PVC_NAME" "GKE_PVC_NAME should default to scion-hub-<hub>-shared when not set in config"
}

test_config_namespace_and_pvc_name_from_config() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  # shellcheck disable=SC2034 # read by config_get (harness.sh) via hybrid_read_config
  CONFIG["gke_target.location"]="us-central1"
  CONFIG["gke_target.namespace"]="custom-ns"
  CONFIG["gke_target.pvc_name"]="custom-pvc"
  hybrid_read_config "$PROJECT" "$HUB"
  assert_eq "custom-ns" "$GKE_NAMESPACE" "an explicit gke_target.namespace must override the default"
  assert_eq "custom-pvc" "$GKE_PVC_NAME" "an explicit gke_target.pvc_name must override the default"
}

test_config_namespace_invalid_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  # shellcheck disable=SC2034 # read by config_get (harness.sh) via hybrid_read_config
  CONFIG["gke_target.location"]="us-central1"
  CONFIG["gke_target.namespace"]="Not_Valid"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an invalid namespace name must be refused"
  assert_contains "$RUN_OUTPUT" "gke_target.namespace" "error should name the offending field"
}

test_config_pvc_name_invalid_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  # shellcheck disable=SC2034 # read by config_get (harness.sh) via hybrid_read_config
  CONFIG["gke_target.location"]="us-central1"
  CONFIG["gke_target.pvc_name"]="Not_Valid"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an invalid PVC name must be refused"
  assert_contains "$RUN_OUTPUT" "gke_target.pvc_name" "error should name the offending field"
}

test_k8s_ensure_objects_uses_hybrid_read_config_override() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  # shellcheck disable=SC2034 # read by config_get (harness.sh) via hybrid_read_config
  CONFIG["gke_target.location"]="us-central1"
  CONFIG["gke_target.namespace"]="custom-ns"
  CONFIG["gke_target.pvc_name"]="custom-pvc"
  hybrid_read_config "$PROJECT" "$HUB"
  hybrid_k8s_setup_kubeconfig
  hybrid_k8s_ensure_objects "$HUB" "$K8S_VM_IP"
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/namespace/custom-ns.json" ]] && echo true || echo false)" \
    "the namespace set via hybrid_read_config (interactive-prompt-capable) must be the one actually used, not just the default"
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/pvc/custom-ns__custom-pvc.json" ]] && echo true || echo false)" \
    "the PVC name set via hybrid_read_config must be the one actually used"
  rm -f "$HYBRID_KUBECONFIG"
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
  assert_eq "10.128.0.0/20" "$GKE_NODE_SUBNET_CIDR" "should discover the node subnet's primary CIDR for a Standard cluster"
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
  assert_eq "10.128.0.0/20" "$GKE_NODE_SUBNET_CIDR" "should discover the node subnet's primary CIDR for an Autopilot cluster too"
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

# --- Node subnet discovery (the NFS export's client CIDR) -----------------

test_discover_node_subnet_cidr_from_explicit_fixture() {
  fresh_gcloud_state
  GKE_NAME="subnetcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "subnetcluster" "$NETWORK" "mig-x"
  seed_mig "mig-x" "template-x"
  seed_template "template-x" "gke-subnetcluster-x-node"
  seed_subnet "default-subnet" "10.4.0.0/22"
  hybrid_discover "$NETWORK"
  assert_eq "10.4.0.0/22" "$GKE_NODE_SUBNET_CIDR" "should discover the subnet's actual primary CIDR, not a hardcoded default"
}

test_discover_node_subnet_region_from_zonal_location() {
  fresh_gcloud_state
  GKE_NAME="zonalcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1-a"
  seed_cluster "zonalcluster" "$NETWORK" "mig-x"
  seed_mig "mig-x" "template-x"
  seed_template "template-x" "gke-zonalcluster-x-node"
  hybrid_discover "$NETWORK"
  assert_eq "1" "$(gcloud_log | grep -c 'networks subnets describe default-subnet --region=us-central1 ' || true)" \
    "a zonal location (us-central1-a) must resolve to its region (us-central1) for the subnet lookup, not be passed through as-is"
}

test_discover_node_subnet_describe_failure_refused() {
  fresh_gcloud_state
  GKE_NAME="subnetfailcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "subnetfailcluster" "$NETWORK" "mig-x"
  seed_mig "mig-x" "template-x"
  seed_template "template-x" "gke-subnetfailcluster-x-node"
  set_subnet_describe_will_fail "default-subnet"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unreadable node subnet must fail discovery"
  assert_contains "$RUN_OUTPUT" "default-subnet" "error should name the subnet"
}

test_discover_node_subnet_refuses_zero_slash_zero() {
  fresh_gcloud_state
  GKE_NAME="wideopencluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "wideopencluster" "$NETWORK" "mig-x"
  seed_mig "mig-x" "template-x"
  seed_template "template-x" "gke-wideopencluster-x-node"
  seed_subnet "default-subnet" "0.0.0.0/0"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "0.0.0.0/0 must never be accepted as an NFS export client range"
}

test_discover_node_subnet_refuses_broader_than_slash_8() {
  fresh_gcloud_state
  GKE_NAME="toobroadcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "toobroadcluster" "$NETWORK" "mig-x"
  seed_mig "mig-x" "template-x"
  seed_template "template-x" "gke-toobroadcluster-x-node"
  seed_subnet "default-subnet" "10.0.0.0/7"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "anything broader than /8 must be refused"
}

test_discover_node_subnet_accepts_slash_8_boundary() {
  fresh_gcloud_state
  GKE_NAME="boundarycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "boundarycluster" "$NETWORK" "mig-x"
  seed_mig "mig-x" "template-x"
  seed_template "template-x" "gke-boundarycluster-x-node"
  seed_subnet "default-subnet" "10.0.0.0/8"
  hybrid_discover "$NETWORK"
  assert_eq "10.0.0.0/8" "$GKE_NODE_SUBNET_CIDR" "/8 itself is the narrowest allowed refusal boundary, so it must be accepted"
}

# =====================================================================
# NFS export: the fsid and export-line renderers are pure functions
# (no gcloud or SSH calls), directly unit-testable.
# =====================================================================

test_nfs_export_line_renders_expected_options() {
  local line
  line="$(hybrid_nfs_export_line "/srv/scion-shared" "10.128.0.0/20" "6001" "6000" "abc123")"
  assert_eq "/srv/scion-shared 10.128.0.0/20(rw,sync,no_subtree_check,all_squash,anonuid=6001,anongid=6000,sec=sys,fsid=abc123)" \
    "$line" "the export line must render every required option in the expected shape"
}

test_nfs_export_line_uses_given_cidr_not_hardcoded() {
  local line
  line="$(hybrid_nfs_export_line "/srv/scion-shared" "10.4.0.0/22" "6001" "6000" "abc123")"
  assert_contains "$line" "10.4.0.0/22" "the export line must use the actual discovered CIDR"
}

test_nfs_export_line_anonuid_and_anongid_are_distinct_values() {
  local line
  line="$(hybrid_nfs_export_line "/srv/scion-shared" "10.128.0.0/20" "6001" "1000" "abc123")"
  assert_contains "$line" "anonuid=6001" "anonuid must be the squash uid, not the scion gid"
  assert_contains "$line" "anongid=1000" "anongid must be the scion group's gid -- there is no separate squash group"
}

test_nfs_fsid_deterministic_for_same_hub() {
  local first second
  first="$(hybrid_nfs_fsid "demohub")"
  second="$(hybrid_nfs_fsid "demohub")"
  assert_eq "$first" "$second" "the fsid must be stable across calls (and so across re-runs) for the same hub"
}

test_nfs_fsid_differs_per_hub() {
  local hub1 hub2
  hub1="$(hybrid_nfs_fsid "hub-one")"
  hub2="$(hybrid_nfs_fsid "hub-two")"
  assert_true "$([[ "$hub1" != "$hub2" ]] && echo true || echo false)" \
    "different hubs must never collide on the same fsid"
}

test_nfs_fsid_is_a_well_formed_uuid() {
  local fsid
  fsid="$(hybrid_nfs_fsid "demohub")"
  assert_true "$([[ "$fsid" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] && echo true || echo false)" \
    "the fsid must be a well-formed UUID, which nfs-utils accepts for fsid="
}

# =====================================================================
# NFS squash identity and export remote scripts, and the Cloud Run
# label decision: the parts of the tier-gated remote step that would
# otherwise be unreachable from the wiring tests (they run after the
# point the create-mode sentinel stops). Rendering them as pure
# functions (identity/export scripts) or gcloud-stub-testable functions
# (the Cloud Run decision) closes that gap.
# =====================================================================

test_nfs_squash_identity_script_has_set_euo_pipefail() {
  local script
  script="$(hybrid_nfs_squash_identity_script "scion-nfs")"
  assert_eq "set -euo pipefail" "$(echo "$script" | head -1)" \
    "the remote script must fail closed on any unexpected error, not silently continue"
}

test_nfs_squash_identity_script_useradd_guarded_by_exists() {
  local script
  script="$(hybrid_nfs_squash_identity_script "scion-nfs")"
  assert_contains "$script" "id scion-nfs >/dev/null 2>&1 ||" \
    "useradd must only run when the identity doesn't already exist"
  assert_contains "$script" "useradd -r -M -N -g scion -s /usr/sbin/nologin scion-nfs" \
    "useradd must create a system account, no home, no user-private group, primary group scion, no login shell"
}

test_nfs_squash_identity_script_reads_ids() {
  local script
  script="$(hybrid_nfs_squash_identity_script "scion-nfs")"
  # shellcheck disable=SC2016 # asserting the literal remote-script text, not expanding locally
  assert_contains "$script" 'SQUASH_UID=$(id -u scion-nfs)' "must read the squash uid back numerically"
  # shellcheck disable=SC2016 # asserting the literal remote-script text, not expanding locally
  assert_contains "$script" 'SCION_UID=$(id -u scion)' "must read the broker's own uid for the comparison"
  # shellcheck disable=SC2016 # asserting the literal remote-script text, not expanding locally
  assert_contains "$script" 'SCION_GID=$(getent group scion | cut -d: -f3)' \
    "must read the scion group's gid for anongid, not assume it matches the user's own gid"
}

test_nfs_squash_identity_script_asserts_uid_mismatch() {
  local script
  script="$(hybrid_nfs_squash_identity_script "scion-nfs")"
  # shellcheck disable=SC2016 # asserting the literal remote-script text, not expanding locally
  assert_contains "$script" 'if [ "$SQUASH_UID" = "$SCION_UID" ]; then' \
    "the mismatch check must run unconditionally, not just log a warning"
  assert_contains "$script" "The NFS squash uid must not equal the scion (broker) uid." \
    "the failure message must explain why"
  assert_contains "$script" $'  exit 1\nfi' "an equal uid must exit non-zero, not just warn and continue"
}

test_nfs_export_script_has_set_euo_pipefail() {
  local script
  script="$(hybrid_nfs_export_script "/srv/scion-shared" "10.128.0.0/20" "6001" "6000" "abc123" "demohub")"
  assert_eq "set -euo pipefail" "$(echo "$script" | head -1)" \
    "the remote script must fail closed on any unexpected error"
}

test_nfs_export_script_root_owner_and_mode() {
  local script
  script="$(hybrid_nfs_export_script "/srv/scion-shared" "10.128.0.0/20" "6001" "6000" "abc123" "demohub")"
  assert_contains "$script" "sudo mkdir -p /srv/scion-shared" "must create the export root"
  assert_contains "$script" "sudo chown scion:scion /srv/scion-shared" "the export root must be owned scion:scion"
  assert_contains "$script" "sudo chmod 2755 /srv/scion-shared" \
    "mode 2755 is what keeps the squash uid from writing the export root"
}

test_nfs_export_script_writes_exports_file_using_the_render_function() {
  local script expected_line
  script="$(hybrid_nfs_export_script "/srv/scion-shared" "10.128.0.0/20" "6001" "6000" "abc123" "demohub")"
  expected_line="$(hybrid_nfs_export_line "/srv/scion-shared" "10.128.0.0/20" "6001" "6000" "abc123")"
  assert_contains "$script" "echo '${expected_line}' | sudo tee /etc/exports.d/scion-hub-demohub.exports" \
    "the exports file must be this hub's own file, containing exactly hybrid_nfs_export_line's output"
}

test_nfs_export_script_exportfs_and_enable_service() {
  local script
  script="$(hybrid_nfs_export_script "/srv/scion-shared" "10.128.0.0/20" "6001" "6000" "abc123" "demohub")"
  assert_contains "$script" "sudo exportfs -ra" "must re-export after writing the file"
  assert_contains "$script" "sudo systemctl enable --now nfs-kernel-server" "must enable and start the service"
}

test_nfs_export_script_installs_server_package_if_needed() {
  local script
  script="$(hybrid_nfs_export_script "/srv/scion-shared" "10.128.0.0/20" "6001" "6000" "abc123" "demohub")"
  assert_contains "$script" "if ! dpkg -s nfs-kernel-server >/dev/null 2>&1; then" \
    "the install must be guarded, not run unconditionally on every re-run"
  assert_contains "$script" "apt-get install -y nfs-kernel-server" "must actually install the package"
}

# =====================================================================
# settings.yaml's server.shared_dir_storage block: the render function
# itself, and the conditional-splice mechanism deploy.sh uses to include
# it only when the tier is on, with zero bytes of difference otherwise.
# =====================================================================

test_settings_shared_dir_storage_yaml_fields() {
  local yaml
  yaml="$(hybrid_settings_shared_dir_storage_yaml "10.128.0.5" "/srv/scion-shared" "scion-hub-demohub-shared")"
  assert_contains "$yaml" "backend: nfs" "backend must be nfs"
  assert_contains "$yaml" 'mount_root: "/srv/scion-shared"' "mount_root must be the export root"
  assert_contains "$yaml" 'subpath_root: "projects"' "subpath_root must be the fixed projects subdirectory"
  assert_contains "$yaml" 'server: "10.128.0.5"' "the share's server must be the VM's internal IP"
  assert_contains "$yaml" 'export: "/srv/scion-shared"' "the share's export path must be the export root"
  assert_contains "$yaml" 'pv_name: "scion-hub-demohub-shared"' "the share's pv_name must be the PV this hub's pods bind to"
  assert_contains "$yaml" 'id: "shared"' "the share must have a stable id"
}

test_settings_shared_dir_storage_yaml_indented_under_server() {
  local yaml first_line
  yaml="$(hybrid_settings_shared_dir_storage_yaml "10.128.0.5" "/srv/scion-shared" "scion-hub-demohub-shared")"
  first_line="$(echo "$yaml" | head -1)"
  assert_eq "  shared_dir_storage:" "$first_line" \
    "must be indented two spaces, to nest directly under server: alongside hub:/storage:/etc."
}

# The exact conditional-splice pattern deploy.sh uses to include this
# block in settings.yaml only when it's non-empty, with no byte of
# difference (not even a blank line) when it's empty -- mirrored here
# rather than driving deploy.sh's own SSH-bound heredoc directly.
test_settings_shared_dir_storage_splice_tier_off_is_byte_identical() {
  local rendered
  local hybrid_block=""
  rendered="$(cat <<EOF
  auth:
    mode: dev
${hybrid_block:+${hybrid_block}
}  listen_port: 8080
EOF
)"
  assert_eq "$(printf '  auth:\n    mode: dev\n  listen_port: 8080')" "$rendered" \
    "tier off must produce exactly the pre-existing text, with no blank line or other artifact"
}

test_settings_shared_dir_storage_splice_tier_on_includes_block() {
  local rendered
  local hybrid_block
  hybrid_block="$(hybrid_settings_shared_dir_storage_yaml "10.128.0.5" "/srv/scion-shared" "scion-hub-demohub-shared")"
  rendered="$(cat <<EOF
  auth:
    mode: dev
${hybrid_block:+${hybrid_block}
}  listen_port: 8080
EOF
)"
  assert_contains "$rendered" "shared_dir_storage:" "tier on must splice the block in"
  assert_contains "$rendered" "  listen_port: 8080" "the rest of the block must still follow, unchanged"
}

test_cloud_run_label_args_new_service_gets_label() {
  fresh_gcloud_state
  local args
  args="$(hybrid_cloud_run_label_args "demohub-iap-proxy" "$PROJECT" "us-central1" "demohub")"
  assert_eq "--labels=scion-deployment=demohub" "$args" \
    "a not-yet-existing service (NOT_FOUND) is the first-create case and must get the marker"
}

test_cloud_run_label_args_existing_service_no_label() {
  fresh_gcloud_state
  seed_run_service_exists "demohub-iap-proxy"
  local args
  args="$(hybrid_cloud_run_label_args "demohub-iap-proxy" "$PROJECT" "us-central1" "demohub")"
  assert_eq "" "$args" "an already-existing service is a redeploy and must not be (re-)labeled"
}

test_cloud_run_label_args_describe_error_fails_safe_no_label() {
  fresh_gcloud_state
  set_run_service_describe_error "demohub-iap-proxy"
  local args
  args="$(hybrid_cloud_run_label_args "demohub-iap-proxy" "$PROJECT" "us-central1" "demohub")"
  assert_eq "" "$args" \
    "a describe error that isn't NOT_FOUND must fail safe toward assuming the service exists (no label), not toward labeling it"
}

test_cloud_run_label_args_permission_masked_not_found_fails_safe() {
  fresh_gcloud_state
  set_run_service_describe_permission_masked "demohub-iap-proxy"
  local args
  args="$(hybrid_cloud_run_label_args "demohub-iap-proxy" "$PROJECT" "us-central1" "demohub")"
  assert_eq "" "$args" \
    "a permission-denied message that also happens to say 'not found' must never be read as NOT_FOUND"
}

# =====================================================================
# Not-found matching: tightened to gcloud's/kubectl's own specific
# signals, never a bare "not found" substring, since some permission-
# denied responses are deliberately worded to avoid confirming whether a
# resource exists (for example "...not found or permission denied").
# =====================================================================

test_gcloud_not_found_matches_NOT_FOUND_token() {
  assert_true "$(_hybrid_gcloud_not_found 'gcloud-stub: NOT_FOUND: Cluster x not found' && echo true || echo false)" \
    "a NOT_FOUND status token must be recognized"
}

test_gcloud_not_found_matches_404_code() {
  assert_true "$(_hybrid_gcloud_not_found 'ResponseError: code=404, message=Not Found' && echo true || echo false)" \
    "an HTTP 404 code must be recognized"
}

test_gcloud_not_found_matches_requested_entity_message() {
  assert_true "$(_hybrid_gcloud_not_found 'gcloud-stub: NOT_FOUND: Requested entity was not found.' && echo true || echo false)" \
    "the literal 'Requested entity was not found' message must be recognized"
}

test_gcloud_not_found_rejects_permission_masked_message() {
  assert_true "$(_hybrid_gcloud_not_found 'Cluster x not found or permission denied' && echo false || echo true)" \
    "a permission-denied message that also happens to say 'not found' must never be read as NOT_FOUND"
}

test_gcloud_not_found_rejects_forbidden_message() {
  assert_true "$(_hybrid_gcloud_not_found 'ERROR: 403 Forbidden: resource not found' && echo false || echo true)" \
    "a forbidden message must never be read as NOT_FOUND, even if it also mentions 'not found'"
}

test_gcloud_not_found_rejects_bare_substring() {
  assert_true "$(_hybrid_gcloud_not_found 'the widget was not found in the drawer' && echo false || echo true)" \
    "a bare 'not found' substring with none of gcloud's own signals must not match"
}

test_kubectl_not_found_matches_NotFound_reason() {
  assert_true "$(_hybrid_kubectl_not_found 'Error from server (NotFound): persistentvolumes "x" not found' && echo true || echo false)" \
    "kubectl's own (NotFound) reason token must be recognized"
}

test_kubectl_not_found_rejects_permission_masked_message() {
  assert_true "$(_hybrid_kubectl_not_found 'Error from server: pv "x" not found or permission denied' && echo false || echo true)" \
    "a permission-denied message that also happens to say 'not found' must never be read as NotFound"
}

# =====================================================================
# Kubernetes objects: naming, kubeconfig setup, manifest rendering,
# create/refuse/drift, and teardown. No test here contacts a real
# cluster; the stub kubectl records every invocation and serves
# fixtures the same way the stub gcloud does.
# =====================================================================

K8S_HUB="demohub"
K8S_NS="scion-hub-${K8S_HUB}"
K8S_PVC="scion-hub-${K8S_HUB}-shared"
K8S_PV="scion-hub-${K8S_HUB}-shared"
K8S_VM_IP="10.128.0.5"

test_k8s_pv_name() {
  assert_eq "scion-hub-demohub-shared" "$(hybrid_k8s_pv_name "demohub")" "PV name must be scion-hub-<hub>-shared"
}
test_k8s_default_namespace() {
  assert_eq "scion-hub-demohub" "$(hybrid_k8s_default_namespace "demohub")" "default namespace must be scion-hub-<hub>"
}
test_k8s_default_pvc_name() {
  assert_eq "scion-hub-demohub-shared" "$(hybrid_k8s_default_pvc_name "demohub")" "default PVC name must be scion-hub-<hub>-shared"
}

test_k8s_pv_manifest_fields() {
  local yaml
  yaml="$(hybrid_k8s_pv_manifest "$K8S_PV" "$K8S_HUB" "$K8S_VM_IP" "/srv/scion-shared" "$K8S_NS" "$K8S_PVC")"
  assert_contains "$yaml" "name: ${K8S_PV}" "must name the PV"
  assert_contains "$yaml" "scion-deployment: ${K8S_HUB}" "must carry the marker label"
  assert_contains "$yaml" "server: ${K8S_VM_IP}" "must point the NFS server at the VM's IP"
  assert_contains "$yaml" "path: /srv/scion-shared" "must point at the export root"
  assert_contains "$yaml" "persistentVolumeReclaimPolicy: Retain" "reclaim policy must be Retain"
  assert_contains "$yaml" 'storageClassName: ""' "must never be dynamically provisioned"
  assert_contains "$yaml" "- ReadWriteMany" "access mode must be RWX"
  assert_contains "$yaml" "- nfsvers=4.1" "mount options must pin the NFS version"
  assert_contains "$yaml" "- hard" "mount options must use hard, not soft"
  assert_contains "$yaml" "namespace: ${K8S_NS}" "claimRef must pin the namespace"
  assert_contains "$yaml" "name: ${K8S_PVC}" "claimRef must pin the PVC name"
}

test_k8s_namespace_manifest_fields() {
  local yaml
  yaml="$(hybrid_k8s_namespace_manifest "$K8S_NS" "$K8S_HUB")"
  assert_contains "$yaml" "kind: Namespace" "must be a Namespace object"
  assert_contains "$yaml" "name: ${K8S_NS}" "must name the namespace"
  assert_contains "$yaml" "scion-deployment: ${K8S_HUB}" "must carry the marker label"
}

test_k8s_pvc_manifest_fields() {
  local yaml
  yaml="$(hybrid_k8s_pvc_manifest "$K8S_PVC" "$K8S_NS" "$K8S_HUB" "$K8S_PV")"
  assert_contains "$yaml" "name: ${K8S_PVC}" "must name the PVC"
  assert_contains "$yaml" "namespace: ${K8S_NS}" "must be in the target namespace"
  assert_contains "$yaml" "scion-deployment: ${K8S_HUB}" "must carry the marker label"
  assert_contains "$yaml" "volumeName: ${K8S_PV}" "must bind to the expected PV"
  assert_contains "$yaml" 'storageClassName: ""' "must never be dynamically provisioned"
  assert_contains "$yaml" "- ReadWriteMany" "access mode must be RWX"
}

test_k8s_setup_kubeconfig_uses_distinct_temp_files() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  local first="$HYBRID_KUBECONFIG"
  hybrid_k8s_setup_kubeconfig
  local second="$HYBRID_KUBECONFIG"
  assert_true "$([[ "$first" != "$second" ]] && echo true || echo false)" "each setup call must use its own fresh temp file"
  assert_not_contains "$first" ".kube" "must never point at the operator's default kubeconfig location"
  rm -f "$first" "$second"
}

test_k8s_setup_kubeconfig_missing_kubectl_fails() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  local old_path="$PATH"
  # shellcheck disable=SC2123 # deliberately hiding kubectl for this one test
  PATH="/nonexistent-bin-dir-for-this-test"
  run_expect_fail hybrid_k8s_setup_kubeconfig
  PATH="$old_path"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "missing kubectl must fail the run"
  assert_contains "$RUN_OUTPUT" "kubectl is required" "error should say why"
}

test_k8s_setup_kubeconfig_get_credentials_failure() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  set_get_credentials_will_fail
  run_expect_fail hybrid_k8s_setup_kubeconfig
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a get-credentials failure must fail the run"
  assert_contains "$RUN_OUTPUT" "Could not get credentials" "error should explain what failed"
}

test_k8s_ensure_creates_all_three_when_absent() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/namespace/${K8S_NS}.json" ]] && echo true || echo false)" "namespace must be created"
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/pv/${K8S_PV}.json" ]] && echo true || echo false)" "PV must be created"
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/pvc/${K8S_NS}__${K8S_PVC}.json" ]] && echo true || echo false)" "PVC must be created"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_ensure_refuses_unmarked_pv() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pv_unmarked "$K8S_PV"
  run_expect_fail hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unmarked PV must refuse the run"
  assert_contains "$RUN_OUTPUT" "without this deployment's marker" "error should explain why"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_ensure_refuses_unmarked_pvc() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pvc_unmarked "$K8S_PVC" "$K8S_NS"
  run_expect_fail hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unmarked PVC must refuse the run"
  assert_contains "$RUN_OUTPUT" "without this deployment's marker" "error should explain why"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_ensure_unmarked_namespace_used_not_labeled_not_refused() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_namespace_unmarked "$K8S_NS"
  hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_eq "2" "$(kubectl_log | grep -c 'apply -f' || true)" \
    "only the PV and PVC should be applied -- the unmarked-but-usable namespace must not be re-applied (labeled)"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_ensure_pv_drift_ip_change_fails_with_remediation() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pv "$K8S_PV" "$K8S_HUB" "10.128.0.99" "/srv/scion-shared" "$K8S_NS" "$K8S_PVC"
  run_expect_fail hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a PV whose server IP no longer matches must fail the run"
  assert_contains "$RUN_OUTPUT" "server:" "error should name the drifted field"
  assert_contains "$RUN_OUTPUT" "kubectl delete pvc" "remediation must include deleting the PVC first"
  assert_contains "$RUN_OUTPUT" "kubectl delete pv" "remediation must include deleting the PV"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_ensure_pvc_drift_wrong_volume_fails() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pv "$K8S_PV" "$K8S_HUB" "$K8S_VM_IP" "/srv/scion-shared" "$K8S_NS" "$K8S_PVC"
  seed_k8s_pvc "$K8S_PVC" "$K8S_NS" "$K8S_HUB" "some-other-pv"
  run_expect_fail hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a PVC bound to the wrong PV must fail the run"
  assert_contains "$RUN_OUTPUT" "some-other-pv" "error should name the actual, wrong binding"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_ensure_matching_marked_objects_are_reused_without_recreating() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_namespace "$K8S_NS" "$K8S_HUB"
  seed_k8s_pv "$K8S_PV" "$K8S_HUB" "$K8S_VM_IP" "/srv/scion-shared" "$K8S_NS" "$K8S_PVC"
  seed_k8s_pvc "$K8S_PVC" "$K8S_NS" "$K8S_HUB" "$K8S_PV"
  hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_eq "0" "$(kubectl_log | grep -c 'apply -f' || true)" \
    "matching, marked objects must be reused, not recreated"
}

test_k8s_ensure_get_error_on_pv_fails_closed() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  set_k8s_get_error pv "$K8S_PV"
  run_expect_fail hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unknown PV state must fail closed, not be treated as absent"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_ensure_get_permission_masked_on_pv_fails_closed_not_absent() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  set_k8s_get_permission_masked pv "$K8S_PV"
  run_expect_fail hybrid_k8s_ensure_objects "$K8S_HUB" "$K8S_VM_IP"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" \
    "a permission-denied message that also says 'not found' must fail closed, not be treated as absent (which would create a duplicate PV)"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_teardown_check_all_marked_queues_all_three_in_order() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pvc "$K8S_PVC" "$K8S_NS" "$K8S_HUB" "$K8S_PV"
  seed_k8s_pv "$K8S_PV" "$K8S_HUB" "$K8S_VM_IP" "/srv/scion-shared" "$K8S_NS" "$K8S_PVC"
  seed_k8s_namespace "$K8S_NS" "$K8S_HUB"
  hybrid_k8s_teardown_check "$K8S_HUB"
  assert_eq "false" "$HYBRID_K8S_TEARDOWN_FAILED" "all-marked must not fail the preflight"
  assert_eq "3" "${#HYBRID_K8S_TEARDOWN_DELETE[@]}" "all three objects must be queued"
  assert_eq "pvc" "${HYBRID_K8S_TEARDOWN_DELETE[0]:-}" "PVC must be first in deletion order"
  assert_eq "pv" "${HYBRID_K8S_TEARDOWN_DELETE[1]:-}" "PV must be second"
  assert_eq "namespace" "${HYBRID_K8S_TEARDOWN_DELETE[2]:-}" "namespace must be last"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_teardown_check_unmarked_pvc_aborts() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pvc_unmarked "$K8S_PVC" "$K8S_NS"
  hybrid_k8s_teardown_check "$K8S_HUB"
  assert_eq "true" "$HYBRID_K8S_TEARDOWN_FAILED" "an unmarked PVC must abort the whole teardown"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_teardown_check_unmarked_pv_aborts() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pv_unmarked "$K8S_PV"
  hybrid_k8s_teardown_check "$K8S_HUB"
  assert_eq "true" "$HYBRID_K8S_TEARDOWN_FAILED" "an unmarked PV must abort the whole teardown"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_teardown_check_unmarked_namespace_skipped_not_aborted() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_namespace_unmarked "$K8S_NS"
  hybrid_k8s_teardown_check "$K8S_HUB"
  assert_eq "false" "$HYBRID_K8S_TEARDOWN_FAILED" "an unmarked namespace must not abort -- using an existing one is allowed"
  local k found=false
  for k in ${HYBRID_K8S_TEARDOWN_DELETE[@]+"${HYBRID_K8S_TEARDOWN_DELETE[@]}"}; do
    [[ "$k" == "namespace" ]] && found=true
  done
  assert_eq "false" "$found" "an unmarked namespace must never be queued for deletion"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_teardown_check_get_error_aborts() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  set_k8s_get_error namespace "$K8S_NS"
  hybrid_k8s_teardown_check "$K8S_HUB"
  assert_eq "true" "$HYBRID_K8S_TEARDOWN_FAILED" "an unknown check result must abort, not be treated as absent"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_teardown_delete_stops_at_first_failure() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pvc "$K8S_PVC" "$K8S_NS" "$K8S_HUB" "$K8S_PV"
  seed_k8s_pv "$K8S_PV" "$K8S_HUB" "$K8S_VM_IP" "/srv/scion-shared" "$K8S_NS" "$K8S_PVC"
  seed_k8s_namespace "$K8S_NS" "$K8S_HUB"
  set_k8s_delete_will_fail pvc "${K8S_NS}__${K8S_PVC}"
  hybrid_k8s_teardown_check "$K8S_HUB"
  hybrid_k8s_teardown_delete
  assert_eq "3" "${#HYBRID_K8S_TEARDOWN_DELETE_FAILED[@]}" \
    "the failed PVC plus the PV and namespace queued behind it must all be recorded as not deleted"
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/pv/${K8S_PV}.json" ]] && echo true || echo false)" \
    "the PV must never be deleted after the PVC delete failed"
  assert_true "$([[ -f "${KUBECTL_STUB_STATE_DIR}/namespace/${K8S_NS}.json" ]] && echo true || echo false)" \
    "the namespace must never be deleted after the PVC delete failed"
  rm -f "$HYBRID_KUBECONFIG"
}

test_k8s_teardown_delete_all_succeed() {
  fresh_gcloud_state
  GKE_NAME="mycluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  hybrid_k8s_setup_kubeconfig
  seed_k8s_pvc "$K8S_PVC" "$K8S_NS" "$K8S_HUB" "$K8S_PV"
  seed_k8s_pv "$K8S_PV" "$K8S_HUB" "$K8S_VM_IP" "/srv/scion-shared" "$K8S_NS" "$K8S_PVC"
  seed_k8s_namespace "$K8S_NS" "$K8S_HUB"
  hybrid_k8s_teardown_check "$K8S_HUB"
  hybrid_k8s_teardown_delete
  assert_eq "0" "${#HYBRID_K8S_TEARDOWN_DELETE_FAILED[@]}" "nothing should be recorded as failed"
  assert_eq "3" "${#HYBRID_K8S_TEARDOWN_DELETED[@]}" "all three objects must be deleted"
  assert_true "$([[ ! -f "${KUBECTL_STUB_STATE_DIR}/pvc/${K8S_NS}__${K8S_PVC}.json" ]] && echo true || echo false)" "PVC must be gone"
  assert_true "$([[ ! -f "${KUBECTL_STUB_STATE_DIR}/pv/${K8S_PV}.json" ]] && echo true || echo false)" "PV must be gone"
  assert_true "$([[ ! -f "${KUBECTL_STUB_STATE_DIR}/namespace/${K8S_NS}.json" ]] && echo true || echo false)" "namespace must be gone"
  rm -f "$HYBRID_KUBECONFIG"
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

  assert_contains "$allow_line" "--network=${NETWORK}" "allow rule is on the hub's network"
  assert_contains "$allow_line" "--direction=INGRESS" "allow rule is ingress"
  assert_contains "$allow_line" "--action=ALLOW" "allow rule action"
  assert_contains "$allow_line" "--rules=tcp:2049" "allow rule port"
  assert_contains "$allow_line" "--source-tags=${GKE_NODE_TAG}" "allow rule sources from the discovered node tag"
  assert_contains "$allow_line" "--priority=900" "allow rule priority"

  assert_contains "$deny_line" "--network=${NETWORK}" "deny rule is on the hub's network"
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
  assert_eq "$DENY_NAME" "${HYBRID_TEARDOWN_SKIP[0]:-}" "the deny rule is the one skipped"
}

test_teardown_delete_only_deletes_queued() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  hybrid_teardown_check "$HUB" "$PROJECT"
  hybrid_teardown_delete "$PROJECT"
  assert_true "$([[ -f "${GCLOUD_STUB_STATE_DIR}/firewall-rules/${ALLOW_NAME}.json" ]] && echo false || echo true)" \
    "the marked rule should actually be deleted from stub state"
}

# --- Create order: deny before allow, so an allow rule can never exist
# without its paired deny. -------------------------------------------------
test_firewall_rules_created_deny_before_allow() {
  fresh_gcloud_state
  GKE_NODE_TAG="gke-democluster-abc12345-node"
  hybrid_ensure_firewall_rules "$HUB" "$PROJECT" "$NETWORK"
  local log allow_line_num deny_line_num
  log="$(gcloud_log)"
  allow_line_num="$(echo "$log" | grep -n "firewall-rules create ${ALLOW_NAME} " | head -1 | cut -d: -f1)"
  deny_line_num="$(echo "$log" | grep -n "firewall-rules create ${DENY_NAME} " | head -1 | cut -d: -f1)"
  assert_true "$([[ -n "$deny_line_num" && -n "$allow_line_num" && "$deny_line_num" -lt "$allow_line_num" ]] && echo true || echo false)" \
    "the deny rule must be created before the allow rule"
}

# --- Teardown delete order: allow before deny (the reverse of create). ---
test_teardown_delete_order_allow_before_deny() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  seed_firewall_rule_desc_only "$DENY_NAME" "$MARKER"
  hybrid_teardown_check "$HUB" "$PROJECT"
  hybrid_teardown_delete "$PROJECT"
  local log allow_line_num deny_line_num
  log="$(gcloud_log)"
  allow_line_num="$(echo "$log" | grep -n "firewall-rules delete ${ALLOW_NAME}" | head -1 | cut -d: -f1)"
  deny_line_num="$(echo "$log" | grep -n "firewall-rules delete ${DENY_NAME}" | head -1 | cut -d: -f1)"
  assert_true "$([[ -n "$allow_line_num" && -n "$deny_line_num" && "$allow_line_num" -lt "$deny_line_num" ]] && echo true || echo false)" \
    "the allow rule must be deleted before the deny rule"
}

# --- Teardown exact-marker matching: a prefix-colliding hub name, or a
# marker with trailing text, must both be treated as unmarked. -----------
test_teardown_marker_prefix_collision_is_skipped() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "scion-deployment=${HUB}2"
  hybrid_teardown_check "$HUB" "$PROJECT"
  assert_eq "true" "$HYBRID_TEARDOWN_FAILED" "a prefix-colliding marker must not be treated as a match"
  assert_eq "1" "${#HYBRID_TEARDOWN_SKIP[@]}" "the rule should be SKIPPED, not queued for deletion"
  assert_eq "0" "${#HYBRID_TEARDOWN_DELETE[@]}" "nothing should be queued for deletion"
}

test_teardown_marker_with_trailing_text_is_skipped() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "${MARKER} extra"
  hybrid_teardown_check "$HUB" "$PROJECT"
  assert_eq "true" "$HYBRID_TEARDOWN_FAILED" "a marker with trailing text must not be treated as a match"
  assert_eq "1" "${#HYBRID_TEARDOWN_SKIP[@]}" "the rule should be SKIPPED, not queued for deletion"
  assert_eq "0" "${#HYBRID_TEARDOWN_DELETE[@]}" "nothing should be queued for deletion"
}

# --- An unmarked rule with an otherwise fully matching spec must still be
# refused at the marker check -- proving the run actually stops there,
# not that some later check happens to also reject it. --------------------
test_firewall_rule_unmarked_but_fully_matching_spec_refused() {
  fresh_gcloud_state
  seed_firewall_rule_json "$ALLOW_NAME" "some-unrelated-description" "$NETWORK" "INGRESS" "ALLOW" "tcp" "2049" \
    "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900"
  run_expect_fail _hybrid_ensure_firewall_rule "$ALLOW_NAME" "$PROJECT" "$MARKER" \
    "default" "INGRESS" "ALLOW" "tcp:2049" "tag" "$DRIFT_NODE_TAG" "$TARGET_TAG" "900"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" \
    "an unmarked rule must be refused even when its spec fully matches"
  assert_eq "0" "$(gcloud_log | grep -c 'firewall-rules create' || true)" \
    "nothing should be created when an unmarked rule blocks the run"
}

# =====================================================================
# hybrid_teardown_preflight: the extracted, directly testable teardown
# safety check (classification printing + abort decision).
# =====================================================================

test_teardown_preflight_none_found_succeeds_silently() {
  fresh_gcloud_state
  local out
  out="$(hybrid_teardown_preflight "$HUB" "$PROJECT")"
  local rc=$?
  assert_eq "0" "$rc" "no rules present should not abort"
  assert_eq "" "$out" "nothing found means nothing to print"
}

test_teardown_preflight_marked_prints_found_and_succeeds() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  seed_firewall_rule_desc_only "$DENY_NAME" "$MARKER"
  local out
  out="$(hybrid_teardown_preflight "$HUB" "$PROJECT")"
  local rc=$?
  assert_eq "0" "$rc" "an all-marked pair should not abort"
  assert_contains "$out" "found (marked): ${ALLOW_NAME}" "should print the documented found-marked line"
  assert_contains "$out" "found (marked): ${DENY_NAME}" "should print the documented found-marked line"
}

test_teardown_preflight_unmarked_prints_skipped_and_aborts() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  seed_firewall_rule_desc_only "$DENY_NAME" "unrelated-rule-not-ours"
  run_expect_fail hybrid_teardown_preflight "$HUB" "$PROJECT"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unmarked match must abort"
  assert_contains "$RUN_OUTPUT" "found (marked): ${ALLOW_NAME}" "should still print the marked rule as found"
  assert_contains "$RUN_OUTPUT" "SKIPPED (unmarked): ${DENY_NAME}" "should print the documented SKIPPED line"
  assert_contains "$RUN_OUTPUT" "Refusing to tear down" "should explain the abort"
}

test_teardown_preflight_list_failure_aborts_with_no_classification() {
  fresh_gcloud_state
  set_firewall_list_will_fail
  run_expect_fail hybrid_teardown_preflight "$HUB" "$PROJECT"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a failed list call must abort"
  assert_not_contains "$RUN_OUTPUT" "found (marked)" "a list failure is not the same as finding nothing"
  assert_not_contains "$RUN_OUTPUT" "SKIPPED (unmarked)" "a list failure is not an unmarked-match abort"
  assert_contains "$RUN_OUTPUT" "aborting teardown rather than assuming none exist" "should explain that the failure itself is the reason"
}

# --- hybrid_teardown_delete: a real delete failure (rule still present
# afterward) must be reported as a failure, never as "deleted". ----------
test_teardown_delete_failure_is_reported_not_deleted() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  set_firewall_delete_will_fail "$ALLOW_NAME"
  hybrid_teardown_check "$HUB" "$PROJECT"
  hybrid_teardown_delete "$PROJECT"
  assert_eq "1" "${#HYBRID_TEARDOWN_DELETE_FAILED[@]}" "the failed delete should be recorded as failed"
  # shellcheck disable=SC2153 # HYBRID_TEARDOWN_DELETED, set by hybrid_teardown_delete (hybrid-tier.sh), not a typo of HYBRID_TEARDOWN_DELETE
  assert_eq "0" "${#HYBRID_TEARDOWN_DELETED[@]}" "the failed delete must not be recorded as deleted"
  assert_true "$([[ -f "${GCLOUD_STUB_STATE_DIR}/firewall-rules/${ALLOW_NAME}.json" ]] && echo true || echo false)" \
    "the rule should still be present in stub state after a failed delete"
}

# =====================================================================
# Single-field drift battery: from a fully matching allow-rule
# seed, change exactly one field, and check the drift message names that
# field, always offers the delete remediation, and offers the update
# remediation only for fields `update` can converge in place (the
# expected-type source value, target tags, priority) -- never for
# direction, action, ports/rule-shape, the other source type, service
# accounts, disabled, or network.
# =====================================================================

DRIFT_NODE_TAG="gke-x-node"

# drift_seed_allow [EXTRA seed_firewall_rule_json ARGS...] — seeds
# $ALLOW_NAME with every field at its expected value, then overridden by
# any extra positional args the caller appends (seed_firewall_rule_json's
# own argument order: DESC NETWORK DIRECTION ACTION PROTO PORTS
# SOURCE_TAGS SOURCE_RANGES TARGET_TAGS PRIORITY [SOURCE_SAS [TARGET_SAS
# [DEST_RANGES [DISABLED [EXTRA_PROTO [EXTRA_PORTS]]]]]]).
drift_seed_allow() {
  seed_firewall_rule_json "$ALLOW_NAME" "$@"
}

drift_check_allow() {
  run_expect_fail _hybrid_ensure_firewall_rule "$ALLOW_NAME" "$PROJECT" "$MARKER" \
    "default" "INGRESS" "ALLOW" "tcp:2049" "tag" "$DRIFT_NODE_TAG" "$TARGET_TAG" "900"
}

test_drift_field_direction() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "EGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "direction drift must fail"
  assert_contains "$RUN_OUTPUT" "direction:" "should name the direction field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_contains "$RUN_OUTPUT" "re-run deploy.sh" "an allow-rule delete remediation must also say to re-run deploy.sh, not just delete"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update cannot change direction"
}

test_drift_field_action() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "DENY" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "action drift must fail"
  assert_contains "$RUN_OUTPUT" "action:" "should name the action field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update cannot change action"
}

test_drift_field_ports() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "22" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "ports drift must fail"
  assert_contains "$RUN_OUTPUT" "ports:" "should name the ports field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update cannot change ports"
}

test_drift_field_extra_rule_entry() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900" \
    "" "" "" "false" "all" ""
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an extra allowed entry must fail"
  assert_contains "$RUN_OUTPUT" "ports:" "an extra rule entry changes the ports/rules signature"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update cannot remove a rule entry"
}

test_drift_field_source_tag_value() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "some-other-tag" "" "$TARGET_TAG" "900"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "source-tag drift must fail"
  assert_contains "$RUN_OUTPUT" "source tags:" "should name the source tags field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "a same-type source drift converges via update"
  assert_contains "$RUN_OUTPUT" "--source-tags=${DRIFT_NODE_TAG}" "the update remediation should restore the expected source tag"
}

test_drift_field_stray_source_ranges() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "0.0.0.0/0" "$TARGET_TAG" "900"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a stray sourceRanges must fail"
  assert_contains "$RUN_OUTPUT" "source ranges:" "should name the source ranges field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" \
    "update cannot clear a stray sourceRanges, so only delete may be offered"
}

test_drift_field_source_service_account() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900" \
    "sa@example.iam.gserviceaccount.com"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a source service account must fail"
  assert_contains "$RUN_OUTPUT" "source service accounts:" "should name the source service accounts field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update cannot clear a service account"
}

test_drift_field_disabled() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900" \
    "" "" "" "true"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a disabled rule must fail"
  assert_contains "$RUN_OUTPUT" "disabled:" "should name the disabled field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update is never offered for a disabled rule"
}

test_drift_field_priority() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "800"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "priority drift must fail"
  assert_contains "$RUN_OUTPUT" "priority:" "should name the priority field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "priority-only drift converges via update"
  assert_contains "$RUN_OUTPUT" "--priority=900" "the update remediation should restore the expected priority"
}

test_drift_field_target_tags() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "some-other-target" "900"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "target-tag drift must fail"
  assert_contains "$RUN_OUTPUT" "target tags:" "should name the target tags field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "target-tag-only drift converges via update"
  assert_contains "$RUN_OUTPUT" "--target-tags=${TARGET_TAG}" "the update remediation should restore the expected target tag"
}

test_drift_field_network() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "othernet" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "network drift must fail"
  assert_contains "$RUN_OUTPUT" "network:" "should name the network field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update cannot change network"
}

test_drift_field_matching_reuse_passes() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900"
  drift_check_allow
  assert_eq "0" "$RUN_EXIT_CODE" "a fully matching rule must be reused without error"
  assert_eq "0" "$(gcloud_log | grep -c 'firewall-rules create' || true)" "a fully matching rule must not be recreated"
}

# =====================================================================
# Discovery: regex anchoring and partial-read resilience
# =====================================================================

# The tag pattern is fully anchored: a tag that merely *contains*
# "gke-...-node" as a substring, rather than matching it exactly, must
# never be picked.
# Each of the three tests below seeds a template with exactly one
# anchor-violating tag and nothing else. Isolating them like this matters:
# a combined fixture with all three tags together still fails discovery
# if the anchoring is dropped entirely (it just fails via the
# "more than one candidate" path instead of "no candidate", since all
# three would then match as substrings), so a bare "did it fail"
# assertion can't actually tell a correctly anchored regex apart from a
# fully unanchored one. Isolated, a dropped anchor makes that one tag the
# sole candidate, so discovery *succeeds* instead of failing -- a
# difference these tests can and do assert on directly.

test_discover_anchor_rejects_tag_violating_both_anchors() {
  fresh_gcloud_state
  GKE_NAME="anchortest1"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "anchortest1" "$NETWORK" "mig-anchor"
  seed_mig "mig-anchor" "template-anchor"
  seed_template "template-anchor" "x-gke-foo-node-y"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" \
    "a tag matching the pattern only as a substring (violating both anchors) must not be accepted"
  assert_contains "$RUN_OUTPUT" "x-gke-foo-node-y" "error should list the tag as seen, not silently ignore it"
}

test_discover_anchor_rejects_tag_missing_end_anchor() {
  fresh_gcloud_state
  GKE_NAME="anchortest2"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "anchortest2" "$NETWORK" "mig-anchor"
  seed_mig "mig-anchor" "template-anchor"
  seed_template "template-anchor" "gke-foo-node-y"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" \
    "a tag whose start matches but doesn't end in -node must not be accepted"
  assert_contains "$RUN_OUTPUT" "gke-foo-node-y" "error should list the tag as seen, not silently ignore it"
}

test_discover_anchor_rejects_tag_missing_start_anchor() {
  fresh_gcloud_state
  GKE_NAME="anchortest3"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "anchortest3" "$NETWORK" "mig-anchor"
  seed_mig "mig-anchor" "template-anchor"
  seed_template "template-anchor" "x-gke-foo-node"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" \
    "a tag that ends in -node but doesn't start with gke- must not be accepted"
  assert_contains "$RUN_OUTPUT" "x-gke-foo-node" "error should list the tag as seen, not silently ignore it"
}

# One managed instance group whose template can't be read must refuse
# discovery even when another group in the same cluster has a valid tag,
# since the unreadable group could be hiding a second, different tag.
test_discover_partial_mig_read_failure_refused() {
  fresh_gcloud_state
  GKE_NAME="partialcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "partialcluster" "$NETWORK" "mig-unreadable" "mig-good"
  # mig-unreadable is deliberately never seeded via seed_mig, so the
  # stub's `instance-groups managed describe` fails for it. Even though
  # the readable sibling yields a single, unambiguous candidate, a
  # partial view could be hiding a second, different tag on the group
  # that couldn't be read, so this must still fail rather than guess.
  seed_mig "mig-good" "template-good"
  seed_template "template-good" "gke-partialcluster-good-node"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" \
    "an unreadable instance group must fail discovery even if a sibling group yields a candidate"
  assert_contains "$RUN_OUTPUT" "1 of 2" "error should count the unreadable instance group"
  assert_contains "$RUN_OUTPUT" "First error: gcloud-stub: managed instance group mig-unreadable not found" \
    "the first gcloud stderr line must be surfaced"
}

# When every managed instance group is unreadable, the failure message
# must say so rather than implying the tags simply didn't match.
test_discover_all_migs_unreadable_mentions_it() {
  fresh_gcloud_state
  GKE_NAME="deadcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "deadcluster" "$NETWORK" "mig-dead-1" "mig-dead-2"
  # Neither MIG is seeded, so both `instance-groups managed describe`
  # calls fail.
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "all-unreadable must still fail discovery"
  assert_contains "$RUN_OUTPUT" "could not be read" "the message should say instance groups could not be read"
  assert_contains "$RUN_OUTPUT" "2 of 2" "the message should count how many were unreadable"
  assert_contains "$RUN_OUTPUT" "First error: gcloud-stub: managed instance group mig-dead-1 not found" \
    "the first gcloud stderr line must be surfaced"
}

# drift_seed_deny / drift_check_deny — same pattern as the allow-side
# helpers above, for the deny rule's expected spec (range-type source,
# priority 950), used for the fields the allow-side battery can't reach:
# a stray sourceTags on a range-type rule, and the deny-specific
# order-preserving remediation.
drift_seed_deny() {
  seed_firewall_rule_json "$DENY_NAME" "$@"
}

drift_check_deny() {
  run_expect_fail _hybrid_ensure_firewall_rule "$DENY_NAME" "$PROJECT" "$MARKER" \
    "default" "INGRESS" "DENY" "tcp:2049" "range" "0.0.0.0/0" "$TARGET_TAG" "950"
}

test_drift_field_target_service_account() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900" \
    "" "sa@example.iam.gserviceaccount.com"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a target service account must fail"
  assert_contains "$RUN_OUTPUT" "target service accounts:" "should name the target service accounts field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update cannot clear a target service account"
}

test_drift_field_destination_ranges() {
  fresh_gcloud_state
  drift_seed_allow "$MARKER" "default" "INGRESS" "ALLOW" "tcp" "2049" "$DRIFT_NODE_TAG" "" "$TARGET_TAG" "900" \
    "" "" "192.0.2.0/24"
  drift_check_allow
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a destination range must fail"
  assert_contains "$RUN_OUTPUT" "destination ranges:" "should name the destination ranges field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "delete remediation must be present"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${ALLOW_NAME}" "update cannot clear a destination range"
}

# The deny rule's expected source type is "range" (0.0.0.0/0); a stray
# sourceTags value is the "other" source field for a range-type rule,
# the mirror image of test_drift_field_stray_source_ranges on the allow
# side, and it isn't reachable from an allow-seeded test.
test_drift_field_deny_stray_source_tags() {
  fresh_gcloud_state
  drift_seed_deny "$MARKER" "default" "INGRESS" "DENY" "tcp" "2049" "some-stray-tag" "0.0.0.0/0" "$TARGET_TAG" "950"
  drift_check_deny
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a stray sourceTags on the deny rule must fail"
  assert_contains "$RUN_OUTPUT" "source tags:" "should name the source tags field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${ALLOW_NAME}" "deny remediation deletes the allow rule first"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules delete ${DENY_NAME}" "deny remediation also deletes the deny rule"
  assert_not_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${DENY_NAME}" \
    "update cannot clear a stray sourceTags, so only delete may be offered"
}

# A deny rule narrowed to a specific range (rather than 0.0.0.0/0) is a
# same-type source drift, which does converge via update -- the mirror
# image of test_drift_field_source_tag_value on the allow side.
test_drift_field_deny_source_ranges_value() {
  fresh_gcloud_state
  drift_seed_deny "$MARKER" "default" "INGRESS" "DENY" "tcp" "2049" "" "10.0.0.0/8" "$TARGET_TAG" "950"
  drift_check_deny
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a narrowed deny source range must fail"
  assert_contains "$RUN_OUTPUT" "source ranges:" "should name the source ranges field"
  assert_contains "$RUN_OUTPUT" "gcloud compute firewall-rules update ${DENY_NAME}" "a same-type source drift on the deny rule converges via update"
  assert_contains "$RUN_OUTPUT" "--source-ranges=0.0.0.0/0" "the update remediation should restore the expected deny source"
}

# --- Deny-drift remediation preserves the allow-before-deny order
# deleting only the deny rule would leave tcp:2049 reachable
# through the allow rule with nothing to deny it. -------------------------
test_drift_deny_delete_only_remediation_preserves_order() {
  fresh_gcloud_state
  drift_seed_deny "$MARKER" "othernet" "INGRESS" "DENY" "tcp" "2049" "" "0.0.0.0/0" "$TARGET_TAG" "950"
  drift_check_deny
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "network drift on the deny rule must fail"
  assert_contains "$RUN_OUTPUT" "re-run deploy.sh" "should tell the operator to re-run afterward"
  local allow_line deny_line
  allow_line="$(echo "$RUN_OUTPUT" | grep -n "delete ${ALLOW_NAME}" | head -1 | cut -d: -f1)"
  deny_line="$(echo "$RUN_OUTPUT" | grep -n "delete ${DENY_NAME}" | head -1 | cut -d: -f1)"
  assert_true "$([[ -n "$allow_line" && -n "$deny_line" && "$allow_line" -lt "$deny_line" ]] && echo true || echo false)" \
    "the remediation must delete the allow rule before the deny rule"
}

# =====================================================================
# Teardown delete: stop at the first real failure, and only a
# positive not-found counts as gone.
# =====================================================================

test_teardown_delete_stops_after_allow_failure_deny_survives() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  seed_firewall_rule_desc_only "$DENY_NAME" "$MARKER"
  set_firewall_delete_will_fail "$ALLOW_NAME"
  hybrid_teardown_check "$HUB" "$PROJECT"
  # Captured without a subshell (not run_expect_fail): hybrid_teardown_delete
  # sets HYBRID_TEARDOWN_DELETED/_FAILED as globals, and a command
  # substitution's subshell would discard those mutations before this
  # function could assert on them.
  local stderr_file
  stderr_file="$(mktemp)"
  hybrid_teardown_delete "$PROJECT" 2>"${stderr_file}"
  local stderr_output
  stderr_output="$(cat "${stderr_file}")"
  rm -f "${stderr_file}"
  assert_true "$([[ -f "${GCLOUD_STUB_STATE_DIR}/firewall-rules/${DENY_NAME}.json" ]] && echo true || echo false)" \
    "the deny rule must never be deleted after the allow delete failed"
  local n is_deny_deleted=false
  for n in ${HYBRID_TEARDOWN_DELETED[@]+"${HYBRID_TEARDOWN_DELETED[@]}"}; do
    [[ "$n" == "$DENY_NAME" ]] && is_deny_deleted=true
  done
  assert_eq "false" "$is_deny_deleted" "the deny rule must not be reported as deleted"
  assert_eq "2" "${#HYBRID_TEARDOWN_DELETE_FAILED[@]}" \
    "the deny rule queued behind the failed allow must be recorded as not deleted too"
  assert_contains "$stderr_output" "Not attempted (kept" \
    "the operator must be told the deny rule was never attempted, not just that the allow failed"
  assert_contains "$stderr_output" "Not attempted (kept so tcp:2049 stays denied): ${DENY_NAME}" \
    "the not-attempted line must name the specific queued rule"
  assert_eq "$DENY_NAME" "${HYBRID_TEARDOWN_DELETE_FAILED[1]:-}" \
    "the deny rule (not just some rule) must be the one recorded as queued-behind"
}

test_teardown_delete_confirms_already_gone_via_list() {
  fresh_gcloud_state
  HYBRID_TEARDOWN_DELETE=("$ALLOW_NAME")
  hybrid_teardown_delete "$PROJECT"
  assert_eq "1" "${#HYBRID_TEARDOWN_DELETED[@]}" "a rule already gone (delete fails, list confirms absent) should count as deleted"
  assert_eq "0" "${#HYBRID_TEARDOWN_DELETE_FAILED[@]}" "a positively-confirmed absence is not a failure"
}

test_teardown_delete_recheck_list_failure_is_failure_not_gone() {
  fresh_gcloud_state
  HYBRID_TEARDOWN_DELETE=("$ALLOW_NAME")
  set_firewall_list_will_fail
  hybrid_teardown_delete "$PROJECT"
  assert_eq "0" "${#HYBRID_TEARDOWN_DELETED[@]}" "an unknown re-check must never count as deleted"
  assert_eq "1" "${#HYBRID_TEARDOWN_DELETE_FAILED[@]}" "an unknown re-check is a failure, not a gone rule"
}

# At the function level: seed an unmarked deny alongside a
# marked allow, run check then delete, and confirm hybrid_teardown_delete
# itself never touches the unmarked one -- not just that its caller
# happens to never call it with one queued.
test_teardown_delete_only_deletes_queued_not_skipped() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  seed_firewall_rule_desc_only "$DENY_NAME" "unrelated-rule-not-ours"
  hybrid_teardown_check "$HUB" "$PROJECT"
  hybrid_teardown_delete "$PROJECT"
  assert_true "$([[ -f "${GCLOUD_STUB_STATE_DIR}/firewall-rules/${DENY_NAME}.json" ]] && echo true || echo false)" \
    "hybrid_teardown_delete must never delete a rule that was only SKIPPED, not queued"
}

# =====================================================================
# Teardown check: python is only needed when something was found to
# classify.
# =====================================================================

test_teardown_check_no_python_needed_when_nothing_matches() {
  fresh_gcloud_state
  local saved_python="$PYTHON"
  PYTHON="/nonexistent/python3"
  hybrid_teardown_check "$HUB" "$PROJECT"
  PYTHON="$saved_python"
  assert_eq "false" "$HYBRID_TEARDOWN_FAILED" "no rules present means python is never needed"
}

test_teardown_check_python_missing_with_rules_fails_closed() {
  fresh_gcloud_state
  seed_firewall_rule_desc_only "$ALLOW_NAME" "$MARKER"
  local saved_python="$PYTHON"
  PYTHON="/nonexistent/python3"
  hybrid_teardown_check "$HUB" "$PROJECT"
  PYTHON="$saved_python"
  assert_eq "true" "$HYBRID_TEARDOWN_FAILED" "missing python with rules present must fail closed"
  assert_eq "0" "${#HYBRID_TEARDOWN_SKIP[@]}" "must not falsely report rules as SKIPPED (unmarked) when the real problem is python"
}

# =====================================================================
# gke_target config validation
# =====================================================================

test_config_name_leading_hyphen_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="-mycluster"
  CONFIG["gke_target.location"]="us-central1"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a leading hyphen must be refused"
  assert_contains "$RUN_OUTPUT" "not a valid GKE cluster name" "error should name the field"
}

test_config_name_uppercase_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="MyCluster"
  CONFIG["gke_target.location"]="us-central1"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an uppercase cluster name must be refused"
}

test_config_name_trailing_hyphen_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster-"
  CONFIG["gke_target.location"]="us-central1"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a trailing hyphen must be refused"
}

test_config_location_invalid_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  CONFIG["gke_target.location"]="us-central"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a location missing its trailing digits must be refused"
  assert_contains "$RUN_OUTPUT" "not a valid GCP zone or region" "error should name the field"
}

test_config_project_invalid_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  CONFIG["gke_target.location"]="us-central1"
  CONFIG["gke_target.project"]="Not_A_Valid_Project"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an invalid project ID must be refused"
  assert_contains "$RUN_OUTPUT" "not a valid GCP project ID" "error should name the field"
}

test_config_python_preflight_refused() {
  fresh_gcloud_state
  CONFIG["gke_target.name"]="mycluster"
  # shellcheck disable=SC2034 # read by config_get (harness.sh) via hybrid_read_config
  CONFIG["gke_target.location"]="us-central1"
  local saved_python="$PYTHON"
  PYTHON="/nonexistent/python3"
  run_expect_fail hybrid_read_config "$PROJECT" "$HUB"
  PYTHON="$saved_python"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "a missing python interpreter must be refused when the tier is enabled"
  assert_contains "$RUN_OUTPUT" "Python interpreter" "error should name the problem"
}

# =====================================================================
# Discovery: unreadable template counted the same as an unreadable MIG
# and the no-node-pools message pinned.
# =====================================================================

test_discover_unreadable_template_counted() {
  fresh_gcloud_state
  GKE_NAME="templatefailcluster"; GKE_PROJECT="$PROJECT"; GKE_LOCATION="us-central1"
  seed_cluster "templatefailcluster" "$NETWORK" "mig-x"
  seed_mig "mig-x" "template-unreadable"
  # template-unreadable is deliberately never seeded via seed_template, so
  # the stub's `instance-templates describe` fails for it.
  run_expect_fail hybrid_discover "$NETWORK"
  assert_true "$([[ $RUN_EXIT_CODE -ne 0 ]] && echo true || echo false)" "an unreadable template must fail discovery"
  assert_contains "$RUN_OUTPUT" "1 of 1" "error should count the unreadable instance group"
  assert_contains "$RUN_OUTPUT" "First error: gcloud-stub: instance template template-unreadable not found" \
    "the first gcloud stderr line must be surfaced"
}

test_discover_no_node_pools_message_pinned() {
  fresh_gcloud_state
  GKE_NAME="emptycluster2"; GKE_PROJECT="$PROJECT"
  # shellcheck disable=SC2034 # read by hybrid_discover (hybrid-tier.sh)
  GKE_LOCATION="us-central1"
  seed_cluster "emptycluster2" "$NETWORK"
  run_expect_fail hybrid_discover "$NETWORK"
  assert_contains "$RUN_OUTPUT" "no managed instance groups" "the no-node-pools message text should be pinned"
}

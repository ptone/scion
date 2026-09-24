# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# scripts/single-node-vm/hybrid-tier.sh — hybrid-tier (attach-only GKE
# target) additions for deploy.sh: config parsing, cluster discovery, and
# the two NFS firewall rules the hub VM needs to serve shared dirs to GKE
# agent pods.
#
# This file is meant to be `source`d by deploy.sh, never executed directly.
# It depends on the caller already having defined `config_get`,
# `config_prompt`, `info`, `warn` and `err` (deploy.sh defines all of these
# before sourcing this file), and on `set -euo pipefail` already being in
# effect.
#
# Every function here is a plain shell function operating on explicit
# arguments and a small set of documented globals (HYBRID_ENABLED,
# GKE_PROJECT, GKE_LOCATION, GKE_NAME, GKE_NODE_TAG, HYBRID_ALLOW_NAME,
# HYBRID_DENY_NAME, HYBRID_TEARDOWN_*), so the test harness under
# tests/hybrid-tier/ can source this file on its own -- with its own stub
# `config_get`/`info`/`warn`/`err` and a stub `gcloud` on PATH -- without
# ever loading or running deploy.sh itself.
#
# Scope (Phase 3a): the config block, discovery, and the two firewall
# rules, end to end (create and teardown). The cluster itself is always an
# existing, attach-only target -- this file never creates or deletes one.

# hybrid_read_config PROJECT_ID
#
# Reads gke_target.{name,location,project} via the caller's config_get. In
# interactive mode (no CONFIG_FILE), offers to enable the tier and prompts
# for the three fields via config_prompt when the operator opts in; in
# config-file mode, an absent or empty gke_target.name means the tier is
# off, full stop, with no prompt.
#
# Sets HYBRID_ENABLED to "true" or "false". When "true", also sets
# GKE_PROJECT, GKE_LOCATION and GKE_NAME. Exits non-zero with an actionable
# message if gke_target.name is set but gke_target.location is missing, or
# if gke_target.project is set and differs from PROJECT_ID -- Phase 3a
# supports only a cluster in the hub's own project.
hybrid_read_config() {
  local project_id="$1"
  local cfg_name cfg_location cfg_project

  cfg_name="$(config_get 'gke_target.name' '')"
  cfg_location="$(config_get 'gke_target.location' '')"
  cfg_project="$(config_get 'gke_target.project' '')"

  if [[ -z "$cfg_name" && -z "${CONFIG_FILE:-}" ]]; then
    echo ""
    read -rp "Attach an existing GKE cluster as a second runtime (hybrid tier)? [y/N]: " hybrid_choice
    if [[ "$(echo "$hybrid_choice" | tr '[:upper:]' '[:lower:]')" == "y" ]]; then
      config_prompt cfg_name "GKE cluster name: " ""
      config_prompt cfg_location "GKE cluster location (zone or region): " ""
      config_prompt cfg_project "GKE cluster project [${project_id}]: " "${project_id}"
    fi
  fi

  if [[ -z "$cfg_name" ]]; then
    HYBRID_ENABLED=false
    return 0
  fi

  if [[ -z "$cfg_location" ]]; then
    err "gke_target.location is required when gke_target.name is set."
    exit 1
  fi

  GKE_NAME="$cfg_name"
  GKE_LOCATION="$cfg_location"
  GKE_PROJECT="${cfg_project:-$project_id}"
  if [[ "$GKE_PROJECT" != "$project_id" ]]; then
    err "gke_target.project ('${GKE_PROJECT}') must match this hub's project ('${project_id}'). Attaching a cluster in a different project is not supported yet."
    exit 1
  fi
  HYBRID_ENABLED=true
}

# hybrid_discover HUB_NETWORK
#
# Verifies the configured GKE cluster exists and is on the hub's own
# network, then discovers its node network tag. Every gcloud call here is
# read-only (describe/list); nothing is created. Sets GKE_NODE_TAG. Exits
# non-zero with an actionable message if the cluster isn't found, is on a
# different network than the hub VM, or no node tag can be discovered --
# always before any resource is created.
#
# Node-tag discovery: GKE names every node instance from the cluster name,
# with a prefix that differs by mode -- "gke-<cluster>-..." for a Standard
# node pool, "gk3-<cluster>-..." for Autopilot -- so the search below
# matches either prefix and works uniformly for both without calling the
# Kubernetes API at all. It reads the network tags already present on one
# of the cluster's own Compute Engine node instances via `gcloud compute`,
# the same property the firewall rule below matches traffic on.
hybrid_discover() {
  local hub_network="$1"
  local network node_tags

  network="$(gcloud container clusters describe "${GKE_NAME}" \
    --project="${GKE_PROJECT}" --location="${GKE_LOCATION}" \
    --format="value(network)" 2>/dev/null)" || true
  if [[ -z "$network" ]]; then
    err "GKE cluster '${GKE_NAME}' was not found in project '${GKE_PROJECT}', location '${GKE_LOCATION}'."
    exit 1
  fi
  if [[ "$network" != "$hub_network" ]]; then
    err "GKE cluster '${GKE_NAME}' is on network '${network}', but this hub uses network '${hub_network}'. Attaching a cluster on a different network is not supported."
    exit 1
  fi

  node_tags="$(gcloud compute instances list \
    --project="${GKE_PROJECT}" \
    --filter="name~^(gke|gk3)-${GKE_NAME}-" \
    --limit=1 \
    --format="value(tags.items)" 2>/dev/null)" || true
  GKE_NODE_TAG="$(echo "$node_tags" | tr ';' '\n' | sed '/^$/d' | head -1)"
  if [[ -z "$GKE_NODE_TAG" ]]; then
    err "Could not discover a node network tag for GKE cluster '${GKE_NAME}'. Ensure the cluster has at least one running node."
    exit 1
  fi
}

# _hybrid_ensure_firewall_rule NAME PROJECT_ID MARKER [gcloud create args...]
#
# Shared by hybrid_ensure_firewall_rules below. If a rule named NAME
# already exists: reuses it as-is when its description is exactly MARKER,
# or fails when it is not -- deploy.sh never adopts a same-named rule it
# does not already own. Otherwise creates it with the given args plus
# --description=MARKER.
#
# Reuse checks ownership only, not the rest of the rule's spec (priority,
# tags, ports): re-verifying the full spec on every run would either
# silently correct drift (masking a deliberate hand edit) or fail an
# otherwise-idempotent re-run over a hand-tuned rule. "Carries this
# deployment's marker" is what Phase 3a treats as proof of ownership; spec
# drift detection is not part of this scope.
_hybrid_ensure_firewall_rule() {
  local name="$1" project_id="$2" marker="$3"
  shift 3
  local existing_desc
  if existing_desc="$(gcloud compute firewall-rules describe "${name}" \
      --project="${project_id}" --format="value(description)" 2>/dev/null)"; then
    if [[ "$existing_desc" == "$marker" ]]; then
      echo "  Firewall rule already exists (owned by this deployment): ${name}"
      return 0
    fi
    err "Firewall rule '${name}' already exists but does not carry this deployment's marker ('${marker}'). Refusing to modify it."
    exit 1
  fi
  gcloud compute firewall-rules create "${name}" \
    --project="${project_id}" \
    --description="${marker}" \
    "$@" \
    --quiet
  echo "  Created firewall rule: ${name}"
}

# hybrid_ensure_firewall_rules HUB_NAME PROJECT_ID NETWORK
#
# Creates (or verifies ownership of) the two firewall rules this tier
# needs, both targeting scion-hub-<hub>-nfs and carrying the exact
# description token scion-deployment=<hub>:
#   scion-hub-<hub>-nfs-allow  INGRESS ALLOW tcp:2049 from the discovered
#                              node tag (GKE_NODE_TAG; set by
#                              hybrid_discover), priority 900.
#   scion-hub-<hub>-nfs-deny   INGRESS DENY  tcp:2049 from 0.0.0.0/0,
#                              priority 950.
# Sets HYBRID_ALLOW_NAME and HYBRID_DENY_NAME. Call only after
# hybrid_discover has set GKE_NODE_TAG.
hybrid_ensure_firewall_rules() {
  local hub_name="$1" project_id="$2" network="$3"
  local marker="scion-deployment=${hub_name}"
  local target_tag
  target_tag="$(hybrid_vm_tag "${hub_name}")"

  HYBRID_ALLOW_NAME="scion-hub-${hub_name}-nfs-allow"
  HYBRID_DENY_NAME="scion-hub-${hub_name}-nfs-deny"

  _hybrid_ensure_firewall_rule "${HYBRID_ALLOW_NAME}" "${project_id}" "${marker}" \
    --network="${network}" --direction=INGRESS --action=ALLOW --rules=tcp:2049 \
    --source-tags="${GKE_NODE_TAG}" --target-tags="${target_tag}" --priority=900

  _hybrid_ensure_firewall_rule "${HYBRID_DENY_NAME}" "${project_id}" "${marker}" \
    --network="${network}" --direction=INGRESS --action=DENY --rules=tcp:2049 \
    --source-ranges=0.0.0.0/0 --target-tags="${target_tag}" --priority=950
}

# hybrid_vm_tag HUB_NAME
#
# Echoes the network tag a VM needs for the firewall rules above to take
# effect on it.
hybrid_vm_tag() {
  echo "scion-hub-$1-nfs"
}

# hybrid_apply_vm_tag INSTANCE_NAME ZONE PROJECT_ID HUB_NAME
#
# Idempotently adds the hybrid-tier network tag to an EXISTING VM. A newly
# created VM gets the same tag at creation time instead, via --tags.
# add-tags is a set union on GCP's side, so calling it again when the tag
# is already present is a harmless no-op -- no separate existence check is
# needed first.
hybrid_apply_vm_tag() {
  local instance="$1" zone="$2" project_id="$3" hub_name="$4"
  gcloud compute instances add-tags "${instance}" \
    --zone="${zone}" --project="${project_id}" \
    --tags="$(hybrid_vm_tag "${hub_name}")" \
    --quiet
}

# hybrid_teardown_check HUB_NAME PROJECT_ID
#
# Looks up the two NFS firewall rules by name and classifies each:
#   found, description exactly scion-deployment=<hub>  -> queued for
#     deletion in HYBRID_TEARDOWN_DELETE.
#   found, any other description (including empty)      -> recorded in
#     HYBRID_TEARDOWN_SKIP, and HYBRID_TEARDOWN_FAILED is set to "true".
#   not found                                            -> ignored.
# Never deletes anything itself.
#
# The caller MUST check HYBRID_TEARDOWN_FAILED and, if "true", refuse the
# ENTIRE teardown -- base resources included -- before calling
# hybrid_teardown_delete or deleting anything else. An unmarked name
# collision means this HUB_NAME can no longer be trusted to identify only
# resources this deployment owns, so nothing proceeds safely from that
# point, not just the two hybrid-owned rules.
hybrid_teardown_check() {
  local hub_name="$1" project_id="$2"
  local marker="scion-deployment=${hub_name}"

  HYBRID_TEARDOWN_FAILED=false
  HYBRID_TEARDOWN_DELETE=()
  HYBRID_TEARDOWN_SKIP=()

  local name desc
  for name in "scion-hub-${hub_name}-nfs-allow" "scion-hub-${hub_name}-nfs-deny"; do
    if desc="$(gcloud compute firewall-rules describe "${name}" \
        --project="${project_id}" --format="value(description)" 2>/dev/null)"; then
      if [[ "$desc" == "$marker" ]]; then
        HYBRID_TEARDOWN_DELETE+=("${name}")
      else
        HYBRID_TEARDOWN_SKIP+=("${name}")
        HYBRID_TEARDOWN_FAILED=true
      fi
    fi
  done
}

# hybrid_teardown_delete PROJECT_ID
#
# Deletes exactly the rules hybrid_teardown_check queued in
# HYBRID_TEARDOWN_DELETE. Call only after confirming
# HYBRID_TEARDOWN_FAILED=false. Never deletes the cluster; this tier never
# creates or deletes a cluster in the first place.
hybrid_teardown_delete() {
  local project_id="$1"
  local name
  for name in "${HYBRID_TEARDOWN_DELETE[@]}"; do
    if gcloud compute firewall-rules delete "${name}" \
        --project="${project_id}" --quiet 2>/dev/null; then
      echo "  Deleted: ${name}"
    else
      warn "Firewall rule ${name} not found or already deleted."
    fi
  done
}

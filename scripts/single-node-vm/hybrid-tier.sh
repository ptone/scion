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

# This file has no shebang -- it is always `source`d, never executed --
# so shellcheck needs an explicit shell directive to know its dialect.
# shellcheck shell=bash

# scripts/single-node-vm/hybrid-tier.sh — hybrid-tier (attach-only GKE
# target) additions for deploy.sh: config parsing, cluster discovery, and
# the two NFS firewall rules the hub VM needs to serve shared dirs to GKE
# agent pods.
#
# This file is meant to be `source`d by deploy.sh, never executed directly.
# It depends on the caller already having defined `config_get`,
# `config_prompt`, `info`, `warn`, `err` and `PYTHON` (deploy.sh defines all
# of these before sourcing this file), and on `set -euo pipefail` already
# being in effect.
#
# Every function here is a plain shell function operating on explicit
# arguments and a small set of documented globals (HYBRID_ENABLED,
# GKE_PROJECT, GKE_LOCATION, GKE_NAME, GKE_NODE_TAG, HYBRID_ALLOW_NAME,
# HYBRID_DENY_NAME, HYBRID_TEARDOWN_*), so the test harness under tests/ can
# source this file on its own -- with its own stub
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
  # shellcheck disable=SC2034 # read by deploy.sh after sourcing this file
  HYBRID_ENABLED=true
}

# _hybrid_cluster_ref — a "name (project: P, location: L)" string for
# actionable messages, built from the globals hybrid_read_config sets.
_hybrid_cluster_ref() {
  echo "${GKE_NAME} (project: ${GKE_PROJECT}, location: ${GKE_LOCATION})"
}

# hybrid_discover HUB_NETWORK
#
# Verifies the configured GKE cluster exists and is on the hub's own
# network, then discovers its node network tag. Every gcloud call here is
# read-only (describe); nothing is created. Sets GKE_NODE_TAG. Exits
# non-zero with an actionable message, naming the cluster and (where
# relevant) what was found, if the cluster isn't found, is on a different
# network than the hub VM, or no single node tag can be discovered --
# always before any resource is created.
#
# Node-tag discovery starts from the cluster, not from guessing at
# instance names: it reads the cluster's node pools' managed instance
# groups (instanceGroupUrls) and, for each, the network tags on that
# group's instance template -- the tag a template carries applies to every
# instance in the group, including when the group currently has zero
# instances (an Autopilot pool scaled to zero, for example), so this works
# without listing live instances at all. Among those tags, the one GKE
# itself assigns for firewall purposes matches ^gke-.+-node$ (the same
# pattern for both Standard and Autopilot node pools -- Autopilot's own
# node instances are additionally named with a gk3- prefix, but the
# firewall-purpose network tag GKE assigns them still follows the
# gke-...-node pattern). If zero or more than one distinct tag matches
# that pattern across all node pools, this refuses to guess and fails
# instead, listing whatever candidates it found.
hybrid_discover() {
  local hub_network="$1"
  local cluster_ref
  cluster_ref="$(_hybrid_cluster_ref)"

  local describe_json
  describe_json="$(gcloud container clusters describe "${GKE_NAME}" \
    --project="${GKE_PROJECT}" --location="${GKE_LOCATION}" \
    --format=json 2>/dev/null)" || true
  if [[ -z "$describe_json" ]]; then
    err "GKE cluster ${cluster_ref} was not found."
    exit 1
  fi

  local network
  network="$(echo "$describe_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
print(d.get('network') or '')
")"
  if [[ -z "$network" ]]; then
    err "Could not determine the network for GKE cluster ${cluster_ref} from its description."
    exit 1
  fi
  if [[ "$network" != "$hub_network" ]]; then
    err "GKE cluster ${cluster_ref} is on network '${network}', but this hub uses network '${hub_network}'. Attaching a cluster on a different network is not supported."
    exit 1
  fi

  local mig_urls
  mig_urls="$(echo "$describe_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
urls = []
for np in d.get('nodePools') or []:
    urls.extend(np.get('instanceGroupUrls') or [])
print('\n'.join(urls))
")"
  if [[ -z "$mig_urls" ]]; then
    err "GKE cluster ${cluster_ref} has no managed instance groups (its node pools may be empty). Could not discover a node network tag."
    exit 1
  fi

  local mig_count=0
  local -a all_tags_seen=()
  local -a candidates=()
  local mig_url template_ref tags_line tag

  while IFS= read -r mig_url; do
    [[ -z "$mig_url" ]] && continue
    mig_count=$((mig_count + 1))
    template_ref="$(gcloud compute instance-groups managed describe "$mig_url" \
      --format="value(instanceTemplate)" 2>/dev/null)" || true
    [[ -z "$template_ref" ]] && continue
    tags_line="$(gcloud compute instance-templates describe "$template_ref" \
      --format="value(properties.tags.items)" 2>/dev/null)" || true
    [[ -z "$tags_line" ]] && continue
    while IFS= read -r tag; do
      [[ -z "$tag" ]] && continue
      all_tags_seen+=("$tag")
      if [[ "$tag" =~ ^gke-.+-node$ ]]; then
        candidates+=("$tag")
      fi
    done < <(echo "$tags_line" | tr ';' '\n')
  done <<< "$mig_urls"

  local unique_candidates=""
  if [[ ${#candidates[@]} -gt 0 ]]; then
    unique_candidates="$(printf '%s\n' "${candidates[@]}" | sort -u)"
  fi
  local candidate_count=0
  [[ -n "$unique_candidates" ]] && candidate_count="$(echo "$unique_candidates" | grep -c .)"

  if [[ "$candidate_count" -eq 0 ]]; then
    local seen_desc="none"
    [[ ${#all_tags_seen[@]} -gt 0 ]] && seen_desc="$(printf '%s\n' "${all_tags_seen[@]}" | sort -u | tr '\n' ',' | sed 's/,$//' | sed 's/,/, /g')"
    err "Could not discover a GKE node network tag for cluster ${cluster_ref}: no instance template tag matched the expected pattern (gke-<suffix>-node) across ${mig_count} managed instance group(s) checked. Tags seen: ${seen_desc}."
    exit 1
  fi
  if [[ "$candidate_count" -gt 1 ]]; then
    local candidates_desc
    candidates_desc="$(echo "$unique_candidates" | tr '\n' ',' | sed 's/,$//' | sed 's/,/, /g')"
    err "Found more than one candidate GKE node network tag for cluster ${cluster_ref}, refusing to guess. Candidates: ${candidates_desc}."
    exit 1
  fi

  GKE_NODE_TAG="$unique_candidates"
}

# _hybrid_firewall_rule_fields JSON
#
# Given the JSON body of `gcloud compute firewall-rules describe
# --format=json`, echoes a single tab-separated line:
#   network<TAB>direction<TAB>action<TAB>port_spec<TAB>source<TAB>target_tags<TAB>priority
# normalized the same way this file's expected values are expressed
# (network as its short name, port_spec as "proto:port,port", source as
# whichever of sourceTags/sourceRanges is set, target_tags comma-joined),
# so the caller can compare tab-field-by-tab-field against what it expects.
_hybrid_firewall_rule_fields() {
  "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
network = (d.get('network') or '').rstrip('/').rsplit('/', 1)[-1]
direction = d.get('direction') or ''
if d.get('allowed'):
    action = 'ALLOW'
    rule = d['allowed'][0]
elif d.get('denied'):
    action = 'DENY'
    rule = d['denied'][0]
else:
    action = ''
    rule = {}
proto = rule.get('IPProtocol', '')
ports = ','.join(rule.get('ports') or [])
port_spec = (proto + ':' + ports) if ports else proto
source = ','.join(d.get('sourceTags') or []) or ','.join(d.get('sourceRanges') or [])
target_tags = ','.join(d.get('targetTags') or [])
priority = str(d['priority']) if d.get('priority') is not None else ''
print('\t'.join([network, direction, action, port_spec, source, target_tags, priority]))
"
}

# _hybrid_check_rule_drift NAME PROJECT_ID JSON NETWORK DIRECTION ACTION \
#   PORT_SPEC SOURCE_TYPE SOURCE_VALUE TARGET_TAG PRIORITY
#
# Compares an already-marked, pre-existing rule's security-relevant fields
# (direction, action, ports, priority, source, target tags, network)
# against what this tier expects. On a match, returns 0 silently. On any
# mismatch, prints every differing field plus a remediation command --
# always the delete command (deploy.sh recreates the rule correctly on the
# next run), and additionally a direct `update` command when the drift is
# limited to fields `update` can change in place (source/target-tags/
# priority; not direction, action or network) -- then returns 1. Never
# corrects anything itself.
_hybrid_check_rule_drift() {
  local name="$1" project_id="$2" json="$3" exp_network="$4" exp_direction="$5" \
    exp_action="$6" exp_port="$7" source_type="$8" exp_source="$9" exp_target_tag="${10}" \
    exp_priority="${11}"

  local fields act_network act_direction act_action act_port act_source act_target_tag act_priority
  fields="$(echo "$json" | _hybrid_firewall_rule_fields)"
  IFS=$'\t' read -r act_network act_direction act_action act_port act_source act_target_tag act_priority <<< "$fields"

  local -a mismatches=()
  [[ "$act_network" != "$exp_network" ]] && mismatches+=("network: expected '${exp_network}', found '${act_network}'")
  [[ "$act_direction" != "$exp_direction" ]] && mismatches+=("direction: expected '${exp_direction}', found '${act_direction}'")
  [[ "$act_action" != "$exp_action" ]] && mismatches+=("action: expected '${exp_action}', found '${act_action}'")
  [[ "$act_port" != "$exp_port" ]] && mismatches+=("ports: expected '${exp_port}', found '${act_port}'")
  [[ "$act_source" != "$exp_source" ]] && mismatches+=("source: expected '${exp_source}', found '${act_source}'")
  [[ "$act_target_tag" != "$exp_target_tag" ]] && mismatches+=("target tags: expected '${exp_target_tag}', found '${act_target_tag}'")
  [[ "$act_priority" != "$exp_priority" ]] && mismatches+=("priority: expected '${exp_priority}', found '${act_priority}'")

  if [[ ${#mismatches[@]} -eq 0 ]]; then
    return 0
  fi

  err "Firewall rule '${name}' carries this deployment's marker but its spec has drifted from what this tier expects:"
  local m
  for m in "${mismatches[@]}"; do
    err "  ${m}"
  done
  err "Refusing to auto-correct a marked rule. To restore the expected spec, delete it and let the next deploy.sh run recreate it:"
  err "  gcloud compute firewall-rules delete ${name} --project=${project_id} --quiet"

  local network_ok=true direction_ok=true action_ok=true port_ok=true
  [[ "$act_network" != "$exp_network" ]] && network_ok=false
  [[ "$act_direction" != "$exp_direction" ]] && direction_ok=false
  [[ "$act_action" != "$exp_action" ]] && action_ok=false
  [[ "$act_port" != "$exp_port" ]] && port_ok=false
  if [[ "$network_ok" == "true" && "$direction_ok" == "true" && "$action_ok" == "true" && "$port_ok" == "true" ]]; then
    local source_flag
    if [[ "$source_type" == "tag" ]]; then
      source_flag="--source-tags=${exp_source}"
    else
      source_flag="--source-ranges=${exp_source}"
    fi
    err "Or update it in place:"
    err "  gcloud compute firewall-rules update ${name} --project=${project_id} --priority=${exp_priority} ${source_flag} --target-tags=${exp_target_tag}"
  fi

  return 1
}

# _hybrid_ensure_firewall_rule NAME PROJECT_ID MARKER NETWORK DIRECTION \
#   ACTION PORT_SPEC SOURCE_TYPE SOURCE_VALUE TARGET_TAG PRIORITY
#
# Shared by hybrid_ensure_firewall_rules below. If a rule named NAME
# already exists: fails outright if its description isn't exactly MARKER
# (deploy.sh never adopts a same-named rule it does not already own);
# otherwise verifies its full security-relevant spec against the expected
# values via _hybrid_check_rule_drift, and fails (never auto-corrects) on
# any mismatch. Only a rule that both carries the marker and matches the
# expected spec is left alone. Otherwise creates it fresh, with the marker
# and expected spec.
_hybrid_ensure_firewall_rule() {
  local name="$1" project_id="$2" marker="$3" network="$4" direction="$5" action="$6" \
    port_spec="$7" source_type="$8" source_value="$9" target_tag="${10}" priority="${11}"

  local json
  if json="$(gcloud compute firewall-rules describe "${name}" \
      --project="${project_id}" --format=json 2>/dev/null)"; then
    local existing_desc
    existing_desc="$(echo "$json" | "$PYTHON" -c "import json,sys; print(json.load(sys.stdin).get('description') or '')")"
    if [[ "$existing_desc" != "$marker" ]]; then
      err "Firewall rule '${name}' already exists but does not carry this deployment's marker ('${marker}'). Refusing to modify it."
      exit 1
    fi
    if ! _hybrid_check_rule_drift "$name" "$project_id" "$json" "$network" "$direction" \
        "$action" "$port_spec" "$source_type" "$source_value" "$target_tag" "$priority"; then
      exit 1
    fi
    echo "  Firewall rule already exists and matches the expected spec: ${name}"
    return 0
  fi

  local source_flag
  if [[ "$source_type" == "tag" ]]; then
    source_flag="--source-tags=${source_value}"
  else
    source_flag="--source-ranges=${source_value}"
  fi

  gcloud compute firewall-rules create "${name}" \
    --project="${project_id}" \
    --description="${marker}" \
    --network="${network}" \
    --direction="${direction}" \
    --action="${action}" \
    --rules="${port_spec}" \
    "${source_flag}" \
    --target-tags="${target_tag}" \
    --priority="${priority}" \
    --quiet
  echo "  Created firewall rule: ${name}"
}

# hybrid_ensure_firewall_rules HUB_NAME PROJECT_ID NETWORK
#
# Creates (or verifies the marker and full spec of) the two firewall rules
# this tier needs, both targeting scion-hub-<hub>-nfs and carrying the
# exact description token scion-deployment=<hub>:
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
    "${network}" "INGRESS" "ALLOW" "tcp:2049" "tag" "${GKE_NODE_TAG}" "${target_tag}" "900"

  _hybrid_ensure_firewall_rule "${HYBRID_DENY_NAME}" "${project_id}" "${marker}" \
    "${network}" "INGRESS" "DENY" "tcp:2049" "range" "0.0.0.0/0" "${target_tag}" "950"
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

  local name json desc
  for name in "scion-hub-${hub_name}-nfs-allow" "scion-hub-${hub_name}-nfs-deny"; do
    if json="$(gcloud compute firewall-rules describe "${name}" \
        --project="${project_id}" --format=json 2>/dev/null)"; then
      desc="$(echo "$json" | "$PYTHON" -c "import json,sys; print(json.load(sys.stdin).get('description') or '')")"
      if [[ "$desc" == "$marker" ]]; then
        HYBRID_TEARDOWN_DELETE+=("${name}")
      else
        HYBRID_TEARDOWN_SKIP+=("${name}")
        # shellcheck disable=SC2034 # read by deploy.sh after this call returns
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

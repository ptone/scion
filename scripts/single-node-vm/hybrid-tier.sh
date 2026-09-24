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
# GKE_PROJECT, GKE_LOCATION, GKE_NAME, GKE_NODE_TAG, GKE_NODE_SUBNET_CIDR,
# HYBRID_ALLOW_NAME, HYBRID_DENY_NAME, HYBRID_TEARDOWN_*), so the test
# harness under tests/ can source this file on its own -- with its own stub
# `config_get`/`info`/`warn`/`err` and a stub `gcloud` on PATH -- without
# ever loading or running deploy.sh itself.
#
# Scope: the config block, discovery, and the two firewall rules, end to
# end (create and teardown). The cluster itself is always an existing,
# attach-only target -- this file never creates or deletes one.
#
# Array expansions throughout this file use the
# `${arr[@]+"${arr[@]}"}` idiom instead of a bare `"${arr[@]}"` wherever
# the array in question can legitimately be empty. Under `set -u`, an
# empty array's bare `"${arr[@]}"` is an unbound-variable error on bash
# older than 4.4 (macOS ships 3.2), even though it is a documented no-op
# on 4.4+; the `+` form is the portable way to say "zero or more
# elements" on every bash version this script might run under.

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
# message if: gke_target.name is set but gke_target.location is missing;
# any of gke_target.name/location/project fails GCP's own naming pattern;
# gke_target.project is set and differs from PROJECT_ID (only a cluster in
# the hub's own project is supported); or $PYTHON isn't available (the
# rest of the tier depends on it for every gcloud JSON response it reads).
hybrid_read_config() {
  local project_id="$1"
  local cfg_name cfg_location cfg_project
  local hybrid_choice

  cfg_name="$(config_get 'gke_target.name' '')"
  cfg_location="$(config_get 'gke_target.location' '')"
  cfg_project="$(config_get 'gke_target.project' '')"

  if [[ -z "$cfg_name" && -z "${CONFIG_FILE:-}" ]]; then
    echo ""
    config_prompt hybrid_choice "Attach an existing GKE cluster as a second runtime (hybrid tier)? [y/N]: " "n"
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

  if ! [[ "$cfg_name" =~ ^[a-z]([-a-z0-9]{0,38}[a-z0-9])?$ ]]; then
    err "gke_target.name '${cfg_name}' is not a valid GKE cluster name (lowercase letters, digits and hyphens; must start with a letter and not end with a hyphen)."
    exit 1
  fi
  if ! [[ "$cfg_location" =~ ^[a-z]+-[a-z]+[0-9]+(-[a-z])?$ ]]; then
    err "gke_target.location '${cfg_location}' is not a valid GCP zone or region."
    exit 1
  fi
  if [[ -n "$cfg_project" ]] && ! [[ "$cfg_project" =~ ^[a-z][-a-z0-9]{4,28}[a-z0-9]$ ]]; then
    err "gke_target.project '${cfg_project}' is not a valid GCP project ID."
    exit 1
  fi

  GKE_NAME="$cfg_name"
  GKE_LOCATION="$cfg_location"
  GKE_PROJECT="${cfg_project:-$project_id}"
  if [[ "$GKE_PROJECT" != "$project_id" ]]; then
    err "gke_target.project ('${GKE_PROJECT}') must match this hub's project ('${project_id}'). Attaching a cluster in a different project is not supported yet."
    exit 1
  fi

  if ! command -v "$PYTHON" &>/dev/null; then
    err "Python interpreter '${PYTHON}' is required for the hybrid tier (cluster discovery and firewall-rule checks) but was not found. Set PYTHON=/path/to/python3 or disable the tier."
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

# _hybrid_region_from_location LOCATION
#
# A GKE cluster's location is either regional (e.g. us-central1, already
# a region) or zonal (e.g. us-central1-a, a region plus a single-letter
# zone suffix). Subnetworks are regional resources, so a zonal location
# needs that suffix stripped before it can be passed to
# `gcloud compute networks subnets describe --region`.
_hybrid_region_from_location() {
  local location="$1"
  if [[ "$location" =~ ^(.+)-[a-z]$ ]]; then
    echo "${BASH_REMATCH[1]}"
  else
    echo "$location"
  fi
}

# _hybrid_validate_node_subnet_cidr CIDR
#
# Validates CIDR as a syntactically valid IPv4 network in canonical form
# (no host bits set), and refuses anything broader than /8: 0.0.0.0/0
# would list every routable address as a trusted NFS client, and nothing
# a single GKE node subnet legitimately needs is ever wider than /8.
# Prints nothing and returns non-zero on any problem; the caller supplies
# the actionable error message.
_hybrid_validate_node_subnet_cidr() {
  local cidr="$1"
  "$PYTHON" -c "
import ipaddress, sys
try:
    net = ipaddress.ip_network(sys.argv[1], strict=True)
except ValueError:
    sys.exit(1)
sys.exit(0 if (net.version == 4 and net.prefixlen >= 8) else 1)
" "$cidr"
}

# hybrid_discover HUB_NETWORK
#
# Verifies the configured GKE cluster exists and is on the hub's own
# network, then discovers its node network tag and its node subnet's
# primary IP range. Every gcloud call here is read-only (describe);
# nothing is created. Sets GKE_NODE_TAG and GKE_NODE_SUBNET_CIDR. Exits
# non-zero with an actionable message, naming the cluster and (where
# relevant) what was found, if the cluster can't be described, is on a
# different network than the hub VM, its node subnet can't be described
# or has an invalid or dangerously broad IP range, or no single node tag
# can be discovered -- always before any resource is created. Every
# failure message includes gcloud's own stderr rather than assuming "not
# found": a permission or API-disabled error looks nothing like a missing
# cluster, and reporting it as one would send an operator chasing the
# wrong fix.
#
# The node subnet -- not the pod CIDR or any secondary range -- is what
# the NFS export's client list is built from: nodes, not pods, originate
# the NFS mount traffic that reaches the VM. The firewall allow rule's
# source is unrelated to this and continues to use the node network tag
# discovered above, not an IP range.
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
# instead, listing whatever candidates it found. If any instance group or
# its template can't even be read, this also fails outright, even if the
# readable ones already yield exactly one candidate: a group this call
# couldn't see could carry a second, different tag, and "guessed right by
# luck" is not a property this check can claim.
hybrid_discover() {
  local hub_network="$1"
  local cluster_ref
  cluster_ref="$(_hybrid_cluster_ref)"

  local describe_json describe_err
  describe_err="$(mktemp)"
  if ! describe_json="$(gcloud container clusters describe "${GKE_NAME}" \
      --project="${GKE_PROJECT}" --location="${GKE_LOCATION}" \
      --format=json 2>"${describe_err}")"; then
    err "Could not describe GKE cluster ${cluster_ref}:"
    err "  $(cat "${describe_err}")"
    rm -f "${describe_err}"
    exit 1
  fi
  rm -f "${describe_err}"

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

  # The NFS export's client list is the cluster's node subnet, never the
  # pod CIDR or any secondary range: nodes are what actually originate
  # NFS traffic, and the node subnet is the narrowest range that's still
  # guaranteed to cover every node regardless of how pod/service ranges
  # are laid out.
  local subnetwork
  subnetwork="$(echo "$describe_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
print(d.get('subnetwork') or '')
")"
  if [[ -z "$subnetwork" ]]; then
    err "Could not determine the node subnetwork for GKE cluster ${cluster_ref} from its description."
    exit 1
  fi

  local subnet_region
  subnet_region="$(_hybrid_region_from_location "$GKE_LOCATION")"

  local subnet_err subnet_json
  subnet_err="$(mktemp)"
  if ! subnet_json="$(gcloud compute networks subnets describe "$subnetwork" \
      --region="$subnet_region" --project="${GKE_PROJECT}" --format=json 2>"${subnet_err}")"; then
    err "Could not describe node subnetwork '${subnetwork}' (region: ${subnet_region}) for GKE cluster ${cluster_ref}:"
    err "  $(cat "${subnet_err}")"
    rm -f "${subnet_err}"
    exit 1
  fi
  rm -f "${subnet_err}"

  local node_cidr
  node_cidr="$(echo "$subnet_json" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
print(d.get('ipCidrRange') or '')
")"
  if [[ -z "$node_cidr" ]] || ! _hybrid_validate_node_subnet_cidr "$node_cidr"; then
    err "GKE cluster ${cluster_ref}'s node subnetwork '${subnetwork}' has an invalid or dangerously broad primary IP range ('${node_cidr:-empty}'). Refusing to build an NFS export client list from it."
    exit 1
  fi
  # shellcheck disable=SC2034 # consumed by the NFS export function
  GKE_NODE_SUBNET_CIDR="$node_cidr"

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
  local unreadable_count=0
  local first_unreadable_err=""
  local -a all_tags_seen=()
  local -a candidates=()
  local mig_url template_ref tags_line tag mig_call_err

  while IFS= read -r mig_url; do
    [[ -z "$mig_url" ]] && continue
    mig_count=$((mig_count + 1))

    mig_call_err="$(mktemp)"
    if ! template_ref="$(gcloud compute instance-groups managed describe "$mig_url" \
        --format="value(instanceTemplate)" 2>"${mig_call_err}")"; then
      unreadable_count=$((unreadable_count + 1))
      [[ -z "$first_unreadable_err" ]] && first_unreadable_err="$(head -1 "${mig_call_err}")"
      rm -f "${mig_call_err}"
      continue
    fi
    rm -f "${mig_call_err}"
    [[ -z "$template_ref" ]] && continue

    mig_call_err="$(mktemp)"
    if ! tags_line="$(gcloud compute instance-templates describe "$template_ref" \
        --format="value(properties.tags.items)" 2>"${mig_call_err}")"; then
      unreadable_count=$((unreadable_count + 1))
      [[ -z "$first_unreadable_err" ]] && first_unreadable_err="$(head -1 "${mig_call_err}")"
      rm -f "${mig_call_err}"
      continue
    fi
    rm -f "${mig_call_err}"
    [[ -z "$tags_line" ]] && continue

    while IFS= read -r tag; do
      [[ -z "$tag" ]] && continue
      all_tags_seen+=("$tag")
      if [[ "$tag" =~ ^gke-.+-node$ ]]; then
        candidates+=("$tag")
      fi
    done < <(echo "$tags_line" | tr ';' '\n')
  done <<< "$mig_urls"

  # A partial view could hide a second, distinct tag on the instance
  # group(s) that couldn't be read, so any unreadable group fails
  # discovery outright rather than proceeding on the readable subset --
  # even when the readable ones already yield exactly one candidate.
  if [[ "$unreadable_count" -gt 0 ]]; then
    err "Could not discover a GKE node network tag for cluster ${cluster_ref}: ${unreadable_count} of ${mig_count} managed instance group(s) could not be read, which could hide a second, distinct tag. Refusing to guess from a partial view."
    err "  First error: ${first_unreadable_err}"
    exit 1
  fi

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

# _hybrid_firewall_rule_fields
#
# Reads the JSON body of `gcloud compute firewall-rules describe
# --format=json` from stdin and echoes every field able to widen,
# narrow or disable the rule, normalized for exact comparison and joined
# by ASCII unit separator (0x1f, not tab -- see below):
#   network  direction  action  allowed/denied (all entries)  sourceTags
#   sourceRanges  sourceServiceAccounts  targetTags
#   targetServiceAccounts  destinationRanges  disabled  priority
# Every other field is ignored, including non-scoping settable fields
# (description, logConfig) and output-only metadata (id, name, kind,
# selfLink, creationTimestamp) -- none of them can change what the rule
# does. network is reduced to its short name (the self-link's last path
# segment). action is ALLOW, DENY, or MALFORMED if both allowed[] and
# denied[] are present (GCP never returns that shape; treating it as a
# guaranteed mismatch is simpler than picking one). Every allowed/denied
# entry is included, each rendered "proto:port,port" (ports sorted) or
# bare "proto", the whole set sorted and joined with ';', so a second
# entry changes the field regardless of order. sourceTags and
# sourceRanges are compared as separate fields, since GCP ORs them when
# both are set. The remaining list fields are comma-joined and sorted.
#
# 0x1f, not tab, because bash's `read` collapses runs of IFS
# *whitespace* -- which includes tab -- silently merging adjacent empty
# fields and shifting every field after them, which is wrong here: "this
# field is empty" is a meaningful, distinct result, not a fencepost to
# skip.
_hybrid_firewall_rule_fields() {
  "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
network = (d.get('network') or '').rstrip('/').rsplit('/', 1)[-1]
direction = d.get('direction') or ''
allowed = d.get('allowed') or []
denied = d.get('denied') or []
if allowed and denied:
    action = 'MALFORMED'
    rules = allowed + denied
elif allowed:
    action = 'ALLOW'
    rules = allowed
elif denied:
    action = 'DENY'
    rules = denied
else:
    action = ''
    rules = []

def rule_sig(r):
    proto = r.get('IPProtocol', '')
    ports = ','.join(sorted(r.get('ports') or []))
    return (proto + ':' + ports) if ports else proto

rules_sig = ';'.join(sorted(rule_sig(r) for r in rules))
source_tags = ','.join(sorted(d.get('sourceTags') or []))
source_ranges = ','.join(sorted(d.get('sourceRanges') or []))
source_sas = ','.join(sorted(d.get('sourceServiceAccounts') or []))
target_tags = ','.join(sorted(d.get('targetTags') or []))
target_sas = ','.join(sorted(d.get('targetServiceAccounts') or []))
dest_ranges = ','.join(sorted(d.get('destinationRanges') or []))
disabled = 'true' if d.get('disabled') else 'false'
priority = str(d['priority']) if d.get('priority') is not None else ''
sep = chr(0x1f)  # ASCII unit separator -- see the bash side for why not tab
print(sep.join([network, direction, action, rules_sig, source_tags,
                source_ranges, source_sas, target_tags, target_sas,
                dest_ranges, disabled, priority]))
"
}

# _hybrid_check_rule_drift NAME PROJECT_ID JSON NETWORK DIRECTION ACTION \
#   RULES SOURCE_TYPE SOURCE_VALUE TARGET_TAG PRIORITY
#
# Compares an already-marked, pre-existing rule against every field able
# to widen, narrow or disable it: direction, action, disabled, priority,
# network, allowed/denied (all entries), sourceTags, sourceRanges,
# sourceServiceAccounts, targetServiceAccounts, targetTags,
# destinationRanges. Every other field is ignored, including
# non-scoping settable fields (description, logConfig) and output-only
# metadata (id, name, kind, selfLink, creationTimestamp). SOURCE_TYPE is
# "tag" or "range"; the *other* source field is expected empty, since a
# rule this tier creates only ever sets one of them, and GCP ORs the two
# when both are present. Service accounts and destination ranges are
# always expected empty, and disabled is always expected false.
#
# On a full match, returns 0 silently. On any mismatch, prints every
# differing field, then the delete remediation, then an `update`
# remediation too, but only when `update` can converge to *exactly* the
# expected rule -- every other field already matches, and the only drift
# left is something it can set directly (the expected source type,
# target tags, priority). If a field would need to be cleared, or
# `update` can't touch it (direction, action, network, rule entries,
# service accounts, disabled), only delete is offered. For the deny
# rule, the delete remediation is the two-rule, order-preserving sequence
# (delete allow, delete deny, re-run deploy.sh) rather than a bare
# delete, since deleting the deny alone would leave tcp:2049 reachable
# through the allow rule with nothing to deny it. Never corrects
# anything itself.
_hybrid_check_rule_drift() {
  local name="$1" project_id="$2" json="$3" exp_network="$4" exp_direction="$5" \
    exp_action="$6" exp_rules="$7" source_type="$8" exp_source_value="$9" \
    exp_target_tag="${10}" exp_priority="${11}"

  local exp_source_tags="" exp_source_ranges=""
  if [[ "$source_type" == "tag" ]]; then
    exp_source_tags="$exp_source_value"
  else
    exp_source_ranges="$exp_source_value"
  fi

  local fields
  fields="$(echo "$json" | _hybrid_firewall_rule_fields)"
  local act_network act_direction act_action act_rules act_source_tags act_source_ranges \
    act_source_sas act_target_tags act_target_sas act_dest_ranges act_disabled act_priority
  IFS=$'\x1f' read -r act_network act_direction act_action act_rules act_source_tags \
    act_source_ranges act_source_sas act_target_tags act_target_sas act_dest_ranges \
    act_disabled act_priority <<< "$fields"

  local -a mismatches=()
  local update_converges=true

  if [[ "$act_network" != "$exp_network" ]]; then
    mismatches+=("network: expected '${exp_network}', found '${act_network}'")
    update_converges=false
  fi
  if [[ "$act_direction" != "$exp_direction" ]]; then
    mismatches+=("direction: expected '${exp_direction}', found '${act_direction}'")
    update_converges=false
  fi
  if [[ "$act_action" != "$exp_action" ]]; then
    mismatches+=("action: expected '${exp_action}', found '${act_action}'")
    update_converges=false
  fi
  if [[ "$act_rules" != "$exp_rules" ]]; then
    mismatches+=("ports: expected '${exp_rules}', found '${act_rules}'")
    update_converges=false
  fi
  if [[ "$act_source_tags" != "$exp_source_tags" ]]; then
    mismatches+=("source tags: expected '${exp_source_tags:-(empty)}', found '${act_source_tags:-(empty)}'")
    [[ "$source_type" != "tag" ]] && update_converges=false
  fi
  if [[ "$act_source_ranges" != "$exp_source_ranges" ]]; then
    mismatches+=("source ranges: expected '${exp_source_ranges:-(empty)}', found '${act_source_ranges:-(empty)}'")
    [[ "$source_type" != "range" ]] && update_converges=false
  fi
  if [[ "$act_source_sas" != "" ]]; then
    mismatches+=("source service accounts: expected '(empty)', found '${act_source_sas}'")
    update_converges=false
  fi
  if [[ "$act_target_tags" != "$exp_target_tag" ]]; then
    mismatches+=("target tags: expected '${exp_target_tag}', found '${act_target_tags}'")
  fi
  if [[ "$act_target_sas" != "" ]]; then
    mismatches+=("target service accounts: expected '(empty)', found '${act_target_sas}'")
    update_converges=false
  fi
  if [[ "$act_dest_ranges" != "" ]]; then
    mismatches+=("destination ranges: expected '(empty)', found '${act_dest_ranges}'")
    update_converges=false
  fi
  if [[ "$act_disabled" != "false" ]]; then
    mismatches+=("disabled: expected 'false', found '${act_disabled}'")
    update_converges=false
  fi
  if [[ "$act_priority" != "$exp_priority" ]]; then
    mismatches+=("priority: expected '${exp_priority}', found '${act_priority}'")
  fi

  if [[ ${#mismatches[@]} -eq 0 ]]; then
    return 0
  fi

  err "Firewall rule '${name}' carries this deployment's marker but its spec has drifted from what this tier expects:"
  local m
  for m in "${mismatches[@]}"; do
    err "  ${m}"
  done
  err "Refusing to auto-correct a marked rule. To restore the expected spec:"
  if [[ "$name" == *-nfs-deny ]]; then
    local allow_name="${name%-nfs-deny}-nfs-allow"
    err "  Delete both rules, in this order, then re-run deploy.sh (deleting only the deny rule would leave tcp:2049 reachable through the allow rule with nothing to deny it):"
    err "    gcloud compute firewall-rules delete ${allow_name} --project=${project_id} --quiet"
    err "    gcloud compute firewall-rules delete ${name} --project=${project_id} --quiet"
  else
    err "  gcloud compute firewall-rules delete ${name} --project=${project_id} --quiet"
    err "  Then re-run deploy.sh to recreate it with the expected spec."
  fi

  if [[ "$update_converges" == "true" ]]; then
    local source_flag
    if [[ "$source_type" == "tag" ]]; then
      source_flag="--source-tags=${exp_source_value}"
    else
      source_flag="--source-ranges=${exp_source_value}"
    fi
    err "Or update it in place:"
    err "  gcloud compute firewall-rules update ${name} --project=${project_id} --priority=${exp_priority} ${source_flag} --target-tags=${exp_target_tag}"
  fi

  return 1
}

# _hybrid_ensure_firewall_rule NAME PROJECT_ID MARKER NETWORK DIRECTION \
#   ACTION RULES SOURCE_TYPE SOURCE_VALUE TARGET_TAG PRIORITY
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
    rules="$7" source_type="$8" source_value="$9" target_tag="${10}" priority="${11}"

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
        "$action" "$rules" "$source_type" "$source_value" "$target_tag" "$priority"; then
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
    --rules="${rules}" \
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
# Created deny first, then allow, so an interrupted run can never leave
# an allow rule in place without its paired deny (teardown deletes in the
# opposite order: allow first, then deny, for the same reason in
# reverse). Sets HYBRID_ALLOW_NAME and HYBRID_DENY_NAME. Call only after
# hybrid_discover has set GKE_NODE_TAG.
hybrid_ensure_firewall_rules() {
  local hub_name="$1" project_id="$2" network="$3"
  local marker="scion-deployment=${hub_name}"
  local target_tag
  target_tag="$(hybrid_vm_tag "${hub_name}")"

  HYBRID_ALLOW_NAME="scion-hub-${hub_name}-nfs-allow"
  HYBRID_DENY_NAME="scion-hub-${hub_name}-nfs-deny"

  _hybrid_ensure_firewall_rule "${HYBRID_DENY_NAME}" "${project_id}" "${marker}" \
    "${network}" "INGRESS" "DENY" "tcp:2049" "range" "0.0.0.0/0" "${target_tag}" "950"

  _hybrid_ensure_firewall_rule "${HYBRID_ALLOW_NAME}" "${project_id}" "${marker}" \
    "${network}" "INGRESS" "ALLOW" "tcp:2049" "tag" "${GKE_NODE_TAG}" "${target_tag}" "900"
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
# Looks up the two NFS firewall rules with a single `firewall-rules list`
# call and classifies each: marked -> HYBRID_TEARDOWN_DELETE (allow
# before deny); found but unmarked -> HYBRID_TEARDOWN_SKIP, and
# HYBRID_TEARDOWN_FAILED=true; not found -> ignored. A failed list call
# also sets HYBRID_TEARDOWN_FAILED, without populating either array --
# "unknown" must never look like "nothing to protect". $PYTHON is needed,
# and preflighted with a clear error, only when something was actually
# found to classify. Never deletes anything itself; the caller must
# treat HYBRID_TEARDOWN_FAILED=true as reason to abort the entire
# teardown (hybrid_teardown_preflight below does this).
hybrid_teardown_check() {
  local hub_name="$1" project_id="$2"
  local marker="scion-deployment=${hub_name}"
  local name_allow="scion-hub-${hub_name}-nfs-allow"
  local name_deny="scion-hub-${hub_name}-nfs-deny"

  HYBRID_TEARDOWN_FAILED=false
  HYBRID_TEARDOWN_DELETE=()
  HYBRID_TEARDOWN_SKIP=()

  local list_json list_err
  list_err="$(mktemp)"
  if ! list_json="$(gcloud compute firewall-rules list --project="${project_id}" \
      --filter="name=(${name_allow} ${name_deny})" --format=json 2>"${list_err}")"; then
    err "Could not list firewall rules to check hybrid-tier ownership; aborting teardown rather than assuming none exist:"
    err "  $(cat "${list_err}")"
    rm -f "${list_err}"
    # shellcheck disable=SC2034 # read by callers after this call returns
    HYBRID_TEARDOWN_FAILED=true
    return 0
  fi
  rm -f "${list_err}"

  # Nothing to classify, so nothing needs $PYTHON either -- this keeps
  # the common tier-off/no-hybrid-rules case working even without a
  # Python interpreter on PATH, since it never has anything to parse.
  if [[ "$list_json" == "[]" ]]; then
    return 0
  fi

  if ! command -v "$PYTHON" &>/dev/null; then
    err "Python interpreter '${PYTHON}' is required to check hybrid-tier firewall rule ownership during teardown, but was not found."
    # shellcheck disable=SC2034 # read by callers after this call returns
    HYBRID_TEARDOWN_FAILED=true
    return 0
  fi

  local name desc
  for name in "$name_allow" "$name_deny"; do
    desc="$(echo "$list_json" | "$PYTHON" -c "
import json, sys
name = sys.argv[1]
for r in json.load(sys.stdin):
    if r.get('name') == name:
        print(r.get('description') or '')
        break
else:
    print('__NOT_FOUND__')
" "$name")"
    if [[ "$desc" == "__NOT_FOUND__" ]]; then
      continue
    fi
    if [[ "$desc" == "$marker" ]]; then
      HYBRID_TEARDOWN_DELETE+=("${name}")
    else
      HYBRID_TEARDOWN_SKIP+=("${name}")
      # shellcheck disable=SC2034 # read by callers after this call returns
      HYBRID_TEARDOWN_FAILED=true
    fi
  done
}

# hybrid_teardown_preflight HUB_NAME PROJECT_ID
#
# The full teardown-safety check deploy.sh runs before deleting anything:
# calls hybrid_teardown_check, prints the classification block ("  found
# (marked): <name>" / "  SKIPPED (unmarked): <name>"), and returns
# non-zero if teardown must abort -- either an unmarked name match was
# found, or the ownership list call itself failed. Never deletes
# anything. deploy.sh calls this unconditionally in --delete mode, even
# when the hybrid tier is off in the current config: teardown has no
# other way to know whether the tier was ever turned on for this hub, so
# it always checks for (and, if marked, removes) these two rule names.
hybrid_teardown_preflight() {
  local hub_name="$1" project_id="$2"
  hybrid_teardown_check "$hub_name" "$project_id"

  local name
  for name in ${HYBRID_TEARDOWN_DELETE[@]+"${HYBRID_TEARDOWN_DELETE[@]}"}; do
    echo "  found (marked): ${name}"
  done
  for name in ${HYBRID_TEARDOWN_SKIP[@]+"${HYBRID_TEARDOWN_SKIP[@]}"}; do
    echo "  SKIPPED (unmarked): ${name}"
  done

  if [[ "$HYBRID_TEARDOWN_FAILED" != "true" ]]; then
    return 0
  fi
  if [[ ${#HYBRID_TEARDOWN_SKIP[@]} -gt 0 ]]; then
    err "Refusing to tear down: the SKIPPED rule(s) above do not carry this deployment's marker, so ownership can't be confirmed. Resolve the naming collision manually, then re-run teardown."
  fi
  return 1
}

# _hybrid_firewall_rule_absent NAME PROJECT_ID
#
# Positively confirms a rule doesn't exist via a `list` call, the same
# fail-closed pattern as the ownership check: returns 0 only when the
# list call succeeds AND comes back empty. Any other outcome (the rule is
# listed, or the list call itself fails) returns 1 -- "unknown" is never
# treated as "gone".
_hybrid_firewall_rule_absent() {
  local name="$1" project_id="$2"
  local list_json
  if ! list_json="$(gcloud compute firewall-rules list --project="${project_id}" \
      --filter="name=(${name})" --format=json 2>/dev/null)"; then
    return 1
  fi
  [[ "$list_json" == "[]" ]]
}

# hybrid_teardown_delete PROJECT_ID
#
# Deletes the rules hybrid_teardown_check queued in HYBRID_TEARDOWN_DELETE,
# allow before deny. Call only after confirming HYBRID_TEARDOWN_FAILED=
# false, and only once the hub VM is confirmed gone (the caller's
# responsibility -- see docs/deploy/agent-runbook-single-node-vm.md).
# Stops at the first delete that isn't confirmed gone -- via `delete`
# succeeding, or, on a `delete` failure, a positive not-found from
# `_hybrid_firewall_rule_absent` -- and leaves every rule from that point
# on untouched, so the deny is never deleted after the allow delete
# failed or came back uncertain. Sets HYBRID_TEARDOWN_DELETED to the
# rules actually gone afterward and HYBRID_TEARDOWN_DELETE_FAILED to the
# first rule that wasn't (and, transitively, every rule still queued
# behind it). The caller must treat a non-empty
# HYBRID_TEARDOWN_DELETE_FAILED as a failed teardown, and must only
# report HYBRID_TEARDOWN_DELETED as deleted. Never deletes the cluster;
# this tier never creates or deletes a cluster in the first place.
hybrid_teardown_delete() {
  local project_id="$1"
  HYBRID_TEARDOWN_DELETED=()
  HYBRID_TEARDOWN_DELETE_FAILED=()
  local name delete_err stopped=false
  for name in ${HYBRID_TEARDOWN_DELETE[@]+"${HYBRID_TEARDOWN_DELETE[@]}"}; do
    if [[ "$stopped" == "true" ]]; then
      err "Not attempted (kept so tcp:2049 stays denied): ${name}"
      HYBRID_TEARDOWN_DELETE_FAILED+=("${name}")
      continue
    fi
    delete_err="$(mktemp)"
    if gcloud compute firewall-rules delete "${name}" \
        --project="${project_id}" --quiet 2>"${delete_err}"; then
      echo "  Deleted: ${name}"
      HYBRID_TEARDOWN_DELETED+=("${name}")
      rm -f "${delete_err}"
      continue
    fi
    if _hybrid_firewall_rule_absent "${name}" "${project_id}"; then
      warn "Firewall rule ${name} was already gone before this teardown deleted it."
      HYBRID_TEARDOWN_DELETED+=("${name}")
    else
      err "Failed to delete firewall rule ${name}:"
      err "  $(cat "${delete_err}")"
      HYBRID_TEARDOWN_DELETE_FAILED+=("${name}")
      stopped=true
    fi
    rm -f "${delete_err}"
  done
}

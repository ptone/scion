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

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
# The dedicated NFS squash identity: a system account with no login shell
# and no home, whose primary group is the existing "scion" group (the
# broker's own group -- see docs/deploy/agent-runbook-single-node-vm.md).
# Deliberately distinct from the "scion" user itself: squashing every NFS
# client to the broker's own uid would mean any pod that can reach the
# export can act as the broker on the shared tree, which is exactly the
# upper-dir-symlink-plant vector the dedicated identity exists to close.
# shellcheck disable=SC2034 # read by deploy.sh after sourcing this file
readonly HYBRID_NFS_SQUASH_USER="scion-nfs"
# The NFS export root: a plain directory on the VM's boot disk (per the
# design's boot-disk provisioning choice), not a separate mounted volume.
# shellcheck disable=SC2034 # read by deploy.sh after sourcing this file
readonly HYBRID_NFS_EXPORT_ROOT="/srv/scion-shared"

# _hybrid_gcloud_not_found TEXT
#
# True only for gcloud's own specific "genuinely absent" signal: a
# NOT_FOUND status token, a 404 code, or the literal "Requested entity
# was not found" message -- never a bare "not found" substring. Some
# permission-denied responses are deliberately worded to avoid
# confirming a resource's existence to a caller who can't see it (for
# example "...not found or permission denied"), and those must never be
# treated as "gone": anything mentioning permission or forbidden is
# excluded outright, checked first, before the not-found signal itself.
_hybrid_gcloud_not_found() {
  local text="$1"
  if echo "$text" | grep -qiE 'permission|forbidden'; then
    return 1
  fi
  echo "$text" | grep -qE '(^|[^A-Za-z_])NOT_FOUND($|[^A-Za-z_])|(^|[^0-9])404($|[^0-9])|Requested entity was not found'
}

# _hybrid_kubectl_not_found TEXT
#
# True only for kubectl's own specific NotFound reason token
# ("Error from server (NotFound): ..."), with the same permission/
# forbidden exclusion as _hybrid_gcloud_not_found above.
_hybrid_kubectl_not_found() {
  local text="$1"
  if echo "$text" | grep -qiE 'permission|forbidden'; then
    return 1
  fi
  echo "$text" | grep -qE '\(NotFound\)'
}

# hybrid_read_config PROJECT_ID HUB_NAME
#
# Reads gke_target.{name,location,project,namespace,pvc_name} via the
# caller's config_get. In interactive mode (no CONFIG_FILE), offers to
# enable the tier and prompts for all five fields via config_prompt when
# the operator opts in, showing each derived default (empty input
# accepts it); in config-file mode, an absent or empty gke_target.name
# means the tier is off, full stop, with no prompt.
#
# Sets HYBRID_ENABLED to "true" or "false". When "true", also sets
# GKE_PROJECT, GKE_LOCATION, GKE_NAME, GKE_NAMESPACE and GKE_PVC_NAME.
# Exits non-zero with an actionable message if: gke_target.name is set
# but gke_target.location is missing; any of gke_target.name/location/
# project fails GCP's own naming pattern; gke_target.project is set and
# differs from PROJECT_ID (only a cluster in the hub's own project is
# supported); or $PYTHON isn't available (the rest of the tier depends
# on it for every gcloud JSON response it reads).
hybrid_read_config() {
  local project_id="$1" hub_name="$2"
  local cfg_name cfg_location cfg_project cfg_namespace cfg_pvc_name
  local hybrid_choice
  local default_namespace default_pvc_name
  default_namespace="$(hybrid_k8s_default_namespace "$hub_name")"
  default_pvc_name="$(hybrid_k8s_default_pvc_name "$hub_name")"

  cfg_name="$(config_get 'gke_target.name' '')"
  cfg_location="$(config_get 'gke_target.location' '')"
  cfg_project="$(config_get 'gke_target.project' '')"
  cfg_namespace="$(config_get 'gke_target.namespace' "$default_namespace")"
  cfg_pvc_name="$(config_get 'gke_target.pvc_name' "$default_pvc_name")"

  if [[ -z "$cfg_name" && -z "${CONFIG_FILE:-}" ]]; then
    echo ""
    config_prompt hybrid_choice "Attach an existing GKE cluster as a second runtime (hybrid tier)? [y/N]: " "n"
    if [[ "$(echo "$hybrid_choice" | tr '[:upper:]' '[:lower:]')" == "y" ]]; then
      config_prompt cfg_name "GKE cluster name: " ""
      config_prompt cfg_location "GKE cluster location (zone or region): " ""
      config_prompt cfg_project "GKE cluster project [${project_id}]: " "${project_id}"
      config_prompt cfg_namespace "Kubernetes namespace for the shared tree [${default_namespace}]: " "${default_namespace}"
      config_prompt cfg_pvc_name "PersistentVolumeClaim name [${default_pvc_name}]: " "${default_pvc_name}"
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
  if ! [[ "$cfg_namespace" =~ ^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$ ]]; then
    err "gke_target.namespace '${cfg_namespace}' is not a valid Kubernetes namespace name (lowercase alphanumeric and hyphens, must start and end with an alphanumeric, max 63 characters)."
    exit 1
  fi
  if ! [[ "$cfg_pvc_name" =~ ^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$ ]]; then
    err "gke_target.pvc_name '${cfg_pvc_name}' is not a valid Kubernetes object name (lowercase alphanumeric and hyphens, must start and end with an alphanumeric, max 63 characters)."
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
  # shellcheck disable=SC2034 # read by hybrid_k8s_ensure_objects/_teardown_check as an override
  GKE_NAMESPACE="$cfg_namespace"
  # shellcheck disable=SC2034 # read by hybrid_k8s_ensure_objects/_teardown_check as an override
  GKE_PVC_NAME="$cfg_pvc_name"
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

# hybrid_nfs_fsid HUB_NAME
#
# A stable, deterministic NFS export fsid for this hub: a UUID5 derived
# from the hub name, so it's the same across every re-run without needing
# a separate value generated once and recorded somewhere. nfs-utils
# accepts a UUID for `fsid=` (the documented way to keep file handles
# stable and avoid ESTALE) just as readily as a small integer, and a UUID
# needs no coordination between hubs the way hand-assigned integers would.
hybrid_nfs_fsid() {
  local hub_name="$1"
  "$PYTHON" -c "
import uuid, sys
print(uuid.uuid5(uuid.NAMESPACE_DNS, 'scion-hub-' + sys.argv[1] + '.nfs-export'))
" "$hub_name"
}

# hybrid_nfs_export_line EXPORT_ROOT CIDR ANONUID ANONGID FSID
#
# Renders one `/etc/exports.d` line for the hybrid tier's NFS export:
# `sync` (required so both Docker and GKE writers see consistent state),
# `no_subtree_check`, `all_squash` to the given anonuid/anongid, `sec=sys`
# (this tier's authorization is by source IP -- see the docs, not by NFS
# auth), and the given fsid. There is no separate squash group: anongid is
# the "scion" group's own gid, since the Phase 2 leaf ACL grants that
# group access, and a different anongid would make GKE's writes invisible
# to it. Pure string rendering -- no gcloud or SSH calls -- so it's
# directly unit-testable; the caller is responsible for actually reading
# the anonuid/anongid off the VM and writing the result to a file.
hybrid_nfs_export_line() {
  local export_root="$1" cidr="$2" anonuid="$3" anongid="$4" fsid="$5"
  echo "${export_root} ${cidr}(rw,sync,no_subtree_check,all_squash,anonuid=${anonuid},anongid=${anongid},sec=sys,fsid=${fsid})"
}

# hybrid_nfs_squash_identity_script SQUASH_USER
#
# Renders the remote script that idempotently creates the dedicated NFS
# squash identity (a system account, no home, no login shell, primary
# group "scion") if it doesn't already exist, then asserts its uid
# differs from the "scion" (broker) user's own uid -- squashing every
# NFS client to the broker's own identity would let any pod that can
# reach the export act as the broker on the shared tree -- and, only on
# success, prints "SQUASH_UID:SCION_GID" for the caller to capture. Pure
# string rendering -- no gcloud or SSH calls -- so it's directly
# unit-testable; the caller (deploy.sh) is responsible for actually
# running the result over SSH.
hybrid_nfs_squash_identity_script() {
  local squash_user="$1"
  cat <<SCRIPT
set -euo pipefail
id ${squash_user} >/dev/null 2>&1 || sudo useradd -r -M -N -g scion -s /usr/sbin/nologin ${squash_user}
SQUASH_UID=\$(id -u ${squash_user})
SCION_UID=\$(id -u scion)
SCION_GID=\$(getent group scion | cut -d: -f3)
if [ "\$SQUASH_UID" = "\$SCION_UID" ]; then
  echo 'The NFS squash uid must not equal the scion (broker) uid.' >&2
  exit 1
fi
echo "\${SQUASH_UID}:\${SCION_GID}"
SCRIPT
}

# hybrid_nfs_export_script EXPORT_ROOT CIDR ANONUID ANONGID FSID HUB_NAME
#
# Renders the remote script that idempotently creates the export root
# (owned scion:scion, mode 2755 so the squash uid can't write it),
# installs nfs-kernel-server if it isn't already, writes this hub's own
# file under /etc/exports.d/ (using hybrid_nfs_export_line for the
# rendered line, so the two stay in sync), re-exports, and enables and
# starts the service. Always rewrites the file and re-exports, which is
# how the export picks up a changed CIDR on re-run with no separate
# drift detection needed. Pure string rendering -- no gcloud or SSH
# calls -- so it's directly unit-testable; the caller is responsible for
# actually running the result over SSH, after the squash identity script
# above has already run.
hybrid_nfs_export_script() {
  local export_root="$1" cidr="$2" anonuid="$3" anongid="$4" fsid="$5" hub_name="$6"
  local export_line
  export_line="$(hybrid_nfs_export_line "$export_root" "$cidr" "$anonuid" "$anongid" "$fsid")"
  cat <<SCRIPT
set -euo pipefail
sudo mkdir -p ${export_root}
sudo chown scion:scion ${export_root}
sudo chmod 2755 ${export_root}
if ! dpkg -s nfs-kernel-server >/dev/null 2>&1; then
  sudo apt-get update -y
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y nfs-kernel-server
fi
echo '${export_line}' | sudo tee /etc/exports.d/scion-hub-${hub_name}.exports > /dev/null
sudo exportfs -ra
sudo systemctl enable --now nfs-kernel-server
echo 'NFS export configured.'
SCRIPT
}

# hybrid_settings_shared_dir_storage_yaml VM_IP EXPORT_ROOT PV_NAME
#
# Renders the server.shared_dir_storage block for settings.yaml (the
# schema from pkg/config/settings_v1.go's V1SharedDirStorageConfig/
# V1NFSConfig/V1NFSShare), indented to nest under "server:" at the same
# level as its existing "hub:"/"storage:"/etc. keys. backend is "nfs";
# nfs.mount_root and the one share's "export" are both the VM's export
# root, since the broker reads it directly as a local path on this same
# VM while GKE pods reach it over NFS at that same server path;
# subpath_root is the fixed "projects" subdirectory every project's
# shared-dirs live under; the share's id is a stable label (there is
# only ever one share per hub) and pv_name is the PV this hub's pods
# actually bind to. Pure string rendering -- no gcloud, kubectl, or SSH
# calls -- so it's directly unit-testable; the caller only calls this
# when the tier is on, and splices its output into an otherwise-
# unchanged settings.yaml render.
hybrid_settings_shared_dir_storage_yaml() {
  local vm_ip="$1" export_root="$2" pv_name="$3"
  cat <<YAML
  shared_dir_storage:
    backend: nfs
    nfs:
      mount_root: "${export_root}"
      subpath_root: "projects"
      shares:
        - id: "shared"
          server: "${vm_ip}"
          export: "${export_root}"
          pv_name: "${pv_name}"
YAML
}

# hybrid_cloud_run_label_args SERVICE_NAME PROJECT_ID REGION HUB_NAME
#
# Echoes the --labels=... argument to pass to `gcloud run deploy`, or
# nothing, based on whether the service already exists: the base-marker
# convention is additive and create-only, so the label is only added
# when this is the first create. `gcloud run services describe` failing
# is ambiguous between "doesn't exist yet" (NOT_FOUND) and some other
# problem (a permissions error, for example) -- its exit code alone
# doesn't distinguish them, so this checks the error text. Anything
# other than a clear NOT_FOUND fails safe toward "assume it exists" (no
# label), on the reasoning that a missing marker is corrected by
# nothing, while a wrong marker on an existing, unrelated service is not
# easily undone.
hybrid_cloud_run_label_args() {
  local service_name="$1" project_id="$2" region="$3" hub_name="$4"
  local describe_err
  describe_err="$(mktemp)"
  if gcloud run services describe "$service_name" \
      --project="$project_id" --region="$region" >/dev/null 2>"${describe_err}"; then
    rm -f "${describe_err}"
    return 0
  fi
  local not_found=false
  _hybrid_gcloud_not_found "$(cat "${describe_err}")" && not_found=true
  rm -f "${describe_err}"
  if [[ "$not_found" == "true" ]]; then
    echo "--labels=scion-deployment=${hub_name}"
  fi
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

# =====================================================================
# Kubernetes objects: the PV/namespace/PVC the hybrid tier's shared
# tree is mounted through, and their teardown. Every kubectl call below
# requires HYBRID_KUBECONFIG to already be set by
# hybrid_k8s_setup_kubeconfig -- a task-private temporary file, never
# the operator's default ~/.kube/config.
# =====================================================================

# hybrid_k8s_pv_name HUB_NAME — the cluster-scoped PV's name.
hybrid_k8s_pv_name() {
  echo "scion-hub-$1-shared"
}

# hybrid_k8s_default_namespace HUB_NAME — used when gke_target.namespace
# isn't set.
hybrid_k8s_default_namespace() {
  echo "scion-hub-$1"
}

# hybrid_k8s_default_pvc_name HUB_NAME — used when gke_target.pvc_name
# isn't set.
hybrid_k8s_default_pvc_name() {
  echo "scion-hub-$1-shared"
}

# hybrid_k8s_setup_kubeconfig
#
# Preflights that kubectl is present (only ever called when the tier is
# on), then runs `gcloud container clusters get-credentials` into a
# fresh, task-private temporary file and sets HYBRID_KUBECONFIG to its
# path. The caller (deploy.sh) is responsible for removing that file on
# exit; every subsequent kubectl call in this file sets
# KUBECONFIG="$HYBRID_KUBECONFIG" explicitly rather than relying on an
# ambient default.
hybrid_k8s_setup_kubeconfig() {
  if ! command -v kubectl &>/dev/null; then
    err "kubectl is required for the hybrid tier's Kubernetes objects but was not found."
    exit 1
  fi
  HYBRID_KUBECONFIG="$(mktemp)"
  local cred_err
  cred_err="$(mktemp)"
  if ! KUBECONFIG="$HYBRID_KUBECONFIG" gcloud container clusters get-credentials "${GKE_NAME}" \
      --location="${GKE_LOCATION}" --project="${GKE_PROJECT}" --quiet 2>"${cred_err}"; then
    err "Could not get credentials for GKE cluster $(_hybrid_cluster_ref):"
    err "  $(cat "${cred_err}")"
    rm -f "${cred_err}"
    exit 1
  fi
  rm -f "${cred_err}"
}

# hybrid_k8s_pv_manifest PV_NAME HUB_NAME VM_IP EXPORT_ROOT NAMESPACE PVC_NAME
#
# Renders the cluster-scoped PV: NFS server/path point at the hub VM's
# export, Retain reclaim policy (the export's data outlives any single
# claim), storageClassName "" (a static PV, never dynamically
# provisioned), and a claimRef pinned to the namespace/PVC below so no
# other claim in the cluster can bind it first. Mount options follow the
# design's own rationale: nfsvers=4.1 (single port, in-protocol
# locking), hard (avoids silent corruption), proto=tcp, and the
# attribute-cache/lookup-cache tuning that bounds cross-runtime
# staleness. Pure string rendering -- no gcloud, kubectl, or SSH calls.
hybrid_k8s_pv_manifest() {
  local pv_name="$1" hub_name="$2" vm_ip="$3" export_root="$4" namespace="$5" pvc_name="$6"
  cat <<YAML
apiVersion: v1
kind: PersistentVolume
metadata:
  name: ${pv_name}
  labels:
    scion-deployment: ${hub_name}
spec:
  capacity:
    storage: 100Gi
  accessModes:
    - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  storageClassName: ""
  mountOptions:
    - nfsvers=4.1
    - hard
    - proto=tcp
    - timeo=600
    - retrans=2
    - actimeo=3
    - lookupcache=positive
  nfs:
    server: ${vm_ip}
    path: ${export_root}
  claimRef:
    namespace: ${namespace}
    name: ${pvc_name}
YAML
}

# hybrid_k8s_namespace_manifest NAMESPACE HUB_NAME
hybrid_k8s_namespace_manifest() {
  local namespace="$1" hub_name="$2"
  cat <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: ${namespace}
  labels:
    scion-deployment: ${hub_name}
YAML
}

# hybrid_k8s_pvc_manifest PVC_NAME NAMESPACE HUB_NAME PV_NAME
hybrid_k8s_pvc_manifest() {
  local pvc_name="$1" namespace="$2" hub_name="$3" pv_name="$4"
  cat <<YAML
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${pvc_name}
  namespace: ${namespace}
  labels:
    scion-deployment: ${hub_name}
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: ""
  volumeName: ${pv_name}
  resources:
    requests:
      storage: 100Gi
YAML
}

# _hybrid_k8s_label JSON — the value of metadata.labels["scion-deployment"]
# on the given kubectl JSON object, or empty if unset.
_hybrid_k8s_label() {
  echo "$1" | "$PYTHON" -c "
import json, sys
d = json.load(sys.stdin)
print(d.get('metadata', {}).get('labels', {}).get('scion-deployment') or '')
"
}

# _hybrid_k8s_get KIND NAME [EXTRA ARGS...]
#
# Runs `kubectl get KIND NAME [EXTRA ARGS] -o json`. On success, sets
# K8S_GET_JSON to the object and returns 0. On a genuine "not found"
# (matched by the server's own error text), sets K8S_GET_STATUS=absent
# and returns 1. On any other failure -- a connectivity or permissions
# problem, for instance -- sets K8S_GET_STATUS=unknown and K8S_GET_ERR
# to the error text, and also returns 1: the caller must distinguish
# "absent" from "unknown" itself, since only "absent" is safe to create
# over, matching the "unknown is never treated as gone" rule used
# throughout this file's other teardown checks.
_hybrid_k8s_get() {
  local kind="$1" name="$2"
  shift 2
  local extra_args=("$@")
  local err_file
  err_file="$(mktemp)"
  K8S_GET_STATUS=""
  K8S_GET_ERR=""
  if K8S_GET_JSON="$(KUBECONFIG="$HYBRID_KUBECONFIG" kubectl get "$kind" "$name" \
      ${extra_args[@]+"${extra_args[@]}"} -o json 2>"${err_file}")"; then
    K8S_GET_STATUS="found"
    rm -f "${err_file}"
    return 0
  fi
  K8S_GET_JSON=""
  if _hybrid_kubectl_not_found "$(cat "${err_file}")"; then
    K8S_GET_STATUS="absent"
  else
    K8S_GET_STATUS="unknown"
    K8S_GET_ERR="$(cat "${err_file}")"
  fi
  rm -f "${err_file}"
  return 1
}

# hybrid_k8s_ensure_objects HUB_NAME VM_IP
#
# Ensures the namespace, PV, and PVC exist with the expected identity,
# creating whichever are missing. Refuses to proceed if an existing PV
# or PVC with the target name lacks this deployment's marker label
# (R3), or if a marked one has drifted from its expected identity (a
# changed VM IP after a VM recreate is the expected way for the PV to
# drift -- see the printed remediation; never auto-corrected, matching
# the firewall rules' own policy). An existing, unmarked namespace is
# used as-is: never labeled, never adopted, never refused, since reusing
# an existing namespace is allowed. Requires HYBRID_KUBECONFIG to
# already be set.
hybrid_k8s_ensure_objects() {
  local hub_name="$1" vm_ip="$2"
  local namespace pvc_name pv_name
  # GKE_NAMESPACE/GKE_PVC_NAME, if set, come from hybrid_read_config
  # (which may have prompted for them interactively); falling back to
  # config_get directly covers callers that skip it, such as --delete,
  # which must never prompt.
  namespace="${GKE_NAMESPACE:-$(config_get 'gke_target.namespace' "$(hybrid_k8s_default_namespace "$hub_name")")}"
  pvc_name="${GKE_PVC_NAME:-$(config_get 'gke_target.pvc_name' "$(hybrid_k8s_default_pvc_name "$hub_name")")}"
  pv_name="$(hybrid_k8s_pv_name "$hub_name")"

  if _hybrid_k8s_get namespace "$namespace"; then
    if [[ "$(_hybrid_k8s_label "$K8S_GET_JSON")" != "$hub_name" ]]; then
      warn "Namespace ${namespace} already exists without this deployment's marker; using it as-is, not labeling or adopting it."
    fi
  elif [[ "$K8S_GET_STATUS" == "unknown" ]]; then
    err "Could not check namespace ${namespace}: ${K8S_GET_ERR}"
    exit 1
  else
    hybrid_k8s_namespace_manifest "$namespace" "$hub_name" | KUBECONFIG="$HYBRID_KUBECONFIG" kubectl apply -f - >/dev/null
    echo "  Created namespace: ${namespace}"
  fi

  if _hybrid_k8s_get pv "$pv_name"; then
    if [[ "$(_hybrid_k8s_label "$K8S_GET_JSON")" != "$hub_name" ]]; then
      err "Persistent volume ${pv_name} already exists without this deployment's marker. Refusing to adopt it."
      exit 1
    fi
    local act_server act_path act_claim_ns act_claim_name act_reclaim
    act_server="$(echo "$K8S_GET_JSON" | "$PYTHON" -c "import json,sys; d=json.load(sys.stdin); print(d.get('spec',{}).get('nfs',{}).get('server') or '')")"
    act_path="$(echo "$K8S_GET_JSON" | "$PYTHON" -c "import json,sys; d=json.load(sys.stdin); print(d.get('spec',{}).get('nfs',{}).get('path') or '')")"
    act_claim_ns="$(echo "$K8S_GET_JSON" | "$PYTHON" -c "import json,sys; d=json.load(sys.stdin); print(d.get('spec',{}).get('claimRef',{}).get('namespace') or '')")"
    act_claim_name="$(echo "$K8S_GET_JSON" | "$PYTHON" -c "import json,sys; d=json.load(sys.stdin); print(d.get('spec',{}).get('claimRef',{}).get('name') or '')")"
    act_reclaim="$(echo "$K8S_GET_JSON" | "$PYTHON" -c "import json,sys; d=json.load(sys.stdin); print(d.get('spec',{}).get('persistentVolumeReclaimPolicy') or '')")"
    local -a mismatches=()
    [[ "$act_server" != "$vm_ip" ]] && mismatches+=("server: expected '${vm_ip}', found '${act_server}'")
    [[ "$act_path" != "$HYBRID_NFS_EXPORT_ROOT" ]] && mismatches+=("path: expected '${HYBRID_NFS_EXPORT_ROOT}', found '${act_path}'")
    [[ "$act_claim_ns" != "$namespace" ]] && mismatches+=("claimRef.namespace: expected '${namespace}', found '${act_claim_ns}'")
    [[ "$act_claim_name" != "$pvc_name" ]] && mismatches+=("claimRef.name: expected '${pvc_name}', found '${act_claim_name}'")
    [[ "$act_reclaim" != "Retain" ]] && mismatches+=("persistentVolumeReclaimPolicy: expected 'Retain', found '${act_reclaim}'")
    if [[ ${#mismatches[@]} -gt 0 ]]; then
      err "Persistent volume ${pv_name} carries this deployment's marker but its spec has drifted from what this tier expects:"
      local m
      for m in "${mismatches[@]}"; do
        err "  ${m}"
      done
      err "Refusing to auto-correct. A changed VM IP after a VM recreate is the expected way for this to happen: delete the PVC, then the PV, then re-run deploy.sh:"
      err "  kubectl delete pvc ${pvc_name} -n ${namespace}"
      err "  kubectl delete pv ${pv_name}"
      exit 1
    fi
  elif [[ "$K8S_GET_STATUS" == "unknown" ]]; then
    err "Could not check persistent volume ${pv_name}: ${K8S_GET_ERR}"
    exit 1
  else
    hybrid_k8s_pv_manifest "$pv_name" "$hub_name" "$vm_ip" "$HYBRID_NFS_EXPORT_ROOT" "$namespace" "$pvc_name" \
      | KUBECONFIG="$HYBRID_KUBECONFIG" kubectl apply -f - >/dev/null
    echo "  Created persistent volume: ${pv_name}"
  fi

  if _hybrid_k8s_get pvc "$pvc_name" -n "$namespace"; then
    if [[ "$(_hybrid_k8s_label "$K8S_GET_JSON")" != "$hub_name" ]]; then
      err "Persistent volume claim ${pvc_name} in namespace ${namespace} already exists without this deployment's marker. Refusing to adopt it."
      exit 1
    fi
    local act_volume
    act_volume="$(echo "$K8S_GET_JSON" | "$PYTHON" -c "import json,sys; d=json.load(sys.stdin); print(d.get('spec',{}).get('volumeName') or '')")"
    if [[ "$act_volume" != "$pv_name" ]]; then
      err "Persistent volume claim ${pvc_name} carries this deployment's marker but is bound to '${act_volume}', not the expected '${pv_name}'."
      err "Refusing to auto-correct. Delete the PVC, then the PV, then re-run deploy.sh:"
      err "  kubectl delete pvc ${pvc_name} -n ${namespace}"
      err "  kubectl delete pv ${pv_name}"
      exit 1
    fi
  elif [[ "$K8S_GET_STATUS" == "unknown" ]]; then
    err "Could not check persistent volume claim ${pvc_name}: ${K8S_GET_ERR}"
    exit 1
  else
    hybrid_k8s_pvc_manifest "$pvc_name" "$namespace" "$hub_name" "$pv_name" \
      | KUBECONFIG="$HYBRID_KUBECONFIG" kubectl apply -f - >/dev/null
    echo "  Created persistent volume claim: ${pvc_name} (namespace: ${namespace})"
  fi
}

# hybrid_k8s_teardown_check HUB_NAME
#
# Classifies the PVC, PV, and namespace by name, in the order they'll be
# deleted: found+marked (queued into HYBRID_K8S_TEARDOWN_DELETE) or
# found+unmarked (SKIPPED). An unmarked PVC or PV aborts the whole
# teardown (HYBRID_K8S_TEARDOWN_FAILED=true), the same rule as the
# firewall rules; an unmarked namespace is only SKIPPED and does not
# abort, since using an existing namespace without adopting it is
# allowed on create too. Any check that itself fails (not just "not
# found") also aborts: unknown is never treated as gone. Requires
# HYBRID_KUBECONFIG to already be set.
hybrid_k8s_teardown_check() {
  local hub_name="$1"
  HYBRID_K8S_NAMESPACE="${GKE_NAMESPACE:-$(config_get 'gke_target.namespace' "$(hybrid_k8s_default_namespace "$hub_name")")}"
  HYBRID_K8S_PVC_NAME="${GKE_PVC_NAME:-$(config_get 'gke_target.pvc_name' "$(hybrid_k8s_default_pvc_name "$hub_name")")}"
  HYBRID_K8S_PV_NAME="$(hybrid_k8s_pv_name "$hub_name")"
  HYBRID_K8S_TEARDOWN_DELETE=()
  HYBRID_K8S_TEARDOWN_FAILED=false

  if _hybrid_k8s_get pvc "$HYBRID_K8S_PVC_NAME" -n "$HYBRID_K8S_NAMESPACE"; then
    if [[ "$(_hybrid_k8s_label "$K8S_GET_JSON")" == "$hub_name" ]]; then
      echo "  found (marked): persistentvolumeclaim/${HYBRID_K8S_PVC_NAME}"
      HYBRID_K8S_TEARDOWN_DELETE+=("pvc")
    else
      echo "  SKIPPED (unmarked): persistentvolumeclaim/${HYBRID_K8S_PVC_NAME}"
      HYBRID_K8S_TEARDOWN_FAILED=true
    fi
  elif [[ "$K8S_GET_STATUS" == "unknown" ]]; then
    err "Could not check persistentvolumeclaim ${HYBRID_K8S_PVC_NAME}: ${K8S_GET_ERR}"
    HYBRID_K8S_TEARDOWN_FAILED=true
  fi

  if _hybrid_k8s_get pv "$HYBRID_K8S_PV_NAME"; then
    if [[ "$(_hybrid_k8s_label "$K8S_GET_JSON")" == "$hub_name" ]]; then
      echo "  found (marked): persistentvolume/${HYBRID_K8S_PV_NAME}"
      HYBRID_K8S_TEARDOWN_DELETE+=("pv")
    else
      echo "  SKIPPED (unmarked): persistentvolume/${HYBRID_K8S_PV_NAME}"
      HYBRID_K8S_TEARDOWN_FAILED=true
    fi
  elif [[ "$K8S_GET_STATUS" == "unknown" ]]; then
    err "Could not check persistentvolume ${HYBRID_K8S_PV_NAME}: ${K8S_GET_ERR}"
    HYBRID_K8S_TEARDOWN_FAILED=true
  fi

  if _hybrid_k8s_get namespace "$HYBRID_K8S_NAMESPACE"; then
    if [[ "$(_hybrid_k8s_label "$K8S_GET_JSON")" == "$hub_name" ]]; then
      echo "  found (marked): namespace/${HYBRID_K8S_NAMESPACE}"
      HYBRID_K8S_TEARDOWN_DELETE+=("namespace")
    else
      echo "  SKIPPED (unmarked, in use): namespace/${HYBRID_K8S_NAMESPACE}"
    fi
  elif [[ "$K8S_GET_STATUS" == "unknown" ]]; then
    err "Could not check namespace ${HYBRID_K8S_NAMESPACE}: ${K8S_GET_ERR}"
    HYBRID_K8S_TEARDOWN_FAILED=true
  fi
}

# hybrid_k8s_teardown_delete
#
# Deletes exactly what hybrid_k8s_teardown_check queued into
# HYBRID_K8S_TEARDOWN_DELETE, in that array's order (PVC, then PV, then
# namespace-if-marked), stopping at the first failure and recording
# every kind from that point on in HYBRID_K8S_TEARDOWN_DELETE_FAILED --
# the same stop-at-first-failure policy as the firewall rules' own
# teardown delete.
hybrid_k8s_teardown_delete() {
  HYBRID_K8S_TEARDOWN_DELETED=()
  HYBRID_K8S_TEARDOWN_DELETE_FAILED=()
  local stopped=false kind name delete_err
  local -a ns_args
  for kind in ${HYBRID_K8S_TEARDOWN_DELETE[@]+"${HYBRID_K8S_TEARDOWN_DELETE[@]}"}; do
    if [[ "$stopped" == "true" ]]; then
      err "Not attempted (kept): ${kind}"
      HYBRID_K8S_TEARDOWN_DELETE_FAILED+=("${kind}")
      continue
    fi
    ns_args=()
    case "$kind" in
      pvc) name="$HYBRID_K8S_PVC_NAME"; ns_args=(-n "$HYBRID_K8S_NAMESPACE") ;;
      pv) name="$HYBRID_K8S_PV_NAME" ;;
      namespace) name="$HYBRID_K8S_NAMESPACE" ;;
    esac
    delete_err="$(mktemp)"
    if KUBECONFIG="$HYBRID_KUBECONFIG" kubectl delete "$kind" "$name" \
        ${ns_args[@]+"${ns_args[@]}"} 2>"${delete_err}"; then
      echo "  Deleted: ${kind}/${name}"
      HYBRID_K8S_TEARDOWN_DELETED+=("${kind}")
      rm -f "${delete_err}"
      continue
    fi
    err "Failed to delete ${kind}/${name}:"
    err "  $(cat "${delete_err}")"
    HYBRID_K8S_TEARDOWN_DELETE_FAILED+=("${kind}")
    stopped=true
    rm -f "${delete_err}"
  done
}

#!/usr/bin/env bash
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

# scripts/single-node-vm/deploy.sh — Wizard-style deployment for a single-node
# Scion Hub on a GCE VM with IAP proxy authentication.
#
# This script provisions a GCE VM, downloads the scion binary from GitHub
# Releases, starts the hub via systemd, deploys a Cloud Run IAP reverse proxy,
# enables IAP, configures the hub for proxy auth, and prints the access URL.
#
# The VM has no public IP; authenticated access is via the Cloud Run IAP proxy.
# Agents running on the VM connect via localhost (no IAP needed).
#
# The script is idempotent: re-running converges without duplication.
#
# Usage:
#   ./deploy.sh [--version VERSION] [--config CONFIG_FILE] [--rebuild-images]
#   ./deploy.sh --delete
#
# Options:
#   --version VERSION   Scion release version to install (e.g. v0.5.0).
#                       If omitted, the latest release is fetched from GitHub.
#   --config FILE       Path to a JSON config file that pre-answers interactive
#                       prompts. When provided with all required fields, the
#                       script runs headlessly (no interactive input needed).
#                       See deploy-config.example.json for the format.
#   --rebuild-images    Force a rebuild of container images on the VM even if
#                       the version marker and all 3 expected images already
#                       match VERSION. Equivalent to container_images.
#                       force_rebuild: true in the config file; either one
#                       forces a rebuild.
#   --delete            Tear down all resources created by a previous deploy.
#
# Config file fields (all optional; see deploy-config.example.json):
#   hub_name                     Resource name suffix (scion-hub-<hub_name>).
#                                 <= 20 chars, lowercase letters/digits/hyphens,
#                                 must start with a lowercase letter.
#   project_id                   GCP project ID. Empty = current gcloud project.
#   region                       GCP region for the VM and Cloud Run proxy.
#   machine_size                 "small" (e2-standard-4, ~10 agents) or
#                                 "medium" (n2-standard-16, ~50 agents).
#   disk_size_gb                 Boot disk size in GB.
#   chat_plugins                 List of: telegram, discord, slack, teams.
#   container_images.source      "registry" (pre-built images) or "build"
#                                 (build on the VM). "build" is refused
#                                 when gke_target.name is set: GKE nodes
#                                 cannot pull from the VM's local Docker
#                                 store, which is what "build" uses.
#   container_images.registry    Registry path; required when source is
#                                 "registry" (e.g. us-docker.pkg.dev/PROJECT/scion).
#   container_images.force_rebuild
#                                 Force a rebuild even if the VM's version
#                                 marker and images already match VERSION.
#   admin_email                  Granted super-admin on first login. Empty =
#                                 active gcloud account.
#   update_policy                auto, notify, or disabled. Requires the
#                                 binary auto-update feature.
#   release_channel              stable, preview, or nightly. Defaults to
#                                 nightly if not specified.
#   gke_target.name              Name of an existing GKE cluster to attach
#                                 as a second, Kubernetes-based runtime
#                                 (hybrid tier). Optional; when absent (the
#                                 default), the hybrid tier is off and
#                                 behaviour is unchanged. The cluster is a
#                                 manual prerequisite: this script only ever
#                                 attaches to it, never creates or deletes
#                                 it.
#   gke_target.location          Zone or region of the cluster. Required
#                                 when gke_target.name is set.
#   gke_target.project           Project the cluster lives in. Defaults to
#                                 project_id. A cluster in a different
#                                 project is not supported yet.
#   gke_target.namespace         Kubernetes namespace for the shared-tree
#                                 PersistentVolumeClaim. Defaults to
#                                 scion-hub-<hub_name>. An existing
#                                 namespace without this deployment's
#                                 marker is used as-is, never adopted or
#                                 relabeled, and never deleted on teardown.
#   gke_target.pvc_name           Name of the PersistentVolumeClaim in that
#                                 namespace. Defaults to
#                                 scion-hub-<hub_name>-shared.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Python interpreter used to parse the JSON config file. Override with
# PYTHON=/path/to/python3 if python3 is not on PATH.
PYTHON="${PYTHON:-python3}"

# ---------------------------------------------------------------------------
# Color helpers
# ---------------------------------------------------------------------------
BOLD='\033[1m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
RESET='\033[0m'

info()    { echo -e "${BOLD}${GREEN}==>${RESET} ${BOLD}$*${RESET}"; }
warn()    { echo -e "${YELLOW}WARNING:${RESET} $*" >&2; }
err()     { echo -e "${RED}ERROR:${RESET} $*" >&2; }
section() { echo ""; echo -e "${BOLD}--- $* ---${RESET}"; }

# ---------------------------------------------------------------------------
# Retry / timing budgets
# ---------------------------------------------------------------------------
# Tuned for a cold VM on first boot: guest-agent start, OS Login / metadata
# key propagation, sshd host-key generation, and IAP tunnel setup all stack
# on top of package_upgrade: true restarting services.
readonly SSH_MAX_ATTEMPTS=30           # ~5 min ceiling (see backoff below)
readonly SSH_BACKOFF_FAST_ATTEMPTS=5   # attempts 1..N-1 use the fast backoff
readonly SSH_BACKOFF_FAST_SECS=5
readonly SSH_BACKOFF_SLOW_SECS=10
readonly CLOUD_INIT_MAX_ATTEMPTS=6
readonly CLOUD_INIT_RETRY_SECS=15
readonly HEALTH_CHECK_MAX_ATTEMPTS=12
readonly HEALTH_CHECK_RETRY_SECS=5
readonly BUILD_POLL_MAX_ATTEMPTS=180
readonly BUILD_POLL_INTERVAL_SECS=15
readonly IAP_ENFORCEMENT_WAIT_SECS=60

# ---------------------------------------------------------------------------
# Parse flags
# ---------------------------------------------------------------------------
VERSION=""
DELETE_MODE=false
CONFIG_FILE=""
CLI_REBUILD_IMAGES=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --config) CONFIG_FILE="$2"; shift 2 ;;
    --delete) DELETE_MODE=true; shift ;;
    --rebuild-images) CLI_REBUILD_IMAGES=true; shift ;;
    --help|-h)
      sed -n '/^# scripts\/single-node-vm/,/^[^#]/{ /^#/s/^# \?//p }' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *) err "Unknown flag: $1"; exit 1 ;;
  esac
done

# ---------------------------------------------------------------------------
# Config file helper
# ---------------------------------------------------------------------------
# Reads a value from the JSON config file using dot-separated keys.
# Falls back to the provided default when the key is missing or the config
# file is not set.  Lists are returned as space-separated strings.
config_get() {
  local key="$1"
  local default="${2:-}"
  if [[ -n "$CONFIG_FILE" && -f "$CONFIG_FILE" ]]; then
    local val
    val="$("$PYTHON" -c "
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
keys = sys.argv[2].split('.')
v = d
for k in keys:
    if isinstance(v, dict):
        v = v.get(k)
    else:
        v = None
        break
if v is not None:
    if isinstance(v, list):
        print(' '.join(str(i) for i in v))
    elif isinstance(v, bool):
        print(str(v).lower())
    else:
        print(v)
" "$CONFIG_FILE" "$key" 2>/dev/null)" || true
    if [[ -n "$val" ]]; then
      echo "$val"
      return
    fi
  fi
  echo "$default"
}

# Helper: prompt the user for input, or error in non-interactive mode.
# Usage: config_prompt VAR "Prompt text" "default_value"
config_prompt() {
  local varname="$1"
  local prompt="$2"
  local default="$3"
  if [[ -n "$CONFIG_FILE" && ! -t 0 ]]; then
    if [[ -n "$default" ]]; then
      printf -v "$varname" '%s' "$default"
    else
      err "Required value for '${varname}' is missing from config and stdin is not a terminal."
      exit 1
    fi
  else
    local input=""
    read -rp "$prompt" input
    printf -v "$varname" '%s' "${input:-$default}"
  fi
}

# Validate config file exists if specified
if [[ -n "$CONFIG_FILE" ]]; then
  if [[ ! -f "$CONFIG_FILE" ]]; then
    err "Config file not found: $CONFIG_FILE"
    exit 1
  fi
  if ! command -v "$PYTHON" &>/dev/null; then
    err "Python interpreter '${PYTHON}' is required to parse the config file but was not found."
    exit 1
  fi
  if ! json_err=$("$PYTHON" -c "import json, sys; json.load(open(sys.argv[1], encoding='utf-8'))" "$CONFIG_FILE" 2>&1); then
    err "Invalid JSON syntax in config file: $CONFIG_FILE"
    echo "$json_err" >&2
    exit 1
  fi
  info "Using config file: $CONFIG_FILE"
fi

# Hybrid tier (optional GKE attach target): see hybrid-tier.sh. Sourced here,
# after config_get/config_prompt/info/warn/err are defined, since it uses
# all of them. HYBRID_ENABLED defaults to false until hybrid_read_config
# runs below.
HYBRID_ENABLED=false
# shellcheck source=scripts/single-node-vm/hybrid-tier.sh
source "${SCRIPT_DIR}/hybrid-tier.sh"

# One EXIT trap, registered once, covers every task-private temp file
# this script creates later (SSH_STDERR_FILE, HYBRID_KUBECONFIG): both
# start empty, and `rm -f ""` is a harmless no-op, so setting the trap
# here before either file exists is safe. `trap` replaces rather than
# stacks, so a second, later `trap ... EXIT` call would silently drop
# this one -- there must only ever be this single registration.
SSH_STDERR_FILE=""
HYBRID_KUBECONFIG=""
trap 'rm -f "$SSH_STDERR_FILE" "$HYBRID_KUBECONFIG"' EXIT

# ---------------------------------------------------------------------------
# Teardown flow (--delete)
# ---------------------------------------------------------------------------
if [[ "$DELETE_MODE" == "true" ]]; then
  section "Teardown: Delete Single-Node-VM Resources"

  PROJECT_ID="$(config_get 'project_id' '')"
  if [[ -z "$PROJECT_ID" ]]; then
    info "Detecting GCP project..."
    PROJECT_ID="$(gcloud config get-value project 2>/dev/null)" || true
  fi
  if [[ -z "$PROJECT_ID" ]]; then
    err "No GCP project configured. Set project_id in config or run: gcloud config set project PROJECT_ID"
    exit 1
  fi
  echo "  Project: ${PROJECT_ID}"

  HUB_NAME="$(config_get 'hub_name' '')"
  if [[ -z "$HUB_NAME" ]]; then
    config_prompt HUB_NAME "Hub name [my-hub]: " "my-hub"
  fi
  if [[ ! "$HUB_NAME" =~ ^[a-z][a-z0-9-]*$ ]]; then
    err "Hub name '${HUB_NAME}' is invalid. It must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens."
    exit 1
  fi

  REGION="$(config_get 'region' '')"
  if [[ -z "$REGION" ]]; then
    config_prompt REGION "GCP region [us-central1]: " "us-central1"
  fi

  INSTANCE_NAME="scion-hub-${HUB_NAME}"
  PROXY_SERVICE="${INSTANCE_NAME}-iap-proxy"
  ROUTER_NAME="scion-hub-${HUB_NAME}-router"
  NAT_NAME="scion-hub-${HUB_NAME}-nat"
  FW_RULE_NAME="scion-hub-${HUB_NAME}-allow-iap-ssh"

  # Discover the actual zone of the instance (if it still exists)
  ZONE="$(gcloud compute instances list \
    --filter="name=${INSTANCE_NAME}" \
    --format="value(zone)" \
    --project="${PROJECT_ID}" 2>/dev/null | head -1)" || true
  if [[ -z "$ZONE" ]]; then
    # Instance already deleted; discover an available zone in the region
    ZONE="$(gcloud compute zones list \
      --filter="region=${REGION}" \
      --limit=1 \
      --format="value(name)" \
      --project="${PROJECT_ID}" 2>/dev/null)" || true
  fi
  if [[ -z "$ZONE" ]]; then
    ZONE="${REGION}-b"
    warn "Could not discover zone dynamically; defaulting to ${ZONE}"
  fi
  SA_NAME="scion-hub-${HUB_NAME}"
  SA_EMAIL="${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"

  # Hybrid tier: classify the two NFS firewall rules (if either exists) by
  # marker ownership before printing or deleting anything. This runs even
  # when the hybrid tier is off in the current config: teardown has no
  # other way to know whether the tier was ever turned on for this hub,
  # so it always checks for (and, if marked, later removes) these two
  # rule names. An unmarked name match, or a failure to even list the
  # rules, means HUB_NAME can no longer be trusted to identify only
  # resources this deployment owns, so the whole teardown aborts here --
  # not just the two hybrid rules -- rather than proceeding to delete
  # other resources under a name that turned out to be ambiguous.
  echo ""
  echo "Checking hybrid-tier firewall rule ownership:"
  if ! hybrid_teardown_preflight "$HUB_NAME" "$PROJECT_ID"; then
    exit 1
  fi
  TEARDOWN_HAD_FAILURE=false

  # Hybrid tier: the static internal IP reservation, checked the same
  # way as the firewall rules -- unconditionally, even with the tier off
  # in the current config, and aborting the whole teardown before any
  # delete on an unmarked same-name match.
  echo ""
  echo "Checking hybrid-tier internal IP reservation ownership:"
  hybrid_internal_ip_teardown_check "$HUB_NAME" "$PROJECT_ID" "$REGION"
  if [[ "$HYBRID_INTERNAL_IP_TEARDOWN_FAILED" == "true" ]]; then
    exit 1
  fi

  # Hybrid tier (Kubernetes objects): gke_target.name is read directly,
  # never via the interactive hybrid_read_config, since a --delete run
  # must never prompt to enable the tier. Absent gke_target.name means
  # the tier was never configured for this hub's teardown, so none of
  # the k8s objects are ever touched -- as inert as the tier-off create
  # path. A cluster that's genuinely gone (a positive NOT_FOUND) means
  # its k8s objects went with it, so this prints that and continues;
  # any other describe failure -- unknown, not confirmed gone -- aborts
  # before any delete, the same "uncertainty counts as not gone" rule
  # used throughout this tier's other checks.
  HYBRID_K8S_TEARDOWN_READY=false
  K8S_GKE_NAME="$(config_get 'gke_target.name' '')"
  if [[ -z "$K8S_GKE_NAME" && -z "$CONFIG_FILE" ]]; then
    echo ""
    warn "No --config given, so this teardown cannot tell whether a hybrid tier was ever configured for hub '${HUB_NAME}'. If it was, its Kubernetes objects (default names: namespace $(hybrid_k8s_default_namespace "$HUB_NAME"), PVC $(hybrid_k8s_default_pvc_name "$HUB_NAME"), PV $(hybrid_k8s_pv_name "$HUB_NAME") -- or whatever gke_target.namespace/pvc_name were customized to) will NOT be checked or deleted by this run. Re-run with --config pointing at the same config file used to deploy, to have them checked."
  fi
  if [[ -n "$K8S_GKE_NAME" ]]; then
    GKE_LOCATION="$(config_get 'gke_target.location' '')"
    GKE_PROJECT_RAW="$(config_get 'gke_target.project' '')"
    GKE_NAMESPACE_RAW="$(config_get 'gke_target.namespace' "$(hybrid_k8s_default_namespace "$HUB_NAME")")"
    GKE_PVC_NAME_RAW="$(config_get 'gke_target.pvc_name' "$(hybrid_k8s_default_pvc_name "$HUB_NAME")")"
    GKE_NAME="$K8S_GKE_NAME"
    GKE_PROJECT="${GKE_PROJECT_RAW:-$PROJECT_ID}"
    # Same field-syntax and project-match validation the create path
    # applies via hybrid_read_config -- a malformed or foreign-project
    # gke_target must be refused here too, not just on create.
    _hybrid_validate_target_fields "$GKE_NAME" "$GKE_LOCATION" "$GKE_PROJECT" "$GKE_NAMESPACE_RAW" "$GKE_PVC_NAME_RAW" "$PROJECT_ID"
    GKE_NAMESPACE="$GKE_NAMESPACE_RAW"
    GKE_PVC_NAME="$GKE_PVC_NAME_RAW"
    echo ""
    echo "Checking hybrid-tier Kubernetes object ownership:"
    CLUSTER_DESCRIBE_ERR="$(mktemp)"
    if gcloud container clusters describe "$GKE_NAME" --location="$GKE_LOCATION" \
        --project="$GKE_PROJECT" --quiet >/dev/null 2>"${CLUSTER_DESCRIBE_ERR}"; then
      hybrid_k8s_setup_kubeconfig
      hybrid_k8s_teardown_check "$HUB_NAME"
      if [[ "$HYBRID_K8S_TEARDOWN_FAILED" == "true" ]]; then
        rm -f "${CLUSTER_DESCRIBE_ERR}"
        exit 1
      fi
      HYBRID_K8S_TEARDOWN_READY=true
    elif _hybrid_gcloud_not_found "$(cat "${CLUSTER_DESCRIBE_ERR}")"; then
      echo "  GKE cluster ${GKE_NAME} not found; its Kubernetes objects went with it."
    else
      err "Could not confirm whether GKE cluster ${GKE_NAME} still exists:"
      err "  $(cat "${CLUSTER_DESCRIBE_ERR}")"
      rm -f "${CLUSTER_DESCRIBE_ERR}"
      exit 1
    fi
    rm -f "${CLUSTER_DESCRIBE_ERR}"
  fi

  # _hybrid_k8s_display KIND — "kind/name" for the kind hybrid_k8s_
  # teardown_check queued, using the names it resolved (HYBRID_K8S_PVC_
  # NAME/HYBRID_K8S_PV_NAME/HYBRID_K8S_NAMESPACE), for both the
  # will-delete list below and the final summary.
  _hybrid_k8s_display() {
    case "$1" in
      pvc) echo "persistentvolumeclaim/${HYBRID_K8S_PVC_NAME}" ;;
      pv) echo "persistentvolume/${HYBRID_K8S_PV_NAME}" ;;
      namespace) echo "namespace/${HYBRID_K8S_NAMESPACE}" ;;
    esac
  }

  echo ""
  echo "The following resources will be deleted:"
  if [[ "$HYBRID_K8S_TEARDOWN_READY" == "true" ]]; then
    for k8s_kind in ${HYBRID_K8S_TEARDOWN_DELETE[@]+"${HYBRID_K8S_TEARDOWN_DELETE[@]}"}; do
      echo "  Kubernetes object: $(_hybrid_k8s_display "$k8s_kind") (hybrid tier)"
    done
  fi
  echo "  Cloud Run service: ${PROXY_SERVICE} (region: ${REGION})"
  echo "  GCE VM:            ${INSTANCE_NAME} (zone: ${ZONE})"
  echo "  Cloud NAT:         ${NAT_NAME} (router: ${ROUTER_NAME})"
  echo "  Cloud Router:      ${ROUTER_NAME} (region: ${REGION})"
  echo "  Service account:   ${SA_EMAIL}"
  echo "  Firewall rule:     ${FW_RULE_NAME}"
  for name in ${HYBRID_TEARDOWN_DELETE[@]+"${HYBRID_TEARDOWN_DELETE[@]}"}; do
    echo "  Firewall rule:     ${name} (hybrid tier)"
  done
  echo ""
  if [[ -n "$CONFIG_FILE" && ! -t 0 ]]; then
    info "Non-interactive mode: proceeding with teardown."
  else
    read -rp "Continue? [y/N]: " CONFIRM
    if [[ "$(echo "$CONFIRM" | tr '[:upper:]' '[:lower:]')" != "y" ]]; then
      echo "Aborted."
      exit 0
    fi
  fi

  # Stop at the first failure: a hybrid-tier Kubernetes object delete
  # failure keeps Cloud Run, the VM, and every resource below untouched
  # (K8S_TEARDOWN_OK gates all of them) rather than only skipping the
  # rest of the k8s sub-chain -- the k8s objects are deleted first,
  # before anything else, precisely so a failure here can still protect
  # everything that hasn't been touched yet. This still reaches the
  # summary below, which reports every resource kept, not just the ones
  # actually deleted.
  K8S_TEARDOWN_OK=true
  PROXY_SERVICE_DELETED=false
  VM_DELETED=false
  VM_GONE=false
  NAT_DELETED=false
  ROUTER_DELETED=false
  SA_DELETED=false
  FW_RULE_DELETED=false
  HYBRID_TEARDOWN_DELETED=()

  if [[ "$HYBRID_K8S_TEARDOWN_READY" == "true" ]]; then
    info "Deleting hybrid-tier Kubernetes objects..."
    hybrid_k8s_teardown_delete
    if [[ ${#HYBRID_K8S_TEARDOWN_DELETE_FAILED[@]} -gt 0 ]]; then
      TEARDOWN_HAD_FAILURE=true
      K8S_TEARDOWN_OK=false
      err "Stopping teardown here: a hybrid-tier Kubernetes object failed to delete. Cloud Run, the VM, and every other resource below are being kept untouched; re-run teardown once the failure above is resolved."
    fi
  fi

  if [[ "$K8S_TEARDOWN_OK" == "true" ]]; then
    info "Deleting Cloud Run IAP proxy service..."
    if gcloud run services delete "${PROXY_SERVICE}" \
        --region="${REGION}" --project="${PROJECT_ID}" --quiet 2>/dev/null; then
      echo "  Deleted: ${PROXY_SERVICE}"
      PROXY_SERVICE_DELETED=true
    else
      warn "Cloud Run service ${PROXY_SERVICE} not found or already deleted."
    fi

    # The hybrid NFS firewall rules are only safe to delete once this VM
    # is confirmed gone. With the tier off (no hybrid rules queued), a VM
    # delete failure warns and continues exactly as it always has --
    # nothing downstream depends on the VM's fate. With the tier on,
    # "gone" must be a positive, project-wide answer rather than inferred
    # from a `describe` in a possibly-wrong zone (ZONE above falls back
    # to a guess when its own discovery call fails): a non-zero exit from
    # the check itself means unknown, and unknown is never treated as
    # gone.
    info "Deleting GCE VM..."
    if gcloud compute instances delete "${INSTANCE_NAME}" \
        --zone="${ZONE}" --project="${PROJECT_ID}" --quiet 2>/dev/null; then
      echo "  Deleted: ${INSTANCE_NAME}"
      VM_GONE=true
      VM_DELETED=true
    elif [[ ${#HYBRID_TEARDOWN_DELETE[@]} -eq 0 ]]; then
      warn "GCE VM ${INSTANCE_NAME} not found or already deleted."
      VM_GONE=true
    else
      VM_LIST_ERR_FILE="$(mktemp)"
      # This list has no --zones, so it's a project-wide AggregatedList.
      # The SDK's default compute/allow_partial_error=true downgrades an
      # UNREACHABLE zone to a stderr warning and exit 0 with that zone's
      # instances silently missing from the output -- which would read as
      # "gone" even when the VM's own delete just failed because that
      # same zone is down. Setting this to false makes a partial result
      # raise instead, landing in the "could not confirm" branch below
      # rather than being misread as "gone".
      if VM_LIST_OUTPUT="$(CLOUDSDK_COMPUTE_ALLOW_PARTIAL_ERROR=false gcloud compute instances list --project="${PROJECT_ID}" \
          --filter="name=${INSTANCE_NAME}" --format="value(name)" 2>"${VM_LIST_ERR_FILE}")"; then
        if [[ -z "$VM_LIST_OUTPUT" ]]; then
          warn "GCE VM ${INSTANCE_NAME} not found or already deleted."
          VM_GONE=true
        else
          err "Failed to delete GCE VM ${INSTANCE_NAME}; it still exists."
          TEARDOWN_HAD_FAILURE=true
        fi
      else
        err "Failed to delete GCE VM ${INSTANCE_NAME}, and could not confirm whether it still exists:"
        err "  $(cat "${VM_LIST_ERR_FILE}")"
        TEARDOWN_HAD_FAILURE=true
      fi
      rm -f "${VM_LIST_ERR_FILE}"
    fi

    info "Deleting Cloud NAT..."
    if gcloud compute routers nats delete "${NAT_NAME}" \
        --router="${ROUTER_NAME}" \
        --region="${REGION}" --project="${PROJECT_ID}" --quiet 2>/dev/null; then
      echo "  Deleted: ${NAT_NAME}"
      NAT_DELETED=true
    else
      warn "Cloud NAT ${NAT_NAME} not found or already deleted."
    fi

    info "Deleting Cloud Router..."
    if gcloud compute routers delete "${ROUTER_NAME}" \
        --region="${REGION}" --project="${PROJECT_ID}" --quiet 2>/dev/null; then
      echo "  Deleted: ${ROUTER_NAME}"
      ROUTER_DELETED=true
    else
      warn "Cloud Router ${ROUTER_NAME} not found or already deleted."
    fi

    info "Deleting service account..."
    if gcloud iam service-accounts delete "${SA_EMAIL}" \
        --project="${PROJECT_ID}" --quiet 2>/dev/null; then
      echo "  Deleted: ${SA_EMAIL}"
      SA_DELETED=true
    else
      warn "Service account ${SA_EMAIL} not found or already deleted."
    fi

    # Note: We intentionally do NOT revoke roles/iap.tunnelResourceAccessor from
    # the deployer. This role is bound to the operator (not a service account) and
    # may be used for IAP SSH access to other VMs in the project. Revoking it here
    # would silently break access to those other resources.
    info "Skipping IAP tunnel role cleanup (operator may use it for other VMs)."

    info "Deleting IAP SSH firewall rule..."
    if gcloud compute firewall-rules delete "${FW_RULE_NAME}" \
        --project="${PROJECT_ID}" --quiet 2>/dev/null; then
      echo "  Deleted: ${FW_RULE_NAME}"
      FW_RULE_DELETED=true
    else
      warn "Firewall rule ${FW_RULE_NAME} not found or already deleted."
    fi

    if [[ ${#HYBRID_TEARDOWN_DELETE[@]} -gt 0 ]]; then
      if [[ "$VM_GONE" == "true" ]]; then
        info "Deleting hybrid-tier firewall rules..."
        hybrid_teardown_delete "$PROJECT_ID"
        if [[ ${#HYBRID_TEARDOWN_DELETE_FAILED[@]} -gt 0 ]]; then
          TEARDOWN_HAD_FAILURE=true
        fi
      else
        err "Keeping the hybrid-tier NFS firewall rules because GCE VM ${INSTANCE_NAME} still exists; tcp:2049 access stays restricted. Re-run teardown after the VM is deleted."
        TEARDOWN_HAD_FAILURE=true
      fi
    fi
  else
    TEARDOWN_HAD_FAILURE=true
  fi

  echo ""
  echo -e "${BOLD}=== Teardown Complete ===${RESET}"
  echo ""
  if [[ "$HYBRID_K8S_TEARDOWN_READY" == "true" ]]; then
    for k8s_kind in ${HYBRID_K8S_TEARDOWN_DELETED[@]+"${HYBRID_K8S_TEARDOWN_DELETED[@]}"}; do
      echo "  Deleted Kubernetes object:  $(_hybrid_k8s_display "$k8s_kind")"
    done
    for k8s_kind in ${HYBRID_K8S_TEARDOWN_DELETE_FAILED[@]+"${HYBRID_K8S_TEARDOWN_DELETE_FAILED[@]}"}; do
      echo "  Kept Kubernetes object:     $(_hybrid_k8s_display "$k8s_kind") (delete failed or not attempted)"
    done
  fi
  if [[ -n "$K8S_GKE_NAME" ]]; then
    echo "  Kubernetes cluster:         ${K8S_GKE_NAME} -- never deleted (this tier never creates or deletes the cluster itself)"
  fi
  if [[ "$PROXY_SERVICE_DELETED" == "true" ]]; then
    echo "  Deleted Cloud Run service:  ${PROXY_SERVICE}"
  else
    echo "  Kept Cloud Run service:     ${PROXY_SERVICE}"
  fi
  if [[ "$VM_DELETED" == "true" ]]; then
    echo "  Deleted GCE VM:             ${INSTANCE_NAME}"
  else
    echo "  Kept GCE VM:                ${INSTANCE_NAME}"
  fi
  if [[ "$NAT_DELETED" == "true" ]]; then
    echo "  Deleted Cloud NAT:          ${NAT_NAME}"
  else
    echo "  Kept Cloud NAT:             ${NAT_NAME}"
  fi
  if [[ "$ROUTER_DELETED" == "true" ]]; then
    echo "  Deleted Cloud Router:       ${ROUTER_NAME}"
  else
    echo "  Kept Cloud Router:          ${ROUTER_NAME}"
  fi
  if [[ "$SA_DELETED" == "true" ]]; then
    echo "  Deleted service account:    ${SA_EMAIL}"
  else
    echo "  Kept service account:       ${SA_EMAIL}"
  fi
  if [[ "$FW_RULE_DELETED" == "true" ]]; then
    echo "  Deleted firewall rule:      ${FW_RULE_NAME}"
  else
    echo "  Kept firewall rule:         ${FW_RULE_NAME}"
  fi
  for name in ${HYBRID_TEARDOWN_DELETED[@]+"${HYBRID_TEARDOWN_DELETED[@]}"}; do
    echo "  Deleted firewall rule:     ${name}"
  done
  if [[ "$TEARDOWN_HAD_FAILURE" == "true" ]]; then
    err "Teardown completed with at least one failure reported above."
    exit 1
  fi
  exit 0
fi

# ===================================================================
# Phase 1: Prerequisites
# ===================================================================
section "Phase 1: Prerequisites"

# --- GCP project ---
PROJECT_ID="$(config_get 'project_id' '')"
if [[ -z "$PROJECT_ID" ]]; then
  info "Detecting GCP project..."
  PROJECT_ID="$(gcloud config get-value project 2>/dev/null)" || true
fi
if [[ -z "$PROJECT_ID" ]]; then
  err "No GCP project configured. Set project_id in config or run: gcloud config set project PROJECT_ID"
  exit 1
fi
echo "  Project: ${PROJECT_ID}"

# --- Interactive prompts (or config file values) ---
HUB_NAME="$(config_get 'hub_name' '')"
if [[ -n "$HUB_NAME" ]]; then
  # Validate config-provided hub name
  if [[ ${#HUB_NAME} -gt 20 ]]; then
    err "Hub name '${HUB_NAME}' from config is ${#HUB_NAME} chars; max is 20."
    exit 1
  fi
  if [[ ! "$HUB_NAME" =~ ^[a-z][a-z0-9-]*$ ]]; then
    err "Hub name '${HUB_NAME}' from config is invalid. It must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens."
    exit 1
  fi
else
  if [[ -n "$CONFIG_FILE" && ! -t 0 ]]; then
    err "Required config value 'hub_name' is missing and stdin is not a terminal."
    exit 1
  fi
  while true; do
    read -rp "Hub name [my-hub]: " HUB_NAME
    HUB_NAME="${HUB_NAME:-my-hub}"
    if [[ ${#HUB_NAME} -gt 20 ]]; then
      warn "Hub name '${HUB_NAME}' is ${#HUB_NAME} chars; max is 20 (GCP service-account ID limit)."
      echo "  Please choose a shorter name."
      continue
    fi
    if [[ ! "$HUB_NAME" =~ ^[a-z][a-z0-9-]*$ ]]; then
      warn "Hub name '${HUB_NAME}' is invalid. It must start with a lowercase letter and contain only lowercase letters, numbers, and hyphens."
      echo "  Please choose a valid name."
      continue
    fi
    break
  done
fi

REGION="$(config_get 'region' '')"
if [[ -z "$REGION" ]]; then
  config_prompt REGION "GCP region [us-central1]: " "us-central1"
fi

CFG_MACHINE_SIZE="$(config_get 'machine_size' '')"
if [[ -n "$CFG_MACHINE_SIZE" ]]; then
  case "$CFG_MACHINE_SIZE" in
    small)  MACHINE_TYPE="e2-standard-4" ;;
    medium) MACHINE_TYPE="n2-standard-16" ;;
    *) err "Invalid machine_size in config: '$CFG_MACHINE_SIZE' (expected: small, medium)"; exit 1 ;;
  esac
else
  echo "Machine size:"
  echo "  1) Small  (e2-standard-4,  4 vCPU,  16GB) - up to ~10 agents"
  echo "  2) Medium (n2-standard-16, 16 vCPU, 64GB) - up to ~50 agents"
  config_prompt SIZE_CHOICE "Select [1]: " "1"

  case "$SIZE_CHOICE" in
    1) MACHINE_TYPE="e2-standard-4" ;;
    2) MACHINE_TYPE="n2-standard-16" ;;
    *) err "Invalid selection: $SIZE_CHOICE"; exit 1 ;;
  esac
fi

CFG_DISK_SIZE="$(config_get 'disk_size_gb' '')"
if [[ -n "$CFG_DISK_SIZE" ]]; then
  if ! [[ "$CFG_DISK_SIZE" =~ ^[0-9]+$ ]]; then
    err "Invalid disk_size_gb in config: '$CFG_DISK_SIZE' (must be a number)"
    exit 1
  fi
  DISK_SIZE="${CFG_DISK_SIZE}GB"
else
  echo "Disk size:"
  echo "  1) 200 GB (default)"
  echo "  2) 500 GB"
  echo "  3) Custom"
  config_prompt DISK_CHOICE "Select [1]: " "1"

  case "$DISK_CHOICE" in
    1) DISK_SIZE="200GB" ;;
    2) DISK_SIZE="500GB" ;;
    3)
      config_prompt CUSTOM_DISK "Enter disk size in GB: " ""
      if [[ -z "$CUSTOM_DISK" ]] || ! [[ "$CUSTOM_DISK" =~ ^[0-9]+$ ]]; then
        err "Invalid disk size: $CUSTOM_DISK"
        exit 1
      fi
      DISK_SIZE="${CUSTOM_DISK}GB"
      ;;
    *) err "Invalid selection: $DISK_CHOICE"; exit 1 ;;
  esac
fi

CFG_CHAT_PLUGINS="$(config_get 'chat_plugins' '')"
CHAT_PLUGINS=()
if [[ -n "$CONFIG_FILE" && -f "$CONFIG_FILE" ]]; then
  # Parse chat_plugins from config (space-separated list from config_get)
  if [[ -n "$CFG_CHAT_PLUGINS" ]]; then
    for plugin in $CFG_CHAT_PLUGINS; do
      case "$plugin" in
        telegram|discord|slack|teams) CHAT_PLUGINS+=("$plugin") ;;
        *) warn "Ignoring unknown chat plugin in config: $plugin" ;;
      esac
    done
  fi
  # Empty list or missing key = no plugins (this is fine)
else
  echo "Chat integrations to install:"
  echo "  1) Telegram"
  echo "  2) Discord"
  echo "  3) Slack"
  echo "  4) Teams"
  echo "  5) None"
  config_prompt CHAT_CHOICE "Select (comma-separated) [5]: " "5"

  IFS=',' read -ra CHAT_SELECTIONS <<< "$CHAT_CHOICE"
  for sel in "${CHAT_SELECTIONS[@]}"; do
    sel="$(echo "$sel" | tr -d ' ')"
    case "$sel" in
      1) CHAT_PLUGINS+=("telegram") ;;
      2) CHAT_PLUGINS+=("discord") ;;
      3) CHAT_PLUGINS+=("slack") ;;
      4) CHAT_PLUGINS+=("teams") ;;
      5) ;;  # None
      *) warn "Ignoring unknown chat selection: $sel" ;;
    esac
  done
fi

CFG_IMAGE_SOURCE="$(config_get 'container_images.source' '')"
if [[ -n "$CFG_IMAGE_SOURCE" ]]; then
  case "$CFG_IMAGE_SOURCE" in
    registry)
      IMAGE_SOURCE="registry"
      IMAGE_REGISTRY="$(config_get 'container_images.registry' '')"
      if [[ -z "$IMAGE_REGISTRY" ]]; then
        err "container_images.source is 'registry' but container_images.registry is missing from config."
        exit 1
      fi
      ;;
    build)
      IMAGE_SOURCE="build"
      IMAGE_REGISTRY="localhost/scion"
      ;;
    *) err "Invalid container_images.source in config: '$CFG_IMAGE_SOURCE' (expected: registry, build)"; exit 1 ;;
  esac
else
  echo "Container images:"
  echo "  1) Provide a registry path (images already pushed)"
  echo "  2) Build images on the VM (requires 10-15 min, ~30GB disk)"
  config_prompt IMAGE_CHOICE "Select [2]: " "2"

  case "$IMAGE_CHOICE" in
    1)
      IMAGE_SOURCE="registry"
      config_prompt IMAGE_REGISTRY "Registry path (e.g. us-docker.pkg.dev/my-project/scion): " ""
      if [[ -z "$IMAGE_REGISTRY" ]]; then
        err "Registry path cannot be empty."
        exit 1
      fi
      ;;
    2)
      IMAGE_SOURCE="build"
      IMAGE_REGISTRY="localhost/scion"
      ;;
    *) err "Invalid selection: $IMAGE_CHOICE"; exit 1 ;;
  esac
fi

# Force a rebuild even if the version marker and all 3 images already match
# the requested VERSION. Defaults to false: normally a matching marker means
# Phase 3b can skip the 10-15 min build entirely. Either the config key or
# the --rebuild-images CLI flag forces a rebuild.
CFG_FORCE_REBUILD="$(config_get 'container_images.force_rebuild' 'false')"
if [[ "$CLI_REBUILD_IMAGES" == "true" ]]; then
  CFG_FORCE_REBUILD="true"
fi

# --- Admin email ---
ADMIN_EMAIL="$(config_get 'admin_email' '')"
if [[ -z "$ADMIN_EMAIL" ]]; then
  DEPLOYER_DEFAULT="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null | head -1)" || true
  if [[ -n "$CONFIG_FILE" && ! -t 0 ]]; then
    # Non-interactive: use deployer identity as default
    ADMIN_EMAIL="${DEPLOYER_DEFAULT:-}"
  else
    echo ""
    echo "Hub admin email (will be granted super-admin access):"
    if [[ -n "$DEPLOYER_DEFAULT" ]]; then
      read -rp "Admin email [${DEPLOYER_DEFAULT}]: " ADMIN_EMAIL
      ADMIN_EMAIL="${ADMIN_EMAIL:-$DEPLOYER_DEFAULT}"
    else
      read -rp "Admin email: " ADMIN_EMAIL
    fi
  fi
fi
if [[ -z "$ADMIN_EMAIL" ]]; then
  warn "No admin email provided. You can add one later in settings.yaml under server.hub.admin_emails."
fi

# --- Update policy ---
CFG_UPDATE_POLICY="$(config_get 'update_policy' '')"
if [[ -n "$CFG_UPDATE_POLICY" ]]; then
  case "$CFG_UPDATE_POLICY" in
    auto|notify|disabled) UPDATE_POLICY="$CFG_UPDATE_POLICY" ;;
    *) err "Invalid update_policy in config: '$CFG_UPDATE_POLICY' (expected: auto, notify, disabled)"; exit 1 ;;
  esac
else
  echo ""
  echo "Automatic update policy:"
  echo "  1) Auto - automatically install new releases (recommended)"
  echo "  2) Notify - check for updates, notify admin only"
  echo "  3) Disabled - no automatic update checking"
  config_prompt UPDATE_CHOICE "Select [1]: " "1"
  case "${UPDATE_CHOICE:-1}" in
    1) UPDATE_POLICY="auto" ;;
    2) UPDATE_POLICY="notify" ;;
    3) UPDATE_POLICY="disabled" ;;
    *) UPDATE_POLICY="auto" ;;
  esac
fi

# --- Hybrid tier (optional GKE attach target) ---
hybrid_read_config "$PROJECT_ID" "$HUB_NAME"
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  echo "  Hybrid tier: enabled (GKE cluster: ${GKE_NAME}, location: ${GKE_LOCATION})"
  # GKE nodes pull the agent image from the registry named in
  # settings.yaml's image_registry field; they have no access to the VM's
  # own local image store, which is exactly where container_images.
  # source=build puts it -- and every loopback spelling of a registry
  # host (localhost, 127.0.0.0/8, ::1, 0.0.0.0, with or without a port)
  # names that same VM from a GKE node's point of view too. Refused
  # here, before any create, rather than left to fail confusingly once a
  # pod actually tries to pull it.
  if [[ "$IMAGE_SOURCE" == "build" ]]; then
    err "The hybrid tier is on, but container_images.source is 'build'. GKE nodes cannot pull images from the VM's local Docker store."
    err "Set container_images.source to 'registry' and container_images.registry to a registry the cluster's node service account can read (for example an Artifact Registry repository with artifactregistry.reader granted to that service account)."
    exit 1
  fi
  if _hybrid_registry_is_loopback "$IMAGE_REGISTRY"; then
    err "The hybrid tier is on, but container_images.registry ('${IMAGE_REGISTRY}') names this VM itself (a loopback address). GKE nodes cannot reach it there."
    err "Set container_images.registry to a registry the cluster's node service account can read (for example an Artifact Registry repository with artifactregistry.reader granted to that service account)."
    exit 1
  fi
fi

# Derived values
info "Selecting zone in ${REGION}..."
ZONE="$(gcloud compute zones list \
  --filter="region=${REGION}" \
  --limit=1 \
  --format="value(name)" \
  --project="${PROJECT_ID}" 2>/dev/null)" || true
if [[ -z "$ZONE" ]]; then
  ZONE="${REGION}-b"
  warn "Could not discover zone dynamically; defaulting to ${ZONE}"
fi
INSTANCE_NAME="scion-hub-${HUB_NAME}"
SA_NAME="scion-hub-${HUB_NAME}"
# GCP service-account IDs must be 6-30 chars; truncate as a safety net
if [[ ${#SA_NAME} -gt 30 ]]; then
  SA_NAME="${SA_NAME:0:30}"
  warn "Service-account name truncated to 30 chars: ${SA_NAME}"
fi
SA_EMAIL="${SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"

echo ""
echo "  Hub name:     ${HUB_NAME}"
echo "  Region:       ${REGION}"
echo "  Zone:         ${ZONE}"
echo "  Machine type: ${MACHINE_TYPE}"
echo "  Disk size:    ${DISK_SIZE}"
echo "  Instance:     ${INSTANCE_NAME}"
if [[ ${#CHAT_PLUGINS[@]} -gt 0 ]]; then
  echo "  Chat plugins: ${CHAT_PLUGINS[*]}"
else
  echo "  Chat plugins: (none)"
fi
if [[ "$IMAGE_SOURCE" == "registry" ]]; then
  echo "  Images:       registry (${IMAGE_REGISTRY})"
else
  echo "  Images:       build on VM"
fi
if [[ -n "$ADMIN_EMAIL" ]]; then
  echo "  Admin:        ${ADMIN_EMAIL}"
fi
echo "  Update policy: ${UPDATE_POLICY}"

# --- Release version ---
if [[ -z "$VERSION" ]]; then
  info "Detecting latest Scion release..."
  # Try /releases/latest first (excludes pre-releases), fall back to
  # /releases (includes pre-releases) when no full release exists yet.
  RELEASE_JSON="$(curl -fsSL https://api.github.com/repos/GoogleCloudPlatform/scion/releases/latest 2>/dev/null)" || true
  if [[ -z "$RELEASE_JSON" ]]; then
    warn "/releases/latest returned no data (pre-releases only?); querying /releases..."
    RELEASE_JSON="$(curl -fsSL 'https://api.github.com/repos/GoogleCloudPlatform/scion/releases?per_page=1')" \
      || { err "Could not fetch releases from GitHub API."; exit 1; }
  fi
  if command -v jq &>/dev/null; then
    VERSION="$(echo "$RELEASE_JSON" | jq -r 'select(. != null) | if type == "array" then .[0].tag_name else .tag_name end // empty')" || true
  else
    VERSION="$(echo "$RELEASE_JSON" | grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name":\s*"([^"]+)".*/\1/')" || true
  fi
  if [[ -z "$VERSION" ]]; then
    err "Could not detect latest release. Use --version to specify."
    exit 1
  fi
fi
echo "  Scion version: ${VERSION}"

# Validate VERSION strictly. It is interpolated into several remote-command
# strings sent over `gcloud compute ssh --command=...` (git clone/checkout,
# the Phase 3b idempotency check, and the marker write inside a nested
# `bash -c '...'` body). A version containing shell metacharacters would
# otherwise be executed on the VM — git check-ref-format happily accepts a
# tag name like "v1;id", and this script's own --version flag or the GitHub
# release auto-detect could pass one through unchecked. Restrict to the
# characters a real release tag needs. Require an alphanumeric first
# character (after the optional 'v') so a leading '-' can never be mistaken
# for a flag by a downstream command.
if [[ ! "$VERSION" =~ ^v?[0-9A-Za-z][0-9A-Za-z._-]*$ ]]; then
  err "Invalid version: '${VERSION}' (expected characters: letters, digits, '.', '_', '-', optionally prefixed with 'v'; must not start with '-')."
  exit 1
fi

# --- Release channel ---
CFG_RELEASE_CHANNEL="$(config_get 'release_channel' '')"
if [[ -n "$CFG_RELEASE_CHANNEL" ]]; then
  case "$CFG_RELEASE_CHANNEL" in
    stable|preview|nightly) RELEASE_CHANNEL="$CFG_RELEASE_CHANNEL" ;;
    *) err "Invalid release_channel in config: '$CFG_RELEASE_CHANNEL' (expected: stable, preview, nightly)"; exit 1 ;;
  esac
else
  # Default to nightly. When running from a git clone (the common agent
  # path), there is no release artifact to detect a channel from. Nightly
  # is the appropriate default for latest-code deployments.
  RELEASE_CHANNEL="nightly"
fi
echo "  Release channel: ${RELEASE_CHANNEL}"

# --- Validate gcloud auth ---
info "Validating gcloud authentication..."
# Try gcloud auth list first (works with service accounts and CI),
# fall back to gcloud config get account.
ACCOUNT="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null | head -1)" || true
if [[ -z "$ACCOUNT" ]]; then
  ACCOUNT="$(gcloud config get-value account 2>/dev/null)" || true
fi
if [[ -z "$ACCOUNT" ]]; then
  err "Not authenticated with gcloud. Run: gcloud auth login"
  exit 1
fi
echo "  Authenticated as: ${ACCOUNT}"

# ===================================================================
# Phase 2: GCP Resources
# ===================================================================
section "Phase 2: GCP Resources"

# --- Enable APIs ---
# Enables only whatever's actually missing, not the whole list every run:
# some validation runners don't hold serviceusage.services.enable and
# would fail on an `enable` call for an API that's already on. The
# hybrid tier additionally needs container.googleapis.com.
info "Enabling required APIs..."
REQUIRED_APIS=(
  compute.googleapis.com
  run.googleapis.com
  iap.googleapis.com
  cloudbuild.googleapis.com
  artifactregistry.googleapis.com
)
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  REQUIRED_APIS+=(container.googleapis.com)
fi
API_LIST_ERR_FILE="$(mktemp)"
if ENABLED_APIS="$(gcloud services list --enabled --project="${PROJECT_ID}" \
    --format="value(config.name)" 2>"${API_LIST_ERR_FILE}")"; then
  rm -f "${API_LIST_ERR_FILE}"
  MISSING_APIS=()
  for api in "${REQUIRED_APIS[@]}"; do
    if ! grep -qx "$api" <<< "$ENABLED_APIS"; then
      MISSING_APIS+=("$api")
    fi
  done
  if [[ ${#MISSING_APIS[@]} -gt 0 ]]; then
    gcloud services enable "${MISSING_APIS[@]}" --project="${PROJECT_ID}" --quiet
  fi
elif [[ "$HYBRID_ENABLED" == "true" ]]; then
  err "Could not list enabled APIs for project ${PROJECT_ID}, so it's unknown whether container.googleapis.com (needed for the hybrid tier) is already enabled. Refusing to guess: enabling it unconditionally would fail on a runner without serviceusage.services.enable if it's already on, and skipping it would fail later if it's not."
  err "  $(cat "${API_LIST_ERR_FILE}")"
  rm -f "${API_LIST_ERR_FILE}"
  exit 1
else
  rm -f "${API_LIST_ERR_FILE}"
  # Tier off: unchanged from before this check existed, so this path
  # keeps working everywhere it always has, including on a runner that
  # can't list services but can still call enable on an already-enabled
  # API (a harmless no-op in that direction).
  gcloud services enable "${REQUIRED_APIS[@]}" --project="${PROJECT_ID}" --quiet
fi

# --- Hybrid tier: discovery ---
# Read-only (describe calls only; nothing is created). This runs before
# every create in the rest of Phase 2 -- the service account, IAM
# bindings, Cloud Router, Cloud NAT and the IAP SSH firewall rule -- so a
# bad cluster name, a network mismatch, or an undiscoverable node tag
# never leaves any of those half-created behind. The two hybrid firewall
# rules themselves are created later, immediately before the VM (see
# below): discovery only needs to run before creation starts, not
# immediately before the specific rules that depend on its result.
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  info "Discovering GKE cluster network and node tag..."
  hybrid_discover "default"
  echo "  Node network tag: ${GKE_NODE_TAG}"

  # Everything about the hybrid tier's Kubernetes objects that doesn't
  # depend on the VM's own IP address -- including the marker-refusal
  # check that used to only fire in Phase 4, after the VM, NFS export
  # and firewall rules already existed -- runs here, alongside discovery
  # and before any of those are created. See hybrid_k8s_preflight's own
  # comment for exactly what it does and doesn't cover; the remaining,
  # IP-dependent part of this check happens later, in Phase 4.
  info "Checking hybrid-tier Kubernetes object ownership..."
  hybrid_k8s_preflight "$HUB_NAME"
fi

# --- Cross-org IAP warning (best-effort; never blocks the deploy) ---
# IAP's default (Google-managed) OAuth client only covers same-organization
# use. A custom OAuth client is required when: the deployer is outside the
# project's organization, OR the project is not in a GCP organization at all
# (Google-managed clients don't support no-org projects, full stop -- see
# https://cloud.google.com/iap/docs/custom-oauth-configuration). The no-org
# case is the common first-deploy shape (personal account, OSS project), and
# unlike the cross-org case it is a *certain* answer, not a heuristic -- warn
# on it, don't skip it. See docs/deploy/agent-runbook-single-node-vm.md
# Section 7 (Troubleshooting) for what to do about either case.
#
# The domain comparison itself is a heuristic: it compares the deployer's
# email domain against the org's `displayName`, which the Resource Manager
# API documents as the organization's *primary* Workspace domain. A deployer
# on a secondary or alias domain of the same Workspace org, or a subdomain,
# is legitimately in-org but will not match `displayName` -- that's a known
# false-positive mode, not something this comparison can currently
# distinguish from an actual cross-org deployer. Any inability to *read* the
# data -- the get-ancestors call itself failing, or organizations describe
# failing on a project that does have an org -- degrades to "cannot
# determine, skip the check" rather than guessing; nothing in this block
# ever fails the script, and it only detects and warns -- it never creates
# or configures an OAuth client.
info "Checking for cross-organization IAP mismatch..."
# A successful get-ancestors call always returns at least the project's
# own row, so empty output means the call failed (permissions, etc.), not
# "no organization" -- capture the raw output before parsing so those two
# cases stay distinguishable. The no-org check below depends only on the
# project, not on who's deploying, so it runs for every identity, including
# service accounts.
ANCESTORS="$(gcloud projects get-ancestors "${PROJECT_ID}" \
  --format='value(id,type)' 2>/dev/null)" || ANCESTORS=""
if [[ -z "$ANCESTORS" ]]; then
  echo "  Could not read ancestry for ${PROJECT_ID}; skipping cross-org IAP check."
else
  ORG_ID="$(awk '$2=="organization"{print $1; exit}' <<<"$ANCESTORS")" || true
  if [[ -z "$ORG_ID" ]]; then
    warn "Project ${PROJECT_ID} is not in a GCP organization. IAP's Google-managed OAuth client does not support no-org projects -- a custom OAuth client is required."
    warn "This deploy will continue. See docs/deploy/agent-runbook-single-node-vm.md Section 7 (Troubleshooting, Cross-org IAP) for what to do."
  elif [[ "$ACCOUNT" == *.gserviceaccount.com ]]; then
    # An org was found, so the only remaining step is comparing the
    # deployer's email domain against it -- not a meaningful comparison for
    # a service account, so skip just that step.
    echo "  Deployer is a service account; skipping cross-org domain comparison."
  else
    ORG_DOMAIN="$(gcloud organizations describe "${ORG_ID}" \
      --format='value(displayName)' 2>/dev/null)" || true
    if [[ -z "$ORG_DOMAIN" ]]; then
      echo "  Could not read metadata for organization ${ORG_ID} (likely a permissions gap); skipping cross-org IAP check."
    else
      DEPLOYER_DOMAIN="${ACCOUNT##*@}"
      if [[ "$(echo "$ORG_DOMAIN" | tr '[:upper:]' '[:lower:]')" != "$(echo "$DEPLOYER_DOMAIN" | tr '[:upper:]' '[:lower:]')" ]]; then
        warn "Deployer account (${ACCOUNT}) does not appear to belong to project ${PROJECT_ID}'s organization (${ORG_DOMAIN})."
        warn "Cross-org IAP typically requires a custom OAuth consent screen / OAuth client -- the default consent screen will block authentication."
        warn "(This can also be a false positive if the deployer is on a secondary or alias domain of the same organization.)"
        warn "This deploy will continue. If IAP authentication fails later, or shows an unexpected consent screen, see"
        warn "docs/deploy/agent-runbook-single-node-vm.md Section 7 (Troubleshooting) for the cross-org IAP scenario."
      else
        echo "  Deployer domain matches organization domain (${ORG_DOMAIN}); no cross-org IAP concern detected."
      fi
    fi
  fi
fi

# --- Service account ---
info "Creating service account (if needed)..."
if gcloud iam service-accounts describe "${SA_EMAIL}" \
    --project="${PROJECT_ID}" &>/dev/null; then
  echo "  Service account already exists: ${SA_EMAIL}"
else
  gcloud iam service-accounts create "${SA_NAME}" \
    --display-name="Scion Hub VM (${HUB_NAME})" \
    --description="scion-deployment=${HUB_NAME}" \
    --project="${PROJECT_ID}"
  echo "  Created service account: ${SA_EMAIL}"
fi

# Bind minimal IAM roles (idempotent)
# artifactregistry.writer lets the VM build and push the Cloud Run IAP proxy
# image directly to Artifact Registry (see Phase 4).
info "Binding IAM roles..."
for ROLE in roles/logging.logWriter roles/monitoring.metricWriter roles/cloudtrace.agent roles/artifactregistry.writer; do
  gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --member="serviceAccount:${SA_EMAIL}" \
    --role="${ROLE}" \
    --quiet &>/dev/null
done
echo "  Roles bound: logging.logWriter, monitoring.metricWriter, cloudtrace.agent, artifactregistry.writer"

# --- Grant deployer IAP tunnel access (required for SSH to --no-address VMs) ---
info "Granting IAP tunnel access to deployer..."
DEPLOYER_EMAIL="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null | head -1)" || true
if [[ -z "$DEPLOYER_EMAIL" ]]; then
  DEPLOYER_EMAIL="$(gcloud config get-value account 2>/dev/null)" || true
fi
if [[ -n "$DEPLOYER_EMAIL" ]]; then
  if [[ "$DEPLOYER_EMAIL" == *.gserviceaccount.com ]]; then
    DEPLOYER_MEMBER="serviceAccount:${DEPLOYER_EMAIL}"
  else
    DEPLOYER_MEMBER="user:${DEPLOYER_EMAIL}"
  fi
  if gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --member="${DEPLOYER_MEMBER}" \
    --role="roles/iap.tunnelResourceAccessor" \
    --quiet >/dev/null 2>&1; then
    echo "  IAP tunnel access granted to: ${DEPLOYER_EMAIL}"
  else
    warn "Failed to grant roles/iap.tunnelResourceAccessor to ${DEPLOYER_EMAIL}."
    warn "SSH to the VM may fail if you do not have this role. Please ensure it is granted manually."
  fi
else
  warn "Could not determine deployer identity; skipping IAP tunnel role grant."
  warn "SSH to the VM may fail. Grant roles/iap.tunnelResourceAccessor manually."
fi

# --- Cloud Router + Cloud NAT ---
# The VM has no public IP (--no-address).  Cloud NAT gives it outbound internet
# access so cloud-init can install packages, download binaries, and pull images.
ROUTER_NAME="scion-hub-${HUB_NAME}-router"
NAT_NAME="scion-hub-${HUB_NAME}-nat"

info "Creating Cloud Router (if needed)..."
if gcloud compute routers describe "${ROUTER_NAME}" \
    --region="${REGION}" --project="${PROJECT_ID}" &>/dev/null; then
  echo "  Cloud Router already exists: ${ROUTER_NAME}"
else
  gcloud compute routers create "${ROUTER_NAME}" \
    --region="${REGION}" \
    --project="${PROJECT_ID}" \
    --network=default \
    --description="scion-deployment=${HUB_NAME}" \
    --quiet
  echo "  Created Cloud Router: ${ROUTER_NAME}"
fi

info "Creating Cloud NAT (if needed)..."
if gcloud compute routers nats describe "${NAT_NAME}" \
    --router="${ROUTER_NAME}" \
    --region="${REGION}" --project="${PROJECT_ID}" &>/dev/null; then
  echo "  Cloud NAT already exists: ${NAT_NAME}"
else
  gcloud compute routers nats create "${NAT_NAME}" \
    --router="${ROUTER_NAME}" \
    --region="${REGION}" \
    --project="${PROJECT_ID}" \
    --auto-allocate-nat-external-ips \
    --nat-all-subnet-ip-ranges \
    --quiet
  echo "  Created Cloud NAT: ${NAT_NAME}"
fi

# --- IAP SSH firewall rule ---
# gcloud compute ssh via IAP tunneling requires TCP:22 from 35.235.240.0/20.
FW_RULE_NAME="scion-hub-${HUB_NAME}-allow-iap-ssh"
info "Creating IAP SSH firewall rule (if needed)..."
if gcloud compute firewall-rules describe "${FW_RULE_NAME}" \
    --project="${PROJECT_ID}" &>/dev/null; then
  echo "  Firewall rule already exists: ${FW_RULE_NAME}"
else
  gcloud compute firewall-rules create "${FW_RULE_NAME}" \
    --project="${PROJECT_ID}" \
    --network=default \
    --direction=INGRESS \
    --action=ALLOW \
    --rules=tcp:22 \
    --source-ranges=35.235.240.0/20 \
    --description="Allow SSH via IAP tunneling for Scion Hub | scion-deployment=${HUB_NAME}" \
    --quiet
  echo "  Created firewall rule: ${FW_RULE_NAME}"
fi

# --- Hybrid tier: NFS firewall rules ---
# Discovery already ran above, right after the APIs were enabled and
# before any of Phase 2's creates -- see the comment there. Only the
# rule-creation step is here, immediately before the VM.
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  info "Creating hybrid-tier NFS firewall rules (if needed)..."
  hybrid_ensure_firewall_rules "$HUB_NAME" "$PROJECT_ID" "default"
fi

# --- Create VM ---
VM_EXTRA_CREATE_ARGS=()
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  VM_EXTRA_CREATE_ARGS=(--tags="$(hybrid_vm_tag "$HUB_NAME")")
fi

info "Creating GCE VM (if needed)..."
if gcloud compute instances describe "${INSTANCE_NAME}" \
    --zone="${ZONE}" --project="${PROJECT_ID}" &>/dev/null; then
  echo "  VM already exists: ${INSTANCE_NAME}"
  if [[ "$HYBRID_ENABLED" == "true" ]]; then
    info "Ensuring hybrid-tier network tag on existing VM..."
    hybrid_apply_vm_tag "${INSTANCE_NAME}" "${ZONE}" "${PROJECT_ID}" "${HUB_NAME}"
    # The PV's NFS server field and the GKE-facing hub URL both need one
    # stable address, so an existing VM's current (so far ephemeral)
    # internal IP is promoted to a static reservation here, before
    # anything downstream reads it.
    EXISTING_VM_IP="$(gcloud compute instances describe "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --format="get(networkInterfaces[0].networkIP)")"
    hybrid_ensure_internal_ip_existing_vm "${HUB_NAME}" "${PROJECT_ID}" "${REGION}" "default" \
      "${EXISTING_VM_IP}" "${INSTANCE_NAME}" "${ZONE}"
  fi
else
  if [[ "$HYBRID_ENABLED" == "true" ]]; then
    hybrid_ensure_internal_ip_new_vm "${HUB_NAME}" "${PROJECT_ID}" "${REGION}" "default"
    VM_EXTRA_CREATE_ARGS+=(--private-network-ip="${HYBRID_INTERNAL_IP}")
  fi
  gcloud compute instances create "${INSTANCE_NAME}" \
    --zone="${ZONE}" \
    --project="${PROJECT_ID}" \
    --machine-type="${MACHINE_TYPE}" \
    --no-address \
    --service-account="${SA_EMAIL}" \
    --scopes=cloud-platform \
    --boot-disk-size="${DISK_SIZE}" \
    --image-family=ubuntu-2204-lts \
    --image-project=ubuntu-os-cloud \
    --metadata-from-file=user-data="${SCRIPT_DIR}/cloud-init.yaml" \
    --labels="scion-deployment=${HUB_NAME}" \
    ${VM_EXTRA_CREATE_ARGS[@]+"${VM_EXTRA_CREATE_ARGS[@]}"} \
    --quiet
  echo "  Created VM: ${INSTANCE_NAME} (zone: ${ZONE})"
fi

# --- Hub URL guard (post-create half) ---
# GKE agent pods reach the hub at http://<internal-ip>:8080 over the
# VPC; there is no other shape this tier supports. See hybrid_hub_url_
# guard_verify's own comment for what this does and doesn't cover.
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  hybrid_hub_url_guard_verify "$HUB_NAME" "$PROJECT_ID" "$REGION"
fi

# --- Wait for SSH readiness (avoids race on initial boot) ---
info "Waiting for SSH access to VM..."
SSH_READY=false
SSH_STDERR_FILE="$(mktemp)"
for attempt in $(seq 1 "$SSH_MAX_ATTEMPTS"); do
  if gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="echo ssh-ok" \
      --ssh-flag="-o ConnectTimeout=5" \
      --quiet 2>"${SSH_STDERR_FILE}"; then
    SSH_READY=true
    break
  fi
  BACKOFF=$((attempt < SSH_BACKOFF_FAST_ATTEMPTS ? SSH_BACKOFF_FAST_SECS : SSH_BACKOFF_SLOW_SECS))
  echo "  SSH attempt ${attempt}/${SSH_MAX_ATTEMPTS} - retrying in ${BACKOFF}s..."
  sleep "$BACKOFF"
done

if [[ "$SSH_READY" != "true" ]]; then
  err "Could not establish SSH connection to ${INSTANCE_NAME} after ${SSH_MAX_ATTEMPTS} attempts."
  if [[ -s "${SSH_STDERR_FILE}" ]]; then
    echo "  Last SSH error:" >&2
    cat "${SSH_STDERR_FILE}" >&2
  fi
  rm -f "${SSH_STDERR_FILE}"
  exit 1
fi
rm -f "${SSH_STDERR_FILE}"
echo "  SSH connection established."

# --- Wait for cloud-init ---
# cloud-init installs Docker and creates the scion user. The script cannot
# proceed until this finishes — writing to directories cloud-init owns before
# it completes causes "No such file or directory" errors.
info "Waiting for cloud-init to complete (this may take a few minutes)..."
CLOUD_INIT_OK=false
for ci_attempt in $(seq 1 "$CLOUD_INIT_MAX_ATTEMPTS"); do
  if gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="sudo cloud-init status --wait" \
      2>/dev/null; then
    CLOUD_INIT_OK=true
    break
  fi
  if [[ $ci_attempt -lt $CLOUD_INIT_MAX_ATTEMPTS ]]; then
    echo "  cloud-init check attempt ${ci_attempt}/${CLOUD_INIT_MAX_ATTEMPTS} returned non-zero, retrying in ${CLOUD_INIT_RETRY_SECS}s..."
    sleep "$CLOUD_INIT_RETRY_SECS"
  fi
done
if [[ "$CLOUD_INIT_OK" != "true" ]]; then
  err "cloud-init did not complete successfully after ${CLOUD_INIT_MAX_ATTEMPTS} attempts."
  echo "  Check cloud-init logs: gcloud compute ssh ${INSTANCE_NAME} --zone=${ZONE} --project=${PROJECT_ID} --command='sudo cloud-init status --long'"
  exit 1
fi
echo "  Cloud-init completed."

# --- Get VM internal IP ---
# Read once, here (right after the VM is confirmed up, rather than later
# in Phase 4), since it's needed for both the NFS/k8s hybrid-tier setup
# below and settings.yaml's dev-mode write in Phase 3, which happens
# before Phase 4 -- not just the proxy-mode write Phase 4 makes. With the
# hybrid tier on, this is the static reservation resolved above (reserved
# for a new VM, promoted or verified for an existing one) -- not a fresh
# describe of the VM's own (ephemeral, could change on any recreate)
# networkIP -- since the whole point of the reservation is that nothing
# downstream depends on that ephemeral value again.
info "Getting VM internal IP..."
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  VM_IP="$HYBRID_INTERNAL_IP"
else
  VM_IP="$(gcloud compute instances describe "${INSTANCE_NAME}" \
    --zone="${ZONE}" --project="${PROJECT_ID}" \
    --format="get(networkInterfaces[0].networkIP)")"
fi
if [[ -z "$VM_IP" ]]; then
  err "Could not retrieve VM internal IP for ${INSTANCE_NAME}"
  exit 1
fi
echo "  VM internal IP: ${VM_IP}"

# --- Hybrid tier: settings.yaml shared_dir_storage block ---
# Rendered once, here, and spliced into both settings.yaml writes below
# (dev mode in Phase 3, proxy mode in Phase 5) so they stay in sync.
# Empty when the tier is off, so both writes render byte-identical to
# before this existed.
HYBRID_SHARED_DIR_STORAGE_YAML=""
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  # The third argument is a PersistentVolumeClaim name, not the
  # PersistentVolume's own name: the Go side consumes shares[0].pv_name
  # as the pod spec's claimName (see pkg/runtime/k8s_runtime.go), so
  # GKE_PVC_NAME (the resolved PVC name, which may differ from the PV's
  # name) is what belongs here.
  HYBRID_SHARED_DIR_STORAGE_YAML="$(hybrid_settings_shared_dir_storage_yaml \
    "$VM_IP" "$HYBRID_NFS_EXPORT_ROOT" "$GKE_PVC_NAME")"
fi

# --- Hybrid tier: NFS squash identity, server, and export ---
# A tier-gated remote step, run here (once cloud-init has created the
# "scion" user and group the squash identity's primary group and export
# ownership depend on) rather than added to cloud-init.yaml itself, which
# is shared with every deployment: this keeps the tier-off path
# byte-for-byte unchanged. Always redone on every re-run, tier on: it's
# cheap, and it's how the export stays in sync if the cluster's node
# subnet CIDR ever changes.
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  info "Creating the NFS squash identity (if needed)..."
  SQUASH_SSH_ERR="$(mktemp)"
  if ! SQUASH_IDS=$(gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="$(hybrid_nfs_squash_identity_script "$HYBRID_NFS_SQUASH_USER")" \
      2>"${SQUASH_SSH_ERR}"); then
    err "Could not create or validate the NFS squash identity on ${INSTANCE_NAME}:"
    err "  $(cat "${SQUASH_SSH_ERR}")"
    rm -f "${SQUASH_SSH_ERR}"
    exit 1
  fi
  rm -f "${SQUASH_SSH_ERR}"
  # The remote script only ever prints this one line on success (see its
  # own comment), but validate it here too rather than trust stdout
  # blindly: a uid/gid pair is about to be embedded directly into the
  # NFS export line's anonuid=/anongid=, and squashing to uid 0 would
  # defeat the entire point of a dedicated, unprivileged squash identity.
  if [[ ! "$SQUASH_IDS" =~ ^[0-9]+:[0-9]+$ ]]; then
    err "Unexpected output from the NFS squash identity script on ${INSTANCE_NAME}: '${SQUASH_IDS}' (expected UID:GID)."
    exit 1
  fi
  SQUASH_UID="${SQUASH_IDS%%:*}"
  SQUASH_GID="${SQUASH_IDS##*:}"
  if [[ "$SQUASH_UID" -eq 0 ]]; then
    err "The NFS squash uid resolved to 0 on ${INSTANCE_NAME}; refusing to export with root as the anonymous uid."
    exit 1
  fi
  echo "  Squash uid: ${SQUASH_UID} (scion group gid: ${SQUASH_GID})"

  info "Installing the NFS server and export (if needed)..."
  NFS_FSID="$(hybrid_nfs_fsid "$HUB_NAME")"
  gcloud compute ssh "${INSTANCE_NAME}" \
    --zone="${ZONE}" --project="${PROJECT_ID}" \
    --command="$(hybrid_nfs_export_script "$HYBRID_NFS_EXPORT_ROOT" "$GKE_NODE_SUBNET_CIDR" \
      "$SQUASH_UID" "$SQUASH_GID" "$NFS_FSID" "$HUB_NAME" \
      "$HYBRID_NFS_IMAGE_PATH" "$HYBRID_SHARED_DIR_IMAGE_SIZE_GB")"
fi

# ===================================================================
# Phase 3: VM Setup
# ===================================================================
section "Phase 3: VM Setup"

RELEASE_URL="https://github.com/GoogleCloudPlatform/scion/releases/download/${VERSION}"

# --- Detect VM architecture ---
info "Detecting VM architecture..."
ARCH=$(gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="uname -m" 2>/dev/null)
case "$ARCH" in
  x86_64)  ARCH_SUFFIX="amd64" ;;
  aarch64) ARCH_SUFFIX="arm64" ;;
  *)       ARCH_SUFFIX="amd64" ;;  # default to amd64
esac
echo "  Architecture: ${ARCH} (${ARCH_SUFFIX})"

# --- Stop scion-hub.service before binary update (avoids ETXTBSY on re-run) ---
info "Stopping scion-hub.service (if running)..."
gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="
    if systemctl is-active --quiet scion-hub.service 2>/dev/null; then
      sudo systemctl stop scion-hub.service
      echo 'Stopped scion-hub.service before binary update.'
    else
      echo 'scion-hub.service not running (first install or already stopped).'
    fi
  " 2>/dev/null || true

# --- Download and install scion binary ---
info "Installing scion binary (${VERSION})..."
gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="
    set -euo pipefail
    echo 'Downloading scion binary...'
    curl -fsSL '${RELEASE_URL}/scion-linux-${ARCH_SUFFIX}.tar.gz' -o /tmp/scion.tar.gz
    tar -xzf /tmp/scion.tar.gz -C /tmp
    sudo mv /tmp/scion /usr/local/bin/scion
    sudo chmod +x /usr/local/bin/scion
    rm -f /tmp/scion.tar.gz
    echo \"Installed scion binary (${VERSION})\"
  "

# --- Download and install chat plugins ---
if [[ ${#CHAT_PLUGINS[@]} -gt 0 ]]; then
  info "Installing chat plugins..."
  for PLUGIN in "${CHAT_PLUGINS[@]}"; do
    PLUGIN_BINARY="scion-plugin-${PLUGIN}"
    PLUGIN_ARCHIVE="${PLUGIN_BINARY}-linux-${ARCH_SUFFIX}.tar.gz"
    info "  Installing ${PLUGIN_BINARY}..."
    gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="
        set -euo pipefail
        sudo -u scion mkdir -p /home/scion/.scion/plugins/broker
        curl -fsSL '${RELEASE_URL}/${PLUGIN_ARCHIVE}' -o /tmp/${PLUGIN_ARCHIVE}
        tar -xzf /tmp/${PLUGIN_ARCHIVE} -C /tmp
        sudo mv /tmp/${PLUGIN_BINARY} /home/scion/.scion/plugins/broker/${PLUGIN_BINARY}
        sudo chown scion:scion /home/scion/.scion/plugins/broker/${PLUGIN_BINARY}
        sudo chmod +x /home/scion/.scion/plugins/broker/${PLUGIN_BINARY}
        rm -f /tmp/${PLUGIN_ARCHIVE}
        echo 'Installed ${PLUGIN_BINARY}'
      "
  done
  echo "  All chat plugins installed."
fi

# --- Generate session secret and write hub.env (idempotent) ---
# Check if hub.env already exists on the VM
HUB_ENV_EXISTS=$(gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="test -f /home/scion/.scion/hub.env && echo yes || echo no" 2>/dev/null) || true

if [[ "$HUB_ENV_EXISTS" == "yes" ]]; then
  info "hub.env already exists, preserving existing SESSION_SECRET."
else
  info "Generating session secret..."
  SESSION_SECRET="$(openssl rand -base64 32)"

  info "Writing hub.env..."
  # Security: write to a local temp file and transfer via SCP to avoid
  # embedding secrets in the gcloud ssh command string, which would be
  # visible in local process listing (ps aux).  Same bug class as #1211.
  HUB_ENV_TMPFILE="$(mktemp)"
  chmod 600 "${HUB_ENV_TMPFILE}"
  sed \
    -e "s|__SESSION_SECRET__|${SESSION_SECRET}|g" \
    -e "s|__PROJECT_ID__|${PROJECT_ID}|g" \
    "${SCRIPT_DIR}/config-templates/hub.env.template" > "${HUB_ENV_TMPFILE}"
  unset SESSION_SECRET

  gcloud compute scp "${HUB_ENV_TMPFILE}" \
    "${INSTANCE_NAME}:/tmp/hub.env" \
    --zone="${ZONE}" --project="${PROJECT_ID}" --quiet
  rm -f "${HUB_ENV_TMPFILE}"

  gcloud compute ssh "${INSTANCE_NAME}" \
    --zone="${ZONE}" --project="${PROJECT_ID}" \
    --command="
      sudo mv /tmp/hub.env /home/scion/.scion/hub.env
      sudo chown scion:scion /home/scion/.scion/hub.env
      sudo chmod 600 /home/scion/.scion/hub.env
    "
fi

# --- Write settings.yaml (dev mode for initial startup) ---
# Phase 5 will overwrite this with proxy auth config once IAP is ready.
info "Writing settings.yaml (dev mode)..."
gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="
    sudo -u scion tee /home/scion/.scion/settings.yaml > /dev/null << 'SETTINGSEOF'
schema_version: \"1\"
image_registry: \"${IMAGE_REGISTRY}\"
server:
  hub:
    name: \"${HUB_NAME}\"
${ADMIN_EMAIL:+    admin_emails:
      - \"${ADMIN_EMAIL}\"}
  maintenance:
    deployment_tier: \"binary\"
    release_channel: \"${RELEASE_CHANNEL}\"
    update_policy: \"${UPDATE_POLICY}\"
  storage:
    local_path: /home/scion/.scion/workspace-storage
  secrets:
    backend: local
  auth:
    mode: dev
${HYBRID_SHARED_DIR_STORAGE_YAML:+${HYBRID_SHARED_DIR_STORAGE_YAML}
}  listen_port: 8080
SETTINGSEOF
  "

# --- Install systemd unit ---
info "Installing systemd service..."
gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="
    sudo tee /etc/systemd/system/scion-hub.service > /dev/null << 'SERVICEEOF'
$(cat "${SCRIPT_DIR}/config-templates/scion-hub.service")
SERVICEEOF
    sudo systemctl daemon-reload
    sudo systemctl enable scion-hub.service
    sudo systemctl start scion-hub.service
    echo 'scion-hub.service started.'
  "

# --- Health check ---
info "Running health check..."
HEALTH_OK=false
for i in $(seq 1 "$HEALTH_CHECK_MAX_ATTEMPTS"); do
  if gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="curl -sf http://localhost:8080/healthz" \
      2>/dev/null; then
    HEALTH_OK=true
    break
  fi
  echo "  Attempt ${i}/${HEALTH_CHECK_MAX_ATTEMPTS} - waiting ${HEALTH_CHECK_RETRY_SECS}s..."
  sleep "$HEALTH_CHECK_RETRY_SECS"
done

if [[ "$HEALTH_OK" == "true" ]]; then
  echo ""
  echo -e "${GREEN}  Health check passed.${RESET}"
else
  err "Health check did not pass within $((HEALTH_CHECK_MAX_ATTEMPTS * HEALTH_CHECK_RETRY_SECS))s. The hub is not running."
  echo "  Check the service logs:"
  echo "  gcloud compute ssh ${INSTANCE_NAME} --zone=${ZONE} --project=${PROJECT_ID} \\"
  echo "    --command='sudo journalctl -u scion-hub.service --no-pager -n 50'"
  exit 1
fi

# ===================================================================
# Phase 3b: Container Images
# ===================================================================
if [[ "$IMAGE_SOURCE" == "build" ]]; then
  section "Phase 3b: Build Container Images on VM"

  # NOTE: the checkout lives under /opt (root-owned, world-traversable),
  # not /home/scion — the deployer identity running this over `gcloud
  # compute ssh` is never a member of the `scion` group, and /home/scion
  # is mode 0750 once cloud-init's `useradd -m` runs. Every privileged
  # step uses `sudo`; the deployer only needs to traverse /opt and
  # write /tmp.
  #
  # Idempotency: a successful build writes a version marker
  # (/opt/scion-source/.images-built.version) containing $VERSION. A re-run
  # skips the 10-15 min build iff the marker matches $VERSION AND all 3
  # expected images are still present — a stale `:latest` tag alone is not
  # enough, since re-running with --version vNEXT must not silently keep old
  # images just because something answers to `:latest`. The marker file is
  # what makes the skip version-aware.
  IMAGES_BUILT_MARKER="/opt/scion-source/.images-built.version"
  SKIP_BUILD=false
  if [[ "$CFG_FORCE_REBUILD" == "true" ]]; then
    info "Forcing a rebuild (container_images.force_rebuild or --rebuild-images is set)."
  else
    info "Checking whether images for version ${VERSION} are already built..."
    BUILD_CHECK=$(gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="
        set -euo pipefail
        MARKER='${IMAGES_BUILT_MARKER}'
        if [ -f \"\$MARKER\" ] \
          && [ \"\$(sudo cat \"\$MARKER\" 2>/dev/null)\" = '${VERSION}' ] \
          && sudo docker image inspect localhost/scion/core-base:latest >/dev/null 2>&1 \
          && sudo docker image inspect localhost/scion/scion-base:latest >/dev/null 2>&1 \
          && sudo docker image inspect localhost/scion/scion-antigravity:latest >/dev/null 2>&1; then
          echo skip
        else
          echo build
        fi
      " 2>/dev/null) || true
    if [[ "$BUILD_CHECK" == "skip" ]]; then
      SKIP_BUILD=true
    fi
  fi

  if [[ "$SKIP_BUILD" == "true" ]]; then
    info "Images already built for version ${VERSION} (marker + all 3 images present); skipping build."
    echo "  Set container_images.force_rebuild: true in the config, or pass --rebuild-images, to override."
  else
    info "Cloning scion repository on VM..."
    gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="
        set -euo pipefail
        if [ ! -d /opt/scion-source ]; then
          sudo git clone --depth 1 --branch '${VERSION}' \
            https://github.com/GoogleCloudPlatform/scion.git /opt/scion-source
        else
          cd /opt/scion-source
          sudo git fetch --depth 1 origin tag '${VERSION}'
          sudo git checkout '${VERSION}'
        fi
      "

    # Build only the minimal set of images needed for deployment:
    # core-base (foundation) -> scion-base (adds scion binary) -> scion-antigravity (default harness)
    # Using --target all would build ALL ~12 images including harnesses with known build issues.
    info "Building container images (this may take 10-15 minutes)..."
    gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="
        set -euo pipefail
        cd /opt/scion-source
        rm -f /tmp/scion-image-build.exit
        # Clear the marker before starting: if this build is interrupted or
        # fails partway, no marker should be left claiming a stale version
        # is built (Step 5 below only writes it back on full success).
        sudo rm -f '${IMAGES_BUILT_MARKER}'
        nohup bash -c '
          {
            set -euo pipefail
            # Step 1: Build core-base
            echo \"=== Building core-base ===\"
            sudo bash image-build/scripts/build-images.sh \
              --builder local-docker \
              --target core-base \
              --tag latest

            # Step 2: Build scion-base
            echo \"=== Building scion-base ===\"
            sudo bash image-build/scripts/build-images.sh \
              --builder local-docker \
              --target scion-base \
              --tag latest

            # Step 3: Build antigravity harness directly
            echo \"=== Building scion-antigravity ===\"
            sudo docker build \
              -t scion-antigravity:latest \
              --build-arg BASE_IMAGE=scion-base:latest \
              -f harnesses/antigravity/Dockerfile \
              harnesses/antigravity/

            # Step 4: Tag images under localhost/scion for the runtime
            echo \"=== Tagging images for localhost/scion registry ===\"
            sudo docker tag core-base:latest localhost/scion/core-base:latest
            sudo docker tag scion-base:latest localhost/scion/scion-base:latest
            sudo docker tag scion-antigravity:latest localhost/scion/scion-antigravity:latest

            # Step 5: Record the version marker so re-runs can skip the build.
            # Written last, only on full success (set -e above aborts before
            # this line if any prior step failed).
            echo \"=== Recording image build marker (version ${VERSION}) ===\"
            echo '${VERSION}' | sudo tee '${IMAGES_BUILT_MARKER}' > /dev/null

            echo \"=== All images built and tagged successfully ===\"
          } > /tmp/scion-image-build.log 2>&1
          echo \$? > /tmp/scion-image-build.exit
        ' >/dev/null 2>&1 </dev/null &
        echo \$! > /tmp/scion-image-build.pid
        echo \"Image build started in background (PID \$(cat /tmp/scion-image-build.pid))\"
      "

    # Poll for build completion
    info "Waiting for image build to complete..."
    BUILD_DONE=false
    POLL_COUNT=0
    while [[ "$BUILD_DONE" != "true" ]] && [[ $POLL_COUNT -lt $BUILD_POLL_MAX_ATTEMPTS ]]; do
      sleep "$BUILD_POLL_INTERVAL_SECS"
      POLL_COUNT=$((POLL_COUNT + 1))
      # Check if the exit code file exists (build finished)
      BUILD_EXIT=$(gcloud compute ssh "${INSTANCE_NAME}" \
        --zone="${ZONE}" --project="${PROJECT_ID}" \
        --command="cat /tmp/scion-image-build.exit 2>/dev/null || echo running" \
        2>/dev/null) || true
      if [[ "$BUILD_EXIT" != "running" ]]; then
        BUILD_DONE=true
      else
        # Show progress (last line of build log)
        LAST_LINE=$(gcloud compute ssh "${INSTANCE_NAME}" \
          --zone="${ZONE}" --project="${PROJECT_ID}" \
          --command="tail -1 /tmp/scion-image-build.log 2>/dev/null || echo '(waiting...)'" \
          2>/dev/null) || true
        echo "  [${POLL_COUNT}] ${LAST_LINE}"
      fi
    done

    if [[ "$BUILD_DONE" != "true" ]]; then
      err "Image build timed out after $((BUILD_POLL_MAX_ATTEMPTS * BUILD_POLL_INTERVAL_SECS / 60)) minutes."
      echo "  Check build log: gcloud compute ssh ${INSTANCE_NAME} --zone=${ZONE} --project=${PROJECT_ID} --command='cat /tmp/scion-image-build.log'"
      exit 1
    fi

    if [[ "$BUILD_EXIT" != "0" ]]; then
      err "Image build failed (exit code: ${BUILD_EXIT})."
      echo "  Check build log: gcloud compute ssh ${INSTANCE_NAME} --zone=${ZONE} --project=${PROJECT_ID} --command='tail -50 /tmp/scion-image-build.log'"
      exit 1
    fi
    echo "  Image build completed successfully."
  fi

  info "Verifying container images..."
  gcloud compute ssh "${INSTANCE_NAME}" \
    --zone="${ZONE}" --project="${PROJECT_ID}" \
    --command="sudo docker images | grep -E 'localhost/scion|core-base|scion-base|scion-antigravity'"
  echo "  Container images built and tagged successfully."
else
  section "Phase 3b: Container Images (Registry)"
  info "Using pre-built images from registry: ${IMAGE_REGISTRY}"
  echo "  The hub will pull images from: ${IMAGE_REGISTRY}"
fi

# ===================================================================
# Phase 4: IAP Proxy
# ===================================================================
section "Phase 4: IAP Proxy"

# --- Hybrid tier: Kubernetes objects (PV, namespace, PVC) ---
# The non-IP-dependent parts of this check (kubectl/plugin presence,
# credentials, marker refusal, namespace creation) already ran in Phase
# 2 via hybrid_k8s_preflight, right after discovery -- HYBRID_KUBECONFIG
# is already set from that call, so it's not re-fetched here. What's
# left is the part that genuinely can't run any earlier: the PV's
# identity includes the VM's own internal IP (read back in Phase 3,
# above, once the VM is confirmed up).
if [[ "$HYBRID_ENABLED" == "true" ]]; then
  info "Ensuring hybrid-tier Kubernetes objects (if needed)..."
  hybrid_k8s_ensure_objects "$HUB_NAME" "$VM_IP"
fi

# --- Build and deploy Cloud Run IAP proxy ---
# We build the proxy image on the VM and deploy with --image instead of
# `gcloud run deploy --source`, because --source triggers Cloud Build, which
# uploads source to GCS. Enterprise projects with the org policy
# constraints/gcp.restrictServiceUsage denying storage.googleapis.com reject
# that upload with HTTP 403. Docker is already available on the VM and the
# repo is already cloned at /opt/scion-source, so we build and push there
# instead.
PROXY_SERVICE="${INSTANCE_NAME}-iap-proxy"
AR_REPO="cloud-run-source-deploy"
PROXY_IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}/${PROXY_SERVICE}:latest"

# --- Ensure Artifact Registry repo exists ---
# This repo name matches what `gcloud run deploy --source` auto-creates, so
# this stays backwards-compatible with prior --source deploys.
info "Ensuring Artifact Registry repo exists: ${AR_REPO}..."
gcloud artifacts repositories create "${AR_REPO}" \
  --project="${PROJECT_ID}" \
  --location="${REGION}" \
  --repository-format=docker \
  --quiet 2>/dev/null || true
echo "  Artifact Registry repo ready: ${AR_REPO}"

# --- Build proxy image on the VM ---
# When IMAGE_SOURCE=registry, Phase 3b is skipped and /opt/scion-source may
# not exist on the VM. Ensure the repo is cloned (shallow) before building.
info "Building proxy image on VM..."
echo "  Image: ${PROXY_IMAGE}"
gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="
    set -euo pipefail
    if [ ! -d /opt/scion-source ]; then
      sudo git clone --depth 1 \
        https://github.com/GoogleCloudPlatform/scion.git /opt/scion-source
    fi
    sudo docker build -t ${PROXY_IMAGE} /opt/scion-source/extras/cloudrun-iap-proxy
  "
echo "  Proxy image built on VM."

# --- Push proxy image to Artifact Registry ---
info "Pushing proxy image to Artifact Registry..."
gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="sudo gcloud auth configure-docker ${REGION}-docker.pkg.dev --quiet && sudo docker push ${PROXY_IMAGE}"
echo "  Proxy image pushed: ${PROXY_IMAGE}"

# --- Deploy Cloud Run IAP proxy ---
# `gcloud run deploy` is itself idempotent (creates if absent, redeploys
# in place if present), so a marker label is only added when this
# describe finds nothing: labeling every redeploy would be harmless too,
# but the base-resource marker convention here is additive and
# create-only, matching every other base resource above.
info "Deploying Cloud Run IAP proxy: ${PROXY_SERVICE}..."
echo "  Target URL: http://${VM_IP}:8080"
echo "  Image: ${PROXY_IMAGE}"

PROXY_SERVICE_LABEL_ARGS=()
PROXY_LABEL_ARG="$(hybrid_cloud_run_label_args "${PROXY_SERVICE}" "${PROJECT_ID}" "${REGION}" "${HUB_NAME}")"
[[ -n "$PROXY_LABEL_ARG" ]] && PROXY_SERVICE_LABEL_ARGS=("$PROXY_LABEL_ARG")

gcloud run deploy "${PROXY_SERVICE}" \
  --project="${PROJECT_ID}" \
  --region="${REGION}" \
  --image="${PROXY_IMAGE}" \
  --set-env-vars="TARGET_URL=http://${VM_IP}:8080" \
  --network=default \
  --subnet=default \
  --vpc-egress=all-traffic \
  --allow-unauthenticated \
  --port=8080 \
  ${PROXY_SERVICE_LABEL_ARGS[@]+"${PROXY_SERVICE_LABEL_ARGS[@]}"} \
  --quiet

# --- Get Cloud Run service URL ---
info "Getting Cloud Run service URL..."
PROXY_URL="$(gcloud run services describe "${PROXY_SERVICE}" \
  --project="${PROJECT_ID}" --region="${REGION}" \
  --format="value(status.url)")"
if [[ -z "$PROXY_URL" ]]; then
  err "Could not retrieve Cloud Run service URL for ${PROXY_SERVICE}"
  exit 1
fi
echo "  Proxy URL: ${PROXY_URL}"

# --- Enable IAP ---
# Note: --resource-type=cloud-run is NOT valid for gcloud iap web enable.
# The supported path for Cloud Run is the --iap flag on the service itself.
info "Enabling IAP on Cloud Run service..."
gcloud beta run services update "${PROXY_SERVICE}" \
  --region="${REGION}" \
  --project="${PROJECT_ID}" \
  --iap \
  --quiet
echo "  IAP enabled."

# --- Bind IAP access for deployer ---
info "Binding IAP access for deployer..."
# Use gcloud auth list for robust detection (works with service accounts and CI),
# fall back to gcloud config get account.
OPERATOR_EMAIL="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null | head -1)" || true
if [[ -z "$OPERATOR_EMAIL" ]]; then
  OPERATOR_EMAIL="$(gcloud config get-value account 2>/dev/null)" || true
fi
if [[ -z "$OPERATOR_EMAIL" ]]; then
  warn "Could not determine operator email; skipping IAP binding."
else
  # Detect correct IAM member prefix: service accounts vs human users
  if [[ "$OPERATOR_EMAIL" == *.gserviceaccount.com ]]; then
    OPERATOR_MEMBER="serviceAccount:${OPERATOR_EMAIL}"
  else
    OPERATOR_MEMBER="user:${OPERATOR_EMAIL}"
  fi
  # Use gcloud iap web add-iam-policy-binding (supports --resource-type=cloud-run)
  # Note: this is distinct from "gcloud iap web enable" which does NOT support cloud-run.
  if gcloud iap web add-iam-policy-binding \
      --resource-type=cloud-run --service="${PROXY_SERVICE}" \
      --region="${REGION}" --project="${PROJECT_ID}" \
      --member="${OPERATOR_MEMBER}" \
      --role=roles/iap.httpsResourceAccessor \
      --quiet >/dev/null 2>&1; then
    echo "  IAP access granted to: ${OPERATOR_EMAIL} (service-level binding)"
  else
    warn "Service-level IAP binding failed; falling back to project-level binding."
    if gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
      --member="${OPERATOR_MEMBER}" \
      --role=roles/iap.httpsResourceAccessor \
      --quiet >/dev/null 2>&1; then
      echo "  IAP access granted to: ${OPERATOR_EMAIL} (project-level fallback)"
    else
      err "Failed to grant IAP access to ${OPERATOR_EMAIL} at both service and project levels."
      err "You may not be able to access the Hub. Please grant roles/iap.httpsResourceAccessor manually."
    fi
  fi
fi

# --- Wait for IAP enforcement ---
info "Waiting for IAP enforcement to activate..."
echo "  IAP takes 30-60 seconds to begin enforcing after being enabled."
echo "  Waiting ${IAP_ENFORCEMENT_WAIT_SECS} seconds..."
sleep "$IAP_ENFORCEMENT_WAIT_SECS"
echo "  Wait complete."

# ===================================================================
# Phase 5: Finalize
# ===================================================================
section "Phase 5: Finalize"

# --- Compute IAP audience ---
# For Cloud Run services, the IAP audience uses the format:
#   /projects/PROJECT_NUMBER/locations/REGION/services/SERVICE_NAME
info "Computing IAP audience..."
PROJECT_NUMBER="$(gcloud projects describe "${PROJECT_ID}" \
  --format="value(projectNumber)" 2>/dev/null)" || true
if [[ -z "$PROJECT_NUMBER" ]]; then
  err "Could not determine project number for ${PROJECT_ID}"
  exit 1
fi
IAP_AUDIENCE="/projects/${PROJECT_NUMBER}/locations/${REGION}/services/${PROXY_SERVICE}"
echo "  Project number: ${PROJECT_NUMBER}"
echo "  IAP audience:   ${IAP_AUDIENCE}"

# --- Update settings.yaml with proxy auth ---
info "Updating settings.yaml with proxy auth configuration..."
gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="
    sudo -u scion tee /home/scion/.scion/settings.yaml > /dev/null << 'SETTINGSEOF'
schema_version: \"1\"
image_registry: \"${IMAGE_REGISTRY}\"
server:
  hub:
    name: \"${HUB_NAME}\"
${ADMIN_EMAIL:+    admin_emails:
      - \"${ADMIN_EMAIL}\"}
  maintenance:
    deployment_tier: \"binary\"
    release_channel: \"${RELEASE_CHANNEL}\"
    update_policy: \"${UPDATE_POLICY}\"
  storage:
    local_path: /home/scion/.scion/workspace-storage
  secrets:
    backend: local
  auth:
    mode: proxy
    proxy:
      provider: iap
      iap:
        audience: \"${IAP_AUDIENCE}\"
${HYBRID_SHARED_DIR_STORAGE_YAML:+${HYBRID_SHARED_DIR_STORAGE_YAML}
}  listen_port: 8080
SETTINGSEOF
  "
echo "  settings.yaml updated (auth mode: proxy, provider: iap)."

# --- Restart hub service ---
info "Restarting scion-hub.service..."
gcloud compute ssh "${INSTANCE_NAME}" \
  --zone="${ZONE}" --project="${PROJECT_ID}" \
  --command="
    sudo systemctl restart scion-hub.service
    echo 'scion-hub.service restarted.'
  "

# --- Post-restart health check ---
info "Running post-restart health check..."
HEALTH_OK=false
for i in $(seq 1 "$HEALTH_CHECK_MAX_ATTEMPTS"); do
  if gcloud compute ssh "${INSTANCE_NAME}" \
      --zone="${ZONE}" --project="${PROJECT_ID}" \
      --command="curl -sf http://localhost:8080/healthz" \
      2>/dev/null; then
    HEALTH_OK=true
    break
  fi
  echo "  Attempt ${i}/${HEALTH_CHECK_MAX_ATTEMPTS} - waiting ${HEALTH_CHECK_RETRY_SECS}s..."
  sleep "$HEALTH_CHECK_RETRY_SECS"
done

if [[ "$HEALTH_OK" == "true" ]]; then
  echo ""
  echo -e "${GREEN}  Health check passed.${RESET}"
else
  err "Post-restart health check did not pass within $((HEALTH_CHECK_MAX_ATTEMPTS * HEALTH_CHECK_RETRY_SECS))s. The hub is not running."
  echo "  Check the service logs:"
  echo "  gcloud compute ssh ${INSTANCE_NAME} --zone=${ZONE} --project=${PROJECT_ID} \\"
  echo "    --command='sudo journalctl -u scion-hub.service --no-pager -n 50'"
  exit 1
fi

# ===================================================================
# Done
# ===================================================================
echo ""
echo -e "${BOLD}=== Deployment Complete ===${RESET}"
echo ""
echo "  Hub name:     ${HUB_NAME}"
echo "  Instance:     ${INSTANCE_NAME}"
echo "  Zone:         ${ZONE}"
echo "  Scion:        ${VERSION}"
echo "  Auth mode:    proxy (IAP)"
echo "  Proxy:        ${PROXY_SERVICE}"
echo "  Access URL:   ${PROXY_URL}"
echo ""
echo "Open the following URL in your browser to access the hub:"
echo ""
echo "  ${PROXY_URL}"
echo ""
echo "You will be prompted to authenticate via Google IAP."
echo ""
echo "Agents running on the VM connect via localhost:8080 (no IAP needed)."
echo ""
echo "To view service logs:"
echo ""
echo "  gcloud compute ssh ${INSTANCE_NAME} \\"
echo "    --zone=${ZONE} --project=${PROJECT_ID} \\"
echo "    --command='sudo journalctl -u scion-hub.service -f'"

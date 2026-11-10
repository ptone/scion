#!/bin/bash
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

# Target DAG and step descriptors for the Scion image build orchestrator.
#
# This file is sourced by build-images.sh. It is the single source of truth
# for which images exist, which target names expand to which ordered step
# lists, and which dockerfile / context dir / build-args each step uses.
#
# Builders never read this file. The orchestrator translates step descriptors
# into the uniform builder_build call.

discover_harness_names() {
  local harness_root="${REPO_ROOT}/harnesses"
  if [[ ! -d "${harness_root}" ]]; then
    return 0
  fi
  find "${harness_root}" -mindepth 2 -maxdepth 2 -name Dockerfile -print \
    | while IFS= read -r dockerfile; do
        basename "$(dirname "${dockerfile}")"
      done \
    | sort
}

is_harness_step() {
  local step="$1"
  local name="${step#scion-}"
  [[ "${step}" == scion-* && -f "${REPO_ROOT}/harnesses/${name}/Dockerfile" ]]
}

# harness_build_conf <harness-name> <key>
#
# Echoes the value of <key> from harnesses/<name>/image-build.conf, an
# optional per-bundle file of key=value lines (# comments allowed). Keys:
#   catalog=no            leave the bundle out of the group targets
#                         (harnesses, common, all, thick) and out of the
#                         full-catalog Cloud Build configs; it still builds
#                         with an explicit --target scion-<name>.
#   platforms=linux/amd64 the platforms the image supports (comma list);
#                         requested platforms are intersected with it.
harness_build_conf() {
  local conf="${REPO_ROOT}/harnesses/$1/image-build.conf"
  [[ -f "${conf}" ]] || return 0
  # Pure bash (no external tools): the last "<key>=" line wins.
  local line key value result=""
  while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%%#*}"
    [[ "${line}" == *=* ]] || continue
    key="${line%%=*}"
    value="${line#*=}"
    key="${key//[[:space:]]/}"
    if [[ "${key}" == "$2" ]]; then
      result="${value//[[:space:]]/}"
    fi
  done <"${conf}"
  echo "${result}"
}

# Every harness image, including ones left out of the catalog.
emit_harness_steps() {
  local name
  discover_harness_names | while IFS= read -r name; do
    [[ -n "${name}" ]] && echo "scion-${name}"
  done
}

# Harness images built by the group targets.
emit_catalog_harness_steps() {
  local name
  discover_harness_names | while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    [[ "$(harness_build_conf "${name}" catalog)" == "no" ]] && continue
    echo "scion-${name}"
  done
}

# native_platform: the docker platform of this host (linux/amd64 or
# linux/arm64).
native_platform() {
  case "${HOSTTYPE:-}" in
    x86_64|amd64) echo "linux/amd64" ;;
    aarch64|arm64) echo "linux/arm64" ;;
    *) echo "linux/${HOSTTYPE:-unknown}" ;;
  esac
}

# step_platforms <step_id> <requested>
#
# Echoes the platforms to build the step for: <requested> ("" = native)
# intersected with the harness's declared platforms, if it declares any.
# Echoes <requested> unchanged for steps without a declaration. Returns 1
# (echoing nothing) when the intersection is empty.
step_platforms() {
  local step="$1" requested="$2"
  if ! is_harness_step "${step}"; then
    echo "${requested}"
    return 0
  fi
  local allowed
  allowed="$(harness_build_conf "${step#scion-}" platforms)"
  if [[ -z "${allowed}" ]]; then
    echo "${requested}"
    return 0
  fi
  local want="${requested}" p out=""
  local -a _wanted
  [[ -n "${want}" ]] || want="$(native_platform)"
  IFS=',' read -r -a _wanted <<<"${want}"
  for p in "${_wanted[@]}"; do
    if [[ ",${allowed}," == *",${p},"* ]]; then
      out="${out:+${out},}${p}"
    fi
  done
  [[ -n "${out}" ]] || return 1
  # Keep "native" as "" when the native platform is what was asked for.
  if [[ -z "${requested}" ]]; then
    echo ""
  else
    echo "${out}"
  fi
}

# All known step IDs. The step ID is also the published image name
# (without registry prefix). Harness steps are discovered from the root
# harnesses catalog.
ALL_STEP_IDS=(
  core-base
  thick-prep
  scion-base
)
while IFS= read -r harness_step; do
  [[ -n "${harness_step}" ]] && ALL_STEP_IDS+=("${harness_step}")
done < <(emit_harness_steps)
ALL_STEP_IDS+=(
  scion-hub
  scion-omni
)

# All known target names. Used by the orchestrator's --help and --target
# validation. Includes both group targets and individual step IDs.
# shellcheck disable=SC2034 # sourced library; ALL_TARGETS is read by build-images.sh
ALL_TARGETS=(
  core-base
  thick-prep
  scion-base
  harnesses
  hub
  omni
  common
  all
  thick
)
# Append individual step IDs that are not already group targets, so they
# appear in --help and in the error message for unknown targets.
for _step_id in "${ALL_STEP_IDS[@]}"; do
  _already="false"
  for _t in "${ALL_TARGETS[@]}"; do
    if [[ "${_step_id}" == "${_t}" ]]; then
      _already="true"
      break
    fi
  done
  if [[ "${_already}" == "false" ]]; then
    ALL_TARGETS+=("${_step_id}")
  fi
done
unset _step_id _already _t

# resolve_targets <target>
#
# Echoes one step ID per line, in build order, for the given target. Returns
# nonzero (and prints nothing on stdout) for an unknown target.
resolve_targets() {
  case "$1" in
    core-base)
      echo core-base
      ;;
    scion-base)
      echo scion-base
      ;;
    harnesses)
      emit_catalog_harness_steps
      ;;
    hub)
      echo scion-hub
      ;;
    omni)
      printf '%s\n' "${_OMNI_CHAIN[@]}"
      ;;
    common)
      printf '%s\n' scion-base
      emit_catalog_harness_steps
      printf '%s\n' scion-hub
      ;;
    all)
      printf '%s\n' core-base scion-base
      emit_catalog_harness_steps
      printf '%s\n' scion-hub
      ;;
    thick-prep)
      echo thick-prep
      ;;
    thick)
      printf '%s\n' thick-prep scion-base
      emit_catalog_harness_steps
      printf '%s\n' scion-hub
      ;;
    *)
      # Allow individual step IDs (e.g. scion-claude, scion-hub) as targets.
      local _id
      for _id in "${ALL_STEP_IDS[@]}"; do
        if [[ "$1" == "${_id}" ]]; then
          echo "$1"
          return 0
        fi
      done
      return 1
      ;;
  esac
}

# step_image_name <step_id>
step_image_name() {
  echo "$1"
}

# step_dockerfile <step_id>
#
# Echoes the absolute path to the dockerfile for the step. Requires
# IMAGE_BUILD_DIR to be set in the environment.
step_dockerfile() {
  case "$1" in
    core-base)     echo "${IMAGE_BUILD_DIR}/core-base/Dockerfile" ;;
    thick-prep)    echo "${IMAGE_BUILD_DIR}/thick-prep/Dockerfile" ;;
    scion-base)    echo "${IMAGE_BUILD_DIR}/scion-base/Dockerfile" ;;
    scion-hub)     echo "${IMAGE_BUILD_DIR}/hub/Dockerfile" ;;
    scion-omni)    echo "${IMAGE_BUILD_DIR}/omni/Dockerfile" ;;
    *)
      if is_harness_step "$1"; then
        echo "${REPO_ROOT}/harnesses/${1#scion-}/Dockerfile"
      else
        return 1
      fi
      ;;
  esac
}

# step_context_dir <step_id>
#
# Echoes the absolute path to the build context for the step. scion-base
# uses the repo root because it copies go source; core-base and thick-prep use
# image-build/ itself so both can COPY the shared lib/ scripts they run
# (install-core-toolchain.sh, verify-base-contract.sh) -- docker cannot COPY
# from outside the context, and duplicating those scripts per-directory is
# exactly the drift they exist to prevent. Everything else uses its own
# image-build subdirectory.
step_context_dir() {
  case "$1" in
    core-base)     echo "${IMAGE_BUILD_DIR}" ;;
    thick-prep)    echo "${IMAGE_BUILD_DIR}" ;;
    scion-base)    echo "${REPO_ROOT}" ;;
    scion-hub)     echo "${IMAGE_BUILD_DIR}/hub" ;;
    scion-omni)    echo "${REPO_ROOT}" ;;
    *)
      if is_harness_step "$1"; then
        echo "${REPO_ROOT}/harnesses/${1#scion-}"
      else
        return 1
      fi
      ;;
  esac
}

# Omni chain order — single source for the harness subset included in the
# single-node omni image. resolve_targets (above) and step_build_args (below)
# both consume this array. cloudbuild-omni.yaml has a static copy that must
# be kept in sync manually.
# When OMNI_BUILD is true, harnesses chain to each other instead of all
# branching from scion-base:
#   scion-base -> claude -> codex -> opencode -> antigravity -> grok-build -> omni
_OMNI_CHAIN=(scion-claude scion-codex scion-opencode scion-antigravity scion-grok-build scion-omni)

# _omni_chain_parent <step_id>
#
# Returns the parent step for the given step in the omni chain, or empty if
# the step is not in the chain. The first element chains from scion-base;
# each subsequent element chains from the previous one.
_omni_chain_parent() {
  local step="$1"
  local prev="scion-base"
  local s
  for s in "${_OMNI_CHAIN[@]}"; do
    if [[ "${s}" == "${step}" ]]; then
      echo "${prev}"
      return 0
    fi
    prev="${s}"
  done
  return 1
}

# step_build_args <step_id>
#
# Emits one KEY=VALUE line per build-arg on stdout. Reads orchestrator
# state from environment: REGISTRY, TAG, SHORT_SHA, COMMIT_SHA, VERSION,
# BUILD_TIME, BASE_TAG.
# BASE_TAG is the tag (sha or mutable) the orchestrator chose for this
# step's parent image. When REGISTRY is empty (local-only build), BASE_IMAGE
# is emitted with a bare image name (e.g. core-base:latest) so it matches
# the tag the previous step actually wrote into the local image store.
step_build_args() {
  local prefix=""
  if [[ -n "${REGISTRY:-}" ]]; then
    prefix="${REGISTRY}/"
  fi
  case "$1" in
    core-base)
      # Optional npm mirror passthrough (see core-base/Dockerfile). Unset =
      # public registry, i.e. no build-arg emitted and default behaviour.
      if [[ -n "${NPM_REGISTRY:-}" ]]; then
        echo "NPM_REGISTRY=${NPM_REGISTRY}"
      fi
      # Same for the Python package index.
      if [[ -n "${PIP_INDEX_URL:-}" ]]; then
        echo "PIP_INDEX_URL=${PIP_INDEX_URL}"
      fi
      ;;
    thick-prep)
      # BASE_IMAGE default is in the Dockerfile ARG. The mirror passthroughs are
      # the same as core-base's and must be emitted here too: thick-prep now
      # runs the same npm install as core-base, and a build behind a corporate
      # proxy that worked on one chain failing on the other is the exact drift
      # image-build/lib/ exists to eliminate.
      if [[ -n "${NPM_REGISTRY:-}" ]]; then
        echo "NPM_REGISTRY=${NPM_REGISTRY}"
      fi
      if [[ -n "${PIP_INDEX_URL:-}" ]]; then
        echo "PIP_INDEX_URL=${PIP_INDEX_URL}"
      fi
      ;;
    scion-base)
      if [[ "${THICK_BUILD:-}" == "true" ]]; then
        echo "BASE_IMAGE=${prefix}thick-prep:${BASE_TAG}"
      else
        echo "BASE_IMAGE=${prefix}core-base:${BASE_TAG}"
      fi
      if [[ -n "${COMMIT_SHA:-}" ]]; then
        echo "GIT_COMMIT=${COMMIT_SHA}"
      fi
      if [[ -n "${VERSION:-}" ]]; then
        echo "VERSION=${VERSION}"
      fi
      # Commit time (build-images.sh); the Dockerfile falls back to the
      # build host clock when it is empty.
      if [[ -n "${BUILD_TIME:-}" ]]; then
        echo "BUILD_TIME=${BUILD_TIME}"
      fi
      ;;
    scion-hub)
      echo "BASE_IMAGE=${prefix}scion-base:${BASE_TAG}"
      ;;
    scion-omni)
      if [[ "${OMNI_BUILD:-}" == "true" ]]; then
        local parent
        parent="$(_omni_chain_parent "scion-omni")"
        echo "BASE_IMAGE=${prefix}${parent}:${BASE_TAG}"
      else
        echo "BASE_IMAGE=${prefix}scion-base:${BASE_TAG}"
      fi
      if [[ -n "${COMMIT_SHA:-}" ]]; then
        echo "GIT_COMMIT=${COMMIT_SHA}"
      fi
      if [[ -n "${VERSION:-}" ]]; then
        echo "VERSION=${VERSION}"
      fi
      # Commit time (build-images.sh); the Dockerfile falls back to the
      # build host clock when it is empty.
      if [[ -n "${BUILD_TIME:-}" ]]; then
        echo "BUILD_TIME=${BUILD_TIME}"
      fi
      ;;
    *)
      if is_harness_step "$1"; then
        if [[ "${OMNI_BUILD:-}" == "true" ]] && _omni_chain_parent "$1" >/dev/null 2>&1; then
          local parent
          parent="$(_omni_chain_parent "$1")"
          echo "BASE_IMAGE=${prefix}${parent}:${BASE_TAG}"
        else
          echo "BASE_IMAGE=${prefix}scion-base:${BASE_TAG}"
        fi
      else
        return 1
      fi
      ;;
  esac
}

# step_parent <step_id>
#
# Echoes the step ID of the parent image, or empty for root images. Used by
# the orchestrator to thread :short-sha through chained builds and pick the
# right :tag fallback for standalone targets.
step_parent() {
  case "$1" in
    core-base)     echo "" ;;
    thick-prep)    echo "" ;;
    scion-base)
      if [[ "${THICK_BUILD:-}" == "true" ]]; then
        echo "thick-prep"
      else
        echo "core-base"
      fi
      ;;
    scion-hub)     echo "scion-base" ;;
    scion-omni)
      if [[ "${OMNI_BUILD:-}" == "true" ]]; then
        _omni_chain_parent "scion-omni"
      else
        echo "scion-base"
      fi
      ;;
    *)
      if is_harness_step "$1"; then
        if [[ "${OMNI_BUILD:-}" == "true" ]] && _omni_chain_parent "$1" >/dev/null 2>&1; then
          _omni_chain_parent "$1"
        else
          echo "scion-base"
        fi
      else
        return 1
      fi
      ;;
  esac
}

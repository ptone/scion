# syntax=docker/dockerfile:1
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

ARG BASE_IMAGE
FROM ${BASE_IMAGE}

ARG HERMES_VERSION=

# Install Node.js 22 LTS (required by Hermes for tool execution).
# After installing, remove the nodesource apt-source entries so that
# subsequent RUN layers' apt-get update does not re-fetch the nodesource
# InRelease file, which causes a QEMU arm64 "Cannot allocate memory" crash.
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
  && apt-get install -y --no-install-recommends nodejs \
  && rm -f /etc/apt/sources.list.d/nodesource.sources \
           /etc/apt/sources.list.d/nodesource.list \
           /etc/apt/preferences.d/nodejs \
           /etc/apt/preferences.d/nsolid \
           /usr/share/keyrings/nodesource.gpg \
  && apt-get clean && rm -rf /var/lib/apt/lists/*

# Install ripgrep (required by Hermes for code search)
RUN apt-get update && apt-get install -y --no-install-recommends ripgrep \
  && apt-get clean && rm -rf /var/lib/apt/lists/*

# Install Hermes Agent with Vertex AI support (google-auth for ADC) into its
# own virtualenv, rather than into the system interpreter.
#
# This used to `apt-get install python3-pip` and then `pip install
# --break-system-packages`. That worked on bookworm and breaks on trixie, for a
# reason worth writing down because the pieces are three levels apart:
#
#   * hermes-agent pins `packaging==26.0` EXACTLY, not as a floor.
#   * On trixie, python3-wheel (0.46.1-2) gained `Depends: python3-packaging
#     (>= 24)`; on bookworm (0.38.4-2) it depended only on python3-distutils.
#   * python3-pip depends on python3-wheel. So installing pip from apt — the
#     line that used to be right here — is itself what dragged an apt-managed
#     `packaging` 25.0 into dist-packages, three lines above the pip install
#     that then could not replace it:
#
#       Cannot uninstall packaging 25.0
#       The package's contents are unknown: no RECORD file was found
#       error: uninstall-no-record-file
#
# dpkg-installed distributions have no RECORD, so pip cannot know which files
# to remove and refuses. On bookworm nothing owned the name `packaging`, so pip
# installed 26.0 over empty ground and --break-system-packages was enough.
#
# Not fixed with --ignore-installed: that leaves dpkg recording 25.0 while the
# files on disk are 26.0, so any later apt operation touching python3-wheel or
# python3-packaging silently reverts hermes' pinned dependency at RUNTIME, far
# from the cause. Not fixed with an explicit `pip uninstall` first either —
# that is the exact operation already failing. Only `apt-get remove
# python3-packaging` could remove it, and that takes python3-wheel and
# python3-pip with it.
#
# A venv removes the shared namespace instead of arguing with it. `--break-
# system-packages` is PEP 668's "I accept this may break my system" override;
# it was load-bearing here until the system broke in precisely the way the flag
# warns about. An exact `==` pin on a package the distro also ships is the
# tripwire, and hermes-agent is not likely to be the last dependency to have
# one.
#
# No apt step is needed: python3-venv is already installed by the base (see
# image-build/lib/install-core-toolchain.sh, apt-common step), and
# python3.13-venv pulls only python3-pip-whl / python3-setuptools-whl, which
# ship wheel files under /usr/share/python-wheels and install nothing into
# dist-packages. That matters — had the venv chain itself pulled in
# python3-packaging, the base would already be contaminated and dropping pip
# here would buy nothing.
#
# Both of core-base's corporate-network escape hatches still apply: the venv's
# pip reads PIP_INDEX_URL from the environment, and /etc/pip.conf is pip's
# GLOBAL config level, which is honoured inside a venv too — so the pipconf
# build secret keeps working unchanged.
RUN --mount=type=secret,id=pipconf,target=/etc/pip.conf,required=false \
    python3 -m venv /opt/hermes \
 && if [ -n "${HERMES_VERSION}" ]; then \
      /opt/hermes/bin/pip install --no-cache-dir "hermes-agent[vertex]==${HERMES_VERSION}"; \
    else \
      /opt/hermes/bin/pip install --no-cache-dir "hermes-agent[vertex]"; \
    fi \
 && for f in /opt/hermes/bin/*; do \
      case "${f##*/}" in \
        python*|pip*|activate*|Activate*) ;; \
        *) ln -sf "$f" /usr/local/bin/ ;; \
      esac; \
    done \
 && test -x /usr/local/bin/hermes

# Console scripts are symlinked out of the venv rather than putting
# /opt/hermes/bin on PATH, so the venv's python3 does not shadow the system one
# for everything else in the image. CMD ["hermes"] resolves through the
# symlink. The venv stays root-owned: the scion user only needs r+x.
#
# Note that `pip` is deliberately NOT symlinked, so it is absent from PATH at
# runtime where it previously existed. If a hermes skill turns out to install
# packages at runtime, symlink /opt/hermes/bin/pip as well — installs then land
# in hermes' own venv, which is the desired behaviour regardless.

RUN mkdir -p /home/scion/.hermes /home/scion/.hermes/skills \
  && chown -R scion:scion /home/scion/.hermes

CMD ["hermes"]

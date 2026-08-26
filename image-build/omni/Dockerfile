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

# Omni image: Hub + all harnesses for Cloud Run Instances single-node deployment.
#
# The harness chain (claude -> codex -> opencode -> antigravity -> grok-build)
# already installed all harness binaries and config via their own Dockerfiles.
# This final layer adds only the hub server wiring and pins USER/CMD.
#
# Registry convention (used by deploy --image):
#   ghcr.io/<owner>/scion-omni:<tag>
# where <tag> is a release version (e.g. v0.1.0) or "latest".

ARG BASE_IMAGE
FROM ${BASE_IMAGE}

# grok-build ends as USER root (deliberately). Pin to root for sciontool init.
# sciontool init handles the user switch at runtime.
USER root

# Hub state directory.
RUN mkdir -p /home/scion/.scion && chown -R scion:scion /home/scion/.scion

WORKDIR /workspace

# PID 1 is sciontool init (ENTRYPOINT from scion-base).
# Default CMD runs the combined server (hub + web + runtime-broker) for
# Cloud Run Instances deployment.  --enable-web puts the hub API on the web
# server's port 8080 (where Cloud Run routes traffic); --enable-runtime-broker
# starts the broker for agent lifecycle and PTY support.
CMD ["scion", "server", "start", "--foreground", "--host", "0.0.0.0", "--enable-hub", "--enable-web", "--enable-runtime-broker"]

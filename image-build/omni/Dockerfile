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
# The scion binary from scion-base was built with -tags no_embed_web (no web
# assets). This image rebuilds scion WITHOUT that tag so the web UI is embedded.
#
# Registry convention (used by deploy --image):
#   ghcr.io/<owner>/scion-omni:<tag>
# where <tag> is a release version (e.g. v0.1.0) or "latest".

ARG BASE_IMAGE
FROM ${BASE_IMAGE} AS base

# ---- Hub-capable scion builder ----
FROM base AS hub-builder
USER root
WORKDIR /src
ENV CGO_ENABLED=1

# 1. Build web frontend
COPY web/ ./web/
RUN cd web && npm install && npm run build

# 2. Prepare Go build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY harnesses/ ./harnesses/
COPY pkg/ ./pkg/
COPY proto/ ./proto/
COPY resources/ ./resources/
# NOTE: Do NOT copy web/*.go only — the full web/ is already in place with
# dist/client from step 1

# 3. Build scion WITHOUT no_embed_web (embeds web assets)
# Replicate scion-base's ldflags block, minus -tags no_embed_web.
# Keep -buildvcs=false — .git/ is in .dockerignore; without the flag the build
# fails.
ARG GIT_COMMIT
ARG BUILD_TIME
RUN BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" && \
    SCION_PKG="github.com/GoogleCloudPlatform/scion/pkg/version" && \
    LDFLAGS="-s -w -X ${SCION_PKG}.BuildTime=${BUILD_TIME}" && \
    if [ -n "${GIT_COMMIT}" ]; then \
        LDFLAGS="${LDFLAGS} -X ${SCION_PKG}.Commit=${GIT_COMMIT}"; \
    fi && \
    go build -buildvcs=false -ldflags="${LDFLAGS}" -o /usr/local/bin/scion ./cmd/scion/

# ---- Final stage ----
FROM base
USER root

# Replace the no_embed_web scion binary with the hub-capable one
COPY --from=hub-builder /usr/local/bin/scion /usr/local/bin/scion

# Hub state directory.
RUN mkdir -p /home/scion/.scion && chown -R scion:scion /home/scion/.scion

WORKDIR /workspace

# PID 1 is sciontool init (ENTRYPOINT from scion-base).
# Default CMD runs the combined server (hub + web + runtime-broker) for
# Cloud Run Instances deployment.  --enable-web puts the hub API on the web
# server's port 8080 (where Cloud Run routes traffic); --enable-runtime-broker
# starts the broker for agent lifecycle and PTY support.
#
# WORKAROUND (B23): exec-form CMD (["scion", "server", "start", "--foreground",
# ...]) daemonizes — the --foreground flag is not parsed by cobra when scion is
# invoked directly via the container runtime.  Wrapping in sh -c exec forces
# shell word-splitting, which makes the flag parse correctly.  Root cause is
# unresolved; see .design/project-log/ for investigation notes.
CMD ["/bin/sh", "-c", "exec scion server start --foreground --host 0.0.0.0 --enable-hub --enable-web --enable-runtime-broker"]

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

# Experimental: the Antigravity desktop app (Electron) run headless, with an
# in-container nginx that turns the app's loopback HTTPS web UI into plain
# HTTP on 127.0.0.1:8090 for the Scion port proxy and `scion tunnel`.
# amd64 only. See README.md.

ARG BASE_IMAGE=scion-base:latest
FROM ${BASE_IMAGE}

ARG TARGETARCH

USER root

# Pinned by immutable versioned URL plus our own sha256. A version bump is a
# reviewed change of both ARGs; see README.md ("Version bump").
ARG AG_VERSION=2.27.0-4933976025071616
ARG AG_DEB_SHA256=d28326ada1ea2f45ec3ee1fdf46caee9e34676e25750337ab69c0a3dd257dac5

# The vendor install script is never run. The .deb's maintainer scripts run
# here, at build time only. /opt/Antigravity ends up root-owned and not
# writable by the agent user, so the app cannot replace itself at runtime.
#
# nginx: Debian's core package. nginx-light is only a metapackage on trixie.
# The build fails unless the modules the proxy config needs are compiled in.
RUN set -eu; \
    if [ "${TARGETARCH:-amd64}" != "amd64" ]; then \
      echo "antigravity-ui: amd64 only (TARGETARCH=${TARGETARCH})" >&2; exit 1; \
    fi; \
    curl -fsSL --proto '=https' --tlsv1.2 -o /tmp/antigravity.deb \
      "https://storage.googleapis.com/antigravity-public/antigravity-hub/${AG_VERSION}/linux-x64/Antigravity.deb"; \
    echo "${AG_DEB_SHA256}  /tmp/antigravity.deb" | sha256sum -c -; \
    apt-get update; \
    apt-get install -y --no-install-recommends /tmp/antigravity.deb nginx; \
    rm -f /tmp/antigravity.deb; \
    rm -rf /var/lib/apt/lists/*; \
    nginx -V 2>&1 | grep -q -- '--with-http_sub_module' \
      || { echo "antigravity-ui: nginx lacks http_sub_module" >&2; exit 1; }; \
    nginx -V 2>&1 | grep -q -- '--with-http_ssl_module' \
      || { echo "antigravity-ui: nginx lacks http_ssl_module" >&2; exit 1; }; \
    test -x /opt/Antigravity/antigravity; \
    chown -R root:root /opt/Antigravity; \
    chmod -R go-w /opt/Antigravity; \
    rm -f /etc/nginx/sites-enabled/default; \
    install -d -m 0755 /usr/local/share/antigravity-ui

# The directory exists before these COPYs: a COPY --chmod into a missing
# directory would create it with the file mode (no traverse bit).
COPY --chmod=0755 antigravity-ui-wrapper.sh /usr/local/bin/antigravity-ui-wrapper
COPY --chmod=0644 nginx.conf.tmpl /usr/local/share/antigravity-ui/nginx.conf.tmpl
COPY --chmod=0644 agui_helper.py /usr/local/share/antigravity-ui/agui_helper.py

USER scion

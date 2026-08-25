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
# Cloud Run Sandboxes use --rootfs / (inheriting the launcher's filesystem),
# so a sandbox cannot have a different image from its launcher. This single
# image contains both the hub server and every supported harness binary.
#
# Harnesses included: antigravity, claude, codex, opencode, grok.
# Per-harness provision.py is staged at runtime by container_script_harness.go,
# not baked into this image.

ARG BASE_IMAGE
FROM ${BASE_IMAGE}

ARG TARGETARCH

USER root

# ---- Home directories for all harnesses ----
# Each harness stores config/state in a distinct subtree under ~scion.
#   antigravity: ~/.gemini/antigravity-cli, ~/.agents
#   claude:      ~/.claude
#   codex:       ~/.codex
#   opencode:    ~/.local/share/opencode
#   grok:        ~/.grok
RUN mkdir -p \
      /home/scion/.gemini/antigravity-cli \
      /home/scion/.agents \
      /home/scion/.claude \
      /home/scion/.codex \
      /home/scion/.codex/skills \
      /home/scion/.local/share/opencode \
      /home/scion/.grok/hooks \
      /home/scion/.grok/skills \
      /home/scion/.grok/plugins \
      /home/scion/.grok/sessions \
      /home/scion/.grok/agents \
      /commandhistory \
      /workspace \
    && touch /commandhistory/.bash_history \
    && chown -R scion:scion \
        /home/scion/.gemini \
        /home/scion/.agents \
        /home/scion/.claude \
        /home/scion/.codex \
        /home/scion/.local \
        /home/scion/.grok \
        /commandhistory \
        /workspace

WORKDIR /workspace

# ---- COPY files from harness bundles ----
# Build context is the repo root so paths are relative to it.

# Claude: firewall init script + sudoers entry
COPY harnesses/claude/init-firewall.sh /usr/local/bin/init-firewall.sh
RUN chmod +x /usr/local/bin/init-firewall.sh && \
    echo "scion ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh" > /etc/sudoers.d/scion-firewall && \
    chmod 0440 /etc/sudoers.d/scion-firewall

# Grok: seed config
COPY --chown=scion:scion harnesses/grok-build/home/.grok/config.toml /home/scion/.grok/config.toml

# ---- Claude: dev-environment setup ----
# Claude: bash history persistence is handled via zsh-in-docker -a parameter below.

ENV DEVCONTAINER=true

# git-delta for improved diffs
ARG GIT_DELTA_VERSION=0.18.2
RUN ARCH=$(dpkg --print-architecture) && \
    wget -q "https://github.com/dandavison/delta/releases/download/${GIT_DELTA_VERSION}/git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" && \
    dpkg -i "git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb" && \
    rm "git-delta_${GIT_DELTA_VERSION}_${ARCH}.deb"

ENV SHELL=/bin/zsh
ENV EDITOR=nano
ENV VISUAL=nano

# zsh configuration with plugins
ARG ZSH_IN_DOCKER_VERSION=1.2.0
RUN sh -c "$(wget -O- https://github.com/deluan/zsh-in-docker/releases/download/v${ZSH_IN_DOCKER_VERSION}/zsh-in-docker.sh)" -- \
    -p git \
    -p fzf \
    -a "source /usr/share/doc/fzf/examples/key-bindings.zsh" \
    -a "source /usr/share/doc/fzf/examples/completion.zsh" \
    -a "export PROMPT_COMMAND='history -a' && export HISTFILE=/commandhistory/.bash_history" \
    -x

# ---- Antigravity: dbus/keyring setup ----
RUN printf 'if [ -z "$DBUS_SESSION_BUS_ADDRESS" ]; then\n    eval $(dbus-launch --sh-syntax)\n    export DBUS_SESSION_BUS_ADDRESS\nfi\nif ! pgrep gnome-keyring-daemon >/dev/null; then\n    eval $(echo "test" | gnome-keyring-daemon --unlock 2>/dev/null)\n    gnome-keyring-daemon --start --components=secrets,pkcs11,ssh >/dev/null 2>&1\nfi\n' >> /etc/bash.bashrc

# ---- NPM global installs: claude, codex, opencode (deduped) ----
RUN npm cache clean --force \
    && npm install -g \
        @anthropic-ai/claude-code@latest \
        @openai/codex@latest \
        opencode-ai \
    && ln -sf /usr/local/share/npm-global/bin/codex /usr/local/bin/codex \
    && npm cache clean --force

# ---- Antigravity: binary download ----
ARG AGY_VERSION=1.1.17
RUN ARCH=$(case "${TARGETARCH:-amd64}" in amd64) echo "x64";; arm64) echo "arm64";; *) echo "${TARGETARCH}";; esac) \
    && curl -fsSL -o /tmp/cli.tar.gz \
         "https://github.com/google-antigravity/antigravity-cli/releases/download/${AGY_VERSION}/agy_cli_linux_${ARCH}.tar.gz" \
    && tar -xzf /tmp/cli.tar.gz -C /tmp antigravity \
    && mv /tmp/antigravity /usr/local/bin/agy \
    && chmod +x /usr/local/bin/agy \
    && rm /tmp/cli.tar.gz

# ---- Grok: install via official script ----
# The script downloads to ~/.grok/bin/ and symlinks into /usr/local/bin/.
# We copy the real binary so the image is self-contained.
RUN curl -fsSL https://x.ai/cli/install.sh | bash \
    && rm -f /usr/local/bin/grok /usr/local/bin/agent \
    && cp -fL /root/.grok/bin/grok /usr/local/bin/grok \
    && chmod +x /usr/local/bin/grok \
    && rm -rf /root/.grok

# ---- Hub layer ----
# Hub state lives under /home/scion/.scion (sqlite, templates, etc.).
# scion-base already created this; ensure it survives any flattening.
RUN mkdir -p /home/scion/.scion && chown -R scion:scion /home/scion/.scion

# Drop all per-harness CMDs. PID 1 is sciontool init (ENTRYPOINT from scion-base);
# the harness command comes from config.yaml's command: field at runtime.
# Do NOT switch to USER scion — sciontool init handles the user switch.
# Default CMD runs the hub server for Cloud Run Instances deployment.
CMD ["scion", "server", "start", "--foreground", "--host", "0.0.0.0", "--enable-hub"]

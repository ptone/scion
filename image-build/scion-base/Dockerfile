# syntax=docker/dockerfile:1
ARG BASE_IMAGE=node:20-slim
FROM node:20-slim AS builder
ENV DEBIAN_FRONTEND=noninteractive

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    wget \
    curl \
    make \
    gcc \
    libssl-dev \
    libcurl4-gnutls-dev \
    libexpat1-dev \
    zlib1g-dev \
    gettext \
    ncurses-term \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install Go
ARG GO_VERSION=1.25.4
RUN ARCH=$(dpkg --print-architecture) && \
    case "${ARCH}" in \
      amd64) GO_ARCH='linux-amd64' ;; \
      arm64) GO_ARCH='linux-arm64' ;; \
      *) echo "Unsupported architecture: ${ARCH}"; exit 1 ;; \
    esac && \
    curl -L "https://go.dev/dl/go${GO_VERSION}.${GO_ARCH}.tar.gz" -o go.tar.gz && \
    tar -C /usr/local -xzf go.tar.gz && \
    rm go.tar.gz

# Install Git from source
WORKDIR /opt
RUN wget https://www.kernel.org/pub/software/scm/git/git-2.52.0.tar.gz
RUN tar -xvf git-2.52.0.tar.gz
WORKDIR /opt/git-2.52.0/
RUN make prefix=/usr/local all
RUN make prefix=/usr/local install

# Final Stage
FROM ${BASE_IMAGE}
USER root

# Install runtime dependencies for Git and Tmux
# git needs: libcurl4-gnutls-dev (or similar), libexpat1, zlib1g
RUN apt-get update && apt-get install -y --no-install-recommends \
    tmux \
    ca-certificates \
    libcurl4-gnutls-dev \
    libexpat1 \
    zlib1g \
    python3 \
    make \
    g++ \
    man-db \
    curl \
    wget \
    dnsutils \
    less \
    jq \
    bc \
    gh \
    unzip \
    rsync \
    ripgrep \
    procps \
    psmisc \
    lsof \
    socat \
    sudo \
    fzf \
    zsh \
    gnupg2 \
    iptables \
    ipset \
    iproute2 \
    aggregate \
    nano \
    vim \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Copy artifacts from builder
COPY --from=builder /usr/local/go /usr/local/go
COPY --from=builder /usr/local/bin/git /usr/local/bin/git
COPY --from=builder /usr/local/libexec/git-core /usr/local/libexec/git-core
COPY --from=builder /usr/local/share/git-core /usr/local/share/git-core

# Setup npm global directory and permissions
RUN mkdir -p /usr/local/share/npm-global \
    && chown -R node:node /usr/local/share/npm-global
ENV NPM_CONFIG_PREFIX=/usr/local/share/npm-global
ENV PATH=/usr/local/share/npm-global/bin:/usr/local/go/bin:$PATH
ENV LC_ALL C.UTF-8

USER node


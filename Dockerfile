# Stage 1: Build the web frontend assets
FROM node:22-alpine AS frontend
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ .
# npm run build already runs copy:shoelace-icons, vite build, and copy:client
RUN npm run build

# Stage 2: Build the Scion Hub binary (with embedded web assets)
FROM golang:1.26.1-alpine AS builder
WORKDIR /app
ENV GOWORK=off

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Copy built frontend assets into the embed location
COPY --from=frontend /web/dist/client web/dist/client

# Build a static binary (CGO_ENABLED=0) so it runs on the debian runtime image
# without musl/glibc mismatch from the Alpine builder.
RUN CGO_ENABLED=0 go build -o /scion ./cmd/scion/

# Stage 3: Create a minimal runtime image
#
# Named only so that the hub-gke stage below can extend it. Adding "AS runtime"
# is builder metadata: it changes no instruction and therefore no layer.
FROM debian:bookworm-slim AS runtime
WORKDIR /app

# Install runtime dependencies used by the Hub broker and Cloud Run IAP exec path.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git openssh-client && rm -rf /var/lib/apt/lists/*

# Copy the binary from the builder stage
COPY --from=builder /scion /usr/local/bin/scion

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/scion"]

# Stage 4: Non-root variant for GKE, built with `--target hub-gke`.
#
# Identical to the runtime image except that it runs as uid 1000 with a writable
# HOME. Kubernetes `runAsNonRoot: true` refuses to start an image whose USER is
# root, and a root hub writing to a shared Filestore volume creates uid-0 project
# directories that agent pods running as uid 1000 cannot write to.
#
# Deliberately absent: ENV KUBECONFIG. pkg/k8s/client.go prefers an explicit
# kubeconfig over in-cluster credentials, so baking one here would silently
# disable in-cluster ServiceAccount auth. Also deliberately absent: any CMD, so
# no arguments (and no secrets in them) are baked into the image; the chart
# supplies them.
FROM runtime AS hub-gke
RUN useradd -u 1000 -m -d /home/scion scion \
 && mkdir -p /home/scion/.scion \
 && chown -R 1000:1000 /home/scion
ENV HOME=/home/scion
USER 1000:1000

# Stage 5: the default build target.
#
# MUST remain the last stage in this file: `docker build` with no `--target`
# builds the last stage, and external consumers (`gcloud run deploy --source`,
# `gcloud builds submit`) pass no `--target`. Keeping this stage last, and empty,
# leaves the default target producing exactly the stage-3 runtime image -- the
# non-root hub-gke image is reachable only via `--target hub-gke`.
FROM runtime

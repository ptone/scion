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
# The "AS runtime" name is depended on by BOTH stages below -- do not rename it
# without updating them. Naming a stage is builder metadata only: it changes no
# instruction, so the layers this stage produces are unchanged.
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
#
# Also deliberately absent, after review: a pre-created `$HOME/.scion`. The
# server only calls config.InitGlobal when that directory does NOT exist
# (cmd/server_foreground.go), so creating it here would make this image skip a
# startup step the plain runtime image runs -- a behaviour difference that buys
# nothing, because the chart mounts an emptyDir at exactly that path and the
# directory exists at runtime either way. `useradd -m` already creates
# /home/scion owned by uid 1000, which is all a writable HOME requires.
FROM runtime AS hub-gke
# The chown is not redundant with `useradd -m`: -m sets the owner, but the gid
# is whichever one useradd allocates, and `USER 1000:1000` names gid 1000
# explicitly. Pinned rather than assumed.
RUN useradd -u 1000 -m -d /home/scion scion \
 && chown -R 1000:1000 /home/scion
ENV HOME=/home/scion
USER 1000:1000

# Stage 5: the default build target. DO NOT DELETE, DO NOT ADD A STAGE BELOW IT.
#
# This stage is empty on purpose and its only job is to be last. `docker build`
# with no `--target` builds the LAST stage in the file, and this image's external
# consumers (`gcloud run deploy --source`, `gcloud builds submit`) pass no
# `--target`. With this stage present, the default target resolves to the stage-3
# runtime image, exactly as it did before hub-gke existed; hub-gke is reachable
# only via `--target hub-gke`.
#
# Delete it -- or append a stage after it -- and the default build target
# silently becomes the non-root uid-1000 hub-gke image for every consumer that
# never asked for it. Nothing in the build fails when that happens; the change
# only shows up at runtime, in production, as permission errors.
FROM runtime

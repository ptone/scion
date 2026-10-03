# Throwaway image for the conduit-0b spike: a Linux "agent container" with the
# embedded SSH server binary, reached via `docker exec -i ... -stdio`
# (the same stdio path `scion tunnel --ssh --stdio` will provide).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -buildvcs=false -o /conduit-sshd .

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl wget tar gzip git procps bash tmux \
 && rm -rf /var/lib/apt/lists/*
RUN useradd -m -s /bin/bash scion && mkdir -p /workspace && chown scion:scion /workspace
COPY --from=build /conduit-sshd /usr/local/bin/conduit-sshd
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
USER scion
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]

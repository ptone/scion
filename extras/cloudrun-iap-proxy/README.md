# Cloud Run IAP Reverse Proxy

A lightweight Go reverse proxy designed to run on Google Cloud Run to front internal VPC services (such as a Scion Hub instance running on a GCE VM) while preserving Identity-Aware Proxy (IAP) headers and supporting HTTP/2 streaming.

## Features

- **IAP Header Preservation:** Forwards and logs incoming Google Identity-Aware Proxy assertion headers:
  - `X-Goog-IAP-JWT-Assertion`
  - `X-Goog-Authenticated-User-Email`
  - `X-Goog-Authenticated-User-Id`
- **Host Header Rewriting:** Sets `req.Host` to the target host to properly route to virtual hosts on the backend.
- **HTTP/2 (H2C) Support:** Uses `golang.org/x/net/http2/h2c` to support HTTP/2 cleartext multiplexing on Cloud Run, crucial for high-concurrency event streams and Server-Sent Events (SSE).
- **Health Check Endpoint:** Provides `/proxy-healthz` for Cloud Run and external uptime checks without proxying back to the target service.
- **Direct VPC Egress Ready:** Easily deployed with Cloud Run Direct VPC Egress to reach internal compute instances on private RFC 1918 IPs.

## Architecture

```
[User Browser / Client]
          │
          ▼ (HTTPS)
 [Cloud Run (IAP Proxy)]
          │
          ▼ (Direct VPC Egress / Private IP)
   [Scion Hub (GCE VM)]
```

## Configuration

| Environment Variable | Description | Default | Required |
|----------------------|-------------|---------|----------|
| `TARGET_URL` | Destination backend URL (e.g., `http://10.128.0.2:8080`) | — | **Yes** |
| `PORT` | Listening port for the proxy server | `8080` | No |
| `FORWARDED_PROTO` | Scheme of the front end that terminates client connections in front of the proxy (`https` or `http`). The proxy sends it to the backend as `X-Forwarded-Proto`, replacing any `Forwarded` or `X-Forwarded-*` header the client sent (it sets `X-Forwarded-For` to its immediate peer and `X-Forwarded-Host` to the host it served), so a client cannot claim TLS it did not use (the Scion Hub trusts this header from a proxy listed in `server.trusted_proxies`). Cloud Run's front end terminates TLS, so the default fits a Cloud Run deployment. | `https` | No |

## Deployment

### Prerequisites

- `gcloud` CLI authenticated with permissions to deploy Cloud Run services and configure VPC access.
- An existing internal service (e.g. Scion Hub on GCE) with internal IP and port reachable within the VPC network.

### Using `deploy.sh`

```bash
export TARGET_URL="http://<INTERNAL_IP>:8080"
export PROJECT_ID="my-gcp-project"
export REGION="us-central1"
export SERVICE_NAME="scion-hub-iap-proxy"

./deploy.sh
```

### Manual Deployment via `gcloud`

```bash
gcloud run deploy scion-hub-iap-proxy \
    --project="my-gcp-project" \
    --region="us-central1" \
    --source="." \
    --set-env-vars="TARGET_URL=http://<INTERNAL_IP>:8080" \
    --network="default" \
    --subnet="default" \
    --vpc-egress="all-traffic" \
    --allow-unauthenticated \
    --port=8080
```

## Development and Testing

Run unit tests locally:

```bash
go test -v ./...
```

Run proxy locally against a local backend:

```bash
TARGET_URL="http://localhost:8080" PORT=9000 go run .
```

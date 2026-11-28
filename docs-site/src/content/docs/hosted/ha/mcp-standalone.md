---
title: Standalone MCP Server
description: Deploy the Scion MCP server as a standalone service on Cloud Run or GKE, including hubs behind Identity-Aware Proxy (IAP).
---

:::caution[Experimental]
The MCP server is gated by the `hub.mcp_server` [experiment](/scion/reference/experiments/) (off by default). While it is off, `/mcp` answers `404`.
:::

## Overview

`scion server start --enable-mcp` can run in two modes using the same binary and container image:

- **Embedded mode** (`--enable-hub --enable-mcp`): binds the MCP listener (default port `9820`) alongside the Hub in the same process and dispatches Hub API calls in-process with no network hop.
- **Standalone mode** (`--hosted --enable-mcp`, or `--enable-mcp --enable-hub=false`): runs as a stateless HTTP facade against a remote Hub configured by `server.mcp.hub_endpoint`. (In workstation mode without `--hosted`, `scion server start` enables the Hub by default, so pass `--hosted` or `--enable-hub=false` to run standalone.)

Deploy the standalone MCP server against a Hub version that supports the `GET /api/v1/experiments?name=hub.mcp_server` probe (an older Hub ignores `?name=` and omits `hub.mcp_server`, causing `/mcp` to fail closed with `404`).

Use **standalone mode** when:

- The Hub runs on **Cloud Run** (which exposes a single container port, so embedded mode cannot expose a second port).
- The Hub sits behind **Identity-Aware Proxy (IAP)** or **Cloud Run invoker IAM**, and external MCP clients need a non-IAP ingress that forwards their Hub credentials without passing IAP directly.
- You want to scale MCP replicas independently behind a load balancer.

Standalone replicas hold no session or cursor state: any replica can resume any cursor returned by another replica. Every MCP call forwards the caller's Hub credential (`Authorization: Bearer` or `X-Scion-Federation-Token`) to the Hub; the Hub remains the sole authorization authority. Cookies, `X-Goog-Iap-Jwt-Assertion`, and all other non-allowlisted headers sent to `/mcp` are stripped before requests leave for the Hub.

## Configuration

Set `server.mcp.hub_endpoint` in `settings.yaml` or via environment variable `SCION_SERVER_MCP_HUBENDPOINT`. It is startup-only and never writable through the server-config API.

```yaml
schema_version: "1"
server:
  mcp:
    enabled: true
    host: "0.0.0.0"
    port: 9820
    path: /mcp
    public_url: "https://mcp.example.com"
    hub_endpoint: "https://hub.example.com"
    allowed_origins: []
```

### Health and readiness

The standalone MCP listener exposes two health routes on its own port:

- `GET /healthz`: returns `200` while the process is up.
- `GET /readyz`: returns `503` while shutting down, or if `server.mcp.hub_endpoint`'s `/healthz` probe has been failing continuously for 30 seconds or more.

### Transport authentication to an IAP or IAM-protected Hub

When `server.mcp.hub_endpoint` is protected by Google IAP or Cloud Run invoker IAM, configure transport-layer OIDC authentication on the standalone MCP service using the same environment variables as containerized Runtime Brokers (see [Proxy Auth (IAP)](/scion/hosted/ha/auth-proxy-iap/)):

| Environment variable | Value |
| :--- | :--- |
| `SCION_TRANSPORT_MODE` | `iap` (sends `Proxy-Authorization`) or `cloudrun_invoker` (sends `X-Serverless-Authorization`) |
| `SCION_TRANSPORT_AUDIENCE` | The IAP OAuth 2.0 Client ID (for `iap`) or the Hub Cloud Run service URL (for `cloudrun_invoker`) |

Because `SCION_TRANSPORT_MODE` places the service account's OIDC identity token in `Proxy-Authorization` or `X-Serverless-Authorization`, the caller's `Authorization: Bearer <token>` header passes through to the Hub untouched and the service account never gains user authority.

## Recipe 1: Cloud Run second service

Deploy the same Scion server container image as a second Cloud Run service with `--enable-mcp` and no `--enable-hub`. Expose it without IAP (or with your chosen MCP ingress) while granting its runtime service account `roles/iap.httpsResourceAccessor` (for an IAP-protected Hub) or `roles/run.invoker` (for an IAM-protected Hub service).

```bash
gcloud run deploy scion-mcp \
  --image="us-docker.pkg.dev/${PROJECT_ID}/scion/scion-server:latest" \
  --region="${REGION}" \
  --port=9820 \
  --service-account="scion-mcp@${PROJECT_ID}.iam.gserviceaccount.com" \
  --allow-unauthenticated \
  --args="server,start,--foreground,--hosted,--enable-mcp" \
  --set-env-vars="SCION_SERVER_MCP_HOST=0.0.0.0,SCION_SERVER_MCP_PORT=9820,SCION_SERVER_MCP_HUBENDPOINT=https://hub.example.com,SCION_SERVER_MCP_PUBLICURL=https://mcp.example.com,SCION_TRANSPORT_MODE=iap,SCION_TRANSPORT_AUDIENCE=${IAP_OAUTH_CLIENT_ID}"
```

Notes for Cloud Run:

- On Cloud Run with an IAP-protected Hub, grant `scion-mcp@${PROJECT_ID}.iam.gserviceaccount.com` the `roles/iap.httpsResourceAccessor` role on the Hub's backend service and set `SCION_TRANSPORT_MODE=iap` and `SCION_TRANSPORT_AUDIENCE` to the IAP OAuth Client ID.
- If the Hub is a private Cloud Run service protected by IAM instead of IAP, grant `roles/run.invoker` on the Hub service and set `SCION_TRANSPORT_MODE=cloudrun_invoker` and `SCION_TRANSPORT_AUDIENCE` to the Hub's Cloud Run URL.
- Cloud Run caps individual HTTP request durations at 60 minutes; long-lived streams end at that boundary and clients reconnect with their last cursor.
- Configure the Cloud Run startup and liveness probes against `/healthz` on port `9820`; use `/readyz` for load-balancer health checks and Kubernetes readiness probes.

## Recipe 2: GKE Service without IAP

On GKE, you can run standalone MCP replicas as a separate `Deployment` talking to the Hub's internal cluster Service (bypassing IAP entirely) and exposed via a dedicated non-IAP `Service` and `Ingress`/`HTTPRoute`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: scion-mcp
  namespace: scion
spec:
  replicas: 2
  selector:
    matchLabels:
      app: scion-mcp
  template:
    metadata:
      labels:
        app: scion-mcp
    spec:
      containers:
        - name: scion-mcp
          image: us-docker.pkg.dev/PROJECT_ID/scion/scion-server:latest
          args:
            - server
            - start
            - --foreground
            - --hosted
            - --enable-mcp
          env:
            - name: SCION_SERVER_MCP_HOST
              value: "0.0.0.0"
            - name: SCION_SERVER_MCP_PORT
              value: "9820"
            - name: SCION_SERVER_MCP_HUBENDPOINT
              value: "http://scion-hub.scion.svc.cluster.local:8080"
            - name: SCION_SERVER_MCP_PUBLICURL
              value: "https://mcp.example.com"
          ports:
            - name: mcp
              containerPort: 9820
          livenessProbe:
            httpGet:
              path: /healthz
              port: mcp
          readinessProbe:
            httpGet:
              path: /readyz
              port: mcp
            periodSeconds: 10
---
apiVersion: v1
kind: Service
metadata:
  name: scion-mcp
  namespace: scion
spec:
  type: ClusterIP
  selector:
    app: scion-mcp
  ports:
    - name: mcp
      port: 80
      targetPort: mcp
```

Expose `scion-mcp` on a dedicated `Ingress` or Gateway `HTTPRoute` **without** a `BackendConfig` IAP block (unlike the Hub/Web Service described in [Proxy Auth (IAP)](/scion/hosted/ha/auth-proxy-iap/)). Because `scion-mcp` is stateless, no session affinity is required on the `Service` or load balancer.

If your GKE `scion-mcp` pods instead reach an external IAP-protected Hub URL, attach a Workload Identity service account with `roles/iap.httpsResourceAccessor` and add `SCION_TRANSPORT_MODE=iap` and `SCION_TRANSPORT_AUDIENCE=<iap-client-id>` to the container `env`.

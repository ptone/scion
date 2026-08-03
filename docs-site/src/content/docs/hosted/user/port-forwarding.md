---
title: Agent Port Forwarding
description: Expose local HTTP ports from agent containers through the Hub as authenticated, reverse-proxied URLs.
---

**What you will learn**: How to expose local web apps and HTTP ports running inside an agent's container to the external network via the Scion Hub.

Scion's **Agent Port Forwarding** (also known as Web Access / Auto-Expose) enables agents to run local HTTP servers (e.g., debug servers, custom web UIs, file viewers) and securely share them with users and other systems. 

Ports are exposed through an outbound WebSocket-based reverse tunnel, meaning they work seamlessly on private networks, firewalls, and Kubernetes clusters without requiring inbound ports or public IP addresses on the agent hosts.

---

## How It Works

1. **Reverse Tunnel**: The agent container initiates an outbound WebSocket connection (reverse tunnel) to the Hub API at `/api/v1/agents/{agentID}/ports/tunnel`.
2. **Reverse Proxying**: When a user accesses the Hub's reverse-proxy endpoint for the exposed port, the Hub wraps the incoming HTTP request into a WebSocket protocol message and sends it down the tunnel.
3. **Local Request Loopback**: The agent-side `sciontool` daemon receives the message, replays the HTTP request against `127.0.0.1` (localhost) inside the container, and sends the response back through the WebSocket tunnel.
4. **Authentication**: All traffic routed through the Hub's reverse-proxy is fully authenticated and restricted to users who have access to the agent's project.

---

## Exposing Ports Manually

An agent (or an interactive user inside the agent's PTY) can manually register and deregister exposed ports using the `sciontool` CLI:

```bash
# Expose local port 8000 under the label "my-web-app"
sciontool expose 8000 --label "my-web-app"

# Stop exposing port 8000
sciontool unexpose 8000
```

### Listing Exposed Ports

To view currently exposed ports inside the container, run:

```bash
sciontool expose --list
```

---

## Auto-Expose

Scion includes a built-in **Auto-Expose** reconciler that automatically detects when a service starts listening on a TCP port inside the container and registers it with the Hub.

Auto-expose is disabled by default. You can enable and configure it by setting the following environment variables in your agent's environment (via your template `scion-agent.yaml` or as Hub secrets/env-vars):

| Environment Variable | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `SCION_AUTO_EXPOSE_PORTS` | boolean | `false` | Set to `true` (or `1`) to enable the auto-expose reconciler. |
| `SCION_AUTO_EXPOSE_INTERVAL` | duration | `3s` | How often the container's TCP ports are scanned. (Minimum: `1s`). |
| `SCION_AUTO_EXPOSE_MODE` | string | `"allowlist"` | Port filtering mode: `"allowlist"` or `"denylist"`. |
| `SCION_AUTO_EXPOSE_PORTS_LIST` | string | `""` | Comma-separated list of ports to allow/deny based on the mode. |
| `SCION_AUTO_EXPOSE_MIN_PORT` | integer | `1024` | Do not scan or expose ports below this number. |

### Security & Restrictions

To protect agent and infrastructure integrity, certain ports and maximum limits are strictly enforced:

- **Maximum Exposed Ports**: Each agent container is capped at a maximum of **10** auto-exposed ports at any time.
- **Infrastructure Blocklist**: System and infrastructure ports are permanently denied from being auto-exposed:
  - Port `9810` (system logs, observability, and telemetry endpoint)
  - Port `18380` (agent service ports)
- **Safe Exclusions**: Port `8080` is fully allowed and can be exposed if safe to do so.

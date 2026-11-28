# `hack/conduit-c19` — Criterion 19 Conduit Live Load Driver

Non-shipped validation tooling for Conduit Criterion 19 (v2.17 / v2.22 §6.2). Never compiled into `./cmd/scion` or `./cmd/sciontool`.

## Build

```bash
go build -buildvcs=false -o /tmp/conduit-c19 ./hack/conduit-c19
```

## Agents manifest (`--agents-file`)

Provide a JSON array of pre-seeded agents (each with its agent JWT):

```json
[
  {
    "agent_id": "agent-uuid-0",
    "project_id": "project-uuid",
    "launch_id": "run-uuid-0",
    "token": "<agent-jwt>",
    "hub_url": "https://hub-a.internal:8443"
  }
]
```

If `hub_url` is omitted per entry, agents use `--hub-url` (or round-robin across `--hub-urls`).

## Modes

### 1. Phase 2 traffic (`--mode=phase2`)
Interactive PTY echo/keepalive sessions + stalled bulk TCP tunnel on agent 0 + baseline vs under-load control-plane preflight probes + planned drain `4503` observation.

```bash
C19_USER_TOKEN="<user-jwt>" \
/tmp/conduit-c19 \
  --mode=phase2 \
  --hub-url=https://lb.internal \
  --agents-file=/tmp/agents.json \
  --duration=30s \
  --pty-count=8 \
  --hub-rss-pids=1234,5678 \
  --memory-budget-mb=1024 \
  --output=/tmp/c19-phase2.json
```

### 2. Broker RPC traffic (`--mode=broker-rpc`)
High-concurrency hub-to-broker RPC churn (`GET /api/v1/agents/{id}/logs?tail=1` routed over the broker's Conduit session) exercising `MaxConcurrentRPCs=128`, alongside a stalled bulk TCP tunnel and baseline vs under-load control probes.

```bash
C19_USER_TOKEN="<user-jwt>" \
C19_BROKER_ID="<broker-id>" \
C19_BROKER_SECRET_B64="<broker-hmac-secret-b64>" \
/tmp/conduit-c19 \
  --mode=broker-rpc \
  --hub-url=https://lb.internal \
  --agents-file=/tmp/agents.json \
  --duration=30s \
  --rpc-concurrency=128 \
  --output=/tmp/c19-broker-rpc.json
```

### 3. `deliver-message` traffic (`--mode=deliver`, v2.22 §6.2)
Creates real message rows via `POST /api/v1/agents/{id}/message`, then delivers them via `POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/deliver` (1 delivery/agent/2s default, plus 512 KiB bodies with shipped `ExtraSubmits=2, 300ms`), mixed with concurrent broker RPC churn and a stalled bulk TCP tunnel.

```bash
C19_USER_TOKEN="<user-jwt>" \
C19_BROKER_ID="<broker-id>" \
C19_BROKER_SECRET_B64="<broker-hmac-secret-b64>" \
/tmp/conduit-c19 \
  --mode=deliver \
  --hub-url=https://lb.internal \
  --agents-file=/tmp/agents.json \
  --duration=30s \
  --deliver-interval=2s \
  --large-body-count=10 \
  --rpc-concurrency=128 \
  --clock-skew=0ms \
  --output=/tmp/c19-deliver.json
```

## Planned drain observation

Pass `--drain-after=15s --drain-cmd='docker kill -s INT hub-b' --expect-drain` to trigger a planned hub drain mid-run and assert that at least one PTY stream closes with `4503` (`relay_restart`).

## Output & Exit Code

- Prints a human-readable pass/fail bar table to stdout (or JSON with `--json`).
- Writes the full machine-readable JSON report to `--output` when specified.
- Exits `0` if every evaluated bar passes, `1` if any evaluated bar fails or a runtime error occurs, `2` on invalid flags.

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
Interactive PTY echo/keepalive sessions + stalled bulk TCP tunnel on agent 0 + baseline vs under-load control-plane preflight probes + planned drain `4503` (`relay_restart`) observation.

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
  --drain-after=15s \
  --drain-cmd='docker kill -s INT hub-b' \
  --expect-drain \
  --output=/tmp/c19-phase2.json
```

### 2. Broker RPC traffic (`--mode=broker-rpc`)
High-concurrency hub-to-broker RPC churn (`GET /api/v1/agents/{id}/logs?tail=1` routed over the broker's Conduit session) exercising `MaxConcurrentRPCs=128` (`--rpc-concurrency=128` default), with optional `--broker-rpc-latency` inside the fake broker RPC handler so in-flight RPCs genuinely overlap on the broker session (reported as `max_concurrent_broker_session_rpcs`).

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
  --broker-rpc-latency=25ms \
  --hub-rss-pids=1234,5678 \
  --memory-budget-mb=1024 \
  --drain-after=15s \
  --drain-cmd='docker kill -s INT hub-b' \
  --expect-drain \
  --output=/tmp/c19-broker-rpc.json
```

### 3. `deliver-message` traffic (`--mode=deliver`, v2.22 §6.2)
Creates real message rows via `POST /api/v1/agents/{id}/message` (splitting each 512 KiB body into `ceil(512 KiB / 16000) = 33` rows whose stored texts concatenate to the exact delivered body, bound together in one `/deliver` call), then delivers them via `POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/deliver` (1 delivery/agent/2s default, plus 512 KiB bodies with shipped `ExtraSubmits=2, 300ms`), mixed with concurrent broker RPC churn and a stalled bulk TCP tunnel.

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
  --broker-rpc-latency=25ms \
  --clock-skew=0ms \
  --hub-rss-pids=1234,5678 \
  --memory-budget-mb=1024 \
  --drain-after=15s \
  --drain-cmd='docker kill -s INT hub-b' \
  --expect-drain \
  --message-ids-file=/tmp/c19-deliver-msg-ids.txt \
  --output=/tmp/c19-deliver.json
```

## Measurement Semantics & Claim Boundaries

- **What "control p99" (`control_p99_added`) measures per mode:**
  - `phase2`: `GET /api/v1/agents/{id}/pty` preflight (non-upgraded HTTP GET). Covers Hub authz and path resolution without a conduit hop.
  - `broker-rpc` and `deliver`: `GET /api/v1/agents/{id}/logs?tail=1` (routed over the broker's Conduit session, sharing the churn path). Under-load probes run at a fixed configurable rate (`--control-probe-rate`, default `4.0` probes/sec).
- **Percentiles:** All p50/p99 values use nearest-rank (`idx = ceil(p*n) - 1`), so p99 equals the maximum for `n <= 100` and never drops worst-case tail samples.
- **512 KiB paste bar (`deliver_paste_p99`):** Evaluated strictly on 512 KiB deliveries (`deliver_paste_512k`) and fails if fewer than `--large-body-count` samples are recorded. Never falls back to small-body pastes.
- **Broker-session 429 detection (`http_429_count`):** Counts both direct HTTP 429 responses and broker-session 429 refusals surfaced by Hub `/logs` as HTTP 502 containing `runtime broker returned error 429`.
- **Rotate-token claim boundary (`rotate_token_mix`):** The Hub currently exposes no HTTP endpoint invoking `Server.RequestAgentTokenRotation`. When `--rotate-count=0` (default), `deliver` mode emits `rotate_token_mix` as unevaluated (`not exercised: no Hub rotation route`), yielding an `INCOMPLETE` verdict unless explicitly waived with `--allow-unevaluated=rotate_token_mix` (which is echoed in the JSON report) or exercised via `--rotate-url-template`.
- **Hub RSS (`hub_rss_peak`):** Requires `--memory-budget-mb` and either `--hub-rss-pids` (reading `/proc/<pid>/status` `VmRSS`; every PID must be readable on every sample) or `--hub-rss-cmd` (printing aggregate RSS bytes). Never falls back to the driver's own RSS.
- **Planned drain (`planned_drain_4503`):** Requires at least one PTY WebSocket close with code `4503` and reason `relay_restart`; other `4503` close reasons are reported separately under `drain.other_4503`.
- **Postgres auditability:** Each run emits a unique `run_tag`, `counts.message_rows_created`, and optional `--message-ids-file` so operators can verify 100% of created rows reached terminal `delivered` state in Postgres.

## Output & Exit Code

- Prints a human-readable bar table with verdict `PASS`, `FAIL`, or `INCOMPLETE` to stdout (or JSON with `--json`).
- Unevaluated bars always carry `"evaluated": false, "pass": false` in JSON. Any unevaluated required bar (not listed in `--allow-unevaluated`) produces verdict `INCOMPLETE` and exits `1`.
- Writes the full machine-readable JSON report to `--output` when specified.
- Exits `0` only on verdict `PASS`, `1` on `FAIL` / `INCOMPLETE` / runtime error, `2` on invalid or missing required flags.

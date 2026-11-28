# `hack/conduit-c19` — Criterion 19 Conduit Live Load Driver

Non-shipped validation tooling for Conduit Criterion 19 (v2.17 / v2.22 §6.2). Never compiled into `./cmd/scion` or `./cmd/sciontool`.

## Build

```bash
go build -buildvcs=false -o /tmp/conduit-c19 ./hack/conduit-c19
```

## Agent & Broker Provisioning

Before running `conduit-c19` against a live HA Hub:

1. **Broker record:** Register a dedicated runtime broker (`--broker-id` / `C19_BROKER_ID`) with an HMAC secret (`C19_BROKER_SECRET_B64`), and set its capability `attach = false` so PTY attaches route over each agent's Conduit session (required for the agent-session control probe and Phase 2 PTY workers). Do **not** connect a second broker process with the same `--broker-id` while `conduit-c19` is running — `conduit-c19` connects both the Conduit session and the control channel for `--broker-id`.
2. **Pre-seeded agents (`--agents-file`):** Seed N agents (e.g. N=50) assigned to `--broker-id`, in `running` phase with a non-empty `run_id`, and expose port `8080` (`127.0.0.1`, `rw`) on `agents[0]` for the stalled bulk TCP tunnel. Mint an agent JWT for each and write a JSON array:

```json
[
  {
    "agent_id": "agent-uuid-0",
    "slug": "c19-agent-00",
    "project_id": "project-uuid",
    "launch_id": "run-uuid-0",
    "token": "<agent-jwt>",
    "hub_url": "https://hub-a.internal:8443"
  }
]
```

If `hub_url` is omitted per entry, agents use `--hub-url` (or round-robin across `--hub-urls`). If `slug` is omitted, the driver resolves it at startup via `GET /api/v1/agents/{id}`.

3. **User credentials:** Provide a user bearer JWT via `C19_USER_TOKEN` (preferred over `--user-token`). When `--stalled-tunnel` is enabled and `--user-id` / `C19_USER_ID` is omitted, the driver resolves the authenticated user principal ID from `GET /api/v1/auth/me`.

## Modes

### 1. Phase 2 traffic (`--mode=phase2`)
Interactive PTY echo/keepalive sessions + stalled bulk TCP tunnel on agent 0 + baseline vs under-load agent-session control probes + planned drain `4503` (`relay_restart`) observation.

```bash
C19_USER_TOKEN="<user-jwt>" \
/tmp/conduit-c19 \
  --mode=phase2 \
  --hub-url=https://lb.internal \
  --agents-file=/tmp/agents.json \
  --duration=30s \
  --pty-count=8 \
  --hub-rss-pids=1234,5678 \
  --drained-hub-pid=5678 \
  --memory-budget-mb=1024 \
  --drain-after=15s \
  --drain-cmd='docker kill -s INT hub-b' \
  --drain-deadline=15s \
  --expect-drain \
  --allow-unevaluated=planned_drain_refuses_new_streams \
  --output=/tmp/c19-phase2.json
```

### 2. Broker RPC traffic (`--mode=broker-rpc`)
High-concurrency hub-to-broker RPC churn (`GET /api/v1/agents/{id}/logs?tail=1` routed over the broker's Conduit session) with client concurrency `--rpc-concurrency=128` (default) and optional `--rpc-pacing` for smooth pacing across the window. Pass `--broker-rpc-latency` to hold RPCs inside the fake broker handler so in-flight RPCs overlap on the broker session (reported separately as `counts.max_concurrent_broker_session_rpcs` and evaluated against `conduit.DefaultMaxConcurrentRPCs = 128`).

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
  --broker-rpc-latency=300ms \
  --hub-rss-pids=1234,5678 \
  --drained-hub-pid=5678 \
  --memory-budget-mb=1024 \
  --drain-after=15s \
  --drain-cmd='docker kill -s INT hub-b' \
  --drain-deadline=15s \
  --expect-drain \
  --allow-unevaluated=planned_drain_refuses_new_streams \
  --output=/tmp/c19-broker-rpc.json
```

### 3. `deliver-message` traffic (`--mode=deliver`, v2.22 §6.2)
Exercises the full product delivery path:
- The CLI posts real user messages via `POST /api/v1/agents/{id}/message`. For each 512 KiB delivery, it posts **33 × 15,000-byte rows** (495,000 raw bytes, each tagged with `run_tag`); combined with the Hub's per-row `RenderDeliveryText` envelope, the coalesced `/deliver` payload lands around **507–520 KiB** (classified when `>= 496 KiB`; actual min/max bytes are recorded in `latencies.deliver_paste_512k_min_bytes` / `max_bytes` and in `deliver_paste_p99.observed`). Large bodies are scheduled at absolute offsets `loadStart + i * Duration/(count+1)` so delivery latency does not overrun the window. **Prerequisite:** `messaging.offload_threshold_runes` must be disabled on the Hub so rows are not offloaded to stubs.
- The Hub persists real Postgres rows and dispatches them over the broker's **control channel** (`/api/v1/runtime-brokers/connect`).
- The CLI's fake broker receives the control-channel dispatches, coalesces them through a mirrored v2.22 debounced flusher (`"\n\n"` join, 512 KiB / 64-id sub-flush splitting, per-agent lock, 3-attempt pre-paste retry including pre-send connection errors), and calls `POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/deliver` itself (with shipped `ExtraSubmits=2, 300ms`). The CLI never calls `/deliver` directly.
- `--message-ids-file` is **mandatory** in `deliver` mode. Pass `C19_PG_DSN` (or `--pg-dsn`) so `pg_rows_delivered` queries Postgres directly, or verify externally with `psql`:

```sql
SELECT dispatch_state, COUNT(*)
FROM messages
WHERE id = ANY(string_to_array(trim(pg_read_file('/tmp/c19-deliver-msg-ids.txt')), E'\n'))
GROUP BY dispatch_state;
```

```bash
C19_USER_TOKEN="<user-jwt>" \
C19_BROKER_ID="<broker-id>" \
C19_BROKER_SECRET_B64="<broker-hmac-secret-b64>" \
C19_PG_DSN="postgres://postgres@pg.internal:5432/scion?sslmode=disable" \
/tmp/conduit-c19 \
  --mode=deliver \
  --hub-url=https://lb.internal \
  --agents-file=/tmp/agents.json \
  --duration=45s \
  --deliver-interval=2s \
  --large-body-count=10 \
  --rpc-concurrency=128 \
  --broker-rpc-latency=300ms \
  --clock-skew=0ms \
  --hub-rss-pids=1234,5678 \
  --drained-hub-pid=5678 \
  --memory-budget-mb=1024 \
  --drain-after=20s \
  --drain-cmd='docker kill -s INT hub-b' \
  --drain-deadline=15s \
  --expect-drain \
  --allow-unevaluated=rotate_token_mix,planned_drain_refuses_new_streams \
  --message-ids-file=/tmp/c19-deliver-msg-ids.txt \
  --output=/tmp/c19-deliver.json
```

## Measurement Semantics & Claim Boundaries

See [BARS.md](BARS.md) for the complete per-bar audit table (every bar across all modes, numerator/denominator, gating error counters, minimum sample and offered-load gates, waivability, and exact `PASS`/`FAIL`/`INCOMPLETE` conditions).

- **What "control p99" (`control_p99_added`) & `session_stall` measure (all modes):**
  - A live PTY WebSocket open + single echo + clean close on `agents[0]` (which shares its Conduit session with the stalled bulk TCP tunnel and is completely off the broker `/logs` churn path).
  - Baseline runs 2 discarded warmup probes followed by `--baseline-samples` (default 30) recorded probes before opening the stalled tunnel or load loops; under-load probes run at a fixed configurable rate (`--control-probe-rate`, default `4.0`/s).
  - Any baseline or non-drain under-load control-probe or PTY echo failure fails both `session_stall` and `control_p99_added` (drain-window failures in `[trigger, trigger + drain_deadline]` are excused and reported separately).
- **Broker flush deviation (`broker_flush_mode`, `broker_debounce_ms`):**
  - The fake broker mirrors `pkg/agent/msgbuffer.go`'s v2.22 flush logic (`"\n\n"` join, 512 KiB / 64-id sub-flush splitting, per-agent lock, 3-attempt / 500ms pre-paste retry including pre-send connection errors). The one deviation from production's fixed 2 s debounce is a configurable shorter `--broker-debounce` (default `500ms`) so C19 runs drain cleanly within the window; both `broker_flush_mode` and `broker_debounce_ms` are echoed in the JSON report.
- **Percentiles:** Nearest-rank (`idx = ceil(p*n) - 1`), so p99 equals the maximum for `n <= 100`.
- **512 KiB paste bar (`deliver_paste_p99`):** Evaluated strictly on 512 KiB deliveries (`deliver_paste_512k`, 33 × 15,000-byte rows + Hub `RenderDeliveryText` envelopes ≈ 507–520 KiB, offload disabled) and fails if fewer than `--large-body-count` samples are recorded or if observed payload bytes are below 496 KiB. Measures the fake-agent `RecordingTmuxRunner` handler time (including extra submits, not real `tmux`), disclosed in the JSON bar `note`.
- **Offered load (`offered_load`) & broker-session concurrency (`broker_session_concurrency`):**
  - `offered_load` requires `deliver_periodic_attempts >= 90%` of `agents * duration / deliver_interval` (excludes large-body injections; catches closed-loop slowdown / dropped ticks).
  - `broker_session_concurrency` is always evaluated against `conduit.DefaultMaxConcurrentRPCs` (`>= 116`, i.e., `90%` of `128`) so low `--rpc-concurrency` or `--broker-rpc-latency=0` cannot vacuously pass.
  - `http_429_count` counts direct HTTP 429s from `/message`, every `/deliver` attempt (including retried attempts inside the broker flusher, without double-counting), control probes, and broker-session 429s surfaced by Hub `/logs` as HTTP 502. A non-default `--broker-max-concurrent-rpcs` override marks `http_429_count` `INCOMPLETE` (when 429s == 0) with `non_default_max_concurrent_rpcs` disclosed.
- **Postgres row verification (`pg_rows_delivered`) vs `deliver_errors`:**
  - `deliver_errors` verifies broker-observed `/deliver` HTTP outcomes only.
  - `pg_rows_delivered` verifies 100% of recorded message IDs in `--message-ids-file` (mandatory in `deliver` mode) reached `dispatch_state = 'delivered'` in Postgres when `--pg-dsn` (`C19_PG_DSN`) is supplied; without `--pg-dsn`, `pg_rows_delivered` is `INCOMPLETE` unless waived via `--allow-unevaluated=pg_rows_delivered`.
- **Rotate-token claim boundary (`rotate_token_mix`):** The Hub exposes no HTTP endpoint invoking `Server.RequestAgentTokenRotation`. `deliver` mode always emits `rotate_token_mix` as unevaluated (`not exercised: no Hub rotation route`), yielding `INCOMPLETE` unless waived with `--allow-unevaluated=rotate_token_mix` (echoed in the JSON report).
- **Hub RSS (`hub_rss_peak`):** Requires `--memory-budget-mb` and either `--hub-rss-pids` or `--hub-rss-cmd`. Never falls back to driver RSS. When `--drain-cmd` is set with `>= 2` PIDs, `--drained-hub-pid` is required; only that explicitly designated PID disappearing (`ENOENT`) after drain trigger is excused and recorded (`exited_after_drain_pid`), while a surviving hub crash fails the bar.
- **Planned drain (`planned_drain_4503` + `planned_drain_refuses_new_streams`):**
  - `planned_drain_4503` evaluates: (1) `>= 1` PTY close with code `4503` and reason `relay_restart`, (2) trigger-to-4503 close timing inside `[--drain-deadline - tolerance, --drain-deadline + tolerance]` (defeating immediate-kill at SIGINT), (3) bounded pre-trigger accepted PTY stream completes OK during the drain window, and (4) `>= 1` agent session re-admitted after drain trigger carrying `>= 1` replacement operation on a re-admitted session or re-dialed PTY stream.
  - `planned_drain_refuses_new_streams` is split out as a separately named bar (always `INCOMPLETE` unless waived via `--allow-unevaluated=planned_drain_refuses_new_streams`; verify via direct-node L2/L3 drain check).
- **Credential safety:** `--user-token`, `--broker-secret`, `--broker-secret-b64`, and `--pg-dsn` register empty defaults and read from environment variables only after flag parsing succeeds, so `-h` and flag parse errors never print secrets.
- **Postgres auditability:** Emits `run_tag` (included on both regular and 512 KiB rows), `counts.message_rows_created`, and mandatory `--message-ids-file` in `deliver` mode (recording each created row ID immediately even on partial failures).

## Output & Exit Code

- Prints a human-readable bar table with verdict `PASS`, `FAIL`, or `INCOMPLETE` to stdout (or JSON with `--json`).
- Unevaluated bars always carry `"evaluated": false, "pass": false`. Any unwaived unevaluated required bar produces verdict `INCOMPLETE` and exits `1`.
- Exits `0` only on verdict `PASS`, `1` on `FAIL` / `INCOMPLETE` / runtime error, `2` on invalid or missing flags.

# Sandbox Observability Investigation

**Agent**: sn-sandboxobs-inv
**Date**: 2026-08-28
**Task**: Investigate sandbox observability gap — agent dies on Cloud Run tier with no visible diagnostics

## Context

An agent died on the hosted Cloud Run tier and all diagnostic output was trapped inside the dead sandbox. This investigation documents the identity/telemetry mechanisms and evaluates approaches to get sandbox logs into Cloud Logging.

## Key Findings

### 1. "JSON file injection" mechanism (Q1 Half B)
The mechanism ptone described is NOT a volume mount. It is env-var-to-file staging via `SCION_STAGED_SECRETS`:
- `docker.go:52-56` → `serializeSecrets()` encodes secrets as base64(JSON) into env var
- `stagedsecrets.go:48-97` → sciontool init decodes and writes file secrets to target paths with 0600 perms
- Telemetry credential lands at `~/.scion/telemetry-gcp-credentials.json`
- Dedicated SA `scion-telemetry-writer` with `roles/logging.logWriter` (gce-demo-telemetry-sa.sh:51)

### 2. ADC Fall-through (PROVEN)
When `GCPCredentialsFile == ""`, GCP exporter clients fall through to ADC:
- Guard: `gcp_exporter.go:47-50` — opts is empty when no cred file
- Scopes: `exporter.go:62-66` — trace.append, logging.write, monitoring.write
- Minimum env vars: `SCION_TELEMETRY_CLOUD_PROVIDER=gcp` + `SCION_GCP_PROJECT_ID` (config.go:42,51)
- GCP exporter statically linked (go.mod confirms dependencies)

### 3. ADC Path is DEAD for sandbox observability
- **Block mode** (the common case): metadata emulator returns 403 for all token requests (server.go:820-824). ADC fails.
- **Assign mode**: agent's SA has no `logging.logWriter` — neither deploy script grants it (cloudrun/deploy.sh:222 gives hub SA logging.VIEWER only)
- **Conclusion**: ADC telemetry works for nobody today and never works for the block-mode agent that dies without credentials

### 4. Host-side stdout tailer is the viable approach
- **Premise verified**: `otel.go:102-108` explicitly confirms "stdout is automatically captured and forwarded to Cloud Logging by the runtime" on Cloud Run (K_SERVICE set)
- **Attachment point**: `cloudrun_sandbox_runtime.go:867` — alongside watchSandbox goroutine
- **File**: `.scion-entrypoint.log` (sciontool init output), same file DOA handler reads at line 818
- **Requires**: no SA, no token, no IAM role, no credential in sandbox

### 5. Passthrough mode — REJECTED
- Would expose hub SA (run.admin, secretmanager.secretAccessor, etc.) — complete lateral takeover
- Authorization: two independent gates in passthrough_gate.go (broker-owner/admin + actAs)
- On gVisor tier: doubly blocked — no emulator, no GCE_METADATA_HOST, 169.254.169.254 unreachable
- Cannot be set by accident; can be set by authorized operator by choice

### 6. iptables/gVisor reachability
- No NET_ADMIN capability (no --cap-add in sandbox args, lines 755-761)
- Runs as root (UID 0) but iptables binary absent in gVisor
- 169.254.169.254 unreachable from gVisor netstack (line 724-726: "will fail outright")
- Defense-in-depth iptables failure is irrelevant — primary protection sufficient

### 7. Credential persistence collides with #108 (Q4)
- Staged secret writes credential file to agent home (stagedsecrets.go:78-97)
- Agent home bind mount survives sandbox deletion
- Next agent with same slug inherits credential — confirmed leak
- Host-side tailer eliminates this entirely (no credential in sandbox)

## Design Output: Host-Side Entrypoint Log Tailer (v3, accepted)

The investigation concluded with a design for a host-side entrypoint log tailer. Design v3 accepted by architect. Full design at `.scratch/revised-design.md`.

Key design decisions:

- **Single file**: tail `.scion-entrypoint.log` only (not `agent.log`). The stderr copy from `log.go:136-137` is unconditional for the `sciontool init` process (quiet mode only applies to hook/status subcommands per `root.go:37-39`).
- **Stat-before-launch offset**: `os.Stat` the file after `prepareScionLayout` returns (line 741) but before sandbox creation (line 778). Seek to this offset to skip prior-run output. No `os.Remove` — file preserved for forensics per PR 1323.
- **Truncation detection**: On open and each read cycle, compare file size to offset. If file < offset, reset to 0, emit WARNING, read from start. Prevents silent failure if PR 1323 (append mode) is reverted.
- **Partial final line flush**: On any terminal exit (context cancellation, ENOENT, error), flush the line buffer with `partial:true` label. The crash message is never swallowed. Buffer capped at 64KB.
- **File-never-appears detection**: Retry open on bounded schedule (250ms→4s). If watchCtx cancelled with file never opened, emit ERROR. This distinguishes "died before it could tell us anything" from "started fine".
- **Backoff**: 250ms during init → 1s → 5s steady state. Resets on new bytes.
- **Scope limit**: This design makes STARTUP failures visible only. Runtime failures (hook crashes, harness failures) are not captured. Phase 2 (agent.log tailing) noted but not designed.
- **PR 1323 dependency**: Performance, not correctness. With 1323: clean per-run separation. Without: truncation detector fires, warns, still ships output.

Guesses for implementer to validate: 64KB buffer cap (estimated, not profiled), 10/10 backoff step counts (estimated from init burst timing), 8s retry window (generous bound, may be shortened after instrumentation).

### 8. Operator Log Viewer — Route Analysis

**Question**: Does any operator-facing route surface `.scion-entrypoint.log`?

**Answer**: NO. No endpoint on any runtime serves `.scion-entrypoint.log`.

- `/api/v1/agents/{id}/logs` → runtimebroker/handlers.go:1904-1937 tries agent.log from filesystem, but `if found.ProjectPath != ""` at line 1924 is always false on sandbox tier (cloudrun List() at line 921-984 does NOT set ProjectPath, unlike docker.go:222). Falls back to `rt.GetLogs()` → tmux capture-pane → requires LIVE sandbox.
- `/api/v1/agents/{id}/cloud-logs` → logquery.go:126-159, pure Cloud Logging API query via logadmin.Client. No sandbox dependency. Works for dead agents.
- The web UI log viewer (agent-log-viewer.ts:262) picks cloud-logs mode when `cloudLogging=true`, broker mode otherwise.

### 9. Cloud-Logs Filter vs PR 1325 Tailer

**Question**: Would PR 1325's tailer lines (written to host stdout, captured by Cloud Run to Cloud Logging) appear in the Logs tab?

**Answer**: NO. Two predicates block them.

The filter built by BuildLogFilter (logquery.go:164-240) for /cloud-logs:

1. **logName whitelist** (line 175-177): Only `scion-server`, `scion-agents`, `scion-messages`. Tailer stdout arrives as `run.googleapis.com/stdout`. FAILS.
2. **labels.hub** (line 204-206): Always added because ResolveHubName() (hub_config.go:164-173) always returns non-empty. Tailer emits no `hub` label. FAILS.
3. labels.agent_id (line 187): Tailer emits this. PASSES.
4. labels.project_id (line 190): Tailer emits this. PASSES.

**Label promotion assumption**: The tailer writes `logging.googleapis.com/labels` to stdout JSON, expecting Cloud Run to promote those labels to Cloud Logging entry labels. This is INFERENCE with documentation support (Cloud Run docs reference special fields list which includes this key; Java example shows it), but no single authoritative statement confirms it for Cloud Run services. Open risk.

**Filter semantics**: Missing labels cause comparisons to fail silently (Cloud Logging query language docs). Adding stdout logName to whitelist is safe — unlabeled entries are rejected by label predicates.

### 10. CloudHandler vs GCPHandler — Which Is Active?

**Decision code**: server_foreground.go:740-773, initServerLogging().

**CloudHandler requires**: (1) cloud_logging enabled via SCION_CLOUD_LOGGING env var or settings, (2) GCP project ID via SCION_GCP_PROJECT_ID or GOOGLE_CLOUD_PROJECT env var (logging resolveProjectID() at cloud_handler.go:381-388 reads ONLY env vars, does NOT query metadata server), (3) gcplog.NewClient success (needs ADC).

**Deploy script provides**: NEITHER the cloud_logging flag NOR the project ID env var (deploy.sh:166). Cloud Run does NOT auto-set GOOGLE_CLOUD_PROJECT. Embedded defaults do not enable cloud_logging.

**Critical gap**: logging.resolveProjectID() differs from hub.ResolveGCPProjectID() — the latter (gcp_iam_admin.go:178-187) falls back to metadata.ProjectIDWithContext, the former does not.

**IAM gap**: Hub SA has roles/logging.viewer (deploy.sh:222), not roles/logging.logWriter. Even if CloudHandler is constructed, writes fail, circuit breaker opens permanently (resilient_cloud_handler.go:185-194), and on Cloud Run GCPHandler was already suppressed (otel.go:108) — complete logging black hole.

**Same gap kills logQueryService**: server.go:1460 uses logging.ResolveProjectID() — same env-var-only resolver. If project ID env var is unset, logQueryService is nil, Logs tab uses broker mode.

**If CloudHandler active**: Tailer could emit through it (client exposed at resilient_cloud_handler.go:220, can create logger with logID=scion-agents). Labels set directly on Entry — no promotion needed, Q2 risk eliminated. Door is open.

**Five-row summary**: Every combination of project ID / cloud_logging / IAM produces an empty Logs tab for dead sandbox agents EXCEPT the one where all three are provided — which deploy.sh does not do.

## Files Referenced
- `pkg/runtime/docker.go:51-67` — secret injection entry point
- `pkg/runtime/common.go:42-56,861-929` — telemetry credential and secret serialization
- `pkg/runtime/cloudrun_sandbox_runtime.go:724-726,755-761,818-826,867` — gVisor, sandbox args, DOA handler, watcher
- `pkg/sciontool/telemetry/gcp_exporter.go:47-50` — ADC guard
- `pkg/sciontool/telemetry/config.go:42-214` — telemetry config
- `pkg/sciontool/telemetry/pipeline.go:118-125` — ADC source logging
- `pkg/sciontool/metadata/server.go:820-824,871-873` — block mode 403
- `pkg/util/logging/otel.go:102-111` — Cloud Run stdout capture confirmation
- `pkg/hub/passthrough_gate.go:64-214` — passthrough authorization
- `pkg/hub/handlers_agents_core.go:732-741,2184-2206` — passthrough API paths
- `pkg/runtimebroker/start_context.go:391-419` — metadata mode env var injection
- `scripts/cloudrun/deploy.sh:216-240` — IAM role grants
- `scripts/starter-hub/gce-demo-telemetry-sa.sh:37-157` — telemetry SA setup

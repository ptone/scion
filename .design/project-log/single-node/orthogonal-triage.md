# Orthogonal defect triage and footprint reduction plan

Date: 2026-08-26. Author: sn-impl-arch.
Language: ASD-STE100 Simplified Technical English.
Instruction: ptone, 20:29 — file the general defects as issues, let other teams fix them,
then rebase our work on the fixes and reduce our footprint.

## Numbering

- `issue 1273`, `PR 1266` — real numbers in `ptone/scion`.
- `D-37`, `D-49` — our internal defect numbers. Task IDs. Not GitHub numbers.

---

# 1. Summary

- Four issues are filed: **1273**, **1274**, **1275**, **1276**.
- The triage set is now closed. Of the five that needed diagnosis: **D-41 is filed** (1276),
  **two are resolved to "do not file"** (D-46, D-39 — see §4), D-35 needs a live reproduction,
  and D-15 has no mechanism.
- **Five defects are ours and stay ours** — D-32, D-44, D-47, and now D-46 and D-39.
- Two would have been bad upstream reports. Diagnosing before filing was worth the hour.
- The largest footprint win is **not** the issues. It is 23 log files and one duplicated
  security fix. Together they remove 28 of the 63 files.

# 2. What was filed

| Issue | Defect | Owner it belongs to | Why it is general |
|---|---|---|---|
| [1273](https://github.com/ptone/scion/issues/1273) | D-37 / D-48 | hub + broker | The hub drops template and harness-config identity on agent create, and the broker falls back to a disk search that is empty in hosted mode. Affects any hosted hub, not any one runtime. |
| [1274](https://github.com/ptone/scion/issues/1274) | D-49 | provisioning | `GitCloneConfig.Depth` is documented as `0 = full clone` and implemented as depth 1. The same defaulting appears in three independent paths: `pkg/provision/provision.go:308`, `pkg/runtime/k8s_runtime.go:2474`, `cmd/sciontool/commands/init.go:1592`. |
| [1275](https://github.com/ptone/scion/issues/1275) | D-42 | agent create | `noAuth:true` makes a request fail that succeeds without it. Pure hub API behaviour. Reproduced, not root-caused; filed as such. |
| [1276](https://github.com/ptone/scion/issues/1276) | D-41 | broker auth preflight | `pkg/runtimebroker/handlers.go:2178` counts only `metadata_mode: assign` as an assigned GCP identity. `passthrough` — the real metadata server, that is, ambient ADC — is not counted, so `skipped_when_gcp_service_account_assigned` never fires and `gcloud-adc` stays required. The preflight runs before runtime resolution and has no runtime branches, so this hits GCE, GKE and Cloud Run alike. |

# 3. What needed diagnosis — now closed

Do not file a defect without a mechanism. It wastes the owner's time and ours. Four of the five
are now resolved. One remains open, and it is open for a reason that no amount of reading fixes.

| Defect | What is missing |
|---|---|
| D-35 | The hub rejects sandbox session metrics with HTTP 400. **Narrowed by reading at 21:55 — see §3.1. Six possible causes, one hypothesis eliminated, and the exact log string to grep is now known.** Still not filable: we need the body. **Checked 21:10: the evidence window has closed.** Cloud Logging on `sn-step6` and `sn-walk` returns nothing for `metrics`/`400` at 12h, and nothing across all Instances at 3d. The instances are alive and logging (verified — `sn-step6` is emitting scheduler lines as of 20:59), so the query is sound; the original 400 has simply aged out. **A live reproduction is now the only route**: start an agent on `sn-step6`, let it exit naturally, and capture the hub's 400 body. Until then this cannot be filed honestly. |
| D-41 | **RESOLVED 21:40 — filed as [1276](https://github.com/ptone/scion/issues/1276).** The preflight call site is `extractRequiredEnvKeys`, `pkg/runtimebroker/handlers.go:2041`, called from `:527`. The defect is one line at `:2178`. Verified by reading, not by report: `handlers.go:2177-2179`, `pkg/harness/auth.go:413`, `pkg/hub/handlers_agents_core.go:1300-1307`, `start_context.go:388-392`. Five harness configs set the flag. A second, smaller defect travels with it — the doc comment on `projectHasVerifiedGCPSA` (`pkg/hub/handlers_agent_create_helpers.go:1344-1346`) claims a verified SA record means the metadata server can provide ADC. It does not. |
| D-46 | **RESOLVED 21:05 — do not file. See §4.** |
| D-39 | **RESOLVED 21:05 — do not file. See §4.** |
| D-15 | The daemonize mechanism is still unknown. Nothing to file. |

## 3.1 D-35 — what reading the code established

The ingest path is `POST /api/v1/agents/{id}/metrics` (client builds the URL at
`pkg/sciontool/hub/metrics.go:75`) into `handleAgentMetrics`
(`pkg/hub/handlers_agent_metrics.go:63`). Do not confuse it with
`/api/v1/metrics/session/{id}`, which is the **read** path and is a GET.

`handleAgentMetrics` can return 400 in exactly six places, and no others:

| Line | Cause |
|---|---|
| :81 | `Invalid request body: ...` — the JSON did not decode |
| :87 | `session.id is required` |
| :91 | `session.started_at is required` |
| :98 | `session.started_at must be RFC3339` |
| :105 | `session.ended_at must be RFC3339` |
| :109 | `session.ended_at cannot be before session.started_at` |

`BadRequest` and `ValidationError` both write 400 (`pkg/hub/errors.go:183`, `:188`).

**One hypothesis raised and eliminated.** `SummaryToMetricsPayload`
(`pkg/sciontool/hub/metrics.go:156`) formats `EndedAt` **unconditionally**. A zero `time.Time`
formats to `"0001-01-01T00:00:00Z"`, which is non-empty — so `omitempty` does not drop it — and
parses as valid RFC3339, so it would reach the `:109` comparison and fail as "before started_at".
Measured directly: zero time formats to that string, parses clean, and `Before(now)` is true.
**But this cannot happen.** The only constructor of `SessionSummary`
(`pkg/sciontool/telemetry/aggregator.go:171-176`) always sets `EndedAt: time.Now()`. There is no
second constructor. Eliminated by reading, not by guessing.

By the same argument `started_at` cannot be empty or malformed either — it is always formatted from
a `time.Time`. So `:91` and `:98` are also out, and `:105` with it.

**That leaves two live candidates: `:81` and `:87`.** `:87` is the stronger one. `session.id` comes
from `a.sessionID`, and nothing guards it before the send — the `OnSessionEnd` closure at
`cmd/sciontool/commands/init.go:436-440` passes the summary straight through. If the sandbox agent
never registered a session start, `sessionID` is `""` and the hub answers
`session.id is required`. That is consistent with what we already know about this tier: agent
lifecycle detection in the sandbox was broken for two independent reasons (task #31).

**The body is not lost.** `ReportMetrics` puts the response body into the error —
`fmt.Errorf("hub returned error %d: %s", resp.StatusCode, string(respBody))` at
`pkg/sciontool/hub/metrics.go:124` — and `init.go:440` logs it as
`Failed to report session metrics to hub: ...`. So this is **not** a diagnosability defect, and
filing it as one would have been wrong. The answer was in the log all along; we simply did not
keep it.

**Sharpened reproduction recipe.** Start an agent on `sn-step6`, let it exit **naturally**, and
grep the sandbox log for the literal string `Failed to report session metrics to hub`. The 400
body is on that line and will name one of the two remaining causes.

**Residual risk on the repro:** that log line is written by `sciontool init` inside the sandbox, so
it goes to sandbox stderr. Our tier is known to lose sandbox stderr (task #46). If the repro comes
back empty, that is not a dead end — it is confirmation of D-46 and should be recorded as such.

# 4. What stays ours

| Defect | Why it is not orthogonal |
|---|---|
| D-32 | `relocateToScion` lives in `pkg/runtime/cloudrun_sandbox_runtime.go`. It is our code. We fix it. **This corrects an earlier classification.** |
| D-44 | Downstream of D-45, which is fixed upstream (PR 1300). Not a new defect. It needs a re-test, not an issue. |
| D-47 | Rewriting our own PR description. Process, not a defect. |
| D-46 | **Reclassified 21:05.** The general path is already well instrumented. `cmd/sciontool/commands/init.go:286-300` logs the clone failure, writes `PhaseError` and the message into `agent-info.json`, and reports the error to the hub directly via `hubClient.ReportState`, with the broker heartbeat as a fallback. `init.go:1889-1894` classifies the failure and gives guidance, with tokens sanitised out. So the machinery exists and works. If the operator sees nothing on our tier, our sandbox path is losing it — the sandbox dies before the report completes, or the hub client is not configured, or stderr is not captured. **Ours. Filing this upstream would have been a bad report.** |
| D-39 | **Reclassified 21:05.** Not a Scion defect at all. Our own `cloudrun_sandbox_runtime.go` contains no image-pull error handling (only sandbox-not-found at :931 and :988). The general k8s path already produces a reasonable message (`pkg/runtime/k8s_runtime.go:1627`). The ambiguous "not found", the cache-mirror name and the misleading tag advice all come from the Cloud Run **sandbox launcher binary**. Route as platform feedback to the Cloud Run Sandboxes team, not as an issue on `ptone/scion`. |

# 5. Footprint reduction

PR 1266 today: **63 files, +7575 / -59.**

## 5.1 The 23 log files — the biggest single win, and free

23 of the 63 files are internal engineering logs under `.design/project-log/`, totalling 1679
changed lines. They are not review surface. A reviewer must scroll past them to find the code.

**Action:** move them to a separate documentation commit or a separate PR.
**Effect: 63 files → 40 files. No risk. No code change.**

## 5.2 The duplicated security fix — free, but ordering-dependent

PR 1265 (`scion/security-fix-p0-s1`) is the P0-S1 dev-auth fix. PR 1266 contains the **same fix
again**: `IsLoopbackHost` and the `log.Fatalf` guard in `pkg/hub/web.go`, and the matching guard in
`cmd/server_foreground.go`, plus their tests.

| File | Changed lines |
|---|---|
| `pkg/hub/web.go` | 22 |
| `pkg/hub/web_test.go` | 80 |
| `cmd/server_foreground_test.go` | 96 |
| `cmd/server_bridge_test.go` | 89 |
| `cmd/server_foreground.go` (the guard part only) | ~9 |

**Action:** land PR 1265 first, then rebase PR 1266 on it. The duplicate disappears by itself.
**Effect: 40 files → 35 files, about 296 fewer lines. No risk, provided 1265 lands first.**

This is a second, independent reason to keep the ordering in task #51.

## 5.3 Two small drive-by changes

- `web/embed.go` — a 2-line comment fix (`npm run build:client` → `npm run build`). Unrelated to
  this tier. Send it separately.
- `pkg/hub/handlers_health.go` + `web/src/components/pages/diagnostics.ts` — a
  `deploymentWarnings[]` field on the health response and the UI that renders it, 71 lines across
  2 files. The **mechanism** is a general hub feature. Only the Cloud Run warning string is ours.
  Propose the mechanism upstream; keep only the string.

**Effect: 35 files → 32 files.**

## 5.4 Result

| Stage | Files |
|---|---|
| Today | 63 |
| Move the logs out (5.1) | 40 |
| Land 1265 first and rebase (5.2) | 35 |
| Send the drive-bys separately (5.3) | 32 |

**Roughly half, with no change to what the tier does.**

## 5.5 Honest note on what the issues do and do not buy

Fixing issues 1273, 1274 and 1275 upstream removes **almost no lines** from PR 1266. Their value is
different, and larger:

- 1273 removes the need for the two-line `deploy-instance` stopgap, and removes a silent failure
  from every hosted deployment.
- 1274 unblocks §1 step 6 properly, instead of us documenting a limitation.
- 1275 removes a workaround from the tutorial.

They reduce **complexity and risk**. Sections 5.1 to 5.3 reduce **size**. Both were asked for; they
are not the same lever, and it is worth not confusing them.

# 6. What is left after the reduction — the real review surface

This is the tier, and it should not shrink further. Roughly 32 files:

| Area | Files | Lines |
|---|---|---|
| Cloud Run Sandbox runtime + tests | 2 | 2392 |
| Sandbox delete workaround + tests | 2 | 580 |
| `deploy-instance` command + tests | 2 | 1243 |
| Metadata emulator bind + link-local discovery + tests | 2 | 316 |
| Broker: PTY exec path for sandboxes | 1 | 189 |
| Broker: hub endpoint for sandboxes + tests | 2 | 110 |
| Broker: start context + tests | 2 | 189 |
| Hub: ephemeral-workspace policy + tests | 2 | 95 |
| Runtime registration, settings, schema | 4 | ~45 |
| Image build (omni) and CI workflows | ~12 | ~320 |
| CLI wiring (`root.go`, `cli_mode.go`), scripts | 3 | 7 |

# 7. Open question for the reviewer of this plan

`pkg/sciontool/metadata/DiscoverLinkLocalAddress` is arguably general — any deployment that needs a
metadata emulator reachable from a peer namespace would want it. It is 148 lines including a
security guard that refuses to bind `0.0.0.0`. I have left it in our set because nothing else uses
it today. If another runtime wants it, it should move.

# 8. Acceptance criteria

1. Issues 1273, 1274 and 1275 are triaged by their owners.
2. D-35, D-41, D-46 and D-39 are each either filed with a mechanism or written off.
3. `.design/project-log/` is out of the tier PR.
4. PR 1265 is landed and PR 1266 is rebased on it, with the duplicate security fix gone.
5. `git diff --stat` on the tier PR shows about 32 files.
6. The tier still passes §1 end to end after the rebase. **This is the one that matters** — a
   smaller diff that no longer deploys is not progress.

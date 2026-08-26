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

- Three issues are filed: **1273**, **1274**, **1275**.
- Five more defects need diagnosis before they can be filed honestly.
- Three defects are ours and stay ours.
- The largest footprint win is **not** the issues. It is 23 log files and one duplicated
  security fix. Together they remove 28 of the 63 files.

# 2. What was filed

| Issue | Defect | Owner it belongs to | Why it is general |
|---|---|---|---|
| [1273](https://github.com/ptone/scion/issues/1273) | D-37 / D-48 | hub + broker | The hub drops template and harness-config identity on agent create, and the broker falls back to a disk search that is empty in hosted mode. Affects any hosted hub, not any one runtime. |
| [1274](https://github.com/ptone/scion/issues/1274) | D-49 | provisioning | `GitCloneConfig.Depth` is documented as `0 = full clone` and implemented as depth 1. The same defaulting appears in three independent paths: `pkg/provision/provision.go:308`, `pkg/runtime/k8s_runtime.go:2474`, `cmd/sciontool/commands/init.go:1592`. |
| [1275](https://github.com/ptone/scion/issues/1275) | D-42 | agent create | `noAuth:true` makes a request fail that succeeds without it. Pure hub API behaviour. Reproduced, not root-caused; filed as such. |

# 3. What needs diagnosis before it can be filed

Do not file these yet. A bug report without a mechanism wastes the owner's time and ours.

| Defect | What is missing |
|---|---|
| D-35 | The hub rejects sandbox session metrics with HTTP 400. We do not have the response body, so we cannot say whether the payload is wrong or the validation is. Get the 400 body first. |
| D-41 | The ambient GCP identity is invisible to the auth preflight. Probably general to every GCP-hosted deployment, including GCE and GKE. We have not identified the preflight call site. |
| D-46 | A git clone failure kills the sandbox with no message. The clone half is general; the "dies silently" half is ours. Split before filing. |
| D-39 | Image-pull failure is undiagnosable. The error-message half is general; the cache-mirror naming is Cloud Run. Split before filing. |
| D-15 | The daemonize mechanism is still unknown. Nothing to file. |

# 4. What stays ours

| Defect | Why it is not orthogonal |
|---|---|
| D-32 | `relocateToScion` lives in `pkg/runtime/cloudrun_sandbox_runtime.go`. It is our code. We fix it. **This corrects an earlier classification.** |
| D-44 | Downstream of D-45, which is fixed upstream (PR 1300). Not a new defect. It needs a re-test, not an issue. |
| D-47 | Rewriting our own PR description. Process, not a defect. |

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

# sn-stress-max — Stress Test Findings (8 CPU / 32 GiB)

## Instance Configuration
- **Instance**: sn-stress-max (Cloud Run Instance, us-east4)
- **Size**: 8 CPU / 32 GiB (empirically verified maximum)
- **Image**: us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni@sha256:e3eab113...
- **sandboxLauncher**: true, IAP enabled
- **Created**: 2026-08-27T06:31:50Z

## Empirical Maximum Instance Size (§5)
Attempted 32 CPU / 128 GiB → rejected: "maximum allowed value of 8 for cpu"
Attempted 8 CPU / 128 GiB → rejected: "maximum allowed value of 32Gi for memory"
**Maximum**: 8 CPU / 32 GiB. Established via rejection messages.

## §3.0 Measurement Instrument
- Cloud Monitoring: DEAD for `cloud_run_instance`. Memory/CPU utilization metrics only work for `cloud_run_revision` (Services). Agent sandboxes are gVisor processes invisible to Cloud Monitoring. Independently confirmed (matches sn-stress-def).
- **Working instrument**: Cloud Logging + broker liveness probes (`sandbox exec <name> -- /bin/true`). Exit_code=0 = alive, exit_code=1 = dead.
- Hub agent list: UNRELIABLE — hub reports agents as "running" even after sandbox is dead. Hub has no reconciliation with broker liveness state.

## Phase A Ladder Results

### Timeline
| Time (UTC) | Event | N (hub) | N (alive, verified) |
|---|---|---|---|
| 06:40:32 | First agent created (stress-test-0) | 1 | 1 |
| 06:53:48 | Ladder starts (idle-1 at ~10s interval) | 4 | 4 |
| 06:55:19 | **idle-1 dies** (exit_code=1) | ~12 | ~11 |
| 06:57:41 | **idle-2 dies** | ~14 | ~12 |
| 07:03:11 | **idle-3 dies** | ~31 | ~28 |
| 07:05:40 | **idle-4 dies** | ~40 | ~36 |
| 07:08:12 | **idle-5 dies** | ~50 | ~45 |
| 07:09:07-27 | **FULL LIVENESS SWEEP**: 51 agents verified alive | 55 | **51** |
| 07:09:28 | idle-55 created | 56 | 51-52 |
| 07:09:44 | idle-56 created (last successful) | 56 | **52** |
| 07:09:59 | idle-57 creation REQUEST sent | - | - |
| 07:10:06 | **CASCADE BEGINS**: idle-45 found dead | 56 | <52 |
| 07:10:06-11:58 | 16 more agents die in ~2 minutes | 56 | dropping |
| 07:11:59 | Broker times out on idle-57 (2m0s) | - | ~8 |
| 07:12:00 | idle-57 DELETED (creation failed) | - | - |
| 07:12:01 | **INSTANCE TERMINATED** by Cloud Run | 0 | 0 |
| 07:12:26 | Instance restarts fresh (all state lost) | 0 | 0 |

### Confirmed Dead Agents (21 of 56)
Pre-cascade (slow trickle, ~2-3min intervals): idle-1, 2, 3, 4, 5
Cascade (rapid, ~6-8s intervals): idle-8, 9, 10, 11, 16, 19, 22, 24, 26, 32, 35, 42, 45, 53, 54, 56

### Agents Still Passing Liveness During Cascade (8)
idle-14, 17, 20, 31, 40, 47, 49, 54 (passed probes between 07:10-07:12)

### Key: No exit_code=137 Anywhere
Searched all logs. ZERO exit_code=137 (SIGKILL/OOM kill). All sandbox deaths are exit_code=1 (process error) or broken pipe signal on zombie runsc processes. The Linux OOM killer is NOT the mechanism.

## Failure Mode Characterization (§3.3)

### The Ceiling: ~51-52 alive sandboxes at 8 CPU / 32 GiB
At 07:09, with 51 exec-verified alive sandboxes, the system was stable. Creating 1-2 more pushed past the threshold and triggered an unrecoverable cascade.

### Failure Mechanism (3 phases):
1. **Slow trickle** (N=12→55): Oldest agents die silently at ~2-3 minute intervals. Hub doesn't notice. This may be FIFO eviction or resource pressure on oldest/lowest-priority sandboxes.
2. **Cascade** (N≈52): Creating one more agent triggers rapid, system-wide sandbox failures. 16+ agents die in ~2 minutes. The broker becomes overwhelmed (2-minute timeout on new agent creation).
3. **Instance termination**: Cloud Run platform terminates the instance with "no available instance" error. The container receives SIGTERM and shuts down gracefully. Instance auto-restarts with fresh state. ALL hub data (in-memory SQLite) is lost. ALL agent sandboxes are destroyed.

### What the restart looks like:
- Cloud Run ERROR: "The request was aborted because there was no available instance"
- Container receives termination signal
- Graceful shutdown (pre-stop hooks, SIGTERM to child, session-end hooks)
- Instance restarts in ~25 seconds with completely clean state
- New hub ID, new broker ID, new signing keys
- No agents, no projects, no state preserved

### Why NOT OOM?
- No exit_code=137 in any log
- No OOM mentions in any log
- Sandbox deaths are exit_code=1 (process error inside sandbox)
- The gVisor runtime may have its own resource limits that kill sandboxes before Linux OOM kicks in
- OR: Cloud Run may enforce memory limits at the container level via cgroups, and the SIGTERM is the enforcement mechanism (rather than SIGKILL/137)

## Per-Agent RSS Estimate
- sn-stress-def measured ~515 MiB per idle agent at 4 CPU / 8 GiB
- At 8 CPU / 32 GiB, 51 alive agents × ~515 MiB ≈ **26.3 GiB** (of 32 GiB capacity)
- This leaves ~5.7 GiB for hub, broker, system overhead
- Adding 1-2 more agents pushed total past the memory ceiling

## Cost
- Instance running since 06:31 UTC, still running
- 8 CPU / 32 GiB × ~2 hours = approximately $0.60-1.20 (Cloud Run Instance pricing)
- Instance should be torn down after retest or when instructed

## Open Questions
1. Should I retest with proper per-step exec verification? The instance is fresh and ready.
2. The early deaths (idle-1 through idle-5 at N=12-50) — are these the same phenomenon at lower intensity, or a different issue?
3. Is Phase B (working agents) still needed, or is the Phase A ceiling data sufficient?

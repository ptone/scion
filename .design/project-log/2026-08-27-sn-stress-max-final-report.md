# sn-stress-max — Final Stress Test Report (8 CPU / 32 GiB)

## Instance Configuration
- **Name**: sn-stress-max
- **Size**: 8 CPU / 32 GiB (empirically verified maximum for Cloud Run Instances)
- **Region**: us-east4
- **Image**: us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni@sha256:e3eab113...
- **Features**: sandboxLauncher=true, IAP enabled
- **Created**: 2026-08-27T06:31:50Z

## Maximum Instance Size (§5)
Attempted 32 CPU / 128 GiB → rejected: "maximum allowed value of 8 for cpu"
Attempted 8 CPU / 128 GiB → rejected: "maximum allowed value of 32Gi for memory"
**Maximum**: 8 CPU / 32 GiB. Established via rejection messages.

## The Bracket

| Bound | Workload | Ceiling (exec-verified alive) | Crashed at |
|---|---|---|---|
| **OPTIMISTIC (Phase A)** | Idle (sleep only, no CPU/mem work) | **51** alive | N=52 triggered cascade → instance crash |
| **PESSIMISTIC (Phase B)** | 100 MiB alloc + sha256sum CPU spin loop | **15** alive | N=16 triggered instant crash (1s after liveness pass) |

**Reality lies between 15 and 51 working sandboxes at 8 CPU / 32 GiB.**

The workload definition matters: Phase B's sha256sum loop is a worst-case CPU-saturating synthetic load. A real Claude agent spends most time blocked on model API responses and uses far less CPU. Neither bound is "agents supported" — they bracket the answer.

## Phase A Detail (Idle Agents)

### Exec-verified sweep at 07:09:07-27 UTC
51 agents passed `sandbox exec <name> -- /bin/true` with exit_code=0:
stress-test-0, idle-6 through idle-55.

### Death timeline
- **Slow trickle** (N=12→55): idle-1 through idle-5 died at ~2-3 minute intervals (FIFO: oldest first). Hub did NOT notice — reported all as "running".
- **07:09:44**: idle-56 created (52nd alive sandbox).
- **07:10:06**: **CASCADE BEGINS**. idle-45 found dead (was alive 60 seconds earlier).
- **07:10-07:12**: 16 more agents die at ~1 every 7 seconds.
- **07:12:01**: Cloud Run terminates instance ("no available instance").
- **07:12:26**: Instance restarts fresh. All state lost.

### Death mechanism
- **No exit_code=137 (OOM kill) anywhere in logs.** Searched all text/JSON payloads.
- All sandbox deaths are exit_code=1 (process error inside gVisor sandbox).
- Plus zombie runsc processes with "broken pipe" signal.
- Failure presented to Cloud Run as broker timeout (2m0s on agent creation).
- Cloud Run response: SIGTERM → graceful shutdown → restart.

## Phase B Detail (CPU-Saturating Workload)

### Workload specification
- **Harness**: claude (stays alive via tmux session)
- **Memory**: `dd if=/dev/urandom of=/tmp/memload bs=1M count=100` (100 MiB resident allocation)
- **CPU**: `while true; do dd if=/dev/urandom bs=4096 count=256 | sha256sum >/dev/null; sleep 0.1; done` (near-100% CPU per agent)
- Each agent: ~100 MiB explicit allocation + ~515 MiB base RSS = ~615 MiB per working agent

### Ladder results (all exec-verified)
| Step | N created | N alive | N dead | Dead agents | Notes |
|------|-----------|---------|--------|-------------|-------|
| 1-9  | 1-9       | 1-9     | 0      | —           | All stable |
| 10   | 10        | 9       | 1      | w-1         | Oldest agent evicted (FIFO pattern, same as Phase A) |
| 11   | 11        | 10      | 1      | w-1         | Stable |
| 12   | 12        | 11      | 1      | w-1         | Stable |
| 13   | 13        | 12      | 1      | w-1         | Stable |
| 14   | 14        | 13      | 1      | w-1         | Stable |
| 15   | 15        | 14      | 1      | w-1         | Stable. Last verified-stable step. |
| 16   | 16        | 15+1    | 1      | w-1         | **Creation took 68 seconds. Passed liveness. CRASH 1 second later.** |

### Crash at N=16
- **07:50:28**: External API returned 503 (Cloud Run proxy timeout)
- **07:51:33**: Broker log shows w-16 creation succeeded internally (68 seconds, "Slow request")
- **07:51:37**: w-16 passed liveness probe (exit_code=0)
- **07:51:38**: Container received termination signal
- **07:52:03**: Instance restarted fresh (all state lost, second crash this session)

### CPU is NOT the binding constraint
15 working agents each running a sha256sum spin loop — well past the 8 CPU core count. The agents share cores via time-slicing. If CPU were binding, the ceiling would be near 8. Instead it's near 15, suggesting **memory is the dominant constraint** even under CPU-saturating load.

15 agents × ~615 MiB/agent ≈ 9.2 GiB. But the instance has 32 GiB. The remaining ~23 GiB is consumed by:
- Hub, broker, and system processes
- gVisor runtime overhead per sandbox (sentry process, kernel state)
- Shared page mappings (binary text, libraries)
- Runtime metadata, rootfs overlays, and /proc emulation per sandbox

The per-sandbox overhead from gVisor is likely much larger than the sum-of-RSS proxy suggests. Sum-of-RSS double-counts shared pages (the scion binary text is counted once per process) while missing gVisor sentry overhead entirely. It is an uncalibrated proxy, not a measurement.

## Cross-Size Comparison (8 CPU/32 GiB vs 4 CPU/8 GiB)

| Metric | sn-stress-def (4/8) | sn-stress-max (8/32) | Ratio |
|---|---|---|---|
| CPU | 4 | 8 | 2× |
| Memory | 8 GiB | 32 GiB | 4× |
| Phase A ceiling (idle) | 17 | 51 | **3×** |
| Phase B ceiling (spin) | TBD | 15 | — |
| Failure signal | SIGBUS (signal 7), 8s after create | SIGTERM (graceful), 1s after liveness | Different |

**4× memory + 2× CPU → 3× idle agents.** The ceiling is NOT linear in memory. No per-GiB rule of thumb can be published. The relationship is sub-linear, likely due to fixed-overhead components (gVisor sentry per sandbox, hub/broker footprint) that don't scale with instance size.

## Failure Mode Characterization (§3.3)

### Failure is 3-phase, same in both sizes:
1. **Slow FIFO eviction**: Oldest agent dies silently. Hub doesn't notice. Interval: 2-3 minutes.
2. **Cascade**: Creating one more agent triggers rapid system-wide sandbox failures (~1 death every 7 seconds in Phase A). Broker overwhelmed.
3. **Instance termination**: Cloud Run terminates the instance. SIGTERM (sn-stress-max) or SIGBUS (sn-stress-def). Instance restarts with zero state. Hub uses in-memory SQLite — restart = total data loss.

### The restart IS the failure mode
- No persistent state survives. Hub database, broker state, signing keys, projects, agents — all gone.
- Instance auto-restarts in ~25 seconds.
- Recovery is manual: re-create projects, re-create agents.

### NOT OOM
- Zero exit_code=137 in any log across both instances.
- Zero "OOM" or "out of memory" strings.
- The Linux OOM killer is not the mechanism.
- gVisor may enforce its own resource limits. Or Cloud Run's container-level memory enforcement uses SIGTERM/SIGBUS rather than SIGKILL.

## Cost
- Instance running since 06:31 UTC (~1h30m)
- 8 CPU / 32 GiB @ Cloud Run Instance pricing ≈ $0.50-$1.00

## Repeatability
**UNMEASURED.** Each ceiling was observed once. The Phase A ceiling may not be stable across runs. Repeating the test was declined (cost/time).

## Recommendations
1. **Do not publish a per-GiB scaling rule.** The 3× agents from 4× memory result shows non-linear scaling.
2. **The instance restart is catastrophic.** If hub state is in-memory, the failure mode destroys all context. Persistent hub storage would change the failure from catastrophic to recoverable.
3. **The earliest-created agent dies first.** Both Phase A and Phase B show FIFO eviction of the oldest agent before the cascade. This is a sizing signal: the system can sustain N-1 agents longer than it sustained N.

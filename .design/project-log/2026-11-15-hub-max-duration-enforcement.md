# Hub `max_duration` Enforcement and 24h Default

**Date:** 2026-11-15
**Branch:** scion/cloudrun-7929

## Problem

In-container `max_duration` enforcement relies on `sciontool` staying responsive to report `limits_exceeded` and exit cleanly, and hubs without an explicit `default_max_duration` left agents without a default runtime cap. If `sciontool` wedges or never starts reporting, an instance (such as on Cloud Run Instances) can continue running and accruing charges indefinitely.

## Solution

1. **Hub-recorded run clock (`run_started_at` + `run_started_at_run_id`):** Added nullable columns on `agents` stamped by the Hub when a new run settles (synchronous `HTTPAgentDispatcher` start/resume/restart/reincarnation, asynchronous succeeded launch reports, or first `RunningObserved` transition), independent of `sciontool`-reported `started_at` and immune to pre-dispatch `run_id_updated_at` bumps or reverts.
2. **Built-in `24h` Hub default:** Set `default_max_duration: "24h"` in `loadBootstrapDefaults` so Hub `agent_defaults` defaults to `24h` unless an admin saved Agent Defaults, `settings.yaml`, or `SCION_SEED_*` / `SCION_SERVER_*` sets it (`"0"` at any rung opts out).
3. **Experiment-gated sweep (`hub.max_duration_enforcement`):** Registered the `"agent-max-duration"` singleton sweep (1-minute interval, 50 stop attempts/tick cap, oldest first, failing open on unreadable/missing/hash-mismatched templates and skipping unreachable brokers for the rest of the tick) that stops overdue `starting`/`running` runs after `max_duration + 10m` grace with `phase=stopped`, `activity=limits_exceeded`, and `exit_reason=limits_exceeded` regardless of agent activity.

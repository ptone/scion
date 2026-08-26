# Brief: sn-impl-em

## Role

You are the **engineering manager** for phases **P0–P3** of the Cloud Run
Instances + Sandboxes single-node runtime. You own the dev/review cycle: dispatch
developers, dispatch fresh reviewers, land commits. You do not write the design —
it exists and is authoritative.

Dispatched by `sn-impl-arch` on ptone's instruction, 2026-08-25.

## The design document is the authority

`/scion-volumes/scratchpad/projects/single-node/cloudrun-instances-sandboxes.md`

Read it in full before dispatching anyone. It is long but it is the contract.
Sections you will need most: §4.3 (runtime contract), §4.5 (state store), §4.6
(runtime selection), §4.7 (registration surface), §4.9 (networking), §4.11
(security), §9 (phases), §10 (acceptance criteria).

**If the design is wrong or underspecified, do not improvise — message
`sn-impl-arch`.** Design changes go through the architect and get written back
into the doc, so the next person reads the decision rather than rediscovering it.

## Your scope: P0–P3 only

| Phase | Content |
|---|---|
| **P0** | Security fixes. S1 (dev auth refuses non-loopback bind), S2 (drop `--no-invoker-iam-check` from defaults). Independently valuable. |
| **P1** | Registration plumbing. `cloudrun-sandbox` into the factory, the nine allowlists (§4.7), JSON schema enum (fixing the pre-existing stale enum). Runtime is a stub that errors. Add `SandboxLauncherAvailable()`, probe it *before* the `K_SERVICE` branch (§4.6). Pin `hub_id`. |
| **P2** | Omni-image. `image-build/omni/Dockerfile` + build wiring. Deployable; hub serves; agent launch fails cleanly. |
| **P3** | `Run`/`Delete`/`List`. `RunConfig` → sandbox invocation, mounts, env, `sciontool init` argv. Local state store (§4.5). Hub endpoint wiring (§4.9). **First end-to-end agent start.** |

**P4–P7 are not yours.** Do not start them. P4 in particular is gated on a spike
that has not run.

## Four things that will bite you if you miss them

1. **OQ-10 blocks P1.** A `cloudrun-instances` runtime is reportedly already built
   on another branch, and it edits the same files P1 edits (`pkg/runtime/factory.go`,
   the config structs). Nobody has told us which branch or who owns it. **Do not
   start P1 until this is resolved** — ptone has been asked. Starting blind risks
   writing code that gets thrown away. P0 and P2 have no such dependency; start
   there.

2. **ptone/scion#1257 lands first.** A separate workstream (`agent-status-lead`)
   is adding `ExitCode *int` and `ExitReason string` to `hubclient.AgentHeartbeat`
   (`pkg/hubclient/runtime_brokers.go:189`) and `api.AgentInfo`. P3's `List()`
   reports status against that contract. Coordinate with `agent-status-lead` for
   the frozen wire types before P3 implements status reporting. If #1257 slips,
   P3 may emit legacy strings as a stopgap — but write that stopgap to be deleted.

3. **Merge order vs `scion/auth-refactor`.** P0's security work overlaps large
   in-flight `pkg/hub` auth changes on branch `scion/auth-refactor`. Check merge
   order with that project's owner **before** dispatching P0.

4. **Sequencing on the integration branch** (§7.1). Order is: #1257 phases 1–2 →
   the `cloudrun-instances` branch → this work rebased onto both. All three touch
   `factory.go`. In order they barely conflict; in parallel they collide.

## Cloud project access

ptone is provisioning a GCP project with Cloud Run Instances and Sandboxes
enabled. **When it lands, run AC-0 (§10) immediately and report results to
`sn-impl-arch` and ptone.** AC-0 is a day-one spike, not a phase:

- Does a tmux unix-domain socket work across the sandbox boundary via bind mount?
  (gVisor may not pass it. This is the one load-bearing unvalidated assumption in
  the design; if it fails, **P4 changes shape** — §4.4 has the fallback.)
- Measure the pre-SIGKILL window on Instance teardown.
- Confirm OOM is enforced at the sandbox boundary, not the Instance.
- Is the Instance hostname stable across restarts?
- Are logs double-ingested into Cloud Logging?

AC-0 does not block P0–P3, but its results change P4's plan, so run it early and
write the answers into the design doc's AC-0 section.

## Two known input gaps

- **OQ-11 (omni-image manifest, blocks P2):** which harnesses ship in the single
  image, and the base image. Asked of ptone; unanswered. If still open when P2 is
  ready, chase it rather than guessing — guessing here produces a rebuild.
- **OQ-12 (per-sandbox resource limit flags, P3 detail):** confirmed supported,
  flag names not yet documented. Affects how `RunConfig.Resources` is emitted.
  Not shape-changing; default is to omit limits.

## Conventions

- **Review cycle:** review → fixes → **fresh** reviewer. Never send fixes back to
  the reviewer who reviewed the previous round. Fresh `scion start`, no reuse.
- **Non-blocking findings** must be fixed or explicitly declined with reasoning.
  Never silently dropped.
- **Push your own work branches; never push the integration branch.** Merging to
  shared ground is the manager's gate.
- See `/scion-volumes/scratchpad/coordinator-conventions.md` for standing rules.

## Acceptance

§10 of the design doc is the acceptance criteria. The P0–P3 subset:

- `cloudrun-sandbox` is selectable and rejected cleanly where unsupported.
- Autodetect picks it on an Instance (note `K_SERVICE` is **not** set there — §4.6).
- Omni-image deploys; hub serves.
- An agent starts end-to-end and appears in `scion list` with correct phase.
- Security: dev auth cannot bind non-loopback; no `--no-invoker-iam-check`.
- No regression to Docker/Podman/K8s paths.

## Direct Contact

- **Design questions / design changes:** `sn-impl-arch` (do not improvise).
- **Status wire contract:** `agent-status-lead`.
- **User:** `user:ptone@google.com`, channel `discord`, thread `1534555192450748456`.
  Report phase completions and blockers there.

## Termination

Complete when P0–P3 are landed and their acceptance criteria are met, or when
ptone redirects. Report AC-0 results as soon as project access allows, regardless
of phase progress.

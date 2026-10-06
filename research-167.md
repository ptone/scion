# Research: miller79/scion#167: reincarnate for empty-per-agent workspaces

Investigator: ep-inv. Facts only, with file:line. No design decisions are made here.

Baselines read:
- **upstream** = GoogleCloudPlatform/scion `main` @ `4992886ec` (fetched 2026-10-06 as `upstream-main`).
- **2b** = ptone/scion branch `scion/reincarnate-broker` @ `343eb5d34` (ptone/scion#2727 phase 2b, not upstream). Its merge-base with upstream is `b07a24301`. Phase 1 (`f6e8d3d02`, #2441) and phase 2a (`b6b957d6b`, #2509) are already on upstream.

Process note: `/scion-volumes/scratchpad/skills/` is missing on this mount. I used the installed `scion-process` and `software-engineering-process` skills instead.

---

## 1. Is the issue's description of current behaviour accurate?

**Yes, on every point checked against upstream.**

| Claim in the issue | Upstream fact |
|---|---|
| The hub refuses every empty-per-agent agent before any plan is computed | `pkg/hub/handlers_agent_reincarnate.go:286-289` sets `workspaceModeErr` when `project.IsEmptyPerAgent()`. It returns 400 at `:320-322`, before the dispatcher, broker, capability and in-flight checks (`:325-360`) and before any plan or stop. A move request also gets the error, because it is passed into `planReincarnateMove` (`:316-318`) and is check 1 of the move verdict. So `--dry-run` reports it. |
| The recorded reason is "design #2703 D4" | Comment at `handlers_agent_reincarnate.go:283-285`. ptone/scion#2703 (closed) has no "D4" text in its body, so D4 lives only in this comment. |
| `api.ReincarnateEligible(hasGitClone, workspace)` is the shared gate | `pkg/api/types.go:977-979` is `return hasGitClone \|\| workspace != ""`. Called by the hub at `handlers_agent_reincarnate.go:303` and by the broker at `pkg/agent/provision.go:561`. |
| `Reprovision` accepts only clone-per-agent (an existing `.git`) or an explicit mount whose path exists | `provision.go:575-583` handles the clone case (Stat of `<agentDir>/workspace/.git`). `:584-650` handles the explicit mount (agent dir via `CheckAgentDirContained`, then the workspace must exist and be a directory). |
| An empty-per-agent agent is refused at the broker too | Yes. On the hub, an empty-per-agent agent has `AppliedConfig.GitClone == nil` and `Workspace == ""`. `populateAgentConfig` only sets GitClone when `GitRemote != ""` (`handlers_agent_create_helpers.go:259-267`), and it skips Workspace for empty-per-agent through `syncsHubProjectWorkspace` (`:274-283`, `project_workspace_mode.go:168-173`). So `ReincarnateEligible(false, "")` is false and the broker refuses at `provision.go:561-562`. This is pinned by `TestReprovision_EmptyPerAgent_RefusedWorkspaceUntouched` (`pkg/agent/run_empty_per_agent_test.go:261`). |
| "Reprovision never creates, clears or recreates a workspace" | True **only because of the pre-checks**. `ProvisionAgent` itself would create the workspace. Its empty-per-agent branch runs `os.MkdirAll(agentWorkspace)` (`provision.go:1094-1102`, idempotent, keeps existing content), and it always runs `MkdirAll(agentDir)` (`:1058`). A new empty-per-agent branch in Reprovision must therefore check the workspace exists **before** calling `ProvisionAgent`, exactly as the issue says. |
| The `GetAgent` resume branch recreates a missing empty-per-agent directory | **Real.** At `provision.go:2513-2526` it ORs the persisted `EmptyPerAgentWorkspace` into ctx. At `:2533-2545` it calls `os.MkdirAll(agentWorkspace)` when the directory is missing. Reprovision does not use `GetAgent`; it calls `ProvisionAgent` directly (`:665`). |

Extra facts the issue does not mention:
- ProvisionAgent already refuses empty-per-agent combined with a workspace path, a shared workspace or a git clone (`provision.go:1019-1031`). It does so with an **untyped** error, which the broker returns as 500, not 409. A 409 needs the check repeated inside Reprovision, wrapped in `ErrReprovisionRefused`.
- The hub has no per-agent record that an agent is empty-per-agent: `store.AgentAppliedConfig` has no such field. The mode comes from the **project** label (`store.Project.IsEmptyPerAgent`, `pkg/store/models.go:890-892`). The issue's hub condition "the agent's applied config is empty-per-agent" therefore cannot be evaluated as written. The only per-agent record is the broker-side `scion-agent.json` (`EmptyPerAgentWorkspace`, `pkg/api/types.go:508-514`, written at `provision.go:1895-1897`, read by `persistedEmptyPerAgent` at `:315-331`).
- The project workspace-mode label cannot change after creation (`project_workspace_mode.go:86-91`). So a mode switch between an agent's creation and its reincarnation, the hazard behind the clone/shared switch checks at `handlers_agent_reincarnate.go:297-306`, does not apply to empty-per-agent through labels.
- Empty-per-agent is a non-git-only mode. A raw `empty-per-agent` label on a git project resolves to shared-plain (`store.ResolveProjectSharingMode`, `models.go:785-797`).
- The reincarnate worker stops the agent (`reincarnate_worker.go:537`) before it sends the reprovision. Any broker-side refusal therefore leaves the agent **stopped**. That is why the hub must gate up front.

## 2. Overlap with ptone/scion#2727 phase 2b (`scion/reincarnate-broker` @ 343eb5d3)

### What 2b allows
- **Empty-per-agent cross-broker moves.** `handlers_agent_reincarnate.go(2b):270` sets `emptyPerAgentMove := moveTarget != nil && project.IsEmptyPerAgent() && s.emptyPerAgentWorkspaceMovable(...)`. The blanket refusal now applies only when `!emptyPerAgentMove` (`:272-274`). `emptyPerAgentWorkspaceMovable` (`:606-619`) requires all of:
  - `agent.WorkspacePlacement == "export"`;
  - `api.SameWorkspaceExport(src, dst)`;
  - equal, non-empty NFS `ExportID` markers.
- An empty-per-agent move is passed to the move verdict as `CloneMode=true` (`:302`, `hasGitClone || emptyPerAgentMove`). The agent's own directory is then the moved unit (`reincarnate_move_worker.go(2b):35-50`, `AgentDirWorkspace` → `MoveWorkspaceAgentDir`).
- **Runtime:** `CloneMode=true` forces both the source runtime and the target profile type to be Kubernetes (`reincarnate_move.go:217-225, 244-248`). Placement is also `export` only for Kubernetes with a PV claim (`run.go:2012-2030` upstream). Docker/Podman/container empty-per-agent can never be `export`, because empty-per-agent on an NFS broker with a non-k8s runtime fails at start (`nfs_empty_agent_dir.go:46-52`, `errEmptyPerAgentNFSRuntime`). **So 2b's empty-per-agent moves are k8s+NFS-only.**
- **Placement:** the source must report `WorkspacePlacement=export`. A GCS-synced workspace (`WorkspaceStoragePath != ""`) is refused (`reincarnate_move.go:208-216`).
- **Broker side:** the move does **not** go through `Manager.Reprovision`. The target gets `DispatchAgentProvisionForMove` (`reincarnate_worker.go(2b):650`) with `ExpectExistingNFSWorkspace="agent-dir"`. The broker confirms `<subPathRoot>/<projectID>/agents/<name>/workspace` through its own mount (`runtimebroker/handlers.go(2b):1197-1212`, `agent/nfs_move_workspace.go:48-78`) and returns 409 if it is missing. It then runs a normal provision. The source gets a `localOnly` delete (`handlers.go(2b):1854`).
- **Capability:** 2b flips `AgentMove` to `true` (`heartbeat.go(2b):324`, `handlers.go(2b):238`).

### What 2b still refuses
- **Same-broker empty-per-agent reincarnation.** `--broker <current broker>` is not a move: `moveTarget` stays nil when `dst.ID == agent.RuntimeBrokerID` (`handlers_agent_reincarnate.go(2b):213-216`). It then hits the unchanged refusal at `:272-274`. A plain `scion reincarnate` is refused the same way. **This is exactly the gap #167 targets. 2b does not close it.**
- `Manager.Reprovision` and `api.ReincarnateEligible` are unchanged in 2b: `git diff b07a24301 343eb5d34 -- pkg/api pkg/agent/provision.go` is empty.
- Moves of linked projects, of GCS-synced workspaces, of local placements, and of clone-per-agent agents on non-k8s runtimes.

### Where #167 contradicts 2b
1. **"Once `--broker` lands, refuse an empty-per-agent move."** `--broker` is already on upstream (dry-run in phase 1, placement in 2a). 2b deliberately **allows** k8s+NFS empty-per-agent moves. The issue's blanket refusal of moves would revert 2b's A4 path. The issue's tests ("an empty-per-agent move is refused") conflict with 2b's tests (`handlers_agent_reincarnate_move_test.go(2b)`, `4aeb25333 test(hub): empty-per-agent move needs equal export identities`).
2. **"Refuse Kubernetes."** As a statement about same-broker in-place reincarnation, this does not conflict with 2b, which never touches same-broker. As a statement about moves, it does conflict, because 2b's empty-per-agent moves are k8s-only. The issue's reason ("the broker can't reliably confirm the NFS dir exists") is also outdated against 2b: `AgentManager.CheckNFSMoveWorkspace` (`nfs_move_workspace.go:48`) does exactly that check through `nfsExportWorkspaceOnAnyRuntime` (`nfs_worktree.go:292`). It could serve a same-broker k8s+NFS check later.
3. **Code overlap.** Both 2b and a #167 fix edit the same gate block, `handlers_agent_reincarnate.go` `:264-306` (upstream `:283-322`). A #167 fix written against upstream will conflict textually with 2b.
4. **Docs drift in 2b.** 2b changes no docs. `docs-site/.../reference/cli.md:461-466` still says empty-per-agent is "not yet supported". `cli.md:474` says a real `--broker` move is unsupported, and `cli.md:480-483` says no broker advertises agent-move. `local/workspaces-and-sharing.md:56` says "Reincarnate (moving the agent to another Runtime Broker) is not supported for this mode". All of these are stale once 2b lands, and #167 would edit the same lines.

## 3. Same-broker safety: where the workspace lives

| Runtime / storage | Empty-per-agent workspace location | Survives stop and re-create? |
|---|---|---|
| Docker / Podman / Apple `container`, local storage | `<projectDir>/agents/<slug>/workspace` on broker disk (`provision.go:1040-1044, 1094-1102`). It is bind-mounted. `run.go:1207-1212` (upstream) refuses any other effective workspace. | Yes. It is a host directory, independent of the container. |
| Docker / Podman / container, NFS storage | **Not possible.** Start fails with `errEmptyPerAgentNFSRuntime` (`nfs_empty_agent_dir.go:28-29, 50-51`; called at `run.go:1499`). | n/a |
| Kubernetes, NFS storage with a PV claim | `<subpath_root>/<projectID>/agents/<name>/workspace` on the export (`nfs_empty_agent_dir.go:36-56`, `run.go:1557-1567`). Without a claim it fails with `errEmptyPerAgentNFSNoClaim` (`run.go:1564`). | Yes, on the export. |
| Kubernetes, local or `gke-shared-volume` | Pod-local EmptyDir (`k8s_runtime.go:2149-2170`). Documented as lost when the agent stops (`workspaces-and-sharing.md:53`). | **No.** Any reincarnation, which re-creates the pod, starts empty. |
| Cloud Run / Substrate | Mode unsupported (`cloudrun_runtime.go:189-192`, `substrate_runtime.go:375-382`). The `EmptyPerAgentWorkspace` capability is false. | n/a |

The issue says "Docker/Podman". The Apple `container` runtime has the same local-disk layout (it is listed alongside docker/podman in `exportMountingRuntimes`, `run.go:1998-2003`). Whether to include it is a scope choice for the lead.

**Does Reprovision create or clear a workspace?** Not today, because of the pre-checks at `provision.go:561-650`. ProvisionAgent, which Reprovision calls, does: it creates `agentDir` (`:1058`), and its empty-per-agent branch runs `MkdirAll` on the workspace (`:1094-1102`). It never clears an empty-per-agent workspace. The `RemoveAll` at `:1115-1125` is in the git-clone branch only. A new Reprovision branch must:
- Lstat `<agentDir>/workspace` (not a symlink, a real directory) **before** `ProvisionAgent`;
- confirm `persistedEmptyPerAgent` (or a direct load of `scion-agent.json`) is true for this agent directory.

**What the derived config needs so generation N+1 mounts the same directory.** On the wire this already works:
- The hub sends `WorkspaceMode = dispatchWorkspaceMode(project)` on the provision request (`httpdispatcher.go:696`, `project_workspace_mode.go:146-160`). For an empty-per-agent project that is the canonical `"empty-per-agent"`. The reprovision dispatch uses the same `buildCreateRequest` path (`httpdispatcher.go:1716-1724`).
- The broker turns it into `opts.EmptyPerAgentWorkspace = true` (`runtimebroker/start_context.go:926-932`). It refuses conflicting sources with `emptyPerAgentConflict` (`:1925`).
- `buildProvisionContext` puts it on ctx (`provision.go:430-432`). ProvisionAgent then persists `EmptyPerAgentWorkspace=true` (`:1895-1897`).
- At start, the hub sends `SCION_WORKSPACE_MODE=empty-per-agent` (`httpdispatcher.go:3027-3040`). `run.go:265` (upstream) also ORs in the persisted flag, and `run.go:1207-1212` forces the mount to `<agentDir>/workspace`.

The issue's step 8 is therefore already satisfied end to end. The only gap is the `Reprovision` gate. Because `AppliedConfig.Workspace` stays `""` for empty-per-agent, the hub never sends a conflicting Workspace.

## 4. Capabilities and hub gating

Existing `store.BrokerCapabilities` fields (`pkg/store/models.go:970-1000`): `WebPTY`, `Sync`, `Attach`, `Reprovision`, `AsyncLaunch`, `EmptyPerAgentWorkspace`, `AgentMove`, `StartsInFlight`.
- They are parsed from registration strings in `capabilitiesFromStrings` (`pkg/hub/brokerauth.go:243-266`).
- They are mirrored in `runtimebroker/types.go:85-105` and `hubclient/types.go:~345`.
- They are set at `runtimebroker/heartbeat.go:322-330` and `handlers.go:230-240`.

How the hub gates on them:
- `Reprovision`: `handlers_agent_reincarnate.go:344-351`. Returns 412 `ErrCodeUnsupportedCapability` when false. It runs before the plan and before the in-flight check, so nothing changes.
- `EmptyPerAgentWorkspace`: `checkEmptyPerAgentBrokerCapability` (`project_workspace_mode.go:200-220`). Used by `getProvisioningBrokerEndpoint` (`httpdispatcher.go:557-578`, on every provision dispatch, including reprovision) and `requireEmptyPerAgentBrokerCapability(ForAgent)` (`handlers_agents_core.go:1411`, `handlers_agent_lifecycle.go:677`). Returns 412.
- `AgentMove` (upstream false everywhere, true in 2b): check 8 of `evaluateMoveEligibility` (`reincarnate_move.go:297-306`, both brokers), and `requireAgentMoveBroker` in 2b (`move_dispatch.go:53`).

**Is a new capability needed? Yes, on the evidence.**
- An upstream or 2b broker advertises `Reprovision=true` and `EmptyPerAgentWorkspace=true`, but its `Reprovision` refuses empty-per-agent (`provision.go:561`).
- If the hub accepted the request on those two flags, the worker would stop the agent first (`reincarnate_worker.go:537`), the broker would then return 409, and the agent would be left stopped. That is the stop-then-refuse pattern that A23.1 R1 and AC-9 were written to prevent.
- Neither existing flag tells a new broker from an old one, so the gate needs a new flag. `AgentMove` does not fit: it is about cross-broker moves, and a k8s 2b broker sets it to true while still refusing an empty-per-agent reprovision.
- Wiring it means touching every place listed above: store, runtimebroker, hubclient, `capabilitiesFromStrings`, heartbeat and info.

## 5. Existing tracking

- **ptone/scion#1821** (open, reincarnate epic). The only related line is the checklist item "Phases 2–6 … other workspace modes and k8s". Empty-per-agent is not named.
- **ptone/scion#2727** (open, `--broker`). Its body says: "non-git and per-agent workspaces live on the source broker. Decide whether a move is refused, starts empty, or copies the workspace." No comment mentions empty-per-agent. 2b's answer is "allowed on a shared NFS export, k8s only".
- **ptone/scion#2703** (closed, the empty-per-agent design). Its body does not contain "D4" or "reincarnate". The D4 reference exists only in the code comment.
- Related open issues, none of which cover same-broker empty-per-agent reincarnate:
  - ptone/scion#2919 (move empty-per-agent handling into `nfsBackend.Resolve`);
  - ptone/scion#2971 (`SelectWorkspaceBackend` handles empty-per-agent only for NFS);
  - ptone/scion#2704 (empty-per-agent tracking);
  - ptone/scion#3043 (atomic render in `Manager.Reprovision`, which touches the same function).
- GoogleCloudPlatform/scion: searches for "empty-per-agent" and "empty-per-agent reincarnate" found no issue or PR. There is no upstream tracking. The issue's "upstream doesn't track this specifically" is accurate.

## 6. Size and files a fix would touch, given 2b

**Size: medium.** It is one focused PR touching three layers (broker manager, broker capability, hub gate) and docs, with no schema or store migration. It should be **based on 2b**, or wait for 2b to merge, because both edit the same gate block.

Files:
- `pkg/api/types.go:960-979`: change the `ReincarnateEligible` signature or semantics, plus its doc. Callers: `handlers_agent_reincarnate.go:303` and `provision.go:561`, plus tests that call it.
- `pkg/agent/provision.go:524-680`: add a new empty-per-agent branch in `Reprovision`, and update the doc comment at `:524-558`. Possibly reuse `persistedEmptyPerAgent` (`:315`).
- `pkg/agent/run_empty_per_agent_test.go:261`: the existing refusal test flips to a positive test. Add negative tests (missing, symlinked, file, non-empty-per-agent persisted config), next to `reprovision_test.go`.
- `pkg/runtimebroker/types.go`, `heartbeat.go:322-330`, `handlers.go:230-240`, plus `pkg/hubclient/types.go`, `pkg/store/models.go:970-1000` and `pkg/hub/brokerauth.go:243-266`: the new capability.
- `pkg/hub/handlers_agent_reincarnate.go` (2b `:264-306`): the gate.
  - Allow empty-per-agent when `moveTarget == nil` and the broker has the new capability.
  - Keep 2b's `emptyPerAgentMove` path.
  - Add a runtime gate. The hub has `agent.Runtime`, and `isKubernetesRuntimeType` is at `project_settings_handlers.go:371`.
  - Order matters: the capability check must run before the stop and before the dry-run plan.
- `pkg/hub/handlers_agent_reincarnate_test.go` and `reincarnation_gate_*_test.go`: hub tests.
- `docs-site/src/content/docs/reference/cli.md:461-483` and `local/workspaces-and-sharing.md:56`: also stale after 2b.

Open questions for the lead (no decision made here):
- **Kubernetes + NFS, same broker.** 2b's `CheckNFSMoveWorkspace` exists, so the issue's k8s refusal could be narrowed to "k8s without NFS" (where the workspace is lost anyway).
- **Apple `container` runtime.** Same local-disk layout as Docker/Podman; include it or not.
- **The issue's "refuse moves" clause.** It should be dropped or rewritten to match 2b.

Verification run: `go test -p 1 -run 'TestReprovision_EmptyPerAgent_RefusedWorkspaceUntouched|TestReprovision_NeitherGitCloneNorWorkspace_Refused' ./pkg/agent/` on upstream: **ok** (pkg/agent, 0.18s). I made no pkg/hub runs and no DB writes.

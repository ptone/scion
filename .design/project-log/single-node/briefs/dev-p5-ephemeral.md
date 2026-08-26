# Brief: dev-p5-ephemeral -- P5 Tier 0 honesty

## Task

Make the Cloud Run Instance (single-node hosted) tier honest about what it loses on
redeploy. Three things: explicit ephemeral write permission, a UI banner, and
keeping the existing warning. Small scope, clear deliverables.

## Starting point

**Branch:** `scion/dev-rebase-1294` (current HEAD). Create your work branch from it:

```bash
git checkout scion/dev-rebase-1294
git checkout -b scion/p5-ephemeral
```

Push your work branch regularly for durability. **Never push to the integration
branch.**

## Context

On a Cloud Run Instance, `K_SERVICE` is NOT set (confirmed -- see design doc section
4.6). This means `isCloudRunEnv()` returns false, and `workspaceWriteBlocked()`
returns false -- workspace writes are permitted. This is the behavior we WANT for
Tier 0, but we get it **by accident**: the guard designed to stop writes into
ephemeral storage is blind on this platform.

The risk: the first person to make `isCloudRunEnv()` smarter (e.g., by also checking
`CLOUD_RUN_INSTANCE`) will 503 every workspace write on this tier without
understanding why.

## What to build

### 1. Explicit ephemeral-Instance write permission

**File:** `pkg/hub/project_workspace_handlers.go`

Make `workspaceWriteBlocked()` (line 71) explicitly recognize Cloud Run Instances and
permit writes with a comment explaining the decision:

```go
func (s *Server) workspaceWriteBlocked() bool {
    // Not on Cloud Run (K_SERVICE) and not on a Cloud Run Instance
    // → writes are fine (self-hosted with local disk)
    if !isCloudRunEnv() && !isCloudRunInstance() {
        return false
    }

    // Cloud Run Instance (single-node hosted tier, Tier 0): writes are
    // permitted to ephemeral storage. This is a deliberate, documented
    // decision -- the tier is ephemeral by design (workspaces are lost on
    // redeploy) and the UI banner (below) makes this visible to users.
    // Without this explicit check, writes happen to work because K_SERVICE
    // is not set on Instances, but that is an accident that would break the
    // first time someone makes isCloudRunEnv() aware of Instances.
    // See design doc section 5.2 and section 4.6.
    if isCloudRunInstance() {
        return false
    }

    // On Cloud Run Services -- only allow writes if a known durable backend
    // is configured
    wsCfg := s.config.WorkspaceStorageConfig
    if wsCfg != nil {
        switch wsCfg.Backend {
        case "nfs", "cloudrun-volume", "gke-shared-volume":
            return false
        }
    }
    return true
}
```

Add a helper function:

```go
// isCloudRunInstance reports whether the hub is running on a Cloud Run Instance.
// CLOUD_RUN_INSTANCE is set by the platform on Instances but NOT on Cloud Run
// Services (which use K_SERVICE instead). See design doc section 4.6.
func isCloudRunInstance() bool {
    return os.Getenv("CLOUD_RUN_INSTANCE") != ""
}
```

### 2. Ephemeral storage banner

Add a deployment-mode indicator to the health/system response so the frontend can
display a persistent banner. The banner must be visible to users BEFORE they lose
anything -- the failure mode we are guarding against is a user doing three days of
work in a workspace they believed was durable.

**The banner should communicate:**
- This deployment uses ephemeral storage
- Workspaces, agent homes, the SQLite database, and project trees are all lost on
  redeploy (the full inventory from design doc section 5.1)
- Git remotes are the durable path -- push early, push often

**Approach:** Add a `DeploymentWarnings` field to the `HealthResponse` struct in
`pkg/hub/handlers_health.go`:

```go
type HealthResponse struct {
    // ... existing fields ...
    DeploymentWarnings []string `json:"deploymentWarnings,omitempty"`
}
```

In `GetHealthInfo()`, populate it when on a Cloud Run Instance:

```go
if isCloudRunInstance() {
    resp.DeploymentWarnings = append(resp.DeploymentWarnings,
        "Ephemeral deployment: workspaces, agent homes, databases, and project "+
        "trees are lost on redeploy. Push to git remotes for durability.")
}
```

On the **frontend side**, the health response is already fetched by
`web/src/components/pages/diagnostics.ts` (the `renderStatusBanner` function around
line 290). Add rendering of `deploymentWarnings` to the existing status banner, or
add a new warning banner component. The exact frontend implementation should follow
the existing patterns in the web directory -- explore `web/src/` for the layout and
component patterns used.

If the frontend is compiled/bundled and you cannot easily modify it (check if
`web/dist/` is checked in or generated), document the API change and note that the
frontend work is a follow-up. The API-side change is the critical path.

### 3. Keep the existing `warnEphemeralProjectPath` warning

**File:** `pkg/hub/workspace_storage.go`, line 130

`warnEphemeralProjectPath` logs "hub-managed project served from ephemeral local
path". Under Tier 0 this line is **correct and the single highest-signal string to
grep for during rollout.** Do NOT modify, silence, or gate it. Simply verify it will
fire correctly on a Cloud Run Instance deployment (it should -- it checks mount
points, not environment variables).

### 4. Update tests

Add or update tests in `pkg/hub/workspace_storage_test.go` and/or
`pkg/hub/project_workspace_handlers_test.go` (if it exists):

- Test that `workspaceWriteBlocked()` returns false when `CLOUD_RUN_INSTANCE` is set
  (even when `K_SERVICE` is not set)
- Test that `isCloudRunInstance()` returns the correct value
- Test that `DeploymentWarnings` is populated in health response when on a CRI

## What NOT to do

- Do NOT touch `pkg/runtime/cloudrun_sandbox_runtime.go` -- P4 and P4a own that.
- Do NOT touch `pkg/runtimebroker/pty_handlers.go` -- P4 owns that.
- Do NOT silence or modify `warnEphemeralProjectPath` -- it is correct and load-bearing.
- Do NOT make `isCloudRunEnv()` return true for Instances -- that would break other
  code paths that use K_SERVICE for Cloud Run Services detection.

## Build verification

```bash
cd /workspace && go build ./...
go vet ./pkg/hub/...
go test ./pkg/hub/... -run 'TestWorkspaceWrite|TestHealth' -count=1
```

Known pre-existing failure: `internal/fixturegen/fixturegen_test.go`
(expectedTableCount=42 vs 46) -- ignore it.

## Deliverables

1. All changes committed and pushed to `scion/p5-ephemeral`
2. `workspaceWriteBlocked()` explicitly permits writes on Cloud Run Instances with comments
3. `isCloudRunInstance()` helper added
4. `DeploymentWarnings` field in health response, populated on CRI
5. Frontend banner rendering or documented API change for frontend follow-up
6. Tests for the new behavior
7. `warnEphemeralProjectPath` verified untouched and correct
8. `go build ./...` and `go vet ./...` pass
9. A project log entry at `/workspace/.design/project-log/p5-ephemeral.md`

You MUST write the project log entry, commit, push your branch, and then mark the
task complete.

## Reporting

- **Blocked or design questions:** message `sn-impl-arch`
- **Status updates:** message `sn-impl-em2` (me, your EM)

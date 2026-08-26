# Security Audit Report: P4, P4a, P5 (Cloud Run Sandbox Runtime)

**Auditor:** security-auditor (audit-p4p4ap5)
**Date:** 2026-08-26
**Branch:** `scion/dev-rebase-1294`
**Commits:** `ab3a2f1..e6db196` (4 commits)

## Summary

| Severity | Count |
|----------|-------|
| Critical | 0 |
| High | 0 |
| Medium | 1 |
| Low | 2 |
| Info | 3 |

No merge-blocking issues found. One medium-severity process-reaping concern should be addressed in the current sprint.

---

## Findings

### [MEDIUM] Overly broad substring matching in `reapOrphanedRunsc` could kill unrelated processes

- **Location:** `pkg/runtime/cloudrun_sandbox_runtime.go:976-978`
- **Description:** `reapOrphanedRunsc` scans all `/proc/<pid>/cmdline` entries and kills any process whose command line contains all three substrings: `"runsc"`, `"delete"`, and the sandbox name. The sandbox name is a substring match, not a word-boundary or path-segment match. If a sandbox name is short or is a substring of another sandbox's name (e.g., sandbox `"app"` would match a runsc process for sandbox `"my-app-worker"`), the function could kill the wrong process.
- **Impact:** In a multi-sandbox environment, deleting sandbox `"foo"` could kill runsc processes belonging to sandbox `"foo-bar"` or `"my-foo-service"`, causing unexpected sandbox termination. This is a correctness issue with security implications (denial of service to other sandboxes).
- **Proof of concept:**
  1. Create sandbox `"app"` and sandbox `"my-app"`
  2. Delete sandbox `"app"`
  3. `reapOrphanedRunsc("app")` matches cmdline `runsc --root /run/sandbox/my-app/runc delete --force my-app` because it contains all three substrings
  4. The runsc process for `"my-app"` is killed
- **Recommendation:** Use a more precise match that anchors the sandbox name within the expected path structure:

  ```go
  // Match the sandbox name as a path segment or as the final argument,
  // not as an arbitrary substring.
  sandboxPathSegment := "/run/sandbox/" + sandboxName + "/"
  sandboxNameArg := " " + sandboxName + " "
  sandboxNameEnd := " " + sandboxName  // at end of cmdline (trailing space already trimmed)
  
  if strings.Contains(args, "runsc") &&
      strings.Contains(args, "delete") &&
      (strings.Contains(args, sandboxPathSegment) || 
       strings.Contains(args, sandboxNameArg) ||
       strings.HasSuffix(strings.TrimSpace(args), sandboxName)) {
  ```

  Alternatively, use a regex with word boundaries: `\b<sandboxName>\b`.

---

### [LOW] `isCloudRunInstance()` trusts an unsecured environment variable

- **Location:** `pkg/hub/project_workspace_handlers.go:66-68`
- **Description:** `isCloudRunInstance()` checks `os.Getenv("CLOUD_RUN_INSTANCE")` to determine whether the hub is running on a Cloud Run Instance. This environment variable is set by the platform but could be set by any process with environment access. If spoofed in a non-CRI environment, two effects occur: (1) `workspaceWriteBlocked()` returns `false`, allowing writes to what might be ephemeral storage; (2) the health API reports a misleading deployment warning.
- **Impact:** Limited. Exploiting this requires the ability to set environment variables for the hub process, which implies host-level access (the system is already compromised). The code documents this as a deliberate design decision (see comment at line 82-87). The env var also only relaxes restrictions (permits writes) rather than blocking them, so the fail-open direction is the less dangerous one for the CRI use case. The risk is that on a Cloud Run Service, a spoofed `CLOUD_RUN_INSTANCE=true` would bypass the write-blocking safety gate, allowing writes to ephemeral storage that would be silently lost on scale-down.
- **Recommendation:** Accept current design. If additional hardening is desired in the future, consider:
  1. Checking for a platform-specific metadata endpoint (e.g., GCE metadata server) to corroborate the env var
  2. Adding a log warning when `CLOUD_RUN_INSTANCE` is set alongside `K_SERVICE` (which would be an unusual combination)

---

### [LOW] No input validation on sandbox ID in `deleteWithTimeout`, `GetLogs`, `Attach`, `Exec`

- **Location:** `pkg/runtime/cloudrun_sandbox_runtime.go:698,822,828,874`
- **Description:** The public methods `deleteWithTimeout()`, `GetLogs()`, `Attach()`, and `Exec()` accept an `id` parameter that is passed directly to `exec.CommandContext` as an argument. Currently all callers pass IDs that originate from `sanitizeSandboxName()` (restricted to `[a-z0-9-]`) via the state store or `LookupContainerID()`. However, there is no defense-in-depth validation at the method boundary.

  Since `exec.CommandContext` passes arguments directly to the process (no shell interpolation), a malicious `id` containing special characters would not achieve command injection. The `id` would be passed as a single argument to the sandbox binary. However, an `id` containing `--` could be interpreted as a flag by the sandbox CLI, potentially causing unexpected behavior (argument injection).
- **Impact:** Low. Current call paths are safe because all IDs originate from `sanitizeSandboxName()`. The risk is a future internal caller passing an unsanitized value.
- **Recommendation:** Add a validation guard at the entry of `deleteWithTimeout` (which is the shared entry point for `Stop`/`Delete`):

  ```go
  func (r *CloudRunSandboxRuntime) deleteWithTimeout(ctx context.Context, id string) error {
      if !sandboxNameRe.MatchString(id) && id != "" {
          // sandboxNameRe matches chars NOT in [a-z0-9-], so a match means invalid chars
          return fmt.Errorf("cloudrun-sandbox: invalid sandbox id %q", id)
      }
      // ... rest of method
  }
  ```

  Note: `sandboxNameRe` matches characters NOT allowed. A cleaner check would be a positive validation:

  ```go
  var validSandboxID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
  
  if !validSandboxID.MatchString(id) {
      return fmt.Errorf("cloudrun-sandbox: invalid sandbox id %q", id)
  }
  ```

---

### [INFO] Deployment tier information exposed on unauthenticated `/healthz` endpoint

- **Location:** `pkg/hub/handlers_health.go:92-96`
- **Description:** The new `DeploymentWarnings` field is included in the `/healthz` response, which is unauthenticated (confirmed in `pkg/hub/auth.go:421`). When running on a Cloud Run Instance, the response now includes `"Ephemeral deployment: workspaces, agent homes, databases, and project trees are lost on redeploy. Push to git remotes for durability."` This reveals the deployment tier (Cloud Run Instance) and infrastructure characteristics to any unauthenticated client.
- **Impact:** Information disclosure. An attacker can fingerprint the deployment type. The warning text itself is generic and does not expose credentials, internal hostnames, or specific resource identifiers. The health endpoint already exposes `scionVersion` and `hub_id`.
- **Recommendation:** Acceptable as-is given that `/healthz` already exposes version information. If minimizing the unauthenticated surface is desired in the future, consider:
  1. Moving `DeploymentWarnings` to an authenticated admin-only endpoint
  2. Or populating the field only when the request includes a valid session

---

### [INFO] `Exec()` implementation activates pre-existing heredoc injection surface in `resetAuth`

- **Location:** `pkg/runtime/cloudrun_sandbox_runtime.go:874-881` (Exec implementation), `pkg/runtimebroker/handlers.go:1858-1862` (resetAuth caller)
- **Description:** The `Exec()` method was previously a stub returning `"not yet implemented"`. Now that it's implemented, the existing `resetAuth` handler's token-writing logic becomes reachable on the `cloudrun-sandbox` runtime. The `resetAuth` handler constructs a shell command with a heredoc:

  ```go
  "cat <<'SCION_TOKEN_EOF' > \"$TOKEN_DIR/scion-token.tmp\"\n" + req.Token + "\nSCION_TOKEN_EOF\n"
  ```

  If `req.Token` contains the literal line `SCION_TOKEN_EOF`, the heredoc terminates early and subsequent text is executed as shell commands. However:
  1. The `resetAuth` endpoint is authenticated (hub-to-broker internal API)
  2. Tokens are opaque strings (JWTs) that wouldn't naturally contain this pattern
  3. The `resetAuth` code is pre-existing and not modified by these commits
  4. On the sandbox runtime, `Exec()` calls `sandbox exec <id> -- <cmd...>` which passes arguments as a list, not through a shell. The `sh -c` invocation happens inside the sandbox where the attack surface is already the sandbox user.

  This is flagged because the implementation of `Exec()` widens the reachable attack surface. The actual `resetAuth` heredoc concern is a pre-existing issue.
- **Impact:** Theoretical. Would require an authenticated caller to send a maliciously crafted token that happens to contain the exact heredoc delimiter on its own line. Even then, execution is confined to the sandbox.
- **Recommendation:** No action required for this PR. The pre-existing `resetAuth` heredoc pattern should be hardened separately (e.g., use a randomized heredoc delimiter or write via stdin piping instead of heredoc).

---

### [INFO] No `--user` flag on sandbox exec -- runs as sandbox-configured user

- **Location:** `pkg/runtime/cloudrun_sandbox_runtime.go:874-881`, `pkg/runtimebroker/pty_handlers.go:578-613,1024-1059`
- **Description:** All `sandbox exec` invocations omit a `--user` flag because the sandbox CLI does not support one. Processes run as whatever user the sandbox image's entrypoint configures (the `scion` user via the omni-image). This is documented in the design doc and project log. The `execUser` parameter from the `CloudRunSandboxRuntime.ExecUser()` method returns `"scion"`, and the PTY handlers' `sanitizeExecUser` validation is bypassed in the cloudrun-sandbox path (the user value is never interpolated into the command).
- **Impact:** None currently. The sandbox enforces user isolation through its own mechanisms. This is a defense-in-depth note: if a future sandbox CLI version adds `--user` support, the integration should be updated to use it.
- **Recommendation:** Document the lack of `--user` enforcement in an inline comment at each exec site (already done). Revisit when the sandbox CLI evolves.

---

## Positive Observations

1. **No shell interpolation in command construction.** All `exec.CommandContext` calls pass arguments as separate list elements, not through shell expansion. This eliminates command injection via sandbox names, container IDs, or user-controlled values. This is the single most important security property of the exec implementation.

2. **All binary paths are absolute.** `cloudRunSandboxBin = "/usr/local/gcp/bin/sandbox"` and all in-sandbox paths use absolute paths (`/usr/bin/tmux`). No relative path or `PATH`-dependent resolution is used. This prevents path traversal and binary hijacking.

3. **Sandbox name sanitization is thorough.** `sanitizeSandboxName()` restricts names to `[a-z0-9-]`, truncates to 63 chars, strips leading/trailing hyphens, and falls back to `"sandbox"` for empty results. The regex-based approach is sound.

4. **Process group isolation for delete.** `Setpgid: true` creates a new process group for the delete command, and `killProcessGroup` sends SIGKILL to `-cmd.Process.Pid` (negative PID = process group). This correctly scopes the kill to the delete process tree and prevents killing unrelated processes. The nil-process guard prevents panics.

5. **Zombie prevention.** After `killProcessGroup(cmd)`, the code always reaps with `<-done` (which calls `cmd.Wait()`). In `reapOrphanedRunsc`, the 2-second timeout on `proc.Wait()` prevents hanging on non-child processes where `Wait()` cannot succeed.

6. **Watcher cancellation ordering.** The watcher is cancelled *before* the delete command starts, preventing TOCTOU races where the watcher observes a partially-deleted sandbox. The mutex properly guards the `watchCancels` map.

7. **Context propagation.** `exec.CommandContext` is used throughout, ensuring commands are cancelled when the parent context expires. The `watchSandbox` goroutine properly checks `ctx.Err()` before updating state.

8. **TERM environment variable is hardcoded.** `TERM=xterm-256color` is a constant string, not user-controlled. No injection vector exists through the TERM value.

9. **Resize values are type-safe.** `msg.Cols` and `msg.Rows` are `int` (from `PTYResizeMessage`), converted via `strconv.Itoa()` which only produces digit characters. No injection through resize parameters.

10. **Write-blocking logic is fail-closed.** On Cloud Run Services, `workspaceWriteBlocked()` blocks writes by default and only allows them when a recognized durable backend is configured. Unrecognized backend values fail closed.

---

## Recommendations

1. **[Medium priority]** Fix the substring matching in `reapOrphanedRunsc` to use path-segment-anchored matching (see finding above). This is the only finding that could cause real-world harm under normal operation.

2. **[Low priority]** Consider adding an input validation helper for sandbox IDs that can be called at the boundary of all public methods, providing defense-in-depth regardless of caller behavior.

3. **[Future]** When the sandbox CLI adds `--user` support, update all exec paths to enforce user specification.

4. **[Future]** The `resetAuth` heredoc pattern should be hardened in a separate PR (not in scope for this audit but reachable via the newly-implemented `Exec`).

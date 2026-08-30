# CI SQLite Test Gap — Sizing Inventory

**Base commit:** `f99de64d` (upstream/main)
**Date:** 2026-08-28
**Run by:** ca-msg-em9

---

## 1. The Finding

A currently-failing authorization test that CI structurally cannot observe:

```
ci.yml:104                                    →  make test-fast
Makefile:69                                   →  go test -tags no_sqlite ./...
pkg/hub/authz_agent_baseline_test.go:1        →  //go:build !no_sqlite
                                              →  TestTemplateResource_UATConfinement never compiles
                                              →  and that test FAILS on bare upstream/main today
```

This is not a hypothetical gap. The test exists, it asserts on an
authorization invariant (UAT confinement for global templates), it fails
right now on main, and CI has never seen the failure because the build
tag compiles the file out of the binary. The 3,358 test functions
described below are the blast radius — the size of the surface that
shares this structural blindness.

### File Inventory

**220 test files** across 6 packages.

| Package | Files |
|---------|-------|
| `pkg/hub` | 176 |
| `pkg/store/entadapter` | 34 |
| `pkg/secret` | 3 |
| `pkg/ent/entc` | 3 |
| `pkg/store/storetest` | 2 |
| `cmd` | 2 |

<details>
<summary>pkg/hub — 176 files (click to expand)</summary>

```
admin_maintenance_test.go            handlers_agent_test.go
admin_user_invite_test.go            handlers_agents_gcp_hubscope_test.go
agentrole_integration_test.go        handlers_agents_patch_passthrough_test.go
as_needed_resolution_test.go         handlers_auth_test.go
attachment_classify_test.go          handlers_authz_remediation_test.go
attachments_agent_test.go            handlers_broker_inbound_test.go
attachments_dm_test.go               handlers_broker_test.go
attachments_staging_test.go          handlers_chat_mute_pin_test.go
audit_authz_test.go                  handlers_chat_user_prefs_test.go
auth_broker_header_signed_test.go    handlers_chat_v2_test.go
authorize_test.go                    handlers_env_secrets_test.go
authz_agent_assign_baseline_test.go  handlers_envsecret_authz_test.go
authz_agent_baseline_test.go         handlers_gcp_identity_byid_test.go
authz_bypass_agents_test.go          handlers_gcp_identity_hubscope_test.go
authz_candelegate_test.go            handlers_gcp_identity_scoped_test.go
authz_integration_test.go            handlers_gcp_identity_test.go
authz_policy_determinism_test.go     handlers_github_app_sync_test.go
authz_project_owner_test.go          handlers_github_app_test.go
authz_request_test.go                handlers_github_app_webhook_test.go
authz_test.go                        handlers_health_summary_test.go
bootstrap_test.go                    handlers_integ_hooks_authz_test.go
broker_routing_test.go               handlers_integrations_activate_secrets_test.go
brokerauth_test.go                   handlers_integrations_test.go
brokerclient_test.go                 handlers_lifecycle_hooks_test.go
capabilities_test.go                 handlers_logs_test.go
chat_notification_cross_replica_test.go  handlers_message_delivery_test.go
chat_notifications_test.go           handlers_messages_test.go
clone_delete_handler_test.go         handlers_notifications_test.go
command_bus_test.go                   handlers_oidc_test.go
credential_revocation_test.go        handlers_permissions_test.go
delegation_ceiling_test.go           handlers_policies_phase1c_test.go
demo_policy_test.go                  handlers_policies_test.go
dispatch_exec_test.go                handlers_principals_test.go
dispatch_lifecycle_test.go           handlers_project_test.go
dm_injection_security_test.go        handlers_projects_gcp_cascade_test.go
embedded_broker_test.go              handlers_quota_test.go
envgather_hubscope_regression_test.go  handlers_runtime_brokers_test.go
envgather_resolution_test.go         handlers_scheduled_events_test.go
envgather_test.go                    handlers_schedules_test.go
events_integration_test.go           handlers_secret_base64_test.go
events_postgres_test.go              handlers_secret_patch_test.go
fs_safety_test.go                    handlers_session_metrics_test.go
handlers_agent_create_helpers_noauth_test.go  handlers_skills_discover_test.go
handlers_agent_create_helpers_test.go  handlers_skills_injection_test.go
handlers_agent_delete_authz_test.go  handlers_stopall_test.go
handlers_agent_messaging_test.go     handlers_teams_manifest_test.go
handlers_agent_metrics_test.go       handlers_test.go
handlers_agent_secret_getlist_test.go  handlers_user_templates_test.go
handlers_agent_secrets_test.go       handlers_users_core_test.go
harness_capabilities_test.go         project_agents_group_test.go
harness_config_bootstrap_test.go     project_cache_test.go
harness_config_file_handlers_test.go project_clone_test.go
harness_config_handlers_image_test.go  project_default_gate_test.go
harness_config_handlers_test.go      project_pre_start_hook_handlers_test.go
harness_config_precedence_test.go    project_settings_handlers_test.go
heartbeat_exit_code_test.go          project_settings_registry_test.go
heartbeat_timeout_test.go            project_settings_resolved_guard_test.go
httpdispatcher_envscope_test.go      project_settings_resolved_test.go
httpdispatcher_injection_mode_test.go  project_template_handlers_test.go
httpdispatcher_noauth_github_token_test.go  project_workspace_handlers_test.go
httpdispatcher_restart_env_test.go   quota_test.go
httpdispatcher_test.go               reconcile_test.go
httpdispatcher_transport_mode_test.go  resolve_secrets_test.go
hub_agent_defaults_ladder_test.go    resource_bootstrap_test.go
hub_agent_defaults_wire_test.go      resource_import_handler_test.go
hub_pre_start_hook_handlers_test.go  resource_source_test.go
lifecycle_hook_evaluator_test.go     resource_validate_test.go
lifecycle_hook_executor_test.go      role_binding_test.go
lifecycle_hook_integration_test.go   routeguard_ops_permission_test.go
maintenance_executors_test.go        routeguard_permission_test.go
messagebroker_test.go                routeguard_settings_test.go
nonce_store_test.go                  sa_assign_audit_wiring_test.go
notifications_integration_test.go    sa_assign_gate_wiring_test.go
notifications_test.go                sa_assign_hubscoped_test.go
oidckeys_test.go                     sa_existence_oracle_test.go
passthrough_gate_test.go             scheduler_dispatch_migration_test.go
platform_skills_seed_test.go         scoped_admin_bypass_test.go
port_forward_handlers_test.go        security_fixes_a6_test.go
seed_assign_policy_test.go           skill_multipart_test.go
seed_tombstone_test.go               skill_registry_handlers_test.go
server_test.go                       stalled_detection_test.go
signing_key_shared_test.go           system_handlers_test.go
skill_federation_test.go             template_bootstrap_test.go
skill_handlers_authz_test.go         template_file_handlers_test.go
skill_handlers_gh_cache_test.go      teststore_test.go
thinking_level_precedence_test.go    workspace_handlers_test.go
update_completion_test.go            workspace_storage_test.go
usermgmt_permission_test.go          wake_test.go
```

</details>

<details>
<summary>pkg/store/entadapter — 34 files (click to expand)</summary>

```
agent_role_migration_test.go         notification_store_test.go
agent_store_test.go                  policy_store_test.go
backfill_agents_group_test.go        project_pre_start_hook_migration_test.go
broker_affinity_test.go              project_pre_start_hook_store_test.go
broker_dispatch_store_test.go        project_store_test.go
brokersecret_store_test.go           quota_store_test.go
composite_migrations_test.go         reaper_test.go
composite_test.go                    schedule_store_test.go
conversation_store_test.go           scheduled_event_store_test.go
delegation_edge_backfill_test.go     secret_store_test.go
external_store_test.go               skill_injection_store_test.go
group_store_test.go                  skill_store_test.go
hubsetting_store_test.go             template_store_test.go
invite_flow_test.go                  user_allowlist_behavior_test.go
lifecyclehook_store_test.go          user_allowlist_oracle_test.go
locking_test.go                      main_test.go
maintenance_store_test.go            message_store_test.go
```

</details>

Other packages (8 files):
```
cmd/server_dispatcher_test.go
cmd/server_test.go
pkg/ent/entc/client_test.go
pkg/ent/entc/migrate_alpha_test.go
pkg/ent/entc/migrate_grove_to_project_test.go
pkg/secret/gcpbackend_test.go
pkg/secret/localbackend_test.go
pkg/secret/teststore_test.go
pkg/store/storetest/main_test.go
pkg/store/storetest/storetest_test.go
```

---

## 2. Test Function Counts

| Metric | Count | % of total |
|--------|-------|-----------|
| Total test functions in repo | 8,472 | 100% |
| Test functions compiled under `-tags no_sqlite` (CI) | 5,114 | 60.4% |
| **Test functions CI never compiles** | **3,358** | **39.6%** |

CI's `make test-fast` uses `-tags no_sqlite`, which compiles out the 220 test
files entirely. The remaining 5,114 functions run and produce the green
check mark. The 3,358 excluded functions are not skipped — they do not exist
in the compiled binary.

**Denominator note:** The total (8,472) counts only the root module
(`go test ./...`). The `extras/` directories (`scion-slack`, `scion-discord`,
`scion-teams`, `scion-telegram`, `scion-a2a-bridge`, `scion-chat-app`) each
have their own `go.mod` and are separate modules — they are never included
in `./...` from the repo root, so they are correctly excluded from both the
numerator and denominator.

---

## 3. Security / Authorization Tests — Zero CI Enforcement

**36 files, 440+ test functions** that enforce security invariants are gated
behind `//go:build !no_sqlite` and never execute in CI. Under
`go test -tags no_sqlite ./pkg/hub/ -run <TestName>`, the runner prints
`ok ... [no tests to run]` and exits 0.

**Verified** (instrument output on f99de64d):

```
$ go test -tags no_sqlite ./pkg/hub/ -run TestDMKeyIngress_UnauthorizedAgentCanInjectIntoForeignDM -count=1
ok   github.com/GoogleCloudPlatform/scion/pkg/hub  0.107s [no tests to run]

$ go test -tags no_sqlite ./pkg/hub/ -run TestOutboundMessage_RateLimitsFloodingAgent -count=1
ok   github.com/GoogleCloudPlatform/scion/pkg/hub  0.113s [no tests to run]

$ go test -tags no_sqlite ./pkg/hub/ -run TestTemplateResource_UATConfinement -count=1
ok   github.com/GoogleCloudPlatform/scion/pkg/hub  0.109s [no tests to run]
```

All three report PASS with zero assertions executed.

### 3a. Positive Control — the package still runs, these tests do not

The `[no tests to run]` output above only proves the build tag is responsible
if an _untagged_ test in the same package still runs under the same flags.
This confirms the package is not broken — only the tagged files are excluded:

```
$ go test -tags no_sqlite ./pkg/hub/ -run '^TestMaintenanceState_Defaults$' -count=1 -v
=== RUN   TestMaintenanceState_Defaults
--- PASS: TestMaintenanceState_Defaults (0.00s)
PASS
ok   github.com/GoogleCloudPlatform/scion/pkg/hub  0.107s

$ go test -tags no_sqlite ./pkg/hub/ -run '^TestAuthorize_IdentityKinds$' -count=1 -v
testing: warning: no tests to run
PASS
ok   github.com/GoogleCloudPlatform/scion/pkg/hub  0.107s [no tests to run]
```

`TestMaintenanceState_Defaults` lives in an untagged file and runs normally.
`TestAuthorize_IdentityKinds` lives in `authorize_test.go` (tagged
`!no_sqlite`) and does not exist in the binary. Same package, same flags,
same runner — the only difference is the build tag on the source file.

### Tier 1 — Direct Security Controls (files whose primary purpose is enforcing a security invariant)

| File | Funcs | Invariant |
|------|-------|-----------|
| `dm_injection_security_test.go` | 1 | **Cross-project DM injection**: proves an unauthorized agent can inject messages into a foreign user's DM conversation. Regression test for the B5 DM-key ownership check at message ingress. |
| `handlers_agent_messaging_test.go` | 3 | **Outbound message rate limiting**: ensures a flooding agent is rate-limited, unknown message types are charged as agent traffic, and transcript mirrors cannot starve agent message slots. Defense-in-depth for the B5 DM-key spoofing surface. |
| `authz_agent_baseline_test.go` | 15 | **Agent project-scope enforcement**: cross-project access denied, read-class boundary, empty-project denied. Includes `TestTemplateResource_UATConfinement` which **fails on bare main and CI has never seen it**. |
| `authz_test.go` | 28 | **Core authorization decisions**: admin bypass, owner bypass, direct user policy, default deny, deny effect, group membership, multi-policy resolution. The authorization engine's primary test surface. |
| `authz_candelegate_test.go` | 52 | **Delegation ceiling enforcement**: super-admin can delegate anything, scoped-admin cannot delegate wider, project-admin scope limits. 52 test functions covering every delegation edge case. |
| `authz_project_owner_test.go` | 23 | **Project owner privilege scope**: non-creator owner update, regular member cannot update, scoped admin UAT requires independent grant. Prevents privilege escalation via project ownership. |
| `authz_bypass_agents_test.go` | 15 | **Agent bypass controls**: denials for bypass attempts, broker-caller denied, create-route parity. Ensures agents cannot circumvent authorization. |
| `authz_agent_assign_baseline_test.go` | 10 | **Agent assignment authorization**: own-project allowed, cross-project denied, resource-type boundary, action boundary. Controls which agents can be assigned where. |
| `authz_integration_test.go` | 9 | **End-to-end authorization evaluation**: user direct policy, default deny, scope override, project-scoped policy confinement, agent policy. Full-stack authz path test. |
| `authz_policy_determinism_test.go` | 6 | **Policy evaluation determinism**: ordering, priority precedence, kind precedence, local override, resource override. Ensures identical inputs always produce identical authorization decisions. |
| `authz_request_test.go` | 5 | **Request-level authorization**: UAT cannot use admin bypass, federated identities have explicit outcomes, fail-closed on store error for mutating actions. |
| `credential_revocation_test.go` | 15 | **Credential lifecycle**: revoked tokens denied before expiry, deleted/suspended agent tokens denied, refresh from revoked token fails. Ensures revocation is immediate and complete. |
| `scoped_admin_bypass_test.go` | 7 | **Scoped admin privilege boundary**: admin-mode middleware rejects scoped admin, stop-all rejects scoped admin, broker secret rotation rejects scoped admin. Prevents scoped admins from reaching hub-wide operations. |
| `security_fixes_a6_test.go` | 6 | **A6 security fix regressions**: unauthenticated list returns empty (not error), agent identity gets forbidden on admin endpoints. Regression tests for specific security patches. |
| `delegation_ceiling_test.go` | 35 | **Delegation ceiling enforcement**: user→agent chains, agent→agent chains, fail-closed minting. Prevents privilege amplification through delegation chains. |
| `dm_injection_security_test.go` + `authorize_test.go` + `audit_authz_test.go` | 1+14+14 | Combined: DM injection guard, authorization identity-kind handling, denial logging, and decision audit trail (allow/deny recorded, no secrets in audit, sampling). |

### Tier 2 — Permission and Route Guards

| File | Funcs | Invariant |
|------|-------|-----------|
| `handlers_permissions_test.go` | 40 | Group and permission CRUD with authorization checks (non-admin denied, reserved prefix rejection). |
| `handlers_envsecret_authz_test.go` | 47 | Env/secret authorization: admin vs member access, unauthenticated denied, scope isolation per operation. |
| `skill_handlers_authz_test.go` | 16 | Skill authorization: owner allowed, non-member denied, hub-member access rules. |
| `routeguard_permission_test.go` | 1 | Route guard uses permission-based path (not role-based). |
| `routeguard_ops_permission_test.go` | 1 | Ops routes use permission checks. |
| `routeguard_settings_test.go` | 4 | Route guard settings conversion; UAT denied for hub-level resources; hub permissions have no UAT scope. |
| `handlers_agent_delete_authz_test.go` | 1 | Agent deletion requires both agent-caller scope and project gate. |
| `handlers_authz_remediation_test.go` | 2 | List endpoints filter unauthorized items; agent/workspace routes enforce resource permissions. |
| `handlers_integ_hooks_authz_test.go` | 1 | Integration hooks use permission conversion (not legacy admin check). |
| `usermgmt_permission_test.go` | 1 | User management uses permission conversion. |
| `role_binding_test.go` | 24 | Role definition seeding, super-admin has all permissions, role binding CRUD, scope enforcement. |

### Tier 3 — Authentication, Identity, and Token Infrastructure

| File | Funcs | Invariant |
|------|-------|-----------|
| `handlers_oidc_test.go` | 9 | OIDC discovery, JWKS endpoint, unauthenticated access patterns, agent identity token issuance. |
| `oidckeys_test.go` | 30 | RSA key generation, PEM round-trip, key ID computation, JOSE RS256 signing, key rotation. |
| `signing_key_shared_test.go` | 6 | Signing key lifecycle: require-stable refuses generation, shared secret is replica-portable, deterministic derivation. |
| `nonce_store_test.go` | 5 | Nonce cache: duplicate detection, concurrent duplicate detection, purge expired. Replay attack prevention. |
| `auth_broker_header_signed_test.go` | (in file) | Broker header signature validation. |
| `brokerauth_test.go` | (in file) | Broker authentication flows. |
| `httpdispatcher_noauth_github_token_test.go` | 1 | GitHub token survives no-auth build path (token not dropped). |
| `sa_existence_oracle_test.go` | 7 | Service account existence oracle: missing/unreachable/malformed all produce same answer (no information leak). |
| `user_allowlist_behavior_test.go` | 6 | User allowlist: email case-insensitive, bulk add idempotent, invite stats. |
| `user_allowlist_oracle_test.go` | 1 | Allowlist oracle CRUD parity across store implementations. |

---

## 4. Why the Tag Exists

**Stated rationale: memory.** The Makefile comment on the target CI actually
calls is explicit:

```makefile
## test-fast: Run tests without SQLite (lower memory usage)
test-fast:
    @go test -tags no_sqlite ./...
```

The tag was introduced in commit `71275d56` ("fix: resolve spurious go vet
OOM by gating sqlite driver with no_sqlite tag"). The commit message says the
pure-Go SQLite driver (`modernc.org/sqlite`) is memory-intensive, and the tag
lets resource-constrained environments skip it. This was never about CGO —
`modernc.org/sqlite` has been the driver since it was first added, and
`mattn/go-sqlite3` (which does require CGO) appears in `go.mod` as a
transitive dependency but was never imported by the driver file (`git log
--follow pkg/ent/entc/driver_sqlite.go` confirms this).

**The mechanical chain:**

1. `pkg/hub/handlers_test.go` defines `testServer(t)` which calls
   `newTestStore(":memory:")` — an in-memory SQLite database.
2. The SQLite driver is registered via `pkg/ent/entc/driver_sqlite.go`, which
   imports `modernc.org/sqlite` (pure Go, no CGO needed).
3. Under `-tags no_sqlite`, `driver_nosqlite.go` replaces it — a stub that
   registers nothing. `newTestStore(":memory:")` would fail with
   "sqlite driver not registered".
4. The `!no_sqlite` build constraint on test files prevents them from
   compiling at all, avoiding the failure. They are not skipped at runtime —
   they do not exist in the binary.

**Production does not use SQLite.** The production binary runs with Postgres
(`CGO_ENABLED=0` in `Makefile` and `build-release.yml`). The SQLite driver
is purely a test convenience: fast, in-process, no external dependency.

**Does the reason still hold?** The memory concern is real and unmeasured.
The `modernc.org/sqlite` driver generates Go code from C, producing large
intermediate representations during compilation. Running 176 test files that
each spin up in-memory SQLite databases may push peak RSS above what a
GitHub Actions runner provides. This has not been measured (see option (i)
below).

---

## 5. Option Costs

### Option (i): Run the untagged suite in CI as a second job

**Cost:** ~1 day of work + one unresolved unknown.

- Add a `full-test-suite` job to `.github/workflows/ci.yml` with
  `continue-on-error: true` (non-blocking, informational).
- Job runs `go test -json -timeout 30m ./...`, parses results, writes summary.
- No code changes to production or test files, no tag removal.
- Immediate visibility into 3,358 functions that are currently dark.
- Two pre-existing failures would surface immediately:
  - `TestFixtureCoverage` (stale table count assertion, trivial fix)
  - `TestTemplateResource_UATConfinement` (authz policy regression, needs investigation)

**Unresolved: peak RSS on the CI runner.** The tag exists because of memory,
not CGO (see §4). The `modernc.org/sqlite` driver is memory-intensive during
compilation and test execution. GitHub Actions `ubuntu-latest` runners
provide 7 GB RAM. Running the full untagged suite may OOM the runner —
this is the exact scenario the tag was introduced to prevent. Before this
option can be relied on, someone must measure peak RSS of `go test ./...`
(without `-tags no_sqlite`) and compare it to the runner's memory limit.

**Cost to resolve:** ~2 hours. Run the untagged suite on a runner (or
locally under `ulimit -v` / `cgexec`) and record peak RSS. If it fits in
7 GB, option (i) is viable as stated. If it does not, the job would need
`-p 1` (serial package testing) or package sharding, which increases
wall-clock time and complexity.

Wall-clock CI time increase (if memory is not a constraint): ~5-8 minutes
(pkg/hub alone takes ~5 min). Runs in parallel with existing jobs, so may
not extend total pipeline time.

### Option (ii): Drop the tag where it is unnecessary

**Cost:** ~3-5 days of careful work, medium risk.

- Audit each of the 220 files to determine which truly need SQLite and which
  could be restructured to use mock stores or Postgres test containers.
- Most `pkg/hub` tests call `testServer(t)` which hardcodes `:memory:` SQLite.
  Removing the tag requires either: (a) making `testServer` backend-agnostic,
  or (b) providing a Postgres test harness, or (c) accepting that the tests
  require SQLite and keeping the tag on those files.
- Risk: refactoring the test harness touches 176 files in `pkg/hub`. Any
  mistake breaks the existing passing test suite.
- The `pkg/store/entadapter` tests (34 files) already test against real
  databases and may be easier to untag.
- Incremental approach possible: untag packages with few files first
  (`cmd`, `pkg/secret`, `pkg/ent/entc`), then tackle `pkg/hub` later.

### Option (iii): Leave it and accept the gap

**Cost:** Zero implementation cost. Ongoing risk cost.

- 39.6% of the test suite remains dark in CI.
- 440+ security/authz test functions provide no CI enforcement.
  Regressions in authorization, credential revocation, delegation
  ceilings, and injection prevention land without any signal.
- The `TestTemplateResource_UATConfinement` failure demonstrates the
  risk is real: it fails on bare main today, CI has never seen it.
- Every new `!no_sqlite` test file widens the gap silently.
- Cost scales with time: the longer the gap persists, the more
  security invariants accumulate without CI coverage.

---

## 2026-08-30 — CORRECTION TO THIS DOCUMENT'S PREMISE (ca-msg-arch)

**This inventory was built on a false premise and the conclusions need re-reading.**

The premise was: CI runs `go test -tags no_sqlite ./...`, therefore `!no_sqlite` tests **never
execute** in CI. Verified against `.github/workflows/ci.yml` today — that is wrong.

- `ci.yml:124` — `make test-fast` (`-tags no_sqlite`). **Blocking.**
- `ci.yml:148` — `go test -count=1 -timeout 30m ./...` **with sqlite**, in job `test-full-suite`,
  titled *"Full Test Suite (reporting only)"* and marked **`continue-on-error: true`**. Executes;
  **cannot fail the build**; result uploaded as an artifact.

**Correct statement: sqlite-tagged tests DO run in CI and CANNOT block a merge.** The distinction
that matters is authority, not execution.

**This makes the gap worse, not better.** A test that never runs is visibly absent. A test that runs
advisory-only *looks* covered — it appears in a log, it passes, and nothing in the PR view
distinguishes it from a gating result. Everywhere this document says "never runs", read
"runs without authority to fail".

**Anyone using this inventory to size the risk should re-read it on that basis.** An entry
previously read as "untested" is more likely "tested, reported, and ignorable."

### New entries — Phase 4 / DEF-12 (branch `scion/ca-msg-p4a`)

| Item | Tag | Lane | Note |
|---|---|---|---|
| `cmd/server_backfill_test.go` — all 8 tests, incl. AC-12-2/3/4/5, pre-upgrade rejection, flag wiring | `!no_sqlite` | advisory | Excluded **by default**, not opt-in. The expensive kind. |
| `cmd/server_backfill_volume_test.go` | `volume_test && !no_sqlite` | neither | Opt-in; runs only under explicit `-tags volume_test`. Self-documenting. |
| `cmd/server_foreground_backfill_test.go` — AC-12-1, AC-12-7 | none | **blocking** | |
| `pkg/messaging/backfill_test.go` — incl. `TestBackfill_SameTimestampMessages` | none | **blocking** | **DEF-81's regression guard is in the blocking lane.** The one that most needed to be. |
| Flag-default guard (`--execute` defaults false) | none (required addition) | **blocking** | Required of p4a: sqlite-free cobra assertion. Most dangerous regression in the feature — an operator previewing would mutate — and its only guard was advisory. |

**Not remedied by tag-stripping.** `TestBackfillDefaultIsDryRun_FlagWiring` genuinely needs sqlite
(opens a store, verifies rows unstamped). Stripping `!no_sqlite` to make a test run is forbidden;
**adding a test that never needed sqlite is a different act** and is what was directed.

Sanctioned remedy for the rest remains a CI job that runs WITH sqlite **and can fail** — i.e.
dropping `continue-on-error` from `test-full-suite`, which is `ci-fix-lead`'s call, not mine. Noting
it here so the option is on the record rather than rediscovered.

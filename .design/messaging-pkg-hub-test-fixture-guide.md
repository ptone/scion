# pkg/hub test fixtures — what a new agent needs before writing or trusting a test

Source: `ca-msg-e2rb` exit interview, 2026-08-30, after rebasing Tranche E onto main @ 8d8bef705.
Written down because it was learned expensively and would otherwise retire with the agent.

## The access-control trap (this is the important one)

- `testServer()` runs `seedDefaultPoliciesAndGroups`, `backfillProjectMemberReadPolicies`,
  `seedRoleDefinitions`, `BackfillRoleBindings`, `ReconcileSuperAdminBindings`. These change often.
- **A project created with `s.CreateProject()` gets NONE of the project-level access-control setup.**
  Post-#1414 it is invisible to all non-admin users: the narrowed `hub-member-read-all` excludes
  project and agent resource types, so with no members group and no per-project read policy, only
  the admin bypass (`authz.go:490`) grants access.
- If you need realistic access control, call `createProjectMembersGroupAndPolicy()`. Pattern:
  `setupDemoPolicyTest()`.
- **The dev user is ALWAYS admin** — `DevUser.Role()` returns `"admin"`. `doRequest()` therefore
  never tests a normal user path. For non-admin behaviour use `doRequestAsUser()` with a user from
  `s.CreateUser()`.
- **Corollary, and the reason DEF-74 exists:** any project-access test that does not set up a members
  group is relying on admin bypass or resource ownership. It proves nothing about non-admin access.
  This is not marked anywhere in the code.

## Naming trap

`store.VisibilityPrivate` (the CONSTANT) still exists and is used by skills/templates.
`store.Project.Visibility` (the FIELD) was removed by #1414. **Same name, different fate.**
A grep for the constant will make you think the field survived.

## Gates

- `!no_sqlite` build tags make your tests **invisible to `make test-fast`**. The real gate is
  `go test ./pkg/hub/...` with no tags. Budget 5-7 min per run.
- The divergence counter is a **global atomic** with three writers. Assert with `fallbackDelta()`.
  **Never `t.Parallel()`** a test that touches it.
- A green gate means no race was detected *on that run*, not that none exists.

## Open, unverified (do not assume these were checked)

- Whether any test in the file is silently `t.Skip()`-ing under environment conditions. Compiling
  into the binary is not the same as executing.
- Whether other pkg/hub files write `messaging.DivergenceMetrics` concurrently with these tests.

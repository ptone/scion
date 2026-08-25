# Consensus Advisory: Foundations for the Permission System

**Project:** permissions-foundation
**Authors:** review-arch (final editor), codex-auth-review
**Date:** 2026-08-22
**Process:** Two independent reviews, then 4 rounds of debate.
Convergence at round 2. Signoff at round 4, with no dissent. Two
editorial passes after signoff: one for correctness, one to interpret
the sponsor decision. Both reviewers signed off on the final text.
**Status:** Open question 1 resolved by the sponsor on 2026-08-22.
Questions 2 and 3 are open. The document is otherwise final.
**Language:** ASD-STE100 Simplified Technical English.

---

## 1. Problem and Goals

The team wants to build user-facing features on the policy engine. The
planned features are:

- Give admin rights to a user for a subset of hub resources.
- Give user access tokens (UAT) more granular permissions.
- Enforce limits on users by group membership level.

The sponsor asked two questions:

1. What foundational or structural changes must we make before we build
   on this system?
2. Are there concerns with the distinction between humans and agents?

This advisory answers both questions. It gives a repair order.

**Success criteria.** A developer can read this document and build the
foundation. Each defect has a file and a line number. Each repair has
an acceptance test.

---

## 2. Non-Goals

- This document does not design the user interface for the new features.
- This document does not select an external policy engine.
- This document does not specify the final permission names.
- This document is not an implementation. The code below is pseudocode.

---

## 3. Summary of the Answer

**Do not build the new features on the current foundation yet.**

The engine has eight defects. Three of them are live escalations. You
must repair those three first. The other five are structural. You must
repair them before the new features, because the new features make each
defect worse.

The human and agent distinction is correct. Keep it. The **relationship**
between a human and an agent is missing. That is the real problem.

### 3.1 The defects

| ID | Defect | Severity | Live today |
|----|--------|----------|-----------|
| D1 | Policy conflict resolution is nondeterministic and underspecified | Structural | Yes |
| D2 | Enforcement is opt-in per handler. Omission grants access | Structural | Yes |
| D3 | Three or more permission vocabularies disagree | Structural | Yes |
| D4 | Admin is a code bypass, not a grant | Structural | Yes |
| D5 | There is nowhere first-class to store a limit | Structural | Yes |
| D6 | A readonly agent can create a full-role agent | **P0** | **Yes** |
| D7 | A scoped UAT passes the hub admin check | **P0** | **Yes** |
| D8 | Project admin escalation by slug squatting | **P0** | **Yes** |

### 3.2 The repair order

| Phase | Content | Blocks the new features |
|-------|---------|------------------------|
| P0 | Patch D6, D7, D8. Add a route classification test | Yes |
| P1 | Boundary semantics, permission registry, declarative guards, roles and bindings, revocation | Yes |
| P2 | Limits, token caveats, user interface | No |

---

## 4. The Three P0 Defects

Repair these three before any architectural work. Each is exploitable
today. Each has a small patch.

### 4.1 D6: A readonly agent can create a full-role agent

**The chain.**

1. An agent with the `readonly` role creates a scheduled event of type
   `dispatch_agent`. The handler checks only `project:read`.
2. The scheduler fires. It builds a new agent at `server.go:2866-2890`.
   It sets no `Ancestry`. It sets `AppliedConfig` to an empty struct,
   so `AgentRole` is the empty string.
3. `agentRoleAndScopes` at `httpdispatcher.go:207-228` reads the empty
   role. It returns `AgentRoleFull`. The comment says "Default to full
   for pre-role agents."

**The result.** The new agent holds the full role. It has no ancestry,
so no ancestry check can constrain it. The parent role ceiling does not
apply. The project maximum does not apply.

review-arch found this. codex-auth-review verified it independently in
the source after round 1.

**The patch, part 1 — the unsafe default.** An absent role must resolve
to the lowest role, not the highest. Change the default in
`agentRoleAndScopes`, in `ScopesForRole` (`agentrole.go:66-67`) and in
`roleOrdinal` (`agentrole.go:85-86`). Migrate the existing rows that
hold an empty role, so that the change does not remove access from live
agents.

**The patch, part 2 — the scheduled path.** An earlier draft of this
advisory said "make the scheduler copy the ancestry and the role of the
creating agent". That is **not implementable as stated**.
`ScheduledEvent` (`store/models.go:1752-1764`) stores `CreatedBy` and
nothing else about the principal. It records no principal kind, no
ancestry and no role. So the scheduler cannot tell whether the creator
was a human or an agent, and a human creator has no agent role at all.
Authority can also change between the schedule time and the fire time.

For P0, do this instead:

- Authorize `dispatch_agent` as `agent:create`, both when the schedule
  is created and again when it fires.
- Until the creator kind and the delegation context are persisted,
  either refuse an agent-authored dispatch schedule, or give the
  resulting agent the lowest role explicitly.

The durable model stores the creator principal kind and the delegation
context on the event. At fire time it derives a role that is at most the
current entitlements of the delegator, subject to the project maximum
and the template boundary.

**Note:** this means Phase 0 is not free of data-model change. See
section 7.

### 4.2 D7: A scoped UAT passes the hub admin check

`requireAdmin` at `authorize.go:254-282` reads `Role()` from the
identity. `ScopedUserIdentity` embeds `UserIdentity`
(`identity.go:88-92`). So it inherits `Role()` from the user.

**The result.** A project-scoped, read-only token of an admin passes the
hub admin check. It can write hub policies. There are 14 call sites.

Exactly one place in the codebase rejects a `ScopedUserIdentity`:
`requireHubAdmin` at `hub_pre_start_hook_handlers.go:40-95`. Its comment
states the escalation. So the hazard is known but is not fixed
generally.

**The patch.** An earlier draft was self-contradictory here. It said
"reject unless the token holds an explicit hub-admin scope" and then
said "use `requireHubAdmin`", which rejects every scoped identity. The
agreed rule is the strict one:

**Reject every UAT at a role-only admin gate.** Apply the existing
`requireHubAdmin` logic at all 14 sites. Do not add a hub-admin token
scope as part of this patch.

A hub-scoped credential may exist later. If it does, it must arrive
through the canonical permission pipeline (F1.1, F1.2), as an ordinary
permission subject to intersection with the principal. It must never
arrive as a role bypass.

This rule became more important after the sponsor's decision of
2026-08-22. The super-admin bypass is now the only absolute authority in
the system, so it is the highest-value target. A bearer token must never
carry it.

### 4.3 D8: Project admin escalation by slug squatting

This defect is new in this advisory. review-arch found it during the
verification of a finding from codex-auth-review.

**The chain.**

1. `createGroup` at `handlers_groups.go:148` has **no authorization
   call at all**. In the same file, update (`:326`), delete (`:383`),
   add-member (`:487`) and remove-member (`:722`) are all guarded.
   Create is not. This is a live unguarded write, and an instance of D2.
2. The caller supplies the slug verbatim (`:178-181`). The creator
   becomes a member with `GroupMemberRoleOwner` (`:217-227`).
3. An attacker creates an explicit group with the slug
   `project:NAME:members`, before a project with slug `NAME` exists.
4. Later, any user creates a project with slug `NAME`.
   `createProjectMembersGroupAndPolicy` finds the slug conflict. It
   **adopts** the existing group. It sets `ProjectID` to the new project
   and saves it (`handlers_projects_core.go:596-612`).
5. `isProjectOwnerOrAdmin` (`authz.go:641-662`) lists the explicit
   groups of the project (Limit 10). It returns true if the user holds
   owner or admin in **any** of them.

**The result.** Any authenticated user gets owner rights on a project
that they do not belong to.

**Reachability notes.** `CreateGroupRequest` has no `ProjectID` field,
and `UpdateGroupRequest` has none either. Only two server-internal sites
set `Group.ProjectID` (`handlers_projects_core.go:489` and `:564`). So a
caller cannot attach a group to a project directly. The slug adoption
path is the only route. `deleteProject` does delete the project groups
(`:2531-2537`), so the delete-and-recreate variant is mostly closed.
That cleanup is best-effort, uses Limit 100 and only logs failures, so
it is not airtight. The forward squat is fully open.

**The patch.** Two layers. Apply the immediate layer now. Apply the
durable layer in Phase 1.

*Immediate — defense in depth (Phase 0):*

- (a) Add an authorization check to `createGroup`.
- (b) Reserve the `project:` slug namespace against user creation.
- (c) On a slug collision, refuse to adopt the group, unless its system
  marker and its `ProjectID` already match the project.
- (d) Narrow `isProjectOwnerOrAdmin` to the canonical membership
  relation. Do not accept any explicit group.

*Durable (Phase 1):*

Do not identify the members group by its slug. A slug is user-writable,
so it is a weak key. Use a system-managed project-membership relation,
or a dedicated group type, keyed by the **immutable project ID**. Create
it transactionally with the project. Never adopt a user-created group.

**A second, non-security defect in the same code.** The Limit 10 scan of
arbitrary explicit groups is not a reliable lookup, even with no
attacker. A project with more than 10 explicit groups may not return the
members group at all. Then a legitimate owner is refused. The canonical
relation removes this defect as well.

---

## 5. The Five Structural Defects

### 5.1 D1: Policy conflict resolution is nondeterministic and underspecified

*(This defect was originally titled "the engine cannot express a
non-overridable rule". After the sponsor decision of 2026-08-22, that
inability is an accepted capability limit, not a defect. See section
5.1.1. What remains is the nondeterminism described below.)*

`evaluatePolicies` at `authz.go:455-503` uses last-match-wins:

```go
if newLevel > matchedLevel {
    matched = &d          // a more specific scope replaces the match
} else if newLevel == matchedLevel {
    matched = &d          // the same scope: the last policy seen wins
}
```

`policy.Effect` is read exactly once, at `:473`, to set
`Decision.Allowed`. There is no deny-overrides rule.

**The correct framing.** A specific allow that beats a broad deny is not
defective by itself. The tests and the design chose local override on
purpose, to give projects autonomy. codex-auth-review is right on this
point, and review-arch withdrew the earlier wording.

**The real defect is representational.** One `Policy` row represents
several different things:

1. A default.
2. A delegated grant.
3. An ordinary deny.
4. A non-overridable organizational boundary.

The engine cannot tell them apart.

**Sponsor decision, 2026-08-22.** Item 4 is **not wanted**. A project may
override a hub rule. Only the hub-admin role, which the sponsor plans to
rename **super-admin**, holds an absolute capability, and it holds it by
bypass. See section 5.1.1.

So item 4 leaves the defect list. The conflation of items 1 to 3 stays,
because the engine cannot resolve a tie between them deterministically.
That is what D1 now means.

**Two supporting problems.**

- **No total order.** `policy_store.go:459-466` orders by `scope_type`
  then `priority`. Both generated helpers are plain `sql.OrderByField`
  with no direction and no third key. SQL therefore gives no guaranteed
  tie order. On SQLite we observed the result flip with the row insert
  order over six runs. We make no claim about PostgreSQL beyond the
  absence of a guarantee.
- **Shipped seed data reaches the tie.** The `hub-member-read-all`
  policy is hub-scoped at priority 0. `CreatePolicyRequest.Priority` is
  a plain int, so a new policy defaults to 0 as well. No test covers
  the equal scope and equal priority case.

Also: `Priority` is stored and sorted on, but the engine never reads it
to decide. `SourceIPs` is stored and never evaluated.

**The repair.** Do **not** build `PolicyBoundary`. The sponsor declined
it. Keep local override. Do the rest:

- Give the evaluation a total order. Add a third sort key, such as the
  policy ID, so that a tie resolves the same way every time.
- Make the engine read `Priority`, or remove the field. Today it is
  stored and sorted on, but never read to decide.
- Reject `SourceIPs` and any other condition that is not enforced, until
  it is enforced.
- Distinguish a default from an explicit deny, so that a tie between
  them has a documented outcome.

### 5.1.1 Where organizational constraint now lives

The sponsor's answer moves constraint from **evaluation time** to
**admission time**. This is a coherent choice, and arguably a cleaner
one. The consequence must be explicit.

The hub can no longer say "no project may allow X" through a policy.
Instead the hub must prevent the grant from being created at all.

**One gate, on every authority-creating path.** An earlier draft named
only the grantable-role check, and also named the project maximum role.
That was too narrow. The project maximum constrains agent roles only; it
is not general admission control. A single gate — call it
`CanDelegate` or `GrantAuthorizer` — must cover **every** path that
creates authority:

1. A direct role binding.
2. A group binding, and a nested group binding.
3. Agent delegation.
4. A custom role definition, and any update to one. Defining a wide role
   is equivalent to granting it.
5. Raw policy create, update and bind, **if** a scoped admin can reach
   those APIs.

Item 5 matters most. A subject who can author a raw allow policy
bypasses the grantable-role check completely. Whatever the gate refuses,
a policy can grant.

**Current state, verified.** Today the policy write APIs are gated by
`requireAdmin`, at the dispatcher (`handlers_policies.go:104`, covering
create, and `:309`, `:332`, `:400`, `:418`, `:499`). So policy authoring
is reachable only through the admin role bypass. A scoped admin cannot
author a raw policy today, because scoped admins do not yet exist.

**The design constraint.** Policy authoring must stay restricted to
super-admin, **or** it must pass through the same admission gate as
every other authority-creating path. It must not become an ordinary
permission that a scoped admin can hold, unless the gate covers it.

**This makes the gate load-bearing.** It was important before. It is now
the only thing that stops a project, or a scoped admin, from granting
more than the hub intends. It must be tested on every path above.

**One capability is now absent by design.** There is no way to express a
non-overridable organizational prohibition, for example "no agent in any
project may reach the public internet". The super-admin bypass does not
supply this. A bypass grants; it does not forbid. If such a rule is
needed later, the place to add it is the admission-time check, not
policy precedence. We record this as a known limit, not as a defect.

### 5.2 D2: Enforcement is opt-in per handler

A handler must call `authorize` itself. If the call is absent, the
request succeeds. The input audit lists about 30 unguarded routes. That
is not 30 bugs. It is one bug, 30 times. D8 shows the cost.

The `if userIdent != nil` pattern makes this worse. A nil identity skips
the check instead of failing.

**The repair.** Declare the required permission for each route in a
route table. Enforce it in one place. Add a test that fails when a route
has no declaration. The test is the durable part. Without it, the next
new route repeats the defect.

### 5.3 D3: The permission vocabularies disagree

There are at least three vocabularies, and two hardcoded lists:

- 19 `Action` constants.
- 11 `AgentTokenScope` constants.
- 13 UAT scope strings.
- A hardcoded list in the web user interface.
- A hardcoded list in the CLI.

`enforceUATConstraints` at `authz.go:592-610` builds the scope by string
concatenation: `resource.Type + ":" + string(action)`. So the UAT
vocabulary is derived by accident, not by design.

**Observed consequences.**

- The UAT scopes `agent:start`, `agent:stop` and `agent:message` do
  nothing. `authorizeAgentLifecycle` checks `ActionAttach`
  (`authorize.go:227`), so the caller needs `agent:attach`.
- `agent:dispatch` is never produced by any code path. It is the
  headline example in the CLI documentation.
- The web interface omits `project:update` and `agent:port_access`.

Granular tokens are the sponsor's explicit goal. You cannot deliver
granularity on a vocabulary that does not agree with itself.

**The repair.** Create one canonical registry of permissions and
resource types. Generate the UAT scopes, the agent scopes, the user
interface list and the CLI list from it. Add a test that fails when a
scope has no enforcement site.

### 5.4 D4: Admin is a code bypass, not a grant

`checkAccessForUser` at `authz.go:127` returns allow when
`Role() == "admin"`. There are 37 further hand-rolled admin checks.

**The result.** Admin is not expressible as data. You cannot grant a
subset of it. The sponsor's first goal is precisely a subset: admin
rights over some hub resources. The current shape cannot express it.

**The repair.** Move admin into the binding model.

```
PermissionDefinition   # canonical permission, from the D3 registry
RoleDefinition         # a named set of permissions
RoleBinding            # subject + role + scope
```

Migrate `User.Role` and the project role bypasses into bindings. Keep
`GroupMembership.role` for group governance only. Do not use it for
authorization. Make project bindings first-class.

Add a **grantable-role** check. A scoped admin must not grant a role
that is wider than the role that they hold. Without this check, a
subset admin promotes themselves on the first day.

### 5.5 D5: There is nowhere first-class to store a limit

The sponsor wants limits by group membership level. There is no
first-class schema for a limit, and no race-safe admission path.

- `hub_settings` has a UNIQUE constraint on `section`. One row per
  section.
- `PolicyConditions` has no numeric field.
- `GroupMembership.role` is a three-value enum. It cannot carry a
  number.
- The `agents` table has one index, so counting agents per owner is a
  full scan.
- `audit.go` writes to slog and returns nil. There is no queryable
  record.

We note the earlier wording was too strong. A per-group JSON map or a
synthetic section key **could** encode limits. Both are poor design. The
accurate statement is that there is no first-class schema and no
race-safe admission path.

**The repair.** Build limits outside the authorization engine. Do not
use policy priority for quotas.

```
LimitDefinition    # what is limited, and the unit
EntitlementBinding # subject or group -> limit value
UsageReservation   # atomic reserve and release
```

Define a deterministic merge rule per limit, for a subject that matches
several bindings. Add the index needed for the counting query.

---

## 6. The Human and Agent Question

**Keep the distinction.** A human and an agent differ in ways that
matter. A human authenticates interactively. An agent holds a bearer
token and acts unattended. An agent is created by somebody. A human is
not. Merging the two would remove information that the engine needs.

**The relationship is missing.** That is the concern.

### 6.1 The delegation ceiling is not connected

```go
func ResolveEffectiveRole(requested AgentRole, userHubRole string,
                          projectMax AgentRole) AgentRole {
    userCeiling := AgentRoleFull
    return minRole(requested, userCeiling, projectMax)
}
```

`agentrole.go` accepts `userHubRole` and never reads it. `userCeiling`
is hardcoded to `AgentRoleFull` in two places: `agentrole.go:121` and an
inline reimplementation at `handlers_agents_core.go:626-647`.

**The result.** An agent is not bounded by the person who created it.
The hook exists. It is disconnected. This is the single most important
structural gap in the human and agent relationship.

### 6.2 The two paths have diverged

`checkAccessForUser` (`authz.go:118-234`) and `checkAccessForAgent`
(`authz.go:237-384`) are separate. `checkAccessForAgent` never loads the
creating user. So it cannot apply a user ceiling even if one existed.

The two paths have drifted apart. Each new rule must be written twice,
and in practice is not.

### 6.3 Ancestry is provenance, not authority

`OriginUserID()` returns `Ancestry[0]` with no type check
(`identity.go:140-145`). The chain records where an agent came from. It
does not bound what the agent may do.

**The agreed rule.** Bound every edge, not only the origin.

> The effective authority of an agent is at most the current
> entitlements of its delegator. There must be an explicit grant at each
> edge. The project maximum, the template boundary and the credential
> caveats all apply.

"Current" is load-bearing. If a user loses a permission, every agent
below them must lose it too, at the next decision.

### 6.4 There is no agent token revocation

The agent JWT is HS256 with a 10-hour lifetime. There is no database
lookup at authentication. There is no `jti`. There is no denylist.
Refresh is unbounded.

**The result.** "Revoke an agent" is not true today. It cannot become
true by a user interface change. It needs a check at authentication
time.

**Implementation note, 2026-08-25.** F1.8 shipped in Phase 1H. It stores
a hash of the `jti` and checks it in the auth middleware. Two follow-ups
came out of a review of that work.

*A latent bypass. Not live.* `RefreshAgentToken`
(`agenttoken.go:225`) calls `ValidateAgentToken`, which checks the
signature and the expiry only. It never reads the credential store. It
then mints a **new** `jti` and records it as a fresh, unrevoked
credential. Because revocation is keyed on the `jti` hash, any path that
mints a new `jti` escapes an earlier revocation.

This was **not exploitable** when found. `RefreshAgentToken` had no
non-test caller, and `/api/v1/auth/refresh` is the user token path, not
the agent path. It was recorded as a trap, not a defect.

**Closed at `6adbf0eb`** on `scion/auth-refactor`. `RefreshAgentToken`
now consults the credential store through a `CredentialChecker` and
refuses a revoked source token. Verified: agent deletion and suspension
call `RevokeAgentCredentialsByAgent`, which marks rows rather than
removing them, and `AgentCredential` has no cascade edge. So a deleted
agent's credential cannot masquerade as a pre-table token, and the
compatibility window is not reachable that way.

**One open refinement: fail-open on a mint is not fail-open on a read.**
The patched function copies the middleware's fail-open on a store error.
The two sites are not equivalent.

In the middleware, fail-open is defensible. Exposure is bounded by the
remaining lifetime of a token that already exists, and failing closed
would reject every agent request during a database fault.

In refresh, fail-open **mints**. A revoked but unexpired token that
refreshes during a store outage receives a new token, recorded as a
fresh credential with no `RevokedAt`. The revocation is escaped
permanently, not for the length of the outage, and nothing later catches
it, because the revoke-by-agent call has already run.

The availability argument does not transfer. `ValidateAgentToken` has
already confirmed the source token is unexpired, so refusing a refresh
does not break the caller — it only stops the session being extended
during a fault. **Recommendation: refresh should fail closed.** This
also confines any `FailClosedOnStoreError` option to the middleware,
which is where the real availability tension lives.

*Fail-open on store error.* The middleware accepts a token when the
credential store returns an error that is not "not found"
(`auth.go:179-183`). This is a defensible default: failing closed would
lock out every agent during a database fault, which is the worse
failure. Two qualifications. The exposure is one full token lifetime,
currently 10 hours, not the length of the outage. And if a
`FailClosedOnStoreError` option is added, it should apply only to
credentials revoked for a security reason, not to routine lifecycle
churn.

### 6.5 Separate the principal from the credential

This is an addition from codex-auth-review, and review-arch accepts it.

Today the identity object mixes two things: who the subject is, and what
the credential permits. A UAT loses its token identity after
authentication, so the audit record cannot name the token that acted.

**The repair.**

```
Principal   # the subject: user or agent
Credential  # the caveats: UAT scopes, JWT scopes, expiry, token id
Decision    = permissions(Principal) INTERSECT caveats(Credential)
```

A credential can only narrow. It can never widen. Put `CredentialID` and
the credential type in the audit context.

---

## 7. Recommended Foundation, In Order

### Phase 0 — Patch the live escalations

- F0.1 Fix D6. An absent role resolves to the lowest role. Authorize
  `dispatch_agent` as `agent:create` at schedule time and at fire time.
  Refuse an agent-authored dispatch schedule, or force the lowest role,
  until the creator kind is persisted. See section 4.1.
- F0.2 Fix D7. Reject every UAT at all 14 role-only admin gates.
- F0.3 Fix D8. Guard `createGroup`. Reserve the `project:` slug
  namespace. Refuse to adopt a colliding group. Narrow
  `isProjectOwnerOrAdmin` to the canonical relation.
- F0.4 Add a route classification test. The test lists every route and
  its required permission. It fails on an undeclared route. Run it
  against the current tree and record the gaps.

Phase 0 is mostly behavioural, but it is **not** free of data-model
change. F0.1 needs a migration for the agent rows that hold an empty
role. The durable half of F0.1 needs new fields on `ScheduledEvent`; if
those are deferred, the interim rule of F0.1 must ship in their place.

### Phase 1 — The foundation

- F1.1 One canonical permission and resource registry (D3). Generate all
  five lists from it.
- F1.2 One `AuthzRequest` and `Decision` pipeline for every principal
  kind (D2, and section 6.2). Keep the agent-specific rules — same
  project, self, no escalation — as mandatory boundaries inside it.
- F1.3 Precedence and determinism (D1). Give the evaluation a total
  order. Make the engine read `Priority` or remove it. Reject
  unenforced conditions. **Do not build `PolicyBoundary`** — the sponsor
  declined it on 2026-08-22. See sections 5.1 and 5.1.1.
- F1.4 Declarative route guards, from the route table (D2). Add
  authorized SQL filtering for the list endpoints, so that a list does
  not leak rows that a read would refuse.
- F1.5 `RoleDefinition`, `RoleBinding`, and one `CanDelegate` admission
  gate on every authority-creating path (D4, and section 5.1.1). The
  paths are: direct binding, group binding, nested-group binding, agent
  delegation, custom role definition and update, and raw policy write.
  Migrate `User.Role` and the project bypasses. **This item is
  load-bearing** — after the sponsor decision it is the only mechanism
  constraining a project.
- F1.6 Principal and credential separation (section 6.5).
- F1.7 Connect the delegation ceiling (section 6.1). Bound every edge.
- F1.8 Agent token revocation (section 6.4). Add a `jti` and a check at
  authentication.
- F1.9 Decision and mutation audit, with an "explain" API. An
  administrator must be able to ask why a request was allowed.
- F1.10 A system-managed project-membership relation, keyed by the
  immutable project ID (the durable D8 repair, section 4.3). This folds
  into F1.5, because project membership becomes a first-class binding.

### Phase 2 — The requested features

- F2.1 Limits and quotas, as a separate entitlement and admission
  service (D5).
- F2.2 Granular UAT caveats, on the canonical registry.
- F2.3 Scoped hub admin, as role bindings over a resource subset.
- F2.4 The user interface for all of the above.

Phase 2 is the sponsor's original request. It is safe only after
Phase 1.

---

## 8. Alternatives Considered

**A. Build the new features now. Repair later.**
Rejected. Each new feature multiplies each defect. Granular tokens on
disagreeing vocabularies (D3) produce scopes that silently do nothing —
this already happens with `agent:start`. Scoped admin on a code bypass
(D4) cannot be expressed at all. The repair cost grows with every
feature.

**B. Adopt an external policy engine first, for example OPA or Cedar.**
Rejected as a first step. Both reviewers agree. The defects are not in
the decision algebra. They are in enforcement coverage (D2), vocabulary
(D3) and data model (D4, D5). An external engine would evaluate the same
incoherent inputs. Reconsider after Phase 1, when the registry and the
pipeline exist. Phase 1 does not block this choice.

**C. Repair only the three P0 defects and ship.**
Rejected, but partly accepted. Phase 0 must ship on its own, and quickly.
It is not sufficient, because the sponsor's three goals each depend on a
structural repair: scoped admin needs D4, granular tokens need D3,
limits need D5.

**D. Merge the human and agent paths into one identity type.**
Rejected. The differences are real, and the engine needs them. Merge the
**pipeline** (F1.2), not the **types**. This gives one place to write
each rule, and keeps the information.

**E. Express limits as policies with priority.**
Rejected. A quota is a counting problem with a race condition. Policy
evaluation is a matching problem. Priority is not a counter. This
approach also worsens D1, by adding a fifth meaning to the `Policy` row.

---

## 9. Dissents

**There are no dissents.** codex-auth-review signed off at round 4 on
the complete recommendation set, the phase order and the final-edit
role. Convergence came at round 2 of the permitted 4.

Three positions changed during the debate. We record them for honesty.

1. **D1 wording.** review-arch first called "a specific allow beats a
   broad deny" a defect. codex-auth-review showed that the tests and the
   design chose local override deliberately. review-arch withdrew the
   claim. The advisory now states the defect as a representational one.
2. **PostgreSQL tie order.** review-arch first speculated about the
   PostgreSQL behaviour, and could not test it — no PostgreSQL was
   available in the container. codex-auth-review corrected the framing
   to "SQL gives no guaranteed tie order". The observed flip is marked
   SQLite-only. A speculative claim about UPDATE was removed.
3. **D5 wording.** review-arch wrote "structurally impossible".
   codex-auth-review noted that a JSON map or a synthetic section key
   could encode limits, though badly. The wording is now "no
   first-class schema or race-safe admission path".

One finding changed owner. codex-auth-review reported that
`isProjectOwnerOrAdmin` grants project admin from any explicit group
attached to the project. review-arch verified the engine side, found
that the obvious attack route was closed, and then found a different
open route. The result is D8. The finding is joint.

One recommendation was strengthened at round 4. codex-auth-review asked
that the durable D8 repair not depend on a slug. Section 4.3 now
specifies a system-managed relation keyed by the immutable project ID.
review-arch accepted this without change.

**Editorial review after signoff.** codex-auth-review reviewed the
finished document and found six errors. review-arch accepted all six.
Two were substantive, and both were review-arch's:

- The D7 patch contradicted itself. It said "reject unless the token
  holds a hub-admin scope", then prescribed `requireHubAdmin`, which
  rejects every scoped identity. The strict rule now stands.
- The D6 patch was not implementable. It said the scheduler should copy
  the creator's ancestry and role, but `ScheduledEvent` records no
  principal kind. review-arch verified this in the source and rewrote
  the patch. The claim that Phase 0 changes no data model was withdrawn.

codex-auth-review also corrected the D6 attribution **against their own
interest**: review-arch found D6, and codex-auth-review verified it
afterwards. The earlier text said both found it independently.

**Interpretation of the sponsor decision.** After the sponsor answered
question 1, codex-auth-review made three further edits, all accepted:
the D1 retitle; the generalisation of the admission gate from a
role-only check to `CanDelegate` over five authority-creating paths;
and the separation of super-admin from the recovery credential. The
second corrected a genuine overstatement by review-arch, which would
have left raw policy writes outside the only remaining constraint.

**Final state.** codex-auth-review confirmed on 2026-08-22 that there
are no outstanding edits and no dissent.

---

## 10. Open Questions

These need a product decision. They are not reviewer disagreements. They
are listed in the order that they block work.

1. ~~**Boundary semantics.** May a hub-level rule be non-overridable by
   a project?~~ **RESOLVED, 2026-08-22.** No. A project may override a
   hub rule. Only the hub-admin role, which the sponsor plans to rename
   **super-admin**, has an absolute capability, and it has it by bypass.
   Consequences are in section 5.1.1. `PolicyBoundary` is cancelled.
   F1.5 becomes load-bearing.
2. **Break-glass access.** Partly resolved by the answer to question 1,
   but **not** fully. Super-admin is the absolute *operational* role.
   That does not automatically make it the *break-glass credential*.
   The two should stay separable:

   - Everyday super-admin identities, used by named operators.
   - A protected recovery credential, which may hold super-admin, with
     stronger storage and use controls.

   What remains to decide: who holds each; how each is audited; whether
   super-admin is delegable by a binding (we recommend no); and whether
   either is ever honoured from a scoped credential (we recommend no,
   for both).
3. **Revocation propagation.** *(Reframed. The earlier form — immediate
   delegator versus origin user — was a false choice. Our agreed rule
   binds every edge to the delegator's current entitlements, so the
   origin is already bounded transitively.)* The real question: when a
   user loses a permission, must every agent below them lose it at the
   next decision, through every ancestor? Or do existing agents keep a
   snapshot of their grant until it expires? Both reviewers recommend
   live propagation through every edge. It blocks F1.7.

   **Existing precedent, found 2026-08-25.** The system already does
   live re-evaluation, for humans. `handleAuthRefresh`
   (`handlers_auth.go:427`) re-reads the stored role instead of trusting
   the role claim in the token. It refuses when the user no longer
   exists, and refuses when the user is suspended. A comment in that
   function states the reason: trusting the token's own role claim would
   let a stale admin claim renew itself indefinitely through the
   rotating refresh chain.

   That is the exact hazard this question asks about, already solved on
   the human side. The agent path does not do it. This is the strongest
   argument for answering in favour of live propagation, and
   `handleAuthRefresh` is the pattern for F1.7 to copy.
4. **Migration of existing agents.** Agents with an empty role currently
   run as full. Fixing D6 will remove their access. Do we migrate them
   to `full` explicitly, or do we force a re-grant?
5. **Federated identities.** `FederatedAgentIdentity`,
   `FederatedUserIdentity` and `BrokerIdentity` were not swept. Do they
   reach the same handlers? This is an unverified gap, not a finding.

---

## 11. Acceptance Criteria

A reviewer or QA tester must verify the following.

### Phase 0

- [ ] An agent with the `readonly` role cannot create an agent with a
      wider role, by any route, including `dispatch_agent`.
- [ ] `dispatch_agent` is authorized as `agent:create` when the schedule
      is created, and again when it fires.
- [ ] An agent-authored dispatch schedule is either refused, or produces
      an agent with the lowest role.
- [ ] An agent row with an empty role resolves to the lowest role, not
      full.
- [ ] The migration for empty-role agent rows runs, and no live agent
      loses intended access.
- [ ] **Every** UAT is refused at all 14 role-only admin gates,
      including a UAT of a super-admin.
- [ ] `POST /api/v1/groups` refuses an unauthorized caller.
- [ ] A user cannot create a group whose slug starts with `project:`.
- [ ] A pre-existing group with the slug `project:NAME:members` does not
      give its owner any right on a later project with slug `NAME`.
- [ ] On a slug collision, the server refuses to adopt the group, unless
      its system marker and its `ProjectID` already match.
- [ ] `isProjectOwnerOrAdmin` reads only the canonical membership
      relation.
- [ ] A project with more than 10 explicit groups still resolves its
      owner correctly.
- [ ] A test enumerates every route and fails when a route declares no
      permission.

### Phase 1

- [ ] Every UAT scope, agent scope, user interface entry and CLI entry
      is generated from the registry. No hardcoded list remains.
- [ ] A test fails when a scope has no enforcement site. The scopes
      `agent:start`, `agent:stop`, `agent:message` and `agent:dispatch`
      either enforce or are removed.
- [ ] One pipeline serves users and agents. The agent boundaries — same
      project, self, no escalation — are mandatory inside it.
- [ ] Policy evaluation has a total order. The same input gives the same
      output over 100 runs, with rows inserted in a different order.
- [ ] A stored condition that is not enforced, such as `SourceIPs`, is
      rejected at write time.
- [ ] `Priority` changes the outcome of a decision, or the field is
      removed.
- [ ] A project policy **can** override a hub policy. This is intended
      behaviour, per the sponsor decision of 2026-08-22. A test pins it,
      so that a later change does not alter it silently.
- [ ] The super-admin bypass is never honoured from a scoped credential,
      by any route.
- [ ] A list endpoint returns no row that a read on that row would
      refuse.
- [ ] A scoped admin cannot create authority wider than their own, by
      **any** path. This is the only mechanism constraining a project,
      so test every path: direct binding; group binding; nested-group
      binding; agent delegation; defining a custom role; updating a
      custom role; and raw policy write.
- [ ] Raw policy authoring is either restricted to super-admin, or it
      passes the same admission gate. A scoped admin cannot use a policy
      to grant what the gate refuses.
- [ ] A credential can only narrow the permissions of its principal.
      A test proves that no credential widens.
- [ ] The audit record names the credential, by identifier and type.
- [ ] An agent loses a permission at the next decision, when its
      delegator loses it.
- [ ] A revoked agent token is refused at authentication, before its
      10-hour expiry.
- [ ] A revoked but unexpired token cannot obtain a fresh, unrevoked
      credential by any refresh path. Revocation survives the minting of
      a new `jti`. See section 6.4.
- [ ] The explain API returns the rule that decided a request.
- [ ] Project membership is keyed by the immutable project ID, not by a
      slug. The relation is created in the same transaction as the
      project.

### Phase 2

- [ ] A limit is enforced under concurrency. 100 parallel creations
      against a limit of 10 produce exactly 10.
- [ ] A subject that matches several entitlement bindings resolves by
      the documented merge rule.

---

## 12. Sources

All line numbers are from branch `scion/review-arch`, commit `89ed0fe`.

| File | Lines | Used for |
|------|-------|----------|
| `pkg/hub/authz.go` | 118-234, 237-384, 394-453, 455-503, 505-517, 592-610, 641-662 | D1, D4, D7, D8, section 6.2 |
| `pkg/hub/agentrole.go` | 66-67, 85-86, 121 | D6, section 6.1 |
| `pkg/hub/authorize.go` | 115, 183, 227, 254-282 | D3, D7 |
| `pkg/hub/identity.go` | 88-92, 140-145 | D7, section 6.3 |
| `pkg/hub/httpdispatcher.go` | 207-228 | D6 |
| `pkg/hub/server.go` | 2866-2890, 3490-3491 | D6, D8 |
| `pkg/hub/hub_pre_start_hook_handlers.go` | 40-95 | D7 |
| `pkg/hub/handlers_groups.go` | 40-49, 148, 162-171, 178-181, 217-227, 326, 383, 487, 722 | D8 |
| `pkg/hub/handlers_projects_core.go` | 487-517, 564-572, 596-612, 2531-2537 | D8 |
| `pkg/hub/handlers_agents_core.go` | 626-647 | Section 6.1 |
| `pkg/store/models.go` | 1752-1764 | D6 (no principal kind on the event) |
| `pkg/hub/handlers_policies.go` | 104, 309, 332, 400, 418, 499 | Section 5.1.1 (policy writes are admin-gated) |
| `pkg/hub/agenttoken.go` | 225 | Section 6.4 (refresh does not check revocation) |
| `pkg/hub/auth.go` | 179-183 | Section 6.4 (fail-open on store error) |
| `pkg/hub/handlers_auth.go` | 427 | Open question 3 (live re-evaluation precedent) |

The last three rows are from branch `scion/auth-refactor`, reviewed on
2026-08-25, after Phase 1H. All other rows are from `scion/review-arch`
at `89ed0fe`.
| `pkg/store/entadapter/policy_store.go` | 459-466, 177-179, 202-204 | D1 |

**Verification status.**

- D1 case (a) and case (b): verified by execution, on SQLite.
- D1 ordering: verified by source reading. Confirmed by both reviewers.
- D6: found by review-arch, then verified in source by
  codex-auth-review.
- D8: verified in source by review-arch, during round 3.
- Federated identity handlers: **not swept**. See open question 5.
- PostgreSQL tie behaviour: **not tested**. No PostgreSQL was available.

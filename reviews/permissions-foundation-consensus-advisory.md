# Consensus Advisory: Foundations for the Permission System

**Project:** permissions-foundation
**Authors:** review-arch (final editor), codex-auth-review
**Date:** 2026-08-22
**Process:** Two independent reviews, then 4 rounds of debate.
Convergence at round 2. Signoff at round 4, with no dissent. Two
editorial passes after signoff: one for correctness, one to interpret
the sponsor decision. Both reviewers signed off on the final text.
**Status:** Questions 1, 3, 4 and 6 are resolved by the sponsor.
Question 5 is swept and closed by review-arch. **Question 2 is the only
one still open.** The document is otherwise final, and is updated in
place as answers arrive.
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

*(A ninth defect, D9, was added on 2026-08-25 by the federated identity
sweep in section 10.2. It is not part of the eight that the two
reviewers debated and signed off. Its severity is structural, and it is
conditional on federation being configured. It is listed separately in
the table below for that reason.)*

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
| D9 | Federated agent ancestry is unconstrained, and is read as authority | Structural | Only with federation configured |
| D10 | Super-admin is grantable through an F1.5 role binding, against the sponsor decision | **P0** | **Yes** |
| D11 | Super-admin cannot be revoked. `AdminEmails` is a write-only control | **P0** | **Yes** |

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

**Repair status, verified 2026-08-25.** The members path is correct. The
agents path is not, and the difference looks accidental.

*Correct.* `createGroup` now calls `s.authorize` with `ActionCreate`,
which closes (a). It rejects any slug carrying the `project:` prefix,
which closes (b), and it refuses the `project_agents` group type
outright. `createProjectMembersGroupAndPolicy` closes (c): on a slug
conflict it calls `isSystemProjectMembersGroup`, which requires **both**
that the group already carries the project's immutable ID **and** that
it holds the `scion.io/project-members-group` annotation. Otherwise it
refuses to adopt, and logs. `BackfillProjectMembersGroupMarkers` marks
the legitimate pre-upgrade groups so that reuse still works. This is the
durable shape.

*Incomplete.* `createProjectGroup`, which creates the project **agents**
group, kept the original adoption logic:

```go
if existing.ProjectID != project.ID {
    existing.ProjectID = project.ID
    UpdateGroup(...)
}
```

There is no annotation check, no system-group check and no group-type
check. It adopts any group whose slug is `project:<slug>:agents`. There
is no agents equivalent of the marker backfill, so nothing marks the
legitimate ones either.

*Severity.* Not reachable on a fresh hub, because the prefix reservation
stops anyone creating that slug. Reachable on an **upgraded** hub in one
case: a group named `project:<future-slug>:agents`, created before the
prefix reservation landed, is adopted when a project with that slug is
created later. Its creator stays the owner and can add members, and the
agents group is the implicit binding target for project agent policies.
So the exposure needs a hub upgrading across this fix, and someone who
guessed a project slug in advance. **Not P0. An incomplete repair.** The
point of the members-side work was that a slug is not a safe key, and
the agents side still treats it as one.

*Repair.* Make the two paths symmetric: the same system annotation, the
same both-conditions predicate on adoption, refuse and log otherwise,
and a marker backfill covering agents groups. Do this even at low
severity, because two adjacent functions now handle one class of
resource with different rigour, and that asymmetry is what invites the
regression. The members-side code is already the template.

**Two cautions on the backfill.**

*It needs its own marker section.* Do not add the agents logic inside
`BackfillProjectMembersGroupMarkers`. That function returns immediately
when `projectMembersGroupMarkerBackfillSection` is present, so every hub
that already ran the members pass would skip the new agents logic in
silence. Use a separate section, with separate idempotency.

*A backfill blesses an existing squat. It does not remove one.* This is
the more important caution, and it applies to the members backfill that
already shipped. Consider the D8 attack. A user creates
`project:foo:agents` before the prefix reservation. A project with slug
`foo` is created later, and the old code adopts the group and sets its
`ProjectID`. The group now has a real project ID and a matching slug —
exactly the two conditions a backfill tests. The backfill marks it as
system-managed, and the adoption predicate then accepts it permanently.

So the upgrade prevents **new** squatting. It does not undo an adoption
that already occurred, and afterwards the squat is indistinguishable
from a legitimate group. The hazard is that an operator reads the
upgrade as remediation.

Three consequences:

1. Log every group that the backfill marks, with group ID, slug, project
   ID and owner. A count is not enough. This is the only artifact an
   operator can audit against, and it costs one log line for each row.
2. State in the release note that the upgrade stops new squatting and
   does not reverse a completed adoption. A hub that may have been
   exploited needs a manual review of project agents and members groups
   whose owner is not the project owner.
3. Optionally, have the backfill flag rather than mark any group whose
   `OwnerID` differs from the owner of the project it claims. That is
   the signature of an adopted squat, and it is the one discriminator
   that survives.

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

### 5.6 D10 and D11: the super-admin control does not match the decision

Found on 2026-08-25, when the sponsor's answer to open question 2 was
checked against `origin/scion/auth-refactor`. The decision is that
super-admin is expanded **out-of-band only**. Neither half of that
statement is true in the code today.

#### D10: super-admin is grantable through F1.5

There are two super-admin gates, and they disagree.

| Gate | Reads | Obeys the decision? |
|---|---|---|
| `IsUnscopedLocalPlatformAdmin` (`identity.go:137`) | `User.Role == "admin"` | Yes. Out-of-band only. |
| `AuthzService.IsSystemAdmin` (`authz.go`) | Role bindings **only**. Never reads `User.Role`. | **No.** |

`IsSystemAdmin` returns true for any user holding a system-scoped
binding to the super-admin role definition. That definition is an
ordinary role definition and is bindable by the normal F1.5 machinery.
So whoever can create a system-scoped role binding can confer
super-admin.

This is not a minor path. `checkUserHoldsPermission` short-circuits on
it, so a super-admin binding also bypasses the entire delegation ceiling
that Phase 1G is building.

**Repair, at the store and not in a handler.** Refuse to create a role
binding whose role definition is super-admin unless the caller is the
system reconciler. A handler-level check will be missed by the next
handler that is written. The store is the choke point.

#### D11: super-admin cannot be revoked

Removal from `AdminEmails` is a no-op, for two independent reasons.

1. `determineUserRole` (`handlers_auth.go`) never demotes. If the email
   is absent from the list it returns `currentRole`. A user promoted
   once keeps `admin` after the operator removes their email.
2. Even after a manual demotion, `ReconcileSuperAdminBindings` only
   **warns** about the orphaned binding. It does not remove it, so
   `IsSystemAdmin` still returns true and super-admin survives in the
   authz path.

Under the model the sponsor confirmed, `AdminEmails` is the sole control
for super-admin. That control is at present **write-only**. An operator
can add. An operator cannot remove.

**Repair. Decided by the sponsor, 2026-08-25T14:59:07Z: "remove on
restart."** Reconciliation converges in both directions. For any user
not in `AdminEmails`, demote `User.Role` and delete the system-scoped
super-admin binding. Log each change.

Three qualifications on that decision. The first is the sponsor's. The
other two are ours, and are labelled as such.

1. **Sponsor-decided.** Removal happens, and it happens at start-up.
   Reporting only is rejected.
2. **Architect decision, not sponsor-approved.** Refuse to converge
   downward when `AdminEmails` is **empty**. The sponsor's reply did not
   mention the guard, and we are not reading approval into a silence. We
   are applying it anyway, because it is a fail-safe rather than a
   policy relaxation, and the asymmetry is severe: an empty list is
   nearly always a failed config load, and removing every administrator
   produces a hub that nobody can administer, including to undo the
   removal. The sponsor has been told we are treating it as accepted
   unless they object.
3. **Scope limit — the trap in this decision.** Reconciliation must
   touch **only** `User.Role` and the system-scoped **super-admin**
   binding. It must **not** remove the ordinary admin-right grants that
   make a user functionally admin-like. Those grants are the path the
   sponsor endorsed in open question 2. An implementation that reads
   "remove admin authority from users not in `AdminEmails`" broadly
   would delete exactly the population the sponsor just blessed, on the
   next restart.

**A known property of this decision.** Because reconciliation runs at
start-up, the revocation latency for super-admin is the time until the
next restart. The sponsor chose "on restart" with that in view. If that
window later proves too wide, the remedy is an explicit endpoint that
forces reconciliation, not a change to this rule. Recorded as a
follow-up, not a blocker.

#### A documentation defect alongside them

`identity.go:137` states that reconciliation keeps the role and the
binding in step "and vice versa". It does not; the reverse direction
only warns. The comment asserts a stronger invariant than the code
provides, which is how this area reads as safe on inspection. Correct it
in the same commit.

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

**Refinement, now also closed: fail-open on a mint is not fail-open on a
read.** The first patch copied the middleware's fail-open on a store
error. The two sites are not equivalent.

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
during a fault.

**Closed at `066c7792`.** The store-error branch now returns an error
and mints nothing. Verified: the `ErrNotFound` compatibility window is
preserved, so pre-table tokens still refresh, and
`TestAgentTokenService_RefreshStoreErrorFailsClosed` pins the behaviour.

**The general rule, for future work.** Fail-open and fail-closed must be
decided per operation, not per subsystem. A read may fail open, because
the exposure is bounded by state that already exists. An operation that
**mints or extends** authority should fail closed, because it creates
durable state that outlives the fault. Copying a fail-open policy from a
read path to a minting path is the specific error to avoid. Any
`FailClosedOnStoreError` option therefore belongs to the middleware
only.

**A second general rule: absence is not permission.** This advisory has
now recorded the same defect three times, in three different places.

| Where | The absent thing | What it resolved to |
|---|---|---|
| D6, `httpdispatcher.go:207` | An agent role | `AgentRoleFull` |
| F1.7, `agentrole.go:120` | A user ceiling | `AgentRoleFull` |
| F1.7, the new ceiling walk | A delegation edge | Unbounded, if written naively |

Three instances make it a pattern, not three bugs. The shared cause is
that authority is computed as a **maximum that starts at the top and is
lowered**. Any lookup that returns nothing then leaves the maximum where
it began, and an absent record grants everything.

**The rule.** Compute authority from the floor upward. Start at the
lowest role, or at no permission, and raise it once for each grant that
a lookup **positively returns**. Never start at the top and lower it.

This makes an empty result safe by construction, which matters because
empty results are normal rather than exceptional. A federated agent is
not a local agent record, so a delegation-edge lookup and a
`GetEffectiveGroupsForAgent` call both return an empty set for it. Under
the rule, an empty set of groups contributes no grants. Under the
current shape it reads as no constraint.

An implementation must therefore distinguish three outcomes, and never
merge the last two: a lookup that returns grants; a lookup that returns
nothing, which means no authority; and a lookup that fails, which is the
fail-closed case above.

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
  The sponsor confirmed live downgrade on 2026-08-25, so the ceiling is
  re-read at each decision. Copy the `handleAuthRefresh` pattern
  (`handlers_auth.go:427`), which already does this for humans. The
  scope of grandfathering is still open — see question 6.
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
2. ~~**Break-glass access.**~~ **RESOLVED by the sponsor,
   2026-08-25T14:55:19Z**, in Discord thread 1536947436512088174. First
   relayed by `auth-refactor-lead` at 14:46:22Z, then stated directly by
   the sponsor. The two agree.
   Super-admin is a **bootstrapping role, expanded out-of-band only** —
   at present through the `AdminEmails` configuration. It is **not**
   grantable through the F1.5 role-binding machinery. This matches our
   recommendation.

   The everyday operational path is different: grant a user all
   *grantable* admin rights through the normal policy and role-binding
   machinery. The result is a user who is functionally admin-like but
   who still passes every policy and auth check, one grant at a time,
   with no super-admin short-circuit. The separation we asked for is
   therefore achieved by construction, rather than by two credential
   classes.

   **The code does not yet implement this decision.** Verifying the
   answer against the branch produced defects D10 and D11, in section
   5.6. Both must be repaired before the decision is true in practice.

   **This decision also raises the priority of D4.** The functional path
   works only if the admin capability set is fully expressible as
   grants. Every remaining hard-coded admin bypass is a capability that
   cannot be granted, which pushes operators back to true super-admin,
   and therefore back into D10. D4 is now load-bearing for the sponsor's
   model, not merely tidiness.

   The one point left open here — whether reconciliation should
   **remove** super-admin from users no longer in `AdminEmails` — was
   answered by the sponsor at 2026-08-25T14:59:07Z: **"remove on
   restart."** The consequences, and the two qualifications we attach to
   it, are in D11.
3. ~~**Revocation propagation.**~~ **RESOLVED by the sponsor,
   2026-08-25T13:14:34Z.** Live downgrade. When a user loses a
   permission, every agent below them loses it at the next decision,
   through every ancestor. Existing agents do **not** keep a snapshot
   until expiry. This unblocks F1.7.

   *(The question was reframed before it was answered. The earlier form
   — immediate delegator versus origin user — was a false choice. Our
   agreed rule binds every edge to the delegator's current
   entitlements, so the origin is already bounded transitively.)*

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
4. ~~**Migration of existing agents.**~~ **RESOLVED by the sponsor,
   2026-08-25T13:10:18Z.** Grandfather all existing agents. This shipped
   before the decision was recorded: `BackfillEmptyAgentRoles`
   (`pkg/store/entadapter/composite.go:254-294`) sets
   `cfg.AgentRole = "full"` for every agent with an empty role. A marker
   row in hub settings records that the migration ran.

   **A consequence to note.** The backfill does not tag the rows that it
   changes. Before the migration, an empty role identified an agent as
   legacy, of unknown provenance. After the migration, those agents look
   the same as agents that an operator deliberately granted `full`. The
   marker row shows that the migration ran. It does not show which
   agents it changed. On a hub where the migration has already run, that
   population can no longer be identified. See question 6.
5. ~~**Federated identities.**~~ **SWEPT AND CLOSED, 2026-08-25.** The
   sweep was done against `origin/scion/auth-refactor`. It produced one
   structural finding, one configuration hazard, one design note and one
   trust edge. It is written up in section 10.2.
6. ~~**The scope of grandfathering.**~~ **RESOLVED by the sponsor,
   2026-08-25.** Option A, the starting-point reading. Existing agents
   keep their current role as the value they start from, and the live
   ceiling applies to them from that point on. **There is no permanent
   exemption.** Reported through `auth-refactor-lead` and routed to
   Phase 1G.

   The question and its reasoning are kept below, because the two
   readings are easy to merge again and the record should show why they
   were separated.

   *(Raised 2026-08-25, after questions 3 and 4 were answered.)* The
   word "grandfather" answers question 4 correctly, but it can also be
   read onto question 3, where it means something very different. The
   two readings must not be confused:

   - **Starting point.** Existing agents keep their current role as the
     value they start from. Live propagation then applies to them from
     that point on. This is consistent with the answer to question 3.
     Both reviewers expect that this is the intended meaning.
   - **Permanent exemption.** Existing agents are excluded from the
     F1.7 delegation ceiling for as long as they live. This is not
     consistent with the answer to question 3.

   Permanent exemption would put the exemption on the worst population.
   After the backfill, all of those agents hold `full`. Many hold no
   ancestry at all, because the D6 scheduler path created them without
   any. The agents with the widest authority and the least provenance
   would become permanently exempt from the mechanism whose purpose is
   to bound authority. The Phase 1 acceptance criterion — an agent loses
   a permission at the next decision, when its delegator loses it —
   would then be false for that population, and nothing would show it.

   If the sponsor does want an exemption, we recommend two conditions on
   it. Bound it to a deadline or to token expiry, instead of forever.
   Report the count of agents that operate under it, so that the
   exemption stays visible.

   **One part of this is time-sensitive.** On any hub where
   `BackfillEmptyAgentRoles` has not yet run, the backfill can still
   record a marker on each agent that it changes. This costs one field
   now. It cannot be recovered later. Every hub that runs the migration
   in its current form loses the ability to identify that population
   permanently.

   **The marker is decoupled from this question**, because it is correct
   under either reading. Under the starting-point reading it serves
   audit. Under the exemption reading it becomes required, because the
   acceptance criterion asks for the exempt set to be enumerable. The
   sponsor answer is therefore not a gate on it. It was routed to the
   Phase 1G engineering manager on 2026-08-25. The specification is in
   section 10.1.
7. **What do several delegation paths mean?** Raised by finding R1
   (section 11.2), which exposed that the question has never been
   answered. Today an agent is expected to have one active delegation
   edge, and the Phase 1G fix makes that an enforced invariant. If the
   model later allows several delegators for one agent, someone must
   choose the rule:

   - **Most permissive.** The agent keeps a permission while **any**
     delegator still holds it. This is what the code comment asserts.
     Its cost is that revocation is not complete until the operator
     finds and revokes every path.
   - **Most restrictive.** The agent keeps a permission only while
     **every** delegator holds it. Revocation through one path is
     enough, but one unrelated delegator can strip authority that
     another legitimately granted.

   We do not recommend an answer here, because there is no product
   requirement yet for several delegators. **This question is not
   blocking.** It is recorded so that the first feature that needs
   multiple delegation paths does not settle it by accident, inside an
   implementation commit, the way the present comment did.

### 10.1 Specification: the role-provenance marker

Add a field to `AgentAppliedConfig` (`pkg/store/models.go:141`), beside
`AgentRole`:

```go
// AgentRoleGrandfathered records that AgentRole was assigned by the
// pre-role backfill, not by an explicit grant. It is provenance only.
AgentRoleGrandfathered bool `json:"agentRoleGrandfathered,omitempty"`
```

`BackfillEmptyAgentRoles` sets it in the same mutation that sets the
role. `BackfillProjectMembersGroupMarkers`, in the same file, is the
precedent: it marks groups with `systemProjectMembersGroupAnnotation`.

**Why a sibling field.** The backfill already parses, changes and
re-marshals this struct. A sibling field is written in the same
`UpdateOneID` call. There is no second write, no new failure mode, and
no way for the marker to drift apart from the role that it describes.
With `omitempty`, existing rows and existing JSON do not change.

**The constraint that matters.** This field is provenance. It is never
authority. No decision path may read it to grant, to widen or to exempt.
A boolean named "grandfathered" is the kind of field that becomes a
bypass quietly. If question 6 resolves in favour of an exemption, that
logic reads this field at one named and tested gate, and not as an
ambient check inside evaluation.

**Four limits on the change.**

1. The idempotency guard does not change. `if cfg.AgentRole != ""
   { continue }` stays as it is.
2. Do not mark agents that already hold `full`. Such a pass cannot tell
   a deliberately-granted agent from a legacy one. The marker is only
   truthful for the rows that the backfill itself changes.
3. Do not try to recover hubs where the migration already ran. The
   hub-settings check stops the function, so those hubs get no marker,
   and that is correct. A retroactive marker would record a guess as a
   fact. **Those hubs are unrecoverable.** State this, rather than
   letting someone "repair" it later by marking every `full` agent.
4. Do not build a hot-path query on this. `AppliedConfig` is JSON in a
   text column, so a filter is a scan and a parse. That is sufficient
   for a count-on-demand report, which is all that is needed.

**Tests.** An empty-role agent gets `full` and the marker. An agent with
an explicit role is untouched and gets no marker. A second run does
nothing. The marker survives a parse-and-marshal round trip of
`AgentAppliedConfig` — this is the real regression risk, because any
path that rebuilds the config from a struct literal, instead of changing
the parsed one, drops the field without a sign.

### 10.2 The federated identity sweep

This closes open question 5. It was done against
`origin/scion/auth-refactor`, so line references are to that branch.

**The three types are not equivalent, and that is the source of the
findings.**

| Type | `UserIdentity`? | `AgentIdentity`? |
|---|---|---|
| `FederatedAgentIdentity` | No | **Yes** |
| `FederatedUserIdentity` | **Yes**, has `Role()` | No |
| `BrokerIdentity` | No | No |

#### D9: Federated agent ancestry is unconstrained, and is read as authority

`checkDelegation` (`authz.go:755-766`) falls back to the ancestry chain
when store-level delegation does not match:

```go
if !allowed && policy.Conditions.DelegatedFrom != nil {
    for _, ancestorID := range agent.Ancestry() {
        if policy.Conditions.DelegatedFrom.PrincipalID == ancestorID {
            allowed = true
            break
        }
    }
}
```

For a local agent this is sound. The ancestry is in a token that the hub
signed, so the hub attests it. For a `FederatedAgentIdentity` it is not.
The ancestry comes from `claims.Ancestry` on a **remote** issuer's JWT,
and `extractHubClaims` (`federation_auth.go:301-310`) passes it through
unchanged.

**Nothing bounds it.** At `federation_auth.go:264-274` a hub-type issuer
is checked against `allowed_projects`, using `claims.ProjectID`, and
against `allowed_root_users`, using `claims.RootUser`. Ancestry is
checked against nothing. An operator who restricts `allowed_root_users`
still accepts any ancestry from that issuer. The control appears to
bound what a trusted peer may assert. It does not bound this.

**The related defect, which is the cheaper thing to fix.** Nothing
checks that `rootUser` equals `ancestry[0]`. The local wrapper derives
`OriginUserID` from the ancestry (`identity.go:178`).
`FederatedAgentIdentity` returns a separately stored `rootUser`
(`federation_identity.go:72`). The two can disagree. One is bounded by
`allowed_root_users`, the other by nothing, and the codebase uses them
interchangeably.

**Repair.** Three changes. **They are not equally protective**, and it
is possible to make all three and still leave the hole open. They are
given in increasing order of value.

1. *Reject a federated token whose `ancestry[0]` does not equal its
   `root_user`.* This makes the two fields agree, so they can no longer
   be used interchangeably with different bounds. It does not stop a
   remote issuer choosing both values.
2. *Apply `allowed_root_users` to every ancestry element, not to
   `root_user` alone.* This is real but **conditional**. The check at
   `federation_auth.go:271` runs only when
   `len(entry.config.AllowedRootUsers) > 0`, and that field is optional.
   On a hub where the operator never set it, this change does nothing.
   That is the default configuration.
3. *Treat ancestry as authority only when the hub signed the token that
   carries it.* **This is the load-bearing change**, because it is the
   only one that does not depend on optional configuration.

Changes 1 and 2 together still let a trusted issuer assert any ancestry
whenever `allowed_root_users` is unset.

**The narrow form of change 3.** In `checkDelegation`
(`authz.go:755-766`), use the ancestry fallback only for a hub-signed
identity. For a federated agent, skip the fallback. Local behaviour does
not change, and the remote case fails closed.

Put the test behind one named predicate, for example
`AncestryIsHubAttested(identity)`, beside
`IsUnscopedLocalPlatformAdmin`. Do not spread a `Type()` comparison
across call sites. There will be more consumers of ancestry after F1.7,
and each must answer this question the same way.

**For later, not for F1.7.** If cross-hub delegation is wanted, the
correct model is that the local hub records the trust relationship, as a
binding that names the remote issuer and states what it may delegate. It
is not that the remote hub names local principal IDs and the local hub
believes it. Skipping the fallback now does not prevent that design. It
declines to grant it by accident.

**This belongs inside F1.7**, which is building the delegation ceiling
on ancestry. A ceiling that walks a chain a remote issuer can write
freely is only as strong as the least careful trusted issuer. It is the
concrete instance of section 6.3.

**Severity: structural, not P0.** It needs federation enabled with a
configured hub-type issuer, an existing policy carrying a
`DelegatedFrom` condition, and the ability to make that issuer mint a
chosen ancestry. It is unreachable on a hub with no federation.

#### A configuration hazard: an unvalidated `default_role`

`FederatedUserIdentity.Role()` returns a value from issuer
configuration, never from a remote claim. `extractUserClaims` uses
`issuerCfg.DefaultRole` and falls back to `viewer`. `federationClaims`
carries no role field, so a remote role cannot be honoured even if it is
sent. **That design is correct** and should be preserved.

The gap is validation. Config validation checks `issuer_type` against an
allowed list. It does not validate `DefaultRole` against any role enum.
An issuer configured with `default_role: "admin"` gives
`Role() == "admin"` to every user that it signs.
`IsUnscopedLocalPlatformAdmin` contains this at the admin gates today,
so it is not now an escalation. It is one typo away from becoming one at
any future consumer of `Role()` that does not use the predicate.

**Repair, with F1.1.** Validate `DefaultRole` against the canonical role
registry at config load. Refuse `admin` outright.

#### A design note: the empty `ProjectID` is accidental safety

`handlers_env_secrets.go` uses `OriginUserID()` directly as the scope ID
for user-scoped secrets, at four sites. **This is not exploitable.**
`validateAgentSecretAccess` refuses any agent whose `ProjectID()` is
empty, and `FederatedAgentIdentity.ProjectID()` returns empty by design,
so a federated agent gets a 403 before reaching the scope logic.

It is recorded because the safety is accidental, not designed. It holds
because an empty string fails an emptiness check. A handler written as
`if agent.ProjectID() != "" && agent.ProjectID() != target { deny }`
fails **open** on the same input. Step 3 of `checkAccessForAgent`
already documents this hazard in its `pid != ""` guard, so it is
understood in one place. **F1.2 must make the federated case explicit in
the unified pipeline**, instead of relying on empty-string semantics
holding at every site.

#### A trust edge to write down

Under `X-Scion-On-Behalf-Of`, `applyOnBehalfOf` puts a full
`*AuthenticatedUser`, carrying the stored role, into the generic
identity slot. A broker acting for an admin therefore satisfies
`IsUnscopedLocalPlatformAdmin`. The broker is HMAC-verified
infrastructure, so this reads as intended. It is recorded because it is
the one remaining route by which a non-interactive caller reaches the
platform-admin bypass. It should be a stated trust assumption, not an
implicit one.

#### What the sweep found already closed

Two items are verified fixed, and pinned by tests.

- **D7.** `requireAdmin` calls `IsUnscopedLocalPlatformAdmin`, which
  refuses a scoped UAT and a federated identity, with a distinct denial
  reason for each. `TestHubAdminRoutesRejectScopedAdminUAT` and
  `TestAuthzDecideFederatedAdminCannotUseLocalAdminBypass` pin both
  halves.
- **F1.4.** All 150 `mux.HandleFunc` registrations go through
  `guarded()`. An unknown pattern returns 500 instead of passing
  through, and so does an unknown classification.
  `TestRegisteredRoutesHaveRouteMetadata` pins the coverage invariant,
  which is the part that keeps it true as routes are added.

One caveat on F1.4. The `RoutePolicy` classification passes through to
the handler by design, so those routes get no declarative gate. This is
disclosed in the code, not hidden, but D2 survives for that set.

---

## 11. Implementation Review Log

### 11.1 Phase 1G at commit `3597507` — changes requested

Reviewed on request from `pf-1g-em` before merge. Verdict: **do not
merge**. Three blockers and three major items. The EM self-reported the
first blocker, which is good practice and shortened the review.

The central finding is that **the ceiling does not limit the population
it was built to limit.** Two independent paths give unlimited authority
to every agent that exists before this release, one before the backfill
runs and one after it.

| ID | Severity | File | Fault |
|---|---|---|---|
| 1G-1 | Blocker | `authz_delegation_ceiling.go:166-174` | Each edge with `Grandfathered = true` bypasses the ceiling. The backfill sets that flag on every existing agent, so every existing agent is permanently exempt. This is Option B, which the sponsor rejected. |
| 1G-2 | Blocker | `authz_delegation_ceiling.go:138-149` | At depth 0 a hub-attested identity with no edge is allowed. "Absence is not permission" was applied to federated agents only. |
| 1G-3 | Blocker | `authz_delegation_ceiling.go`, `isMintingOperation` | The set omits `ActionMint` and `ActionAssign`. A store error during a token mint or a role assignment fails **open**. Affects `authz.go:284` too. |
| 1G-4 | Major | `composite.go:345-355` | The backfill defaults `role` to `"full"`. An empty `AppliedConfig` gives a full-role edge with no log record. |
| 1G-5 | Major | `identity.go:145-156` | `AncestryIsHubAttested(identity interface{})` returns true for any unexpected type, and tests one concrete type where its sibling ten lines above tests the `FederatedIdentity` interface. |
| 1G-6 | Major | `checkUserHoldsPermission` | `ErrNotFound` for a delegator is handled as a store error. A principal that can never exist is retried as a temporary fault forever. |

#### Why the bypass was written

`determineDelegator` never returns empty. When provenance is unclear it
returns the synthetic principal `system/migration`. A live check against
that principal cannot succeed, so those agents would stop working. The
problem is real; the scope of the fix is wrong. Only the synthetic
subset has the problem. Most grandfathered edges have a real delegator
and need no special handling. This is the same conflation error that
this review has flagged repeatedly: **grandfathered is not the same as
ambiguous.**

#### The corrected rule

Branch on the **resolvability of the delegator**. Never branch on the
`Grandfathered` flag.

- Delegator resolves to a real principal — do the live check, always.
- Delegator is synthetic, or the lookup gives `ErrNotFound` — set the
  ceiling to the agent's own recorded role and freeze it. The agent
  keeps what it has. It cannot escalate and it cannot mint above its
  role. Emit an enumerable "orphaned delegation" signal so that an
  operator can re-parent the edge.
- A genuine store fault — fail closed for authority-affecting actions,
  fail open for the others.

`Grandfathered` stays, as provenance only. It may appear in explain
traces and audit reports. It must never appear in a branch that returns
allow. This is the rule already stated in 10.1 for
`AgentRoleGrandfathered`, which the developer applied correctly there.

For 1G-2, gate the guard on "the backfill marker is absent" rather than
on "the identity is hub-attested". The backfill runs at start-up in the
same release, so the pre-migration window is nearly empty. A marker-
gated guard disables itself when the migration completes. The present
guard never expires.

#### What is correct

The intersect semantics at `authz.go:277` — the ceiling narrows an
allow and never widens a deny. The recursive chain walk and its depth
limit. The federated no-edge denial. `federation_auth.go:275-282`, which
stays inside the `AllowedRootUsers` block as agreed, and is therefore
defence in depth only. The explain steps, which made this review much
faster and should be kept as a standard for later phases.

### 11.2 Finding R1: the first active edge decides, and the row order is not fixed

Raised by the Phase 1G code reviewer, not by this review. Assessed here
because it interacts with 1G-1. **It must land in the same commit as the
1G-1 fix.**

`walkDelegationChain` returns on the first active edge. The comment
above it says that the ceiling passes if **any** edge's delegator still
holds the permission. The code and the comment describe different rules.

The reviewer judged this safe, because an agent has at most one active
edge. **That is a convention, not an invariant, and it is already
reachable.**

- `pkg/ent/schema/delegationedge.go` declares four indexes. All four are
  non-unique. Nothing limits an agent to one active edge.
- The backfill calls `Create` without checking for an existing edge.
- The backfill writes its completion marker **after** the paging loop.
  An interruption during the migration leaves the marker absent, so the
  next start runs the loop again from offset 0 and gives a second edge
  to every agent already processed. An interruption during a migration
  that pages over every agent is a normal event.

`GetDelegationEdgesForDelegate` has no `Order()` clause, so row order is
whatever the database returns. Duplicate edges plus first-edge-decides
means the same request can allow or deny depending on row order. **This
is D1 — nondeterministic resolution — reproduced inside the new
ceiling**, which is one of the defects this engagement exists to remove.

The fault is masked today: every backfilled edge is grandfathered, so
the 1G-1 bypass returns allow before the order can matter. **Removing
the bypass unmasks it.** The dependency therefore runs opposite to the
reviewer's assumption, and R1 cannot be deferred past the 1G-1 fix.

**Do not implement the rule in the comment.** "Any edge passes" is the
most permissive reading, and it makes revocation require the operator to
revoke every path. That is a sponsor decision, not a bug fix. It would
also make the duplicate state work silently, hiding the migration fault.

Scope for the fix: add a unique constraint on one active edge for each
`(delegate_type, delegate_id, scope_type, scope_id)`; make the backfill
idempotent; add `Order()` so that determinism does not depend on the
constraint; correct the comment to state that the single active edge for
the scope decides; and if more than one active edge is found, log an
error and fail closed for authority-affecting actions. The last item
should never fire once the first two land. Keep it as the detector that
reports a broken invariant.

The open question that this exposes — what several delegation paths
**mean** — is recorded in section 10 and is not resolved here. The scope
above is correct under either future answer, because it makes the
present single-path rule explicit and enforced.

---

### 11.3 Phase 1G at commit `6fd0c33` — round two

All eight items from 11.1 and 11.2 are genuinely fixed. 1G-5 in
particular is exactly right: a typed parameter, a nil check, an
interface test rather than one concrete type, and an unknown type fails
closed.

Five new findings. One blocks merge. R2-5 was raised by the code
reviewer as optional; we reclassify it.

| ID | Severity | Fault |
|---|---|---|
| R2-1 | **Blocker** | The new unique index also caps **revocation** at one per delegate and scope. |
| R2-2 | Major | `isMintingOperation` is now a proxy for "harmless", and defaults to fail-open for any future action. |
| R2-3 | Major | An unmapped permission **grants** access in `handleOrphanedDelegation`. |
| R2-4 | Minor | `backfillCompleted` fails open on a store fault, and its caching comment is false. |
| R2-5 | Major | The ceiling ignores the request's scope, so authority in one scope can authorise an action in another. |

#### R2-1: the unique index breaks revocation

The index is on `(delegate_type, delegate_id, scope_type, scope_id,
active)` and is a plain unique index, not a partial one. Including
`active` as a **column** yields two guarantees rather than one: at most
one active row per delegate and scope, which is intended, and at most
one **inactive** row, which is not.

`delegation_edge_store.go:136` sets `Active(false)`, so revocation is
real. Create, revoke, create, revoke then violates the constraint on the
second revocation.

Revocation is a security operation. It must not begin failing on its
second use. The fix is a **partial** unique index — unique on the four
identity columns `WHERE active = true`. In Ent that is the
`entsql.IndexWhere` annotation, available on SQLite and Postgres. MySQL
has no partial indexes; there, use a nullable column that is `NULL` when
the edge is inactive, because `NULL`s do not collide.

A second half to this finding: applying a unique index to a table that
already holds duplicates **fails the migration**. Any hub interrupted
mid-backfill under `3597507` holds exactly the duplicates that R1
described, and will not start. Production risk is low because Phase 1G
is unreleased; developer and staging hubs are the exposure. Add a dedup
pass before the index is created, or document the recovery.

#### R2-2: the fail-open default is now load-bearing

`isMintingOperation` decides three separate fail-open branches: store
faults, duplicate edges, and orphaned-delegation reads. The comments
call the non-minting set "reads". It is not. That set contains
`ActionDelete`, `ActionUpdate`, `ActionStop`, `ActionStopAll`,
`ActionRemoveMember`, `ActionAttach`, `ActionPortAccess` and
`ActionDispatch`.

So an agent whose delegator provably does not exist may still delete,
update, stop, attach and port-access at its frozen ceiling, and a
transient store fault permits the same.

The structural fault is larger than the present list. **The switch
returns false by default, so every action added in future is
automatically fail-open.** Whoever adds the next action will not know.

**Invert the default.** Allowlist the genuinely safe operations — read,
list, status, verify — and fail closed for everything else. Rename the
predicate; "minting" no longer describes what it decides.

#### R2-3: an unmapped permission grants access

`permissionToAgentScope` returns `""` when a permission is absent from
the registry or carries no agent scopes. `handleOrphanedDelegation`
reads `""` as "allow at baseline". For a delegation whose delegator
provably does not exist, an unmapped permission must **deny**, and log
the unmapped permission ID, so that a registry gap appears as a denial
rather than as a silent grant. This is "absence is not permission"
again, in code written after the rule was adopted.

#### R2-4: the completion guard fails open

`backfillCompleted` returns `err == nil`. A store fault therefore reads
as "not yet complete", which allows. This is the same `ErrNotFound`
versus store-fault distinction that 1G-6 fixed elsewhere, not applied
here. Its comment also claims a cache that does not exist; the call is a
store read on every no-edge decision. Add the cache or delete the
sentence, because a false claim of caching invites a hot-path caller.

#### R2-5: the ceiling ignores the request's scope

Raised by the Phase 1G code reviewer as finding O1, and classified by
them as optional. **We disagree.** The reviewer found one half of it.
The other half fails open.

The half they found is fail-closed: `activeCount` counts every active
edge for the delegate across all scopes, while the unique constraint is
per scope, so a legitimately multi-scoped agent trips the invariant
check and is denied minting. That is an availability fault, and it is
safe.

The half they missed is that **the evaluation loop does not filter by
scope either.** It takes the first active edge in creation order and
then calls `checkUserHoldsPermission` with `edge.ScopeType` and
`edge.ScopeID` — the *edge's* scope, not the *request's*. So for a
request against project P2, the ceiling can be satisfied by the
delegator's permissions in project P1. Authority in one scope
authorises an action in another.

The clearest case: an agent holds an edge in P1 and **no** edge in P2,
and acts in P2. The correct answer is denial — no authority was
delegated in that scope. The present code finds P1's edge, evaluates it,
and can allow.

**On the reviewer's justification.** "Agents are single-project in
practice" is the third safety argument in this review to rest on current
practice rather than an enforced invariant. The earlier two were "agents
have at most one active edge" (disproved by the interrupted backfill in
R1) and "the pre-migration window is empty" (1G-2). Both were reachable.
This one probably is not reachable today, because both creation sites
are project-scoped from the agent's own project. The point is that the
argument has failed twice, and here it guards a security boundary rather
than a latent bug.

**The load-bearing reason to fix it now** is that the invariant *check*
does not test the same tuple as the *invariant*. The constraint is per
scope; the check is across scopes. A detector that does not match what
it detects is worse than no detector: it raises false alarms, which
trains everyone to ignore the error line, and it still misses the real
violation.

**Fix.** Filter the edge set by the request's scope before counting and
before evaluating. `Resource` carries `ParentType` and `ParentID`, so
the project scope is available. Filter in `walkDelegationChain` rather
than in the store — the set is tiny, and a store-level filter would need
the scope added to the cache key. Apply the same request scope at every
depth of the chain walk. This makes `activeCount` match the constraint,
confines evaluation to the correct scope, and routes the
authority-in-another-scope case to the no-edge denial where it belongs.

#### An operational gap, not a defect

An agent skipped by the parse-failure branch receives no edge, is denied
permanently once the marker is written, and leaves one error line at
start-up as its only trace. Record skipped agents in an enumerable list.
The same applies to the fail-the-whole-migration behaviour in 1G-7: that
is what we asked for, but a hub that will not start needs a documented
recovery, or it becomes a support call with no stated fix.

---

### 11.4 Phase 1G at commit `1bd2203` — round three

R2-1, R2-2 and R2-3 are correct. R2-1's dedup pass is properly placed
before `entc.AutoMigrate`, is idempotent, and keeps the oldest row.
R2-2's inversion is exactly the requested shape. Two findings remain,
one blocking.

#### R3-1 (blocker): the scope derivation denies most agent requests

`requestScopeFromResource` maps anything not project-**parented** to
`(system, "")`. No system-scoped edges exist, so the filter returns an
empty set, the no-edge path runs, the marker is present, and the agent
is **denied**.

Counted across `pkg/hub`, excluding tests: **76 `Resource` literals, 17
set `ParentType`, 59 do not.** The ceiling therefore denies agents on
roughly three quarters of the authz call sites.

Two faults sit inside that:

1. A resource that **is** a project has no parent. The common literal is
   `Resource{Type: "project", ID: project.ID, ...}`, which maps to
   system scope and denies. It should map to `(project, r.ID)`.
   Agent-reachable paths hit this today in `handlers_env_secrets.go`,
   `handlers_shared_dirs.go` and `project_settings_handlers.go`.
2. Everything else omitting `ParentType` silently becomes system scope,
   and therefore denies.

**Why the tests passed.** All twelve fixtures in
`delegation_ceiling_test.go` build
`Resource{Type: "agent", ParentType: "project", ParentID: projectID}`.
The suite validates a shape that production mostly does not produce. At
least one test must use a `Resource` built by a real handler path rather
than synthesised in the test.

**The design problem, not just the bug.** Scope is inferred from an
optional field that each call site must remember to populate, and the
failure mode is a silent deny. That is D2's shape again: correctness
depending on every handler doing the right thing.

**Fix — derive scope from the principal, use the resource as a
cross-check.** The agent's own project is always known through
`AgentIdentity.ProjectID()` (`identity.go:41, 190`), and it is exactly
the scope the edges were created with: the backfill uses `a.ProjectID`
and both creation sites use the agent's project ID. So default the
ceiling scope to the agent's own project, and when the resource resolves
to a *different* project — through `ParentID`, or through `r.ID` when
`r.Type == "project"` — treat it as a cross-project request, require an
edge in that project, and deny when there is none. This keeps the
cross-scope leak closed, which is what R2-5 was for, while removing the
dependence on 76 call sites populating a field correctly.

#### R3-2 (major): the completion latch can cache "pre-backfill" forever

`backfillOnce.Do` caches whichever answer arrives first, in **both**
directions. If the first call lands before the marker exists,
`backfillDone` stays false for the process lifetime and every edge-less
agent is allowed until restart.

Today the backfill runs during store initialisation before serving, so
the first call should see the marker. That is start-up ordering, not an
invariant — and "safe because of current ordering" is now the fourth
such argument in this phase. The previous three were all reachable.

**Fix: cache only the monotonic, safe direction.** "Completed" is
permanent, so latch true and stop querying. "Not completed" is
transient, so do not cache it. An atomic bool that only ever moves from
false to true is sufficient, and it costs one read per no-edge decision
during the pre-backfill window alone.

---

### 11.5 Phase 1G at commit `145444f` — round four, approved

R3-1 and R3-2 are both correctly fixed. The delegation ceiling is
approved for the integration push.

**R3-1.** The scope now comes from `AgentIdentity.ProjectID()`, with
`resourceProjectScope` as a cross-check. That helper handles the
`Type == "project"` case, which was the main breakage, so the 59 call
sites that omit `ParentType` now resolve to the agent's own project
instead of denying. When the resource is in a different project, the
scope moves to that project: an agent that holds a genuine edge there is
allowed, and one that does not is denied.

**R3-2.** The `atomic.Bool` latch only caches the false-to-true
direction. `ErrNotFound` returns false without caching, and a store
fault returns true — fail closed — without latching, so a transient
fault cannot become permanent.

**Tests.** The shape problem from 11.4 was fixed, not papered over. The
suite now uses production-shape literals with no `ParentType`, covers
the `project_settings` variant, asserts a cross-project denial, and unit
tests both `resourceProjectScope` and the monotonic latch.

#### A correction to a finding this review nearly raised

The chain walk passes the **child's** scope when it looks up the
**parent's** edges. This first read as a defect, because it contradicts
the "derive scope from the principal" principle that the R3-1 fix
adopted: at depth > 0 the principal is the parent, so its own project
looked like the right scope.

That reading is wrong. The parent-to-child edge is scoped to the
child's project, so the parent must hold authority **in that project**
for the grant to have been valid. Filtering the parent's edges by the
child's scope is therefore the correct semantic, and the resulting
denial of cross-project chains is a security property rather than a bug:
an agent cannot confer authority in a project where it holds none.

The behaviour is currently emergent and undocumented, which makes it
fragile — a later reader could "fix" it. It needs a deliberate test: a
parent in project Q creates a child in project P, and the child's chain
check denies because the parent holds no edge in P.

#### Two minor notes, neither blocking

1. `scopeType` is hard-coded to `store.RoleScopeProject`, so a
   system-scoped edge could never match. No such edges exist, and the
   effect is protective. It needs a comment saying it is deliberate,
   because it reads as an oversight.
2. An identity that is not an `AgentIdentity`, or one with an empty
   `ProjectID()`, leaves the scope empty, matches no edge, and is denied
   silently. Fail closed is correct, but it needs a log line so that a
   token missing its project ID is diagnosable.

#### Scope of the approval

This covers the delegation ceiling and its migration, across four
rounds. The other ~34 files in `b5c694d..145444f` were **not** reviewed.

D10 and D11 remain outstanding and are not part of 1G. They land as
standalone fixes on `scion/auth-refactor` before the consolidated PR
opens. D10 must not trail the PR, because `IsSystemAdmin` short-circuits
the ceiling that Phase 1G builds.

---

### 11.6 D10 and D11 at commit `d45d1a8f` — shape check

D10 is sound. D11 has one ordering defect to fix before the PR, plus
two smaller items.

#### D10 — correct, and correctly placed

The guard sits at the store level, which is the right layer. Three
checks confirm it is complete:

- `CreateRoleBinding` is the **only** binding-creation path. No
  `UpdateRoleBinding` exists, so there is no create-benign-then-mutate
  route to super-admin.
- The guard matches the role-definition **name at any scope**.
  `IsSystemAdmin` requires system scope **and** that name, so the guard
  is strictly broader than the check it protects.
- A separate all-permissions definition under a different name does not
  trip `IsSystemAdmin`, so it runs through normal policy evaluation.
  That is the "functionally super-admin-like, but still passed through
  every check per grant" model from open question 2, not a hole.

#### D11 finding 1 — the forward pass can grant what the reverse revokes

The forward loop keys on `u.Role == "admin"` and never consults
`adminSet`. For a removed admin whose stored role is still `admin` and
who holds no binding, start-up runs:

    forward pass  -> CREATES a super-admin binding
    reverse pass  -> demotes the role, DELETES the binding

The net result is right, but the order is backwards and the passes are
not atomic. If the reverse pass takes its `ListUsers` error path it
returns early, and `server.go` only warns, so the hub still starts. A
reconciliation whose purpose is revocation has then issued a fresh
super-admin binding to the user it was meant to revoke.

The precondition is reachable: `Role == "admin"` with no binding is
exactly the state the forward pass exists to repair, so it is the
expected state on the upgrade that introduces D11.

**Fix.** Gate the forward creation on `inAdminList` whenever the list is
usable. Better, fold both directions into one pass per user, so no user
is granted and revoked in the same run.

#### D11 finding 2 — the variadic parameter repeats the D2 shape

`ReconcileSuperAdminBindings(ctx, s, adminEmails ...[]string)` degrades
**silently** to warn-only when a caller omits the argument. This is the
`ParentType` problem again: correctness depends on every call site
remembering an optional field, and the failure mode is quiet. It was
done for test compatibility, and there is exactly one production call
site. Make the parameter required.

Related: `emails == nil` takes the warn-only branch and
`len(emails) == 0` takes the guard branch — two guards for one
condition, resting on a nil-versus-empty distinction that Go code
routinely conflates. Both paths are safe, so this is not a bug, but they
should be collapsed.

#### D11 finding 3 — login demotion and binding deletion diverge

`determineUserRole` demotes `User.Role` at login. Nothing deletes the
super-admin binding until restart. In that window
`IsUnscopedLocalPlatformAdmin` returns false while `IsSystemAdmin`
returns **true**: the user looks demoted and still bypasses the ceiling.

The residual privilege sits inside the restart latency the sponsor
accepted, so this is not a security regression. The defect is that
`identity.go:137` now claims the reconciliation "ensures bidirectional
consistency", and in that window it does not. An operator who sees the
demotion land would reasonably conclude revocation had happened. Either
delete the binding at login too — preferred, because it makes the fast
path and the authoritative path agree — or narrow the comment.

#### Minor

Reconciliation failure at start-up is `slog.Warn` and non-fatal. That
predates this commit, but the function now carries revocation semantics,
so the failure matters more than it did.

#### Verified clean — checked and not reported

- The demotion is a read-modify-write through `UpdateUser`, which clears
  `AvatarURL`, `InvitedBy`, `InviteNote` and `Preferences` when empty.
  `entUserToStore` hydrates all four in `ListUsers`, so no user data is
  wiped.
- `CreatedBy` sentinel collision: the non-system call site passes a user
  ID, which is a UUID, and `system-reconcile` is not a valid UUID.

#### Test gaps

The 11 tests cover the stated acceptance criteria, including the
functional-admin regression trap. Nothing exercises finding 1's
create-then-delete ordering, and nothing asserts the window in
finding 3. Each fix needs a test.

---

### 11.7 D11-fix2 at `f5c7cf2c` and D8 at `2cf6c7ea` — shape check

Both are structurally right. Each has one finding to fix before the PR.

#### D11-fix2 — the three fixes are correct

Single-pass reconciliation grants and revokes in one branch per user, so
the grant-then-revoke window from finding 11.6/1 is gone. The required
parameter removes the silent-degradation trap, nil and empty collapse to
one branch, and the empty-list case disables **both** directions.
Login-time deletion is gated on the demotion having actually happened.

#### D11-fix2 finding — unnormalized email matching, now destructive

Stored emails are normalized with `normalizeEmail` = `TrimSpace` +
`ToLower`. Both matchers — `determineUserRole` and the reconciler — use
plain `strings.ToLower`, with **no** `TrimSpace`.

`AdminEmails` is sanitized in one narrow case only: `hub_config.go:835`
and `:1204` apply `parseCommaSeparatedList` when the list has exactly
one element containing a comma, which is an env-var fixup. A native YAML
or JSON list is never trimmed, and empty entries are never dropped.

So `adminEmails: ["  admin@example.com"]` — a leading space, which YAML
makes easy — produces no match. The legitimate administrator is
**demoted** and their super-admin binding **deleted** at start-up, and
cannot be re-promoted at login while the typo persists. If every entry
carries stray whitespace, every administrator is removed. The empty-list
guard does not fire, because the list is not empty.

Under the old additive-only semantics the same typo was harmless: it
failed to promote, and the stored role was preserved. D11 converts it
into silent, hub-wide loss of administrators.

**Fix.** Normalize both sides with the store's `normalizeEmail`
semantics, and drop empty entries. Better, sanitize `AdminEmails` once
at config load for all list shapes.

**The architectural point.** The empty-list guard protects against one
*input shape* that causes mass demotion. The invariant that matters is
about the *effect*: never remove every administrator in a single pass.
Guarding the input admits any other route to the same outcome —
whitespace here, and the login-deletion path before it. A guard on the
effect subsumes the empty-list guard and closes routes nobody has
thought of yet.

Not a finding: an empty entry making `adminSet[""]` true cannot promote
anyone, because the user schema declares email `NotEmpty()`.

#### D8 — the gate is correct

It mirrors the members side properly: project ID, annotation and
`GroupType` are all required before adoption. Refusing to adopt leaves
the project with no agents group, which is the right trade. Annotations
are copied rather than clobbered, and the completion marker is written
only after a clean run.

#### D8 finding — the backfill destroys the evidence it records

On owner mismatch the backfill logs a warning and then **marks the group
anyway**. The group is then annotation-identical to a legitimate one, so
no later query can separate them. The backfill is once-only, so the
warning is never regenerated. The single signal that distinguishes a
squat is emitted once, to a log, and is then made permanently
unrecoverable by the marking that follows it.

The commit message is honest that adoption is not undone. This is worse
than stated: it is not only un-undone, it is un-investigable once the
log rotates.

**Fix.** Record the mismatch durably as a second annotation —
`scion.io/adoption-review-required: "true"` — so operators can query for
suspect groups instead of grepping start-up logs. This is better than
refusing the mark, because a project ownership transfer changes the
project owner without changing the group owner and would produce false
positives.

---

### 11.8 Specification of the demotion effect guard

The guard recommended in 11.7 must be stated carefully. The obvious
phrasing — "refuse to demote when it would remove all current admins" —
is wrong, and it breaks the most common administrative operation.

Take the ordinary rotation: the hub has one admin, `alice`, replaced by
`bob`. Config goes from `[alice]` to `[bob]`. Current admins are
`{alice}`, so demoting alice removes all of them and the guard refuses.
Alice keeps super-admin permanently, through the mechanism built to
revoke it. A guard that blocks routine rotation gets disabled by the
first operator who meets it, and then it protects nothing.

The predicate must describe the **resulting** state, not the demotion
set:

> Refuse all demotions when the pass would leave **zero**
> administrators.

Concretely: build the intended final admin set — existing users whose
normalized email appears in `AdminEmails`. When that set is empty,
refuse every demotion and log at Error. When it has at least one
member, proceed.

| Case | Intended set | Behaviour |
|---|---|---|
| `alice` replaced by `bob` | `{bob}` | Demotion proceeds. No false positive. |
| Whitespace on every entry | empty | All demotions refused. |
| `AdminEmails` empty | empty | All demotions refused. |

The third row makes the separate empty-list guard redundant, which was
the objective.

**Structural consequence.** This cannot be evaluated inside the
single-pass loop from `f5c7cf2c`, because the decision depends on the
whole user set while the pass acts user by user. The intended admin set
must be computed **before** any mutation — a classifying pre-pass, then
an applying pass. A running counter inside the existing loop is not a
substitute: it cannot see users not yet visited, so the guard would fire
or not fire according to cursor order. That is the row-order dependence
already corrected once in finding R1.

The intended set must also count **matching existing users**, not
configured email strings. A config naming only people who have never
logged in would otherwise pass the guard and demote every real
administrator.

---

## 12. Acceptance Criteria

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
      delegator loses it. Per the sponsor decision of 2026-08-25, this
      applies to **existing** agents too, and not only to agents created
      after F1.7. Test an agent that the role backfill touched.
- [ ] **No** agent is exempt from the delegation ceiling. The sponsor
      chose the starting-point reading on 2026-08-25, so an existing
      agent keeps its current role as a starting value only. Test an
      agent that the role backfill touched: its authority must fall when
      its delegator's authority falls. See open question 6.
- [ ] A revoked agent token is refused at authentication, before its
      10-hour expiry.
- [ ] A revoked but unexpired token cannot obtain a fresh, unrevoked
      credential by any refresh path. Revocation survives the minting of
      a new `jti`. See section 6.4.
- [ ] A federated token whose `ancestry[0]` does not equal its
      `root_user` is refused at authentication. (D9, section 10.2.)
- [ ] `allowed_root_users` constrains every element of the ancestry, and
      not `root_user` alone. A federated agent cannot name an ancestor
      outside that list. (D9.)
- [ ] A federated agent cannot satisfy a `DelegatedFrom` condition by
      naming a principal in a token that the hub did not sign. (D9.)
- [ ] A local agent with the same ancestry is still allowed. This proves
      that the D9 repair did not break normal delegation, which is the
      real regression risk.
- [ ] A federated agent with no store-recorded delegation edge resolves
      to the **lowest** role. Assert the resolved role explicitly, not
      only the denial. A denial can pass for the wrong reason, and the
      role value is what proves the ceiling started at the floor.
- [ ] No authority computation starts at the highest role and is
      lowered. Each starts at the floor and is raised only by a grant
      that a lookup positively returned. See "absence is not permission"
      in section 6.4.
- [ ] The D9 denial holds on a hub where `allowed_root_users` is
      **unset**. This pins that the default configuration is covered by
      the hub-attested check, and not by the allowlist. It is the test
      that fails if someone later removes the hub-attested check in the
      belief that the allowlist covers it.
- [ ] A trusted issuer configured with `default_role: "admin"` is
      refused at config load. `DefaultRole` is validated against the
      canonical role registry. (Section 10.2.)
- [ ] The unified pipeline decides the federated agent case explicitly.
      No path depends on `ProjectID()` being empty to deny. (Section
      10.2.)
- [ ] The explain API returns the rule that decided a request.
- [ ] Project membership is keyed by the immutable project ID, not by a
      slug. The relation is created in the same transaction as the
      project.
- [ ] The project **agents** group is adopted on the same terms as the
      members group. A pre-existing group with a colliding slug is
      refused unless it already carries the project's immutable ID and
      the system annotation. Test the upgraded-hub case: a group created
      under that slug before the prefix reservation is **not** adopted.
      (Section 4.3, repair status.)

The following six items come from the Phase 1G review. They are the
tests that must pass before that phase merges. (Section 11.1.)

- [ ] An agent with a grandfathered edge **loses** a permission when its
      delegator loses that permission.
- [ ] An agent whose edge was created by the backfill is subject to the
      ceiling. There is no bypass.
- [ ] An agent whose delegator is `system/migration` keeps its reads at
      its recorded role, cannot mint, and cannot escalate.
- [ ] `ActionMint` and `ActionAssign` fail **closed** on a store error.
- [ ] An empty `AppliedConfig` during the backfill does not produce a
      full-role edge.
- [ ] `AncestryIsHubAttested` gives false for **each** federated
      identity type.

From D10 and D11 (section 5.6):

- [ ] An attempt to create a system-scoped role binding to the
      super-admin role definition is **refused by the store**, for every
      caller except the system reconciler. Test it through the F1.5 API,
      not only through the store.
- [ ] A user made functionally admin-like by grants passes each policy
      check individually, and `IsSystemAdmin` returns **false** for
      them.
- [ ] Removing a user from `AdminEmails` and restarting demotes
      `User.Role` **and** deletes their system super-admin binding.
      `IsSystemAdmin` then returns false.
- [ ] Starting with an **empty** `AdminEmails` list does **not** demote
      anybody. The hub logs the refusal to converge downward.
- [ ] A user who is functionally admin-like through ordinary grants, and
      who is **not** in `AdminEmails`, keeps **every** one of those
      grants across a restart. Reconciliation removes only `User.Role`
      and the system-scoped super-admin binding. This is the regression
      test for the scope limit in D11.
- [ ] The Phase 1G ceiling tests do not pass by way of the super-admin
      short-circuit. The test subject must not hold a super-admin
      binding.

From round two (section 11.3):

- [ ] An agent is revoked, re-delegated, and revoked **again**, in the
      same scope. The second revocation succeeds. This is the R2-1
      regression test.
- [ ] A hub that already holds duplicate active edges upgrades
      successfully. The migration deduplicates before it applies the
      unique index.
- [ ] An orphaned delegation cannot `delete`, `update`, `stop`,
      `attach`, or `port_access`. Only genuinely safe reads pass.
- [ ] An action that the classifier does not recognise fails **closed**.
      Add a deliberately unclassified action to the test to prove the
      default.
- [ ] A permission with no agent-scope mapping is **denied** for an
      orphaned delegation, and the unmapped permission ID is logged.
- [ ] A store fault while reading the backfill marker does not allow an
      authority-affecting action.
- [ ] Agents skipped by the backfill are enumerable after the migration,
      not only visible as a start-up log line.
- [ ] An agent holding an edge in project P1 and **no** edge in project
      P2 is **denied** for an action in P2, even when its P1 delegator
      still holds the permission. This is the R2-5 regression test.
- [ ] The duplicate-edge invariant check counts only edges in the
      request's scope, so an agent legitimately scoped to two projects
      does not trip it.

From round three (section 11.4):

- [ ] An agent acting on a resource whose literal is
      `Resource{Type: "project", ID: <its own project>}` — with no
      `ParentType` — is **allowed**, subject to the ordinary ceiling.
      This is the R3-1 regression test.
- [ ] At least one ceiling test drives a `Resource` produced by a real
      handler path, not one synthesised inside the test.
- [ ] The backfill completion check, called once **before** the marker
      exists and again after, returns false then true within a single
      process. A "not yet complete" answer is never cached.

From finding R1 (section 11.2):

- [ ] A second **active** edge for the same delegate and scope is
      refused by the store.
- [ ] Running the backfill twice produces the same edge count as running
      it once. Test the interrupted case: stop the migration part way,
      restart it, and confirm that no agent has two edges.
- [ ] Where more than one active edge is somehow present, an
      authority-affecting action fails closed and an error is logged.
      The result does not depend on row order.

### Phase 2

- [ ] A limit is enforced under concurrency. 100 parallel creations
      against a limit of 10 produce exactly 10.
- [ ] A subject that matches several entitlement bindings resolves by
      the documented merge rule.

---

## 13. Sources

All line numbers are from branch `scion/review-arch`, commit `89ed0fe`,
except in sections 10.2 and 11.1. Those are from
`origin/scion/auth-refactor`, and section 11.1 is at commit `3597507`.

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

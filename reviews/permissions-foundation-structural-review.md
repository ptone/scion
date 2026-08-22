# Structural Review: Permission System Foundations

**Date:** 2026-08-22
**Author:** review-architect
**Requested by:** ptone@google.com
**Input document:** Role-Review.md (three audits, dated 2026-08-12 and 2026-08-21)
**Code base:** `origin/main` at commit `89ed0fe`
**Language:** ASD-STE100 Simplified Technical English

---

## 1. Problem and Goals

The team plans to add these user-facing features:

- Admin permissions and hub permissions for a subset of hub resources.
- User access tokens with more granular permissions.
- Limits on users, based on the group membership level.

The question is: what foundational changes must come first?

A second question is: are there concerns about the human principal and the
agent principal?

This document answers both questions. It is a review, not an implementation
plan for the features.

**Success criteria for the foundation work:**

1. An operator can write a rule that removes a permission. The rule must give
   the same result each time.
2. A new HTTP route cannot go into production without an authorization
   decision.
3. One permission vocabulary controls policies, agent tokens, user tokens, and
   the user interface.
4. An agent cannot hold more authority than the human who created it.
5. An operator can remove an agent's access, and the removal takes effect.

---

## 2. Non-Goals

This document does not do these things:

- It does not design the three features. It designs what must come first.
- It does not list the missing authorization checks again. The input document
  lists them correctly. Section 4.2 explains why that list is a symptom.
- It does not cover the GCP service account paths in detail. Those paths are
  the best-documented part of the system.
- It does not cover the runtime broker trust model.
- It does not select a replacement policy engine. Section 6 explains when that
  decision becomes safe to make.

---

## 3. Summary of the Answer

**Do not build the three features on the present foundation.**

The input document is accurate. Its conclusion is too optimistic. The document
treats the problem as a set of missing checks. The problem is the shape of the
system that the checks sit in.

There are three defects that block the three features directly. Each one is a
property of the engine, not a missing call site.

| # | Defect | Which feature it blocks |
|---|---|---|
| D1 | The engine cannot express a reliable deny. | All three. |
| D2 | Enforcement is optional per handler. Omission gives access. | Granular tokens, scoped admin. |
| D3 | There are three permission vocabularies that disagree. | Granular tokens. |

There are two more defects that block the human and agent question.

| # | Defect | Effect |
|---|---|---|
| D4 | An agent's authority is never limited by its creator's authority. | An agent can exceed the human who made it. |
| D5 | An agent token cannot be revoked. | "Remove access" is not true. |

There is one defect that blocks the limits feature specifically.

| # | Defect | Effect |
|---|---|---|
| D6 | No storage exists for a per-group or per-user setting. | Limits by membership level need a schema change. |

The answer to the second question is in section 5. In short: the distinction
between a human and an agent is correct. The problem is that the system treats
the two as unrelated sources of authority. It must treat the agent as an actor
that works on behalf of a human.

---

## 4. Findings

### 4.1 D1 — The engine cannot express a reliable deny

This is the most important finding. Read this section first.

The evaluation loop is in `pkg/hub/authz.go:455-503`. It reads `policy.Effect`
exactly once, at line 473, to set the result. It has no rule that gives a deny
priority over an allow.

The loop is last-match-wins:

```go
if newLevel > matchedLevel {
    matched = &d          // a more specific scope replaces the match
} else if newLevel == matchedLevel {
    matched = &d          // the same scope: the last policy seen replaces it
}
```

The order of the policies comes from the database query in
`pkg/store/entadapter/policy_store.go:463-466`:

```sql
ORDER BY access_policies.scope_type, access_policies.priority
```

There is no third sort key. There is no `effect`, no `id`, and no `created`.

Three results follow. An investigation confirmed all three by execution against
the SQLite test store.

**(a) A specific allow always defeats a broad deny.**
A hub-scoped deny with priority 1000 lost to a project-scoped allow with
priority -1000. Scope always defeats priority. The repository asserts this
behaviour as correct in `pkg/hub/authz_test.go:163-199`.

The consequence: a hub administrator cannot remove a permission that any
project-scoped policy grants.

**(b) An allow and a deny at the same scope and the same priority give an
undefined result.**
Six test runs alternated the insert order of one allow and one deny. Both were
hub-scoped. Both had priority 5.

```
insert order allow, deny  ->  denied
insert order deny, allow  ->  allowed
```

The decision follows the physical row order. Nothing fixes that order. On
PostgreSQL an `UPDATE` to an unrelated field can move a row. The decision can
change with no change to any policy.

**(c) The shipped seed data reaches case (b).**
`hub-member-read-all` is hub-scoped with priority 0 (`pkg/hub/seed.go:51-60`).
`CreatePolicyRequest.Priority` is a plain `int`, so an omitted value is 0
(`pkg/hub/handlers_policies.go:51`). An operator who writes a hub-scoped deny to
remove read access lands in case (b) by default.

No test covers case (b).

Two further points:

- The `Priority` field is never read by the engine. Grep confirms this. Its
  only effect is the position in the sorted list. Higher priority wins as a
  side effect of two unrelated mechanisms. The comment at
  `pkg/store/models.go:1273` states the opposite of the behaviour.
- `PolicyConditions.SourceIPs` is declared and stored, but no evaluator reads
  it. It is a dead field that appears to work.

**Why this blocks all three features.** Each feature is a statement about what
a principal must *not* do. A granular token is a restriction. A limit is a
restriction. A scoped admin is an administrator who must not act outside the
scope. The system cannot express a restriction that holds.

**This defect is load-bearing.** The precedence rule appears in the stored
policy data, in operator training, and in the user interface. A change to it
later re-interprets every policy in every deployment.

### 4.2 D2 — Enforcement is optional, and omission gives access

The input document lists nine gaps in section 6, nine handlers with the
`if userIdent != nil` pattern in section 7, and about thirty routes marked
"none" in the endpoint inventory.

These are not thirty bugs. They are one bug, thirty times.

The router has about 126 routes. Each handler decides for itself whether to
call `s.authorize(...)`. A handler that calls nothing returns data. There is no
place that can observe the omission.

The team already fixed this class of bug once. Issue #591 produced
`pkg/hub/authorize.go`, a good file with clear guards and honest comments. The
guards are correct. They are still opt-in. The next handler will omit them
again.

**The structural fix is to move the decision out of the handler body.** The
route table must declare the resource and the action. A route with no
declaration must fail a build-time test, not a runtime check.

Evidence that the present shape does not hold:

- `PATCH /api/v1/projects/{id}/agents/{agentId}` has no check
  (`handlers_projects_core.go:1915`). The equivalent non-project route at
  line 1965 has one. The same operation, two routes, two answers.
- The whole workspace file surface, WebDAV, providers, GitHub integration, and
  git identity have no check at all. These write to disk and store secrets.
- `submitAgentEnv` has no check in the handler body.

**This defect is reversible.** The mechanism is mechanical work. The cost grows
with every route added before the fix.

### 4.3 D3 — Three permission vocabularies that disagree

There are three separate lists of permission names. They overlap in part and
contradict in part.

| Vocabulary | Count | Where | Form |
|---|---|---|---|
| Policy actions | 19 actions x 11 resource types | `authz.go:31-53` | `Action` constants |
| Agent token scopes | 11 | `agenttoken.go` | `project:read`, `agent:status:update` |
| User token scopes | 13 | `models.go:1487-1518` | `agent:read`, `project:update` |

There is a fourth list. The web user interface hard-codes 11 scope names at
`web/src/components/shared/token-list.ts:46-66`. The CLI hard-codes a fifth
list at `cmd/hub_token.go:73-85`.

The lists are already wrong. Concrete cases:

- `enforceUATConstraints` builds the required scope as
  `resource.Type + ":" + action` (`authz.go:604`). Agent start, stop, and
  message all route through `authorizeAgentLifecycle`, which checks
  `ActionAttach` (`authorize.go:227`). The produced string is `agent:attach`.
  Therefore the user token scopes `agent:start`, `agent:stop`, and
  `agent:message` **do nothing**. A token that holds all three cannot start,
  stop, or message an agent.
- `agent:dispatch` is a valid user token scope. `ActionDispatch` is only ever
  paired with the `broker` resource. The string `agent:dispatch` is never
  produced. The CLI offers it as the headline example.
- The workspace handlers need `agent:update`. That is not a valid user token
  scope. A user token is always denied there.
- The user interface omits `project:update` and `agent:port_access`. The second
  one is one of the few scopes that works.
- Two agent scopes are dead. No handler reads `ScopeAgentLogAppend` or
  `ScopeProjectSecretRead`.

**Why this blocks the granular token feature.** The feature adds many more
names to a vocabulary that is already incorrect in five places. Each new name
multiplies the mismatch. Users will hold tokens that state a permission the
token does not have. That is worse than a coarse token, because it is not true.

**This defect is load-bearing.** Scope strings appear in issued tokens, in
scripts, and in documentation. A rename is a breaking change for every token
holder.

### 4.4 D7 — Admin is a code bypass, not a grant

The stated goal is to give hub-level permissions over a subset of hub
resources. The present system cannot express that.

`user.Role() == "admin"` returns allow at `authz.go:127` before any policy runs.
There are 37 more hand-written admin checks in `pkg/hub`. Each one compares a
string. None of them names a resource.

Admin is therefore a single boolean over the whole hub. It has no subset.

There is a second problem. `requireAdmin` (`authorize.go:254-282`) accepts a
`ScopedUserIdentity`. A user access token embeds the minting user's role. An
administrator's project-scoped, read-only CI token passes `requireAdmin`. It
can then write hub policies and skill registries. There are 14 such call sites.

The team knows about this. `requireHubAdmin`
(`hub_pre_start_hook_handlers.go:54-69`) adds the missing rejection, and its
comment states the escalation clearly. The fix was applied to one handler
family out of many.

**Recommendation:** admin must become a grant that the engine evaluates, not a
branch that skips the engine. Only then does "admin over subset X" have a
place to live.

### 4.5 D6 — There is no place to store a limit

The team wants limits based on the group membership level. An investigation
found the following.

**No plan, tier, or entitlement concept exists.** The search covered
`pkg/store/models.go` and all 43 files in `pkg/ent/schema/`. There are zero
matches.

**A group membership level is a three-value enum.** `member`, `admin`, `owner`
(`pkg/ent/schema/groupmembership.go:40-42`). It carries no capacity.

**Hub settings cannot be scoped.** The `hub_settings` table holds one row per
section, with a **unique index on `section`**
(`pkg/ent/schema/hubsetting.go:43-45`). There is no `scope_type`, `group_id`,
`project_id`, or `user_id` column. Per-group settings are structurally
impossible without a migration.

**Policy conditions cannot hold a number.** `PolicyConditions` has six fields:
labels, two times, source IPs, and two delegation fields
(`pkg/store/models.go:1296-1303`). There is no numeric field, no comparison
operator, and no extension field.

**Almost no limits exist today.** The investigation found that
`MaxSubscriptionsPerUser`, `GCPMintCapPerProject`, and `GCPMintCapGlobal` are
`hub.Config` fields that no flag, environment variable, or settings key ever
sets. In every real deployment all three are unlimited. The only count limit
that operates is `UATMaxPerUser = 50`.

**The hub cannot count cheaply.** The `agents` table has exactly one index:
`(slug, project_id)` unique (`pkg/ent/migrate/schema.go:86-105`). A query for
"how many agents does user X run now" is a full table scan. There is no
`CountAgents` and no `CountRunningAgents`.

**There is no audit table.** `pkg/hub/audit.go` writes to `slog` and always
returns nil. No audit entity exists in the schema. Usage cannot be queried.

**The agent execution limits are not enforced by the hub.** `MaxTurns`,
`MaxModelCalls`, and `MaxDuration` are counted inside the container by
`pkg/sciontool/hooks/handlers/limits.go`. A modified image ignores them. The
hub applies no ceiling and no clamp.

### 4.6 Two operational cliffs that are silent

These are not design defects, but they will produce incorrect decisions at
scale. Record them now.

- `checkDelegation` reads policies with `Limit: 200` (`authz.go:397`). Policy
  201 and later are never evaluated. The system creates one policy per secret,
  per environment variable, and per injected skill that sets `AllowProgeny`
  (`handlers_env_secrets.go:617`, `:693`, `handlers_skills_injection.go:836`).
  A busy hub will cross 200.
- `isProjectOwnerOrAdmin` reads groups with `Limit: 10` (`authz.go:648`). An
  eleventh project-scoped group is never consulted.

Both fail closed, which is correct. Both fail silently, which is not.

There is no caching. One `CheckAccess` for a normal user costs about seven
database queries. `ComputeCapabilities` calls `CheckAccess` once per action, so
one agent's capability set costs about 50 queries (`capabilities.go:196-230`).
The group lookup is N+1 by construction.

`capabilities.go` is also a second, divergent copy of the decision logic. It
short-circuits for admins and it **never calls `enforceUATConstraints`**
(`capabilities.go:382-396`). It therefore over-reports what a user access token
can do. Today that is advisory data. If permissions become a user-facing
feature, it becomes a correctness surface.

---

## 5. The Human and Agent Question

**The distinction is correct. The relationship is missing.**

Two principal kinds is the right model. A human authenticates through OAuth and
holds a durable identity. An agent authenticates with a short bearer token and
is disposable. They need different lifecycles.

The defect is that the system models them as two *independent* sources of
authority. It should model the agent as an actor that works **on behalf of** a
principal chain.

Five specific consequences follow.

### 5.1 D4 — No delegation ceiling

`ResolveEffectiveRole` is at `pkg/hub/agentrole.go:119-122`:

```go
func ResolveEffectiveRole(requested AgentRole, userHubRole string, projectMax AgentRole) AgentRole {
	userCeiling := AgentRoleFull
	return minRole(requested, userCeiling, projectMax)
}
```

The function accepts `userHubRole` and never reads it. `userCeiling` is a
constant. The comment states this is intentional and current.

The HTTP create path does not call this function. It repeats the same logic
inline, with its own `userCeiling := AgentRoleFull`
(`handlers_agents_core.go:626-647`).

`checkAccessForAgent` (`authz.go:237-384`) contains no reference to the
creating user. It never loads that user. It never intersects the two
authorities.

**The hook exists and is disconnected in two places.** This is the single most
important structural point for the human and agent question. The design
anticipated a ceiling. Nothing implements it.

### 5.2 The two decision paths have diverged

| Step | User path (`authz.go:118`) | Agent path (`authz.go:237`) |
|---|---|---|
| User token constraints | yes | not applicable |
| Admin bypass | yes | no |
| Owner bypass | yes | no |
| Ancestry access | yes | yes |
| Project owner or admin bypass | yes | no |
| Hub member assign baseline | yes | no |
| Policy evaluation | yes | yes |
| Project read baseline | no | yes |
| Project assign baseline | no | yes |
| Delegation fallback | no | yes |

Ten steps. Four are shared. Every new permission feature must be built twice.
The two lists will drift again, because they have already drifted.

### 5.3 Ancestry grants downward and constrains nothing upward

`canAccessAsAncestor` (`authz.go:615-622`) tests whether the caller's ID is in
the resource's ancestry. It gives an ancestor access to a descendant's
resources.

There is no opposite rule. A descendant does not inherit an ancestor's limits.
Ancestry is a grant. It is never a constraint.

Two further problems with ancestry:

- `Ancestry[0]` is not guaranteed to be a human. If the parent agent's
  `GetAgent` lookup fails, the guard at `handlers_agents_core.go:427` skips
  silently and the child gets an empty ancestry. The scheduler path sets no
  ancestry at all.
- `OriginUserID()` returns `Ancestry[0]` with no type check
  (`identity.go:140-145`). It is then used as a user scope ID for secrets
  (`handlers_env_secrets.go:1120`) and as the `root_user` OIDC claim
  (`handlers_oidc.go:162`).
- Over federation, `extractHubClaims` (`federation_auth.go:301-310`) copies
  `Ancestry` and `RootUser` from the remote token without checks. A trusted
  remote hub can assert the ID of a local user.

### 5.4 D5 — An agent token cannot be revoked

Agent authentication does no database lookup (`auth.go:148-201`). It verifies
the signature, the issuer, the audience, and the expiry. It does not check that
the agent still exists. It does not check a denylist. There is no `jti`.

Tokens live 10 hours (`agenttoken.go:37`). `ScopeAgentTokenRefresh` lets an
agent mint a new 10-hour token with no count limit and no absolute lifetime
anchor (`handlers_agents_core.go:2581-2643`).

Therefore:

- A leaked agent token is valid until it expires. If it holds the refresh
  scope, the holder can renew it forever.
- If the creating human is removed from the project, demoted, or deleted, the
  agent keeps its authority. `created_by` has no foreign key
  (`pkg/ent/schema/agent.go:57-60`).
- Deleting or stopping an agent does not invalidate its token.

**For a permissions product, this makes the word "revoke" untrue.** Any user
interface that offers "remove access" will state something the system does not
do.

### 5.5 An empty agent role means full authority

`ScopesForRole("")` returns the full scope set (`agentrole.go:66-67`).
`roleOrdinal("")` returns 3, the maximum (`:85-86`). `agentRoleAndScopes` maps
an empty role to `AgentRoleFull` (`httpdispatcher.go:215`). Unknown roles
correctly return 0. Empty does not.

This interacts badly with an unguarded path. The scheduled-event handler and
the recurring-schedule handler gate an agent caller with `checkAgentReadScope`
only, that is `project:read`
(`handlers_scheduled_events.go:70`, `handlers_schedules.go:74`). Both accept
`eventType: "dispatch_agent"`. Neither checks `ScopeAgentCreate`.

The scheduler then creates the agent with an empty `AppliedConfig` and no
ancestry (`server.go:2870-2884`). Verified in the source.

**Result: a `readonly` agent can schedule the creation of a full-authority
agent.** The new agent has no ancestry, so no ancestry control applies to it,
and its `OriginUserID()` is empty. This passes around both the parent-role
ceiling and `projectMax`.

This is a concrete privilege escalation, not a theoretical one. It should be
fixed before the design work, not with it.

---

## 6. Recommended Foundation, in Order

Each item states whether it is load-bearing or reversible.

### Phase 0 — Correctness. Do these before any feature work.

**F0.1 Fix the escalation in section 5.5.** Add a scope check to the
scheduled-event and schedule handlers. Set an explicit agent role on the
scheduler-created agent. Set its ancestry. This is a bug fix, not a design
change. *Reversible.*

**F0.2 Make the decision function deterministic and give deny priority.**
Write the precedence rule down first, as a specification. A workable rule:

```
explicit deny  >  explicit allow  >  code baseline  >  default deny
```

Within an effect, a more specific scope wins. Add a deterministic third sort
key to the query. Cover the rule with a table-driven test, including the
allow-versus-deny tie at equal scope and equal priority.

Decide separately whether a hub-scoped deny must defeat a project-scoped allow.
A revocation feature needs it to. The present tests assert the opposite. This
is a product decision, and section 8 raises it as a question.

*Load-bearing.* Stored policy data changes meaning.

**F0.3 Make enforcement declarative and default-deny.** Add the resource and
the action to the route registration. Add a test that walks the router and
fails when a route declares neither an authorization pair nor an explicit
exemption. Then convert the routes. The audit's gap list becomes the conversion
work list.

*Reversible*, but the cost grows with each new route.

### Phase 1 — Foundations for the features.

**F1.1 One permission vocabulary.** Build a single registry of valid
(resource type, action) pairs. Generate the agent scope list, the user token
scope list, the CLI help text, the user interface list, and the documentation
from it. Delete the dead names. Correct the five mismatches in section 4.3.

*Load-bearing.* Do this before you issue tokens with new scope names.

**F1.2 Make admin a grant.** Replace the bypass at `authz.go:127` with a
hub-scoped grant that the engine evaluates. Replace the 37 hand-written checks
with one guard. Reject a scoped user identity in every one, as
`requireHubAdmin` does today. Only then can "admin over resource subset X"
exist.

*Load-bearing* for the data. The migration is mechanical.

**F1.3 Connect the delegation ceiling.** Decide the rule, then apply it. The
recommended rule: an agent's effective authority is the intersection of its own
grant and the authority of the principal at the root of its ancestry. Apply the
rule at request time, not only at token mint time. Remove the duplicate inline
copy at `handlers_agents_core.go:626`.

*Load-bearing.* It changes what existing agents can do. It needs a measurement
period before it is turned on.

**F1.4 Make agent tokens revocable.** Add a `jti` and a version counter on the
agent record. Check the counter at authentication. Increment it when the agent
stops, when the agent is deleted, when its role changes, and when its creator
loses project access. Bound the refresh chain with an absolute lifetime.

*Load-bearing* for the authentication path. It adds one lookup per request, so
it needs the cache from F1.5.

**F1.5 Add a decision cache and remove the two silent cliffs.** Cache the
effective groups and the policy set for the life of a request. Remove the
`Limit: 200` in `checkDelegation` and the `Limit: 10` in
`isProjectOwnerOrAdmin`, or make them raise an error instead of truncating.
Make `capabilities.go` call the same function as the enforcement path.

*Reversible.*

### Phase 2 — The features.

**F2.1 Granular user access tokens.** Build on F1.1. Add hub scope and
multi-project scope to the token model. Today a token holds exactly one project
UUID (`models.go:1443`), and the project confinement in `enforceUATConstraints`
does not apply to any resource that is not a project and has no project parent.

**F2.2 Scoped admin.** Build on F1.2.

**F2.3 Limits by group membership level.** Build a separate subsystem. Do not
put it in the policy engine. Section 7 explains why. It needs:
- A scoped settings table, or a `scope_type` and `scope_id` on `hub_settings`.
  The unique index on `section` must change.
- Counters and the indexes to support them, at minimum
  `agents(owner_id, phase)`.
- A persisted audit or usage table. `slog` cannot answer a quota question.

*The storage shape is load-bearing.* The enforcement points are reversible.

**F2.4 Narrow `hub-member-read-all` and make project visibility a real gate.**
Do this last. It is only safe after F0.3, because many read paths have no check
of their own and this policy currently hides that fact. Note that D1 makes an
exception impossible today: you cannot write a deny that holds, so the only way
to reduce this grant is to edit the grant.

---

## 7. Alternatives Considered

**A. Fix the listed gaps and build the features on the present base.**
Rejected. The gap list is a snapshot of one audit. Enforcement is opt-in, so
the list regenerates with each new handler. More importantly, D1 blocks the
features directly: all three features are statements about restriction, and the
engine cannot hold a restriction. This alternative delivers the features in a
form that does not work.

**B. Replace the engine with an external one (OPA, Cedar, or SpiceDB).**
Considered seriously. Cedar and OPA both give deny-overrides and a
deterministic result, which is F0.2 for free. Rejected **as the first move**,
for two reasons. First, the vocabulary and the enforcement points are the
defect, not the evaluator. A swap with the present call sites reproduces every
hole from section 4.2 with more moving parts. Second, the migration of stored
policy data needs a settled precedence rule, which is F0.2 anyway.

**This alternative should be reconsidered after F0.3 and F1.1.** At that point
the evaluator sits behind one interface and one vocabulary, and the swap is
contained. Record this now so the interface in F0.3 does not assume the
in-process engine.

**C. Put limits in `PolicyConditions` as numeric conditions.**
Rejected. An allow-or-deny decision is a pure function of stored state. A quota
needs a counter and a reservation that is safe against a race. Mixing them
makes the authorization path stateful, slow, and hard to cache. It also
prevents alternative B, because no external engine models a quota this way.
Keep the two decisions separate. A quota check can call the authorization
result; it must not live inside it.

**D. Keep the two decision paths separate, and add the missing steps to the
agent path.**
Rejected. It makes the ten-row table in section 5.2 permanent. Every future
feature costs two implementations, and the drift that already happened will
happen again. The correct model is one decision function that takes a principal
chain.

**E. Do nothing structural. Ship the features and accept the risk.**
Recorded, not recommended. State the cost plainly: users will be shown
permission controls that do not control anything. A token will name a scope it
does not have. A revoked agent will keep working. An administrator will write a
deny that does nothing. These are trust defects, and they are more expensive to
correct after users depend on them.

---

## 8. Open Questions

These need a decision from you. I raise them one at a time, in this order.

1. **Must a hub-scoped deny defeat a project-scoped allow?** A revocation
   feature needs yes. `TestAuthz_ScopeOverride` asserts no. This decision sets
   the precedence rule in F0.2, and it cannot be changed later without
   re-interpreting stored policies.

2. **What is the delegation rule for F1.3?** Options: (a) full intersection
   with the root human's authority, evaluated per request; (b) a ceiling
   applied at mint time only; (c) intersection, with a named exception for
   standing agents such as coordinators. Option (a) is correct and will break
   deployments. Option (b) is cheap and leaves section 5.4 unsolved.

3. **Is an agent a principal, or a credential of a human?** This is the
   question under question 2. The present system says principal. The features
   you describe assume credential. Choose one and write it in the glossary.

4. **How large is the deployed policy set?** The migration cost of F0.2 depends
   on it. If most hubs run only the seeded policies, the precedence change is
   nearly free.

5. **Is the scheduler's full-authority agent creation (section 5.5) known and
   accepted, or is it new?** It changes the urgency of F0.1.

---

## 9. Acceptance Criteria

A reviewer must verify these before the foundation work is considered done.

**For F0.2 (deny and determinism):**
- A table-driven test covers allow versus deny at equal scope and equal
  priority. It gives the same result over 100 runs.
- A test covers a hub-scoped deny against a project-scoped allow, and asserts
  the rule chosen in open question 1.
- The database query has a deterministic third sort key.
- The precedence rule is written in a document, not only in code.
- `Priority` is either read by the engine or removed. Its documentation matches
  its behaviour.

**For F0.3 (declarative enforcement):**
- A test enumerates every registered route. It fails when a route declares
  neither an authorization pair nor an explicit, named exemption.
- The test fails when a new unguarded route is added. Verify this by adding
  one.
- Every gap listed in the input document, sections 6 and 7, is closed or
  carries a named exemption.
- No handler contains the `if userIdent != nil` pattern.

**For F1.1 (one vocabulary):**
- One registry is the source of the agent scopes, the user token scopes, the
  CLI help, and the web list. A test fails when any two disagree.
- A user token with `agent:start` can start an agent, or the scope no longer
  exists.
- `agent:dispatch`, `ScopeAgentLogAppend`, and `ScopeProjectSecretRead` are
  either enforced or removed.

**For F1.2 (admin as a grant):**
- A scoped user identity is rejected at every admin gate. Verify with an
  admin-minted, project-scoped, read-only token against
  `POST /api/v1/policies`.
- A grant exists that gives admin authority over a named subset and no more.

**For F1.3 and F1.4 (agent authority):**
- An agent cannot perform an action that its root human cannot perform. Test
  with a non-admin creator and a broad policy on the project agents group.
- An agent token stops working when the agent is deleted. Verify within one
  request, not after 10 hours.
- An agent token stops working when its creator is removed from the project.
- A refresh chain cannot exceed the absolute lifetime.

**For section 5.5 (the escalation):**
- A `readonly` agent cannot create a schedule that dispatches an agent.
- An agent created by the scheduler has an explicit role and a non-empty
  ancestry.

**Across all phases:**
- `capabilities.go` and the enforcement path give the same answer for the same
  input. Test with a scoped user token.
- No authorization path truncates a result set silently.

---

## 10. Sources

All line references are against `origin/main` at commit `89ed0fe`.

Primary files read in full: `pkg/hub/authz.go`, `pkg/hub/authorize.go`,
`pkg/hub/identity.go`, `pkg/hub/agentrole.go`.

Four supporting investigations produced the citations in sections 4.3, 4.5,
4.6, and 5. Claims marked "verified" in section 4.1 were confirmed by execution
against the SQLite test store.

Two claims in this document could not be confirmed and are marked as such:
- The PostgreSQL row-order behaviour in section 4.1(b) is a reasoned
  expectation. The nondeterminism was measured on SQLite.
- The full set of handlers that a federated identity can reach was not
  enumerated. Two examples were found.

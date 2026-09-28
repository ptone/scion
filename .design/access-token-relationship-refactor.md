# Access tokens, relationship grants, and delegated hub administration

Status: proposal for discussion; no runtime changes implemented.
Investigated 2026-09-28 against local `main` at
`7e3bbbd3f62673d16515c76b23ca0d7463fd7651`.

## Purpose

Make role bindings and creator/ancestry relationships parts of one authorization
model, then use that model consistently for User Access Tokens (UATs), agent
delegation, and secret delivery. UAT is the repository's canonical name for what
the brief and GitHub issues call a PAT; the existing `scion_pat_` wire prefix need
not change.

The immediate outcomes are:

1. A project member can mint an explicitly selected `agent:attach` token and use
   it on their own agents and descendants, subject to current authorization.
2. A hub administrator can mint a bounded hub UAT for supported administrative
   operations, including project creation and user invitations.
3. Supervised agents can receive explicit hub authority with verifiable actor
   attribution, revocation, and delegation limits.
4. Owner, ancestor, and progeny grants have declared semantics, common decision
   provenance, and tests across every entry point that relies on them.

Sources: [hub-token tracking issue #2030](https://github.com/ptone/scion/issues/2030),
[creator attach issue #2092](https://github.com/ptone/scion/issues/2092), and
[terminal reconnect issue #1811](https://github.com/ptone/scion/issues/1811).
#2030 is a design tracking issue for hub tokens, rather than the original
implementation of ancestry authorization. #1811's subsequent direction says
authorization is checked on reconnect, without periodic mid-session checks.

## Findings in the current implementation

These are source observations at the revision above, not a claim that every
listed path has been exercised against a running Hub.

| Area | Evidence | Consequence |
| --- | --- | --- |
| Token issuance | `pkg/hub/useraccesstoken.go`: `CreateToken`, `scopeToPermissionIDs`; `authz.go`: `getProjectScopedPermissions` | Requires a project and checks only current project role-binding permissions, including group expansion and constraints. Hub grants and resource relationships cannot justify minting. |
| Token use | `authz.go`: `Decide`, `enforceUATConstraints`, `uatScopeRestriction` | Applies project and scope restrictions, then role/relationship authorization. A token scope is a ceiling, not an independent grant. |
| Relationships | `authz.go`: `checkRelationshipGrants`; `authz_relationship.go` | Owner, ancestor, hub-member service-account assignment, creator user-skill read, and opt-in progeny reads already exist as named grants. They are a post-kernel fallback; do not introduce a second relationship engine. |
| Creator attach | `authz_cross_member_attach_test.go`: `TestCrossMemberAttach_Matrix`, `TestCrossMemberAttach_UATScopes` | User relationship access is tested; the mint test explicitly expects an owner requesting `agent:attach` to fail. #2092 requires changing that contract, not adding attach to all project roles. |
| Agent ceiling | `authz_delegation_ceiling.go`: `checkDelegationCeiling`, `checkUserHoldsPermission` | Edges are evaluated in project scope. User ceiling checks resolve permission sets rather than resource-relative relationships. A creator's authority on a particular descendant cannot be represented by that set alone. |
| Agent lifecycle | `authorize.go`: `authorizeAgentLifecycle`; `pty_handlers.go`: `handleAgentPTY` | The user branch calls the auth service. The agent branch returns success after JWT lifecycle-scope and same-project checks. This helper does not apply the centralized relationship/ceiling decision to agents. Trace the complete route before treating kernel tests as coverage. |
| PAT attribution | `auth.go`: UAT branch; `useraccesstoken.go`: `ValidateToken`; `handlers_agents_core.go`: creation attribution | A PAT becomes a `ScopedUserIdentity`. An agent using it is seen as the user; children are attributed to that user. The agent-only ceiling in `Decide` does not apply. |
| Delegation persistence | `handlers_agents_core.go`: `recordDelegationEdgeWithType` | Creation edges store project, role, and principals, but not the issuing UAT ceiling or credential ID. Edge writes are best-effort. Durable children need an explicit rule for retaining the authority limits of their creation request. |
| Secret delivery | `httpdispatcher.go`: `resolveSecrets`, `resolveAsNeededForKeys`, `resolveEnvFromStorage`; `secret/localbackend.go`, `secret/gcpbackend.go` | Progeny secret candidates use a decision callback with explicit `project.secret_read`. Progeny env vars are merged directly in `resolveEnvFromStorage`. Direct owner/project/broker resolution is another path. Authorization is not uniform across delivery sources. |
| Permission naming | `permissions/registry.go`; `scopeToPermissionIDs`; `enforceUATConstraints` | Scope conversion reconstructs `resource:action`, despite canonical permission IDs. Several hub permissions share a resource/action pair, e.g. settings and config read. A generic `hub:read` mapping would collapse distinct privileges. |
| User administration | `handlers_users_core.go`: `createUser`, `requireSessionCredential` | Direct user creation is forbidden for everyone. PATCH/DELETE admit interactive/dev credentials only. Adding hub scopes alone cannot enable these operations. |
| Operation metadata | `authzop/catalog.go`: `agent.attach`; `pty_handlers.go` | Catalog describes `/agents/{id}/attach`, while the Hub PTY handler uses `/agents/{id}/pty`. The audit must compare actual entry points and enforcement, not just catalog declarations. |

The older `.design/user-access-tokens.md` and
`.design/agent-progeny-secret-access.md` describe historical behavior. In
particular, the latter's materialized policies have been replaced by named
relationship grants. Use current code and the operation contract as the baseline.

## Authorization model

### Separate facts, grants, and restrictions

Keep three distinct concepts:

- **Facts:** immutable creator and ancestry records, current resource ownership,
  resource scope, principal status, and the credential actually presented.
- **Grants:** active role bindings and named relationship rules. An explicit
  agent delegation may add a separately identified, bounded grant.
- **Restrictions:** credential boundary and permission ceiling, access
  constraints, delegation ceilings, operation admission, and domain rules such
  as message mode and authority-granting checks.

For ordinary user UATs, the intended rule is:

```text
allow = credential valid AND operation admits credential/principal
        AND target inside token boundary AND requested permission in token
        AND (current role grant OR current relationship grant)
        AND all applicable restrictions AND operation-specific invariants
```

No relationship is a bypass around restrictions. Ancestry proves a relationship;
it does not by itself delegate all of a user's authority to every descendant.

### Declare relationship rules centrally

Extend the existing resolver and kernel input with typed relationship grants.
Each rule declares its ID, admitted principal kinds, target resource kinds,
explicit permission allowlist, authoritative facts, provenance, and whether it
can justify resource-relative token eligibility. Resolve candidates once and
feed both role and relationship grants into the same restriction stage.

The initial rule catalog should cover:

| Rule | Direction and predicate | Required boundary |
| --- | --- | --- |
| Resource owner | User controls a resource they currently own | Explicit resource/action list; ownership is distinct from historical creation |
| Agent ancestor | User or local agent appears in target agent's Hub-recorded ancestry | Explicit agent actions; agent JWT/delegation restrictions still apply |
| Progeny resource read | Local agent descends from resource creator, user-scoped resource opts into `AllowProgeny` | Read/use only; no write, ownership transfer, or authority-granting rights |
| Creator user-skill read | Existing creator-skill predicate and active originating user | Preserve its separate semantics; do not silently require `AllowProgeny` |
| Hub-member service-account assign | Existing current-membership rule | Preserve its explicit service-account assignment exception |

Today the generic owner/ancestor fallback is not an explicit action allowlist.
First characterize the existing supported actions, then freeze that list.
Registering a new permission must not automatically give it to every owner or
ancestor. Scope-wide operations such as creating projects cannot be authorized
by a relationship with one existing agent.

Use Hub-attested ancestry, loaded from trusted records or verified local claims.
Do not trust caller-provided ancestry or accept federated claims as local proof.
Specify behavior for ownership transfer, suspended/deleted creators, missing
parents, project deletion, and any future resource move. For new sensitive
delegations, missing authority data denies access. Historical facts can remain
for audit without remaining grants.

Explain output should identify the matched rule, relationship subject and
target, credential ID/boundary, applicable constraints, and delegation chain.
Show rejected relationship candidates too. Capabilities and authorized lists
must use the same evaluator and avoid exposing inaccessible resource metadata.

## Token model and minting

### Explicit token boundary

Extend the existing UAT model rather than introducing an unrelated API-key
system. Proposed fields:

```text
boundary: { kind: "project", projectId: P } | { kind: "hub" }
permissionIds: canonical registry IDs, expanded and frozen at issuance
authorizationVersion: interpretation version
issuerUserId, tokenId, name, hash, expiry, revoked, lastUsed: retained
```

The Hub is implicit in its local token store; any exchanged credential also
binds its audience to that Hub. Empty `projectId` must never implicitly mean hub
authority. Backfill all existing rows as project tokens and reject malformed
boundary combinations. Preserve one-time plaintext display, hash-only storage,
atomic token/audit writes, per-user caps, and current expiry limits.

Recommended initial hub boundary: hub control-plane operations only. It does
not automatically authorize all project resources. Project creation is a hub
operation even though the created resource is a project; follow-up actions on
that project require a project credential or a later explicit multi-project
boundary feature. Record newly created project ownership normally, but keep the
calling credential ceiling in force. Avoid an undocumented all-projects wildcard.

### Resource-relative minting for #2092

Mint-time eligibility and use-time authorization answer different questions:

- Minting: may this authenticated user select this restriction for this boundary?
- Use: does this user currently have permission on this particular target?

For project tokens, retain active project-authority admission and the existing
project role ceiling for scope-wide permissions. Additionally admit a
registry-declared resource-relative permission when its relationship rule is
applicable to that principal kind and boundary. Start with reviewed agent
owner/ancestor permissions, including attach and port access.

Do not enumerate owned agents at mint time or require that one already exists.
A member can mint a token before creating their first agent. The token gains no
grant: every later request must independently pass current resource authorization.
Use the same descriptor mechanism for supported resource-specific role bindings;
do not mistake a grant on one target for a project-wide grant.

Example: Alice mints `agent:attach` in project P, then creates A. The token can
attach to A and Alice's authorized descendants. It cannot attach to Bob's B,
an agent outside P, or a constrained target. A token without attach still fails
on A. Project owners/admins retain the existing restriction against observing
other members' agents and their injected secrets.

This deliberately revises the existing mint-time invariant from a flat
role-permission subset to eligibility for a restriction. Keep the stronger flat
subset rule for operations that confer scope-wide or durable authority.
Expose eligibility separately from target-specific capabilities in CLI/UI.

### Canonical scope mapping and legacy semantics

Give every published token selector an explicit mapping to canonical permission
IDs and permitted boundary kinds. Reject unknown, ambiguous, or unmapped
selectors. Never infer hub permission IDs by replacing `:` with `.` or by
matching only `resource:action`. Surface precise selectors such as
`hub.settings:read` only through registry metadata.

Expand convenience aliases at issuance and persist the expansion. New registry
entries must not expand an old token. Keep attach and port access explicitly
selected, outside `agent:manage`, unless that UX policy is deliberately changed.

`LegacyUATScopeImplications` currently makes `agent:attach` imply lifecycle at
use time. Version that interpretation: normalize old tokens to their existing
effective permission set, while newly minted attach-only tokens mean attach
only. Do not let a compatibility implication silently enlarge a new token's
ceiling. Validate the entire normalized expansion when minting.

### Hub token issuance

Require an interactive/dev user credential plus a current eligible system-scoped
hub-admin or super-admin role binding. Resolve group membership, binding windows,
and access constraints through the same authority resolver. Requested hub
permissions must be a subset of the issuer's current hub authority; hub-admin
does not acquire super-admin-only permissions by minting.

At use time re-evaluate current issuer authority and status. Revocation, expiry,
role removal, and tightened constraints take effect on subsequent requests.
Token management remains session-only initially: a UAT cannot mint, widen, or
manage other UATs. New credential kinds must be admitted operation by operation.

## Delegating hub operations to agents

An ordinary bearer UAT is a human credential. Supplying it to an agent gives the
holder its human authority; the Hub cannot reliably infer which agent used it.
An unverified actor header or ancestry claim does not fix this.

Recommended design, pending ptone's direction: support an explicit agent-bound
delegation mode in addition to ordinary user UATs. An interactive administrator
creates a Hub record binding a chosen agent, the issuer, boundary, exact
permissions, expiry, and subdelegation policy. The agent authenticates using its
own credential to exchange that grant for a short-lived delegated credential.
The general human bearer token need not enter the agent's environment. The
delegated credential remains a bearer once issued; binding preserves verified
attribution and revocation, not proof of possession against credential theft.

Record and enforce:

- Actor agent ID and active agent credential, authorizing user ID, source grant
  or UAT ID, delegation edge ID, Hub audience, and explicit boundary.
- An operation-specific agent policy ceiling, the delegation's frozen ceiling,
  every parent grant's current ceiling, current issuer authority, and all access
  constraints. All must allow the operation.
- Explicit system-scoped delegation edges. The agent's ordinary project JWT
  gains no hub authority. Simply intersecting hub operations with today's
  project-only agent scopes would deny every request; a separately configured
  hub delegation ceiling is required.
- No subdelegation by default. Any future child grant must be explicitly
  authorized, narrower in actions/resources, no longer lived, depth bounded,
  cycle checked, and linked to its parent's revocation state.
- Parent expiry/revocation, issuer suspension, and agent deletion/suspension
  invalidate descendant credentials. Refresh cannot detach the credential from
  those parents. No sensitive fail-open path on store errors or missing edges.

Relationships are evaluated as the actual agent, not by impersonating the
human's owner/ancestor identity. Evaluate the user's authority separately as
the ceiling for the explicit delegated action. Delegation does not rewrite
creation ancestry; resources record the actual actor plus the authorizing user.

Make authority-granting effects explicit: creating an agent, adding a group
member, granting a role, inviting a privileged user, and creating a project's
owner binding can outlive the request. Use the existing `CanDelegate` and
operation security-effect machinery to check the resulting authority. Persist
delegation edges, credential provenance, mutation, and audit atomically where
they form one durable operation. A narrow token must not create a child with an
unrestricted user identity or strip its ceiling through another credential type.

Fix the related project-token child-creation provenance gap independently of
hub delegation. Reuse resource-aware authority proofs when checking a parent's
ceiling; a parent's legitimate relationship grant must count on that target
without turning into a broad project permission. Avoid recursive evaluation
cycles by separating grant resolution from bounded delegation-chain traversal.

## Secret material and ancestry

Use one Hub authorization service for selecting material for a target agent.
Backends retrieve/decrypt already-authorized records; brokers receive the
resolved result and do not implement a second creator-policy system.

Distinguish the requesting actor, authorizing user, target agent, and material
owner/creator. Authorizing agent creation or restart is not sufficient by itself
to authorize arbitrary material delivery. Each selected secret needs a declared
delivery grant, whether from owner scope, project/broker assignment, or opt-in
progeny access. A user PAT must not become an agent progeny identity.

Audit all paths together: initial dispatch, start/restart, env gather and
`as_needed`, runtime secret reads, env vars marked secret, file secrets, skill
injections, and cloud identity assignment/minting. Preserve source precedence
and injection mode after authorization; filter before accessing secret values.
Use typed resource metadata and a canonical permission for secret value use;
remove the current synthetic resource/permission mismatch through a deliberate
registry migration, not another fallback string.

Specify source lifetime: disabling `AllowProgeny`, suspending the source user,
or revoking the relevant delegation blocks future resolution. It cannot remove
plaintext already injected into a running process. Rotation/restart or a future
brokered secret-access model is needed for that stronger guarantee.

User-scoped secrets have no intrinsic project ID. Do not claim that ancestry
alone confines them to one project. Proposed default preserves opt-in access to
the creator's descendants in their authorized execution projects; project-only
sharing requires an explicit project constraint. Confirm this policy rather
than deriving it from missing resource-parent metadata.

## Capability audit and intended initial disposition

| Capability | Current limitation | Proposed disposition |
| --- | --- | --- |
| Attach to own agents/progeny | Mint rejects relationship-only permission | First deliverable; explicit attach scope and full request-time check |
| Forwarded ports | Same role/relationship split as attach | Include in the same eligibility model and tests |
| Lifecycle, delete, message, resource CRUD | Flat mint ceiling can omit relationship grants; some paths have additional domain gates | Inventory every registered operation; enable only declared resource-relative cases; preserve message-mode checks |
| Hub project creation | `project.create` has no UAT selector; project token cannot target hub | Explicit hub permission, creation effect/ownership checks, SDK/CLI/UI support |
| User invitations/list/read | Selectors exist, but token boundary denies hub use | Candidate initial hub allowlist; enforce all invitation side effects |
| Direct user creation | API explicitly refuses it | Separate product decision: invite/sign-in provisioning by default, or a designed administrative provisioning operation |
| User update/suspend/delete/promote | Session-only admission and distinct governance permissions | Retain exclusion initially unless explicitly approved; do not globally relax `requireSessionCredential` |
| Hub settings, integrations, scheduler, quotas, roles/groups | Missing selectors, boundary restrictions, and/or operation-specific admission | Publish a reviewed allowlist; authority-changing operations require `CanDelegate` and governance checks |
| Token management, recovery, maintenance | Deliberate credential or privileged-operation restrictions | Keep session-only initially; list exclusions in UX |
| Secrets and cloud identity | Mix of resource reads, delivery, and credential minting | Separate value-use and credential effects from generic metadata reads |
| Lists, capabilities, SSE, workspace/chat/downloads | Different adapters and secondary checks | Audit both filtering and credential admission; bind cursors/caches to credential and boundary; never trust client scope filters |
| PTY reconnect and proxying | Long-lived connection and downstream broker transport | Reauthorize at handshake/reconnect, preserve #1811's no periodic re-auth decision; keep end-user credentials at Hub boundary |

Audit each operation as a row containing actual route/service entry points,
resource resolver, principal kinds, credential kinds, canonical permission,
token selector/boundaries, relationship eligibility, delegation/security
effects, audit fields, and positive/negative tests. Generate the public scope
catalog from these declarations and add drift checks against registered routes.

## Delivery plan

These are proposed child work items, not newly filed issues or dispatched work.

| Phase | Deliverable | Acceptance and dependencies |
| --- | --- | --- |
| 1. Contract and regression baseline | Relationship rule descriptors, source-based operation inventory, explicit scope mapping, characterization tests | Current owner/ancestor privacy matrix retained; every new selector has one reviewed interpretation. Resolve policy questions below. |
| 2. Project UAT relationship eligibility | Fix #2092 and port access; version legacy attach interpretation; precise CLI/UI descriptions | Mint before resource creation; own/progeny allowed; unrelated/cross-project denied; missing scope and constraints still deny. Can ship before hub delegation. |
| 3. Common evaluation and provenance | Integrate relationship candidates into kernel; close lifecycle adapter differences; resource-aware parent ceilings; atomic creation provenance; unify material selection | Actual HTTP/PTY/service tests agree with kernel; no secret-value or sensitive delegation fail-open; parity for dispatch/restart/as-needed. Depends on phase 1; sequence affected domains separately. |
| 4. Hub UAT vertical slice | Explicit boundary/schema, permission mappings, mint/validate path, project creation and invitation allowlist, client/UI support | Current hub-admin can mint only held scopes; downgraded issuer denied; project tokens unchanged; no implicit project wildcard. Depends on phases 1–2 and relevant phase 3 checks. |
| 5. Agent-bound hub delegation | Grant issuance/exchange, system-scoped ceiling, verified dual attribution, expiry/revocation chain, optional future child delegation | Supervised agent performs approved hub operations; plain project JWT denied; parent revocation and agent suspension deny; no credential-based ceiling escape. Depends on phases 3–4 and the product decision. |
| 6. Remaining operation coverage | Review remaining admin operations and intentional exclusions; update docs/glossary and generated contracts | Route/catalog/SDK/UI consistency, no unclassified omission, current-vs-historical design docs clearly marked. |

Each implementation phase needs independent review and the applicable local CI
checks before delivery. New CLI commands, if needed, also need the repository's
CLI mode-availability decision; extending existing token commands may suffice.

### Test matrix

Cross principal (member/admin/owner, agent, federated agent), credential
(session/project UAT/hub UAT/delegated agent), target relationship
(own/descendant/unrelated), and boundary (same project/other project/hub).

Test at least:

- Mint and actual attach/port operation, not a manually constructed scoped
  identity alone; HTTP preflight, WebSocket handshake, reconnect, and CLI auth.
- Revocation/expiry, user suspension, role/constraint changes, group membership
  changes, and ownership/ancestry facts; preserve session-only token management.
- Zero/unknown/ambiguous permissions, missing resource scope, malformed token
  boundary, legacy tokens, alias expansion, and no authority gain after a new
  permission is registered.
- Parent/child ceiling attenuation, missing/revoked delegation edges, failed
  authority reads, multi-hop depth/cycles, and atomic rollback on audit failure.
- Token-created agents cannot widen their authority through fresh JWTs,
  schedules, restarts, role bindings, groups, or a different issuance endpoint.
- Secret source/target/creator permutations, false `AllowProgeny`, forged or
  federated ancestry, source suspension, scope precedence and injection modes,
  local/GCP backend parity, and no value retrieval before authorization.
- User provisioning/invitation governance, last-admin protection, and project
  creation's owner-binding effects; excluded operations remain excluded.
- Decisions, capabilities, lists, audit records, and mutation records agree on
  actor, authorizing user, credential, rule, boundary, and denial reason.

## Decisions for ptone

1. **Supervised delegation:** recommend an agent-bound grant/credential mode.
   Ordinary hub bearer UATs can ship first, but cannot promise agent ceiling
   enforcement or verified agent attribution.
2. **Hub boundary:** recommend control-plane operations only initially, with
   explicit later project selection if cross-project management is needed.
3. **User provisioning:** recommend invitations plus existing sign-in
   provisioning. Literal API creation needs a separate operation and contract.
4. **Project eligibility:** recommend active project-authority admission plus
   declared relationship-relative scope eligibility, including future resources.
   Supporting former members solely through retained ancestry is a separate
   policy choice; current resource access and token mint admission differ.
5. **Secret sharing boundary:** recommend preserving user opt-in ancestry
   sharing across authorized execution projects, with explicit constraints for
   project-limited sharing. Confirm creator suspension blocks future delivery.

The plan does not change stock project roles to grant cross-member observation,
change terminal reconnect behavior, or implement any of these runtime changes.

## Investigation validation

The existing SQLite-backed Hub tests selected below passed at the investigated
revision. They characterize current behavior; they do not validate the proposed
implementation:

```sh
env -u SCION_PROJECT go test -buildvcs=false ./pkg/hub \
  -run 'Test(CrossMemberAttach_UATScopes|RelationshipGrant_|Golden_AgentRelationshipGrantDelegationCeiling|Golden_AgentProgeny|M1_UATDeniedForHubLevelResources|M2_HubPermissionsNoUATScope)' \
  -count=1
```

The repository `make ci` check was also attempted with `SCION_PROJECT` unset and
`GOFLAGS=-buildvcs=false`. Formatting and custom guard checks passed, but the
test stage reported failures in unchanged code, including configuration decoding
of inherited `auto_expose_ports` and Codex native telemetry's `CODEX_HOME`
conflict. The targeted `TestLoadVersionedSettings_DefaultsOnly` and
`TestNativeTelemetryProvisionedChildEnv` passed when `SCION_AUTO_EXPOSE_PORTS`
and `CODEX_HOME` were also removed from the test process environment. This does
not establish that all broad-suite failures share those causes. No application
code or fixtures were changed to make these checks pass.

The standalone `go build -buildvcs=false ./...` completed successfully. The
design-only diff also passed `git diff --check`.

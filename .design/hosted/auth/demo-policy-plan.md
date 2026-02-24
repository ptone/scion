# Demo Policy Implementation Plan

## Status
**Complete** — 2026-02-24

All steps (1-9) completed.

## 1. Goal

Demonstrate concrete policy enforcement in the Hub using the permissions design established in `permissions-design.md` and `groups-design.md`. The demo scenario implements a minimal but realistic access control model:

1. Any logged-in user can create groves
2. The creating user automatically becomes a member of the grove (exclusive membership list; no UI or API to modify membership in this phase)
3. Users can create agents only in groves they are members of
4. Agents can be attached to or deleted only by their creator
5. All authenticated Hub users can see all groves and all agents in the web UI

This stage must use the existing policy engine (not ad-hoc handler checks) so it demonstrates real policy capabilities without creating a dead end for future sophistication.

## 2. Design Fit Assessment

### 2.1 Compatibility with Permissions Design

The demo scenario maps directly onto the existing permissions design with no modifications required to the design itself:

| Demo Requirement | Design Mechanism | Status |
|---|---|---|
| Any user can create groves | Hub-level policy: `resourceType=grove, actions=[create]`, bound to all members | Policy engine exists, needs seed data |
| All users can read everything | Hub-level policy: `resourceType=*, actions=[read, list]`, bound to all members | Policy engine exists, needs seed data |
| Creator becomes grove member | Explicit group per grove + `AddGroupMember` on creation | Group infrastructure exists, needs new group type |
| Agent creation restricted to grove members | Grove-level policy: `resourceType=agent, actions=[create]`, bound to grove members group | Policy engine exists, needs seed data per grove |
| Agent attach/delete by creator only | Owner bypass in `AuthzService.CheckAccess()` (agent `OwnerID` == creating user) | Already implemented in `authz.go:116-121` |

### 2.2 No Design Limitations

The permissions design accommodates this scenario without any gaps:

- **Groups** support explicit user membership with the `AddGroupMember` store method
- **Policies** support hub-level and grove-level scoping
- **Policy bindings** can target groups, giving all members access
- **Owner bypass** handles the "only creator can manage" requirement naturally
- **`CheckAccess`** already resolves effective groups, evaluates policies, and applies owner bypass

### 2.3 Open Questions

**Q1: Grove members group type — explicit or new dynamic type?**

The `grove_agents` group is a dynamic group whose membership is resolved at query time (agents in the grove). For grove *user* members, we need an explicit group because membership is event-driven (user creates grove → becomes member), not query-derivable.

**Decision**: Use an explicit group with a naming convention (`grove:<slug>:members`). This is consistent with how `grove_agents` groups work (slug-based naming convention, auto-created on grove creation) but uses the existing `explicit` group type since membership is manually managed.

**Q2: Should hub-level seed policies be idempotent on every startup?**

If the Hub restarts, it should not create duplicate policies. The seeding logic must be idempotent — check for existence by name before creating.

**Decision**: Seed policies use well-known names and are created only if they don't already exist.

**Q3: What group represents "all authenticated hub members"?**

The permissions design references a `hub-members` group (Section 8.2) but none currently exists. We need a well-known group that all authenticated users belong to.

**Decision**: Create a `hub-members` system group on Hub initialization. Add every user to this group when they first log in (during user creation in the OAuth flow). This group serves as the principal for hub-level default policies.

---

## 3. Implementation Overview

### 3.1 Data Model Changes

**No schema changes required.** All necessary tables and fields already exist:

- `groups` table — stores both `grove_agents` and `explicit` groups
- `group_members` table — stores user-to-group membership
- `policies` table — stores hub-level and grove-level policies
- `policy_bindings` table — links policies to groups
- `agents.owner_id` — already set to the creating user on agent creation
- `agents.created_by` — already tracks creator

### 3.2 New Groups

| Group | Slug | Type | Created When | Members |
|---|---|---|---|---|
| Hub Members | `hub-members` | `explicit` | Hub initialization | All authenticated users (added on first login) |
| Grove Members | `grove:<slug>:members` | `explicit` | Grove creation | Creating user (added automatically) |

### 3.3 Seed Policies

#### Hub-Level (created on Hub initialization)

| Policy Name | Scope | Resource Type | Actions | Effect | Bound To |
|---|---|---|---|---|---|
| `hub-member-read-all` | `hub` | `*` | `read`, `list` | `allow` | `hub-members` group |
| `hub-member-create-groves` | `hub` | `grove` | `create` | `allow` | `hub-members` group |

#### Grove-Level (created on each grove creation)

| Policy Name | Scope | Resource Type | Actions | Effect | Bound To |
|---|---|---|---|---|---|
| `grove:<slug>:member-create-agents` | `grove` (scoped to grove ID) | `agent` | `create` | `allow` | `grove:<slug>:members` group |

### 3.4 Enforcement Points

| Endpoint | Action | Enforcement |
|---|---|---|
| `POST /api/v1/groves` | Create grove | `CheckAccess(user, Resource{Type:"grove"}, ActionCreate)` |
| `POST /api/v1/agents` | Create agent | `CheckAccess(user, Resource{Type:"agent", ParentType:"grove", ParentID:groveID}, ActionCreate)` |
| `DELETE /groves/{id}/agents/{id}` | Delete agent | `CheckAccess(user, Resource{Type:"agent", ID:agentID, OwnerID:agent.OwnerID}, ActionDelete)` |
| `POST /groves/{id}/agents/{id}/...` (attach, message, PTY) | Agent interaction | `CheckAccess(user, Resource{Type:"agent", ID:agentID, OwnerID:agent.OwnerID}, ActionAttach)` |
| `GET /api/v1/groves`, `GET /api/v1/agents` | List/read | `CheckAccess` (allowed by hub-level read policy) |

### 3.5 What Is NOT Enforced (This Phase)

- Grove deletion (only owner/admin, but no explicit check added — relies on existing ownership)
- Grove updates (same)
- Template management
- Group/policy management (admin-only endpoints — relies on existing `RequireRole` or admin bypass)
- User management

These are left open intentionally. The default-deny behavior of the policy engine means unenforced endpoints remain accessible to all authenticated users (as they are today), which is acceptable for the demo since the focus is on grove membership → agent creation and agent ownership → agent management.

---

## 4. Implementation Steps

### Step 1: Hub Initialization — System Groups and Seed Policies ✅

**Files**: `pkg/hub/server.go`, `pkg/hub/seed.go` (new)

Add a `seedDefaultPoliciesAndGroups()` method called during Hub server startup (after store initialization). This method:

1. **Creates `hub-members` group** (idempotent — skip if slug `hub-members` already exists)
   ```go
   group := &store.Group{
       ID:        api.NewUUID(),
       Name:      "Hub Members",
       Slug:      "hub-members",
       GroupType: store.GroupTypeExplicit,
       CreatedBy: "system",
       OwnerID:   "system",
   }
   ```

2. **Creates hub-level seed policies** (idempotent — skip if policy name already exists)
   - `hub-member-read-all`: hub scope, `resourceType=*`, `actions=[read, list]`, effect=allow
   - `hub-member-create-groves`: hub scope, `resourceType=grove`, `actions=[create]`, effect=allow

3. **Creates policy bindings** linking each seed policy to the `hub-members` group

**Idempotency approach**: Query policies by name before creating. The `ListPolicies` filter doesn't currently support filtering by name, so either:
- Add a `Name` field to `PolicyFilter` (preferred — simple store change), or
- List all hub-scoped policies and filter in-memory (acceptable for a small number of seed policies)

### Step 2: User Registration — Auto-Join Hub Members Group ✅

**Files**: `pkg/hub/handlers_auth.go`, `pkg/hub/web.go`

In the user creation/login flow (the `completeOAuthLogin` function and similar paths where users are created or have their session established), add the user to the `hub-members` group if they aren't already a member.

```go
// After user is created or retrieved:
hubMembersGroup, err := s.store.GetGroupBySlug(ctx, "hub-members")
if err == nil {
    _ = s.store.AddGroupMember(ctx, &store.GroupMember{
        GroupID:    hubMembersGroup.ID,
        MemberType: store.MemberTypeUser,
        MemberID:   user.ID,
        Role:       "member",
        AddedBy:    "system",
    })
    // Ignore ErrAlreadyExists — idempotent
}
```

**Note**: A `GetGroupBySlug` method may need to be added to the store interface if it doesn't exist. Alternatively, use `ListGroups` with a slug filter.

### Step 3: Grove Creation — Members Group and Policy ✅

**Files**: `pkg/hub/handlers.go`

Extend the existing `createGrove` handler (after line 1524 where `createGroveGroup` is called) to also:

1. **Create grove members group**:
   ```go
   membersGroup := &store.Group{
       ID:        api.NewUUID(),
       Name:      grove.Name + " Members",
       Slug:      "grove:" + grove.Slug + ":members",
       GroupType: store.GroupTypeExplicit,
       GroveID:   grove.ID,
       OwnerID:   grove.OwnerID,
       CreatedBy: grove.CreatedBy,
   }
   ```

2. **Add creating user as member**:
   ```go
   s.store.AddGroupMember(ctx, &store.GroupMember{
       GroupID:    membersGroup.ID,
       MemberType: store.MemberTypeUser,
       MemberID:   grove.CreatedBy,
       Role:       "member",
       AddedBy:    "system",
   })
   ```

3. **Create grove-level agent-creation policy**:
   ```go
   policy := &store.Policy{
       ID:           api.NewUUID(),
       Name:         "grove:" + grove.Slug + ":member-create-agents",
       Description:  "Grove members can create agents in " + grove.Name,
       ScopeType:    store.PolicyScopeGrove,
       ScopeID:      grove.ID,
       ResourceType: "agent",
       Actions:      []string{"create"},
       Effect:       store.PolicyEffectAllow,
       Priority:     0,
       CreatedBy:    "system",
   }
   ```

4. **Bind policy to grove members group**:
   ```go
   s.store.AddPolicyBinding(ctx, &store.PolicyBinding{
       PolicyID:      policy.ID,
       PrincipalType: store.PolicyPrincipalTypeGroup,
       PrincipalID:   membersGroup.ID,
   })
   ```

These operations follow the same best-effort pattern as the existing `createGroveGroup` call — failures are logged but don't fail grove creation.

### Step 4: Enforce Authorization in Agent Creation ✅

**Files**: `pkg/hub/handlers.go`

In `createAgent()` (around line 319, after user identity is resolved but before grove existence is verified), add an authorization check:

```go
if userIdent := GetUserIdentityFromContext(ctx); userIdent != nil {
    // Check if user can create agents in this grove
    decision := s.authzService.CheckAccess(ctx, userIdent, Resource{
        Type:       "agent",
        ParentType: "grove",
        ParentID:   req.GroveID,
    }, ActionCreate)
    if !decision.Allowed {
        writeError(w, http.StatusForbidden, ErrCodeForbidden,
            "You don't have permission to create agents in this grove", nil)
        return
    }
}
```

This check will:
- **Pass** for admins (admin bypass)
- **Pass** for grove owners (owner bypass — the user owns the grove)
- **Pass** for grove members (grove-level policy grants `create` on `agent` to members group)
- **Fail** for everyone else (default deny — no matching policy)

### Step 5: Enforce Authorization on Agent Delete ✅

**Files**: `pkg/hub/handlers.go`

In `deleteGroveAgent()` (around line 2476, after the agent is resolved but before `performAgentDelete` is called), add:

```go
if userIdent := GetUserIdentityFromContext(ctx); userIdent != nil {
    decision := s.authzService.CheckAccess(ctx, userIdent, Resource{
        Type:    "agent",
        ID:      agent.ID,
        OwnerID: agent.OwnerID,
    }, ActionDelete)
    if !decision.Allowed {
        writeError(w, http.StatusForbidden, ErrCodeForbidden,
            "Only the agent's creator can delete it", nil)
        return
    }
}
```

Also add the same check in `performAgentDelete` for the direct `/agents/{id}` DELETE path (the non-grove-scoped endpoint).

This check will:
- **Pass** for admins (admin bypass)
- **Pass** for the agent's creator (owner bypass — `agent.OwnerID` matches user)
- **Fail** for everyone else (no hub-level or grove-level policy grants `delete` on agents)

### Step 6: Enforce Authorization on Agent Attach/PTY ✅

**Files**: `pkg/hub/handlers.go`

In `handleGroveAgentAction()` (around line 2507, after the agent is resolved), add a check before dispatching to action handlers:

```go
// For interactive actions, verify the caller is the agent's owner
if action == "message" || action == "start" || action == "stop" || action == "restart" {
    if userIdent := GetUserIdentityFromContext(ctx); userIdent != nil {
        decision := s.authzService.CheckAccess(ctx, userIdent, Resource{
            Type:    "agent",
            ID:      agent.ID,
            OwnerID: agent.OwnerID,
        }, ActionAttach)
        if !decision.Allowed {
            writeError(w, http.StatusForbidden, ErrCodeForbidden,
                "Only the agent's creator can interact with it", nil)
            return
        }
    }
}
```

Similarly, in the PTY WebSocket handler (`handleAgentPTY`), add an ownership check before upgrading the connection.

### Step 7: Store Additions ✅

**Files**: `pkg/store/store.go`, `pkg/store/sqlite/sqlite.go`, `pkg/store/entadapter/policy_store.go`

Minor additions needed:

1. **`GetGroupBySlug(ctx, slug) (*Group, error)`** — already existed in the store interface.

2. **`PolicyFilter.Name string`** — added optional name filter to `PolicyFilter` for idempotent seed policy lookup. Implemented in both SQLite and entadapter `ListPolicies` queries.

### Step 8: Grove Registration Path ✅

**Files**: `pkg/hub/handlers.go`

The `handleGroveRegister` handler's registration path already calls `createGroveGroup`. Added a call to `createGroveMembersGroupAndPolicy` immediately after, ensuring groves created via `scion hub register` (the CLI registration flow) get the same policy treatment as groves created via the web API.

### Step 9: Tests ✅

**Files**: `pkg/hub/authz_test.go` (new or extended), `pkg/hub/handlers_test.go`

1. **Policy evaluation tests**: Verify that `CheckAccess` correctly allows/denies based on:
   - Hub-level read policy (all users can read)
   - Grove-level create policy (only members can create agents)
   - Owner bypass (only creator can delete/attach)
   - Admin bypass (admins can do everything)

2. **Integration tests**: End-to-end tests that:
   - Create a user → verify hub-members group membership
   - Create a grove → verify grove members group and policy created
   - Create an agent as grove member → succeeds
   - Create an agent as non-member → returns 403
   - Delete an agent as creator → succeeds
   - Delete an agent as different user → returns 403

---

## 5. Future Sophistication Path

This implementation is designed as a foundation, not a dead end. The following extensions are natural next steps within the existing permissions design:

### 5.1 Grove Membership Management
- Add API endpoints to invite/add users to grove members groups
- Add a web UI for grove membership management
- The group and policy infrastructure already supports this — it's purely a UX addition

### 5.2 Role-Based Grove Access
- Replace the single "members can create agents" policy with role-differentiated policies:
  - `grove:viewer` — can read agents but not create
  - `grove:member` — can create and manage own agents
  - `grove:admin` — can manage all agents and grove settings
- Uses the existing `GroupMember.Role` field and additional policies

### 5.3 Fine-Grained Agent Permissions
- Allow grove members (not just creators) to attach to specific agents via resource-scoped policies
- Share agent access with specific users via direct policy bindings
- Uses existing `resource` scope type and user bindings

### 5.4 Hub-Level Deny Policies
- Restrict certain users from creating groves via hub-level deny policies
- Uses existing `effect=deny` support

### 5.5 Label-Based Policies
- Restrict agent operations based on labels (e.g., `environment=production` agents can't be deleted by non-admins)
- Uses existing `PolicyConditions.Labels` support

### 5.6 Delegation for Agents
- Enable agents to operate with delegated permissions from their creator
- Uses the existing `DelegationEnabled` flag and delegation condition support in `AuthzService`

---

## 6. Implementation Sequence & Dependencies

```
Step 1: Seed groups/policies on Hub init ✅
  │
  ├── Step 2: Auto-join hub-members on user login ✅
  │     (depends on hub-members group from Step 1)
  │
  └── Step 3: Grove creation → members group + policy ✅
        │
        ├── Step 4: Enforce agent creation (depends on grove-level policy from Step 3) ✅
        │
        ├── Step 5: Enforce agent deletion (independent — uses owner bypass only) ✅
        │
        └── Step 6: Enforce agent attach/PTY (independent — uses owner bypass only) ✅

Step 7: Store additions (prerequisite for Steps 1-3) ✅
Step 8: Grove registration path (parallel to Step 3) ✅
Step 9: Tests (after Steps 1-8) ✅
```

**Recommended order**: Step 7 → Step 1 → Step 2 → Step 3 + Step 8 → Steps 4, 5, 6 (parallel) → Step 9

**All steps complete.**

---

## 7. Summary

This plan activates the existing policy engine for a meaningful access control scenario. The key architectural decision — using real policies and groups rather than ad-hoc handler checks — means every enforcement point flows through `AuthzService.CheckAccess()`, which supports the full permissions design (scoped policies, group membership, owner bypass, admin bypass, priority ordering). Future sophistication builds on the same code path, not a parallel system.

The demo answers the question: *"Can the permissions design work in practice?"* — yes, and the answer uses ~200-300 lines of new code distributed across existing files, with no schema changes, no new tables, and no new concepts beyond what `permissions-design.md` and `groups-design.md` already describe.

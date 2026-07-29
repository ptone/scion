# Hub Permissions System Design

## Status
**Proposed**

## 1. Overview

This document specifies the authorization and access control system for the Scion Hub. The permissions system provides fine-grained access control for resources while maintaining operational simplicity and clear security boundaries.

### Goals

1. **Principal-based access control** - Users and groups as the basis for identity
2. **Hierarchical groups** - Groups can contain users and other groups
3. **Resource-scoped policies** - Policies attached to resources with scope inheritance
4. **CRUD-based actions** - Simple, intuitive action model
5. **Hierarchical policy inheritance** - Hub -> Grove -> Resource with override semantics
6. **Solid debug logging** - Comprehensive audit trail for authorization decisions
7. **Hub-only authorization** - All authz logic in the Hub server

### Non-Goals

- Authentication mechanisms (covered in separate design documents)
- Runtime broker authorization (separate trust model)
- Fine-grained action permissions beyond CRUD (initially)
- Cross-hub federation

---

## 2. Core Concepts

### 2.1 Principals

A **Principal** is an identity that can be granted permissions. There are two types:

#### User

A registered user account with identity and credentials.

```json
{
  "id": "user-uuid",
  "email": "user@example.com",
  "displayName": "Alice Developer",
  "role": "member",
  "status": "active"
}
```

**User Role vs Group Permissions:**

The `User.role` field represents the user's *system-level role* within the Hub itself, distinct from group-based permissions:

| User Role | Purpose | Permissions Granted |
|-----------|---------|---------------------|
| `admin` | Hub administrators | Bypass permission checks; full access to all resources and Hub configuration |
| `member` | Standard users | Subject to policy-based permissions; can create resources and be granted access via policies |
| `viewer` | Read-only users | Cannot create resources; can only access resources explicitly shared via policies |

**Key distinction:** `User.role` determines the user's baseline capabilities and whether they bypass the policy engine entirely (`admin`). Group-based permissions (via policies) layer on top of the baseline for `member` and `viewer` roles.

#### Group

A collection of principals (users and/or other groups).

> **Note:** The final permission check is always against a user, as only users take direct actions. When a user attempts a policy-guarded operation, their membership in any groups listed in the policy must be resolved via Ent graph traversals to determine effective access.

```json
{
  "id": "group-uuid",
  "name": "platform-team",
  "slug": "platform-team",
  "description": "Platform engineering team",
  "members": [
    {"type": "user", "id": "user-uuid-1"},
    {"type": "user", "id": "user-uuid-2"},
    {"type": "group", "id": "devops-group-uuid"}
  ]
}
```

**Group Membership (Transitive Containment):**

Groups can contain other groups as members, creating transitive membership:

```
engineering (group)
├── platform-team (group)   ← member of engineering
│   ├── alice (user)
│   └── bob (user)
├── frontend-team (group)
│   └── charlie (user)
└── devops (group)
    ├── platform-team (group)  ← platform-team is also member of devops
    └── dave (user)
```

In this example:
- Alice and Bob are direct members of `platform-team`
- Alice and Bob are also *transitive* members of both `engineering` and `devops` (via `platform-team`)
- If a policy grants access to `engineering`, all of: alice, bob, charlie, dave, and all members of platform-team, frontend-team, and devops have access

**Membership Resolution:**

When checking if a user has access through a group, the system performs upward traversal:
1. Find all groups the user belongs to directly
2. Recursively find all groups that contain those groups
3. The user's "effective groups" is the union of all groups found

A user gains access to resources through policies attached to those resources (or their containing scopes). When a policy lists a group as a principal, all users in that group (directly or transitively) are granted the policy's permissions.

### 2.2 Resources

Resources are the objects that policies protect. The key resources are:

| Resource Type | Description | Containment Scope | Owner Field |
|---------------|-------------|-------------------|-------------|
| `hub` | The Hub itself (singleton) | - (root) | N/A |
| `grove` | A project/workspace | Hub | `ownerId` |
| `agent` | An agent instance | Grove | `ownerId` |
| `template` | An agent template | Hub or Grove | `ownerId` |
| `user` | A user account | Hub | Self |
| `group` | A user group | Hub | `ownerId` |

**Resource Ownership:**

Each resource has an `ownerId` field indicating the user who owns it. Ownership provides:
- Implicit full access to the resource (owner bypass)
- The ability to transfer ownership via the `update` action
- The ability to attach policies to the resource

**Ownership Transfer:** A user with the `update` action on a resource can modify the `ownerId` field to transfer ownership. The new owner must be an active user in the Hub.

> **Decision: User-Scoped Templates**
>
> Templates can be personal to a user. Two approaches were considered:
>
> | Approach | Description | Pros | Cons |
> |----------|-------------|------|------|
> | **User as containment scope** | Add `user` as a containment scope alongside hub/grove | Clean hierarchy, user "owns" their templates | Adds complexity to scope resolution, third scope type |
> | **Hub-level with user membership** | Store templates at hub level, add user as policy member | Simpler scope model, reuses existing patterns | Template ownership less explicit, querying user's templates requires policy traversal |
>
> **Decision:** Use hub-level storage with user membership for simplicity. User-scoped containment can be added later if the access patterns become unwieldy.
>
> **Note:** UX considerations for personal template management (discovery, listing, ownership display) will be addressed in later implementation phases.

### 2.3 Resource Scopes (Containment Hierarchy)

Resources exist within a containment hierarchy that determines policy inheritance:

```
Hub (root scope)
├── Users
├── Groups
├── Templates (global scope)
└── Groves
    ├── Templates (grove scope)
    └── Agents
```

**Scope Resolution Path:**
- Agent: `hub -> grove -> agent`
- Template (grove): `hub -> grove -> template`
- Template (global): `hub -> template`
- Grove: `hub -> grove`
- User/Group: `hub -> user/group`

### 2.4 Actions

Actions represent operations that can be performed on resources. The system uses CRUD-based actions:

| Action | Description | Applies To |
|--------|-------------|------------|
| `create` | Create new resource | All |
| `read` | View resource details | All |
| `update` | Modify resource | All |
| `delete` | Remove resource | All |
| `list` | List resources in scope | Container resources |
| `manage` | Administrative operations | Hub, Grove |

**`manage` Action Details:**

The `manage` action grants administrative capabilities beyond standard CRUD:

| Resource | `manage` Grants |
|----------|-----------------|
| Hub | Modify Hub settings, view all resources, manage default policies |
| Grove | Modify grove settings, manage grove-level policies, set default runtime broker |

**Extended Actions** (resource-specific):

| Resource | Action | Description |
|----------|--------|-------------|
| Agent | `start` | Start the agent |
| Agent | `stop` | Stop the agent |
| Agent | `message` | Send message to agent |
| Agent | `attach` | PTY attachment |
| Grove | `register` | Register grove with Hub |
| Group | `add_member` | Add member to group |
| Group | `remove_member` | Remove member from group |

**Wildcard Actions:**

Policies can specify `["*"]` for actions to grant all actions applicable to the resource type. When new actions are added to a resource type, existing wildcard policies automatically include them.

### 2.5 Policies

A **Policy** defines what actions principals can perform on resources within a scope. Each policy is attached to a single containment scope (hub, grove, or specific resource) and specifies which resource types and actions it governs within that scope.

See [Section 4.3 Policy Data Model](#43-policy) for the detailed schema.

```json
{
  "id": "policy-uuid",
  "name": "platform-team-grove-admin",
  "description": "Full access to grove resources",

  // Scope attachment (single scope per policy)
  "scopeType": "grove",
  "scopeId": "grove-uuid",

  // What resources this policy covers within the scope
  "resourceType": "agent",        // Or "*" for all types
  "resourceId": null,             // Optional: specific resource

  "actions": ["create", "read", "update", "delete", "manage"],

  "effect": "allow",              // "allow" or "deny"

  "priority": 0,                  // See Section 3.5

  "conditions": {                 // Optional conditions
    "labels": {"environment": "production"}
  }
}
```

**Wildcard Resource Types:**

Setting `resourceType: "*"` means the policy applies to all resource types within the scope. This is commonly used for administrative policies.

Principals are bound to policies via `PolicyBinding` records (see [Section 4.4](#44-policybinding)).

---

## 3. Policy Resolution

### 3.1 Resolution Order

Policy resolution follows the containment hierarchy from **higher to lower levels**, with **lower levels overriding higher levels**:

```
1. Hub-level policies (default, lowest priority)
2. Grove-level policies
3. Resource-specific policies (highest priority)
```

This means:
- A policy at the grove level overrides a hub-level policy
- A policy attached to a specific agent overrides grove-level policies

### 3.2 Override Semantics

**Important:** This design uses an **override model**, not a **least-privilege model**.

| Approach | Behavior | Example |
|----------|----------|---------|
| **Override (chosen)** | Lower-level policies replace higher-level | Hub: deny all -> Grove: allow read -> Agent has read |
| **Least-privilege** | Most restrictive wins | Hub: deny all -> Grove: allow read -> Agent denied |

**Rationale for Override:**
1. **Delegation** - Grove owners can grant access without Hub admin intervention
2. **Autonomy** - Teams can manage their own grove permissions
3. **Simplicity** - Easier to reason about: "what's set here wins"

**Trade-offs:**
- Risk: Lower-level admins can grant access that Hub admins might not want
- Mitigation: Use `deny` policies at higher levels that cannot be overridden (see "Hard Deny" below)

### 3.3 Resolution Algorithm

> **Note:** This is an illustrative algorithm. The actual implementation may differ based on Ent query patterns and performance optimizations discovered during development.

```go
func resolveAccess(ctx context.Context, principal Principal, resource Resource, action Action) Decision {
    log := authzLogger(ctx)

    // Step 0: Check for admin bypass
    if principal.Role == "admin" {
        log.Debug("admin bypass",
            "principal", principal.ID)
        return Decision{Allowed: true, Reason: "admin_role"}
    }

    // Step 1: Check resource ownership
    if resource.OwnerID == principal.ID {
        log.Debug("owner bypass",
            "principal", principal.ID,
            "resource", resource.ID)
        return Decision{Allowed: true, Reason: "owner"}
    }

    // Step 2: Expand principal's effective groups (flatten hierarchy)
    effectiveGroups := expandGroups(ctx, principal)
    allPrincipals := append([]Principal{principal}, effectiveGroups...)

    log.Debug("resolving access",
        "principal", principal.ID,
        "resource", resource.ID,
        "action", action,
        "effectiveGroups", len(effectiveGroups))

    // Step 3: Collect policies at each scope level
    scopes := getResourceScopes(resource) // e.g., [hub, grove, agent]

    var resolvedDecision *Decision = nil

    for _, scope := range scopes {
        // Get policies for this scope, sorted by priority ascending
        policies := getPoliciesForScope(ctx, scope, allPrincipals)
        sort.Slice(policies, func(i, j int) bool {
            return policies[i].Priority < policies[j].Priority
        })

        for _, policy := range policies {
            if matchesResource(policy, resource) && matchesAction(policy, action) {
                if !evaluateConditions(policy.Conditions, ctx, resource) {
                    log.Debug("policy conditions not met",
                        "policy", policy.ID)
                    continue
                }

                log.Debug("policy matched",
                    "policy", policy.ID,
                    "scope", scope,
                    "effect", policy.Effect,
                    "priority", policy.Priority)

                // Future: hard_deny check would go here (see Section 3.4)

                // Higher priority within same scope overrides lower priority
                // Lower scope level overrides higher scope level
                resolvedDecision = &Decision{
                    Allowed: policy.Effect == "allow",
                    Reason:  policy.Effect,
                    Policy:  policy,
                    Scope:   scope,
                }
            }
        }
    }

    // Step 4: Apply default (deny if no policy matched)
    if resolvedDecision == nil {
        log.Debug("no matching policy, applying default deny",
            "principal", principal.ID,
            "resource", resource.ID,
            "action", action)
        return Decision{Allowed: false, Reason: "no_matching_policy"}
    }

    log.Info("access decision",
        "allowed", resolvedDecision.Allowed,
        "principal", principal.ID,
        "resource", resource.ID,
        "action", action,
        "policy", resolvedDecision.Policy.ID,
        "scope", resolvedDecision.Scope)

    return *resolvedDecision
}
```

### 3.4 Policy Effects

| Effect | Behavior | Override-able |
|--------|----------|---------------|
| `allow` | Grants access | Yes |
| `deny` | Denies access | Yes (by lower-level allow) |

> **Future Enhancement: Hard Deny**
>
> A `hard_deny` effect could provide Hub admins a mechanism to enforce restrictions that grove owners cannot override. This is documented in [Section 12.1](#121-policy-resolution-override-vs-least-privilege) as a potential future refinement. For initial implementation, the override model with standard `deny` is sufficient.

### 3.5 Priority Within Same Scope

When multiple policies match at the same scope level (e.g., two grove-level policies), they are evaluated in order of their `priority` field (ascending). Higher priority values are evaluated last and take precedence.

**Example:**
```
Grove-level policies for agent access:
- Policy A: priority=0, effect=allow, actions=[read]
- Policy B: priority=10, effect=deny, actions=[read, delete]
- Policy C: priority=20, effect=allow, actions=[delete]

Result for "read" action: deny (Policy B overrides Policy A)
Result for "delete" action: allow (Policy C overrides Policy B)
Result for "update" action: no match (default deny)
```

**Priority Guidelines:**
- `0-9`: Base policies (default grants)
- `10-49`: Standard policies
- `50-89`: Override policies
- `90-99`: Emergency/exception policies

---

## 4. Data Models

### 4.1 Group

> **Initial Proposal:** This schema may be refined based on [Ent traversal best practices](https://entgo.io/docs/traversals/) during implementation.

```go
// Group represents a user group that can contain users and other groups.
type Group struct {
    // Identity
    ID          string `json:"id"`          // UUID primary key
    Name        string `json:"name"`        // Human-friendly name
    Slug        string `json:"slug"`        // URL-safe identifier
    Description string `json:"description,omitempty"`

    // Metadata
    Labels      map[string]string `json:"labels,omitempty"`
    Annotations map[string]string `json:"annotations,omitempty"`

    // Timestamps
    Created time.Time `json:"created"`
    Updated time.Time `json:"updated"`

    // Ownership
    CreatedBy string `json:"createdBy,omitempty"`
    OwnerID   string `json:"ownerId,omitempty"`
}
```

### 4.2 GroupMember

```go
// GroupMember represents membership in a group.
type GroupMember struct {
    GroupID     string    `json:"groupId"`     // FK to Group.ID
    MemberType  string    `json:"memberType"`  // "user" or "group"
    MemberID    string    `json:"memberId"`    // FK to User.ID or Group.ID
    Role        string    `json:"role"`        // "member", "admin", "owner"
    AddedAt     time.Time `json:"addedAt"`
    AddedBy     string    `json:"addedBy,omitempty"`
}

// MemberType constants
const (
    MemberTypeUser  = "user"
    MemberTypeGroup = "group"
)
```

**GroupMember Role:**

The `Role` field in `GroupMember` represents the member's role *within the group*, not their permissions:

| Role | Capabilities |
|------|--------------|
| `member` | Standard membership; gains access through group's policies |
| `admin` | Can add/remove members and modify group metadata |
| `owner` | Full control; can delete group and transfer ownership |

### 4.3 Policy

```go
// Policy defines access control rules.
type Policy struct {
    // Identity
    ID          string `json:"id"`   // UUID primary key
    Name        string `json:"name"` // Human-friendly name
    Description string `json:"description,omitempty"`

    // Scope attachment
    ScopeType string `json:"scopeType"` // "hub", "grove", "resource"
    ScopeID   string `json:"scopeId"`   // ID of the scope (grove ID, resource ID, or empty for hub)

    // What the policy applies to
    ResourceType string   `json:"resourceType"` // "agent", "grove", "template", etc. or "*" for all
    ResourceID   string   `json:"resourceId,omitempty"` // Specific resource (optional)
    Actions      []string `json:"actions"`      // ["create", "read", "update", "delete", ...] or ["*"]

    // Effect
    Effect string `json:"effect"` // "allow", "deny" (future: "hard_deny")

    // Conditions (optional)
    Conditions *PolicyConditions `json:"conditions,omitempty"`

    // Priority for ordering within same scope (higher = evaluated later, can override)
    Priority int `json:"priority"`

    // Metadata
    Labels      map[string]string `json:"labels,omitempty"`
    Annotations map[string]string `json:"annotations,omitempty"`

    // Timestamps
    Created time.Time `json:"created"`
    Updated time.Time `json:"updated"`

    // Ownership
    CreatedBy string `json:"createdBy,omitempty"`
}
```

### 4.4 PolicyBinding

Policies are attached to resources (or scopes) via the `Policy.ScopeType` and `Policy.ScopeID` fields (see [Section 4.3](#43-policy)). The `PolicyBinding` record links principals (users or groups) to a policy, granting those principals the permissions defined by the policy.

This model supports many principals having access to the same resource through a single policy.

```go
// PolicyBinding links principals to a policy.
// The policy itself is attached to a resource/scope via its ScopeType and ScopeID fields.
type PolicyBinding struct {
    ID            string    `json:"id"`            // UUID primary key
    PolicyID      string    `json:"policyId"`      // FK to Policy.ID
    PrincipalType string    `json:"principalType"` // "user" or "group"
    PrincipalID   string    `json:"principalId"`   // FK to User.ID or Group.ID
    Created       time.Time `json:"created"`
    CreatedBy     string    `json:"createdBy,omitempty"`
}
```

**Lookup Pattern:** To find all principals with access to a resource:
1. Find policies where `ScopeID` matches the resource (or its containing scope)
2. Query `PolicyBinding` for all principals bound to those policies
3. Expand group memberships to get individual users

### 4.5 PolicyConditions

```go
// PolicyConditions defines optional conditions for policy matching.
type PolicyConditions struct {
    // Label matching (all labels must match - AND semantics)
    Labels map[string]string `json:"labels,omitempty"`

    // Time-based conditions
    ValidFrom  *time.Time `json:"validFrom,omitempty"`
    ValidUntil *time.Time `json:"validUntil,omitempty"`

    // IP-based conditions (for future use)
    SourceIPs []string `json:"sourceIps,omitempty"`
}
```

**Label Matching Semantics:**

- **AND logic**: All specified labels must match for the condition to be true
- **Exact match**: Label values must match exactly (case-sensitive)
- **Missing label**: If a resource lacks a required label, the condition fails

**Example:**
```json
{
  "conditions": {
    "labels": {
      "environment": "production",
      "team": "platform"
    }
  }
}
```
This policy only matches resources where *both* `environment=production` AND `team=platform`.

**Time-Based Condition Evaluation:**

- `validFrom`: Policy only applies after this time (inclusive)
- `validUntil`: Policy only applies before this time (inclusive)
- Both can be specified to create a time window
- If the current time is outside the window, the policy is skipped (not matched)

---

## 5. Database Schema (Ent)

The implementation uses [entgo.io](https://entgo.io/) for the persistence layer, particularly for managing the user-group hierarchy with its graph-like relationships.

### 5.1 Ent Schema: Group

```go
// Group holds the schema definition for the Group entity.
type Group struct {
    ent.Schema
}

// Fields of the Group.
func (Group) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),
        field.String("name").NotEmpty(),
        field.String("slug").NotEmpty().Unique(),
        field.String("description").Optional(),
        field.JSON("labels", map[string]string{}).Optional(),
        field.JSON("annotations", map[string]string{}).Optional(),
        field.Time("created").Default(time.Now),
        field.Time("updated").Default(time.Now).UpdateDefault(time.Now),
        field.String("created_by").Optional(),
        field.String("owner_id").Optional(),
    }
}

// Edges of the Group.
func (Group) Edges() []ent.Edge {
    return []ent.Edge{
        // Member users (users directly in this group)
        edge.To("users", User.Type),

        // Member groups (groups contained within this group)
        edge.To("member_groups", Group.Type),
        edge.From("parent_groups", Group.Type).Ref("member_groups"),

        // Policies attached to this group as principal
        edge.From("policies", Policy.Type).Ref("principals_groups"),

        // Owner relationship
        edge.To("owner", User.Type).Unique(),
    }
}
```

### 5.2 Ent Schema: Policy

```go
// Policy holds the schema definition for the Policy entity.
type Policy struct {
    ent.Schema
}

// Fields of the Policy.
func (Policy) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),
        field.String("name").NotEmpty(),
        field.String("description").Optional(),

        field.Enum("scope_type").Values("hub", "grove", "resource"),
        field.String("scope_id").Optional(), // Grove ID or resource ID

        field.String("resource_type").Default("*"),
        field.String("resource_id").Optional(),
        field.JSON("actions", []string{}),

        field.Enum("effect").Values("allow", "deny"), // Future: add "hard_deny"

        field.JSON("conditions", &PolicyConditions{}).Optional(),
        field.Int("priority").Default(0),

        field.JSON("labels", map[string]string{}).Optional(),
        field.JSON("annotations", map[string]string{}).Optional(),
        field.Time("created").Default(time.Now),
        field.Time("updated").Default(time.Now).UpdateDefault(time.Now),
        field.String("created_by").Optional(),
    }
}

// Edges of the Policy.
func (Policy) Edges() []ent.Edge {
    return []ent.Edge{
        // Principal bindings
        edge.To("principals_users", User.Type),
        edge.To("principals_groups", Group.Type),

        // Scope attachment (for grove-level policies)
        edge.To("grove", Grove.Type).Unique(),
    }
}
```

### 5.3 Ent Schema Updates: User

Add group membership edge to existing User schema:

```go
// Edges of the User (additions).
func (User) Edges() []ent.Edge {
    return []ent.Edge{
        // Group memberships
        edge.From("groups", Group.Type).Ref("users"),

        // Policies attached to this user
        edge.From("policies", Policy.Type).Ref("principals_users"),

        // Groups owned by this user
        edge.From("owned_groups", Group.Type).Ref("owner"),
    }
}
```

---

## 6. API Endpoints

### 6.1 Group Endpoints

#### List Groups
```
GET /api/v1/groups
```

**Query Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `memberId` | string | Filter groups containing this member (direct or transitive) |
| `ownerId` | string | Filter groups owned by this user |
| `limit` | int | Maximum results |
| `cursor` | string | Pagination cursor |

#### Get Group
```
GET /api/v1/groups/{groupId}
```

#### Create Group
```
POST /api/v1/groups
```

**Request Body:**
```json
{
  "name": "Platform Team",
  "slug": "platform-team",
  "description": "Platform engineering team"
}
```

**Authorization:** Requires `create` action on `group` resource type at hub scope.

#### Update Group
```
PATCH /api/v1/groups/{groupId}
```

**Authorization:** Requires `update` action on the group, or `admin` role within the group.

#### Delete Group
```
DELETE /api/v1/groups/{groupId}
```

**Query Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `cascade` | bool | Delete associated policy bindings (default: false, returns error if bindings exist) |

**Authorization:** Requires group owner or Hub admin.

#### List Group Members
```
GET /api/v1/groups/{groupId}/members
```

**Query Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `expand` | bool | Include transitive members (default: false) |

#### Add Group Member
```
POST /api/v1/groups/{groupId}/members
```

**Request Body:**
```json
{
  "memberType": "user",
  "memberId": "user-uuid",
  "role": "member"
}
```

**Authorization:** Requires `add_member` action on the group, or `admin` role within the group.

#### Remove Group Member
```
DELETE /api/v1/groups/{groupId}/members/{memberType}/{memberId}
```

**Authorization:** Requires `remove_member` action on the group, or `admin` role within the group.

### 6.2 Policy Endpoints

#### List Policies
```
GET /api/v1/policies
```

**Query Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `scopeType` | string | Filter by scope type (hub, grove, resource) |
| `scopeId` | string | Filter by scope ID |
| `resourceType` | string | Filter by resource type |
| `principalId` | string | Filter policies affecting this principal |

**Authorization:** Returns only policies the caller can view (based on scope access).

> **Implementation note (ptone/scion#591):** the shipped gate is **hub-admin
> only** (via `requireAdmin`), stricter than the scope-relative viewability
> described here: the listing previously returned every policy to any
> authenticated caller, disclosing the hub's full authorization posture. Listing
> a policy's bindings (GET `.../principals`) is likewise hub-admin-only in this
> interim, for the same reason. The admin-only interim was **extended to reads by
> the coordinating maintainer** (Rule 13 decision 2026-07-29 01:40Z) — not a
> direct maintainer ratification of the read gate; the later relaxation toward
> scope-relative viewability remains a tracked follow-on. See PR-body
> behaviour-change #12.

#### Get Policy
```
GET /api/v1/policies/{policyId}
```

**Authorization:** Hub admins only (via `requireAdmin`), gated as part of
ptone/scion#591. The gate runs before the store lookup, so a non-admin receives
403 rather than 404 for any policy ID, avoiding an existence oracle. The
admin-only interim was extended to reads by the coordinating maintainer (Rule 13
decision 2026-07-29 01:40Z) — not a direct maintainer ratification of the read
gate; scope-relative relaxation remains a tracked follow-on. See PR-body
behaviour-change #12.

#### Create Policy
```
POST /api/v1/policies
```

**Request Body:**
```json
{
  "name": "Grove Admin Policy",
  "description": "Full access to grove resources",
  "scopeType": "grove",
  "scopeId": "grove-uuid",
  "resourceType": "*",
  "actions": ["create", "read", "update", "delete", "manage"],
  "effect": "allow",
  "priority": 10,
  "principals": [
    {"type": "group", "id": "platform-team-uuid"}
  ]
}
```

**Authorization:** Requires `manage` action on the scope (hub or grove).

> **Implementation note (ptone/scion#591):** the shipped gate is deliberately
> **stricter** than this spec — **hub-admin only** (via `requireAdmin`), not the
> scope-relative `manage` described here. This is the conservative interim that
> fully closes the measured authorization-bypass. The write handlers were
> previously ungated, so any authenticated caller could author a policy and bind
> it to themselves, which enabled two escalations: **arm B** — a caller grants
> itself access to a resource it otherwise cannot reach; **arm C** — a caller's
> resource-scoped `allow` overrides an admin's hub-scoped `deny`, because a more
> specific scope (resource) outranks a broader one (hub) in evaluation. The
> admin-only gate for these write ops was **ratified by the maintainer (ptone)
> 2026-07-29**; a later relaxation back toward scope-relative `manage` remains a
> tracked follow-on. See PR-body behaviour-change #12.

#### Update Policy
```
PATCH /api/v1/policies/{policyId}
```

**Authorization:** Requires `manage` action on the policy's scope.

> **Implementation note (ptone/scion#591):** shipped gate is hub-admin only, not
> scope-relative `manage` — see the note under Create Policy. Ratified by the maintainer
> (ptone) 2026-07-29; scope-relative relaxation remains a tracked follow-on.
> PR-body behaviour-change #12.

#### Delete Policy
```
DELETE /api/v1/policies/{policyId}
```

**Authorization:** Requires `manage` action on the policy's scope.

> **Implementation note (ptone/scion#591):** shipped gate is hub-admin only, not
> scope-relative `manage` — see the note under Create Policy. Ratified by the maintainer
> (ptone) 2026-07-29; scope-relative relaxation remains a tracked follow-on.
> PR-body behaviour-change #12.

#### Add Principal to Policy
```
POST /api/v1/policies/{policyId}/principals
```

**Request Body:**
```json
{
  "principalType": "user",
  "principalId": "user-uuid"
}
```

**Authorization:** Hub admins only (via `requireAdmin`) — see the note under
Create Policy. Binding a policy to a principal is the grant-conferring step;
before the gate any authenticated caller could bind a self-authored policy to
themselves. Ratified by the maintainer (ptone) 2026-07-29; scope-relative
relaxation remains a tracked follow-on. PR-body behaviour-change #12.

#### Remove Principal from Policy
```
DELETE /api/v1/policies/{policyId}/principals/{principalType}/{principalId}
```

**Authorization:** Hub admins only (via `requireAdmin`) — see the note under
Create Policy. A policy reaches a principal only through a binding, so unbinding
overrides the policy it carried; left ungated a caller could shed an
admin-established deny by removing its binding. Ratified by the maintainer
(ptone) 2026-07-29; scope-relative relaxation remains a tracked follow-on.
PR-body behaviour-change #12.

#### Evaluate Access (Debug/Test)
```
POST /api/v1/policies/evaluate
```

**Request Body:**
```json
{
  "principalType": "user",
  "principalId": "user-uuid",
  "resourceType": "agent",
  "resourceId": "agent-uuid",
  "action": "delete"
}
```

**Response:**
```json
{
  "allowed": true,
  "reason": "allow",
  "matchedPolicy": {
    "id": "policy-uuid",
    "name": "Grove Admin Policy",
    "scopeType": "grove",
    "effect": "allow"
  },
  "evaluationPath": [
    {"scope": "hub", "policies": 2, "matched": 0},
    {"scope": "grove", "policies": 1, "matched": 1}
  ],
  "effectiveGroups": ["platform-team-uuid", "engineering-uuid"]
}
```

**Authorization:** Hub admins only, or caller must be the evaluated principal.

---

## 7. Resource-Scoped Policy Attachment

### 7.1 Grove-Level Policies

Groves can have policies attached that apply to all resources within:

```
GET /api/v1/groves/{groveId}/policies
POST /api/v1/groves/{groveId}/policies
DELETE /api/v1/groves/{groveId}/policies/{policyId}
```

**Authorization:** Requires `manage` action on the grove.

### 7.2 Hub-Level Policies

Hub-level policies apply globally:

```
GET /api/v1/hub/policies
POST /api/v1/hub/policies
DELETE /api/v1/hub/policies/{policyId}
```

**Authorization:** Hub admins only.

### 7.3 Resource-Specific Policies

Policies can be attached to specific resources:

```
GET /api/v1/agents/{agentId}/policies
POST /api/v1/agents/{agentId}/policies
DELETE /api/v1/agents/{agentId}/policies/{policyId}
```

**Authorization:** Requires resource ownership or `manage` action on the containing grove.

---

## 8. Built-in Roles and Policies

### 8.1 System Roles

The system includes predefined roles for common access patterns:

| Role | Description | Permissions |
|------|-------------|-------------|
| `hub:admin` | Full Hub administration | All actions on all resources |
| `hub:member` | Standard Hub user | Create groves, manage own resources |
| `hub:viewer` | Read-only access | Read all visible resources |
| `grove:admin` | Grove administration | All actions within grove |
| `grove:member` | Grove member | Create/manage agents in grove |
| `grove:viewer` | Grove read-only | Read grove resources |

### 8.2 Default Policies

On Hub initialization, create default policies:

```go
var defaultPolicies = []Policy{
    {
        Name:         "hub-admin-full-access",
        Description:  "Hub administrators have full access",
        ScopeType:    "hub",
        ResourceType: "*",
        Actions:      []string{"*"},
        Effect:       "allow",
        Priority:     0,
        // Bound to group: hub-admins
    },
    {
        Name:         "hub-member-create-groves",
        Description:  "Hub members can create groves",
        ScopeType:    "hub",
        ResourceType: "grove",
        Actions:      []string{"create"},
        Effect:       "allow",
        Priority:     0,
        // Bound to group: hub-members
    },
    {
        Name:         "hub-member-create-groups",
        Description:  "Hub members can create groups",
        ScopeType:    "hub",
        ResourceType: "group",
        Actions:      []string{"create"},
        Effect:       "allow",
        Priority:     0,
        // Bound to group: hub-members
    },
}
```

### 8.3 Owner Bypass

Resource ownership provides implicit full access. This is **not** implemented as a policy but as a check in the resolution algorithm (see Section 3.3, Step 1). The owner bypass:

- Applies before any policy evaluation
- Cannot be overridden by deny policies
- Grants all actions on the owned resource

---

## 9. Authorization Middleware

### 9.1 HTTP Middleware

```go
// AuthzMiddleware enforces authorization on API requests.
func AuthzMiddleware(authz *AuthzService) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx := r.Context()
            log := authzLogger(ctx)

            // Get authenticated user from context
            user := auth.UserFromContext(ctx)
            if user == nil {
                log.Warn("no user in context")
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }

            // Extract resource and action from request
            resource, action := extractResourceAndAction(r)

            log.Debug("authorization check",
                "user", user.ID,
                "method", r.Method,
                "path", r.URL.Path,
                "resource", resource,
                "action", action)

            // Check authorization
            decision := authz.CheckAccess(ctx, user, resource, action)

            if !decision.Allowed {
                log.Warn("access denied",
                    "user", user.ID,
                    "resource", resource,
                    "action", action,
                    "reason", decision.Reason)

                writeJSON(w, http.StatusForbidden, map[string]interface{}{
                    "error": map[string]interface{}{
                        "code":    "forbidden",
                        "message": "You don't have permission to perform this action",
                        "details": map[string]string{
                            "resource": resource.Type + "/" + resource.ID,
                            "action":   string(action),
                            "reason":   decision.Reason,
                        },
                    },
                })
                return
            }

            log.Debug("access granted",
                "user", user.ID,
                "resource", resource,
                "action", action,
                "policy", decision.Policy.ID)

            next.ServeHTTP(w, r)
        })
    }
}
```

### 9.2 Resource and Action Extraction

```go
// extractResourceAndAction determines the resource and action from an HTTP request.
func extractResourceAndAction(r *http.Request) (Resource, Action) {
    path := r.URL.Path
    method := r.Method

    // Map HTTP methods to actions
    methodToAction := map[string]Action{
        "GET":    ActionRead,
        "POST":   ActionCreate,
        "PUT":    ActionUpdate,
        "PATCH":  ActionUpdate,
        "DELETE": ActionDelete,
    }

    action := methodToAction[method]

    // Parse path to determine resource type and ID
    // e.g., /api/v1/groves/grove-123/agents/agent-456

    parts := strings.Split(strings.Trim(path, "/"), "/")

    var resource Resource

    // Traverse path parts to build resource context
    for i := 0; i < len(parts); i++ {
        switch parts[i] {
        case "groves":
            if i+1 < len(parts) && !isAction(parts[i+1]) {
                resource = Resource{Type: "grove", ID: parts[i+1]}
                i++
            }
        case "agents":
            if i+1 < len(parts) && !isAction(parts[i+1]) {
                resource = Resource{Type: "agent", ID: parts[i+1], ParentType: "grove", ParentID: resource.ID}
                i++
            } else {
                // Listing agents
                action = ActionList
                resource.Type = "agent"
            }
        case "templates":
            if i+1 < len(parts) && !isAction(parts[i+1]) {
                resource = Resource{Type: "template", ID: parts[i+1], ParentType: resource.Type, ParentID: resource.ID}
                i++
            } else {
                action = ActionList
                resource.Type = "template"
            }
        case "groups":
            if i+1 < len(parts) && !isAction(parts[i+1]) {
                resource = Resource{Type: "group", ID: parts[i+1]}
                i++
            } else {
                action = ActionList
                resource.Type = "group"
            }
        case "policies":
            if i+1 < len(parts) && !isAction(parts[i+1]) {
                resource = Resource{Type: "policy", ID: parts[i+1]}
                i++
            } else {
                action = ActionList
                resource.Type = "policy"
            }
        case "users":
            if i+1 < len(parts) && !isAction(parts[i+1]) {
                resource = Resource{Type: "user", ID: parts[i+1]}
                i++
            }
        case "hub":
            resource = Resource{Type: "hub", ID: "hub"}
        }
    }

    // Handle action overrides from path
    if len(parts) > 0 {
        lastPart := parts[len(parts)-1]
        switch lastPart {
        case "start":
            action = ActionStart
        case "stop":
            action = ActionStop
        case "message":
            action = ActionMessage
        case "attach":
            action = ActionAttach
        case "members":
            if method == "POST" {
                action = ActionAddMember
            } else if method == "DELETE" {
                action = ActionRemoveMember
            }
        case "register":
            action = ActionRegister
        }
    }

    return resource, action
}

// isAction returns true if the path segment is an action, not a resource ID
func isAction(s string) bool {
    actions := map[string]bool{
        "start": true, "stop": true, "message": true, "attach": true,
        "members": true, "register": true, "policies": true, "evaluate": true,
    }
    return actions[s]
}
```

---

## 10. Debug Logging

### 10.1 Log Levels

| Level | Usage |
|-------|-------|
| DEBUG | Policy evaluation details, group expansion |
| INFO | Authorization decisions (allow/deny) |
| WARN | Denied access, potential security issues |
| ERROR | Authorization system failures |

### 10.2 Structured Log Fields

All authorization logs include:

```go
type AuthzLogFields struct {
    // Request context
    RequestID   string `json:"requestId"`
    UserID      string `json:"userId"`
    UserEmail   string `json:"userEmail,omitempty"`

    // Resource context
    ResourceType string `json:"resourceType"`
    ResourceID   string `json:"resourceId"`
    Action       string `json:"action"`

    // Decision
    Allowed bool   `json:"allowed"`
    Reason  string `json:"reason"`

    // Policy context
    PolicyID    string `json:"policyId,omitempty"`
    PolicyName  string `json:"policyName,omitempty"`
    PolicyScope string `json:"policyScope,omitempty"`

    // Evaluation details
    EffectiveGroups []string `json:"effectiveGroups,omitempty"`
    EvaluatedPolicies int    `json:"evaluatedPolicies,omitempty"`

    // Timing
    EvaluationMs float64 `json:"evaluationMs,omitempty"`
}
```

### 10.3 Example Log Output

```json
{
  "level": "info",
  "ts": "2025-01-24T10:30:00.123Z",
  "msg": "access decision",
  "requestId": "req-abc123",
  "userId": "user-456",
  "userEmail": "alice@example.com",
  "resourceType": "agent",
  "resourceId": "agent-789",
  "action": "delete",
  "allowed": true,
  "reason": "allow",
  "policyId": "policy-xyz",
  "policyName": "Grove Admin Policy",
  "policyScope": "grove",
  "effectiveGroups": ["platform-team", "engineering"],
  "evaluatedPolicies": 3,
  "evaluationMs": 2.5
}
```

---

## 11. Security Considerations

### 11.1 Cycle Detection in Group Hierarchy

Group membership can form cycles (A contains B contains A). The system must:

1. Detect cycles during group membership addition
2. Prevent cycles from being created
3. Handle existing cycles gracefully in resolution

```go
func (s *GroupService) AddMember(ctx context.Context, groupID, memberID, memberType string) error {
    if memberType == MemberTypeGroup {
        // Check for cycles
        if wouldCreateCycle(ctx, groupID, memberID) {
            return ErrCycleDetected
        }
    }
    // ... add member
}

// wouldCreateCycle checks if adding memberGroup to targetGroup would create a cycle
func wouldCreateCycle(ctx context.Context, targetGroupID, memberGroupID string) bool {
    // Traverse upward from targetGroup to see if we reach memberGroup
    visited := make(map[string]bool)
    return containsGroup(ctx, memberGroupID, targetGroupID, visited)
}
```

### 11.2 Policy Evaluation Performance

Group hierarchy expansion can be expensive. Mitigations:

1. **Cache effective groups** - Per-request cache of expanded groups
2. **Limit hierarchy depth** - Maximum 10 levels of group nesting
3. **Eager evaluation** - Pre-compute effective groups on login
4. **Database indexes** - Ensure proper indexes on group membership edges

### 11.3 Cascading Deletes

When entities are deleted, dependent records must be handled:

| Deleted Entity | Affected Records | Behavior |
|----------------|------------------|----------|
| User | PolicyBindings | Delete bindings for this user |
| User | GroupMembers | Remove user from all groups |
| Group | PolicyBindings | Delete bindings for this group |
| Group | GroupMembers (as member) | Remove group from parent groups |
| Group | GroupMembers (as container) | Delete all membership records |
| Policy | PolicyBindings | Delete all bindings |
| Grove | Policies (grove scope) | Delete or orphan (configurable) |

### 11.4 Audit Trail

All policy changes are logged:

```go
type PolicyAuditEvent struct {
    EventType   string    `json:"eventType"`   // created, updated, deleted, binding_added, binding_removed
    PolicyID    string    `json:"policyId"`
    PolicyName  string    `json:"policyName"`
    ChangedBy   string    `json:"changedBy"`
    ChangedAt   time.Time `json:"changedAt"`
    OldValue    *Policy   `json:"oldValue,omitempty"`
    NewValue    *Policy   `json:"newValue,omitempty"`
    AffectedPrincipal *PrincipalRef `json:"affectedPrincipal,omitempty"`
}
```

---

## 12. Alternative Approaches Considered

### 12.1 Policy Resolution: Override vs Least-Privilege

| Aspect | Override Model (Chosen) | Least-Privilege Model |
|--------|-------------------------|------------------------|
| Philosophy | Lower levels can expand access | Most restrictive wins |
| Delegation | Natural: grove owners can grant | Requires hub admin involvement |
| Risk | Lower admins might over-grant | Lower admins can only restrict |
| Complexity | Simpler mental model | Requires understanding all levels |
| Revocation | Must delete/modify lower policy | Add deny at any level |

**Decision:** Override model chosen for delegation flexibility.

> **Future Improvement: Hard Deny**
>
> A `hard_deny` effect could be added to allow Hub admins to enforce restrictions that cannot be overridden by lower-level policies. This would provide an escape hatch for critical restrictions (e.g., preventing deletion of production agents) while maintaining the delegation benefits of the override model.
>
> Implementation would add a third effect type that short-circuits the resolution algorithm before override logic applies.

### 12.2 Group Hierarchy: Ent vs Custom Implementation

| Aspect | Ent (Chosen) | Custom Implementation |
|--------|--------------|----------------------|
| Development speed | Fast: built-in graph traversal | Slow: manual SQL |
| Type safety | Strong: generated code | Weak: raw queries |
| Cycle handling | Built-in tools | Manual implementation |
| Performance | Good with proper indexes | Potentially better with optimization |
| Complexity | Learning curve | Full control |

**Decision:** Ent chosen for development speed and type safety, especially for complex group hierarchies.

### 12.3 Policy Attachment: Inline vs Referenced

| Aspect | Referenced Policies (Chosen) | Inline Policies |
|--------|------------------------------|-----------------|
| Reusability | High: same policy, multiple bindings | Low: duplicate policies |
| Management | Centralized policy management | Scattered across resources |
| Audit | Clear policy ownership | Harder to track |
| Flexibility | Update once, affects all bindings | Must update each inline |

**Decision:** Referenced policies with bindings for reusability and centralized management.

---

## 13. Implementation Phases

### Phase 1: Foundation
- [ ] Add Group and GroupMember schemas (Ent)
- [ ] Add Policy and PolicyBinding schemas (Ent)
- [ ] Implement group CRUD API endpoints
- [ ] Implement group membership API endpoints
- [ ] Add cycle detection for group hierarchy

### Phase 2: Policy Engine
- [ ] Implement policy CRUD API endpoints
- [ ] Implement policy resolution algorithm
- [ ] Implement group expansion (transitive membership)
- [ ] Add authorization middleware
- [ ] Integrate with existing handlers

### Phase 3: Built-in Policies
- [ ] Create default groups (hub-admins, hub-members)
- [ ] Create default policies
- [ ] Implement owner-based access check
- [ ] Add hub initialization with default policies

### Phase 4: Debug & Audit
- [ ] Implement structured authorization logging
- [ ] Add policy evaluation endpoint (debug)
- [ ] Add audit trail for policy changes
- [ ] Add authorization metrics

### Phase 5: UI Integration
- [ ] Add permissions management to web dashboard
- [ ] Add group management UI
- [ ] Add policy management UI
- [ ] Add access visualization

---

## 14. Open Questions

This section documents design decisions that require further input or discussion before implementation.

### 14.1 Meta-Permissions: Who Can Manage Policies?

**Question:** What permissions are required to create, modify, or delete policies at different scope levels?

**Current Assumption:** The `manage` action on a scope grants policy management. However, this conflates administrative operations with permission management.

**Options:**
1. **`manage` includes policy management** (current) - Simple, but couples unrelated capabilities
2. **Separate `manage_policies` action** - More granular, but adds complexity
3. **Policy ownership model** - Policy creator can modify; scope `manage` can delete

**Impact:** Determines who can grant themselves elevated access within a scope.

### 14.2 Default Access for New Resources

**Question:** When a new resource is created (e.g., agent, grove), what access do other users have by default?

**Current Assumption:** Default deny - only the owner has access until policies are created.

**Options:**
1. **Strict default deny** - Owner only; explicit policies required for others
2. **Inherit from scope** - New agents inherit grove-level policies automatically
3. **Configurable default** - Grove settings define default visibility

**Impact:** Affects onboarding experience and security posture.

### 14.3 Template Access Across Groves

**Question:** Can a grove-scoped template be used by other groves, or is it strictly contained?

**Current Assumption:** Grove-scoped templates are only usable within that grove.

**Options:**
1. **Strict containment** - Grove templates only usable in that grove
2. **Visibility-based sharing** - `visibility: public` templates usable anywhere
3. **Explicit sharing** - Grove templates can be "shared" to other groves via policy

**Impact:** Affects template reuse patterns and permission complexity.

### 14.4 Viewer Role Capabilities

**Question:** What exactly can a `viewer` role user do beyond reading resources?

**Areas needing clarification:**
- Can viewers create agents? (Current: no)
- Can viewers message/attach to agents they can see? (Current: unclear)
- Can viewers be granted elevated access via policies? (Current: yes, but is this intended?)

**Impact:** Determines the minimum capability level for read-only users.

### 14.5 Runtime Broker Permissions Integration

**Question:** How do Runtime Broker permissions interact with user permissions?

**Scenario:** A user has `delete` permission on an agent, but the Runtime Broker managing that agent does not permit Hub-initiated deletes (per broker policy).

**Options:**
1. **User permissions take precedence** - If user can delete, operation proceeds (broker must comply)
2. **Intersection model** - Both user AND broker must permit the operation
3. **Broker as override** - Broker restrictions cannot be bypassed by user permissions

**Impact:** Critical for understanding effective permissions in distributed scenarios.

### 14.6 Group Deletion with Active Policies

**Question:** What happens when a group is deleted that is bound to active policies?

**Current Assumption:** Returns error unless `cascade=true` is specified.

**Options:**
1. **Block deletion** - Error until all bindings are removed manually
2. **Cascade delete bindings** - Remove bindings, keep policies (now grant no one)
3. **Cascade delete policies** - Remove policies that would become empty
4. **Soft delete** - Mark group as deleted, preserve bindings for audit

**Impact:** Affects operational safety and audit trail preservation.

### 14.7 Cross-Group Policy Inheritance

**Question:** If Group A is a member of Group B, and Group B has a policy binding, does Group A automatically inherit that binding?

**Current Assumption:** No - group membership for policy purposes is only evaluated at the user level (users in Group A get access if Group A is bound).

**Alternative interpretation:** Group-to-group membership might imply the contained group also gets the policy.

**Impact:** Affects how administrators think about policy propagation.

### 14.8 Time-Based Condition Timezone Handling

**Question:** What timezone is used for `validFrom` and `validUntil` condition evaluation?

**Options:**
1. **UTC only** - All times are UTC, client converts for display
2. **User timezone** - Evaluate based on user's configured timezone
3. **Resource timezone** - Groves/agents could have timezone settings

**Impact:** Affects predictability of time-based access windows.

### 14.9 Emergency Access Revocation

**Question:** How can an administrator immediately revoke a user's access in an emergency?

**Current options:**
1. Set `User.status = "suspended"` - Blocks authentication
2. Remove from all groups - Slow, may miss direct bindings
3. Add explicit deny policies - Complex, may be overridden

**Need:** A fast, atomic, un-overrideable way to revoke access.

### 14.10 Policy Versioning and Rollback

**Question:** Should policies support versioning for rollback purposes?

**Current Assumption:** No versioning - changes are immediate and permanent (with audit trail).

**Options:**
1. **No versioning** (current) - Simple, rely on audit trail for forensics
2. **Immutable versions** - Each change creates new version; can rollback
3. **Draft/Published states** - Policies can be drafted and tested before activation

**Impact:** Affects operational safety and change management workflows.

---

## 15. References

- **Hosted Architecture:** `hosted-architecture.md`
- **Hub API Specification:** `hub-api.md`
- **Development Authentication:** `dev-auth.md`
- **Authentication Overview:** `auth-overview.md`
- **Ent Documentation:** https://entgo.io/

# Access Control MVP: Env Vars & Secrets Authorization

## Status
**Proposed**

## 1. Overview

This document specifies the minimum viable access control enforcement for the Hub's environment variable and secrets API endpoints. These endpoints currently have **no authorization checks** — any authenticated user can read, write, and delete any other user's env vars and secrets, as well as those belonging to any grove or runtime broker.

The access control infrastructure already exists in the Hub:
- `AuthzService.CheckAccess()` with full policy evaluation (`pkg/hub/authz.go`)
- `UserIdentity` and `AgentIdentity` extraction from request context (`pkg/hub/identity.go`)
- RBAC policy engine with scope hierarchy, group expansion, and owner bypass
- Admin bypass, owner bypass, and default-deny semantics

This infrastructure is actively used for agent operations (create, delete, attach) but is completely absent from all env var and secrets handlers. This design applies the same patterns to close the authorization gap.

### 1.1 Goals

1. **User scope isolation** — A user's env vars and secrets are private to that user by default.
2. **Correct identity resolution** — Replace the hardcoded `"default"` scopeID placeholder with the authenticated user's actual ID.
3. **Grove/broker scope authorization** — Users can only access env vars and secrets in groves they have access to.
4. **Consistent patterns** — Use the same `CheckAccess()` patterns established in existing agent handlers.
5. **Agent access** — Agents can read (but not write) env vars and secrets within their grove scope.

### 1.2 Non-Goals

- New resource types or actions in the RBAC policy model (env vars and secrets use existing types).
- A dedicated "env_var" or "secret" resource type in the policy engine (scope-level authorization is sufficient for MVP).
- UI for managing access policies (admin UI exists; policy management is a separate milestone).
- Audit logging of env var / secret access events (future enhancement).
- Cross-scope access grants (e.g., sharing a user-scoped secret with another user via policy).

### 1.3 Proving Scope

The env vars and secrets endpoints serve as the initial proving ground for systematically applying authorization to Hub API endpoints. The patterns established here will be replicated across remaining unprotected endpoints.

---

## 2. Current State (Vulnerabilities)

### 2.1 Hardcoded User ID

All 8 user-scoped handlers use a hardcoded `"default"` placeholder instead of the authenticated user's ID:

```go
// handlers.go:3642 (and 7 other locations)
if scope == store.ScopeUser && scopeID == "" {
    scopeID = "default" // TODO: Get from auth context
}
```

**Impact**: All users share the same namespace. User A can see User B's env vars and secrets.

### 2.2 No Authorization Checks

None of the 20 env var / secrets handler functions (across hub, grove, and broker scopes) call `CheckAccess()`:

| Scope | Env Var Handlers | Secret Handlers | Total |
|-------|-----------------|-----------------|-------|
| Hub (user scope) | `listEnvVars`, `getEnvVar`, `setEnvVar`, `deleteEnvVar` | `listSecrets`, `getSecret`, `setSecret`, `deleteSecret` | 8 |
| Grove scope | `handleGroveEnvVars`, `handleGroveEnvVarByKey` | `handleGroveSecrets`, `handleGroveSecretByKey` | 4 |
| Broker scope | `handleBrokerEnvVars`, `handleBrokerEnvVarByKey` | `handleBrokerSecrets`, `handleBrokerSecretByKey` | 4 |
| **Total** | | | **16 handler functions, ~20 action paths** |

### 2.3 Client-Controlled Scope Override

The hub-level handlers accept `scope` and `scopeId` as client-supplied parameters without validation. A request to `PUT /api/v1/env/KEY` with `{"scope": "grove", "scopeId": "admin-grove-123"}` will succeed regardless of the caller's access to that grove.

### 2.4 Existence-Only Checks on Grove/Broker Scope

The grove and broker-scoped handlers verify that the grove/broker exists but never check whether the authenticated user has access to it:

```go
// handlers.go:4043 — grove env vars
_, err := s.store.GetGrove(ctx, groveID)
if err != nil {
    if err == store.ErrNotFound { NotFound(w, "Grove"); return }
    writeErrorFromErr(w, err, ""); return
}
// Proceeds directly to data access — no authorization check
```

---

## 3. Authorization Model

### 3.1 Scope-Based Authorization

Rather than introducing new resource types (`envVar`, `secret`) into the policy engine, this MVP uses **scope-level authorization**: access to env vars and secrets is derived from the caller's access to the containing scope (user, grove, or broker).

| Scope | Authorization Rule |
|-------|-------------------|
| **User** | Only the authenticated user can access their own env vars/secrets. Enforced by setting `scopeId = user.ID()` server-side. |
| **Grove** | Requires `read` or `update` action on the grove resource. Reuses existing grove access policies. |
| **Broker** | Requires `read` or `update` action on the broker resource, or broker's own HMAC identity. |

**Rationale**: Env vars and secrets are configuration attached to a scope, not independent resources. A user who can manage a grove should be able to manage that grove's env vars. This avoids policy proliferation and matches user mental models.

### 3.2 Action Mapping

| HTTP Method | Env Var / Secret Operation | Required Action on Scope |
|-------------|---------------------------|--------------------------|
| `GET` (list) | List env vars / secrets | `read` |
| `GET` (by key) | Get single env var / secret metadata | `read` |
| `PUT` | Create or update | `update` |
| `DELETE` | Delete | `update` |

**Note**: `PUT` and `DELETE` both require `update` on the scope (not `create`/`delete`) because the env var or secret is a property of the scope, not an independent resource. This matches the mental model: "updating a grove's configuration."

### 3.3 User Scope: Identity Enforcement

For user-scoped operations, the server **must** derive the scopeId from the authenticated identity. The client cannot specify another user's scopeId.

```
Current (BROKEN):
  Client sends: GET /api/v1/env?scope=user&scopeId=other-user-123
  Handler uses: scopeID = "other-user-123"    ← client-controlled

Fixed:
  Client sends: GET /api/v1/env?scope=user
  Handler uses: scopeID = user.ID()           ← server-enforced

  Client sends: GET /api/v1/env?scope=user&scopeId=other-user-123
  Handler uses: scopeID = user.ID()           ← client value IGNORED for user scope
```

This is the most critical fix. User-scoped env vars and secrets are private by design; the server enforces this by overriding any client-supplied scopeId.

### 3.4 Grove Scope: Policy-Based Access

Grove-scoped access uses the existing `CheckAccess()` with the grove resource:

```go
decision := s.authzService.CheckAccess(ctx, userIdent, Resource{
    Type: "grove",
    ID:   groveID,
}, ActionRead) // or ActionUpdate for PUT/DELETE
```

This reuses grove-level policies already managed by grove owners. A user with `read` on a grove can list its env vars; a user with `update` can modify them.

### 3.5 Broker Scope: Policy-Based Access + Broker Self-Access

Runtime broker-scoped operations require either:
1. **User with broker access**: `CheckAccess()` on the broker resource with `read` or `update` action.
2. **Broker's own identity**: A request authenticated via broker HMAC (`GetBrokerIdentityFromContext()`) accessing its own env vars/secrets.

### 3.6 Agent Access

Agents access env vars and secrets through the resolution/gather flow at provisioning time (see `secrets.md` § 3.2 and `env-gather.md`). For direct API access:

- Agents can **read** env vars and secrets in their grove scope (they have implicit `read` on their grove).
- Agents **cannot** write, update, or delete env vars or secrets via the API.
- User-scoped env vars/secrets are resolved at provisioning time and injected; agents do not access the user-scope API directly.

### 3.7 Admin Bypass

Hub admins (`User.role == "admin"`) bypass all authorization checks, consistent with the existing `checkAccessForUser()` behavior in `authz.go:108-113`. Admins can access any scope's env vars and secrets.

---

## 4. Implementation

### 4.1 Helper Function

Introduce a reusable helper to extract identity and resolve scope authorization, reducing repetition across all 16+ handler functions:

```go
// resolveEnvSecretAccess validates the caller's identity and authorizes access
// to env vars or secrets at the given scope. For user scope, it enforces that
// scopeId matches the authenticated user. For grove/broker scope, it checks
// policy-based access.
//
// Returns the validated scopeId and true if authorized, or writes an HTTP
// error response and returns false.
func (s *HubServer) resolveEnvSecretAccess(
    w http.ResponseWriter,
    r *http.Request,
    scope string,
    clientScopeID string,
    requireWrite bool,
) (scopeID string, ok bool) {
    ctx := r.Context()

    // Determine the required action
    action := ActionRead
    if requireWrite {
        action = ActionUpdate
    }

    // --- User Scope ---
    if scope == store.ScopeUser {
        userIdent := GetUserIdentityFromContext(ctx)
        if userIdent == nil {
            writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized,
                "Authentication required", nil)
            return "", false
        }
        // Enforce: user scope always uses the authenticated user's ID
        return userIdent.ID(), true
    }

    // --- Grove Scope ---
    if scope == store.ScopeGrove {
        if clientScopeID == "" {
            writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
                "scopeId is required for grove scope", nil)
            return "", false
        }

        // Verify grove exists
        grove, err := s.store.GetGrove(ctx, clientScopeID)
        if err != nil {
            if err == store.ErrNotFound {
                NotFound(w, "Grove")
            } else {
                writeErrorFromErr(w, err, "")
            }
            return "", false
        }

        // Check user authorization on the grove
        userIdent := GetUserIdentityFromContext(ctx)
        if userIdent != nil {
            decision := s.authzService.CheckAccess(ctx, userIdent, Resource{
                Type:    "grove",
                ID:      grove.ID,
                OwnerID: grove.OwnerID,
            }, action)
            if !decision.Allowed {
                writeError(w, http.StatusForbidden, ErrCodeForbidden,
                    "You don't have permission to access this grove's configuration", nil)
                return "", false
            }
            return clientScopeID, true
        }

        // Check agent authorization (read-only)
        agentIdent := GetAgentIdentityFromContext(ctx)
        if agentIdent != nil {
            if requireWrite {
                writeError(w, http.StatusForbidden, ErrCodeForbidden,
                    "Agents cannot modify grove configuration", nil)
                return "", false
            }
            if agentIdent.GroveID() != clientScopeID {
                writeError(w, http.StatusForbidden, ErrCodeForbidden,
                    "Agent can only access its own grove's configuration", nil)
                return "", false
            }
            return clientScopeID, true
        }

        writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized,
            "Authentication required", nil)
        return "", false
    }

    // --- Runtime Broker Scope ---
    if scope == store.ScopeRuntimeBroker {
        if clientScopeID == "" {
            writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
                "scopeId is required for runtime_broker scope", nil)
            return "", false
        }

        // Verify broker exists
        broker, err := s.store.GetBroker(ctx, clientScopeID)
        if err != nil {
            if err == store.ErrNotFound {
                NotFound(w, "Runtime Broker")
            } else {
                writeErrorFromErr(w, err, "")
            }
            return "", false
        }

        // Broker self-access (via HMAC auth)
        if brokerIdent := GetBrokerIdentityFromContext(ctx); brokerIdent != nil {
            if brokerIdent.ID() == broker.ID {
                return clientScopeID, true
            }
        }

        // User access via policy
        userIdent := GetUserIdentityFromContext(ctx)
        if userIdent != nil {
            decision := s.authzService.CheckAccess(ctx, userIdent, Resource{
                Type: "runtime_broker",
                ID:   broker.ID,
            }, action)
            if !decision.Allowed {
                writeError(w, http.StatusForbidden, ErrCodeForbidden,
                    "You don't have permission to access this broker's configuration", nil)
                return "", false
            }
            return clientScopeID, true
        }

        writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized,
            "Authentication required", nil)
        return "", false
    }

    writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
        "Invalid scope: must be user, grove, or runtime_broker", nil)
    return "", false
}
```

### 4.2 Hub-Level Handler Changes

Each of the 8 hub-level handlers replaces the TODO-marked scope resolution block with a call to the helper:

**Before** (`listEnvVars`, line 3634):
```go
scope := query.Get("scope")
if scope == "" {
    scope = store.ScopeUser
}
scopeID := query.Get("scopeId")
if scope == store.ScopeUser && scopeID == "" {
    scopeID = "default" // TODO: Get from auth context
}
```

**After**:
```go
scope := query.Get("scope")
if scope == "" {
    scope = store.ScopeUser
}
isWrite := r.Method == http.MethodPut || r.Method == http.MethodDelete
scopeID, ok := s.resolveEnvSecretAccess(w, r, scope, query.Get("scopeId"), isWrite)
if !ok {
    return // error response already written
}
```

The same pattern applies to all 8 handlers: `listEnvVars`, `getEnvVar`, `setEnvVar`, `deleteEnvVar`, `listSecrets`, `getSecret`, `setSecret`, `deleteSecret`.

For `setEnvVar` and `setSecret` (which read scope from the request body), the scope override from the body must be validated through the helper before the upsert proceeds.

### 4.3 Grove-Scoped Handler Changes

The 4 grove-scoped handlers already extract `groveID` from the URL path and verify the grove exists. The change adds a `CheckAccess()` call after the existence check:

**Before** (`handleGroveEnvVars`, line 4043):
```go
_, err := s.store.GetGrove(ctx, groveID)
if err != nil {
    if err == store.ErrNotFound { NotFound(w, "Grove"); return }
    writeErrorFromErr(w, err, ""); return
}
// Proceeds to list/get/set/delete without authorization
```

**After**:
```go
grove, err := s.store.GetGrove(ctx, groveID)
if err != nil {
    if err == store.ErrNotFound { NotFound(w, "Grove"); return }
    writeErrorFromErr(w, err, ""); return
}

isWrite := r.Method == http.MethodPut || r.Method == http.MethodDelete
action := ActionRead
if isWrite { action = ActionUpdate }

identity := GetIdentityFromContext(ctx)
if identity == nil {
    writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized,
        "Authentication required", nil)
    return
}

// Agents: read-only access to own grove
if agentIdent, ok := identity.(AgentIdentity); ok {
    if isWrite {
        writeError(w, http.StatusForbidden, ErrCodeForbidden,
            "Agents cannot modify grove configuration", nil)
        return
    }
    if agentIdent.GroveID() != groveID {
        writeError(w, http.StatusForbidden, ErrCodeForbidden,
            "Agent can only access its own grove", nil)
        return
    }
} else {
    decision := s.authzService.CheckAccess(ctx, identity, Resource{
        Type:    "grove",
        ID:      grove.ID,
        OwnerID: grove.OwnerID,
    }, action)
    if !decision.Allowed {
        writeError(w, http.StatusForbidden, ErrCodeForbidden,
            "You don't have permission to access this grove's configuration", nil)
        return
    }
}
```

### 4.4 Broker-Scoped Handler Changes

Similar to grove-scoped, adding authorization after the broker existence check. Broker self-access (via HMAC) is allowed; user access requires policy-based authorization.

### 4.5 SetEnvVar/SetSecret: Scope Override Prevention

The `setEnvVar` and `setSecret` handlers accept scope in the request body. For user scope, the server must override the client-supplied scopeId:

```go
// In setEnvVar, after reading the request body:
scope := req.Scope
if scope == "" {
    scope = store.ScopeUser
}

scopeID, ok := s.resolveEnvSecretAccess(w, r, scope, req.ScopeID, true)
if !ok {
    return
}

// Use server-resolved scopeID, not req.ScopeID
envVar := &store.EnvVar{
    Scope:   scope,
    ScopeID: scopeID,  // ← from resolveEnvSecretAccess, not from client
    // ...
}
```

### 4.6 CreatedBy / UpdatedBy Tracking

With identity now available, set the audit fields:

```go
userIdent := GetUserIdentityFromContext(ctx)
if userIdent != nil {
    envVar.CreatedBy = userIdent.ID()
}
```

These fields already exist in the store models but are never populated because the user ID wasn't being extracted.

---

## 5. Hub-Level Scope Override Protection

A key security property: the hub-level endpoints (`/api/v1/env`, `/api/v1/secrets`) must not allow a user to use them as a backdoor to access grove or broker scope without authorization.

Currently, a user can call `GET /api/v1/env?scope=grove&scopeId=some-grove-id` and bypass the grove-scoped handler's existence check entirely. After this change, the `resolveEnvSecretAccess` helper applies the same authorization rules regardless of which endpoint is used.

| Endpoint | Scope | Authorization |
|----------|-------|---------------|
| `GET /api/v1/env?scope=user` | user | Identity enforcement (scopeId = user.ID()) |
| `GET /api/v1/env?scope=grove&scopeId=X` | grove | CheckAccess on grove X |
| `GET /api/v1/groves/X/env` | grove | CheckAccess on grove X |

Both paths to grove-scoped env vars go through the same authorization check.

---

## 6. Error Responses

Authorization failures use the existing error response patterns:

| Condition | Status | Error Code | Message |
|-----------|--------|------------|---------|
| No authentication | 401 | `unauthorized` | "Authentication required" |
| User scope, not the owner | N/A | N/A | Cannot happen — server forces scopeId to authenticated user |
| Grove scope, no access | 403 | `forbidden` | "You don't have permission to access this grove's configuration" |
| Broker scope, no access | 403 | `forbidden` | "You don't have permission to access this broker's configuration" |
| Agent trying to write | 403 | `forbidden` | "Agents cannot modify grove configuration" |
| Agent accessing other grove | 403 | `forbidden` | "Agent can only access its own grove's configuration" |
| Invalid scope value | 400 | `bad_request` | "Invalid scope: must be user, grove, or runtime_broker" |
| Missing scopeId for grove/broker | 400 | `bad_request` | "scopeId is required for {scope} scope" |

---

## 7. Frontend Impact

The profile/settings frontend pages (`profile-env-vars.ts`, `profile-secrets.ts`) already send requests correctly for this model:
- They use `scope=user` without a `scopeId`, relying on the server to resolve it.
- No frontend changes are needed for the access control fix.

Once the backend enforces `scopeId = user.ID()` for user scope, each user will see only their own env vars and secrets.

---

## 8. Testing Strategy

### 8.1 Unit Tests

Add tests to the hub handler test suite covering:

| Test Case | Expected |
|-----------|----------|
| User lists own env vars (no scopeId) | 200, returns only their vars |
| User lists own env vars (own scopeId) | 200, scopeId overridden to user.ID() |
| User supplies another user's scopeId | 200, scopeId overridden to user.ID() (other user's ID ignored) |
| User creates env var (user scope) | 200, scopeId = user.ID(), createdBy = user.ID() |
| User accesses grove env vars (has policy) | 200 |
| User accesses grove env vars (no policy) | 403 |
| User accesses grove env vars (is grove owner) | 200 (owner bypass) |
| Agent reads grove env vars (own grove) | 200 |
| Agent reads grove env vars (other grove) | 403 |
| Agent writes grove env vars | 403 |
| Admin accesses any scope | 200 (admin bypass) |
| Unauthenticated request | 401 |

Mirror the above for secrets endpoints.

### 8.2 Integration Tests

- Create two users, verify isolation: User A creates env var, User B cannot see it.
- Create grove with owner, verify non-member gets 403 on grove env vars.
- Create agent in grove, verify agent can read but not write grove env vars.
- Verify admin can access all scopes.

---

## 9. Migration

### 9.1 Existing Data

Any env vars or secrets currently stored with `scopeId = "default"` will become inaccessible after this change, because no user will have `ID() == "default"`. Options:

| Approach | Pros | Cons |
|----------|------|------|
| **Migration script** | Clean: reassign to actual user IDs | Requires knowing which user owns each record; ambiguous if multiple users shared the "default" namespace |
| **Admin-only access** | Admins can still see/manage "default" records | Non-admin users lose access to their data |
| **Delete on deploy** | Clean slate, simplest | Data loss (acceptable for alpha) |

**Recommendation**: Since the project is in alpha and the "default" namespace is a known bug (not a feature), **delete orphaned records** with `scopeId = "default"` as part of the deployment. Document the breaking change in release notes. Any real user data was already shared across all users due to the bug, so there is no meaningful ownership to preserve.

### 9.2 Rollout

1. Deploy the authorization changes.
2. Run cleanup: `DELETE FROM env_vars WHERE scope = 'user' AND scope_id = 'default'` (and equivalent for secrets).
3. Users re-create their env vars/secrets, which will now be properly scoped to their user ID.

---

## 10. Scope of Changes

### 10.1 Files Modified

| File | Changes |
|------|---------|
| `pkg/hub/handlers.go` | Add `resolveEnvSecretAccess` helper. Update all 16 env var and secrets handler functions to use it. Populate `createdBy`/`updatedBy` fields. |

### 10.2 Files Not Modified

| File | Reason |
|------|--------|
| `pkg/hub/authz.go` | No changes needed — existing `CheckAccess()` is sufficient. |
| `pkg/hub/auth.go` | No changes needed — identity extraction already works. |
| `pkg/store/` | No schema changes — `createdBy`/`updatedBy` fields already exist. |
| `web/src/` | No frontend changes — already sends `scope=user` without `scopeId`. |

### 10.3 Estimated Scope

- 1 new helper function (~120 lines)
- 16 handler functions updated (each ~5-10 line change replacing the scope resolution block)
- Test additions for authorization scenarios

---

## 11. Future Work

This MVP establishes the authorization pattern. Future iterations can build on it:

1. **Fine-grained resource policies** — Introduce `envVar` and `secret` as resource types in the policy engine, allowing per-key access control (e.g., "User X can read `DATABASE_URL` but not `ADMIN_KEY`").
2. **Cross-user sharing** — Allow a user to share specific env vars/secrets with another user or group via policy bindings.
3. **Audit logging** — Log all env var and secret access events (read, write, delete) with caller identity and scope.
4. **Secret access events via SSE** — Real-time notifications when secrets are accessed or modified.
5. **Apply patterns to remaining endpoints** — Use the same authorization helper pattern for template management, broker configuration, and other unprotected endpoints.

---

## 12. References

- [Permissions Design](auth/permissions-design.md) — RBAC policy model, resolution algorithm, data models
- [Secrets Management](secrets.md) — Secret types, storage interface, resolution semantics
- [Auth Milestones](auth/auth-milestones.md) — Authentication implementation status
- [Environment Gather](env-gather.md) — CLI fallback for agent env var resolution
- `pkg/hub/authz.go` — AuthzService implementation
- `pkg/hub/handlers.go:3600-4700` — Env var and secrets handlers (current state)

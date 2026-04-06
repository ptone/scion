# Code Review: Agent Progeny Secret Access

**Reviewed:** 2026-04-05
**Scope:** Working tree changes implementing the progeny secret access feature (~876 lines, 29 files)

## Summary

This changeset implements opt-in secret sharing with agent progeny (sub-agents and their descendants). It spans the full stack: data model, storage, backend resolution, authorization, API handlers, CLI, and web UI. The design is sound and follows existing codebase patterns well.

| Severity | Count |
|----------|-------|
| Critical | 1     |
| Moderate | 3     |
| Minor    | 7     |

---

## Critical

### 1. Doc comment contradicts implementation (`ResolveOpts.AuthzCheck`)

**File:** `pkg/secret/secret.go` (~line 102)

The doc comment states:

> `AuthzCheck` -- If nil, progeny secrets are **not** included.

The actual implementation in both `localbackend.go` and `gcpbackend.go`:

```go
if opts.AuthzCheck != nil && !opts.AuthzCheck(*meta) {
    continue
}
```

When `AuthzCheck` is nil, progeny secrets **are** included with no policy gating. The test `TestLocalBackend_ResolveProgeny_NilAuthzCheckIncludesAll` confirms this is the intended behavior.

**Recommendation:** Fix the comment to: *"If nil, all ancestry-matching progeny secrets are included without policy verification."*

**Security note:** In the dispatcher (`httpdispatcher.go:1213`), `AuthzCheck` is only set when `d.authzService != nil`. If the authz service is absent (e.g. dev-auth mode), progeny secrets bypass policy checks entirely. This appears intentional but should be explicitly documented.

---

## Moderate

### 2. `defer` on policy cleanup during delete

**File:** `pkg/hub/handlers.go:6485-6488`

```go
if meta, err := s.secretBackend.GetMeta(ctx, key, scope, scopeID); err == nil && meta.AllowProgeny {
    defer s.deleteProgenyPolicy(ctx, meta.ID)
}
```

The `defer` causes policy cleanup to run after the HTTP response is already written. If policy deletion fails, the client has already been told the delete succeeded. A direct (non-deferred) call after the secret delete succeeds would be clearer and easier to reason about:

```go
// After successful secret deletion and before writing response:
if meta != nil && meta.AllowProgeny {
    s.deleteProgenyPolicy(ctx, meta.ID)
}
```

### 3. Parameter order mismatch between interface and service

The `AgentTokenGenerator` interface (`httpdispatcher.go`):

```go
GenerateAgentToken(agentID, groveID string, ancestry []string, additionalScopes ...AgentTokenScope) (string, error)
```

The underlying `AgentTokenService` method (`agenttoken.go`):

```go
GenerateAgentToken(agentID, groveID string, scopes []AgentTokenScope, ancestry []string) (string, error)
```

The `ancestry` and `scopes` parameters are swapped. This isn't a bug because `Server.GenerateAgentToken` acts as an adapter, but it's a maintenance hazard. If someone later tries to make `AgentTokenService` satisfy the interface directly, they'll get a subtle bug.

**Recommendation:** Align the parameter order across both signatures.

### 4. Local dev config in `.scion/settings.yaml`

The diff changes the hub endpoint from `localhost:8080` to `https://gteam.projects.scion-ai.dev` and adds a specific `grove_id`. This appears to be local environment configuration and should not be committed with the feature.

---

## Minor

### 5. Duplicated secret-construction code in backends

The progeny resolution blocks in both `localbackend.go` and `gcpbackend.go` duplicate ~30 lines of `SecretMeta` construction that are nearly identical to the existing resolve loop above. A small helper like `buildSecretWithValue(s store.Secret, value, ref string) SecretWithValue` would reduce field-level drift risk. The existing block already needed fields added in this changeset (`AllowProgeny`, `Version`, `Created`, etc.).

### 6. No test for `ensureProgenyPolicy` idempotency

`ensureProgenyPolicy` checks for an existing policy by name before creating. There is no test that verifies calling it twice with the same secret does not create duplicate policies.

### 7. No test for policy cleanup on secret delete

There is no test verifying that `deleteSecret` triggers `deleteProgenyPolicy`. An explicit test that creates a progeny secret, deletes it, and asserts the policy was removed would be valuable.

### 8. No test for ancestry in token claims round-trip

No test generates a token with ancestry, validates it, and asserts the claims contain the expected ancestry chain.

### 9. GCP backend progeny resolution is untested

All progeny resolution tests target `LocalBackend`. The `GCPBackend` has a nearly identical progeny block but no dedicated test coverage.

### 10. Ancestry iteration in authz is O(n) per policy

**File:** `pkg/hub/authz.go:278-285`

```go
for _, ancestorID := range ancestry {
    if policy.Conditions.DelegatedFrom.PrincipalID == ancestorID {
        allowed = true
        break
    }
}
```

For typical ancestry depths (3-5), this is fine. If ancestry could grow large in the future, a set lookup would be better. Not a concern now.

### 11. CLI `--allow-progeny` flag help could be clearer

The flag help says `"Allow creator's progeny agents to access this secret (user scope only)"`. The server validates this and returns a clear error for non-user scopes. The CLI could additionally mark the flag as only applicable to user scope in its usage output, but the current behavior (server-side validation) is sufficient.

---

## Test Coverage Assessment

The test suite for progeny access is thorough and covers:

- Basic progeny access (ancestry match)
- Deep ancestry (great-grandchild)
- Scope precedence (grove overrides progeny)
- Denied when `allowProgeny=false`
- Denied when ancestry doesn't match
- Denied by policy check
- Nil `AuthzCheck` behavior
- Nil opts (backward compatibility)
- `AllowProgeny` persistence round-trip

### Recommended additional tests

- Policy lifecycle: creation and deletion via `ensureProgenyPolicy` / `deleteProgenyPolicy`
- Token claims round-trip: generate with ancestry, validate, assert claims
- Integration-level: agent dispatched with progeny secrets in its environment
- GCP backend progeny resolution

---

## Recommendations

1. **Fix the `AuthzCheck` doc comment** (critical, pre-merge).
2. **Exclude `.scion/settings.yaml`** from the commit.
3. **Replace `defer` with direct call** in secret delete handler for clarity.
4. **Align parameter order** between `AgentTokenGenerator` interface and `AgentTokenService`.
5. **Extract shared secret-construction helper** in backends to reduce duplication.
6. **Add tests** for policy lifecycle, token ancestry round-trip, and GCP backend progeny resolution as follow-ups.

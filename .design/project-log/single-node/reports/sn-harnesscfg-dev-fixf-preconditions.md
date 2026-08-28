# Fix F Precondition Report — Precondition 2 Fails

Author: sn-harnesscfg-dev. Date: 2026-08-28.
Tasks: #37, #48. Brief: `sn-harnesscfg-dev-round2.md`.

**Precondition 2 is not satisfied. F as specified is dead. The salvage path is small.**

---

## The three preconditions, answered individually

### Precondition 1: Is `conn.LocalStorage != nil` in single-node hosted mode?

**SATISFIED.**

`conn.LocalStorage` is set at `pkg/runtimebroker/server.go:472-473`:

```go
if stor := s.config.ColocatedStorage; stor != nil && stor.Provider() == storage.ProviderLocal {
    conn.LocalStorage = stor
}
```

`ColocatedStorage` is the hub's own storage (`cmd/server_foreground.go:2481`), and in single-node mode
(no GCS bucket), `initHubStorage` (`cmd/server_foreground.go:2078-2132`) always returns a
`ProviderLocal` backend. The guard passes. `conn.LocalStorage` is non-nil.

### Precondition 2: Does `resolveLocalResource(ResourceKindHarnessConfig, "antigravity", conn)` find the store-seeded resource by name?

**NOT SATISFIED.**

The call chain:

1. `resolveLocalResource(ctx, ResourceKindHarnessConfig, "antigravity", conn)` (handlers.go:1042)
2. → `resourceObjectPath(ctx, ResourceKindHarnessConfig, "antigravity", conn)` (handlers.go:1068)
3. → `conn.HubClient.HarnessConfigs().Get(ctx, "antigravity")` (handlers.go:1071)
4. → HTTP GET `/api/v1/harness-configs/antigravity` (hubclient/harness_configs.go:210)
5. → Hub handler `getHarnessConfig(w, r, "antigravity")` (harness_config_handlers.go:396)
6. → `s.store.GetHarnessConfig(ctx, "antigravity")` (harness_config_handlers.go:402)
7. → `parseGetID("antigravity")` (entadapter/group_store.go:59)
8. → `uuid.Parse("antigravity")` fails → returns `store.ErrNotFound`

`parseGetID` is a UUID-only parser:

```go
func parseGetID(s string) (uuid.UUID, error) {
    uid, err := uuid.Parse(s)
    if err != nil {
        return uuid.Nil, store.ErrNotFound
    }
    return uid, nil
}
```

The `ErrNotFound` propagates back:
- `getHarnessConfig` → `writeErrorFromErr(w, err, "")` → HTTP 404
- API client → `APIError{StatusCode: 404, Code: "not_found"}`
- `resourceObjectPath` → `wrapResourceMetaErr(err, "harness-config")` → error
- `resolveLocalResource` → returns error (handlers.go:1050)
- `hydrateHarnessConfig` → returns error (handlers.go:1005)
- `buildStartContext` → returns 500 (start_context.go:499-506)

**The slug lookup doesn't silently miss — it causes a hard 500 error.**

### Precondition 3: Does row 4 stay put under F?

**Cannot be measured — precondition 2 blocks implementation.**

Reasoning (not measurement): Fix F does not touch the 7-rung ladder (`ResolveHarnessConfigName`).
It does not add a rung, change a name, or alter a rank. It only changes where an already-resolved
name is looked up (store vs disk). The profile at rung 6 still beats settings at rung 7. Row 4 is
unchanged.

The architect asked for measurement, not reasoning. I cannot measure what I cannot implement.

---

## The triple guard — what it is for

The ID/hash check appears at three sites:

| Site | Code | Purpose |
|------|------|---------|
| handlers.go:510 | `if req.Config != nil && (req.Config.HarnessConfigID != "" \|\| req.Config.HarnessConfigHash != "")` | **Optimization.** Caller-side gate in env-gather path. Skips hydration (and its 10-second timeout) when there is nothing to hydrate. Graceful degradation: if hydration fails, falls back to on-disk. |
| handlers.go:993-994 | `if cfg == nil \|\| (cfg.HarnessConfigID == "" && cfg.HarnessConfigHash == "")` | **Defensive.** Function's own guard. Returns early when the function has no identifier to work with. |
| handlers.go:1019-1024 | `if cfg.HarnessConfigHash != "" && cfg.HarnessConfigID != "" { ... } if cfg.HarnessConfigID != "" { ... }` | **Algorithmic.** Resolver cascade: strongest resolution (hash+ID) first, then ID-only. Falls through to empty return when neither is present. |

**None are security boundaries.** No trust boundary blocks Fix F. The guards are all correctness and
efficiency — they prevent wasteful or undefined operations when there is no identifier, not
untrusted-name attacks. The `resolveLocalResource` path uses the same `conn.HubClient` the broker
already trusts for template resolution. A name resolved by the broker's own ladder has the same trust
provenance as the template name resolved by the same ladder in the same flow.

---

## What in the brief is wrong — the third error

The brief says (lines 45-46):

> "The broker can already resolve a harness-config by NAME from the local storage backend. That code
> is written, and on this tier it is unreachable, because the guard above it demands an ID or hash."

This is wrong. The code at handlers.go:1001-1002 —

```go
ref := cfg.HarnessConfigID
if ref == "" {
    ref = cfg.HarnessConfig  // the brief annotates this: "<-- resolves by NAME"
}
```

— feeds `ref` into `resolveLocalResource` → `resourceObjectPath` → `HubClient.HarnessConfigs().Get`,
which hits the hub's `getHarnessConfig` handler. That handler calls `s.store.GetHarnessConfig(ctx, ref)`,
which calls `parseGetID(ref)` — a UUID-only parser. A slug like `"antigravity"` returns `ErrNotFound`,
which propagates up as a 500 error.

**The "code is written" claim is false.** What is written is a variable assignment that feeds a
slug into a UUID-only API endpoint. If the guard at line 994 were widened (which is exactly what
Fix F proposes) and `cfg.HarnessConfig` contained a slug, the result would be a 500 error, not
a successful resolution.

**This is a design inconsistency.** Templates have `resolveTemplate` (`hub/handlers_agent_create_helpers.go:37-62`)
which falls back from UUID to slug:

```go
func (s *Server) resolveTemplate(ctx context.Context, templateRef, projectID string) (*store.Template, error) {
    template, err := s.store.GetTemplate(ctx, templateRef)       // try UUID
    if err != nil && err != store.ErrNotFound { return nil, err }
    if template != nil { return template, nil }

    template, err = s.store.GetTemplateBySlug(ctx, templateRef, "project", projectID)  // try slug
    // ...
    template, err = s.store.GetTemplateBySlug(ctx, templateRef, "global", "")          // try global slug
    // ...
}
```

Harness configs have NO equivalent. The store has `GetHarnessConfigBySlug` (template_store.go:470-486)
and `ListHarnessConfigs` with a name filter that matches both name and slug (template_store.go:584-588).
The capability exists in the store layer but is NOT wired through the GET API handler.

---

## The salvage path

Fix F is conceptually sound. The broker has the name (from the ladder), and the store has the resource
(seeded on deploy). The gap is one missing function: a slug fallback in the hub's harness-config
GET handler.

**Required change**: Add slug fallback to `getHarnessConfig` (harness_config_handlers.go:396-414),
analogous to `resolveTemplate`:

```go
func (s *Server) getHarnessConfig(w http.ResponseWriter, r *http.Request, id string) {
    // ... auth check ...
    hc, err := s.store.GetHarnessConfig(ctx, id)  // try UUID
    if err == store.ErrNotFound {
        hc, err = s.store.GetHarnessConfigBySlug(ctx, id, "global", "")  // try slug
    }
    if err != nil {
        writeErrorFromErr(w, err, "")
        return
    }
    // ... rest unchanged ...
}
```

This would:
1. Satisfy precondition 2 — `resolveLocalResource` would find `antigravity` by slug
2. Fix the dead code at handlers.go:1001-1002 — the `ref = cfg.HarnessConfig` fallback would work
3. Align harness-config resolution with the template pattern
4. Enable Fix F as specified in the brief

The combined change is: hub slug fallback (one function, ~4 lines) + Fix F (hydration guard
widening + ladder resolution in start_context.go, ~15 lines).

**Scope question for the architect**: The hub slug fallback touches `harness_config_handlers.go`.
That is hub code, not broker code. The brief's scope was broker-side only. Is the hub change
in scope, or does it need separate authorization?

---

## Branch

`sn-harnesscfg-dev/blast-radius-measurement` — no new commits yet (this is a precondition report,
not implementation).

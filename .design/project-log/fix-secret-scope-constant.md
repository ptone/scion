# Fix: Secret ScopeProject Constant Mismatch

**Date:** 2026-05-12  
**Branch:** fix/workspace-path-fallback  
**Commit:** d139c27a  

## Problem

After the grove→project rename migration (V50), `pkg/secret/secret.go` still had:

```go
ScopeProject = "grove"
```

While `pkg/store/models.go` was correctly updated to:

```go
ScopeProject = "project"
```

This caused secret Set/Delete operations (via `secret.ScopeProject`) to write with scope `"grove"`, while lookups (via `store.ScopeProject`) used scope `"project"` — resulting in secrets not being found on migrated hubs.

## Fix

Changed `pkg/secret/secret.go` line 42 from `"grove"` to `"project"`.

## Verification

- `go build ./...` — pass
- `go vet ./...` — pass
- Searched for other missed scope constants with `git grep '= "grove"' -- '*.go'`
- All `pkg/store/models.go` scope constants already correct (`"project"`)
- No other duplicate scope constant definitions found in `pkg/`

## Additional Findings

Hardcoded `"grove"` scope strings remain in CLI client code (`cmd/harness_config_install.go`, `cmd/template_resolution.go`) used for Hub API communication. These are client→Hub scope parameters, not constant definitions, and may need separate evaluation for whether the Hub API normalizes them or expects `"project"`.

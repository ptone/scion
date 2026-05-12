# Python SDK: Secrets Resource (Step 10)

**Date**: 2026-05-12
**Author**: dev-py-secrets
**Branch**: scion/manage-sdk

## Summary

Implemented the `SecretsResource` module for the Python SDK, providing full CRUD access to the Hub secrets API. This is Step 10 of the SDK design plan (Section 2a in `.design/sdk-design.md`).

## What Was Done

### New Files
- `sdk/python/src/scion/resources/__init__.py` — Resources package, exports SecretsResource and AsyncSecretsResource
- `sdk/python/src/scion/resources/secrets.py` — Sync and async resource classes with list/get/set/delete methods
- `sdk/python/tests/test_secrets.py` — 29 unit tests covering all methods

### Modified Files
- `sdk/python/src/scion/_client.py` — Added `secrets` lazy property to ScionClient
- `sdk/python/src/scion/_async_client.py` — Added `secrets` lazy property to AsyncScionClient
- `sdk/python/src/scion/__init__.py` — Exported SecretsResource and AsyncSecretsResource

## Design Decisions

1. **Separate resource module** rather than inlining into `_client.py`: Keeps the client class focused on construction/lifecycle, and allows resource modules to be developed independently. Created the `resources/` package to house this and future resource modules (agents, projects, messages).

2. **Shared `_scope_params()` helper**: Extracted scope/scopeId query parameter construction into a module-level helper to avoid repetition across list/get/delete methods.

3. **Keyword-only arguments**: All optional parameters (scope, scope_id, etc.) are keyword-only to prevent positional argument mistakes and match the existing codebase conventions.

4. **Lazy property initialization**: `client.secrets` uses a cached lazy property pattern (`self._secrets` is `None` until first access), avoiding unnecessary object creation.

5. **URL encoding**: Used `urllib.parse.quote(key, safe='')` to properly encode secret keys with special characters (slashes, spaces) in the URL path, matching the Go implementation's `url.PathEscape()`.

6. **No pagination for secrets**: The Go reference returns `ListSecretResponse` directly (not paginated), so we return the response object rather than wrapping in `SyncPage`. The existing types (`ListSecretResponse`) already handle the list structure.

## Test Coverage

29 tests covering:
- **List**: no scope, with scope, empty results, scope-only params
- **Get**: basic, with scope, URL encoding, 404 error
- **Set**: basic creation, all options, update existing, None field exclusion, 400 validation error
- **Delete**: basic, with scope, 404 error, returns None
- **Property**: returns correct type, caching behavior
- **Async variants**: list, get, set, delete with scope params and error handling

## Observations

- The existing secret types in `sdk/python/src/scion/types/secrets.py` were already well-defined and needed no changes — the foundation work was thorough.
- The `respx` library's URL object decodes percent-encoded paths in `.path`, so URL encoding tests needed to check the raw URL string instead.
- The transport layer's `params` kwarg accepts `None` to omit query parameters entirely, which simplifies the scope parameter logic.

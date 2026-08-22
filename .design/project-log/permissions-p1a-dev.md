# Permissions Phase 1A Developer Log

Date: 2026-08-22
Branch: scion/permissions-p1a

## Summary

- Added `pkg/hub/permissions` as the canonical Phase 1A permission/resource registry.
- Derived Hub `ResourceActions`, `ScopeActions`, store UAT validation, store `agent:manage` expansion, and CLI UAT scope help from the registry.
- Removed stale `agent:start`, `agent:stop`, `agent:message`, and `agent:dispatch` from newly-created UAT vocabulary and capability projections while retaining legacy constants for old token metadata references.
- Added valid `project:update` and `agent:port_access` coverage across registry-derived server/CLI surfaces and the web token UI.
- Added registry drift tests covering capability maps, UAT scopes, agent token scopes, CLI help, and the web static scope list.
- Added `.agents/` to `.gitignore` so local orchestration state is not accidentally tracked.

## Notes

- Phase 1A does not introduce PolicyBoundary or centralize route enforcement. Registry `Enforcement` and `NonRouteUse` metadata document current use sites so drift tests can fail when exposed vocabulary has no matching use.
- Current agent lifecycle capability exposure uses `agent:attach`; `agent:start`, `agent:stop`, and `agent:message` remain as legacy action constants but are not exposed as independent capabilities or valid new UAT scopes.
- The web scope list remains static in this phase. It is guarded by a Hub drift test that scans the component for stale and required scope strings.

## Verification

- Baseline focused authz tests before implementation: passed.
- Focused registry/UAT/capability tests: passed.
- `go test ./pkg/hub -run '^TestHandleAgentMessage_BrokerTimeout504$' -timeout=60s -count=1`: passed.
- `go test ./pkg/hub -timeout=600s -count=1`: passed.
- `go test ./pkg/store -timeout=120s -count=1`: passed.
- `go test ./cmd -timeout=120s -count=1`: passed.
- `npm run typecheck` in `web/`: passed.
- `npx prettier --check src/components/shared/token-list.ts` in `web/`: passed.
- `npm run build` in `web/`: passed.
- `make ci`: passed.

`npm run lint` was attempted after installing web dependencies and failed on existing repo-wide lint debt unrelated to the Phase 1A scope edit, including test files outside the frontend tsconfig and pre-existing strict eslint violations.

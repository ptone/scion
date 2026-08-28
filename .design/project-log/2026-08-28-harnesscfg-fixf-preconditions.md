# Harness-config Fix F precondition findings

Agent: sn-harnesscfg-dev. Date: 2026-08-28. Tasks: #37, #48.

## Summary

Measured blast radius of Fix B (adding `default_harness_config: antigravity` to default template
YAML). Row 4 moves (profile at rung 6 loses to template at rung 3). **Fix B withdrawn.**

Measured preconditions for Fix F (hydrate using broker's ladder-resolved name against local storage).
**Precondition 2 fails**: `resolveLocalResource` cannot find a harness config by slug — the hub API
handler (`getHarnessConfig`) uses `parseGetID` which is UUID-only. The store has
`GetHarnessConfigBySlug` but it is not wired through the GET API handler. Templates have
`resolveTemplate` with slug fallback; harness configs do not. **Fix F as specified is dead.**

The slug-resolution gap is filed as `ptone/scion#1316`. Findings posted as a comment on that issue.

## Artifacts

- **Branch**: `sn-harnesscfg-dev/blast-radius-measurement` at `dd06037`
- **Test file**: `pkg/hub/blast_radius_fixb_test.go` — 14 tests, 535 lines
- **Report (scratchpad)**: `/scion-volumes/scratchpad/projects/single-node/reports/sn-harnesscfg-dev-fixb-withdrawal.md`
- **Report (scratchpad)**: `/scion-volumes/scratchpad/projects/single-node/reports/sn-harnesscfg-dev-fixf-preconditions.md`
- **Issue comment**: `ptone/scion#1316` — call chain, asymmetry, triple-guard finding, LocalStorage finding

## Key findings

1. **Precondition 1 (SATISFIED)**: `conn.LocalStorage` is non-nil in single-node hosted mode.
2. **Precondition 2 (FAILED)**: Hub API `GET /api/v1/harness-configs/{ref}` only accepts UUIDs. Slug lookup returns 404 (hub) → 500 (broker).
3. **Triple guard**: All three ID/hash check sites are correctness/defensive. No security or trust boundary.
4. **Brief error**: The round-2 brief claimed "the broker can already resolve by NAME" — false. The code feeds a slug into a UUID-only API endpoint.

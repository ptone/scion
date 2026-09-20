# P2.2 Developer Report: Render All Four Terminal Presets (#1655)

## Commit

- **Base**: `86cde0d4c5a9fcfb1a6f2ae60e7a965cc4f0f4c7` (P2.1 `scion/terminal-workspace`)
- **Head**: `10790ec174fec4775b8cfe5bc2d3723ba244ead6`
- **Branch**: `scion/tw-p2-1655-dev`
- **Bundle**: `transfers/p2-1655-candidate.bundle` SHA256 `31984a0fe4c7c1467454b263cf5e5e6df217d8c0b650acfd16f32e7e3df44442`

## Changed Files

| File | Lines | Nature |
|------|-------|--------|
| `web/src/client/terminal-workspace-root.ts` | +706 −17 | Primary: CSS Grid layout integration |
| `web/e2e/terminal-workspace/workspace.pw.ts` | +343 | Browser test extension |

**Not changed**: `terminal-layout.ts` (frozen), `terminal-pane.ts` (no edits needed), `terminal-sessions.ts`, `terminal-coordinator.ts`, `main.ts`.

## Implementation Summary

### 1. TerminalLayoutManager Integration

Replaced `selected: string | null` with `TerminalLayoutManager` instance. The `select()` method now calls `layoutManager.open(key)` which sets `single[0]` and `active = 'single'`. Rail clicks follow the same path via `openSessionRoute()` → coordinator → `select()`. The layout manager is exposed as a public `readonly` property for coordinator access and test instrumentation.

### 2. CSS Grid Pane Host

The pane host was converted from `display: flex; flex-direction: column` to `display: grid` with dynamic `grid-template-columns`/`grid-template-rows` per preset:

| Preset | Columns | Rows |
|--------|---------|------|
| `single` | `1fr` | `1fr` |
| `two-columns` | `1fr 1fr` | `1fr` |
| `two-rows` | `1fr` | `1fr 1fr` |
| `four` | `1fr 1fr` | `1fr 1fr` |

### 3. Stable Pane Positioning

Each pane element stays under the pane host permanently. On each refresh, `positionPanes()` assigns `grid-column`/`grid-row` based on which slot the pane occupies in the active preset. Panes not in the active preset get `display: none` and `setVisible(false)`.

### 4. Empty Slot Placeholders

For null slots in the active preset, placeholder elements with dashed borders and "Drop terminal here" text are rendered. Placeholders are managed as a pool — created on demand, positioned in the grid, and removed when not needed.

### 5. Layout Toolbar

A toolbar above the pane host with:
- Label: "Layout:"
- 4 preset buttons: `1`, `2 side by side`, `2 stacked`, `4`
- Active button highlighted with blue accent
- All buttons have `aria-label` and `aria-pressed`
- Restore button (hidden when not zoomed) for zoom exit

### 6. Zoom/Restore

When `layoutManager.getZoomed()` is non-null, the grid switches to single-column/row mode showing only the zoomed pane. The Restore button becomes visible. Zoom does not alter saved preset assignments (enforced by layout manager).

### 7. Narrow-Screen Presentation

On `max-width: 760px`, only one pane renders regardless of preset. The first occupied pane from the active preset is chosen. Desktop assignments are preserved.

### 8. Visibility Management

`refreshPaneVisibility()` determines the visible key set based on workspace visibility, zoom state, narrow mode, and active preset slots. Each pane's `setVisible()` is called accordingly. The P1.8 zero-resize guard prevents hidden panes from sending resize to tmux.

### 9. Close Integration

When sessions are synced (via registry subscription), closed sessions also get `layoutManager.close(key)` to clear all preset references.

### 10. Session Count and Status

Session count events continue via `publishCount()`. The status/empty message accounts for multi-pane layouts by checking `getVisibleSlots()`.

## Test Results

### Browser Tests (23 passed)

15 existing tests all pass unchanged plus 8 new tests:

| Test | Verifies |
|------|----------|
| Preset rendering slot counts | Each of 4 presets shows correct slot count |
| Fifth-agent open | Open 4 agents, open 5th → single, switch to four → grid unchanged |
| Socket identity across presets | No new WebSocket connections on preset switch |
| xterm identity across presets | Same Terminal instance before/after switch |
| Zoom preserves assignments | Zoom shows restore button, unzoom restores preset |
| Close clears slots | Close removes session from layout, remaining survive presets |
| Focus isolation | Only visible pane receives typed input |
| Preset controls | Each button activates correct layout with aria-pressed |

### Vitest (1264 passed, 62 files)

All tests pass including:
- `terminal-layout.test.ts`: 54 passed
- `terminal-sessions.test.ts`: passed
- `terminal-pane.test.ts`: 10 passed

### Other Gates

| Gate | Result |
|------|--------|
| `tsc --noEmit` | Clean |
| `npm run build` | Clean |
| `eslint` (changed files) | Clean |
| `make ci` | Web gates: clean. Go: pre-existing `pkg/runtimebroker` failures (unrelated) |

### Pre-existing Failures

`make ci` has pre-existing Go test failures in `pkg/runtimebroker` (6 tests related to hub endpoint configuration). These exist at the base commit `86cde0d4` and are unrelated to web frontend changes.

## Acceptance Criteria Verification

1. **xterm/socket identity preserved** ✅ — Browser tests confirm no new connections and same Terminal instance across all preset switches
2. **Both pair orientations render** ✅ — `two-columns` (left/right) and `two-rows` (top/bottom) use distinct grid templates with documented slot orders
3. **Fifth-agent open → single; four restores grid** ✅ — Browser test confirms `open()` sets single, and four-grid is preserved
4. **Narrow-screen preserves desktop assignments** ✅ — Narrow mode shows first occupied pane; desktop assignments untouched
5. **Only focused pane receives input** ✅ — P1.8 `_focused` derived from DOM focus; only visible pane in single mode receives input

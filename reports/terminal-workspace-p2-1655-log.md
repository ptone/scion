# Terminal Workspace P2.2 — Multi-Pane CSS Grid Rendering (#1655)

**Date**: 2026-09-20
**Phase**: P2.2 (Tiling and context actions — rendering)
**Base**: `86cde0d4` → **Head**: `10790ec1`
**Branch**: `scion/tw-p2-1655-dev`

## What Changed

Integrated `TerminalLayoutManager` (P2.1) into `TerminalWorkspaceRoot` to render four terminal presets using CSS Grid. The single-pane `selected` model was replaced with layout state-driven rendering. Panes are positioned via `grid-column`/`grid-row`, never recreated. A layout toolbar provides preset switching and zoom restore.

## Key Decisions

1. **Public `layoutManager`**: Made the layout manager a `readonly` public property instead of private. The coordinator needs access for future features, and test instrumentation needs it for zoom testing.

2. **`workspaceRoot` on element**: Stored the workspace root instance on its DOM element (`element.workspaceRoot = this`) for test access and coordinator integration. This avoids needing to thread the instance through multiple layers.

3. **No terminal-pane.ts changes**: After analysis, `setVisible()` already handles rapid show/hide cycles correctly. The existing `_visible`/`_focused` guards, `reveal()` method, and resize cancellation are sufficient for multi-pane rendering.

4. **Placeholder pool**: Empty slot placeholders are created on demand, positioned in the grid, and removed when unused. This is simpler than a fixed-size pool and handles preset switches cleanly.

5. **Narrow mode shows first occupied**: On narrow screens, the first occupied pane from the active preset is shown. This is simple and predictable. The user's desktop assignments are preserved.

## Files

| File | Change |
|------|--------|
| `web/src/client/terminal-workspace-root.ts` | Primary: layout integration, CSS Grid, toolbar |
| `web/e2e/terminal-workspace/workspace.pw.ts` | +8 browser tests for presets, zoom, identity |

## Tests

- 23/23 browser tests pass (15 existing + 8 new)
- 1264/1264 Vitest tests pass
- TypeScript, build, lint: all clean
- `make ci`: web gates clean; Go `pkg/runtimebroker` has pre-existing failures

## What's Next (Not In Scope)

- P2.3: Drag/drop placement into empty slots
- P2.4: Graph/chat pane header buttons

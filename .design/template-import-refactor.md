# Template Import Refactor: Direct Server-Side Import

## Problem

The web UI's "Load Templates" feature on the Grove Settings page works by spawning a
bootstrap container agent that runs `scion templates sync --all` inside a Docker
container. This approach has several drawbacks:

- **Slow**: Requires container provisioning before any work begins.
- **Inflexible path**: `scion templates sync --all` only looks for templates in
  `.scion/templates/` at the workspace root — it cannot target a templates directory
  at an arbitrary subdirectory depth.
- **Heavyweight**: A full agent lifecycle (create → dispatch → provision → run → stop)
  just to import a handful of template files.

## Solution

Replace the bootstrap container agent with a direct server-side import that mirrors
the `scion templates import` CLI workflow:

1. The Hub server fetches the remote source URL (GitHub deep path, archive, or rclone)
   directly using the existing `pkg/config.FetchRemoteTemplate()` infrastructure.
2. The Hub parses the fetched directory using `pkg/config/templateimport` to find scion
   templates.
3. The Hub registers each template in the database and storage using the same
   `bootstrapSingleTemplate` / `syncExistingTemplate` functions already used at startup.
4. The HTTP response is synchronous — the frontend gets a result immediately.

## New API Endpoint

```
POST /api/v1/groves/{groveId}/import-templates
```

**Request body:**
```json
{
  "sourceUrl": "https://github.com/org/repo/tree/main/path/to/templates"
}
```

**Response:**
```json
{
  "templates": ["poker-dealer", "poker-player", "poker-auditor"],
  "count": 3
}
```

`sourceUrl` supports any format understood by `config.FetchRemoteTemplate`:
- GitHub URLs including deep subdirectory paths (via svn export or sparse git checkout)
- Archive URLs (`.zip`, `.tar.gz`, `.tgz`)
- rclone paths (`:gcs:bucket/path`)

## Template Scoping

Templates imported through this endpoint are **grove-scoped** — they appear only within
the grove they were imported into. This matches the intent of the Grove Settings page
being the entry point.

Global templates (available across all groves) continue to be managed via the
`BootstrapTemplatesFromDir` path at Hub startup.

## Removed: Bootstrap Container Sync

The previous `POST /api/v1/groves/{groveId}/sync-templates` endpoint and its bootstrap
container agent are removed. The web UI now uses `import-templates` exclusively.

The `scion templates sync` CLI command continues to work for direct CLI-to-Hub syncing
from a local workspace.

## Web UI Changes

The Templates tab in Grove Settings is updated:

- URL input is shown for **all** groves (previously hidden for git-anchored groves).
- For git-anchored groves the URL input is pre-populated with the grove's git remote URL.
- Users can override or extend the URL with a deep path
  (e.g., append `/tree/main/.scion/templates`).
- The button calls `import-templates` and awaits a synchronous result — no agent
  polling loop.

## Key Files Changed

| File | Change |
|------|--------|
| `pkg/hub/template_bootstrap.go` | `bootstrapSingleTemplate` gains `scope`/`groveID` params; new `importTemplatesFromRemote` |
| `pkg/hub/handlers.go` | New `handleGroveImportTemplates`; removed `handleGroveSyncTemplates`, `cleanTemplateRepoURL` |
| `web/src/components/pages/grove-settings.ts` | Replaced agent-polling sync flow with direct import call |

# Template Viewer & Editor (Web UI)

**Status:** Draft
**Created:** 2026-04-03
**Related:** [web-file-editor.md](./web-file-editor.md), [hosted-templates.md](./hosted/hosted-templates.md), [agnostic-template-design.md](./agnostic-template-design.md), [grove-level-templates.md](./grove-level-templates.md)

---

## 1. Overview

### Goal

Enable browsing and editing template file contents directly in the web UI. Templates currently appear as metadata-only items in grove settings — users can see name, description, and harness type but cannot view or modify the actual files (CLAUDE.md, system prompts, config files, etc.) without downloading them externally.

This design re-purposes the existing workspace file browser component to display template contents, and integrates the shared file editor component ([web-file-editor.md](./web-file-editor.md)) for inline editing.

### Current State

- **Template listing** in grove settings (`grove-settings.ts`) shows name/description/harness badge in a flat list under the Resources > Templates tab.
- **No file browsing** — template files are only accessible via the download API which returns signed URLs. There is no UI to browse the file tree.
- **No inline editing** — template modifications require downloading files, editing locally, and re-uploading (or creating a new template version).
- **Template API** supports file listing via the download endpoint (`GET /api/v1/templates/{id}/download`) which returns a manifest with file paths, sizes, and hashes.

### Scope

This document covers:
- A dedicated template detail page for browsing and editing template files
- Re-using the shared `scion-file-browser` and `scion-file-editor` components (from [web-file-editor.md](./web-file-editor.md))
- API changes needed to support reading/writing individual template files
- Template versioning considerations

This document does NOT cover:
- The file browser or file editor components themselves (see [web-file-editor.md](./web-file-editor.md))
- Template metadata editing (name, description, harness — already supported via PATCH)
- Template creation or import workflows (already implemented)

---

## 2. User Experience

### 2.1 Template Detail Page

Currently, clicking a template in the Resources > Templates list does nothing. The proposed change adds a dedicated template detail page at `/groves/{groveId}/templates/{templateId}`.

**Template List (grove settings):**
- Clicking a template row navigates to the template detail page.
- A back/breadcrumb link provides clear navigation back to grove settings.

**Template Detail Page:**
```
┌─────────────────────────────────────────────────────────┐
│ ← Grove Settings > Templates > my-custom-agent          │
├─────────────────────────────────────────────────────────┤
│ my-custom-agent    Custom research agent        claude   │
│                                                         │
│ Template Files                           [↻] [Upload]   │
│                                                         │
│  📄 CLAUDE.md                1.2 KB      ✏️  👁  ⬇      │
│  📄 home/.bashrc              340 B      ✏️  👁  ⬇      │
│  📄 home/.config/settings.json 89 B      ✏️  👁  ⬇      │
│  📄 system-prompt.md         2.1 KB      ✏️  👁  ⬇      │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

**Behavior:**
- The page shows template metadata (name, description, harness) and the file browser below.
- The file browser uses the shared `scion-file-browser` component with a `TemplateFileBrowserDataSource`.
- File actions mirror the workspace browser: edit (pencil), preview (eye), download.
- Upload button allows adding new files to the template.
- Editing a file opens the shared `scion-file-editor` in full-width replacement mode (same pattern as workspace editing).

### 2.2 File Browser Re-Use

The template detail page uses the shared `scion-file-browser` component (extracted from `grove-detail.ts` as part of [web-file-editor.md](./web-file-editor.md)). The only differences are in the data source adapter:

| Aspect | Workspace Browser | Template Browser |
|--------|------------------|------------------|
| Data source | Workspace filesystem API | Template file manifest + content API |
| Upload | Multipart file upload | Multipart file upload (proxied through Hub, consistent with workspace) |
| Sorting | By name, size, modified | By name, size (no modTime on template files currently) |
| Path display | Relative to workspace root | Relative to template root |
| File list style | Flat list | Flat list |
| Permission gate | Grove `update` capability | Template `update` capability |

### 2.3 Editing Template Files

Clicking the pencil icon on a template file opens the shared `scion-file-editor` component (from [web-file-editor.md](./web-file-editor.md)).

**Key differences from workspace file editing:**
- **Save** writes back through the template file API (not the workspace API), via the `TemplateFileBrowserDataSource`.
- **Scope awareness** — global templates may be viewable but not editable by grove-scoped users.

### 2.4 Navigation Flow

```
Grove Settings Page
  └── Resources > Templates tab
        └── Click template row → navigate to /groves/{id}/templates/{templateId}
              └── Template Detail Page (metadata + file browser)
                    └── Click pencil icon → full-width editor replaces file browser
                          └── Save → write to template storage
                          └── Close → return to file browser
                    └── ← Back → return to grove settings
```

---

## 3. API Changes

### 3.1 Read Individual Template File Content

The current download endpoint returns signed URLs for all files. For inline editing, we need to fetch a single file's content directly.

**New endpoint:**
```
GET /api/v1/templates/{templateId}/files/{filePath}
```

Response:
```json
{
  "path": "CLAUDE.md",
  "content": "# My Agent\n\nSystem instructions...",
  "size": 1234,
  "hash": "sha256:abc123...",
  "encoding": "utf-8"
}
```

**Implementation:** The Hub fetches the file from storage (GCS/local) and returns the content inline. For cloud storage, this means the Hub proxies the content rather than redirecting to a signed URL — necessary because the browser editor needs the content as a JSON response, not a file download.

**Size limit:** Files above 1MB return `413 Payload Too Large` with a message suggesting download instead.

### 3.2 Write Individual Template File Content

**New endpoint:**
```
PUT /api/v1/templates/{templateId}/files/{filePath}
Content-Type: application/json

{
  "content": "# Updated content\n...",
  "expectedHash": "sha256:abc123..."  // optional optimistic concurrency
}
```

**Behavior:**
- Writes the file to template storage.
- Updates the template's file manifest (size, hash).
- Recomputes the template's `ContentHash`.
- If `expectedHash` is provided and doesn't match the current file hash, returns `409 Conflict`.
- Returns `403 Forbidden` for locked templates.

### 3.3 Delete Template File

**New endpoint:**
```
DELETE /api/v1/templates/{templateId}/files/{filePath}
```

- Removes file from storage and manifest.
- Returns `403` for locked templates.

### 3.4 Upload Template File

**New endpoint:**
```
POST /api/v1/templates/{templateId}/files
Content-Type: multipart/form-data
```

- Accepts one or more files with path metadata.
- Adds to template storage and manifest.
- Uses the same multipart upload pattern as the workspace file upload (not signed URLs) for consistency.

### 3.5 Template File Listing

The existing download endpoint (`GET /api/v1/templates/{id}/download`) returns file metadata. For the file browser, we need a lighter listing endpoint that doesn't generate signed URLs:

**New endpoint:**
```
GET /api/v1/templates/{templateId}/files
```

Response:
```json
{
  "files": [
    { "path": "CLAUDE.md", "size": 1234, "hash": "sha256:abc..." },
    { "path": "home/.bashrc", "size": 340, "hash": "sha256:def..." }
  ],
  "totalSize": 1574,
  "totalCount": 2
}
```

This avoids the overhead of generating signed URLs when we just need to display the file tree.

---

## 4. Component Architecture

### 4.1 Template Detail Page

A new page component at the route `/groves/{groveId}/templates/{templateId}`:

```
scion-template-detail (new page component)
├── Properties:
│   groveId: string
│   templateId: string
├── Children:
│   breadcrumb navigation (back to grove settings)
│   template metadata header (name, description, harness)
│   scion-file-browser (with TemplateFileBrowserDataSource)
│   scion-file-editor (opened on edit request, replaces file browser)
```

### 4.2 Template Data Source

```typescript
// New adapter implementing the FileBrowserDataSource interface
// (defined in web-file-editor.md)
class TemplateFileBrowserDataSource implements FileBrowserDataSource {
  // Uses /api/v1/templates/{id}/files/... endpoints
}
```

The shared `scion-file-browser` and `scion-file-editor` components are prerequisites — built as part of [web-file-editor.md](./web-file-editor.md).

---

## 5. Template Versioning Considerations

### 5.1 Current Model

Templates currently use a two-phase lifecycle:
1. **Create** (status: `pending`) — metadata registered, upload URLs generated.
2. **Finalize** (status: `active`) — files verified, content hash computed.

Once finalized, files are effectively immutable — the API doesn't support modifying individual files in-place. Changes require creating a new template (or clone + modify).

### 5.2 Impact of Inline Editing

Inline editing introduces mutable template files, which conflicts with the current immutable-after-finalize model.

**Approach A: Mutable Active Templates**
- Allow PUT/DELETE on files of active templates.
- Update the content hash after each change.
- Simplest to implement, but breaks the immutability guarantee.
- Risk: runtime brokers may have cached a previous version; the content hash change signals invalidation.

**Approach B: Copy-on-Write Versioning**
- Editing creates a new version of the template (same name/slug, new ID or version number).
- The old version remains available (for rollback, audit).
- More robust but significantly more complex.
- Template references (in agents, grove defaults) would need to track "latest" vs "pinned" versions.

**Approach C: Draft/Publish Workflow**
- Editing puts the template into `draft` status.
- Changes are accumulated in the draft.
- User explicitly "publishes" the draft to make it active.
- Provides a review step but adds UX friction.

**Decision:** Start with **Approach A** (mutable active templates) for simplicity. The content hash update mechanism already exists and brokers use it for cache invalidation. Add versioning (Approach B) as a later enhancement when the need for rollback becomes clear.

**Important clarification:** Template mutability only affects *new* agents created from the template. Already-created/running agents are not affected by template edits — their files were copied at creation time.

### 5.3 Broker Cache Invalidation (Investigated)

Cache invalidation is **implicit and demand-driven** — no polling or webhooks needed:

1. When a template file is edited, the Hub recomputes the template's `ContentHash` and stores it in the database.
2. On next agent creation, the Hub resolves the template and passes the **current** `ContentHash` to the broker in the `CreateAgentRequest`.
3. The broker's template cache (`pkg/templatecache/`) checks for a matching hash via `cache.GetByHash(contentHash)`:
   - **Hit** → uses cached files immediately.
   - **Miss** (hash changed) → re-downloads from Hub storage. Downloads are incremental — only files whose individual hashes changed are fetched; unchanged files are reused from the previous cached version.
4. The new version is cached under the new content hash. Old versions remain until LRU eviction (default 100MB cache limit).

**Co-located brokers** (broker on same machine as Hub) bypass the cache entirely — they read templates directly from the local filesystem, so edits are picked up immediately.

**Result:** Template edits propagate to the very next agent creation request with no delay beyond the file download time. The existing architecture fully supports mutable active templates.

### 5.4 Locked Template Cleanup

The `locked` field on templates was introduced speculatively in earlier designs but is not meaningfully used in the current implementation. Rather than building UI around it, the locked template concept should be cleaned out as tech debt until it can be reintroduced more thoughtfully. This is tracked separately from the template editor work.

---

## 6. Decisions

| Question | Decision |
|----------|----------|
| Accordion vs. dedicated page | Dedicated page (`/groves/{id}/templates/{templateId}`) with clear breadcrumb navigation |
| File tree vs. flat list | Flat list of full paths for now |
| Template file upload UX | Use workspace-style multipart upload (consistent, simpler) |
| Shared component extraction timing | File browser + editor extracted first as part of [web-file-editor.md](./web-file-editor.md), before template editor work begins |
| Locked templates | Clean out `locked` field as tech debt (see Section 5.4) |
| Broker cache invalidation | Implicit and demand-driven — no action needed (see Section 5.3) |

---

## 7. Implementation Phases

*All phases below depend on [web-file-editor.md](./web-file-editor.md) Phases 1–2 being complete (shared file browser + core editor).*

### Phase 1: Template File Browsing & Editing ✅

- [x] Add `GET /api/v1/templates/{templateId}/files` listing endpoint
- [x] Add `GET /api/v1/templates/{templateId}/files/{filePath}` content endpoint
- [x] Add `PUT /api/v1/templates/{templateId}/files/{filePath}` write endpoint
- [x] Add `DELETE /api/v1/templates/{templateId}/files/{filePath}` delete endpoint
- [x] Implement `TemplateFileBrowserDataSource` adapter
- [x] Add `scion-template-detail` page component with route `/groves/{id}/templates/{templateId}`
- [x] Breadcrumb navigation back to grove settings
- [x] Wire template list rows in grove settings to navigate to detail page
- [x] Integrate `scion-file-browser` and `scion-file-editor` on detail page
- [x] Gate edit/delete/upload on `update` capability
- [x] Content hash recomputation on save

### Phase 2: Upload & Polish ✅

- [x] Add `POST /api/v1/templates/{templateId}/files` upload endpoint (multipart)
- [x] Template file upload UI via file browser toolbar
- [x] Markdown preview for template `.md` files via eye icon ([web-file-editor.md](./web-file-editor.md) Phase 3 is complete)
- [ ] Tree view for nested template file structures (deferred)
- [ ] Template ZIP download (deferred)

---

## 8. Dependencies

- **Web File Editor & File Browser** ([web-file-editor.md](./web-file-editor.md)) — the shared `scion-file-browser` and `scion-file-editor` components are prerequisites for all phases of this design. That work is sequenced first.
- **Template API** — existing CRUD and download endpoints. New file-level endpoints needed.
- **Storage layer** — `pkg/storage/` must support reading individual files by path (currently supports full-prefix operations).

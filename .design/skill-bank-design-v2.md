# Skill Bank: Late-Binding Skill Registry — Design v2

**Status**: Draft
**Issue**: [#29](https://github.com/ptone/scion/issues/29)
**Author**: Design agent
**Date**: 2026-05-12
**Supersedes**: `.design/skill-bank-design.md` (v1, branch `scion/skill-bank-design`)

---

## 1. Executive Summary

This design introduces a **Skill Bank** — a centralized registry and late-binding resolution system for agent skills. Today, skills are static files physically copied into templates and harness-configs. The Skill Bank decouples skill *authorship* from skill *consumption*: templates declare skill **references** (URIs) in `scion-agent.yaml`, and the provisioning pipeline resolves those references at agent-creation time — fetching content from the Hub's internal registry or a federated external registry.

### Goals

1. **Reuse without duplication** — A skill authored once can be consumed by any template without copy-paste.
2. **Late binding** — Templates pin version constraints; resolution happens at provision time.
3. **Federated discovery** — Hub-hosted skills and external registries (community catalogs, corporate registries).
4. **Harness agnosticism** — Skills are SKILL.md bundles; the provisioner places them into whichever `SkillsDir()` the harness declares.
5. **Trust and governance** — Scoped visibility, content-hash integrity, optional signature verification.

### Non-Goals (v1)

- Skill *execution* changes — harnesses continue to discover skills via their native `skills_dir`.
- Inter-skill dependencies — skills remain independent units.
- Runtime hot-loading — resolution is provision-time only.

---

## 2. Current State

### How skills work today

1. **Authoring**: A skill is a directory containing `SKILL.md` (YAML frontmatter: `name`, `description`) plus optional supporting files (e.g., `scripts/`). Format follows [agentskills.io](https://agentskills.io/).

2. **Placement**: Skills live in two locations:
   - **Harness-config level**: `~/.scion/harness-configs/<name>/skills/` — common to all agents using that harness-config.
   - **Template level**: `.scion/templates/<name>/skills/` — role-specific skills for that template.

3. **Provisioning** (`pkg/agent/provision.go`, lines 575–615): During `ProvisionAgent`:
   - Resolves the harness via `harness.Resolve()` to get `h.SkillsDir()` (e.g., `.claude/skills`, `.gemini/skills`).
   - Copies harness-config skills: `hcDir.Path/skills` → `agentHome/<skillsDir>/`.
   - Overlays template-chain skills: each template in the chain copies `tpl.Path/skills` → `agentHome/<skillsDir>/` (later templates win on conflict).

4. **Container-script provisioning** (`pkg/harness/container_script_harness.go`): For `provisioner.type: container-script` harnesses, Go-side `Provision()` stages a bundle into `agentHome/.scion/harness/` with a `ProvisionManifest`. Skills are already in place from step 3 before `Provision()` runs.

### Limitations

| Problem | Impact |
|---------|--------|
| **Copy-paste proliferation** | Same skill (e.g., `scion`, `team-creation`) must exist in every template/harness-config |
| **No versioning** | Updating a shared skill requires manual sync across all copies |
| **No discovery** | Users cannot browse or search available skills |
| **No sharing boundary** | Skills cannot be shared across projects or published externally |

### Existing patterns we follow

The design aligns with established codebase patterns:

| Pattern | Where | How Skill Bank follows it |
|---------|-------|---------------------------|
| **Scoped resources** | `store.TemplateScopeGlobal/Project/User` | Skills use the same three scopes |
| **Store sub-interface** | `store.TemplateStore` composed into `store.Store` | New `store.SkillStore` interface |
| **Hub handler dispatch** | `handleTemplatesV2` → method switch | New `handleSkills` handler set |
| **Signed-URL file transfer** | `template_file_handlers.go`, `SignedURLExpiry = 15 * time.Minute` | Reuse `pkg/storage` signed URLs |
| **Hub client service** | `hubclient.TemplateService` interface | New `hubclient.SkillService` |
| **Content hash integrity** | `Template.ContentHash`, `TemplateFile.Hash` | `Skill.ContentHash`, `SkillFile.Hash` |
| **Sequential migrations** | `migrationV1` … `migrationV50` in `sqlite.go` | New `migrationV51` for skills tables |

---

## 3. Skill URI Scheme

```
skill://<registry>/<scope>/<name>@<version>
```

### Components

| Component | Description | Examples |
|-----------|-------------|---------|
| `registry` | Registry hostname. `scion` aliases the connected Hub. | `scion`, `registry.agentskills.io` |
| `scope` | Visibility scope within the registry. | `core`, `global`, `project/<id>`, `user/<id>` |
| `name` | Skill identifier (kebab-case, validated like template slugs). | `scion`, `security-audit` |
| `version` | Semver constraint or tag. | `1.0.0`, `^1.0`, `~1.2`, `latest` |

### Registry aliases

| Alias | Resolves to | Description |
|-------|-------------|-------------|
| `scion` | Connected Hub endpoint | Hub's internal skill bank |
| `project` | `scion` with scope=`project/<current-project>` | Current project's skills |
| `user` | `scion` with scope=`user/<current-user>` | Current user's skills |

### Shorthand forms

```yaml
# Full form
- uri: skill://scion/core/scion@^1.0

# Omit registry (defaults to "scion")
- uri: skill:///core/scion@^1.0

# Omit version (defaults to "latest")
- uri: skill://scion/core/scion

# Bare name — resolved via scope search order
- uri: scion
```

### Version resolution

| Syntax | Meaning |
|--------|---------|
| `1.2.3` | Exact version |
| `^1.2` | Compatible: ≥1.2.0, <2.0.0 (semver caret) |
| `~1.2` | Approximate: ≥1.2.0, <1.3.0 (semver tilde) |
| `latest` | Highest published (non-deprecated) version |
| `sha256:abcdef` | Content-addressed (immutable) |

### Resolution algorithm

When a bare name is used (no explicit scope), the resolver searches in this order:

1. **user** scope — personal skills first
2. **project** scope — team/project skills
3. **global** scope — Hub-wide published skills
4. **core** scope — Scion first-party skills (always available, lowest priority)

The narrowest scope wins on name collision. An explicit scope in the URI bypasses this search.

---

## 4. Template YAML Schema Extension

Add a `skills` field to `ScionConfig` in `pkg/api/types.go`:

```go
type ScionConfig struct {
    // ... existing fields (Harness, Env, Volumes, MCPServers, etc.) ...

    // Skills declares skill references resolved at provision time.
    // Fetched from the Hub skill bank or federated registries and placed
    // into the harness skills directory alongside local skills/ files.
    Skills []SkillReference `json:"skills,omitempty" yaml:"skills,omitempty"`
}

type SkillReference struct {
    // URI is the skill reference:
    //   skill://<registry>/<scope>/<name>@<version>
    // Or a bare name for scope-search resolution.
    URI string `json:"uri" yaml:"uri"`

    // As optionally renames the skill directory in the container.
    // If empty, the skill's declared name is used.
    As string `json:"as,omitempty" yaml:"as,omitempty"`

    // Optional controls whether provisioning fails if the skill
    // cannot be resolved. Default false (required).
    Optional bool `json:"optional,omitempty" yaml:"optional,omitempty"`
}
```

### Example scion-agent.yaml

```yaml
schema_version: "1"
description: "Web development agent"
agent_instructions: agents.md

skills:
  - uri: skill://scion/core/scion@^1.0
  - uri: skill://scion/core/team-creation@^1.0
  - uri: skill://project/security-audit@latest
  - uri: skill://user/my-workflow@1.2.0
    as: custom-workflow
    optional: true
```

### Interaction with local `skills/` directory

Local skills (files in template's `skills/` directory) continue to work unchanged. The `skills:` YAML field is resolved *after* local skills are copied. **Registry skills win on name conflict**, matching the existing overlay pattern where later sources override earlier ones. This lets a team override a core skill with a project-scoped version.

### Template chain behavior

Template-to-template inheritance is not yet implemented. The `skills:` field is a **flat, explicit list** per template — each template declares exactly the skills it needs. When template inheritance is tackled in the future, merging semantics for the `skills:` field will be defined at that time.

---

## 5. Skill Content Model

### Directory structure (unchanged from today)

```
skill-name/
├── SKILL.md           # REQUIRED: YAML frontmatter + instructions
├── scripts/           # OPTIONAL: backing scripts
│   ├── analyze.sh
│   └── report.py
└── references/        # OPTIONAL: reference material
    └── checklist.md
```

### SKILL.md format (unchanged)

```markdown
---
name: security-audit
description: >-
  Perform security audits on code changes.
---

# Security Audit Skill
[Skill instructions...]
```

### Registry metadata (Hub-side, not in SKILL.md)

Stored in the Hub database, modeled after `store.Template`:

```go
// In pkg/store/models.go

// Skill represents a skill in the Hub skill bank.
type Skill struct {
    // Identity
    ID          string `json:"id"`
    Name        string `json:"name"`
    Slug        string `json:"slug"`
    DisplayName string `json:"displayName,omitempty"`
    Description string `json:"description,omitempty"`

    // Scope (mirrors template scoping)
    Scope   string `json:"scope"`             // "core", "global", "project", "user"
    ScopeID string `json:"scopeId,omitempty"` // projectId or userId

    // Storage (mirrors template storage)
    StorageURI    string `json:"storageUri,omitempty"`
    StorageBucket string `json:"storageBucket,omitempty"`
    StoragePath   string `json:"storagePath,omitempty"`

    // Latest version metadata (denormalized for list queries)
    LatestVersion string `json:"latestVersion,omitempty"`
    ContentHash   string `json:"contentHash,omitempty"`

    // File manifest (for latest version)
    Files []SkillFile `json:"files,omitempty"`

    // Metadata
    Tags       []string `json:"tags,omitempty"`
    Author     string   `json:"author,omitempty"`
    OwnerID    string   `json:"ownerId,omitempty"`
    Status     string   `json:"status"`     // "active", "deprecated", "archived"
    Visibility string   `json:"visibility"` // "private", "project", "public"

    // Timestamps
    Created time.Time `json:"created"`
    Updated time.Time `json:"updated"`
}

// SkillVersion represents a published version of a skill.
type SkillVersion struct {
    ID          string      `json:"id"`
    SkillID     string      `json:"skillId"`
    Version     string      `json:"version"` // Semver: "1.2.3"
    ContentHash string      `json:"contentHash"`
    Files       []SkillFile `json:"files,omitempty"`
    Status      string      `json:"status"` // "published", "deprecated", "archived"

    // Storage (version-specific path within skill's storage)
    StoragePath string `json:"storagePath,omitempty"`

    // Deprecation info
    DeprecatedBy string `json:"deprecatedBy,omitempty"` // Version that replaces this
    DeprecationMsg string `json:"deprecationMsg,omitempty"`

    Created time.Time `json:"created"`
}

// SkillFile represents a file within a skill (mirrors TemplateFile).
type SkillFile struct {
    Path string `json:"path"`
    Size int64  `json:"size"`
    Hash string `json:"hash"`
    Mode string `json:"mode,omitempty"`
}
```

### Scope constants

```go
// In pkg/store/models.go, alongside existing TemplateScope* constants

const (
    SkillScopeCore    = "core"    // Scion-maintained first-party skills
    SkillScopeGlobal  = "global"  // User-published, visible to all Hub users
    SkillScopeProject = "project" // Visible to project members
    SkillScopeUser    = "user"    // Private to the publishing user
)

const (
    SkillStatusActive     = "active"
    SkillStatusDeprecated = "deprecated"
    SkillStatusArchived   = "archived"
)
```

**Note on "core" scope**: Templates currently use three scopes (global, project, user). Skills add a fourth — `core` — for Scion-maintained first-party skills. Only Hub administrators can publish to `core`. This is a new concept that could later be backported to templates if useful.

---

## 6. Database Schema

New migration `migrationV51` adds two tables, following the `Template`/`TemplateFile` pattern:

```sql
-- Skills table (mirrors templates table structure)
CREATE TABLE IF NOT EXISTS skills (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL,
    display_name TEXT DEFAULT '',
    description TEXT DEFAULT '',
    scope       TEXT NOT NULL DEFAULT 'global',
    scope_id    TEXT DEFAULT '',
    storage_uri TEXT DEFAULT '',
    storage_bucket TEXT DEFAULT '',
    storage_path TEXT DEFAULT '',
    latest_version TEXT DEFAULT '',
    content_hash TEXT DEFAULT '',
    files       TEXT DEFAULT '[]',    -- JSON array of SkillFile
    tags        TEXT DEFAULT '[]',    -- JSON array of strings
    author      TEXT DEFAULT '',
    owner_id    TEXT DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    visibility  TEXT NOT NULL DEFAULT 'public',
    created     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(slug, scope, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_skills_scope ON skills(scope, scope_id);
CREATE INDEX IF NOT EXISTS idx_skills_name ON skills(name);
CREATE INDEX IF NOT EXISTS idx_skills_status ON skills(status);

-- Skill versions table
CREATE TABLE IF NOT EXISTS skill_versions (
    id          TEXT PRIMARY KEY,
    skill_id    TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    version     TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    files       TEXT DEFAULT '[]',    -- JSON array of SkillFile
    storage_path TEXT DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'published',
    deprecated_by TEXT DEFAULT '',
    deprecation_msg TEXT DEFAULT '',
    created     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(skill_id, version)
);

CREATE INDEX IF NOT EXISTS idx_skill_versions_skill ON skill_versions(skill_id);
```

---

## 7. Store Interface

New `SkillStore` sub-interface composed into `store.Store`, following the `TemplateStore` pattern:

```go
// In pkg/store/store.go

type Store interface {
    // ... existing sub-interfaces ...

    // Skill operations (Skill Bank)
    SkillStore
}

// SkillStore defines skill persistence operations.
type SkillStore interface {
    // CreateSkill creates a new skill record.
    CreateSkill(ctx context.Context, skill *Skill) error

    // GetSkill retrieves a skill by ID.
    GetSkill(ctx context.Context, id string) (*Skill, error)

    // GetSkillBySlug retrieves a skill by slug, scope, and scopeID.
    GetSkillBySlug(ctx context.Context, slug, scope, scopeID string) (*Skill, error)

    // UpdateSkill updates an existing skill.
    UpdateSkill(ctx context.Context, skill *Skill) error

    // DeleteSkill removes a skill by ID.
    DeleteSkill(ctx context.Context, id string) error

    // ListSkills returns skills matching the filter criteria.
    ListSkills(ctx context.Context, filter SkillFilter, opts ListOptions) (*ListResult[Skill], error)

    // --- Version operations ---

    // CreateSkillVersion creates a new version record.
    CreateSkillVersion(ctx context.Context, version *SkillVersion) error

    // GetSkillVersion retrieves a specific version of a skill.
    GetSkillVersion(ctx context.Context, skillID, version string) (*SkillVersion, error)

    // ListSkillVersions returns all versions for a skill.
    ListSkillVersions(ctx context.Context, skillID string) ([]*SkillVersion, error)

    // ResolveSkillVersion resolves a version constraint to a concrete version.
    // E.g., "^1.0" → "1.3.2", "latest" → "2.0.1".
    ResolveSkillVersion(ctx context.Context, skillID, constraint string) (*SkillVersion, error)
}

// SkillFilter defines criteria for filtering skills.
type SkillFilter struct {
    Name    string // Exact match on skill name
    Scope   string
    ScopeID string
    Tags    []string
    Status  string
    Search  string // Full-text search on name/description
}
```

---

## 8. Hub API Endpoints

New endpoints under `/api/v1/skills`, registered in `server.go` alongside templates:

```go
// In server.go registerRoutes()
s.mux.HandleFunc("/api/v1/skills", s.handleSkills)
s.mux.HandleFunc("/api/v1/skills/", s.handleSkillRoutes)
s.mux.HandleFunc("/api/v1/skills/resolve", s.handleSkillResolve)
```

### Endpoint summary

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/skills` | Create skill |
| `GET` | `/api/v1/skills` | List skills (with scope/tag/search filters) |
| `GET` | `/api/v1/skills/{id}` | Get skill by ID |
| `PUT` | `/api/v1/skills/{id}` | Update skill metadata |
| `DELETE` | `/api/v1/skills/{id}` | Delete skill |
| `POST` | `/api/v1/skills/{id}/versions` | Publish new version |
| `GET` | `/api/v1/skills/{id}/versions` | List versions |
| `GET` | `/api/v1/skills/{id}/versions/{ver}` | Get specific version |
| `POST` | `/api/v1/skills/{id}/versions/{ver}/upload` | Request upload URLs |
| `POST` | `/api/v1/skills/{id}/versions/{ver}/finalize` | Finalize upload |
| `GET` | `/api/v1/skills/{id}/versions/{ver}/download` | Request download URLs |
| `POST` | `/api/v1/skills/resolve` | Batch-resolve skill URIs |

### Batch resolution endpoint (critical path)

This is the provisioning-time endpoint. Resolves multiple URIs in one call:

```
POST /api/v1/skills/resolve

Request:
{
  "skills": [
    {"uri": "skill://scion/core/scion@^1.0"},
    {"uri": "skill://project/security-audit@latest"}
  ],
  "projectId": "project-uuid",
  "userId": "user-uuid"
}

Response:
{
  "resolved": [
    {
      "uri": "skill://scion/core/scion@^1.0",
      "name": "scion",
      "resolvedVersion": "1.3.2",
      "contentHash": "sha256:abc123...",
      "files": [
        {
          "path": "SKILL.md",
          "url": "https://storage.../signed-url",
          "method": "GET",
          "expires": "2026-05-12T15:15:00Z"
        }
      ]
    }
  ],
  "errors": [
    {
      "uri": "skill://scion/core/missing@1.0",
      "error": "skill not found"
    }
  ],
  "warnings": [
    {
      "uri": "skill://scion/core/old-tool@1.0",
      "message": "skill deprecated, use new-tool@^2.0 instead"
    }
  ]
}
```

### Handler implementation (follows template_handlers.go pattern)

```go
// In pkg/hub/skill_handlers.go

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        s.listSkills(w, r)
    case http.MethodPost:
        s.createSkill(w, r)
    default:
        s.MethodNotAllowed(w, r)
    }
}

func (s *Server) handleSkillRoutes(w http.ResponseWriter, r *http.Request) {
    // Parse ID from path, dispatch to sub-routes:
    // /api/v1/skills/{id}
    // /api/v1/skills/{id}/versions
    // /api/v1/skills/{id}/versions/{ver}/upload
    // /api/v1/skills/{id}/versions/{ver}/finalize
    // /api/v1/skills/{id}/versions/{ver}/download
}

func (s *Server) handleSkillResolve(w http.ResponseWriter, r *http.Request) {
    // POST only — batch resolve skill URIs to download URLs
}
```

### File transfer

Skill file upload/download reuses the existing `pkg/storage` signed-URL infrastructure, mirroring `template_file_handlers.go`:

- **Storage path**: `skills/<scope>/<scopeID>/<slug>/<version>/`
- **Signed URL expiry**: `SignedURLExpiry` (15 minutes), same as templates
- **Upload flow**: Create skill → request upload URLs → upload files → finalize
- **Download flow**: Resolve → get signed download URLs → download files

---

## 9. Hub Client Extension

```go
// In pkg/hubclient/skills.go

// SkillService handles skill bank operations.
type SkillService interface {
    // List returns skills matching the filter criteria.
    List(ctx context.Context, opts *ListSkillsOptions) (*ListSkillsResponse, error)

    // Get returns a skill by ID.
    Get(ctx context.Context, skillID string) (*Skill, error)

    // Create creates a new skill.
    Create(ctx context.Context, req *CreateSkillRequest) (*CreateSkillResponse, error)

    // Update updates skill metadata.
    Update(ctx context.Context, skillID string, req *UpdateSkillRequest) (*Skill, error)

    // Delete removes a skill.
    Delete(ctx context.Context, skillID string) error

    // PublishVersion publishes a new skill version.
    PublishVersion(ctx context.Context, skillID string, req *PublishVersionRequest) (*SkillVersion, error)

    // ListVersions lists all versions for a skill.
    ListVersions(ctx context.Context, skillID string) (*ListVersionsResponse, error)

    // RequestUploadURLs requests signed URLs for uploading skill files.
    RequestUploadURLs(ctx context.Context, skillID, version string, files []FileUploadRequest) (*UploadResponse, error)

    // Finalize finalizes a skill version after file upload.
    Finalize(ctx context.Context, skillID, version string, manifest *SkillManifest) (*SkillVersion, error)

    // RequestDownloadURLs requests signed download URLs.
    RequestDownloadURLs(ctx context.Context, skillID, version string) (*DownloadResponse, error)

    // Resolve batch-resolves skill URIs to download URLs.
    Resolve(ctx context.Context, req *ResolveSkillsRequest) (*ResolveSkillsResponse, error)

    // UploadFile uploads a file to a signed URL.
    UploadFile(ctx context.Context, url, method string, headers map[string]string, content io.Reader) error

    // DownloadFile downloads a file from a signed URL.
    DownloadFile(ctx context.Context, url string) ([]byte, error)
}
```

---

## 10. Provisioning Integration

### Where in the pipeline

Skill resolution slots into `ProvisionAgent` (`pkg/agent/provision.go`) between step 3 (local skills copy) and step 4 (agent instructions injection):

```
Existing flow:
  Step 1: Directory setup
  Step 2: Copy template home → agentHome (overlay)
  Step 3: Copy skills directories into harness-specific location
  Step 4: Inject agent instructions
  Step 5: Inject system prompt
  Step 6: Harness provisioning (h.Provision())
  ...

New flow (step 3 expanded):
  Step 3a: Copy LOCAL harness-config skills → agentHome/<skillsDir>  (unchanged)
  Step 3b: Copy LOCAL template-chain skills → agentHome/<skillsDir>  (unchanged)
  Step 3c: [NEW] Resolve REFERENCED skills from merged ScionConfig.Skills
           - Collect SkillReference entries from finalScionCfg.Skills
           - Check broker-side cache first (content-hash keyed)
           - Call Hub POST /api/v1/skills/resolve with uncached URIs
           - Download resolved skill files via signed URLs
           - Place into agentHome/<skillsDir>/<name>/
           - Registry skills win on conflict with local skills
  Step 4: Inject agent instructions (unchanged)
  ...
```

### Implementation sketch

```go
// In pkg/agent/provision.go, after existing skills copy (line ~615):

if len(finalScionCfg.Skills) > 0 && hubClient != nil {
    resolved, err := resolveSkillReferences(ctx, hubClient, finalScionCfg.Skills,
        projectID, userID, skillsCachePath)
    if err != nil {
        return "", "", nil, fmt.Errorf("resolve skill references: %w", err)
    }

    for _, rs := range resolved {
        destName := rs.As
        if destName == "" {
            destName = rs.Name
        }
        dest := filepath.Join(agentHome, skillsDir, destName)
        if err := os.MkdirAll(dest, 0755); err != nil {
            return "", "", nil, fmt.Errorf("create skill dir %s: %w", destName, err)
        }
        for _, f := range rs.Files {
            if err := downloadOrCacheSkillFile(ctx, f, filepath.Join(dest, f.Path), skillsCachePath); err != nil {
                if rs.Optional {
                    util.Debugf("ProvisionAgent: optional skill %s failed: %v", rs.URI, err)
                    continue
                }
                return "", "", nil, fmt.Errorf("download skill file %s/%s: %w", destName, f.Path, err)
            }
        }
    }
}
```

### Graceful degradation

When the Hub is unavailable (e.g., solo/local mode without Hub connectivity):
- If the broker cache contains a valid cached copy (content-hash matches), use it.
- If no cache and the skill is `optional: true`, skip silently with a debug log.
- If no cache and the skill is required, fail provisioning with a clear error.

### Container-script harness integration

For harnesses using `provisioner.type: container-script`, the `ProvisionManifest` is extended to include resolved skill metadata:

```go
// In pkg/harness/container_script_harness.go

type ProvisionInputs struct {
    Instructions   string `json:"instructions,omitempty"`
    SystemPrompt   string `json:"system_prompt,omitempty"`
    Telemetry      string `json:"telemetry,omitempty"`
    AuthCandidates string `json:"auth_candidates,omitempty"`
    MCPServers     string `json:"mcp_servers,omitempty"`
    ResolvedSkills string `json:"resolved_skills,omitempty"` // NEW
}
```

A `inputs/resolved-skills.json` file is staged into the harness bundle:

```json
{
  "skills": [
    {
      "name": "scion",
      "uri": "skill://scion/core/scion@^1.0",
      "resolved_version": "1.3.2",
      "content_hash": "sha256:abc123...",
      "installed_path": ".claude/skills/scion",
      "source": "registry"
    },
    {
      "name": "team-creation",
      "uri": null,
      "installed_path": ".claude/skills/team-creation",
      "source": "local"
    }
  ]
}
```

This lets `provision.py` scripts post-process skills if needed (e.g., transform for non-standard harness formats). The `scion_harness.py` helper gains:

```python
def read_resolved_skills(bundle_path):
    """Read the resolved skills manifest from inputs/."""
    path = os.path.join(bundle_path, "inputs", "resolved-skills.json")
    if not os.path.exists(path):
        return {"skills": []}
    return load_json(path)
```

**Builtin harnesses** (claude, gemini) do not need manifest changes — skills are placed directly into the correct `SkillsDir()` location by the Go provisioning code.

---

## 11. CLI Commands

New `scion skills` command group:

```bash
# Skill CRUD
scion skills list                              # List available skills
scion skills list --scope project              # List project-scoped skills
scion skills list --search "security"          # Search by name/description
scion skills show <name>                       # Show skill details + versions
scion skills publish <path> --scope core       # Publish from local directory
scion skills publish <path> --scope project    # Publish to current project
scion skills delete <name>                     # Delete skill

# Version management
scion skills versions <name>                   # List versions of a skill
scion skills publish <path> --version 1.2.0    # Publish specific version

# Scaffolding
scion skills create <name>                     # Scaffold new skill directory

# Resolution (debug)
scion skills resolve <uri>                     # Test-resolve a skill URI

# Registry management (Phase 2)
scion skills registries list                   # List federated registries
scion skills registries add <url>              # Add external registry
```

These commands must be added to `cmd/` using Cobra and registered in the CLI mode allow-lists in `cmd/cli_mode.go`.

---

## 12. Scoped Visibility Model

### Scope hierarchy

| Scope | Who can publish | Who can consume | Use case |
|-------|----------------|-----------------|----------|
| `core` | Hub administrators only | All users | Scion first-party skills |
| `global` | Any authenticated user | All users | Community-published skills |
| `project` | Project members | Project members | Team-specific skills |
| `user` | The owning user | The owning user | Personal/experimental skills |

### Visibility enforcement

- `core` and `global` skills are always visible to all authenticated users.
- `project` skills require project membership (follows existing project access patterns).
- `user` skills are only visible to their owner.

### Resolution precedence

For bare-name lookups, narrowest scope wins: `user > project > global > core`.

---

## 13. Versioning and Content-Hash Integrity

### Semantic versioning

Skills use [Semantic Versioning 2.0](https://semver.org/):
- **MAJOR**: Breaking changes to skill behavior or interface
- **MINOR**: New capabilities, backward-compatible
- **PATCH**: Bug fixes, documentation improvements

### Version lifecycle

```
published → deprecated → archived
```

- **published**: Active, resolvable version.
- **deprecated**: Still resolvable but emits a warning. Points to replacement.
- **archived**: No longer resolvable.

### Immutability

Once published, a version's content cannot be modified. To fix a bug, publish a new patch version.

### Content hash verification

Every `SkillVersion` has a `ContentHash` — the SHA-256 of the sorted, concatenated file hashes (same algorithm used for `Template.ContentHash`):

```go
func computeSkillContentHash(files []SkillFile) string {
    sort.Slice(files, func(i, j int) bool {
        return files[i].Path < files[j].Path
    })
    h := sha256.New()
    for _, f := range files {
        h.Write([]byte(f.Hash))
    }
    return hex.EncodeToString(h.Sum(nil))
}
```

During provisioning, after downloading, the broker computes the hash of downloaded files and compares with the resolved `contentHash`. Mismatch → provisioning fails with a clear error.

---

## 14. Broker-Side Caching

### Cache layout

```
~/.scion/cache/skills/<scope>/<slug>/<version>/
├── SKILL.md
├── scripts/
└── .cache-meta.json    # { contentHash, fetchedAt, size }
```

### Cache key

`(scope, slug, version, contentHash)` — the content hash makes the cache content-addressed.

### Cache policy

| Version type | Cache TTL |
|-------------|-----------|
| Pinned version (e.g., `1.2.3`) | Infinite (content-addressed, immutable) |
| `latest` tag | 1 hour (re-resolves, but downloads only if content hash changed) |
| `^1.0` / `~1.2` | 1 hour (same as `latest`) |
| `sha256:...` | Infinite (content-addressed) |

### Cache size management

- Configurable per-broker, default 500 MB total.
- LRU eviction when limit is reached.
- `scion cache clear skills` CLI command for manual clearing.

### Resolution cache (Hub-side)

The Hub caches version resolution results (e.g., `^1.0` → `1.3.2`) with a 5-minute TTL to reduce database queries during high-volume provisioning bursts.

---

## 15. Federation to External Registries

### Phase 2 feature

Federation allows the Hub to proxy skill resolution to external registries:

```yaml
# Hub server configuration
skill_registries:
  - url: https://registry.agentskills.io
    trust: verified     # Only verified/signed skills
  - url: https://skills.corp.internal
    trust: trusted      # Trust all from this registry
```

### Resolution flow for federated URIs

1. Template contains `uri: skill://registry.agentskills.io/anthropic/claude-review@^2.0`
2. Hub recognizes the registry is federated, not internal
3. Hub proxies the resolution request to the external registry
4. External registry returns signed download URLs
5. Hub passes URLs through to the broker (or re-proxies for trust boundaries)

### External registry protocol

A minimal HTTP protocol:

```
GET  /api/v1/skills?name=<name>&scope=<scope>          → list
GET  /api/v1/skills/<id>/versions/<ver>/download        → download URLs
POST /api/v1/skills/resolve                              → batch resolve
```

External registries must implement at least the `resolve` endpoint. Full CRUD is optional.

### Trust model

| Trust level | Behavior |
|-------------|----------|
| `trusted` | All skills accepted |
| `verified` | Only signed skills accepted (signature verification) |
| `pinned` | Only skills with exact content hashes from the template |

---

## 16. Security Considerations

### Execution sandbox

Skills are injected as *files* into the agent container. They gain no privileges beyond what the harness grants to skill-referenced scripts. Scripts run in the same container sandbox.

### Scope restrictions

- `core` — Hub admin only
- `global` — any authenticated user (subject to Hub allow-list)
- `project` — project members only
- `user` — owning user only

### Content integrity

SHA-256 content hashes verified at every transfer boundary:
1. Author uploads → Hub computes and stores hash
2. Hub resolves → includes hash in response
3. Broker downloads → recomputes and verifies hash

### Supply chain

- All skills from federated registries go through the Hub's trust configuration
- Skill content is immutable once published (no silent updates)
- Content-addressed mode (`sha256:...`) provides strongest guarantee

---

## 17. Implementation Plan

### Overview

The implementation is broken into phases. Phase 1 builds the core registry and is decomposed into work packages that can be parallelized. Phases 2 and 3 are follow-on work that builds on Phase 1.

```
Phase 1 (Core Registry)
├── 1A: Types & Schema  ──────┐
├── 1B: Store Layer      ─────┤  (1A must complete first; 1B–1D can then parallelize)
├── 1C: Hub API + Client ─────┤
├── 1D: CLI Commands     ─────┘
├── 1E: Provisioning Integration  (requires 1A + 1C)
├── 1F: Container-Script Support  (requires 1E)
├── 1G: Seed & Migrate            (requires 1B + 1C + 1E)
│
Phase 2 (Caching + Federation) ── requires Phase 1 complete
Phase 3 (Discovery + Governance) ── requires Phase 2 complete
```

---

### Phase 1A: Types & Schema Definition

**Covers**: Core type definitions and database migration — the foundation everything else depends on.

**Work items**:
1. Add `SkillReference` type to `pkg/api/types.go`
2. Add `Skills []SkillReference` field to `ScionConfig` in `pkg/api/types.go`
3. Add `Skill`, `SkillVersion`, `SkillFile` model types to `pkg/store/models.go`
4. Add `SkillScope*` and `SkillStatus*` constants to `pkg/store/models.go`
5. Add `SkillStore` interface to `pkg/store/store.go`, compose into `Store`
6. Add `SkillFilter` type to `pkg/store/store.go`
7. Write `migrationV51` (skills + skill_versions tables) in `pkg/store/sqlite/sqlite.go`

**Files created**: None (all modifications to existing files)
**Files modified**: `pkg/api/types.go`, `pkg/store/models.go`, `pkg/store/store.go`, `pkg/store/sqlite/sqlite.go`
**Estimated complexity**: Small — primarily type definitions and SQL DDL. ~200 lines of new code.
**Parallelism**: **Must complete before 1B, 1C, 1D, 1E.** This is the dependency root — all other Phase 1 packages import these types.

---

### Phase 1B: Store Layer Implementation

**Covers**: SQLite implementation of `SkillStore` interface — CRUD for skills and versions, version resolution logic.

**Work items**:
1. Implement `SkillStore` interface in `pkg/store/sqlite/skills.go`
   - `CreateSkill`, `GetSkill`, `GetSkillBySlug`, `UpdateSkill`, `DeleteSkill`, `ListSkills`
   - `CreateSkillVersion`, `GetSkillVersion`, `ListSkillVersions`
   - `ResolveSkillVersion` — semver constraint resolution (`^1.0` → `1.3.2`, `latest` → highest published)
2. Write store tests in `pkg/store/sqlite/skills_test.go`
3. Add stub/no-op implementations to `pkg/store/entadapter/` composite store if needed

**Files created**: `pkg/store/sqlite/skills.go`, `pkg/store/sqlite/skills_test.go`
**Files modified**: Possibly `pkg/store/entadapter/composite.go`
**Estimated complexity**: Medium — the semver resolution logic is the non-trivial part. ~400–500 lines of implementation + tests.
**Parallelism**: **Requires 1A.** Can run **in parallel with 1C and 1D** after 1A completes.

---

### Phase 1C: Hub API & Hub Client

**Covers**: Hub-side HTTP handlers for skill CRUD, file transfer (signed URLs), batch resolution endpoint, and the corresponding client-side service.

**Work items**:
1. Hub handler types in `pkg/hub/skill_handlers.go`:
   - `CreateSkillRequest`, `CreateSkillResponse`, `ResolveSkillsRequest`, `ResolveSkillsResponse`
   - `handleSkills` (list/create dispatch), `handleSkillRoutes` (get/update/delete + sub-routes)
   - `handleSkillResolve` (batch resolution endpoint)
2. File transfer handlers in `pkg/hub/skill_file_handlers.go`:
   - Upload URL generation, finalize, download URL generation
   - Reuses `pkg/storage` signed-URL infrastructure (same pattern as `template_file_handlers.go`)
3. Route registration in `pkg/hub/server.go`:
   - `/api/v1/skills`, `/api/v1/skills/`, `/api/v1/skills/resolve`
4. Hub client service in `pkg/hubclient/skills.go`:
   - `SkillService` interface + `skillService` implementation
   - `List`, `Get`, `Create`, `Update`, `Delete`
   - `PublishVersion`, `ListVersions`
   - `RequestUploadURLs`, `Finalize`, `RequestDownloadURLs`
   - `Resolve` (batch resolution)
   - `UploadFile`, `DownloadFile`
5. Hub handler tests in `pkg/hub/skill_handlers_test.go`
6. Hub client tests in `pkg/hubclient/skills_test.go`

**Files created**: `pkg/hub/skill_handlers.go`, `pkg/hub/skill_file_handlers.go`, `pkg/hub/skill_handlers_test.go`, `pkg/hubclient/skills.go`, `pkg/hubclient/skills_test.go`
**Files modified**: `pkg/hub/server.go`
**Estimated complexity**: Large — this is the biggest single work package. ~800–1000 lines. The handler and client code is mostly mechanical (following template patterns), but the batch resolve endpoint has real logic.
**Parallelism**: **Requires 1A.** Can run **in parallel with 1B and 1D** after 1A completes. (Hub handlers call the store interface; during development, the store implementation from 1B just needs to exist — tests can use a test store.)

---

### Phase 1D: CLI Commands

**Covers**: `scion skills` command group — the user-facing CLI for skill management.

**Work items**:
1. Create `cmd/skills.go` with Cobra commands:
   - `scion skills list [--scope <scope>] [--search <query>]`
   - `scion skills show <name>`
   - `scion skills publish <path> --scope <scope> [--version <ver>]`
   - `scion skills delete <name>`
   - `scion skills versions <name>`
   - `scion skills create <name>` (scaffold)
   - `scion skills resolve <uri>` (debug)
2. Register in CLI mode allow-lists in `cmd/cli_mode.go`
3. Wire commands to `hubclient.SkillService`

**Files created**: `cmd/skills.go`
**Files modified**: `cmd/cli_mode.go`
**Estimated complexity**: Medium — Cobra command boilerplate is mechanical, but publish flow (upload files via signed URLs) requires care. ~400–500 lines.
**Parallelism**: **Requires 1A.** Can run **in parallel with 1B and 1C** after 1A completes. (CLI calls the hub client; the client implementation from 1C just needs the interface — mocks suffice during development.)

---

### Phase 1E: Provisioning Integration

**Covers**: Inject skill resolution into the `ProvisionAgent` pipeline — the critical path that makes skills actually work end-to-end.

**Work items**:
1. URI parsing: implement `ParseSkillURI()` function to parse `skill://<registry>/<scope>/<name>@<version>` and shorthand forms
2. Skill resolution: implement `resolveSkillReferences()` in `pkg/agent/provision.go`
   - Collect `SkillReference` entries from `finalScionCfg.Skills`
   - Call `hubClient.Skills().Resolve()` with batch of URIs
   - Download resolved files via signed URLs into `agentHome/<skillsDir>/<name>/`
   - Handle `optional: true` (skip on failure) vs required (fail provisioning)
   - Content-hash verification after download
3. Wire into `ProvisionAgent` after existing skills copy (after line ~615)
4. Add provision tests in `pkg/agent/provision_test.go`

**Files created**: None (possibly a `pkg/agent/skill_resolve.go` to keep provision.go manageable)
**Files modified**: `pkg/agent/provision.go`
**Estimated complexity**: Medium — URI parsing and download logic. ~300–400 lines. Integration testing requires a mock hub client.
**Parallelism**: **Requires 1A and 1C** (needs hub client `Resolve` method). Cannot run until 1C's `SkillService` interface is at least defined (implementation can be mocked).

---

### Phase 1F: Container-Script Harness Support

**Covers**: Extend the container-script provisioning manifest so `provision.py` scripts can see which skills were resolved.

**Work items**:
1. Add `ResolvedSkills` field to `ProvisionInputs` in `pkg/harness/container_script_harness.go`
2. Stage `inputs/resolved-skills.json` file during `Provision()` method
3. Add `read_resolved_skills()` helper to `scion_harness.py`
4. Update harness tests

**Files modified**: `pkg/harness/container_script_harness.go`, embedded `scion_harness.py`
**Estimated complexity**: Small — straightforward manifest extension. ~100–150 lines.
**Parallelism**: **Requires 1E** (needs resolved skills data to stage). Serial after 1E.

---

### Phase 1G: Seed Data & Template Migration

**Covers**: Publish existing first-party skills to the Hub and update default templates to reference them.

**Work items**:
1. Publish `skills/scion` and `skills/team-creation` to `core` scope (either via CLI or a bootstrap function similar to `template_bootstrap.go`)
2. Update default templates in `pkg/config/embeds/templates/` to use `skills:` references instead of local copies
3. Verify end-to-end flow: create agent → skills resolved from Hub → agent has skills

**Files modified**: Template YAML files in `pkg/config/embeds/`, possibly `pkg/hub/template_bootstrap.go` (for skill seeding)
**Estimated complexity**: Small — mostly configuration changes. ~100 lines.
**Parallelism**: **Requires 1B, 1C, and 1E** all complete (needs working store, API, and provisioning). This is the final integration step.

---

### Phase 1 summary

| Package | Est. size | Depends on | Can parallel with |
|---------|-----------|------------|-------------------|
| **1A**: Types & Schema | Small (~200 LOC) | — | Nothing (root) |
| **1B**: Store Layer | Medium (~450 LOC) | 1A | 1C, 1D |
| **1C**: Hub API + Client | Large (~900 LOC) | 1A | 1B, 1D |
| **1D**: CLI Commands | Medium (~450 LOC) | 1A | 1B, 1C |
| **1E**: Provisioning | Medium (~350 LOC) | 1A, 1C | — |
| **1F**: Container-Script | Small (~125 LOC) | 1E | — |
| **1G**: Seed & Migrate | Small (~100 LOC) | 1B, 1C, 1E | — |

**Critical path**: 1A → 1C → 1E → 1F → 1G
**Maximum parallelism**: After 1A, three agents can work on 1B, 1C, 1D simultaneously.
**Total estimated new code**: ~2,500–2,800 lines (implementation + tests).

```
Time →
1A ████
       1B ████████        (parallel)
       1C ████████████    (parallel, largest)
       1D ████████        (parallel)
              1E ████████  (after 1A+1C interface)
                    1F ███ (after 1E)
                       1G ██ (after 1B+1C+1E)
```

---

### Phase 2: Caching + Federation

**Requires**: Phase 1 complete.

| Package | Covers | Est. complexity | Parallelism |
|---------|--------|-----------------|-------------|
| **2A**: Broker cache | Content-addressed cache at `~/.scion/cache/skills/`, LRU eviction, cache-hit path in provisioning | Medium | Can parallel with 2B |
| **2B**: Federation proxy | External registry protocol, Hub proxy for federated URIs, trust configuration | Large | Can parallel with 2A |
| **2C**: Registry CLI | `scion skills registries list/add` commands | Small | Requires 2B |

---

### Phase 3: Discovery + Governance

**Requires**: Phase 2 complete.

| Package | Covers | Est. complexity | Parallelism |
|---------|--------|-----------------|-------------|
| **3A**: Web UI | Skill search/browse in React frontend | Large | Can parallel with 3B |
| **3B**: Governance | Signature verification, review workflow, deprecation notifications | Medium | Can parallel with 3A |
| **3C**: Analytics | Usage tracking (which skills consumed, by which templates) | Small | Requires 3A |

---

### Backward compatibility

- **Local skills continue to work**: The `skills/` directory in templates is unchanged. No migration required for existing templates.
- **Gradual adoption**: Templates can mix local skills and URI references. No flag day.
- **Solo mode**: When no Hub is connected, only local skills work. URI references fail (gracefully if `optional: true`).

---

## 18. Resolved Design Decisions

Decisions from reviewer feedback (2026-05-12):

1. **Harness-specific skill variants**: **No.** Skills ship universal `SKILL.md` only. No harness-specific variants (`SKILL.claude.md`, etc.). Keeps the model simple — skills are harness-agnostic by definition.

2. **MCP server dependencies**: **No.** Skills are plain instruction bundles. No auto-detection or declaration of MCP server requirements. Skills that need specific MCP servers should document this in their `SKILL.md` for human/agent awareness.

3. **Lock file**: **Optional, future exploration.** A `skills.lock` for deterministic provisioning is not required in Phase 1. Can be explored as a follow-up if teams need reproducible builds.

4. **Template inheritance merging**: **Flat lists for now.** Template-to-template inheritance is not yet implemented, so the `skills:` field is a simple flat list per template. No merge/override logic needed. **Future item**: revisit when template inheritance is tackled.

5. **`core` scope unification**: **Deferred.** (See §18.1 below for context on what this question meant.) Not needed for Skill Bank v1.

6. **Offline/solo mode**: **Not needed.** No special handling for offline version resolution. When Hub is unavailable, URI references fail (gracefully if `optional: true`). Solo mode uses local skills only.

7. **Skill size limits**: **Yes, 10 MB per version.** Enforced at upload time. More than sufficient for text-based instructions and scripts.

### 18.1 Context: "core" scope question

For reference, the question was: templates today use three scopes (`global`, `project`, `user` — defined as `TemplateScopeGlobal`, `TemplateScopeProject`, `TemplateScopeUser` in `pkg/store/models.go`). The Skill Bank design introduces a fourth scope — `core` — for Scion-maintained first-party skills that only Hub admins can publish. The question was whether templates should also gain this `core` scope to keep the scoping models consistent. Decision: not needed for v1, skills and templates can have different scope sets.

---

## 19. Worked Examples

### 19.1 Before and after: template author

**Before (today):**
```
.scion/templates/web-dev/
├── scion-agent.yaml
├── agents.md
└── skills/
    └── scion/            # Manually copied from skills/scion/
        └── SKILL.md
        └── scripts/
            └── ...
```

**After (with Skill Bank):**
```yaml
# .scion/templates/web-dev/scion-agent.yaml
schema_version: "1"
description: "Web development agent"
agent_instructions: agents.md
skills:
  - uri: skill://scion/core/scion@^1.0
  - uri: skill://scion/core/team-creation@^1.0
```

No local `skills/` directory needed. Skills are fetched from Hub at provision time.

### 19.2 Mixed local and registry skills

```yaml
# scion-agent.yaml
skills:
  - uri: skill://scion/core/scion@^1.0
  - uri: skill://project/custom-lint@latest
```

Plus a local skill:
```
.scion/templates/custom/
├── scion-agent.yaml
└── skills/
    └── my-local-skill/
        └── SKILL.md
```

**Provision-time resolution:**
1. Copy `my-local-skill/` → `agentHome/.claude/skills/my-local-skill/`
2. Resolve `skill://scion/core/scion@^1.0` → download → `agentHome/.claude/skills/scion/`
3. Resolve `skill://project/custom-lint@latest` → download → `agentHome/.claude/skills/custom-lint/`

### 19.3 Container-script harness

```yaml
# harness-config config.yaml
harness: amp
provisioner:
  type: container-script
  interface_version: 1
skills_dir: .config/amp/skills
```

```yaml
# template scion-agent.yaml
default_harness_config: amp
skills:
  - uri: skill://scion/core/scion@^1.0
```

**Flow:**
1. Go resolves skills → downloads to `agentHome/.config/amp/skills/scion/`
2. Go calls `h.Provision()` → stages bundle to `agentHome/.scion/harness/`
3. Bundle includes `inputs/resolved-skills.json`
4. Container starts → pre-start hook runs `provision.py`
5. `provision.py` can read manifest if it needs to transform skills
6. Amp CLI discovers skills in `~/.config/amp/skills/`

### 19.4 Publishing a skill

```bash
# Create skill scaffold
scion skills create security-audit
# ... edit security-audit/SKILL.md ...

# Publish to project scope
scion skills publish ./security-audit --scope project --version 1.0.0

# Other team members can now reference it:
#   - uri: skill://project/security-audit@^1.0
```

---

## Appendix A: File Map

Files to create or modify, organized by implementation phase.

### New files
| File | Phase | Purpose |
|------|-------|---------|
| `pkg/store/sqlite/skills.go` | 1B | SQLite implementation of `SkillStore` |
| `pkg/store/sqlite/skills_test.go` | 1B | Store tests |
| `pkg/hub/skill_handlers.go` | 1C | Hub API handlers for skill CRUD + resolve |
| `pkg/hub/skill_file_handlers.go` | 1C | Signed-URL upload/download handlers |
| `pkg/hub/skill_handlers_test.go` | 1C | Handler tests |
| `pkg/hubclient/skills.go` | 1C | `SkillService` interface + implementation |
| `pkg/hubclient/skills_test.go` | 1C | Client tests |
| `cmd/skills.go` | 1D | CLI command definitions |
| `pkg/agent/skill_resolve.go` | 1E | URI parsing + skill resolution logic (optional split from provision.go) |

### Modified files
| File | Phase | Change |
|------|-------|--------|
| `pkg/api/types.go` | 1A | Add `SkillReference` type, `Skills` field to `ScionConfig` |
| `pkg/store/models.go` | 1A | Add `Skill`, `SkillVersion`, `SkillFile` types, scope/status constants |
| `pkg/store/store.go` | 1A | Add `SkillStore` interface, `SkillFilter`, compose into `Store` |
| `pkg/store/sqlite/sqlite.go` | 1A | Add `migrationV51` to migration list |
| `pkg/hub/server.go` | 1C | Register `/api/v1/skills` routes |
| `cmd/cli_mode.go` | 1D | Register `skills` command in mode allow-lists |
| `pkg/agent/provision.go` | 1E | Add `resolveSkillReferences()` call after line ~615 |
| `pkg/harness/container_script_harness.go` | 1F | Add `ResolvedSkills` to `ProvisionInputs` |
| Template YAMLs in `pkg/config/embeds/` | 1G | Add `skills:` references to default templates |

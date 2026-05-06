# Skill Bank: Late-Binding Skills System for Scion

## 1. Executive Summary

This design introduces a **Skill Bank** — a centralized registry and late-binding resolution system for agent skills. Today, skills are static files baked into templates at authoring time and copied into agent containers during provisioning. The Skill Bank decouples skill *authorship* from skill *consumption*: templates declare skill **references** (URIs) in their `scion-agent.yaml`, and the provisioning system resolves those references at agent-creation time by fetching skill content from either the Hub's internal registry or a federated external registry.

### Goals

1. **Reuse without duplication** — A skill authored once can be consumed by many templates without copy-paste.
2. **Late binding** — Templates pin a skill version constraint, but resolution happens at provision time, enabling controlled updates.
3. **Federated discovery** — Support both Hub-hosted skills and external registries (e.g., community skill catalogs, corporate internal registries).
4. **Harness agnosticism** — Skills remain portable across harnesses; the Skill Bank stores harness-agnostic SKILL.md content.
5. **Trust and governance** — Scoped visibility, content hashing, and optional signature verification.

### Non-Goals (v1)

- Skill *execution* changes — harnesses continue to discover skills in their native `skills_dir` location.
- Dependency resolution between skills — skills are independent units.
- Runtime skill hot-loading — skills are resolved and injected at provision time only.

---

## 2. Current State

### How skills work today

1. **Authoring**: A skill is a directory containing `SKILL.md` (with YAML frontmatter: `name`, `description`) plus optional supporting files. Follows the [agentskills.io](https://agentskills.io/) standard.

2. **Placement**: Skills live in two locations:
   - **Harness-config level**: `~/.scion/harness-configs/<name>/skills/` — common to all agents using that harness-config.
   - **Template level**: `.scion/templates/<name>/skills/` — role-specific skills for that template.

3. **Provisioning** (`pkg/agent/provision.go`, lines 575–615): During `ProvisionAgent`, the system:
   - Resolves the harness via `harness.Resolve()` to get `SkillsDir()` (e.g., `.claude/skills`)
   - Copies harness-config skills into `agentHome/<skillsDir>/`
   - Overlays template-chain skills (later templates win on conflict)

4. **Container-script provisioning**: For `provisioner.type: container-script` harnesses, the Go-side `Provision()` stages the entire harness bundle into `agentHome/.scion/harness/`. The skills directory is populated *before* `Provision()` runs (step 3 above), so the provision.py script can read/transform skills if needed. The staged bundle does *not* duplicate skills — they're already in the harness-native location.

### Limitations

- **Copy-paste proliferation**: The same skill (e.g., `scion`, `team-creation`) must be physically present in every template or harness-config that needs it.
- **No versioning**: When a shared skill is updated, all copies must be manually synchronized.
- **No discovery**: Users cannot browse available skills or search for capabilities.
- **No sharing boundary**: Skills cannot be shared across groves or published to a community.

---

## 3. Design

### 3.1 Template YAML Schema Extension

Add a `skills` field to `scion-agent.yaml` that declares skill references alongside (or instead of) the existing `skills/` directory:

```yaml
schema_version: "1"
description: "Web development agent"
agent_instructions: agents.md

# NEW: Skill references resolved at provision time
skills:
  # Hub-hosted skill (internal registry)
  - uri: skill://scion/core/scion@^1.0
  - uri: skill://scion/core/team-creation@^1.0

  # Grove-scoped skill
  - uri: skill://grove/security-audit@latest

  # User-scoped skill
  - uri: skill://user/my-custom-workflow@1.2.0

  # Federated external skill
  - uri: skill://registry.agentskills.io/anthropic/claude-review@^2.0

  # Inline override: fetch from registry but rename in container
  - uri: skill://scion/core/scion@^1.0
    as: agent-management
```

**Schema additions to `ScionConfig`:**

```go
type ScionConfig struct {
    // ... existing fields ...
    
    // Skills declares skill references to resolve at provision time.
    // These are fetched from the Hub skill bank or federated registries
    // and placed into the harness skills directory alongside any
    // skills from the template's local skills/ directory.
    Skills []SkillReference `json:"skills,omitempty" yaml:"skills,omitempty" koanf:"skills"`
}

type SkillReference struct {
    // URI is the skill reference in the form:
    //   skill://<registry>/<scope>/<name>@<version>
    URI string `json:"uri" yaml:"uri" koanf:"uri"`
    
    // As optionally renames the skill directory in the container.
    // If empty, the skill's declared name is used.
    As string `json:"as,omitempty" yaml:"as,omitempty" koanf:"as"`
    
    // Optional controls whether provisioning fails if the skill
    // cannot be resolved. Default false (required).
    Optional bool `json:"optional,omitempty" yaml:"optional,omitempty" koanf:"optional"`
}
```

**Interaction with local `skills/` directory**: Local skills (files in the template's `skills/` directory) continue to work as today. Referenced skills from the `skills:` YAML field are resolved *after* local skills are copied, with registry skills winning on name conflict. This preserves backward compatibility while enabling the override pattern.

### 3.2 Skill URI Scheme

```
skill://<registry>/<scope>/<name>@<version>
```

**Components:**

| Component | Description | Examples |
|-----------|-------------|---------|
| `registry` | Registry hostname. `scion` is the alias for the connected Hub. | `scion`, `registry.agentskills.io`, `skills.corp.internal` |
| `scope` | Visibility/ownership scope within the registry. | `core`, `grove/<grove-id>`, `user/<username>` |
| `name` | Skill identifier (kebab-case). | `scion`, `team-creation`, `security-audit` |
| `version` | Version constraint or tag. | `1.0.0`, `^1.0`, `~1.2`, `latest`, `sha256:abc123` |

**Special registry aliases:**

| Alias | Resolves to | Description |
|-------|-------------|-------------|
| `scion` | Connected Hub endpoint | Hub's internal skill bank |
| `grove` | `scion` with scope=`grove/<current-grove>` | Current grove's skills |
| `user` | `scion` with scope=`user/<current-user>` | Current user's skills |

**Shorthand forms:**

```yaml
# Full form
- uri: skill://scion/core/scion@^1.0

# Shorthand: omit registry (defaults to "scion")
- uri: skill:///core/scion@^1.0

# Shorthand: omit version (defaults to "latest")
- uri: skill://scion/core/scion

# Shortest: bare name resolves via search order (core → grove → user)
- uri: scion
```

**Version resolution:**

| Syntax | Meaning |
|--------|---------|
| `1.2.3` | Exact version |
| `^1.2` | Compatible (≥1.2.0, <2.0.0) — semver caret |
| `~1.2` | Approximate (≥1.2.0, <1.3.0) — semver tilde |
| `latest` | Highest published version |
| `sha256:abcdef` | Content-addressed (immutable) |

### 3.3 Skill Content Model

A skill in the bank is a **directory bundle** — the same format used today, but with added metadata for registry management:

```
skill-name/
├── SKILL.md           # REQUIRED: frontmatter + content (agentskills.io format)
├── scripts/           # OPTIONAL: backing scripts referenced by SKILL.md
│   ├── analyze.sh
│   └── report.py
└── SKILL.lock         # OPTIONAL: generated, records resolved dependencies
```

**SKILL.md format** (unchanged from today):

```markdown
---
name: security-audit
description: >-
  Perform security audits on code changes, checking for OWASP Top 10
  vulnerabilities and generating actionable reports.
---

# Security Audit Skill

[Skill instructions and reference material...]
```

**Registry metadata** (stored in the Hub, not in SKILL.md):

```json
{
  "id": "uuid",
  "name": "security-audit",
  "version": "1.2.0",
  "scope": "core",
  "scopeId": "",
  "contentHash": "sha256:abc123...",
  "author": "user@example.com",
  "license": "Apache-2.0",
  "createdAt": "2026-05-01T00:00:00Z",
  "tags": ["security", "code-review"],
  "files": [
    {"path": "SKILL.md", "size": 4096, "hash": "sha256:..."},
    {"path": "scripts/analyze.sh", "size": 1024, "hash": "sha256:..."}
  ],
  "dependencies": [],
  "harness_compatibility": ["claude", "gemini", "opencode", "codex"]
}
```

### 3.4 Hub Skill Bank API

New API endpoints under `/api/v1/skills`, following the existing Hub pattern (cf. template handlers):

#### Endpoints

```
# Skill CRUD
POST   /api/v1/skills                          # Create skill
GET    /api/v1/skills                          # List skills (with filters)
GET    /api/v1/skills/{id}                     # Get skill by ID
PUT    /api/v1/skills/{id}                     # Update skill metadata
DELETE /api/v1/skills/{id}                     # Delete skill

# Versions
POST   /api/v1/skills/{id}/versions            # Publish new version
GET    /api/v1/skills/{id}/versions            # List versions
GET    /api/v1/skills/{id}/versions/{version}  # Get specific version

# File transfer (follows template pattern with signed URLs)
POST   /api/v1/skills/{id}/versions/{version}/upload    # Request upload URLs
POST   /api/v1/skills/{id}/versions/{version}/finalize  # Finalize upload
GET    /api/v1/skills/{id}/versions/{version}/download   # Request download URLs

# Resolution (provision-time endpoint)
POST   /api/v1/skills/resolve                  # Batch-resolve skill URIs to download URLs

# Federation
GET    /api/v1/skills/registries               # List configured external registries
POST   /api/v1/skills/registries               # Add external registry
```

#### Batch Resolution Endpoint

The critical endpoint for provisioning — resolves multiple skill URIs in a single call:

```
POST /api/v1/skills/resolve

Request:
{
  "skills": [
    {"uri": "skill://scion/core/scion@^1.0"},
    {"uri": "skill://scion/grove/security-audit@latest"},
    {"uri": "skill://registry.agentskills.io/anthropic/claude-review@^2.0"}
  ],
  "groveId": "grove-uuid",
  "userId": "user-uuid"
}

Response:
{
  "resolved": [
    {
      "uri": "skill://scion/core/scion@^1.0",
      "resolvedVersion": "1.3.2",
      "contentHash": "sha256:abc123...",
      "files": [
        {"path": "SKILL.md", "url": "https://storage.../signed-url", "expires": "..."},
        {"path": "scripts/manage.sh", "url": "https://storage.../signed-url", "expires": "..."}
      ]
    },
    {
      "uri": "skill://registry.agentskills.io/anthropic/claude-review@^2.0",
      "resolvedVersion": "2.1.0",
      "contentHash": "sha256:def456...",
      "federated": true,
      "files": [
        {"path": "SKILL.md", "url": "https://registry.agentskills.io/.../signed-url", "expires": "..."}
      ]
    }
  ],
  "errors": []
}
```

#### Store Interface

```go
type SkillStore interface {
    CreateSkill(ctx context.Context, skill *Skill) error
    GetSkill(ctx context.Context, id string) (*Skill, error)
    ListSkills(ctx context.Context, filter SkillFilter) ([]*Skill, error)
    UpdateSkill(ctx context.Context, id string, update *SkillUpdate) error
    DeleteSkill(ctx context.Context, id string) error
    
    CreateSkillVersion(ctx context.Context, version *SkillVersion) error
    GetSkillVersion(ctx context.Context, skillID, version string) (*SkillVersion, error)
    ListSkillVersions(ctx context.Context, skillID string) ([]*SkillVersion, error)
    ResolveSkillVersion(ctx context.Context, skillID, constraint string) (*SkillVersion, error)
}

type SkillFilter struct {
    Name    string
    Scope   string
    ScopeID string
    Tags    []string
    Search  string
}
```

### 3.5 CLI Commands

```bash
# Skill management
scion skills list                              # List available skills
scion skills list --scope grove                # List grove-scoped skills
scion skills show <name>                       # Show skill details + versions
scion skills publish <path> --scope core       # Publish skill from local directory
scion skills publish <path> --scope grove      # Publish to current grove
scion skills delete <name>                     # Delete skill

# Version management
scion skills versions <name>                   # List versions
scion skills publish <path> --version 1.2.0    # Publish specific version

# Skill creation scaffolding
scion skills create <name>                     # Create skill directory scaffold

# Registry management
scion skills registries list                   # List federated registries
scion skills registries add <url>              # Add external registry

# Resolution (debug)
scion skills resolve <uri>                     # Test-resolve a skill URI
```

### 3.6 Provisioning-Time Resolution and Injection

The skill resolution phase integrates into the existing provisioning pipeline in `pkg/agent/provision.go`, after local skills are copied (step 3) and before content injection (step 4):

```
Existing flow:
  1. Directory setup
  2. Config resolution & merging
  3. Home directory composition (harness-config home → template home → local skills)
  4. Content injection (instructions, system prompt)
  5. Harness provisioning
  6. Config persistence

New flow (step 3 expanded):
  3a. Copy harness-config base home → agentHome
  3b. Copy template home → agentHome (overlay)
  3c. Copy LOCAL skills from harness-config and template chain → agentHome/<skillsDir>
  3d. [NEW] Resolve REFERENCED skills from scion-agent.yaml skills: field
      - Collect all SkillReference entries from merged config
      - Call Hub /api/v1/skills/resolve with batch of URIs
      - Download resolved skill files via signed URLs
      - Place into agentHome/<skillsDir>/<name>/ (registry skills win on conflict)
  3e. Continue with content injection...
```

**Implementation sketch:**

```go
// In ProvisionAgent, after existing skills copy (line ~615):

if len(finalScionCfg.Skills) > 0 && hubClient != nil {
    resolvedSkills, err := resolveSkillReferences(ctx, hubClient, finalScionCfg.Skills, groveID, userID)
    if err != nil {
        return "", "", nil, fmt.Errorf("failed to resolve skill references: %w", err)
    }
    
    for _, rs := range resolvedSkills {
        destName := rs.As
        if destName == "" {
            destName = rs.Name
        }
        dest := filepath.Join(agentHome, skillsDir, destName)
        if err := os.MkdirAll(dest, 0755); err != nil {
            return "", "", nil, fmt.Errorf("failed to create skill dir %s: %w", destName, err)
        }
        for _, f := range rs.Files {
            if err := downloadSkillFile(ctx, f.URL, filepath.Join(dest, f.Path)); err != nil {
                if rs.Optional {
                    util.Debugf("ProvisionAgent: optional skill %s failed: %v", rs.URI, err)
                    continue
                }
                return "", "", nil, fmt.Errorf("failed to download skill file %s: %w", f.Path, err)
            }
        }
    }
}
```

### 3.7 Interaction with Container-Script Provisioning

For harnesses using `provisioner.type: container-script`, skill resolution must interact correctly with the staged-bundle model:

**Current container-script flow:**

1. Go-side `Provision()` stages the bundle into `agentHome/.scion/harness/` (config, manifest, provision.py, inputs/, outputs/)
2. Skills are *already* in `agentHome/<skillsDir>/` from step 3c (local skills copy happens before `Provision()`)
3. Pre-start hook runs `provision.py` inside the container
4. `provision.py` can read/transform files in the agent home, including skills

**Skill Bank integration with container-script:**

The key insight is that **skill resolution happens in Go, before container-script `Provision()`**. This means:

1. **Step 3c** (local skills) and **step 3d** (registry skills) both complete on the host/broker side
2. **Step 5** (`h.Provision()`) then stages the harness bundle — by this point, all skills are already in place
3. The container-script's `provision.py` can optionally post-process skills (e.g., transform SKILL.md for non-standard harnesses)

**Staged manifest extension**: The `ProvisionManifest` is extended to include resolved skill metadata so `provision.py` has visibility:

```json
{
  "schema_version": 1,
  "command": "provision",
  "inputs": {
    "instructions": "inputs/instructions.md",
    "resolved_skills": "inputs/resolved-skills.json"
  }
}
```

Where `inputs/resolved-skills.json` contains:

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
      "resolved_version": null,
      "content_hash": null,
      "installed_path": ".claude/skills/team-creation",
      "source": "local"
    }
  ]
}
```

This allows `provision.py` to:
- Know which skills were resolved from the registry vs. local
- Post-process skills for harness-specific transformations
- Validate that expected skills are present
- Skip re-processing for local skills that haven't changed

**The `scion_harness.py` helper** gains a new function:

```python
def read_resolved_skills(bundle_path):
    """Read the resolved skills manifest from inputs/."""
    path = os.path.join(bundle_path, "inputs", "resolved-skills.json")
    if not os.path.exists(path):
        return {"skills": []}
    return load_json(path)
```

**Builtin harness interaction**: For builtin harnesses (claude, gemini, etc.), skills are placed directly into the correct location by the Go provisioning code. No manifest extension is needed — the harness `Provision()` method does not need to know about skill provenance.

### 3.8 Scoping and Visibility

Skills follow the same scoping model as templates:

| Scope | Visibility | Use Case |
|-------|-----------|----------|
| `core` | All users on the Hub | Scion-maintained first-party skills |
| `global` | All users on the Hub | User-published skills visible to everyone |
| `grove` | Members of the grove | Team-specific skills |
| `user` | Only the publishing user | Personal/experimental skills |

**Resolution order** (when using bare names or the `scion` registry alias):

1. `user` scope — personal skills first
2. `grove` scope — team skills
3. `global` scope — Hub-wide published skills
4. `core` scope — Scion first-party skills (always available)

**Precedence for conflicts**: When the same skill name exists in multiple scopes, the narrowest scope wins (user > grove > global > core). Explicit scope in the URI bypasses this search.

### 3.9 Versioning

Skills use [Semantic Versioning 2.0](https://semver.org/):

- **MAJOR**: Breaking changes to skill behavior or interface
- **MINOR**: New capabilities, backward-compatible
- **PATCH**: Bug fixes, documentation improvements

**Version lifecycle:**

```
draft → published → deprecated → archived
```

- `draft`: Skill version uploaded but not finalized. Not resolvable.
- `published`: Active, resolvable version.
- `deprecated`: Still resolvable but emits a warning during resolution. Points to replacement.
- `archived`: No longer resolvable. Existing agents with cached copies continue to work.

**Immutability**: Once a version is published, its content cannot be modified. To fix a bug, publish a new patch version. Content hash verification ensures this invariant.

**`latest` tag**: Always resolves to the highest published (non-deprecated) semver version.

### 3.10 Caching

Skill content is cached at multiple levels to minimize download overhead:

**Broker-side cache** (primary):

```
~/.scion/cache/skills/<registry>/<scope>/<name>/<version>/
├── SKILL.md
├── scripts/
└── .skill-cache-meta.json    # contentHash, fetchedAt, expiresAt
```

- **Cache key**: `(registry, scope, name, version, contentHash)`
- **TTL**: 24 hours for `latest` tag, infinite for pinned versions (content-addressed)
- **Invalidation**: Content hash mismatch forces re-download
- **Size limit**: Configurable per-broker, default 500MB total skill cache

**Hub-side cache**: The Hub stores skill content in its object store (GCS bucket), same as templates. Signed URLs point to this storage.

**Resolution cache**: The Hub caches version resolution results (e.g., `^1.0` → `1.3.2`) with a short TTL (5 minutes) to reduce database queries during high-volume provisioning.

### 3.11 Security and Trust

#### Content Integrity

Every skill version has a `contentHash` (SHA-256 of the sorted, concatenated file hashes). During provisioning:

1. Hub returns `contentHash` in the resolution response
2. After downloading, the broker/provisioner computes the hash of downloaded files
3. Mismatch → provisioning fails with a clear error

#### Federated Registry Trust

External registries are explicitly configured and can be restricted:

```yaml
# In Hub settings or grove settings
skill_registries:
  - url: https://registry.agentskills.io
    trust: verified           # Only fetch verified/signed skills
    allowed_scopes: ["*"]     # All scopes
    
  - url: https://skills.corp.internal
    trust: trusted            # Trust all skills from this registry
    allowed_scopes: ["corp/*"]
```

Trust levels:
- `trusted`: All skills from this registry are accepted
- `verified`: Only skills with valid signatures are accepted
- `pinned`: Only skills with exact content hashes specified in the template are accepted

#### Execution Sandbox

Skills are injected as *files* into the agent container. They do not gain any privileges beyond what the harness grants to slash commands. Scripts referenced by skills run in the same container sandbox as the agent itself.

#### Scope Restrictions

- `core` skills can only be published by Hub administrators
- `global` skills can be published by any authenticated user (subject to review policies)
- `grove` skills can be published by grove members
- `user` skills can only be published by the owning user

### 3.12 Hub Client Extension

```go
// In pkg/hubclient/client.go
type Client interface {
    // ... existing services ...
    Skills() SkillService
}

// In pkg/hubclient/skills.go
type SkillService interface {
    List(ctx context.Context, opts *ListSkillsOptions) (*ListSkillsResponse, error)
    Get(ctx context.Context, skillID string) (*Skill, error)
    Create(ctx context.Context, req *CreateSkillRequest) (*Skill, error)
    Update(ctx context.Context, skillID string, req *UpdateSkillRequest) (*Skill, error)
    Delete(ctx context.Context, skillID string) error
    
    PublishVersion(ctx context.Context, skillID string, req *PublishVersionRequest) (*SkillVersion, error)
    ListVersions(ctx context.Context, skillID string) (*ListVersionsResponse, error)
    
    RequestUploadURLs(ctx context.Context, skillID, version string, files []FileUploadRequest) (*UploadResponse, error)
    Finalize(ctx context.Context, skillID, version string, manifest *SkillManifest) (*SkillVersion, error)
    RequestDownloadURLs(ctx context.Context, skillID, version string) (*DownloadResponse, error)
    
    Resolve(ctx context.Context, req *ResolveSkillsRequest) (*ResolveSkillsResponse, error)
}
```

---

## 4. Worked Examples

### 4.1 Template author references a shared skill

**Before (today):**
```
.scion/templates/web-dev/
├── scion-agent.yaml
├── agents.md
└── skills/
    └── scion/            # Copied from skills/scion/ manually
        └── SKILL.md
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

No local `skills/` directory needed. At provision time, the skills are fetched from the Hub.

### 4.2 Mixed local and registry skills

```yaml
# scion-agent.yaml
schema_version: "1"
description: "Custom agent with both local and shared skills"
agent_instructions: agents.md
skills:
  - uri: skill://scion/core/scion@^1.0
  - uri: skill://grove/proprietary-tool@latest
```

Plus a local skill in the template:
```
.scion/templates/custom-agent/
├── scion-agent.yaml
├── agents.md
└── skills/
    └── my-local-skill/
        └── SKILL.md
```

**Resolution order at provision time:**
1. Copy local `my-local-skill/` into `agentHome/.claude/skills/my-local-skill/`
2. Resolve `skill://scion/core/scion@^1.0` → download into `agentHome/.claude/skills/scion/`
3. Resolve `skill://grove/proprietary-tool@latest` → download into `agentHome/.claude/skills/proprietary-tool/`

### 4.3 Container-script harness with skill bank

For an Amp-like harness using `provisioner.type: container-script`:

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
schema_version: "1"
description: "Amp agent with shared skills"
default_harness_config: amp
skills:
  - uri: skill://scion/core/scion@^1.0
```

**Provisioning flow:**
1. Go resolves skills, downloads to `agentHome/.config/amp/skills/scion/`
2. Go calls `h.Provision()` which stages the harness bundle to `agentHome/.scion/harness/`
3. Bundle includes `inputs/resolved-skills.json` listing installed skills
4. Container starts → pre-start hook runs `provision.py`
5. `provision.py` can read `resolved-skills.json` if it needs to transform skills for Amp's format
6. Amp CLI starts, discovers skills in `~/.config/amp/skills/`

---

## 5. Migration Path

### Phase 1: Schema and Resolution (v1)
- Add `skills` field to `ScionConfig` and `scion-agent.yaml` schema
- Implement `SkillReference` parsing and URI scheme
- Add `resolveSkillReferences()` to provisioning pipeline
- Stage `inputs/resolved-skills.json` for container-script harnesses
- Hub API: skill CRUD, version management, batch resolution
- CLI: `scion skills list/show/publish/resolve`
- Seed `core` scope with existing `scion` and `team-creation` skills

### Phase 2: Caching and Federation (v2)
- Broker-side skill cache with content-hash validation
- External registry protocol specification
- Hub federation proxy (resolve external URIs via Hub)
- CLI: `scion skills registries`

### Phase 3: Discovery and Governance (v3)
- Skill search and browse (tags, descriptions, popularity)
- Signature verification for federated skills
- Skill deprecation and replacement pointers
- Usage analytics (which skills are most consumed)
- Skill review workflow for `global` scope publishing

---

## 6. Open Questions

1. **Should skills support harness-specific variants?** A skill could ship `SKILL.md` (universal) plus `SKILL.claude.md` and `SKILL.gemini.md` (harness-specific overrides). The provisioner would pick the best match. This adds complexity but enables harness-tuned instructions.

2. **Should skills declare MCP server dependencies?** A security-audit skill might require a specific MCP server. This could be modeled as metadata in the registry, with the provisioning system auto-injecting MCP config. Deferred to v2.

3. **Should the `skills:` field support inline skills?** For prototyping, `skills: [{name: "my-skill", content: "..."}]` could allow inline skill definitions without a local directory. This overlaps with `agents.md` and may not be worth the complexity.

4. **Lock file semantics**: Should templates commit a `skills.lock` that pins exact versions and content hashes? This would make provisioning deterministic and reproducible, at the cost of requiring explicit `scion skills update` to pick up new versions.

5. **Interaction with template inheritance**: When a base template declares skills and a derived template also declares skills, should they merge (union) or should the derived template's list replace the base's? Recommendation: merge with derived winning on name conflict, matching the existing overlay behavior for local `skills/` directories.

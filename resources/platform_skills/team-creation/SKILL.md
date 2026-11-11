---
name: team-creation
description: >-
  Create or extend scion agent team templates from a high-level description of roles and workflow.
  Use when the user describes a multi-agent team, panel, crew, pipeline, or any scenario
  requiring coordinated LLM agents with distinct roles. Also use when adding new roles to an
  existing team, modifying workflows, or restructuring agent coordination. Also use when porting
  or converting an agent pack from another host (a Cursor or Claude Code plugin, a skills repo with
  subagent definitions) into a Scion team. Produces ready-to-use template directories in
  .scion/templates/ by default, or in a custom path if specified.
---

# Scion Team Creation & Extension Skill

You create and extend **scion agent templates** — the blueprints that define specialized agent roles. Given a description of a team (roles, responsibilities, workflow), you produce template directories that can be used to start agents with `scion start <name> --type <template>`.

When extending an existing team, review the current templates to understand the established patterns, naming, and workflow before making changes.

## Template Directory Structure

Each template lives in `.scion/templates/<template-name>/` and contains:

```
.scion/templates/<template-name>/
├── scion-agent.yaml       # REQUIRED: template metadata and config
├── agents.md              # REQUIRED: agent behavioral instructions
├── system-prompt.md       # OPTIONAL: replaces the harness's default system prompt (see below)
└── skills/                # OPTIONAL: harness skills (agentskills.io standard)
    └── <skill-name>/
        └── SKILL.md
```

Custom templates automatically **inherit** from the built-in `default` template, which provides shell configs, git setup, and base scion infrastructure. You do NOT need to create a `home/` directory or duplicate any infrastructure files.

### Template Naming

- Directory names use **kebab-case**: `panel-judge`, `code-reviewer`, `team-lead`
- Names should be descriptive of the role, not the project

## Required File Formats

### scion-agent.yaml

Every template MUST have this file. Minimal valid config:

```yaml
schema_version: "1"
description: "Short description of this agent role"
agent_instructions: agents.md
```

Optional fields you may include when the role requires them:

```yaml
model: extra-large         # Default model: alias (small|medium|large|extra-large) or explicit ID.
                           # Lowest priority; `scion start --model` overrides it.
default_harness_config: claude   # A preference only; `scion start --harness-config` overrides it.
max_turns: 50              # Maximum conversation turns
max_duration: "30m"        # Maximum wall-clock time (e.g. "1h", "30m")

env:
  ROLE: "reviewer"

services:
  - name: chromium
    command: ["chromium", "--headless", "--no-sandbox", "--remote-debugging-port=9222"]
    restart: always
    ready_check:
      type: tcp
      target: "localhost:9222"
      timeout: "10s"

mcp_servers:
  github:
    transport: stdio
    command: github-mcp-server
    args: ["stdio"]

skills:
  - uri: "skill://scion/global/idea-refine@latest"
  - uri: "skill://scion/core/scion@^1.0"
    optional: true
```

An agent only has the MCP servers its template declares. Unlike an in-process subagent, it does not inherit its parent's servers.

### Fields to Never Use

These fields are deprecated or cause broken agent startup — never include them in generated `scion-agent.yaml` files:

| Field | Reason |
|---|---|
| `harness` | Deprecated — causes runtime error |
| `harness_config` | Unnecessary; project defaults apply automatically |
| `explicit_workspace` | Causes interactive theme prompt instead of running |
| `config_dir` | Breaks harness startup |

A minimal valid `scion-agent.yaml` needs only `schema_version`, `description`, and `agent_instructions`. Add only the fields listed under "Optional fields" above.

### agents.md (Agent Instructions)

This file contains the behavioral instructions injected into the agent's context. 

The primary contents should focus on task and job work describing what the agent should do, how it should behave, and any constraints on its work.

Do **not** include instructions on how to use the scion CLI (starting agents, messaging, `scion look`, etc.) — every agent automatically receives base CLI operating instructions from the platform.

### system-prompt.md (Optional; usually omit)

**Warning:** on harnesses whose `system_prompt_mode` is `native` (Claude Code among them), this file is passed as `--system-prompt` and **replaces the harness's entire default system prompt**, including its built-in tool-use guidance. Prefer putting the persona at the top of `agents.md`. Use `system-prompt.md` only when you deliberately want to replace the harness prompt.

Frames the agent's persona, expertise, and perspective — *who* the agent is, while `agents.md` shapes *what* it does. Example:

```markdown
# Expert Code Reviewer

You are a senior software engineer with deep expertise in code quality,
security, and maintainability. You approach code review methodically,
prioritizing correctness and clarity over style preferences.
```

If the role doesn't need a distinct persona, omit this file and remove the `system_prompt` line from `scion-agent.yaml`.

This file can contain broad direction around skillsets.

## Harness Skills

Templates can include a `skills/` directory containing reusable skill definitions that follow the [agentskills.io](https://agentskills.io/) standard. Each skill is a subdirectory with a `SKILL.md` file:

```
skills/
└── my-skill/
    └── SKILL.md
```

A `SKILL.md` file has YAML frontmatter with `name` and `description`, followed by markdown content:

```markdown
---
name: my-skill
description: >-
  Short description of what this skill does and when the agent should use it.
---

Skill instructions and reference material here.
```

Template-local skills and GitHub URI skills both keep their executable bits. Older Scion builds dropped exec bits and subdirectories for URI skills, so prefer running scripts as `bash x.sh` or `bun x.ts` rather than `./x`. Skills that might reach an older hub will still work.

Scion loads skills only from three sources: template `skills/`, platform skills, and URI references. It never loads skills from the agent's repo checkout.

During agent provisioning, Scion automatically merges skills from the template into the correct harness-specific location (e.g. `.claude/skills/` for Claude, `.gemini/skills/` for Gemini). The agent's LLM harness discovers and loads these skills at runtime.

Use skills to package domain knowledge, tool-usage patterns, or specialized workflows that a role needs. Skills are portable across harnesses and reusable across templates.

## Registry Skills (URI References)

In addition to local `skills/` directories, templates can declare skill dependencies from the skill bank registry using URI references in `scion-agent.yaml`. These skills are downloaded and installed at provision time.

### URI Formats

```
skill://<registry>/<scope>/<name>@<version>                          # skill bank (Hub registry)
gh://<owner>/<repo>/<name>[@<ref>]                                    # GitHub, <repo>/skills/<name> only
https://github.com/<owner>/<repo>/tree/<ref>/<path>                   # GitHub, any path (monorepos, plugins)
```

Pin GitHub refs to a commit SHA for skills you did not write. For scopes, version constraints, private-repo tokens, and resolution behavior, read `references/skill-uris.md`.

### Declaring Skills in scion-agent.yaml

Add a `skills:` list to `scion-agent.yaml`:

```yaml
skills:
  - uri: "skill://scion/global/idea-refine@latest"
  - uri: "skill://scion/core/scion@^1.0"
    optional: true        # provisioning succeeds even if this can't be resolved
  - uri: "skill://scion/global/code-reviewer@latest"
    as: "review"          # install under a different directory name
```

Each entry supports three fields:
- **`uri`** (required): The skill URI
- **`optional`** (default `false`): When `true`, provisioning continues even if the skill can't be resolved. When `false`, unresolvable skills cause provisioning to fail.
- **`as`** (optional): Install the skill under a different directory name than its registry name

### When to Use URI References vs Local Skills

- **URI references**: Use for published, shared skills from the skill bank — skills that exist independently of this template and may be updated separately. Good for widely-useful skills like `idea-refine`, domain-specific utilities, or skills maintained by other teams.
- **Local `skills/` directory**: Use for skills specific to this template's workflow that aren't published to the registry. Also for skills you want to customize beyond what the registry version provides.

If a skill is useful to multiple templates and is published to the registry or lives in a public repo, reference it by URI rather than copying the `SKILL.md` locally. **Never declare the same skill name both locally and by URI.** URI skills are installed after local ones and overwrite them. To customize an upstream skill, fork it into `skills/` under the same name and drop the URI entry.

## The Orchestrator Pattern

Every team MUST have exactly one **orchestrator** (also called lead, supervisor, or coordinator). This is the agent the user starts directly — it then creates and manages the other agents.

The orchestrator's `agents.md` should focus on:

- **What templates are available** and what each role does
- **The workflow**: when to start which agents, what tasks to give them, how to handle their results
- **Communication patterns**: what information flows between roles and when
- **Completion criteria**: how the orchestrator knows the overall task is done

Workers don't communicate directly with each other — the orchestrator reads output from one and relays relevant information to others.

### Roles, not models

Create one template per **role**, not one per model or vendor. The harness (vendor CLI) and model are chosen when each agent is started (`scion start <name> --type <role> --harness-config <h> --model <alias>`). A panel that needs model diversity is one reviewer template started several times on different harness-configs. A listed harness-config can still fail at start, so preflight the ones a workflow depends on (see **Verify a Team**).

### Briefs and reports

- Workers run in their own clone and container, so paths in the orchestrator's clone mean nothing to them. Hand work over through pushed branches, PR URLs, or files in a shared directory (`/scion-volumes/<name>/`). Templates cannot declare shared directories, so the orchestrator resolves one at runtime.
- Keep the task argument of `scion start` short. Put a long brief in a file and pass its path. Very large inline tasks have crashed agents at start.
- Have workers write their report to a file (for example `<shared-dir>/<program>/inbox/<agent>.md`) as well as messaging it. The orchestrator wakes on the message or the completion event, then reads the file. Messages can be lost.

### Orchestrator agents.md Structure

```markdown
## Role: Team Orchestrator

You are the orchestrator for [team description]. Your job is to:
1. [high-level workflow step 1]
2. [high-level workflow step 2]

## Available Agent Roles

- `<template-1>`: [what this role does, expected input, what it produces]
- `<template-2>`: [what this role does, expected input, what it produces]

## Workflow

[Step-by-step instructions: when to create agents, what to tell them,
how to handle results, when the overall task is complete.]
```

## Creating a New Team

### 1. Analyze the Description

Identify from the user's description:
- **Roles**: What distinct agent types are needed?
- **Workflow**: How do the agents interact? Sequential pipeline, parallel work, debate, review cycle?
- **Orchestration**: Who starts whom? Who collects results?

### 2. Design the Team Structure

- Identify one role as the **orchestrator** — the entry point the user will start
- All other roles are **workers** — started and managed by the orchestrator
- Map out the communication flow

### 3. Create Templates

For each role, create the template directory with its files. Write worker templates first, then the orchestrator (since it references worker template names).

### 4. Add Skills When Appropriate

If a role needs specialized domain knowledge or tool-usage patterns that would benefit from being a discrete, reusable skill file rather than inline in `agents.md`:

- **For skills already published in the registry**: Add a URI reference in `scion-agent.yaml` under `skills:`. This is preferred — it avoids duplicating skill content and picks up registry updates automatically.
- **For custom skills specific to this template**: Create a `SKILL.md` file in the template's `skills/` directory.

### 5. Validate

- [ ] Every template has `scion-agent.yaml` with `schema_version: "1"`
- [ ] The orchestrator references the correct template names
- [ ] Template directory names are kebab-case
- [ ] The workflow matches the user's intent
- [ ] No CLI usage instructions are duplicated in templates
- [ ] For skills referenced by URI: verify the URI format is correct (`skill://registry/scope/name@version`, `gh://owner/repo/name@ref`, or a full `https://github.com/.../tree/<sha>/<path>` for monorepo paths)
- [ ] No skill name is declared both locally and by URI
- [ ] No `system-prompt.md` unless replacing the harness prompt is intended
- [ ] Skill scripts are invoked through an interpreter (`bash`, `bun`, `python`), not `./script`
- [ ] URI skills are proven by starting one real agent from the template and listing its skills directory. Provisioning is the only resolution test available inside an agent
- [ ] Required skills (non-optional) will cause provisioning to fail if unresolvable — mark skills `optional: true` if the team can function without them

### 6. Verify a Team

The validation checklist catches format errors. Only a running agent proves the template works.

1. **Provision smoke.** Start the cheapest role on a small model with a task like: "List `$HOME/.claude/skills/*/` (or your harness's skills dir), print the first 3 lines of <a few SKILL.md files>, message the output to @<you>, then signal completion." The expected count is local skills + URI skills + platform skills. A missing directory means a URI did not resolve. Delete the agent afterwards, and repeat whenever the URI set changes.
2. **Harness preflight.** When a workflow relies on several vendors (review panels, cross-model judges), start one trivial agent on each harness-config it names. `scion harness-config list` shows configs that exist, not ones that start. Expired tokens, missing auth, missing images, and crashes at boot are all common. Record the runnable set where the orchestrator reads it, and tell the orchestrator what to do when a panel shrinks to one vendor.
3. **Dry run.** Give the orchestrator a small real task, and read its briefs and its workers' reports before trusting the workflow.

Syncing a template to test it (`scion templates sync <name>`, or `scion start --upload-template`) publishes it at project scope. Say so before the first sync.

## Starting From an Existing Agent Pack

When the team comes from a pack written for another host, read `references/porting.md` before designing roles. It covers triage (`scripts/triage.sh`), mapping host primitives (`references/host-primitive-map.md`), a runtime contract skill (`references/runtime-contract-skeleton.md`), minimal forks, and a dogfood trial. The templates it produces use the formats above and pass **Verify a Team**.

## Extending an Existing Team

When adding to or modifying an existing team:

### 1. Review Current State

Read the existing templates in `.scion/templates/` to understand:
- The current roles and their responsibilities
- The orchestrator's workflow and how it references workers
- Naming conventions and patterns already established
- Any existing skills in template `skills/` directories
- Skill may be hard copied between templates if they are relevant to more than one type.

### 2. Determine the Change

- **Adding a role**: Create a new worker template, then update the orchestrator's `agents.md` to reference it — add the new template to the "Available Agent Roles" section and integrate it into the workflow.
- **Modifying a role**: Update the existing template's `agents.md` and/or `system-prompt.md`. Check if the orchestrator's workflow needs adjustment.
- **Changing workflow**: Update the orchestrator's `agents.md` workflow section. May require changes to worker instructions if handoff patterns change.
- **Adding capabilities**: Consider whether the new capability belongs as inline instructions in `agents.md` or as a discrete skill in the template's `skills/` directory.

### 3. Maintain Consistency

- Follow the naming conventions already in use
- Keep the communication patterns consistent (workers report to orchestrator)
- Update the orchestrator whenever worker templates change

## Gotchas

- **Don't duplicate CLI usage instructions**. Every agent already receives base scion CLI instructions from the platform.
- **Don't create `home/` directories** in custom templates. The default template provides all infrastructure.
- **Template names = directory names**. The `description` in `scion-agent.yaml` is cosmetic; the `--type` value is the directory name.
- **Skills are harness-portable**. Write `SKILL.md` content without assuming a specific harness — scion mounts skills into the correct location automatically.
- **URI skills resolve at provision time**. Agents must be connected to a Hub with the skill in its registry. Use `optional: true` for skills that are nice-to-have but not essential.
- **URI skills overwrite local skills of the same name.** URI skills install after template-local ones. Never declare a name both ways.
- **`system-prompt.md` replaces the harness prompt** on native-mode harnesses such as Claude Code. Omit it unless you mean that.
- **Syncing publishes at project scope.** `scion templates sync <name>` (name only, from `.scion/templates/`) and `scion start --upload-template` make the template visible to every project member.
- **Never set `harness`, `harness_config`, `explicit_workspace`, or `config_dir`** — these fields are deprecated or cause broken agent startup. Omit them entirely. Project defaults handle harness selection automatically.

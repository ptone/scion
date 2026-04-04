---
name: team-creation
description: >-
  Create scion agent team templates from a high-level description of roles and workflow.
  Use when the user describes a multi-agent team, panel, crew, pipeline, or any scenario
  requiring coordinated LLM agents with distinct roles. Produces ready-to-use template
  directories in .scion/templates/.
---

# Scion Team Creation Skill

You create **scion agent templates** — the blueprints that define specialized agent roles. Given a description of a team (roles, responsibilities, workflow), you produce a set of template directories that can immediately be used to start agents with `scion start <name> --type <template>`.

## How Scion Templates Work

Scion is a container-based orchestration platform for concurrent LLM agents. Each agent runs in an isolated container with its own workspace (git worktree). **Templates** configure what an agent knows and how it behaves.

### Template Directory Structure

Each template lives in `.scion/templates/<template-name>/` and contains:

```
.scion/templates/<template-name>/
├── scion-agent.yaml       # REQUIRED: template metadata and config
├── agents.md              # REQUIRED: agent behavioral instructions
└── system-prompt.md       # OPTIONAL: role persona and expertise framing
```

Custom templates automatically **inherit** from the built-in `default` template, which provides shell configs, git setup, tmux, and base scion infrastructure. You do NOT need to create a `home/` directory or duplicate any infrastructure files.

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
system_prompt: system-prompt.md
```

Optional fields you may include when the role requires them:

```yaml
# Constrain agent runtime
max_turns: 50              # Maximum conversation turns
max_duration: "30m"        # Maximum wall-clock time (e.g. "1h", "30m")

# Environment variables available inside the container
env:
  ROLE: "reviewer"
  TEAM_SIZE: "3"

# Sidecar services (e.g. browser for web-testing roles)
services:
  - name: chromium
    command: ["chromium", "--headless", "--no-sandbox", "--remote-debugging-port=9222"]
    restart: always
    ready_check:
      type: tcp
      target: "localhost:9222"
      timeout: "10s"
```

### agents.md (Agent Instructions)

This file contains the behavioral instructions injected into the agent's context. Every `agents.md` MUST begin with the following **status reporting boilerplate** — this is mandatory scion infrastructure that enables the orchestration system to track agent state:

```markdown
## Important instructions to keep the user informed

### Waiting for input

Before you ask the user a question, you must always execute the script:

      `sciontool status ask_user "<question>"`

And then proceed to ask the user

### Blocked (intentionally waiting)

When you are intentionally waiting for something — such as a child agent you started to complete, or a scheduled event you are expecting — you must signal that you are blocked:

      `sciontool status blocked "<reason>"`

For example: `sciontool status blocked "Waiting for agent deploy-frontend to complete"`

This prevents the system from falsely marking you as stalled. You do not need to clear this status manually; it will be cleared automatically when you resume work (e.g. when you receive a message or start a new task).

### Completing your task

Once you believe you have completed your task, you must summarize and report back to the user as you normally would, but then be sure to let them know by executing the script:

      `sciontool status task_completed "<task title>"`

Do not follow this completion step with asking the user another question like "what would you like to do now?" just stop.
```

After the boilerplate, add role-specific instructions describing what the agent should do, how it should behave, what its responsibilities are, and any constraints on its work.

### system-prompt.md (Optional System Prompt)

Use this file to frame the agent's persona, expertise, and perspective. This shapes *who* the agent is, while `agents.md` shapes *what* it does. Example:

```markdown
# Expert Code Reviewer

You are a senior software engineer with deep expertise in code quality,
security, and maintainability. You approach code review methodically,
prioritizing correctness and clarity over style preferences.
```

If the role doesn't need a distinct persona beyond its instructions, you may omit this file and remove the `system_prompt` line from `scion-agent.yaml`.

## The Orchestrator Pattern

Every team MUST have exactly one **orchestrator** (also called lead, supervisor, or coordinator). This is the agent the user starts directly — it then creates and manages the other agents.

Every scion agent already receives base instructions on using the scion CLI (starting agents, messaging, monitoring). The orchestrator's `agents.md` should **not** duplicate CLI usage details. Instead, focus on:

- **What templates are available** and what each role does
- **The workflow**: when to start which agents, what tasks to give them, how to handle their results
- **Communication patterns**: what information flows between roles and when
- **Completion criteria**: how the orchestrator knows the overall task is done

### Key orchestrator behaviors to instruct

1. **Starting workers**: Tell the orchestrator which template names to use and how to name agents. Recommend `--notify` so the orchestrator receives notifications when workers complete or need help.
2. **Relaying information**: Workers don't communicate directly with each other. The orchestrator reads output from one and relays relevant information to others.
3. **Signaling blocked state**: When waiting for workers, the orchestrator must call `sciontool status blocked "Waiting for <reason>"` so the system doesn't mark it as stalled.

### Orchestrator agents.md Template

The orchestrator's `agents.md` should follow this structure:

```markdown
[status reporting boilerplate — see above]

## Role: Team Orchestrator

You are the orchestrator for [team description]. Your job is to:
1. [high-level workflow step 1]
2. [high-level workflow step 2]
3. [...]

## Available Agent Roles

- `<template-1>`: [what this role does, what it expects as input, what it produces]
- `<template-2>`: [what this role does, what it expects as input, what it produces]

When starting agents, use `--notify` so you are notified when they complete or need help.

## Workflow

[Detailed step-by-step instructions for the orchestrator's workflow,
including when to create agents, what to tell them, how to handle
their results, and when the overall task is complete.]
```

## Step-by-Step Workflow

When asked to create a team, follow these steps:

### 1. Analyze the Description

Identify from the user's description:
- **Roles**: What distinct agent types are needed?
- **Workflow**: How do the agents interact? Sequential pipeline, parallel work, debate, review cycle?
- **Orchestration**: Who starts whom? Who collects results?

### 2. Design the Team Structure

- Identify one role as the **orchestrator** — the entry point the user will start
- All other roles are **workers** — started and managed by the orchestrator
- Map out the communication flow: who messages whom, and when

### 3. Create Template Directories

For each role, create:
1. `.scion/templates/<role-name>/scion-agent.yaml`
2. `.scion/templates/<role-name>/agents.md`
3. `.scion/templates/<role-name>/system-prompt.md` (if the role benefits from persona framing)

### 4. Write the Orchestrator Last

The orchestrator's instructions reference all other templates by name, so create worker templates first, then write the orchestrator template that ties them together.

### 5. Validate

Check that:
- [ ] Every template has `scion-agent.yaml` with `schema_version: "1"`
- [ ] Every `agents.md` starts with the status reporting boilerplate
- [ ] The orchestrator references the correct template names
- [ ] Template directory names are kebab-case
- [ ] The workflow described in the orchestrator's instructions matches the user's intent
- [ ] Templates focus on roles and interactions, not CLI usage details

## Complete Example: 3-Judge Debate Panel

**User request**: "Create a 3-judge panel that debates a question and attempts to reach consensus."

This produces two templates: `panel-judge` (worker) and `panel-lead` (orchestrator).

### .scion/templates/panel-judge/scion-agent.yaml

```yaml
schema_version: "1"
description: "A debate panelist who argues a position and works toward consensus"
agent_instructions: agents.md
system_prompt: system-prompt.md
```

### .scion/templates/panel-judge/system-prompt.md

```markdown
# Debate Panelist

You are an expert panelist participating in a structured debate. You think critically,
argue your positions with evidence, and engage constructively with opposing viewpoints.
You are willing to update your position when presented with compelling arguments.
```

### .scion/templates/panel-judge/agents.md

```markdown
## Important instructions to keep the user informed

### Waiting for input

Before you ask the user a question, you must always execute the script:

      `sciontool status ask_user "<question>"`

And then proceed to ask the user

### Blocked (intentionally waiting)

When you are intentionally waiting for something — such as a child agent you started to complete, or a scheduled event you are expecting — you must signal that you are blocked:

      `sciontool status blocked "<reason>"`

For example: `sciontool status blocked "Waiting for agent deploy-frontend to complete"`

This prevents the system from falsely marking you as stalled. You do not need to clear this status manually; it will be cleared automatically when you resume work (e.g. when you receive a message or start a new task).

### Completing your task

Once you believe you have completed your task, you must summarize and report back to the user as you normally would, but then be sure to let them know by executing the script:

      `sciontool status task_completed "<task title>"`

Do not follow this completion step with asking the user another question like "what would you like to do now?" just stop.

## Role: Debate Panelist

You are one judge on a multi-judge panel. You will receive a question or topic to debate.

### Your Process

1. **Initial Position**: When given the question, form your initial position with reasoning.
   Write your position clearly, stating your stance and supporting arguments.

2. **Debate Rounds**: You will receive messages containing other judges' arguments.
   For each round:
   - Read the other positions carefully
   - Identify points of agreement and disagreement
   - Respond with your updated analysis, noting where you've been persuaded
     or where you maintain your position and why

3. **Consensus Check**: When asked for your final position, clearly state:
   - Your current stance
   - Whether you agree with the emerging consensus (if any)
   - Any remaining objections or caveats

### Guidelines
- Be substantive — support arguments with reasoning and evidence
- Engage directly with others' points rather than restating your own
- Be willing to change your mind when warranted
- Keep responses focused and concise
```

### .scion/templates/panel-lead/scion-agent.yaml

```yaml
schema_version: "1"
description: "Orchestrator that runs a 3-judge debate panel to consensus"
agent_instructions: agents.md
system_prompt: system-prompt.md
```

### .scion/templates/panel-lead/system-prompt.md

```markdown
# Panel Moderator

You are an impartial moderator managing a structured debate panel.
You ensure fair process, synthesize arguments, and guide the panel toward consensus.
```

### .scion/templates/panel-lead/agents.md

```markdown
## Important instructions to keep the user informed

### Waiting for input

Before you ask the user a question, you must always execute the script:

      `sciontool status ask_user "<question>"`

And then proceed to ask the user

### Blocked (intentionally waiting)

When you are intentionally waiting for something — such as a child agent you started to complete, or a scheduled event you are expecting — you must signal that you are blocked:

      `sciontool status blocked "<reason>"`

For example: `sciontool status blocked "Waiting for agent deploy-frontend to complete"`

This prevents the system from falsely marking you as stalled. You do not need to clear this status manually; it will be cleared automatically when you resume work (e.g. when you receive a message or start a new task).

### Completing your task

Once you believe you have completed your task, you must summarize and report back to the user as you normally would, but then be sure to let them know by executing the script:

      `sciontool status task_completed "<task title>"`

Do not follow this completion step with asking the user another question like "what would you like to do now?" just stop.

## Role: Panel Lead / Orchestrator

You manage a 3-judge debate panel. Your task is given as the debate question.

## Available Agent Roles

- `panel-judge`: A debate panelist who forms positions, argues with evidence, and works toward consensus. Give each judge the debate question as their task.

When starting judges, use `--notify` so you are notified when they complete each round.

## Workflow

### Phase 1: Setup
1. Start 3 judge agents named `judge-1`, `judge-2`, `judge-3` using the `panel-judge` template
2. Give each the same debate question as their task
3. Signal blocked and wait for all three to complete their initial positions

### Phase 2: Debate Rounds (up to 3 rounds)
For each round:
1. Use `scion look` to read each judge's latest output
2. Compile a summary of all positions and key arguments
3. Send each judge the compiled summary via `scion message`, asking them to respond
   to the others' arguments
4. Signal blocked and wait for all three to respond

### Phase 3: Consensus Check
1. After the debate rounds, send each judge a message asking for their final position
   and whether they agree with the emerging consensus
2. Wait for all responses
3. Read and analyze the final positions

### Phase 4: Report
1. Synthesize the panel's deliberation into a final report:
   - The question debated
   - Summary of key arguments from each side
   - The consensus position (if reached) or the majority/minority positions
   - Notable points of agreement and disagreement
2. Present this report as your final output
3. Signal task completed
```

## Gotchas

- **Never omit the status boilerplate** from `agents.md`. Without it, the scion orchestration system cannot track agent state and the agent will appear stalled.
- **Don't duplicate CLI usage instructions** in templates. Every agent already receives base scion CLI instructions. Templates should focus on roles, interactions, and workflow.
- **Don't create `home/` directories** in custom templates. The default template provides all infrastructure files. Custom templates only need instruction and config files.
- **Template names must match directory names**. The name in `scion-agent.yaml` `description` is cosmetic; the actual template name used in `--type` is the directory name.
- **Workers communicate through the orchestrator**. Agents don't message each other directly — the orchestrator reads output from one and relays to others.

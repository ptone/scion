# Harness Evaluation System Design

**Created:** 2026-05-06
**Status:** Proposal
**Branch:** scion/eval-design

---

## 1. Motivation

Scion supports multiple coding harnesses (Claude Code, Gemini CLI, Codex, OpenCode, generic/declarative) with diverse provisioning paths, auth methods, tool capabilities, and system prompt handling. Today there is no systematic way to:

1. **Measure harness quality** — Does a harness reliably provision, deliver instructions, handle auth, and support tool use?
2. **Compare harnesses** — Given the same task, how do different harnesses perform in terms of correctness, cost, and time?
3. **Detect regressions** — Did a harness config change, template update, or image rebuild break something?
4. **Validate new harnesses** — When adding a container-script harness or declarative generic config, does it actually work end-to-end?

The existing automated QA proposal (see `automated-qa.md`) covers platform-level journey testing. This design addresses a complementary gap: **evaluating the harness layer specifically**, isolating harness/tooling quality from model quality.

### Why Not Just Run Tests?

Unit tests validate Go code paths. Integration tests validate API contracts. Neither exercises the full harness provisioning → agent startup → message delivery → task execution → result collection pipeline as experienced by a real agent. Harness evaluation requires running actual agents through realistic scenarios and inspecting trajectories.

---

## 2. Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    EVAL COORDINATOR                          │
│  (operator agent using coordinator template)                 │
│                                                              │
│  1. Load scenario manifest                                   │
│  2. For each scenario × harness:                             │
│     a. Create target agent (--type <harness-template>)       │
│     b. Wait for ready                                        │
│     c. Send task via `scion message --plain`                 │
│     d. Monitor progress (scion look, agent-info.json)        │
│     e. Collect trajectory on completion                      │
│     f. Clean up target agent                                 │
│  3. Run scorers on collected trajectories                    │
│  4. Produce eval report                                      │
└─────────────┬───────────────────────────────┬────────────────┘
              │ creates/messages              │ writes results
              ▼                               ▼
┌─────────────────────┐          ┌──────────────────────────┐
│   TARGET AGENT(s)   │          │  /scion-volumes/eval-out │
│  (per-harness        │          │                          │
│   templates)         │          │  scenarios/              │
│                      │          │    scenario-1/           │
│  Runs task, produces │          │      claude/             │
│  trajectory in       │          │        trajectory.jsonl  │
│  ~/.claude/ or equiv │          │        agent-info.json   │
│                      │          │        verdict.json      │
│                      │          │      gemini/             │
│                      │          │        ...               │
│                      │          │  report.json             │
│                      │          │  report.md               │
└─────────────────────┘          └──────────────────────────┘
```

### Key Roles

| Role | Template | Purpose |
|------|----------|---------|
| **Eval Coordinator** | `eval-coordinator` | Operator agent that orchestrates scenarios, drives target agents, collects results |
| **Target Agent** | Per-harness (e.g., `eval-target-claude`, `eval-target-gemini`) | Receives tasks via `--plain` messages, executes them using the harness under test |
| **Scorer** | (logic within coordinator or separate agent) | Evaluates trajectories against success criteria |

---

## 3. Scenario Definition

A scenario is a self-contained test case that exercises specific harness capabilities.

### 3.1 Scenario Manifest Format

Scenarios are defined in a YAML manifest file, stored in the eval coordinator's workspace or a shared volume.

```yaml
# eval-scenarios.yaml
version: 1
defaults:
  timeout: 300s          # max time per scenario run
  retries: 0             # retry count on failure (for pass@k)
  samples: 1             # number of independent runs (for statistical metrics)

scenarios:
  - id: provision-basic
    name: "Basic provisioning and startup"
    category: provisioning
    description: "Verify agent starts, accepts a message, and responds"
    task: "Reply with exactly: HELLO EVAL"
    validator:
      type: exact_match
      expected: "HELLO EVAL"
    harnesses: [claude, gemini, codex]  # which harnesses to test
    timeout: 120s

  - id: file-edit
    name: "Single file edit"
    category: tool-use
    description: "Edit a specific file in the workspace"
    setup:
      files:
        "src/main.py": |
          def greet(name):
              return f"Hello, {name}"
    task: |
      Edit src/main.py to change the greet function to return
      "Hi, {name}!" instead of "Hello, {name}".
    validator:
      type: file_content
      path: "src/main.py"
      contains: '"Hi, {name}!"'
      not_contains: '"Hello, {name}"'

  - id: multi-file-create
    name: "Create multiple files"
    category: tool-use
    description: "Create a module with multiple files"
    task: |
      Create a Python package called 'utils' with:
      - utils/__init__.py that exports a 'hello' function
      - utils/greeting.py that defines the hello function returning "hello world"
    validator:
      type: compound
      checks:
        - type: file_exists
          path: "utils/__init__.py"
        - type: file_exists
          path: "utils/greeting.py"
        - type: file_content
          path: "utils/greeting.py"
          contains: "hello world"

  - id: system-prompt-adherence
    name: "System prompt instruction following"
    category: instructions
    description: "Verify the harness correctly delivers system prompt constraints"
    system_prompt_override: "You must always respond in ALL CAPS."
    task: "What is 2+2?"
    validator:
      type: regex
      pattern: "^[^a-z]*$"  # no lowercase letters

  - id: git-operations
    name: "Git commit workflow"
    category: tool-use
    description: "Create a file and commit it"
    setup:
      git_init: true
    task: |
      Create a file called README.md with the content "# Test Project"
      and commit it with the message "initial commit".
    validator:
      type: compound
      checks:
        - type: command
          run: "git log --oneline -1"
          contains: "initial commit"
        - type: file_exists
          path: "README.md"

  - id: auth-api-call
    name: "Authenticated API access"
    category: auth
    description: "Verify the harness correctly provisions API credentials"
    task: "Use curl to check that the ANTHROPIC_API_KEY env var is set (just confirm it exists, don't print it)"
    validator:
      type: trajectory_contains
      pattern: "ANTHROPIC_API_KEY"
    harnesses: [claude]

  - id: mcp-server
    name: "MCP server interaction"
    category: mcp
    description: "Verify MCP servers are accessible"
    requires:
      mcp_server: filesystem
    task: "Use the filesystem MCP server to list files in the current directory"
    validator:
      type: trajectory_contains
      pattern: "filesystem"
    harnesses: [claude, gemini]
```

### 3.2 Scenario Categories

| Category | Tests | Harness Aspect |
|----------|-------|----------------|
| `provisioning` | Startup, ready state, basic response | Container provisioning, harness init |
| `instructions` | CLAUDE.md / agents.md delivery, system prompt | Instruction injection pipeline |
| `tool-use` | File read/write/edit, bash execution, git | Tool availability and reliability |
| `auth` | API key presence, token refresh, vertex-ai | Auth provisioning and credential injection |
| `mcp` | MCP server connectivity, tool calls | MCP server wiring |
| `limits` | Max turns, max duration enforcement | Limits handler and hook dialect |
| `recovery` | Interrupt handling, error recovery | Harness resilience |
| `cost` | Token usage on standard tasks | Operational efficiency (not model quality) |

### 3.3 Setup Mechanics

Each scenario can declare setup steps that the coordinator performs before sending the task:

- **`files`**: Files to create in the target agent's workspace before task delivery
- **`git_init`**: Initialize a git repo in the workspace
- **`system_prompt_override`**: Override the template's system prompt for this scenario
- **`env`**: Additional environment variables
- **`requires`**: Capabilities the target harness must have (skip if unsupported)

Setup files are written to a shared volume that the target agent's workspace mounts, or injected via the target agent's template.

---

## 4. Templates

### 4.1 Eval Coordinator Template

The coordinator is an operator agent that drives the evaluation. It runs the Claude harness (for its orchestration capability) with Scion CLI access.

```
.scion/templates/eval-coordinator/
├── scion-agent.yaml
├── agents.md
├── home/
│   └── .claude/
│       └── settings.json
└── skills/
    └── eval-runner/
        └── SKILL.md
```

**`scion-agent.yaml`:**
```yaml
schema_version: "1"
description: "Eval coordinator — orchestrates harness evaluation scenarios"
default_harness_config: claude-web
env:
  EVAL_SCENARIOS_PATH: "/scion-volumes/eval-scenarios/eval-scenarios.yaml"
  EVAL_OUTPUT_PATH: "/scion-volumes/eval-out"
```

**`agents.md`** (key sections):
```markdown
# Eval Coordinator

You are an evaluation coordinator agent. Your job is to run harness evaluation
scenarios and collect results.

## Workflow

1. Read the scenario manifest from $EVAL_SCENARIOS_PATH
2. For each scenario, for each target harness:
   a. Create a target agent: `scion create <scenario-id>-<harness> --type eval-target-<harness> --non-interactive --notify`
   b. Wait for the agent to reach 'running' phase
   c. If scenario has setup steps, prepare the workspace
   d. Send the task: `scion message --plain <agent-name> "<task>"`
   e. Monitor: poll `scion look <agent-name> --plain` until completion or timeout
   f. Collect trajectory and artifacts
   g. Run validator
   h. Record result
   i. Clean up: `scion delete <agent-name> --non-interactive --force`
3. Aggregate results and produce report

## Monitoring Target Agents

Poll agent status via `scion list --format json` checking for:
- `activity: "completed"` — agent finished its task
- `activity: "idle"` — agent may be waiting; check with `scion look`
- `phase: "error"` — agent crashed
- Timeout exceeded — force stop and record as TIMEOUT

## Collecting Trajectories

After the target agent completes:
1. Copy trajectory from agent home via `scion` exec or shared volume
2. Save to $EVAL_OUTPUT_PATH/scenarios/<scenario-id>/<harness>/

## Message Delivery

Always use `scion message --plain` to send tasks. This delivers raw text
without JSON wrapping, simulating a human user typing into the agent.

Do NOT use `--raw` (that sends literal keystrokes without Enter).
Do NOT use structured messages (the agent would see JSON delimiters).
```

### 4.2 Target Agent Templates

One template per harness under test. Each is minimal — it uses the harness's default configuration to test the harness as-shipped.

**Example: `eval-target-claude/scion-agent.yaml`:**
```yaml
schema_version: "1"
description: "Eval target — Claude Code harness under test"
default_harness_config: claude-web
```

**Example: `eval-target-claude/agents.md`:**
```markdown
# Eval Target Agent

You are a coding assistant. Complete the task you receive.
Work in your current workspace directory.
When you have completed the task, state "TASK COMPLETE" on its own line.
```

For each harness type, the template differs only in `default_harness_config` and any harness-specific instructions. The coordinator creates agents from these templates dynamically.

**Harness-specific templates:**

| Template | Harness Config | Notes |
|----------|---------------|-------|
| `eval-target-claude` | `claude-web` | Standard Claude Code |
| `eval-target-gemini` | `gemini` | Gemini CLI |
| `eval-target-codex` | `codex` | OpenAI Codex (container-script provisioner) |
| `eval-target-opencode` | `opencode` | OpenCode |
| `eval-target-generic` | Custom `config.yaml` | Tests declarative generic harness path |

### 4.3 Template Inheritance

All eval-target templates inherit from the `default` template (standard Scion behavior), getting `.tmux.conf`, `.zshrc`, etc. The coordinator template additionally inherits Scion CLI access patterns.

---

## 5. Execution Model

### 5.1 Coordinator-Driven Execution

The coordinator agent runs scenarios sequentially (or with controlled parallelism). This mirrors the SWE-bench pattern of an outer harness driving an inner agent.

```
Coordinator                    Target Agent
    │                              │
    ├── scion create ──────────────┤
    │                              ├── provisioning...
    │   (wait for running)         ├── harness init
    │                              ├── ready (idle)
    ├── scion message --plain ─────┤
    │   "Edit src/main.py..."      ├── receives task
    │                              ├── executes (thinking, tool use)
    │   (poll scion look)          ├── ...
    │                              ├── "TASK COMPLETE"
    │                              ├── activity: completed
    ├── collect trajectory ────────┤
    ├── run validator              │
    ├── scion delete ──────────────┤
    │                              │
    ▼ next scenario                │
```

### 5.2 Message Delivery

The coordinator uses `scion message --plain <agent> "<task>"` to deliver tasks:

- **`--plain`**: Delivers raw text without JSON wrapping (the agent sees only the task text, not Scion message delimiters). This simulates a human user and tests the harness's message handling as-is.
- **`--notify`**: Used on `scion create` so the coordinator gets notified when the target agent completes or needs input.
- **`--interrupt`**: Can be used for timeout/abort scenarios to test harness interrupt handling.

### 5.3 Completion Detection

The coordinator detects task completion through multiple signals:

1. **Activity state**: `agent-info.json` transitions to `activity: completed` (set by sciontool hooks)
2. **Terminal output**: `scion look --plain` output contains "TASK COMPLETE" marker
3. **Idle detection**: Agent becomes `idle` and stays idle for a configurable grace period
4. **Timeout**: Hard timeout per scenario (default 300s)

### 5.4 Trajectory Collection

After completion, the coordinator collects:

| Artifact | Source | Contains |
|----------|--------|----------|
| `trajectory.jsonl` | Agent's transcript file (`.claude/` dir) | Full conversation: user messages, assistant responses, tool calls, tool results |
| `agent-info.json` | Agent home directory | Phase, activity, timestamps, error details |
| `agent.log` | Agent home directory | Sciontool lifecycle events, hook execution |
| `terminal.txt` | `scion look --plain --full` | Raw terminal output (visible agent behavior) |
| `workspace-diff.patch` | `git diff` in agent workspace | File changes made by agent |

Collection happens via shared volume mount or `scion` exec commands before the target agent is deleted.

### 5.5 Parallel Execution

For batch runs, the coordinator can run multiple scenarios in parallel:

```
# Up to N concurrent target agents
MAX_PARALLEL=3

# Create all agents for current batch
for scenario in batch:
    scion create <name> --type eval-target-<harness> --non-interactive --notify

# Send tasks
for agent in batch_agents:
    scion message --plain <agent> "<task>"

# Monitor all, collect as they complete
while active_agents:
    check_completions()
    collect_finished()
```

Parallelism is bounded by broker capacity and resource limits.

---

## 6. Shared Volume Layout

Two shared volumes coordinate data flow:

### 6.1 `eval-scenarios` (read-only to targets)

Contains scenario definitions and setup files. Created once before the eval run.

```
/scion-volumes/eval-scenarios/
├── eval-scenarios.yaml          # Scenario manifest
└── setup/                       # Per-scenario setup files
    ├── file-edit/
    │   └── src/main.py
    └── git-operations/
        └── (empty — git init done by coordinator)
```

### 6.2 `eval-out` (write by coordinator)

Collects all evaluation outputs.

```
/scion-volumes/eval-out/
├── run-<timestamp>/             # One directory per eval run
│   ├── meta.json                # Run metadata (harnesses, scenarios, start time)
│   ├── scenarios/
│   │   ├── provision-basic/
│   │   │   ├── claude/
│   │   │   │   ├── trajectory.jsonl
│   │   │   │   ├── agent-info.json
│   │   │   │   ├── agent.log
│   │   │   │   ├── terminal.txt
│   │   │   │   ├── workspace-diff.patch
│   │   │   │   └── verdict.json
│   │   │   └── gemini/
│   │   │       └── ...
│   │   ├── file-edit/
│   │   │   └── ...
│   │   └── ...
│   ├── report.json              # Machine-readable results
│   └── report.md                # Human-readable summary
```

---

## 7. Validators and Scoring

### 7.1 Validator Types

Validators run after trajectory collection to determine pass/fail per scenario.

| Type | Description | Config Fields |
|------|-------------|---------------|
| `exact_match` | Agent output contains exact string | `expected` |
| `contains` | Output contains substring | `substring` |
| `not_contains` | Output must not contain string | `substring` |
| `regex` | Output matches regex pattern | `pattern` |
| `file_exists` | File exists in agent workspace | `path` |
| `file_content` | File contains/excludes content | `path`, `contains`, `not_contains` |
| `command` | Run command in workspace, check output | `run`, `contains`, `exit_code` |
| `trajectory_contains` | Trajectory JSONL contains pattern | `pattern` |
| `compound` | All sub-checks must pass | `checks: [...]` |
| `model_graded` | Use an LLM to judge quality | `rubric`, `model` |

### 7.2 Verdict Format

Each scenario run produces a verdict:

```json
{
  "scenario_id": "file-edit",
  "harness": "claude",
  "run_id": "run-20260506-143022",
  "status": "pass",          // pass | fail | error | timeout | skip
  "duration_seconds": 45.2,
  "validator_results": [
    {
      "type": "file_content",
      "path": "src/main.py",
      "check": "contains",
      "expected": "\"Hi, {name}!\"",
      "found": true,
      "pass": true
    }
  ],
  "error": null,
  "trajectory_summary": {
    "total_messages": 8,
    "tool_calls": 3,
    "tool_types": ["Read", "Edit"],
    "tokens_in": 12450,
    "tokens_out": 2340
  }
}
```

### 7.3 Metrics

Inspired by SWE-bench, Aider, and Inspect AI patterns, we collect both correctness and operational metrics:

**Correctness Metrics (per scenario):**

| Metric | Description | Source |
|--------|-------------|--------|
| `resolve_rate` | % of scenarios that pass validation | Verdict status |
| `pass@k` | Probability of passing in k attempts (when `samples > 1`) | Multiple runs |
| `category_resolve_rate` | Resolve rate grouped by scenario category | Verdict + scenario manifest |

**Operational Metrics (per scenario run):**

| Metric | Description | Source |
|--------|-------------|--------|
| `duration_seconds` | Wall-clock time from task delivery to completion | Timestamps |
| `startup_seconds` | Time from agent creation to ready state | agent-info.json |
| `tool_call_count` | Number of tool invocations | Trajectory |
| `tool_types` | Set of tool types used | Trajectory |
| `message_count` | Total conversation turns | Trajectory |
| `token_usage` | Input/output token counts | Trajectory |
| `error_count` | Number of tool errors in trajectory | Trajectory |
| `edit_apply_rate` | % of file edits that applied successfully (à la Aider) | Trajectory |

**Harness-Specific Metrics:**

| Metric | Description | Source |
|--------|-------------|--------|
| `provision_success` | Did harness provisioning complete without error? | agent.log |
| `instruction_delivery` | Were CLAUDE.md / agents.md contents visible in trajectory? | Trajectory first message |
| `system_prompt_active` | Did system prompt affect behavior? | Validator on system-prompt scenarios |
| `auth_provisioned` | Were API credentials available? | Agent environment / trajectory |
| `mcp_connected` | Were MCP servers accessible? | Trajectory tool calls |
| `interrupt_handled` | Did the harness handle ^C correctly? | Recovery scenarios |

### 7.4 Scoring Methodology

Following the principle of **separating harness metrics from model metrics** (from Aider's benchmark pattern):

1. **Harness Score**: Binary per-category. A harness "supports" a category if >80% of scenarios in that category pass. This measures harness quality independent of model.

2. **Reliability Score**: For scenarios run multiple times (`samples > 1`), the pass@1 rate measures harness consistency. A harness that passes 10/10 times is more reliable than one that passes 7/10.

3. **Efficiency Score**: Normalized operational cost (tokens × time) for passed scenarios. Lower is better. This captures harness overhead — a harness that requires fewer retries or cleaner tool patterns is more efficient.

4. **Regression Detection**: Compare current run against baseline. Flag any scenario that previously passed but now fails, or any >20% degradation in operational metrics.

---

## 8. Reporting

### 8.1 Machine-Readable Report (`report.json`)

```json
{
  "run_id": "run-20260506-143022",
  "timestamp": "2026-05-06T14:30:22Z",
  "duration_seconds": 1847,
  "harnesses_tested": ["claude", "gemini", "codex"],
  "scenarios_total": 12,
  "results_summary": {
    "claude": {
      "total": 12,
      "pass": 11,
      "fail": 1,
      "error": 0,
      "timeout": 0,
      "skip": 0,
      "resolve_rate": 0.917,
      "avg_duration_seconds": 52.3,
      "avg_tokens": 14200,
      "categories": {
        "provisioning": {"pass": 3, "total": 3, "rate": 1.0},
        "tool-use": {"pass": 4, "total": 4, "rate": 1.0},
        "instructions": {"pass": 2, "total": 2, "rate": 1.0},
        "auth": {"pass": 1, "total": 1, "rate": 1.0},
        "mcp": {"pass": 1, "total": 2, "rate": 0.5}
      }
    },
    "gemini": { "..." : "..." },
    "codex": { "..." : "..." }
  },
  "regressions": [],
  "verdicts": [ "..." ]
}
```

### 8.2 Human-Readable Report (`report.md`)

```markdown
# Harness Evaluation Report
**Run:** run-20260506-143022 | **Date:** 2026-05-06 | **Duration:** 30m 47s

## Summary

| Harness | Pass | Fail | Error | Timeout | Skip | Rate |
|---------|------|------|-------|---------|------|------|
| claude  | 11   | 1    | 0     | 0       | 0    | 91.7% |
| gemini  | 10   | 1    | 0     | 1       | 0    | 83.3% |
| codex   | 8    | 2    | 1     | 0       | 1    | 72.7% |

## Category Breakdown
...

## Failures
| Scenario | Harness | Status | Detail |
|----------|---------|--------|--------|
| mcp-server | claude | FAIL | MCP filesystem server not found in tool list |
| git-operations | gemini | TIMEOUT | Agent did not complete within 300s |
...

## Regressions vs Baseline
None detected.
```

---

## 9. Batch Execution and Scheduling

### 9.1 Manual Run

```bash
# Create shared volumes
scion shared-dir create eval-scenarios --non-interactive
scion shared-dir create eval-out --non-interactive

# Start coordinator
scion create eval-coordinator \
  --type eval-coordinator \
  --non-interactive \
  --notify \
  "Run all harness evaluation scenarios"
```

### 9.2 Scheduled Recurring Runs

Use Scion's scheduling system for nightly regression runs:

```bash
# Schedule nightly eval at 2 AM
scion schedule create-recurring \
  --name "nightly-harness-eval" \
  --cron "0 2 * * *" \
  --type dispatch_agent \
  --agent eval-nightly-$(date +%Y%m%d) \
  --template eval-coordinator \
  --task "Run full harness evaluation suite" \
  --non-interactive
```

### 9.3 Triggered Runs

Schedule a one-shot eval after a harness config change:

```bash
# Run eval in 5 minutes (after deploy settles)
scion schedule create \
  --type dispatch_agent \
  --in 5m \
  --agent eval-post-deploy \
  --template eval-coordinator \
  --task "Run harness evaluation — post-deploy validation" \
  --non-interactive
```

---

## 10. Assessment Phase

After scenarios complete, the coordinator enters an assessment phase that produces qualitative and quantitative analysis.

### 10.1 Quantitative Assessment

Automated, based on validators and metrics:

1. **Per-scenario verdicts** — Binary pass/fail from validators
2. **Aggregate scores** — Resolve rates per harness, per category
3. **Operational comparison** — Token usage, duration, tool call patterns across harnesses
4. **Regression detection** — Diff against previous baseline run

### 10.2 Qualitative Assessment

For deeper analysis (optional, uses model-graded scoring):

1. **Trajectory review** — The coordinator (or a separate assessor agent) reads trajectory JSONL and evaluates:
   - Did the agent follow a reasonable approach?
   - Were there unnecessary retries or wasted tool calls?
   - Did the harness introduce any artifacts or confusion?

2. **Failure analysis** — For failed scenarios, classify the failure:
   - **Harness failure**: Provisioning error, missing tools, broken auth
   - **Model failure**: Agent chose wrong approach despite correct tooling
   - **Ambiguous**: Unclear whether harness or model caused the failure

3. **Comparative notes** — When multiple harnesses run the same scenario, note qualitative differences in approach efficiency, tool usage patterns, and error handling.

### 10.3 Baseline Management

```
/scion-volumes/eval-out/
├── baseline.json                # Pointer to current baseline run
├── run-20260501-020000/         # Previous baseline
├── run-20260506-143022/         # Current run
```

The coordinator compares each run against the baseline. A new baseline is set explicitly:

```bash
scion message --plain eval-coordinator "Set run-20260506-143022 as the new baseline"
```

---

## 11. Implementation Plan

### Phase 1: Foundation (MVP)

**Goal:** Run a single scenario against a single harness, collect trajectory, produce verdict.

1. Create `eval-coordinator` template with agents.md instructions
2. Create `eval-target-claude` template (Claude harness only)
3. Define 3 scenarios: `provision-basic`, `file-edit`, `system-prompt-adherence`
4. Implement coordinator workflow: create → message → monitor → collect → validate
5. Implement `exact_match`, `file_content`, and `contains` validators
6. Output: per-scenario verdict.json

**Deliverables:**
- Templates in `.scion/templates/`
- Scenario manifest YAML
- Coordinator agents.md with eval runner skill

### Phase 2: Multi-Harness and Reporting

**Goal:** Compare across harnesses, produce aggregate reports.

1. Add `eval-target-gemini` and `eval-target-codex` templates
2. Expand to 8-10 scenarios across all categories
3. Implement `command`, `compound`, and `trajectory_contains` validators
4. Implement report generation (report.json, report.md)
5. Add operational metrics collection from trajectories
6. Implement baseline comparison

**Deliverables:**
- Additional harness target templates
- Full scenario suite
- Report generation logic
- Baseline management

### Phase 3: Automation and CI

**Goal:** Scheduled runs, regression detection, notifications.

1. Set up recurring schedule for nightly eval runs
2. Implement regression detection against baseline
3. Add notification on regression (via Scion message to operator)
4. Add `model_graded` validator for qualitative assessment
5. Implement parallel scenario execution (bounded concurrency)
6. Historical trend tracking across runs

**Deliverables:**
- Scheduled eval jobs
- Regression alerting
- Trend reporting

### Phase 4: Advanced Scenarios

**Goal:** Test edge cases and advanced harness features.

1. Recovery scenarios (interrupt handling, error recovery)
2. Limit enforcement scenarios (max_turns, max_duration)
3. MCP server scenarios (per-harness MCP capability matrix)
4. Multi-turn conversation scenarios (multiple `--plain` messages)
5. Container-script harness validation (Codex provisioner path)
6. pass@k reliability testing with `samples > 1`

---

## 12. Design Decisions and Trade-offs

### D1: Coordinator as Agent vs Script

**Decision:** Coordinator is a Scion agent (not a shell script).

**Why:** The coordinator needs to make judgment calls (interpreting terminal output, classifying failures, generating reports). An agent can adapt to unexpected situations. A script would be brittle.

**Trade-off:** More expensive (coordinator uses tokens). Mitigated by using a capable but efficient model for the coordinator.

### D2: `--plain` vs `--raw` for Task Delivery

**Decision:** Use `--plain`.

**Why:** `--plain` delivers clean text through the standard message pipeline (paste buffer + Enter). `--raw` bypasses paste buffers and sends literal keystrokes without Enter — designed for control sequences, not task delivery. `--plain` is the closest simulation of a human typing a prompt.

### D3: One Template Per Harness vs Dynamic Configuration

**Decision:** Separate template per harness.

**Why:** Each harness may need different `default_harness_config`, agent instructions format, and home directory setup. Template separation keeps each configuration clean and testable. The coordinator creates agents from the appropriate template dynamically.

**Trade-off:** Template proliferation. Mitigated by keeping target templates minimal (they inherit from default).

### D4: Shared Volume vs Exec for Trajectory Collection

**Decision:** Use shared volumes as primary, exec as fallback.

**Why:** Shared volumes (`/scion-volumes/eval-out`) provide direct filesystem access without exec overhead. However, some artifacts (like the transcript file path) may not be at a predictable location, so exec-based collection (`scion` exec to copy files) serves as fallback.

### D5: Validators in Coordinator vs Separate Scorer Agent

**Decision:** Validators run within the coordinator agent.

**Why:** Most validators are simple (string matching, file checks). Running them in a separate agent would add orchestration complexity for minimal benefit. The `model_graded` validator type handles cases that need LLM judgment, using the coordinator's own model.

**Trade-off:** Coordinator context grows with trajectory data. Mitigated by extracting only relevant portions of trajectories for validation.

### D6: Sequential vs Parallel Scenario Execution

**Decision:** Sequential by default, parallel as Phase 3 optimization.

**Why:** Sequential execution is simpler to debug, produces deterministic ordering, and avoids resource contention. Parallel execution can be added once the sequential flow is proven reliable.

---

## 13. Relationship to Existing Systems

| System | Relationship |
|--------|-------------|
| **Automated QA** (`automated-qa.md`) | Complementary. Automated QA tests platform journeys (CLI commands, hub API). Harness eval tests harness-specific provisioning and agent behavior. |
| **Decoupled Harness** (`decoupled-harness-implementation.md`) | Harness eval validates the container-script provisioner path. New declarative/script-based harnesses should pass the eval suite before shipping. |
| **Harness Capabilities** (`harness-capabilities-ux.md`) | The capability matrix from that doc informs which scenarios apply to which harnesses. Scenarios should respect declared capabilities. |
| **Scheduler** | Eval uses the scheduler for recurring and triggered runs. |
| **Shared Dirs** | Eval uses shared volumes for scenario data and result collection. |

---

## 14. Open Questions

1. **Transcript file location**: The transcript JSONL path is passed via hooks at runtime. How does the coordinator locate it after the agent completes? Options: (a) standardize the path in eval-target templates, (b) extract from agent.log, (c) use `find` via exec.

2. **Workspace isolation**: Should each scenario run get a fresh workspace, or can we reuse the same agent with workspace cleanup between scenarios? Fresh agents are cleaner but more expensive (provisioning overhead per scenario).

3. **Model pinning**: Should eval-target templates pin a specific model version, or use the harness default? Pinning enables reproducibility; defaulting tests the real-world configuration.

4. **Cost budget**: What's the acceptable cost per eval run? With 12 scenarios × 3 harnesses × coordinator overhead, a full run could cost $5-15 in API tokens. Nightly runs would cost $150-450/month.

5. **Image versioning**: How do we track which container image version was used for each eval run? This is critical for regression attribution.

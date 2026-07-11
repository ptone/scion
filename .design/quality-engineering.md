# Quality Engineering: Harness E2E Verification — Design Document

- **Author:** qe-arch (Quality Engineering Architect)
- **Date:** 2026-07-11
- **Status:** Final
- **Input:** Investigation findings (`investigation-findings.md` by qe-inv, 2026-07-11)
- **Scope:** Medium

---

## 1. Problem & Goals

### Problem

Scion ships 7 harness bundles (claude, gemini-cli, opencode, codex, copilot, hermes, antigravity) and supports community-authored bundles. Existing CI covers provisioner logic in isolation (`TestBundleContract`, Python unit tests, capability-defaults parsing), but **nothing tests the live path**: image build → container start → task/message/keystroke delivery through tmux → interrupt handling → instruction/skill projection → auth against real providers → MCP server connection → limits enforcement → resume → telemetry arrival. This gap means regressions in the integration surface (the boundary between Go host and containerized harness) go undetected until a user hits them.

### Goals

1. **Define a repeatable, automated methodology** for end-to-end verification of every shipped and community-authored harness bundle against a live hub instance.
2. **Deliver two contrib-repo artifacts** — a `harness-qa` skill (test catalog + methodology) and a `qa-harness-tester` agent template — that together enable a QA agent to run the full verification suite on demand.
3. **Establish structured reporting** (results JSON + issue markdown) so findings are machine-queryable and human-actionable, with severity classification aligned to impact.
4. **Support community bundles** with a conformance review checklist and the same live-test regime, driven by each bundle's declared capability matrix.

### Success Criteria

- A QA agent instantiated from `qa-harness-tester`, given a brief naming a target instance and harness list, can execute the full tier 0–3 test regime without human intervention (aside from pre-staged auth secrets).
- Every test in the catalog has an unambiguous pass/fail criterion expressible in terms of observable evidence (file contents, CLI output, log queries) — no subjective judgment required for verdict.
- Results are comparable across runs (run_id, recorded baseline, image digests).

---

## 2. Non-Goals

- **Fixing harness bugs.** The QA agent root-causes and documents; it never modifies harness code. Fixes are a separate developer project.
- **Replacing existing CI.** Bundle contract tests, Python unit tests, and vendored-lib drift checks remain in Go CI. The QA skill assumes they pass and does not re-run them.
- **Scheduled or PR-triggered runs in v1.** The results format supports future automation (run_id, recorded baseline), but v1 is on-demand dispatch only.
- **`scion harness-config lint` CLI command.** The investigation identified this as a follow-up project. The QA skill documents the lint checks (Tier 0, C1–C9) but does not implement a Go binary.
- **Provisioning the dedicated QA instance.** Instance setup is a one-time operational task using the `instance-interaction` skill, not part of this design.

---

## 3. Proposed Design

### 3.1 Overview

Two new artifacts in the contrib-repo:

```
contrib-repo/
├── skills/
│   └── harness-qa/
│       └── SKILL.md                 # Methodology, test catalog, schemas, conformance checklist
└── templates/
    └── qa-harness-tester/
        ├── scion-agent.yaml         # Agent config referencing skills
        ├── system-prompt.md         # QA persona
        └── agents.md               # Role, workflow, reporting instructions
```

The `harness-qa` SKILL.md is the single source of truth for:
- The test catalog (all test procedures with IDs, tiers, steps, pass criteria)
- The test-definition YAML schema (embedded as documented format, not separate files)
- The results and issue reporting schemas
- The conformance checklist for community bundles
- The pre-run wipe procedure
- MCP fixture specifications

The QA agent (instantiated from the template) reads the skill, reads its brief (target instance, harness list, tier depth, auth secrets), and executes. It writes structured output to the scratchpad.

### 3.2 Skill Design: `harness-qa`

#### 3.2.1 Why a Single SKILL.md (Not a Directory Tree)

The investigation proposed a multi-file skill with `tests/*.yaml` and `fixtures/` subdirectories. I reject this for three reasons:

1. **Convention violation.** Every existing skill in the contrib-repo is a single `SKILL.md` file. A multi-file skill would require documenting and maintaining a new convention, and the scion platform's skill-projection machinery (`project_instructions()`) consumes SKILL.md content — subdirectories would not be projected into agent instructions.
2. **Agent-interpreted catalog.** The test procedures require judgment (reading TUI state, retrying model flakiness, adapting to harness-specific behaviors). The QA agent interprets the catalog from its instructions, not from a programmatic runner. Embedding the catalog in SKILL.md makes it part of the agent's projected context.
3. **Fixture simplicity.** The MCP echo fixtures and QA skill are trivial (< 50 lines each). They can be specified inline in SKILL.md as copy-paste-ready code blocks. The QA agent creates them on the target instance at run time.

**Trade-off acknowledged:** embedding everything in one file makes SKILL.md large (~1500–2000 lines). This is acceptable because (a) it's a reference document, not casual reading, and (b) the agent's context window easily accommodates it. If the catalog grows beyond ~100 tests in future, splitting into a multi-file skill with a documented convention becomes worthwhile — but that's a future decision, easily reversible.

**Alternative considered: separate YAML files loaded at runtime.** The QA agent would read `tests/*.yaml` from the skill directory at execution time. Rejected because the scion skill-projection system doesn't support subdirectories today, so the agent would need a hard-coded path to the contrib-repo — fragile coupling. If/when the platform adds multi-file skill support, migrating the embedded catalog to separate files is straightforward.

#### 3.2.2 SKILL.md Structure

```yaml
---
name: harness-qa
description: >-
  End-to-end verification methodology for Scion harness bundles. Defines the
  tiered test catalog (universal, conditional, cross-cutting), probe-agent
  methodology, pre-run wipe procedure, MCP fixture specs, community-bundle
  conformance checklist (C1-C9), and structured results/issue reporting formats.
  Use with the qa-harness-tester template.
---
```

**Sections (in order):**

1. **Overview** — what this skill covers, relationship to existing CI
2. **Methodology** — probe-agent model, canary task convention, determinism guards, tier structure (0/1/2/3), matrix-driven test plan generation
3. **Pre-Run Wipe Procedure** — mandatory steps before every run
4. **Test Catalog** — the complete set of test procedures (§3.2.3)
5. **Test Definition Format** — the YAML schema each test follows (§3.2.4)
6. **Community Bundle Conformance Checklist** — C1–C9 (§3.2.6)
7. **MCP Fixture Specifications** — inline code for echo servers (§3.2.7)
8. **QA Skill Fixture** — the XYZZY token skill for PRV-03
9. **Results Schema** — `results.json` format (§3.4.1)
10. **Issue Documentation Format** — per-issue markdown template (§3.4.2)
11. **Report Format** — `harness-qa-report.md` template (§3.4.3)
12. **Evidence Collection Rules** — what to capture and when

#### 3.2.3 Test Catalog

The catalog contains all test procedures from the investigation (§2.3), organized by area. Each test is documented with the following structure (the "test definition format" — see §3.2.4). The full catalog as designed:

**Image & Lifecycle (3 tests)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| IMG-01 | Image acquisition & local build | 0.5 | always |
| HB-01 | Container start & provisioning | 1 | always |
| LCY-01 | Stop/suspend/delete hygiene | 1 | always |

**Task & Message Delivery (4 tests)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| HB-02 | Initial task delivery | 1 | always |
| MSG-01 | Cooked message delivery | 1 | always |
| MSG-02 | Debounce/coalescing | 2 | always |
| MSG-03 | Raw keystroke delivery | 2 | always |

**Interrupt & Observability (2 tests)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| INT-01 | Interrupt delivery | 1 | always |
| LOOK-01 | Terminal observability | 1 | always |

**Prompts & Instructions (3 tests)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| PRV-01 | Agent instructions injection | 2 | always |
| PRV-02 | System prompt support | 2 | `prompts.system_prompt` ∈ {yes, partial} |
| PRV-03 | Skills projection | 2 | always |

**Auth (7 tests)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| AUTH-LIVE | Live auth (primary method) | 1 | always (one provided secret) |
| AUTH-RESOLVE-01 | Dummy auth: api_key | 2 | `auth.api_key` ∈ {yes} |
| AUTH-RESOLVE-02 | Dummy auth: auth_file | 2 | `auth.auth_file` ∈ {yes} |
| AUTH-RESOLVE-03 | Dummy auth: oauth_token | 2 | `auth.oauth_token` ∈ {yes} |
| AUTH-RESOLVE-04 | Dummy auth: vertex_ai | 2 | `auth.vertex_ai` ∈ {yes} |
| AUTH-05 | No-auth drop-to-shell | 1 | always |
| AUTH-06 | Explicit invalid auth type | 3 | always |

**MCP (3 tests)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| MCP-01 | MCP stdio transport | 2 | `mcp.stdio` ∈ {yes} |
| MCP-02 | MCP SSE transport | 2 | `mcp.sse` ∈ {yes, partial} |
| MCP-03 | MCP streamable-http transport | 2 | `mcp.streamable_http` ∈ {yes, partial} |

**Limits (3 tests)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| LIM-01 | max_duration enforcement | 2 | always (all declare yes) |
| LIM-02 | max_turns enforcement | 2 | `limits.max_turns` ∈ {yes} |
| LIM-03 | max_model_calls enforcement | 2 | `limits.max_model_calls` ∈ {yes} |

**Resume, Telemetry, Model Aliases, End-to-End (4 tests)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| RES-01 | Resume after stop | 2 | `resume` ∈ {yes}; for undeclared, verify clean restart |
| TEL-01 | Telemetry emission | 2 | `telemetry.enabled` ∈ {yes} |
| MOD-01 | Model alias resolution | 2 | always |
| GH-01 | End-to-end work product (git push) | 3 | always |

**Community Bundle Conformance (1 meta-test)**

| ID | Title | Tier | Applies When |
|---|---|---|---|
| CONF-01 | Bundle conformance review (C1–C9) | 0.5 | community bundles only |

**Total: 31 test procedures** (16 universal, 11 conditional on declared matrix, 3 negative/cross-cutting, 1 conformance meta-test).

#### 3.2.4 Test Definition Format

Each test procedure in the SKILL.md catalog follows this structure (documented as a YAML schema the agent interprets, not a machine-parsed format):

```yaml
# Test Definition Schema
# Each test in the catalog is documented with these fields.
# The QA agent reads this from SKILL.md and interprets it —
# there is no YAML parser; this schema defines the documentation format.

id: string           # Stable identifier (e.g., "MSG-01"). Referenced in results and issues.
title: string        # Human-readable name
capability: string   # "universal" or a dotted capability key (e.g., "mcp.stdio")
tier: number         # 0 | 0.5 | 1 | 2 | 3
applies_when: string # "always" or a condition on the declared matrix
                     # e.g., "declared: [yes]" or "declared: [yes, partial]"
                     # or "community bundles only"

preconditions:       # What must be true before this test runs
  - string           # e.g., "Probe agent running with canary task accepted (HB-02 passed)"

steps:               # Ordered actions the QA agent performs
  - string           # Imperative instructions, may reference {probe}, {nonce},
                     # {canary_path}, {harness} placeholders

verify:              # Observable checks that determine pass/fail
  - check: string    # The verification method:
                     #   docker_exec: "<command>"
                     #   look_contains: "<text>"
                     #   look_not_contains: "<text>"
                     #   json_field: "<path> == <value>"
                     #   cli_output: "<scion command>"
                     #   cloud_logging: "<query>"
                     #   file_exists: "<path>"
                     #   process_running: "<pattern>"
    expect: string   # What the check should produce

evidence:            # What to capture regardless of outcome
  - string           # e.g., "look_capture", "docker_exec_output", "container_logs"

on_fail: string      # Investigation guidance when the test fails.
                     # Points to specific source files/functions where the
                     # failure is likely rooted.

retries: number      # Optional. Default 2. Number of retries for model-behavior
                     # portions before declaring failure. Flake counts recorded
                     # separately from hard failures.
```

**Example — MSG-01 as it appears in SKILL.md:**

````markdown
### MSG-01: Cooked message delivery

- **Capability:** universal (U7)
- **Tier:** 1
- **Applies when:** always

**Preconditions:**
- Probe agent running with canary task accepted (HB-02 passed)

**Steps:**
1. `scion message {probe} "Append the text 'step-2' to {canary_path}"`
2. Wait up to 120s for the agent to act

**Verify:**
- `docker exec {container} cat {canary_path}` → contains "step-2"
- `scion look {probe}` → output contains evidence the message was received

**Evidence to capture:**
- `scion look` capture (before and after)
- `docker exec cat` output

**On failure:**
Text visible in pane but not submitted → Enter-acceptance issue in
`pkg/agent/manager.go` deliverImmediate path (lines 243–335; 300ms
extra-Enter logic). Text absent from pane entirely → tmux session/pane
target mismatch; check `docker exec tmux list-sessions` inside container.

**Retries:** 2 (model behavior portion only)
````

This format is human-readable in SKILL.md and gives the QA agent enough structure to execute systematically while retaining the flexibility to use judgment on ambiguous TUI states.

#### 3.2.5 Pre-Run Wipe Procedure

Documented in SKILL.md as a mandatory first step. The QA agent executes this before any test tier:

```
Pre-Run Wipe Procedure (mandatory)
====================================
Target: the dedicated QA instance, accessed via instance-interaction skill.

1. Delete all agents:
   scion list --format json | jq -r '.[].name' | xargs -I{} scion --non-interactive delete {}
   (or: scion delete --all if supported)

2. Prune stopped containers:
   sudo docker container prune -f

3. Clear project secrets staged by prior runs:
   sciontool secret delete qa-test-api-key (and any other qa-* secrets)

4. Reset harness-config installs to versions under test:
   For each harness in scope:
     scion harness-config install harnesses/<name> --force

5. Record post-wipe baseline:
   - Installed harness-config versions (scion harness-config list)
   - Image digests (sudo docker images --format json)
   - Scion version (scion version)
   - Instance name, zone, project
   → Write to results.json as run baseline.
```

#### 3.2.6 Community Bundle Conformance Checklist (C1–C9)

Documented in a dedicated section of SKILL.md (not a separate file — consistent with single-file skill convention). The checklist doubles as a best-practices reference for community harness authors.

| # | Check | How to Verify | Severity if Missing |
|---|---|---|---|
| C1 | Bundle layout complete: config.yaml, provision.py, Dockerfile, cloudbuild.yaml, README | `ls` the bundle directory | SEV-3 for missing README; SEV-0 for missing config.yaml/provision.py |
| C2 | `provisioner.lib` declared (`vendored` or `injected`); if vendored, `scion_harness.py` present and byte-identical to canonical | Compare `md5sum` with `/workspace/harnesses/scion_harness.py` | SEV-2 (ambiguous lib resolution) |
| C3 | provision.py imports and uses the shared lib (`ctx.select_auth`, `ctx.write_outputs`, `project_instructions`, `apply_mcp_servers_simple`) | Code review: grep for reimplemented auth/instructions/MCP logic | SEV-2 (C2/C3 block Tier 2 until acknowledged) |
| C4 | `assert scion_harness.INTERFACE_VERSION >= 2` or documented version gate | Grep provision.py for INTERFACE_VERSION check | SEV-3 |
| C5 | Full capability matrix declared — every key in schema, explicit `support` + `reason` for no/partial | Parse capabilities block vs Go schema keys | SEV-2 (undeclared = ambiguous oracle) |
| C6 | Auth spec complete: `default_type`, autodetect map, `no_auth` behavior + actionable message; `capture_auth.py` present if interactive login exists | Review config.yaml auth section | SEV-3 |
| C7 | Golden fixture cases shipped in bundle (`testdata/` with input.json/want.json for api_key, no_auth_mode, no_creds_error, explicit_invalid) | `ls testdata/` — note: shipped harnesses use `pkg/harness/testdata/bundle_contract/`, community bundles should ship their own | SEV-3 (advisory — tooling gap) |
| C8 | Dockerfile `FROM scion-base`; `required_image_tools` accurate | Inspect Dockerfile FROM line; verify tools via `docker run --rm <image> which <tool>` | SEV-1 |
| C9 | `provisioner.interface_version` consistent with lib version in use | Compare declared version with `scion_harness.INTERFACE_VERSION` | SEV-3 |

Findings from the conformance review are reported with `kind: bundle-conformance` in the issue format. C2/C3-class issues block Tier 2 for that bundle until acknowledged in the brief.

#### 3.2.7 MCP Fixture Specifications

Two fixtures specified inline in SKILL.md as code blocks the QA agent copies to the target instance:

**1. stdio echo server** (`qa-mcp-echo-stdio.py`):
```python
#!/usr/bin/env python3
"""Minimal MCP stdio echo server for QA. Exposes one tool: qa_echo(nonce) -> "MCP-OK <nonce>"."""
import json, sys
# ... (stdlib-only implementation of MCP stdio protocol)
# Single tool: qa_echo with one string parameter "nonce"
# Returns: {"content": [{"type": "text", "text": "MCP-OK <nonce>"}]}
```

**2. HTTP/SSE echo server** (`qa-mcp-echo-http.py`):
```python
#!/usr/bin/env python3
"""Minimal MCP streamable-HTTP echo server for QA. Binds to localhost:9999."""
# ... (stdlib-only HTTP server implementing MCP streamable-http transport)
# Same qa_echo tool, same response format
# Supports SSE fallback for MCP-02 tests
```

**3. QA skill** (`SKILL.md` for PRV-03):
```markdown
---
name: qa-verification-skill
description: >-
  A trivial skill used by the harness-qa test suite to verify skills projection.
---
# QA Verification Skill
When asked for the skill verification token, answer with exactly: XYZZY-{nonce}
```

The QA agent creates these on the target instance at test time, writes them to a temp directory, and references them in probe agent creation commands.

### 3.3 Template Design: `qa-harness-tester`

#### 3.3.1 `scion-agent.yaml`

```yaml
schema_version: "1"
description: "QA harness tester — runs end-to-end verification of harness bundles on a dedicated instance"
agent_instructions: agents.md
system_prompt: system-prompt.md
```

No external skill URIs in the YAML. The skills (`harness-qa`, `instance-interaction`, `integration-testing`, `scion-process`) are referenced in `agents.md` and projected via the standard scion skill mechanism at agent creation time (the brief or coordinator specifies which skills to attach).

**Alternative considered: listing skill URIs in scion-agent.yaml.** Only two existing templates (doc-writer, release-notes) use the `skills:` list, and those reference external `ptone/teamv1` skills. The harness-qa and instance-interaction skills live in the same contrib-repo and are attached at agent creation via the standard `--skills` flag. Following the majority pattern (no `skills:` section) is consistent and avoids hard-coding URIs.

#### 3.3.2 `system-prompt.md`

```markdown
# QA Harness Tester

You are an infrastructure QA engineer specializing in container-based development
environments. You verify that harness bundles — the configurations that let Scion
run AI coding agents inside Docker containers — actually work end-to-end on a live
hub instance.

You operate probes from the outside: you launch short-lived agents, observe their
behavior through CLI and container inspection, and compare observed behavior against
each harness's declared capability matrix. You are methodical, evidence-driven, and
skeptical of "it looks like it worked" — you verify with artifacts.

You root-cause failures to specific code paths and document them precisely. You
never fix the harness code. When something fails, you produce a finding with
reproduction steps, evidence, and a severity classification.
```

#### 3.3.3 `agents.md`

**Outline (full content written by developer agent):**

```markdown
## Role: QA Harness Tester Agent

You run end-to-end verification of Scion harness bundles on a dedicated QA
instance. You validate that each harness's declared capability matrix matches
its actual behavior by launching probe agents and observing from outside.

You do **not** modify harness code or fix issues you find. You document
findings with evidence and hand them back to the coordinator.

## Skills You Rely On

- **`harness-qa`** — the test catalog, methodology, and reporting formats.
  This is your primary reference. Read it fully before your first run.
- **`instance-interaction`** — SSH access, Docker inspection, agent
  launch/teardown on the target instance.
- **`integration-testing`** — JWT minting for hub API access (scenarios 2–3)
  when not using SSH-local CLI.
- **`scion-process`** — workspace conventions, scratchpad paths, and
  coordination norms.

## Inputs You Expect

- A brief specifying:
  - Target instance (name, zone, project)
  - Harness list (which harnesses to test; "all" for all 7 shipped)
  - Tier depth (0, 1, 2, 3 — default: all tiers)
  - Auth secrets available (which pre-staged secrets power AUTH-LIVE per harness)
  - Test repo URL (for GH-01 end-to-end work product test)
  - Cloud Logging project (for TEL-01 telemetry tests)
  - Community bundle URLs (if any, for CONF-01 + full test regime)

## Output

Write all output to `/scion-volumes/scratchpad/projects/<project-slug>/harness-qa/`:

- `results.json` — machine-readable matrix (see harness-qa skill §Results Schema)
- `harness-qa-report.md` — human summary with verdict per harness
- `evidence/<harness>/<test-id>/` — captured artifacts
- `issues/<run-id>-<harness>-<test-id>.md` — one file per finding

Message the dispatching coordinator with the overall verdict and the report path.

## Standing Workflow

1. **Read the brief.** Confirm target instance, harness list, tier depth,
   and available auth secrets.
2. **Read the `harness-qa` skill fully.** This is your test catalog and
   methodology reference.
3. **Connect to the target instance** (instance-interaction skill).
4. **Execute the pre-run wipe procedure** (harness-qa skill §Pre-Run Wipe).
5. **Record the post-wipe baseline** in results.json.
6. **For each harness in scope, in order:**
   a. **Tier 0 / 0.5:** Static conformance checks (capability matrix
      completeness, vendored lib, for community bundles: C1–C9 review).
      If Tier 0 fails, record and continue to Tier 1.
   b. **Tier 0.5 (community only):** Bundle conformance review (CONF-01).
      C2/C3 failures block Tier 2 until acknowledged.
   c. **IMG-01:** Build/pull the harness image if not present.
   d. **Tier 1:** Golden path — HB-01 → HB-02 → AUTH-LIVE → MSG-01 →
      LOOK-01 → INT-01 → AUTH-05 → LCY-01.
      If any Tier 1 test fails, skip Tier 2/3 for this harness, record
      as BLOCKED, and move to the next harness.
   e. **Tier 2:** Capability depth — iterate the declared matrix. For
      each capability with support ∈ {yes, partial}: run the corresponding
      test procedure. Reuse the running probe where possible (MSG/INT/LOOK/PRV
      tests share a probe lifecycle).
   f. **Tier 3:** Cross-cutting negatives — AUTH-06, GH-01, and any
      matrix-combination tests.
   g. **Tear down** all probe agents for this harness.
7. **Generate results.json, harness-qa-report.md, and any issue files.**
8. **Message the coordinator** with the overall verdict and report path.

## Probe Agent Conventions

- Probe names: `qa-probe-{harness}-{nonce}` (e.g., `qa-probe-claude-a3f2`)
- Nonce: 4-character hex, generated once per run, shared across all probes
- Model alias: `small` (cheapest; determinism over quality)
- Canary task: "Create the file `/workspace/.qa/canary-{nonce}.txt` containing
  exactly `SCION-QA {nonce} step-1`. After that, wait for further instructions."
- Retries: up to 2 retries on model-behavior portions; record flake counts
- Evidence: capture at observation time, not at report time (crash-safe)

## Auth Secret Handling

- Auth secrets are pre-staged on the instance by the operator before the run.
- The brief specifies which secret name maps to which harness/auth-type.
- For AUTH-LIVE: use the provided secret. This credential powers the entire
  test regime for that harness (canary, MSG, INT, MCP, LIM, RES tests all
  count as continuous evidence of live auth).
- For AUTH-RESOLVE tests: create synthetic/dummy credentials shaped correctly
  for each auth mode. These test provisioning wiring, not real model access.
- Never log or echo secret values. Reference secrets by name only.

## Communication

- Use `scion message` for all communication; terminal stdout is invisible.
- Raise blockers immediately (don't batch them to the end).
- Report one harness verdict at a time if the run is long.
- Final message: overall verdict + report path.

## What You Never Do

- Modify harness code, config.yaml, or provision.py.
- Skip the pre-run wipe procedure.
- Mark a test PASS without capturing evidence.
- Run Tier 2 on a harness that failed Tier 1.
- Continue testing after the instance becomes unreachable (report and stop).
- Expose or log auth secret values.
```

### 3.4 Results & Issue Reporting

#### 3.4.1 Results Schema (`results.json`)

```json
{
  "$comment": "Machine-readable test results matrix. One file per run.",
  "run_id": "hq-YYYY-MM-DD-XXXX",
  "instance": "scion-integration-qa",
  "instance_zone": "us-central1-a",
  "instance_project": "scion-testing",
  "scion_version": "v0.x.y (commit abc1234)",
  "baseline": {
    "harness_config_versions": {
      "claude": "ef4d140",
      "gemini-cli": "ef4d140"
    },
    "image_digests": {
      "scion-claude": "sha256:abc...",
      "scion-base": "sha256:def..."
    }
  },
  "started": "2026-07-11T10:00:00Z",
  "finished": "2026-07-11T11:15:00Z",
  "harnesses_tested": ["claude", "gemini-cli", "copilot"],
  "tier_depth": 3,
  "results": [
    {
      "harness": "claude",
      "test": "MSG-01",
      "title": "Cooked message delivery",
      "tier": 1,
      "capability": "universal",
      "declared_support": "n/a",
      "outcome": "pass",
      "attempts": 1,
      "flake_count": 0,
      "duration_s": 41,
      "evidence": [
        "evidence/claude/MSG-01/look-before.txt",
        "evidence/claude/MSG-01/look-after.txt",
        "evidence/claude/MSG-01/docker-exec-cat.txt"
      ],
      "issue": null
    },
    {
      "harness": "copilot",
      "test": "MCP-01",
      "title": "MCP stdio transport",
      "tier": 2,
      "capability": "mcp.stdio",
      "declared_support": "yes",
      "outcome": "fail",
      "attempts": 3,
      "flake_count": 0,
      "duration_s": 185,
      "evidence": [
        "evidence/copilot/MCP-01/look-1.txt",
        "evidence/copilot/MCP-01/mcp-config.json",
        "evidence/copilot/MCP-01/provision-stderr.txt"
      ],
      "issue": "issues/hq-2026-07-11-a3f2-copilot-MCP-01.md"
    }
  ],
  "summary": {
    "total": 62,
    "pass": 55,
    "fail": 3,
    "flaky_pass": 2,
    "skip": 1,
    "blocked": 1
  }
}
```

**Outcome values:**
- `pass` — all verify checks succeeded on first attempt or after retries
- `fail` — verify checks failed after all retry attempts
- `flaky-pass` — passed on retry (not first attempt); flake_count > 0
- `skip` — test not applicable (capability not declared, or community-only test on shipped harness)
- `blocked` — precondition not met (e.g., Tier 1 failed, blocking Tier 2)

#### 3.4.2 Issue Documentation Format

One file per finding: `issues/<run-id>-<harness>-<test-id>.md`

```markdown
# [SEV-N] <harness> / <test-id> — <one-line summary>

- **Run:** <run_id>   **Instance:** <instance>
- **Harness:** <harness>   **Image:** <image>@<digest>   **Scion:** <version>
- **Capability:** <capability key or "universal"> (<U/C label>)
- **Test:** <test-id> (attempts: N/M failed — <"not flake" | "flake: passed on attempt K">)

## Expected

<What the test's pass criteria specify>

## Actual

<What was observed, with specific values/text>

## Root Cause Analysis

<Chain of evidence with file:line refs into scion source where identified.
Confidence: confirmed | probable | suspected.
If not root-caused: state the boundary reached and what access/info would unblock.>

## Evidence

- <path to each evidence file, relative to harness-qa/>
- Cloud Logging query + result snippet (if applicable)

## Reproduction

1. <Exact scion commands to reproduce>
2. <Observation commands>
3. <What to look for>

## Classification

- **Severity:** SEV-0 | SEV-1 | SEV-2 | SEV-3
- **Kind:** regression | config-drift | upstream-cli-change | env/instance |
           flake | matrix-misdeclaration | bundle-conformance
- **Suspected owner surface:** <specific code path or config area>
```

**Severity scale:**
- **SEV-0** — harness unusable (won't start / no model turn on golden path with valid auth)
- **SEV-1** — core interaction broken (message/interrupt/task delivery, resume loses everything, auth mode declared `yes` fails live)
- **SEV-2** — declared capability broken or degraded beyond its `partial` reason (MCP transport, limits not enforced, telemetry missing, skills not projected)
- **SEV-3** — cosmetic/latency/docs; matrix-misdeclaration (capability works but declaration wrong, or vice-versa where behavior is graceful)

#### 3.4.3 Report Format (`harness-qa-report.md`)

```markdown
# Harness QA Report — <run_id>

- **Instance:** <instance> (<zone>, <project>)
- **Scion version:** <version>
- **Run window:** <started> – <finished>
- **Tier depth:** <N>
- **Overall verdict:** PASS | PASS-WITH-ISSUES | FAIL

## Per-Harness Verdicts

| Harness | Verdict | Pass | Fail | Flaky | Skip | Blocked | Issues |
|---------|---------|------|------|-------|------|---------|--------|
| claude | PASS | 28 | 0 | 1 | 0 | 0 | 0 |
| copilot | FAIL | 20 | 2 | 0 | 3 | 5 | 2 |
| ... | | | | | | | |

## Test Matrix

<Full matrix table: harness × test-id → outcome>

## Issues

1. [SEV-1] copilot / MCP-01 — see issues/hq-...-copilot-MCP-01.md
2. ...

## Flake Register

| Harness | Test | Attempts | Pass On |
|---------|------|----------|---------|
| claude | HB-02 | 2 | attempt 2 |

## Not Tested

- <test-id>: <reason> (e.g., "TEL-01 for hermes: telemetry.enabled = no")
- ...

## Baseline

<Harness-config versions, image digests, scion version — from results.json>
```

### 3.5 Scratchpad Structure

```
/scion-volumes/scratchpad/projects/<project-slug>/harness-qa/
├── results.json
├── harness-qa-report.md
├── evidence/
│   ├── claude/
│   │   ├── HB-01/
│   │   │   ├── scion-list.json
│   │   │   ├── docker-inspect.json
│   │   │   └── resolved-auth.json
│   │   ├── MSG-01/
│   │   │   ├── look-before.txt
│   │   │   ├── look-after.txt
│   │   │   └── docker-exec-cat.txt
│   │   └── .../
│   ├── copilot/
│   │   └── .../
│   └── .../
└── issues/
    ├── hq-2026-07-11-a3f2-copilot-MCP-01.md
    └── .../
```

Evidence is written at capture time (crash-safe). The QA agent creates directories as it goes. Results.json and the report are written at the end.

---

## 4. Alternatives Considered

### 4.1 Multi-File Skill with YAML Test Catalog Files

**Considered:** A `harness-qa/` skill with `tests/*.yaml` files and a `fixtures/` subdirectory, as proposed in the investigation findings (§3.1).

**Rejected because:**
- All 16 existing skills are single-file (`SKILL.md` only). Introducing a multi-file skill requires a new convention with no platform support (skill projection reads SKILL.md).
- The test catalog is agent-interpreted, not machine-parsed. Embedding it in SKILL.md keeps it in the agent's projected context.
- Fixtures are small enough to inline as code blocks.

**Reversibility:** High. If the skill grows past ~100 tests or the platform adds multi-file skill support, the embedded catalog can be extracted to separate files with no impact on the template or results format.

### 4.2 Extending the Existing `qa-tester` Template

**Considered:** Adding harness-testing capabilities to the existing `qa-tester` template rather than creating `qa-harness-tester`.

**Rejected because:**
- Different audiences: `qa-tester` validates fork branch code changes; `qa-harness-tester` validates infrastructure configurations.
- Different targets: `qa-tester` deploys and tests a branch on an instance; `qa-harness-tester` tests harness bundles already installed on a dedicated instance.
- Different cadence: `qa-tester` runs per-PR; `qa-harness-tester` runs on-demand for release or regression sweeps.
- Merging would overload `qa-tester`'s instructions and confuse the agent's mission.

**Reversibility:** High. If the templates converge in practice, merging them later is straightforward — the skills are independent.

### 4.3 Scripted Test Runner (Bash/Python) Instead of Agent-Interpreted Catalog

**Considered:** A deterministic test runner script that executes the mechanical portions (docker exec, scion look, file checks) without LLM judgment.

**Rejected for v1 because:**
- Many verification steps require judgment: reading TUI state, interpreting model output, adapting to timing variations, deciding whether a harness is "at its input prompt."
- The agent-interpreted approach is faster to ship and more resilient to harness UI differences.
- The catalog format is designed so that a scripted runner can be layered on later for the purely mechanical checks (Tier 0, docker_exec assertions) without changing the catalog.

**Reversibility:** High. A scripted runner consuming the same test definitions is a natural follow-up, not a replacement. The agent and the script can coexist (agent delegates mechanical checks to the script).

### 4.4 Capability Matrix as External YAML Oracle (Not Inline in config.yaml)

**Considered:** Maintaining a separate "expected capabilities" file in the QA skill, decoupled from each harness's self-declaration.

**Rejected because:**
- The declared matrix in `config.yaml` is already consumed by the Go host (`pkg/harness/resolve.go`). It is the canonical truth about what a harness claims to support.
- A separate oracle would drift from config.yaml and create a maintenance burden.
- The investigation's finding about matrix incompleteness (undeclared keys) is better solved by a completeness lint (Tier 0) than by a shadow matrix.

**Reversibility:** N/A — this is a load-bearing decision. The entire test plan generation depends on treating config.yaml's capability matrix as the oracle. Changing this would require restructuring how tests are selected per harness.

---

## 5. Migration / Rollout

### 5.1 No Breaking Changes

Both artifacts are purely additive:
- `skills/harness-qa/SKILL.md` — new file in a new directory
- `templates/qa-harness-tester/` — new directory with 3 files

No existing skill, template, or harness code is modified. No schema changes to config.yaml or the Go host.

### 5.2 Rollout Sequence

1. **Phase 1 ships the skill and template** — immediately usable by creating a QA agent with the template and attaching the skill.
2. **Phase 2 validates with a pilot run** — a QA agent runs against 2–3 harnesses to validate the methodology and refine the test catalog.
3. **Phase 3 refines** — catalog updates based on pilot findings, committed to the same feature branch.

No feature flags, no gradual rollout, no backward compatibility concerns. The artifacts are inert until a QA agent is dispatched.

---

## 6. Open Questions

1. **MCP fixture hosting on locked-down instances.** The MCP-01/02/03 tests require running a small HTTP server on the QA instance. If the instance's firewall or security policy blocks this, the MCP tests need an alternative (e.g., a pre-deployed fixture service). The QA agent should detect this at runtime and skip MCP-02/03 with a `blocked` outcome if the HTTP fixture can't bind. The stdio fixture (MCP-01) is local-only and should always work.

2. **Cloud Logging access from QA agent identity.** TEL-01 requires querying Cloud Logging. The QA agent's service account (or the SSH user on the instance) needs `roles/logging.viewer` on the logging project. If not available, TEL-01 should be skipped with a `blocked` outcome and the brief should note the gap.

3. **Tier-0 matrix-completeness lint placement.** The investigation recommends this as a Go CI test rather than a QA-agent check. This design includes it in Tier 0 (the QA agent runs it), but a parallel CI implementation is a strong follow-up — it's deterministic and should catch regressions before a QA run is even dispatched. Not blocking for this project.

---

## 7. Implementation Phases

### Phase 1: Skill Foundation (1 developer agent session)

**Scope:** Create `skills/harness-qa/SKILL.md` with:
- Frontmatter (name, description)
- Overview and methodology sections
- Pre-run wipe procedure
- Test definition format documentation
- Tier 0 and Tier 1 test catalog (IMG-01, HB-01, HB-02, MSG-01, LOOK-01, INT-01, AUTH-LIVE, AUTH-05, LCY-01) — the 9 tests needed for golden-path verification
- MCP fixture specifications (code blocks)
- QA skill fixture (code block)
- Results schema (results.json)
- Issue documentation format
- Report format (harness-qa-report.md)
- Evidence collection rules

**Output:** A functional skill that supports Tier 0–1 runs. Commit and push.

**Size:** ~800–1000 lines of SKILL.md. Single developer agent session.

### Phase 2: Template (1 developer agent session)

**Scope:** Create `templates/qa-harness-tester/` with:
- `scion-agent.yaml`
- `system-prompt.md`
- `agents.md` (full role definition, workflow, conventions)

**Dependency:** None (can be developed in parallel with Phase 1, but references the skill content).

**Output:** A functional template. Commit and push.

**Size:** 3 files, ~200 lines total. Single developer agent session.

### Phase 3: Tier 2 Test Catalog (1 developer agent session)

**Scope:** Extend SKILL.md with the Tier 2 test procedures:
- AUTH-RESOLVE-01..04 (dummy auth per declared mode)
- AUTH-06 (explicit invalid type)
- MCP-01, MCP-02, MCP-03
- LIM-01, LIM-02, LIM-03
- PRV-01, PRV-02, PRV-03
- RES-01
- TEL-01
- MOD-01

**Dependency:** Phase 1 (extends the same file).

**Output:** Complete Tier 2 catalog. 16 additional test procedures. Commit and push.

**Size:** ~500–600 additional lines. Single developer agent session.

### Phase 4: Tier 3 + Community Bundle Track (1 developer agent session)

**Scope:** Extend SKILL.md with:
- Tier 3 cross-cutting tests (GH-01, MSG-02, MSG-03)
- Community bundle conformance checklist (C1–C9) as a dedicated section
- CONF-01 meta-test procedure
- Community bundle install-from-source procedure
- Bundle-local golden-fixture runner documentation

**Dependency:** Phase 3 (extends the same file; the conformance checks reference capability schema knowledge from Tier 2).

**Output:** Complete skill with all 31 test procedures + C1–C9 conformance. Commit and push.

**Size:** ~400–500 additional lines. Single developer agent session.

### Phase 5: Pilot Run + Refinement (1 QA agent session + 1 developer agent fixup)

**Scope:**
- Dispatch a QA agent (from the template) against 2–3 harnesses (recommend: claude, copilot, one of opencode/gemini-cli) on the dedicated QA instance.
- Validate that the methodology works end-to-end: wipe, Tier 0/1, at least partial Tier 2.
- Collect findings about the *methodology itself* (ambiguous pass criteria, missing evidence types, timing issues).
- Developer agent refines the skill based on pilot findings.

**Dependency:** Phases 1–4 complete.

**Output:** Refined SKILL.md, first real `results.json` and `harness-qa-report.md`. Commit and push.

**Size:** 1 QA agent session (~1–2 hours wall time) + 1 developer fixup session.

### Phase Summary

| Phase | Content | Dependency | Agent Sessions | Independently Shippable? |
|-------|---------|------------|----------------|--------------------------|
| 1 | Skill foundation (Tier 0/1 catalog + schemas + fixtures) | None | 1 dev | Yes — supports golden-path runs |
| 2 | Template (3 files) | None | 1 dev | Yes — with Phase 1 |
| 3 | Tier 2 catalog (16 tests) | Phase 1 | 1 dev | Yes — extends coverage |
| 4 | Tier 3 + community track | Phase 3 | 1 dev | Yes — extends coverage |
| 5 | Pilot run + refinement | Phases 1–4 | 1 QA + 1 dev | Yes — validates everything |

Phases 1 and 2 can run in parallel. Phases 3 and 4 are sequential (same file). Phase 5 requires all prior phases.

---

## 8. Acceptance Criteria

The QA tester or reviewer should verify the following before this project is considered done:

### Skill (`harness-qa`)

- [ ] `skills/harness-qa/SKILL.md` exists with valid frontmatter (`name: harness-qa`, description present)
- [ ] SKILL.md contains all 31 test procedures from the catalog (§3.2.3), each with: id, title, tier, applies_when, preconditions, steps, verify, evidence, on_fail
- [ ] Every test procedure's pass criteria are unambiguous — a different QA agent reading the same procedure would reach the same verdict given the same observations
- [ ] Test IDs are stable and match the investigation's naming (HB-01, MSG-01, AUTH-LIVE, etc.)
- [ ] Pre-run wipe procedure is documented with exact commands
- [ ] Community conformance checklist (C1–C9) is present with severity ratings
- [ ] MCP fixture code blocks are present and syntactically valid Python
- [ ] QA skill fixture (XYZZY token) is present
- [ ] Results schema (`results.json`) is documented with all fields, outcome values, and an example
- [ ] Issue documentation format is documented with all fields, severity scale, and kind values
- [ ] Report format (`harness-qa-report.md`) template is documented
- [ ] Evidence collection rules are documented (capture at observation time, directory structure)

### Template (`qa-harness-tester`)

- [ ] `templates/qa-harness-tester/scion-agent.yaml` exists with `schema_version: "1"`, description, agent_instructions, system_prompt
- [ ] `templates/qa-harness-tester/system-prompt.md` exists with a persona description (not workflow)
- [ ] `templates/qa-harness-tester/agents.md` exists with sections: Role, Skills You Rely On, Inputs You Expect, Output, Standing Workflow, Probe Agent Conventions, Auth Secret Handling, Communication, What You Never Do
- [ ] agents.md references all 4 skills: `harness-qa`, `instance-interaction`, `integration-testing`, `scion-process`
- [ ] agents.md specifies the scratchpad output structure
- [ ] agents.md specifies probe naming convention (`qa-probe-{harness}-{nonce}`)
- [ ] agents.md specifies canary task format

### Integration

- [ ] No existing files modified (purely additive)
- [ ] File naming follows kebab-case conventions
- [ ] Cross-references to other skills use backtick format (`` `skill-name` ``)
- [ ] The design doc itself is committed to `.design/quality-engineering.md` in the repo

### Pilot Run (Phase 5)

- [ ] A QA agent using the template can execute at least Tier 0 and Tier 1 against one harness without human intervention (aside from pre-staged secrets)
- [ ] `results.json` produced is valid JSON matching the documented schema
- [ ] `harness-qa-report.md` produced follows the documented template
- [ ] Evidence files are captured at observation time (not reconstructed at report time)
- [ ] Any methodology issues found during the pilot are addressed in a refinement commit

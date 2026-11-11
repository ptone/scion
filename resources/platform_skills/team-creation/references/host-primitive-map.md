# Host primitive map

Fill one column per source host. The **Scion** column is what the port's runtime contract states.
**Lifecycle** is the part that bites: what must be torn down, and what outlives what.
The Cursor column is proven by the pstack port. The Claude Code column is inferred and must be verified when used.

| Concept | Cursor | Claude Code plugin | Scion | Lifecycle / gotchas |
|---|---|---|---|---|
| Spawn a helper | `Task` with `subagent_type` | `Task`/Agent tool with `subagent_type` | Tier 1: the in-harness subagent tool, for read-only, same-vendor, one-turn work. Tier 2: `scion start <name> --type <tpl> --harness-config <h> [--model <alias>] "<pointer task>"`, for a different vendor, a branch, or work that outlives the turn | Tier 2 is always background, in its own container and clone. Use unique names and a fresh agent per unit. Delete it when done. |
| Per-call model | `model:` slug on Task | `model:` in agent frontmatter | A role in a models file resolved to `--harness-config` + `--model` alias (`small`…`extra-large`) | Listed does not mean runnable. Preflight each harness-config. "Different family" means a different harness-config vendor. |
| Background | `run_in_background: true` | `run_in_background` | Implicit for Tier 2 | |
| Completion | Task notification | Task result | State-change event, REPORT message, and an inbox file in the store (the file is primary) | Messages can be lost. Recover with `scion look <n> --full --plain`. |
| Readonly | `readonly: true` (also drops MCPs) | tool allowlist | A brief clause plus a template rule. Carve out the agent's own inbox file | No enforcement. Keep the intent, not the flag. |
| Where the brief goes | Inline prompt | Inline prompt | A file in `<store>/<program>/briefs/`. The task argument is a short pointer | A 31 KB inline task crashed 5 of 5 agents. |
| Return artifacts | Paths in a shared FS, `/tmp` | same | A pushed branch (reviewed with `git fetch` + `git diff`) or a store file | Containers vanish, and their `/tmp` goes with them. |
| Ask the human | `AskQuestion` | `AskUserQuestion` | `sciontool status ask_user "<q>"` then `scion message` with numbered options, a recommendation, and a default | 2000-character cap. Long form goes in the store. |
| Wait / wake | `/loop`, cloud sleeper | `/loop`, hooks | Agent events: `sciontool status blocked`. External events: a background-shell watcher plus a `scion schedule create` self-callback | **A schedule persists until deleted.** Record its ID, and delete it at stand-down and at close. |
| Session goal | `/goal` | — | `<store>/<program>/goal.md` | |
| Model rules file | `~/.cursor/rules/*.mdc` | `CLAUDE.md` / settings | `<store>/<pack>-models.yaml`, written by a setup skill | |
| Slash commands | `/skill-name` | `/command` | Routing in the lead's `agents.md`. Naming a skill routes to it | Slash-only frontmatter (`disable-model-invocation`) can hide the skill. Read skills as files. |
| Transcripts | `agent-transcripts/` | `~/.claude/projects/*.jsonl` | (0) your own harness log, (1) a trail file you write during the run, (2) `scion conversation`, (3) `scion look` | `scion logs` returned 403 even for agents the caller started. |
| Local vs cloud | `environment:` | — | Not applicable. "Needs this machine" becomes a template service, mount, or secret, or a gate | |
| Base branch | `cloud_base_branch` | — | Named in the brief. The worker runs `git fetch` + `checkout -B` | |
| Shared state | the agent store (implicit) | project dir | A mounted shared dir: `/scion-volumes/<name>/<pack>/<program>/`, with one writer per file | Templates cannot declare shared dirs. Check the mount, since `shared-dir list` can say none while one is mounted. PID locks are meaningless across containers. |
| MCP servers | inherited by subagents | inherited | Declared per template (`mcp_servers`) | Moving a step to a container can silently drop a data source. Report it as a gap. |
| Built-in skills | `create-skill`, `/babysit` | built-in commands | Absent. Point to an equivalent playbook, or mark the item optional | |
| Project-local skill output | `.cursor/skills/` | `.claude/skills/` | `.scion/skills/<name>/`, plus a delivery path (template-local copy, publish, or URI) | Scion never loads skills from the repo. |
| Human surface | IDE the user watches | terminal | Messages only | Rewrite "open the diff/debugger" as "write to the store and send a pointer". |

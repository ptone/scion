# Runtime contract skeleton

Copy this to `skills/<pack>-scion-runtime/SKILL.md`, replace `<pack>`, and delete the rows your pack
does not use. The finished pstack version is `skills/pstack-scion-runtime` in scion-frontiers/scion-pstack. It is the
worked example.

````markdown
---
name: <pack>-scion-runtime
description: >-
  How <pack>'s host primitives (<list the ones your pack names>) map onto Scion. Read this
  before any other <pack> skill whenever you run <pack> inside Scion, and whenever a <pack> skill
  tells you to spawn a helper, pick a model, ask the human, wait, or read history.
---

# <pack> on Scion

When a <pack> skill and this table disagree about *mechanics*, this table wins. When they disagree
about *judgment*, the <pack> skill wins.

## 0. Skills are files. Read them directly.
When a <pack> skill names another skill, open `$HOME/<skills_dir>/<name>/SKILL.md`
(`ls -d $HOME/.*/skills/*/` finds it). Do not rely on your harness's skill tool, because
slash-only frontmatter can hide skills from it. Relative paths resolve against that skill's directory.

## 1. Delegation
- Tier rule. Use a Scion agent if the step needs a different vendor, writes a branch, or outlives
  your turn. Otherwise use your in-harness subagent tool, or do the step inline.
- `<source subagent type>` → template table.
- Model role → harness-config + model, resolved from `<pack>-models.yaml` (§3).
- Spawn defaults:
  - Naming: `<lead>-<role>-<unit>`, one fresh agent per unit.
  - The brief goes in `<store>/<program>/briefs/<agent>.md`. The task argument is a short pointer.
  - Readonly is a brief clause that allows the agent's own inbox file.
  - The brief ends with the REPORT line: write `<store>/<program>/inbox/<agent>.md`, message `@<lead>`, signal completion.
- Completion: wake on the message or the COMPLETED event, then read the inbox file. Wait with
  `sciontool status blocked`. Never poll. Recover a missing report once with `scion look --full --plain`.
- Crashed at start: retry once with a pointer-only task. On a second crash, mark the harness unavailable (§3).
- Results come back on a pushed branch or in the store. Review diffs yourself.

## 2. Other primitives
| <source host> | Scion |
|---|---|
| ask the human | `sciontool status ask_user`, then `scion message` with options, a recommendation, and a default |
| wait / loop | blocked for agent events. For external events, a watcher plus `scion schedule create`. Record the schedule ID and delete it at stand-down and at close |
| transcripts | (0) own harness log, (1) the trail file, (2) `scion conversation`, (3) `scion look`. Name the source you used |
| `<built-in or kit dependency>` | the referenced skill by name, or a substitute only if Phase 1 classified it host-proprietary |
| ... | ... |

## 3. <pack>-models.yaml
Schema: `roles:` (role → `{harness, model}`, `inherit`, or an alias to another role) and
`panels:` (list of harness-configs). Name the fallbacks for missing roles.
Listed is not runnable: the setup skill preflights every entry.
**Degraded diversity:** when a panel collapses to fewer vendors, run it anyway, label reviewers by
the vendor that actually ran, and say so in the verdict. Prefer a non-model check to make up for it.

## 4. The <pack> store
Resolution order: `$<PACK>_STORE`, then a mounted shared dir (`ls -d /scion-volumes/*/`, with `/<pack>` appended), then a gate.
Layout: `<root>/<pack>-models.yaml`, and `<root>/<program>/` holding every per-program file.
Nothing per-program goes at the root.
Every file has one writer. Workers write only under `inbox/`. Keep the store out of commits and cleanup.

## 5. Human surface
Replies go by `scion message` into the originating conversation, 2000 characters at most, with long form in the store.
A human naming a skill routes to it. Signal status before every turn ends.
````

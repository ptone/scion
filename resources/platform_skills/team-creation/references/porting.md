# Porting an agent pack to Scion

Loaded from the **team-creation** skill. Paths below (`scripts/`, `references/`) are relative to the team-creation skill directory. Worked example: the pstack port, [scion-frontiers/scion-pstack](https://github.com/scion-frontiers/scion-pstack/tree/eddd0b25b8850a454ffd2ad9bb5e22242816c5fd) (Cursor plugin → four templates). It is private at the time of writing.

A pack is mostly portable prose wrapped around a handful of host primitives. The port that
re-syncs cheaply keeps the prose, references unchanged skills from upstream by URI, forks only
what names a host primitive, and puts every host translation in **one** runtime contract skill
that the forks defer to. The team-creation SKILL.md covers formats and verification. This file
covers what is specific to porting.

Work in phases, in order. Keep a `LEARNINGS.md` in the project folder from the start. Each entry
records what happened, why it matters, and what it changes. That log is half the deliverable.

## Phase 0. Workspace and snapshot

- Keep drafts in a shared dir (`/scion-volumes/<name>/<project>/`), never in a repo you will commit to.
- Pin the upstream ref to a **commit SHA** and snapshot it into `upstream/<pack>-<sha7>/` (drop images and other binary assets). Every URI and every diff is against that SHA.
- Snapshot **sibling packages** from the same repo at the same SHA whenever the pack cites them (see Phase 1, dependency triage).
- Record the license of each package you will reference or fork.

Project layout that worked:

```
<project>/
  upstream/<pack>-<sha7>/        # read-only snapshot
  skills/<name>/                 # adapted forks only, each with PORT-NOTES.md
  templates-src/<role>/agents.md # hand-authored per role
  build.sh                       # assembles templates/ (example: scion-pstack build.sh)
  templates/                     # build output, synced to the hub
  diffs/<name>.diff              # fork diff against upstream, regenerated each build
  store/                         # scratch state for trial runs
  DESIGN.md  LEARNINGS.md
```

## Phase 1. Triage

Run `bash scripts/triage.sh <upstream>/<pack> <host>`. It buckets each skill as `verbatim`,
`check-boundary`, or `adapt`. It also lists frontmatter keys, cited external packages, sibling
packages, scripts, and agent files. Then do these by hand. The script only finds candidates.

1. **Dependency triage comes before primitive mapping.** For every external name the pack cites
   (a kit, a plugin, a built-in skill), open it. Is it in the same repo? What is its license?
   Does it name host primitives? Classify it as **referencable** (URI, verbatim), **host-proprietary**
   (needs a substitute), or **absent** (a gate or an optional skill). A vendor's name in a package name
   is not evidence of lock-in. The pstack port first wrote substitutes for three MIT, host-neutral
   `cursor-team-kit` skills that could simply be referenced.
2. **Grep every file, not just SKILL.md.** That includes references, prompt templates, runner
   prompts, and scripts. A "pure judgment" skill usually still names a default model somewhere.
3. **Scripts encode the prose they check.** A validator that requires literal strings breaks
   silently when you edit the playbook it validates. List every literal the script matches or emits.
4. **Classify each hit before replacing it.** Some are external facts, like a bot's GitHub login or a
   product name in an example. Those stay.
5. **Check frontmatter keys against every target harness.** Example: `disable-model-invocation: true`
   hides a skill from Claude Code's Skill tool. The runtime contract makes skills readable as files
   (`references/runtime-contract-skeleton.md` §0), so verbatim skills still work.
6. **Read the agent definitions yourself.** A role summary ("read-only reviewer") can hide a write
   the workflow depends on.
7. Drop what cannot run here (for example webhook automations owned by the source host), and say so in DESIGN.md.

## Phase 2. Map host primitives

Fill the map in `references/host-primitive-map.md` for your source host. Each row names the Scion
mechanism **and its lifecycle**. A clean-looking mapping can differ in teardown: `/loop` dies with
the session, but a `scion schedule` lives until it is deleted. The rows that always need a decision:

- **Delegation.** A subagent call becomes one of two tiers. It becomes a Scion agent when the step
  needs a different vendor, writes a branch, or outlives the turn. Otherwise it stays an in-harness
  subagent.
- **Per-call model.** A model slug becomes a role in a models file, resolved to `--harness-config`
  plus `--model` alias at spawn. Use one template per role, never one per model.
- **Shared filesystem.** `/tmp`, worktrees, and "return the paths" become a pushed branch or a file
  in a shared store. Separate containers share nothing.
- **Ask the human.** This becomes `sciontool status ask_user` plus `scion message`, with a
  2000-character cap. Long form goes in the store.
- **Slash commands.** These become routing in the lead's `agents.md`. A human naming a skill
  routes to it.
- **Transcripts and history.** These become a trail file written during the run. There is no
  after-the-fact store you can rely on.
- **Readonly and permission flags.** Keep the intent (write nothing, keep data access) as a brief
  and template clause. Scion has no flag for it.

## Phase 3. Write the runtime contract skill

Create `<pack>-scion-runtime`, starting from `references/runtime-contract-skeleton.md`. It is the
only place host mechanics live. Rules: mechanics defer to the runtime, and judgment defers to the
upstream skill. Every adapted skill points at a runtime section instead of restating it. When a
trial or a fork finds a new rule, it goes here once.

## Phase 4. Fork minimally

- Fork only `adapt` skills. Change the host-primitive lines and nothing else. Put a
  `PORT-NOTES.md` beside each fork, listing each edit, why it was made, and where the behavior of any
  dropped frontmatter went.
- Before rewriting upstream text, look for an existing override hook, such as a `--store` flag or
  an env var.
- Prefer `bun script.ts` or `bash script.sh` over `./script`. Current hubs keep exec bits on URI
  installs, but older ones did not.
- Session state becomes files in the store (`goal.md`, `resume.md`), and each file is added to the
  runtime's store layout. Paths name `<store>/<program>/...`, never the store root.
- Scripts need **negative fixtures**. For every literal marker you change, prove that deleting the
  guarded content still fails. Check the fixtures in next to the script. Also run a parity corpus
  that compares the old script on the old template against the new script on the new template.
- Keep interface tokens byte-identical. A token one prompt emits and another parses is an API.
- Anything that *generates* a project-local skill must also say how agents receive it. Scion loads
  skills from templates, platform skills, and URIs, never from the repo.
- After forking, **rerun the triage grep over the finished tree**. Include the container-boundary
  terms (`/tmp`, `~/`, `localhost`, "return the paths"). Partial substitution, where one line was
  fixed and the next was not, is the most common residue.

## Phase 5. Assemble templates

Formats, URI grammar, the `system-prompt.md` warning, and roles-not-models are all in SKILL.md and
`references/skill-uris.md`. Porting adds these:

- **Team shape.** Build a lead (the entry point, which takes on the pack's "main agent" mode, such
  as always-on routing), one worker, one read-only reviewer, and one template for each named
  subagent whose behavior differs.
- **Unchanged upstream skills go by SHA-pinned `https://github.com/.../tree/<sha>/<path>` URI**,
  including cited sibling packages. Forks and the runtime ship template-local, or by URI to your
  port repo once it exists.
- **Generate `scion-agent.yaml` and the skill lists with a build script.** Hand-maintaining about 30
  URIs across 4 templates drifts. See [`build.sh`](https://github.com/scion-frontiers/scion-pstack/tree/eddd0b25b8850a454ffd2ad9bb5e22242816c5fd/build.sh) in the worked example.
- **Foreign agent files do not import** (for example Cursor `agents/*.md`, whose frontmatter
  `scion templates import` does not recognize). Hand-port each one into `agents.md` as a short Scion
  wrapper (scope, where writes go, report channel) followed by the upstream body verbatim.

## Phase 6. Verify

Run SKILL.md's **Verify a Team** (provision smoke, harness preflight), with a rung on each side:

1. **Unit, first.** Upstream tests pass on the forks. Every literal marker a script checks has a
   negative fixture. A parity corpus applies N mutations to the old and new templates, runs each
   through its own validator, normalizes the swapped marker strings, and diffs. Every row should
   match, or be stricter on the new side for a reason you can state.
2. **Dogfood trial, last.** Start the lead and have it run the pack's own review workflow
   (interrogate, review, audit) on the port's own diff. Give it a brief file, a store dir, and a
   request to add a "Port friction" section to its verdict. Treat the verdict like any review: fix,
   rebuild, re-sync, then delete the trial agents.

What each rung caught in the pstack port:

| Rung | Caught |
|---|---|
| Unit | Upstream tests still green after the script edits |
| Smoke | All 27 pinned URIs resolved. Later, sibling-kit URIs resolved from a different subpath |
| Preflight | 7 of 8 listed vendors could not start in the project |
| Dogfood | A silently dropped validator check; a `/tmp` screenshot path; a schedule with no teardown; store-root collisions; brief-size crashes; a lost report message |

## Phase 7. Report and hand off

- Send milestone messages at the end of triage, forks, first sync, and each trial verdict. Keep each
  under 2000 characters and point to DESIGN.md, LEARNINGS.md, and verdict files.
- Split platform gaps (things only Scion can fix) from port defects (things you fix). Report the
  platform gaps separately so they can be filed.
- Leave a re-sync recipe in DESIGN.md: bump the SHA, rerun triage, rebase the forks against
  `diffs/`, rebuild, smoke-test.

## Checklist

- [ ] Upstream is pinned to a SHA, snapshotted, and its license noted. Cited siblings are snapshotted too.
- [ ] `triage.sh` output is reviewed, and each external dependency is classified before any substitute is written.
- [ ] The host-primitive map is filled in, with lifecycle and teardown for each row.
- [ ] One runtime contract skill exists, and every fork defers to it.
- [ ] Forks are minimal, each has `PORT-NOTES.md`, and the diffs are regenerated.
- [ ] Changed script markers have negative fixtures, and the parity corpus is green.
- [ ] The finished-tree grep is clean, including container-boundary terms.
- [ ] The templates pass the SKILL.md validation checklist, and upstream URIs are SHA-pinned.
- [ ] Verify a Team has passed (smoke, preflight). The dogfood trial verdict is addressed.
- [ ] LEARNINGS.md is up to date, and platform gaps are reported separately.

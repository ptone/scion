# Brief: measure how many agents a single-node Cloud Run Instance actually supports

Author: sn-impl-arch (architect). Date: 2026-08-27. Approved by ptone 06:24.

You are the developer. I designed this; I do not implement it. **Read the whole brief before you
start.** If a step contradicts what you find, **stop and message me** — do not improvise. That rule
has caught several of my own errors on this project, four of them last night.

**Two agents are running this brief in parallel, at different instance sizes.** Your size is in your
dispatch message. Do not touch the other agent's instance.

---

## 1. Why this exists

ptone asked what sizing guidance we give a new operator. The honest answer was: **none, and no data
to write it from.** We have never run more than **one** agent on an instance. Every walk of the §1
path was single-agent. The tier merged upstream at `f99a8189` with `--cpu 4 --memory 8Gi` as
defaults and nobody has ever tested what those defaults hold.

## 2. What I want out of this. Three things, and the third matters most.

1. **The curve** — marginal cost per agent, so we can predict other sizes instead of testing each.
2. **The ceiling** — how many agents your size actually holds.
3. **The failure mode at the ceiling.** This is the one I care about most.

On (3): a tester *will* hit the limit. The question is what they see when they do. A clean refusal
is fine. Silent degradation is bad. The OOM killer taking the instance down and every agent on it
with it is very bad. **We do not know which of the three it is, and that is the single most valuable
thing you can find out.** A ceiling number with no failure mode is half a result.

## 3. Method

### 3.0 FIRST — validate your instrument before you run the experiment

**Before any ladder, establish how you are measuring memory and CPU, and prove the measurement
responds.** Take a baseline with zero agents, start exactly one agent, and confirm the number moves
in the direction and rough magnitude you expect.

Do not skip this. A metric that reads plausibly but is actually scoped to the wrong thing — a
sandbox's cgroup instead of the instance's, say — will produce a clean-looking curve that means
nothing. **Last night I produced three separate measurements that were technically correct about the
wrong population.** Tell me what instrument you chose and what the one-agent delta was.

Cloud Monitoring `run.googleapis.com/container/memory/utilizations` and `.../cpu/utilizations` are
the obvious candidates. If you find something better from inside the instance, say so.

### 3.1 The ladder

**Add agents ONE AT A TIME.** After each one, record:

- agent count attempted, and count **independently confirmed alive** (see §4 trap 1)
- instance memory and CPU
- time for that agent to reach a working state
- anything that changed for the *already-running* agents

**Do not launch N at once.** A batch launch cannot distinguish a capacity limit from an admission
or rate-limit problem, and it destroys the curve.

Continue until an agent fails. Then **characterise the failure** (§3.3) before doing anything else.

### 3.2 Two phases. Idle and working agents are not the same thing.

**Phase A — idle agents.** Started, attached, sitting at a prompt, doing no work. This gives fixed
overhead per agent. **It is the flattering number and on its own it is misleading**, but it isolates
the constant, which the curve needs.

**Phase B — working agents.** Each running a real task, and **the same task**, so load is uniform.
Something that genuinely uses CPU and memory — a clone plus a build, or a test run — in a loop, not
a `sleep`. This gives the number we would actually publish.

**I expect a large gap between A and B. If we publish one number, it is B.** Report both.

### 3.3 Characterise the failure. Do not just record that it happened.

When the ladder breaks, answer all of these:

- What did the operator see? Exact error text, and where it surfaced — CLI, hub UI, or nowhere.
- Was it a **clean refusal** (request rejected, nothing else affected)?
- **Did the already-running agents survive?** Check each one independently.
- **Did the hub stay up and responsive?**
- Did anything get OOM-killed? Check the instance logs for it specifically.
- Is the failure **repeatable** at the same N, or does it wander?

## 4. Two traps that will silently invalidate your result

### Trap 1 — DO NOT count the hub's "running" agents

**That signal has lied to us before.** Task #17 on this project was exactly this: the hub reported
agents as `running` while the sandbox entrypoint had hung. A stress test that counts hub state will
**overcount, and produce a number that is too good.**

**Prove liveness independently.** Attach to the agent and confirm the harness process is actually
alive and responsive. Define your liveness probe explicitly in your report so I can judge whether it
is strong enough. If you can get an agent to do a trivial round-trip that only a live harness could
complete, that is better than a process check.

### Trap 2 — specify `template` AND `harnessConfig` explicitly on EVERY create

Defects #37 and #48 mean a create with neither specified fails with a **500 for reasons that have
nothing to do with capacity**. `harnessConfig: "claude"` and `template: "default"` are known to work
— an explicit create returned 201 where the identical implicit one returned 500.

Without this you will measure a known bug and call it a ceiling.

### The general form of both traps

We have an **open register of defects** that this test will trip over: `ptone/scion#1287` through
`#1299`, plus tasks #15, #32, #35, #37, #39, #46, #48, #49. **Before you attribute any failure to
capacity, run the same operation at one agent and confirm it succeeds there.** A/B before
attribution. If it fails at N=1 too, it is a defect, not a ceiling.

This is not a formality. It is the single most likely way this test produces a confident wrong
answer that we then publish.

## 5. Your instance

Deploy your **own** instance. Project `ptone-experiments`, region `us-east4`. Credentials come from
the metadata server — no key file. Impersonate
`scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`. **Do not print access tokens to
stdout; this has happened before on this project.**

Image: `us-docker.pkg.dev/ptone-misc/scion-alt/scion-omni@sha256:e3eab113675848be634513b1e35bb40a03c0ba109b4ce771eac4b8905beafaaa`

Use the **digest**, not a tag. A tag can be moved under you mid-test and then your numbers describe
an artifact you cannot identify.

**If your dispatch says LARGEST:** the max size is not documented in the CLI help. Find it
empirically — attempt a deploy at an absurd size (say 32 CPU / 128Gi) and read the rejection, which
should name the real limit. Report what the limit is; that is a useful finding by itself.

**Tear your instance down when you are finished.** Report the cost if you can get it.

## 6. What you must NOT do

- **Do not touch the other stress agent's instance.** You each own exactly one.
- **Do not delete any Instance or agent that is not yours.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`,
  `q2-control`, `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t` are all **do-not-delete**. `sn-ready`
  is ptone's live instance — do not touch, restart or delete it. Keep `iap-demo` up.
- **Do not fix any defect you find.** Report it. You are measuring, not repairing.
- **Do not touch any branch, PR, or code.** This task produces a measurement, not a commit.
- **Do not publish or infer a sizing recommendation.** Give me the data; the recommendation is a
  design decision and it is mine.
- **Do not round, smooth, or extrapolate your numbers.** Report what you measured. If N wandered
  between runs, say it wandered — that is a finding, not noise to be tidied away.

## 7. Report back

Message `sn-impl-arch` with:

- Your instance size, and for LARGEST, the real maximum and how you determined it.
- **Your instrument, and the one-agent delta that validated it** (§3.0).
- **Your liveness probe** (§4 trap 1).
- Phase A: the ladder, and the ceiling.
- Phase B: the ladder, and the ceiling.
- **The failure mode, answering every question in §3.3.**
- Every defect you tripped over, and for each, the A/B result at N=1.
- Anything that surprised you.

**If a premise in this brief turns out to be wrong, stop and tell me.** I would much rather revise
it than have you build on a wrong premise of mine. Several of mine have been wrong this week, and
one of them was caught only because a developer refused to accept a number I asserted.

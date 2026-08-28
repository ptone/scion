# Where does the EMPTY harness-config name come from, and what resolves it? (tasks #37 / #48)

Author: sn-impl-arch (architect). Date: 2026-08-28. **Dispatched. Start now.**

**THIS IS A CODE-READING TASK. TOUCH NO CLOUD INSTANCE. Do not deploy, do not create an agent, do not
call a live hub.** That restriction is the entire reason this task is being given to you now — see
"Why this is code-only" below. It is not a formality.

## What is already established. Do not re-derive it.

1. **The empty name does not originate in the browser form.** Verified by three code citations I
   spot-checked myself:
   - `web/src/components/pages/agent-create.ts:69` — harness state initialises to `'gemini-cli'`.
   - `resolvedHarness` (`:741`) returns `customHarness` **only** when `harness === '__other__'`.
   - `:880` always includes `harnessConfig: this.resolvedHarness`.

   Empty is reachable from the browser only by selecting "Other…" and blanking the name — a deliberate
   override, not the default path.

2. **The default template still has `harness: ""`.** That is the suspected source.

3. **The symptom:** the hub dispatches with an empty harness-config name; the broker then invents one
   from embedded settings and cannot resolve it. On a deployed instance this surfaces as a failure to
   start an agent — **§1 step 5**, the step this whole tier is measured on.

4. **`ptone/scion#1316`** (resource-name resolution) is the upstream issue these tasks were rewritten
   to point at. Read it before you start.

5. **#37 and #48 are one defect wearing two resource types.** Treat them as one.

6. **A previous agent claimed this "works out of the box" and was wrong.** It created an agent with
   `harnessConfig: claude` — an **explicit** name — and concluded about the empty case from the working
   one. Do not repeat that: **the input shape that matters is EMPTY, not "some name".**

## The question, and it is a single question

**Trace the empty harness-config name from its origin to the point of failure, and name every place it
could be defaulted but is not.**

Concretely, answer these with file:line, not prose:

1. **Origin.** Where does `harness: ""` enter a dispatch? The default template is the suspect —
   confirm or refute it. If a create request omits `harnessConfig` entirely, what does the hub put on
   the wire? Empty string, absent field, or a default?
2. **Transit.** Every hop between hub dispatch and broker resolution. At each hop: is empty passed
   through, rejected, or substituted?
3. **The invention step.** The broker "invents one from embedded settings." **Find that code.** What
   exactly does it invent, and why can the result not be resolved? A previous investigation saw an
   invented name `antigravity` that is not registered — confirm whether that is still what happens.
4. **The failure.** What is the operator-visible symptom and error text? An agent that will not start
   with an unhelpful message is a different severity from one that fails loudly.

## What I want you to be suspicious of

**"Empty" and "absent" are different, and they may take different paths.** A JSON field that is `""`,
a field that is `null`, and a field that is not present at all can each be handled differently by an
unmarshaller, a validator, and a resolver. **Check all three shapes**, and say so per hop. If they
converge, say that explicitly — it is a useful result and it is not the default assumption.

This project's recurring defect is that **"no value" and "a good value" look identical unless something
forces them apart.** That is the shape of this bug. Expect a place where an empty string silently
satisfies a check that was meant to require a real name.

## Why this is code-only, and why that matters

The obvious way to settle this is to create an agent with an empty harness-config on a live instance.
**That is forbidden here.** Exceeding the agent ceiling (defect #67) **destroys the entire Instance
about 8 seconds after returning HTTP 201**, and the only suitable live instance is `sn-harness-lab`,
which ptone is about to use. The measurement is cheap; the downside is demolishing his lab.

**So the value you add is precisely that you can answer this without touching anything.** If you reach a
point where you genuinely cannot settle a question by reading, **stop and tell me what experiment would
settle it** — do not run it. I will decide whether it is worth the risk, and that is my call, not yours.

## Deliverable

A note at `/scion-volumes/scratchpad/projects/single-node/investigations/harnesscfg-origin.md`:

- The trace, hop by hop, with `file:line` at every hop.
- The three input shapes (`""`, `null`, absent) and what each does at each hop.
- The invention step, quoted.
- **A recommended fix shape** — where the default belongs, and why there rather than at the other
  candidate sites. Name at least one alternative site and say why it is worse.
- **What you could not determine by reading**, stated plainly, with the experiment that would settle it.

## Report

Message `agent:sn-impl-arch` with the note path and a one-line summary. **And tell me what in this brief
is wrong.** My last three briefs each contained a defective requirement and every one was caught by this
paragraph, so it is the most productive part of the document. In particular: item 1 of "already
established" is a claim about the *browser* path only — if you find a non-browser create path that also
supplies empty, that does not contradict it, and I want it named.

## Constraints

- **Touch no Instance:** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`,
  `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`. **A restart IS a deletion.**
- Never print an access token.
- **You are not fixing this.** Investigation only — no code changes, no commits to product code.
- Fully qualify issue numbers: local is `task #37` / `task #48`; GitHub is `owner/repo#NNNN`.

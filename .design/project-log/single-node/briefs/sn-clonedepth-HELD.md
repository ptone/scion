# HELD — task #49: the documented sentinel for "full clone" is the one value the wire cannot carry

Author: sn-impl-arch (architect). Date: 2026-08-28.

> **DO NOT START THIS BRIEF UNTIL I DISPATCH IT BY MESSAGE.** It is written ahead of a free fleet slot.
> The dispatch message will say, in these words: *"Task #49 is approved, execute the held brief."*
> If you are reading this without having received that sentence, stop and message `agent:sn-impl-arch`.

**TOUCH NO CLOUD INSTANCE.** Everything here is unit-testable. Defect #67 destroys a whole Instance ~8s
after a 201, and `sn-harness-lab` is ptone's. **On this tier a restart IS a deletion.**

## Why this matters: it is the last step of §1

§1 of the design doc ends *"…and watches it commit to a git remote."* Task #49 says the agent workspace
is a depth-1 shallow clone, which blocks pushing to any remote but origin. **Already tracked upstream as
`ptone/scion#1274` — do not file a second issue.**

Note the honest scope: pushing a shallow clone **back to origin** generally succeeds, so the narrowest
possible §1 demo may pass today. It fails the moment the project has a fork/upstream split — which is
exactly how this repository itself is worked. **Confirm or refute that reading; do not inherit it.**

## What I found reading the code, and I want it falsified, not confirmed

The declared contract, `pkg/api/types.go:769-773`:

```go
type GitCloneConfig struct {
    URL    string `json:"url"`
    Branch string `json:"branch,omitempty"`
    Depth  int    `json:"depth,omitempty"`  // Clone depth (default: 1, 0 = full)
}
```

**"0 = full".** Now the three consumers, which read `0` three different ways:

| Site | Code | What `0` means there |
|---|---|---|
| `pkg/provision/provision.go:308-315` | `depth := gc.Depth; if depth == 0 { depth = 1 }` | **shallow-1** — the exact opposite of the doc |
| `pkg/runtime/k8s_runtime.go:2476-2479` | same `if depth == 0 { depth = 1 }` | **shallow-1** — same inversion |
| `pkg/runtimebroker/start_context.go:549-551` | `if gc.Depth > 0 { env["SCION_GIT_DEPTH"] = ... }` | **emit nothing**, defer to the in-container default — a third behaviour |

And the value that actually works is **-1**, per `start_context.go:866-870`:

```go
// Copy GitClone config so we don't mutate the shared pointer, and force a
// full clone (Depth -1 ≡ no --depth flag). The shared base needs full
// history for coordinator merges, git log, and git blame (design §4.2a).
gcCopy.Depth = -1
```

So the codebase already has a working full-clone sentinel, and **it is not the documented one.**

### The part that makes this more than a comment bug

`json:"depth,omitempty"`. Under `omitempty`, **`Depth: 0` serialises to absent.** So even in a world
where `0 = full` were honoured, no API client could transmit it — the encoder deletes precisely the
value the documentation instructs you to send. On decode, absent becomes `0`, and two of the three
consumers then turn `0` into `1`.

**The documented way to ask for a full clone is unrepresentable on the wire, and silently means its own
opposite.** `-1` survives `omitempty` because it is non-zero, so the undocumented sentinel is the only
transmissible one. If that is right, this is rule 28 with teeth: the contract is not merely unmeasured,
it is unusable, and nobody notices because the failure is a silent downgrade rather than an error.

### There is also no operator lever at all

`pkg/hub/handlers_agent_create_helpers.go:142-146` hardcodes `Depth: 1` for every git-anchored project:

```go
agent.AppliedConfig.GitClone = &api.GitCloneConfig{
    URL:    cloneURL,
    Branch: defaultBranch,
    Depth:  1,
}
```

Three lines above it, `URL` and `Branch` are both read from **project labels** —
`scion.dev/clone-url` and `scion.dev/default-branch`. Depth is the one field of the three that is a
literal. **Rule 34 applies: look for the slot before you build one.** The label convention exists and
two of its three siblings already use it.

**Check whether I am wrong about "no lever."** Search for any request field, template field, profile
key, or env var that reaches `GitCloneConfig.Depth`. If one exists, this brief's framing is wrong and I
want to hear that first.

## The decision I am NOT making for you

**Do not simply make everything a full clone.** Depth-1 is a deliberate cost choice for ephemeral
agents, and full history on a large repository is a real regression in start-up time and disk. The
`start_context.go:867` comment shows the project already reasons this way: the *shared base* gets full
history for merges and blame; the *per-agent* clone does not.

So the fix has two independent halves, and I want them argued separately:

**Half A — make the contract and the code agree.** Three shapes:

| Shape | Change | Blast radius | Cost |
|---|---|---|---|
| **A1** | Fix the comment to match reality: default 1, **-1 = full**, 0 ≡ unset | zero behaviour change | leaves `omitempty`, leaves 0 meaning its opposite, does not fix #49's harm |
| **A2** | Make `0 = full` real: drop `omitempty`, remove both `if depth == 0 { depth = 1 }` | every caller that relies on the zero value being shallow | **must** verify the hub keeps sending an explicit `1`, or every agent silently becomes a full clone |
| **A3** | `Depth *int` — nil = default, 0 = full, n = shallow-n | every construction site | most correct typing, largest diff, hardest to review |

**I lean A1 + Half B**, because A1 is honest and free and the real user-facing gap is the missing lever,
not the sentinel. **But I have talked myself into a wrong "obviously correct" answer three times this
week, so treat that lean as the thing to attack.** In particular: A1 leaves a struct where `0` is
documented as unset and *behaves* as 1 at two sites and as "defer" at a third. Argue whether that is
tolerable or whether it is the next person's trap.

**Half B — give the operator a way to ask for full history.** My candidate is a project label
`scion.dev/clone-depth`, parsed at `handlers_agent_create_helpers.go:142-146` alongside the two labels
already read there. State what happens on a malformed value: I want it to **fail legibly and name the
label**, not silently fall back — that is the same failure class as the whole harness-config saga
(#37/#48), where a silent fallback to a default produced an error blaming the wrong thing.

## Measurement, and it comes first (rule 6)

Write these as tests **before** you change anything.

| # | Scenario | Today | Predicted | Why it matters |
|---|---|---|---|---|
| 1 | `GitCloneConfig{Depth: 0}` marshalled to JSON | key **absent** | — | proves the `omitempty` claim, or kills it |
| 2 | JSON `{}` unmarshalled, then through `gitCloneWorkspace` | `--depth 1` | — | proves 0 means shallow, not full |
| 3 | `GitCloneConfig{Depth: -1}` marshalled | key **present**, `-1` | — | proves the undocumented sentinel is the transmissible one |
| 4 | `Depth: -1` through `provision.go` | **no** `--depth` flag | unchanged | the full-clone path that already works |
| 5 | `Depth: 0` through `start_context.go:549` | no `SCION_GIT_DEPTH` emitted | unchanged | the third reading of 0 |
| 6 | Default git-anchored project agent create | `Depth: 1` | unchanged unless B applied | **the no-op row: prove the default does not move** |
| 7 | Push from a depth-1 clone to a **second** remote | ? | ? | **the actual §1 harm — see below** |
| 8 | Push from a depth-1 clone back to **origin** | ? | ? | the honest-scope row |

**Rows 7 and 8 are the ones I cannot predict and the ones I most want.** Do them with real `git`
against local bare repos — no network, no Instance. If row 8 succeeds and row 7 fails, that is the
precise statement of the defect and it should replace my prose above in the task description.

**Row 6 is the withdrawal condition.** If any change you make moves the default clone depth for an
ordinary agent, stop and report before implementing. A start-up cost regression on every agent, shipped
as a fix for a push failure, is a bad trade and it is mine to make, not yours.

## Mutation standard

Mutate every pin and **read why it went red** (rule 2 — a red is necessary, not sufficient). Named
mutation: **revert your change to the doc comment or the parse and confirm the specific row goes red for
the stated reason**, not for an unrelated compile error.

Rule 33: every row must render a legible verdict, and **"absent" must look different from "passing."**
Row 1's expected result is literally an absent key — make sure your harness cannot report that as a pass
when the test did not run.

## Constraints

- Branch from current fork main. **New branch.** Push to `ptone/scion` only — **no upstream PR**. That
  is ptone's gate.
- Additive commits. No rebase, amend, or force-push.
- `golangci-lint` and `gofmt` clean before you report green. Both have failed branches on this project
  this week for things a human called nits. **If a machine fails it, it is not a nit.**
- Never print an access token.
- **Touch no Instance:** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`,
  `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`.
- Local is `task #49`; GitHub is `owner/repo#NNNN`. The upstream tracking issue is `ptone/scion#1274`.
- Design doc §9.2 cites this as a bare `#1274`, which resolves to an unrelated PR in the repo the file
  now lives in. That ambiguity is separately tracked as `ptone/scion#1297` — **do not fix it here.**

## Report

Rows 1-8 **measured, not predicted**; your argument for A1 vs A2 vs A3 with the blast radius you found
rather than the one I guessed; what you propose for Half B; and the named mutation with why it went red.

**And tell me what in this brief is wrong.** Four of my last five briefs contained a defective
requirement and every one was caught by an agent answering this paragraph — one where I called a
postgres-only change "one line", one where I described a rank change as inert, and one where I asserted
a file was not created when it was. **The likeliest error here is my `omitempty` claim in rows 1 and 3**,
which I derived by reading a struct tag and did not execute. If it is wrong, most of this brief
collapses to a comment fix, and I would much rather learn that from you in the first ten minutes.

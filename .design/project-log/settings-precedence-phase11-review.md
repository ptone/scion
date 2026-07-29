# Phase 11 docs review — `sp-rev2`

**Reviewed:** `scion/sp-dev7` @ `01b869ae14a61023fb24fc82ab419ea0f9256d6d`
**Base:** `ff835b0b` · **Cross-referenced:** `scion/sp-integration` @ `d2989488`
**Verdict:** CHANGES REQUESTED (one required fix, one line)
**Full report:** `/scion-volumes/scratchpad/pr-reviews/pr-sp-dev7-phase11-review.md`

## Why the tip was re-pinned

`scion/sp-dev7` was force-updated from `cd7de0ca`, which is not in my object store. I resolved
`01b869ae` explicitly and asserted it equals `origin/scion/sp-dev7` before measuring anything.

## The blocking finding

`.design/project-log/settings-precedence-phase11-docs.md:221` cites `cmd/hub_secret.go:58` and
`cmd/hub_env.go:58` **with no sha**, and states "the two now genuinely differ."

At `ff835b0b` — the rev every other citation in the document pins to — the two cited lines are
**byte-identical** (`hub -> user -> project -> broker -> agent config`). The claim is true only at
`d2989488`, where `cmd/hub_env.go` was rewritten and `cmd/hub_secret.go` was not.

`citecheck` passes both, because it validates **citation → referent**, not **referent → claim**.
That is the boundary of the tool and it should be stated as such wherever the tool is mandated.

The consequence is directional: this workstream has a standing prohibition against "fixing"
`hub_secret.go:58` to match `hub_env.go`, because that would make the two help strings agree and
make one of them lie. A reader resolving the unpinned citation at the document's declared baseline
finds two identical strings — **evidence for the trap, inside the warning against it.** Fix is to
pin both citations to `d2989488`.

## The gate hole

`sp-dev7` reported 16 citations parsed, 0 failed. The artifact contains **18** `file:line@sha`
citations; two live in inline prose (lines 138, 140) rather than table rows and its parser does not
see them. Both pass on inspection, so there is no live defect — but the parser's own `10 → 16`
control counts rows the parser found, not citations the artifact contains, so it cannot detect the
gap. **A parser control validates the parser against itself.** Remedy: regex the raw artifact for
`file:line@sha`, assert that total equals the number of rows checked.

## What verified

Outline correct by machine-listing all 36 headings (the six misfiled env sections are genuinely
fixed). Page carries zero `file:line` citations, with the same regex firing 21× in the project log
as positive control. 27/27 citations resolve under my own parser. `V1ProfileConfig.Env` survives
with its `koanf` tag and `ResolveRuntime` still reads it, so the silence claim is true in code and
present on both surfaces. The `profiles.<name>.env` retirement actually shipped (1 → 0, positive
control 2). Resources ladder matches the five ranks derived independently in the Gap 2 work.

Criterion-18 error is **absent**: A1 gives the full total order plus the three inverting pairs by
name, not just the winner. Resources is stated as a chain but over **channels**, with the broker
file annotated at three separate ranks, and the prose explicitly forbids the inequality-flip repair.

## Instrument notes

`citecheck.sh` is immune to both of tonight's falsifications, measured: colon forms braced on both
sides, paths always variables (so the zsh modifier never fires), `bash` shebang, and `cat-file -e`
is an object check rather than a content search — verified against a real tracked zero-byte file.

**My own error, disclosed:** a `||` fallback in my first release-notes check succeeded on its first
branch and measured the pre-Phase-11 file. Caught by line count, re-run against the correct rev.
Same laundering family as the rest: a wrong artifact producing a clean, plausible number.

---

## Addendum — re-point to `f36e99d1` (21:40Z)

`sp-dev7` force-moved the tip after this review was written. **Verdict unchanged: CHANGES
REQUESTED.**

Verified rather than accepted, as `sp-dev7` itself asked: `git diff --name-only 01b869ae f36e99d1`
returns **exactly one path**, and all three user-facing blobs match the reported hashes
(`860eafd3c88c`, `fc9e81db2165`, `00b0d8078cf7`). The page is untouched and I did not re-read it.

**But the one changed file is `.design/project-log/settings-precedence-phase11-docs.md`, which is
where both findings live** — so "do not re-review the page" was correct and not the relevant
question. Re-measured: H1 unchanged at line 221 and still unpinned; M1 unchanged at 18 citations
against 16 gated, same four unpinned. `sp-dev7`'s "closes rows, opens none" **holds, checked the way
that could have falsified it**: the log grew 340 → 374 lines while citation counts stayed identical
at 22 total / 18 pinned / 4 unpinned.

### Instrument addendum — `rev-parse` echo (`sp-arch` 21:17)

Bare `git rev-parse "<rev>:<path>"` echoes its argument to stdout on failure, so `[ -n "$v" ]`
passes on failure. Audited against my own work:

- `citecheck.sh` is **already closed** on this limb — lines 24 and 78 use
  `rev-parse --verify --quiet`, which suppresses the echo where a `^[0-9a-f]{40}$` shape guard
  detects it.
- The re-point verification above **did** use the bare form, and was **fail-closed by luck of task
  shape**: I compared against reported 40-hex constants, and an echoed path can never equal one. I
  ran the equality limb and **no must-differ control** — the must-differ limb is the one that would
  have lied.

### Delivery note

Every outbound message from `sp-rev2` has failed `0/N` since 21:10Z (502 / `context deadline
exceeded`); inbound and `git push` both work. This addendum is committed because **git is the only
channel that has not broken**. Full detail, including a fleet-facing warning that retrying may
multiply rather than repair, is at
`/scion-volumes/scratchpad/pr-reviews/UNDELIVERED-sp-rev2-2127Z-supersession-audit-gap.md`.

---

## Addendum 2 — confirm-only pass at `29575142`, and a disclosure against myself (21:58Z)

**VERDICT: APPROVED** on `.design/project-log/settings-precedence-phase11-docs.md`.

### The rev I was asked to check does not exist, and the gate I was given fails at the one that does

`1477659c` is ABSENT from my object store before and after `git fetch origin scion/sp-dev7`;
`origin/scion/sp-dev7` is `295751428216d3d3e20ee000fa4f251eb1882a41`. Run at the actual tip:

```
git diff --name-only 01b869ae 29575142
  .design/project-log/settings-precedence-phase11-docs.md
  docs-site/src/content/docs/reference/settings-precedence.md      <- TWO paths, not one

page blob  01b869ae 860eafd3c88c | f36e99d1 860eafd3c88c | 29575142 39e189b8623e  (+36/-10)
```

`sp-dev7`'s "user-facing blobs byte-identical, you need not re-read the page" was **true at
`f36e99d1` and is false at the tip**. This is the resend-staleness finding aimed at a *report*:
**a report is state frozen at composition time and read at current time.** Gating on the diff
rather than on the report is what caught it. The 46 changed page lines are outside this pass.

### H1 — closed. **The wrong line number was mine.**

`cmd/hub_secret.go:58@d2989488` and `cmd/hub_env.go:59@d2989488` are both correct at `29575142`,
and the log documents the `:58`→`:59` move at `:235`.

`cmd/hub_env.go:58@d2989488` was **my** suggested-fix diff, written before it was relayed to anyone.
I measured the *content* at `d2989488` and carried the *coordinate* from `ff835b0b`: **I updated the
moment and kept the set.** A line number is part of the referent, not part of the path, so
re-pinning a citation means re-measuring the line, not appending a sha to the old one.
**A pin makes a citation resolvable, and resolvable is not correct.** `sp-dev7`'s negative
control — requiring the *pre*-pin form to fail — is what caught it and should be mandatory.

### M1 — 33 occurrences, 22 unique, **0 truly unpinned**

*My own probe error:* the first run reported "33 pinned, 33 unpinned" because `\.go:[0-9]+` matches
the **prefix of every pinned citation**. Caught only because both arms returned the same number.
**Two arms returning the same number is either a result or an overlap, and the cheap check is
whether one pattern is a prefix of the other.** Non-blocking: `sp-dev7` reports "26 contained, 22
checked" under an invariant it states as `checked == contained`; by its own predicate that is the
gate firing, reported as a pass.

## The provenance question — **AFFECTED**

`docs-site/.../settings-precedence.md:188-192` (verbatim at both `f36e99d1` and `29575142`) claims
`envScopePrecedence` is the single source and that **every** consumer derives its order from it,
naming three, and concludes "changing that one list is the only edit needed to change the order
everywhere."

`Server.buildEnvGatherResponse` (`pkg/hub/handlers_agents_core.go:1019@ff835b0b`, `:1019@29575142`,
`:1101@d2989488`) is an unnamed **fourth** provenance reporter that hardcodes its scope labels as
string literals. Measured: `envScopePrecedence` in that file = **0** at `ff835b0b` and `d2989488`,
positive control `ListEnvVars` = 4, 4. User-visible consequence: a key stored only at
`runtime_broker` scope is reported **`hub`** by the env-gather API and **`broker`** by
`scion hub env list` (`envScopeSourceLabel@d2989488`). Two surfaces, two answers to "where did this
come from", under a sentence that says they cannot differ.

**The code defect is pre-existing, not a Phase 11 regression** — seven hardcoded literals at
`b03a09ac`. What is new is the claim. And the page faithfully transcribes a false source comment at
`pkg/hub/httpdispatcher.go:1119-1123@d2989488`, so **the fix is two places or it regenerates.**
A caveat is not sufficient on its own: `every consumer` must narrow to `the three consumers below`,
and `the order everywhere` to `the resolution order`, or the caveat reads as an exception to a rule
whose unqualified form is the false part.

## Instrument change — `citecheck.sh` existence limb, `cat-file -e` → `ls-tree`

Per instruction. Folded existence and the blob/tree check into one `ls-tree` entry, added a
positive control before any "absent" is believed, an exact-name assertion, a magic-pathspec
rejection, and `--literal-pathspecs`. The blob is now read **by object hash** rather than by
`"${sha}:${path}"`, which retires every `rev:path` colon form in the file.

Two bugs found in my own replacement while testing it, both disclosed:

1. `read x y < <(helper)` takes **`read`'s** rc, not the helper's — and because `fail()` prints to
   *stdout*, a failing helper feeds its own error message into `read`, which succeeds, leaving the
   parsed value as the literal string `FAIL` and discarding the real diagnostic. Fixed with command
   substitution, branch on rc, re-emit the captured message.
2. Testing non-emptiness of the parsed type would have accepted that `FAIL`. Replaced with a
   whitelist (`blob` / `tree`|`commit` / anything-else-is-internal-error).

13-case battery, ground truth stated before running: 2 expected-PASS and 11 expected-FAIL, all 13
correct, each failing for the *stated* reason rather than merely failing. The zero-byte tracked file
(`.scion/templates/instance-manager/skills/.gitkeep`) is correctly reported present and fails on the
referent, not on existence.

## Conceded to `sp-arch` (21:55)

My two-sided partition gate `BAD + GOOD == CTL` is **blind to `sp-dev5`'s field-reorder attack**, and
the reason is general: **a partition check validates the split, not the universe, and the control
term is usually written by widening the subject — the one operation that preserves the subject's
blind spot.** "Its expectation comes from neither arm" is true and *not sufficient*, because the
expectation still comes from the arms' shared prefix. My residual limb is itself one rung short
(`var e store.EnvVarFilter; e.Scope = ...` has no brace). The three-limb LOOP/HARD/RESID form
supersedes what I proposed.

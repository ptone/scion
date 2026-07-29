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

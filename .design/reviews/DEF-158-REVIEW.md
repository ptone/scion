# DEF-158 review log — findings across five rounds

Working notes. Folds into `DEFECTS.md` as footnotes when DEF-158 closes.
Branch under review: `scion/ca-msg-def158fix`. Base `f38f3ba18`.

| Round | Commit | Findings raised | Outcome |
|---|---|---|---|
| 1 | `a7065301d` | F-A scope inverted, F-B invented fallback, F-C probe requested | rework |
| 2 | `c3542602` | G-1 severed doc comment, G-2 mutation matrix hole, G-3 map collision, minor conditional assert | rework |
| 3 | `48cdfe64` | G-3a test did not exercise the guard it named | rework |
| 4 | `66ff1caf` | G-4 broker-less 503 regression, G-5 wrong regression family, G-6 tag/CI gap | rework |
| 5 | pending | — | — |

## Lesson 1 — my own ruling caused the regression (G-4)

R2 as I wrote it: *"Channel validation must run after Channel is finally set —
on every path."*

I wrote that to close a fail-open where a caller-supplied channel could reach
send unvalidated. It does not distinguish **provenance**, and the developer
applied it literally to a value the server derives itself. Result: on any hub
with no broker plugin — a supported configuration, `notifications.go:44`
`nil = no broker, use ChannelRegistry` — every `conv:` DM began returning 503.

The three cases my rule collapsed into one:

| Provenance | Trust | Validate? |
|---|---|---|
| caller-supplied | arbitrary string | yes — this is what R2 was for |
| affinity-supplied | stored, may name a removed spoke | yes — pre-existing behaviour |
| surface-derived | total function over closed enum | no — this is a liveness check, not input validation |

**The generalisable form: a rule of the shape "always validate X" is
under-specified unless it states X's provenance.** Validation of untrusted
input and validation of a self-derived value are different operations that
happen to share a function name, and the second one converts a spoke outage
into a write rejection. When I write a fail-closed ruling, it needs to name the
inputs it distrusts, not the field it distrusts.

Cheap tell I missed: the code I was ruling over *already* carried a
`GetMessageBrokerProxy() != nil` guard on the affinity lookup. That guard is
evidence that a nil proxy is an expected state on this path. I read that line
several times while writing R2 and treated it as incidental.

## Lesson 2 — adjacent family is not the owning family (G-5)

The developer reported "14 DEF-138 tests: all PASS (no regression)" in three
consecutive rounds. The block modified is the **DEF-152** block. Both
regressions were in DEF-152.

DEF-138 is the family the *file* is associated with; DEF-152 is the family the
*edited lines* belong to. **The first regression family to run is the one that
owns the code you touched, and the report must name which families were run** —
otherwise "no regression" is a claim whose scope is invisible to the reader.

I did not catch this in rounds 1–3 either. I accepted "14 DEF-138 PASS" three
times without asking which family owned the edit.

## Lesson 3 — `-run` matching nothing exits 0 (G-6)

```
$ go test -tags no_sqlite -run 'TestDEF158|TestDEF159' -v -count=1 ./pkg/hub/
ok  ...  0.122s [no tests to run]
exit=0
```

Already a standing rule for me; this is the first time it fired in a merge
decision. Had I read the exit code and merged, I would have merged on a run
that executed none of the code under review.

Compounding factor worth keeping: **the same command with two tag sets has two
different meanings, and neither of us stated the tag set.** The developer's
"7 PASS" (untagged) and my "no tests to run" (`no_sqlite`) were both accurate
and mutually unintelligible. Every `pkg/hub` result must now carry its tags.

Downstream fact, handed to `ci-fix-lead`: the tag on
`handlers_outbound_def158_test.go` is *legitimate* (depends on `def138Setup` in
an already-tagged file), and the consequence is that none of this regression
suite runs in the only blocking gate.

## Lesson 4 — three invented mechanisms in one review

1. **F-B** — comment claimed `deliverToUser` defaults Channel to `"web"`.
   `messagebroker.go:457` copies it verbatim. No such default exists.
2. **The fan-out claim** — write-side comment asserts empty Channel "fans out to
   all spokes." Spoke selection is in the broker plugin, outside this repo.
   Unverifiable from here; I scoped the fix narrow *because* of that.
3. **G-3a** — test comment claimed it proved the init panic reachable. The test
   re-implemented the check and never called production code; deleting the panic
   left it green.

Each accompanied a change that was substantively correct. That is what makes the
pattern durable — there is nothing wrong to notice except the sentence.

**A wrong justification costs more than a missing one: it tells the next reader
the question is settled, so they stop looking.** The response to the third
instance is no longer "verify this claim" but "treat an unverified mechanism
claim as the default state of any comment describing code the author did not
open."

## Lesson 5 — mutation matrices can mask themselves

Round 2's matrix mutated each half of a two-path fix while the other half was
live. `AC1_AC2` stayed green under every mutation run. **A test that survives
every mutation in the matrix has not been shown to test anything** — the matrix
needs the row where the whole fix is reverted, which is what "revert your fix"
meant and what I had to ask for explicitly.

Round 3's G-3a is the same failure one level down: a test that cannot fail
because it does not call the code it names.

## Lesson 6 — the instrument is part of the measurement (deploy postscript)

Not a code finding. The `2519aa8b3` deploy report to gteam flagged a boot line
(`Permanently unattributable messages in listed projects … permanent:12583`) as
**new**, and explained it as "new logging for an existing condition."

Both halves were false. `cmd/boot_data_migrations.go:620` emits it, and that file
is **byte-identical** on `f38f3ba18` and `2519aa8b3` — the merge touched nothing
in `cmd/`. The line was present in *both* boots.

The actual cause: the two reports used **different grep filters**. The first
filtered on `migration|backfill|boot|…`, the second added `INFO`. The first
filter caught the neighbouring `Message attribution complete` line only by
accident — the word "backfill" appears inside its detail string — and dropped the
`permanent` line entirely. Two outputs, two instruments, one conclusion about the
subject.

**A difference between two observations is attributable to the subject only if
the instrument was identical.** Once the filter moves, a diff of the outputs
measures the filter.

This is the same family as Lesson 3. There, the developer's "7 PASS" and my "no
tests to run" were both accurate and mutually unintelligible because the **tag
set** — the instrument — differed and neither of us stated it. Filter, tag set,
`-run` pattern: all are part of the measurement, all invisible in the result, all
capable of turning "no change" and "changed" into the same observation.

Practice, applied to both: **state the invocation with the result, and when
comparing two runs, make it the same invocation.** If it must change, re-run the
baseline under the new one before concluding.

### And: my withheld theory was wrong

I had a hypothesis — a marker write-ordering effect across boots. I did not send
it. I sent the hole (*"same code, same boot sequence, different output means an
input moved — which input?"*) plus three discriminating questions.

Had I sent the theory, I would have aimed the investigation at markers when the
answer was in the grep invocation, and the investigator would have spent its time
disproving my idea instead of examining its own method.

**Arguing "your evidence does not reach your claim" outperformed proposing a
rival conclusion** — second time on this project. The asymmetry is structural: a
hole is correct whether or not my private theory is, and it returns the
investigation to the person holding the evidence. A rival theory is only useful
when it is right, and it relocates the work to me.

The investigator's own summary, which is the right standard: *"I compared outputs
from two different grep filters and concluded an input moved. Nothing moved. My
comparison was unsound and my report was wrong."*

## Standing: what I verified myself rather than relaying

- G-1: `git diff | grep -E '^-[^-]'` — confirmed the only deletions were the
  extraction, so `disclosableResolutionReason` is byte-identical.
- G-2: cross-checked the reported RED line numbers (188/189/211) against the
  actual assertions in the file.
- G-3a: applied the mutation myself, confirmed it applied with `grep -c`,
  confirmed RED, restored, confirmed GREEN.
- G-4: ran the DEF-138/140/141/142/152 set — 45 PASS / 2 FAIL. Target 47 / 0.

Every round turned up something the report said was handled. In each case the
code was right and the claim about it was wrong, which is a failure mode reports
are structurally unable to surface.

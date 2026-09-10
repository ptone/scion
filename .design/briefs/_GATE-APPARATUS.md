# Gate apparatus — include this section in every developer brief

Measured facts about this tree. These are not suggestions; each one has already
cost someone a wrong conclusion.

## Base verification — do this before you edit a single file

`scion start` clones the repository at its **default branch, `main`**. Our work
does not live on `main`. If you begin editing without an explicit checkout you
will silently do correct-looking work on the wrong tree, and your own numstat
will look clean because it is computed against the base you actually have.

This has already happened once and was caught only in review.

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git fetch "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" <base-branch>
git checkout -B work FETCH_HEAD
git rev-parse HEAD    # MUST equal the base hash in your brief, exactly
```

**Do not proceed unless that hash matches.** If it does not, stop and report.

Then **lead your final report** with:

```sh
git merge-base --is-ancestor <base-hash> HEAD && echo BASE-OK || echo BASE-WRONG
git diff --numstat <base-hash> HEAD
```

The numstat must be computed against the brief's base hash, not against
whatever your local branch happens to sit on.

**If you discover mid-task that your base was wrong: do not rebase or
cherry-pick your work across.** Files diverge between branches, and resolving
those conflicts by taking your own side is how a refactor gets silently
reverted. Start again on the correct base and re-apply the intent by hand.

## Timeouts

`pkg/hub` takes **~395 seconds** to compile and test. Use `-timeout 900s`.

A shorter timeout fires mid-run and prints a goroutine dump — typically
`created by database/sql.OpenDB in goroutine NNN` at `database/sql/sql.go:841`.
That dump looks exactly like a real concurrency defect and is not one. Anyone
who "fixes" code in response to it is chasing their own timeout.

`pkg/messaging`, `pkg/store` and `cmd` are fast.

## Never truncate a measurement

**Do not pipe `go test` through `tail` or `head`.** Capture whole, filter after:

```sh
go test ./pkg/hub/ -count=1 -timeout 900s > /tmp/hub.log 2>&1; echo "exit=$?"
grep -nE "^(ok|FAIL|---|panic)" /tmp/hub.log
```

This has cost this project real evidence twice: once the identity of a flake in
the *blocking* CI gate, and once a grep that reported one failure where there
were three. **Both times the truncated output looked clean.** Keep the full log
on disk for every gate run and quote from the file when reporting.

## `go vet ./...` is mandatory before any push

`make test-fast` runs `-tags no_sqlite` and **excludes 256 of 829 test files
(31%)**. The SQLite suite is `continue-on-error` in CI, and `fmt-check` is not
wired into CI at all. So `go build ./...` green + `make test-fast` green is
compatible with `pkg/hub` test code that does not typecheck — that exact
combination shipped a broken merge here recently.

`go vet ./...` typechecks test files in every package regardless of build tags
and costs about a minute.

## `//go:build !no_sqlite` — when it is required, and it is narrower than you think

**This section is the authority. If a brief tells you something different about
this tag, the brief is stale and this wins — tell me so I can fix it.**

The rule everyone assumes is *"my test uses sqlite, therefore it needs the tag."*
That is wrong, and following it costs coverage. Measured 2026-09-09:

- `no_sqlite` gates exactly two things repo-wide: `pkg/store/sqlite/driver.go`
  and the `pkg/ent/entc` sqlite driver (plus its `migrate_*` pairs).
  **`github.com/mattn/go-sqlite3` is an ordinary, ungated dependency** and
  compiles and runs fine under `-tags no_sqlite`.
- `CGO_ENABLED=0` appears only at Makefile:126 and :135, both container-binary
  targets. `test-fast` and ci.yml run with cgo on, so cgo is not a hidden term.
- In `pkg/hub`, 13 test files import the raw driver: **5 tagged, 8 untagged.**
  The tag is not even the majority in the package people generalise from.
- Probed directly: 11 DEF-156 tests and 7 `TestPromoteDM*` tests, all in untagged
  files that open sqlite, run and pass under `-tags no_sqlite` with identical
  PASS counts and zero silent skips.

**The rule: the tag is required iff the file reaches a `!no_sqlite`-only package.
Calling `sql.Open("sqlite3", ...)` through the raw driver does not qualify.**

In `pkg/hub`, exactly one file imports the `!no_sqlite`-only `pkg/store/sqlite`:
`system_handlers_test.go`. It is tagged. It is the only one that has to be.

Why this matters enough to have its own section: `make test-fast` is the **only**
gate in ci.yml that can fail a build. Adding the tag to a file that does not need
it silently deletes those tests from that gate, and **everything stays green** —
success and failure look identical, which is why you must probe both directions:

```sh
go test -tags no_sqlite -run '<Your>' -v -count=1 ./pkg/<pkg>/
go test                 -run '<Your>' -v -count=1 ./pkg/<pkg>/
```

Compare the `--- PASS:` counts. If they match, no tag. If the tagged run fails,
you need the tag — and the failure output is more interesting than the tag, so
send it to me.

**Never strip the tag from an existing file to make something run.** Removing a
tag adds tests to the blocking gate and is a deliberate act with its own review;
`TestRS1_StaleAuthorityForcedOverlap` is 23m37s and is tagged for exactly that
reason.

## `-run` patterns

`-run <pattern>` matching *something* and matching *what you meant* are
different facts. Always pass `-v` when using `-run`, and confirm the test you
intended actually appears in the output.

## Shell

The shell is **zsh**, not bash.

- `${PIPESTATUS[0]}` is **empty** (zsh uses `$pipestatus`, 1-indexed). Capture
  exit codes without a pipe, as shown above.
- Quote all globs: `--include='*.go'`, `'pkg/messaging'`.
- `cwd` drifts between calls — use `git -C /workspace`.

## Build cache

`/scion-volumes/gocache` is shared between agents and will corrupt under
concurrent use. Set your own: `export GOCACHE=/tmp/gocache-<yourname>`.

## Working tree

Agents dispatched in parallel may share a working tree. **Never `git add -A`** —
stage only the files you changed, by name.

## Reporting

Report **per-file `git diff --numstat`** and the **full** output of `make ci`
and `go vet ./...`.

**Never make a gate pass by weakening the gate.** Any red comes to the architect
with the full log attached. A real failure is far more welcome than a tuned-green
one. Re-running a failing gate until it goes green, without capturing the
failure, is the same offence.

## Baseline — required on every defect, breakage or security finding

**State the baseline commit before you write the finding, not after someone asks
for it.** A finding is not complete until you have said whether the behaviour is
present on `main`, and on the branch base.

The check is one grep. It is required because **the same facts read as "a known
issue we inherited" or "a regression we are about to ship" depending on it**, and
those two dispositions have opposite consequences: one is filed and scheduled, the
other blocks a merge. A finding that omits the baseline has not narrowed the
decision at all — it has only moved the work to the reader.

Two live examples from DEF-160, one investigation, both missing this:

- **DEF-161** was reported at the branch head as a security hole. It is real —
  but `main` has neither `ConversationID` nor `ConversationRef`, so the path is
  **ours**, authored by this tranche, and becomes reachable on `main` at merge.
  Worse than reported.
- **DEF-163** was reported as a live breakage. `main` carries the identical call
  site and an equivalent guard, and our edit only *relaxed* that guard. **Not
  ours**, not merge-blocking. Better than reported.

Report it in this form:

```
Baseline: present on main @ <sha> (<yes|no>); present on branch base @ <sha> (<yes|no>)
Method:   <the command you ran>
```

If you cannot establish it, write "baseline not established" and say what you
tried. That is a usable answer. Silence is not.

## Severity — a property of the dependents, not of the break

Tracing a break precisely and then assuming its blast radius yields a confident
number unrelated to the truth. **Before assigning severity, establish what
actually depends on the broken thing** — and say which parts of that you checked
versus assumed.

DEF-163 again: the mirror path is genuinely broken, but deliberate `scion
message` sends traverse the same handler *with* a recipient and are unaffected,
so the user-facing loss is far smaller than reported. The real cost was somewhere
else entirely — a `log.Error` on every agent turn, fleet-wide — and that was not
in the report at all.

## Scope statements must name every package the feature spans

"I searched `pkg/hub/` exhaustively" is an honest sentence that can still support
a wrong conclusion, because a feature split across `cmd/` and `pkg/hub/` is
invisible to an exhaustive search of either one.

**An exhaustive search of one package is not an exhaustive search.** When you
report a search as exhaustive, name the directories it covered. If the feature
has a CLI half and a server half, both must appear in that list or the claim
means less than it looks like it means.

## A named function must carry its file:line — in briefs and designs too

DEF-160's design named `ThreadConversationExternalRef` "and its inverse" as the
authority for parsing a thread ref. The forward helper exists
(`pkg/messaging/derive_key.go:119`). **The inverse does not exist anywhere in the
repo.** The only code that decomposes a `thread:` ref is an ad-hoc `TrimPrefix`
in `divergence.go`.

The sentence was plausible because the forward helper is real, the naming
convention is regular, and a codebase that centralises construction *usually*
centralises parsing. Plausibility is exactly the problem: nothing in the sentence
invites checking.

This is the same failure as the three invented mechanisms in the DEF-158 review,
with the blast radius pointed the other way. **A wrong mechanism claim in a
review wastes a round-trip. A wrong one in a design gets implemented** — the
developer would have gone looking for the inverse, not found it, and then either
hand-rolled a split (reintroducing precisely the DEF-156 defect the forward
helper was written to fix) or stopped to ask, having already lost the time.

**Rule, applying to me as much as to anyone I dispatch: every function named as
existing carries `file:line`. A function that should exist but does not gets
written as "add X" with its contract, never as a reference.** The two are one
word apart in prose and a whole phase apart in the work.

The tell to reach for: if you cannot produce the `file:line` without searching,
you do not know the function exists — you know the naming convention.

---

## A negative assertion must be mutated in the direction that makes the absent thing present

`assert.NotContains`, "does not panic", "no row was written", "no notification
fired" — every assertion whose subject is a **non-event** passes trivially in the
world it was written in, because in that world the event was already not
happening. Passing tells you nothing about whether the assertion is connected to
the code at all. A `NotContains` against a typo'd variable, against a body that
is empty for an unrelated reason, or against a handler that never ran, is green.

So the mutation direction is inverted from the usual one. For a positive
assertion you break the behaviour and expect red. For a negative assertion you
**cause the forbidden thing** and expect red.

Worked example, DEF-160 R4-A: the fix removed two participant UUIDs from a
caller-visible error body and added three `NotContains` guards. Verification was
to put the enumeration back via `fmt.Sprintf`, confirm with `grep -c` that the
mutation applied, and observe two of the three assertions fire with the leaked
UUIDs quoted in the failure output. Only then was the guard evidence.

**A negative assertion you have not mutated is a comment.**

Corollary for briefs: when you ask a developer for a negative assertion, ask in
the same breath for the mutation that proves it, or expect to run it yourself.

## State the counting rule alongside any pass count

`grep -cE '^--- PASS:'` counts top-level test functions. `grep -cE '^ *--- PASS:'`
includes subtests. On the DEF suite these give 72 and 81 — both correct, and the
gap is one package's table tests.

This is the third form the same problem has taken here, after build-tag sets
(which tests ran at all) and grep filters (`^\+\+\+` swallowing the file header).
The common shape: **two people measure honestly with different instruments and
read the disagreement as a disagreement about the code.** It costs a round-trip
every time, and the round-trip is spent re-running rather than comparing.

The durable fix is not a canonical command — people will still deviate — it is a
**disclosure requirement**. Any reported count carries the rule that produced it.
Then a mismatch is resolved by reading two sentences instead of by rebuilding.

Related, same family: `go test -run 'Pattern'` with a pattern matching nothing
prints `ok` and exits 0. A green from a `-run` nobody confirmed matches something
is not evidence. Confirm the pattern hits before trusting its result.

---

## JSON-escaped angle brackets: `Contains` fails loudly, `NotContains` fails silently

Go's `encoding/json` escapes `<`, `>` and `&` as `<`, `>`, `&`.
Any assertion made against a **raw response body** — `rr.Body.String()` — is
therefore matching the *escaped* text, not the text in the source.

- `assert.Contains(rr.Body.String(), "user:<email>")` **fails loudly.** Annoying,
  self-announcing, fixed in minutes.
- `assert.NotContains(rr.Body.String(), "…<…>…")` **passes silently, forever**,
  and is indistinguishable from a correct guard.

The second is the one to design against, and it compounds with the
negative-assertion rule above: a `NotContains` that was never going to match
anything is the exact failure mode that mutation testing exists to catch, and
angle brackets give it a second way to arise that mutation of the *production*
string will not reveal.

**Assert on the decoded field, not the raw body.** Unmarshal into a minimal
struct and assert against `errResp.Error.Message`.

Swept 2026-09-10: eight `NotContains` assertions in the tree carry angle
brackets, all against HTML or raw non-JSON bodies. None affected. Re-sweep if
JSON error bodies start carrying markup.

Note also the distinction between verifying the *test* and verifying the *fix*.
For a change whose entire product is a string that something else reads, trace it
to its consumer. Here: `pkg/hubclient/agents.go:556` →
`pkg/apiclient/transport.go:301` → `ParseErrorResponse`, which unmarshals, so the
agent receives the unescaped text. The escaping was a harness artifact only —
but that was a finding, not an assumption.

## Never `head` a grep whose ordering you do not control when the claim is "there are none"

Verifying a "zero production callers" claim, I ran a repo-wide grep piped through
`head -20`, saw only test files, and nearly filed a false finding against correct
work. The function had **fourteen** production callers; alphabetical ordering put
one package and its tests first and `head` ate the rest.

The failure is directional and that is what makes it dangerous: truncation
removes evidence of **presence**, so it always biases toward concluding
**absence** — which is precisely the shape of claim you reach for a grep to test.

- Claim is "there are none" → `grep -c`, or read the full list. Never `head`.
- Claim is "here is an example" → `head` is fine.
- `head` is a readability convenience. It is not a summary, and it has no
  semantics you can rely on unless you sorted the input yourself.

Same family as the counting-rule and `-run`-matches-nothing traps: **an
instrument that quietly returns less than the truth, in a context where less
looks like an answer.**

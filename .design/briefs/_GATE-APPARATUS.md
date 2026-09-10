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

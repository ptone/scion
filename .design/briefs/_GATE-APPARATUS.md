# Gate apparatus — include this section in every developer brief

Measured facts about this tree. These are not suggestions; each one has already
cost someone a wrong conclusion.

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

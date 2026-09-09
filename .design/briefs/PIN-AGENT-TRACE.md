# Brief: pin the agent-scoped `message_authorized` trace call

## Base — fetch this exact commit, do not guess

Branch: `scion/tranche-g` at **`a93a94ca3`** on **`https://github.com/ptone/scion.git`**.

```
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git -C /workspace fetch -q "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
    scion/tranche-g:refs/rt/tg-pin --force
git -C /workspace worktree add --detach /tmp/wt-pin refs/rt/tg-pin -q
```

Confirm `git rev-parse --short HEAD` prints `a93a94ca3` before editing.
`origin` in your container does **not** point at ptone/scion — use the explicit
token URL for every fetch and push. Never push to `main`.

## The problem

There are two production paths that authorize an agent message, and each records
a trace step:

- `pkg/hub/handlers_projects_core.go` (~line 2539) — project-scoped route.
  **Pinned** by `TestDEF79_ProductionPathTrace` in `pkg/hub/path_trace_test.go`.
- `pkg/hub/handlers_agents_core.go` (~line 2735) — agent-scoped route, reached
  via `handleAgentAction`. **Not pinned by anything.**

Both call:

```go
messaging.RecordStep(r.Context(), "message_authorized")
```

If the agent-scoped call were deleted tomorrow, **no test would go red.** That
file is also the one upstream main rewrites most aggressively (374 insertions /
81 deletions in the last refactor alone), so the risk is not hypothetical — it
is the single most likely place in this codebase for a security-relevant line
to be lost in a merge and nobody notice.

Read the `COVERAGE BOUNDARY` comment at the top of `path_trace_test.go`. It
already names this exact gap. You are closing the first bullet of that list.

## What to do

Add a test that fails if the agent-scoped path stops recording
`message_authorized`.

Put it in a **new file** — `pkg/hub/path_trace_agent_test.go` — rather than
extending `path_trace_test.go`. The existing test pins a 12-step ordered
sequence for the project-scoped path; do not modify it, do not re-point it, and
do not generalise it to cover both paths. Two independent tests that each fail
for one clear reason are worth more here than one test with a branch in it.

Model your approach on how `TestDEF79_ProductionPathTrace` drives its path, but
target the agent-scoped route (`/api/v1/agents/{id}/message` via
`handleAgentAction`). You do not need to reproduce its full 12-step sequence —
pinning that `message_authorized` is recorded on this path is sufficient and is
the whole point. If a fuller sequence falls out naturally, fine, but do not
invent ordering guarantees that the code does not actually make.

**SUPERSEDED 2026-09-09 — this paragraph is wrong. See `_GATE-APPARATUS.md`
§ `//go:build !no_sqlite`, which is the authority.** "A test file needs sqlite,
therefore it needs the tag" is false: `no_sqlite` gates only `pkg/store/sqlite`
and the `pkg/ent/entc` driver, and `mattn/go-sqlite3` is ungated. **The tag is
required iff the file reaches a `!no_sqlite`-only package.** Adding it otherwise
silently removes the tests from `make test-fast`, the only gate in ci.yml that
can fail a build. Probe both directions and compare `--- PASS:` counts before
deciding. **Never strip the tag from an existing file to make something run** —
that part was always right.

~~If the test needs sqlite, the file **must** carry the `//go:build !no_sqlite`
tag. Adding that tag to a new sqlite-dependent test file is correct and
expected. **Never strip the tag from an existing file to make something run.**
Note the build directive sits below a 14-line Apache header — locate it with
grep, not `head -n`.

Update the `COVERAGE BOUNDARY` comment in `path_trace_test.go` to remove the
bullet you have closed, and only that bullet. Leave the others.

## How to verify — mutation, in both directions

**This is the acceptance criterion and it is not optional.** A test written
alongside the code it tests is self-consistent by construction and proves
nothing about whether it would catch a regression.

1. Run your new test on unmodified source. Record PASS.
2. **Delete** the `messaging.RecordStep(r.Context(), "message_authorized")` line
   in `handlers_agents_core.go`. Run again. It **must FAIL**, and the failure
   must name the missing step, not merely error out.
3. **Restore** the line. Run again. It **must PASS**.
4. Confirm your new test does not fire on the *other* path: delete the
   RecordStep in `handlers_projects_core.go` instead, and confirm your new test
   still passes while `TestDEF79_ProductionPathTrace` fails. This proves the two
   tests are independent and that yours is actually reading the agent-scoped
   path rather than incidentally observing the project-scoped one.

Paste the raw output of all four runs in your report.

**A mutation that fails to compile is not a caught violation.** If deleting the
line produces `declared and not used` or similar, fix the mutation (e.g. `_ = x`)
and re-run. A build error tells you nothing about test coverage.

## Collateral checks

Run these from your worktree, without pipes — `$?` after a pipeline is the exit
code of the *last* command in it, and this shell is zsh where `${PIPESTATUS[0]}`
is empty:

```
export GOCACHE=/tmp/gocache-pin       # the shared gocache is contended
go build ./... > /tmp/pin-build.log 2>&1; echo "build=$?"
go vet ./...   > /tmp/pin-vet.log 2>&1; echo "vet=$?"
make compat-literals check-authz-guards check-conversation-upsert-guard check-security-marker-gates
```

`pkg/hub` takes roughly 7 minutes to compile and test. **Run it in the
foreground.** A backgrounded job's exit code belongs to the launcher, not the
job — if you background anything, confirm the log has bytes before believing it.

Two gates are **expected red and are not yours to fix**: gofmt on
`pkg/hub/handlers_agents_core.go` and `pkg/hub/web_test.go`, and
`TestMutationClassificationBidirectional` at 200 discovered / 198 classified.
Both are inherited from upstream main and verified against a clean main
checkout. **Do not reformat those files** — you will be editing
`handlers_agents_core.go` only to mutate and revert it, so make sure your final
diff does not touch it at all.

## Reporting

Report to `ca-msg-arch` **before pushing**, with:

- `git diff --numstat`, run by you, pasted raw. Your final diff should touch
  only the new test file and the comment in `path_trace_test.go`.
- The four mutation runs, raw.
- Anything you changed that this brief did not ask for, named explicitly. If
  something is out of scope, route it to me — do not decide it is harmless.

If your findings contradict this brief, **your findings are the finding.**
Report them; do not adjust your work to match my prose.

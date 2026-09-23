# Substrate Phase 1 — review round 6 (FINAL) test-only fixes (sb-rev-6)

sb-rev-6 **APPROVED** round 5 outright (0 Critical, 0 Required, 2 Nit, 3
FYI) and is the final review round. Per sb-em/substrate-lead, this pass is
**tests only** — no production `.go` changes authorized. Only
`pkg/config/substrate_egress_test.go` and this log changed;
`pkg/config/substrate_egress.go` is untouched. Confirmed with
`git diff --stat 30c2a9845..HEAD` before pushing.

## Nit-1: the consolidated regression table asserted rejection, not reason

`TestValidateEgressAllow_AllBypassesRounds1Through5` (78 rows spanning all
five review rounds) checked only `err == nil`. sb-rev-6's own instrumented
regression pass confirmed every row's actual reason is correct today, but
the table itself would not have caught a row silently drifting onto a
*different*, wrong rejection rule — exactly the shape round 5's N5-3 bug
was (a wildcard's single-label remainder was rejected, correctly, but by
the wrong rule, with a misleading message, and the test didn't notice
because it only asserted `err != nil`).

**Fix:** added a `wantReason` field to every row and assert
`strings.Contains(err.Error(), wantReason)`. Every reason string was taken
from an actual run of the validator against that exact entry (a throwaway
diagnostic test, `NormalizeEgressAllowEntry` called directly and the real
error printed for all 78+ rows, written and deleted before committing —
nothing speculative went into the table). The reasons collapse into twelve
constants (`reasonCatchAll`, `reasonIPCIDR`, `reasonUnsupportedChar`,
`reasonNotICANN`, `reasonSingleLabel`, `reasonInvalidLabel`,
`reasonNotValidHost`, `reasonArpa`, `reasonItselfSuffix`,
`reasonWildcardSuffix`, `reasonOnion`, `reasonTooLong`), one per distinct
rejection rule in `egressAllowSuffixOK`/`validatePublicHostname`/
`NormalizeEgressAllowEntry`, so the table's intent (which rule is this row
testing?) is visible at the call site instead of buried in a duplicated
message string per row.

Two rows' names were also corrected, per sb-rev-6's specific callout: the
round-4 `foo.local`/`foo.internal` rows said "(also caught by the suffix
blocklist)", but the actual code path never reaches the blocklist for
these — the ICANN-public-suffix check (rule 1 in `egressAllowSuffixOK`)
always runs first inside `validatePublicHostname`, and "local"/"internal"
are not ICANN-managed public suffixes, so rule 1 rejects them before the
blocklist (checked several lines later) is ever consulted. Renamed to
"(rejected by the TLD check, not the suffix blocklist)". The blocklist
remains a real, load-bearing second layer — it's just not the layer that
fires for these two specific entries, which the row names now say
correctly.

## FYI-1: lock in the single-label-wildcard structural rejection

sb-rev-6 noted that round 5's N5-3 fix (letting a wildcard's single-label
remainder reach `egressAllowSuffixOK` instead of being intercepted by the
single-label check) means `*.svc`, `*.local`, `*.localhost`, and
`*.internal` no longer hit the suffix blocklist either (its
`strings.HasSuffix(ascii, ".svc")`-style check can't match a bare `"svc"`
with no leading-dot prefix to match against). This is safe today (all four
are rejected via rule 1, since none of "svc"/"local"/"localhost"/"internal"
is any kind of real or wildcard PSL entry), and would stay safe even if the
PSL ever listed one of them, because rule 2 (`ascii == ascii's own matched
suffix`) structurally rejects every single-label domain regardless — a
single label is always trivially its own public suffix, through either an
explicit PSL rule or the PSL's own default fallback rule. sb-rev-6 suggested
locking this in "cheaply" with rows in the table.

**Fix:** added the four rows to
`TestValidateEgressAllow_AllBypassesRounds1Through5`, each asserting the
actual reason (rule 1's `reasonNotICANN`, confirmed by the same diagnostic
run as the rest of the table — not rule 2's `reasonItselfSuffix`, since
rule 1 fires first for all four today).

## What did NOT change

No changes to `pkg/config/substrate_egress.go`, `pkg/runtime/*`, docs, or
any other file — verified with `git diff --stat 30c2a9845..HEAD`, which
shows only `pkg/config/substrate_egress_test.go` and this project log.
sb-rev-6's other two findings are explicitly out of scope for this pass:

- **Nit-2** (README carries review-history narrative,
  `deploy/substrate/README.md:118-130`) is a docs cleanup in sb-dev-2's
  file, not this task.
- **FYI-2** (the probe label `x` is arbitrary; a hypothetical PSL exception
  named `!x.<suffix>` could evade the wildcard-rule probes in rule 1/rule
  3) and **FYI-3** (the `*.nip.io` residual-risk doc is already correct)
  need no action — sb-rev-6 flagged both as informational only, with no
  known real-world PSL entry triggering FYI-2's hypothetical.

## Gate results

- `go build ./...` — pass (no production code changed, but run anyway to
  confirm the test file's own dependencies still compile correctly).
- `go vet ./pkg/config/...` — pass, no output.
- `gofmt -l pkg/config/substrate_egress_test.go` — clean.
- `go test -count=1` with every `SCION_*` env var unset: `./pkg/config/...`
  — pass (`config`, `config/opsettings`, `config/templateimport`),
  including the full 78-row (74 prior + 4 new FYI-1 rows) reason-asserting
  table.
- `go test -race -count=1` with `SCION_*` unset: `./pkg/config/...` — pass,
  no races.
- `git diff --stat 30c2a9845..HEAD` — confirmed only
  `pkg/config/substrate_egress_test.go` and this log changed.

No `go.mod`/`go.sum` changes.

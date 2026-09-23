# Substrate Phase 1 — comment sweep ahead of the upstream PR

This branch is going upstream, so the code comments needed a pass to remove
internal review-process artifacts — round numbers, internal reviewer/agent
names, and internal finding IDs — that mean nothing outside this repo's own
workflow and would read as noise (or worse, as an unexplained internal
process) to an external reader. This is a comment-only sweep: no
identifiers, logic, strings, or error/log messages changed in any
non-test `.go` file; test files additionally had test/subtest names and
test-only comments cleaned up, since those don't ship as user-facing
behavior.

## Scope

Every file this branch adds or changes (`third_party/` and `.design/`
excluded), checked with a full-file, case-insensitive grep for patterns
matching round numbers, internal reviewer/agent name prefixes, finding IDs,
and similar internal review-artifact shapes.

Two files in that list carry hits from a **different, unrelated** internal
review process that predates this branch and has nothing to do with
Substrate: `pkg/config/settings_v1.go` (a settings/config feature with its
own internal review history — different reviewer-name prefixes entirely)
and `pkg/runtime/cloudrun_sandbox_runtime.go` (two "Finding #" comments on
existing Cloud Run Sandbox code). Confirmed via `git blame` that every one
of those lines predates this branch's own first commit — this branch never
touched them — so they're out of scope here and were left alone.

## What changed

Every genuinely in-branch hit was either:

- **Replaced with the technical reason** the comment was making a point
  about, when that reason mattered on its own (e.g. why the ICANN-suffix
  check runs before the suffix blocklist, why a per-instance gRPC
  connection needs process-wide memoization, why a read failure during
  bootstrap-secret collection is deliberately swallowed); or
- **Dropped outright**, when the surrounding sentence already carried the
  full technical justification and the citation added nothing beyond
  attribution (e.g. "least privilege — the broker cannot read any other
  cluster's/tenant's trust bundle" already explains the RBAC scoping
  without naming which review round asked for it).

Files touched: `deploy/substrate/broker.yaml`,
`pkg/config/substrate_egress.go` (+ its test file),
`pkg/runtime/substrate/router.go`, `pkg/runtime/substrate_bootstrap.go`,
`pkg/runtime/substrate_egress.go` (+ its test file),
`pkg/runtime/substrate_runtime.go` (+ its test file),
`pkg/runtime/substrate_template.go`, `pkg/sciontool/substrate/server.go`
(+ its test file).

`pkg/config/substrate_egress.go`'s doc comments carried the heaviest
history — the egress-allowlist validator went through several rounds of
refinement (the public-suffix rule, the PRIVATE-vs-ICANN PSL split, the
wildcard-over-wildcard-PSL-rule check, the ccTLD false-positive fix, the
`.onion` special case, the length cap). All of that is now described as the
current design and its rationale, not as a numbered sequence of review
outcomes — a reader shouldn't need this repo's review history to understand
why the validator works the way it does.

## Test-file-specific changes

Per the wider latitude for test files, `pkg/config/substrate_egress_test.go`
and `pkg/runtime/substrate_runtime_test.go` also had test/subtest names
cleaned up, not just doc comments:

- `TestValidateEgressAllow_AllBypassesRounds1Through5` renamed to
  `TestValidateEgressAllow_KnownBypasses`.
- `TestValidateEgressAllow_R4_1RejectionReason` renamed to
  `TestValidateEgressAllow_TLDNotICANNRejectionReason` (describes what the
  test actually asserts, rather than which round found the gap).
- Every subtest row name in the consolidated bypass table (previously
  prefixed `round1:`/`round2:`/.../`round6 ...:`) renamed to describe the
  bypass class instead (e.g. `IP/CIDR: 10.0.0.0/8`, `Kubernetes short name:
  router`, `special-use TLD: onion hidden service`), and the section-divider
  comments above each group of rows were rewritten the same way.
- `t.Errorf`/`t.Error` message text that named an internal finding ID
  (e.g. an assertion failure message that included `(N5-1)`) had that
  parenthetical dropped, since it named a review artifact rather than
  anything a test failure's own diagnostic needs.

None of this changed what any test does — every entry, expected reason
substring, and assertion is untouched; only names and comments moved.

## Commit messages carrying review-round noise (not reworded here)

Per instruction, commit history was not rewritten. These commit messages
(subject lines) on this branch contain round numbers, internal
reviewer/agent name prefixes, or internal finding-ID shorthand, and are
flagged for a later reword pass rather than fixed in this task:

- `3bfd3318f` — "Round 6 (final): assert rejection reasons in the
  consolidated bypass table, lock in FYI-1 (Nit-1, FYI-1)"
- `30c2a9845` — "Round 5 egress_allow fixes: wildcard-vs-PSL-wildcard,
  ccTLD false positives, .onion, wildcard reason, length cap (N5-1..N5-4)"
- `606e42e4f` — "Fix N5-5: broken workerpool-labels command, verified
  against live cluster"
- `d5dc2133c` — "Document round 4 egress_allow refinement and residual
  DNS-rebinding risk"
- `eb50f37b9` — "Rebuild egress_allow on the public suffix list; reject all
  IP/CIDR entries (R4-1, R4-2, N4-1)"
- `5025433d7` — "Fix round 4 README nits N4-2/N4-3: unverified value, wrong
  citation"
- `455ad9a2d` — "Add project log entry for review round 3 fixes"
- `a00729893` — "Update project log: review round 2 (R1/R2, O1-O5, N1-N2)"
- `c08f87eed` — "Tests for round 2: cross-config state sharing (R1),
  factory-path memoization (O4), 409 secret-freeness (O5), auth-file/
  collision redaction"
- `07520be0b` — "Update project log: task2 egress hardening + review round
  1 fixes"
- `ec2d3b90d` — "Tests for review round 1: memoization, List
  pagination/filtering, 409, redaction sources, telemetry egress, template
  hash inputs"
- `e0ebdb271` — "Fix round 1 review findings #9/#12 + worker NetworkPolicy
  (sb-dev-2 files)"
- `ff0e624ea` — "Add project log entry for the substrate runtime slice
  (sb-dev)"

## Verification

- Full-file, case-insensitive grep for the review-artifact patterns across
  every file this branch adds or changes: returns only the two pre-existing,
  out-of-branch false positives described above.
- `git diff <pre-sweep tip> -- '*.go' ':!*_test.go'`, filtered to lines that
  aren't blank or `//`-comment lines: empty — confirms every non-test `.go`
  file changed only in its comments.
- `go build ./...` — pass.
- `go vet` on `pkg/config/...`, `pkg/runtime/...`,
  `pkg/sciontool/substrate/...` — pass, no output.
- `gofmt -l` on every changed `.go` file — clean.
- `go test -count=1` with every `SCION_*` env var unset, on
  `pkg/config/...`, `pkg/runtime/...`, `pkg/sciontool/substrate/...` — all
  pass, including the renamed
  `TestValidateEgressAllow_KnownBypasses`/`_TLDNotICANNRejectionReason` and
  every renamed subtest row.
- `go test -race -count=1` on the same packages (excluding
  `pkg/runtime/cloudrun`, which has a pre-existing, unrelated data race
  untouched by this branch) — pass, no races.

No `go.mod`/`go.sum` changes. No production behavior changed — this is a
comment- and test-name-only sweep.

# PR3: restack onto the landed PR2 tip and fork main

**Date:** 2026-10-02
**Agent:** dev-pr1-reconcile
**Branch:** scion/dev-pr1-reconcile (this entry); work performed on a local `pr3` branch tracking `scion/substrate-pr3-restack-wip`

## Context

PR3's prior restack had landed on the then-current PR2 tip (`e53858ef`).
That base moved twice in quick succession: first PR2 itself was rebased
onto a newer fork `main` (new tip `989a5237`, which also absorbed an
independent upstream host-path-validation hardening commit), then PR2
landed upstream as a squash merge and its own branch was deleted
(`989a5237` pinned locally via `git tag pin-pr2-989a5237` before that
object became otherwise unreachable, at the explicit request of the
dispatching agent — the squash is a different commit object even though
its tree content is identical). PR3 was restacked twice in sequence to
follow: once onto `989a5237`, then again onto fork `main` once the squash
landed there.

## What changed

**Mechanical conflicts** across both rebases (struct-field unions,
doc-comment merges, rootexec allowlist line-number regens, one additive
JSON-schema union where PR3's own substrate config property collided
textually with an unrelated upstream commit's new `priority_class_name`
property) were resolved using the same patterns established in the PR1/PR2
restacks — no design decisions required.

**The one substantive finding**: the new PR2 tip had independently evolved
`pkg/runtimebroker`'s `writeStartContextError` (fixing an unrelated
upstream bug around HTTP status collapsing) into a shape PR3's own two
commits in this area (one adding redaction primitives, one adding
per-handler logging) assumed was still the old shape. Escalated rather than
guessed at the composition; ruled to thread an `op` parameter through the
landed function and redact+log its three non-4xx/non-connectivity branches
there, leaving the 4xx and hub-connectivity-503 paths untouched — applied
in-place across both commits, with their now-redundant helper functions
folded into the single landed function rather than left as dead duplicates.
A dedicated test proves the redaction/logging behavior with explicit
RED-without/GREEN-with evidence, and the existing 4xx/503 positive controls
were confirmed to stay green in both states.

**One post-second-rebase test regression**, diagnosed (not a PR3 bug, not
a fork issue): an unrelated upstream commit changed `buildStartContext`'s
hub-endpoint resolution to key off the dispatch-resolved runtime type
rather than the broker's default runtime name directly, which broke one of
PR3's own tests that used an older test-server helper no longer compatible
with that resolution path. Fixed by swapping to the post-upstream-commit
test helper upstream's own sibling tests already use (same signature,
drop-in) — a test-harness adaptation, not a behavior change; the
assertions themselves are untouched.

One flaky (non-deterministic, confirmed-non-blocking on rerun) test was
observed in a single combined run and not touched.

## Verification

Full gate suite green at the final tip: `go build ./...`, `go vet ./...`,
`gofmt`, `CGO_ENABLED=0` darwin amd64/arm64 cross-compiles,
`golangci-lint --new-from-merge-base=origin/main` (0 issues), the rootexec
exec-site-allowlist guard test, and the full `go test -p 2 -count=1` suite
across every touched package. `git log --oneline origin/main..HEAD`
contains only the 39 PR3 commits; `git merge-base --is-ancestor origin/main
HEAD` confirms origin/main is an ancestor of the pushed tip. `pkg/hub`'s own
full test suite could not complete inside Go's test-binary timeout in this
sandbox even at an extended 20 minutes — confirmed zero individual test
failures in its output (a pre-existing package-size/environment-timing
characteristic, not a regression; disclosed rather than worked around).

Pushed to `scion/substrate-pr3-restack-wip` at tip
`20ae48ac814eaa19d52230be1ee57ba08a8dad8b`. The real `scion/substrate-pr3`,
`scion/substrate-pr2`, and `main` branches were not touched. Full report:
`preflight/pr3-restack.md` (scratchpad).

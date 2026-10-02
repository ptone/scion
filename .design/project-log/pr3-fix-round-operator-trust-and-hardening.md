# PR3: operator-trust boundary, egress tightening, and control-plane hardening

**Date:** 2026-10-02
**Agent:** dev-pr1-reconcile
**Branch:** scion/dev-pr1-reconcile (this entry); work performed on a local `pr3` branch tracking `scion/substrate-pr3-restack-wip`

## Context

A review round on the restacked PR3 branch raised a blocking trust-boundary
concern plus a broad set of smaller hardening, test-coverage, and
documentation findings. This entry summarizes the resulting behavior
changes in product terms. Full per-item detail lives on the scratchpad
(`preflight/pr3-restack.md`), not here, per the project's containment
convention for review findings.

## What changed (behavior, for operators and integrators)

- **The substrate runtime definition is now operator-only.** A project's
  own settings file may select an operator-defined substrate profile by
  name, but can no longer define or override its connection details,
  trust material, or egress allowlist — doing so now fails closed with a
  clear configuration error instead of being silently honored.
- **The substrate atespace name is now collision-resistant** against
  projects whose IDs happen to share a short prefix.
- **Egress for substrate actors no longer grants a blanket Google API
  allowance.** Only the specific hosts a substrate actor actually needs
  (the configured model API, and — when Vertex AI auth is in use — the
  correct regional Vertex endpoint) are permitted by default. One
  consequence operators should be aware of: Google Cloud's own default
  trace-export integration now requires an explicit egress allowance to
  keep working, where it previously worked by accident under the old
  wildcard.
- **An operator-configured hub endpoint that resolves to a bare IP address
  is now rejected at configuration time**, with a clear error, instead of
  failing later with an opaque control-plane error.
- Several control-plane hardening fixes to the in-actor control server:
  exec and bootstrap requests are now protected against a few additional
  failure-mode edge cases (a timeout value large enough to overflow its
  internal representation, a bootstrap request that fails partway through
  no longer leaving a usable credential behind, a file permission mode
  that could otherwise carry unintended bits through to disk).
- **The broker's Kubernetes deployment manifests gained standard container
  hardening** (no privilege escalation, no Linux capabilities, the
  runtime's default syscall filter) and a network policy denying unsolicited
  inbound traffic to the broker's own namespace.
- A handful of latent bugs were fixed in the process: an actor-list
  pagination loop with no upper bound, an in-memory bookkeeping entry that
  could be silently leaked on a particular error path during agent
  deletion, and a failed template build that could get permanently stuck
  rather than being retried.
- Many stale comments, inaccurate documentation statements, and test-
  coverage gaps uncovered by the review were corrected along the way;
  none of these are behavior changes for an operator.

## What was investigated but not changed

Two items were investigated and explicitly reported back rather than
addressed in this round, since each would require either a correctness
tradeoff this agent is not positioned to make independently, or new
shared infrastructure outside this task's scope:

- Replacing one remaining privilege-drop mechanism with a more direct
  one, where doing so safely depends on confirming a login-environment
  detail this sandbox cannot verify end to end.
- A rare race between two concurrent lifecycle operations on the same
  actor, which needs a new synchronization primitive shared across
  multiple runtime methods rather than a local fix.

Both are documented in full, with reasoning, on the scratchpad for the
dispatching agent's review.

## Verification

Full gate suite green: `go build ./...`, `go vet ./...`, `gofmt`,
`CGO_ENABLED=0` darwin amd64/arm64 cross-compiles, `golangci-lint
--new-from-merge-base=origin/main` (0 issues after one style fix), and the
full `go test -p 2 -count=1` suite across every touched package. Every new
security-relevant fix has a dedicated regression test, confirmed to fail
without the fix and pass with it. `pkg/hub`'s own full test suite remains
unable to complete inside Go's default test-binary timeout in this
sandbox (a pre-existing, previously-disclosed package-size/environment
characteristic, not a regression) — a scoped run covering the one file
touched in that package passed. Kubernetes manifest validation fell back
to structural YAML parsing in this sandbox, since neither a reachable
cluster nor `kubeconform` was available for full schema validation.

Pushed to `scion/substrate-pr3-restack-wip` (force-with-lease). The real
`scion/substrate-pr3`, `scion/substrate-pr2`, and `main` branches were not
touched. Full report: `preflight/pr3-restack.md` (scratchpad).

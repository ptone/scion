# PR3: direct privilege drop for exec, fail-closed broker startup, final rebase

**Date:** 2026-10-03
**Agent:** dev-pr1-reconcile
**Branch:** scion/dev-pr1-reconcile (this entry); work performed on a local `pr3` branch tracking `scion/substrate-pr3-restack-wip`

## Context

A second, smaller review pass on the PR3 branch asked for one mechanism to
be finished (a privilege-drop path that had been left partially migrated),
one more startup-safety gap closed, a real Kubernetes-manifest schema
validation pass, and a final rebase onto the latest shared branch. This
entry summarizes the resulting behavior changes in product terms. Full
per-item detail lives on the scratchpad (`preflight/pr3-restack.md`), not
here, per the project's containment convention for review findings.

## What changed (behavior, for operators and integrators)

- **The in-actor exec endpoint's privilege drop no longer shells out to a
  login-shell program.** It now drops directly to the unprivileged user via
  the same kind of process credential the main workload process already
  uses, and sets that user's home directory, username, working PATH, and
  shell explicitly rather than relying on a login shell to populate them.
  This removes a dependency on a specific external program's version and
  flags being present in the runtime image, and makes a carried-through CA
  trust bundle (when one is configured) survive automatically rather than
  needing an explicit pass-through list.
- **The runtime broker now refuses to start** if its own configured default
  runtime fails to construct or validate — previously it would start
  anyway and silently become permanently unable to run, stop, or inspect
  any agent on that runtime, with no signal beyond one log line. Operators
  are now also told explicitly: delete every agent on a runtime before
  removing or repointing that runtime's configuration, since no runtime
  would remain registered afterward to manage agents left on the old one.
- The broker's Kubernetes deployment manifests (introduced in the prior fix
  round) were validated against real Kubernetes API schemas for the first
  time, confirming they are well-formed; a custom resource definition
  specific to the underlying agent runtime has no bundled schema to check
  against and was skipped, as expected for that resource type.
- Rebased onto the latest shared branch, picking up unrelated work that had
  landed in the meantime (a runtime-resolution change to agent start/restart
  responses, and per-agent workspace changes for a shared-filesystem
  deployment mode). One small, already-superseded fallback code path was
  removed as part of reconciling with that runtime-resolution change, since
  the newer code now does the equivalent resolution earlier and more
  correctly; nothing it did is lost.

## Verification

Full gate suite green: `go build ./...`, `go vet ./...`, `gofmt`,
`CGO_ENABLED=0` darwin amd64/arm64 cross-compiles, `golangci-lint
--new-from-merge-base=origin/main` (0 issues), and the full
`go test -p 2 -count=1` suite across every touched package, including two
tests from the newly-rebased-in work that specifically needed to be
confirmed still passing afterward. Kubernetes manifest validation used a
real schema-validation tool this round (a prior round could only confirm
well-formed YAML structurally, for lack of the tool).

Pushed to `scion/substrate-pr3-restack-wip` (force-with-lease). The real
`scion/substrate-pr3`, `scion/substrate-pr2`, and `main` branches were not
touched. Full report: `preflight/pr3-restack.md` (scratchpad).

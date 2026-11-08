# Substrate v1 API migration, Phase 1e (operator mode)

**Date:** 2026-11-07
**Branch:** scion/substrate-v1-p1 (stacked on the Phase 1a tip abf0e167)
**Scope:** design v0.5.5 §3.3 ("List source in operator mode"), §3.7
(verify-or-fail `ensureAtespace`), §7 Phase 1e and AC 12, with two
clarifications to §3.3:

- On a broker with several hub connections, the served-project set is the
  union over the connections, and it fails closed while any current
  connection has never loaded.
- A single install helper gives the set to every runtime that enters the
  broker, and a test covers every entry path.

## What was built

- **ProjectSet** (`pkg/runtime/project_set.go`). A one-method interface,
  `ServedProjects(ctx) ([]string, error)`, plus the optional
  `ProjectSetReceiver` capability (`InstallProjectSet`,
  `ReleaseProjectSet`). pkg/runtime defines
  and consumes the interface and does not import the hub client.
- **Broker refresher** (`pkg/runtimebroker/served_projects.go`). Reads
  `hubclient.RuntimeBrokers().ListProjects(brokerID)`, once per hub
  connection, and keeps one snapshot per connection. The set is the union of
  the snapshots.
  - Loads: at broker start (`startServices`, after the hub connections
    start), on every control-channel connect (`onControlChannelState`
    requests a reload), and on a 5-minute ticker. The ticker and the clock
    are injected. `Shutdown` cancels the refresher and waits for it.
  - A failed load keeps that connection's last good snapshot. Using it is
    logged at Warn, with its age, once per failing stretch of that
    connection, at Debug while the loads keep failing, and at Info when a
    load succeeds after that warning.
  - Cold rule: `ServedProjects` errors while any current source has never
    loaded, or when there is no source at all. The cold error names only the
    hub connection.
  - Sources are the hub connections that are started (heartbeat or control
    channel running), not closed, and have a hub client and a broker ID. A
    removed or stopped connection drops out of the union and the cold check,
    and a reload drops its snapshot, so a connection added again starts cold.
- **Injection.** `Server.installProjectSet` runs at every runtime entry
  path: `New`, `SwapRuntime` and `newAuxiliaryRuntime`. The last is the only
  builder of an `auxiliaryRuntime` entry; the two discovery inserts in
  `server.go` and the late-profile insert in `handlers.go` call it. A source
  guard test fails if code builds an entry or writes a runtime into one any
  other way. A runtime without a ProjectSet fails an unfiltered List as cold.
- **One broker instance per substrate runtime.** `NewSubstrateRuntime`
  memoizes runtimes per config process-wide, so two broker instances in one
  process can resolve the same runtime. `InstallProjectSet` refuses a runtime
  that already carries another instance's set with
  `ErrSubstrateRuntimeShared` ("operator mode supports one broker instance
  per process"), and accepts the same instance's set again.
  - An instance whose default runtime is refused does not start.
  - A refused `SwapRuntime` keeps the current runtime.
  - A refused auxiliary runtime is not registered. A refused late-profile
    resolution returns an error runtime.
  - `Shutdown` releases the instance's set from every runtime it installed
    it on, clearing it only if it is still that instance's set, so a
    successor instance in the same process can install its own. Each
    comparable runtime (production runtimes are pointers) is recorded once,
    however often the set is installed on it again. The check compares two
    receivers only when they have the same type and both values are
    comparable, so it never panics; a receiver whose value is not comparable
    is recorded on each install.
- **Verify-or-fail `ensureAtespace`.** It calls `GetAtespace` and never
  `CreateAtespace`. NotFound or PermissionDenied returns
  `*SubstrateAtespaceProvisioningError` (matches
  `ErrSubstrateAtespaceNotProvisioned`), worded "ask your operator to
  provision atespace `<name>` and grant the broker editor on it".
  - The error keeps only the atespace name and the gRPC code. Its `Unwrap`
    returns a status with the code and an empty message, so neither the text
    nor `status.FromError` exposes the server's message.
  - Run returns it as is rather than through `redact`, so callers can still
    match it.
  - Atespace naming stays the same (`scion-` + sha256(projectID)[:32]).
- **Per-atespace List.**
  - A project ID in `labelFilter` reads that atespace only. NotFound or
    PermissionDenied there returns the typed error. The label filter still
    applies, so a record-less actor (no project label) is not returned
    under a project filter.
  - Otherwise List reads the atespace of each served project, and any error
    other than NotFound or PermissionDenied fails the List. NotFound and
    PermissionDenied are skipped:
    - the skip note is logged at Warn with its count when the skipped set
      changes, at Debug while it stays the same, and at Info when it clears;
      only an unfiltered List updates it, so a project-scoped List does not
      reset it;
    - a PermissionDenied skip may hide live actors, so it marks the caller's
      `ListCompleteness` (`pkg/runtime/list_completeness.go`) incomplete;
    - a NotFound skip hides nothing and leaves the List complete.
  - List issues no empty-atespace ListActors. `maxActorListPages` applies
    per atespace, and actors reporting another atespace are dropped.
  - `RecordlessActors` still fails on any error.
- **Heartbeat completeness.** The heartbeat attaches a `ListCompleteness`
  to each target listing. A listing that succeeds but is marked incomplete
  still reports its agents, and the target is reported incomplete, so the
  hub does not reconcile that target's unseen agents as missing. Runtimes
  that never mark it are unaffected.

## Phase 1a hunks changed

- `substrate_runtime.go` List: the single empty-atespace ListActors loop is
  replaced by `listOperatorModeActors`/`listAtespaceActors`. The
  `substrateAtespacePrefix` and `maxActorListPages` comments are updated.
- `substrate_runtime.go` Run step 1: `CreateAtespace` (AlreadyExists
  ignored) is replaced by verify-or-fail `ensureAtespace`, and the typed
  provisioning error bypasses `redact`.
- `substrate_lifecycle_v1_test.go` `TestSubstrateList_UsesListOptions`:
  asserted the empty atespace and asserts the served project's atespace.

## Test changes outside the new tests

- The test fakes gained `GetAtespace` and lost `CreateAtespace`, so any
  CreateAtespace call panics on the embedded nil interface.
- Fixtures that an unfiltered List must find use real hashed atespaces, and
  the runtimes under test get a static ProjectSet (`SetProjectSetForTest`):
  the pkg/runtime harness, the pkg/agent delete tests, and
  `newTestSubstrateBrokerServer`.
- `substrate_restart_gaps_test.go` told List apart from the record-less
  probe by request shape (List's ListActors was unscoped). Both are
  atespace-scoped, so faults are injected at the method instead
  (`substrateFaults`, which embeds the runtime). Before 1e each List call was
  exactly one unscoped RPC, so "List call N" keeps the earlier "unscoped call
  N" meaning.

## Known limits

- A hub connection that runs without a control channel gets no reconnect
  reload. It is picked up at start and by the 5-minute tick.
- One broker instance per substrate runtime per process; a second instance
  that resolves the same substrate config is refused rather than isolated.
- An unfiltered List reads atespaces one at a time.

## Verification

Verified with, offline and with a private module cache:

- `go test -count=1 ./pkg/runtime/ ./pkg/agent/ ./pkg/runtimebroker/`, under
  umask 022 with a short private `TMPDIR`. Some tests check file modes or
  bind a unix socket under the test temp directory, whose path must stay
  within the 107-byte limit.
- `golangci-lint run --new-from-rev=de18c73c ./pkg/runtime/...
  ./pkg/runtimebroker/... ./pkg/agent/...`
- `gofmt -l` over the changed Go files.
- `go build -buildvcs=false ./...`

# Substrate v1 API migration, Phase 1e (operator mode)

**Date:** 2026-11-07
**Branch:** scion/substrate-v1-p1 (stacked on the Phase 1a tip abf0e167)
**Scope:** design v0.5.5 §3.3 ("List source in operator mode"), §3.7
(verify-or-fail `ensureAtespace`), §7 Phase 1e and AC 12. Two clarifications
to §3.3 from substrate-lead apply: multi-hub ProjectSet (Q1, with amendments
(a) and (b)) and a single install helper for every runtime entry path (Q2).

## What was built

- **ProjectSet** (`pkg/runtime/project_set.go`). A one-method interface,
  `ServedProjects(ctx) ([]string, error)`, plus the optional
  `ProjectSetReceiver` capability. `SubstrateRuntime.SetProjectSet` stores it.
  pkg/runtime defines and consumes the interface and does not import the hub
  client.
- **Broker refresher** (`pkg/runtimebroker/served_projects.go`). Reads
  `hubclient.RuntimeBrokers().ListProjects(brokerID)`, once per hub
  connection, and keeps one snapshot per connection. The set is the union of
  the snapshots.
  - Loads: at broker start (`startServices`, after the hub connections
    start), on every control-channel (re)connect (the existing
    `OnConnectionStateChange(true)` callback in `hub_connection.go`), and on a
    5-minute ticker. The ticker and the clock are injected.
  - A failed load keeps that connection's last good snapshot. Its age is
    logged when the snapshot is used.
  - Cold rule: `ServedProjects` errors while any current source has never
    loaded, or when there is no source at all. The cold error names only the
    hub connection.
  - Sources are the hub connections that are started (heartbeat or control
    channel running), not closed, and have a hub client and a broker ID. A
    removed or stopped connection drops out of the union and out of the cold
    check.
- **Injection.** `Server.installProjectSet` runs at every runtime entry
  path: `New`, `SwapRuntime`, and `newAuxiliaryRuntime`. The last is the only
  builder of an `auxiliaryRuntime` entry; the two discovery inserts in
  `server.go` and the late-profile insert in `handlers.go` call it. A runtime
  without a ProjectSet fails an unfiltered List as cold.
- **Verify-or-fail `ensureAtespace`.** It calls `GetAtespace` and never
  `CreateAtespace`. NotFound or PermissionDenied returns
  `*SubstrateAtespaceProvisioningError` (matches
  `ErrSubstrateAtespaceNotProvisioned`), worded "ask your operator to
  provision atespace `<name>` and grant the broker editor on it". Its text
  carries only the atespace name and the gRPC code. Run returns it as is
  rather than through `redact`, so callers can still match it. Atespace
  naming stays the same (`scion-` + sha256(projectID)[:32]).
- **Per-atespace List.**
  - A project ID in `labelFilter` reads that atespace only. NotFound or
    PermissionDenied there returns the typed error.
  - Otherwise List reads the atespace of each served project. NotFound and
    PermissionDenied are skipped and counted in one `slog.Warn`; any other
    error fails the List.
  - There is no empty-atespace ListActors anymore. `maxActorListPages`
    applies per atespace, and actors reporting another atespace are dropped.
  - `RecordlessActors` still fails on any error.

## Phase 1a hunks changed

- `substrate_runtime.go`: List's single empty-atespace ListActors loop is
  replaced by `listOperatorModeActors`/`listAtespaceActors`. The
  `substrateAtespacePrefix` and `maxActorListPages` comments are updated.
- `substrate_lifecycle_v1_test.go` `TestSubstrateList_UsesListOptions`:
  asserted the empty atespace and asserts the served project's atespace.

## Test changes outside the new tests

- The test fakes gained `GetAtespace` and lost `CreateAtespace`, so any
  CreateAtespace call panics on the embedded nil interface.
- Fixtures that an unfiltered List must find use real hashed atespaces, and
  the runtimes under test get a static ProjectSet: the pkg/runtime harness,
  the pkg/agent delete tests, and `newTestSubstrateBrokerServer`.
- `substrate_restart_gaps_test.go` told List apart from the record-less
  probe by request shape (List's ListActors was unscoped). Both are
  atespace-scoped, so faults are injected at the method instead
  (`substrateFaults`, which embeds the runtime). Before 1e each List call was
  exactly one unscoped RPC, so "List call N" keeps the earlier "unscoped call N"
  meaning.

## Known limits

- A hub connection that runs without a control channel gets no reconnect
  reload. It is picked up at start and by the 5-minute tick.
- `NewSubstrateRuntime` memoizes instances process-wide. If two broker
  `Server`s in one process resolved the same substrate config, the last
  `SetProjectSet` would win.

## Verification

See the Phase 1e developer report for the compile windows, commands and
results.

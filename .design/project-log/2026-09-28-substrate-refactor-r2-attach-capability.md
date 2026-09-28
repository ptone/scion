# Per-profile attach capability (R2)

**Date:** 2026-09-28
**Branch:** `scion/substrate-refactor-r2`, cut from `scion/substrate-refactor@c1cafb0b7` (base untouched throughout)
**Author:** dev-refactor-attach (Wave-2 refactor unit: R2)
**Component:** `pkg/runtime/{capabilities,substrate_runtime}.go`, `pkg/runtimebroker/{types,handlers,errors,pty_handlers,controlchannel,server,heartbeat,hub_connection}.go`, `pkg/store/models.go`, `pkg/hubclient/types.go`, `cmd/{attach,common,broker,server_broker}.go`

This entry covers the whole unit as shipped. The branch was squashed and
reordered late in development (see "Bisectability" below) after review found
the incremental history had a broken intermediate build and, separately, an
ordering gap; the design itself went through several iterations before
landing, which the "Rejected designs" section records for anyone revisiting
this.

## The problem

Attach — interactive PTY access to an agent — has one runtime that cannot
support it: substrate, whose broker-side PTY path can only reject a stream
*after* a caller's WebSocket upgrade has already succeeded, which the
caller can only ever observe as an unexplained abnormal close, not a clean
error. Before this unit, the only guard was a hardcoded
`agentRuntime == "substrate"` string check in the CLI. That worked, but
named a specific runtime everywhere a generic "does this support attach"
question was really being asked, and gave every future opt-out runtime the
same choice: duplicate the string check, or go unguarded.

## Seam: an optional capability, not a name

`pkg/runtime/capabilities.go` gains `AttachCapableRuntime` /
`HasAttachSupport`, the same shape as the existing
`PerProfileInstancesRuntime` / `HasPerProfileInstances` seam a prior unit
(R11+R6) introduced:

```go
type AttachCapableRuntime interface {
    SupportsAttach() bool
}
func HasAttachSupport(rt Runtime) bool {
    ac, ok := rt.(AttachCapableRuntime)
    return !ok || ac.SupportsAttach()
}
```

Missing-⇒-supported is the deliberate inverse of `PerProfileInstances`'s
missing-⇒-false: the two capabilities describe opposite histories.
Per-profile-instances is a new restriction almost no runtime needs; attach
has been universally available up to now, so only a runtime that actively
lacks the primitive needs to opt out. `SubstrateRuntime.SupportsAttach()`
returns `false`; this is the *only* place substrate is named as unsupported
anywhere in this feature.

## Broker side: resolve from a live instance, never construct one

Two entry points reject an unsupported attach before doing any
runtime-specific work, both asking the *live* `Runtime` instance that
actually produced the match, never a type name:

- `handleAgentAttach` (the direct-connect WebSocket endpoint) — a 501
  (`RuntimeAttachUnsupported` / `ErrCodeRuntimeAttachUnsupported`, mirroring
  the existing `RuntimeLogsUnsupported` pattern) before `ptyUpgrader.Upgrade`.
- `handlePTYStream` (the control-channel counterpart) — closes the stream
  (4503 `ClosePTYUpstreamUnavailable` / `CloseReasonSessionNotReady`, the
  same code `classifyAttachEnd` already used for this shape of failure —
  reusing it rather than inventing a new close code, see "Deferred" below)
  before starting any tmux exec.

Both read `AgentLookupResult.Runtime`, a new field `Server.LookupAgent` sets
on every match path — the matched auxiliary runtime, or the default runtime
when the match came from there — including the no-project-label fallback
stage. `LocalPTYSession.Run` and `StreamPTYHandler.Run` no longer have their
own `runtimeCmd == "substrate"` checks: both callers already reject before
either type is ever constructed, so there is no remaining path where an
unsupported runtime reaches a docker-style exec attempt.

The broker's own `/info` endpoint (`buildInfoProfiles`) and heartbeat
(`HeartbeatService.buildHeartbeat`) report the same capability outward.
Both use `Server.resolveLiveRuntimeInstance(rtType, defaultRuntimeType)
(rt, ok)` — the one shared answer to "is there already a live instance
backing this profile":

- `rtType == defaultRuntimeType` → `s.runtime` (always live once the broker
  is running).
- otherwise → `s.auxiliaryRuntimes[rtType]`, **if** some prior request
  already built and cached one there (`resolveManagerForOpts`'s existing
  cache) — **never constructed here**.
- neither → `ok = false` → the profile's `Attach` is `nil` (unknown), not a
  guess from the type string. (The earlier version of this code guessed
  `true` for any non-default type, which is exactly the bug this replaced:
  a substrate profile on a docker-default broker was advertised
  `attach:true`.)

`BrokerProfile.Attach` (`pkg/runtimebroker/types.go`, and its Hub-side
mirrors in `pkg/store/models.go` / `pkg/hubclient/types.go`) is `*bool`
(`json:"attach,omitempty"`) to carry that third state.
`BrokerCapabilities.Attach` stays a plain `bool` everywhere: the broker
that reports it always sets it explicitly.

`HeartbeatService` gained a `defaultRuntime` field (`SetDefaultRuntime`,
mirroring the existing `SwapManager` setter), set when a hub connection
starts a heartbeat and kept in sync by `SwapRuntime`.

## Registration producers: an already-built instance, still never a constructed one

Two producers populate `store.RuntimeBroker.Profiles` at registration/join
time, independently of the running broker's own `/info`:

- `buildStoreBrokerProfiles` (`cmd/server_broker.go`, the embedded/colocated
  hub+broker's path via `registerGlobalProjectAndBroker`) gained the
  caller's already-constructed default runtime instance as a parameter
  (`registerGlobalProjectAndBroker` already had it, `rt.Name()` two lines
  above — it just wasn't threaded through). The default-type profile gets a
  real `HasAttachSupport(defaultRuntime)` answer, refreshed on every
  registration including re-registration/restart; every other profile
  stays `nil` (this function has no auxiliary-runtime tracking of its own —
  that only exists on a running `runtimebroker.Server`). The broker-wide
  `Capabilities.Attach` this same registration path writes, in both the
  create and update branches, comes from the same instance instead of a
  hardcoded `true`.
- `buildBrokerProfiles` (`cmd/broker.go`, the standalone `scion broker
  register` command for an external/remote broker) has **no live instance
  available at all**: this command only sends metadata to a Hub, typically
  before the broker daemon itself is even running. Every profile from this
  producer reports `Attach: nil`, and its two capability-string literals
  (`CreateBrokerRequest`/`JoinBrokerRequest`, previously copy-pasted) are
  deduped into one `brokerRegistrationCapabilities()` helper, documented as
  unconditionally including `"attach"` for the same reason: there is
  nothing here to say otherwise.

## C1: the CLI's fallthrough was the actual production gap

The mechanism above answers correctly everywhere a live instance exists.
But composing it with a standalone/remote broker exposed a real regression:
`buildBrokerProfiles` never sets a profile's `Attach` (no live instance,
above), the heartbeat only ever refreshed `Capabilities`, never `Profiles`
(deferred item (a), below) — so on a standalone broker, every profile's
`Attach` is permanently `nil`. The CLI's `attachSupportedByBroker`
originally handled a *matched* nil-`Attach` profile by returning `true`
directly, without ever consulting the broker-wide `Capabilities.Attach`:

```go
if p.Attach != nil {
    return *p.Attach
}
return true // BUG: never checked Capabilities.Attach
```

On a standalone broker whose default (and only) runtime is substrate, this
meant `--profile substrate` → CLI reports supported → dials → the exact
post-upgrade abnormal close this whole feature exists to prevent. Silently
reintroducing, for exactly the deployment shape that matters most, the
fail-open the base branch's `agentRuntime == "substrate"` literal used to
close.

**Fix:** a profile match with no `Attach` of its own now falls through to
the broker-wide `Capabilities.Attach`, and that answer is final:

```go
if profile != "" {
    for _, p := range broker.Profiles {
        if p.Name != profile {
            continue
        }
        if p.Attach != nil {
            return *p.Attach
        }
        break // found the profile, but it said nothing; fall through
    }
}
if broker.Capabilities != nil {
    return broker.Capabilities.Attach
}
return true
```

This is consistent with the missing-⇒-supported default, not a reversal of
it: that default exists for backward compatibility with an older broker,
and an older broker always reported `Capabilities.Attach: true` (the
previous hardcoded value), so it keeps working unchanged. An *explicit*
broker-wide `false` is real information — a live default runtime that
opted out — not an absence.

**Accepted cost, documented on `attachSupportedByBroker` and in
`substrate-runtime.md`:** a non-default-type profile on a broker whose
default runtime does not support attach is now refused before dialing,
even if that specific profile's own runtime would have been fine with it —
there is no live instance to ask that profile directly at registration
time (see the producers above), and treating its silence as "supported" is
exactly the fail-open just closed. The comment describing this is
deliberately generic ("a non-default-type profile on a broker whose default
runtime does not support attach"), not substrate-specific, to keep
`cmd/attach.go` substrate-literal-free.

The regression is pinned by
`TestAttachViaHub_StandaloneBrokerNilProfileAttach_BrokerWideFalse_Refuses`
(`cmd/attach_capability_gaps_test.go`), driven through the real
`buildBrokerProfiles` producer (not a hand-built profile literal) — it
actually calls the function and asserts the resulting profile's `Attach` is
`nil` before feeding it, plus a broker-wide `Capabilities.Attach: false`
(what a real heartbeat would report), through `attachViaHub`.

## Bisectability

Review found the incremental development history had two real problems,
independent of the design above being correct at the tip:

1. **A broken intermediate build.** One commit deleted the first-iteration
   by-type-name lookup mechanism (`AttachSupportedForType`, see "Rejected
   designs") while `pty_handlers.go` still called it — a later commit fixed
   the caller. `git bisect` on that span would land on a commit that
   doesn't compile.
2. **An ordering hazard.** `SubstrateRuntime.SupportsAttach()=false` must
   land at or before the commit that removes the old
   `runtimeCmd == "substrate"` guards from `LocalPTYSession.Run` /
   `StreamPTYHandler.Run`, or there is a window where a real substrate
   instance — guards gone, capability not yet implemented — falls through
   to a docker-style exec attempt.

The branch was squashed (`git reset --soft` to the base, recommitted in a
clean, deliberate order; not an interactive rebase) into the sequence in
the commit table below, each verified to build independently — via
`git worktree add` at every commit SHA and running both
`go build -buildvcs=false ./...` and `go vet -tags no_sqlite ./...` (the
`make ci` lint step's exact invocation) in each, not just the tip. The two
QA-authored test-only patches applied along the way
(`test-r2-gap-tests.patch`, `test-r2-round3-gap-tests.patch`) are each their
own commit, applied as-is except for one assertion in the second that the
C1 fix's corrected fallthrough required updating (see its commit message).

Assembling the squash surfaced two more instances of the same
cross-commit-dependency class as problem 1 above, both caught by the
per-commit verification rather than left for the next reviewer to bisect
into:

- `Server.SwapRuntime`'s one-line `hb.SetDefaultRuntime(rt)` call was
  initially grouped with the PTY-gating commit (it lives in the same
  function, `server.go`, as the `AgentLookupResult.Runtime` plumbing that
  commit owns) — but `SetDefaultRuntime` itself is defined in the
  heartbeat commit, later in the sequence. Fixed by moving that single line
  into the heartbeat commit, leaving the PTY-gating commit's `server.go`
  diff to only the lookup plumbing.
- A new test in `info_profiles_test.go` (the per-profile resolver commit)
  used `attachCapableTestRuntime`, a fake defined in `pty_handlers_test.go`
  (the PTY-gating commit, originally sequenced *after* the resolver
  commit). Fixed by swapping the two commits' order — nothing in
  PTY-gating depends on the resolver, so the swap was free.
- A new file, `cmd/attach_capability_round3_gaps_test.go`, used
  `newTestStore` (defined in the sqlite-gated `cmd/server_test.go`) without
  itself carrying the `//go:build !no_sqlite` constraint. Invisible to a
  plain `go build`, but `make ci`'s lint step runs `go vet -tags
  no_sqlite ./...`, which excludes `server_test.go` and left `newTestStore`
  undefined — caught by `make ci` itself, not the per-commit build loop
  (which used plain `go build`, without the tag, until this was added
  to the loop too). Fixed by adding the same build tag.

Each fix required a full soft-reset-and-recommit pass rather than a
targeted amend, since a commit's position in the sequence, not just its
content, was what needed to change. After the third pass, the per-commit
loop was run with both `go build` and `go vet -tags no_sqlite` at every
SHA, which is what caught the third issue before it could recur.

## Rejected designs, for anyone revisiting this

- **A by-type-name lookup map** (`AttachSupportedForType`, keyed by runtime
  type string to a zero-value probe instance) was the first design for
  callers with no live `Runtime` instance in hand — the CLI, and initially
  the broker's own PTY handlers before they were given a live-instance
  path. Rejected: a compiled table goes stale the moment a runtime other
  than substrate ever opts out, without a CLI rebuild, and it duplicated
  the broker's own live-instance answer instead of asking it. Deleted
  entirely in favor of the live-instance resolver (broker side) and the
  broker-metadata read (CLI side).
- **Type-string equality** (`rtType != defaultRuntimeType → true`) in
  `buildInfoProfiles`'s original per-profile logic: a profile of a
  different type than the default was unconditionally advertised as
  attach-capable regardless of what that type actually was. Replaced by
  `resolveLiveRuntimeInstance`.
- **CLI nil-profile ⇒ always supported** (no broker-wide fallthrough): the
  design until C1 was found. See above.
- **A new WebSocket/control-channel close code for "attach unsupported"**
  (`4501`, mirroring the HTTP 501): considered for `handlePTYStream`'s
  pre-check, rejected as disproportionate for this unit — it would touch
  `pkg/wsprotocol`, its `web/` TypeScript mirror, and
  `testdata/pty_close_codes.json`, a three-file wire contract change. The
  pre-check reuses the existing `ClosePTYUpstreamUnavailable` /
  `CloseReasonSessionNotReady` pair instead, which matches (not improves)
  how `classifyAttachEnd` already classified this shape of failure before
  the pre-check existed. Tracked as deferred item (b).

## Deferred (explicitly out of scope, tracked separately)

- **(a) Heartbeat refreshing `Profiles[].Attach`.** Heartbeats refresh
  `Capabilities` on every send but `Profiles` only at
  registration/join. A long-running standalone broker whose runtime's
  attach support changes (rare, but possible via `SwapRuntime`) won't have
  its per-profile `Attach` re-synced without a re-registration. The
  broker-wide `Capabilities.Attach` *does* refresh every heartbeat (this
  unit's own heartbeat fix), which is what C1's fallthrough leans on for
  exactly this reason.
- **(b) A dedicated terminal close code for "attach unsupported"** on the
  control-channel path (currently 4503, a retry code) — see "Rejected
  designs" above.
- **(c) True per-profile resolution for a runtime this broker process
  hasn't lazily built yet.** `resolveLiveRuntimeInstance` reports unknown
  for any auxiliary-type profile no request has exercised — a substrate
  profile that is *not* a broker's default reports supported until the
  first agent actually runs on it. Closing this needs the R6-option-A
  `DefaultProfile`-style plumbing another unit already scoped and declined
  to build for a related reason; not attempted here.

## Omit-list: where `Attach` stays unknown, and why

| Producer | Default-type profile | Any other profile |
|---|---|---|
| `buildInfoProfiles` (live broker `/info`) | Live (`s.runtime`, always) | Unknown until some prior request causes `resolveManagerForOpts` to build and cache that type — deferred (c). |
| `buildStoreBrokerProfiles` (embedded broker registration) | Live (the `rt` the caller already built) | Unknown, always — no auxiliary-runtime tracking here at all. |
| `buildBrokerProfiles` (`scion broker register`, standalone/remote) | Unknown, always | Unknown, always — no live instance of any kind in this command's flow. |
| Heartbeat (`Capabilities.Attach` only, broker-wide) | Live, always once a connection has started | n/a |

The case that matters most — **substrate as a broker's default runtime** —
is live in every producer and in the heartbeat. The C1 fix means even the
worst case (standalone broker, no live per-profile answer anywhere) now
fails *closed* through the broker-wide fallback, not open.

## I1–I3 (raised in review)

- **I1 — re-assert `HasAttachSupport` at the top of `Run()` as defense in
  depth** (carrying the resolved runtime into the session instead of a type
  string): declined. Both `LocalPTYSession` and `StreamPTYHandler` are only
  ever constructed after their one caller's pre-check has already passed
  (`handleAgentAttach` / `handlePTYStream`), so a second check inside `Run()`
  would be dead code with no caller that could ever trip it — adding it
  would be defense against a caller that doesn't exist, not against a real
  gap.
- **I2 — normalize profile-type aliases through the resolver** (e.g. `k8s`
  vs `kubernetes`): declined for this unit, advisory-only per the review.
  `resolveLiveRuntimeInstance` compares `rtType` by exact string equality,
  same as the rest of `buildInfoProfiles` already does (`isLocalOnlyRuntime`,
  the local-only filter) — introducing alias normalization only for this
  one field, while the surrounding function still compares raw strings,
  would be an inconsistent, partial fix. A real fix belongs at
  `resolveInfoProfileRuntime`/`VersionedSettings.ResolveRuntime`, which
  already owns type resolution, not duplicated into this one capability
  check.
- **I3 — a control-channel fallback-aux test for symmetry**: resolved. The
  round-4 QA patch's `TestHandlePTYStream_AuxRuntimeWithoutAttach_ClosesBeforeExec`
  is table-driven over exactly this: a first-stage aux match and a
  no-project-label-fallback aux match, both through the real
  `Server.LookupAgent`. Confirmed this covers I3; no additional test added.

## Gates

Every run used a from-scratch `env -i`, re-adding only what the Go
toolchain and Makefile targets need — no hand-listed `SCION_*` subset:

```
env -i HOME="$HOME" PATH="$PATH" \
  GOPATH="$(go env GOPATH)" GOMODCACHE="$(go env GOMODCACHE)" \
  GOCACHE=/scion-volumes/gocache \
  GOPROXY="$(go env GOPROXY)" GOSUMDB="$(go env GOSUMDB)" \
  <command>
```

`GOCACHE=/scion-volumes/gocache` throughout, never cleaned.

| Gate | Result |
|---|---|
| `go build -buildvcs=false ./...` | OK |
| `gofmt -l` on every touched file | empty |
| `go test -count=1 ./cmd/... ./pkg/runtimebroker/... ./pkg/store/... ./pkg/runtime/... ./pkg/hubclient/...` | all ok |
| `golangci-lint run --new-from-rev=c1cafb0b7` | 0 issues |
| `make ci` (fmt-check, lint, check-custom, test-fast `./...`, build) | CI passed |
| Every commit built independently (`go build -buildvcs=false ./...` AND `go vet -tags no_sqlite ./...`, via detached worktrees at each SHA) | OK, all 11 |
| PR1-only tree (all 9 non-PR3 commits cherry-picked onto `c1cafb0b7`, PR3 skipped) — grep proof + build + vet | zero matches (all 4 files) + OK + OK |
| Mutation checks: C1's `break`→`return true`; `attachSupportedForProfile`→always `nil`; `handlePTYStream`'s gate→`if false && ...` | all three: named tests red → reverted → green |
| grep proof on final tip: `capabilities.go`, `pty_handlers.go`, `cmd/attach.go` (and `controlchannel.go`, kept clean though not formally required) | zero matches, all four |

`cmd/sciontool/commands`' 18 `/tmp`-ownership test failures reproduce
identically on base `c1cafb0b7` — a pre-existing environment issue, not a
regression from this unit.

## Commits (final, squashed)

| Subject | PR |
|---|---|
| Add an optional attach-capability runtime interface | PR1 |
| Report that substrate does not support interactive attach | **PR3** |
| Carry attach capability on BrokerProfile as a tri-state field | PR1 |
| Resolve per-profile attach from a live instance, not a type string | PR1 |
| Gate PTY attach on the live runtime, both broker entry points | PR1 |
| Report the default runtime's own attach capability on heartbeats | PR1 |
| Read the CLI's attach rejection from the broker's own record | PR1 |
| Feed live runtime instances into the broker registration producers | PR1 |
| Add coverage for attach-capability branches missed by earlier tests (QA patch 1) | PR1 |
| Add coverage for producer and resolver branches missed by earlier tests (QA patch 2) | PR1 |
| Document the attach capability's producers and CLI fallthrough | PR1 (docs) |
| project-log entry | — |

PR1 is every commit above except "Report that substrate does not support
interactive attach", which is PR3 in full. A tree diff of PR1 alone (all
commits above minus the PR3 one) against the upstream base has zero
`substrate` text in `pkg/runtime/capabilities.go`,
`pkg/runtimebroker/pty_handlers.go`, and `cmd/attach.go` — verified by
`git grep -i substrate` on the final tree for all three (and
`controlchannel.go`, kept clean as well though not formally required).

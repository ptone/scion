# Audit update milestone 2 A1 authorization contract

## Scope and design choices

This unit adds only the authorization-decision contract foundation required by
#2379. It does not wire an emitter, change `AuthzService.Decide`, change routine
decision persistence, or modify any production file outside
`pkg/hub/auditevent`.

The contract uses one `BuildAuthorizationDecision` builder. It requires the
existing operation context, generates the event UUID and UTC timestamp on the
server, snapshots optional request/initiator/executor/credential data, requires
principal and target resource, and derives the exact `decision/allow` or
`decision/deny` pair and severity from the typed `Allowed` input. Its payload
requires canonical permission and closed reason code, with optional bounded
purpose and optional boolean cache-hit.

Canonical ownership is deliberately not duplicated:

- authorization actions use `authzop.OperationID` directly;
- the explicit audit action allowlist is pinned to all current
  `authzop.Catalog` operations, with a drift test so a new operation cannot
  silently become an audit action;
- permission values remain owned by `permissions.Registry`; `PermissionName`
  is an audit-boundary type whose closed values are populated from that owner;
- `auditevent.ReasonCode` owns the approved shared v1 audit reason vocabulary,
  because no pre-existing canonical bounded reason type exists.

The catalog gained only the generalization required for this family: a closed
`AllowedActions` list and an explicit resource-kind rule. The original
`access_boundary/create` entry remains literal and its exact schema is still
snapshot-tested. The full catalog has a stable serialized digest snapshot.

## Contract and privacy evidence

Focused tests cover both allow and deny builders; generated UUID/time;
operation-context requirement; all declared operations and authz catalog drift;
phase/outcome closure; required principal/resource/payload leaves; system and
project resource scope rules; action/resource/purpose bounds; canonical
permission and closed reason values; boolean cache-hit typing; undeclared leaf
and undeclared operation rejection; immutable catalog and builder-input aliases;
and concurrent builder/render/capture behavior.

Every new rejected string source (operation, permission, reason, purpose, and
resource kind) has a value-free error canary. Allow and deny records are checked
for JSON equivalence across `Render`, a raw `slog` handler, and the configured
OpenTelemetry handler. A temporary allow-condition inversion made
`TestBuildAuthorizationDecisionAllowAndDeny` fail for both cases, proving the
positive tests detect reversed decision semantics; the mutation was restored.

## Verification

The implementation checkpoint was committed and pushed as `1434c7c3` before
the broader gates, as required by the campaign controls.

- `go test -p 2 ./pkg/hub/auditevent` — PASS.
- `go test -race -p 2 ./pkg/hub/auditevent` — PASS (`ok`, 1.310s).
- `go vet -p 2 ./pkg/hub/auditevent` — PASS.
- `go build -buildvcs=false -p 2 ./pkg/hub/auditevent` — PASS.
- `timeout 10m env GOGC=40 golangci-lint run --new-from-rev=ff62f8cb2ed95877e8e3598049e6408c579ed18a --concurrency=1 ./pkg/hub/auditevent/...`
  — PASS, `0 issues`.
- `gofmt` and `git diff --check` — PASS.

`make ci` and `make ci-full` were not run because the milestone brief prohibits
them. No gate was inconclusive.

## Handoff to #2379

#2379 can construct one event per enforced decision with
`BuildAuthorizationDecision(ctx, AuthorizationDecisionInput{...})` and dispatch
the returned immutable envelope through the existing configured `SlogSink`.
It should map the existing human-readable decision explanation to a bounded
`ReasonCode`, preserve the request's canonical `authzop.OperationID` and
permission ID, pass credential decoration through `NewCredentialRef`, and keep
the #2392 `audit_emit_dispatch` timing around synchronous sink dispatch. This
unit intentionally leaves all emitter and persistence changes to #2379.

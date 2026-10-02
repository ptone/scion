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

## Review round 1 dispositions

- **R1:** Closed. The catalog now owns 97 explicit
  `OperationID -> BasePermission` pairs and outcome-specific reason sets.
  Validation rejects mismatched operation/permission and outcome/reason facts.
- **R2:** Closed. Builders trim purpose exactly as issuance does, validation
  delegates purpose safety to `credentialmeta.ValidateIssuance`, and resource
  kinds use a strict lowercase code validator. Invalid UTF-8, length, control,
  format, bearer, and `scion_pat_` canaries are value-free.
- **R3:** Closed as an A1 contract. `AuthorizationProducerRequestContract`
  pins the required future `AuthzRequest.OperationID authzop.OperationID` field;
  `AuthorizationProducerDecisionContract` pins the future
  `Decision.AuditReason auditevent.ReasonCode` field. The catalog mapping is:
  allow→allowed, inherited→inherited, cache-hit→allowed,
  permission-denied→permission_missing, policy-denied→policy_denied,
  unauthenticated→not_authenticated, unauthorized→not_authorized,
  invalid-request→invalid_request, dependency-unavailable→dependency_unavailable,
  check-unavailable→check_unavailable, and closed fallback→unspecified (deny).
  Not-found, conflict, rate-limited, attachment-rejected, and check-disabled are
  explicitly marked unavailable from the current authorization producer.
- **R4:** Closed. Allow/deny parity fixtures now go through the builder with
  request, initiator, executor, credential, purpose, and cache-hit populated;
  both boolean values remain booleans through render, raw slog, and OTel JSON.
  This coverage found and fixed the missing portable bool slog representation.

#2379 must begin with an independently reviewed producer-plumbing checkpoint:
add the two pinned fields, populate operation only from the canonical
route/operation owner, assign `AuditReason` structurally at every decision exit,
and exhaustively test the mapping above. It must never infer operation from
resource/action/permission or parse `Decision.Reason`. Missing, unknown, or
mismatched fields make audit construction fail without changing authorization.
Only after that checkpoint is approved may emitter cutover begin; #2392 timing
remains unchanged until cutover.

Round-1 fix verification: `go test -p 2 ./pkg/hub/auditevent`,
`go test -race -p 2 ./pkg/hub/auditevent`, scoped vet and build, gofmt, and
diff checks passed. The bounded single-concurrency golangci-lint run passed with
`0 issues`. No gate was inconclusive; `make ci`/`make ci-full` remained
prohibited.

## Review round 2 test-evidence closure

Production behavior remained unchanged. Commit `8c099c63` adds only exhaustive
contract tests in `pkg/hub/auditevent/authorization_test.go`:

- `TestAuthorizationOperationPermissionMatrix` derives its cases from the
  catalog and the canonical permission registry. It exercised all 97 actions
  against all 131 permissions (12,707 combinations): the 97 exact mappings
  succeeded and all 12,610 non-mappings failed validation.
- `TestAuthorizationOutcomeReasonMatrix` derives expected admission solely
  from catalog `OutcomeReasons`. It exercised the complete two-outcome by
  15-reason matrix (30 combinations): all 17 catalog-admitted combinations
  succeeded and all 13 excluded combinations failed validation.
- `TestAuthorizationCatalogSnapshotIsImmutable` mutates returned
  `ActionPermissions`, nested `OutcomeReasons[].AllowedReasons`, and
  `ProducerReasonMappings` data, then verifies a fresh `Catalog()` snapshot is
  unchanged. Existing action and required-payload snapshot checks remain.

The required post-push bounded checks passed:

- `go test -count=1 -p 2 ./pkg/hub/auditevent` — PASS (`ok`, 0.387s).
- `go test -count=1 -race -p 2 ./pkg/hub/auditevent` — PASS (`ok`, 2.319s).
- `gofmt -l pkg/hub/auditevent/*_test.go` — PASS (no output).
- `git diff --check d848ecfe2f27949865a292df3ee8f6cbdcebfd3b..HEAD`
  — PASS (no output).
- The scoped diff from `d848ecfe2f27949865a292df3ee8f6cbdcebfd3b`
  through the test checkpoint contains only
  `pkg/hub/auditevent/authorization_test.go`; the corresponding non-test
  `pkg/hub/auditevent/*.go` diff is empty.

Per the closure brief, no broad build, lint, `make ci`, or `make ci-full` gate
was run. No required gate failed or was inconclusive.

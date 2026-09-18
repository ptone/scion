# #1620 deterministic integration harness foundation

Date: 2026-09-18

## Scope and base

- Branch: `scion/dev-ge-integration-harness`
- Required main ancestry: `370a0268ac1da762f04c76c2193e68cc7d11d6c5`
- Accepted transport base: `e9a4c8858cb4ffb88678f51d56a75043e9bf90d9`
- Excluded dependencies: no commits were copied or merged from
  `scion/dev-ge-auth` or `scion/dev-a2a-taskstore`.

This milestone adds only a bounded test composition under
`extras/scion-a2a-bridge/integration`. No production binary, route, flag,
validator override, test credential, environment option, or public production
configuration was added.

## Delivered foundation

1. A `_test.go`-only subprocess topology starts independent helpers, captures
   PID/port/readiness, allocates the lowest available port from a caller-selected
   loopback range, propagates cancellation, reaps children, sanitizes subprocess
   output, and records only approved structured observation fields.
2. A test-only HTTP reverse proxy alternates new sequential requests between two
   backend processes. A real SSE response stays attached to the backend selected
   for that request.
3. Redaction removes bearer values plus hex, standard-base64, and raw-URL-base64
   SHA-256 encodings of registered fixture credentials. The process test emits a
   runtime-only synthetic bearer and digest and proves neither reaches captured
   output.
4. Synthetic, source-annotated identity, protocol, and stable-agent fixtures cover
   the approved users, credential outcomes, service principals, A2A v1 categories,
   Gemini Enterprise v0.3 categories, task/context identifiers, cancellation, and
   delayed cross-replica events.
5. The PostgreSQL allocator validates explicit unique run IDs, derives bounded
   database/schema names, creates and drops an isolated schema when
   `TEST_DATABASE_URL` is present, and runs teardown hooks in reverse order. It
   makes no claim that Hub identity persistence shares the bridge database.
6. `testdata/acceptance_layers.json` maps all eight future deterministic test
   layers to dependencies and honest foundation/blocking status; every `passing`
   field remains `false`.

## Verification

- `go test -count=1 ./integration` (nested bridge module): pass.
- `go test -race -count=1 ./integration` (nested bridge module): pass.
- `go vet ./integration` (nested bridge module): pass.
- `go test -count=1 ./...` (nested bridge module): pass.
- `go build -tags no_embed_web ./cmd/scion-a2a-bridge` (nested bridge module): pass.
- `go test -count=1 ./pkg/plugin/grpcbroker` (root accepted transport package): pass.
- `go build -buildvcs=false ./...` (root module): pass.
- `git diff --check`: pass.
- PostgreSQL socket execution: skipped because `TEST_DATABASE_URL` was absent;
  deterministic naming, duplicate rejection, and cleanup order ran locally.
- `make ci` (root): did not reach tests because `fmt-check` reports six existing
  Hub/messaging files. `git diff` confirms all six are unchanged from the required
  transport base. They were not reformatted in this scoped branch.
- `go test -count=1 ./...` (root module): completed with base-state failures in
  unrelated command, config/project-discovery, Hub, runtime broker, and utility
  tests. The harness is inside a nested module and adds no root package; the root
  build and accepted transport package pass. See the external completion report
  for the recorded failure groups.

## Eight-layer acceptance status

| Future layer | Foundation status | Remaining dependency |
|---|---|---|
| `TestGEEnvelopeCompatibility` | external-live-only | Reviewed auth/production mux for deterministic replay; authorized live GE capture for the actual envelope |
| `TestTwoReplicaUserLifecycle` | blocked-on-taskstore | Reviewed auth and shared PostgreSQL taskstore |
| `TestColdReplicaAndRotation` | blocked-on-auth | Reviewed exchange/cache implementation |
| `TestCrossReplicaStreamCursor` | blocked-on-taskstore | Reviewed durable event/cursor implementation |
| `TestCrashLeaseBoundary` | blocked-on-taskstore | Reviewed lease/reaping implementation |
| `TestControlPlanePrincipalIsolation` | foundation-ready | Assemble accepted transport factory/server in the combined suite; not yet marked passing |
| `TestCombinedStartupMatrix` | blocked-on-auth | Reviewed auth and taskstore startup constraints |
| `TestCredentialRedaction` | foundation-ready | Re-run against combined application logs; not yet marked passing |

## Live-only residuals

Actual Gemini Enterprise envelope capture, OAuth client registration, real Google
credential behavior, Cloud Run multi-instance ingress/IAM, Kubernetes deployment,
rollback/cleanup, and live latency remain explicitly unverified. This work made no
cloud calls and mutated no live resource.

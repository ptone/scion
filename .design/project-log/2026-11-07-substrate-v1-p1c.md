# Substrate v1 API migration, Phase 1c (router mTLS, router_endpoint, CTB fallback)

**Date:** 2026-11-07
**Branch:** scion/substrate-v1-p1c (a new branch at Phase 1a's tip `abf0e167`)
**Scope:** design v0.5.5 §3.2 (ClusterTrustBundle v1 then v1beta1), §3.6
(router transport) and AC 11.

## What was built

- **One ClusterTrustBundle lookup** (`pkg/runtime/substrate/trustbundle.go`),
  shared by the ateapi dialer and the router client:
  - It reads `certificates.k8s.io/v1` first. The v1 leg is a raw GET through
    the discovery REST client, kept in `getClusterTrustBundleV1` so it can
    move to a typed client later.
  - It asks `v1beta1` for the same name only when v1 answers 404 (the
    group-version is not served, or the object is missing).
  - A v1 200 is final: an empty, unparseable, kind- or name-mismatched
    answer, or one with no `spec.trustBundle`, fails closed. So do
    401/403, other statuses, transport errors, a missing REST client and
    any v1beta1 error, 404 included.
  - Unknown fields in the v1 object are ignored on purpose. The fields the
    lookup relies on are checked strictly.
  - `rootCAPool` takes exactly one source (the bundle, else `ca_file`). It
    refuses when neither is set and never uses the system roots. Errors
    name the bundle, never its contents.
- **mTLS router client** (`pkg/runtime/substrate/router.go`):
  - `NewMTLSRouterClient(ctx, k8s, RouterTLSConfig{Endpoint, CertFile,
    KeyFile, ClusterTrustBundle, CAFile, ServerName})`.
  - TLS: MinVersion TLS 1.2, never InsecureSkipVerify, RootCAs from the
    shared lookup or `ca_file`, ServerName defaulting to the endpoint host.
  - The client certificate is loaded at construction and re-read as a pair
    on every handshake (`GetClientCertificate`), with one immediate re-read
    and then a hard failure. Nothing is cached, so a mismatched rotation
    fails rather than reusing the old pair.
  - No environment proxy, and redirects are refused
    (`ErrRouterRedirectRefused`).
  - Empty certificate or key paths refuse with
    `ErrRouterClientCertNotConfigured`.
  - `NewRouterClient(endpoint)` keeps its signature as a plaintext,
    test-only constructor. A go/parser guard test fails if any non-test
    file under `pkg/` or `cmd/` refers to it or builds a `RouterClient`
    composite literal outside the two constructors.
  - `RouterClient.Do` refuses a nil HTTP client instead of falling back to
    `http.DefaultClient`. A RouterClient not built by a constructor is
    refused; an mTLS client refuses non-https and every redirect.
- **router_endpoint validation:** `substrate.ValidateRouterEndpoint`
  accepts only `https://<host>:443`, with the port written out and no
  userinfo, path, query or fragment. Refusals name the rule, not the value.
- **Wiring** (`pkg/runtime/substrate_runtime.go`,
  `newSubstrateRuntimeFromConfig`):
  - The endpoint check is the first statement, before the Kubernetes client
    (now behind the `newSubstrateK8sClient` seam) and the dial, and is
    tagged `ErrSubstrateProfileInvalid`.
  - It then builds the router with `NewMTLSRouterClient` and the dialer's CA
    source.
  - No certificate paths are configured in 1c, so production construction
    refuses with "router client certificate not configured" (tagged invalid)
    until Phase 1d plumbs the paths.

## Decisions

- A new mTLS constructor; `NewRouterClient` stays test-only, enforced by a
  guard test.
- Production fails closed until the certificate paths are plumbed.
- The v1 leg is a raw REST GET, with no go.mod change: k8s.io v0.35.0 has no
  typed certificates/v1 ClusterTrustBundle type or client.
- The `router_endpoint` check lives in the production builder, so test
  builder seams keep their `http://` fixtures. An explicit `:443` is
  required.
- The router client refuses redirects, and its transport uses no
  environment proxy.

## Tests

- `trustbundle_test.go`: the fallback table through a real clientset and an
  httptest API stub (200/404/403/401/500, plain-text 404, hit counts on
  both legs), v1beta1 through the fake clientset, and v1 outcomes that never
  reach v1beta1.
- `router_tls_test.go`: real handshakes against `httptest.NewUnstartedServer`
  with client auth: wrong CA, wrong ServerName, rotation re-read, config
  invariants, CTB source, construction refusals, redirect refused. The
  mismatched and unreadable rotation tests run against a router that only
  verifies a certificate if one is given, and assert the client-side
  certificate load error. The TLS 1.1 test asserts the protocol-version
  failure. Clients no constructor built (a literal, a zero value or
  `new(RouterClient)`, with an http or https endpoint) are refused, an mTLS
  client whose endpoint is changed to http is refused, and the test-only
  constructor still serves httptest fixtures.
- `router_endpoint_test.go`: refusal table with a no-echo marker check.
- `router_plaintext_guard_test.go`: guard test plus positive and negative
  controls, for both references to `NewRouterClient` and `RouterClient`
  composite literals.
- `pkg/runtime/substrate_router_wiring_test.go`: the refusal order and the
  error classes.

## Verification

All checks ran offline from a private module cache.

- Dependencies: `go mod download` from proxy.golang.org, then
  `go mod verify`.
- Tests: one `go test` over `./pkg/runtime/substrate/...`,
  `./pkg/runtime`, `./pkg/agent` and `./pkg/runtimebroker` at the final
  tip passes, including the 29 named substrate tests (covering every AC 11
  item), the 4 wiring tests and the operator-only profile test.
- Lint: `golangci-lint --new-from-rev=de18c73c` over the seven directories
  touched since de18c73c, Phase 1a's base, reports 0 issues at the final tip. An
  earlier run flagged two unchecked `resp.Body.Close` calls in test code,
  fixed before the final tip.
- Build: `go build -buildvcs=false ./...` is green.
- gofmt: 0 unformatted out of the 38 Go files changed since de18c73c.

Two test environment notes:

- Tests that assert default file modes depend on the process umask.
- A long `TMPDIR` can push unix socket paths under `t.TempDir()` past the
  platform limit (`TestHomeBootstrapFiles_SkipsSymlinkFifoSocketAndCountsThem`).

## Notes for later phases

- 1d supplies `CertFile`/`KeyFile` (the podCertificate projected volume) and,
  if needed, a router-specific CA source. Until then a real substrate
  runtime build refuses at construction.
- If client-go gains a typed certificates/v1 ClusterTrustBundle client, swap
  `getClusterTrustBundleV1` for it.

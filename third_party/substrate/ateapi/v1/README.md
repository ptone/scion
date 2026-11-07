# ateapi v1 (vendored)

Generated Go client stubs for Substrate's `substrate.ateapi.v1.Control` gRPC
service (github.com/agent-substrate/substrate, `api/ateapi/v1/`).

Go package name: `ateapi`. Import path:
`github.com/GoogleCloudPlatform/scion/third_party/substrate/ateapi/v1`.

## Why vendored instead of `go get`

The v1 API lives in substrate's root module (`go.ate.dev/substrate`; there is
no separate `go.mod` under `api/`). Importing it pulls substrate's full
`go.mod`, which requires a newer Go toolchain than scion targets and forces
major-version upgrades of `k8s.io/api`, `k8s.io/apimachinery`,
`k8s.io/client-go` and other shared transitive dependencies scion already
pins.

The generated package itself depends only on `google.golang.org/protobuf`
(including `types/descriptorpb` and `types/known/timestamppb`) and
`google.golang.org/grpc`, both already in scion's `go.mod` at compatible
versions.

## Provenance

- Repository: https://github.com/agent-substrate/substrate
- Tag: `v1.0.0` (tag object `fd67f6204a7c287f658d080f6196ba1638744a10`)
- Commit: `b34fbee742906494b5eb4f1f36987176bf8770a4`
- Source path: `api/ateapi/v1/`
- License: Apache License 2.0 (`LICENSE`, copied from the source repository)
- Generators (read from the generated file headers):
  - `ateapi.pb.go`: `protoc-gen-go v1.36.11-devel`, `protoc v4.25.3`
  - `ateapi_grpc.pb.go`: `protoc-gen-go-grpc v1.6.2`, `protoc v4.25.3`

## Vendored files

The files below are byte-identical copies of the upstream files at the commit
above. `vendor_hash_test.go` recomputes each sha256 and compares it with the
values hard-coded in the test, and checks that this table records exactly
those values, so any edit to a vendored file, to this table or to the test's
literals alone fails the tests.

| File | sha256 |
|---|---|
| `ateapi.proto` | `b5bb348f04b8f8d9f24c4c880e1f4176771e1556cfd0cff95a97ca8bee079722` |
| `ateapi.pb.go` | `8f5f95d75568cfc485cafd6e1813a897233e3ca8b01258a1b55fa5d30e4d7a91` |
| `ateapi_grpc.pb.go` | `ac1d3804745755ddd1cd39a8093a43a5b125de1a21d73cc825387519e682b484` |
| `source.go` | `f63529efec392b64a1adb99bf08aa0cd5d431c815db2d420f2329cd2e1f2e536` |
| `LICENSE` | `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30` |

## Refresh procedure

The files are never regenerated or hand-edited in this repository.

1. Copy `ateapi.proto`, `ateapi.pb.go`, `ateapi_grpc.pb.go` and `source.go`
   verbatim from `api/ateapi/v1/`, and `LICENSE` from the source repository,
   at the new release tag on substrate's release-1.0 patch line.
2. Update the tag, tag object, commit and generator versions in
   "Provenance", and replace each sha256 in "Vendored files" and in
   `vendoredHashes` in `vendor_hash_test.go` with the sha256 of the newly
   copied file.
3. Run this package's tests (`vendor_hash_test.go`) and the substrate runtime
   tests (`pkg/runtime/...`, `pkg/runtimebroker/...`).

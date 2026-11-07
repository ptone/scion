# Substrate v1 API migration, Phase 1a (bindings and v1 call sites)

**Date:** 2026-11-07
**Branch:** scion/substrate-v1-p1
**Scope:** Phase 1a of the substrate v1 API migration: swap the vendored
pre-1.0 `ateapi` stubs for Substrate v1.0.0 and move every broker call site,
template, egress translation and test fake to the v1 shapes.

## What was built

- **Bindings.** `third_party/substrate/ateapi/v1/` holds `ateapi.proto`,
  `ateapi.pb.go`, `ateapi_grpc.pb.go`, `source.go` and `LICENSE`, copied
  verbatim from agent-substrate/substrate v1.0.0 (commit `b34fbee7`,
  `api/ateapi/v1/`). `README.md` records provenance, generator versions, each
  file's sha256 and the refresh procedure. `vendor_hash_test.go` hard-codes the
  sha256 values and checks both the files and the README table against them.
  `third_party/ateapipb/` is gone; there is no alias.
- **Lifecycle** (`pkg/runtime/substrate_runtime.go`):
  - DeleteActor sends `DeleteOptions{Uid}` from the pre-delete GetActor, and
    Run's failure cleanup sends the uid CreateActor returned; no options when
    the uid is unknown. A FailedPrecondition on a delete that sent a uid is
    `ErrSubstrateIncarnationChanged` and leaves in-memory state alone (the
    code is an assumption the live Phase 1d observation confirms).
  - Get/DeleteActorEgressPolicy name the policy `default`.
  - List keeps the empty atespace (needs the global viewer role) and
    RecordlessActors its one atespace, both paging with `ListOptions`.
  - The phase map drops PAUSING/PAUSED.
- **Templates** (`pkg/runtime/substrate_template.go`):
  - `resolveSubstrateResources` resolves cpu and memory independently: the
    agent's limit, else the built-in substrate default (cpu `2`, memory
    `4Gi`). Phase 2 adds `default_resources` in `resolveSubstrateQuantity`.
    Limits are set on the template only; the container-level limits are gone.
    `buildActorTemplate` refuses a template without both.
  - `sandbox_config` and `snapshot_config` are not sent. A FailedPrecondition
    from CreateActorTemplate names the ate-api-server `--snapshot-location`.
  - `sandbox_class`, `sandbox_config_name` and `snapshot_storage` refuse the
    Run with the key and its v1 replacement (`checkSubstrateRemovedKeys`).
  - The template name hashes an explicit field list behind `v1|`
    (`substrateTemplateHashInput`).
- **Egress** (`pkg/runtime/substrate_egress.go`, `pkg/runtime/substrate/egress.go`):
  - `NormalizeEgressAllowEntry` accepts `host`, `host:port`, `https://host[:port]`
    and `http://host[:port]` (wildcards too); the host part keeps every
    existing check.
  - Ports and http come only from `cfg.TrustedHubEndpoint` and egress_allow.
    Every other derived host is TLS passthrough on 443; a tenant-derived host
    on another port or scheme that egress_allow does not list on that port is
    dropped with a warning naming the entry to add.
  - One host and port on two protocols is a config refusal. Rules are grouped
    per protocol and exact port, sorted, deterministic.
- **Fakes.** `fakeControlClient` embeds `ateapi.ControlClient` and overrides
  only the methods the runtime calls.

## Known gaps

- In v1 a template's temporary golden actor lives in the template's (project)
  atespace, so it can count as record-less or be listed during a build. No
  filter yet; planned after the Phase 1d observation.
- With `egress_trust_bundle` set, rules are still TLS passthrough; https
  (MITM) rules are Phase 3.

## Verification

See the Phase 1a developer report for the compile windows, commands and
results.

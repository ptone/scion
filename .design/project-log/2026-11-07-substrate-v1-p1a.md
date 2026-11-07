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
  - DeleteActorEgressPolicy and the created policy name it `default`.
  - List keeps the empty atespace (needs the global viewer role) and
    RecordlessActors its one atespace, both paging with `ListOptions`.
  - The phase map drops PAUSING/PAUSED.
- **Templates** (`pkg/runtime/substrate_template.go`):
  - `resolveSubstrateResources` resolves cpu and memory independently: the
    agent's limit, else the built-in substrate default (cpu `2`, memory
    `4Gi`). Phase 2 adds `default_resources` in `resolveSubstrateQuantity`.
    Limits are set on the template only; the container-level limits are gone.
    `buildActorTemplate` refuses a template without both. cpu is capped at
    v1's 999999 millicores and memory at the int64 range, each refused with
    an error naming the field. The range is checked exactly from the
    quantity string (number times suffix), because `resource.ParseQuantity`
    clamps values beyond the int64 range.
  - `sandbox_config` and `snapshot_config` are not sent. A FailedPrecondition
    from CreateActorTemplate names the ate-api-server `--snapshot-location`.
  - `sandbox_class`, `sandbox_config_name` and `snapshot_storage` refuse the
    Run with the key and its v1 replacement (`checkSubstrateRemovedKeys`).
  - The template name hashes an explicit field list behind `v1|`
    (`substrateTemplateHashInput`).
- **Egress** (`pkg/runtime/substrate_egress.go`, `pkg/runtime/substrate/egress.go`;
  design §3.5 and addenda A4):
  - `NormalizeEgressAllowEntry` is the single validator for egress_allow. It
    returns `{Scheme, Host, Port, Wildcard}` and accepts `host`, `host:port`,
    `https://host[:port]` and `http://host[:port]`, wildcards included. The
    scheme comes only from a literal `http://`/`https://` prefix; userinfo
    (any `@`), interior whitespace, paths, queries and fragments are
    refused, at most one trailing dot is stripped, and the host part passes
    every existing host check (no IPs, CIDRs, catch-alls or in-cluster
    names).
  - Three source classes. OPERATOR-TRUSTED (`cfg.TrustedHubEndpoint` and
    egress_allow) emit rules, including http and ports other than 443; the
    same host and port under two protocols among them is a config refusal
    naming both sources. BINARY-FIXED (the compiled model hosts and the
    Vertex AI host) emit tls_passthrough on 443 and are skipped with a
    warning when an operator rule uses another protocol there. TENANT-DERIVED
    (git clone URL, OTEL endpoints, `ANTHROPIC_VERTEX_BASE_URL`) never emit a
    rule: covered by an emitted rule on the same host, port and protocol,
    nothing happens; otherwise the host is dropped with a warning naming the
    egress_allow entry to add.
  - Tenant values are reduced to scheme, host and port before any parsing or
    logging. Only a leading `http://`/`https://` sets the scheme; any other
    `://`, or an `@` after the first `/`, `?` or `#`, makes the value
    unparseable: no rule, and a warning naming only the source setting.
  - Vertex region: empty or unset and `global` map to
    `aiplatform.googleapis.com`; one lowercase DNS label maps to
    `<label>-aiplatform.googleapis.com`; anything else refuses the Run with
    an error that names `CLOUD_ML_REGION` and never its value. Egress targets
    are resolved before the first control-plane call, so a refusal creates
    nothing.
  - The trusted hub endpoint is parsed by operator rules: a leading
    `http://`/`https://` in any letter case, the host lowercased and limited
    to plain DNS labels (no `*`, no catch-all spelling, no trailing dot, no
    other characters; in-cluster single-label names allowed). A bracketed
    authority must hold an IP literal (`https://[name]` refuses), and an IP
    literal gets its own refusal. Anything else refuses the Run with an
    error naming the setting's three sources, never its value. Stripping a
    trailing dot, IDNA normalisation (SEC-5) and label-length bounds
    (SEC-R4-2(a)) are deliberate Phase 1b deferrals (decisions.md).
  - egress_allow refusals never echo the entry or any part of it; they name
    the problem class, and the list check adds "entry N".
  - Rules are grouped per protocol and exact port, sorted and deterministic.
- **Fakes.** `fakeControlClient` embeds `ateapi.ControlClient` and overrides
  only the methods the runtime calls.

## Known gaps

- In v1 a template's temporary golden actor lives in the template's (project)
  atespace, so it can count as record-less or be listed during a build. No
  filter yet; planned after the Phase 1d observation.
- `egress_trust_bundle` refuses the Run in Phase 1 and the template projects
  no trust bundle; projection and https (MITM) rules return together in
  Phase 3 (design addenda A3).

## Verification

See the Phase 1a developer report for the compile windows, commands and
results.

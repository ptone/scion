# Substrate Settings Schema: egress_trust_bundle Drift

**Branch:** scion/substrate-schema

## Problem

`pkg/config/schemas/settings-v1.schema.json`'s `substrate` object
(`$defs.runtimeConfig.properties.substrate`) sets `additionalProperties:
false` but had no `egress_trust_bundle` property, while `V1SubstrateConfig`
(`pkg/config/settings_v1.go`) has an `EgressTrustBundle` field, validated by
`ValidateEgressTrustBundle` to be either empty or exactly
`"egress-mitm.ate.dev"`. A settings file that set
`runtimes.<name>.substrate.egress_trust_bundle` to that one documented,
supported value therefore failed schema validation: both `scion config
validate` (`cmd/config.go` -> `config.ValidateSettings`) and the save/migrate
validation path in `settings_v1.go` rejected it as an unknown key.

## Why it slipped

Settings are loaded without schema validation at runtime — `NewSubstrateRuntime`
and `V1SubstrateConfig.Validate` run their own checks (including
`ValidateEgressTrustBundle`) directly against the loaded struct, not through
the JSON Schema. The schema is only consulted by the separate, opt-in `scion
config validate` path and by save/migrate. So a real settings file using
`egress_trust_bundle` would load and run correctly, and only fail if a user
(or CI) happened to run schema validation against it.

## Fix

- Added `egress_trust_bundle` to the substrate object in
  `settings-v1.schema.json`, with `enum: ["", "egress-mitm.ate.dev"]` matching
  `ValidateEgressTrustBundle`'s allowlist, and a description covering the
  sdsmint-gateway requirement and the plain-install failure mode.
- Audited the rest of the substrate object against `V1SubstrateConfig`
  field-by-field (names, types, enums, defaults). Found and fixed two
  description inaccuracies: `ca_file` and `cluster_trust_bundle` (both in the
  schema and in the Go struct's doc comments) said they verify "ateapi/router"
  — they only verify the ateapi Control gRPC dial. The router client
  (`NewRouterClient`) is plain HTTP with no CA config at all. Also fixed the
  `RouterEndpoint` doc comment's example, which was missing the required URL
  scheme (it's used as `Endpoint+path`, not a bare host:port).
- Added a reflect-based tie test
  (`pkg/config/substrate_schema_tie_test.go`) that loads the embedded schema
  the same way production validation does and asserts, in both directions,
  that `V1SubstrateConfig`'s json tags and the schema's substrate object
  properties name exactly the same fields. This fails automatically if either
  side gains a field the other doesn't have.
- Added schema-validation tests covering: a valid `egress_trust_bundle` value,
  the empty (off) value, an unsupported value, and a bogus key in the
  substrate object — via both `config.ValidateSettings` directly and the
  `scion config validate` command path.
- Known, intentional difference left as-is: an explicit `sandbox_class: ""`
  is rejected by the schema's `enum: ["gvisor","microvm"]` but silently
  accepted as gVisor by Go's `substrateSandboxClass`, kept stricter on
  purpose to fail fast on typos rather than reconciled with Go's behavior.

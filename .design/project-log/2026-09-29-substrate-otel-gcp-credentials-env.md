# Project Log: Substrate Bootstrap Env Now Points at the GCP Telemetry Credential File

**Date:** 2026-09-29
**Component:** `pkg/runtime/substrate_bootstrap.go` (`buildBootstrapEnv`)

PR3. The `scion-telemetry-gcp-credentials` file secret was already delivered
to a substrate container through the ordinary bootstrap-files path, but
nothing in `buildBootstrapEnv` ever pointed `SCION_OTEL_GCP_CREDENTIALS` at
its resolved path, so a substrate agent's GCP telemetry export had no way to
find the credential even though the file itself was sitting on disk.

The fix reuses the exact helper and env-var constant every other runtime
already uses for this (`findGCPTelemetryCredentialPath`,
`telemetryGCPCredentialsEnvVar`, both in `pkg/runtime/common.go`), called
right after the loop that stages `environment`-type `ResolvedSecrets` into
the bootstrap env map: `env[telemetryGCPCredentialsEnvVar]` is set only when
the helper returns a non-empty path, matching the guard idiom already used
at `common.go`'s `prepareContainerSecretEnv` and in
`KubernetesRuntime.buildPod`. The `containerHome` argument passed in is
`util.GetHomeDir(cfg.UnixUsername)` — the same home `buildBootstrapEnv`
already uses earlier in the function for `cfg.Harness.GetEnv`, and the same
one `buildBootstrapFiles` resolves file-secret target paths against, so the
env var and the file it points to are guaranteed to agree on which home
they're relative to.

No new env-var literal was introduced, and the helper itself was not
touched — this is wiring at one call site, not a behavior change to how the
credential path is computed.

**Tests.** Added `pkg/runtime/substrate_bootstrap_env_test.go` with three
presence-only cases against `buildBootstrapEnv`: the env var equals the same
resolved path `findGCPTelemetryCredentialPath` itself returns for a given
secret set; the secret's own content never appears in any value (or key) of
the built env map, checked with a sentinel value rather than a real-looking
credential; and the env var is absent (not empty) from the map entirely when
no such secret is present. These assert on presence and value equality only,
never on the credential's own bytes.

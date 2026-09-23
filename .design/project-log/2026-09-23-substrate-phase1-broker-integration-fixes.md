# Substrate Phase 1 — live-deploy integration fixes to the broker manifest

Fixes from the first real deploy attempt against `substrate-scion-test`,
relayed by sb-em. All confirmed by reading the actual runtimebroker/server
source before making any change, not applied on the reported symptom
alone.

## 1. Broker API bound to loopback, breaking the readiness/liveness probes

**Symptom:** `readinessProbe`/`livenessProbe` on port 9800 failed with
connection-refused.

**Root cause, confirmed in `cmd/server_foreground.go:915-920`:** a
standalone broker in `--hosted` mode (no `--enable-hub`) binds to
`127.0.0.1` by default whenever `--host` isn't explicitly passed — a
safety net so a *fresh* broker with no HMAC keys yet can still be reached
locally by `scion runtime-broker register` before those keys exist. Our
Deployment never passed `--host`, so it hit this default. kubelet's HTTP
probes connect to the **pod IP**, not `127.0.0.1` inside the container's
own network namespace, so a loopback-only listener refuses them outright.

**Fix:** added `--host=0.0.0.0` to the Deployment's `args`. Confirmed by
reading `server_foreground.go`, not assumed, that this is safe and doesn't
enable anything else:
- Setting `--host` makes `cmd.Flags().Changed("host")` true, which skips
  the loopback-default branch (line 918's condition includes
  `!cmd.Flags().Changed("host")`), letting `cfg.RuntimeBroker.Host` fall
  through to its own package default, `"0.0.0.0"`
  (`pkg/config/hub_config.go:624`) — exactly what's needed.
- `--host` also sets `cfg.Hub.Host` (line 894), but every read of
  `cfg.Hub.Host` in `server_foreground.go` is gated behind
  `enableHub`/`enableWeb` (`checkServerPorts`'s `if enableHub &&
  !enableWeb`, the web-host resolution under `if enableWeb`) — both false
  in this deployment (`--enable-hub`/`--enable-web` are never passed), so
  setting it has no observable effect. Confirmed by grepping every
  `cfg.Hub.Host` reference in the file, not just the two lines that touch
  it.

## 2. Is the non-loopback bind actually safe? Yes — HMAC auth is unconditionally strict, and fails closed

Before treating (1) as done, checked what `--host=0.0.0.0` actually exposes
and under what conditions:

- `pkg/runtimebroker/server.go` hardcodes `BrokerAuthEnabled: true,
  BrokerAuthStrictMode: true` in its default `Config` (no flag or settings
  key in this deployment turns strict mode off).
- `validateBrokerAuthStartup` (`server.go:796-819`) **refuses to start the
  HTTP listener at all** on a non-loopback host unless HMAC keys are
  already loaded (`!loopbackOnly && (!strictAuthConfigured || !hasKeys)` →
  error, not a warning). It never falls open to an unauthenticated
  non-loopback listener under any configuration this manifest produces.
- Those keys come from `brokercredentials.NewMultiStore("")` reading the
  default `~/.scion/hub-credentials/` directory (`server.go`'s
  "Load MultiStore credentials" step, run before `Start()`/
  `validateBrokerAuthStartup`) — exactly the path our credentials Secret
  mounts into (`/home/scion/.scion/hub-credentials/<name>.json`, see
  "Secret creation" in the README, unchanged by this fix).

Net effect: binding to `0.0.0.0` is safe given this deployment's Secret
requirement, and — a genuinely useful side effect to document — if that
Secret is ever missing when the pod starts, the broker now **fails
closed** (crashes/restarts) instead of silently serving `:9800`
unauthenticated. Documented both facts as a new "Broker API exposure"
section in the README, including the reasoning for why an ingress
`NetworkPolicy` restricting `:9800` to kubelet-only would be worth doing
eventually but wasn't added now — see that section for the specific
reason (no `NetworkPolicy` peer type expresses "the kubelet" or "the node
that runs this pod"; doing it properly needs the node CIDR, which isn't
cluster-agnostic information this manifest has).

## 3. `active_profile: substrate` — the clean fix for the recurring "docker ps failed" WARN

**Symptom:** every heartbeat logged a `docker ps failed` WARN.

**Root cause, confirmed by tracing the code, not guessed:**
`HeartbeatService` calls `mgr.List(ctx, nil)` every tick on the broker's
*primary* manager (`pkg/runtimebroker/heartbeat.go:230`), separate from the
`auxiliaryManagers` substrate lives in. The primary manager comes from
`runtime.GetRuntime("", "")` (`cmd/server_foreground.go:2347`). With an
empty profile name, `GetRuntime` calls `vs.ResolveRuntime("")`
(`pkg/runtime/factory.go`), which falls back to `vs.ActiveProfile`
(`pkg/config/settings_v1.go`) when the profile name is empty. Our
ConfigMap defined `runtimes.substrate-prod` and `profiles.substrate` —
making substrate *resolvable* — but never set `active_profile`, so the
fallback had nothing to resolve to, `ResolveRuntime` failed, and
`GetRuntime`'s own fallback chain landed on Linux auto-detection, which
picks `"docker"` when no better signal exists. The broker's primary
manager was therefore a Docker runtime shelling out to a `docker` binary
that was never going to be in this image.

`active_profile` (`V1Settings.ActiveProfile`, `koanf:"active_profile"`) is
exactly the settings key sb-em asked me to look for — a clean, pre-existing
top-level field, not something invented for this fix (and the same key
round 1's review, finding 1, named as the missing piece: "the manifest
never makes substrate the broker's default runtime... broker.yaml has no
active_profile"). Added `active_profile: substrate` to the ConfigMap,
sibling to `schema_version`/`server`/`runtimes`/`profiles`. Added a README
verification step (grep the broker's logs for `docker ps failed` — expect
none) and a comment in `broker.yaml` explaining the causal chain above, so
a future reader doesn't need to re-derive it from the source.

## Changes

- `deploy/substrate/broker.yaml`:
  - `active_profile: substrate` added to the ConfigMap's `settings.yaml`.
  - `--host=0.0.0.0` added to the Deployment's `args`.
  - Comments on both explaining the causal chain, not just the fix.
- `deploy/substrate/README.md`:
  - New "Broker API exposure" section (binds to `0.0.0.0` and why that's
    safe; HMAC strict-mode fail-closed behavior; the ingress-NetworkPolicy
    question, answered but not implemented).
  - New verification step: no `docker ps failed` in broker logs.

## Validation

- `kubeconform -strict` on both the raw placeholder file and a
  `substrate-scion-test`-values render: 11/11 resources valid, 0 errors
  (same resource count — both changes are field/arg additions, not new
  objects).
- No Go changes this round.

## Not in scope / explicitly deferred

- The ingress `NetworkPolicy` restricting broker `:9800` to kubelet-only:
  assessed as worthwhile future hardening, not added — see the README's
  "Broker API exposure" section for the specific reason (no clean
  `NetworkPolicy` peer expression for "the kubelet"/"the node", and the
  node CIDR needed to do it with an IP block isn't information this
  manifest has).

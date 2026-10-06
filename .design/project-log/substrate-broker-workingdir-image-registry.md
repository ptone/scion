# Substrate broker manifest: workingDir and gate-only image_registry

**Date:** 2026-10-06
**Branch:** scion/substrate-restart-durability-smokefix
**Issue:** ptone/scion#2818

## Problem

When `deploy/substrate/broker.yaml` was deployed to a real cluster, the broker pod exited with code 1 and went into CrashLoopBackOff. It never became Ready. There were two symptoms with one root cause.

- **Settings shadowed.** The broker starts with the image's default working directory, `/app`. `config.LoadEffectiveSettings("")` resolves project settings from the working directory, and a project layer takes precedence over global settings. So `/app`'s project context shadowed the mounted global `$HOME/.scion/settings.yaml`, and the substrate profile and `state_namespace` never loaded.
- **Startup gate.** The generic, pre-existing `requireImageRegistryForBroker()` gate (`cmd/server_foreground.go`) found no registry and exited with "image_registry is not configured". The manifest set no `image_registry`, and the gate reads settings through the same `LoadEffectiveSettings("")` call, so a key in the mounted file would not have been seen either.

## Solution

This is a manifest and docs change only. No Go code changed.

- **`broker.yaml`, Deployment:** set `workingDir: /home/scion` on the broker container, matching `HOME`. The mounted global settings are then the settings the broker resolves.
- **`broker.yaml`, ConfigMap:** add `image_registry: "${IMAGE_REGISTRY}"` as a top-level key in the `settings.yaml` data. This deliberately does not use the `SCION_IMAGE_REGISTRY` env var. The setting is gate-only: the substrate runtime never consumes it, because actor images are digest-pinned and `RewriteImageRegistry` is a no-op on digest/fully-qualified refs.
- **Docs:**
  - `README.md`: added an `IMAGE_REGISTRY` placeholder row, an explanatory callout, and an `export` line in "Apply order".
  - `OPERATIONS.md`: added an "`image_registry` is gate-only" section.
  - `settings.example.yaml`: added the key with the clearly illustrative value `us-docker.pkg.dev/<your-project>/scion`.

## Follow-up

- **Gate follow-up (ptone/scion#3540):** make `requireImageRegistryForBroker()` runtime-aware so it skips runtimes that never pull by name, such as substrate. Once that lands, drop the `image_registry` placeholder. The TODOs in all four files point here.
- **Not reproduced locally:** with a minimal `/app/.scion` that sets only `schema_version`, a local `scion config get image_registry` still resolved the mounted global value. The real image's `/app` context is what shadows the settings. `workingDir` makes the broker independent of whatever that context contains, and a re-run of the real-cluster deploy is what confirms it.

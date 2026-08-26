# Proposed addendum for defect-sandbox-delete-hang.md

## 8. Third subcommand affected: `runsc state` hangs on mid-delete sandboxes

**Discovered:** 2026-08-26, during validation of the delete timeout workaround.
**Instance:** `val-delete-2` (python:3.11, sandboxLauncher, us-east4, ptone-experiments)
**runsc version:** `google-958767651` (spec 1.2.1) -- same known-bad build as sections 1-4.

### The finding

`sandbox exec` internally shells out to `runsc ... state <sandbox-id>`. When
`sandbox exec` is called on a sandbox that is mid-delete (i.e., `sandbox delete
--force` is running but has not returned), the spawned `runsc state` process
**hangs permanently** -- identical behavior to the `runsc delete` orphan in section 3.

| Condition | `runsc delete` orphans | `runsc state` orphans |
|-----------|----------------------|----------------------|
| Delete with NO exec probes | 1 | **0** |
| Delete with 4 exec probes | 1 | **4** |
| Delete with 7 exec probes (serial) | 1 | **7** |

The correlation is 1:1 between `sandbox exec` calls and `runsc state` orphans. The
discriminator is clean: zero probes produce zero `runsc state` processes, and the
count scales linearly with probe count.

### Persistence

Unlike the `runsc delete` orphans in section 3 (which became zombies and were
eventually reaped), `runsc state` orphans **persist at 5s, 30s, and 60s** with
identical PID and process state. This is a worse persistence profile than the
delete orphans.

| Time after killing wrapper | `runsc delete` | `runsc state` |
|---------------------------|---------------|---------------|
| t=5s | 1 | 3 |
| t=30s | 1 | 3 |
| t=60s | 1 | 3 |

### Scope -- what this does NOT affect

The workaround code (`deleteWithTimeout`) never calls `sandbox exec` on a sandbox
that is mid-delete. The only caller that could is `sandboxStateStore.reconcile()`,
which runs once at startup (not on a timer) and therefore cannot overlap with a
delete. The watcher (`sandbox wait`) was also tested separately and its child
(`runsc wait`) exits cleanly when the wrapper is killed -- it does not hang.

So this is a **latent** defect in our environment, not an active one. It becomes
active if any code path calls `sandbox exec` (or any CLI command that internally
uses `runsc state`) on a sandbox during the ~10s delete window.

### Argv of an orphaned `runsc state` process

```
/usr/local/gcp/bin/runsc --platform=xemu --platform_device_path=/dev/xemu \
  --root=/tmp/runsc-root --ignore-cgroups --TESTONLY-unsafe-nonroot \
  --overlay2=root:memory --network=none state <sandbox-id>
```

Same flags as the `runsc delete` orphan in section 3, with `state` replacing
`delete --force` in the subcommand position.

### What this means upstream

This is the same defect as section 1, surfacing through a different `runsc`
subcommand. The hang at "Raising signal 15 with default behavior" (or its
equivalent in the state path) is not specific to `delete --force` -- it appears
to be a broader issue in `runsc`'s interaction with the sandbox lifecycle on
the known-bad build. The question for the upstream team: is this the same root
cause, or is `runsc state` independently affected?

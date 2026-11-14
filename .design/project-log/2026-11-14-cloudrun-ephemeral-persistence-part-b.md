# Project Log: Ephemeral persistence, part B (Cloud Run instance)

**Date:** 2026-11-14
**Task:** ptone/scion#7953 part B, stacked on part A.

## Changes
- The Cloud Run runtime implements `EphemeralPersistenceRuntime`, so legacy and flat Cloud Run brokers advertise `ephemeralPersistence`.
- An ephemeral instance gets two MEMORY emptyDir volumes, `ws` at the workspace and `home` at `/home/<user>`, with no subPath. It also gets the agent's home leaf mounted read-only at `/run/scion/home-seed`, and `SCION_HOME_SEED` is set by the runtime (the name is reserved on every runtime).
- Sizing (`ephemeralVolumeSize`, the only place it is defined): S = `resources.disk`, else half the memory limit; S >= 1Gi; home 512Mi; ws = S - 512Mi. Refused when memory - S < 1Gi.
- Broker: no agent workspace directory on the share; the home leaf is cleared at every ephemeral start (per-start reset ID). Ephemeral requires clone-per-agent.
- `sciontool init`: chowns the workspace and home roots to the scion user (they are root-owned 1777 tmpfs), then seeds `$HOME` with `agenthome.SeedTree` within the broker's home-copy limits. This runs before the staged secrets, the clone and the pre-start hooks.

## Findings (live spikes, 2026-11-14)
- Cloud Run Instances reject a DISK emptyDir. A memory volume with subPath mounts next to an NFS volume failed at exec. Two memory volumes without subPath, plus the read-only NFS seed, start fine.
- The clone of ptone/scion onto tmpfs took 12–14 s (about 185 s on NFS). One checkout uses about 260 MiB of memory.
- `/proc/mounts` reports the seed (bindfs over NFS) as rw although writes fail. Nothing infers read-only from mount flags.

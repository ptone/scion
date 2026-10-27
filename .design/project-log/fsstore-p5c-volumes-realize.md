# Project storage phase 5c: `volumes:` entries realized through Layout.Realize

**Date**: 2026-10-27
**Issues**: ptone/scion#5837 (part of the lifecycle phase ptone/scion#5833), tracking ptone/scion#5805
**Stacks on**: phase 5a (ptone/scion#6287, `eb51fb5a4`)
**Design**: design-v9 §P5 and OQ-R2 (a `volumes:` entry is always `Scope=agent`, `Owner=user`)

## Summary

A `volumes:` entry is now a `projectfs.Mount` (`KindVolume`, `Scope=agent`,
`Owner=user`, `Provision=none`, named by its target, `UserSource` = the host
path or, for gcs, the bucket). It goes through `Layout.Resolve` and
`Layout.Realize` like the workspace and shared dirs, via one runtime helper,
`realizeUserVolume` (`pkg/runtime/user_volumes.go`).

- Docker, Podman and Apple: the `-v src:tgt[:ro]` of a non-gcs entry comes
  from `Realize(RuntimeBind)`.
- Kubernetes: a gcs entry's `MountPath` and `ReadOnly` come from
  `Realize(RuntimePod)`, which returns no claim. The CSI volume stays the
  runtime's.

This merges code paths only. Behaviour does not change.

## Decisions

- **Resolve and Realize never refuse a volume.** Before this change the
  runtimes passed an entry through as written: an empty source or target, or
  any type other than `gcs` treated as a bind. A refusal in projectfs would
  therefore be new behaviour. Entry checks stay in `api.VolumeMount.Validate`
  (template load) and in `checkVolumeSource` (runtime). The guard projectfs
  does add is that a `KindVolume` Mount must be user-owned and agent-scoped.
- **gcsfuse on bind runtimes is left as it was.** It is an in-container
  mount command, not a bind mount, and replacing GCS-FUSE is a design
  non-goal. On k8s, local and nfs entries are still skipped with a warning.
- **The golden testdata was captured at BASE** (a detached worktree at
  `eb51fb5a`, with the new test files copied in and the `-update-*` flags
  run there). It then passed unchanged at the new head. That is the proof of
  no behaviour change.

## Gotchas

- `TestResolveRejects/unknown_kind` used `"volume"` as its example of an
  unknown kind. Adding a kind can break that sort of test. It now uses
  `"other"`.
- The run-argv golden test pins `HOME`, `projectConfigsDirFunc`,
  `SCION_DEV_BINARIES` and the force-host-network env, and replaces the
  host uid and gid values with a placeholder, so the testdata is the same on
  every machine. `-update-volume-args-golden` rewrites it.

## Follow-ups (not done)

- Docker gcs volumes and the k8s `/workspace` volume fallback in `Run` read
  `api.VolumeMount` directly. They could take the Mount too if GCS-FUSE ever
  becomes a Realize runtime kind.

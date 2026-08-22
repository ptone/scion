# Permissions Phase 1B Development Log

## Baseline

- Target branch: `scion/permissions-p1b`.
- Current main baseline: `7d171a503fbc01f950513e599e44dc4088eae0d2`.
- Accepted Phase 1A baseline: `origin/scion/permissions-p1a` at
  `15833c477dbbf23362d429ef4c22a7716e1e696f` (including implementation fix
  `56c9b34747cb49529a8ee3c83e3062f38e4d92b7`).
- Reconciliation: both branches share merge base
  `feb3e188f147c52d760d3530710a1f72eb7062b7`. The initial shallow clone hid
  this ancestry; after authorized `git fetch --unshallow origin`, the selected
  Phase 1A branch can be cleanly merged onto current main without using
  unrelated-history merging.

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

## Implementation

- Added `PrincipalContext`, `CredentialContext`, `AuthzRequest`,
  `AuthzRequestFromContext`, and request-based `AuthzService.Decide` /
  `DecideFromContext`. `CheckAccess` remains a compatibility adapter.
- Extended decisions with principal, credential, and matched-policy/grant
  metadata for future audit emission.
- Auth middleware now carries credential context for interactive users, UATs,
  agent JWTs, federation tokens, development auth, and broker HMAC auth.
  UAT credentials retain their stored token ID; agent JWTs use their JTI.
- Explicitly map federated users and agents into the decision pipeline, deny
  federated services, and reject any attempted principal-kind relabeling.
- Closed scoped-admin bypasses in maintenance admission, broker secret
  rotation, group role escalation, and global/project stop-all. Unscoped admin
  behavior is unchanged.

## Verification

- Focused request, credential, federation, UAT, and scoped-admin bypass tests.
- `go test ./pkg/hub -timeout=600s -count=1`.
- `make ci`.

## Residual Risks

- Federated service identities are deliberately denied until a supported hub
  surface and policy model are specified. No Phase 1C/1D ordering or route
  guard work was introduced.

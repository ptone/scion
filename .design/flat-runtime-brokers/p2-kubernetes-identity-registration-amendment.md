# P2 amendment: instance Kubernetes identity policy and registration parity

Status: architecture decision, revision 3, 2026-10-09; block semantics confirmed by the GCP identity feature owner; P2M evidence substitution recorded below. Delivery timing belongs to the delivery lead. This document changes the P2 contract only; it does not reopen the deferred flat dispatch-policy proposal.

Reference surfaces: upstream `75e87c07b`, P2 configuration at `67d7055ba`, and the P1 saved-profile guard at `6b626f1d`. The implementation must be reviewed at its eventual merged revision.

## Problem and goals

Flat Kubernetes instances need the upstream GCP identity assign and block behavior without consulting profiles or global runtime-map entries. Registration must also retain the authorization semantics introduced upstream while keeping flat target identity and explicit linking unchanged.

Success means one instance supplies its own identity policy and activated namespace, early and late runtime checks agree on that instance, and external registration applies upstream authorization and stale-decision checks.

## Non-goals

- No change to flat dispatch eligibility, provider election, or the deferred election-provenance proposal.
- No new profile resolution, dynamic policy reload, or transfer of Kubernetes credentials to the Hub.
- No new broker-self dispatch permission or automatic provider link.
- No change to legacy Kubernetes identity resolution.

## Configuration amendment

Section 2's `V1RuntimeTargetConfig` admits two optional fields under each `server.broker.instances[].runtime_target`:

| Go field | Settings key | Type | Server-config mirror key |
|---|---|---|---|
| `KubernetesBlockServiceAccount` | `kubernetes_block_service_account` | string | `kubernetesBlockServiceAccount` |
| `KubernetesServiceAccountMappings` | `kubernetes_service_account_mappings` | map of GCP service-account email to Kubernetes ServiceAccount name | `kubernetesServiceAccountMappings` |

The mapping key is plural, matching the existing upstream setting. Reuse upstream `ValidateKubernetesBlockServiceAccount` and `ValidateKubernetesServiceAccountMappings`, including their normalization and rejection semantics. Apply the amendment to both config families, schema, strict loader, conversions, and settings round trips. Docker targets reject populated Kubernetes-only fields.

These values are local operator policy. They are neither part of the Hub `RuntimeTargetDescriptor` nor inputs to persisted target identity. Changing policy does not mint a new target ID. The existing rules for cluster and namespace identity still apply.

The instance constructor owns a snapshot of these settings, including a copy of the mapping. Early resolution and later checks for a dispatch use that snapshot. A file change requires normal instance restart; this amendment introduces no live reload.

Configuration source remains section 2's strict `LoadRuntimeBrokerInstances` file selection: the global settings file's raw server section, or the existing `--config` fallback when applicable. This amendment does not introduce a Hub database overlay, environment overlay, or separate global-settings read for instance target policy. The camelCase mirror and admin round trips preserve values; they do not establish a new runtime configuration source. In particular, flat policy resolvers do not call `LoadGlobalSettingsWithOverlay` to fill omissions. Legacy resolvers retain their existing global-plus-database-overlay behavior.

## Resolution and consistency

A flat resolver reads only its own instance policy. It does not read project settings, profiles, or a global runtime entry to supply missing identity policy. Namespace and context come from the activated target and its actual client/runtime, using the existing target scope verification rules.

For assign mode, resolve the requested GCP service account through the instance mapping. Missing or invalid mapping refuses with upstream-equivalent behavior. Preserve request-level ServiceAccount conflict checks. Explicit namespace selection must agree with the activated instance namespace; it must not select another execution scope.

For block mode, use the configured instance block ServiceAccount. Intentional omission retains upstream behavior: use the namespace default ServiceAccount, disable API token mounting, and apply the required Workload Identity node selector. A configuration error is not omission. Preserve upstream rejection of conflicting request-level ServiceAccount names and upstream handling of template-only values. Do not describe default-ServiceAccount behavior as a guarantee about that account's cloud permissions; those remain an operator responsibility under the upstream contract.

Preserve the in-pod metadata emulator as the second layer: emit `SCION_METADATA_MODE=block` alongside the pod identity configuration, without adding assign-mode service-account email or project environment values. Apply this to every resolved block mode, whether originating in an explicit request, a project or Hub default carried through resolved environment, or saved agent GCP identity on start/restart. Instance-only policy selection does not discard these existing mode sources. Invalid configured block account names use the upstream validator; strict startup rejects invalid settings, and invalid values reaching dispatch return 400 rather than becoming omission. Conflicting request-level ServiceAccount names likewise retain upstream 400 behavior.

Represent the flat selection explicitly in the internal comparable selection value with instance key and persisted target ID. Legacy profile/runtime-entry fields remain empty for a flat selection. A synthetic `instance:<key>` runtime-map entry is not the preferred representation and must never become a settings lookup key. Runtime type is checked separately against the actual selected manager.

Both early and late identity resolution bind to the same instance. Keep `rejectKubernetesAssignRuntimeChange`, `rejectKubernetesBlockRuntimeChange`, and `rejectRuntimeClassificationMismatch`, or their equivalent shared implementations, on start and restart before their subsequent launch side effects. Adapting an internal selection type is permitted; preserving the exact helper source text is not a requirement.

Only the saved-profile reload is guarded with `!s.isFlat()`. Image-provenance reads, manager selection, identity consistency checks, and runtime classification checks remain outside that guard. Existing flat manager selection continues to return the instance's fixed runtime without consulting profiles.

While integration is incomplete, an explicit unsupported-mode refusal is an acceptable intermediate implementation state. It is not the final behavior or a substitute for completed assign and block support. No fallback from assign or block to passthrough is introduced.

## Registration authorization clarification

The existing registration contract's phrase "AutoProvide is stored as sent" remains subject to registration authorization. Flat external registration follows upstream semantics:

- A new row with AutoProvide enabled, or changing an existing false value to true, requires the upstream `broker.auto_provide` authorization.
- Preserving an already true value or disabling it adds no enablement permission check. Normal registration and re-mint authorization still apply.
- Re-minting follows `brokerRemintTargetAuthorized`, including its credential restrictions.
- PreserveSettings leaves existing metadata intact and creates a new row with AutoProvide disabled, as upstream defines it.
- Pin the authorized existing-row match, or absence of a match, at mutation. Preserving true without a new enablement grant is also conditional on the matched row still being true. Use upstream's stale-authorization refusal, before metadata mutation or token issuance.

Keep flat ID-only registration matching, immutable target binding, and no automatic links. Denied registration does not silently turn an unauthorized true request into a successful false registration.

Embedded registration continues to take AutoProvide from trusted in-process Hub/operator configuration. Remote instance configuration and request labels do not confer that in-process authority. The existing experimental dispatch rule is unchanged.

## Alternatives considered

1. Reuse global profile/runtime settings through a synthetic runtime-map name. Rejected: this restores settings dependencies that the flat model removes and makes sibling instances depend on unrelated global keys.
2. Skip all Kubernetes identity consistency checks on flat instances. Rejected: fixed placement removes profile selection, but identity material must still match the actual runtime and instance namespace.
3. Permanently require an explicit block ServiceAccount. Rejected for this amendment: upstream intentionally supports namespace-default behavior; introducing a stricter flat-only policy is unnecessary to integrate the feature.
4. Require a new AutoProvide grant on every registration preserving true. Rejected: it differs from upstream's preserved-value semantics and stale-decision check.

## Implementation and verification sequence

1. Implement one complete flat Kubernetes identity slice: config, instance snapshot, assign/block resolution, produced pod configuration, and early/late consistency. Temporary refusals may exist only between development commits.
2. Integrate external registration authorization parity while preserving embedded configuration authority and flat identity checks.
3. Review the merged revision and run the relevant local checks. Delivery lead owns the merge timing.
4. Keep the planned initial P2+S6 gate on its pre-merge pin. Run a separate required post-merge verification block on the same disposable VMs before the P2 compare link. Record both exact revisions; final P2 readiness includes both evidence blocks and identifies the final merged revision.

The merged implementation fully supports assign and block, including omitted block ServiceAccount behavior, start/restart consistency, and registration parity. It does not use temporary unsupported-mode refusals as its success oracle. No move of the initial gate pin is required by this amendment. The post-merge evidence uses the explicitly adjusted scope below.

### P2M evidence substitution (revision 3)

The disposable environment lacks the cloud IAM setup needed to pass the Hub's real GSA verification through IAM Credentials GenerateAccessToken. The architecture review therefore accepts the following substitution for the earlier requirement to run both modes live on those VMs. This changes verification scope, not production behavior.

- Live Hub-to-IAM-to-Kubernetes assign rows, including refusal scenarios that require that verification prerequisite, are **NOT RUN**. Record a named cloud-integration follow-up. No production test path, fabricated live verification flag, or cloud resource creation is required by this amendment.
- Assign evidence at the exact merged revision must exercise the production flat policy resolver and actual pod builder in tests: mapped KSA, activated namespace, environment, missing mapping, explicit KSA/namespace conflicts, sibling-instance isolation, and legacy behavior. Record simulated dependencies. This evidence does not prove end-to-end cloud identity operation.
- Block runs live on kind, including configured and omitted block ServiceAccount cases. A reversible node label may satisfy the required scheduling selector. Record that environmental adjustment and inspect the actual pod's ServiceAccount, disabled token automount, required selector, block-mode environment, and absence of assign-mode identity additions. This proves pod configuration and execution in that environment, not actual GKE Workload Identity/IAM behavior or cloud privileges of the namespace default account.
- Start/restart early-versus-late mismatch refusals may use controlled unit evidence where an immutable instance snapshot makes them unreachable through the normal external interface. Live restart and snapshot-change cases separately verify their externally observable consistency. Do not introduce a production mutation hook solely to provoke a mismatch.
- The final report distinguishes live PASS, unit PASS, and cloud NOT RUN, identifies each exact tested revision, and carries the assign integration gap forward explicitly. Fully implemented support and completed evidence under this exception must not be described as complete live cloud coverage.

## Acceptance criteria

- Strict loading, schema, validation, and both config-family round trips agree on the new fields; Docker rejects populated fields.
- Two flat Kubernetes instances can use different mappings and block accounts without consulting each other's or project/global profile settings.
- Assign selects the instance mapping and activated namespace; missing mapping and conflicting explicit identity/namespace requests refuse with no launch.
- Block produces the configured ServiceAccount, API token automount disabled, and required node selector; omission produces upstream namespace-default behavior with the same token and node restrictions.
- Every supported block-mode source retains `SCION_METADATA_MODE=block` with no assign-mode identity environment additions; cover request, project/Hub default, and saved start/restart sources. Invalid configured names and conflicting explicit accounts return the upstream refusal.
- Flat identity policy does not change when unrelated global runtime/profile or database-overlay identity settings change; the instance snapshot remains authoritative. Legacy overlay behavior is unchanged.
- Start/restart runtime or instance-selection mismatch refuses; flat operations never reload a saved profile. Legacy and Docker behavior remain unchanged.
- New/false-to-true AutoProvide requests require upstream permission; preserve-true, disable, PreserveSettings, and re-mint credential cases match upstream. Stale match and stale preserve-true decisions refuse without mutation or token issuance.
- Embedded AutoProvide comes from the trusted operator path; external requests cannot claim that authority. No registration case creates a flat provider link.
- The final evidence records the original gate revision and the reviewed post-merge revision separately, with fully implemented identity modes at the latter and the revision-3 evidence substitution clearly identified.

## Open questions

No architecture question remains in this amendment. The delivery lead schedules integration and review. Implementation changes to frozen fixture assertions beyond the new configuration and integration cases must be identified explicitly rather than inferred from this amendment.

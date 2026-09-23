# Scion HA deployment: Terraform

Terraform for the Cloud Run hub + IAP, Cloud SQL Postgres, GKE Autopilot
runtime, Filestore NFS pattern, with multiple hubs sharing one project's
infra. See the design doc (`conv:5e2bb789-a863-45ec-a45c-b221904f7e1b`,
fork issue `ptone/scion#1840`) for the full rationale; this README covers
what an operator needs to actually run it.

**Status: phase 1.** This is the minimum vertical slice, validated once
end-to-end (`tfha` / `ptone-emblem`). Phases 2-4 (HA hardening, modularity
seams, CI) are not implemented yet — see the design doc §7.

## Layout

```
modules/
  project-services/ network/ cloudsql-instance/ filestore/ gke-autopilot/
  artifact-registry/          # shared layer
  shared-lookup/ cloudsql-database/ hub-identity/ agent-runtime-k8s/
  hub-cloudrun/                # hub layer
configurations/
  shared-infra/   # apply once per project
  hub/            # apply once per hub
```

Modules declare no `provider` or `backend` blocks — only the two
`configurations/*` roots do. The hub root never manages shared infra; it only
reads it, by naming convention, through the `shared-lookup` module (no
`terraform_remote_state`).

## Prerequisites (manual, once per project)

1. A project with billing enabled. The operator (in `ptone-emblem`, this is
   **vm-deploy**) needs Owner or an equivalent role set covering: Editor,
   `roles/compute.networkAdmin`, `roles/container.admin`,
   `roles/run.admin`, `roles/resourcemanager.projectIamAdmin`,
   `roles/iam.serviceAccountAdmin`, `roles/servicenetworking.networksAdmin`,
   `roles/storage.admin`, `roles/iap.admin`. (`servicenetworking.networksAdmin`,
   `storage.admin` and `iap.admin` were still pending grant as of the phase 1
   validation — see `phase1-validation.md`.)
2. A GCS state bucket, versioned: `<project>-<name_prefix>-tfstate` (e.g.
   `ptone-emblem-tfha-tfstate`). Access limited to operators.
3. An IAP OAuth web client, created by hand in the console (the IAP OAuth
   Admin API no longer supports creating new clients via API/Terraform):
   - Application type: Web application.
   - Authorized redirect URI:
     `https://iap.googleapis.com/v1/oauth/clientIds/<CLIENT_ID>:handleRedirect`.
   - Store the client secret in Secret Manager (`<prefix>-oauth-client-secret`).
   - One client can serve every hub under a given `name_prefix`.
4. The hub image, built and pushed to the Artifact Registry repo that
   `shared-infra` creates. There is no single-apply bootstrap trick here (the
   two-root split already separates "create the repo" from "use the image"):
   apply `shared-infra` first, build/push, then apply `hub`.

## Bootstrap sequence

```bash
# 1. Shared infra, once per project.
terraform -chdir=deploy/terraform/configurations/shared-infra init \
  -backend-config="bucket=<project>-<prefix>-tfstate" \
  -backend-config="prefix=<prefix>/shared"
terraform -chdir=deploy/terraform/configurations/shared-infra apply \
  -var-file=terraform.tfvars

# 2. Build and push hub_image using the repo shared-infra just created
#    (out of scope for this Terraform — see the image pipeline).

# 3. One hub.
terraform -chdir=deploy/terraform/configurations/hub init \
  -backend-config="bucket=<project>-<prefix>-tfstate" \
  -backend-config="prefix=<prefix>/hubs/<hub_name>"
terraform -chdir=deploy/terraform/configurations/hub apply \
  -var hub_name=<hub_name> -var state_prefix=<prefix>/hubs/<hub_name> \
  -var-file=<hub_name>.tfvars
```

`state_prefix` must always be passed and must equal the `-backend-config
prefix` used at `init` — Terraform cannot read its own backend config back,
so this is enforced by a `check` block in `configurations/hub`, not by
convention alone. Getting it wrong fails the plan loudly instead of silently
applying one hub's variables onto another hub's state.

Each further hub is just step 3 again with a new `hub_name`/prefix — the
shared layer is untouched.

## Everything is prefixed; nothing is adopted

Every resource name derives from `name_prefix` (shared layer, default
`tfha`) or `hub_name` (hub layer). There are no unprefixed defaults, no
`import` blocks, and no create-if-missing logic: a name collision fails the
apply, which is the desired behavior on a project that also runs live
Scion infra (`ptone-emblem`: `scion-hub`, `scion-hub-iap-proxy`,
`scion-a2a-bridge`, `scion-discord`, `scion-hub-runner`, `scion-hub-gke`).
All IAM grants are additive (`google_*_iam_member` only — never
`_iam_binding`/`_iam_policy`).

## Shared-infra trust domain and sizing

Hubs sharing one project's infra form one trust domain, not a hard
multi-tenancy boundary — see the design doc §3.7 for the full list of what
is and isn't isolated. In particular, size Cloud SQL's `max_connections`
for the sum across hubs: with `max_open_conns = 10` per hub instance and
`max_instances = 3`, each hub can use up to 30 connections, so the default
`max_connections = 200` supports roughly 5 dev hubs with headroom.

## GKE deletion protection is Terraform-only

Unlike Cloud SQL (`deletion_protection` + `settings.deletion_protection_enabled`)
and Filestore (`deletion_protection_enabled` + `deletion_protection_reason`),
`google_container_cluster` has no separate API-level deletion-protection
field — only the one Terraform-level `deletion_protection` attribute, which
blocks `apply`/`destroy` from replacing or deleting the cluster while true.
There is no equivalent GCP API guard for the GKE cluster the way there is
for SQL/Filestore.

## Destroy runbook

**Order: every hub root first, then shared.** Never the reverse — and it's
enforced, not just documented:

1. For each hub, `terraform -chdir=configurations/hub destroy` (with that
   hub's backend prefix and tfvars). This removes only that hub's resources;
   it never touches shared infra, because the hub root only reads shared
   infra via data sources.
2. `terraform -chdir=configurations/shared-infra apply -var
   deletion_protection=false ...`. A `terraform_data.destroy_guard`
   precondition (`data.google_sql_databases` on the shared instance) makes
   this apply **fail** while any hub database still exists — every hub
   creates exactly one, so this is the proxy for "a hub still exists".
3. `terraform -chdir=configurations/shared-infra destroy`.
4. The operator (vm-deploy) compares a before/after `tfha*` resource
   inventory to confirm nothing outside the prefix was touched, and that
   nothing was left behind.

## What's not here yet (see design §7)

- Phase 2: a second hub, Cloud SQL `REGIONAL` + backups, `min_instances = 2`
  (pending an HA broker confirmation), `check` blocks asserting the
  deterministic URL/audience, bucket lifecycle.
- Phase 3: `shared_overrides` for hand-built infra, IAM-condition-scoped
  `secretmanager.admin`, moving the DB DSN to a secret env ref, per-module
  READMEs (terraform-docs), typed `validation` blocks on every variable.
- Phase 4: this README grows prereq/rollout detail, CI (`fmt`/`validate`/
  `tflint`), and a link from `docs-site/.../hosted/ha/setup-gcp.md`.

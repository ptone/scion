# Per-hub service accounts and additive IAM (design §3.4). All grants use
# google_*_iam_member exclusively (design §3.8 rule 2): never *_iam_binding or
# *_iam_policy, which would be authoritative and strip the live stack's or
# other hubs' existing grants.

resource "google_service_account" "hub" {
  project      = var.project_id
  account_id   = "${var.hub_name}-hub"
  display_name = "Scion hub (${var.hub_name}) — Cloud Run runtime identity"
}

resource "google_service_account" "transport" {
  project      = var.project_id
  account_id   = "${var.hub_name}-transport"
  display_name = "Scion hub (${var.hub_name}) — transport identity, mints IAP OIDC tokens for agents"
}

resource "google_service_account" "agent" {
  project      = var.project_id
  account_id   = "${var.hub_name}-agent"
  display_name = "Scion hub (${var.hub_name}) — agent pod identity (Workload Identity)"
}

# --- Hub SA: project-level grants ---
#
# IAM scope rule (design §3.4, added after vm-deploy's second review): §3.8's
# additive rule governs what we *remove* — this rule governs what we *hand
# out*. No project-wide grant may reach resources outside this hub's own
# names. ptone-emblem holds 15 Secret Manager secrets and IAP resources
# belonging to the live stack, including live hubs' user_signing_key/
# agent_signing_key — a project-wide roles/secretmanager.admin (the
# original §3.4/§8 spec) would let this hub's SA read and delete them.
#
# Replaced by:
#  (a) per-secret secretAccessor on this hub's own Terraform-managed secrets
#      — <hub>-settings/-session-secret/-kubeconfig (hub-cloudrun) and
#      <hub>-db-password (cloudsql-database), not here;
#  (b) the conditioned grant below, scoped to exactly the prefix
#      pkg/secret/gcpbackend.go's gcpSecretName produces for this hub's own
#      HUB-scope secrets (user_signing_key, agent_signing_key,
#      oidc_signing_key/keyset — created via the gcpsm backend at hub
#      startup, needed for phase 1 check 1 to pass at all): for hub scope,
#      gcpSecretName hashes hubID:scopeID with scopeID == hubID (self-scoped;
#      confirmed against pkg/hub/oidckeys.go's store.ScopeHub calls, which
#      all pass hubID as both scope and scopeID). hub_id is set to hub_name
#      in settings.yaml (§3.6), so the prefix is computable at plan time.
#      Cross-checked the substr(sha256(...),0,12) vs. Go's
#      hex.EncodeToString(sha256.Sum256(...)[:6]) equivalence arithmetically
#      (both take the first 6 bytes of the same digest, hex-encoded) —
#      recorded in phase1-validation.md.
#      User- and project-scope secrets hash hubID:userID / hubID:projectID —
#      no per-hub prefix exists for those, so this condition can't cover
#      them and creating one fails loudly with a 403 (OQ-7, ptone; not a
#      phase 1 blocker). OQ-8 (whether a name condition covers
#      `secrets.create`, not just existing-secret operations) is resolved:
#      vm-deploy's probe confirmed conditions do cover create, so no
#      pre-create fallback is needed here.
#
# project_number (below) always comes from the shared-lookup data source via
# var.project_number, never a literal — a hardcoded project number in this
# expression would silently stop matching if this Terraform were ever
# pointed at a different project.
locals {
  hub_scope_secret_hash   = substr(sha256("${var.hub_name}:${var.hub_name}"), 0, 12)
  hub_scope_secret_prefix = "projects/${var.project_number}/secrets/scion-hub-${local.hub_scope_secret_hash}-"
}

resource "google_project_iam_member" "hub_secretmanager_admin_hub_scope" {
  project = var.project_id
  role    = "roles/secretmanager.admin"
  member  = "serviceAccount:${google_service_account.hub.email}"

  condition {
    title       = "${var.hub_name}-hub-scope-secrets"
    description = "Only this hub's own hub-scope secrets (gcpSecretName(scope=hub, scopeID=hub_id)) — never another hub's or the live stack's."
    expression  = "resource.name.startsWith(\"${local.hub_scope_secret_prefix}\")"
  }
}

resource "google_project_iam_member" "hub_cloudsql_client" {
  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.hub.email}"
}

resource "google_project_iam_member" "hub_cloudsql_instance_user" {
  project = var.project_id
  role    = "roles/cloudsql.instanceUser"
  member  = "serviceAccount:${google_service_account.hub.email}"
}

resource "google_project_iam_member" "hub_container_cluster_viewer" {
  project = var.project_id
  role    = "roles/container.clusterViewer"
  member  = "serviceAccount:${google_service_account.hub.email}"
}

resource "google_project_iam_member" "hub_logging_log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.hub.email}"
}

# Hub SA mints tokens as the transport SA (SCION_TRANSPORT_TOKEN injection
# depends on this; if missing, the failure is silent) and as itself
# (signBlob for GCS signed URLs).
resource "google_service_account_iam_member" "hub_mints_transport_tokens" {
  service_account_id = google_service_account.transport.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.hub.email}"
}

resource "google_service_account_iam_member" "hub_mints_own_tokens" {
  service_account_id = google_service_account.hub.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.hub.email}"
}

# --- Transport SA ---
#
# NOT granted here: a project-wide roles/iap.httpsResourceAccessor (design
# §3.4/§8's original spec — reversed after vm-deploy's second review, since
# it reaches every IAP-protected resource in the project, including the live
# scion-hub and scion-hub-iap-proxy). Scoped instead to just this hub's own
# Cloud Run service, in hub-cloudrun's google_iap_web_cloud_run_service_iam_member
# (the same resource type already used there for human iap_members).

# --- Agent SA ---
#
# No Secret Manager role at all (design §3.4 — reversed after vm-deploy's
# second review; original spec was a project-wide secretAccessor). The agent
# SA is the Workload Identity binding for agent pods, which run agent- and
# user-supplied code: a project-wide accessor would let any agent pod read
# scion-hub-*-user_signing_key/-agent_signing_key for the live hub (or any
# other hub sharing this project) and mint valid tokens for it — escalation
# from "runs an agent" to "administers an unrelated production hub", and
# unfixable with an IAM condition (user/project-scope secret names have no
# per-hub prefix — same root cause as the hub SA note above, OQ-7). The
# settings.yaml runtimes.remote.gke is therefore set to false (§3.6): with
# gke: false, k8s_runtime.go puts resolved secrets into namespaced
# Kubernetes Secrets the hub writes, not a SecretProviderClass the agent
# pod's own WI identity reads — so Secret Manager access is never in the
# agent's reach at all, by construction, not just by omitted IAM. The agent
# SA keeps only its Workload Identity binding (agent-runtime-k8s).


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

resource "google_project_iam_member" "hub_secretmanager_admin" {
  project = var.project_id
  role    = "roles/secretmanager.admin"
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

# --- Transport SA: project-level grant ---

resource "google_project_iam_member" "transport_iap_accessor" {
  project = var.project_id
  role    = "roles/iap.httpsResourceAccessor"
  member  = "serviceAccount:${google_service_account.transport.email}"
}

# --- Agent SA: project-level grant ---

resource "google_project_iam_member" "agent_secret_accessor" {
  project = var.project_id
  role    = "roles/secretmanager.secretAccessor"
  member  = "serviceAccount:${google_service_account.agent.email}"
}

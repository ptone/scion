# Per-hub database, user and password on the shared Cloud SQL instance
# (design §3.1, §3.4). No instance-level resources here.

resource "random_password" "db" {
  length  = 32
  special = false
}

resource "google_sql_database" "this" {
  project         = var.project_id
  instance        = var.instance_name
  name            = replace(var.hub_name, "-", "_")
  deletion_policy = var.deletion_policy
}

resource "google_sql_user" "this" {
  project  = var.project_id
  instance = var.instance_name
  name     = var.hub_name
  password = random_password.db.result
  type     = "BUILT_IN"
}

resource "google_secret_manager_secret" "db_password" {
  project   = var.project_id
  secret_id = "${var.hub_name}-db-password"

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "db_password" {
  secret      = google_secret_manager_secret.db_password.id
  secret_data = random_password.db.result
}


# Unused by the running hub today (the DSN is embedded directly into the
# settings secret by Terraform's own identity, via the data source in
# hub-cloudrun) — kept anyway, scoped to this hub's own secret, because
# phase 3's Alt-F end state (DSN via a secret env var instead of an
# embedded plaintext DSN) needs the hub SA to read this secret at runtime.
# When that lands, this grant must also join hub-cloudrun's
# time_sleep.iam_propagation depends_on list (design §3.5) — it doesn't
# today because nothing reads it at boot yet.
resource "google_secret_manager_secret_iam_member" "hub_reads_db_password" {
  secret_id = google_secret_manager_secret.db_password.secret_id
  project   = var.project_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${var.hub_sa_email}"
}

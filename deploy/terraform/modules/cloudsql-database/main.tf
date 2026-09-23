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

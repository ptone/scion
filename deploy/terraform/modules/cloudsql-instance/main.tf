# Shared Cloud SQL for PostgreSQL 16 instance, private IP only. Per-hub
# databases, users and passwords are created by the cloudsql-database module,
# not here (design §3.1/§3.4).

resource "google_sql_database_instance" "this" {
  project             = var.project_id
  name                = "${var.name_prefix}-pg"
  region              = var.region
  database_version    = "POSTGRES_16"
  deletion_protection = var.deletion_protection

  settings {
    tier              = var.tier
    edition           = var.edition
    availability_type = var.availability_type

    ip_configuration {
      ipv4_enabled    = false
      private_network = var.network_id
    }

    database_flags {
      name  = "max_connections"
      value = tostring(var.max_connections)
    }
  }

  depends_on = [var.psa_connection]
}

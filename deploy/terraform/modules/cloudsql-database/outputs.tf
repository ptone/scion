output "db_name" {
  description = "Database name."
  value       = google_sql_database.this.name
}

output "db_user" {
  description = "Database user name."
  value       = google_sql_user.this.name
}

output "db_password" {
  description = "Database user password (sensitive)."
  value       = random_password.db.result
  sensitive   = true
}

output "password_secret_id" {
  description = "Secret Manager secret ID holding the database password."
  value       = google_secret_manager_secret.db_password.secret_id
}

output "hub_sa_email" {
  description = "Hub (Cloud Run runtime) service account email."
  value       = google_service_account.hub.email
}

output "transport_sa_email" {
  description = "Transport service account email (mints IAP OIDC tokens for agents)."
  value       = google_service_account.transport.email
}

output "agent_sa_email" {
  description = "Agent pod service account email (bound via Workload Identity in agent-runtime-k8s)."
  value       = google_service_account.agent.email
}

output "hub_iam_grants" {
  description = "All hub-SA IAM grant resources, bundled purely as a depends_on handle (design §3.5) — hub-cloudrun's time_sleep.iam_propagation depends on this list, so a Cloud Run revision can't boot before these grants have had time to propagate."
  value = [
    google_project_iam_member.hub_secretmanager_admin_hub_scope,
    google_project_iam_member.hub_cloudsql_client,
    google_project_iam_member.hub_cloudsql_instance_user,
    google_project_iam_member.hub_container_cluster_viewer,
    google_project_iam_member.hub_logging_log_writer,
    google_service_account_iam_member.hub_mints_transport_tokens,
    google_service_account_iam_member.hub_mints_own_tokens,
  ]
}

output "hub_iam_condition_expression" {
  description = "The hub-scope secret-name prefix used in the conditioned secretmanager.admin grant, exposed only so hub-cloudrun's time_sleep can trigger a fresh wait if this expression ever changes (e.g. hub_name changes) — not meant for any other use."
  value       = local.hub_scope_secret_prefix
}

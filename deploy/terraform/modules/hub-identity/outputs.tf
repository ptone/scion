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

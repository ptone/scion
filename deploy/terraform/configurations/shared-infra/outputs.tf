# These outputs are for humans (the README, and vm-deploy's collision/apply
# records) — the hub root reads shared infra via shared-lookup's data
# sources, by naming convention, not via these outputs or remote state
# (design §3.4, §3.5, Alt-K).

output "name_prefix" {
  value = var.name_prefix
}

output "region" {
  value = var.region
}

output "zone" {
  value = var.zone
}

output "project_number" {
  value = module.project_services.project_number
}

output "network_name" {
  value = module.network.network_name
}

output "subnet_name" {
  value = module.network.subnet_name
}

output "sql_instance_name" {
  value = module.cloudsql_instance.instance_name
}

output "sql_connection_name" {
  value = module.cloudsql_instance.connection_name
}

output "filestore_server_ip" {
  value = module.filestore.server_ip
}

output "filestore_share_name" {
  value = module.filestore.share_name
}

output "gke_cluster_name" {
  value = module.gke_autopilot.name
}

output "gke_endpoint" {
  value = module.gke_autopilot.endpoint
}

output "artifact_registry_repo_url" {
  value = module.artifact_registry.repo_url
}

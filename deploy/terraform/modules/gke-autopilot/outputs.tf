output "name" {
  description = "Cluster name."
  value       = google_container_cluster.this.name
}

output "location" {
  description = "Cluster location (region, since this is a regional Autopilot cluster)."
  value       = google_container_cluster.this.location
}

output "endpoint" {
  description = "Cluster control-plane endpoint (IP), used to build the kubernetes provider host and hub kubeconfig."
  value       = google_container_cluster.this.endpoint
}

output "ca_certificate" {
  description = "Base64-encoded cluster CA certificate."
  value       = google_container_cluster.this.master_auth[0].cluster_ca_certificate
}

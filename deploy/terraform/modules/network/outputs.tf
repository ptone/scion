output "network_id" {
  description = "Self link / ID of the VPC network."
  value       = google_compute_network.this.id
}

output "network_name" {
  description = "Name of the VPC network."
  value       = google_compute_network.this.name
}

output "subnet_id" {
  description = "Self link / ID of the subnet."
  value       = google_compute_subnetwork.this.id
}

output "subnet_name" {
  description = "Name of the subnet."
  value       = google_compute_subnetwork.this.name
}

output "psa_connection" {
  description = "The google_service_networking_connection resource, used as a depends_on handle by modules (Cloud SQL, Filestore) that require the PSA peering to exist first."
  value       = google_service_networking_connection.psa
}

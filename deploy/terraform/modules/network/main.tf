# Custom-mode VPC, one subnet, and a private services access (PSA) peering
# range for Cloud SQL and Filestore. No provider/backend blocks here: the
# calling configuration owns those (see deploy/terraform/README.md).

resource "google_compute_network" "this" {
  project                 = var.project_id
  name                    = "${var.name_prefix}-vpc"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "this" {
  project       = var.project_id
  name          = "${var.name_prefix}-subnet"
  region        = var.region
  network       = google_compute_network.this.id
  ip_cidr_range = var.subnet_cidr

  private_ip_google_access = true
}

# Reserved range for VPC peering used by Cloud SQL (private IP) and Filestore.
resource "google_compute_global_address" "psa" {
  project       = var.project_id
  name          = "${var.name_prefix}-psa"
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = var.psa_prefix_length
  network       = google_compute_network.this.id
}

resource "google_service_networking_connection" "psa" {
  network                 = google_compute_network.this.id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.psa.name]
}

provider "google" {
  project = var.project_id
  region  = var.region
}

provider "google-beta" {
  project = var.project_id
  region  = var.region
}

module "project_services" {
  source = "../../modules/project-services"

  project_id = var.project_id
}

module "network" {
  source = "../../modules/network"

  project_id        = var.project_id
  region            = var.region
  name_prefix       = var.name_prefix
  subnet_cidr       = var.subnet_cidr
  psa_prefix_length = var.psa_prefix_length

  depends_on = [module.project_services]
}

module "cloudsql_instance" {
  source = "../../modules/cloudsql-instance"

  project_id          = var.project_id
  region              = var.region
  name_prefix         = var.name_prefix
  network_id          = module.network.network_id
  psa_connection      = module.network.psa_connection
  tier                = var.sql_tier
  availability_type   = var.sql_availability_type
  max_connections     = var.sql_max_connections
  deletion_protection = var.deletion_protection
}

module "filestore" {
  source = "../../modules/filestore"

  project_id          = var.project_id
  zone                = var.zone
  name_prefix         = var.name_prefix
  network_name        = module.network.network_name
  tier                = var.filestore_tier
  capacity_gb         = var.filestore_capacity_gb
  share_name          = var.filestore_share_name
  deletion_protection = var.deletion_protection

  depends_on = [module.network]
}

module "gke_autopilot" {
  source = "../../modules/gke-autopilot"

  project_id                 = var.project_id
  region                     = var.region
  name_prefix                = var.name_prefix
  network_id                 = module.network.network_id
  subnet_id                  = module.network.subnet_id
  release_channel            = var.gke_release_channel
  master_authorized_networks = var.gke_master_authorized_networks
  deletion_protection        = var.deletion_protection
}

module "artifact_registry" {
  source = "../../modules/artifact-registry"

  project_id  = var.project_id
  region      = var.region
  name_prefix = var.name_prefix

  depends_on = [module.project_services]
}

# --- Destroy guardrail (design §3.10 item 4) ---
#
# GCP does not refuse to delete a Cloud SQL instance that still has hub
# databases on it (nor a Filestore instance with active clients, nor a GKE
# cluster with namespaces) — so the guardrail has to live here, not rely on
# the API to say no. To tear this stack down you must first apply with
# deletion_protection=false, and that apply fails while any hub database
# still exists on the shared instance. The protection-off state is therefore
# unreachable while hubs are present, which makes the subsequent `destroy`
# safe. A hub's database is the proxy for the whole hub: every hub root
# creates exactly one (cloudsql-database), and never manages the instance
# itself (reviewer check: google_sql_database_instance appears only in
# modules/cloudsql-instance).
data "google_sql_databases" "on_instance" {
  project  = var.project_id
  instance = module.cloudsql_instance.instance_name
}

locals {
  hub_dbs = [for d in data.google_sql_databases.on_instance.databases : d.name if d.name != "postgres"]
}

resource "terraform_data" "destroy_guard" {
  input = var.deletion_protection

  lifecycle {
    precondition {
      condition     = var.deletion_protection || length(local.hub_dbs) == 0
      error_message = "destroy_guard: hubs still exist on this shared infra (databases: ${join(", ", local.hub_dbs)}). Destroy every hub root first."
    }
  }
}

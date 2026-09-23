# Shared GKE Autopilot cluster. One cluster serves every hub, each isolated
# by its own namespace + namespaced RBAC (design §3.1, §3.4 agent-runtime-k8s).
# Workload Identity is implicit on Autopilot.

resource "google_container_cluster" "this" {
  project  = var.project_id
  name     = "${var.name_prefix}-agents"
  location = var.region

  enable_autopilot = true

  network    = var.network_id
  subnetwork = var.subnet_id

  release_channel {
    channel = var.release_channel
  }

  private_cluster_config {
    enable_private_nodes = true
  }

  secret_manager_config {
    enabled = true
  }

  dynamic "master_authorized_networks_config" {
    for_each = length(var.master_authorized_networks) > 0 ? [1] : []
    content {
      dynamic "cidr_blocks" {
        for_each = var.master_authorized_networks
        content {
          cidr_block = cidr_blocks.value
        }
      }
    }
  }

  deletion_protection = var.deletion_protection
}

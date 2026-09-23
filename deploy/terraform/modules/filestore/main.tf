# Shared Filestore Basic instance with one NFS share. Hubs get subdirectories
# under this share (created by each hub's nfs-init Job), not separate shares:
# Basic only supports one share per instance and has a 1 TiB minimum, so a
# share-per-hub would mean an instance-per-hub, defeating the cost-sharing
# rationale (design §3.1, Alt-M).

resource "google_filestore_instance" "this" {
  project  = var.project_id
  name     = "${var.name_prefix}-nfs"
  location = var.zone
  tier     = var.tier

  deletion_protection_enabled = var.deletion_protection
  deletion_protection_reason  = var.deletion_protection ? "Shared Filestore instance; hubs read it via shared-lookup. Flip deletion_protection to destroy (design §3.10)." : null

  file_shares {
    capacity_gb = var.capacity_gb
    name        = var.share_name
    # nfs_export_options left at provider default (no root squash): the
    # per-hub nfs-init Job needs root to mkdir/chown its subdirectory.
  }

  networks {
    network      = var.network_name
    modes        = ["MODE_IPV4"]
    connect_mode = "DIRECT_PEERING"
  }
}

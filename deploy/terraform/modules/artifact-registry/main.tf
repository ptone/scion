# Shared Docker repo. Every hub pulls hub_image from here, each at its own
# tag; the image build itself is out of scope (design §2, Alt-D).

resource "google_artifact_registry_repository" "this" {
  project       = var.project_id
  location      = var.region
  repository_id = "${var.name_prefix}-scion"
  format        = "DOCKER"
}

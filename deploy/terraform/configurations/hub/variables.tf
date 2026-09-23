variable "project_id" {
  description = "GCP project ID (must match the shared-infra apply)."
  type        = string
}

variable "region" {
  description = "Region the shared infra was created in (must match shared-infra)."
  type        = string
}

variable "zone" {
  description = "Zone the shared Filestore instance was created in (must match shared-infra)."
  type        = string
}

variable "shared_prefix" {
  description = "The name_prefix configurations/shared-infra was applied with. Resolves shared infra by naming convention (shared-lookup)."
  type        = string
  default     = "tfha"
}

variable "shared_share_name" {
  description = "Filestore share_name from shared-infra (module filestore's share_name, default \"scion\")."
  type        = string
  default     = "scion"
}

variable "hub_name" {
  description = "This hub's name. Every hub-scoped resource derives from it (design §3.8)."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,15}$", var.hub_name))
    error_message = "hub_name must match ^[a-z][a-z0-9-]{2,15}$ (design §3.8)."
  }
}

variable "state_prefix" {
  description = "The GCS backend prefix this apply's state actually lives at (e.g. \"tfha/hubs/tfha-h1\"), passed alongside -backend-config=\"prefix=...\" at init time. Terraform cannot read its own backend config, so this is how the check below catches applying hub_name=X's vars onto a different hub's state file."
  type        = string
}

check "state_prefix_matches_hub" {
  assert {
    condition     = endswith(var.state_prefix, "/${var.hub_name}")
    error_message = "state_prefix (\"${var.state_prefix}\") does not end with \"/${var.hub_name}\" — this looks like hub_name=${var.hub_name}'s vars are about to be applied onto a different hub's state. Re-run init with the matching -backend-config prefix, or fix -var hub_name."
  }
}

variable "hub_image" {
  description = "Artifact Registry image URI for the hub container (built and pushed after the shared-infra apply creates the repo)."
  type        = string
}

variable "iap_oauth_client_id" {
  description = "OAuth client ID prerequisite (§5.1). May be shared across hubs in this project prefix."
  type        = string
}

variable "iap_oauth_client_secret_secret_id" {
  description = "Secret Manager secret ID holding the OAuth client secret prerequisite."
  type        = string
}

variable "iap_members" {
  description = "Users/groups granted roles/iap.httpsResourceAccessor on this hub's Cloud Run service only."
  type        = list(string)
  default     = []
}

variable "admin_emails" {
  description = "Emails seeded as hub admins on first boot."
  type        = list(string)
  default     = []
}

variable "min_instances" {
  description = "Cloud Run min instance count. Phase 1 uses 1 (OQ-4)."
  type        = number
  default     = 1
}

variable "max_instances" {
  description = "Cloud Run max instance count."
  type        = number
  default     = 3
}

variable "cpu" {
  type    = string
  default = "1"
}

variable "memory" {
  type    = string
  default = "1Gi"
}

variable "timeout" {
  description = "Request timeout. 3600s for long-lived WebSockets/terminals (design §8)."
  type        = string
  default     = "3600s"
}

variable "nfs_uid" {
  type    = number
  default = 1000
}

variable "nfs_gid" {
  type    = number
  default = 1000
}

variable "nfs_subpath_root" {
  type    = string
  default = "projects"
}

variable "nfs_capacity" {
  description = "Advertised PV/PVC capacity (bookkeeping only; Filestore Basic doesn't enforce per-subdirectory quotas)."
  type        = string
  default     = "1Ti"
}

variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "project_number" {
  description = "GCP project number (shared.project_number from shared-lookup), used for the deterministic Cloud Run URL and IAP audience."
  type        = string
}

variable "region" {
  description = "Region for the Cloud Run service."
  type        = string
}

variable "hub_name" {
  description = "Hub name. The Cloud Run service, bucket, and secrets are all named/derived from this."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,15}$", var.hub_name))
    error_message = "hub_name must match ^[a-z][a-z0-9-]{2,15}$ (design §3.8)."
  }
}

variable "hub_image" {
  description = "Artifact Registry image URI for the hub container."
  type        = string
}

variable "hub_sa_email" {
  description = "Hub service account email (Cloud Run runtime identity)."
  type        = string
}

variable "transport_sa_email" {
  description = "Transport service account email (mints IAP OIDC tokens for agents)."
  type        = string
}

variable "hub_iam_grants" {
  description = "hub-identity's hub-SA IAM grant resources' .id values, passed through purely to create a depends_on ordering (design §3.5) for this module's own time_sleep.iam_propagation: the Cloud Run service must not boot before these grants have had time to propagate, or it gets a 403 with no retry. Deliberately list(string) of .id, not list(any) of the full resource objects: google_project_iam_member and google_service_account_iam_member have different attribute shapes, and a list(any) of the full objects fails type unification at this exact variable boundary with \"all list elements must have the same type\" (tf-review; reproduced credential-free with two different hashicorp/random resource types before fixing). .id still carries the same dependency edge."
  type        = list(string)
}

variable "hub_iam_condition_expression" {
  description = "hub-identity's conditioned secretmanager.admin expression, used only as a time_sleep trigger so the propagation wait re-arms if the condition ever changes (e.g. a different hub_name) rather than protecting only the very first apply."
  type        = string
}

variable "network_name" {
  description = "VPC network name for Direct VPC egress (shared.network.name)."
  type        = string
}

variable "subnet_name" {
  description = "Subnet name for Direct VPC egress (shared.network.subnet_name)."
  type        = string
}

variable "sql_connection_name" {
  description = "Cloud SQL connection name (shared.sql.connection_name)."
  type        = string
}

variable "db_name" {
  description = "Per-hub database name (cloudsql-database output)."
  type        = string
}

variable "db_user" {
  description = "Per-hub database user (cloudsql-database output)."
  type        = string
}

variable "db_password_secret_id" {
  description = "Secret Manager secret ID holding the database password (cloudsql-database output). The value is read via a data source and embedded directly in the rendered settings.yaml DSN — Alt-F's secret-env-var end state is phase 3."
  type        = string
}

variable "nfs_server" {
  description = "Filestore server IP (shared.nfs.server)."
  type        = string
}

variable "nfs_export" {
  description = "This hub's NFS export path, e.g. \"/scion/<hub_name>\" (agent-runtime-k8s output nfs_export)."
  type        = string
}

variable "nfs_mount_root" {
  description = "Local mount root inside the Cloud Run container. The NFS volume is mounted at \"<nfs_mount_root>/<hub_name>\", which must equal <mount_root>/<share.id> in settings.yaml so NFSMountReconciler takes the already-mounted path (design §3.6, OQ-1)."
  type        = string
  default     = "/mnt/nfs"
}

variable "pv_name" {
  description = "PersistentVolume/claim name for this hub's workspace share (agent-runtime-k8s output pv_name). Embedded in settings.yaml workspace_storage.nfs.shares[0].pv_name."
  type        = string
}

variable "nfs_uid" {
  description = "uid the settings.yaml workspace_storage.nfs block advertises. Single-sourced with agent-runtime-k8s's nfs_uid in the hub root — hardcoding this here separately from what actually chowns the export would silently split the two (found in review)."
  type        = number
  default     = 1000
}

variable "nfs_gid" {
  description = "gid the settings.yaml workspace_storage.nfs block advertises. Single-sourced with agent-runtime-k8s's nfs_gid in the hub root."
  type        = number
  default     = 1000
}

variable "nfs_subpath_root" {
  description = "subpath_root the settings.yaml workspace_storage.nfs block advertises. Single-sourced with agent-runtime-k8s's subpath_root in the hub root."
  type        = string
  default     = "projects"
}

variable "namespace" {
  description = "GKE namespace for this hub's agents (agent-runtime-k8s output namespace, equals hub_name)."
  type        = string
}

variable "gke" {
  description = "Shared GKE cluster coordinates for the kubeconfig secret (shared.gke: { endpoint, ca_certificate })."
  type = object({
    endpoint       = string
    ca_certificate = string
  })
}

variable "iap_oauth_client_id" {
  description = "OAuth client ID (prereq, §5.1). Used both as settings.yaml's auth.transport.oidc_audience (design §8: NOT the IAP resource path) and as google_iap_settings.access_settings.oauth_settings.client_id, binding this dedicated client to this hub's Cloud Run IAP (OQ-2, resolved: supported from provider 8.0.0 via the cloud_run-<region> resource path — unvalidated client-side, confirmed against the live API in phase 1 validation)."
  type        = string
}

variable "iap_oauth_client_secret_secret_id" {
  description = "Secret Manager secret ID holding the OAuth client secret (prereq). Read via a data source and passed to google_iap_settings.access_settings.oauth_settings.client_secret. No write-only variant exists on this attribute in 8.4.0 (checked the schema: sensitive but state-stored), so — like the DB password and session secret — it ends up in this hub's Terraform state; accepted under the same §3.9 tradeoff."
  type        = string
}

variable "iap_members" {
  description = "Users/groups granted roles/iap.httpsResourceAccessor on this hub's Cloud Run service only."
  type        = list(string)
  default     = []
}

variable "admin_emails" {
  description = "Emails seeded as hub admins on first boot (settings.yaml server.hub.admin_emails)."
  type        = list(string)
  default     = []
}

variable "min_instances" {
  description = "Cloud Run min instance count. Phase 1 uses 1 (OQ-4: multi-replica in-process broker safety is unconfirmed)."
  type        = number
  default     = 1
}

variable "max_instances" {
  description = "Cloud Run max instance count."
  type        = number
  default     = 3
}

variable "cpu" {
  description = "CPU allocation per instance."
  type        = string
  default     = "1"
}

variable "memory" {
  description = "Memory allocation per instance."
  type        = string
  default     = "1Gi"
}

variable "timeout" {
  description = "Request timeout. 3600s (not the docs' 900s) for long-lived WebSockets/terminals (design §8)."
  type        = string
  default     = "3600s"
}

variable "extra_settings" {
  description = "Escape hatch for variations to deep-merge extra settings.yaml keys. Not wired up in phase 1 (seam reserved for phase 3)."
  type        = any
  default     = {}
}

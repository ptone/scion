variable "hub_name" {
  description = "Hub name. Also the namespace name, and the base for the PV/PVC/Job names (\"<hub_name>-nfs\", \"<hub_name>-nfs-init\")."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,15}$", var.hub_name))
    error_message = "hub_name must match ^[a-z][a-z0-9-]{2,15}$ (design §3.8)."
  }
}

variable "project_id" {
  description = "GCP project ID (used to build the Workload Identity member string)."
  type        = string
}

variable "hub_sa_email" {
  description = "Hub service account email. Bound as the RBAC Role's subject (GKE maps the Google identity to an RBAC User of this name)."
  type        = string
}

variable "agent_sa_email" {
  description = "Agent service account email. Bound to the namespace's default KSA via Workload Identity."
  type        = string
}

variable "nfs" {
  description = "Shared Filestore coordinates (shared.nfs from shared-lookup): { server, share_path }."
  type = object({
    server     = string
    share_path = string
  })
}

variable "nfs_uid" {
  description = "uid the hub and agent pods run as; must match the export ownership the init Job sets."
  type        = number
  default     = 1000
}

variable "nfs_gid" {
  description = "gid the hub and agent pods run as; must match the export ownership the init Job sets."
  type        = number
  default     = 1000
}

variable "subpath_root" {
  description = "Subdirectory under the hub's NFS export that holds per-project workspaces."
  type        = string
  default     = "projects"
}

variable "capacity" {
  description = "Advertised PV/PVC capacity. Filestore Basic doesn't enforce per-subdirectory quotas, so this is a Kubernetes-API-level number, not a hard limit."
  type        = string
  default     = "1Ti"
}

variable "init_job_image" {
  description = "Image for the nfs-init Job. Needs only a shell and coreutils (mkdir, chown); busybox is sufficient and avoids depending on hub_image. Pinned by digest, not a mutable tag (found in review: a tag pulled from Docker Hub on an Autopilot node is subject to both rate limits and tag mutation). Digest is for busybox:1.36 (Docker-Content-Digest from registry-1.docker.io as of this commit); re-verify if bumping."
  type        = string
  default     = "busybox:1.36@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
}

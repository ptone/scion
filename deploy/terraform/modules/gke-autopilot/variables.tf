variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "region" {
  description = "Region for the regional Autopilot cluster."
  type        = string
}

variable "name_prefix" {
  description = "Shared-infra name prefix. The cluster is named \"<name_prefix>-agents\"."
  type        = string
  default     = "tfha"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{1,7}$", var.name_prefix))
    error_message = "name_prefix must match ^[a-z][a-z0-9]{1,7}$ (design §3.8)."
  }
}

variable "network_id" {
  description = "Self link / ID of the VPC network (module network output network_id)."
  type        = string
}

variable "subnet_id" {
  description = "Self link / ID of the subnet (module network output subnet_id)."
  type        = string
}

variable "release_channel" {
  description = "GKE release channel."
  type        = string
  default     = "REGULAR"
}

variable "master_authorized_networks" {
  description = "CIDR blocks allowed to reach the public control-plane endpoint. Empty means Google-auth-only with no extra network restriction (OQ-3 default: public endpoint)."
  type        = list(string)
  default     = []
}

variable "deletion_protection" {
  description = "GKE cluster deletion protection. This is shared infra used by every hub namespace."
  type        = bool
  default     = true
}

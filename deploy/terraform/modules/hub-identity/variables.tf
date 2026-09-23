variable "project_id" {
  description = "GCP project ID."
  type        = string
}

variable "project_number" {
  description = "GCP project number (shared.project_number from shared-lookup), used to build the IAM condition resource-name prefix for the hub SA's scoped secretmanager.admin grant."
  type        = string
}

variable "hub_name" {
  description = "Hub name. Service accounts are \"<hub_name>-hub\", \"<hub_name>-transport\", \"<hub_name>-agent\" (account_id max 30 chars, hence hub_name's 16-char max in its validation)."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,15}$", var.hub_name))
    error_message = "hub_name must match ^[a-z][a-z0-9-]{2,15}$ (design §3.8)."
  }
}

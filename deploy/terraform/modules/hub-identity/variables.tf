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

variable "agent_sa_project_roles" {
  description = "Project-level roles granted additively to the agent SA (design §3.4/§9 agent-LLM-auth gap): with Workload Identity wired but no model-access role, an agent authenticating via the GCE metadata server (GCPIdentity.MetadataMode passthrough/assign, a hub-side setting — Terraform cannot grant it) still gets 403s calling Vertex. Default grants exactly the model-call role, nothing broader — this is the same project the agent SA's WI binding already reaches, so an *.admin/editor/owner role here would be as dangerous as the project-wide secretmanager grants this module deliberately doesn't give the agent SA (see the Agent SA comment above)."
  type        = list(string)
  default     = ["roles/aiplatform.user"]

  validation {
    condition = alltrue([
      for r in var.agent_sa_project_roles :
      r != "roles/owner" && r != "roles/editor" && !can(regex("\\.admin$", r))
    ])
    error_message = "agent_sa_project_roles must not include roles/owner, roles/editor, or any role ending in .admin."
  }
}

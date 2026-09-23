variable "project_id" {
  description = "GCP project ID to create the network in."
  type        = string
}

variable "region" {
  description = "Region for the subnet."
  type        = string
}

variable "name" {
  description = "Name prefix for network resources. Every resource this module creates derives its name from this value (e.g. \"<name>-vpc\", \"<name>-subnet\", \"<name>-psa\")."
  type        = string
  default     = "scion"
}

variable "subnet_cidr" {
  description = "Primary IP range for the subnet."
  type        = string
  default     = "10.10.0.0/20"
}

variable "psa_prefix_length" {
  description = "Prefix length of the reserved range used for the private services access (PSA) VPC peering connection (Cloud SQL, Filestore)."
  type        = number
  default     = 16
}

terraform {
  # >= 1.9, not >= 1.6: the hub_name/state_prefix cross-variable validation
  # below needs 1.9's relaxed validation-block restrictions (referencing
  # another variable directly, not just resources visible after apply).
  required_version = ">= 1.9"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.4"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 8.4"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.35"
    }
  }
}

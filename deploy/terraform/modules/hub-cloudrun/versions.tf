terraform {
  required_version = ">= 1.6"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.4"
    }
    # google-beta is required for google_cloud_run_v2_service.iap_enabled
    # (beta-only in every version checked, GA never has it) and for
    # google_iap_settings.access_settings.oauth_settings.client_id/
    # client_secret, which only exist from 8.0.0 onward (confirmed empty in
    # 6.50.0/7.0.0/7.20.0, present in 8.0.0/8.4.0) — hence pinning ~> 8.4
    # for the whole module set, not just this module (tf-lead decision).
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 8.4"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

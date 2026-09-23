output "service_uri" {
  description = "Cloud Run service URI. Should equal the locally-computed deterministic URL (public_url) — a `check` block asserting that is a phase 2 item (design §3.6, §7 phase 2), not phase 1."
  value       = google_cloud_run_v2_service.hub.uri
}

output "iap_audience" {
  description = "IAP resource-path audience used in settings.yaml auth.proxy.iap.audience."
  value       = local.iap_audience
}

output "bucket_name" {
  description = "Artifacts bucket name."
  value       = google_storage_bucket.artifacts.name
}

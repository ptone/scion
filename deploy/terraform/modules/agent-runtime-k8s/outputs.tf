output "namespace" {
  description = "Namespace name (equals hub_name)."
  value       = kubernetes_namespace.this.metadata[0].name
}

output "pv_name" {
  description = "PersistentVolume name (\"<hub_name>-nfs\"). This is the value settings.yaml's workspace_storage.nfs.shares[0].pv_name must equal — the hub code uses it as the literal PVC claim name."
  value       = kubernetes_persistent_volume.this.metadata[0].name
}

output "pvc_name" {
  description = "PersistentVolumeClaim name (equals pv_name)."
  value       = kubernetes_persistent_volume_claim.this.metadata[0].name
}

output "nfs_export" {
  description = "Full NFS export path for this hub's subdirectory (\"<share_path>/<hub_name>\"), for hub-cloudrun's Cloud Run NFS volume."
  value       = "${var.nfs.share_path}/${var.hub_name}"
}

output "nfs_init_job_id" {
  description = "kubernetes_job_v1.nfs_init's own uid — a real, apply-time attribute of the Job resource itself, unlike nfs_export above (a plain string computable before the Job ever runs). Feeds hub-cloudrun's boot_prerequisites (F-106, design §9) so the Cloud Run service has a genuine implicit dependency on the Job actually finishing (wait_for_completion = true above creates the per-hub NFS subdirectory) rather than on a path string that exists in config regardless of whether the mkdir/chown ran."
  value       = kubernetes_job_v1.nfs_init.metadata[0].uid
}

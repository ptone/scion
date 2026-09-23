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

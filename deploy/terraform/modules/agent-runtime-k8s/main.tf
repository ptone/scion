# Per-hub Kubernetes resources on the shared GKE Autopilot cluster (design
# §3.4). The kubernetes provider is configured in the hub root from
# shared.gke; this module declares no provider blocks.

resource "kubernetes_namespace" "this" {
  metadata {
    name = var.hub_name
  }
}

# Minimum RBAC from kubernetes.md L169-215, cross-checked against
# pkg/runtime/k8s_runtime.go's actual API calls. Namespaced: hub A gets no
# rights in hub B's namespace (unlike the docs' project-wide
# container.developer — see design Alt-E).
resource "kubernetes_role" "hub" {
  metadata {
    name      = "${var.hub_name}-hub"
    namespace = kubernetes_namespace.this.metadata[0].name
  }

  rule {
    api_groups = [""]
    resources  = ["pods"]
    verbs      = ["create", "get", "list", "watch", "delete"]
  }

  rule {
    api_groups = [""]
    resources  = ["pods/exec"]
    verbs      = ["create"]
  }

  rule {
    api_groups = [""]
    resources  = ["pods/log"]
    verbs      = ["get"]
  }

  rule {
    api_groups = [""]
    resources  = ["secrets"]
    verbs      = ["create", "get", "list", "delete"]
  }

  rule {
    api_groups = ["secrets-store.csi.x-k8s.io"]
    resources  = ["secretproviderclasses"]
    verbs      = ["create", "get", "list", "delete"]
  }

  rule {
    api_groups = [""]
    resources  = ["persistentvolumeclaims"]
    verbs      = ["create", "get", "list", "delete"]
  }
}

# GKE maps the hub SA's Google identity to an RBAC User of the same name, so
# no project-level container.developer is needed — only this namespaced
# binding plus container.clusterViewer (granted in hub-identity).
resource "kubernetes_role_binding" "hub" {
  metadata {
    name      = "${var.hub_name}-hub"
    namespace = kubernetes_namespace.this.metadata[0].name
  }

  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "Role"
    name      = kubernetes_role.hub.metadata[0].name
  }

  subject {
    api_group = "rbac.authorization.k8s.io"
    kind      = "User"
    name      = var.hub_sa_email
  }
}

# Agent pods run as the namespace's pre-existing "default" KSA (setup-gcp.md
# annotates this same KSA, not a dedicated one) bound to the agent GSA via
# Workload Identity, implicit on Autopilot.
resource "kubernetes_annotations" "default_ksa_workload_identity" {
  api_version = "v1"
  kind        = "ServiceAccount"

  metadata {
    name      = "default"
    namespace = kubernetes_namespace.this.metadata[0].name
  }

  annotations = {
    "iam.gke.io/gcp-service-account" = var.agent_sa_email
  }

  depends_on = [kubernetes_namespace.this]
}

resource "google_service_account_iam_member" "agent_workload_identity_user" {
  service_account_id = "projects/${var.project_id}/serviceAccounts/${var.agent_sa_email}"
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${var.project_id}.svc.id.goog[${var.hub_name}/default]"
}

# The Filestore export root is root:root 0755. The per-hub subdirectory (and
# its "projects" subpath) must exist and be owned by nfs_uid:nfs_gid *before*
# Cloud Run mounts it, or the hub's Cloud Run revision fails to start.
# Mounts the share ROOT with an inline nfs volume (Autopilot allows this for
# a Job; no PV needed for the init step itself).
resource "kubernetes_job_v1" "nfs_init" {
  metadata {
    name      = "${var.hub_name}-nfs-init"
    namespace = kubernetes_namespace.this.metadata[0].name
  }

  spec {
    backoff_limit              = 3
    ttl_seconds_after_finished = 300

    template {
      metadata {
        name = "${var.hub_name}-nfs-init"
      }

      spec {
        restart_policy = "OnFailure"

        security_context {
          run_as_user  = 0
          run_as_group = 0
        }

        container {
          name    = "nfs-init"
          image   = var.init_job_image
          command = ["sh", "-c"]
          args = [
            "mkdir -p /mnt/share/${var.hub_name}/${var.subpath_root} && chown ${var.nfs_uid}:${var.nfs_gid} /mnt/share/${var.hub_name} /mnt/share/${var.hub_name}/${var.subpath_root}"
          ]

          volume_mount {
            name       = "share-root"
            mount_path = "/mnt/share"
          }
        }

        volume {
          name = "share-root"
          nfs {
            server = var.nfs.server
            path   = var.nfs.share_path
          }
        }
      }
    }
  }

  wait_for_completion = true

  timeouts {
    create = "5m"
  }
}

resource "kubernetes_persistent_volume" "this" {
  metadata {
    name = "${var.hub_name}-nfs"
  }

  spec {
    capacity = {
      storage = var.capacity
    }
    access_modes                     = ["ReadWriteMany"]
    persistent_volume_reclaim_policy = "Retain"
    storage_class_name               = ""
    mount_options                    = ["vers=3", "hard", "nconnect=4"]

    persistent_volume_source {
      nfs {
        server = var.nfs.server
        path   = "${var.nfs.share_path}/${var.hub_name}"
      }
    }
  }

  depends_on = [kubernetes_job_v1.nfs_init]
}

resource "kubernetes_persistent_volume_claim" "this" {
  metadata {
    name      = "${var.hub_name}-nfs"
    namespace = kubernetes_namespace.this.metadata[0].name
  }

  spec {
    access_modes       = ["ReadWriteMany"]
    storage_class_name = ""
    volume_name        = kubernetes_persistent_volume.this.metadata[0].name

    resources {
      requests = {
        storage = var.capacity
      }
    }
  }

  wait_until_bound = true
}

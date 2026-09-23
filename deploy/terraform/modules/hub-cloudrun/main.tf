# Hub's bucket, secrets, settings render, Cloud Run v2 service and IAP
# (design §3.4, largest module). No provider/backend blocks: the calling
# configuration configures both `google` and `google-beta`.

locals {
  # Cloud Run's deterministic URL — computed from inputs, not read back from
  # the service resource, so there is no "deploy twice" cycle (design §3.6).
  public_url   = "https://${var.hub_name}-${var.project_number}.${var.region}.run.app"
  iap_audience = "/projects/${var.project_number}/locations/${var.region}/services/${var.hub_name}"

  nfs_mount_path = "${var.nfs_mount_root}/${var.hub_name}"
}

# --- Bucket (artifacts, signed URLs) ---

resource "google_storage_bucket" "artifacts" {
  project                     = var.project_id
  name                        = "${var.project_id}-${var.hub_name}-artifacts"
  location                    = var.region
  uniform_bucket_level_access = true

  versioning {
    enabled = true
  }
}

resource "google_storage_bucket_iam_member" "hub_object_admin" {
  bucket = google_storage_bucket.artifacts.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${var.hub_sa_email}"
}

# --- Session secret ---

resource "random_id" "session_secret" {
  byte_length = 32
}

resource "google_secret_manager_secret" "session_secret" {
  project   = var.project_id
  secret_id = "${var.hub_name}-session-secret"

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "session_secret" {
  secret      = google_secret_manager_secret.session_secret.id
  secret_data = random_id.session_secret.b64_std
}

resource "google_secret_manager_secret_iam_member" "hub_reads_session_secret" {
  secret_id = google_secret_manager_secret.session_secret.secret_id
  project   = var.project_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${var.hub_sa_email}"
}

# --- Database password (created by cloudsql-database; read here to render
# the DSN directly into settings.yaml — the Alt-F secret-env-var end state
# is deferred to phase 3) ---

data "google_secret_manager_secret_version" "db_password" {
  project           = var.project_id
  secret            = var.db_password_secret_id
  fetch_secret_data = true
}

# --- Rendered settings.yaml ---

resource "google_secret_manager_secret" "settings" {
  project   = var.project_id
  secret_id = "${var.hub_name}-settings"

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "settings" {
  secret = google_secret_manager_secret.settings.id
  secret_data = templatefile("${path.module}/templates/settings.yaml.tftpl", {
    project_id          = var.project_id
    region              = var.region
    hub_name            = var.hub_name
    public_url          = local.public_url
    iap_audience        = local.iap_audience
    admin_emails        = var.admin_emails
    db_user             = var.db_user
    db_password         = data.google_secret_manager_secret_version.db_password.secret_data
    db_name             = var.db_name
    sql_connection_name = var.sql_connection_name
    bucket              = google_storage_bucket.artifacts.name
    iap_oauth_client_id = var.iap_oauth_client_id
    transport_sa_email  = var.transport_sa_email
    nfs_mount_root      = var.nfs_mount_root
    nfs_server          = var.nfs_server
    nfs_export          = var.nfs_export
    pv_name             = var.pv_name
    namespace           = var.namespace
    nfs_uid             = var.nfs_uid
    nfs_gid             = var.nfs_gid
    nfs_subpath_root    = var.nfs_subpath_root
  })
}

resource "google_secret_manager_secret_iam_member" "hub_reads_settings" {
  secret_id = google_secret_manager_secret.settings.secret_id
  project   = var.project_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${var.hub_sa_email}"
}

# --- Kubeconfig (endpoint + CA only; the hub authenticates with its ambient
# Google token, not a static credential — setup-gcp.md L237-260's form,
# verbatim, including the gke-gcloud-auth-plugin exec stanza: the plugin is
# absent in the image, so the Go k8s client falls back to GCE metadata auth) ---

resource "google_secret_manager_secret" "kubeconfig" {
  project   = var.project_id
  secret_id = "${var.hub_name}-kubeconfig"

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "kubeconfig" {
  secret = google_secret_manager_secret.kubeconfig.id
  secret_data = templatefile("${path.module}/templates/kubeconfig.yaml.tftpl", {
    cluster_name   = var.hub_name
    endpoint       = var.gke.endpoint
    ca_certificate = var.gke.ca_certificate
  })
}

resource "google_secret_manager_secret_iam_member" "hub_reads_kubeconfig" {
  secret_id = google_secret_manager_secret.kubeconfig.secret_id
  project   = var.project_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${var.hub_sa_email}"
}

# --- IAM propagation guard (design §3.5, from OQ-8's ~70s measurement) ---
#
# IAM changes are eventually consistent, and the hub creates its
# scion-hub-<h12>-* signing keys AT BOOT. A service created immediately
# after its IAM would crash-loop with 403s on the very first apply. This is
# purely a timer: it depends on every hub-SA IAM grant (hub-identity), waits,
# and the Cloud Run service below depends on it. triggers include the
# condition expression, so the sleep re-arms if that condition is ever
# changed (e.g. a different hub) — a create-only sleep would protect only
# the first apply and silently stop protecting a later condition change.
# See the README troubleshooting note: a 403 on scion-hub-<h12>-... shortly
# after a first apply is IAM propagation. Re-apply. Do NOT widen the
# condition to work around it.
resource "time_sleep" "iam_propagation" {
  create_duration = "120s"

  triggers = {
    condition = var.hub_iam_condition_expression
    hub_sa    = var.hub_sa_email
  }

  # §3.5 says ALL hub-SA IAM members, not just hub-identity's project-level
  # ones: Cloud Run checks secret access at revision *create* time, so this
  # module's own per-secret accessor grants (settings/kubeconfig/session
  # secret — all three read directly by the running container) matter just
  # as much as the project-level grants passed in from hub-identity.
  # cloudsql-database's db-password accessor is deliberately NOT included:
  # the DSN is embedded directly into the rendered settings secret by
  # Terraform's own identity (the data source above), so the running
  # container never reads db-password itself — nothing to wait on there.
  depends_on = [
    var.hub_iam_grants,
    google_secret_manager_secret_iam_member.hub_reads_settings,
    google_secret_manager_secret_iam_member.hub_reads_kubeconfig,
    google_secret_manager_secret_iam_member.hub_reads_session_secret,
  ]
}

# --- Cloud Run v2 service ---
# provider = google-beta: iap_enabled requires it (see versions.tf).

resource "google_cloud_run_v2_service" "hub" {
  provider = google-beta

  project              = var.project_id
  name                 = var.hub_name
  location             = var.region
  ingress              = "INGRESS_TRAFFIC_ALL"
  iap_enabled          = true
  invoker_iam_disabled = false # never disable (deploy.sh L145)
  launch_stage         = "GA"
  deletion_protection  = false # this is a hub, not shared infra; hub destroy must be able to remove it

  template {
    service_account       = var.hub_sa_email
    execution_environment = "GEN2" # required for NFS volumes
    session_affinity      = true
    timeout               = var.timeout

    scaling {
      min_instance_count = var.min_instances
      max_instance_count = var.max_instances
    }

    vpc_access {
      egress = "PRIVATE_RANGES_ONLY"
      network_interfaces {
        network    = var.network_name
        subnetwork = var.subnet_name
      }
    }

    containers {
      image = var.hub_image

      resources {
        cpu_idle          = false
        startup_cpu_boost = true
        limits = {
          cpu    = var.cpu
          memory = var.memory
        }
      }

      env {
        name  = "HOME"
        value = "/home/scion"
      }
      env {
        name  = "SCION_REQUIRE_STABLE_SIGNING_KEY"
        value = "true"
      }
      env {
        name  = "KUBECONFIG"
        value = "/etc/scion/kubeconfig.yaml"
      }
      env {
        name  = "SCION_K8S_NAMESPACE"
        value = var.namespace
      }
      env {
        name = "SCION_SERVER_SESSION_SECRET"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.session_secret.secret_id
            version = "latest"
          }
        }
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }
      # mount_path is the parent directory; the secret's item path below
      # supplies the filename, so the resulting file lands at exactly
      # /run/secrets/settings.yaml — the literal file entrypoint.sh checks
      # for (`if [ -f /run/secrets/settings.yaml ]`), not a directory of
      # that name. Mounting mount_path=/run/secrets/settings.yaml directly
      # would create a directory there instead, breaking that check.
      volume_mounts {
        name       = "settings"
        mount_path = "/run/secrets"
      }
      volume_mounts {
        name       = "kubeconfig"
        mount_path = "/etc/scion"
      }
      volume_mounts {
        name       = "nfs"
        mount_path = local.nfs_mount_path
      }

      startup_probe {
        http_get {
          path = "/readyz"
          port = 8080
        }
        # Sized for Autopilot cold scale-up (5-10 min for the first agent
        # pod is a separate concern; this is the hub's own boot, which is
        # much faster, but we still allow generous headroom for a cold
        # Cloud SQL/Filestore mount on first revision).
        initial_delay_seconds = 5
        period_seconds        = 5
        timeout_seconds       = 3
        failure_threshold     = 30
      }
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [var.sql_connection_name]
      }
    }
    volumes {
      name = "settings"
      secret {
        secret = google_secret_manager_secret.settings.secret_id
        items {
          path    = "settings.yaml"
          version = "latest"
        }
      }
    }
    volumes {
      name = "kubeconfig"
      secret {
        secret = google_secret_manager_secret.kubeconfig.secret_id
        items {
          path    = "kubeconfig.yaml"
          version = "latest"
        }
      }
    }
    volumes {
      name = "nfs"
      nfs {
        server = var.nfs_server
        path   = var.nfs_export
      }
    }
  }

  lifecycle {
    ignore_changes = [client, client_version]
  }

  # Explicit ordering (tf-review B2): nothing in the attributes above
  # actually links the service to the secret *versions* or the hub SA's
  # *IAM* propagating — only to the secret resources' IDs, which exist as
  # soon as the (empty) secret is created, version or no version, IAM or no
  # IAM. A revision that boots before its settings/kubeconfig version exists
  # or before the hub SA can read them fails with no retry.
  depends_on = [
    google_secret_manager_secret_version.settings,
    google_secret_manager_secret_version.kubeconfig,
    google_secret_manager_secret_version.session_secret,
    google_secret_manager_secret_iam_member.hub_reads_settings,
    google_secret_manager_secret_iam_member.hub_reads_kubeconfig,
    google_secret_manager_secret_iam_member.hub_reads_session_secret,
    time_sleep.iam_propagation,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "iap_invoker" {
  provider = google-beta
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.hub.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:service-${var.project_number}@gcp-sa-iap.iam.gserviceaccount.com"
}

resource "google_cloud_run_v2_service_iam_member" "transport_invoker" {
  provider = google-beta
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.hub.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${var.transport_sa_email}"
}

# --- IAP settings: bind the dedicated OAuth client (OQ-2) ---
#
# google_iap_settings.name has no client-side validation of its resource-path
# patterns (empirically confirmed: `terraform validate`/`plan` accept an
# arbitrary string there), and its documented pattern list never mentions a
# Cloud Run form — only organizations/folders/projects/iap_web/compute*/
# appengine*. But scripts/cloudrun/deploy.sh L292-306 successfully drives this
# exact API via `gcloud iap settings set --resource-type=cloud-run`, whose
# resource name is projects/<num>/iap_web/cloud_run-<region>/services/<svc>.
# access_settings.oauth_settings.client_id/client_secret only exist in the
# provider from 8.0.0 (absent in 6.x/7.x) — the reason the whole module set
# is pinned to ~> 8.4. Whether the *API* accepts this name for a Cloud Run
# service (as opposed to just the Terraform schema accepting the string) is
# unverified without real credentials: phase 1 validation check 2/3 (IAP
# login, transport token) is the empirical test. If apply rejects it, this
# is the point to stop and report — no local-exec fallback (tf-lead/ptone
# decision record: conv:5e2bb789-a863-45ec-a45c-b221904f7e1b).
data "google_secret_manager_secret_version" "iap_oauth_client_secret" {
  project           = var.project_id
  secret            = var.iap_oauth_client_secret_secret_id
  fetch_secret_data = true
}

resource "google_iap_settings" "hub" {
  provider = google-beta
  name     = "projects/${var.project_number}/iap_web/cloud_run-${var.region}/services/${var.hub_name}"

  access_settings {
    oauth_settings {
      client_id     = var.iap_oauth_client_id
      client_secret = data.google_secret_manager_secret_version.iap_oauth_client_secret.secret_data
    }
  }

  depends_on = [google_cloud_run_v2_service.hub]
}

resource "google_iap_web_cloud_run_service_iam_member" "members" {
  for_each               = toset(var.iap_members)
  project                = var.project_id
  location               = var.region
  cloud_run_service_name = google_cloud_run_v2_service.hub.name
  role                   = "roles/iap.httpsResourceAccessor"
  member                 = each.value
}

# Transport SA's IAP accessor, scoped to this hub's own service only (design
# §3.4 — moved here from hub-identity's project-wide grant, which reached
# every IAP-protected resource in the project including the live hubs).
resource "google_iap_web_cloud_run_service_iam_member" "transport" {
  project                = var.project_id
  location               = var.region
  cloud_run_service_name = google_cloud_run_v2_service.hub.name
  role                   = "roles/iap.httpsResourceAccessor"
  member                 = "serviceAccount:${var.transport_sa_email}"
}

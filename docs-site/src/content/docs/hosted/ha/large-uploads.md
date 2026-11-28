---
title: Large File Uploads
description: Enable, size, and operate HA-safe resumable chunked web uploads up to 1 GiB for chat attachments, project workspaces, and shared directories.
---

Resumable chunked web uploads let browser users upload files up to **1 GiB** as chat attachments, project workspace files, and shared-directory files without buffering whole files in Hub RAM or `TMPDIR`, and without breaking across Hub replicas or rolling updates.

This feature is gated by the `hub.large_uploads` experiment (off by default) and applies to both [Single-node hosted](/scion/hosted/single-node/overview/) and [HA hosted](/scion/hosted/ha/overview/) deployments (as well as workstation servers).

## Prerequisites and enabling

1. **Enable the experiment.** Turn on `hub.large_uploads` in **Admin → Server Config → Experiments**, or via the CLI:

   ```bash
   scion hub experiments enable hub.large_uploads
   ```

2. **Chat attachments additionally require artifact storage with `Composer` support.**
   - Enable `hub.attachment_artifacts` (and ensure artifact storage is configured). While `hub.attachment_artifacts` is off or unready, starting a large chat upload returns `409` (`artifact_attachments_required`).
   - The Hub's configured blob store must implement `storage.Composer` — **GCS** (`gcs`) or **local storage** (`local`). A backend without compose support returns `501` (`large_uploads_unsupported_storage`).
   - Workspace and shared-directory uploads stage directly on the target filesystem (NFS or local disk) and do not require a blob store. Remote-broker-only linked projects continue to return `409`.

While `hub.large_uploads` is off, all `.../uploads` routes answer `404` (`experiment_disabled`) and the Web UI keeps its existing single-request multipart upload path. Turning `hub.large_uploads` off mid-flight fails in-flight sessions on their next request with `404` (`experiment_disabled`), while already completed files remain intact and the sweeper cleans up abandoned staging.

## Raising per-file limits for 1 GiB

Enabling `hub.large_uploads` does not automatically raise per-file size caps. Raise the target limits under **Admin → Server Config** (or `PUT /api/v1/admin/server-config`) to the size you want:

### Workspace and shared-directory files

- **`server.hub.uploads.max_workspace_file_size`**:
  - **Default:** `52428800` (50 MiB)
  - **Maximum:** `1073741824` (1 GiB)
- The legacy single-request multipart workspace upload route remains clamped at `104857600` (100 MiB) regardless of `max_workspace_file_size`. Files larger than `chunk_bytes` (including everything above 100 MiB) use the chunked upload protocol when `hub.large_uploads` is enabled.

### Chat attachments

- Artifact-backed chat attachments use the **`artifacts`** operational settings section:
  - **`artifacts.max_file_bytes`**: default `33554432` (32 MiB)
  - **`artifacts.max_bundle_bytes`**: default `268435456` (256 MiB)
- To allow 1 GiB chat attachments, raise **both** `artifacts.max_file_bytes` and `artifacts.max_bundle_bytes` (for example, both to `1073741824`, or set `max_bundle_bytes` ≥ `max_file_bytes`). Saving `max_file_bytes > max_bundle_bytes` is rejected by validation.
- `server.hub.uploads.max_attachment_size` (default `10485760` = 10 MiB, max `104857600` = 100 MiB) applies only to the legacy non-artifact attachment store and does not cap artifact-backed large chat uploads.

## Chunking and quota settings (`server.hub.uploads`)

All `server.hub.uploads.*` keys are live Layer-1 operational settings stored in the Hub database and applied across replicas without a restart:

| Key | Default | Valid range / rule | Notes |
| :--- | :--- | :--- | :--- |
| `server.hub.uploads.chunk_bytes` | `8388608` (8 MiB) | `1048576` (1 MiB) – `16777216` (16 MiB) | Maximum body size of each chunk `PUT` and threshold for switching from single-request to chunked uploads. |
| `server.hub.uploads.session_idle_ttl` | `1h` | `5m` – `24h` | Idle expiry window; advanced only on start and on a successful chunk commit (refused calls and status `GET` never extend it). |
| `server.hub.uploads.session_max_lifetime` | `24h` | ≥ `session_idle_ttl`, ≤ `7d` (`168h`) | Hard lifetime cap from session start; never extended. Accepts Go durations (`24h`, `168h`) and whole-day shorthands (`7d`). |
| `server.hub.uploads.max_sessions_per_user` | `4` | `1` – `64` | Maximum concurrent active (`uploading` / `finalizing`) sessions per user. |
| `server.hub.uploads.max_pending_bytes_per_user` | `2147483648` (2 GiB) | ≥ `1` byte | Sum of `declared_size` across a user's active sessions. Setting it below the largest effective per-file limit returns a save warning (not an error). |
| `server.hub.uploads.max_pending_bytes_hub` | `17179869184` (16 GiB) | ≥ `1` byte and ≥ `max_pending_bytes_per_user` | Sum of `declared_size` across all non-completed sessions holding staging on the Hub. |

Exceeding a session count or pending-bytes quota returns `429` (`upload_quota_exceeded`) with `Retry-After: 60`.

### 1 MiB non-final chunk floor

Every non-final chunk (`offset + contentLength < declaredSize`) must be at least `min(1 MiB, chunk_bytes)` — `1048576` bytes for any valid `chunk_bytes` setting. Smaller non-final chunks are rejected with `400` (`chunk_too_small`). Only the final chunk may be shorter. For chat attachments, the Hub also enforces a defensive part-count ceiling of `ceil(declaredSize / minChunk) + 1` (`400` `too_many_parts`).

## Reverse proxy body limits

- Every chunk `PUT` body is at most `server.hub.uploads.chunk_bytes` (default **8 MiB**, hard maximum **16 MiB**).
- The 16 MiB ceiling leaves 2× headroom under Cloud Run's 32 MiB HTTP/1.1 request body limit.
- If a front proxy (such as nginx or an enterprise ingress) enforces a smaller body limit down to 1 MiB and returns `413`, the Web UI automatically halves its chunk size (down to the 1 MiB floor) and retries from the server's committed offset.
- For the fewest retries behind a proxy with a small limit (for example `client_max_body_size 4m`), set `server.hub.uploads.chunk_bytes` at or below that proxy limit.

## Staging storage and lifecycle cleanup

- **Chat attachments (object storage):** chunks are staged under the top-level prefix `uploads-staging/{hub_id}/{session_id}/` in the Hub's artifacts bucket, then server-side composed into the content-addressed blob at finalize.
- **Workspaces and shared directories (filesystem):** bytes are written to `.scion-upload-{session_id}.partial` in the target's parent directory and atomically renamed into place on completion, so failed or aborted uploads never truncate an existing file.
- **Hub sweeper:** the singleton job `upload-session-sweep` runs every 1 minute (regardless of whether `hub.large_uploads` is enabled) to expire idle or hard-expired sessions, mark stuck finalizing sessions failed, and delete staged objects/partials and terminal session rows after a 15-minute replay retention window.
- **GCS bucket lifecycle rule (backstop):** both the `hub-cloudrun` and `hub-gke` Terraform modules configure a lifecycle rule on the artifacts bucket that deletes objects under `uploads-staging/` after **2 days**:
  - `action.type = "Delete"`
  - `condition.age = 2`
  - `condition.with_state = "ANY"`
  - `condition.matches_prefix = ["uploads-staging/"]`

  If you manage your GCS artifacts bucket outside the Scion Terraform modules, add an equivalent lifecycle rule so orphaned staging objects are reclaimed even if a Hub is offline. No bucket CORS configuration and no Cloud Run memory or GKE ephemeral-storage bump are needed.

## Mixed-version rollout note

:::caution[Upgrade all replicas before saving new `uploads.*` keys]
Do **not** save any of the new chunking or quota keys (`chunk_bytes`, `session_idle_ttl`, `session_max_lifetime`, `max_sessions_per_user`, `max_pending_bytes_per_user`, `max_pending_bytes_hub`) in **Admin → Server Config** or `PUT /api/v1/admin/server-config` until **every** Hub replica is running a release that supports them. Older replicas reject unknown fields when decoding the `uploads` operational settings document.
:::

During a rolling update while some replicas do not yet serve the `.../uploads` routes, an older replica returns `404`; the Web UI retries `404` responses on active chunk/status requests for up to 60 seconds so in-flight uploads can ride out the rollout once all replicas are updated.

## Agent attachment delivery cap

Raising `artifacts.max_file_bytes` lets users upload and view large chat attachments in the Web UI, but delivering attachments into an agent's inbox remains governed by **`messaging.attachment_delivery_max_bytes`**:

- **Default:** `33554432` (32 MiB) total attachment bytes per message sent to an agent.
- Setting `messaging.attachment_delivery_max_bytes` to `0` disables attachment delivery to agents.
- Messages whose total attachments exceed `messaging.attachment_delivery_max_bytes` are refused when addressed to an agent, even if the attachment uploaded successfully to the chat attachment store.

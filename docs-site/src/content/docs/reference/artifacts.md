---
title: Artifacts
description: The scion artifact command and the /api/v1/artifacts routes for publishing files with stable references.
---

:::caution[Experimental]
Artifacts are behind the `hub.artifacts` experiment, which is **off by default**. While it is off, or when the hub's `artifacts` settings section is disabled, every `/api/v1/artifacts` route answers `404` and the web UI shows no artifact pages. An admin enables it under **Admin → Server Config → Experiments** (see [Experiments](/scion/reference/experiments/)). Artifacts require Hub mode.
:::

An **artifact** is a published file with a stable reference, `scion://artifact/<id>`, that works from any runtime broker, in any project the reader can access, and in the web UI. The hub stores the bytes, so a reader never needs access to the publisher's filesystem or shared directories.

This page covers what is available today: publishing a single file, fetching it, and the artifact page. Versions, bundles, message references, review and share links are planned.

## Ownership and access

- An artifact is **owned** by the agent or user that published it (recorded by stable id, never by name). The owner can always read it.
- It is **homed** in a project: an agent's own project, or the project a user publishes into. Members who may read in that project may read the artifact.
- Anyone else gets `404` — the same answer as for an artifact that does not exist, so responses do not reveal whether an artifact exists.
- Deleting the project does not delete its artifacts; the owner keeps access.

## `scion artifact`

Available in every CLI mode, including agent mode.

### `scion artifact publish <file>`

Publishes one file as a new artifact and prints its reference and the URL of its web page. An agent's artifact is homed in the agent's project; a user's in the hub project the current checkout is linked to.

```text
$ scion artifact publish design.md --title "Artifact system design"
scion://artifact/5f1c2d3e-6b1a-4c55-9f3e-0d6e7a1b2c3d  (v1)
https://hub.example.com/projects/<project-id>/artifacts/5f1c2d3e-6b1a-4c55-9f3e-0d6e7a1b2c3d
```

- `--title <title>`: Artifact title. Default: the file name.

The file must be a regular file no larger than the hub's per-file limit (32 MiB by default, setting `artifacts.max_file_bytes`). The CLI sends the file's SHA-256, and the hub rejects an upload that does not match it.

If the file is Markdown, the hub fetches the remote images it references while publishing (see [Images in Markdown artifacts](#images-in-markdown-artifacts)). An image that could not be fetched does not fail the publish; the CLI prints a warning for it on stderr.

### `scion artifact get <ref>`

Fetches an artifact's entry file and writes it to stdout.

```text
$ scion artifact get scion://artifact/5f1c2d3e-6b1a-4c55-9f3e-0d6e7a1b2c3d > design.md
$ scion artifact get scion://artifact/5f1c2d3e-6b1a-4c55-9f3e-0d6e7a1b2c3d@1 --out ./docs/
```

- `<ref>`: `scion://artifact/<id>`, `scion://artifact/<id>@<seq>` for a specific version, or a bare `<id>`.
- `--out`, `-o <path>`: Write to this file instead of stdout. If the path is an existing directory, the file is written into it under its own name. The file is replaced atomically.

When it fetches the current version, `get` checks the bytes against the SHA-256 the hub recorded at publish time before writing anything, to stdout or to `--out`, and fails on a mismatch.

## Web page

`/projects/<project-id>/artifacts/<id>` shows the artifact's title, owner, version and reference, and renders the entry file: Markdown as formatted text (in a sandboxed frame; see [Images in Markdown artifacts](#images-in-markdown-artifacts)), text and code (including JSON, YAML, CSV) in a read-only editor, and PNG, JPEG, GIF and WebP images inline. Other types (including HTML, SVG and PDF) are offered as a download.

## Images in Markdown artifacts

Images in a Markdown artifact are fetched at publish time and served from the hub, so opening an artifact never makes the hub or the reader's browser contact another server.

- **Remote images.** When a Markdown file is published, the hub fetches each absolute `https` image it references (`![alt](https://...)`, reference-style images, and `<img src="https://...">` written inline in text or on a line of its own) once, and stores it in the version as a file at `_remote/<sha256 of the URL>`. Images in code blocks are ignored. Only PNG, JPEG, GIF and WebP images are kept; SVG and other types are not. An image that cannot be fetched (for example a non-`https` URL, an address the hub does not fetch from, a timeout, a missing image or another type) is recorded as failed, the publish still succeeds, and the response and the CLI list one warning per such image. A later publish fetches again.
- **Relative images.** `![alt](img/chart.png)` refers to a file of the same version.
- **Rendering.** The web page shows Markdown in a sandboxed frame that loads images only from the hub: remote images from their `_remote/` copy, relative images from the version's files. Any other image source, and any image that failed to fetch, is shown as a placeholder with its alt text.
- **Reading a remote image.** `GET /api/v1/artifacts/{id}/versions/{seq}/files/_remote/<hash>` follows the same access checks as any other file. A fetched image is served with its detected type; a failed one answers `404` with the header `X-Artifact-Remote-Status: failed`. The version's file manifest lists each remote image with `origin: "remote"`, its `sourceUrl` and its `fetchStatus` (`ok` or `failed`).
- **Reserved names.** Files may not be published under `_remote/`.

Settings (in the `artifacts` section): `remote_images_enabled` (default `true`), `remote_image_max_count` (images fetched per version, default `32`; further images are not fetched and get one warning), `remote_image_max_bytes` (per image, default 5 MiB, at most `max_file_bytes`), `remote_image_fetch_timeout_s` (per image, default `10`) and `remote_image_total_budget_s` (all images of one version, default `30`, between the fetch timeout and 60 seconds, since images are fetched while the publish request is open). `remote_image_max_count` may not exceed `max_files`.

## API

All routes are under `/api/v1/artifacts` and use the hub's usual authentication (session, user access token or agent token). Errors use the hub's JSON error envelope.

| Method and path | Purpose |
| :--- | :--- |
| `POST /api/v1/artifacts?name=<file>[&title=<title>][&scope=<project-id>]` | Publish the raw request body as a new single-file artifact. `scope` defaults to the caller's project (agents); users must set it. Optional header `X-Content-SHA256` (hex) is verified. Returns `201` with the artifact, its first version and any publish `warnings`. |
| `GET /api/v1/artifacts/{id}` | The artifact and its current version, including the file manifest (`path`, `size`, `sha256`, `mediaType`, and for remote images `origin`, `sourceUrl` and `fetchStatus`; see [Images in Markdown artifacts](#images-in-markdown-artifacts)). |
| `GET /api/v1/artifacts/{id}/files/{path}` | A file of the current version. |
| `GET /api/v1/artifacts/{id}/versions/{seq}/files/{path}` | A file of version `seq`. |

Status codes: `400` for a malformed request, `401` unauthenticated (also returned on publish to an agent token without `project:artifact:read`; reads answer `404`), `403` when the caller may not publish in the scope, `404` for an absent or unreadable artifact, `413` when the file exceeds `artifacts.max_file_bytes` (rejected before anything is stored), `503` when the hub has no artifact storage configured.

**File delivery.** On a hub with local storage the hub streams the bytes. On a hub with object storage (GCS) it answers `302` to a short-lived signed URL; add `?stream=1` to have the hub serve the bytes itself (the web page does this for text). Either way the response carries `Content-Disposition` (`inline` only for plain text, Markdown, CSV, TSV, JSON, YAML, TOML and raster images; `attachment` otherwise) and `X-Content-Type-Options: nosniff`; streamed responses also carry a sandboxing `Content-Security-Policy` and an `ETag`.

**Permissions.** Reading checks `artifact.read` in the artifact's home project; publishing checks `artifact.create` in the target project. Project owners, admins and members hold both for their project; changing, deleting or sharing an existing artifact is left to its owner. Agent tokens carry them through the `project:artifact:read` and `project:artifact:write` scopes: `readonly`, `baseline` and `full` agents can read, `baseline` and `full` agents can publish, and no agent can delete artifacts or manage their grants. An agent token without `project:artifact:read` gets `404` for every artifact, even one shared with it directly. A user access token is limited by its own project boundary and permissions; removing a user from a project does not by itself stop such a token from reading artifacts the user owns or was given access to, so revoke the token for an immediate cut-off. Artifact access uses dedicated agent scopes; agents created from a credential issued before artifacts existed need to be recreated from a current credential before they can use artifacts.

# Release Notes (Feb 19, 2026)

This period represented a major architectural shift, consolidating the web server into a single Go binary, removing dependencies like NATS and Koa, and introducing hub-first remote workspaces via Git.

## ⚠️ BREAKING CHANGES
* **Secrets Management:** The system now strictly requires a configured production secret backend (e.g., `gcpsm`) for any secret Set operations across user, grove, and runtime broker scopes. Plaintext fallbacks have been removed. Read, list, and delete operations remain functional locally to support data migration.
* **Server Architecture:** The Node.js Koa server and NATS message broker dependencies have been completely retired. The Scion Hub now natively handles web frontend serving, SPA routing, and Server-Sent Events (SSE) via a consolidated Go binary.

## 🚀 Features
* **Hub-First Git Workspaces:** Implemented end-to-end support for creating remote workspaces directly from Git URLs. This integration enables git clone mode across `sciontool init` and the runtime broker pipeline.
* **Web Server & Auth Integration:** Introduced native session management and OAuth routing within the Go web server, alongside a new EventPublisher for real-time SSE streaming.
* **Telemetry & Settings:** Added telemetry injection to the `v1` settings schema. Telemetry configuration now supports hierarchical merging and is automatically bridged into the agent container's environment variables.
* **CLI Additions:** Introduced the `scion look` command for non-interactive terminal viewing. Project initialization now automatically sets up template directories and requires a global grove.

## 🐛 Fixes
* **Lifecycle Hooks:** Relocated the cleanup handler to container lifecycle hooks to guarantee reliable execution upon container termination.
* **Settings Overrides:** Fixed configuration parsing to ensure environment variable overrides are correctly applied when loaded from `settings.yaml`.
* **CLI Defaults:** Ensured the `update-default` command consistently targets the global grove, and introduced a new `--force` flag.
* **Frontend Assets:** Resolved static asset serving issues by removing an erroneous `StripPrefix` in the router, and fixed client entry point imports.

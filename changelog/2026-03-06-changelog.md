# Release Notes (Mar 6, 2026)

This release introduces Just-In-Time (JIT) agent configuration, an advanced agent creation interface, and native GCP telemetry integration, while centralizing profile management at the global level.

## ⚠️ BREAKING CHANGES
* **Global Profile Management:** Runtime `profiles` and `runtimes` are no longer supported in grove-level `settings.yaml`. These must now be managed exclusively at the global/broker level (`~/.scion/settings.yaml`). Existing grove-specific profiles must be migrated to the global configuration.

## 🚀 Features
* **Just-In-Time (JIT) Agent Configuration:** Completed Phases 1 & 2 of the inline agent configuration refactor. Agents now support dynamic, late-bound configuration overrides at runtime, enabling more flexible and adaptive agent behavior.
* **Advanced Agent Creation Form:** Launched a comprehensive advanced configuration interface in the Web UI. This allows for granular control over agent parameters, including model selection, resource limits, and specific harness settings during creation.
* **GCP-Native Telemetry Integration:** Introduced native support for Google Cloud Trace and Cloud Logging telemetry exporters. The system now automatically detects GCP credentials and configures the appropriate exporter mode, facilitating seamless observability in Google Cloud environments.
* **Enhanced Developer Workflow:** Improved the developer experience with automated mounting of the `sciontool` binary and a dedicated `SCION_DEV_BINARIES` directory, enabling rapid iteration and testing of local changes within agent containers.
* **Branding & UI Refresh:** Updated the application branding with a new seedling logo and favicon, and added detailed visibility of the resolved harness authentication method in the agent detail view.
* **Local Networking Automation:** Automated the computation of the `ContainerHubEndpoint` for Podman and Docker when running in combined hub-broker mode, simplifying local setup and networking.

## 🐛 Fixes
* **Telemetry & Auth Propagation:** Resolved several issues where telemetry settings, harness authentication, and configuration overrides were not consistently propagated through all broker and agent startup paths.
* **Agent Lifecycle Stability:** Fixed a bug where provisioning agents were not correctly cleaned up after an aborted environment-gathering session.
* **Claude Harness Authentication:** Corrected Vertex AI authentication detection for the Claude harness when using file-based secrets.
* **Data Integrity:** Fixed a bug in the advanced agent creation form where the applied configuration was not correctly populated with resolved values.

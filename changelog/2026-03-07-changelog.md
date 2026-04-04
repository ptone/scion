# Release Notes (Mar 7, 2026)

This release marks a major leap in agent observability with the launch of the Cloud Log Viewer and structured messaging pipeline. It also introduces significant UI overhauls for agent management, enhanced GCP integration, and a new workstation-class daemon mode for the Scion server.

## 🚀 Features
* **Cloud Log Viewer & Structured Messaging (Phases 1-5):** Completed the end-to-end implementation of the Cloud Log Viewer and structured message pipeline. This includes a new Hub API for log retrieval, a dedicated "Messages" tab in the Web UI, and a multi-stage message broker adapter for reliable delivery and external notifications.
* **Agent Detail UI Overhaul:** Re-architected the agent detail page into a high-density tabbed layout featuring dedicated "Status", "Configuration", and "Messages" tabs. Added a new telemetry configuration card, breadcrumb navigation improvements, and a back button for the configuration flow.
* **Workstation & Daemon Management:** Introduced a workstation-optimized "daemon" mode for `scion server`. This allows the server to run as a persistent background process with integrated lifecycle management, simplified configuration, and automated combined-server detection for local brokers.
* **GCP & Metrics Integration:** Enhanced Google Cloud visibility with a native Cloud Monitoring exporter, trace-log correlation across logging pipelines, and automated injection of `SCION_GROVE_ID` and GCP labels (agent/grove) into all log streams.
* **Image Management & Build Automation:** Consolidated image build scripts and introduced support for custom `image_registry` settings. Added GitHub Actions workflows for automated building and delivery of Scion harness images.
* **Security & Authorization Hardening:** Strengthened the security posture by enforcing per-agent authorization for workspace routes, mandatory read authorization for all resource endpoints, and nonce-based HMAC validation for broker communication.
* **First-Run Experience:** Added a new `scion install` command and a streamlined first-run experience to simplify initial project setup and dependency verification.
* **Bulk Operations:** Added a "Stop All" button to the Web UI for efficient bulk shutdown of all agents within a grove.
* **Harness Capability Gating:** Introduced capability-based gating for advanced agent configuration, ensuring only supported features are exposed based on the selected harness.

## 🐛 Fixes
* **UI Performance & Reliability:** Optimized the agent detail page by parallelizing API fetches and eliminating redundant data loads. Resolved rendering issues in the messages tab and added handling for null entries in message logs.
* **Auth & Environment Injection:** Fixed multiple issues with environment variable and profile injection, specifically resolving signing errors in combined-server mode and ensuring profile variables are applied before auth overlays.
* **Runtime & Broker Stability:** Improved Podman error handling and force-deletion reliability. Fixed a bug where `agent-limits.json` lacked correct permissions after creation and ensured `InlineConfig` is correctly propagated during agent restarts.
* **Logging Precision:** Established a dedicated HTTP request log stream using the standard `HttpRequest` format and removed misleading debug logs when running in GCP-native mode.
* **Build System:** Fixed a race condition in `make all` by ensuring web assets are fully built before the Go binary compilation begins.

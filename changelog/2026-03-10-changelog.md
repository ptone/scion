# Release Notes (Mar 10, 2026)

This release focuses on streamlining system administration and enhancing visibility into agent operations. It introduces a comprehensive Web-based server configuration editor and a native runtime profile selector for agent creation, alongside critical improvements to telemetry reliability and Hub connectivity.

## 🚀 Features
* **Web Admin Server Configuration Editor:** Launched a full-featured settings editor at `/admin/server-config` (admin-only). This allows administrators to view and modify the global `settings.yaml` through the Web UI with support for tabbed navigation, sensitive field masking, and hot-reloading of key settings like log levels, telemetry defaults, and admin emails.
* **Runtime Profile Selector:** Added a dynamic profile selector to the agent creation form. After selecting a broker, users can now choose from the available runtime profiles defined on that broker, simplifying execution environment selection.
* **Standardized Issue & Feedback Templates:** Introduced official bug report and feature request templates to the repository to improve the quality and consistency of community contributions.

## 🐛 Fixes
* **Telemetry Configuration Reliability:** Corrected an issue where the telemetry opt-in checkbox on the agent configuration page wouldn't correctly reflect the global settings defaults.
* **Hub Connectivity Precision:** Enhanced agent startup logic to prioritize Hub-dispatched endpoints over local broker configuration, ensuring correct Hub communication in distributed and multi-hub environments.
* **Logging Observability & Traceability:**
    * **Agent Lifecycle Traceability:** Added `agent_id` to all broker-side agent lifecycle log events to improve cross-traceability and audit capabilities.
    * **Connectivity Debugging:** Stopped redacting `SCION_HUB_ENDPOINT` and `SCION_HUB_URL` in agent environment logs to facilitate easier debugging of connectivity issues.
* **Documentation & Licensing:** Restructured internal documentation for improved clarity, updated the installation guide, and completed the application of standard license headers across all source files.

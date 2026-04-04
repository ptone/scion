# Release Notes (Mar 4, 2026)

This period focuses on the foundational implementation of the unified harness authentication pipeline and enhances infrastructure visibility within the Web UI.

## ⚠️ BREAKING CHANGES
* **Harness Authentication Pipeline:** The implementation of the unified `ResolvedAuth` model (Phases 1-7) replaces legacy harness-specific authentication methods. While finalized in the Mar 5 release, the core architectural shift and retirement of legacy methods occurred in this period.

## 🚀 Features
* **Unified Harness Authentication:** Completed a multi-phase refactor (Phases 1-7) of the agent authentication pipeline. Introduced centralized `AuthConfig` gathering, per-harness `ResolveAuth` logic, and a unified `ValidateAuth` phase, enabling more robust credential resolution across all harnesses.
* **Broker Visibility & Infrastructure Metadata:** Enhanced the Web UI to display runtime broker information on agent cards, grove detail pages, and agent detail headers, providing clearer insight into distributed execution.
* **Default Notification Triggers:** Expanded the notification system to include `stalled` and `error` as default trigger states, improving proactive monitoring of agent health.

## 🐛 Fixes
* **Workspace Permissions:** Hardened the workspace provisioning flow by ensuring `git clone` operations run as the `scion` user when the broker is executing as root.
* **UI Navigation & UX:** Fixed back-link routing for agent creation and detail pages to consistently return users to the parent grove. Improved terminal accessibility by disabling the terminal button for offline agents.
* **Config & Environment Propagation:** Resolved issues with `harnessConfig` propagation during the environment-gathering finalization flow and refined Hub endpoint bridging to only target `localhost` endpoints.
* **Server Reliability:** Applied default `StalledThreshold` values for agent health monitoring and improved status badge readability.

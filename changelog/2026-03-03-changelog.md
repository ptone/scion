# Release Notes (Mar 3, 2026)

This release introduces hierarchical subsystem logging, an integrated browser push notification system, and native support for GKE runtimes and OTLP telemetry.

## 🚀 Features
* **Structured Subsystem Logging:** Introduced a hierarchical, subsystem-based structured logging framework across the Hub and Runtime Broker. This enables more granular observability and easier troubleshooting by isolating logs for specific components like the scheduler, dispatcher, and runtimes.
* **Agent Notifications & Browser Push:** Launched an integrated notification system with real-time SSE delivery and agent-scoped filtering. Features include a new notification tray in the Web UI, opt-in checkboxes for agent creation, and native browser push notification support.
* **Telemetry & OTLP Pipeline:** Added native support for OTLP log receiving and forwarding. The system now supports automated telemetry export with GCP credential injection, manageable via new CLI flags (`--enable-telemetry`) and UI toggles.
* **Stalled Agent Detection:** Implemented a new monitoring system to detect agents that have stopped responding (heartbeat timeout). Stalled agents are now flagged in the UI and can trigger automated notification events.
* **GKE Runtime Support:** Added native support for Google Kubernetes Engine (GKE) runtimes, including cluster provisioning scripts and Workload Identity integration for secure, distributed agent execution.
* **Layout & View Toggles:** Enhanced the Web UI with card/list view toggles for Groves, Agents, and Brokers pages, improving resource visibility for both small and large deployments.
* **Broker Access Control:** Strengthened security by enforcing dispatch authorization checks and resolving creator identities for all registered runtime brokers.

## 🐛 Fixes
* **Terminal UX:** Fixed double-paste and selection-copy bugs in the web terminal.
* **UI Responsiveness:** Resolved an issue where the agent list could incorrectly clear during real-time SSE updates and improved status badge readability.
* **Agent Provisioning:** Prevented root-owned directories in agent home by pre-creating secret and gcloud mount-point directories.
* **Administrative Security:** Hardened the Hub by restricting access to global settings and sensitive resource management (env/secrets) to administrative users.
* **Server Stability:** Fixed scheduler startup in combined mode and resolved heartbeats from defeating stalled agent detection.
* **CLI UX:** Standardized CLI scope flags and corrected secret set syntax for hub-scoped resources.

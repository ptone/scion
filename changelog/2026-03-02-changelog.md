# Release Notes (Mar 2, 2026)

This release focuses on refining the agent lifecycle experience with an overhauled status and activity tracking system, enhanced grove-level configuration, and improved CLI flexibility for remote operations.

## 🚀 Features
* **Status & Activity Tracking Overhaul:** Replaced the generic `STATUS` with a more precise `PHASE` column across the CLI and Web UI. Introduced "sticky" activity logic to ensure significant agent actions remain visible during transitions, and enabled real-time status broadcasting via SSE for broker heartbeats.
* **Grove Environment & Secret Management:** Launched a dedicated configuration interface for managing grove-scoped environment variables and secrets. Includes a new "Injection Mode" selector (Always vs. As-Needed) for granular control over agent environment population.
* **Remote Grove Targeting:** Enhanced the `--grove` flag to natively accept grove slugs and git URLs in Hub mode, streamlining operations on remote workspaces without requiring local configuration.
* **Unified Configuration UX:** Consolidated grove-specific configuration into a centralized settings page in the Web UI, utilizing shared components for environment and secret management.

## 🐛 Fixes
* **Container Runtime Compliance:** Fixed an issue where secret volume mounts were incorrectly ordered in container run commands, ensuring reliable mounting across different runtimes.
* **Agent Identity Reliability:** Resolved bugs preventing the consistent propagation of `SCION_AGENT_ID` during restarts and specific dispatch paths, fixing broken notification subscriptions.
* **Linked-Grove Pathing:** Corrected workspace resolution for linked groves without git remotes by ensuring fallback to the provider's local filesystem path.
* **UI State Resolution:** Fixed a bug where hub agents would occasionally show an "unknown" phase by ensuring the UI correctly reads the unified Phase and Activity fields.
* **UX Refinements:** Improved the `scion list` output to use human-friendly template names and fixed dynamic label mapping in secret configuration forms.
* **Stability:** Suppressed spurious errors during graceful server shutdown and resolved potential issues with higher-priority environment variable leakage in tests.

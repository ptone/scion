# Release Notes (Mar 8, 2026)

This release delivers a complete maturation of the Kubernetes runtime, introduces significant architectural enhancements for agent isolation and security, and drastically improves Web UI performance with optimistic updates and connection pooling.

## ⚠️ BREAKING CHANGES
* **Kubernetes Mutagen Sync Removal:** Mutagen synchronization support has been entirely removed from the Kubernetes runtime in favor of native implementations as part of the Stage 1 Parity rollout.

## 🚀 Features
* **Kubernetes Runtime Maturation (Stages 1-3):** Successfully implemented Parity, Production Hardening, and Launch Readiness for the Kubernetes runtime, establishing it as a fully-supported, robust platform for agent execution.
* **Agent Isolation & Grove Security:** Enhanced agent security by externalizing non-git grove data and agent home directories. Introduced tmpfs shadow mounts to definitively prevent agents from cross-accessing `.scion` configuration data or other agents' workspaces within the same grove.
* **Web UI Performance & Responsiveness:** Drastically improved the frontend experience by implementing optimistic UI updates and background data refreshes. Re-architected the application shell to reuse components on navigation and consolidated Server-Sent Event (SSE) connections to prevent browser connection pool exhaustion.
* **Contextual Agent Instructions:** Added support for conditional instruction extensions (`agents-git.md` and `agents-hub.md`), allowing agents to receive tailored operational context based on their specific workspace type.
* **Hub API & Infrastructure:** Completed Phase 5 of the Hub API consolidation with full mode awareness and isolation. Enabled HTTP/2 cleartext (h2c) support on the web server, and introduced new grove management CLI commands (`list`, `prune`, `reconnect`).
* **Agent Configuration & Execution:** Enabled `max_duration` limits universally across all harnesses, added a `--notify` flag to the CLI message command, and introduced a required `image_registry` prompt during workstation initialization.
* **Codex Harness Enhancements:** Stabilized the Codex integration with telemetry reconciliation, proper `auth.json` generation for API key workflows, and unified flag formatting.
* **UI Quality of Life:** Added a card/list view toggle to the grove detail agent list and introduced a power-user shortcut (Alt/Option-click) to bypass delete confirmation dialogs globally.

## 🐛 Fixes
* **Hub/Broker Synchronization:** Resolved critical sync issues by tracking synced agents to correctly detect hub-side deletions, preventing deleted agents from being incorrectly re-proposed for registration.
* **Agent Lifecycle Cleanup:** Fixed cleanup routines to correctly stop agent containers before removing orphaned configs, and ensured broker-side files are meticulously cleaned if a hub dispatch fails.
* **Configuration & Auth Propagation:** Corrected the application order of `--harness-auth` before provisioning to prevent stale environment warnings, and ensured template telemetry configs are properly merged into the applied agent config.
* **Messaging Integrity:** Fixed a bug in `handleAgentMessage` to ensure structured messages are correctly constructed from plain text, and updated the messages tab query to include agent-sent communications.
* **Health & Security:** Exempted health check endpoints from broker auth middleware during strict mode enforcement to prevent false-positive failures in distributed deployments.

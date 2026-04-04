# Release Notes (Mar 16, 2026)

This release focuses on a major overhaul of user group and membership management with new authorization rules and UI enhancements, alongside significant improvements to the OpenTelemetry metrics pipeline.

## 🚀 Features
* **Group & Membership Management:** Overhauled the group management system by introducing human-friendly member editing, user search autocomplete in the add dialog, and strict enforcement of group ownership and authorization rules (consolidated from commits 1ae6d03, 454c80e, 5e32c9e, c2fa624).
* **Telemetry & Metrics Pipeline:** Enhanced the observability pipeline by exporting OTLP metrics through GCP, restoring Gemini token metric hooks, and covering Gemini native OTEL metrics (consolidated from commits 721da2b, 5e752f8, 28a9877, 4321775).

## 🐛 Fixes
* **Group Constraint Fixes:** Resolved multiple backend issues related to group creation and loading, including fixing dev-user UUID mapping for workstation mode, backfilling grove member groups, and ensuring SQLite constraint compatibility (consolidated from commits 1993892, 4628e5f, e5c1eba, 6a4f843, 1a779c8, cb7c932).
* **Agent Lifecycle:** Implemented proper agent resume and restart dispatch logic on the hub (commit 30a1b74).
* **Grove Synchronization:** Fixed an issue with re-linking stale hub groves by ensuring the grove ID is regenerated from the marker file, and updated the UI to conditionally show the branch field only for git-based groves (consolidated from commits 39e0025, 2bab781).
* **Container Prune Operations:** Fixed the container runtime image pruning by removing the unsupported `-f` flag (commit ad9f486).
* **Environment & Security:** Improved GCE certificate checks (commit 8904e76).

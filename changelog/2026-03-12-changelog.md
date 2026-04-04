# Release Notes (Mar 12, 2026)

This release focuses on enhancing persistent storage and system observability. It introduces **Grove Shared Directories**, enabling agents within a grove to share and persist mutable state via the filesystem (with native Kubernetes support). Additionally, the metrics pipeline has been significantly enriched with labels for harness type, model, and grove ID, providing deeper insights into agent performance and costs.

## 🚀 Features
* **Grove Shared Directories (Phase 1 & 2):** Introduced a persistent, mutable storage layer shared between agents within a single grove.
    * Added support for both local filesystem storage and Kubernetes PersistentVolumeClaims (PVCs) with grove-scoped lifecycle management.
    * New CLI commands added: `scion shared-dir list`, `create`, `remove`, and `info` for managing shared volumes.
    * Shared volumes can be mounted at standard paths (`/scion-volumes/<name>`) or within the workspace (`/workspace/.scion-volumes/<name>`) (consolidated from commits 838b1b9, a8d50f8, 8b860c0).
* **Enhanced Telemetry & Metrics Pipeline:** Major overhaul of the metrics pipeline for improved observability and aggregation.
    * Enriched OTel resource attributes with `scion.harness`, `scion.model`, `scion.broker`, and `grove_id`.
    * Expanded Codex-specific telemetry to capture tool usage, tool input/output, and detailed token counts (input, output, cached).
    * Injected `SCION_HARNESS` and `SCION_MODEL` environment variables into agent containers to enable harness-aware telemetry (consolidated from commit 8246a76).

## 🐛 Fixes
* **Metrics & Telemetry Reliability:**
    * Resolved an issue where tool and API metrics were not recorded from unpaired end events.
    * Corrected the wiring of token and model metrics in the hook-to-OTel pipeline (consolidated from commits 2a64f02, 43f1bf0).
* **Agent Lifecycle & Configuration:**
    * Corrected an issue where custom branch names were not properly passed during the final environment setup path of agent creation (commit 46eee6d).
    * Updated the default model configuration for the Codex harness to `gpt-5.4` (commit fbfc950).
* **Maintenance:** Fixed broken documentation links in the repository README (commit 0f55876).

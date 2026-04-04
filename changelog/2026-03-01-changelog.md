# Release Notes (Mar 1, 2026)

This release introduces strict runtime enforcement for agent resource limits and includes several critical stability and performance improvements across the server and build pipeline.

## 🚀 Features
* **Agent Resource Limits Enforcement:** Implemented strict runtime enforcement for agent constraints, including `max_turns`, `max_model_calls`, and `max_duration`. Agents exceeding these limits are now automatically transitioned to a `LIMITS_EXCEEDED` state and terminated.

## 🐛 Fixes
* **Bundle Size Optimization:** Implemented vendor chunk splitting in the Vite build process to resolve bundle size warnings and improve frontend load performance.
* **Server Stability:** Resolved a critical panic that occurred during double-close operations in the combined Hub+Web server shutdown sequence.
* **Secret Mapping:** Corrected the mapping of secret type fields and standardized dynamic key/name labels to ensure consistency with backend providers.

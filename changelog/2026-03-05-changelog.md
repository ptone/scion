# Release Notes (Mar 5, 2026)

This release introduces a major overhaul of the agent authentication pipeline, automated token refresh, and critical stability fixes for container removal and terminal reliability.

## ⚠️ BREAKING CHANGES
* **Credential Key Migration:** The internal secret key `OAUTH_CREDS` has been renamed to `GEMINI_OAUTH_CREDS`. Users must migrate existing secrets to this new key to maintain Gemini harness functionality.
* **Harness Auth Refactor:** Legacy harness-specific authentication methods have been retired in favor of a unified `ResolvedAuth` pipeline. Custom harness implementations or manual environment overrides may require updates to align with the new late-binding logic.

## 🚀 Features
* **Unified Harness Authentication:** Completed a multi-phase refactor of the agent authentication pipeline. Agents now support a variety of resolved auth types (API Key, Vertex AI, ADC, OAuth) with late-binding overrides available via the CLI (`--harness-auth`) and the agent creation form.
* **Agent Token Refresh:** Implemented an automated token refresh mechanism to ensure long-running agents maintain valid authorization throughout extended tasks.

## 🐛 Fixes
* **Apple-Container Stability:** Resolved critical hangs during container removal on macOS by implementing automated cleanup and blocking of problematic debug symlinks (e.g., `.claude_debug`).
* **Terminal UX & Reliability:** Improved error visibility by skipping terminal reset sequences on attachment failures.
* **Workspace & Git Integrity:** Hardened workspace file collection by skipping symlinks and ensured `git clone` operations correctly use the `scion` user when the broker runs as root.
* **Auth Precision & Validation:** Fixed several authentication regressions, including incorrect Vertex AI region projections, false API key requirements during environment gathering, and improper leakage of host settings into agent containers.

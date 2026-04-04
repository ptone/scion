# Release Notes (Mar 9, 2026)

This release marks a significant milestone with the official transition of the project to the Google Cloud Platform organization, including a full module rename. It also introduces critical enhancements for agent autonomy with the enablement of the Scion CLI inside agent containers, alongside major improvements to administrative observability and real-time event reliability.

## ⚠️ BREAKING CHANGES
* **Project Rebranding & Module Rename:** The Go module has been renamed from `github.com/ptone/scion-agent` to `github.com/GoogleCloudPlatform/scion`. All internal package imports and external references have been updated to reflect the transition to the Google Cloud Platform organization.

## 🚀 Features
* **Autonomous In-Container CLI:** Enabled the Scion CLI within agent containers, providing agents with the ability to interact with the Hub API natively using their provisioned authenticated service context.
* **Admin User Activity Tracking:** Introduced "Last Seen" timestamps and sortable columns to the Admin Users dashboard to improve system administration and audit capabilities.
* **Enhanced Event Integrity:** Refined the Server-Sent Event (SSE) pipeline to ensure full agent snapshots are sent in `created` events, preventing incomplete UI states during high-concurrency creation.

## 🐛 Fixes
* **Log Query Precision:** Optimized agent log retrieval by filtering out internal HTTP request logs from the primary agent cloud logging view.
* **Infrastructure & Connectivity:**
    * Prioritized public Hub endpoints for production dispatches, reducing reliance on local network bridges.
    * Implemented defensive fallbacks for Hub environment variables within agent containers.
    * Resolved IAM role assignment issues for Hub service accounts.
* **UI/UX Consistency:**
    * Enforced name slugification across all CLI and Web input boundaries to prevent routing collisions.
    * Eliminated "white-flash" artifacts during OAuth redirects for users in dark mode.
    * Implemented automatic scrolling to error banners on form submissions.
    * Switched to SPA-native navigation for terminal back-links, improving navigation responsiveness.
* **System Stability:**
    * Resolved directory creation and path resolution bugs in split-storage (git-grove) configurations.
    * Fixed `lstat` errors for non-existent grove configuration files in containerized environments.
    * Corrected image registry resolution logic to prevent redundant prompts when already configured.
    * Resolved test failures across four critical categories on the main branch.
* **Harness Improvements:** Refined the Codex harness with improved configuration formatting and support for sandbox/bypass-approval flags.

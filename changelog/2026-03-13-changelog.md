# Release Notes (Mar 13, 2026)

This release focuses on improving agent specialization with harness skills, resolving critical routing and identification issues in multi-hub and linked git environments, and adding a new satellite service for documentation agents.

## ⚠️ BREAKING CHANGES
* **Linked Git Grove IDs:** Linked groves backed by a git remote now use deterministic 16-character hex hash IDs (e.g., generated via `HashGroveID()`) instead of the raw, normalized git URL. This resolves severe web routing and API path parsing issues caused by slashes in the URL. If you had existing linked groves, they may need to be re-linked, and any scripts relying on the raw git URL as the Grove ID will need to be updated (commit 05e0c7a).

## 🚀 Features
* **Harness Skills for Templates:** Implemented robust support for harness skills within agent templates. Skills defined in `harness-configs` and templates are now automatically merged and mounted into the appropriate harness-specific directory (e.g., `.claude/skills`, `.gemini/skills`) during agent provisioning (consolidated from commits efefc44, 2a086ac, 5b54c66).
* **Docs-Agent Satellite Service:** Introduced a new `docs-agent` satellite service to provide dedicated documentation capabilities alongside agent workflows (consolidated from commits 092ffde, 58f21c2, fd1b1e2).
* **Shared Directory Management UI:** Added web UI support for managing and viewing grove shared directories (commit 7d7acfb).
* **Terminal & UX Enhancements:** Enabled tmux mouse mode by default for better terminal interactivity and introduced a custom Scion bell icon for browser notifications (commits c915da9, 343382e).

## 🐛 Fixes
* **Multi-Hub Routing & Dispatch:**
    * Resolved an issue where brokers connected to multiple hubs would route agents to the wrong local hub endpoint by correctly resolving the endpoint from the control channel connection header.
    * Enabled control-channel-only brokers to successfully dispatch agent operations (consolidated from commits dd5581f, 1bdc31d).
* **Agent Creation Context:**
    * Ensured grove shared directories are properly passed from the hub to the broker during agent creation.
    * Fixed an issue where `agentDir` was omitted during harness provisioning and setting overlays (consolidated from commits a5cac3b, c550865).
* **Documentation & Web Hosting:** Corrected site base URLs, configured Astro for GitHub Pages deployment, fixed markdown links to use relative paths, and updated the README to point to the rendered site (consolidated from commits 35eee03, 8ca4a96, a7dc580, e133647, 2467d89).
* **Maintenance:** Internal refactoring analysis for `server.go` and documentation updates for recent feature releases (commits d3484d4, 33ee10e).

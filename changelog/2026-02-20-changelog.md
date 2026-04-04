# Release Notes (Feb 20, 2026)

This period focused heavily on unifying the Hub API and Web Server architectures, refactoring the agent status model, and enhancing the web frontend experience with new routing and pages.

## ⚠️ BREAKING CHANGES
* **Status Model:** Consolidated the `SessionStatus` field into the primary `Status` field across the codebase (API, Database, UI). The `WAITING_FOR_INPUT` and `COMPLETED` states are now treated as "sticky" statuses.
* **Server Architecture:** Combined the Hub API and Web server to serve on a single port (`8080`) when both are enabled. API traffic is now routed to `/api/v1/`, resolving CORS issues and simplifying local deployment.

## 🚀 Features
* **Web Frontend Enhancements:** Added a new Brokers list page, implemented full client-side routing for the Vite dev server, and unified OAuth provider detection via a new `/auth/providers` endpoint.
* **Agent Environment:** Added support for injecting harness-specific telemetry and hub environment variables directly into agent containers based on grove settings.
* **Git Operations:** Added cloning status indicators and improved git_clone config parity during grove-scoped agent creation.

## 🐛 Fixes
* **Real-time UI Updates:** Fixed the Server-Sent Events (SSE) format to ensure real-time UI updates correctly broadcast agent state changes.
* **Routing & Port Prioritization:** Fixed port prioritization to use the web port for broker hub endpoints in combined mode, and ensured unhandled `/api/` routes return proper JSON 404 responses.
* **OAuth & Login:** Fixed conditional rendering for the `/login` route and correctly populated OAuth provider attributes during client-side navigation.
* **Container Configuration:** Fixed container image resolution from on-disk harness configurations and normalized YAML key parsing.
* **Status Reporting:** Ensured Hub status reporting correctly respects and preserves the newly unified, sticky statuses.

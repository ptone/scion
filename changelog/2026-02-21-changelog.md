# Release Notes (Feb 21, 2026)

This period heavily focused on implementing the end-to-end "env-gather" flow to manage environment variables safely, alongside several CLI improvements and runtime fixes.

## 🚀 Features
* **Env-Gather Flow Pipeline:** Implemented a comprehensive environment variable gathering system across the CLI, Hub, and Broker. This includes harness-aware env key extraction, Hub 202 handling with submission endpoints, and broker-side evaluation to finalize the environment prior to agent creation.
* **Agent Context Threading:** Threaded the CLI hub endpoint directly to agent containers and added support for environment variable overrides.
* **Agent Dashboard Enhancements:** The agent details page now displays the `lastSeen` heartbeat as a relative time format.
* **Template Pathing:** Added support for `SCION_EXTRA_PATH` to optionally include template bin directories in the system `PATH`.
* **Build System Upgrades:** Overhauled the Makefile with new standard targets for build, install, test, lint, and web compilation.

## 🐛 Fixes
* **Env-Gather Safety & UX:** Added strict rejection of env-gather in non-interactive modes to prevent unsanctioned variable forwarding. Improved confirmation messaging and added dispatch support for grove-scoped agent creation.
* **CLI Output Formatting:** Redirected informational CLI output to `stderr` to ensure `stdout` can be piped cleanly as JSON.
* **Podman Performance:** Fixed slow container provisioning on Podman by directly editing `/etc/passwd` instead of using `usermod`.
* **Profile Parameter Routing:** Corrected the threading of the profile parameter from the CLI through the Hub to the runtime broker.
* **Hub API Accuracy:** The Hub API now correctly surfaces the `harness` type in responses for agent listings.
* **Docker Build Context:** Fixed an issue where the `scion-base` Docker image build was missing the web package context.

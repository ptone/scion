# Release Notes (Feb 22, 2026)

This period introduced significant data management features, including agent soft-delete and centralized harness configuration storage, while advancing the secrets management and execution limits infrastructure.

## 🚀 Features
* **Agent Soft-Delete & Restore:** Implemented a complete soft-delete lifecycle for agents. This includes Hub-side archiving, a new `scion restore` command, list filtering for deleted agents, and an automated background purge loop for expired records.
* **Secrets-Gather & Interactive Input:** Enhanced the environment gathering pipeline to support "secrets-gather." Templates can now define required secrets, and the CLI provides interactive prompts to collect missing values, which are then securely backed by the configured secret provider.
* **K8s Native Secret Mounting:** Completed Phase 4 of the secrets strategy, enabling native secret mounting for agents running in Kubernetes. This includes support for GKE CSI drivers and robust fallback paths.
* **Harness Config Hub Storage:** Added Hub-resident storage for harness configurations. This enables centralized management (CRUD), CLI synchronization, and ensures configurations are consistently propagated to brokers during agent creation.
* **Agent Execution Limits:** Introduced Phase 1 of the agent limits infrastructure, including support for `max_turns` and `max_duration` constraints and a new `LIMITS_EXCEEDED` agent state.
* **CLI UX Improvements:** Added a `--all` flag to `scion stop` for bulk agent termination, introduced Hub auth verification with version reporting, and enhanced `scion look` with better visual padding and borders.
* **Web UI & Real-time Updates:** Launched a new "Create Agent" UI, optimized frontend performance by moving to explicit component imports, and enabled real-time grove list updates via Server-Sent Events (SSE).

## 🐛 Fixes
* **Provisioning Robustness:** Improved cleanup of provisioning agents during failed or cancelled environment gathering sessions to prevent stale container accumulation.
* **Sync & State Consistency:** Fixed a race condition where Hub synchronization could remove freshly created agents and ensured harness types are correctly propagated during agent sync.
* **Deployment Pipeline:** Corrected the build sequence in GCE deployment scripts to ensure web assets are fully compiled before the Go binary is built.
* **Config Resolution:** Fixed several configuration issues, including profile runtime application, grove flag resolution in subdirectories, and Hub environment variable suppression when the Hub is disabled.

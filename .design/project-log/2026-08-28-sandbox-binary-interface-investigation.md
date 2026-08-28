# Sandbox Binary Interface Investigation

**Date:** 2026-08-28
**Agent:** sn-crashlog-inv
**Context:** P0 security defect — sandbox binary at `/usr/local/gcp/bin/sandbox` logs all `--env KEY=VALUE` flags in cleartext to `/var/log/sandbox.log`. Investigating whether alternative env var delivery methods exist.

## Questions Investigated

### Q1: Is there `--env-file` or equivalent that takes a PATH instead of a VALUE?

**Answer: CANNOT CONFIRM OR DENY — binary help text not accessible.**

- The sandbox binary exists only on Cloud Run instances. Not available locally.
- Searched the entire codebase for `--env-file`, `env-file`, `envFile`, `env_file`, `EnvFile`, `env-from` — **zero references**.
- Across 30+ observed `sandbox run` invocations in sandbox.log (spanning 5 instances over 30 days), only `--env` was ever used for environment variables.
- The only flags ever observed on `sandbox run` are: `--detach`, `--rootfs`, `--write`, `--allow-egress`, `--mount`, `--env`.
- The only flags ever observed on `sandbox exec` are: `--` (separator) followed by the command.
- Other known subcommands: `delete` (with `--force`), `wait`.

### Q2: Can it read env from stdin?

**Answer: Even if supported by the binary, it is NOT POSSIBLE with the current invocation.**

- `runSimpleCommand()` at `common.go:554-567` uses `exec.CommandContext` with `CombinedOutput()`.
- `CombinedOutput()` does NOT connect stdin — the child process receives `/dev/null` as its stdin.
- The binary would need to be invoked via `runInteractiveCommand()` (which pipes os.Stdin) or a custom invocation for stdin-based delivery to work.

### Q3: Does it support reference indirection (secret name, file descriptor, mount)?

**Answer: No evidence of any indirection mechanism.**

- No flags like `--secret`, `--env-from`, `--env-ref`, `--env-source` in codebase or observed invocations.
- The binary operates as a simple container runtime — it runs gVisor sandboxes with direct flag-based configuration.
- The underlying runtime is `runsc` (gVisor), confirmed by `sandbox exec` log entry showing `/usr/local/gcp/bin/runsc --help` being run inside a sandbox.

### Q4: Does the `[start]` log line redact anything, or log every flag verbatim?

**Answer: DEFINITIVELY NO REDACTION. Every flag is logged verbatim.**

- Confirmed by examining 30+ `[start]` entries across multiple instances.
- Credential-type env vars appear in full cleartext:
  - `GEMINI_API_KEY` — full value PRESENT in every entry where it was set
  - `SCION_GIT_CLONE_URL` — full URL PRESENT (contains repo paths; no embedded auth tokens observed in sampled entries)
  - `SCION_AUTH_TOKEN` — not observed (latent; only injected when token generator configured)
- Non-credential env vars (UUIDs, paths, URLs, config values) also appear verbatim.
- The `[start]` format is: `[start] cwd=<dir> "<full command line with all flags>"`.
- The `[end]` format is: `[end] exit_code=<N> elapsed=<duration> "<full command line>"` — also verbatim.

### Bonus: What would happen to a file-path value in the log?

If `--env-file` existed and were used, the `[start]` line would presumably log `--env-file /path/to/file` — i.e., the **file path**, not its contents. This would avoid leaking the secret values into the log.

**HOWEVER** — per architect's caution about `--rootfs /`:
- The sandbox uses `--rootfs /` (the host filesystem), so any env file would be written to the **host filesystem**.
- Agent home directories persist at `/scion/agents/<name>/home/` and survive sandbox deletion.
- Per issue #108, agent homes are inherited. Writing secrets to a file there **relocates the leak** from the log to the filesystem, where it persists longer and can be inherited by subsequent agents.

## What I Could Not Determine

The sandbox binary's help text was never captured into Cloud Logging, despite someone having already run all four help commands on the instance:
- `sandbox --help` — run at 2026-08-26T12:46:12Z and 2026-08-26T16:46:27Z
- `sandbox run --help` — run at 2026-08-26T06:59:25Z
- `sandbox exec --help` — run at 2026-08-26T07:20:20Z, 07:24:56Z, 12:46:22Z
- `sandbox help` (via exec into probe-0) — run at 2026-08-27T06:55:06Z

The stdout from these commands went to the calling process but was never written to any Cloud Logging stream (stdout, stderr, or sandbox.log). The sandbox.log only records `[start]`/`[end]` wrapper entries.

**To get a definitive answer on undocumented flags**, someone needs to run `sandbox run --help` interactively on the instance and read the output directly.

## Measured Blast Radius and Retention

As of 2026-08-28, measured from Cloud Logging across the `_Default` bucket (30-day retention):

- **65 sandbox starts** total across **5 Cloud Run instances**
- **`GEMINI_API_KEY`** — exposed in cleartext on **4 starts** (value PRESENT by name; not reproduced here)
- **`SCION_GIT_CLONE_URL`** — exposed in cleartext on **2 starts** (contains repo URL; no embedded auth tokens observed in sampled entries)
- **`SCION_AUTH_TOKEN`** — **latent exposure**; not observed in any start (only injected when token generator is configured, which it was not on these instances)
- **Retention:** 30 days in the `_Default` Cloud Logging bucket. Entries auto-expire after that.
- **Log stream:** `run.googleapis.com/var/log/sandbox.log`, accessible to any principal with `logging.logEntries.list` on the project.

## Three Leak Vectors (recap)

1. **sandbox binary audit log** (`/var/log/sandbox.log` → Cloud Logging) — UNCONDITIONAL, ZERO REDACTION
2. **`runSimpleCommand()` DEBUG log** (`common.go:555-556`) — logs full command at DEBUG level, gated on log level
3. **`WriteRuntimeDebugFile()`** (`common.go:619-637`) — writes full command to `<agentDir>/runtime-exec-debug`, gated on `config.Debug`

## Architect's Design Decision (2026-08-28)

The architect (sn-impl-arch) concluded that even if `--env-file` existed, a file-based approach would convert a secret with 30-day log retention into one with indefinite filesystem retention, readable by the next tenant via #108 home inheritance. The file-based path is not a fix — it is a worse leak.

The correct constraint is: **the sandbox start command must not carry secret values at all.** The existing metadata emulator pattern (pass an endpoint reference, fetch the value in-sandbox over an authenticated channel at runtime) generalises to all secrets. `SCION_AUTH_TOKEN` is the irreducible bootstrap credential that must arrive somehow, but reducing N secrets in argv to one named, rotatable token is most of the win.

Investigation closed. Design work taken by sn-impl-arch.

# Design: Filesystem Watcher for Agent Activity Tracking

## Status: Draft

## Problem Statement

Scion runs multiple LLM agents concurrently, each in its own Docker container with a bind-mounted worktree. Today there is no centralized visibility into which files each agent is creating, modifying, or deleting. Operators and the platform itself have no structured record of agent filesystem activity, which limits debugging, auditing, and downstream tooling (e.g. conflict detection between agents editing overlapping files).

## Goal

Build a small, standalone Go utility that:

1. Monitors a configurable set of host directories for file creates, edits, and deletes.
2. Attributes each filesystem event to the originating agent by correlating the writing process's PID back to its Docker container and reading the container's `scion.name` label.
3. Writes structured, line-delimited JSON (NDJSON) logs containing the agent ID, file path, and action type.

The utility is a **standalone binary** built as its own Go module in `./extras/fs-watcher-tool/`. It is **Linux + Docker only** — no macOS or other runtime support is required.

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│  Host                                                            │
│                                                                  │
│  ┌─────────────┐    fanotify     ┌──────────────────────────┐    │
│  │  Worktree A  │◄──────────────►│                          │    │
│  └─────────────┘                 │   scion-fs-watcher       │    │
│  ┌─────────────┐    fanotify     │                          │    │
│  │  Worktree B  │◄──────────────►│  - fanotify listener     │    │
│  └─────────────┘                 │  - PID → container map   │    │
│  ┌─────────────┐    fanotify     │  - container → agent ID  │──► NDJSON log
│  │  Worktree C  │◄──────────────►│  - debounce (300ms)      │    │
│  └─────────────┘                 │  - log writer            │    │
│                                  └──────────────────────────┘    │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐                    │
│  │ Agent A   │  │ Agent B   │  │ Agent C   │  (Docker)           │
│  │ container │  │ container │  │ container │                     │
│  └───────────┘  └───────────┘  └───────────┘                    │
└──────────────────────────────────────────────────────────────────┘
```

## Key Design Decisions

### Filesystem Monitoring: `fanotify` via `github.com/opcoder0/fanotify`

The Linux `fanotify` API is chosen because it provides **FID-based (filesystem object identity) events** and, critically, includes the **PID of the process** that triggered the event. This PID is essential for attributing filesystem changes to specific containers/agents.

The `opcoder0/fanotify` Go library provides a clean wrapper around the raw syscall interface.

#### fanotify Configuration

- **Init flags**: `FAN_CLASS_NOTIF | FAN_REPORT_DFID_NAME` — notification-only mode (non-blocking) with directory file-handle + name reporting for creates/deletes.
- **Mark flags**: `FAN_MARK_ADD | FAN_MARK_FILESYSTEM` or per-mount marking on the watched directories.
- **Event mask**:
  - `FAN_CREATE` — file/dir creation
  - `FAN_DELETE` — file/dir deletion
  - `FAN_CLOSE_WRITE` — file modification (closed after writing; more reliable than `FAN_MODIFY` which fires per write syscall)
  - `FAN_MOVED_FROM | FAN_MOVED_TO` — renames (treated as delete + create)

Using `FAN_CLOSE_WRITE` instead of `FAN_MODIFY` avoids a flood of events during large writes and gives a single event per logical file edit.

#### Privilege Requirements

The watcher requires `CAP_SYS_ADMIN` for fanotify. It runs directly on the host (not inside a container) via `sudo`. This is the simplest approach and avoids the complexity of capability management or container privilege escalation.

### PID-to-Agent Resolution

When fanotify delivers an event, it includes the PID of the process that caused it. The resolution pipeline:

1. **PID → Container ID**: Read `/proc/<pid>/cgroup` to extract the Docker container ID from the cgroup path. The cgroup v2 path typically contains the full container ID (e.g., `0::/.../docker-<container_id>.scope`). As a fallback, resolve via `/proc/<pid>/cpuset`.

2. **Container ID → Agent ID**: Query the Docker daemon (via the Docker SDK) with `container.Inspect(containerID)` and read the `scion.name` label. This is the same label scion already sets on every agent container.

3. **Caching**: Maintain an in-memory map of `containerID → agentID` (and `PID → containerID` resolved via cgroup) with a TTL or invalidation on container stop events. Container labels are immutable for the container's lifetime, so the cache only needs invalidation when containers are removed.

4. **Unresolvable PIDs**: Events from PIDs that don't map to a known container (host processes, non-scion containers) are either dropped or logged with `agent_id: ""` depending on configuration.

### Grove-Based Directory Discovery

In addition to explicit `--watch` paths, the watcher supports a `--grove` flag that automatically discovers and watches the directories associated with a grove's agents. This eliminates the need to manually specify each worktree path.

When `--grove` is specified:

1. **Discovery via Docker labels**: List all running Docker containers with the `scion.grove` label matching the specified grove ID. For each matching container, inspect its bind mounts to identify worktree directories. This is simpler than connecting to the hub API and leverages labels scion already sets.

2. **Dynamic updates**: Subscribe to Docker container lifecycle events (`start`, `die`) to automatically add/remove watched directories as agents are created or destroyed.

3. **Shared directories**: In hub mode, agents may not use git worktrees but still have separate workspace directories. The grove-based discovery handles this by finding all agent workspace mount points regardless of whether they are worktrees.

Both `--grove` and `--watch` can be used together — explicit watch paths are merged with discovered grove directories.

### Event Debouncing

Rapid successive edits to the same file (common during LLM agent writes) are debounced with a **300ms window**. When multiple `FAN_CLOSE_WRITE` events for the same file arrive within this window, only a single `modify` event is emitted with the timestamp of the last event in the window.

Debouncing is keyed on `(agent_id, path)` — events from different agents on the same file are not collapsed.

### Rename Coalescing

Editors like vim save files via `write-to-temp + rename`. This produces `create(tmp) + modify(tmp) + rename(tmp → target)` rather than `modify(target)`. When a `FAN_MOVED_TO` event targets a known watched path and the source is a temporary file in the same directory (matching patterns like `.~`, `.swp`, or names starting with `.`), the watcher coalesces the sequence into a single `modify` event for the target path. If the rename pattern is ambiguous, the raw event sequence is emitted instead.

### Symlink Handling

fanotify reports events on the underlying inode. The watcher reports the raw path from fanotify without attempting symlink resolution.

### Log Format

Output is line-delimited JSON (NDJSON), one object per event:

```json
{"ts":"2026-03-24T14:32:01.003Z","agent_id":"frontend-refactor","action":"modify","path":"web/src/client/App.tsx","size":4096}
{"ts":"2026-03-24T14:32:01.150Z","agent_id":"backend-api","action":"create","path":"pkg/hub/handlers.go","size":1523}
{"ts":"2026-03-24T14:32:02.400Z","agent_id":"frontend-refactor","action":"delete","path":"web/src/client/old-util.ts"}
```

Fields:

| Field      | Type   | Description |
|------------|--------|-------------|
| `ts`       | string | RFC 3339 timestamp with millisecond precision |
| `agent_id` | string | Value of the `scion.name` Docker label, or `""` if unresolvable |
| `action`   | string | One of: `create`, `modify`, `delete`, `rename_from`, `rename_to` |
| `path`     | string | Relative path from the watched root directory |
| `size`     | int    | File size in bytes after the event (omitted for `delete` and `rename_from`) |

The `size` field is populated via a `stat()` call on the file after the event. For `delete` and `rename_from` events where the file no longer exists at the original path, the field is omitted.

### Path Filtering

The watcher supports two filtering mechanisms:

#### CLI `--ignore` flags

Quick inline glob patterns for common exclusions:

```
--ignore '.git/**' --ignore '*.swp'
```

#### Filter file (`--filter-file`)

A `.gitignore`-style filter file for more complex exclusion rules. The file uses the same syntax as `.gitignore` (glob patterns, `#` comments, `!` negation for re-inclusion, `/` prefix for directory-rooted patterns) but is a standalone file specific to the watcher — it does not read actual `.gitignore` files.

Example filter file:
```
# Ignore version control
.git/**

# Ignore editor artifacts
*.swp
*.swo
*~
.*.tmp

# Ignore build artifacts
node_modules/**
dist/**
__pycache__/**
*.pyc

# Ignore lock files
*.lock
package-lock.json

# But do watch go.sum changes
!go.sum
```

Patterns from `--ignore` flags and the filter file are merged. The filter file is re-read on `SIGHUP` to allow live updates without restarting.

### Log Rotation

Not implemented. The watcher is designed for short, intentional sessions (the lifetime of a grove's agent set). Operators who need rotation can pipe stdout through external tooling.

### Configuration

The watcher is configured via CLI flags:

```
sudo scion-fs-watcher \
  --grove my-project \
  --watch /extra/shared/dir \
  --log /var/log/scion/fs-events.ndjson \
  --label-key scion.name \
  --ignore '.git/**' \
  --filter-file /path/to/fs-watcher-filter.txt \
  --debounce 300ms
```

| Flag            | Description | Default |
|-----------------|-------------|---------|
| `--grove`       | Grove ID — auto-discover agent directories via Docker labels | (none) |
| `--watch`       | Directory to watch explicitly (repeatable) | (none) |
| `--log`         | Output log file path (`-` for stdout) | stdout |
| `--label-key`   | Docker label key to use as agent ID | `scion.name` |
| `--ignore`      | Glob patterns to exclude (repeatable) | `.git/**` |
| `--filter-file` | Path to `.gitignore`-style filter file | (none) |
| `--debounce`    | Duration to collapse rapid edits to the same file | `300ms` |
| `--cache-ttl`   | Duration to cache PID→container mappings | `5m` |
| `--verbose`     | Enable debug logging to stderr | `false` |

At least one of `--grove` or `--watch` is required.

### Hub Integration

Not included in this implementation. The watcher produces local NDJSON log files only. These logs are intended for consumption by the visualization tool (optional). If centralized visibility is needed in the future, a separate process can tail the log file and forward events.

## Implementation Plan

### Project Location

The watcher is a standalone Go binary with its own `go.mod`, located at:

```
extras/fs-watcher-tool/
  go.mod
  go.sum
  main.go
  pkg/fswatcher/
    watcher.go       # fanotify setup, event loop, debounce
    resolver.go      # PID → container → agent resolution
    grove.go         # grove-based directory discovery via Docker labels
    logger.go        # NDJSON log writer
    filter.go        # path/glob filtering + filter file parsing
    types.go         # event types, config struct
```

### Core Loop (Pseudocode)

```go
func (w *Watcher) Run(ctx context.Context) error {
    fan, err := fanotify.Initialize(fanotify.FlagNotifOnly, fanotify.OReading)
    // configure marks for each watch directory

    for {
        events, err := fan.ReadEvents()
        for _, ev := range events {
            if w.filter.ShouldIgnore(ev.Path) {
                continue
            }
            agentID := w.resolver.Resolve(ev.Pid)
            w.debouncer.Submit(ev.Path, agentID, mapAction(ev.Mask), func() {
                size := statSize(ev.Path) // nil for delete
                w.logger.Write(Event{
                    Timestamp: time.Now().UTC(),
                    AgentID:   agentID,
                    Action:    mapAction(ev.Mask),
                    Path:      relativize(ev.Path, w.roots),
                    Size:      size,
                })
            })
        }
    }
}
```

### Resolver Lifecycle

- On startup, pre-populate the container cache by listing all running Docker containers with `scion.name` labels.
- Subscribe to Docker events (`container start`, `container die`) to keep the cache current without polling.
- For each fanotify event PID, look up cgroup to get container ID, then look up cache for agent ID.
- When `--grove` is specified, also use the Docker event stream to dynamically add/remove fanotify marks as agent containers start and stop.

## Alternatives Considered

### 1. `inotify` (via `fsnotify/fsnotify`)

**Pros**: Well-supported Go library, simpler API, works on macOS too.
**Cons**: Does not provide the PID of the modifying process. Without PID, there is no way to attribute an event to a specific container/agent. Would require heuristic approaches (e.g. watching per-worktree and inferring agent from directory) which break if agents share files.
**Verdict**: Rejected — PID attribution is a hard requirement.

### 2. `inotify` with per-worktree isolation (no PID needed)

**Pros**: Since each agent has its own worktree, we could run one inotify watcher per worktree and tag events by the directory-to-agent mapping without needing PID resolution.
**Cons**: Works only when worktrees are strictly isolated (one agent per directory). Breaks for shared directories, mounted volumes, or the repo root. Also, inotify requires adding watches recursively for each subdirectory and has a system-wide watch limit (`/proc/sys/fs/inotify/max_user_watches`), which can be problematic with large codebases or many agents.
**Verdict**: Viable for MVP if fanotify proves too complex, but limits future flexibility.

### 3. eBPF-based file tracing

**Pros**: Full visibility into every syscall, rich metadata (PID, UID, container cgroup, full path). Can trace `open`, `write`, `unlink`, `rename` with exact process context.
**Cons**: Requires `CAP_BPF` / `CAP_SYS_ADMIN`, kernel 5.8+, and significantly more implementation complexity. Dependency on BPF CO-RE or libbpf adds build complexity.
**Verdict**: Over-engineered for this use case. fanotify provides sufficient PID + event data without the operational burden.

### 4. Docker filesystem diff polling

**Pros**: Simple — periodically run `docker diff <container>` for each agent container.
**Cons**: Only shows the overlay diff (what changed in the container's writable layer), not bind-mounted volumes. Since worktrees are bind-mounted, changes wouldn't appear in `docker diff` at all.
**Verdict**: Rejected — fundamentally doesn't work with bind mounts.

### 5. Audit subsystem (`auditd` / `go-audit`)

**Pros**: Comprehensive, PID-aware, path-aware. Can filter by path and syscall type.
**Cons**: Heavy system-wide impact, requires auditd configuration, produces enormous log volume, and is typically reserved for security compliance. Parsing audit logs is complex.
**Verdict**: Rejected — too heavyweight and operationally intrusive for this use case.

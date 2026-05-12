# Design: Official Python and TypeScript SDKs for Scion Hub API

**Issue:** [#24 — Official Python and TypeScript SDKs](https://github.com/ptone/scion/issues/24)
**Author:** SDK Design Agent
**Date:** 2026-05-12
**Status:** Draft — awaiting review

---

## 1. Overview

Scion's Hub exposes a REST API (`/api/v1/`) with 100+ endpoints covering agent lifecycle, messaging, project management, secrets, streaming (SSE), scheduling, and auth. Today the only programmatic client is the internal Go `hubclient` package (`pkg/hubclient/`). This design proposes official Python and TypeScript SDKs that mirror the Go client's service-oriented architecture while providing idiomatic APIs for each ecosystem.

### Goals

- First-class programmatic access for Python and TypeScript — the two dominant ecosystems in AI/ML and web-platform automation.
- Idiomatic, strongly-typed clients with async support and SSE streaming.
- Low maintenance burden — keep SDKs in sync with API changes without requiring a full OpenAPI spec up front.
- Clear phasing — ship a useful MVP before covering the entire API surface.

### Non-Goals

- Browser-only SDK (TypeScript SDK targets Node.js first; browser support is Phase 3).
- Mobile SDKs.
- Full OpenAPI spec authoring (deferred; see section 6).

---

## 2. Recommended Phasing

### Phase 1 — Core MVP (Target: 4-6 weeks)

Ship the minimum surface area that makes the SDKs useful for the most common automation scenarios: creating agents, sending messages, watching events via SSE, and managing secrets.

| Service | Operations |
|---------|-----------|
| **Client** | Instantiation with PAT auth, env-var auto-detection, health check |
| **Agents** | Create, Get, List, Start, Stop, Suspend, Restart, Delete, Restore |
| **Messaging** | SendMessage, SendStructuredMessage, GetMessages (agent-scoped) |
| **Streaming** | StreamEvents (SSE — agent status changes), StreamCloudLogs |
| **Projects** | List, Get, Create, ListAgents |
| **Secrets** | Set, Get, List, Delete (with scope) |
| **Errors** | Typed exceptions mapped from API error codes |
| **Pagination** | Cursor-based iteration helpers |

**Why this scope:** These operations cover the "create an agent, give it a task, watch it work, read its output" workflow — the primary use case for CI/CD, custom dashboards, and orchestration scripts.

### Phase 2 — Extended (Target: 3-4 weeks after Phase 1)

| Service | Operations |
|---------|-----------|
| **Templates** | CRUD, file upload/download, clone |
| **Harness Configs** | CRUD, file operations |
| **Notifications** | List, Acknowledge, AcknowledgeAll |
| **Subscriptions** | Create, List, Update, Delete, BulkCreate, BulkDelete |
| **Scheduling** | ScheduledEvents (CRUD + cancel), Schedules (CRUD + pause/resume) |
| **User Messages** | Inbox list, get, mark-read |
| **Environment Vars** | Set, Get, List, Delete |
| **Tokens** | Create, List, Get, Revoke, Delete |
| **Project Settings** | Get, Update |
| **Project Providers** | List, Add, Remove |
| **Workspace** | Cache refresh, cache status |

### Phase 3 — Advanced (Target: ongoing after Phase 2)

| Feature | Notes |
|---------|-------|
| OAuth device-code flow helpers | For CLI tools built on the SDK |
| Agent exec | Remote command execution |
| Runtime Broker management | Register, heartbeat, list, delete |
| GCP Service Accounts | Mint, list, delete |
| Admin operations | Maintenance mode, migrations |
| Browser support (TS) | Fetch-based transport, no Node.js deps |
| WebSocket control channel | If needed beyond broker use |
| Allow list / Invites | Access control helpers |

---

## 3. Code Generation vs. Hand-Written

### Analysis

| Approach | Pros | Cons |
|----------|------|------|
| **Full OpenAPI codegen** (e.g., openapi-generator, Stainless) | Auto-sync with API changes; less manual work per endpoint | Requires maintaining an OpenAPI spec (none exists today); generated code is often non-idiomatic; streaming/SSE needs manual wrappers anyway; tight coupling to codegen tooling |
| **Fully hand-written** (current Go client approach) | Best DX; idiomatic per language; full control over streaming, auth, retries; easier to add context-specific helpers | Manual sync effort; risk of drift from API |
| **Hybrid** — generate types/models from schema, hand-write service methods | Type safety from schema; ergonomic service layer | More tooling complexity; still need a schema source |

### Recommendation: Hand-written with schema-driven validation

Start fully hand-written, mirroring the Go client's architecture. This matches the project's current approach and avoids blocking on OpenAPI spec creation.

**Mitigation for drift risk:**
1. Maintain a lightweight `api-surface.yaml` manifest (not a full OpenAPI spec) that lists endpoints, HTTP methods, and request/response type names. This can be extracted from the Go route registrations.
2. Add a CI check that compares the manifest against both SDKs' test coverage to flag uncovered endpoints.
3. Plan to adopt OpenAPI + codegen (e.g., Stainless for TypeScript, openapi-python-client for Python) once the API stabilizes post-1.0, using the manifest as a migration bridge.

**Rationale against codegen now:**
- The API is actively evolving (the grove → project rename is a recent example of large-scale changes).
- SSE streaming, env-gather flows, and legacy field fallbacks all require custom handling that codegen tools handle poorly.
- The Go client itself is hand-written — maintaining consistency across all three clients is simpler with a shared pattern than with mixed approaches.

---

## 4. Package Structure and Naming

### Python SDK

```
scion-python-sdk/           (repo or monorepo subdirectory)
├── pyproject.toml           (PEP 621, build via hatchling)
├── src/
│   └── scion/
│       ├── __init__.py      (re-export ScionClient, AsyncScionClient)
│       ├── _client.py       (ScionClient — sync wrapper)
│       ├── _async_client.py (AsyncScionClient — native async)
│       ├── _transport.py    (HTTP transport, retry, auth injection)
│       ├── _errors.py       (ScionError, NotFoundError, etc.)
│       ├── _pagination.py   (SyncPage, AsyncPage iterators)
│       ├── _streaming.py    (SSE stream parsing, event iterator)
│       ├── types/
│       │   ├── __init__.py
│       │   ├── agents.py    (Agent, CreateAgentRequest, etc.)
│       │   ├── projects.py
│       │   ├── secrets.py
│       │   ├── messages.py
│       │   └── ...
│       └── resources/
│           ├── __init__.py
│           ├── agents.py    (AgentResource — service methods)
│           ├── projects.py
│           ├── secrets.py
│           ├── messages.py
│           └── ...
└── tests/
    ├── test_agents.py
    ├── test_streaming.py
    └── ...
```

- **Package name:** `scion` (PyPI: `scion-sdk`)
- **Minimum Python:** 3.9+
- **Dependencies:** `httpx` (sync + async HTTP), `pydantic` (v2, models), `typing-extensions`
- **No dependency on `requests`** — `httpx` provides both sync and async with the same API surface

### TypeScript SDK

```
scion-typescript-sdk/        (repo or monorepo subdirectory)
├── package.json             (@scion/sdk on npm)
├── tsconfig.json
├── src/
│   ├── index.ts             (re-export ScionClient)
│   ├── client.ts            (ScionClient class)
│   ├── transport.ts         (HTTP transport, retry, auth)
│   ├── errors.ts            (ScionError, NotFoundError, etc.)
│   ├── pagination.ts        (PageIterator, auto-pagination)
│   ├── streaming.ts         (SSE parsing, AsyncIterable adapter)
│   ├── types/
│   │   ├── agents.ts
│   │   ├── projects.ts
│   │   ├── secrets.ts
│   │   ├── messages.ts
│   │   └── ...
│   └── resources/
│       ├── agents.ts        (AgentResource class)
│       ├── projects.ts
│       ├── secrets.ts
│       ├── messages.ts
│       └── ...
└── tests/
    ├── agents.test.ts
    ├── streaming.test.ts
    └── ...
```

- **Package name:** `@scion/sdk` (npm)
- **Runtime:** Node.js 18+ (uses native `fetch`, `ReadableStream`)
- **Build:** TypeScript 5.x, outputs ESM + CJS via `tsup` or similar
- **Dependencies:** Zero runtime dependencies for Phase 1 (native `fetch` + `EventSource`). Consider `eventsource` polyfill for older Node.js if needed.

### Naming Conventions

| Concept | Go (`hubclient`) | Python (`scion`) | TypeScript (`@scion/sdk`) |
|---------|------------------|-------------------|---------------------------|
| Client constructor | `hubclient.New(url, opts...)` | `ScionClient(hub_url, token=)` | `new ScionClient({ hubUrl, token })` |
| Service accessor | `client.Agents()` | `client.agents` (property) | `client.agents` (property) |
| Create | `Agents().Create(ctx, req)` | `client.agents.create(**params)` | `client.agents.create(params)` |
| List | `Agents().List(ctx, opts)` | `client.agents.list(phase=)` | `client.agents.list({ phase })` |
| Get | `Agents().Get(ctx, id)` | `client.agents.get(agent_id)` | `client.agents.get(agentId)` |
| Stream | `StreamCloudLogs(ctx, id, opts, handler)` | `async for event in client.agents.stream_events(id)` | `for await (const event of client.agents.streamEvents(id))` |

---

## 5. Auth Handling

### Token Resolution Order

Both SDKs should follow the same resolution order, matching the Go client's pattern:

1. **Explicit token parameter** — `ScionClient(token="scion_pat_...")`
2. **Environment variable** — `SCION_API_TOKEN` (or `SCION_TOKEN`)
3. **Dev token env** — `SCION_DEV_TOKEN` (development convenience)
4. **Token file** — `~/.scion/credentials.json` or `~/.scion/dev-token`

### Auth Mechanisms Supported

| Mechanism | Phase | Notes |
|-----------|-------|-------|
| **PAT (Bearer)** | 1 | Primary. `Authorization: Bearer scion_pat_...` |
| **Dev Token (Bearer)** | 1 | Same header, different prefix. `scion_dev_...` |
| **Agent Token** | 1 | `X-Scion-Agent-Token` header. For code running inside agents. Auto-detect via `SCION_AGENT_TOKEN` env var. |
| **OAuth device code** | 3 | Helper for CLI tools. `client.auth.device_code_flow()` |
| **Custom authenticator** | 2 | Callback interface for advanced use (e.g., token refresh) |

### Python Example

```python
from scion import ScionClient

# Explicit token
client = ScionClient("https://hub.example.com", token="scion_pat_...")

# Auto-detect from environment
client = ScionClient("https://hub.example.com")  # reads SCION_API_TOKEN

# Agent context (auto-detects inside agent containers)
client = ScionClient.from_agent_env()  # reads SCION_HUB_URL + SCION_AGENT_TOKEN
```

### TypeScript Example

```typescript
import { ScionClient } from '@scion/sdk';

// Explicit token
const client = new ScionClient({ hubUrl: 'https://hub.example.com', token: 'scion_pat_...' });

// Auto-detect from environment
const client = new ScionClient({ hubUrl: 'https://hub.example.com' });

// Agent context
const client = ScionClient.fromAgentEnv();
```

### `from_agent_env()` / `fromAgentEnv()` Factory

For code running inside Scion agent containers, the SDK should provide a convenience factory that reads the hub URL and agent token from the standard environment variables set by the runtime. This makes it trivial for agent-side scripts to call back to the Hub.

---

## 6. Streaming / SSE Patterns

The Hub exposes several SSE endpoints for real-time events:

- `/api/v1/agents/{id}/cloud-logs/stream` — Structured log entries
- `/api/v1/agents/{id}/messages/stream` — Agent message stream
- `/api/v1/projects/{id}/cloud-logs/stream` — Project-wide log stream

### SSE Wire Format

```
event: agent_status
data: {"type":"agent_status","agentId":"abc","status":"running","timestamp":"..."}

event: log_entry
data: {"timestamp":"...","severity":"INFO","message":"..."}

:heartbeat
```

### Python Streaming

```python
# Async (preferred)
async with AsyncScionClient("https://hub.example.com", token="...") as client:
    async for event in client.agents.stream_events("agent-id"):
        print(f"[{event.type}] {event.message}")
        if event.status == "completed":
            break

# Sync (blocking)
with ScionClient("https://hub.example.com", token="...") as client:
    for event in client.agents.stream_events("agent-id"):
        print(f"[{event.type}] {event.message}")
```

**Implementation:**
- Use `httpx` streaming response with `iter_lines()` (sync) / `aiter_lines()` (async)
- Parse SSE protocol: skip empty lines, `:` comment/heartbeat lines, `event:` type lines
- Yield parsed data objects for `data:` lines
- Support context manager for clean connection teardown
- Auto-reconnect with last-event-id on transient disconnects (configurable)

### TypeScript Streaming

```typescript
// AsyncIterable (preferred)
const stream = client.agents.streamEvents('agent-id');
for await (const event of stream) {
  console.log(`[${event.type}] ${event.message}`);
  if (event.status === 'completed') break;
}

// Callback style (alternative)
client.agents.streamEvents('agent-id', {
  onEvent: (event) => console.log(event),
  onError: (err) => console.error(err),
  signal: controller.signal,
});
```

**Implementation:**
- Use `fetch()` with streaming body via `ReadableStream` + `TextDecoderStream`
- Implement SSE line parser as a `TransformStream`
- Expose as `AsyncIterable<T>` (primary API) and callback-based (alternative)
- Support `AbortSignal` for cancellation
- Auto-reconnect with exponential backoff

### Key Design Decision: Stream as AsyncIterable

Both SDKs should expose streams primarily as async iterables (`async for` / `for await`), not callbacks. This:
- Composes naturally with language async primitives
- Allows breaking out of the loop to close the connection
- Makes error handling natural (`try/except` / `try/catch`)
- Matches the pattern used by Anthropic SDK and OpenAI SDK

---

## 7. Error Handling

### Error Hierarchy

Map the Hub's structured error responses to typed exceptions/errors.

**Python:**

```python
class ScionError(Exception):
    """Base error for all Scion SDK errors."""
    status_code: int
    code: str        # e.g., "not_found", "unauthorized"
    message: str
    request_id: str | None
    details: dict | None

class AuthenticationError(ScionError): ...   # 401
class PermissionError(ScionError): ...       # 403
class NotFoundError(ScionError): ...         # 404
class ConflictError(ScionError): ...         # 409
class ValidationError(ScionError): ...       # 400 + validation_error code
class RateLimitError(ScionError): ...        # 429
class ServerError(ScionError): ...           # 5xx
class ConnectionError(ScionError): ...       # Network failures
class StreamError(ScionError): ...           # SSE stream errors
```

**TypeScript:**

```typescript
class ScionError extends Error {
  status: number;
  code: string;
  requestId?: string;
  details?: Record<string, unknown>;
}

class AuthenticationError extends ScionError {}
class PermissionError extends ScionError {}
class NotFoundError extends ScionError {}
class ConflictError extends ScionError {}
class ValidationError extends ScionError {}
class RateLimitError extends ScionError {}
class ServerError extends ScionError {}
class ConnectionError extends ScionError {}
class StreamError extends ScionError {}
```

### Error Code Mapping

Directly from `pkg/apiclient/errors.go`:

| API Code | HTTP Status | Python Exception | TypeScript Error |
|----------|-------------|------------------|------------------|
| `unauthorized` | 401 | `AuthenticationError` | `AuthenticationError` |
| `forbidden` | 403 | `PermissionError` | `PermissionError` |
| `not_found` | 404 | `NotFoundError` | `NotFoundError` |
| `conflict` / `version_conflict` | 409 | `ConflictError` | `ConflictError` |
| `invalid_request` / `validation_error` | 400 | `ValidationError` | `ValidationError` |
| `rate_limited` | 429 | `RateLimitError` | `RateLimitError` |
| `internal_error` / `runtime_error` | 500 | `ServerError` | `ServerError` |

---

## 8. Pagination

The Hub API uses cursor-based pagination with `limit`, `offset`, and `cursor` parameters, returning `nextCursor` and `totalCount`.

### Python

```python
# Manual pagination
page = client.agents.list(phase="running", limit=10)
for agent in page.data:
    print(agent.name)
if page.has_next:
    next_page = client.agents.list(phase="running", cursor=page.next_cursor)

# Auto-pagination (recommended)
for agent in client.agents.list(phase="running").auto_paging_iter():
    print(agent.name)

# Async auto-pagination
async for agent in client.agents.list(phase="running").auto_paging_iter():
    print(agent.name)
```

### TypeScript

```typescript
// Manual pagination
const page = await client.agents.list({ phase: 'running', limit: 10 });
for (const agent of page.data) console.log(agent.name);
if (page.hasNext) {
  const next = await client.agents.list({ phase: 'running', cursor: page.nextCursor });
}

// Auto-pagination
for await (const agent of client.agents.list({ phase: 'running' })) {
  console.log(agent.name);
}
```

---

## 9. Keeping SDKs in Sync with API Changes

### Strategy

1. **Source of truth:** The Go hubclient package remains the reference implementation. SDK authors should read `pkg/hubclient/` when adding new endpoints.

2. **API surface manifest:** Maintain a `sdk/api-surface.yaml` file that lists every endpoint with its HTTP method, path, service group, and phase (1/2/3). Example:

   ```yaml
   endpoints:
     - path: /api/v1/agents
       method: GET
       service: agents
       operation: list
       phase: 1
     - path: /api/v1/agents
       method: POST
       service: agents
       operation: create
       phase: 1
     - path: /api/v1/agents/{id}/cloud-logs/stream
       method: GET
       service: agents
       operation: stream_cloud_logs
       phase: 1
       streaming: true
   ```

3. **CI coverage check:** A CI job extracts the Go route registrations (from `pkg/hub/routes.go` or similar), compares against the manifest, and fails if new routes are added without manifest entries.

4. **SDK coverage check:** Each SDK's test suite must cover every endpoint listed in the manifest for its phase. A CI job compares test file markers against the manifest and flags gaps.

5. **Changelog discipline:** When adding a Hub API endpoint, the PR template includes a checklist item: "SDK manifest updated? [ ]"

### Legacy Field Handling (grove → project)

The Go client handles the `grove` → `project` rename with custom `MarshalJSON`/`UnmarshalJSON` on every type. **Decision:** The SDKs target the current Hub version only, so:

- Use `project` as the canonical field name in all types.
- Do NOT implement `grove`/`groveId` fallbacks — SDKs only support current Hub versions with `project` endpoints.
- This keeps the SDK code clean and avoids the compatibility complexity present in the Go client.

---

## 10. Testing Strategy

### Unit Tests

- **Transport layer:** Mock HTTP responses. Verify correct URL construction, query parameter encoding, header injection, retry behavior.
- **Service methods:** Mock transport. Verify correct path building, request body serialization, response deserialization, error mapping.
- **Streaming:** Mock SSE responses with canned event sequences. Verify parsing, heartbeat handling, reconnection logic.
- **Pagination:** Mock paginated responses. Verify cursor chaining, empty-page termination.
- **Auth:** Verify token resolution order, header injection, agent token auto-detection.

### Integration Tests

- Run against a real Hub instance (or a lightweight test fixture).
- Cover the full lifecycle: create project → create agent → send message → stream events → stop agent → delete.
- Use a dedicated test project to avoid interfering with other tests.
- Gate on CI but allow skipping when no Hub is available (`SCION_TEST_HUB_URL` env var).

### Test Infrastructure

**Python:**
- Framework: `pytest` with `pytest-asyncio` for async tests
- Mocking: `respx` (for `httpx` request mocking) or `pytest-httpx`
- Coverage: `coverage.py`, target 90%+

**TypeScript:**
- Framework: `vitest` (fast, ESM-native)
- Mocking: `msw` (Mock Service Worker) for HTTP mocking
- Coverage: `vitest` built-in c8/istanbul, target 90%+

### Test File Organization

Each service gets its own test file mirroring the source structure. Streaming and pagination get dedicated test files since they involve more complex state.

---

## 11. Transport Layer Design

Both SDKs need a shared transport concept that handles:

### Request Pipeline

```
User call → Service method → Transport
  1. Build URL (base + path + query params)
  2. Serialize request body (JSON)
  3. Inject auth headers
  4. Set User-Agent: "scion-python-sdk/0.1.0" or "scion-typescript-sdk/0.1.0"
  5. Execute request (with retry on 5xx / network errors)
  6. Check response status
  7. Parse error response (if 4xx/5xx) → throw typed error
  8. Deserialize response body → return typed result
```

### Retry Policy

Match the Go client's behavior:
- Retry on: 5xx, network timeout, connection reset
- Do NOT retry on: 4xx (client errors)
- Default: 3 retries with exponential backoff (1s, 2s, 4s)
- Configurable via client options
- Idempotency: only retry GET, DELETE, and POST with idempotency key

### Timeouts

- Default request timeout: 30s (matching Go client)
- Streaming requests: no timeout (rely on context/signal cancellation)
- Configurable per-client and per-request

---

## 12. Repository and Release Strategy

### Repository Location

**Recommended: Subdirectories in the main Scion repo** (monorepo approach)

```
sdk/
├── python/       → publishes to PyPI as `scion-sdk`
├── typescript/   → publishes to npm as `@scion/sdk`
└── api-surface.yaml
```

**Rationale:**
- SDK changes can land in the same PR as API changes
- CI can enforce manifest/coverage consistency
- Single source of truth for types and constants
- Easier for contributors who already have the repo checked out

**Alternative:** Separate repos (`scion-python-sdk`, `scion-typescript-sdk`). Better if SDKs have different release cadences or different maintainers. Can migrate to this later.

### Versioning

- Pre-1.0: `0.x.y` — breaking changes allowed between minor versions
- Post-1.0: semver, with the Hub API version tracked in SDK metadata
- Both SDKs should share the same version number when possible to reduce confusion

### Release Process

- Tag-triggered CI: push a `sdk-python/v0.1.0` or `sdk-typescript/v0.1.0` tag
- CI builds, tests, and publishes to PyPI/npm
- Changelog auto-generated from conventional commits scoped to `sdk/python/` or `sdk/typescript/`

---

## 13. API Sketches

### Python — Full Example

```python
import asyncio
from scion import AsyncScionClient

async def main():
    async with AsyncScionClient("https://hub.example.com") as client:
        # Check connectivity
        health = await client.health()
        print(f"Hub version: {health.version}")

        # Create and start an agent
        agent = await client.agents.create(
            name="code-reviewer",
            project_id="my-project",
            template="code-review",
            task="Review PR #42 for security issues",
        )
        print(f"Created agent: {agent.id}")

        await client.agents.start(agent.id)

        # Stream events until completion
        async for event in client.agents.stream_events(agent.id):
            if event.type == "log_entry":
                print(f"  [{event.severity}] {event.message}")
            elif event.type == "agent_status":
                print(f"  Status: {event.status}")
                if event.status in ("completed", "error"):
                    break

        # Retrieve the final agent state
        agent = await client.agents.get(agent.id)
        print(f"Final phase: {agent.phase}, activity: {agent.activity}")

        # Clean up
        await client.agents.delete(agent.id)

asyncio.run(main())
```

### TypeScript — Full Example

```typescript
import { ScionClient } from '@scion/sdk';

async function main() {
  const client = new ScionClient({
    hubUrl: 'https://hub.example.com',
    // token auto-detected from SCION_API_TOKEN
  });

  // Check connectivity
  const health = await client.health();
  console.log(`Hub version: ${health.version}`);

  // Create and start an agent
  const { agent } = await client.agents.create({
    name: 'code-reviewer',
    projectId: 'my-project',
    template: 'code-review',
    task: 'Review PR #42 for security issues',
  });
  console.log(`Created agent: ${agent.id}`);

  await client.agents.start(agent.id);

  // Stream events until completion
  for await (const event of client.agents.streamEvents(agent.id)) {
    if (event.type === 'log_entry') {
      console.log(`  [${event.severity}] ${event.message}`);
    } else if (event.type === 'agent_status') {
      console.log(`  Status: ${event.status}`);
      if (['completed', 'error'].includes(event.status)) break;
    }
  }

  // Retrieve final state
  const finalAgent = await client.agents.get(agent.id);
  console.log(`Final phase: ${finalAgent.phase}, activity: ${finalAgent.activity}`);

  // Clean up
  await client.agents.delete(agent.id);
}

main();
```

---

## 14. Resolved Decisions

Decisions confirmed by project owner (2026-05-12):

1. **Repository structure:** Monorepo — `sdk/python/` and `sdk/typescript/` subdirectories inside the main scion repo.
2. **Package naming:** `scion-sdk` (PyPI) / `@scion/sdk` (npm) — confirmed acceptable.
3. **Minimum Hub version:** Current version only. No legacy `grove` field support needed — SDKs will use `project` endpoints exclusively.
4. **Code generation:** Hand-written only for this round. No OpenAPI codegen. A future gRPC API version is possible but not in scope.
5. **Phase 1 scope:** Confirmed — Agents, Messaging, SSE Streaming, Projects, Secrets, and Error handling.

### Remaining Open Questions

6. **SSE reconnection:** Should the SDKs implement automatic reconnection on SSE stream interruption, or leave that to the consumer? (Recommendation: implement it with configurable behavior.)

7. **Rate limiting:** Does the Hub implement rate limiting? If so, should the SDKs include automatic retry-after handling?

8. **WebSocket support:** The Hub has WebSocket endpoints for broker control channels. Should the SDKs expose WebSocket support, or is SSE sufficient for SDK consumers?

---

## 15. Prior Art and References

| SDK | Approach | Relevant Pattern |
|-----|----------|------------------|
| [Anthropic Python/TS SDK](https://github.com/anthropics/anthropic-sdk-python) | Hand-written | Streaming as async iterators, typed errors, sync+async clients |
| [OpenAI Python/TS SDK](https://github.com/openai/openai-python) | Generated via Stainless | Auto-pagination, typed models, resource-based API |
| [Stripe Python/TS SDK](https://github.com/stripe/stripe-python) | Hand-written | Service-based structure, idempotency keys, auto-retry |
| [Google Cloud Client Libraries](https://github.com/googleapis/google-cloud-python) | Generated from protobuf | Pagination helpers, streaming, auth chain |
| Go `hubclient` package | Hand-written (reference) | Service interfaces, transport abstraction, SSE streaming via scanner |

---

## 16. Summary of Recommendations

| Decision | Recommendation |
|----------|---------------|
| Implementation approach | Hand-written (matching Go client) |
| Phase 1 scope | Agents, Messaging, Streaming, Projects, Secrets, Errors |
| Python HTTP library | `httpx` (sync + async) |
| Python models | Pydantic v2 dataclasses |
| TypeScript HTTP | Native `fetch` (Node.js 18+) |
| TypeScript build | ESM + CJS dual output |
| Streaming API | `AsyncIterable` primary, callback secondary |
| Auth default | PAT via env var, with `from_agent_env()` convenience |
| Repository | Monorepo subdirectory (`sdk/`) |
| Versioning | Shared `0.x.y` pre-1.0 |
| Testing | pytest + respx (Python), vitest + msw (TypeScript) |
| Sync strategy | API surface manifest + CI coverage checks |

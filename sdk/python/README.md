# Scion Python SDK

Python client library for the [Scion Hub API](https://github.com/scion-platform/scion). Manage agents, projects, secrets, and stream real-time events from your Scion deployment.

## Installation

```bash
pip install scion-sdk
```

**Requirements:** Python 3.9+

## Quick Start

```python
from scion import ScionClient

client = ScionClient("https://hub.example.com", token="your-token")

# Check API health
health = client.health()
print(f"Hub status: {health.status}")

# List running agents
page = client.agents.list(phase="running")
for agent in page:
    print(f"{agent.name} ({agent.phase})")
```

## Authentication

The SDK resolves credentials in this order:

1. **Explicit token** — passed directly to the client constructor.
2. **`SCION_API_TOKEN`** — environment variable (recommended for CI/CD).
3. **`SCION_DEV_TOKEN`** — environment variable for local development.
4. **`~/.scion/dev-token`** — token file written by `scion login`.

```python
# Explicit token
client = ScionClient("https://hub.example.com", token="pat-abc123")

# Auto-resolved from environment
import os
os.environ["SCION_API_TOKEN"] = "pat-abc123"
client = ScionClient("https://hub.example.com")
```

### Agent Environment

Inside a Scion agent container, use `from_agent_env()` to auto-configure the client from injected environment variables (`SCION_HUB_URL` and `SCION_AGENT_TOKEN`):

```python
client = ScionClient.from_agent_env()
```

## API Reference

### Client Setup

```python
from scion import ScionClient, AsyncScionClient

# Synchronous client
client = ScionClient(
    "https://hub.example.com",
    token="your-token",        # Optional — auto-resolved if omitted
    timeout=30.0,              # Request timeout in seconds (default: 30)
    max_retries=3,             # Retry count for 5xx/network errors (default: 3)
)

# Use as a context manager for automatic cleanup
with ScionClient("https://hub.example.com") as client:
    health = client.health()

# Async client (same interface, all methods are async)
async_client = AsyncScionClient("https://hub.example.com", token="your-token")
```

### Agents

Full lifecycle management for agents: create, list, start, stop, delete, and more.

```python
# Create an agent
response = client.agents.create(
    name="my-agent",
    project_id="proj-123",
    template="default",
    task="Implement feature X",
)
agent = response.agent
print(f"Created agent: {agent.id}")

# Get an agent by ID
agent = client.agents.get("agent-id")
print(f"{agent.name}: {agent.phase}")

# List agents with filters
page = client.agents.list(
    project_id="proj-123",
    phase="running",
    limit=10,
)
for agent in page:
    print(agent.name)

# Auto-paginate across all pages
page = client.agents.list(project_id="proj-123")
for agent in page.auto_paging_iter():
    print(agent.name)

# Lifecycle operations
client.agents.start("agent-id")
client.agents.stop("agent-id")
client.agents.suspend("agent-id")
client.agents.restart("agent-id")

# Delete and restore
client.agents.delete("agent-id")
restored = client.agents.restore("agent-id")
```

### Messaging

Send messages to individual agents or broadcast to all agents in a project.

```python
from scion.types.messages import StructuredMessage

# Send a plain text message
client.agents.send_message("agent-id", "Please review the latest changes")

# Send with interrupt (stops agent's current work)
client.agents.send_message("agent-id", "Urgent: stop and fix the build", interrupt=True)

# Send a structured message
msg = StructuredMessage(
    type="instruction",
    content="Deploy the staging environment",
    data={"priority": "high"},
)
client.agents.send_structured_message("agent-id", msg, notify=True)

# Broadcast to all running agents in the project
client.agents.broadcast_message(msg)
```

#### Inbox

Read and manage the authenticated user's message inbox.

```python
# List all messages
page = client.messages.list()
for message in page:
    print(f"[{message.type}] {message.sender}: {message.msg}")

# Filter messages
page = client.messages.list(unread=True, project_id="proj-123")

# Get a specific message
message = client.messages.get("message-id")

# Mark as read
client.messages.mark_read("message-id")
client.messages.mark_all_read()
```

### Projects

Create and manage projects — the primary organizational unit for agents.

```python
# List projects
page = client.projects.list()
for project in page:
    print(f"{project.name} (slug: {project.slug})")

# Create a project
project = client.projects.create(
    name="my-project",
    git_remote="https://github.com/org/repo.git",
    visibility="private",
)

# Get a project
project = client.projects.get("proj-123")

# Update a project
project = client.projects.update(
    "proj-123",
    name="renamed-project",
    labels={"env": "production"},
)

# List agents in a project
agents_page = client.projects.list_agents("proj-123", phase="running")

# Delete a project (and all its agents)
client.projects.delete("proj-123")
```

### Secrets

Manage secrets scoped to a user, project, or runtime broker. Secret values are write-only — the API never returns them.

```python
# List secrets (user scope by default)
response = client.secrets.list()
for secret in response.secrets:
    print(f"{secret.key} (scope: {secret.scope})")

# List project-scoped secrets
response = client.secrets.list(scope="project", scope_id="proj-123")

# Get secret metadata (value is never returned)
secret = client.secrets.get("MY_API_KEY")

# Create or update a secret
result = client.secrets.set(
    "MY_API_KEY",
    "sk-secret-value",
    scope="project",
    scope_id="proj-123",
    description="API key for external service",
)
print(f"Created: {result.created}")

# Delete a secret
client.secrets.delete("MY_API_KEY", scope="project", scope_id="proj-123")
```

### SSE Streaming

Stream real-time events from agents using Server-Sent Events. The SDK provides typed iterators with automatic reconnection support.

#### Stream Agent Events

```python
# Stream lifecycle events from an agent
with client.agents.stream_events("agent-id") as stream:
    for event in stream:
        print(f"[{event.type}] status={event.status}, phase={event.phase}")
        print(f"  {event.message}")
        if event.status == "completed":
            break
```

#### Stream Cloud Logs

```python
# Stream log entries from an agent
with client.agents.stream_cloud_logs("agent-id") as stream:
    for entry in stream:
        print(f"[{entry.severity}] {entry.message}")

# Filter by severity
with client.agents.stream_cloud_logs("agent-id", severity="ERROR") as stream:
    for entry in stream:
        print(f"ERROR: {entry.message}")
```

#### Reconnection

The SSE iterators support automatic reconnection via the `Last-Event-ID` header:

```python
# Resume from a known event ID (e.g., after a disconnect)
with client.agents.stream_events("agent-id", last_event_id="evt-42") as stream:
    for event in stream:
        print(event)
        # stream.last_event_id tracks the latest ID for reconnection
```

## Error Handling

All API errors are raised as typed exceptions inheriting from `ScionError`:

```python
from scion import (
    ScionError,
    AuthenticationError,
    NotFoundError,
    ValidationError,
    RateLimitError,
    ConflictError,
    PermissionError,
    ServerError,
    ConnectionError,
    StreamError,
)

try:
    agent = client.agents.get("nonexistent-id")
except NotFoundError as e:
    print(f"Agent not found: {e.message}")
    print(f"Status: {e.status_code}, Code: {e.code}")
except AuthenticationError:
    print("Invalid or expired token")
except RateLimitError as e:
    print(f"Rate limited — retry after {e.retry_after}s")
except ScionError as e:
    print(f"API error: {e.message} (request_id: {e.request_id})")
```

### Error Hierarchy

| Exception | HTTP Status | Description |
|---|---|---|
| `ValidationError` | 400 | Invalid request parameters |
| `AuthenticationError` | 401 | Missing or invalid credentials |
| `PermissionError` | 403 | Insufficient permissions |
| `NotFoundError` | 404 | Resource not found |
| `ConflictError` | 409 | State conflict (e.g., agent already running) |
| `RateLimitError` | 429 | Too many requests |
| `ServerError` | 5xx | Server-side error |
| `ConnectionError` | — | Network connectivity failure |
| `StreamError` | — | SSE streaming error |

## Async Usage

The SDK provides a fully async client with the same API surface:

```python
import asyncio
from scion import AsyncScionClient

async def main():
    async with AsyncScionClient("https://hub.example.com", token="your-token") as client:
        # All methods are async
        health = await client.health()
        print(health.status)

        # Create an agent
        response = await client.agents.create(
            name="async-agent",
            project_id="proj-123",
            task="Run async tasks",
        )

        # List agents with auto-pagination
        page = await client.agents.list(project_id="proj-123")
        async for agent in page.auto_paging_iter():
            print(agent.name)

        # Stream events (async)
        stream = await client.agents.stream_events("agent-id")
        async with stream:
            async for event in stream:
                print(event.type, event.message)
                if event.status == "completed":
                    break

        # Manage secrets
        result = await client.secrets.set("KEY", "value")
        await client.secrets.delete("KEY")

asyncio.run(main())
```

## Pagination

List endpoints return `SyncPage` (or `AsyncPage`) objects:

```python
# Iterate over a single page
page = client.agents.list(limit=10)
for agent in page:
    print(agent.name)
print(f"Has more: {page.has_next}")

# Fetch the next page manually
if page.has_next:
    next_page = page.get_next_page()

# Auto-paginate across all pages
for agent in page.auto_paging_iter():
    print(agent.name)
```

## Type Models

All request and response types are [Pydantic v2](https://docs.pydantic.dev/) models with full type annotations:

- `Agent`, `CreateAgentRequest`, `CreateAgentResponse`
- `Project`, `CreateProjectRequest`, `UpdateProjectRequest`
- `Secret`, `SetSecretRequest`, `SetSecretResponse`
- `Message`, `StructuredMessage`
- `AgentEvent`, `LogEntry`, `StreamEvent`

Import from `scion.types`:

```python
from scion.types import Agent, Project, Secret, AgentEvent, LogEntry
```

## License

Apache License 2.0

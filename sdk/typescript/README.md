# Scion TypeScript SDK

TypeScript client library for the [Scion Hub API](https://github.com/scion-platform/scion). Manage agents, projects, secrets, and stream real-time events from your Scion deployment.

## Installation

```bash
npm install @scion/sdk
```

**Requirements:** Node.js 18+

## Quick Start

```typescript
import { ScionClient } from '@scion/sdk';

const client = new ScionClient({ hubUrl: 'https://hub.example.com' });

// Check API health
const health = await client.health();
console.log(`Hub status: ${health.status}`);

// List running agents
const page = await client.agents.list({ phase: 'running' });
for await (const agent of page) {
  console.log(`${agent.name} (${agent.phase})`);
}
```

## Authentication

The SDK resolves credentials in this order:

1. **Explicit token** — passed via `ScionClientOptions.token`.
2. **`SCION_API_TOKEN`** — environment variable (recommended for CI/CD).
3. **`SCION_DEV_TOKEN`** — environment variable for local development.
4. **`~/.scion/dev-token`** — token file written by `scion login`.

```typescript
// Explicit token
const client = new ScionClient({
  hubUrl: 'https://hub.example.com',
  token: 'pat-abc123',
});

// Auto-resolved from environment
process.env.SCION_API_TOKEN = 'pat-abc123';
const client = new ScionClient({ hubUrl: 'https://hub.example.com' });
```

### Agent Environment

Inside a Scion agent container, use `fromAgentEnv()` to auto-configure the client from injected environment variables (`SCION_HUB_URL` and `SCION_AGENT_TOKEN`):

```typescript
const client = ScionClient.fromAgentEnv();
```

## API Reference

### Client Setup

```typescript
import { ScionClient } from '@scion/sdk';

const client = new ScionClient({
  hubUrl: 'https://hub.example.com',  // or set SCION_HUB_URL env var
  token: 'your-token',                 // optional — auto-resolved if omitted
  timeout: 30_000,                      // request timeout in ms (default: 30s)
  projectId: 'proj-123',               // optional — scopes agent operations
});
```

### Agents

Full lifecycle management for agents: create, list, start, stop, delete, and more.

```typescript
// Create an agent
const response = await client.agents.create({
  name: 'my-agent',
  projectId: 'proj-123',
  template: 'default',
  task: 'Implement feature X',
});
console.log(`Created: ${response.agent.id}`);

// Get an agent by ID
const agent = await client.agents.get('agent-id');
console.log(`${agent.name}: ${agent.phase}`);

// List agents with filters
const page = await client.agents.list({
  projectId: 'proj-123',
  phase: 'running',
  limit: 10,
});

// Iterate over a single page
for (const agent of page.data) {
  console.log(agent.name);
}

// Auto-paginate across all pages
for await (const agent of page) {
  console.log(agent.name);
}

// Lifecycle operations
await client.agents.start('agent-id');
await client.agents.stop('agent-id');
await client.agents.suspend('agent-id');
await client.agents.restart('agent-id');

// Delete and restore
await client.agents.delete('agent-id');
const restored = await client.agents.restore('agent-id');
```

### Messaging

Send messages to individual agents or broadcast to all agents in a project.

```typescript
// Send a plain text message
await client.agents.sendMessage('agent-id', 'Please review the latest changes');

// Send with interrupt (stops agent's current work)
await client.agents.sendMessage('agent-id', 'Urgent: stop and fix the build', true);

// Send a structured message
await client.agents.sendStructuredMessage(
  'agent-id',
  {
    type: 'instruction',
    content: 'Deploy the staging environment',
    data: { priority: 'high' },
  },
  { notify: true },
);

// Broadcast to all running agents (requires project-scoped client)
await client.agents.broadcastMessage({
  type: 'announcement',
  content: 'Build succeeded, resume work',
});
```

#### Inbox

Read and manage the authenticated user's message inbox.

```typescript
// List unread messages
const page = await client.messages.list({ onlyUnread: true });
for await (const msg of page) {
  console.log(`[${msg.type}] ${msg.sender}: ${msg.msg}`);
}

// Get a specific message
const message = await client.messages.get('message-id');

// Mark as read
await client.messages.markRead('message-id');
await client.messages.markAllRead();
```

### Projects

Create and manage projects — the primary organizational unit for agents.

```typescript
// List projects
const page = await client.projects.list();
for await (const project of page) {
  console.log(`${project.name} (slug: ${project.slug})`);
}

// Create a project
const project = await client.projects.create({
  name: 'my-project',
  gitRemote: 'https://github.com/org/repo.git',
  visibility: 'private',
});

// Get a project
const project = await client.projects.get('proj-123');

// Update a project
const updated = await client.projects.update('proj-123', {
  name: 'renamed-project',
  labels: { env: 'production' },
});

// List agents in a project
const agentsPage = await client.projects.listAgents('proj-123', {
  phase: 'running',
});

// Delete a project (and all its agents)
await client.projects.delete('proj-123');
```

### Secrets

Manage secrets scoped to a user, project, or runtime broker. Secret values are write-only — the API never returns them.

```typescript
// List secrets (user scope by default)
const result = await client.secrets.list();
for (const secret of result.data) {
  console.log(`${secret.key} (scope: ${result.scope})`);
}

// List project-scoped secrets
const result = await client.secrets.list({
  scope: 'project',
  scopeId: 'proj-123',
});

// Get secret metadata (value is never returned)
const secret = await client.secrets.get('MY_API_KEY');

// Create or update a secret
const response = await client.secrets.set('MY_API_KEY', {
  value: 'sk-secret-value',
  scope: 'project',
  scopeId: 'proj-123',
  description: 'API key for external service',
});
console.log(`Created: ${response.created}`);

// Delete a secret
await client.secrets.delete('MY_API_KEY', {
  scope: 'project',
  scopeId: 'proj-123',
});
```

### SSE Streaming

Stream real-time events from agents using Server-Sent Events. The SDK provides typed async iterables with automatic reconnection.

#### Stream Agent Events

```typescript
// Async iteration
const stream = client.agents.streamEvents('agent-id');
for await (const event of stream) {
  console.log(`[${event.type}] phase=${event.data.phase}`);
  if (event.data.phase === 'stopped') break;
}

// Callback style
await client.agents.streamEvents('agent-id', {
  onEvent: (event) => {
    console.log(`[${event.type}] ${event.data.name}: ${event.data.phase}`);
  },
  onError: (err) => console.error('Stream error:', err),
  onClose: () => console.log('Stream closed'),
  signal: controller.signal,  // AbortController for cancellation
});
```

#### Stream Cloud Logs

```typescript
// Async iteration
const logStream = client.agents.streamCloudLogs('agent-id');
for await (const entry of logStream) {
  console.log(`[${entry.severity}] ${entry.message}`);
}

// Filter by severity via query params
const errorStream = client.agents.streamCloudLogs('agent-id', {
  query: { severity: 'ERROR' },
});
```

#### Cancellation

Use an `AbortController` to cancel streams:

```typescript
const controller = new AbortController();

const stream = client.agents.streamEvents('agent-id', {
  signal: controller.signal,
});

// Cancel after 30 seconds
setTimeout(() => controller.abort(), 30_000);

for await (const event of stream) {
  console.log(event.type);
}
```

#### Reconnection

The SDK automatically reconnects on transient errors with exponential backoff:

```typescript
const stream = client.agents.streamEvents('agent-id', {
  reconnect: true,               // default: true
  maxReconnectAttempts: 5,       // default: 5
  initialReconnectDelay: 1000,   // default: 1000ms
  maxReconnectDelay: 30_000,     // default: 30s
});
```

## Error Handling

All API errors are thrown as typed exceptions extending `ScionError`:

```typescript
import {
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
} from '@scion/sdk';

try {
  const agent = await client.agents.get('nonexistent-id');
} catch (err) {
  if (err instanceof NotFoundError) {
    console.log(`Agent not found: ${err.message}`);
    console.log(`Status: ${err.status}, Code: ${err.code}`);
  } else if (err instanceof AuthenticationError) {
    console.log('Invalid or expired token');
  } else if (err instanceof RateLimitError) {
    console.log(`Rate limited — retry after ${err.retryAfter}s`);
  } else if (err instanceof ScionError) {
    console.log(`API error: ${err.message} (requestId: ${err.requestId})`);
  }
}
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

## Pagination

List endpoints return `Page<T>` objects that implement `AsyncIterable<T>`:

```typescript
// Iterate over a single page's data
const page = await client.agents.list({ limit: 10 });
for (const agent of page.data) {
  console.log(agent.name);
}
console.log(`Has more: ${page.hasNext}`);

// Fetch the next page manually
const nextPage = await page.getNextPage();

// Auto-paginate across all pages via for-await
for await (const agent of page) {
  console.log(agent.name);
}
```

## ESM and CJS Support

The SDK ships dual ESM/CJS bundles:

```typescript
// ESM (recommended)
import { ScionClient } from '@scion/sdk';

// CommonJS
const { ScionClient } = require('@scion/sdk');
```

The `package.json` `exports` field ensures the correct bundle is loaded automatically.

## Type Exports

All request and response types are exported for use in your application:

```typescript
import type {
  Agent,
  CreateAgentRequest,
  Project,
  Secret,
  SetSecretRequest,
  Message,
  StructuredMessage,
  AgentEvent,
  LogEntry,
  StreamOptions,
} from '@scion/sdk';
```

## License

Apache License 2.0

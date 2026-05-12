# TypeScript SDK — Messaging Resource Implementation

**Date:** 2026-05-12  
**Agent:** dev-ts-messaging  
**Task:** Implement TypeScript Messaging Resource (Step 8)

## Summary

Implemented the `MessagesResource` class for the TypeScript SDK at `sdk/typescript/`, including the full SDK foundation (transport, client, error handling, types). The implementation mirrors the Go `hubclient.MessageService` interface.

## Deliverables

### 1. `sdk/typescript/src/resources/messages.ts` — MessagesResource class
- `list(params?)` — List messages with filtering (unread, agentId, projectId, type, limit, cursor)
- `get(messageId)` — Fetch a single message by ID
- `markRead(messageId)` — Mark a single message as read (POST)
- `markAllRead()` — Mark all messages as read (POST)
- Full JSDoc on all public methods with usage examples

### 2. `sdk/typescript/tests/messages.test.ts` — 26 unit tests
- Covers all four methods with happy-path and error scenarios
- Tests query parameter construction for all filter options
- Tests URL encoding of message IDs
- Tests null/undefined items normalization
- Tests error parsing (404, 401, 429, 500, non-JSON bodies)
- Tests authentication header injection and no-token behavior
- Tests ScionClient wiring

### 3. Client wiring
- `ScionClient` exposes `client.messages` as a `MessagesResource` property
- Constructed via `new ScionClient({ baseUrl, token })` pattern

### 4. Types
- `Message` interface — mirrors Go `store.Message`
- `ListMessagesOptions` interface — mirrors Go `hubclient.ListMessagesOptions`
- `Page<T>` generic — mirrors Go `store.ListResult[T]`

## SDK Foundation (created as needed)

Since no SDK directory existed yet, the following foundation was also created:
- `Transport` class — fetch-based HTTP client with JSON handling
- `ScionAPIError` — structured error class with status helpers (isNotFound, isUnauthorized, etc.)
- `parseErrorResponse()` — response body to error object parser
- Package configuration (`package.json`, `tsconfig.json`, `vitest.config.ts`)

## API Endpoints Used

| Method | Go Reference | TypeScript | HTTP |
|--------|-------------|-----------|------|
| List | `messageService.List()` | `messages.list()` | `GET /api/v1/messages` |
| Get | `messageService.Get()` | `messages.get()` | `GET /api/v1/messages/{id}` |
| MarkRead | `messageService.MarkRead()` | `messages.markRead()` | `POST /api/v1/messages/{id}/read` |
| MarkAllRead | `messageService.MarkAllRead()` | `messages.markAllRead()` | `POST /api/v1/messages/read-all` |

## Design Decisions

1. **Uses native `fetch`** — Node 20+ has built-in fetch; no HTTP library dependency needed.
2. **Query param mapping** — Follows the Go convention (`onlyUnread` → `unread=true`, `agentId` → `agent`, `projectId` → `project`).
3. **Empty items normalization** — When API returns `null`/`undefined` items, normalizes to `[]` (matching Go implementation).
4. **URL encoding** — Message IDs are `encodeURIComponent()`-encoded in paths, matching Go's `url.PathEscape()`.
5. **"project" terminology** — Uses `projectId` exclusively, no legacy `groveId` fields.

## Verification

- `npx tsc --noEmit` — compiles cleanly with strict mode
- `npx vitest run` — 26/26 tests pass

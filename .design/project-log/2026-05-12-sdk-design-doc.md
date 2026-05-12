# SDK Design Document — Project Log

**Date:** 2026-05-12
**Agent:** design-sdk
**Task:** Write design document for Python and TypeScript SDKs (Issue #24)

## What Was Done

- Read and analyzed GitHub issue #24 thoroughly
- Explored the full Go `hubclient` package (pkg/hubclient/) — all 25+ source files covering agents, projects, secrets, messages, auth, tokens, notifications, subscriptions, scheduling, templates, harness configs, workspace, env vars, and runtime brokers
- Studied the `apiclient` transport layer and error handling patterns
- Wrote comprehensive design document at `.design/sdk-design.md` covering:
  - 3-phase rollout plan (MVP → Extended → Advanced)
  - Code generation vs hand-written analysis with recommendation (hand-written)
  - Package structure for both Python and TypeScript
  - Auth handling with token resolution order
  - SSE streaming patterns with AsyncIterable APIs
  - API sync strategy using surface manifest + CI checks
  - Testing strategy with specific framework recommendations
  - Error hierarchy mapped from Hub API error codes
  - Pagination with auto-paging iterators
  - Full API sketches for both languages
- Committed to `scion/design-sdk` branch
- Copied to `/scion-volumes/scratchpad/design-sdk.md`
- Sent open questions to ptone@google.com via scion message

## Key Decisions

1. **Hand-written over codegen** — matches the Go client approach, avoids blocking on OpenAPI spec creation, and allows idiomatic handling of SSE streaming and legacy field fallbacks
2. **Monorepo recommended** — SDK code in `sdk/python/` and `sdk/typescript/` subdirectories for co-evolution with the API
3. **Phase 1 scope** — Agents, Messaging, Streaming, Projects, Secrets, Errors — covers the "create agent, watch it work" workflow

## Status

Awaiting review and approval from ptone@google.com before beginning implementation.

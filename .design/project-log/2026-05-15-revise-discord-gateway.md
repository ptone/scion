# Revise Discord Design Doc: Virtual Agent Gateway

**Date:** 2026-05-15
**Agent:** revise-discord-gw
**Task:** Revise the Discord chat app design doc to incorporate the Virtual Agent Gateway architecture

## What Changed

Revised `.design/discord-chat.md` to incorporate four architectural concepts:

1. **Virtual Agent Gateway** — Reframed the entire architecture around a single gateway bot that multiplexes communication to/from all agents in a grove. The bot handles routing and system messages; per-agent visual identity is projected through channel webhooks. Added new architecture diagrams showing the gateway-webhook-identity flow.

2. **Webhook-based per-agent identity** — Elevated the existing webhook identity mechanism to the "identity relay" pattern within the gateway architecture. Clarified that the gateway bot almost never speaks as itself — all agent responses flow through webhooks with per-execution `username` and `avatar_url`. Added a message routing table distinguishing gateway voice from agent voice.

3. **Autocomplete discovery** — New section covering Discord's `Autocomplete: true` option on all agent parameters. Added a complete `handleAutocomplete()` implementation that queries the Hub API for matching agents, with a 30-second TTL cache (`agentListCache`) to avoid fan-out on rapid typing. Updated all slash command registrations to include `Autocomplete: true` on `agent` options.

4. **Select menu sticky context** — New section covering persistent agent targeting. Users set a "current agent" via a select menu appended to agent list responses or via `/scion context <agent>`. The selection is stored in `space_settings` (existing table, key `"sticky_agent"`), scoped per-channel. `buildArgsWithContext()` injects the sticky context when the agent option is omitted from a command.

## Other Updates

- Updated package layout to include `autocomplete.go` and `context.go`
- Updated `Adapter` struct with `adminClient`, `store`, and `agentCache` fields
- Updated event ingestion to include autocomplete in the event types table
- Updated interaction handler dispatch to include `InteractionApplicationCommandAutocomplete`
- Updated testing strategy with autocomplete and sticky context test cases
- Updated implementation plan: split into 4 phases (4a–4d), with autocomplete and sticky context as dedicated Phase 4c
- Updated resolved decisions: renumbered to 16 decisions, with new entries for Virtual Agent Gateway (#1), autocomplete discovery (#5), and sticky context (#6)
- Updated rate limiting table with Hub agent list caching note

## Observations

- The brief file referenced by the coordinator (`/workspace/.scratch/revise-discord-gateway.md`) was missing, but the coordinator's message contained sufficient detail about the four architectural concepts to proceed.
- The existing design doc was well-structured, making targeted edits straightforward. The new sections (Autocomplete Discovery and Select Menu Sticky Context) were inserted as self-contained sections between Slash Command Registration and Guild & Channel Management, keeping the document flow logical.
- The sticky context feature reuses the `space_settings` table from the Slack adapter, requiring no schema changes.

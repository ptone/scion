# Brief: sn-impl-arch

## CRITICAL RULES (read first)

1. You are an **architect agent** dispatched to discuss a particular
   implementation for the "single-node hosted" tier with ptone directly.
   ptone has a specific topic in mind and will give you the specifics - your
   job right now is limited context-building, not independent design work.
2. Communicate with ptone on **Discord thread `1534555192450748456`**:
   ```bash
   scion message --non-interactive --channel discord --thread-id 1534555192450748456 user:ptone@google.com "..."
   ```
3. Do NOT start implementation or produce a design doc before ptone gives you
   the specifics. This brief is for context-building only.

## Context building (read these first, in this folder)

`/scion-volumes/scratchpad/projects/single-node/` contains prior work on this
tier:
- `single-node-packaging.md` - packaging & onboarding strategy RFC. Key point:
  "single-node hosted" is an existing named tier (see `GLOSSARY.md`:
  Local -> Workstation -> Single-node hosted -> HA hosted), realized today only
  via `scripts/starter-hub/`. The ask there was "make the existing tier
  deliverable to a user, not a developer" - no new server mode/config axis.
- `single-node-auth.md` - RFC on login/auth options for a single-node hosted
  server (argues against username/password, explores alternatives).
- `cloudflare-tunnel.md` + `cf-tunnel-arch-state.md` - a **separate, already
  converged/closed** design (owned by a different standing architect,
  `cf-tunnel-arch`) covering Cloudflare Tunnel specifically as one piece of the
  single-node exposure story. Read `cf-tunnel-arch-state.md` to understand
  what's already decided/closed there so you don't duplicate or contradict it -
  if ptone's new topic overlaps, flag it and coordinate with `cf-tunnel-arch`
  rather than re-deciding something already converged.

## Your first task

1. Read the four docs above.
2. Introduce yourself to ptone on the thread and confirm you've done the
   context-building - ask for the specific implementation topic ptone wants to
   discuss.
3. Wait for ptone's specifics before producing any design work.

## Key Locations

- Project folder: `/scion-volumes/scratchpad/projects/single-node/`
- `GLOSSARY.md` (repo root) - tier definitions
- `scripts/starter-hub/` - current single-node hosted realization
- Standing conventions: `/scion-volumes/scratchpad/coordinator-conventions.md`
- Projects tracker (update as you go): `/scion-volumes/scratchpad/projects-tracker.md`

## Direct Contact

- **User:** `user:ptone@google.com`
- **Channel:** `discord`
- **Thread ID:** `1534555192450748456`
- Contact `cf-tunnel-arch` if your topic overlaps its converged Cloudflare
  Tunnel design.
- Contact `coordinator` (top-level) only for things outside your authority.

## Termination

Scope and duration depend entirely on what ptone asks for on the thread - this
brief only covers the context-building step. Do not mark complete until ptone
indicates the discussion/work is done.

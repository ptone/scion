# cf-tunnel-arch — session state / resume note

**Purpose:** if this agent is restarted or context-compacted, this file + the design doc are
sufficient to resume. Updated 2026-08-17T10:08Z.

## Deliverable (complete, reviewed)
- **Design doc:** `/scion-volumes/scratchpad/projects/single-node/cloudflare-tunnel.md`
  (NOT `/workspace/.design/` — moved to scratchpad 2026-08-17 per ptone's convention; a
  coordinator sweep note later cited the stale workspace path — the scratchpad is canonical).
- Companion inputs, same folder: `single-node-auth.md`, `single-node-packaging.md`
  (s-node-strat's; exist ONLY there, not in any repo).
- All file:line cites verified at repo HEAD `066eeba`. Review loop with s-node-strat is
  **converged/closed** (his sign-off 2026-08-17T01:09). No uncommitted work in `/workspace`
  (tree clean; nothing was ever repo-bound — deliverable is doc-only, no push required).

## Protocol state (the part not in the doc)
- **Point of contact: `user:ptone@google.com`** for all questions/decisions. Message via
  `scion message user:ptone@google.com "..."` — **2000-char limit per message, split not
  truncate.** Do not route through s-node-strat or coordinator.
- **Serial question queue, one at a time, wait for answer between.** Six total (doc §10):
  - **Q1 SENT 2026-08-17T00:59, AWAITING ANSWER:** remove dead header-provider /
    `extractProxyUser` code as part of Phase 1? (My recommendation: yes; s-node-strat concurs.)
  - Q2-Q6 NOT yet sent. Raise in order from doc §10 as answers arrive: Q2 CLI service-token
    passthrough (Phase 5) wanted now?; Q3 auth fields placement (Auth tab vs inline);
    Q4 provider name `cloudflare` confirm; Q5 hide Phase 4 UI when postgres?; Q6 make
    `server.base_url` a real settings key (correctness item; s-node-strat also sent ptone this
    finding independently — likely highest-leverage).
- **When an answer arrives:** update the doc in place (resolutions replace open questions;
  adjust phases/file list if needed), confirm back to ptone, then send the next question.
- **Standing commitment to s-node-strat:** if any ptone answer contradicts his two single-node
  docs (likeliest: Q6 vs the Addendum's three-URL-roles analysis; Q2 vs its CLI/transport
  discussion), message him to reconcile so the three co-located docs stay consistent.
- ptone intends to iterate; stay available after each exchange. Status signal in use:
  `sciontool status ask_user "Q1/6 pending with ptone"`.

## Facts established in-session but NOT fully in the doc (avoid re-derivation)
- Settings schema (`settings-v1.schema.json` `$defs/serverAuth`) lacks `mode`/`proxy` entirely
  → in doc §3.3/Phase 2. Admin settings: file/SQLite mode writes settings.yaml, postgres mode
  uses DB sections (`admin_settings.go:137-147`).
- Maintenance ops surface is admin-role-gated only (`admin_maintenance.go:40-44`), not
  workstation/loopback gated — chosen home for tunnel executors.
- Bearer-wins ordering `auth.go:231-234` (proxy authenticator only consulted when no bearer) —
  basis for the CLI/service-token story.
- My shell's `grep` is ugrep (disclosed to gke-deploy-lead 2026-08-17T09:14 re their
  instrumentation broadcast; no pinned-PATH numbers produced; no action outstanding).
- s-node-strat's brief/line numbers were from stale clone 52cf5a9 (~130 lines off) — trust the
  doc's cites, not the brief's.

## 2026-08-25 update
- Q1 still unanswered after 8 days; one nudge sent to ptone 2026-08-25 (no re-ask spam — the
  nudge restates Q1 compactly). If still silent, do NOT escalate through coordinator without
  cause; he owns the timeline.
- Drift check done at origin/main b1d9075c (81 commits past 066eeba): all structural claims
  hold; note added to doc header. #1246 (port-proxy redirect) is unrelated to auth-proxy.
- The `auth-refactor` project (Permissions Foundation, branch scion/auth-refactor, phases
  1E-1H accepted) is landing large pkg/hub auth changes NOT yet on main. Before the Cloudflare
  plan is implemented, whoever briefs the developer should check merge order vs. that branch —
  it touches role/permission surfaces adjacent to provisionUser/determineUserRole. Its QA
  broadcasts are noise for this design otherwise.

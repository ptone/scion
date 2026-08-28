# Round 2 — tasks #37/#48. Your recommended fix site is wrong. Here is why, and what I need instead.

Author: sn-impl-arch (architect). Date: 2026-08-28. **Dispatched. Start now.**

**Same hard constraint as round 1: CODE READING ONLY. TOUCH NO CLOUD INSTANCE.** Defect #67 destroys
the whole Instance ~8s after a 201, and `sn-harness-lab` is ptone's.

**Your trace was right and I am not re-opening it.** The hop-by-hop path from the default template's
absent `harness` field through to the broker's 502 is confirmed — I checked the load-bearing citations
myself. What is wrong is only the **fix site**, and it is wrong for a reason that is written down in the
repo, which is the interesting part.

## What I found after reading your note

You recommended: add `embeds/default_settings.yaml` as the first layer in `LoadBootstrapKoanf`,
mirroring `LoadVersionedSettings` at `settings_v1.go:1074-1077`.

Three problems, in increasing order of importance.

1. **Wrong keyspace.** `default_harness_config` is a **top-level** key in `default_settings.yaml`
   (`:32`). `LoadBootstrapKoanf` is explicit that *"All layers use snake_case keys matching the
   opsettings registry (e.g. `server.hub.admin_emails`, not `hub.adminEmails`). This ensures
   `ExtractSectionFromKoanf` can find values from any layer."* Merging that file raw would load the key
   at path `default_harness_config`, where nothing reads it. The change would appear to work — the file
   loads, no error — and change nothing. That is this project's signature defect shape, and I want you
   to notice that the recommendation walked into it.

2. **You did not need to add a source at all.** Layer 1 of `LoadBootstrapKoanf` is a hand-written
   confmap of coded defaults (`server.hub.port`, `server.database.driver`, …) carrying its own comment
   saying it is *"a manually maintained subset of GlobalConfig defaults"* that must be extended when new
   defaulted keys appear. If the answer is "seed a default", **that confmap is the place it goes**, one
   line, in the right keyspace.

3. **The machinery you are trying to build already exists, and its emptiness is deliberate.** This is
   the one that matters. See below.

## The thing neither of us saw in round 1

`pkg/hub/server.go:166-181` documents an `AgentDefaults opsettings.AgentDefaultsSettings` field —
Layer-1 operational settings, koanf keys `default_template`, `default_harness_config`, and five others.
`pkg/hub/hub_agent_defaults.go:96-151` is `applyHubAgentDefaults`, which stamps
`DefaultHarnessConfig` into `AppliedConfig` **only if empty**, positioned between `applyProjectDefaults`
and `populateAgentConfig` with a 30-line comment explaining that placement.

So the hub already has a designed, documented slot for exactly the value we are missing. **It is empty,
and at least one mode empties it on purpose:**

> *"In file mode this stays at its zero value: `BuildLayer1SnapshotFromFile` deliberately leaves the
> agent-defaults fields empty because a co-located broker reads the same settings.yaml and applies them
> itself at the BOTTOM of its own chain. Populating them hub-side as well would promote them to the hub
> tier and silently outrank broker profile resources and template limits. See the design's §3.2.4 and
> alternative A7."* — `server.go:173-180`

And `server.go:616-625` says `default_harness_config` is excluded from the hub→broker wire struct
`RemoteHubAgentDefaults` **by design**, because *"the hub must resolve those itself so it can stamp
TemplateID/TemplateHash and HarnessConfigID/HarnessConfigHash, and they therefore ride the existing
AppliedConfig ladder instead."*

Read together: **the intended design is exactly what we want** — hub resolves the name, stamps ID+hash,
broker hydrates from the store instead of from disk. The tier is broken not because the design is wrong
but because on this tier the value never arrives in `AgentDefaults`.

**Rule 19 applies.** Any fix here reverses a documented deliberate decision, so the reason for that
decision must be tested harder than the fix. The stated reason is *"a co-located broker reads the same
settings.yaml"*. My reading is that this **precondition does not hold in hosted mode** — there is no
`~/.scion/settings.yaml` (hosted mode skips materialization, `cmd/server_foreground.go:111-117`), and
the broker instead falls back to its own **embedded** default. That is the asymmetry you found. But that
is my reading and I want it checked, not adopted.

## What I need from you — four questions, code-reading only

Answer with `file:line`. Where an answer is "I could not determine this by reading", say so plainly and
name the experiment; do not run it.

1. **Which modes does `LoadBootstrapKoanf` actually feed?** `BuildLayer1SnapshotFromFile` is the file-mode
   path and is separate. Trace who consumes `LoadBootstrapKoanf` and confirm whether a change to its
   layer-1 confmap reaches **DB mode only**, or also file mode. If it reaches both, the by-design
   emptiness at `server.go:176` is violated by the fix and that changes my answer.

2. **Test the by-design reason against hosted mode.** Is it true that in hosted mode no co-located
   broker reads a shared `settings.yaml`, so the stated justification for keeping `AgentDefaults` empty
   does not apply? Find where the co-located broker gets its `DefaultHarnessConfig` in hosted mode and
   confirm it is the embedded file, not a shared on-disk one. **If the justification DOES still hold on
   this tier, say so first and plainly — that would kill the whole fix shape and I would rather learn it
   from you than from a reviewer.**

3. **Is `SCION_SEED_AGENT_DEFAULTS_*` a live alternative on this tier?** There is a fifth fix shape I
   want priced: change no product code, and have `deploy.sh` set a `SCION_SEED_*` env var. Blast radius
   zero. **But task #44 recorded "SCION_SEED_* is postgres-only, tier runs SQLite" as MEASURED BROKEN,
   and task #45 later recorded the koanf chain as sound end-to-end with the real fault downstream in a
   WebServer config split-brain.** Those two do not obviously agree. Settle it by reading: on SQLite, in
   hosted mode, does a `SCION_SEED_*` value reach a seeded opsettings section? Name the exact env-var
   spelling `agent_defaults.default_harness_config` would need — I do not want that guessed.

4. **What is the registry path and env spelling for the `agent_defaults` section?** I could not find a
   `pkg/opsettings` directory at that path. Locate `AgentDefaultsSettings`, its section key, and the
   koanf paths for `default_harness_config` and `default_harness_auth`. `applyHubAgentDefaults` sets
   `HarnessAuth` too, and I do not yet know whether that one also needs a default on this tier — check
   it and tell me, because a fix that supplies the config name and not the auth may just move the 502.

## What I am NOT asking

Do not recommend a single site this time. **Price the candidates.** I have five and I want each one's
blast radius named, not a winner declared:

- **A.** One line in `LoadBootstrapKoanf`'s layer-1 confmap (right keyspace). Affects every DB-mode hub.
- **B.** Give the bundled default template a `default_harness_config`. Lands at template rank, beneath
  project and request — arguably the most correct rank. But the template is shared across tiers.
- **C.** Deploy-time `SCION_SEED_*` only. Zero shared-code change. Depends entirely on Q3.
- **D.** Fail loudly: when `hcName == ""` reaches `populateAgentConfig`, return a 4xx naming the missing
  setting rather than letting the broker invent an unresolvable name. **Does not fix §1** — makes it
  diagnosable. I regard this as complementary to whichever of A/B/C wins, not as an alternative.
- **E.** Fix the broker's rung 7 to not return a name it cannot resolve. Also complementary.

For A, B and C give me: which tiers change behaviour, at what precedence rank the value lands, and
whether an operator can override it. **The rank matters** — `applyHubAgentDefaults` carries an
"ACCEPTED CONSEQUENCE" note that a hub-resolved harness config reaches the broker at CLIFlag rank and
outranks the broker's own profile and settings defaults. A hub-wide floor that outranks a template is
the precise inversion that whole workstream existed to remove. Say whether A re-introduces it.

## One thing I already settled, so you do not redo it

**Your open question 1 is closed for the fresh-deploy case, by reading.** `BootstrapBundledResources`
sets `skipHarnessConfigs` only when `ListHarnessConfigs(Active, Limit: 1)` returns a row; on a fresh
deploy there are none, so seeding proceeds. `resources/catalog.go:73-99` builds one harness-config
resource per directory in `harnesses.FS`, and `harnesses/embed.go:24` embeds `antigravity`. **So
`antigravity` IS in the store on a fresh hosted deploy — it is only missing from disk.** That is why
stamping ID+hash fixes it: the resource exists, the broker was just looking in the wrong place.

Residual, and I want it in your note: on a **non-fresh** deploy `SkipIfAnyExist: true` skips all
harness-config seeding, so a hub whose store has some other config but not `antigravity` would get a
default naming something absent. Whichever fix wins must degrade legibly there. That is a real argument
for D.

## Report

Message `agent:sn-impl-arch`. Update `investigations/harnesscfg-origin.md` in place — append a round-2
section, do not rewrite round 1; the round-1 trace is correct and I want it preserved as written.

**And tell me what in THIS brief is wrong.** Round 1's version of this paragraph earned its place — you
caught that the template has no `harness` field at all rather than an empty one, and you found two
non-browser origins I had not asked about. Do it again. In particular I have asserted in Q2 that the
hosted-mode precondition fails; if I am wrong about that, lead with it.

## Constraints

- **Touch no Instance:** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`,
  `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`, `sn-harness-lab`. **A restart IS a deletion.**
- Never print an access token.
- **You are not fixing this.** No product-code changes, no commits to product code.
- Local is `task #37` / `task #48`; GitHub is `owner/repo#NNNN`.

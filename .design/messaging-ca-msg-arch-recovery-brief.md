# ca-msg-arch — RECOVERY BRIEF. Read this before anything else.

Written 2026-08-30 04:30Z at 7% context, deliberately, so a post-compaction me has ONE short entry
point instead of an 8000-line log. If this file and the state doc disagree, the STATE DOC WINS — but
read this first for orientation.

## Who I am and the single most important standing correction

Implementation **coordinator** for the messaging tech-debt refactor. ptone (`user:ptone@google.com`)
is the owner. **I DISPATCH investigators/developers/reviewers. I DO NOT DO THE WORK.** His words,
02:02Z: *"as a coordinator you dispatch investigators, developers, reviewers. you do not do the
work."* The one verification I keep for myself is the numstat vs `upstream/main`, because it serves
his "do not revert other work on main" directive.

## THE HOLD IS IN FORCE

ptone, 02:25:45Z: **"ok. hang tight. block until i get back to you. it may be a while."**

No new staffing. No compare URLs. No escalations absent ACTIVE BREAKAGE (nothing currently qualifies
— the read-switch is OFF and nothing is broken in production).

**HOLDS LIVE IN PTONE'S MESSAGES, NOT IN THIS FILE.** Check his actual messages before acting in
EITHER direction. v18 carried a lifted hold as active; v19 carried "hold is lifted" into a real hold.
Both directions have already failed once.

## CURRENT STATE IN ONE LINE

Tranche E is COMPLETE and closed out. No ca-msg agents are live. Nothing is in flight. Everything is
pre-built and waiting on ptone.

| artifact | path | runes |
|---|---|---|
| PR (A) compare URL | `compare-url-prA.txt` | 1743 |
| PR (B) compare URL | `compare-url-prB.txt` | 1426 |
| Tranche E report | `pending-report-tranche-E.txt` | 1576 |

Send report -> Discord thread **1541161053118005308**. Compare URLs -> thread **1532864101909528737**.
User messages are capped at **2000 runes, SERVER-ENFORCED** — wrap every send in `if [ "$N" -lt 2000 ]`.
Agent-to-agent has no cap.

- PR (A) `scion/ca-msg-e1a` @ `5f95371d1` — 1018 added / 0 deleted, test-only, gate `ok pkg/hub 336.074s`
- PR (B) `scion/ca-msg-e1b2` @ `15db406` — 291 / 0, reviewed APPROVE, gate `ok pkg/hub 344.216s`
- `upstream/main` = `f1f86d3e0`. **`upstream` = GoogleCloudPlatform/scion. `origin` = ptone/scion and LAGS.**
- My branch `origin/scion/ca-msg-arch` @ `38282f64` (verify with `git ls-remote`).
- **I do NOT open PRs. Merging is ptone's gate.** Never push to `main`.

## WHAT PTONE OWES ME — do not re-ask, do not act without

1. **DEF-64** — fix now, or queue for Tranche G? (escalated 02:24Z)
2. **DEF-58** — staffing nod. This is a GATE change; brief item 12 says gate changes are not the
   changing team's call.
3. May `check-authz-reachability.sh` be deleted now the AST gates cover it?
4. contrib-repo cleartext PAT on a shared mount.

## THE HEARTBEAT IS THE OPERATING MANUAL

`ca-msg-impl-heartbeat-v24`, schedule `23a609f4-463e-4607-a21f-69d33ebbf27f`, cron `13,43 * * * *`.
Source: `heartbeat-v24.txt`. It carries the full sweep, the live-agent roster, all standing traps,
and every settled fact. **READ IT — it is more current than my memory will be.**

**`scion schedule create-recurring` does NOT replace.** I once ran v22/v23/v24 concurrently. Sweep
step 9 now checks the roster is exactly one. Verify it.

## SECTION 0 OF THE STATE DOC — settled by the owner, DO NOT RE-DERIVE

Chiefly: agents do NOT round-trip an external broker, and the **broker inbound partial checks are
misleading tech debt, NOT a security surface.** Three agents burned an investigation on that comment
block. Do not open a fourth.

## QUEUED, NOT STAFFED — do this first when the hold lifts

**Enumerate EVERY `ConversationReadSwitch()` call site** and check each for the DEF-64 authz
blindness, AND every early return upstream of one. My S1/S2/S3 list is a **SAMPLE I assembled while
investigating something else** — never verified exhaustive. Tranche G precondition.

## DEFECTS THAT CHANGE DECISIONS

- **DEF-73 settles the Tranche G go/no-go.** `IncFallback` has BOTH blind spots (3 paths where it
  never fires) and FALSE POSITIVES (fires for non-migration reasons: `GetTopic` failure, empty topic
  `ProjectID`). The counter is **uninterpretable, not merely imprecise.** The go/no-go comes from the
  OFFLINE REPORT (state doc 5ff), never the counter. Do not re-litigate.
- **DEF-72** — PR (A) test #4 accepts either 400 OR 200. The strict 5-part DM key parse is
  NON-SKIPPABLE in my own design (after the read-switch the DM key IS the ACL), so the most
  security-load-bearing assertion in PR (A) is the one that cannot fail.
- **DEF-64** — switch ON, a manager who already has a DM sees ONLY that DM. Intermittent by caller,
  invisible to smoke tests, and the metric cannot see it. Nothing is broken today (switch is off).
- **DEF-59a** — `scion messages --agent <slug>` silently returns EMPTY. Confirmed with a UUID control.
  NOT a cross-tenant read — `RecipientID: user.ID()` is unconditional. **I overstated this once; do
  not re-derive the overstatement from the record.**
- Open/unstaffed: DEF-53, 55, 56, 58, 59b/c, 60, 61, 62, 65, 66, 67, 68, 69, 70, 72, 73. Held ledger:
  DEF-5/6/9/10/18/32/33/35/34/12/46/47. **CLOSED: DEF-71.**

## HANDED OFF — NOT MINE, do not restaff

**32 of 45 permission-bearing `RouteHubAdmin` routes have no authz test.** NOT a vulnerability —
`guarded()` enforces at runtime. Owned by **`ci-fix-lead`** as a tracked item. Full writeup:
`findings/routeguard-authz-coverage-gap.md`.

## THE HANDFUL OF TRAPS THAT COST ME MOST

- **FOR ANY GATE, READ THE OUTPUT, NOT THE EXIT CODE.** A pipeline destroys the exit STATUS, not the
  output — piping is safe if you assert on the TEXT, fatal if you assert on `$?`.
- **`gofmt -l` exits 0 whether or not it lists files.**
- **`make test-fast` = `-tags no_sqlite`.** Untagged `pkg/hub` takes 5-7 min; tagged takes ~8s. Never
  compare two durations without confirming the same test set.
- **THE SHELL IS ZSH.** No `--file` on `scion message` — heredoc to `/tmp`, then `cat`. cwd resets
  after heredocs: use `git -C /workspace`.
- **A bare `@token` in a message body is a mention; an unmatched mention SUPPRESSES DELIVERY.** Check
  for `Message delivered to agent`.
- **`scion create` provisions WITHOUT starting. Use `scion start`.** `phase=created` +
  `lastSeen=0001-01-01` is CORRECT for a provisioned agent, not broken.
- **Briefs go in a FILE** at `/scion-volumes/scratchpad/briefs/<name>.md`; pass the path. Always give
  the explicit URL `https://github.com/GoogleCloudPlatform/scion` — agent workspaces have only
  `origin`, and one developer tried `github.com/scionproto/scion`, an UNRELATED project.
- **Retirement order:** extract to a durable artifact -> confirm downstream owners -> exit interview
  -> `git ls-remote` the branch -> THEN `scion delete`. `--preserve-branch` does NOT push.
- **The exit interview is deliverable work.** "What did you conclude from READING vs confirm by
  RUNNING?" produced DEF-53/54/55, 68/69/70, 71/72/73 and the 32-of-45 authz gap.
- **Workspace is `shared-plain`. NEVER `git add -A`.** Rule 4: never check out another branch in
  `/workspace`; use `git worktree` in `/tmp`.
- Rule 415: I may retire `ca-msg-*` agents I dispatched. I may NOT retire `ci-fix-lead`,
  `chat-admin-lead`, `coordinator`, or ptone's `ca-d-test` (stalled BY DESIGN — leave it alone).

---

## RESOLVED 2026-08-30 12:11Z — git push outage (kept for its lessons, NOT an active problem)

**DO NOT ACT ON THIS SECTION AS A LIVE INCIDENT.** Pushes work. `0da2811ba` is on
`origin/scion/ca-msg-arch`, ls-remote verified. Nothing of mine is unpushed. History follows.

```
remote: Invalid username or token. Password authentication is not supported for Git operations.
fatal: Authentication failed for 'https://github.com/ptone/scion.git/'
```

**Reads work, writes fail.** Confirmed **fleet-wide** by `coordinator`, which found the same failure
in its own credential config (`GITHUB_TOKEN` used directly as the push password, same token `gh` uses
for reads). It had already escalated the token failure to ptone twice before it began showing up in
`git push`. **NOT MINE TO FIX. DO NOT INVESTIGATE MY OWN CREDENTIAL** — coordinator said so
explicitly. It is folding this into its next ptone update.

**Resolution:** ptone rotated the token at ~11:28Z. I was omitted from coordinator's hand-built
relay list, so I kept retrying the DEAD token for ~40min and reported it as evidence the outage was
narrower than fleet-wide. It wasn't. **If a push ever fails again, FIRST re-read the token** —
`export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)` as the literal
first line of the push command (it does not persist across commands) — before theorising.

### State at the time of the outage

- ~~Everything through `38282f64`~~ — superseded: **`0da2811ba` is pushed and verified.**
- ~~One unpushed local commit `0da2811ba`~~ — **PUSHED 12:11Z.** Originally: — this recovery brief, docs only. **Its content is not
  at risk**: this scratchpad copy is the authoritative one and outlives the container. Push
  `0da2811ba` when auth is restored; if it is gone, re-copy this file to
  `/workspace/.design/messaging-ca-msg-arch-recovery-brief.md` and commit.
- Both Tranche E branches were pushed and verified BEFORE their authors were retired —
  `scion/ca-msg-e1a` @ `5f95371d1`, `scion/ca-msg-e1b2` @ `15db406`. **Nothing is stranded.**

### Possible cause — UNCONFIRMED, and the seam matters

`coordinator`'s hypothesis, explicitly marked by it as plausible-but-unverified: I raised a
**cleartext PAT on the contrib-repo shared mount** as a security concern; `coordinator` recommended
**rotating** it hours ago; if ptone acted on that, this outage is the direct and foreseeable side
effect. **Nobody has confirmed that is what happened.** Do not repeat it as established.

**Rule 614.** A credential rotation is an availability event, not only a security one. If you raise a
secret-exposure finding, the remediation you are implicitly asking for will break every consumer of
that secret — so name the consumers and the sequencing *in the same message as the finding*. I
flagged the exposure and said nothing about what depended on it.

**Rule 615.** When the fix for a problem you reported causes an outage, that is still the right
trade — a leaked credential is worse than a few hours of no pushes. Record it as a **sequencing**
failure, not as a reason to hesitate next time. The lesson is "warn the dependents," never "raise it
more quietly."

**Rule 616.** Duplicate durable artifacts across independent failure domains *before* you need them.
This brief survived a push outage only because it was written to a shared volume in the same action
that committed it to git — not by foresight about tokens, just by not trusting one channel.

**Rule 617.** When your observation contradicts the shared model, check whether you are missing an
update everyone else received before concluding the model is wrong. I had the rarer *state*, not the
better *evidence*. A lone dissenting data point is more often a stale observer than a broken
consensus — and I built three theories on a fact I simply did not have, nearly persuading coordinator
to retract a correct escalation.

**Rule 618.** A fleet-wide announcement sent to a hand-assembled recipient list will miss someone,
and the one it misses becomes the fleet's source of phantom incident reports. Enumerate recipients
from `scion list` at send time. Coordinator has adopted this.

**Rule 619.** When an incident is resolved, go back and mark the incident note resolved *at the point
where a future reader enters it* — the heading, not a footnote. A stale ACTIVE-INCIDENT banner in a
recovery brief is worse than no note at all, because it will be believed.

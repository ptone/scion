# Task #92 — a fresh hosted deploy defaults the runtime profile to "remote (kubernetes)", which cannot work on this tier

Author: sn-impl-arch (architect). Date: 2026-08-28, 02:45. Task #92. **§1 BLOCKER. ptone is awake and hit this live.**

## What happened, in his words

> "The default 'runtime profile' was set to 'remote (kubernetes)' which is non-functional. I'm actually
> trying to get the antigravity harness to work, but it crashed on launch."

This was a **fresh single-node Cloud Run deploy** that otherwise succeeded — script ran, instance deployed, IAP worked. So this is not a deploy failure. **It is the next thing an operator hits after the deploy works**, which puts it squarely on §1: *"…creates a project, starts a Claude agent, attaches to its terminal."*

## THERE ARE TWO OBSERVATIONS IN THAT SENTENCE AND I DO NOT KNOW THAT THEY ARE THE SAME DEFECT

1. The default runtime profile is `remote (kubernetes)`.
2. The antigravity harness crashed on launch.

**Do not assume (2) is a consequence of (1).** It is plausible — dispatching to a Kubernetes runtime that does not exist on this tier could produce a launch crash — and it is equally plausible that they are independent, and that fixing the default leaves the crash. **Task #92 is (1).** Establish whether (2) survives the fix and **report it as a separate finding**; do not silently fold it in, and do not chase it before (1) is fixed.

## The question that decides where the fix goes — answer it BEFORE you write any code

**Is this default tier-specific, or is it the general product default that every tier gets?**

This is not bookkeeping. `.design/hosted/cloud-run-single-node.md:390` states as a non-goal: **"Docker, Podman, and Kubernetes paths are untouched."** So:

- **If the wrong default is written somewhere hosted/Cloud-Run-specific**, fix it there and the non-goal holds.
- **If it is the global default for all tiers**, then changing it **violates the design doc's own non-goal** and alters behaviour for Docker, Podman and Kubernetes users who are not part of this work. **In that case do not change the global default. Come back to me** — the fix then has to be a hosted-tier override, or it becomes a product decision that is ptone's, not ours.

**Report the answer to this question to me before you commit a fix.** It is the one genuinely architectural choice in the task and I do not want it decided implicitly by whichever file you opened first.

## Surface I have already located, so you do not have to

I did one grep. **Treat these as starting points, not as the answer, and verify each yourself.**

| Path | Why it is interesting |
|---|---|
| `web/src/components/pages/agent-create.ts:1243-1260` | The "Runtime Profile" control ptone actually saw. Note the comment: *conditional: broker has profiles*. **What does it select when nothing is chosen?** |
| `web/src/components/pages/admin-server-config.ts:2778` | A setting whose hint is literally *"Default runtime profile for agents"*. |
| `pkg/config/settings.go:160`, `pkg/config/settings_v1.go:122` | `ResolveRuntime(profileName)` — where a name becomes a runtime. **What happens for the empty name?** |
| `pkg/runtimebroker/server.go:1034-1079` | `discoverAuxiliaryRuntimes` scans project settings for profiles. |
| `cmd/server_foreground.go:2771`, `cmd/doctor.go:150` | Other `ResolveRuntime` callers. |

**Four candidate mechanisms. I am not telling you which; I am telling you not to stop at the first one that looks right:**

- (a) A seeded/shipped settings document that lists a kubernetes profile first or names it as default.
- (b) The UI defaulting to the first profile in a list, where ordering is incidental.
- (c) An explicit `default_runtime_profile` value that is wrong for this tier.
- (d) `deploy.sh` never setting it, so a general default applies by omission.

**(b) and (d) are defaults by accident. (a) and (c) are defaults by decision.** The distinction matters for the fix and for the commit message.

## SUSPECTED FAMILY — read this before you start

Tasks #37 and #48 are open and look like the same shape: **the hub dispatches with no template identity, and the broker's local fallback is empty in hosted mode; the hub dispatches an EMPTY harness-config name and the broker invents one it cannot resolve.** Both are "hosted mode leaves a field unset, and something downstream substitutes a wrong value."

**If #92 turns out to be the same root cause, that is a much more valuable finding than a third point fix, and I want to know immediately.** Do not merge the tasks on your own authority — tell me and I will decide. But please spend the ten minutes to check, because three symptoms of one cause is a different piece of work from three bugs.

## What "non-functional" means, and please confirm it rather than assuming

ptone says the kubernetes profile is non-functional on this tier. **That is almost certainly right** — the design doc says *"NFS, no Filestore, no Kubernetes"* (`:48`) and this tier runs sandboxes as Cloud Run resources. **But confirm what actually happens when an agent is created under it**: a clear error, a silent hang, or a crash. That decides whether the secondary fix is "pick the right default" alone, or "pick the right default **and** fail legibly when a profile cannot work here."

**This project's standing rule applies: a message asserting a WRONG cause is more expensive than one asserting none.** If selecting kubernetes on this tier produces a confusing failure, the legibility fix is in scope as a second commit.

## Deliverable

1. **The answer to the tier-specific-vs-global question, sent to me before the fix commit.**
2. The mechanism — which of (a)-(d), or something I did not list — named by file and line.
3. The fix, on a branch off current `main`, pushed to `ptone/scion` only.
4. **A pin.** A test that fails on the old default and passes on the new one. **Mutate it and read WHY it went red** — a default-value assertion that passes because the code path never ran is the failure mode this project has hit repeatedly. **A negative assertion is not a pin until it has been observed positive.**
5. Whether the antigravity crash survives the fix, as a separate finding.
6. Whether this is the same root cause as #37/#48.

## Constraints

- **Do not change behaviour for Docker, Podman or Kubernetes tiers.** If the fix cannot avoid it, stop and tell me.
- New branch off current `main`, pushed to `ptone/scion`. **No upstream PR, no merge** — ptone's gate.
- **Do not touch `scripts/single-node/deploy.sh`.** It is frozen on another branch under review and ptone has validated it byte-for-byte on his Mac.
- Never print an access token. Credentials come from the metadata server; impersonate `scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`.
- Cloud testing: project `ptone-experiments`, region `us-east4`.
- **Touch no Instance that is not yours**: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`, `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, and especially **`sn-ready`, which is ptone's live instance**. **On this tier a restart IS a deletion** — all state is ephemeral.
- Exceeding the agent ceiling destroys the entire Instance about 8s after returning HTTP 201. Be careful what you create.
- Fully qualify GitHub issue numbers — 48 of 48 in `#1270`-`#1320` exist in **both** `ptone/scion` and `GoogleCloudPlatform/scion`. Local task IDs are written `task #92`, never bare.

## And tell me what in here is wrong

My four candidate mechanisms are a guess from a single grep. **A priority list is also a blind-spot list** — that lesson cost us a review round tonight. If the real mechanism is outside my list, **say so plainly and say what my list caused you to look at last.**

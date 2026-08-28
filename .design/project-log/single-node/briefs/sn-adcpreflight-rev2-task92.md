# Task #92 review — `scion/task-92-runtime-profile-fix` @ `54cc98b`

Author: sn-impl-arch (architect). Date: 2026-08-28, 03:10. **New task, not a continuation of #88.**

Your #88 APPROVE stands and is untouched. This is a different branch, different language (Go, not
shell), different developer (`sn-runtimeprofile-dev`). I am reusing you because you have the project's
mutation standard in hand, not because the subject is related.

## What the branch claims to fix

A fresh single-node deploy defaults the agent-create runtime profile to **`remote (kubernetes)`**, which
is non-functional on this tier. §1 says the operator *"starts a Claude agent"*. Today they cannot,
because the pre-selected profile cannot run one.

Root cause, established by the developer and accepted by me: `GetRuntime` substitutes `docker` ->
`cloudrun-sandbox` at the **runtime** layer and never writes that back to the **profile** layer. The
`local` profile still declares `docker`. `buildInfoProfiles` (`pkg/runtimebroker/handlers.go:206`) then
drops `local` for being local-only while the broker is not, and keeps `remote`/kubernetes. One profile
survives, so `autoSelectProfile()` (`web/src/components/pages/agent-create.ts:607`) fires on
`length === 1` and selects the broken one.

The fix seeds a tier-specific settings template via `InitMachine`, following the sibling multi-node
tier's `scripts/cloudrun/hub-settings-template.yaml` precedent. `deploy.sh` is deliberately untouched
(it is frozen under task #88).

## The one thing I most want measured, and it is not in their tests

**Their own report says the fix does NOT restore auto-selection.** After the koanf merge the effective
settings carry **three** profiles; `buildInfoProfiles` returns **two** (`remote/kubernetes` and
`default/cloudrun-sandbox`). At length 2, `autoSelectProfile` does **not** fire.

Their argument that this is still correct rests on a single claim:

> UI shows 'Use broker default' (empty), which resolves to active_profile 'default' -> cloudrun-sandbox
> -> works.

**That claim is load-bearing for the entire fix and I can find no test of it.** Every test they list is
Go-level — config layer and broker layer. The assertion is about what the **browser** does with a
two-element list and an empty selection. Nobody has executed that.

**I rejected two earlier proposals from this same developer for precisely the property this fix has** —
adding a right answer without removing the wrong one, leaving length at 2 and auto-select silent. I may
be wrong to accept it now. The difference, if there is one, is that the *empty* state is correct here.
**Establish whether that is true by execution, not by reading the resolution path.**

If it is true, the fix is good. If the empty selection resolves to anything else — first list entry,
`active_profile` read from a different layer, or a request rejected for a missing profile — then §1 is
still blocked and the branch must not land.

## Four more things, ranked

1. **The fallback path reproduces the original defect.** Detection is `CLOUD_RUN_INSTANCE` set **AND**
   `/usr/local/gcp/bin/sandbox` present. When the binary is absent it falls back to docker defaults —
   which is the exact state the bug report describes. **Is that fallback reachable on a real Instance?**
   Note `GetRuntime` has a matching branch for Instances *without* a sandbox launcher, so the codebase
   evidently believes that state exists. If it is reachable, this fix has a hole in the shape of its own
   bug.
2. **A pin may be on a non-determining layer.** `TestInitMachine_CloudRunSandbox_SeedsCorrectProfile`
   asserts exactly one profile, "no local, no remote". But the developer states that the koanf merge
   re-adds local and remote regardless of the seeded file. So that pin asserts a state that does not
   survive into the state the system actually uses. It is not necessarily wrong — seeded file and
   effective settings are different layers — but **say whether it pins anything that determines
   behaviour.** Rule 3 applies: a pin has a location as well as an assertion.
3. **`TestBuildInfoProfiles_OldWorkstationDefaults_Task92_Regression` is the interesting test.** It
   claims to document the defective state. **Run it against the fix and confirm it still passes for the
   right reason** — a regression test that documents a bug must keep reproducing the bug, and one that
   quietly starts passing because the defect moved is worse than absent.
4. **`defaultSandboxBin` is duplicated** into `pkg/config` because config cannot import runtime
   (circular). A comment marks it. Comments do not stay in sync. Say whether a shared constant package
   or a build-time assertion is warranted, or whether the duplication is acceptable at this size. Your
   call; I want it named, not necessarily fixed.

## Standard, unchanged from #88

- **Mutate every pin and read WHY it went red.** A red is necessary, not sufficient.
- **Rule 18 applies and may bite here:** the per-location matrix only separates locations when the
  defect is input-dependent. If a mutation aborts the enclosing unit, off-diagonal green is unobtainable
  in principle. If you hit that, say so — do not manufacture separation.
- **Rule 12:** the clean case is load-bearing. An instrument that only fails correctly is not correct.

## Constraints

- **Review only. Change no code, push nothing, open no PR.** If you find a defect, tell me and I brief
  the developer. That is the process and I have broken it before.
- **The branch may move under you.** Protocol from #88 holds: if the developer pushes, they announce it
  and name the file, and I re-point you and scope the re-read to the delta. Do not re-read the whole
  branch on your own initiative.
- Never print an access token. Touch no Instance: `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk`, `sn-ready`. **A restart IS a deletion.**
- Fully qualify issue numbers. Local tasks are `task #92`; GitHub is `owner/repo#NNNN`.

## Report

Verdict in the #88 terms (Critical / Required / Nit). Lead with the empty-selection question — if that
one is unresolved, say so plainly and do not let the other four dilute it.

**And tell me what in here is wrong.** In particular: I accepted a fix with a property I twice rejected.
If my stated reason for the reversal does not hold up, that is the most useful thing you can tell me.

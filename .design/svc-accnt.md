# Design: Service Account Rework — IAM-gated assignment & hub-level SAs

**Status:** Proposed — approved for implementation. Sections marked ⏳ are open *implementation*
choices behind the frozen interface in §5; none of them block the phases now in flight. §10
is the authoritative list.
**Date:** 2026-07-28
**Supersedes the deferral in:** `.design/hosted/sciontool-gcp-identity-pt2.md` §1.3
**Implements the unbuilt requirement in:** `.design/hosted/sciontool-gcp-identity.md` §"Authorization Requirements"
**Related:** `ptone/scion` #591, #595–#600 (the authorization track this design depends on and
partly motivates)

**How to read this document.** It carries an unusual amount of *provenance* — who found what,
which claims were wrong and were corrected, and which conclusions are discharged. That is
deliberate. Nearly every hazard recorded here was first mis-stated by someone competent, and
in several cases the mis-statement was the thing that made the hazard survive review. Sections
that read as over-argued (§4.1, §5.1, §8.3) are argued at that length because a shorter
version of the same claim was previously acted on incorrectly.

**Two conventions used throughout.** Line numbers are stamped with the SHA they were verified
at; treat an unstamped line number as stale. Superseded reasoning is struck or quoted in place
rather than deleted, because the reasons a wrong conclusion was attractive are what stop it
being reached again.

> ### ⚠️ Which tree this document describes
>
> **Every line number and every source quotation here is relative to
> `origin/scion/svc-accnt-lead` @ `5985b0fd`, not to `main`.**
>
> As of `origin/main` @ `db8f6fc5`, **main contains neither #591 nor #595.** `matchesResource`
> on main is at `authz.go:357` and still carries the original fail-open form. Both fixes exist
> only on the integration branch (`4c0b6757` for #595, `fc71ffef` for #591), which is 41 commits
> ahead and unmerged.
>
> This matters in two directions:
>
> 1. **Every "discharged" claim in this document — §8.3's hard gate, and the structural
>    confinement of the assign-grant baseline in §8.2 — is true of the integration branch and
>    false of main.** A reader who checks out main and greps the cited lines will find fail-open
>    code, and may reasonably conclude the document is wrong about something else.
> 2. **#595 is a hard merge predecessor for this project.** Nothing here may be cherry-picked
>    to main ahead of it. The assign-grant baseline is confined *only* by #595's fail-closed
>    rejection of parentless resources; on a tree without that fix it grants `assign` on every
>    hub-scoped SA to every project member — the §8.2 hole again, reached by a different route.
>
> Recorded because the discrepancy was found by two agents citing the same clause at different
> line numbers and both being right. The SHA convention above is what surfaced it; the branch
> name is the part the convention was missing.

---

## 1. Background

`.design/hosted/sciontool-gcp-identity.md` §"Authorization Requirements" specified:

> **Assigning a service account to an agent**: Requires `assign` permission on the
> specific service account resource, in addition to agent creation permission. This
> prevents an agent creator from assigning arbitrary service accounts they don't have
> permission to use.

That permission was never built. `-pt2.md` §1.3 then tabulated three options for it
(grove-admin-only / explicit assign permission / any grove member), recommended
"grove admin only", and explicitly left the decision unresolved. **This document is that
resolution** — and it resolves it differently, because the intervening `mint` work and the
agent-spawns-agent capability changed the threat model.

What shipped instead was `ActionRead` on the SA resource, with a comment stating the
weakest of the three options outright:

```go
// Authorization: any project member who can see the SA can assign it.
```

...guarded by `if userIdent != nil`, so it does not run for agent callers at all.

---

## 2. Threat model

The asset is the **authority a service account carries in the customer's GCP
organisation** — not the SA record in the Hub database. An SA binding is a grant of real
cloud privilege.

**Primary threat — lateral privilege escalation via agent creation.** An agent holding a
low-privilege SA creates a child agent bound to a high-privilege SA, then uses the child
to act with privileges its own principal was never granted. Today this is unchecked: the
only guard is user-only and agents skip it.

Properties that make this materially worse than the equivalent human mistake:

- **Machine speed and scale.** An agent can enumerate every SA in a project and attempt
  assignment in a loop.
- **Silence.** The skip produces no error, no log, no audit record (there is no audit
  event for SA assignment at all — see §7).
- **Durability.** The child's JWT carries `project:gcp:token:<sa-id>` for its full 10h
  lifetime; the escalation outlives the request that caused it.

**Secondary threat — assignment surfaces other than create.** There are four distinct
paths that bind an SA to an agent (§4). Hardening only `POST /api/v1/agents` leaves three
open, and the PATCH path is currently the *weakest* of the four, so it would become the
preferred route.

**Explicitly out of scope.** Runtime token theft from a correctly-assigned SA; compromise
of the Hub's own SA; anything reachable only by a GCP project owner acting in their own
project. Those are pre-existing and not made worse by this work.

### 2.1 What the gate is — and the two things it is not

Stated up front because both limits are structural, hold under **every** mechanism Q2 might
select, and are easy to over-claim when writing the change up.

**It is an admission gate, not a continuous control.** The check runs at *assignment* time.
Revoking `actAs` afterwards does not unassign the SA, does not stop the running agent, and
does not invalidate the JWT already issued — which, per the durability point above, carries
`project:gcp:token:<sa-id>` for its full 10h lifetime. Revocation stops the *next*
assignment, not the current one. Anything stronger requires re-validation on token issuance
or a revocation sweep, neither of which is in this project's scope.

**Its revocation is loose, and no mechanism choice fixes that.** Verified against Google's
IAM access-change-propagation documentation:

| Change | Typical | Maximum |
|---|---|---|
| Allow/deny **policy** edit | ~2 min | *"potentially 7 minutes or longer"* |
| **Group membership** change | "several minutes" | *"potentially hours or longer"* |

Google states that during propagation *"principals might still be able to use a recently
revoked role,"* and that removal propagates **slower** than addition. Since `actAs` is
commonly granted via group, the hours case is the realistic one, not the pathological one.

Two things follow. First, the Hub's own decision cache (Q6, 60s allow-TTL) is **not** the
dominant term in the revocation window and must not be defended as though it were — that
reasoning appeared in an earlier draft of Q6 and has been withdrawn. Second, because
`testIamPermissions`, `getIamPolicy`, Policy Troubleshooter and impersonation all read the
same eventually-consistent state, this **cannot be used to argue for one Q2 option over
another.**

Neither limit undermines the project. The primary threat in scope is an agent *acquiring*
privilege it was never granted, and an admission gate is the right shape for that. These are
recorded so the change is not described as delivering prompt revocation, which it does not.

---

## 3. Principal model — whose permission is checked

**RESOLVED (Q11).** The check evaluates exactly **one** principal: the immediate caller.

| Caller kind | Principal evaluated |
|---|---|
| Agent | The calling agent's own service account (`AppliedConfig.GCPIdentity.ServiceAccountEmail`) |
| Human (Google OAuth) | The user's Google account principal (`user:<email>`) |
| Human (GitHub OAuth) | **No GCP principal exists** — the check cannot be evaluated, so it denies. See the note below |
| Broker / unknown | Denied — fail closed (see security track) |

> **On GitHub-OAuth users.** This began as Q1 and was overtaken by Q1's actual ruling: the
> check is a **hub-level toggle, OFF by default**. A Hub whose users authenticate by GitHub
> has no way to express `actAs` for them and should leave the toggle off, which is the
> default — so nothing breaks. Turning the toggle *on* is an assertion that callers have GCP
> identities. The failure is therefore loud and configuration-shaped rather than silent,
> which is the right place for it. There is no partial mode and should not be one: a
> per-principal-kind exemption would make the toggle mean "enforced except where
> inconvenient", and the exempt kind would become the assignment route.

**No ancestry walk.** `Ancestry[0]` / `OriginUserID()` is *not* consulted. Checking the
originating human would be weaker, not stronger: an agent started by an admin but holding
a low-privilege SA could otherwise pass on the admin's authority to a child. Ancestry
retains its existing meaning (ancestors may access descendants) and plays no part here.

**Operational definition of "same-or-lesser".** GCP provides no ordering over service
accounts, so a literal subset test over effective permissions is not computable. The
IAM-native expression of the intent is:

> creator may assign target SA **Y** iff the creator's principal holds
> `iam.serviceAccounts.actAs` on **Y**. *(Q3, locked: `actAs` alone —
> `roles/iam.serviceAccountUser`. Not `tokenCreator`, and not `getAccessToken`.)*

This is an explicit delegation grant made by Y's owner. It delivers the anti-escalation
property — a principal cannot hand out authority it was not itself granted the right to
delegate. *Interpretation confirmed with ptone.*

### 3.1 Derived rules

**Same-SA propagation is auto-allowed.** An agent holding SA X binding SA X to its child
grants no new privilege — the child gets exactly what the parent already had. Short-circuit
before any IAM call. Necessary for ergonomics: GCP does not grant SAs `actAs` on
themselves by default, so requiring the check here would break the common case (an
orchestrator spawning workers that share its identity) and push users toward granting
self-`actAs`, which is worse.

**A `block`-mode agent cannot assign any SA.** It has no GCP principal, so there is
nothing to evaluate and nothing it could have been delegated. Fail closed. *Flagged to
ptone as possibly too strict; interacts with Q4 (project defaults).*

---

## 4. The four assignment surfaces

Any design hardening only surface (a) is incomplete. All four must reach the same
decision function.

| # | Surface | Today | Plan |
|---|---|---|---|
| a | `POST /api/v1/agents`, explicit `gcp_identity` | `ActionRead`, user-only | Full gate |
| b | Project-default annotation (`scion.io/default-gcp-identity-service-account-id`) | **No check.** The path virtually every CLI-created agent takes, since `hubclient.CreateAgentRequest` has no `gcp_identity` field | Gate required by Q4. **Whose** permission is checked here is the one part still open — see §10 |
| c | `PATCH /api/v1/agents/{id}` | **No authz at all**, and no scope-match or verified validation | Full gate + validation parity (security track, Q10) |
| d | Lifecycle hook `execution_identity` | Admin-only; existence + verified + scope checks | Full gate; lower risk but must not remain an exception |

Surface (b) deserves emphasis: because the CLI cannot send `gcp_identity`, the project
default is the **dominant** real-world path. A design that gates (a) rigorously and leaves
(b) open would gate the road nobody drives on.

### 4.1 `POST /api/v1/projects/{id}/agents` — a route, not a fifth surface

*Added 2026-07-28, prompted by the agent-id-fix track reclassifying `createProjectAgent`.*

`createProjectAgent` (`handlers_projects_core.go:1720`) accepts the same
`CreateAgentRequest` and delegates to the same `createAgentInProject` (`:1766`), where all
SA-assignment logic lives (`handlers_agents_core.go:423-455`). The surface-(a) gate is
therefore **inherited by construction** — placing the check in `createAgentInProject` covers
both routes, and adding a second gate on the project route would be duplication that can
drift out of sync.

This is the design rule worth stating explicitly: **the gate belongs at the shared
chokepoint, not on each route.** The bug class this project exists to fix is precisely what
happens when equivalent paths carry non-equivalent checks.

That rule is urgent rather than tidy, because the ungated route is the busy one. Per
aid-arch (2026-07-28): `hubclient.agentsPath()` switches to the project-scoped URL whenever
`ProjectAgents(projectID)` is set, and `createAgentWithBrokerResolution`
(`cmd/common.go:1136-1148`) — the single funnel for **both** CLI create paths — always uses
it. So `scion create`, `scion start` and `scion sync` all traverse
`POST /api/v1/projects/{id}/agents`. Only the web UI and the A2A bridge use the gated
`POST /api/v1/agents`. A gate placed on the `/agents` route alone would cover the minority
of traffic and miss the CLI entirely.

This also corrects an earlier claim of mine. I had argued surface (b) dominates *because the
CLI cannot send `gcp_identity`* — the premise is right (`hubclient.CreateAgentRequest` has no
GCP field, and a Go zero value serialises as absence, not an empty object) but it is not what
makes the project route matter. The empty-object governance hole is reachable only from a
hand-rolled HTTP caller, so it is defence-in-depth, not live CLI exposure. The route being
ungated is the live issue, and it is independent of the payload question.

Which the project route already demonstrates. It skips the field-level GCP validation block
(`handlers_agents_core.go:294-311`) that `createAgent` performs, so it accepts a
`service_account_id` alongside `metadata_mode: block|passthrough`, and accepts an
unrecognised `metadata_mode` outright. That is the third instance of `createProjectAgent`
omitting a check its sibling performs — it also omitted `ScopeAgentCreate` and the
project-match. Validation must move to the chokepoint for the same reason the gate does.

**Ownership:** the agent-id-fix track is performing this hoist — `validateGCPIdentityRequest()`
lifted into `createAgentInProject` — as part of its own work on this file. svc-accnt does
**not** implement it; see the svc-accnt implementation plan, §P3.5. This design states the rule and depends on
the outcome; it does not claim the change. When svc-accnt's gate lands, it lands beside an
already-hoisted validator at the same chokepoint.

**Security note on unknown `metadata_mode`:** the Hub currently fails closed only by
accident — the switch at `:569` has no `default:` arm, so an unknown mode yields a nil
`GCPIdentity` and no env injection. Both downstream layers fail *open*: the broker
(`start_context.go:360`) leaves `GCE_METADATA_HOST` unredirected, exposing the real GCE
metadata server, and the sidecar (`metadata/server.go:631, 679`) denies only on
`== modeBlock`. Both are deny-listed on `block` rather than allow-listed on known modes.

*Correction, 2026-07-28, from sa-dev-p0:* I previously wrote that `:332` "skips the iptables
hardening." That is wrong and understates the problem. `setupIPTablesRedirect` (`:322`) runs
**unconditionally** once the uid/network-mode check passes; `:332` gates only the additional
*filter-level block*. So a sidecar running in an unrecognised mode does not merely fail to
harden — it installs the nat REDIRECT, captures metadata traffic to itself, and then answers
it (the endpoints do not deny for non-`block`) with an empty `SAEmail`. The failure is a
traffic hijack, not an absent protection. Validation must therefore **strict-reject**, never
normalise or pass through.

The Hub-side strict-reject is the agent-id-fix track's (implementation plan §P3.5). Converting
the two downstream layers from deny-on-`block` to allow-list-on-known is svc-accnt's, as
**§P7** — a separate phase precisely because it is unblocked while the Goal 1 gate is not.
The two are independent defences and neither substitutes for the other: strict-reject stops
bad input entering, allow-listing stops bad state already stored from being honoured.

---

## 5. The permission check — ✅ **INTERFACE FROZEN**

> **Frozen 2026-07-28, at the request of the phase that needed it.** P3 must wire routing
> before P2 exists. The *mechanism* is still open (Q2, and Q14/Q15/Q16 with ptone), but the
> **signature is frozen now** and P3 may define it plus a fake. P2 lands the concrete
> implementations behind it and **must not change this signature**. The whole point of the
> shape below is that every open question is an *implementation* choice.

```go
// pkg/store — placement matters; see §5.2.
type ActAsOutcome int

const (
    // Zero value is INDETERMINATE, deliberately. A result struct that was
    // never populated must not read as "allowed".
    ActAsIndeterminate ActAsOutcome = iota
    ActAsAllowed
    ActAsDenied
)

type ActAsResult struct {
    Outcome   ActAsOutcome
    Mechanism string // which check produced this — required for audit (§7)
    Reason    string // human-readable; surfaced to the caller on denial
}

type CallerPermissionChecker interface {
    CanActAs(ctx context.Context, caller Principal, targetSA *store.GCPServiceAccount) (ActAsResult, error)
}
```

**`Principal` — frozen too. My omission, sa-dev-p3's design.** The signature above referenced
a `Principal` type that **did not exist**: `pkg/store` had no such type, and `pkg/hub`'s
`Identity` is unreachable from `store` under the import direction in §5.2. I froze a
signature with a dangling type in it. p3 hit it immediately, defined one, and flagged it as
their decision rather than quietly adopting it — the right call, and the result is now
frozen as specified:

- `PrincipalUnknown = 0`, so an unpopulated principal is **not** a valid caller.
- `HasGCPIdentity()` returns **false** for a block-mode agent with no SA email.

Both are the same construction the three-valued `ActAsResult` uses, applied one level up:
**the unknown caller and the block-mode agent fail closed by the zero value, not by a check
someone has to remember to write.** This matters specifically because Q11's immediate-creator
model makes the agent's own SA the impersonation principal — an agent with no SA cannot be
checked at all, so "cannot determine" must not be reachable as "allowed". Note the Q1 toggle
still short-circuits ahead of all of this (§5, property 3), so an operator with the check off
sees no behaviour change from block-mode agents.

**Three properties are load-bearing. Do not "simplify" any of them.**

1. **Three-valued, not a bool.** Q15 and Q16 are both arguments about what to do when the
   check *cannot reach an answer* — Policy Troubleshooter returning `UNKNOWN*`, the
   `getIamPolicy` fallback being blind to IAM Deny/PAB. If the interface returns
   allowed/denied only, "could not determine" has nowhere to live and gets flattened into
   one of them **at the point of least information**. With `ActAsIndeterminate`, Q15/Q16
   become a policy decision at a single call site and **either ruling is a one-line change**.
   A bool would make ptone's answer a signature change across P2, P3 and every test.
2. **`error` is for programming and transport failures only — never for "denied" and never
   for "unknown".** A checker that returns `(ActAsResult{}, err)` on an API timeout is
   returning `ActAsIndeterminate`, which is correct and explicit. Overloading `error` with
   denial is how a caller that forgets to check `err` fails open.
3. **The Q1 toggle is evaluated by the CALLER, not inside the checker.** The checker answers
   "may this principal act as this SA"; it does not answer "do we care". Keeping the toggle
   out makes the checker pure and testable, and stops the off state from being re-implemented
   in each of the assignment surfaces.

### 5.2 Placement — `pkg/store`, not `pkg/hub`

**State the reason as cohesion, not as a dependency workaround** — the distinction is
practical and aid-arch is right that it decides whether the placement survives. The import
graph does force it (`pkg/hub` → `pkg/lifecyclehooks` → `pkg/store`; `store` imports
neither, so it is a genuine common ancestor and there is no cycle, and the hook
execution-identity path is a Q4 consumer). But "we put it in `store` because `hub` could not
reach it" invites the next refactor to move it somewhere more convenient — and the moment it
moves into `hub`, the `lifecyclehooks` consumer silently stops being covered, which is
exactly the failure this structure exists to prevent.

The durable reason: **this is a predicate over `store.GCPServiceAccount`'s own fields.** It
belongs to the type, not to either consumer. Put that in a comment at the definition.

### 5.4 ⏳ OPEN OPTION — user-supplied ID-token assertion (proposed by ptone, 2026-07-28)

**The proposal.** Give the user a `gcloud` command to impersonate the SA and mint an **ID
token** from it; they paste it back; the Hub verifies the Google signature and stores the
assertion with an expiry. ptone raised the obvious weakness themselves — no guarantee the
permission still exists at agent-create time.

**Assessment: sound but incomplete.** An excellent *third allow-path*; a bad primary.

**⚠️ It proves the wrong permission, and cannot be made to prove the right one.** Minting an
ID token needs `iam.serviceAccounts.getOpenIdToken`, which lives in
`roles/iam.serviceAccountTokenCreator`. **Q3 locked the check on
`iam.serviceAccounts.actAs`**, i.e. `roles/iam.serviceAccountUser` — which does **not** grant
`getOpenIdToken`. So the scheme *denies* exactly the population Q3 said to allow, and
*accepts* holders of the permission Q3 explicitly rejected.

This is not fixable by picking a different token. **`actAs` is a permission to ATTACH an SA
to a resource, not to obtain credentials — so no credential can prove you hold it.** Any
unforgeable token-based proof necessarily demonstrates a `tokenCreator`-family permission
instead. Worth stating as a general limit, because it will be re-proposed.

**But it errs only in the safe direction, which is why it is still worth having.** Anyone who
can mint tokens as SA *X* can already act as *X* anywhere, outside Scion entirely. Against
§2's threat model — privilege escalation *via attachment* — a valid ID token is a conclusive
proof of **non-escalation**, even though it is not a proof of `actAs`. It never wrongly
allows; it only wrongly denies, and only the `serviceAccountUser`-only population.

**Staleness is smaller than it appears.** *Every* option here is point-in-time; Policy
Troubleshooter describes the policy as of the call, and the agent may run for weeks. The
stored assertion does not introduce staleness — it makes it **visible and numeric**. What
must be decided is the failure direction at expiry: **re-prompt-or-deny, never
continue-on-expired.** This is the one mechanism that *can* fail open, so expiry handling
carries the entire safety argument.

**🛑 Replay hazard — must be designed in, not bolted on.** The ID token's identity claim is
the **service account**, not the user. It proves "someone who can mint as *X* produced this",
not *who*. Handed to a colleague, it works for them, and the audit trail then names the wrong
person. **Fix: the Hub issues a short-lived nonce and requires it as the token's `aud`, bound
to the authenticated user requesting it.** Without that the assertion is bearer-transferable.

**Placement — this may resolve Q16 rather than sit beside it.** Proposed as the **BYOSA
fallback, replacing the `getIamPolicy` fallback** I asked ptone to drop. It is strictly better
on the axis I objected to: `getIamPolicy` fails **open** against IAM Deny / PAB and does so
most often in exactly the orgs that use them, whereas here a valid token is positive evidence
and no token means no assertion. It also needs **zero new Hub grants**. Resulting ladder:
agent callers → impersonate + `testIamPermissions`; humans → Policy Troubleshooter where
available; **neither → this, as an offer rather than a degrade.**

### 5.3 Mechanism — recommendation, still open

Per-principal-type strategy, because what the Hub can do differs by principal:

- **Agent caller** → impersonated `testIamPermissions`. The Hub already holds
  `roles/iam.serviceAccountTokenCreator` on every verified SA (that is precisely what
  `VerifyImpersonation` proves, `gcp_token_iam.go:101`), so it can mint a `cloud-platform`
  token *as the calling agent's SA* and call `testIamPermissions` on the target with it.
  GCP evaluates the caller's true effective permissions — inheritance and group membership
  included. **Zero new Hub grants, zero new API enablement.**
- **Human + hub-minted SA** → the SA lives in the Hub's own GCP project, so Policy
  Troubleshooter works with no user friction.
- **Human + BYOSA** → Troubleshooter if the Hub was granted it, else documented degrade.

Why not the alternatives: `getIamPolicy` misses inherited and group grants (false
denials), *and* requires `iam.serviceAccounts.getIamPolicy`, which `tokenCreator` does not
grant — so it needs a new grant from every BYOSA user. Policy Troubleshooter has the same
grant problem plus API enablement. Impersonation rides permissions the Hub is already
guaranteed to have.

Since Q11 settled on the immediate-creator model, the agent branch is the dominant path —
which is exactly the branch that needs no new grants.

### 5.1 Constraint — the gate must deny when the Hub has no GCP token generator

*Added 2026-07-28, from sa-rev-p0's R-3 during P0 review.*

The recommendation above rests on a premise worth stating explicitly, because it is not
universally true: *the Hub holds `tokenCreator` on every verified SA, because that is what
`VerifyImpersonation` proves.*

That premise fails on a Hub with no GCP token generator configured.
`verifyGCPServiceAccount` (`handlers_gcp_identity.go:402`) guards **only the failure branch**:

```go
if s.gcpTokenGenerator != nil {
    if err := s.gcpTokenGenerator.VerifyImpersonation(ctx, sa.Email); err != nil {
        sa.Verified = false; sa.VerificationStatus = "failed"; ...
        return
    }
}
sa.Verified = true          // reached unconditionally when the generator is nil
sa.VerificationStatus = "verified"
sa.VerificationError = ""
```

When the generator is nil, nothing is verified and the SA is still marked `verified` with
its error cleared. P0.1 made that unearned status **durable** rather than recomputed on read.

It is the *only* site that behaves this way. All four handlers that touch the generator were
checked (three by sa-rev-p0, and the contrast is the point):

| Site | Handling of `gcpTokenGenerator == nil` | Result |
|---|---|---|
| `:187` `createGCPServiceAccount` | nil check wraps the **whole** auto-verify block | SA left `unverified` — truthful |
| `:816` access-token issuance | explicit nil check | `503 gcp_not_configured` |
| `:896` ID-token issuance | explicit nil check | `503 gcp_not_configured` |
| `:402` `verifyGCPServiceAccount` | nil check wraps **only the failure branch** | falls through to `verified` |

Three of four wrap the whole operation; one wraps the failure path. That asymmetry reads as a
slip rather than a policy — which is precisely why the constraint below must be stated
explicitly rather than left for P3 to infer the house style from whichever site it copies.
Copying `:187`, `:816` or `:896` yields the right answer; copying `:402` yields a silent pass.

**Constraint:** the Goal 1 gate must **explicitly deny** when `gcpTokenGenerator == nil`. It
must not inherit the fall-through above, and it must not treat `sa.Verified` as sufficient
evidence on its own. Two independent reasons, either sufficient:

1. `verified` may be unearned, so it is not proof of a `tokenCreator` grant.
2. The impersonated `testIamPermissions` call the agent branch depends on **cannot be made
   at all** without a generator. There is no degraded mode here — there is no check.

This is the correct outcome regardless: a Hub that cannot talk to GCP has no business
authorising GCP service-account assignment. The failure mode to avoid is the gate silently
passing because `sa.Verified` was true.

*(Fixing `verifyGCPServiceAccount` itself is a separate question — it is pre-existing and
also surfaces in Goal 2's hub UI, which would display an unearned "verified" badge. The
constraint above holds whether or not that fix lands, which is why it is stated as a
property of the gate rather than a dependency on someone else's change.)*

**Caching (Q6, decided):** decision cached on
`(callerPrincipal, targetSAEmail, permissionSet)`. Allow TTL 60s, deny TTL 10s —
deliberately asymmetric so a just-fixed grant takes effect promptly while a retry loop
still can't hammer the IAM API. Invalidate on SA delete and on Hub-initiated policy change.

**Failure handling (Q7, decided):** fail closed on error; distinguish "not configured"
(feature already inert — no verified SAs exist, so nothing to gate) from "call failed".
Escape hatch `gcpIamCheckMode: enforce | off`, default `enforce`.

---

## 6. Authorization shape — the `assign` action

Add `ActionAssign` to the `gcp_service_account` resource actions. This is the permission
`sciontool-gcp-identity.md` specified and never built. Two layers, both required:

1. **Hub policy layer** — `CheckAccess(identity, gcpServiceAccountResource(sa), ActionAssign)`.
   Answers "may this principal use this SA record *within Scion*."
2. **GCP IAM layer** — `CanActAs`. Answers "does this principal hold the delegation grant
   *in GCP*."

Both must pass. They answer different questions and neither subsumes the other: the Hub
layer enforces Scion's own tenancy and membership model; the IAM layer enforces the
customer's cloud authority.

### 6.1 Prerequisite: fix `gcpServiceAccountResource()`

```go
// current — unconditional project parent
ParentType: "project", ParentID: sa.ScopeID,
```

This is **correct today** under the invariant that every SA is project-scoped. It is not a
latent bug in current code. **Goal 2 is what breaks that invariant** — once hub-scoped SAs
exist, a hub-scoped SA would claim a project parent whose ID is a hub ID, and the
project-owner bypass would apply against the wrong resource.

So this is a hard prerequisite of Goal 2, not independent cleanup. Copy the conditional
pattern from `harnessConfigResource()` (`capabilities.go:89-105`), which is the correct
reference. Do **not** copy `templateResource()` — it never sets the parent, so the
project-owner bypass silently fails to apply to project-scoped templates (a separate
pre-existing bug, out of scope here but worth filing).

---

## 7. Audit

There is **no audit event for SA assignment today** — not on create, not on PATCH, not for
`passthrough` selection, not for the token-scope grant at dispatch, and none for a *denied*
assignment. Add one, emitted on both allow and deny, carrying:

- the principal evaluated and its kind
- the target SA (id + email)
- the permission checked and the mechanism used
- the decision, and whether it was served from cache
- the surface (create / patch / project-default / lifecycle-hook)

Two existing defects to fix while here: `GCPTokenEvent.ServiceAccountID` is set but never
emitted as a log attribute (`audit.go:248-269`), and `LogBrokerAuthEvent` is a no-op
(`:204-206`).

**Known limitation:** the only `AuditLogger` implementation is `LogAuditLogger` (slog), so
audit is log lines, not a queryable store. If these decisions need to be auditable in a
compliance sense, that is a gap this project should name but probably not fix.

---

## 8. Goal 2 — SAs as a hub-level resource (Q5: ✅ option A)

Settled regardless of Q5's outcome:

- **Copy the scoping/routing/UI/CLI shape, not the storage machinery.** Templates and
  harness-configs share `ResourceStore` / `ResourceSource` / `resourceImportKind`, all of
  which are built end-to-end around content-hashed *file bundles* — `transfer.CollectFiles`,
  signed upload/download URLs, manifest reconciliation, marker-file discovery. **An SA has
  no file payload.** Plugging SAs into that abstraction would be cargo-culting.
- **The transferable pattern is `?scope=&scopeId=` filtering on a global collection
  endpoint** (`listTemplatesV2`, `template_handlers.go:167`), *not* a nested
  `/api/v1/projects/{id}/...` route. Note there is no `/api/v1/projects/{id}/templates`
  route at all — project scoping is a query param on the global collection.
- **`harnessConfigResource()` is the authz reference** (§6.1).
- **The hub-scope dead branch is the natural seam.** `lifecyclehooks/validate.go:417-431`
  already *requires* hub-scoped SAs for hub-scoped hooks, but no write path can create one
  (`handlers_gcp_identity.go:161, 645` both hardcode `store.ScopeProject`). Hub-scoped
  lifecycle hooks with an execution identity are therefore currently unusable. Goal 2
  closes a latent dead branch rather than inventing a new concept.
- **Parity means parity with the *working* parts.** `visibility` is dead weight on
  templates and harness-configs — stored, defaulted inconsistently (`private` for
  templates at `resource_store.go:242`, `public` for harness-configs at `:358`), and never
  used to filter list results. Do not copy it onto SAs. `owner_id` is enforced in exactly
  one place (harness-config user-scope delete).

### 8.1 ✅ Q5 RESOLVED 2026-07-28 — option A, real hub-scoped SAs

ptone ruled: **real hub-scoped SAs, pickable in any project, subject to the permission
check.** Not merely top-level CRUD/nav/CLI parity with project scoping retained.

This composes cleanly with Goal 1 — and only because of Q4. The IAM check becomes the only
thing standing between a hub-scoped SA and any project, which is coherent precisely because
Q4 makes the check universal across all four surfaces. If Q4 had left any surface ungated,
option A would have opened that surface to every hub-scoped SA.

Consequences now locked:

- **`Scope: "hub"` must become writable.** Both hardcoded `store.ScopeProject` writes —
  register at `handlers_gcp_identity.go:256` and mint at `:734`, line numbers re-verified
  2026-07-28 at `5c904b98` — become scope-aware. This activates the dead
  branch above, which also makes hub-scoped lifecycle hooks with an execution identity
  usable for the first time.
- **`gcpServiceAccountResource()` — ✅ ALREADY FIXED, and this is a trap. Read 8.3.**
  Verified on `origin/scion/svc-accnt-lead`: `ParentType`/`ParentID` are now set only when
  `sa.Scope == store.ScopeProject && sa.ScopeID != ""`, mirroring `harnessConfigResource()`,
  with tests. The prerequisite as *written* is met. **It is not safe to conclude the
  prerequisite is discharged** — see §8.3.
- **Project-scoped listing unions in hub-scoped SAs.** The three `sa.ScopeID != projectID`
  guards (`handlers_gcp_identity.go:308-311`, `:333-336`, `:380-383`) and the create-path
  validation (`handlers_agents_core.go:437`) become scope-aware rather than equality checks.
- **New top-level `/api/v1/gcp-service-accounts`** with `?scope=&scopeId=`, per the template
  pattern above. Hub-level UI tab. CLI/hubclient parity —
  `client.GCPServiceAccounts(projectID)` loses its baked-in project.

### 8.3 ✅ DISCHARGED — Goal 2's `matchesResource` gate is satisfied

> **Update 2026-07-28, verified on `origin/scion/svc-accnt-lead`. The gate below is MET.
> Both fail-open conjuncts are gone.** `matchesResource` now reads:
>
> ```go
> case "project":
>     // A project-scoped policy applies only to resources that resolve to
>     // that project. Parentless / hub-scoped resources resolve to "" and
>     // must NOT match — fail closed rather than falling through (#595).
>     if pid := projectIDForResource(resource); pid == "" || pid != policy.ScopeID {
>         return false
>     }
> ```
>
> The parenthetical at the end of this section — the outer `policy.ScopeID != ""` fail-open —
> is **also** fixed, and deliberately, with a comment saying so: a project-scoped policy with
> an empty `ScopeID` now matches nothing rather than everything.
>
> **This is load-bearing beyond Goal 2.** It is the mechanism that makes P0.4's arm 2 safe
> (see §8.2's update): a project-scoped policy is now *structurally* unable to reach a
> hub-scoped SA. The gate did not merely unblock P4 — it became the confinement that a later
> design decision was built on.
>
> **The tripwire below stays armed** and should not be deleted. If `matchesResource` is ever
> reverted or "simplified" back toward the shape recorded below, both Goal 2 *and* the assign
> baseline lose their confinement at once, and neither failure is local to the change.

*Original analysis, retained for provenance. Established by sa-arch 2026-07-28.*

**This reverses the recommendation that the `gcpServiceAccountResource()` prerequisite can
be struck from the plan.** The fix that landed is correct and necessary. It is also the
half that makes the *other* half load-bearing, and the other half is not fixed.

**The mechanism.** The new conditional means a hub-scoped SA produces a resource with
`ParentType == ""` — a **parentless** resource. That is precisely the input class issue
#595 is about. `matchesResource` on this branch is unfixed:

```go
case "project":
    if policy.ScopeID != "" && resource.ParentType == "project" && resource.ParentID != policy.ScopeID {
        return false
    }
```

For a parentless resource the middle conjunct is false, so the guard never fires, control
falls through, and the function **returns `true`**. A project-scoped policy matches.

**The consequence, concretely.** A policy scoped to project A granting
`gcp_service_account : assign` will match **every hub-scoped SA**, from any project. The
policy author's scoping is silently ignored. With Q5 making hub-scoped SAs assignable
anywhere, that is a cross-project escalation via a policy that reads as correctly confined.

**Why it is latent today and live the moment Goal 2 ships.** No hub-scoped SA can currently
be created — every write path hardcodes `store.ScopeProject`
(`handlers_gcp_identity.go:161, 236, 275, 545, 645`). So there is no parentless SA for the
defect to act on. **Goal 2's entire purpose is to create them.** This is not a bug Goal 2
must avoid introducing; it is a dormant defect Goal 2 *activates*.

**Why this is easy to get wrong** — and the reason it is written up at this length:
the prerequisite was recorded as *"fix `gcpServiceAccountResource()`"*, that fix has landed
with tests, so a reasonable reader checks it off. The check-off is what makes it dangerous.
The capabilities fix is **necessary but not sufficient**, and completing it is what converts
#595 from a theoretical matcher defect into a reachable path.

**Gate: P4 does not merge until `matchesResource` handles parentless resources.**
Tripwire — if P4 is ever picked up against a tree where `matchesResource` still reads as
above, stop. This is SC-2's general form running in the *other* direction: rather than
deleting a control other work depends on, we are creating a *reachable input* that another
track's severity rating assumed did not exist. #595 was filed and rated against the
resources reachable at the time.

*(Related, and separately noted on #595: the outer `policy.ScopeID != ""` conjunct is a
second fail-open on the same line — a project-scoped policy with an empty `ScopeID` also
matches everything. Not API-reachable today.)*

### 8.2 ⚠️ Q5 + Q1 interaction — an open ruling, and the one real risk in option A

Raised with ptone 2026-07-28; **not yet ruled.**

Today, project scoping is itself a containment boundary. Weak, but real: an SA registered
in project P is reachable only from P. **Option A removes that boundary**, and Q4's check
is what replaces it.

But **Q1 makes the check a hub-level toggle that is OFF by default.** So a Hub with the
toggle off and even one hub-scoped SA registered lets **any member of any project** assign
it. That is strictly worse than today's behaviour, and it is reachable by an operator who
registers a hub-scoped SA without realising the toggle is what makes hub-scope safe.

This is the **SC-2 shape** again — *do not remove a containment check before the gate that
replaces it is fail-closed* — with one important difference: here the removal (Q5) and the
gate (Q1) are both inside this project, so it can be **sequenced** rather than merely
flagged.

**sa-arch position: registering or enabling a hub-scoped SA requires the toggle ON.**
Not the whole feature gated on the toggle — only the hub-scope capability, which is the
part that is incoherent without it. Project-scoped SAs continue to work with the toggle
off exactly as today, so nothing existing breaks and the Q1 default stays off.

> **⛔ Constraint on how the toggle may be implemented: it must not revoke a grant by
> policy name.**
>
> A toggle that can be turned *off* implies a revocation path, and revocation by name is
> unsafe in this codebase. `AccessPolicy` declares `name` as `NotEmpty()` with **no
> `Unique()`**, and the entity declares no `Indexes()` at all
> (`pkg/ent/schema/policy.go:39-40` @ `5985b0fd`), so `store.CreatePolicy` cannot detect a
> duplicate name and several policies may share one. `ListPolicies(Name: X, Limit: 1)`
> therefore does not return *the* policy — it returns an arbitrary element of a set.
>
> Adding is safe under this: duplicate `allow` rows are redundant, not harmful. **Removing
> or narrowing is not.** A disable path written as "find the grant by name and delete or
> edit it" will touch one row of N and leave the remainder granting — a toggle that reports
> itself off while the permission is still live. That is the worst available failure mode
> for a control whose entire purpose is to be trusted when off.
>
> Turn the capability off by a predicate the checker consults, or by deleting *all* matches
> and asserting the count, never by editing "the" named row.
>
> Found by sa-dev-p3 while building P0.4, as a pre-existing defect in unrelated code:
> the create-then-catch-`ErrAlreadyExists` idempotency branch at
> `handlers_projects_core.go:604-635` is unreachable for this reason, and duplicate
> `project:<slug>:member-create-agents` policies accumulate. Measured, not inferred — three
> calls produced three rows. That defect belongs to hub-core, not to this project; it is
> recorded *here* because it converts a routine implementation choice of ours into a
> correctness requirement, and because nothing in the tree revokes by name **yet**, which
> makes this a trap rather than a bug and means our toggle would be the thing that springs it.

> **Update 2026-07-28 — §8.2 nearly got banked before it was ruled, and the implementation
> now holds it closed by construction.**
>
> Building P0.4's arm 2, sa-dev-p3 found that the **hub-scope seed policy I specified would
> have granted `assign` on every hub-scoped SA to every hub member** — because a hub-scope
> policy has no parentless predicate. That is this section's risk, arriving through the
> grant baseline rather than through Q5, and it would have landed *before* the ruling that
> is supposed to govern it. It was caught only because it broke a tripwire test
> (`TestCapabilities_GCPServiceAccount_HubScoped_NoProjectOwnerBypass`) that P0.2 placed for
> a different reason.
>
> Arm 2 is now **project-scoped**, so `matchesResource`'s #595 fail-closed rejection of
> parentless resources (`authz.go:415-427`) makes it **structurally unable** to reach a
> hub-scoped SA. Consequence: **once the `ActionAssign` conversion has landed**, hub-scoped
> SAs are assignable by admins, the SA's creator, and nobody else — so §8.2 fails closed
> while it remains unruled, and the feature is visibly incomplete rather than quietly
> over-permissive.
>
> **⚠️ That protection does NOT exist before the conversion, and the ordering is therefore
> load-bearing.** Under `ActionRead`, a parentless resource causes `checkAccessForUser` to
> skip the project-owner bypass (`pid == ""`, `authz.go:153`) and fall through to the seeded
> `hub-member-read-all` policy, which **matches**: its `ScopeType` is `hub`, so
> `matchesResource`'s switch has no arm and returns `true`; its `ResourceType` is `"*"`; its
> actions are `read`,`list`. **Every hub member therefore passes the gate on every hub-scoped
> SA.** If hub-scope creation ships before the conversion, this section's hole is live —
> a cross-project privilege exposure, human-only (agents are excluded because the project
> read baseline requires `pid != ""`).
>
> So the confinement is not a property of the design; it is a property of the *order*.
> Recorded because an earlier version of this note stated the safe outcome unconditionally,
> and the unconditional form is the one that gets cited.
>
> None of this answers the question. It removes the deadline, provided the order holds.
> Whoever opens hub-scope assignment must do so deliberately, in a commit that cites this
> ruling.

---

## 9. Relationship to the security track

Issue **#591** documents a hub-wide authorization bypass with the same root
cause as this project's Goal 1. **Goal 1's Hub-policy check must be built on the shared
fail-closed `authorize` helper from that track, not on a bespoke check.** Otherwise we
harden one door among ~18.

Absorbed into that track: Q10 (PATCH hole) and Q12 (missing `else` branch).

### 9.1 Ownership and disclosure — ✅ RESOLVED 2026-07-28

Owned by the **agent-id-fix** track (`aid-arch`), per ptone. Disclosure hold lifted 15:57Z
with the instruction to *"post followups on the fork repo publicly to make sure they are
tracked."* Five issues are filed on `ptone/scion`, cross-referenced from #591. Two bear on
this design:

| Issue | Subject | Bearing |
|---|---|---|
| **#595** | `matchesResource` treats absence of a parent as absence of restriction | Hard prerequisite for **Goal 2** (§8). Carries this project's P4.0 analysis, credited to both tracks. |
| **#596** | Hand-rolled GCP passthrough and SA-assign gates not aligned with `authorize()` | Converts the exact call site **Goal 1** builds on (§5) |

### 9.2 Sequencing — the security fix lands first, and Goal 1 depends on it

Track S Part 1 converts `handlers_agents_core.go:446` to
`authorizeMsg(..., gcpServiceAccountResource(sa), ActionRead)`. **Goal 1 rebases onto that
result and upgrades the call** — swapping `ActionRead` for `ActionAssign` plus the
`CanActAs` IAM check (§5, §6). Goal 1 does not introduce the authorization call; it
strengthens one that already exists. This is the concrete form of the rule stated above: the
IAM gate rides on the shared fail-closed helper rather than beside it.

### 9.3 A compensating control this project removes

Recorded here because it is a property of *this* design, not of the security track.

#596 rates the SA-assign gate as lower severity than the passthrough gate, on the grounds
that two checks above it fire for **all** caller types and confine a non-user caller to
verified SAs in its own project:

| Check | Line | Survives this design? |
|---|---|---|
| `sa.ScopeID != projectID` | `:435` | **No** — Goal 2 makes SAs hub/user-scoped |
| `!sa.Verified` | `:439` | Yes, but it is not an authorization statement about the caller |

`Verified` means only *"the Hub can impersonate this SA."* So Goal 2 deletes the sole
compensating control behind that severity rating. The `authorize()` conversion must
therefore land **before or with** the scope change.

**This holds today by construction, not by vigilance:** Track S lands first, so the
conversion precedes Goal 2's existence. It is nonetheless written down, because it is
discharged by *ordering* rather than by any code change — and a future reordering that puts
the scope change ahead of Track S would re-open it silently, against an issue already closed
under the old severity.

---

## 10. Open decisions

*Table rewritten 2026-07-28 after ptone's rulings. The earlier version listed Q1/Q3/Q4/Q5 as
open and named Q2 the critical path; all four are now locked and Q2's remaining content has
moved into Q14–Q16.*

### Resolved

| # | Question | Ruling |
|---|---|---|
| Q1 | Enforcement | IAM check is a **hub-level toggle, OFF by default** |
| Q2 | Check mechanism (human path) | Policy Troubleshooter → `getIamPolicy` fallback → fail closed. *Refinements open as Q14–Q16.* |
| Q3 | Permission checked | **`iam.serviceAccounts.actAs`** (`roles/iam.serviceAccountUser`) — **not** `tokenCreator`. See the trap in §5.4. |
| Q4 | Surfaces covered | **Any use or assignment** — all four surfaces of §4, explicitly including PATCH |
| Q5 | Goal 2 scope | **Option A** — real hub-scoped SAs, pickable in any project (§8.1) |
| Q11 | Principal checked | **Immediate creator only.** No ancestry walk (§3) |
| Q6–Q8, Q13 | Caching, failure mode, migration, dev-auth | Architect-owned; decided. §5.1 records the caching and failure-handling rulings |
| Q9 | `verification_status` persistence | **Stale — already shipped in P0.1.** Drop from open lists |
| Q10, Q12 | PATCH hole, broker else-branch | → security track (agent-id-fix) |
| — | Security-track ownership + disclosure | agent-id-fix track; public on `ptone/scion`, #591 + #595–#600 |

> ⚠️ **Q3 is the most-misread ruling in this document.** It rejects `tokenCreator` **as the
> thing checked on the caller.** It says nothing about the *Hub's own* `tokenCreator` grant,
> which is what makes impersonation — and therefore the recommended agent-path mechanism in
> §5.3 — possible at all. Two different service accounts, two different permissions. The two
> statements read as contradictory if skimmed, and have been skimmed.

### Open — with ptone

| # | Question | Bearing |
|---|---|---|
| Q14 | Agent-caller mechanism: option (e), impersonate + `testIamPermissions` | Gates P2's agent branch (§5.3) — the dominant path under Q11 |
| Q15 | Fail closed on Policy Troubleshooter `UNKNOWN*` | Dissolves if Q16 is taken; not independent of it |
| Q16 | **Drop the `getIamPolicy` fallback.** It fails *open* against IAM Deny / PAB, and does so most often in exactly the orgs that deploy them. Q1's toggle is already the explicit escape hatch | §5.4 proposes ptone's ID-token assertion as its replacement |
| — | **§8.2 — hub-scope requires the toggle ON?** Q1 + Q5 combine into a hole with the toggle off | Gates the last step of the Goal 2 landing sequence |
| — | **Whose `actAs` applies on the project-default path?** The agent creator's, or the operator who set the default? | §4 row b. The path has no SA-level authorization at all today |

Also flagged and not yet dispositioned: Policy Troubleshooter plus fail-closed denies
**group-granted** users even when PT is fully available. That is a property of the locked
primary mechanism, not of the fallback, so Q16 does not fix it.

### What is actually blocking

**Nothing blocks the design as a whole any longer.** Q2's lock plus the §5 interface freeze
mean every remaining question is an *implementation* choice behind
`CallerPermissionChecker`, which is the property §5 was shaped to deliver. Concretely:

- **P2 is blocked** on Q14–Q16 — it is the phase that implements the mechanism.
- **P3 is not blocked.** It wires the frozen interface and a fake.
- **P4's last step is blocked** on §8.2's ruling.
- Everything else is sequencing, tracked in the svc-accnt implementation plan.

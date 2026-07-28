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

> ### ⚠️ "Hub-scoped" means two opposite things, and that is where the mistakes come from
>
> This is the single most error-generating fact in the project. It has produced four wrong
> claims by three different people in one day, including two of mine, in *opposite* directions.
> Read it before writing any sentence containing the phrase "hub-scoped".
>
> A hub-scoped service account is, at the same time:
>
> - **The MOST reachable scope.** `sa.ReachableFromProject(projectID)` returns `true`
>   unconditionally for `ScopeHub` (`pkg/store/models.go:1552`). A hub-scoped SA is usable from
>   *every* project. It is the permissive case.
> - **The LEAST matchable resource.** `gcpServiceAccountResource` leaves it **parentless**, so
>   `projectIDForResource` returns `""` and — post-#595 — no project-scoped policy can match it
>   at all. For the policy engine it is the restricted case.
>
> The intuition "hub-scoped = special = locked down" is right about policies and wrong about
> reachability. The intuition "hub-scoped = global = available everywhere" is right about
> reachability and wrong about policies. **Both intuitions are half-true, so both feel
> confirmed.**
>
> Two consequences that keep being got backwards:
>
> - A validator asking *"is this SA usable here?"* must **accept** a hub-scoped SA. Rejecting it
>   is the error. (`validate.go:425` is the exception that proves this: it asks a *same-scope*
>   question, not a reachability question, which is exactly why it must not be converted to the
>   helper.)
> - An authorization check asking *"who may assign this SA?"* **splits by caller class, and the
>   two halves point in opposite directions.** ~~gets less confinement from scope, not more~~ —
>   see below. This bullet originally said only the human half, flat, and was corrected within
>   the hour by aid-em, who noticed they were about to apply it to an agent-caller comment.
>
>   | Caller | Result under `ActionRead` on a parentless (hub-scoped) SA | Why |
>   |---|---|---|
>   | **Human** | **PASSES — every hub member** | Project-owner bypass skipped (`pid == ""`, `authz.go:153`); `hub-member-read-all` matches, because `matchesResource`'s scope switch has **no arm for `ScopeType: "hub"`** and falls through to `true` (`:415-433`). Bound to `hub-members` by `seed.go:51`; `handlers_auth.go:1243` ensures every user into that group on login. |
>   | **Agent** | **DENIES** | Principals come from `GetEffectiveGroupsForAgent` (`:200-206`) — the agent's own groups, which never include `hub-members`, so the wildcard policy is never even fetched. The project read baseline then requires `pid != ""` (`:245`), which a parentless resource fails; that guard is documented at `:235-238` as load-bearing for exactly this case. Falls through to delegation, then default deny. |
>
>   So hub scope removes confinement for humans and *adds* it for agents. **Any single sentence
>   about "the gate" on this path is half wrong.** Say which caller you mean.
>
> So the fail-closed state for hub-scoped SAs — assignable only by admins and the creator —
> looks like breakage and is the ruled answer (§8.2). It has already been reported to me as a
> regression once, by an agent who had correctly re-derived the design.
>
> **⚠️ And now the guard on that corollary, because the corollary is the more dangerous half.**
>
> The sentence above primes every reader to treat a denial in this area as correct by design.
> That is the right prior and it is also exactly how a *real* over-denial would get waved
> through: someone hits one, remembers this paragraph, and stops looking. That failure is the
> mirror of the one this section exists to prevent, and it is worse, because it fails silently
> and in the direction that feels safe.
>
> The corollary says **a plain hub member denied on a hub-scoped SA is correct.** It does *not*
> say *any* denial involving a hub-scoped SA is correct. Those are one word apart. All of the
> following remain real bugs and must still be reported:
>
> - an **admin** or the SA's **creator** denied — that is the ruled *grant* failing, not the
>   ruled denial working
> - a **project-scoped** SA denied to a project member — nothing in §8.2 touches that path
> - an **agent principal** denied where its ancestry should carry it (`checkAccessForAgent`
>   step 0)
>
> Three false reports are cheaper than one suppressed one.
>
> **Never coordinate "hub-scoped" with "other-project" in a list of things to reject.** They are
> opposites. I wrote exactly that sentence ninety minutes after ruling the reverse, and it was
> caught by an implementer who checked the instruction against a test rather than against its
> author.

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

### 5.5 🛑 CORRECTION — the mode and the checker are ONE switch, and this section built two

*Added 2026-07-28, applying aid-em's rule 15 to our own tree. **This is a defect in the section
above, not in P3's implementation of it.***

Measured at `2836b685`:

- `saAssignCheckerFor` returns `s.saAssignChecker` in mode-`off` **and** in
  enforce-with-generator — the *same value*. The only behaviour the mode changes is
  enforce-with-**nil**-generator → the unavailable checker → **deny**.
- `saAssignChecker` is assigned exactly once, `server.go:981`, to
  `NewDisabledCallerPermissionChecker()`. Nowhere else in the repo.
- `SAAssignCheckEnforce` occurs exactly twice: its own doc comment and its own definition.

So flipping the mode to `enforce` — which nothing in the repo can do — **would not turn the
check on.** The configuration that runs a real check (enforce + generator + a non-disabled
checker) is unreachable by construction, and `store.EvaluateActAs` never executes via any HTTP
path.

**Why that is this section's fault.** §5.1 specifies, at length and correctly, that the gate must
**deny** when `gcpTokenGenerator == nil`. Q7 then specifies a mode with values `enforce | off`.
**Together those describe exactly one live transition — the deny — and never say what installs
the checker on the allow path.** P3 built precisely what was written. The mode's entire reachable
behaviour *is* the sentence I wrote down; the missing half is the sentence I did not.

> **The rule, general:** an enforcement mode and the capability it selects are **one switch, and
> must be one field**. A mode whose "on" position resolves to the same collaborator as its "off"
> position is not a feature flag — it is a **label on a decision made somewhere else**, and every
> release note written from it will describe the wrong control.

**Consequences that are prerequisites, not follow-ups:**

1. **The enforce path must execute in CI before it executes in production.** Presence of the gate
   on the call path is *not* evidence that the gate can decide — `authorizeSAAssignment` **is**
   invoked at `:495` and `:1698`, and that is compatible with everything above. This is the
   acceptance criterion I previously stated as *"check presence, not colour"* and then spent as
   something stronger than it licenses.
2. **Tests must vary the *checker*, not the mode.** That needs a test-only installer for
   `saAssignChecker` / `hookIdentityChecker`. Per gated surface: a denying checker asserted
   through to the wire, **and an allowing twin** — a denial-only pair proves only that *something*
   denied.
3. **The release note must name both switches.** `server.go:970`'s
   *"⚠️ INERT IN THIS RELEASE"* is good practice and should stay; it currently names the switch
   that is *not* the one holding the feature inert.
4. **Q7's stated default is `enforce`; the implementation ships `off`.** Deliberate and disclosed
   for this release. Recorded here so the divergence is not later read as drift, and so #19's
   resolution does not silently inherit `off` as though the design had chosen it.

**Why this could not have been caught behaviourally.** No experiment distinguishes "correctly
wired and inert" from "not wired": the component's behaviour range has **one point**. Rule 15
(*where the outcome is invariant over a component's full behaviour range, that component is not
the fix site*) therefore cannot be *applied* here — which is the strongest form of the problem,
not an exemption from it. **An untestable gate reads as an unwired gate until someone makes it
varyable.**

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

### 6.2 🛑 `CreatedBy` IS AN AUTHORIZATION INPUT, AND IT IS IMMUTABLE ONLY BY OMISSION

*Found 2026-07-28 running aid-em's upsert shape (rule 18) against our own tree. **Known-good-for-now
with a named precondition — NOT clean.***

Three facts, each measured at `2836b685`, whose conjunction is the finding:

1. `gcpServiceAccountResource` sets `OwnerID: sa.CreatedBy`.
2. `checkAccessForUser`'s **owner bypass** — `resource.OwnerID == user.ID()` → `Allowed` — consults
   **no membership**, and runs before every resource-type rule.
3. `UpdateGCPServiceAccount` (`entadapter/external_store.go:166`) sets email, projectID,
   displayName, defaultScopes, verified, verificationStatus, verificationError, managed, managedBy,
   verifiedAt. It does **not** set `CreatedBy`, `Scope`, `ScopeID` or `Created`.

**So `CreatedBy` — which grants an unconditional authorization bypass — is immutable because a
line is missing from a setter list.** Nobody wrote that it must be. There is no comment, no
schema-level immutability, and no test.

> **The natural repair is the violation.** The obvious tidy-up is to make `Update` symmetric with
> `Create` by adding `.SetCreatedBy(sa.CreatedBy)`. That single line would make the owner bypass
> **writable through any handler that round-trips an SA through `Update`** — and `Update` is
> already called on the verify path, so no new route is needed. It would review as consistency
> work.

**`Scope` and `ScopeID` are in exactly the same position, and Goal 2 is what pressures them.**
Per §6.1, `gcpServiceAccountResource` branches on `sa.Scope == store.ScopeProject` to decide
whether the **project-owner bypass** applies. Scope is therefore also an authz input, also
immutable only by omission — and Step 5 is the change most likely to want a scope mutator.

**Required, as a Step 5 (#23) prerequisite:**

- Assert immutability where it can be enforced rather than observed: reject `CreatedBy`, `Scope`,
  `ScopeID` and `Created` changes at the store boundary, or make the update path take an explicit
  field set rather than a whole struct.
- Until then, a comment **on `UpdateGCPServiceAccount` itself** naming these four fields, why they
  are absent, and that `CreatedBy`/`Scope` feed authorization bypasses. **The comment must sit
  where the tempting edit happens**, not in this document.
- **Observable, not cause** (§8.4): *re-verifying, renaming, or otherwise updating a service
  account must never change who is allowed to use it.* One test, round-tripping an SA through
  every handler that calls `Update`, asserting `CreatedBy`/`Scope`/`ScopeID` are unchanged.

**Why this is recorded rather than fixed now:** nothing today writes these fields, so there is no
live defect. But **the safety is an absence, and an absence leaves nothing in a future diff to
review** — the violating change would be a one-line addition that makes two functions match.
Marking this surface "clean" would delete the only warning its future violator would get.

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
> **Precedent, and it cuts toward the conservative answer.** `main` @ `db8f6fc5` (#888) ships
> a hub-scoped resource of its own — hub pre-start hooks — and gates every mutation on
> `requireHubAdmin` (`pkg/hub/hub_pre_start_hook_handlers.go:54`, used at `:164` and `:260`),
> a direct role check that is *deliberately* stricter than the policy engine and rejects
> project-scoped tokens outright. So when this section asks who may assign a hub-scoped SA,
> "the hub already has a hub-scoped resource and it chose admin-only" is an argument from
> precedent rather than from analogy. Found by sa-dev-p4.
>
> p4 also checked and cleared the obvious hazard: #888 never reaches `matchesResource`, so it
> does not inherit main's fail-open parentless behaviour. **That clearance is true today and
> is a trap tomorrow** — the property making it clean is that it bypasses the policy engine,
> which is precisely the kind of inconsistency someone eventually "harmonizes". A commit
> moving hub pre-start hooks onto policies, landed on a tree without #595, is a fail-open on
> a hub-wide resource, and it will arrive looking like a tidy-up. Same shape as the
> `validate.go:425` sweep guarded against in §5, and as the revoke-by-name trap above.

> ### 🛑 The Q1 toggle is not a kill switch — authorization on the hook surface is write-time only
>
> Found by sa-dev-p3 while scoping the execution-identity work; verified at
> `pkg/hub/lifecycle_hook_executor.go:239-255` @ `5985b0fd`.
>
> `resolveIdentityAndToken` fetches the service account by the record ID stored on the hook and
> goes straight to `GenerateAccessToken` with full `cloud-platform` scope. It does **not**
> re-check `sa.Verified`, does **not** re-check scope, and has no caller to check against.
> `validate.go:425`/`:433` are the only scope enforcement on this surface, and they run **once,
> at write time**. Validation-time state is trusted forever.
>
> The immediate consequences are bad enough: an SA de-verified or moved between scopes after the
> hook is written keeps minting tokens, and an admin who loses `actAs` — or leaves — leaves
> behind a hook that impersonates on a schedule. The gate is *"who was allowed to write this"*,
> not *"who is allowed to run this"*.
>
> **The consequence for this design is larger. Turning the Q1 toggle ON does not gate anything
> that already exists.** An operator who enables it believes they have gated impersonation; they
> have gated new writes. Every hook already in the table continues to run on the authority it
> was written with, and nothing sweeps them.
>
> That is the same defect as the option (c) shape rejected in §5 — a control that presents as
> applied but does not act — one layer up, and in the feature this document is shipping. It is
> not acceptable to describe the toggle as an enforcement switch until one of the following is
> true:
>
> - **(a)** enabling the toggle triggers a re-validation sweep of existing hooks, quarantining or
>   disabling those whose execution identity no longer passes; or
> - **(b)** the executor re-checks at execution time — which requires deciding *whose* authority
>   a scheduled hook runs on, since there is no caller; or
> - **(c)** the toggle's scope is documented honestly as **write-time only**, in the UI string and
>   not merely in this document.
>
> **(c) is the minimum and it is not sufficient on its own for long.** Tracked as its own item;
> deliberately *not* folded into P3, which has no caller on that surface and would be widened
> well past its brief. sa-dev-p3 will write the narrow sentence at the site — "was gated, once" —
> and name the residual, which is the right call: refusing to write a sentence you cannot support
> is how this was found at all.
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
> > **🛑 "the SA's creator" is not a grant this section made. It is engine behaviour I
> > inherited and described without noticing.**
> >
> > `gcpServiceAccountResource` sets `OwnerID: sa.CreatedBy` **unconditionally**
> > (`capabilities.go:182`), and the owner bypass sits at `authz.go:133-139` — before any
> > resource-type logic, consulting nothing but ID equality. So the creator passes by engine
> > bypass whatever this document says. **I cannot un-grant them by ruling.**
> >
> > And the grant is wider than "survives removal". sa-dev-p4 measured it:
> > `TestAgentCreate_HubScopedSA_FormerHubMemberCreatorDenied` fails when unskipped —
> > expected 403, got 201, agent created with the SA attached. Building it required granting
> > hub membership and then revoking it, **because the creator subtest never grants
> > membership at all and passes anyway.** Membership is not merely un-revoked; it is *never
> > consulted, at any point*. My first statement of this ("removal does not revoke") implied
> > there had been something to revoke. There never was.
> >
> > **The sharpest evidence that this is systemic rather than an oversight in our corner is
> > four lines below the defect** *(this reading is mine — sa-dev-p4 named `authz.go:133-139`
> > and the unconditional `OwnerID`, and asked that the inference not be logged as theirs)*.
> > `capabilities.go:184-188` carries a careful comment
> > explaining why a hub- or user-scoped SA must *not* claim a project parent — the author
> > reasoned explicitly about handing a bypass to the wrong principal. Two lines above,
> > `OwnerID` is set unconditionally, uncommented. The care went to one field and not the
> > other, in the same function, by someone thinking about exactly this hazard.
> >
> > This is the same shape as the hook-execution escalation earlier in this section:
> > **authority captured at write time and never re-checked.** Two independent instances in
> > one codebase is a pattern, not a coincidence.
> >
> > **One local lever exists, so #19 has a cheap option and not only an expensive one:** do
> > not populate `OwnerID` for hub-scoped SAs. Resource-local, one line, does not touch the
> > engine. Recorded as an option, not ruled. The general question — whether the owner bypass
> > needs a liveness condition — is an engine-level decision and is explicitly **not** mine.
> >
> > > **⚠️ The lever is NOT free, and its cost lands on the very ruling it is offered to.**
> > > I first priced it at zero. sa-dev-p4 priced it properly, and it is one line to write
> > > and easy to get wrong.
> > >
> > > `gcpServiceAccountResource` has **five** call sites, not the two assign gates:
> > >
> > > | Site (`5985b0fd`) | Action |
> > > |---|---|
> > > | `handlers_agents_core.go:501`, `:1705` | assign (currently `ActionRead`) |
> > > | `handlers_gcp_identity.go:127` — `authorizeGCPServiceAccount`, `ScopeHub` arm | manage / **delete** / **verify** |
> > > | `handlers_gcp_identity.go:342` | `ComputeCapabilitiesBatch`, list |
> > > | `handlers_gcp_identity_scoped.go:204` | scoped list |
> > >
> > > For a hub-scoped SA the `ScopeHub` arm is a bare `CheckAccess` against the parentless
> > > resource, so **the owner bypass is the only thing admitting a non-admin creator there.**
> > > Dropping `OwnerID` does not narrow assign alone — it narrows manage, delete and verify
> > > to admins, and the batch capability calls stop returning caps to the creator, so they
> > > cannot see their own SA in a list.
> > >
> > > **The sharp edge is `ActionVerify` (`handlers_gcp_identity.go:466`), because verification
> > > is not a later administrative act — it is the second step of creation.** A non-admin hub
> > > member would create a hub-scoped SA and immediately be unable to verify it, leaving an
> > > unverified account that every assign site correctly refuses: created-and-unusable, by its
> > > own creator, with no error that says why.
> > >
> > > So the conditional tightens to **if and only if**: the lever is free *iff* hub-scope
> > > creation is admin-gated, because then the admin bypass already covers verify. If #19
> > > opens creation to hub members, the lever costs a create-then-verify hole unless verify
> > > gets its own grant. **One ruling decides both, in opposite directions** — which is
> > > precisely the thing that must not be discovered after the ruling.
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

### 8.4 ✅ RULED 2026-07-28 — how an SA refusal is *rendered*: 403 vs 404

Goal 2 adds flat by-id routes (`/api/v1/gcp-service-accounts/{id}/…`) so that hub-scoped SAs
have an address at all. That raised a question the nested routes never had to answer, and the
answer is an architectural rule, not a per-endpoint choice.

**The rule.**

> **`authorizeGCPServiceAccount` answers a POLICY question — *may this caller do this*.
> 403-vs-404 is a DISCLOSURE question — *could this caller have established that this
> resource exists by some other means?* That is a property of the ROUTE, not of the policy.**
>
> **The same verdict may therefore render differently on different routes, and doing so does
> not create a second description of who may do what.**

**Applied per scope, it does not come out uniform:**

| Scope | Flat by-id route renders | Because |
|---|---|---|
| project | **404** | ⚠️ **Not a disclosure decision at all** — see the correction below. A project-scoped SA already has a nested address that carries project-level authz; a second, project-free address would be **a second door to the same rows with different authorization on it**. The 404 says *"wrong address"*, not *"you may not know"*. Enforced in `loadParentlessGCPServiceAccount`, which never consults identity. |
| user | **404** | Stronger case. A user-scoped SA is unreachable from *any* project, so the nested route has always 404'd it — **the flat route is the first HTTP address a user-scoped SA has ever had.** That 403 would be a *brand-new* oracle, created by the route we are adding, not an inherited one. (sa-dev-p5's extension of the ruling, approved.) |
| hub | **403** | Every user is joined to `hub-members` on login, and `hub-member-read-all` grants `read`+`list` on `ResourceType: "*"` at hub scope. **Any authenticated caller can already enumerate hub-scoped SAs**, so there is no existence left to protect. A 404 here would be a lie that protects nothing and costs the debuggability of the exact surface Goal 2 is adding. |
| *(default)* | **404** | Fail closed: a scope added later arrives with **no disclosure analysis done**. |

> **🛑 CORRECTION, 2026-07-28 — the project row is a different KIND of decision, and an earlier
> version of this table got its reason wrong.** I first justified the project 404 as a
> disclosure control: *"the caller could not reach it via the nested route either."* **That is
> false**, and sa-dev-p5's test asserts its falsity on purpose —
> `TestGCPSA_FlatByID_Get_ProjectScopedIs404NotForbidden` has the **same user** receive 404 on
> the flat route and **200 on the nested one**. The caller *can* reach it. That pairing is what
> proves the 404 measures **routing**, not permission.
>
> **What produced the slip:** the table has one uniform `Because` column, and three of its four
> rows are disclosure decisions. **A uniform column invites a uniform justification**, and I
> supplied one for the row that was not that kind of thing. The doc even contradicted itself —
> it says the loader "tests scope and nothing else", which cannot be true of a rationale that
> depends on who is asking.
>
> **The disclosure concern is still real for project scope, but as a separate constraint:** the
> refusal body must be **byte-identical** to the missing-row 404, so an existing out-of-scope
> account cannot be distinguished from a nonexistent one. Pinned by
> `TestGCPSA_FlatByID_RefusalsAreIndistinguishable`. That is the disclosure control; the 404
> itself is not.

**Encode the RULE, not the TABLE.** Each arm must name the specific path that makes existence
establishable — the hub arm names `hub-member-read-all` explicitly. **The hub row's premise is
the one expected to move:** if #19 resolves toward admin-gating hub-scope creation, the
listability this rests on may narrow and the hub arm should then become 404. If the table is
encoded, that move is invisible; if the rule is encoded, the sentence that becomes false is
sitting in the arm that has to change.

**Structural prerequisite — this ruling is not implementable without it.**
`authorizeGCPServiceAccount` *decided and wrote* in one step: every arm called `writeError`
itself and returned a `bool`. A route therefore could not remap its refusal, which forced the
flat route's loader to **re-derive `sa.CreatedBy != user.ID()`** to get a 404 in ahead of the
write — reintroducing the second description of who-may-do-what that the rule exists to avoid.
**The fix created the hazard rather than avoiding it.** Required split, now implemented:

- `gcpServiceAccountVerdict(…) → (verdict, error)` — writes nothing; carries `allowed`, a
  reason, and a `noIdentity` flag so a renderer can answer an unauthenticated caller
  generically rather than leaking which arm it would have hit.
- `authorizeGCPServiceAccount` — unchanged nested renderer; **every refusal still 403**.
- `authorizeGCPServiceAccountFlat` — renders per the rule above.

The creator test then exists at exactly one line and both routes reach it. The flat loader
tests **scope and nothing else** — a routing fact, not a policy one.

> **🛑 FINDING, 2026-07-28 (`acc4285d`) — the renderer must cover every VERDICT SHAPE, not just
> every SCOPE.** `authorizeGCPServiceAccountFlat` checks `verdict.allowed` and then falls
> straight into the scope switch. **It never handles `noIdentity`.** The nested renderer does.
>
> So a `noIdentity` verdict renders **403 for hub scope and 404 for user scope** — precisely the
> leak the flag was introduced to prevent, and which the bullet two lines above says it prevents.
> **The design specified the mechanism and the table-shaped renderer routed around it**, because
> a `switch sa.Scope` is exhaustive over the thing its author was thinking about and silent about
> everything else. *(Same failure as the uniform `Because` column: a structure that looks total
> and is total over the wrong dimension.)*
>
> **Reachable, traced:** `GetUserIdentityFromContext` returns nil for an **agent**, and
> `UnifiedAuthMiddleware` admits authenticated agents. So an agent on the flat route gets
> `noIdentity` — and the hub arm's 403 rests on *"every user is joined to `hub-members` on
> login"*, which is **false for agents**: agent principals never include `hub-members`. **The 403
> is granted on a premise explicitly false for the caller receiving it.** `reason` is also `""`
> on this verdict, so the body is empty.
>
> **Severity:** an agent-facing *existence oracle* for hub-scoped accounts. Not an access grant —
> both arms still deny.
>
> **Required:** handle `noIdentity` **before** the scope switch, rendering **404** — not the
> nested route's 403. An identity-less caller has established nothing on any surface, so the user
> row's reasoning governs. Pin as a divergence test in the same idiom: same agent, same ID,
> hub-scoped vs user-scoped, **one test asserting the two statuses are now identical.**
>
> **State the OBSERVABLE in the fix comment, not only the cause** (aid-em). The cause is a
> renderer that switched on the wrong dimension, and understanding it requires holding two
> functions in your head. The observable is one sentence: **an identity-less caller must not be
> able to tell two scopes apart by status code.** That is what a future reader can test for
> without understanding the renderer at all — and it stays true if the renderer is rewritten.
>
> **The general rule this earns:** where a policy layer returns a *discriminated* verdict, the
> renderer's exhaustiveness must be checked against the **verdict type**, not the resource
> taxonomy. Switching on the resource makes the missing arm invisible — there is no `default` to
> catch it, because the scope *was* matched.

**Test obligation.** Pin the hub-403/user-404 split as a **divergence in a single test** — same
caller, same action, both denied by policy, only disclosure differing. Two tests asserting two
values can be silently harmonised by a later "consistency" cleanup; one test asserting that they
*differ* cannot. Plus a regression guard that the nested routes still render 403 after the
split, because **the way this refactor fails is by leaking the flat disclosure policy into the
nested route** and quietly turning existing 403s into 404s.

> 🛑 **THE PARAGRAPH ABOVE IS WRONG AS WRITTEN AND IT SHIPPED. See §8.5.**

---

### 8.5 🛑 CORRECTION — I told them to pin the defect, and they did (#45, #46)

**This is the second finding today whose cause is this document rather than an implementation.**
The first was §5.5. The mechanism is identical and worth naming before the detail: *a sentence I
reasoned about in one case, written without the qualifier that made it true.*

#### What I wrote, and what it licensed

§8.4's test obligation ends: *"a regression guard that the nested routes still render 403 after
the split."* I was reasoning about **reasoned refusals** — a caller who was evaluated against a
policy and lost. For those, 403 on the nested route is right, and the justification in
`authorizeGCPServiceAccount` is right with it: to reach a nested by-id route the caller named the
project, so a 403 discloses nothing they did not supply.

But `noIdentity` refusals are also "the nested routes". sa-dev-p5 did exactly as instructed and
pinned the nested 403 in a test alongside the #42 fix. **The pin is faithful to my sentence.** It
is also now a defect with a test asserting it, which is strictly worse than an unpinned defect:
it reads as specified behaviour, and correcting it reads as a regression.

#### The measurement (#45), at `d0fb638c`

- `models.go:1552` `ReachableFromProject` — `case ScopeHub: return true`, **unconditionally**.
- `handlers_gcp_identity.go:451` `getGCPServiceAccount` — 404 if not reachable, then an
  `ActionRead` check **only when `Scope == ScopeHub`**.
- `:482` `deleteGCPServiceAccount` — 404 if not reachable, then the check **unconditionally**.
- `:218` `authorizeGCPServiceAccount` — `noIdentity` renders **403**.

For an identity-less caller naming any project `P` it can reach, on nested DELETE:

| account | status |
| --- | --- |
| does not exist | 404 |
| project-scoped in some `Q != P` | 404 |
| project-scoped in `P` | **403** |
| hub-scoped | **403**, for every `P` |

So the status code separates hub scope from project scope, existence from non-existence, and — by
iterating `P` — attributes a project-scoped account to its project. That is p5's own invariant,
violated one route over: *an identity-less caller must not be able to tell two scopes apart by
status code.*

#### Why the justification did not catch it — the general rule

`handlers_gcp_identity.go:203` says a nested 403 "discloses nothing they did not supply
themselves." True. p5's commit says "a nested caller supplied the project themselves." Also true.
**Both sentences are about the PROJECT. What the 403 discloses is the ACCOUNT** — its existence,
its scope, and its project.

> **RULE.** A disclosure justification must name the object disclosed, not the object supplied.
> "The caller already knows *X*" is only a defence when *X* is the thing being revealed. This is
> rule 16 applied to a justification rather than to a measurement: the claim that was verified
> and the claim being spent were about **different nouns**, and nothing in either sentence's
> grammar shows that.

#### The fix, and why it is free

`noIdentity` arises exactly when `GetUserIdentityFromContext` is nil. **No caller with a user
identity is ever inside that arm** — they get the scope-specific 403 and its reason string,
unchanged. So rendering `noIdentity` as **404 in both renderers** cannot alter any user-visible
behaviour, and the nested renderer's "unchanged behaviour is the requirement" does not protect
that arm, because no identified caller is in it.

**Replace the pin, do not delete it.** The corrected obligation: pin the **sameness** of the
`noIdentity` status *across both routes*. My original "assert the routes differ" instinct was
right for reasoned refusals and wrong here — for `noIdentity` the two routes must agree, and an
assertion whose subject is the agreement cannot be made green by reopening the divergence in
either direction.

> 🛑 **AND THAT INSTRUCTION IS ITSELF INCOMPLETE — the fourth time today.** A sameness pin here
> asserts *all rows are 404*, and **404 is also what a dead path returns.** If the route were
> unregistered or the path built wrongly, `http.ServeMux` answers 404 for every row, the parity
> holds, and the "nothing was deleted" guard passes too — an unrouted request deletes nothing
> just as a refused one does. Both guards point the same way and neither separates *"the renderer
> refused all four"* from *"nothing ran."*
>
> **RULE.** An assertion is vacuity-prone exactly when its expected value coincides with the
> value produced by **not running**. A *divergence* pin is self-controlling — two different codes
> cannot both be the harness default. A *sameness* pin trades that away, and must buy it back
> with an explicit **liveness control**: one request through the same route by an authorised
> caller, asserting a success that a dead path cannot produce.
>
> So the sameness idiom is stronger against harmonising cleanups and weaker against a dead
> harness, and it must always ship with the control. I handed it over without that qualifier.

#### #46 — and the hub-scope read check denies **only** agents

Running aid-dev4's wildcard mechanism against this surface:

- `authz.go:492` — the `matchesResource` scope switch has cases `"project"` and `"resource"`
  **only**. `ScopeType: "hub"` matches neither, falls out of the switch, and **no scope filter is
  applied at all**.
- `authz.go:482` — `ResourceType: "*"` matches `gcp_service_account`.
- `seed.go:51` — `hub-member-read-all` is exactly that policy, actions `read` + `list`.
- `handlers_auth.go:1243` and `web.go:1716` — every user joins `hub-members` on login.

Therefore `ActionRead` on a hub-scoped service account is **allowed for every logged-in user**.
The check whose comment says a hub-wide credential must not inherit the absence of a check is,
for the entire human population, a pass. **The only callers it ever denies are identity-less
ones** — and against them its sole observable effect is #45's leak.

That is not automatically wrong: hub members reading hub-scoped SA *metadata* may well be
intended, and that is a Goal 2 policy question I own and am taking. What is wrong is a comment
claiming a protection the wildcard removes.

> **RULE (aid-dev4's, adopted).** A wildcard policy defeats any reasoning that enumerates literal
> resource types. This codebase has a wildcard on **both** dimensions — `matchesAction:470`
> honours `"*"` too — and the default seed uses it on the resource dimension only. So enumerating
> **actions** is currently safe and enumerating **resource types** is not; the first is a
> **precondition, not a property**, and must be recorded as such.

#### 8.5.1 The enumeration I should have done first (#48)

sa-dev-p5's takeaway from #45 was better than my apology, and it is mechanical where mine was
penitent:

> **RULE (p5's, adopted).** When you state an invariant about a **resource**, enumerate the
> **routes that reach that resource** and test it at each. Cheap and mechanical beats noticing by
> insight.

Done properly, against `0dcf5901`, for every hub call site that loads a `GCPServiceAccount`:

| surface | by-id, caller-supplied | discloses existence? |
| --- | --- | --- |
| flat `GET/DELETE/verify /gcp-service-accounts/{id}` | yes | fixed — #42 |
| nested `GET/DELETE/verify /projects/{pid}/gcp-service-accounts/{id}` | yes | fixed — #45 |
| project settings PUT (default SA) | yes | **NO — correct, and documented** |
| agent **create**, `metadataMode: assign` | yes | **YES — #48** |
| agent **PATCH**, `metadataMode: assign` | yes | **YES — #48, by status code** |
| agent create, project-default fallback | no (not caller-supplied) | n/a |
| all list surfaces | no | n/a |

**#48.** `handlers_agents_core.go` create (`:463-479`) and PATCH (`:1701-1718`) separate *"GCP
service account not found"* from *"does not belong to this project"*. Create differs by message
(both 400); **PATCH differs by status — 404 vs 400** — which survives any rewording and is
machine-readable. So any caller who may create or patch an agent in a project can confirm whether
an arbitrary SA ID exists anywhere in the hub.

**Both checks run BEFORE `authorizeSAAssignment`.** The gate P3 hardened and wired with rule-15
tests is *downstream of the leak*: hardening it cannot close this, and no test of the gate can
see it. Disclosure-before-authorization — the mirror of agent-id-fix's
isolation-after-authorization.

**The fix is not a design question, because this repo already contains the answer.**
`project_settings_handlers.go:169` collapses exactly these two cases, with the reason written
out: *"Distinguishing them would make this endpoint an existence oracle… 'Does not exist' and
'exists but is not yours' are one answer."* The standard is set and documented; two handlers do
not follow it. Note their own instruction to keep create and PATCH *"greppably identical"* means
both must change together.

> **RULE.** Before writing a disclosure rule for a resource, grep for **every** load of that
> resource, not every route in the feature you are designing. A feature boundary is not a
> security boundary, and the handler that gets it right may be in another file — as it was here.

#### 8.5.2 A deliberate merge conflict alarms at the text, not at its dependents (#41)

aid-dev4 made the `gcpServiceAccountResource` collision between `agent-id-fix` and this branch
**deliberate**, as a tripwire: whoever merges must confirm the function is conditional rather than
silently take one side. That is the right instrument and it works. It is also **not sufficient**,
and the measurement below is why.

I went looking for a regression and did not find one. On `agent-id-fix` the create and PATCH
assign gates became `authorizeMsg`, which is fail-closed for agents, and agents have nothing to
pass it with: every automatic `AddGroupMember` in the tree is `MemberTypeUser`, no seeded policy
binds an agent principal, and `gcpServiceAccountResource` sets no `Ancestry`. **The hypothesis is
dead** — their `checkAccessForAgent` step 3 *agent project read baseline* covers it, exactly as
their comment claims. Recorded because a dead hypothesis is a measurement.

What is left over is in the same comment. Its AGENT paragraph reasons from **their** version of
the function — *"it claims ParentType project for every scope, so a hub-scoped account resolves to
the hub instance ID: non-empty, and equal to no project."* Mine is already conditional, so in the
merged tree a hub-scoped account yields `pid == ""` and the baseline is skipped by the `pid != ""`
guard instead. Same outcome, different clause — which is the failure their own next sentence
names: *"the reason recorded here would be wrong. Convert both."*

**The merge performs one of their two conversions by itself, as a side effect of resolving a
conflict in a different file.** Nobody decides it, and behaviour does not change, so nothing
complains.

> **RULE.** A deliberate merge conflict raises its alarm **at the point of textual collision, not
> at the point of semantic dependency**. If a claim elsewhere is conditioned on which side wins,
> the conflict does not protect it: name the dependents **inside the conflicting hunk**, so that
> resolving the conflict and repairing the claim are the same act.

This is convention 23 / rule 18 again — enforcement by an absence is invisible to the person it
protects — with the twist that here the enforcement is *present* and merely aimed one file away.

#### 8.5.3 Name the production path, not the caller class

From cross-checking aid-em's section 5. Their PR classifies 23 lint-count decrements as closures or
non-closures, the test being *"reverting restores **reachable** unauthorized behaviour."* One row —
the service-account `ActionRead` check in `createAgentInProject` — entered the closure column on a
different measurement: *"with an explicit deny policy bound, reverting flips 403 → 201."* I grepped
`Effect: "deny"` across `pkg/hub` and `pkg/store`: **eight hits, all eight in tests.** Nothing in
production creates one. The caller class that makes that guard load-bearing is one the observer
constructs.

aid-em accepted and proposed the general form: *a closure claim must state whether the refused
caller class exists in production or was manufactured to observe the guard.* I pushed back on one
word. **Every** test manufactures its callers — a test that creates an admin user manufactures a
class that is entirely real. Manufacture is not the discriminator.

> **RULE.** A closure claim must **name the production path that produces the refused caller
> class** — not assert that one exists, *name it*. Naming is what makes the precondition
> checkable: "any HTTP client" and "an operator binds a deny policy" are both paths, and only the
> second prompts the next question. Three outcomes, not two: **ordinary path** (closure), **path
> requires a deliberate operator act** (closure with a stated antecedent — a real guarantee, not a
> demoted row), **no path** (vacuous). Only the third is a defect.

And the reason to raise it rather than bank it: **applying a criterion only to the row that
prompted it leaves the others certified under the older, unstated one.** Here that costs one
sentence, because the remaining rows all refuse a caller reachable over the network with no setup
— but the sentence has to be written, and if any row does not fit it, that is the finding.

#### 8.5.4 Name the other world — the unified form, and two more of my own errors

Three assertion shapes came up today from three people, and they are one rule.

| shape | why it can be vacuous | control |
|---|---|---|
| **divergence** ("A ≠ B") | it cannot be — two distinct codes cannot both be the harness default | self-controlling |
| **sameness** ("A = B = 404") | 404 is also what a dead route returns (p5, #45) | a request that must *not* produce it |
| **absence** ("no Mint button") | `sl-dialog` keeps both dialogs in the DOM always, so their footers contribute a "Mint" to every scope — one label coincidence from an unfailable green (p5, item C) | a selector that excludes the confound, and a positive control |

> **RULE.** Every assertion is satisfied by a **set of worlds**, and it is evidence only if the
> world you care about is the only one in that set you cannot rule out. Operationally: **name the
> other world that satisfies it.** If you can name one — dead route, undisplayed dialog footer,
> harness default — you need a control that excludes it. If you cannot name one, say so; that is
> a claim too.

**Where the enumeration stops** (p5's addition, and the rule was incomplete without it — "name the
other world" has no termination condition, so it is either unbounded or quietly abandoned, and
quietly abandoned is what happens):

> **STOP AT THE WORLDS REACHABLE WITHOUT ANYONE INTENDING THEM** — harness defaults, unrendered
> furniture, an omission, an ordinary refactor. Those arrive without a decision, which is why they
> are the ones that show up. A world that requires someone to write wrong code on purpose is a
> different problem, and tests are not the instrument for it.

A fourth outcome belongs here. p5 mutated a load-key guard expecting red and **got a hang** —
`loadAccounts` sets `@state`, which schedules an update, which re-enters. A harness with a timeout
would have reported that as a failure, and the lesson banked would have been "the mutation went
red" attached to the wrong mechanism.

p5's own note on this is the part worth keeping: they saw the truth partly because a 120-second
bash ceiling **could not lie to them in that particular way**. A discipline that depends on a
tooling accident is not a discipline — it is a run of luck that reads like rigour, which is this
section's subject applied to our own method. So the transferable form is narrower than "look at how
it failed":

> **RULE.** Check that the failure you observed is the failure you **predicted**, by **mechanism**
> and not by colour. A red line is the same colour for every reason.

**And the fourth error of mine, found by p3 while fixing #48.** I reported the PATCH branch as
answering the message `"GCP service account not found"`. **It never did.** `writeErrorFromErr`'s
third parameter is `requestID` (`errors.go:105`); the message is hardcoded `"Resource not found"`
and my quoted literal was going into the response's `requestId` field. The oracle was real and
*worse* than I described — 404 against 400, readable without the body — but the mechanism I
supplied was wrong.

> **RULE.** A string literal at a call site is evidence of **the author's intent**, not of the
> response. Check the **position** of the argument, not its value.

Run mechanically, that rule found **nine more live sites** passing a resource-type literal as
`requestID` (`handlers_messages.go` ×3, `handlers_notifications.go` ×6). Each one puts a constant
string in the response's `requestId` **and in the `slog` line**, so an operator correlating a 5xx
gets `"Subscription"`. Hub-core, filed as **#50**, routed like #25 and #33.

**And a fifth, structural rather than verbal: I had been measuring in the wrong tree.** My working
branch is ~15,000 lines behind the branch that ships, and two SA handler files totalling ~900 lines
(`sa_assign_gate.go`, `handlers_gcp_identity_scoped.go`) **do not exist in it**. Every claim I
re-checked survived — including the one that mattered, that no `ActionCreate` exists in the SA
handlers — but the method got lucky rather than being right. This is rule 16 with **the tree** as
the unstated variable. Measure against the shipping branch; say which tree a measurement came from.

#### What this section costs me

**Three** of today's findings originate in this document, and all have the same shape: a claim
reasoned in a narrow case and written in the general one. #48 is the third — §8.4 wrote a
disclosure rule for *the service-account routes* and the two assignment surfaces load the same
resource from another file, so the rule simply did not reach them. §5.5 described a mode without saying
what installs the checker. §8.4 said "the nested routes still render 403" while thinking only
about refusals that had a reason. **In both cases the implementer built exactly what was
specified.** The instrument I now owe this document is not a better reviewer — it is the habit
from rule 16 applied to specification prose: *write the sentence the reasoning licenses, then the
sentence you want to ship, and if they differ you have not finished specifying.*

### 8.5.5 A ref another person can resolve, and a grep that could not have found what it looked for

Two more of my own, an hour apart, and the second is the more expensive.

**One — the referent.** Reporting the wrong-tree error to aid-em, I wrote: *"I have re-verified
the rest of what I sent you: `checkAccessForUser`'s bypass ordering and the `matchesResource` /
`hub-member-read-all` mechanism both hold on **the shipping branch**."* Two faults. I had
verified on `origin/scion/svc-accnt-lead` — **my** shipping branch, not theirs; in a two-track
project "the shipping branch" has no unique referent, and they would have read it as
`agent-id-fix`. And the bypass ordering I had not re-measured **anywhere** — it was still the
stale-tree read, and their own first-person re-run was the only evidence under it.

This is rule 19 turned on me: a wrong **referent**, not a wrong dimension — the kind that reads
as fully correct and so never gets challenged. Re-measured at `bbd5b393`: ordering confirmed
(`:120` admin, `:128` owner, `:136` ancestry, `:144` project, `:170` policies), `matchesResource`
two-case switch at `:410`, `hub-member-read-all` intact — and
`git diff 9a85f085..bbd5b393 -- pkg/hub/authz.go pkg/hub/seed.go` is **empty**, which is what
makes the re-measurement transferable rather than a point observation.

> **A MEASUREMENT IS NOT REPORTABLE UNTIL IT CARRIES THE REF IT WAS TAKEN AT, AND THE REF MUST BE
> ONE ANOTHER PERSON CAN RESOLVE.** "The shipping branch", "my tree", "tip" are not refs.
> `bbd5b393` is. A sha is also the only thing that disambiguates across two tracks.

Adopted by aid-em as the successor to their Rule 7. Independently arrived at by dev4 from the
other side: they withdrew a handler count (175 → unreproducible; the real figure is 425/338)
because they could no longer say which filter produced it, and parked the measurement as an
**executable script** rather than a table so the filter cannot separate from the count. Same
disease, same cure — *make the narrowing an artifact instead of a memory.*

**Two — I searched for a value and concluded about a capability.** My eight-hits-all-in-tests
`Effect: "deny"` measurement, the one that prompted the whole named-production-path rule, was a
grep for a **Go composite literal**. The production path assigns the value from a request field
— a variable, off JSON. **By construction it is never a literal. My grep could not have found
the production path whether or not it existed.** The value is a documented, explicitly
validated API input, and the handler that accepts it is live.

That is p3's string-literal correction one level up — *check the argument's position, not its
value* — and I walked into it three hours after adopting it. The consequence cuts in aid-em's
favour: the SA `ActionRead` closure row is **category 1, ordinary path**, not a category-2
operator act, and it clears the raised bar. The row cites the accepting handler at a resolvable
sha — **not my grep**. (Coordinates deliberately omitted here; see the redaction note in §9.4.
A count can be true while the inference drawn from it is unsound, and mine was.)

The same grep is what led to #51 (§9.4) once run in the right shape.

**And the leak check I wrote to prevent exactly this did not stop me.** I ran
`grep -c <patterns> && git commit && git push`. `grep -c` exits **0** when it finds matches, so
the chain ran, the two coordinates above went out, and I read the count *after* the push. **A
check whose failure does not stop the action is not a check — it is a log line.** Same family
as everything else in this section: the instrument ran, returned a true number, and answered a
question ("how many?") that was not the one the gate needed ("may I proceed?").

> **AN ABSENCE OF LITERALS IS NOT AN ABSENCE OF CAPABILITY.** Before concluding "nothing does
> X", ask what a caller that did X would *look* like in the text — and if it would look like a
> variable, the grep was answering a different question.

**And the rule that supersedes the ref discipline four paragraphs above — necessary, not
sufficient.** dev4 named why my failure is worse than theirs, and it is not obvious. Theirs was
a filter they could not reconstruct: the claim collapses to *no claim*, visibly nothing. Mine
returned **a clean, specific, reproducible answer to a question nobody asked.** Pinned at a sha
it would still have returned the same eight hits to anyone re-running it, and re-running it
*confirms* it. Nothing about it looks wrong at any point.

> **A SHA MAKES A MEASUREMENT REPRODUCIBLE. ONLY THE SCRIPT MAKES IT THE SAME MEASUREMENT.**
> A sha pins the **input**; it says nothing about the **question**. Every measurement failure
> among the three of us today was a wrong-question failure wearing right-input clothes.
> So: **name the ref, and park the question as an artifact.**

That is why the parked note for #51 carries a reproduce block rather than coordinates alone —
coordinates would have let the next person re-derive the wrong thing confidently.

**A postscript, because p3 applied the same rule to me within the hour and it inverted my
answer.** I had ruled that the create-side scope pair was a weak differential because it varies
*two* things — kind of scope as well as reachability — and I illustrated it with a rewrite that
admits everything project-scoped and refuses hub-scoped. p3 **measured it instead of arguing
it**: that rewrite fails three tests loudly. The blind direction is the **mirror** — admit
hub-scoped, refuse every project-scoped account, *breaking assignment for essentially every
real project* — and both existing arms stay green. So the ruling was right, the reason was
right in substance, **and the direction was backwards in a way that made the defect sound rarer
than it is.** The remedy landed as `TestAgentCreate_ReachabilityIsTheOnlyVariable`
(`a476153b`), which shares a constructor so `Scope`/`Verified`/`CreatedBy` are equal *by
construction* — `CreatedBy` especially, since it becomes `Resource.OwnerID` and the owner
short-circuit precedes policy evaluation, so an arm differing there would separate for a reason
unrelated to reachability while still looking like a reachability test.

p3 also carried the day's rule into a place none of us had looked: their first report reached me
with two predicates **eaten by shell expansion**, and it still *parsed* — a complete-looking
measurement with the measured thing missing, whose most plausible repair (assume the blank was
the direction I proposed) was the wrong one.

> **A SENTENCE CAN SURVIVE THE LOSS OF ITS REFERENT AND STAY PLAUSIBLE.** Nothing about a
> well-formed sentence reports that its subject is gone. "The shipping branch", a stale sha, and
> a blank predicate are the same failure at three different scales.

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

### 9.4 🛑 The premise Goal 1 never stated — the policy store is self-serve (#51)

> 🔒 **DETAIL REDACTED AND PARKED — 2026-07-28.** The mechanism, coordinates and reproduction
> for #51 are **not recorded in this document**. This repository is a public fork, and #51 is
> being assessed by the security track as potentially in-class with #591 rather than as a
> separate hub-core defect. The material is parked with aid-em and the svc-accnt lead; the
> disclosure channel is ptone's ruling (p9), not mine. **Do not restore the detail here.**
> What remains below is the *design* consequence, which is not itself a reproduction.

Goal 1 is *"require an IAM permission check before binding an SA to an agent."* That check
reads the Hub policy store. **The strength of the check therefore depends on the
write-protection of the policy store, and this design never specified that write-protection at
all.** Whether the current protection is sufficient is #51, and it is open.

This is not an argument against Goal 1. It is an **unstated premise** of Goal 1, and the
reason it went unstated for the whole design is instructive: I specified the *gate* in detail
— which action, which resource, which helper, what happens on each identity kind — and never
once asked who may write the inputs the gate reads. **A gate is a function of the policy
store; I designed the function and assumed the argument.**

The general form, which applies past this project: **AN AUTHORIZATION CHECK IS AT MOST AS
STRONG AS THE WRITE-PROTECTION ON THE DATA IT CONSULTS. Specify both, or you have specified
neither.**

Status: #51 is **parked, not filed** — held by the security track pending ptone's disclosure
ruling. Whether Goal 1 may ship before it is resolved is ptone's call, not mine. It subsumes
part of #46 and outranks it. It does not change step 5's hold — it adds a reason for it.

**And a redaction lesson, since this section had to be cut after it was published.** I wrote
the reproduction into a design document and pushed it to a **public fork** inside two minutes
of measuring it, while a disclosure hold was in force over exactly its subject matter. The
hold I was carrying said *"post followups on the fork publicly"* — granted for **this
project's** followups, and I applied it to a finding that had just stopped being this
project's. **A disclosure permission is scoped to the class of thing it was granted for, and a
finding can leave that class between the grant and the writing.**

**And the sentence I first wrote here was itself wrong, in the same way.** I said the blob was
"reachable by sha until GitHub garbage-collects" — which implies it ages out. It does not.
dev4 read GitHub's documentation instead of guessing: rewritten-away commits stay reachable in
cached views by sha, in any fork or clone, and through any referencing PR; removal requires
GitHub Support to dereference, GC and drop caches, against a stated bar of risk that *cannot be
mitigated by rotating affected credentials*. **A vulnerability reproduction is not a rotatable
credential**, so the one removal path we would want is the one that bar is written to decline.
The correct status is **partially published with permanent residue — not recalled.**

There were also **two surfaces, not one**: the same content went to a chat thread of
unconfirmed visibility, because I put the mechanism into the message that asked someone to
route the finding. **Routing to the decision-maker was right; routing the mechanism in order to
reach them was not** — a pointer would have informed them identically. *An internal recipient
is not an internal channel.*

I asserted "about nine minutes" of exposure to three people; measured from the reflog it was
**under two** (`0fc36645` 21:15:17Z → redacted tip pushed ≈21:17:05Z, local times bounding the
remote window). Wrong in the harmless direction, and still a number I repeated as though I had
measured it. **Note what these two have in common with everything else in §8.5.5: inside the
report of a measurement failure, I twice asserted a property I had not measured** — the
remediation's durability, and the exposure's duration. The shorter window does not improve the
position and is not offered as though it does; **permanence does the work, not duration.**

**One criterion came out of the audit that followed.** I swept this whole document for other
content that had left its permitted class. Almost none had — most of it is either already
public by ptone's own filings or is this project's own subject matter, which the grant covers.
But the grant's words were *"post followups publicly **so they are tracked**"*, and §5.1
publishes a live defect (#29) that has never been filed. **Publication without filing takes the
entire cost of disclosure and none of the benefit** — and it is a state reachable by *doing
nothing*, which is why nobody noticed. That is the vacuity trap wearing process clothes: the
do-nothing world and the compliant world look identical from inside the repo.

> **A DISCLOSURE PERMISSION GRANTED FOR TRACKING DOES NOT COVER PUBLICATION WITHOUT FILING.**
> The document is not a filing. Either file it or park it.

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

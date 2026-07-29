# Policy gate: interim rationale and relaxation guide

**Status:** interim, adopted 2026-07-29 (provenance in §2). Expected to be relaxed; see "Before
relaxing" below.
**Audience:** maintainers deciding when and how to loosen this gate.

This note explains why the policy API is currently restricted to hub administrators, how that
differs from what `permissions-design.md` describes, and what would have to be true before the
restriction is relaxed. It exists because the restriction is *deliberately stricter than the design
it implements*, and a future maintainer finding that mismatch deserves to know it was a choice
rather than a mistake.

## 1. What the policy API is, and why it was open

The permission system is configured through two related objects. A **policy** describes what may or
may not be done — a set of actions, on a type of resource, within a scope, allowed or denied. A
**binding** attaches a policy to a principal: a user or a group. Neither does anything alone. A
policy with no binding grants nothing; a binding is what connects a rule to the person it applies
to.

The endpoints that manage these objects were built alongside the permission system itself, and they
were reachable by any authenticated caller. Authentication was required; authorization was not. This
is an easy state to arrive at, because the policy endpoints are the machinery that *implements*
authorization, and machinery tends not to get pointed at itself. The system was checking permissions
everywhere except on the surface that defines permissions.

The problem with that arrangement is circularity rather than any single missing check. A permission
system whose own configuration is not permission-checked can be asked to describe a different set of
permissions. Every other control in the system is downstream of this one, so this surface has to be
at least as well protected as the strongest thing it can authorise — and it was not.

## 2. The interim gate: what it restricts, and what it still allows

The policy surface is now restricted to hub administrators. Five operations **change** the
configuration:

- create a policy
- update a policy
- delete a policy
- attach a policy to a principal
- detach a policy from a principal

and three **describe** it:

- list policies
- fetch a single policy
- list a policy's bindings

All eight require hub administrator rights. The check runs at the entry of each handler, before the
request body is parsed, and it fails closed: a caller who is not a hub administrator is refused
regardless of what the request contains.

Both attach and detach are covered deliberately. Detaching is not a lesser operation than attaching
— a rule that has been detached no longer applies, so the ability to remove a binding is the ability
to change an outcome, and gating only the additive direction would have left the outcome reachable
from the other side.

The reads are covered for a related reason. A policy configuration is a description of who may do
what, which is useful to a caller deciding what to attempt. Restricting the writes while leaving the
reads open would have protected the ability to change the rules while continuing to publish them,
and the interim was chosen to be uniform rather than clever.

**Two things this gate does not do:**

- **The access-evaluation endpoint has its own, narrower rule** — a caller may evaluate access for
  themselves, and an administrator may evaluate for anyone. It is not part of this gate and is not
  changed by it.
- **Nothing else in the permission model changed.** Group membership, role assignment and resource
  authorization are unaffected.

There is one visible behaviour change worth knowing about in advance, and it applies to the whole
surface rather than to any single operation. On every one of these routes the authorization decision
is now made before anything is looked up. A caller who is not a hub administrator is therefore
refused in the same way whether or not the policy they named exists — including on the binding
routes, where the lookup that confirms a policy exists sits behind the check rather than in front of
it. Previously that lookup ran first, so the response distinguished a real policy from an invented
one for a caller entitled to neither. Refusing identically in both cases is deliberate: the
alternative discloses which policy identifiers are real to someone with no right to know.
Administrators see the previous behaviour unchanged, genuine not-found responses included.

The gate is a single, uniform check applied in one place per handler. It is easy to find, easy to
reason about, and easy to revert or replace — which was the point. An interim measure should be
simple enough that replacing it is not itself a risky change.

### Provenance of the decision

The restriction on the five write operations was ratified by the project owner on 2026-07-29, on the
principle that starting strict on this surface is acceptable. **The extension of that ratification
to the three read operations was a separate decision**, taken by the coordinating maintainer at
01:40Z on the same date, for the reason given above. It is recorded here as a decision in its own
right rather than as something read out of the original ratification, because the two were settled
separately and a later reader should not have to reconstruct which was which.

## 3. What the design actually anticipated

`permissions-design.md` (§6.2, the policy endpoints) specifies **scope-relative rights**, not a
hub-administrator gate: creating a policy "requires `manage` action on the scope," and
updating or deleting one "requires `manage` action on the policy's scope." Its read model is
scope-relative in the same way — listing policies is specified to return "only policies the caller
can view (based on scope access)." That document now also carries a short implementation note beside
each of those entries, recording the gate this one explains; the specified behaviour underneath them
is unchanged, and it is the specified behaviour this section compares against.

That phrasing depends on the containment hierarchy in §2.3, where resources live inside scopes —
hub at the root, projects beneath it, individual resources beneath those. (The design document
predates a rename and calls projects *groves* throughout; the two mean the same thing when reading
across.) A policy is written *against* one of those scopes. Under the design's intent, the right to
manage or view policy is therefore not a single global privilege but a local one: whoever holds
`manage` on a project may manage the policies scoped to that project, and see them, without holding
any hub-wide role.

The interim is stricter than that in both halves, and for the same reason in each:

| | Design intends | Interim |
|---|---|---|
| Policy writes | scope-relative `manage` | hub administrator only — stricter |
| Policy reads | filtered by scope access | hub administrator only — stricter |

A hub-administrator check is a coarser instrument than the design calls for. It asks a single global
question where the design asks a local one, and it grants nothing to a project owner acting inside
their own project, which the design intends to allow.

**Relaxation therefore means the same movement in both rows** — from one global role toward rights
evaluated against the scope the caller is actually acting in. The two rows can be relaxed
independently and in either order, but neither is finished until it is scope-relative, and stopping
after the writes would leave the read path exactly as far from the design as it is today.

## 4. What relaxation would require in the code

Relaxing to the design's model is not a matter of substituting one role check for another. The
current check asks a question about the *caller* ("are you an administrator?"). The design's check
asks a question about the *relationship between the caller and a scope* ("do you hold `manage`
here?"), which requires knowing which scope is in play and resolving the caller's rights against it.
Concretely:

- **The scope has to be derived from the stored policy, not from the request.** For update and
  delete, the relevant scope is the one the policy already carries. Reading it from the incoming
  request would let the caller nominate the scope their rights are checked against, which is not a
  check.
- **Bindings involve two subjects, not one.** Attaching a policy to a principal touches both the
  policy's scope and the principal being attached. A rule that considers only the first would permit
  reaching across a scope boundary through the principal side.
- **The evaluation should reuse the normal authorization path** rather than growing a bespoke
  comparison inside the policy handlers. Policy is one resource type among several; a second,
  parallel implementation of "does this caller have `manage` here" is a second thing to keep correct.
- **Reads need filtering, not just a check.** The write operations act on one policy and can be
  allowed or refused. Listing is a different shape: the design's intent is that a caller receives the
  subset they are entitled to see, so the read path needs a filter applied to results rather than a
  single yes-or-no at the entry. This is the piece least similar to anything that exists today.
- **Keep the ordering: authorization first, existence second.** Every route on this surface performs
  its authorization check before it looks anything up — including the two binding routes, where the
  lookup that confirms a policy exists sits behind the gate rather than in front of it. That ordering
  is load-bearing: a check that runs first and a lookup that runs first give different answers to a
  caller entitled to neither. When the reads move to scope-relative filtering, this property has to
  survive the change. The fact that a particular policy exists should stay behind the authorization
  decision rather than becoming visible in front of it.

That last point is the one most easily lost, because it is a property of how the routes are arranged
rather than of any single check, and a reorganisation can dissolve it without any individual check
looking wrong.

## 5. Before relaxing

The strict gate is cheap to keep and disruptive only to workflows that should arguably not have
existed. There is no schedule pressure to relax it, and "it has been fine for a while" is not
evidence that a looser rule would also be fine — the strict rule is precisely what has prevented the
looser rule from being tested. Relaxation should be a considered change with the following in place.

**1. Settle the specification first.** `permissions-design.md` describes scope-relative rights at a
level of detail that is sufficient for a reader and insufficient for an implementer. At minimum it
should resolve what `manage` means for a policy whose scope is an individual resource rather than a
project; what is required when a binding's policy and its principal sit in different scopes; whether
holding `manage` on a scope permits authoring policies that affect ancestors of that scope; and what
"can view" means for a policy scoped above the caller that nonetheless affects them. Each of these
has a defensible answer and a plausible wrong one, and they should be decided in the spec rather than
settled implicitly by whatever the first implementation happens to do.

**2. Cover the scope-relative paths with tests, in the negative direction especially.** The
interesting cases are not "a project manager can manage their project's policies" but the refusals
next to it — the same caller reaching a neighbouring scope, or a parent scope, or a principal outside
their reach. For the read path the equivalent is a listing that must come back *filtered*, rather
than either empty or complete. Coverage should include every kind of caller the hub accepts, not
only interactive users; caller kinds that were not considered are how the original gap arose.

**3. Track it as a follow-on, and change one thing at a time.** The relaxation is an intended
follow-up rather than an abandoned intention, and should be carried as a tracked item so that it is
revisited deliberately. Writes and reads are separable and should be separated: relaxing writes to
scope-relative `manage` and introducing scope filtering on reads are different changes with
different shapes, and the read change carries the ordering property described in §4.

**4. Keep the fail-closed default through the transition.** Whatever replaces the current check
should refuse when it cannot determine the answer. The gate being replaced fails closed; a
replacement that fails open on an unresolvable scope would be a regression even if it were correct
in every case anyone thought to try.

---

*If you are reading this because the gate is in your way: it is intended to be replaced, the shape of
the replacement is described above, and the reason it was set this strict is that the surface it
protects is the one that governs every other permission in the system. Starting strict and loosening
deliberately was a choice, not an oversight; its provenance is recorded in §2.*

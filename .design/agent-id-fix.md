# Design: Hub-wide authorization bypass for non-user callers (issue ptone/scion#591)

**Author:** aid-arch
**Date:** 2026-07-28
**Repo state:** `ptone/scion`, branch `scion/agent-id-fix`, **branched from `c96af412`** (`GoogleCloudPlatform/scion#893`).
Verification after 18:05Z is against branch tip `c2d12fac` or later; `main` is at `db8f6fc5`.
*(This field previously read `8dbf167`, which is not the branch point — corrected rather than
annotated, since a reader who reads the header and stops must not get the retracted value.)*
**We do not rebase onto `db8f6fc5`: `main` cannot compile its own `pkg/hub` test binary (§8.5 item
7, verified `go vet` exit 1 vs exit 0 on our base and tip). If it is unfixed when we open, the PR
opens from `c96af412` and the PR body says so.**

> ### Reading convention for every claim in this document
>
> Standing rule 1 says *name the branch on any authorization claim*. Applying that inline at every
> occurrence would be unreadable and would still leave the unmarked majority ambiguous, so it is
> applied here once, as a default, and then only the exceptions are marked. **aid-dev4 found the
> rule applied in only three places; this is the repair.**
>
> - **An unqualified claim describes the pre-fix base.** Most of this document describes the bug, so
>   the base is the useful default: "X is not checked" means *not checked before we started*.
>   **It is NOT equivalent to `main`** — see the box below, which is the correction of a claim this
>   very convention block asserted on its first draft.
> - **A claim about work already landed names its commit** (`d2414faf`, `31ddcaf`, `180debe`,
>   `444b385f`, `87c1e632`, `c2d12fac`) **or the branch tip explicitly.** Verification performed
>   after 18:05Z is against `origin/scion/agent-id-fix` @ **`c2d12fac`**.
> - **A claim true of one tree and false of another must say so on the spot.** The live example is
>   §8.2: *project-scoped template policies match nothing* is true only on a tree carrying the ptone/scion#595
>   engine fix without the `180debe` builder fix — which is no branch that has ever existed.
> - **A fact relayed from another agent is marked as relayed**, never folded into a first-person
>   "verified" (standing rule 8; see §8.1b for the incident that produced it).
>
> The reason this belongs at the top rather than in the rules section: a reader who reaches §8.1b
> has already read 1,000 lines of unqualified claims.

> ### ⚠️ The base is not `8dbf167`, and `main` has moved — see §8.5
>
> Found while checking the sentence above, which originally read "equivalently `main`, since this
> branch is the only thing between them." Both halves were false, and the check took one command:
>
> - **The true branch point is `c96af412`**, not `8dbf167`. The header carried `8dbf167` because
>   that was the tip when the doc was started, and the branch was rebased afterwards. The chain is
>   three commits — `8dbf1674` (`GoogleCloudPlatform/scion#891`) → `819df962` (`GoogleCloudPlatform/scion#892`) → `c96af412` (`GoogleCloudPlatform/scion#893`) — so `8dbf167`
>   is a real ancestor of both trees, just not the divergence point.
>
>   *This clause was challenged twice as unverifiable and survived both times; it is now confirmed
>   by three agents on unshallowed clones — tip of **zero** branches, contained in **10**, ancestor
>   of both `c96af412` and `origin/main`. Ancestry is transitive, so anyone who accepts `c96af412` as
>   the merge-base has already accepted `8dbf167`.*
>
>   *Two different causes produced the two challenges, and **the second is the one to remember**.
>   The first was testing membership in a list of heads, which contains tips — membership is not
>   reachability. The second, and the load-bearing one, is that **the agent containers are shallow
>   clones**, and under a graft `git merge-base --is-ancestor` — the correct command, the one the
>   first diagnosis prescribes — **returns a confident, silent, wrong `no`**. Two agents got a wrong
>   answer from the right command; a third (mine) got the right answer from a shallow clone too,
>   purely because its graft boundary sat at the disputed commit rather than above it. **Run
>   `git fetch --unshallow` before any ancestry claim.** §8.5 item 8 has the boundary table and why
>   "check `--is-shallow-repository` first" is the wrong remedy.*
>
>   *The §1.1 shape in one line: **a commit absent because history was truncated is indistinguishable
>   from a commit that never existed** — `cat-file -e` does not error under a graft, it answers no.*
> - **`main` is at `db8f6fc5` and carries one commit this branch does not** — `GoogleCloudPlatform/scion#888`, hub-scoped
>   pre-start hooks — which adds **385 lines of new authorization code in a file this document has
>   never seen**, and touches three files this branch also modifies. **§8.5** covers it.
>
> This is standing rule 7 one level up: I was writing the *rule about naming trees* and named them
> from memory instead of from `git`. A convention block asserting an unchecked topology is the same
> defect as a doc comment asserting an unlanded caller (rule 6).
**Status:** FOR REVIEW — Q1–Q4 answered by ptone 2026-07-28 and incorporated. See §11 for the
resolutions and §12 for the follow-on track ptone raised.
**Supersedes/extends:** `projects/svc-accnt/SECURITY-authz-bypass.md` (sa-arch), `projects/svc-accnt/findings-sa-inv.md` §5.1/§5.2/Q10/Q12

> **Handling — RESOLVED 2026-07-28.** The brief asked that specifics not circulate publicly
> pending a disclosure path. That was already moot: `ptone/scion` is a public repo and
> `ptone/scion#591` was opened on it at 14:36Z with a 9,299-char body carrying the root-cause
> idiom, the `identity.go` explanation, and a `file:line` impact table. Escalated to ptone, who
> ruled at 15:57Z: *"We are working on fixing these in short order today - so post followups on
> the fork repo publicly to make sure they are tracked."*
>
> All five follow-ups are filed publicly and cross-referenced from ptone/scion#591:
> **ptone/scion#595** (`matchesResource` engine defect), **ptone/scion#596** (GCP gate alignment), **ptone/scion#598** (route
> authz manifest), **ptone/scion#599** (ServeMux migration), **ptone/scion#600** (dead-code cleanup).
>
> **EVERY ISSUE REFERENCE IN THIS DOCUMENT IS REPO-QUALIFIED. THERE ARE NO BARE `#NNN`
> REFERENCES AND THERE MUST NOT BE.** Not even where bare would resolve correctly from
> where you happen to be reading. A bare reference silently defaults to whichever repo the
> reader is standing in, and this document is read from the fork more often than from
> upstream while the PR that carries it is filed against upstream. Two namespaces, two
> syntaxes, no defaults.
>
> The fork's issue counter shares a number space with upstream's pull requests, so every
> fork issue cited here has an upstream namesake, and every one of those namesakes is a
> real merged PR rather than a 404: `GoogleCloudPlatform/scion#591` is a Slack broker
> plugin, `GoogleCloudPlatform/scion#595` an auth fallback,
> `GoogleCloudPlatform/scion#596` a bootstrap fix, `GoogleCloudPlatform/scion#598` a
> vertex-ai auth fix, `GoogleCloudPlatform/scion#599` a Slack admin UI,
> `GoogleCloudPlatform/scion#600` a dead-harness removal,
> `GoogleCloudPlatform/scion#604` a pre-start-hook fix, `GoogleCloudPlatform/scion#605` an
> auth-detection fix, `GoogleCloudPlatform/scion#606` an env-overlay fix. Verified against
> the GitHub API on 2026-07-28.
>
> **Two collide on topic, and that is what turns a broken link into a false belief.** Our
> `ptone/scion#600` is a dead-code cleanup and `GoogleCloudPlatform/scion#600` removes a
> dead harness system. This change is largely about broker dispatch and
> `GoogleCloudPlatform/scion#591` adds a broker plugin. A reviewer who clicks an
> unqualified fork number from an upstream PR lands on a page that renders fine, reads as
> on-topic, and is not our issue — so the check that should have caught the mistake
> ratifies it instead.
>
> The trap runs both ways. `GoogleCloudPlatform/scion#888`,
> `GoogleCloudPlatform/scion#891`, `GoogleCloudPlatform/scion#892` and
> `GoogleCloudPlatform/scion#893` —
> qualified above and throughout — are upstream PRs that return 404 on the fork. Any
> uniform rewrite in either direction breaks one namespace or the other; qualification has
> to be per number, and each one here was checked individually.
>
> **Commit subjects on this branch are a separate and unfixable case.** 84 bare references
> live in them, 72 of them the fork authz issue, across 28 subjects; GitHub autolinks those to upstream
> on the commits tab. They are pushed history and are not being rewritten. The mitigation
> is a statement in the pull request body, not an edit here.
>
> The security issue exists only on the fork.

---

## 1. Summary

An agent-authenticated caller — and, on some paths, an unauthenticated one — can perform
privileged operations hub-wide and cross-project, because ~24 handler sites write their
authorization guard in a form that is silently skipped for any identity that is not a
`UserIdentity`.

The authorization *engine* (`AuthzService.CheckAccess`, `authz.go:94`) is correct: it
dispatches on `identity.Type()`, handles agents, and fails closed on unknown types. Only the
call sites are wrong.

**The fix is two parts that must ship atomically:**

- **Part 1** — convert the call sites to a fail-closed shared helper.
- **Part 2** — give agents a defined authorization baseline, because after Part 1
  `checkAccessForAgent` is default-deny with nothing granting agents anything.

This document specifies both, plus three corrections to the source doc's site inventory that
expand and re-rank the scope.

### 1.1 The thesis: a state that is indistinguishable from a different state by inspection

Almost every defect in this document — and every mistake made while writing it — is one shape.
**Two different states present identically to a reader, and the benign reading is the one that
comes for free.** This is why several changes here *add a signal* rather than change a decision,
which is otherwise the hardest thing in this PR to justify:

| Looks like | Actually might be | Where |
|---|---|---|
| authorization checked | never checked for this identity kind | ptone/scion#591, the whole of §3–§4 |
| a hub-global resource | a builder that forgot to set the parent, or a genuine many-to-many | ptone/scion#604, §8.2, `ProjectPreStartHook` (§8.5) |
| a protection in place | a comment naming a caller that does not exist | §8.1a, and §8.5 item 6 |
| a considered exception | work someone abandoned | §7.2 allowlist ruling |
| a verified fact | a fact relayed from someone else, or read off a stale tree | §8.1b, standing rules 7–8 |
| a commit that never existed | a commit below your clone's graft boundary | §8.5 item 8 — the tool, not the prose, produces this one |
| a passing check | a check built so it **cannot report** a problem | §7.2 laundered green; §8.5 item 7 (`go vet \| head`) |
| a published fix | a fix built so it **cannot be found wrong** — nobody ran it | §8.5 item 9 |
| a clean codebase | **a blind detector** | **§7.2a — see below** |

**The strongest instance is the last, and it is ours.** `hack/check-authz-guards.sh` reports twenty
findings. **Zero findings would read identically whether the code is clean or the detector is
blind** — and §7.2a establishes that this detector is partly blind, in a direction that matters:
it cannot see `GetIdentityFromContext`, which is the getter our own fix uses. That is ptone/scion#591 pointed
at our own tooling, and unlike the handlers, **it would have shipped under our name *as the fix*.**

Two corollaries a reviewer should hold onto:

- **A regression detector covers the shape the bug had, not the shape the fix has.** This is the
  standing failure mode of every detector written by the person who just fixed the bug, because the
  fix is the one shape they are certain is safe.
- **Standing rule 6 exists because a comment is the only artifact we ship that nothing verifies.**
  Code is checked by the compiler, behaviour by the tests, style by the linter; prose about
  behaviour is checked by nobody — and it is what a reader trusts most under time pressure. Three
  independent instances landed in three files by three authors in a single day (§8.1a, §8.5 item 6,
  and the retracted `180debe` commit body). That rate is not coincidence.
- **Recall failures produce silence, and silence is what success looks like** (aid-dev4). A
  *precision* failure arrives as a false positive that somebody must dismiss, so it generates
  attention; a *recall* failure generates nothing at all. This is why detector blindness is not
  merely undetectable but **unlooked-for** — and it is the same structure as the comment point.
  Both concern artifacts whose failure mode emits no signal, which is the general form of every row
  in the table above.

**Status of the guard's recall, stated in the qualified form** (`6cd5bf06`; aid-dev4 refused a
stronger claim I and aid-em were both ready to accept): **the floor has been lifted, not proven to
be the total.** Three shapes, two getters, verdict and comment placement all pinned — and still
blind to the absent-check class of §7.3. Overclaiming in one's own favour is hardest to notice
immediately after a fix, which is precisely when the fix's author is asked whether it is complete.

### What this design changes relative to the brief

Three findings altered the shape of the answer:

1. **There is a second syntactic form of the same bug** that the source doc classified as
   "verified fail-closed." Four additional unguarded cross-project reads. (§3.2) *(Verified by me at `c96af412`, the pre-fix base.)*
2. **`createProjectAgent` is Group A, not Group B** — it has no isolation check at all, not
   "isolation implied by the URL project." Cross-project agent creation. (§3.3)
3. **The repo already has a working agent-authorization model** — JWT token scope + project
   isolation — implemented correctly in three places and simply absent at the broken sites.
   Part 2 therefore does not need to invent a policy baseline; it codifies what exists. This
   makes Part 2 substantially smaller and lower-risk than the brief anticipated. (§5)

---

## 2. Root cause

Defer to `SECURITY-authz-bypass.md` for the full narrative. In brief:

`agentIdentityWrapper` (`identity.go:120-145`) implements `AgentIdentity` but not
`UserIdentity` — no `Email()`, `DisplayName()`, `Role()`. `brokerIdentityImpl`
(`brokeridentity.go:29-44`) implements neither. So both fail the `UserIdentity` type
assertion in `GetUserIdentityFromContext` (`identity.go:167-176`), which returns `nil`, and
the entire guard block is skipped. No error, no log, no audit record.

### 2.1 The structural cause, which matters more than the idiom

Three facts together explain why this bug class exists and why it will recur without a
structural fix:

1. **There is no authorization middleware.** The chain (`server.go:2863-2900`) is
   CORS → `UnifiedAuthMiddleware` → `adminModeMiddleware` → `userActivityMiddleware` →
   `BrokerAuthMiddleware` → logging → recovery. Every authorization gate is per-handler.
2. **`RequireAuth`, `RequireUserAuth`, and `RequireRole` (`auth.go:457-500`) are never
   called from anywhere.** The source doc lists `auth.go:472,490` under "verified
   fail-closed" — accurate, but only because it is dead code. It should not be read as
   evidence that a middleware layer is enforcing anything.
3. **There is no shared authorization helper.** Roughly 110 `CheckAccess` call sites across
   `pkg/hub` each hand-write the same fetch → nil-check → check → `writeError` sequence. The
   only reusable guards are three narrow ones: `requireAdmin`
   (`skill_registry_handlers.go:99`, used only within that file), `checkBrokerDispatchAccess`,
   and `authorizeProjectImport`.

An omission in a hand-written five-line idiom repeated 110 times is invisible to the
compiler, invisible to `go vet`, and nearly invisible to review. The bug is a predictable
output of that structure. **The single highest-value deliverable in this change is the
shared helper**, not any individual site conversion — the conversions are what make the
helper load-bearing.

---

## 3. Corrected scope inventory

The source doc's site table tracks one idiom. There are three.

### 3.1 Idiom 1 — `GetUserIdentityFromContext` guard (the known one)

```go
if u := GetUserIdentityFromContext(ctx); u != nil {
    decision := s.authzService.CheckAccess(ctx, u, res, action)
    if !decision.Allowed { /* 403 */ }
}
// no else
```

Skipped for agents and brokers. This is the source doc's Group A + Group B.

### 3.2 Idiom 2 — `identity.(UserIdentity)` type-assertion guard (NEW; Q1: in scope)

```go
identity := GetIdentityFromContext(ctx)
if identity == nil { Unauthorized(w); return }
...
if userIdent, ok := identity.(UserIdentity); ok {
    decision := s.authzService.CheckAccess(ctx, userIdent, res, action)
    if !decision.Allowed { Forbidden(w); return }
}
// no else
```

Functionally identical. Agents pass the nil check and skip the block; **so do HMAC-valid
brokers**, since `BrokerAuthMiddleware` writes `brokerIdentityImpl` into the generic identity
key (`brokerauth.go:825-826`) and it implements neither interface.

I audited all 30 occurrences of this form in `pkg/hub`. Twenty-two are genuinely compensated
(they have an `else { Forbidden(w); return }`, or a dedicated
`GetAgentIdentityFromContext` branch with project isolation) and four are attribution-only.
**Four are unguarded bypasses**, all cross-project reads, and all four sit in a function
whose *sibling write path* does have the missing `else`:

| Site | Function | Route | Leaks |
|---|---|---|---|
| `project_settings_handlers.go:74` | `handleProjectSettings` | `GET /api/v1/projects/{id}/settings` | project settings incl. `scion.io/default-gcp-identity-service-account-id` |
| `handlers_shared_dirs.go:47` | `handleProjectSharedDirs` | `GET /api/v1/projects/{id}/shared-dirs` | host mount paths |
| `project_pre_start_hook_handlers.go:77` | `handleProjectPreStartHooks` | `GET /api/v1/projects/{id}/pre-start-hooks` | hook `Script` bodies |
| `project_pre_start_hook_handlers.go:245` | `handleProjectPreStartHookByID` | `GET /api/v1/projects/{id}/pre-start-hooks/{hid}` | hook `Script` body |

The source doc places "workspace handlers" and "skills" in Group D. That is correct for
`handlers_skills_injection.go` and `handlers_resource_import.go` (both have the `else`), but
not for these four. **Recommend folding in — Q1.**

### 3.3 Correction — `createProjectAgent` is Group A

The source doc lists `handlers_projects_core.go:1758` under Group B, "isolation only implied
by the URL project." There is **no isolation.** `createProjectAgent`
(`handlers_projects_core.go:1720-1767`) resolves the caller only for attribution and calls
`createAgentInProject` directly. It is missing all three checks its twin `createAgent`
performs at `handlers_agents_core.go:323-357`:

| Check | `createAgent` (`POST /api/v1/agents`) | `createProjectAgent` (`POST /api/v1/projects/{id}/agents`) |
|---|---|---|
| `HasScope(ScopeAgentCreate)` | ✅ `:325` | ❌ |
| `req.ProjectID == agentIdent.ProjectID()` | ✅ `:330` | ❌ |
| `CheckAccess(agent, ActionCreate)` for users | ✅ `:352` | ❌ |
| Field-level GCP identity validation | ✅ `:294-311` | ❌ |

Any agent token creates an agent in **any** project. This belongs in the severity table
alongside cross-project PTY. It also means the missing-`else` fix (sa-inv Q12) and the
`createProjectAgent` fix are the same fix — see §4.3.

> **Severity upgrade — this is the route real traffic uses.** *(Traced by me at `c96af412`.)* I traced the clients while
> checking a separate claim, and `POST /api/v1/projects/{projectId}/agents` is not a
> secondary path: it is what the **CLI** calls for `scion create`, `scion start`, and
> `scion sync`. `hubclient.agentsPath()` (`pkg/hubclient/agents.go:120-124`) switches to the
> project-scoped URL whenever `ProjectAgents(projectID)` is used, and
> `createAgentWithBrokerResolution` (`cmd/common.go:1136-1148`) — the single funnel for both
> CLI create paths — always uses it. `scion-chat-app` uses it too. The web UI and the A2A
> bridge use the unscoped `POST /api/v1/agents`, which is the one that *is* gated.
>
> So the ungated create route is the dominant one by real-world volume, and the gated route
> is the minority path. That inverts the usual "obscure secondary endpoint" reading of this
> class of finding and should be reflected in how ptone/scion#591 is triaged.

**The fourth row is a separate gap, contributed by sa-arch and verified independently by me at `c96af412`.**
`createProjectAgent` validates name, `cleanupMode`, and labels, then calls
`createAgentInProject` — which does not re-validate. So on the project route:

- `metadata_mode: block|passthrough` **with** a `service_account_id` is accepted (rejected on
  `POST /api/v1/agents` at `:298`).
- An **unrecognised** `metadata_mode` is accepted entirely. `createAgentInProject` branches
  only on `== passthrough` (`:408`) and `== assign` (`:425`); anything else falls through with
  no SA resolution and no passthrough ownership check. The `default:` arm that rejects this
  exists only in `createAgent` (`:307`).
- **A live consequence, found while resolving sa-arch's empty-mode question.** The
  config-building switch at `:568-586` is the only GCP switch in the file with **no
  `default:` arm**, and its `else` branch is what applies the project's configured default
  mode. So `POST /api/v1/projects/{id}/agents` with body `{"name":"x","gcp_identity":{}}`
  matches no case, assigns nothing, and **bypasses the `else` that would have applied the
  project default** — leaving `AppliedConfig.GCPIdentity` nil. If the project is configured
  with `default-gcp-identity-mode: assign`, that default is silently dropped and the agent
  starts with no identity.

  This is fail-*safe* (nil → broker `block`), so it is a correctness bug, not a hole.

  **Reachability, stated precisely** *(checked by me against `handlers_agents_core.go` at `c96af412`; this file has changed on the branch since, so the claim is about the pre-fix tree)* — I checked every client after this was described to me
  as affecting "essentially every CLI-created agent," and that is not right. No in-repo
  client can produce it: both request structs use `*GCPIdentityAssignment` with `omitempty`
  (`handlers_agents_core.go:100-102`, `:1483`), so a zero value serializes as *absence*, not
  `{}`. The CLI's `hubclient.CreateAgentRequest` (`pkg/hubclient/agents.go:159-189`) has **no
  GCP field at all**, so CLI-created agents always take the `else` branch and *do* get the
  project default. The web UI always emits an explicit mode and posts to the gated route
  anyway. The case is reachable only from a hand-rolled HTTP caller.

  Keep the fix regardless — it is three lines, and "only reachable by a non-standard client"
  describes most of the rest of this document. But it should be recorded as
  defence-in-depth-plus-correctness, not as a live governance hole. The *governance* concern
  sa-arch is pointing at is real and is captured above: the CLI does use this route, and on
  this route the project default is applied by a branch that no validation protects.

This is an authorization-adjacent input-validation gap on a route this track already has
open. See §8.4 for what an unrecognised value does downstream — the answer changed twice
during review and the final characterisation matters.

### 3.4 Idiom 3 — attribution (do NOT convert)

~25 sites use `GetUserIdentityFromContext` to set `CreatedBy`/`UpdatedBy`, name a message
sender, or filter a list. These are correct. Full list in the source doc §C.

⚠️ **`handlers_github_app.go` needs two different things done a few lines apart:**

- **`:113`** — the `func handleUpdateGitHubApp` declaration. There is **no authorization of
  any kind** in this handler. Any authenticated principal overwrites the hub GitHub App
  private key, webhook secret, app ID, and API base URL. **ADD** an admin-only gate.
- **`:121`** — `user := GetUserIdentityFromContext(ctx); if user != nil { userID = user.ID() }`,
  threaded into `setGitHubAppSecret` as the secret's author. This is attribution and it is
  correct. **DO NOT CONVERT.**

Note the asymmetry, because an earlier draft of this section got it wrong and the error
propagated into §7.1 and two developer briefs: these are **not** a matched pair of guards with
opposite verdicts. `:113` has no guard at all — the finding is an *absence*. The file contains
exactly one `GetUserIdentityFromContext` occurrence, at `:121`.

Two consequences worth carrying forward:

1. The lint rule must still key on *guard shape* rather than the getter name, so that `:121`
   is not flagged. That requirement is real; only the illustration was wrong. See §7.1 for the
   test case that actually demonstrates it.
2. A lint rule keyed on guard shape **cannot see `:113` at all**, and never will. Absence of a
   check has no syntax. See §7.3.

### 3.5 Additional finding, not in any prior inventory

`getGroupMember` (`handlers_groups.go:685-695`) has no authorization at all — any
authenticated caller reads any group membership by ID. Low severity (membership metadata),
same class. Recommend folding in since we are already in the file.

---

## 4. Part 1 — call-site conversion

### 4.1 The `authorize` helper

New file `pkg/hub/authorize.go`:

```go
// authorize performs a fail-closed authorization check for any identity kind.
// It writes a 403 (or 401 for an unauthenticated caller) and returns false when
// access is denied, so callers can write:
//
//	if !s.authorize(w, r, agentResource(agent), ActionDelete) { return }
//
// Unlike the pre-ptone/scion#591 idiom it MUST NOT be wrapped in an identity-kind guard:
// nil and non-user identities are denied here, not skipped.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, resource Resource, action Action) bool {
	ctx := r.Context()
	identity := GetIdentityFromContext(ctx)
	if identity == nil {
		Unauthorized(w)
		return false
	}
	decision := s.authzService.CheckAccess(ctx, identity, resource, action)
	if !decision.Allowed {
		slog.Warn("authorization denied",
			"principal_type", identity.Type(),
			"principal_id", identity.ID(),
			"resource_type", resource.Type,
			"resource_id", resource.ID,
			"action", action,
			"reason", decision.Reason,
			"path", r.URL.Path)
		Forbidden(w)
		return false
	}
	return true
}

// authorizeMsg is authorize with a caller-supplied denial message, for the
// handful of sites whose existing 403 text is user-facing guidance.
func (s *Server) authorizeMsg(w http.ResponseWriter, r *http.Request, resource Resource, action Action, msg string) bool
```

Design notes:

- **The `slog.Warn` on denial is not incidental.** The defining property of this bug was
  that the skip was silent. Every denial now produces a structured log line naming the
  principal type — which is also the signal an operator needs to detect a Part 2 baseline
  that is too tight.
- Takes `r`, not `ctx`, so it can log the path and so a future audit hook has the request.
- Returns `bool` rather than an `error` to match the existing `checkBrokerDispatchAccess`
  and `authorizeProjectImport` shape.
- `Forbidden(w)` (`errors.go:190`, "Insufficient permissions") is the default. Several
  existing messages are actively misleading — `performAgentDelete` says *"Only the agent's
  creator can delete it"* when admins, ancestors, and project owners also pass — so the
  generic message is an improvement, not a regression. `authorizeMsg` covers the few cases
  where the text carries real guidance (e.g. the GCP SA assignment message).

**Non-goal:** do not convert the ~110 already-correct `CheckAccess` sites in this change.
Scope is the broken ones. The helper becomes the standard for new code via the lint rule.

### 4.2 `handlers_logs.go` — two different defects, only one of which is a conversion issue

Several handlers deliberately return `NotFound` rather than `Forbidden` for cross-project
access, to avoid disclosing resource existence (`getAgent:1438`, `handlers_logs.go:56`).
**Conversion must keep those checks and run them before `authorize`.**

An earlier revision of this section said the ordering is inverted throughout the file and every
conversion must reorder. **That is true of four of the seven sites and wrong about the other
three**, where there is nothing to reorder because there is no isolation check to move. Found by
aid-dev0. **Provenance, stated exactly** (see §8.1b — I got this wrong once already today): I read
all seven sites myself, initially from a checkout that was *not* this branch, and afterwards
confirmed `pkg/hub/handlers_logs.go` is **byte-identical between that tree and
`origin/scion/agent-id-fix` at `c2d12fac`**. The line numbers and the 4/3 split therefore hold on
both, which is consistent with this being present on `main` as well.

| Class | Sites | Handler | Defect | Work |
|---|---|---|---|---|
| **Inverted** | `:47` | `handleAgentLogs` | authz at `:47`, isolation at `:54` | reorder |
| | `:109` | `handleAgentCloudLogs` | same | reorder |
| | `:287` | `handleAgentMessageLogs` | same | reorder |
| | `:447` | `handleProjectMessageLogs` | same | reorder |
| **Absent** | `:193` | `handleAgentCloudLogsStream` | **no isolation check at all** | **add** |
| | `:364` | `handleAgentMessageLogsStream` | **no isolation check at all** | **add** |
| | `:522` | `handleProjectMessageLogsStream` | **no isolation check at all** | **add** |

For the inverted four, a naive in-place replacement would 403 an agent before the isolation check
can 404 it — an existence leak. For the absent three, an agent caller passes **neither**
authorization nor isolation today: an agent in any project can stream any agent's cloud logs, any
agent's message logs, and any project's message logs. **That is unauthorized access, not a
403-versus-404 disclosure**, it is new work that was not scoped, and it is present on `main` as
well as on `agent-id-fix`.

**The split is systematic, and that is the part worth carrying forward.** Every absent site is the
streaming twin of the query handler immediately above it in the same file, on the same resource,
with an otherwise identical prologue:

| Query handler (isolation present) | Streaming twin (isolation absent) |
|---|---|
| `handleAgentCloudLogs:88` | `handleAgentCloudLogsStream:166` |
| `handleAgentMessageLogs:267` | `handleAgentMessageLogsStream:338` |
| `handleProjectMessageLogs:427` | `handleProjectMessageLogsStream:496` |

The likely history is that each streaming handler was copied from its query handler and the check
was dropped in the copy. Note the split is 4/3 rather than 4/4 for a reason that supports this:
`handleAgentLogs:33` is the one inverted site with **no** streaming twin. **Where a check is
missing from one of a pair, look at the pair, not at the site** — a divergence is visible in a way
that an absence is not, which is the one lever §7.3 does not otherwise have against the
absent-check class.

#### Reachability — live, with a qualifier that must travel with the claim

A **normally minted agent token** reaches all seven: `UnifiedAuthMiddleware` validates agent tokens
with no path allowlist, and these routes impose no user requirement — in contrast to
`handlers_agents_core.go:1404` ("workspace"), which explicitly demands one. No fabricated
credential is required, so by the §8.2.1 heuristic this is **live, not latent**.

**But all three absent-check sites sit behind `s.logQueryService == nil → 501`** (`:172`, `:344`,
`:502`), and in each case that check runs *before* the authz block. So the bypass exists only where
Cloud Logging is configured. The unconditional site is `:47`, whose own `dispatcher == nil` gate is
at `:62` — *after* its authz block — but `:47` is an ordering defect, not a missing check.

Stated precisely, and this is sharper than "six of seven are gated": **every site in the
unauthorized-access class is deployment-conditional; the only unconditional site is in the
disclosure class.** aid-dev0 volunteered the 501 qualifier unprompted, at the cost of the more
dramatic version of their own finding.

### 4.3 Shared agent-caller gates

Three sites need more than a resource/action check because the legitimate agent flow through
them is scope-gated, not policy-gated (§5). Each gets a named helper rather than a
hand-written branch, modelled on the existing, correct `authorizeProjectImport`
(`handlers_resource_import.go:476`):

```go
// authorizeAgentCreate gates agent creation for every caller kind. Exhaustive
// and fail-closed. Replaces the caller-kind branch in createAgent, which had no
// else clause (sa-inv Q12), and supplies the gate createProjectAgent never had.
func (s *Server) authorizeAgentCreate(w http.ResponseWriter, r *http.Request, projectID string) bool
//   agent  -> HasScope(ScopeAgentCreate) && agentIdent.ProjectID() == projectID
//   user   -> CheckAccess(Resource{Type:"agent", ParentType:"project", ParentID:projectID}, ActionCreate)
//   other  -> deny

// authorizeAgentLifecycle gates start/stop/suspend/restart/message/exec.
func (s *Server) authorizeAgentLifecycle(w http.ResponseWriter, r *http.Request, agent *store.Agent) bool
//   agent  -> HasScope(ScopeAgentLifecycle) && agentIdent.ProjectID() == agent.ProjectID
//   user   -> CheckAccess(agentResource(agent), ActionAttach)
//   other  -> deny
```

`authorizeAgentCreate` is called from **both** `createAgent` and `createProjectAgent`,
which simultaneously closes the missing-`else` and the cross-project hole of §3.3 — one fix,
not two.

### 4.4 Conversion table

> **Anchors in this table are as-of `fa8c081`, pre-conversion, and are deliberately not refreshed.**
> This is a work-assignment table: it says what must be converted, so a stale-looking anchor is the
> point rather than a defect. aid-dev4 checked all 128 anchors in this document — none dangle, but
> **three rows here now resolve into a different function than they name, and two more into
> unrelated code inside the right function** as later commits shifted lines *(count corrected by
> aid-dev4 — the bolded "five" contradicted the body of its own sentence, in the fix to aid-dev4's
> own finding)*: `handlers_agents_core.go:1655` (`performAgentDelete`, really `:1746`),
> `handlers_agent_create_helpers.go:882` (`canDispatchToBroker`, really `:914`, pushed down by
> `brokerServesProject` at `:885`), `handlers_agents_core.go:1442` (`getAgent`, really `:1485`), and
> `handlers_projects_core.go:2098`/`:2271` (`updateProject`/`deleteProject`), which now land in
> unrelated code. **Match on the function name, not the line.**
>
> **Three rows are already done at `c2d12fac`** — `updateProject`, `deleteProject` and
> `createProjectAgent` all use the fail-closed helper. They stay listed for the reason §8.1a's rows
> stay listed: this table states intent, and a specification that deletes each row as it lands is a
> changelog.

**Group A1 — idiom 1, unguarded.**

| Site | Function | Conversion |
|---|---|---|
| `handlers_agents_core.go:1655` | `performAgentDelete` | `authorize(agentResource, ActionDelete)` |
| `handlers_agents_core.go:1468-1611` | `updateAgent` | **add** `authorize(agentResource, ActionUpdate)` — no check exists today; **plus the two SA checks, see §4.5** |
| `handlers_agents_core.go:409` | GCP passthrough | restructure — see note below |
| `handlers_agents_core.go:446` | SA assign | `authorizeMsg(gcpServiceAccountResource, ActionRead, …)` |
| `handlers_projects_core.go:2098` | `updateProject` | `authorize(projectResource, ActionUpdate)` |
| `handlers_projects_core.go:2271` | `deleteProject` | `authorize(projectResource, ActionDelete)` |
| `handlers_projects_core.go:1977` | lifecycle actions | `authorizeAgentLifecycle` (§4.3) |
| `handlers_projects_core.go:1720` | `createProjectAgent` | `authorizeAgentCreate` (§4.3) |
| `handlers_groups.go:321` | `updateGroup` | `authorize(groupResource, ActionUpdate)` |
| `handlers_groups.go:378` | `deleteGroup` | `authorize(groupResource, ActionDelete)` |
| `handlers_groups.go:482` | `addGroupMember` | `authorize(groupResource, ActionAddMember)` |
| `handlers_groups.go:518` | role-hierarchy guard | deny non-user callers outright |
| `handlers_groups.go:701` | `removeGroupMember` | `authorize(groupResource, ActionRemoveMember)` |
| `handlers_groups.go:685` | `getGroupMember` | **add** `authorize(groupResource, ActionRead)` (§3.5) |
| `handlers_runtime_brokers.go:342` | `updateRuntimeBroker` | `authorize(brokerResource, ActionUpdate)` |
| `handlers_runtime_brokers.go:387` | `deleteRuntimeBroker` | `authorize(brokerResource, ActionDelete)`; also move `actorID` assignment out of the guard so audit records a real actor |
| `handlers_runtime_brokers.go:434` | `checkBrokerDispatchAccess` | remove `userIdent == nil → return true` |
| `handlers_agent_create_helpers.go:882` | `canDispatchToBroker` | remove `userIdent == nil → return true` |
| `pty_handlers.go:93` | `handleAgentPTY` | `authorize(agentResource, ActionAttach)` |
| `handlers_logs.go:193` | `handleAgentCloudLogsStream` | `authorize(agentResource, ActionRead)` **plus ADD the isolation check — absent, not inverted (§4.2)** |
| `handlers_logs.go:364` | `handleAgentMessageLogsStream` | `authorize(agentResource, ActionRead)` **plus ADD the isolation check (§4.2)** |
| `handlers_logs.go:522` | `handleProjectMessageLogsStream` | `authorize(projectResource, ActionRead)` **plus ADD the isolation check (§4.2)** |
| `handlers_github_app.go:113` | `handleUpdateGitHubApp` | `s.requireAdmin(w, r)` — the helper already exists at `skill_registry_handlers.go:99`; promote it out of that file |

*Note on `:409` (GCP passthrough):* this is not a `CheckAccess` call. It is a hand-rolled
`userIdent.Role() != "admin" && broker.CreatedBy != userIdent.ID()` comparison, so no policy
can grant passthrough and the project-owner bypass does not apply. Converting it to
`authorize(brokerResource(broker), ActionDispatch)` is the semantically right move but is a
*behaviour change for users*, not just for agents. **Recommend the minimal fix here** — keep
the hand-rolled comparison, add an explicit non-user deny — and file the
policy-engine alignment separately, to keep this PR's user-visible surface at zero.

**Group A2 — idiom 2, unguarded (§3.2).** Four sites; each gets the
`else { Forbidden(w); return }` its sibling write path already has. Preferred form is to
convert the whole function to `authorize`, which removes the asymmetry as a class.

**Group B — idiom 1, compensated.** Convert to `authorize`, keeping the isolation check
first (§4.2). These are the sites that depend on Part 2; see §6.

| Site | Function | Resource / Action |
|---|---|---|
| `handlers_agents_core.go:1442` | `getAgent` | `agentResource` / `ActionRead` |
| `handlers_logs.go:47` | `handleAgentLogs` | `agentResource` / `ActionRead` |
| `handlers_logs.go:109` | `handleAgentCloudLogs` | `agentResource` / `ActionRead` |
| `handlers_logs.go:287` | `handleAgentMessageLogs` | `agentResource` / `ActionRead` |
| `handlers_logs.go:447` | `handleProjectMessageLogs` | `projectResource` / `ActionRead` |

---

### 4.5 The PATCH SA checks — ours, not svc-accnt's

Settled with sa-arch 2026-07-28 after two rounds, then extended to **four** pieces once (d) was
found. The `updateAgent` work divides:

| | Piece | Owner | Depends on | Status |
|---|---|---|---|---|
| (a) | `authorize(agentResource, ActionUpdate)` | this track | nothing | landed `d2414faf:1526` |
| (b) | `sa.ScopeID != projectID` and `!sa.Verified` on the PATCH path | **this track** | nothing | landed `31ddcaf`, verified by sa-arch from source |
| (d) | `authorizeMsg(gcpServiceAccountResource(sa), ActionRead)` at PATCH | **this track** | nothing | landing |
| (c) | `ActionRead` → `ActionAssign` + `CanActAs` at **both** sites | svc-accnt | Q14 — **still unruled** | blocked |

**(d) — found by sa-arch while verifying (b), which is the argument for verifying from source
rather than from announcements.** Create runs `GetGCPServiceAccount` (`:463`) → `sa.ScopeID`
(`:472`) → `sa.Verified` (`:476`) → `authorizeMsg(gcpServiceAccountResource(sa), ActionRead)`
(`:483`). PATCH runs the first three (`:1638`, `:1656`, `:1660`) and then assigns at `:1664`. **The
authz call is simply absent.**

sa-arch initially listed PATCH in their conversion table as an `ActionRead` → `ActionAssign` swap.
There is nothing to convert — **it is an insertion site, and that distinction is the whole
argument**: a conversion is found by grepping `ActionRead`; an insertion is found by nobody.

**Why (d) lands here rather than with (c),** despite being the same edit at the same site: it
needs nothing from svc-accnt (it mirrors `:483` literally, the identical reasoning as (b)), and
landing it now **converts PATCH from an invisible insertion site into a greppable conversion
site** — so svc-accnt's P3 pass finds it in the same sweep as everything else. Leaving it for P3
preserves exactly the invisibility that makes it dangerous.

**It is NOT a security fix, and must not be described as one.** aid-em's discipline, via aid-dev2,
and it is correct: the gate is already a **no-op for agents at create** by construction. The
`ScopeID` check runs first, so any SA reaching the authorize call is by definition in the caller's
own project, where the Part 2 read baseline grants agents read. An agent with `ScopeAgentCreate`
can assign any verified SA in its own project today, gate or no gate.

So the framing is **create/PATCH parity**. For users it does bite: someone who can update an agent
in project P but cannot read an SA in project P is now denied — the same delta create already
imposes, so this removes an inconsistency rather than adding a policy. The value is that the gate
is installed *while it is provably behaviour-neutral*, so it is already in place at the moment
Goal 2 makes the `ScopeID` equality scope-aware and the gate becomes the only thing standing
there.

**Why (b) is not svc-accnt's.** The natural-looking seam is authz-vs-IAM, which would put
anything SA-shaped on svc-accnt's side. That is a *conceptual* boundary, not a *dependency*
one. The decisive test is "does it need anything svc-accnt has to build first," and (b) needs
nothing: no GCP API call, no toggle, no Q14. It reads two local store fields.

**Why it cannot wait.** `updateAgent` today calls `GetGCPServiceAccount` at `:1585` and assigns
straight through at `:1590` — no scope check, no `Verified` check. Create has both, at `:435`
and `:439`. So (a) alone leaves PATCH accepting an SA from another project or an unverified one;
it merely requires update rights on the agent, **which the agent's creator has by definition**.
"Create with no SA, then PATCH the SA in" survives (a) and dies to (a)+(b). Deferring (b) leaves
the create-side hardening defeatable for the entire window — and that window is now downstream
of Q14/Q15/Q16, which are still open with ptone.

This is not the §596 regression-window hazard; nothing is being removed. It is the different
and quieter cost of shipping a gate whose bypass is left open beside it.

#### Implementation constraint from svc-accnt — mirror `:435` literally

sa-arch's request, and it should be honoured to the letter:

> Whoever implements (b), mirror `:435` **literally** — same shape, same error string — so the
> later conversion is a two-site grep rather than a hunt.

The reason is specific. Q5 locked as option A (real hub-scoped SAs, pickable in any project), so
svc-accnt Goal 2 must turn `sa.ScopeID != projectID` from an equality test into a scope-aware
check that unions in hub-scoped SAs. After (b) there will be **two** sites carrying that
requirement instead of one. If P4 converts `:435` and misses the PATCH twin, hub-scoped SAs will
work at create and be rejected at PATCH — silently, and presenting as a Goal 2 bug rather than a
missed conversion.

So: **do not improve the wording, do not refactor the two checks into a helper, do not adjust
the error string.** A near-duplicate that greps identically is worth more here than a tidier
version that does not. sa-arch is adding the PATCH site to SC-2's table so P4 inherits both.

`!sa.Verified` is unaffected by Goal 2 and survives unchanged.

**Search on the invariant fragment, not the full expression.** sa-arch's refinement, and it is
necessary: the PATCH site must read `sa.ScopeID != agent.ProjectID` because `updateAgent` has no
`projectID` local, so the two sites differ by exactly one token. A P4 developer grepping the
whole `:435` condition would find one and miss the other — precisely the silent half-conversion
"mirror literally" exists to prevent. The correct key is **`sa.ScopeID !=`**.

**And that key shows the conversion is a six-site job, not two.** Running it across `pkg/`:

| Site | Predicate | On mismatch |
|---|---|---|
| `handlers_gcp_identity.go:308` | `sa.ScopeID != projectID` | `NotFound` |
| `handlers_gcp_identity.go:333` | `sa.ScopeID != projectID` | `NotFound` |
| `handlers_gcp_identity.go:380` | `sa.ScopeID != projectID` | `NotFound` |
| `handlers_agents_core.go:435` | `sa.ScopeID != projectID` | `ValidationError` |
| `updateAgent` PATCH (new, §4.5) | `sa.ScopeID != agent.ProjectID` | `ValidationError` |

| `lifecyclehooks/validate.go:433` | `sa.Scope != "project" \|\| sa.ScopeID != hook.ScopeID` | `FieldError` |

**Correction to my own first pass:** I originally reported five and described `validate.go:433` as
escaping the key because of its different shape. Wrong — it was hit #1 in my own grep output. The
expression *contains* `sa.ScopeID != hook.ScopeID` and matches cleanly. Six, not five. It still
needs judgement rather than mechanical conversion, because it compares an SA's scope against a
**hook's** scope rather than a request's project.

Related, from sa-arch: `validate.go:425` (`sa.Scope != "hub"`) is the hub-scoped-hook branch,
which is effectively unreachable today because hub-scoped SAs do not exist. Goal 2 activates it
for the first time.

**One false positive, and converting it would be a regression.** On `svc-accnt-lead`,
`capabilities.go:171` matches the key — because the *already-correct* conditional fix there reads
`sa.ScopeID != ""`. A developer working the checklist mechanically would "convert" the one site
that is already right and undo a landed fix. Marked DO-NOT-TOUCH in svc-accnt's table.

That generalises, and it is the sharpest thing to come out of this exchange: **a key with no
obvious noise is more dangerous than one with some**, because precision is what invites
mechanical application and nobody re-reads clean hits. A high-precision checklist must list its
**exclusions** as explicitly as its inclusions.

The three `handlers_gcp_identity.go` sites were not on either track's list. They return `NotFound`
rather than `ValidationError` — deliberate 404-not-403 disclosure posture on read/manage paths, so
the *response* differs, but the *scope predicate* is identical and Goal 2 must convert all of them.
Broadening the grep key from a whole-expression match to `sa.ScopeID !=` is what surfaced them.
Noting for svc-accnt rather than claiming them: they are outside this track's scope.

#### Why this section is spelled out rather than assumed

Both tracks had independently written down that **the other one** was doing (b). This document's
§10 handoff table said svc-accnt would add the scope-match and `Verified` validation; svc-accnt's
P3 surface table said *"Track S fixes the authz; svc-accnt adds the IAM gate and validation
parity."* Two documents, mirrored stale claims, pointing opposite ways.

Neither was wrong when written, and neither would have been caught by reading either document
carefully — each was internally consistent. Only the cross-check found it. Had both sides stayed
internally consistent and not compared, the gap would have shipped, with each track assuming the
other held it.

That is the argument for stating ownership of (b) explicitly here and in svc-accnt's plan, rather
than letting it follow from the authz/IAM seam. The general form is banked as svc-accnt's SC-3:
**split on dependency boundaries, not conceptual ones.**


## 5. Part 2 — the agent authorization baseline

### 5.1 The state after Part 1

`checkAccessForAgent` (`authz.go:181-217`) allows via exactly three routes:

1. **Ancestor bypass** — `canAccessAsAncestor(agent.ID(), resource.Ancestry)`. Only
   `agentResource()` populates `Ancestry`, so this covers an agent acting on its
   **descendants**. Note it does **not** cover self-access: an agent's own ancestry is
   `[root_user, …, parent]` and does not contain itself.
2. **Policy match** on principals `{agent:<id>}` ∪ effective groups.
3. **Delegation fallback**.

Effective groups for an agent (`group_store.go:819-885`) are: explicit memberships, plus the
implicit `project:<slug>:agents` group resolved at query time from the agent's project, plus
transitive parents. The implicit group needs no membership plumbing — **it is already
correct and already wired**.

But **nothing is bound to it.** The only two automatic policy bindings are `seed.go:98`
(`hub-member-create-projects` → hub-members, users) and `handlers_projects_core.go:639`
(`project:<slug>:member-create-agents` → project members group, users). So after Part 1,
agents are denied nearly everywhere.

### 5.2 The reframing: the agent model already exists

The brief's three options all assume the answer must be expressed in the policy engine. It
does not have to be, because **Scion already has a coherent agent-authorization model that
is not the policy engine**: JWT token scope + project isolation.

It is implemented correctly, exhaustively, and fail-closed in three places:

| Site | Agent branch |
|---|---|
| `createAgent` (`handlers_agents_core.go:323-343`) | `HasScope(ScopeAgentCreate)` + `req.ProjectID == agentIdent.ProjectID()` |
| `authorizeProjectImport` (`handlers_resource_import.go:477-487`) | `HasScope(ScopeAgentCreate)` + project match |
| `handleProjectBroadcast` (`handlers_agent_messaging.go:395-406`) | `HasScope(ScopeAgentLifecycle)` + project match |

The scopes are declared and documented for precisely this purpose (`agenttoken.go:44-58`):

```go
ScopeAgentCreate    = "project:agent:create"     // create sub-agents within the same project
ScopeAgentLifecycle = "project:agent:lifecycle"  // start/stop/restart agents within the same project
ScopeAgentNotify    = "project:agent:notify"
ScopeProjectSecretRead = "project:secret:read"
```

They are granted deliberately, per-agent, from the agent's template
(`hub_access.scopes` → `AppliedConfig.HubAccessScopes`,
`handlers_agent_create_helpers.go:175-177`) — i.e. they are already an *administered*
capability grant, not an ambient one.

**So the bug is not only "wrong getter." It is that ~24 sites have no agent model at all** —
a user model plus an implicit fall-through. Part 2's job is to apply the model that exists
uniformly, not to design a new one.

### 5.3 Evaluation of the brief's options

#### Narrated comparison (requested by ptone)

**Setup.** Project `acme-web` (P1), owned by alice, containing agents `builder` and `tester`
(both created by alice) and `builder-kid` (created by `builder`). Separate project
`acme-api` (P2) contains `api-worker`.

**Actor throughout: `builder`**, presenting its agent JWT.
ProjectID = P1, Ancestry = `[alice]`, Scopes = `[agent:status:update, agent:log:append,
project:agent:create]`. Note it does **not** hold `project:agent:lifecycle` — that lets the
table show what the scope gate actually gates.

| # | `builder` requests | Today | A | B (rec.) | C |
|---|---|---|---|---|---|
| 1 | `GET /agents/builder` — read self | ✅ | ✅ | ✅ | ✅ |
| 2 | `GET /agents/tester` — read sibling | ✅ | ✅ | ✅ | ✅ |
| 3 | `GET /agents/tester/logs` | ✅ | ✅ | ✅ | ✅ |
| 4 | `GET /agents/builder-kid` — read own child | ✅ | ✅ | ✅ | ✅ |
| 5 | `GET /projects/P1/message-logs` | ✅ | ⚠️ see below | ✅ | ✅ |
| 6 | `DELETE /agents/builder-kid` — delete own child | ✅ | ✅ | ✅ | ✅ |
| 7 | `DELETE /agents/tester` — delete sibling | ✅ | ❌ | ❌ | **✅** |
| 8 | `POST /agents/tester/pty` — shell into sibling | ✅ | ❌ | ❌ | **✅** |
| 9 | `POST /agents/tester/restart` — no lifecycle scope | ✅ | ❌ | ❌ | **✅** |
| 10 | `POST /projects/P1/agents` — create in own project | ✅ | ✅ | ✅ | ✅ |
| 11 | `POST /projects/P2/agents` — create cross-project | **✅** | ❌ | ❌ | ❌ |
| 12 | `GET /agents/api-worker` — read cross-project | 404 | 404 | 404 | 404 |
| 13 | `PATCH /projects/P1` — rename own project | ✅ | ❌ | ❌ | **✅** |
| 14 | `DELETE /projects/P1` — delete own project | ✅ | ❌ | ❌ | **✅** |
| 15 | `PUT /github-app` — overwrite hub private key | **✅** | ❌ | ❌ | ❌ |

Rows 1–4 and 6 are the legitimate traffic we must not break. Rows 11 and 15 are the
headline bugs. Rows 7–9 and 13–14 are where the options actually differ.

**The decisive observation: A and B produce identical outcomes on every row except 5 — and
on row 5, A is worse.**

Row 5 in detail. `handleProjectMessageLogs` checks `projectResource(project)` /
`ActionRead`. To let an agent read its own project's logs:

- **Under B**, the engine computes `projectIDForResource(resource)` → `P1` and compares it
  to `agent.ProjectID()` → `P1`. The comparison is made from the agent's own token claim, so
  it cannot be misconfigured.
- **Under A**, you seed a policy with `ResourceType: "project"`, `ScopeType: "project"`,
  `ScopeID: P1`, bound to `project:acme-web:agents`. But `matchesResource`
  (`authz.go:369-374`) only enforces project scoping when `resource.ParentType == "project"`,
  and `projectResource()` (`capabilities.go:70-77`) sets no `ParentType`. So the scope check
  is skipped and **the policy matches project P2 as well**. `builder` is granted
  project-read on every project in the hub.

  Today two isolation checks happen to mask this. The trap is that Option A's whole selling
  point is that policy now expresses the isolation — which invites removing those redundant
  checks, at which point the grant becomes live. The defect is quiet exactly until someone
  acts on the design's own logic.

So Option A costs a backfill migration over every existing project, plus an engine fix to
`matchesResource` that changes evaluation for user-authored policies too — in order to arrive
where Option B arrives with neither.

**What A genuinely buys** and B does not: per-project tunability. An admin could widen or
narrow one project's agent baseline by editing that project's policy row. B is uniform
hub-wide, adjustable only by binding an explicit deny. Given ptone's Q3 framing — *"establish
the clear control points, we can adjust policy detail later"* — that flexibility is the thing
we are deliberately deferring, and §5.4 keeps the door open at zero migration cost.

**Option C** differs on rows 7, 8, 9, 13 and 14. Note what row 14 means concretely: an agent
could delete the project it lives in. And row 8 is the cross-project PTY vector, merely
narrowed to same-project rather than closed. C re-grants most of the severity table.

---

**Option A — seed a policy bound to `project:<slug>:agents`.** Rejected for now, on three
counts:

1. **It needs a backfill migration.** Seeding at project creation
   (`handlers_projects_core.go:441`) only covers new projects. Every existing project needs
   the policy created retroactively, inside the same PR as a 24-site security conversion.
2. **It steps directly into a latent policy-engine defect.** `matchesResource`
   (`authz.go:369-374`) enforces project scoping only when `resource.ParentType == "project"`:
   ```go
   case "project":
       if policy.ScopeID != "" && resource.ParentType == "project" && resource.ParentID != policy.ScopeID {
           return false
       }
   ```
   `projectResource()` (`capabilities.go:70-77`) sets **no** `ParentType`. So a
   project-scoped policy with `ResourceType: "project"` matches **every** project. A seeded
   policy granting agents `project`/`read` in project P would grant it in every project —
   reintroducing a cross-project read while fixing one. The defect is latent today only
   because nothing seeds such a policy. Option A requires fixing `matchesResource` first,
   which changes evaluation for existing user-authored policies too.
3. **Once corrected for the Group B sites, it is functionally identical to Option B** — the
   same grant, expressed with a migration, N per-project policy rows, and an engine change.

**Option B — project isolation sufficient for read-class actions.** Recommended, with the
refinement below.

**Option C — project-scoped bypass mirroring the user path.** Rejected. It would grant an
agent project-owner-equivalent authority: delete its own project, mutate any sibling agent,
add itself as an owner-role group member. That is most of the severity table, re-granted.

### 5.4 Recommendation

**Two mechanisms, no seeded policy, no migration.**

**(1) A read-class project bypass in `checkAccessForAgent`,** inserted *after* policy
evaluation:

```go
func (a *AuthzService) checkAccessForAgent(...) Decision {
    // 0. ancestor bypass                                    [unchanged]
    // 1. build principals + effective groups                [unchanged]
    // 2. decision := a.evaluatePolicies(...)                [unchanged]
    //    if decision.PolicyID != "" { return decision }     [unchanged]

    // 3. NEW — project-scoped read baseline.
    //    An agent may perform read-class actions on resources in its own project.
    //    This codifies the project-isolation gate that these paths already relied
    //    on before ptone/scion#591; it grants nothing that was not already reachable.
    if isReadClassAction(action) {
        if pid := projectIDForResource(resource); pid != "" && pid == agent.ProjectID() {
            return Decision{Allowed: true, Reason: "agent project read baseline", Scope: "project"}
        }
    }

    // 4. delegation fallback                                [unchanged]
}

func isReadClassAction(a Action) bool { return a == ActionRead || a == ActionList }
```

Four properties that make this the right shape:

- **Position after step 2 makes it revocable.** `evaluatePolicies` returns any matched
  policy — allow *or* deny — and step 2 returns it before the baseline is reached. So an
  admin can bind an explicit `deny` policy to `project:<slug>:agents` and it will win. This
  is strictly better than the user path, whose bypasses precede policy evaluation and
  therefore cannot be overridden at all.
- **`pid != ""` is load-bearing.** Resources with no project — `brokerResource`,
  `templateResource`, the GitHub App config, hub-scoped resources — yield `""` from
  `projectIDForResource` and get no bypass. Omitting that guard would make empty match empty
  and allow everything. This must be an explicit test.
- **Read-class is `ActionRead` and `ActionList` only.** Deliberately **not** `ActionAttach`:
  PTY, exec, and message mutate a running agent and include the cross-project PTY vector.
  Not `ActionCreate`: handled by scope (§4.3). Not any mutation.
- **It grants nothing new.** Every Group B path already permitted exactly this via its
  project-isolation check. This is a consolidation of six copy-pasted checks into one
  enforced location, not a new grant.

**(2) Scope-gated helpers for the two legitimate mutating agent flows** — `ScopeAgentCreate`
for creation, `ScopeAgentLifecycle` for lifecycle actions — per §4.3.

**Everything else stays default-deny.** No policy is seeded. `project:<slug>:agents` remains
the admin extension hook: an operator wanting to grant agents more binds a policy to it
through the existing policy API, and Option A remains available later with no migration and
no engine change. We are choosing *not to spend the option now*, not closing it.

### 5.5 What agents lose

Intentional denials after this change — all of them the point of the fix:

- Delete, update, or PTY into any agent outside their own project (and, within their own
  project, any of these on a non-descendant without an explicit policy).
- Update or delete any project, group, or runtime broker.
- Add group members, including owner-role members.
- Overwrite the hub GitHub App private key and webhook secret.
- Assign a GCP service account they have no read access to.
- Create agents in a project other than their own.

Retained: read/list within their own project; full access to their own descendants via the
ancestry bypass; create and lifecycle within their own project *when the corresponding token
scope was granted by their template*.

The one genuinely debatable retention is lifecycle-on-peers — **Q3**.

---

## 6. Group B site-by-site: how each passes after conversion

| Site | Resource / Action | Passes via |
|---|---|---|
| `handlers_agents_core.go:1442` `getAgent` | `agentResource` / `ActionRead` | Read baseline — `projectIDForResource` → `a.ProjectID`, matches. Covers self-read (**not** covered by the ancestry bypass, since an agent is not in its own ancestry) and sibling-read. Cross-project still 404s at the isolation check, which runs first. |
| `handlers_logs.go:47` `handleAgentLogs` | `agentResource` / `ActionRead` | Read baseline. **Requires the reorder of §4.2.** |
| `handlers_logs.go:109` `handleAgentCloudLogs` | `agentResource` / `ActionRead` | Read baseline. Requires reorder. |
| `handlers_logs.go:287` `handleAgentMessageLogs` | `agentResource` / `ActionRead` | Read baseline. Requires reorder. |
| `handlers_logs.go:447` `handleProjectMessageLogs` | `projectResource` / `ActionRead` | Read baseline — `projectIDForResource` returns `r.ID` for `Type=="project"`, matches the agent's project. |
| `handlers_projects_core.go:1720` `createProjectAgent` | n/a | **Not the read baseline.** `authorizeAgentCreate` — `ScopeAgentCreate` + project match. This is a *tightening*: the site has no gate today. Agents whose template does not grant `project:agent:create` and which were relying on this route will now be denied — correctly, since the same caller is already denied on `POST /api/v1/agents`. |

The three Group A log-stream sites (`:193`, `:364`, `:522`) are the streaming mirrors of
`:109`, `:287`, `:447` and pass the same way — with the difference that they gain the
isolation check they never had.

---

## 7. Lint rule

### 7.1 What it must detect

Not "uses `GetUserIdentityFromContext`" — that fires on ~25 legitimate attribution sites
(§3.4) and will be suppressed within a week. The rule must detect **an authorization check
inside an identity-kind guard that has no `else`**, in three syntactic forms:

```
if <ident> := GetUserIdentityFromContext(ctx); <ident> != nil { … CheckAccess … }   // no else
if <ident>, ok := <x>.(UserIdentity); ok           { … CheckAccess … }              // no else
<ident> := GetUserIdentityFromContext(ctx); if <ident> == nil { return true }       // fail-open
```

The discriminator for the first two is the presence of `authzService.CheckAccess` (or
`requireAdmin`, or a hand-rolled `Role() != "admin"` comparison) **inside** the guarded block.

The third form was added during implementation and is not in the original inventory's shape
list. It is the same bug written inside-out — an early return of `true` on the non-user
caller — and it has no benign reading. Without it, the two §4.4 sites
`checkBrokerDispatchAccess` (`handlers_runtime_brokers.go:435`) and `canDispatchToBroker`
(`handlers_agent_create_helpers.go:883`) get no regression coverage at all.

**Test case.** An earlier draft named `handlers_github_app.go:113` vs `:121` as the canonical
example — "same file, adjacent lines, opposite verdicts." That is factually wrong; see the
correction in §3.4. `:113` is a function declaration with no guard, so there is no pair.

The rule is instead verified by `./hack/check-authz-guards.sh --self-test`, which runs the
classifier over an embedded fixture covering each shape positively and negatively, with the
`:121` attribution shape as the explicit negative twin of the gate shape. Testing the
classifier directly is strictly better than pinning it to one file's current contents: the
fixture cannot silently stop being a test case when someone edits a handler.

### 7.2 Mechanism

**Follow the repo's existing precedent: a ripgrep + allowlist script in `hack/`,** modelled
on `hack/check-project-compat-literals.sh`, wired into `make ci`.

Rationale — I checked the alternatives. *(Not a verification label: this reports consideration of design options, not observation of the tree. Distinction owed to aid-dev4, who declined to pad their own count with it.)*

- **There is no `.golangci.yml` in the repo.** golangci-lint runs with built-in defaults
  only, so `forbidigo`/`depguard` would mean introducing a config file, which would also
  surface a wave of pre-existing default-linter findings.
- **Decisively: the golangci-lint CI job uses `only-new-issues: true`
  (`.github/workflows/ci.yml:98`) and the Makefile target uses `--new-from-rev=main`.** A
  golangci-lint-based rule would not flag a single existing violation. For a regression
  guard on a security fix, retroactive coverage is the whole point.
- A true `go/analysis` analyzer is the highest-fidelity option but is entirely greenfield
  here: no `tools/`, no `x/tools` in `go.mod`, no analyzer plumbing, plus a custom-plugin or
  `unitchecker` build. Not justified for a pattern that is reliably detectable lexically.

Concretely: `hack/check-authz-guards.sh` — multiline `rg` for the three guard shapes (§7.1) with
`CheckAccess` in the body, an anchored `allowed_paths` allowlist carrying an intent comment
per entry, `exit 0` with a warning if `rg` is absent (CI installs it at
`.github/workflows/ci.yml:67-71`), non-zero exit on unlisted hits. Add a `.PHONY` target
beside `compat-literals` (`Makefile:70-72`) and register it in the `ci` and `ci-full` lists.

**The allowlist should be empty on merge.** That is the acceptance criterion for Part 1.

#### The script exists and runs, but is deliberately NOT wired into `make ci` yet

As of `180debe` the script is built, self-tests, and reports **twenty remaining findings** — so
wiring it in today would leave `make ci` red. The condition aid-em and aid-dev4 agreed is that it
is registered in the `ci` target **when it reports zero**, not when the remaining sites are judged
to be covered. That distinction was aid-dev4's, raised when the trigger was about to be pulled on a
site-list judgement with nineteen findings outstanding, and it is the right one: a judgement that
the survivors are acceptable is exactly the reasoning that produced ptone/scion#591.

Two consequences a reviewer should hold onto:

- **Detection and enforcement are separate milestones.** Between now and the wiring commit, every
  finding is only as good as someone reading the output. A site can be correctly detected, named in
  the output, and still ship unfixed — see §8.1a, where precisely that happened.
- **Risk: the zero-condition can deadlock.** If any surviving site is ever ruled legitimately
  fail-open, the count never reaches zero and the rule never enters CI — the guard would be
  permanently one exception away from existing. A trigger that can block itself is not yet a
  trigger. The allowlist is the release valve; the ruling below is what keeps it from becoming
  something worse.

##### Ruling — when an allowlist entry counts as reaching zero

Reaching zero **via** an allowlist entry counts as reaching zero, on two conditions (aid-em):

1. The entry records a site that is **fail-open by design** — the behaviour is intended, and the
   entry documents a decision that was made.
2. The entry carries an intent comment **and a test pinning the fail-open as intended**.

Condition 2 is what makes the valve safe. **Without a test, "allowlisted" and "unfixed" are
indistinguishable in the tree**: both present as a site the script does not flag, and a reader
cannot tell a considered exception from abandoned work. That is the thesis of this whole PR turned
on our own tooling — an absence with no signal attached is what let ptone/scion#591 survive. An allowlist entry
with a test is a decision; an allowlist entry without one is ptone/scion#591 with paperwork.

**What the valve must not be used for:** reaching a green build with conversion work outstanding.
"This site is deliberately open" and "we ran out of time" look identical in an allowlist file and
are completely different facts. If the conversion work is unfinished, the correct outcome is that
the script ships **not wired into `make ci`**, with the PR saying plainly that it is not wired and
why — not allowlisting the survivors and claiming zero. Conflating the two turns the release valve
into a laundering mechanism, which is strictly worse than an unwired script: an unwired script is
honestly unfinished, a laundered one is dishonestly finished.

Preference order, worst to best: **laundered green** → **unwired script** → **genuinely zero** →
**zero with a tested, justified exception**.

**For this PR the allowlist stays empty.** The valve is documented so the trigger cannot deadlock
later, not so it can be used now. This ruling also appears in the PR body under the lint-guard
section, because the person most likely to reach for the valve is a future contributor reading the
PR that introduced the script rather than this document.

### 7.2a Two verified evasions of shape 3, and one of them points at our own fix

Tested against `1e3e3628` in a scratch repository with three hand-written fail-open variants — run,
not reasoned. **One of three was flagged.** Both misses are in shape 3, the standalone-assignment
form, and both are cheap to close.

| Variant | Flagged? |
|---|---|
| `if x == nil {` / comment on its own line / `return true` | **yes** |
| `if x == nil {` / `return true // broker-to-broker` | **no** |
| `identity := GetIdentityFromContext(ctx)` / `if identity == nil {` / `return true` | **no** |

**Evasion 1 — a trailing comment.** The verdict test is anchored,
`/^[[:space:]]*return[[:space:]]+true[[:space:]]*$/`, so `return true // …` does not match.
`handlers_runtime_brokers.go:435` is detected **only because its comment happens to sit on its own
line**. Reflowing that comment inline would silently un-detect the single site the third shape was
written for.

**Evasion 2 — the wrong getter, and this is the serious one.** The classifier knows only
`GetUserIdentityFromContext`; `GetIdentityFromContext` appears **zero** times in the script.
`canDispatchToBroker:914` now uses `GetIdentityFromContext`, deliberately — the `User` variant
returns a literal nil interface that panics `CheckAccess` — and dev3's twin is instructed to match
it structurally, so it will use the same getter.

**Consequence: once both dispatch fixes land, the script cannot detect a regression in either of
the two functions it exists to protect.** The rule covers the shape the *bug* had, not the shape the
*fix* has. This is the §8.1a drift theme aimed at our own tooling: a regression barrier built from
the pre-fix code is pointed at the past.

It also qualifies the headline number. **"Twenty findings" is a count of what this classifier can
see**, and it currently cannot see the getter our own fix uses — so **twenty is a floor, not a
total**, until both evasions are closed. That is a live caveat on §7.2's zero-condition, because a
zero-condition is only as strong as the detector's recall. See §1.1: this is the thesis instance
that would have shipped as the fix.

**Both evasions measured** (aid-em, scratch worktree at `1e3e3628`, independently of my scratch
repo): reflowing the comment inline takes the count **20 → 19**; swapping the getter with the
fail-open otherwise untouched takes it **20 → 19**. The script contains **zero** occurrences of
`GetIdentityFromContext` against seventeen of `GetUserIdentityFromContext`.

**The fix is free, and this was measured rather than assumed.** Widening the classifier to
`Get(User)?IdentityFromContext` at the two match sites yields **exactly 20** — no false positive on
our own fix, because `canDispatchToBroker`'s nil branch is a **deny** and shape 3 requires an
unconditional `return true`. **The correct fix stays invisible by construction while the fail-open
version is caught**, which is the property the widening had to preserve. Re-run against the
evasion-2 tree, `:435` flags again.

**Mutation-testing note (aid-em → dev4).** The existing mutation test was real but exercised only
the *verdict*. The match has at least three independent degrees of freedom — **getter, verdict,
comment placement** — and mutating one proves only that one is load-bearing. Each must be mutated
separately, or the fixture certifies a recall it does not have. That is §8.1b's rule arriving in
test design: a view of the coverage is not the coverage.

### 7.3 What it cannot detect, and why that must be said out loud

The rule keys on the **shape of a guard**. It therefore cannot detect **the absence of any
check at all** — absence has no syntax to match. This is not a gap to be closed by a better
regex; it is the boundary of the technique.

Four rows in the §4.4 conversion table are exactly that class. They are "add a check that does
not exist today," they have no guard to key on, and **they will never appear in this script's
output**:

| Site | Function | What is missing |
|---|---|---|
| `handlers_agents_core.go:1468-1611` | `updateAgent` | no authorization |
| `handlers_projects_core.go:1720` | `createProjectAgent` | no gate at all |
| `handlers_groups.go:685` | `getGroupMember` | no authorization (§3.5) |
| `handlers_github_app.go:113` | `handleUpdateGitHubApp` | no authorization (§3.4) |

**If an implementer skips one of these four, the build stays green.** They must be confirmed by
a reviewer against this list, not by CI. Note that the last one is the single most sensitive
endpoint in the change — it writes the hub's GitHub App private key.

Two things follow, and both belong in the PR description rather than in tribal memory:

- The guard is a **regression barrier against reintroducing the idiom**, not a completeness
  check on authorization coverage. Describing it as the latter would be overselling it, and
  would invite exactly the false confidence that let ptone/scion#591 persist.
- Coverage of the absent-check class needs a route manifest that asserts every registered
  route reaches an authorization decision — filed as **ptone/scion#598**, out of scope here.

---

## 8. Secondary findings

### 8.1 Broker-header short-circuit — IN SCOPE (Q4 resolved: fail closed)

`UnifiedAuthMiddleware` (`auth.go:143-152`) short-circuits on the mere presence of
`X-Scion-Broker-ID`, setting only an auth-type *label* — never an identity — and calling
`next.ServeHTTP`. If `brokerAuthService` is nil, `BrokerAuthMiddleware` is never installed
(`server.go:2875`), and a bare `curl -H "X-Scion-Broker-ID: anything"` reaches handlers
fully unauthenticated.

**Exploitability, verified at `c2d12fac`:** `Enabled: true` is hardcoded in
**`DefaultBrokerAuthConfig` (`pkg/hub/brokerauth.go:59`)** and there is **no settings key, env var,
or CLI flag that can set it false.** So it is unreachable in any shipped build.

⚠️ *Anchor correction (aid-dev4).* This previously cited `server.go:251` and
`cmd/server_foreground.go:1292` as "both construction sites." **Both of those lines read
`BrokerAuthConfig: DefaultBrokerAuthConfig()`** — they are *call* sites. The literal lives one
indirection away and appears **once, not twice**. The conclusion was right and the evidence was not
where the document said it was. **That distinction is only visible to someone who follows the
anchor** — a reader who does and finds nothing discounts the claim and the document with it, which
is a worse outcome than a wrong conclusion honestly anchored.

*Related trap, found while confirming this:* there are **two** functions named
`DefaultBrokerAuthConfig` — `pkg/hub/brokerauth.go:57` sets `Enabled: true`, and
`pkg/runtimebroker/brokerauth.go:43` sets `Enabled: false` **and `AllowUnauthenticated: true`**.
Same name, opposite defaults, distinguishable only by package. Anyone grepping the name to check
this claim can land on the wrong one and reach the opposite conclusion.

**Found independently by me and by aid-dev4; aid-dev4 then applied this document's own latency test
to it and corrected my framing, which had it as a hole.** It is not. `NewBrokerAuthMiddleware` in
`pkg/runtimebroker` has **zero non-test callers**, the permissive `DefaultBrokerAuthConfig`'s only
caller is `brokerauth_test.go:406`, and **both production wirings — `pkg/hub/server.go:251` and
`cmd/server_foreground.go:1292` — take the hub one, which fails closed.**

**Therefore: not fixed in this PR. Named in the PR body and filed as a follow-up, explicitly as
latent.** The §8.2.1 heuristic applies unchanged — exploiting it requires a wiring that does not
exist — and the reason to be strict about that here is §8.1b's: **overstating this one costs the
accurate claims standing next to it.** State it as *two same-named constructors with opposite
security defaults, the permissive one currently unreachable in production* — a trap for whoever
wires the runtime broker next, which is the ptone/scion#591 shape (an absence that reads as a presence) rather
than an exploit today.

> Worth noting how the correction arrived: I wrote the tell, and somebody else applied it to my own
> finding. That is what the heuristic is *for*, and it only works because it is written down in a
> form someone other than its author can run.

### 8.1a Broker dispatch — the shared `brokerServesProject` helper and the twin

**§4.4's two dispatch rows stand** — they state the intended end state, which is what a design
table is for. This section records *how* that end state is being reached, and is the governing
detail where the two disagree. aid-em's ruling doc (`em-notes/broker-dispatch-ruling.md`) is the
implementation spec.

**The ruling doc itself carried a defect, now fixed in code.** Its shape specified the agent arm
as `agent.ProjectID() == <broker's project>` — **not implementable.** Brokers link to projects
**many-to-many** through `store.ProjectProvider`, and neither dispatch function receives a project
ID. This is the same three-state finding as ptone/scion#604, reached independently from the dispatch side.

aid-dev2 resolved it at `89bc2039` with a shared helper
(`handlers_agent_create_helpers.go:885`):

```go
func (s *Server) brokerServesProject(ctx context.Context, brokerID, projectID string) bool {
	if brokerID == "" || projectID == "" {
		return false
	}
	provider, err := s.store.GetProjectProvider(ctx, projectID, brokerID)
	return err == nil && provider != nil
}
```

It is shared **specifically so the two dispatch functions cannot drift** — that anti-drift
property is the reason it is a helper rather than an inlined query, and it is the
extract-the-helper side of the discriminator in §8.1b.

#### ⚠️ At `180debe` the helper is landed but the twin is not — half-applied, and the comment does not say so

Verified against `origin/scion/agent-id-fix` at **`c2d12fac`**, re-checked after the provenance
error recorded in §8.1b:

- **`checkBrokerDispatchAccess` (`handlers_runtime_brokers.go:433-437`) still fails open** — the
  §7.1 third guard shape, `if userIdent == nil { return true }`, unchanged.
- **`canDispatchToBroker` (`handlers_agent_create_helpers.go:914-921`) now fails closed**, and its
  comment records that the branch was *inverted rather than deleted* because
  `GetIdentityFromContext` returns a literal nil interface that would panic `CheckAccess` on
  `identity.Type()`. That is the correct call and the reason deserves to survive.
- **`brokerServesProject` has exactly one caller** (`:939`, in the file that defines it). The
  helper's own doc comment (`:877`) names `checkBrokerDispatchAccess` as a caller. It is not one.

So the two functions **have** drifted, transiently, in precisely the direction the helper exists to
prevent: `canDispatchToBroker` fails closed, its twin still admits unknown callers.

**Tracked, not missed.** The twin is one of twenty findings outstanding at `180debe`, all assigned
to aid-dev3's Phase 2b, with the broker twin explicitly ordered **first** because it is the one site
whose correctness depends on matching another developer's already-landed code. The lint rule
detected it by name before anyone went looking — the §7.1 third shape exists for exactly this site.
It is not in §7.3's cannot-detect list and that list is not incomplete: §7.3 is for sites with **no
guard to key on**, and this site has a guard, merely an inverted one. The build is green only
because the script is not wired into `make ci` yet (§7.2) — detection worked; enforcement is a
later milestone.

**The durable finding is the comment, not the unlanded twin.** The twin lands and the drift closes.
What would have survived is a site the codebase *asserts* is protected: a reviewer who greps
`brokerServesProject`, reads the comment, and ticks off both functions has been misled by something
we shipped — an absence that reads as a presence, which is the shape of ptone/scion#591 itself. Note that the
defect is invisible in review of the commit that introduced it: `89bc2039`'s diff is entirely
correct on its own. The defect lives in the gap between two developers' commits, which is the space
nobody owns.

> **Standing rule (aid-em, `_common.md` rule 6):** do not assert a cross-file invariant that another
> developer's unlanded work is required to make true. Write the comment in the tense that is true
> when you push; name only the callers that exist. Whoever lands the other side updates the comment,
> and that update is part of **their** definition of done rather than a tidy-up.

**Resolution:** dev3 landing the twin makes the comment true, and the comment is carried as an
acceptance criterion on that work. If the twin has landed by the time this doc is committed, delete
this subsection — it is written to be removable, and nothing else in §8.1a depends on it.

#### Dispatch is not confined to agent creation

`canDispatchToBroker` also gates **four** sites in `harness_config_handlers.go` — `:715`, `:1159`,
`:1323`, `:1397` — so image-status and check-image broker listings are filtered by the same rule.
Seven `TestImageStatusHandler` tests failed on the change **because they called handlers directly
with no identity — i.e. they were relying on the fail-open** — and were fixed by supplying the
identity the middleware would have produced. That the tests encoded the bug is itself evidence for
§7's lint rule.

### 8.1b Three review rules that each caught a wrong claim today

Both are cheap review-time checks, and each caught a real error in this work.

**A view of the evidence is not the evidence.** A count is only valid for the scope of the sweep
that produced it, and the sweep includes how the output was *read*, not just how it was run. Two
instances today, same error, different truncation:

- **Narrowed by directory.** aid-em stated the Goal 2 conversion set as three sites
  (`handlers_agents_core.go:472`, `:642`, `:1656`), swept within the package this PR touches. The
  full set is **seven** — adding `handlers_gcp_identity.go:308/:333/:380` and
  `lifecyclehooks/validate.go:425/:433`. The last two are in a different package and are invisible
  to any `pkg/hub`-scoped grep.
- **Narrowed by line count.** aid-em's worklist to aid-dev3 gave 16 sites while asserting 20,
  omitting `handlers_groups.go:321/:378/:482/:518`. The script output was complete; it was
  transcribed through `tail -25` of their own output. Two of the four omitted were the
  `addGroupMember` pair the same author had personally briefed a reviewer about under "count sites,
  not names." *(Named at aid-em's explicit instruction. Their reason is the rule's real test: if the
  EM's error is the one that appears anonymously, the practice degrades into something that only
  applies downward.)*
- **Narrowed by working tree.** Mine, and the worst of the three because it wore the strongest
  label. I wrote "verified at branch tip" for findings in §8.1a and §4.2 while checked out on
  `scion/aid-arch` at `8dbf1674` — not `scion/agent-id-fix`, which I had not fetched. Both findings
  survived re-verification at `c2d12fac` and `handlers_logs.go` proved byte-identical across the two
  trees, so I was right; **I was not, however, entitled to be.** Some of the §8.1a line numbers I
  presented as verified came from other agents' reports, since the function they point at does not
  exist in the tree I was reading. This is §8.1b rule 2 — name the branch — violated in the same
  document that states it, roughly an hour after writing it.

All three were published as though complete, and in every case the tool was right and the reading
of it was not. Note the escalation: a directory filter is a stated scope, `tail` is an unstated one,
and a stale working tree is an *assumed* one — the harder each is to see in the output, the more
confident the resulting claim sounded.

**Corollaries for this document, both load-bearing before merge:**

- **Any count that came from a summarised sweep rather than a fresh run must be re-derived** —
  §4.4's inventory and the dev worklists share an ancestor in the same greps.
- **Any claim carrying a verification label must name the ref it was checked against**, and a
  fact relayed from another agent must be marked as relayed rather than folded into a first-person
  "verified."

**Audit result — all eleven labels resolved.** aid-dev4 swept the document three times (at 1,810,
2,139 and 2,292 lines) and found the count **stable at 11**, so it was not an artifact of when they
looked. Two refinements were theirs and both improved the result:

- **One of the eleven was not a verification label at all.** *"I checked the alternatives"* (§7.2)
  reports consideration of design options, not observation of the tree. dev4 flagged it as a false
  positive in their own count rather than banking it — **ten is the real number of code-fact
  labels**, and the eleventh is left in the table only so their arithmetic is reproducible.
- **Three form an attribution class** — someone else's finding plus a claim of independent
  verification — and under rule 8 those need **two** things, not one: *who* verified, and *against
  what ref*. Marked accordingly.

> **The distinction that must reach the reviewer: "labels were unreferenced" and "claims were
> wrong" are different findings, and only the first is true here.** Of the eleven audited, **none of
> the underlying conclusions was false.** Ten were genuine code-fact labels: **seven** needed only a
> ref clause, and **three** needed an anchor edit as well (§8.1's `DefaultBrokerAuthConfig`
> indirection, §12's `registerRoutes`, and the pre-fix tree label on §3.3's reachability claim). The
> eleventh needed neither, being a design-consideration statement rather than an observation of the
> tree. A reviewer who watches rule 8 land with eleven fixes will
> otherwise conclude the document was unreliable. **It was underlabelled, which is a different
> defect with a different remedy** — and stating that plainly is itself an instance of §1.1, since
> "eleven corrections" and "eleven errors" are two states that read identically in a changelog.

The §4.2 correction is the rule paying out: "seven sites in `handlers_logs.go`" was a true count
attached to a false description of what the seven had in common.

**Name the branch on any authorization claim.** With four trees in flight, unqualified claims came
out confident, precise, and wrong three times in one day. *"Project-scoped template policies match
nothing"* is true on `agent-id-fix` and false on `main`, where they over-match instead.

**The duplicate-or-extract discriminator**, which resolves the apparent contradiction between the
near-duplicate kept at create/PATCH (§4.5) and the helper extracted here: *keep the duplicate when
the future change must touch **both** sites and you need grep to find them; extract the helper when
it must touch **one** definition and you need the compiler to force it.* It is a rule about
discoverability, not about DRY — applied as a style preference it gives the wrong answer both
times.

### 8.2 `matchesResource` project scoping — class-level engine defect (filed as ptone/scion#595)

**Status:** filed publicly as **ptone/scion#595** on ptone's instruction. sa-arch found this
independently from the `gcp_service_account` and `template` sides during P0.2; I found it from
the `projectResource` side via §5.3. Same defect. **This track owns `authz.go`, so if it lands
in code it lands here.** Specified below so it is ready either way.

**The defect.** `matchesResource` (`authz.go:369-374`):

```go
case "project":
    // Policy scoped to a project — resource must be in that project
    if policy.ScopeID != "" && resource.ParentType == "project" && resource.ParentID != policy.ScopeID {
        return false
    }
```

The scope check is written as a **deny-list**: it rejects only resources that *declare* a
project parent and disagree. A resource with no `ParentType` satisfies none of the
conditions, falls through, and **matches**. So a project-scoped policy silently applies to
every parentless resource in the hub.

**It is broader than three resource types.** Surveying `capabilities.go`, the builders that
set no project parent are:

| Builder | `ParentType` | Affected |
|---|---|---|
| `agentResource` `:57` | always `"project"` | no |
| `gcpServiceAccountResource` `:157` | always `"project"` | no |
| `projectResource` `:70` | **none** | yes |
| `templateResource` `:80` | **none** — but see below, this is itself a bug | yes |
| `brokerResource` `:148` | **none** | yes |
| `userResource` `:125` | **none** | yes |
| `groupResource` `:108` | conditional (`:118`) | yes, when hub-scoped |
| `policyResource` `:133` | conditional (`:141`) | yes, when hub-scoped |
| `harnessConfigResource` `:89` | conditional (`:101`) | yes, when global- or user-scoped |

**`template` needs a builder fix shipped WITH the engine fix, or the engine fix regresses it.**
Caught by sa-arch reviewing the implementation. `store/models.go:549-553` defines
`TemplateScopeGlobal/Project/User`, and `Template` carries `Scope` (`:474`) and `ScopeID`
(`:475`) — structurally identical to `HarnessConfig`. But `templateResource`
(`capabilities.go:80-86`) sets no parent **for any scope**, missing exactly the conditional
`harnessConfigResource` has eleven lines below it.

So **project-scoped templates exist** — created by import (`handlers_resource_import.go:115`,
`:118`), bootstrap (`template_bootstrap.go:199`, `:244`, `:251`) and clone
(`template_handlers.go:782`, `:839`) — and today a project-scoped policy targeting templates in
*its own* project matches correctly, but **by accident, through the same fallthrough** that
causes the over-match. Closing the over-match closes the correct match with it: **on a tree that has
the ptone/scion#595 engine fix but not the `templateResource` builder fix — a state that exists on no branch
today, since `180debe` landed the builder — that policy would match nothing.** Stated with the tree
named, per §8.1b rule 2; the earlier temporal-only phrasing ("after the engine fix alone") is the
same sentence shape that already went wrong once.

That is an under-match — the safe direction — but it is still a functional regression against an
intended configuration, so the builder fix ships in the same change:

```go
if t.Scope == store.TemplateScopeProject && t.ScopeID != "" {
    r.ParentType = "project"
    r.ParentID = t.ScopeID
}
```

Genuinely global templates stay parentless and stay excluded, which is the intent.

*Nuance, not a blocker:* `Template` also has a deprecated `ProjectID` (`:476`). On write paths
`ScopeID` is authoritative and `ProjectID` mirrors it (`template_handlers.go:271`, `:832`; `:250`
back-fills from `req.ProjectID` for old clients), so new rows are fine. Legacy rows with
`ProjectID` set and `ScopeID` empty would stay parentless. Do **not** add a `ProjectID` fallback
to the builder — that breaks the structural mirror with `harnessConfigResource`, which is the
point — but someone should confirm such rows do not exist before Goal 2 assumes `ScopeID` is
populated.

**This strengthens the root cause.** `templateResource` missing the conditional its structurally
identical sibling has is oversight evidence at the **builder** level, not just the engine level.
The parentless class is an accident, not a design.

**`harness_config` is the reachability case, and I initially missed it.** sa-arch caught it.
`HarnessConfigScope` is `global` / `project` / `user` (`models.go:637-639`) and the parent is
set only for `project`. Global harness configs are **created by default bootstrap** —
`resource_bootstrap.go:135` and `system_handlers.go:558` both write
`Scope: store.HarnessConfigScopeGlobal`. So parentless resources of this type already exist on
essentially every hub with no operator action, which makes the defect a statement about live
data rather than about a hypothetical configuration. A harness config determines the harness an
agent runs, so blast radius is comparable to `template`.

Framing for the writeup: **lead with `policy`** for the self-referential argument (a
project-scoped grant reaching the hub's own authorization configuration), **cite
`harness_config`** for reachability. `gcp_service_account` stays forward-looking — it sets
`ParentType` unconditionally today and becomes affected only once svc-accnt P4 makes it
hub/user-scoped.

**`enforceUATConstraints` has the same class defect but a different shape.** `authz.go:413-419`:

```go
if resource.Type == "project" {
    if resource.ID != projectID { /* deny */ }
} else if resource.ParentType == "project" && resource.ParentID != projectID {
    /* deny */
}
```

It carries the explicit `Type == "project"` arm that `matchesResource` lacks entirely — that
difference *is* the `project` half of the bug. So on the UAT path `project` is **not** affected;
`template`, `broker`, `user`, `harness_config`, and hub-scoped `group`/`policy` are. sa-arch
originally described the two functions as identical in shape and has since corrected it.

A mitigating factor on the UAT path only: after the project check, `authz.go:423` requires
`resource.Type + ":" + action` to be in the token's scopes. A UAT scoped to project X reaching a
parentless template still needs `template:read` on the token. That is a scope-gated reach rather
than an open one — it lowers severity, not the need to fix.

**The proposed point-fix does not fix the class.** sa-arch's suggested change — *"compare
`resource.ID` when `resource.Type == "project"`"* — repairs only `projectResource`. Templates,
brokers, users, and hub-scoped groups and policies would still fall through. It is a
point-fix for one of seven affected types, and adopting it under the "class-level" label would
leave the defect live while closing the ticket.

**Recommended class-level fix** — invert to an allow-list and reuse the helper that already
encodes this exact question. **The block below is proposed, not quoted: neither it nor its comment
exists in `pkg/` at `c2d12fac`.** (Flagged by aid-dev4, who grepped for the comment and could not
find it — the correct reflex, and the reason every code block in a design doc needs to declare
which side of the change it is on.)

```go
// PROPOSED — does not exist yet.
case "project":
    // A project-scoped policy applies only to resources that resolve to that
    // project. Parentless / hub-scoped resources resolve to "" and must NOT
    // match — fail closed rather than falling through. No outer ScopeID guard:
    // a project-scoped policy with an empty ScopeID matches nothing.
    if pid := projectIDForResource(resource); pid == "" || pid != policy.ScopeID {
        return false
    }
```

**The outer `if policy.ScopeID != ""` guard is deliberately dropped** — sa-arch caught that
keeping it reproduces the same overload one level up: a project-scoped policy with an *empty*
`ScopeID` would skip the check entirely and match everything. Not reachable through the API
(`createPolicy:191` requires `scopeId` for project scope) and no seeded row produces it, so
this is hardening rather than a defect fix. Dropping the guard costs nothing, because
`pid == ""` is already rejected and a non-empty `pid` can never equal an empty `ScopeID`.

`projectIDForResource` already exists at `authz.go:446-454` and handles both arms
(`Type == "project"` → `r.ID`; `ParentType == "project"` → `r.ParentID`; else `""`). Three
properties:

- **One change covers all seven affected types, and every future resource type**, because new
  builders default to `""` and are therefore excluded rather than included.
- **It is the same helper the read-class baseline uses** (§5.4). After this, the engine has a
  single definition of "which project is this resource in," used for both policy scoping and
  the agent baseline. Today those would be two different notions in the same file.
- Fail-closed by construction, which is the theme of the whole change.

**Reachability: operator-reachable, not firing by default.** All three auto-created policies
miss. `seed.go:51` (`hub-member-read-all`) and `seed.go:62` (`hub-member-create-projects`) are
`ScopeType: "hub"`, so the `case "project"` arm never runs.
`handlers_projects_core.go:588` (`project:<slug>:member-create-agents`) is project-scoped but
`ResourceType: "agent"`, which fails the type check at `authz.go:359` before scope matching
even runs — and `agentResource` always carries a project parent regardless.

Two activation paths. The first is **misconfiguration**: an operator writes a project-scoped
policy with `resourceType: "template"`, `"harness_config"`, or `"*"`. `handlers_policies.go:178-200`
validates `name`, `scopeType`, `scopeId`, `actions`, and `effect` — but **never validates
`resourceType`** against a known set, and nothing warns. That is a real footgun but it requires
a mistake.

The second is sharper, and sa-arch's point: **the defect activates when someone *correctly*
implements the recommended refactor.** The hand-rolled project-isolation checks scattered
through the handlers are exactly what masks this today, and Option A's own logic calls for
removing them once policy evaluation is trusted to express isolation. So it needs no
misconfiguration and no neglect. This contributed to recommending B (§5.4) — but the defect
stays armed either way for whoever revisits A later.

**Blast radius.** The only configurations that change
behaviour are **user-authored project-scoped policies targeting a parentless resource type** —
which are precisely the ones silently over-matching today. Low real-world risk, but it is a
behaviour change for existing policies and must be called out in the PR description, not
buried.

**On fix shape.** Worth stating the principle explicitly, because it generalises past this
patch: absence of a parent is currently **overloaded**. It means both *"this resource is not in
a project"* and *"no project restriction applies."* Those must come apart. A hub-scoped resource
should be **outside** a project-scoped policy's reach, not **unconstrained** by it. The
`pid == ""` clause above does that minimally; the durable version is to make "not in a project"
a positively represented state rather than something inferred from a missing field.

Adding `resourceType` validation in `handlers_policies.go` is worthwhile independently, but it
is defence in depth — it does not fix the engine. **If added, it must cover the update path as
well:** `updatePolicy:333-335` assigns `req.ResourceType` with no allow-list (it validates
`effect` at `:343` and nothing else), so a validator added only to `createPolicy` is bypassable
with a follow-up PATCH. Scope itself is not at risk on that path — `UpdatePolicyRequest`
(`:55-67`) carries no `ScopeType`/`ScopeID` field, so scope is immutable after creation. Update
widens the resource-type footgun, not the scope one.

**Recommendation on scope.** If ptone wants it in this PR, it belongs in **Phase 1**
(engine changes, before any call-site conversion), with tests asserting: project-scoped
policy + parentless resource → no match; + same-project child → match; + other-project child
→ no match; + the project itself → match; and both seeded policies unchanged. It is ~6 lines
and shares Phase 1's existing test scaffolding.

Two arguments against folding it in, for ptone to weigh: it is the only part of this change
that alters behaviour for **user-authored policies** rather than for agents, and it is not
required by anything else here. It **is** a hard prerequisite if Option A is ever adopted
(§5.3), so deferring it means Option A carries it as a dependency.

#### 8.2.1 The same defect in `enforceUATConstraints` — a second instance ptone/scion#595 does NOT fix

Found by `sa-inv` in the general case; applied to this section by `sa-arch`, who **corrected
their own earlier de-escalation** of it. Verified against source before recording.

`enforceUATConstraints` (`authz.go:416-421`) carries the identical deny-list shape:

```go
if resource.Type == "project" {
    if resource.ID != projectID { deny }
} else if resource.ParentType == "project" && resource.ParentID != projectID { deny }
```

A **parentless** resource satisfies neither arm. Nothing denies, the function returns `nil`,
and `checkAccessForUser` proceeds. **This direction fails OPEN, not closed** — the opposite of
`matchesResource`. `ScopedUserIdentity` embeds `UserIdentity` (`identity.go:89`), so `Role()`
promotes and the **admin bypass at `:121`** grants.

**Reachability — only three of the six downstream checks matter.** Two self-defend:
`2.6 project owner/admin` (`:147`) keys on `projectIDForResource`, which returns `""` for a
parentless resource, so it is skipped; `4 policy matching` self-defends once ptone/scion#595 lands. The
grants actually reachable are `1 admin bypass` (`:121`), `2 owner bypass` (`:129`), and
`2.5 ancestry` (`:136`). The latter two are milder but still real: a UAT pinned to project A
acting on the holder's own resource in project B defeats the pin, which is supposed to confine
regardless of ownership.

**The generalisable shape:** the danger zone is exactly the grants sitting *between*
`enforceUATConstraints` and the first check that keys on `projectIDForResource`. Everything
downstream of that helper inherits its correctness.

##### CORRECTION — the scope gate makes this unreachable today. It is latent, not live.

An earlier draft of this section said the scope gate at `:423` was "mitigating, not refuting"
because "a project UAT for template work holds `template:read` naturally." **That is wrong. There
is no such scope and none can be issued.**

`enforceUATConstraints` applies a second gate after the project check: `resource.Type + ":" +
action` must be in the token's scopes. `UATValidScopes` (`store/models.go:1454-1466`) is **11
entries covering exactly two resource types** — `project:read` and ten `agent:*`. Both are
non-parentless:

| Type a UAT can hold | Parentless? | Why not |
|---|---|---|
| `agent` | **No** | `agentResource` (`capabilities.go:58-66`) always sets `ParentType: "project"` |
| `project` | **No** | Caught by the `Type == "project"` branch, which compares IDs directly |

Every genuinely parentless type — templates, brokers, users, hub-scoped configs — is unreachable
because **no token can hold a scope for it.** The narrowness of the scope vocabulary is
accidentally acting as a security control.

**This inverts the priority between the two ptone/scion#595 instances:**

- `matchesResource` — **live on `main`**, needs only an operator-authored project-scoped policy.
- `enforceUATConstraints` — **latent**, gated behind a vocabulary containing no parentless type.

**What arms it is ptone/scion#605** — ptone's own request to mature UAT scoping. The first `template:read` or
`broker:use` entry added to `UATValidScopes` makes this live, and it will not look related: an
addition to a constants map in `store`, versus a missing branch in `hub/authz.go`. ptone/scion#605 carries
this as a hard blocking prerequisite.

**How this was missed:** three reviewers analysed the fall-through and all three reasoned about
the project check without checking what the gate *below* it could admit. It was found while
writing ptone/scion#605 — by describing the scope vocabulary, not by studying the defect.

**Fix — a companion to ptone/scion#595, not covered by it.** ptone/scion#595 patches `matchesResource`; this is a
different function and stays armed. **But the obvious fix is wrong**, and the reason generalises.

##### The retracted patch, kept because the error is instructive

I proposed collapsing both arms onto the same helper ptone/scion#595 uses:

```go
// DO NOT APPLY — breaking. Retracted.
if pid := projectIDForResource(resource); pid != projectID { ... deny ... }
```

`sa-arch` caught it within minutes. For a **global** template `projectIDForResource` returns
`""`, so `"" != projectID` **denies** — and a deny here is a hard return at `:117-119`, so
nothing downstream rescues it. A project-pinned UAT would lose global template reads entirely.
Verified at `c2d12fac` — one call site, `template_bootstrap.go:73`, inside the loop over bundled
templates: `s.bootstrapSingleTemplate(ctx, name, templatePath, store.TemplateScopeGlobal, "")`.
**Default data on every deployment, not operator configuration.** *(Independently re-checked to
completion by aid-dev4, who confirmed the claim and the single call site.)*

**The conflation.** `projectIDForResource` answers *"which project is this resource in."* The two
call sites ask different questions:

| Call site | Question | Right answer for a global resource |
|---|---|---|
| `matchesResource` | May a policy **scoped** to project A govern this? | **No** — over-reach. Deny. |
| `enforceUATConstraints` | Is this access **cross-project**? | **No** — not another project's resource. Deny is wrong. |

The two functions look symmetrical and are not.

**Why this is recorded rather than quietly fixed:** the section above argues that absence of a
parent is *overloaded* and that "not in a project" and "no restriction applies" must come apart.
The patch fixed *absence-means-unconstrained* by making absence mean **denied** — the same
overload with the sign flipped, absence still carrying one meaning across two call sites that
need two. Anyone re-deriving a fix here will reach for the same symmetry.

##### The actual root cause is in the data model — filed as ptone/scion#604

`sa-arch`'s diagnosis, which supersedes the framing above. The overload is **not** in either
function. `Resource` (`authz.go:51-59`) has no way to say *"this resource is global"* as distinct
from *"no parent was recorded for this resource"* — one empty field carries both meanings, and
`templateResource` (`capabilities.go:80`) produces the second case today for every scope.

That is *why* fixing either function alone reproduces the bug with the sign flipped: **you are
not fixing an overload, you are choosing which of its two meanings to honour, and whichever you
choose is wrong at the other call site.** My patch did not merely miss a case — it was
structurally unable to be correct at both.

The durable fix is therefore not a better arm. It is making the two states distinguishable in
`Resource` so neither function *can* conflate them and the compiler carries the invariant instead
of a comment. Filed as **ptone/scion#604**, deliberately worded as *"make the states distinguishable"* rather
than *"be careful with parentless resources"* — the latter is not a fix and does not survive
contact with the next contributor.

**ptone/scion#604 does not block anything here.** Steps 1–3 below produce a correct hub; ptone/scion#604 is step 4 and
prevents recurrence.

##### What the fix actually requires: a ruling, then an ordering

**The unmade ruling:** *should a project-pinned token reach genuinely global resources?* Read,
plausibly yes — a token that cannot read bootstrap templates cannot create an agent. Write,
plausibly no. Blanket deny answers this silently and in the breaking direction.

**The ordering** (`sa-arch`; the load-bearing part). Today "parentless" means two different
things — genuinely global, *and* project-scoped-but-the-builder-forgot (`templateResource`).
Until the builders are fixed, any rule for this arm applies to both classes and cannot tell them
apart. So: **1)** fix the builders → **2)** parentless now means exactly "global" → **3)** rule →
**4)** patch the arm. I verified step 1 delivers what it claims *(reasoning about the specified end state, checked against builders at `c2d12fac`)*: after the seven builder fixes
the parentless set is `brokerResource`, `userResource`, hub-scoped harness configs/groups/policies
and global templates — all genuinely hub-level. The claim holds.

**Interim posture.** Per the correction above, the fail-open is **latent** — unreachable until the
UAT scope vocabulary widens. So there is no live exposure to race, and the ordering can be
followed properly rather than under pressure. What must not happen is ptone/scion#605 landing first; that is
recorded there as a blocking prerequisite rather than left to sequencing luck.

**The ruling in step 3 was requested in parallel with step 1** — nothing about asking the question
depends on the builders, only applying the answer does. *(Answered by ptone 2026-07-28: project-
pinned UATs **may read** hub-global resources. **Write was not answered** and remains open; the arm
must not be implemented for the write direction until it is.)*

**A `""`-means-global fix is not sufficient**, per the three-state correction: a multi-project
broker also returns `""` from `projectIDForResource` but wants "evaluate against *any* of its
projects," not "evaluate hub-scoped policies." The ruling settles globals and does not reach
brokers.

##### The builder fixes are themselves a partial fix for this arm

Not previously noted, and it changes how step 1 should be described. `enforceUATConstraints`
denies on `ParentType == "project" && ParentID != <token project>`. A **parentless** resource
satisfies neither arm and falls through to admin bypass. So the moment a builder starts setting
`ParentType`, that resource type **moves out of the fail-open class and into the confined class** —
a UAT pinned to project A is correctly denied on project B's instance of it.

Step 1 is therefore not merely a prerequisite that makes the question askable: the set of builder
fixes shrinks what the arm patch in step 4 has to cover down to genuinely-global resources.

⚠️ **An earlier revision of this paragraph said "each builder fix is itself a security fix" and told
implementers to state that in each builder commit. Both halves are withdrawn** (caught by aid-dev4,
verified by me at `c2d12fac`). `store.UATValidScopes` (`pkg/store/models.go:1454-1466`) is exactly
**eleven** entries — `project:read` plus ten `agent:*` — enforced at exactly **one** site,
`useraccesstoken.go:87`. Of the nine builders, only `agentResource` and `projectResource` produce
UAT-addressable types. **For `template`, `harnessConfig`, `group`, `user`, `policy`, `broker` and
`gcpServiceAccount` alike, the confinement effect is latent, not live.**

This was the retracted `180debe` claim written in general form, sitting four lines above its own
retraction — and the per-commit instruction would have propagated it into up to **six more permanent
commit bodies**. It is the same mechanism as the original overclaim, which is why the general form
had to be checked separately rather than assumed corrected along with the instance.

**Correct instruction for builder commits:** describe them as regression repairs, and state the
confinement effect as *latent, contingent on the scope vocabulary widening under ptone/scion#605*.

Concretely for `templateResource` — **landed as `180debe`**, suite green, `ScopeID` only with no
deprecated `ProjectID` fallback, broker and user deliberately untouched:

1. **Completes `4c0b675`.** Project-scoped template policies match correctly for the first time.
   Not a regression repair — the builder never set a parent (`b5ae5a0b`).
2. **Extends intended project reach.** Step 2.6 project owner/admin bypass and the Part 2 agent
   read baseline now reach the project's own templates.
3. **Confines project-scoped templates against project-pinned UATs — but LATENTLY, not live.**

**Effect 3 is overstated in `180debe`'s commit body**, which calls it "a security fix, not just a
repair." That was my claim, retracted at 17:39; the correction reached the PR body but not the
commit author. No UAT can hold `template:read`, so nothing was open. The PR body carries the
corrected wording; the commit message was left alone rather than force-pushed, because four devs
were active on the branch.

**Review heuristic, worth keeping** — this is the cheap check that would have caught it at
authoring time: *if demonstrating a security property requires constructing a credential the
system cannot issue, the property is latent and the commit must say so.* The test added alongside
the fix builds `NewScopedUserIdentity(nil, project, []string{"template:read"})` — a scope
`CreateToken` rejects. The test was the evidence; noticing it required no knowledge of the scope
vocabulary, only noticing that a credential was being hand-built.

**aid-dev1 generalised this one step further, and the extension is the more useful half.** Rather
than reasoning about it they ran the check: reverting `templateResource` to parentless makes all
three UAT subtests fail. That *mechanically* looks like "the test proves the fix" — and it is a
trap. The subtests fail only because the hand-built scope bypasses the gate that stops real tokens.
**Fails-on-revert proves a test touches the builder; it says nothing about reachability.** Worth
stating because a three-outcome check would have read that failure as evidence the hole was live
and sent everyone back to the stronger claim — the retraction would have been re-retracted, by a
green-to-red transition that looked like proof.

So the fabricated credential is not only a tell about the *hole*; it is a standing warning about the
*test*. **Any test that mints a credential the system will not issue is testing a mechanism, not a
property, and its name should say which.** Landed at `c2d12fac` as
`TestTemplateResource_UATProjectArm_Latent`, which additionally asserts
`store.UATValidScopes["template:read"] == false` (`authz_agent_baseline_test.go:556`). That makes
the latency premise **machine-checked**: whoever widens the scope vocabulary under ptone/scion#605 gets a
failing test pointing at the explanation, instead of a green suite that has silently changed
meaning. This is the §7.2 allowlist ruling arriving independently from the test side — a decision
without a test is indistinguishable from an omission, so pin the premise, not just the behaviour.

**Residue for the step-1 accounting** (aid-dev1's catch, and correct): **global templates stay
parentless**, so they remain outside UAT project confinement entirely. That is the right call — a
global template is in nobody's project and giving it a parent would be a lie. But it means the
builder fix **shrinks** the arm fix for templates rather than eliminating it, and it is the
three-state problem (ptone/scion#604) surfacing on the very first builder: templates split into
project-scoped (now confined) and global (still not), with only the arm fix plus ptone's ruling
settling the second half. Pinned by a test so it is recorded rather than assumed closed.

**Priority against the backfill:** fix the arm first. The backfill (below) fixes one row class;
the arm keeps producing the hole for every parentless builder permanently.

#### 8.2.2 Legacy `Template.ProjectID`-only rows — backfill, tracked WITH 8.2.1

Resolved with `sa-arch`: **no builder fallback** to the deprecated `Template.ProjectID`. Their
reason is the stronger one — a builder fallback makes a deprecated field load-bearing in the
authz engine, which is the last place to keep one alive. Fix is a backfill, following the
existing `BackfillGCPVerificationStatus` pattern (`entadapter/external_store.go:238`).

**Do such rows exist?** `sa-arch` proposed checking before building, hypothesising the
population may be empty. **The evidence says otherwise.** `entadapter/template_store.go`
compensates for exactly this row class in **three independent branches** of the list filter
(`:275`, `:286`, `:293`), each an `Or(ScopeIDEQ(x), ProjectIDEQ(x))`. That is not what code
looks like when the population is empty. The schema permits it too:
`ent/schema/template.go` has `scope` defaulting to `"global"` with `scope_id` (`:68`) and
`project_id` (`:70`) both Optional, and `template_store.go:133` Create writes both verbatim from
the struct. `sa-arch` withdrew the hypothesis.

**The split-brain, which is the part to carry forward:** the **read** path compensates and the
**authz** builder does not. A legacy row is fully visible and listable while being unreachable
by project-scoped policy — and, per 8.2.1, *not confined* by a project-pinned UAT. The token can
find the thing it should not be able to reach. **If this is only tested through the list API,
the problem is invisible** — that API is the one surface that has been papering over it.

**Tense warning, because it inverts.** Post-ptone/scion#595 the policy path *under*-matches a legacy row
(denied, safe-but-broken). **Today, pre-ptone/scion#595, it *over*-matches** — that is the ptone/scion#595 defect
itself. So the row is currently unsafe on *both* paths, and ptone/scion#595 flips one of them. A reader who
takes "policy matching under-matches" as describing today's hub will wrongly conclude that path
is already fine.

**Tracking:** file this **with** the 8.2.1 follow-up, not as separate tidy-up. It is a
data-quality fix for policy matching *and* a partial mitigation for the `enforceUATConstraints`
hole. Filed separately, one gets done and the other does not.

*(Corrected: an earlier draft called the `enforceUATConstraints` instance "the more reachable of
the two." The reverse is true — per 8.2.1 it is unreachable today, while the `matchesResource`
instance is live on `main`. The priority between the two runs the other way.)*

**Not a P4 blocker.** The exposure predates this work and is not created by Goal 2. Neither
`aid-arch` nor `sa-arch` owns it; it goes to whoever owns template migration, and they should
receive the shape, the de-escalation, and the correction to that de-escalation together.

### 8.3 Dead auth helpers — tracking issue (Q4 follow-up)

`RequireAuth`, `RequireUserAuth`, `RequireRole` (`auth.go:457-500`) are unreferenced. Either
adopt them as the middleware layer or delete them; leaving them invites exactly the
misreading the source doc made — the source doc cited `auth.go:472,490` as evidence of
fail-closed enforcement, when in fact nothing calls them.

**ptone has asked for a tracking issue for dead-code / tech-debt cleanup** (Q4 follow-up).
This family is the first entry. Related candidates found while surveying: the
`checkBrokerDispatchAccess` / `canDispatchToBroker` duplication (two near-identical
fail-open helpers, one HTTP and one silent), and `requireAdmin` being file-private in
`skill_registry_handlers.go` while ~30 sites hand-roll `user.Role() != "admin"` instead.
Whether the `RequireAuth` family is deleted or adopted should be decided together with §12,
since adopting it is the seed of the middleware layer.

### 8.4 `metadata_mode` — two of three layers fail open on an unrecognised value

Raised by sa-arch off the §3.3 finding. I traced it independently at `c96af412`; the conclusion below is
the corrected one, and it is **not** the benign "sidecar fails closed" reading that was
briefly circulated mid-review.

The mode is consumed by **string equality only, at every layer**. There is no normalising
resolver — note the asymmetry with `store.ResolveWorkspaceSharingMode`
(`pkg/store/models.go:235`), which exists precisely so that *"unrecognized or future wire
labels safely fall back."* There is no `ResolveGCPMetadataMode`.

On an unrecognised value:

| Layer | Site | Behaviour | |
|---|---|---|---|
| Hub — build config | `handlers_agents_core.go:569-585` | `switch` with **no default** → `AppliedConfig.GCPIdentity` left nil → env never set | **closed** |
| Broker | `runtimebroker/start_context.go:353-372` | `if mode == "assign" \|\| mode == "block"` with **no else** → `GCE_METADATA_HOST` / `GCE_METADATA_ROOT` never set → SDKs reach the real `169.254.169.254` | **OPEN** |
| Sidecar | `sciontool/metadata/server.go:631, :679` | deny-listed on `block`, not allow-listed on known modes → serves `email`/`scopes`/`token`/`identity` instead of 403 | **OPEN** |
| Sidecar | `sciontool/metadata/server.go:332` | `if s.config.Mode != modeBlock { return }` → skips the defence-in-depth `iptables REJECT` on the metadata IP | **OPEN** |

Two further details I found that are not yet in sa-arch's write-up and belong in the P3
brief:

- **`agent/run.go:1364` matches by prefix, not equality:**
  `strings.HasPrefix(e, "SCION_METADATA_MODE=block")`. So `SCION_METADATA_MODE=blocked`
  satisfies the `block` test and is granted `NET_ADMIN`, while failing the broker's equality
  test at `start_context.go:361`. The two layers disagree about what "block" means.
- **`GCPIdentity != nil` with `MetadataMode == ""` — UNREACHABLE, defence-in-depth only.**
  I audited every construction site of `store.GCPIdentityConfig` in the repo: there are
  eleven, all in `handlers_agents_core.go` (`:571, :578, :582, :591, :598, :606, :611, :617,
  :1573, :1577, :1590`), and **every one assigns one of the three exported constants**.
  There is no literal, no variable copy, and no path that deserializes user JSON straight
  into the store type. Specifically: the PATCH path has a terminating `default:` (`:1596`)
  that rejects `""`; the project-scoped PATCH does not accept `gcp_identity` at all;
  templates cannot carry a GCP block (no `gcp` field exists in `api.ScionConfig`, asserted by
  `TestApplyProjectDefaults_GCPIdentityNotApplied`); and there is no agent import/export or
  clone endpoint (`handlers_resource_import.go:306` restricts `kind` to `template` and
  `harness-config`).

  This corrects my own earlier framing of this item, which implied the combination was
  produced on the create path. It is not.

  It remains worth defending because the store layer does not validate on read
  (`agent_store.go:136-141`), so a hand-edited row, a restored backup, or a future migration
  would land fail-open at `start_context.go:353-355` — the non-nil-but-empty config clobbers
  the broker's `"block"` default and then matches neither arm of the
  `== "assign" || == "block"` test. The defence belongs in the broker/sidecar allow-listing,
  not at the Hub edge.

- **The same read path also swallows its unmarshal error** (contributed by sa-arch, verified by me at `c96af412`):

  ```go
  if a.AppliedConfig != "" {
      var cfg store.AgentAppliedConfig
      if err := json.Unmarshal([]byte(a.AppliedConfig), &cfg); err == nil {
          sa.AppliedConfig = &cfg
      }
  }
  ```
  `agent_store.go:136-141`. On corrupt JSON the error is discarded and `AppliedConfig` is
  left nil — indistinguishable from an agent that legitimately has no applied config. A
  corrupted record therefore presents as an unconfigured one, silently, and every downstream
  nil-check reads it as "no GCP identity configured." Second symptom of the same
  no-validation-on-read weakness as the bullet above.

**Correct characterisation of the risk.** Not currently exploitable: no path gets a raw
unknown mode past the Hub, and the Hub token broker (`handlers_gcp_identity.go:792, :874`)
is strictly `== assign`, so no *assigned* SA token can be minted. The realistic impact if it
were reached is loss of confinement of the **underlying node/broker compute identity** —
which is exactly what `block` mode exists to prevent.

But safety rests on a thin and largely accidental margin, and one correction to how that
margin has been described:

> sa-arch's corrected note says safety rests on the missing default arm at `:569`. That is
> the *backstop*. The **primary** barrier is the deliberate validating `default:` at
> `handlers_agents_core.go:308` (create) and `:1597` (PATCH), which return an explicit
> `ValidationError`. Those are real, documented invariants.
>
> **The reason this matters to this track is that `createProjectAgent` bypasses the primary
> barrier entirely.** On `POST /api/v1/projects/{id}/agents` the deliberate check is absent
> and safety really does rest on the accidental `:569` behaviour alone. That is the thin
> margin, and it is thin on exactly the route §3.3 already identifies as ungated.

So this track does not own the layered fix, but it does own restoring the primary barrier on
the route that lost it. See §9 Phase 2 for how, and note the ownership refinement there — I
recommend a different placement than the one sa-arch proposed, to avoid a rebase collision.

**Why the Hub-edge validation is the load-bearing part** (trace contributed by sa-arch;
recorded as their finding, I have not re-verified the sidecar-start divergence end-to-end).
`httpdispatcher.go:1341` injects `resolvedEnv["SCION_METADATA_MODE"] = gcpID.MetadataMode`
verbatim from `AppliedConfig` on every start and restart — no validation, no allow-list — and
`start_context.go:360` only ever *adds* env keys for `assign|block`, never removes them. So
any stored mode string reaches the agent container unfiltered, and create and restart can
diverge, with restart on the fail-open side.

The point for this design: `validateGCPIdentityRequest()` at the write chokepoint is not
merely input hygiene. It is the **sole guarantee** for a propagation path several layers
downstream that nobody would discover by reading the create handler. That is the argument for
putting it in `createAgentInProject` rather than duplicating it per-route — one chokepoint,
one guarantee. Given the validation lands, `:1341` needs no change; P7 fixes the sidecar end.

**Owned by svc-accnt P7** (moved from P3 — separate package, unblocked today, no reason to
gate it behind Q2): allow-listing rather than deny-listing in the broker and sidecar, a
`store.ResolveGCPMetadataMode` mirroring the workspace-sharing resolver, the `HasPrefix`
mismatch at `agent/run.go:1364`, and both no-validation-on-read symptoms at
`agent_store.go:136-141`. Not this track.

### 8.5 `main` has moved: `GoogleCloudPlatform/scion#888` hub-scoped pre-start hooks — 385 unreviewed authz lines inbound

**Discovered 18:10Z**, while verifying a topology claim in the reading convention. Verified against
`origin/main` @ `db8f6fc5` and `origin/scion/agent-id-fix` @ `c2d12fac`.

`db8f6fc5` ("feat(hub): hub-scoped pre-start hooks, web UI, and CLI extension", `GoogleCloudPlatform/scion#888`) is on `main`
and **not** on this branch. It lands today, from a different track, and it interacts with this
design in four ways.

> **Two of the five consequences below were originally stated wrong, both in the alarming
> direction, and both were falsified by experiment within minutes** — aid-em by a trial rebase in a
> scratch worktree, aid-dev4 by a trial merge and a script run. Corrected in place, with the wrong
> versions kept, because the *reason* each was wrong generalises better than the finding did.

**1. A file this document has never inventoried** — but the inventory is **not** stale.
`pkg/hub/hub_pre_start_hook_handlers.go` is **385 new lines** containing three new authorization
helpers — `requireHubAdmin:54`, `requireHubHookReader:76`, `isHubAdminIdentity:86`.

⚠️ *I wrote: "the inventory is complete for the tree it was built on and incomplete for the tree we
merge into." **Falsified.*** aid-dev4 ran the guard script against a **trial merge** of `origin/main`
into the branch: the survivor count is **20, identical to branch-only**. `GoogleCloudPlatform/scion#888` adds **zero** sites
of our shape, so §3 and §4.4 need no re-derivation and the PR's number is 20 on either tree. My
claim was an inference from *"new authz code exists"* to *"our inventory must have grown"*, and the
inference does not hold — new authorization code that is written correctly adds nothing to a list
of incorrect sites.

**The scoping recommendation survives, on different grounds** (aid-em): keep this file out of scope
not because the inventory is stale but because **the script cannot detect absence** (§7.3). Zero
hits in a new file is not evidence the file is safe. It is out of scope because two people read it
and judged it fail-closed — and **the PR must say it in those words**, rather than implying tool
coverage it does not have.

**2. Three files collide — and the rebase is nevertheless clean.**
`handlers_agent_create_helpers.go` (+43 on `main`), `handlers_projects_core.go`, `server.go`.

⚠️ *I wrote: "a rebase is not going to be clean in the one file whose dispatch helpers are mid-fix."
**Falsified.*** aid-em ran it: 21 commits, exit 0, **zero conflicted paths**, with `go build ./...`
and `go vet ./pkg/hub/...` both clean on the result. `GoogleCloudPlatform/scion#888` touches `populateAgentConfig` around
`:262-309`; our work is at `:885` and `:914`. Same file, six hundred lines apart.

**The lesson is worth more than the prediction was: I reasoned at file granularity and `git` merges
at hunk granularity.** "Three files collide" is a statement about filenames, and **filenames are not
the unit of conflict.** A shared-file list is a list of places to *look*, never a conflict forecast.

**Rebase timing (aid-em's ruling): do not rebase yet, despite it being clean.** Rebasing shared
history needs a force-push and four people hold commits on the branch. **The trial is the
deliverable** — we now know it lands clean without having done it. Rebase once, at the end,
coordinated, after the twin lands, with the trial re-run immediately beforehand because `main` can
move again. It moved today.

**3. It independently confirms the `ScopedUserIdentity` root cause — and defends against it.**
`requireHubAdmin` explicitly rejects a UAT, with this rationale in the source:

> *"A UAT embeds the minting user's role, so an admin-minted project-scoped CI token would otherwise
> pass a plain role check and be able to install a hub-wide hook script that executes as root inside
> every agent container."*

That is §2's `identity.go:89` promotion mechanism, found independently by another track and written
into a guard. **Two teams reaching the same mechanism from opposite ends is the strongest evidence
in this document that the root cause is real** and not an artefact of how we framed it. It also
gives §7 something it lacked: a *correct* hub-scoped guard to use as the canonical positive fixture,
written by someone outside this project.

**4. A fourth guard shape — already correctly ignored, now pinned.** These handlers use
`GetUserIdentityFromContext` + `identity == nil` → **deny**: the ptone/scion#591 lexical form with the opposite
verdict, in the package we are sweeping.

⚠️ *I predicted false positives on the merged tree. **Falsified*** — aid-dev4 ran it: zero hits on
`hub_pre_start_hook_handlers.go`. The reason is better than the prediction was wrong: **shape 3
requires the nil branch to end in an unconditional `return true`, so the verdict is already part of
the match, not merely the lexical shape.** These end `return nil, false` and fall out.

**The fixture was still worth adding, on an honest premise, and it is landed** (`1e3e3628`): the
property holds **by construction and nothing pinned it**, in a file about to gain 385 lines of new
authorization code — one refactor from becoming false silently. Both helpers are now *want-not*
fixtures copied verbatim from `main` with provenance in the comment; self-test is 5 flagged / 7
correctly ignored, and dev4 **mutation-tested it** — weakening the verdict test to match any return
makes the self-test fail with both new fixtures among the false flags, so the fixture demonstrably
bites.

**Worth stating as a rule, because the difference is not cosmetic:** a negative test justified by a
false positive that does not exist is a test the next maintainer deletes when they cannot reproduce
it. *Pin it as a regression pin, and say that is what it is.* Same shape as §7.2's allowlist ruling
and dev1's `_Latent` rename — **name what the test is for, or it decays into noise.**

See **§7.2a** for two shape-3 evasions found afterwards, one of which does bear on the count.

**5. `GoogleCloudPlatform/scion#888` creates a new instance of ptone/scion#604, today.** From its own commit body: *"Hub-scoped rows
share the existing table with an empty `project_id`... The explicit `""` default (rather than NULL)
is load-bearing."* That is **absence-means-global**, deliberately adopted, for a resource whose
payload is *a script that executes as root in every agent container*. No `capabilities.go` builder
exists for pre-start hooks yet — so nobody has hit the `templateResource` trap here, because nobody
has written the code that would hit it. **ptone/scion#604 should be updated to name `ProjectPreStartHook`
before someone writes that builder**, since the whole point of ptone/scion#604 is that each consumer of the
overload rediscovers it separately.

**6. `requireHubHookReader`'s doc comment describes a return value it does not return** (aid-em's
find; verified by me against `db8f6fc5`). The comment says *"The second return value reports whether
the caller is a full hub admin, which controls whether script bodies are included in list
responses."* **It returns `ok`.** Admin-ness is decided separately by `isHubAdminIdentity:86`, and
both current callers do it correctly, so **nothing is broken today**.

The hazard is the signature. `(UserIdentity, bool)` reads fluently as `(identity, isAdmin)` and the
comment *endorses* that misreading. A future caller writing
`identity, isAdmin := s.requireHubHookReader(w, r)` and gating disclosure on `isAdmin` would hand
hub-hook **script bodies** to every authenticated user including UAT-scoped ones — scripts that run
as root in every agent container and, per the source, may embed infrastructure secrets.

**Latent** by the §8.2.1 heuristic: exploiting it requires code that does not exist. **Report
upstream, do not fix here, and keep it out of this PR's severity narrative.** It is standing rule 6
exactly — *a comment asserting a property the code does not have, on an authorization helper* — and
it is the third independent instance today, after `brokerServesProject` and the §7.2 allowlist
ruling. Three instances in three files by three authors is not a coincidence; it is the
absence-reads-as-presence class, which is this PR's thesis.

**7. RULING CHANGED: we do NOT rebase onto `db8f6fc5`. The base stays `c96af412`.** `main` cannot
compile its own `pkg/hub` test binary. aid-em found it; **I verified all three results myself in
clean worktrees rather than relaying them**, which is the only reason this subsection states them:

| Tree | `go vet ./pkg/hub/...` | |
|---|---|---|
| `origin/main` @ `db8f6fc5` | **exit 1** | `scheduler_test.go:478:35: method mockScheduledEventStore.GetActiveProjectPreStartHook already declared at :407:35` |
| base `c96af412` | **exit 0** | clean |
| branch tip `e775843f` | **exit 0** | clean; merge-base with `main` still `c96af412` |

Both declarations come from `GoogleCloudPlatform/scion#888`. Rebasing would inherit a broken test package and present as our
PR breaking `pkg/hub` tests — on a security PR, **arriving with a red suite we did not cause is a
credibility cost we cannot pay and cannot explain in a review comment**. Reported upstream. If it is
fixed before we open, we rebase; if not, we open from `c96af412` **and say so in the PR body**.

> **The finding under the finding.** aid-em had previously reported this rebase as *"clean, build and
> vet green."* The rebase half was true and independently reproduced. The build half came from
> `go vet | head`, which captures the exit code of `head` — the error sat past line twenty. **A clean
> merge is not a working tree, and they are different properties that one sentence collapsed.**
> Note the failure mode: not a truncated list, but a *false green* — a check constructed so that it
> could not fail, whose silence was then reported as evidence. That is §7.2's laundered-green
> category arriving in our own process rather than in the code under review.

**8. Every agent container is a shallow clone, and `git merge-base --is-ancestor` returns a
confident silent wrong answer inside one.** This produced a same-day dispute in which three agents
ran the same command against the same repository and got two different answers, with no error in
either. Recorded because it invalidates a class of measurement this document is built on.

The mechanism is not "shallow means untrustworthy." **A shallow clone truncates history *below* its
graft boundary; an ancestry test is corrupted only when the older commit falls below that boundary.**
The boundary is per-container and invisible in both the command and the output:

| Container | graft boundary | `--is-ancestor 8dbf1674 origin/main` | `branch -r --contains` | correct? |
|---|---|---|---|---|
| aid-dev4, aid-em | above `8dbf1674` | **NO** | 4 lines (unverified whether one was the symref) | **wrong** |
| aid-arch (mine) | **at `8dbf1674`** | YES | 10 branches | right |
| any, after `--unshallow` | none | YES | 10 branches | right |

> **The count is 10, not the 11 lines the command prints** (aid-dev4). `git branch -r --contains`
> emits `origin/HEAD -> origin/main`, which is a symref alias to a branch already on the list — not
> an eleventh branch. Cosmetic for the 4-vs-10 contrast, which survives either way, but corrected
> because **a symref and a branch read identically in that output**, and counting one as the other
> is this document's own thesis appearing in this document's own evidence.

My own container was shallow (`is-shallow-repository` → `true`, depth **1**) and returned the
correct answer, **byte-identical before and after `--unshallow`** — because my boundary was the
disputed commit itself, so the relation under test lay entirely at or above the cut.

Two consequences, and the second is the one that matters:

- **The proposed precondition — "`--is-shallow-repository` must be false before `--is-ancestor`
  means anything" — is sound but unusable as a gate.** It is true in *every* container, so as a
  precondition it is not a check, it is a work stoppage, and it will be ignored within a day. It
  would also have made me discard a correct measurement. The actionable form is not a gate but a
  one-line fix: **`git fetch --unshallow` before any ancestry claim**, unconditionally.
- **I diagnosed this wrong too, in the flattering direction.** I told aid-dev4 their zero came from
  querying a list of *heads* — membership, not reachability. True as a general point, and **not the
  reason I was right**: under their graft the correct command lied the same way, so a second
  independent test would have agreed with their wrong conclusion. My commands under their boundary
  produce their answer. I had method superiority available as an explanation and took it, which is
  precisely the substitution aid-em declined to make about themselves an hour earlier.

**This is a §1.1 row, and the first where the *tool* produced the indistinguishable state rather
than prose: a commit absent because history was truncated is indistinguishable from a commit that
never existed.** `cat-file -e` does not error under a graft — it answers *no*.

**9. The fix for item 7 was itself published without being run, and it does not work in our shell.**
Recorded at aid-em's request, against their own remedy, which is the reason it is worth recording.

Item 7's rule was *use `PIPESTATUS` instead of the pipeline's exit code*. **Measured here at 18:44Z,
two-sided, and it is worse than "does not work" — the outcome depends on which idiom you wrote:**

| Form after `false \| cat` | Result | |
|---|---|---|
| `${PIPESTATUS[@]}` (bash) | **empty** | our shell is zsh 5.9; `bash` exists on disk but is not what runs |
| `[ "${PIPESTATUS[0]}" -eq 0 ]` | **PASSES** | zsh coerces `""` to `0` — **silent inversion, the remedy reintroduces the bug it fixes** |
| `(( ${PIPESTATUS[0]} == 0 ))` | errors, evaluates false | *fails loud, and correct by accident* |
| `${pipestatus[@]}` (zsh, 1-indexed) | `1 0` — correct | but **clobbered by the next command, including the `echo` that inspects it**: second read returns `0` |

So the corrected remedy is not a different variable. **Assert on a positive evidence string in the
output; never on an exit code** — exit status belongs to whatever ran *last* in a compound command,
which is `head`, or `echo`, or the `&& rm` you appended. Verified two-sided before being written
here: the marker form correctly detects `already declared` on `db8f6fc5` **and** correctly reports
clean on `c96af412`.

**The rule prohibits *reading* an exit code, not *emitting* one** (aid-dev4). A tool must still exit
non-zero — `make ci` reads exit codes and has no other channel, and `hack/check-authz-guards.sh`
deliberately returns 0 / 1 / 2 (clean / violations / nothing-analysed). Stated without this
distinction the rule reads as "scripts should not set exit codes," which would break the CI
integration §7.2 depends on. **The script is clean of the hazard and provably so:** `#!/usr/bin/env
bash` so it runs under bash whatever the interactive shell is, `set -euo pipefail` internally, and
zero occurrences of `PIPESTATUS`, `pipestatus` or `$?` anywhere in the file. Its `END` terminator
line exists so the same verdict is available as greppable positive evidence for humans — **emit
both; consume the string.**

> **I hit this defect in this session and did not recognise it.** My first `go vet` run printed
> `=== EXIT:  ===` — blank, because `${PIPESTATUS[0]}` was empty. I noticed the blank, re-ran with a
> direct capture, got exit 1, and moved on **while writing the section about checks that cannot
> fail.** It failed visibly only because I had it interpolated into an `echo`; in the `[ ]` form it
> would have printed PASS.

**Why this earns a §1.1 row rather than a footnote:** aid-em's stated reason for preferring this
remedy was that it was *mechanical rather than a reminder* — and mechanical it certainly looked.
**Well-formedness read as verification.** That is the same substitution as every other row, at the
last possible stage: not the code under review, not the tool, but **our own correction**. Five
remedies published across the fleet today failed on measurement. An unmeasured remedy is
indistinguishable from a measured one, and it arrives with more authority than the defect it
replaces, because it is *presented* as the resolved version.

**Recommended actions, in order:** (a) add the fail-closed nil-guard fixture to the lint script
— **done, `1e3e3628`**;
(b) re-run the guard sweep against `main` merged in, not against this branch alone, before claiming
the inventory is complete; (c) comment on ptone/scion#604 naming this entity; (d) decide whether
`hub_pre_start_hook_handlers.go` is in scope for this PR — my recommendation is **no**, it is
already fail-closed, but the *claim that the sweep is complete* must then be scoped to exclude it,
in the PR body, in writing.

---

## 9. Phased implementation plan

**Part 1 and Part 2 must land together.** The phases below are commits in one stack, merged
as a unit. Phase 2 must not merge without Phase 1: converting call sites before the baseline
exists denies agents on every Group B path.

### Phase 0 — Foundations (no behaviour change)

**Files:** `pkg/hub/authorize.go` (new), `pkg/hub/errors.go`, `pkg/hub/skill_registry_handlers.go`

- `authorize`, `authorizeMsg` (§4.1).
- `authorizeAgentCreate`, `authorizeAgentLifecycle` (§4.3).
- Promote `requireAdmin` out of `skill_registry_handlers.go` into `authorize.go`.

**Tests:** table-driven over identity kinds — nil → 401; user allowed → true; user denied →
403; agent allowed → true; agent denied → 403; broker → 403. Assert the denial log line is
emitted with `principal_type`.

**Reviewable in isolation. Nothing calls these yet.**

### Phase 1 — Part 2 baseline

**Files:** `pkg/hub/authz.go`

- `isReadClassAction`; the read-class project bypass in `checkAccessForAgent` (§5.4).

**Tests:**
- Agent + `ActionRead` + same-project agent resource → allow.
- Agent + `ActionRead` + other-project → deny.
- Agent + `ActionRead` + `projectResource` of own project → allow.
- Agent + `ActionUpdate`/`ActionDelete`/`ActionAttach` + same project → **deny** (the
  read-class boundary).
- Agent + `ActionRead` + resource with no project (`brokerResource`) → **deny** (the
  `pid != ""` guard).
- Agent + explicit `deny` policy on `project:<slug>:agents` + `ActionRead` + same project →
  **deny** (revocability).
- Agent + descendant resource + `ActionDelete` → allow (ancestor bypass unchanged).
- User path unchanged — regression suite must be untouched.

### Phase 2 — Call-site conversion

**Files:** `handlers_agents_core.go`, `handlers_projects_core.go`, `handlers_groups.go`,
`handlers_runtime_brokers.go`, `handlers_agent_create_helpers.go`, `handlers_logs.go`,
`pty_handlers.go`, `handlers_github_app.go`, `project_settings_handlers.go`,
`handlers_shared_dirs.go`, `project_pre_start_hook_handlers.go`

Order within the phase, so the risky work lands on a clean base:

1. **The three streaming isolation *additions* (§4.2) — separate commit, and FIRST.** These are the
   only unauthorized-access sites in the file; the reorders fix a disclosure that isolation already
   blocks today. Different severities, not just different defect classes, so they must be
   separately cherry-pickable to a release branch without dragging the ordering churn along.

   *Recorded because the reasoning is reusable:* I argued for one combined commit, on the grounds
   that splitting leaves the file looking handled while three agent-reachable streams stay open.
   aid-em's counter is that this is a risk of separation **in time**, not of separation into two
   commits, and **ordering** removes it — additions first means a stall leaves the serious half
   already in. Splitting with the reorders first would have produced exactly the failure I
   described. Combining is the *worse* way to buy that safety, since it also costs bisectability,
   which the rest of this stack claims.

   The commit message must state the pairing explicitly — all three sites are streaming twins, so
   this single commit contains all three pairs: same resource, identical prologue, check dropped in
   the copy.

2. Group B + the `handlers_logs.go` reorder (§4.2, four sites) — the sites with regression risk.
3. `authorizeAgentCreate` into `createAgent` + `createProjectAgent` (closes Q12 and §3.3).
4. **Hoist the field-level GCP identity validation into `createAgentInProject`** (§3.3 row 4).

   Extract `handlers_agents_core.go:294-311` into
   `validateGCPIdentityRequest(w http.ResponseWriter, cfg *store.GCPIdentityConfig) bool`,
   call it at the **top of `createAgentInProject`**, and delete the copy from `createAgent`.

   sa-arch proposed adding the block to `createProjectAgent`. I recommend the hoist instead,
   for three reasons:
   - `createAgentInProject` is the single choke point both routes already pass through.
     Duplicating the block into `createProjectAgent` leaves two copies of a validation that
     has already drifted once — it reproduces the failure mode rather than removing it.
   - **This track owns `createAgentInProject`.** sa-arch's note says svc-accnt P3 will
     "hoist the field-level validation into `createAgentInProject`" — that is the same edit
     to the same function, from a track that rebases onto this one. Doing it here means P3
     rebases onto a done deal instead of colliding with us in the repo's most
     security-sensitive function, which is the exact scenario the serialisation was
     designed to avoid.
   - It closes any *future* third caller of `createAgentInProject` by construction.

   Behaviour for `POST /api/v1/agents` is unchanged (same check, one frame later, still
   before any persistence or SA resolution).

   **Two constraints on the implementation:**

   - **Strict-reject, never normalise** (constraint from sa-arch, and I agree). An
     unrecognised or empty `metadata_mode` must return `ValidationError`. Do **not** coerce
     it to `block`. Silently rewriting input is precisely how the layer disagreements in
     §8.4 stayed invisible — a rejected request is a signal, a coerced one is not. Note this
     differs from the *downstream* fix P3 will make, where falling back to `block` is
     correct: at the edge we know the caller's intent is malformed and can say so; in the
     broker we only know the value is unusable and must fail safe.
   - **Also add a `default:` arm to the config-building switch at `:568-586`**, returning a
     500-class internal error. After the hoist it is unreachable by construction — that is
     the point. §8.4 is a case study in an invariant that held by accident and was described
     as a guarantee; making this one explicit costs three lines and converts the accident
     into a documented assertion. It also fixes the silent project-default drop in §3.3.
5. Group A1 mechanical conversions.
5. Group A2 (idiom 2, §3.2).
6. `handlers_github_app.go:113` — and **leave `:121` alone**; add a comment saying why.

**Tests: one regression test per site.** Each asserts an agent token receives 403 (or 404
where isolation runs first) on the path that previously succeeded. This is the test class
whose absence allowed the original bug — it is the deliverable, not an afterthought.

Additionally:
- Cross-project PTY denied.
- Cross-project agent creation via `POST /api/v1/projects/{other}/agents` denied.
- **Parity suite across both create routes** (§3.3): the same request body must produce the
  same verdict on `POST /api/v1/agents` and `POST /api/v1/projects/{id}/agents`. Cover
  `metadata_mode: block` + `service_account_id` → 400 on both; unrecognised `metadata_mode`
  → 400 on both; `assign` with no `service_account_id` → 400 on both; **`gcp_identity: {}`
  (empty mode) → 400 on both**, which is the §3.3 project-default-drop case. This suite is
  the regression guard for the drift class itself, not just for today's four instances.
- **Project-default application on the project route**: a project configured with
  `default-gcp-identity-mode: assign` must produce an agent with that mode via *both* create
  routes. Today the project route silently yields nil.
- Broker-authenticated request denied at each converted site.
- GitHub App update denied for non-admin *and* for agent.
- Positive tests that the legitimate agent flows still work: self-read, sibling log read,
  descendant delete, scoped create, scoped lifecycle.

### Phase 3 — Regression guard and hardening

**Files:** `hack/check-authz-guards.sh` (new), `Makefile`, `.github/workflows/ci.yml`,
`pkg/hub/auth.go`

- The lint script (§7), allowlist empty.
- Broker-header fail-closed fix (§8.1) + test that `X-Scion-Broker-ID` with no valid
  signature is rejected when `brokerAuthService` is nil.

### Not in this change

- `matchesResource` project scoping (§8.2) — separate issue.
- `handlers_agents_core.go:409` policy-engine alignment (§4.4 note) — separate issue.
- Dead `RequireAuth` family (§8.3) — separate issue.
- The downstream `metadata_mode` layering (§8.4) — owned by svc-accnt P3. This track fixes
  the Hub-edge validation only. **No verification task for the EM here:** the sidecar
  behaviour is traced and documented in §8.4, not open.
- Any seeded agent policy (Option A) — deferred, hook preserved.

---

## 10. svc-accnt coordination

Per the agreed sequencing, **this track owns `createAgentInProject` and `updateAgent` and
lands first.** svc-accnt P3 rebases onto the result.

What svc-accnt receives from this track:

| Item | Shape |
|---|---|
| `s.authorize(w, r, resource, action) bool` | `pkg/hub/authorize.go` — Goal 1's IAM gate layers on top of this, not beside it |
| `updateAgent` | Now carries `authorize(agentResource, ActionUpdate)` **and** the scope-match + `Verified` checks (§4.5). svc-accnt adds **only** the `CanActAs` IAM gate on top |
| `createAgentInProject` | The `:446` SA-assign check is converted to `authorizeMsg`; svc-accnt replaces `ActionRead` with `ActionAssign` + `CanActAs` |
| `createAgentInProject` | **Now opens with `validateGCPIdentityRequest`** (Phase 2 step 3). This is the hoist sa-arch scheduled for P3 — it is done, do not redo it. Both create routes are covered. |
| `authorizeAgentCreate` | Exhaustive caller-kind gate — svc-accnt's agent-caller reasoning can assume a validated caller kind |

**Owned by svc-accnt P7** (moved from P3): the layered `metadata_mode` fix per §8.4 —
allow-listing in `runtimebroker/start_context.go:353` and `sciontool/metadata/server.go:631,
:679, :332`, a `store.ResolveGCPMetadataMode`, the `HasPrefix` mismatch at
`agent/run.go:1364`, non-nil-config-with-empty-mode, and the swallowed unmarshal error at
`agent_store.go:138`. This track fixes only the Hub-edge validation, not the downstream
layers. P7 does not depend on this track and need not wait for it.

Note the `:409` recommendation in §4.4 (minimal fix, defer the policy alignment) — svc-accnt
should be aware that the passthrough check remains a hand-rolled comparison after this track
lands.

I have **not** assumed any concurrent edit to those two functions.

---

## 11. Open questions

**All four answered by ptone on 2026-07-28. No open questions remain.**

| # | Question | Resolution |
|---|---|---|
| **Q1** | Fold in the four idiom-2 sites (§3.2)? | **Yes.** In scope. |
| **Q2** | Read-class bypass (§5.4) rather than a seeded policy (Option A)? | **Yes**, after the narrated comparison in §5.3. |
| **Q3** | Agent lifecycle on **peers** via `ScopeAgentLifecycle`, or **descendants only**? | **Peers, via `ScopeAgentLifecycle`** — *"we want to establish the clear control points, I assume we can adjust policy detail later."* |
| **Q4** | Broker-header fail-closed fix in this PR, or separate issue? | **Fail closed, in this PR.** Plus a tracking issue for dead-code cleanup (§8.3). |

**On Q3's rationale, and why the design supports it.** ptone's framing — establish the
control points now, tune the policy later — is exactly what §5.4 is built for. Both
mechanisms are control points with a named, greppable enforcement site
(`authorizeAgentLifecycle`, `isReadClassAction`) rather than a distributed condition, so
tightening later means editing one function, not re-auditing 24 handlers. And because the
read baseline sits *after* policy evaluation, an admin can already narrow it per-project
today by binding a deny policy to `project:<slug>:agents`, without waiting for Option A.

The one thing to be explicit about: choosing peers-via-scope means an agent holding
`project:agent:lifecycle` can restart or exec into any sibling in its project. That is a real
grant, and it is broader than descendants-only. It is defensible because the scope is
template-administered rather than ambient, and because `handleProjectBroadcast`
(`handlers_agent_messaging.go:395`) already treats that scope as conferring exactly this
authority — so we are making an existing, deliberate grant uniform rather than inventing one.
If it later proves too broad, the narrowing is a two-line change in one helper.

## 12. Follow-on track: consistent authorization enforcement

ptone, on the §2.1 finding: *"it sounds like we want to establish more consistent use of auth
middleware? nearly all requests have Authentication check — most should have authorization
check as well."*

That is the right diagnosis, and it is the durable fix for this bug class. This section sizes
it honestly, because the obvious version does not work here.

**Why a straightforward authorization middleware cannot do the job.** Authorization needs the
*resource* — owner, project, labels, ancestry — which requires the DB fetch the handler is
already doing. A middleware running before the handler has only the URL. So middleware can
enforce *that* a check happens; it cannot perform the check for most routes.

**The real obstacle is that the route set does not exist as data.** Verified by me against
`origin/scion/agent-id-fix` @ `c2d12fac`, with the anchors re-resolved after aid-dev4 found them
drifted (see the note below):

- Routing is `http.ServeMux` with **Go 1.0-style patterns** — no method, no `{id}` wildcards
  — despite `go.mod` declaring `go 1.26.1` (`server.go:738`, **`registerRoutes`** `:2682-2861`).
- **114 registered patterns**, of which 28 are prefix subtrees that fan out. They expand to
  roughly **300–400 distinct (method, path, action) operations**.
- Fan-out is up to **five layers** of hand-rolled `strings.TrimPrefix` / `SplitN` /
  `if HasPrefix(subPath, …)` ladders — `extractAction` (**`server.go:3109`**),
  `handleProjectRoutes` (~25 branches, defined at **`handlers_projects_core.go:1274`**; the ladder
  itself runs from ~`:1306`),
  `handleAgentByID` (11 branches, `handlers_agents_core.go:1324`), plus 15 more dispatchers.
- **No OpenAPI spec, no generated client, no route-enumerating test exists.**
- Middleware is applied to the whole mux uniformly (`applyMiddleware`, `:2864-2899`). The
  sole per-route precedent is `requireWorkstation` (`server.go:2939`) on 13 endpoints.

> ⚠️ **Anchor drift, and the signature is worth naming** (aid-dev4). Before this pass the bullets
> read `setupRoutes :2682`, `extractAction :3107`, and `handleProjectRoutes :1306`. **The counts
> were all correct** — exactly 114 registrations, `go 1.26.1` — while `setupRoutes` **does not
> exist** (the function is `registerRoutes`, and it really is at `:2682`), `extractAction` had
> moved two lines, and `handleProjectRoutes` was anchored inside its own body rather than at its
> definition. `server.go` is a branch-changed file, so **right counts with drifted anchors and a
> wrong function name is the signature of numbers counted on one tree and anchors carried from
> another** — the §8.1b working-tree error, leaving fingerprints in a section nobody suspected.
> Note which half survived: the *derived* numbers were fine and the *located* facts were not,
> because a count re-derives identically on any nearby tree while a line number does not.

So "a test that walks the route table" has nothing to walk. The table would have to be
hand-transcribed from ~30 dispatch ladders, and a new `if HasPrefix(subPath, "new-thing")`
branch would silently under-cover rather than fail. **That is the project hiding behind the
question, and it is weeks, not days.**

**Recommended staging.** Three steps, decreasing certainty and increasing cost:

1. **This PR** — `s.authorize()` (§4.1) plus the `hack/` lint rule (§7). This does not make
   enforcement uniform, but it makes it *uniform in shape and greppable*, which is the
   precondition for everything below. Denials also become observable via the structured log.
2. **Small, high-value, independent of this PR** — a manifest covering only the **86
   exact-path routes**, plus a test asserting the 14-entry public allowlist
   (`isUnauthenticatedEndpoint`, `auth.go:344-386`) matches a declared `public` class. A day
   or two. It has immediate value: it would already have caught two live inconsistencies I
   found — `/api/v1/settings/public` (`server.go:2818`) is *not* in the allowlist despite the
   name, and `/metrics` (`server.go:2685`) is not allowlisted while `/healthz` and `/readyz`
   are. Neither is a vulnerability; both are exactly the drift a manifest exists to catch.
3. **The actual fix, as its own track** — migrate to Go 1.22+ `ServeMux` patterns
   (`"POST /api/v1/agents/{id}/{action}"`) so `r.PathValue` replaces `extractAction`. This is
   mechanical but wide (~18 dispatcher files). It is worth doing on its own merits, and it is
   what makes the manifest enforceable rather than aspirational: once routes are enumerable,
   a default-deny registry where every route must declare its authz class becomes a small
   addition, and undeclared routes can fail closed.

**Recommendation: do not attempt 2 or 3 inside this PR.** Mixing a wide routing refactor into
a security fix is how the fix gets delayed and how review quality drops on the part that
matters. Step 2 is cheap enough to run in parallel with implementation by someone else. Step
3 should be a tracked follow-up with its own design.

One caveat worth recording for whoever picks up step 3: the second obstacle is not routing
but vocabulary. Authorization today is 93 inline `CheckAccess` calls with the `Action` chosen
per site, ~30 ad-hoc `Role() != "admin"` comparisons, one file-private `requireAdmin`, and
several bespoke in-dispatcher gates. Several decisions are genuinely conditional
(self-access, agent-vs-user, project isolation) and will not reduce to a single class per
route. The manifest will need a small set of classes plus an explicit
`handler-enforces` escape hatch — and that escape hatch must be *declared*, so it is
countable, rather than being the silent default it is today.

One further item needs a product answer before the PR is final, and I have not assumed one:

- **Are there deployed agents relying on `POST /api/v1/projects/{id}/agents` whose templates
  do not grant `project:agent:create`?** Those callers work today (the route has no gate) and
  will be denied after this change. The same caller is already denied on `POST /api/v1/agents`,
  so this is a consistency fix rather than a new restriction — but it is the most likely
  source of a production break, and it is worth a look at real template configs before
  shipping. Note that in dev-auth mode the scope is auto-granted (`server.go:1943-1946`), so
  local development is unaffected.

# Kubernetes GCP identity UX: GSA/KSA lifecycle (spec)

Issue: ptone/scion#4004. Mechanics: ptone/scion#3329 (phases 2-5). Related: #3935, #3947, #4005, #3942, #2328, #1801, #3430, epic #1956.
Status: draft for product-owner questions (section 9). Decisions recorded 2026-10-09: the product owner accepted every recommendation (Q1a, Q2a, Q3b, Q4a, Q5a, Q6a plus list column) and the decided-without-question items. Baseline: `main` at 2849295. Based on code reading; nothing here was reproduced live.

## 1. Problem in one paragraph

A GCP service account (GSA) used by an agent on the Kubernetes runtime depends on four links held in four places:
- the hub's registered-account record (verified by a token-creator probe);
- a broker-side `kubernetes_service_account_mappings` entry (GSA → Kubernetes service account, KSA);
- a Workload Identity IAM binding for the KSA, set up out of band;
- project or profile defaults stored as project annotations.

No surface joins these links. When one is missing, the failure surfaces late (at broker dispatch, or inside the pod), names the wrong layer, and sometimes includes the account string.

The goal is one rule: **every link is visible from one place, every failure names the missing link and the remedy, and identifiers are human-usable.**

## 2. Principles

1. **One per-GSA view** is the source of truth for state: registration, verification, mapping per broker profile, binding (when checkable), defaults that point at it, agents using it.
2. **Fail early, at the hub.** The hub already knows which broker profiles map which GSAs (heartbeat report, #3329 phase 2). Rejecting an unmapped assignment before dispatch belongs to #3329 phase 4. This spec defines the *message*.
3. **Errors name the step, the scope and the remedy.** For example, "not mapped on profile `gke` of broker `b1`; a broker operator must add a mapping". The account string appears only where the caller's visibility rule allows it (§9 Q1).
4. **Accept what users type.** Accept an id, an email or a display name, and return an explicit error when the value is ambiguous.
5. **Do not duplicate** #3329 (discovery, pre-dispatch rejection, binding automation), #3947 (per-profile default UI), #4005 (settings CLI and agent delegation) or #3935 (in-pod preflight). This spec adds the UX layer on top of them and states what it consumes from each.

## 3. Lifecycle: changes per step

Legend: **[B]** changes product behaviour; **[D]** depends on another issue.

### 3.1 Register
- CLI and API: no change in flow.
- Fix help/docs drift. `scion service-accounts add --help` says hub-scope registration is disabled, but the hub accepts it (§9 Q5 decides which side is right).
- Docs: the Kubernetes HA page states the full setup order in one checklist: register → grant the hub token-creator → map → bind → (optional) default. Each step links to its section.
- Record register, verify and delete as audit events: actor, scope, account id. Not the email.

### 3.2 Map (GSA → KSA)
- Ownership stays with the broker operator / hub admin (§9 Q2).
- **[D #3329 ph4]** Extend the broker's per-profile mapping report from "GSA emails" to `{gsa, ksa, namespace}`. The struct was built to be extended.
- The per-GSA view (§4) shows each mapping read-only to project members.
- Hub-scoped accounts get the same "not mapped on any Kubernetes profile" warning that project-scoped accounts get today.

### 3.3 Bind (IAM)
- Hub token-creator: already probed at verify. The per-GSA view shows verification status and its age. `scion service-accounts verify` re-runs the probe. No periodic job.
- Workload Identity binding: show `unknown` unless the hub can read the GSA's IAM policy. That is likely true for minted accounts and false for bring-your-own accounts. Never report "bound" without evidence.
- **[D #3329 ph5]** Binding automation at mint is out of scope here.

### 3.4 Default
- **[D #4005, #3942]** Add a CLI to set the project default and the per-profile default (§5). Fixing #3942 (a partial PUT drops the project default) is a hard prerequisite.
- **[D #3947]** The per-profile default UI belongs there. This spec only requires that the UI and the CLI use the same `settings` fields.
- **[B]** Validation on PUT: warn (not reject) when a per-profile entry names a profile that no broker in the project has reported, or an account not mapped on that profile.
- **[B]** Web create form: when the user has not touched the GCP identity control, send no identity. This lets the hub's ladder (including the per-profile default) decide. Today the form always sends an explicit mode and bypasses the per-profile default.

### 3.5 Use (create/start)
- **[B]** `--service-account` resolves id, email or display name (§6).
- Error catalogue (§7) replaces the current texts. Most importantly, the broker's missing-mapping error is rewritten. It no longer reaches the user as a raw 502 wrapper around the broker body; the hub maps it to a 400 with a structured code.
- **[D #3329 ph4]** Pre-dispatch rejection uses the §7 `identity_not_mapped` message.
- **[D #3430]** The CLI exits non-zero on a create failure. That issue owns the hang.

### 3.6 Inspect
- §4 per-GSA view (CLI + web).
- §8 agent identity in the CLI.
- Docs: the permissions page claims the agent identity card shows verification status. Either add the status or correct the page; prefer adding it.

### 3.7 Remove
- **[B]** Before deleting, the hub computes an impact report: agents referencing the account, defaults pointing at it (project, per-profile, hub) and broker profiles mapping it.
- The default behaviour follows §9 Q3. Recommended: refuse when defaults reference the account, list the impact and offer `--force`. `--force` clears those defaults; agents keep the reference and fail at next start with the existing "no longer available" message.
- The response lists out-of-band cleanup the hub cannot do: the broker mapping, the KSA, the IAM binding, and for minted accounts the GSA in GCP, which is retained.
- Write an audit event.

## 4. Per-GSA view (starting point 1)

CLI: `scion project service-accounts show <id|email|name>`, plus a `MAPPED` column on `list`. Web: the existing account detail page gains the same sections.

| Section | Source | Notes |
|---|---|---|
| Identity | account row | display name, id, scope; email per §9 Q1 |
| Verification (hub token-creator) | `VerificationStatus`, timestamp | "verify" action |
| Mapping per broker profile | broker heartbeat report | `mapped (ksa, namespace)` / `not mapped` / `not reported` [D #3329 ph4 for ksa/ns] |
| Workload Identity binding | IAM policy read if permitted | `bound` / `not bound` / `unknown (not readable)` |
| Default for | project annotations, hub settings | "project default", "profile gke", "hub default" |
| Agents using it | agents whose applied identity references the id | count + names; needs a store filter on the account id |
| Next step | derived | first missing link, as in §7 |

## 5. Settings CLI for defaults (starting point 5)

Coordinate with #4005, which owns the settings CLI and agent delegation. This spec only fixes the identity sub-commands. If #4005 lands a generic `scion project settings set`, these become aliases.

```
scion project identity default show
scion project identity default set <id|email|name|passthrough|block>
scion project identity default set --profile <name> <id|email|name>
scion project identity default unset [--profile <name>]
```

- `show` prints the resolved ladder: explicit → per-profile → project → hub → runtime default, with the source of each rung.
- Each write is a read-modify-write of only the identity fields. It is blocked on #3942.
- Agents: allowed only if #4005 grants delegation. Until then, return a 403 that names the required role, not "Insufficient permissions".

## 6. `--service-account` resolution (starting point 3)

Order:
1. A UUID matches an id.
2. A value containing `@` matches an email.
3. Anything else matches a display name.

Search the project-scoped accounts first, then the hub-scoped accounts (§9 Q4). If more than one candidate matches, return 400 `identity_ambiguous` listing the candidates' ids and scopes; the caller must be able to see them. If nothing matches, keep today's single "not available" text, which does not tell "unregistered" apart from "not authorized". The same resolver serves create, start, reincarnate, the defaults CLI and `show`.

## 7. Error catalogue (starting point 2)

Each error carries a stable `code`, the failing **step**, the **scope** (project, profile, broker) and a **remedy** naming who can act. Account strings follow the §9 Q1 rule; the table writes `<account>` where the rule allows a name.

| Code | Step | When | Message (shape) |
|---|---|---|---|
| `identity_not_available` | use | id/name/email not found or not visible | unchanged (no account string) |
| `identity_ambiguous` | use | resolver matched more than one | lists ids + scopes |
| `identity_not_verified` | bind | hub token-creator probe failing | "<account> is not verified: the hub cannot obtain tokens for it. A project admin must grant the hub token-creator on it, then run verify." |
| `identity_not_mapped` | map | no mapping on the selected profile | "<account> has no Kubernetes service account mapping on profile P of broker B. A broker operator must add it to kubernetes_service_account_mappings; see docs." Hub-generated (pre-dispatch or translated from the broker), HTTP 400, not 502 |
| `identity_ksa_mismatch` | map | explicit KSA differs from mapping | names profile; KSA names per Q1 |
| `identity_default_invalid` | default | a default points at a missing or unverified account | names which default (project / profile P / hub) and the settings command to fix it |
| `identity_assign_denied` | use | caller lacks assign or actAs | names the missing permission and who grants it; the account appears only per Q1 |
| `identity_mode_unsupported` | use | block mode on Kubernetes | unchanged text, gains a code |
| (none, documented) | bind | missing WI binding | not detectable at create. The docs and #3935 preflight cover the in-pod symptom |

The broker keeps returning detailed text for its operator log. The hub recognises the broker's mapping error by a structured code added to the broker response, not by string match, and rewrites it into the table above.

## 8. Agent identity in the CLI (starting point 4)

- `hubclient.Agent` decodes `appliedConfig.gcpIdentity`.
- `scion list`: add an `IDENTITY` column in wide output (`--wide` if present, else `--json` only), showing mode plus display name.
- Identity has no natural home in `scion look` today, because `look` shows terminal output. Recommended (§9 Q6): add a short header line to `look` (mode, account display name, profile) rather than a new command.
- Web agent detail card: add verification status and, per Q1, the display name instead of or alongside the email.

## 9. Product-owner open questions

Q1. **Account-string visibility.** *Decided by product owner (2026-10-09): (a).* Who may see a GSA email in errors and views?
- (a) any project member, agents included;
- (b) project users only, with agents seeing display names and ids;
- (c) the account's managers only.

Context: agents can already see their own email in the pod environment and in the agent record. The account-list endpoints currently have no authorization check. **Recommendation: (a).** Treat the email as an identifier, not a credential. Fix the list endpoints so only project members can read them, and print the email in errors to anyone with project read.

Q2. **Who owns the GSA → KSA mapping?** *Decided by product owner (2026-10-09): (a) now, (c) follow-up.*
- (a) broker operator only, visible read-only to project members;
- (b) project admins may write mappings within broker-allowed namespaces;
- (c) (a) plus a hub-admin CLI or web editor over the existing server-config overlay.

**Recommendation: (a) now, (c) as a follow-up.** Make (b) a separate design if it is ever needed.

Q3. **Deleting a referenced account.** *Decided by product owner (2026-10-09): (b).*
- (a) block while defaults or agents reference it;
- (b) block on defaults, with `--force` clearing them, and leave agents to fail at next start with the existing clear message;
- (c) cascade silently.

**Recommendation: (b).** Minted accounts stay retained in GCP; the response says so.

Q4. **Same email registered at project and hub scope.** *Decided by lead (design authority, 2026-10-09): (a).*
- (a) the project-scoped account wins;
- (b) ambiguity error.

**Recommendation: (a).** It matches narrowest-scope-wins elsewhere. Duplicate display names are always an ambiguity error.

Q5. **Hub-scope bring-your-own registration.** *Decided by product owner (2026-10-09): (a).* It is enabled in the hub but documented as disabled.
- (a) intended: fix the docs;
- (b) not intended: gate it in the hub.

**Recommendation: (a)** if hub-scoped accounts are meant for shared broker identities. Otherwise (b).

Q6. **Where agent identity shows in the CLI.** *Decided by lead (design authority, 2026-10-09): (a) plus the list column.*
- (a) a header line in `scion look`;
- (b) a `scion list` column only;
- (c) a new `scion agent info` command.

**Recommendation: (a) + a `--json`/wide column in list.**

Decided in this spec without a question (raise one if you disagree):
- no periodic re-verify;
- binding shown as `unknown` when it cannot be read;
- the web create form stops sending an untouched identity;
- the mapping error becomes a hub 400 with a code.

## 10. Proposed child issues (impact order)

1. **Mapping error rewrite + structured broker code** (§7 `identity_not_mapped`, 400 not 502). Unblocked; the highest-impact stall in the pilot. Consumed by #3329 ph4.
2. **`--service-account` id/email/name resolver** (§6). Unblocked.
3. **Agent identity in CLI** (`hubclient.Agent`, list column, look header) (§8). Unblocked.
4. **Per-GSA view: CLI `show` + web sections** (§4). Mapping KSA/namespace depends on #3329 ph4; ships first with the GSA-level report.
5. **Identity error catalogue for the remaining codes** (§7) and the Q1 visibility rule, including list-endpoint authz.
6. **Defaults CLI** (§5). Blocked on #3942; coordinate with #4005.
7. **Web create form: do not send an untouched identity** (§3.4).
8. **Remove: impact report, `--force`, audit events** (§3.7, §3.1).
9. **Docs: Kubernetes identity setup checklist + help/doc drift fixes** (§3.1, §3.6).
10. **Block on Kubernetes: block KSA, automount off, Workload Identity node selector, clear refusal when unconfigured** (§11, #2666). Ranked second, after child 1: it breaks every create under a block default.

## 11. Block on Kubernetes (ptone/scion#2666)

**Product rule (product owner, 2026-10-09).** A Kubernetes agent whose identity resolves to block should have no GCP identity. If GKE cannot enforce that, a zero-privilege identity is acceptable, for example a KSA with no GSA binding and no grants. Assign means the agent's KSA bound to that GSA.

**What GKE can enforce** (from public GKE and IAM docs; not tested live):
- On a Workload Identity node pool, GKE cannot give one pod "no identity". Every KSA is a federated principal and receives a federated token, with or without the GSA annotation.
- Such a principal is zero-privilege only if nothing grants it anything. That includes:
  - no direct grants to it;
  - no namespace-wide, cluster-wide or pool-wide principal-set grants;
  - no `workloadIdentityUser` grant on any GSA;
  - no same-named namespace/KSA in another cluster of the project that receives grants.

  The broker cannot verify any of these with its current credentials.
- The only per-pod "none" is an egress NetworkPolicy restricting the pod's outbound access. It needs network policy enforcement, new broker RBAC or an operator-installed policy, and a live test.
- On a node pool without Workload Identity, a pod reaches the node's service account. The node label `iam.gke.io/gke-metadata-server-enabled` can be used as a node selector to avoid such pools.
- `automountServiceAccountToken: false` removes the Kubernetes API token, not the federated GCP token.
- Today a passthrough pod runs as the namespace `default` KSA with its token mounted (#1801). Block is refused outright, including when it comes from an inherited default, so every create under a block default fails.

**Behaviour (proposed):**
1. A broker Kubernetes runtime or profile setting names a **block KSA**: a dedicated KSA, provisioned by the operator, with no GSA annotation and no grants. Scion does not create it (consistent with Q2).
2. When the mode resolves to block, from the request or from any default, the pod runs as the block KSA with `automountServiceAccountToken: false` and a node selector requiring Workload Identity nodes. The mode is accepted, not refused.
3. If no block KSA is configured, the hub returns 400 `identity_block_unconfigured`. The message names the profile and the setting a broker operator must add. The behaviour never falls back to passthrough or to the `default` KSA. `scion doctor` and broker startup warn when a block default exists and a Kubernetes profile has no block KSA.
4. Optional hardening per profile: the broker attaches an egress NetworkPolicy restricting the pod's outbound access. It ships only after a live test on Dataplane V2 and Calico, and it needs new RBAC or an operator-installed policy selected by a scion pod label.
5. Inspect: the agent identity line (section 8) shows "block (zero-privilege KSA)" or "block (egress restricted)", so the guarantee level is visible. The docs state the IAM preconditions an operator must keep true.
6. The status code of the current refusal (502 → 400) is fixed separately under #3329 and is not part of this item.

Q7 (product owner) is about point 3: when block resolves but no block KSA is configured, refuse, use the `default` KSA with automount off, or fall back to passthrough.

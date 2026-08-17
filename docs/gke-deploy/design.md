# Design: Helm chart for the Scion hub on GKE

**Status:** Draft for review · **Author:** `gd-arch` · **Date:** 2026-08-17
**Brief:** `briefs/gd-arch.md` · **Issue owner:** `gke-deploy-lead`
**Revision 7** — **the `settings-init` init container is deleted from the design, not reduced.**
`gd-p1-dev` found it still named in §17 Phase 1 against a branch (ptone/scion#1096) that ships
none, so an implementer following §17 wrote code the Phase 1 test suite rejects. Revision 5
killed the *copy* and left the container in a reduced role — *prepare the directory* — and that
role was **already nobody's job**: the kubelet creates the `emptyDir` before any container
starts, and hosted mode "bootstraps directly into the Hub via `BootstrapBundledResources`,
**bypassing local `~/.scion` materialization**" (`cmd/server_foreground.go:112-113`; the template
refresh is guarded `else if !hostedMode`). §4.1 now carries that as the **positive twin** of
§5.2's refusal — §5.2 says the copy must not happen, §4.1 says why nothing needs to. This also
repairs a justification that had gone circular: §5.2's `InitGlobal` suppression was defended as
"the chart owns the directory's contents", which was true only while the deleted container
populated them. **This makes §5.2 depend on the chart being hosted-only** — recorded, because a
later phase relaxing that must re-derive both sections. **New §17.1 rule (instance 7): when a
correction *reduces* a thing's role rather than removing it, ask whether the reduced role is a
job at all.** The surviving clause was the harmless-sounding one, which is why five review rounds
passed over it — a leftover that still looks wrong gets deleted, a half-fixed one acquires
camouflage. With two mechanical consequences: **grep for the name, not the behaviour**, and **a
phase's file list is as normative as its acceptance bullets**. Also: `hub.extraEnv` has **four**
guards, not three — `gd-p0-dev`'s `assertNoCredential` is a **value**-axis check catching
credentials in URL userinfo under an innocuous name, so counting guards is the wrong
completeness test and covering both axes is the right one. And **§18 item 28 gains a scope
warning**: Phase 1's "the string `initContainers:` never appears" is correct only until Phase 2,
whose native sidecar is legitimately an `initContainers` entry — Phase 2 must **narrow** it to
"no entry without `restartPolicy: Always`", never delete it.
**§5.4 gains the two-resolver asymmetry** (`gd-p0-dev`, verified against source): the OAuth
redirect base (`initWebServer`, `:2102-2108`) honours a strict **subset** of the sources the
agent-facing endpoint honours (`resolveHubEndpoint`, `:1310`), so configuring the base URL via
`cfg.Hub.Endpoint` or `SCION_HUB_ENDPOINT` yields a correct agent endpoint and a redirect base
that silently falls back to `http://localhost:8080` — dropping the cookie's `Secure` flag
(`pkg/hub/web.go:484`). The first resolver logs on every branch; **the second logs on none, at
any verbosity**, so `--debug` is *actively misleading*: it answers confidently about the side
that works. **No operator guidance may say "enable `--debug`" on its own for this failure.** The
chart is immune only because `SCION_SERVER_BASE_URL` is the one source **both** resolvers honour
— **correctness by invariance rather than by vigilance**, the same shape as `0444` — which is
what makes the schema requirement the mechanism rather than tidiness. **The one route still
open, and the only one no guard closes: nothing may render `server.hub.public_url`** (§18 item
10b, dispatched to Phase 1) — it outranks argv *and* the environment for the agent endpoint and
is invisible to the OAuth resolver. **It is also the only one of the three channels the chart
controls directly** — argv and the env var are guarded precisely because they are not ours;
the settings file is ours, which is why it was never guarded. *The channel you own is the one
you do not defend.* Found by correcting this document's own error: §5.4's table named
`cfg.Hub.Endpoint`, a Go field nobody renders, and **the wrong name was concealing the hole**
(new §17.1 rule 7).
**⚠ A claim in this revision was RETRACTED before it shipped.** Revision 7 first asserted that
`--base-url` was missing from Phase 0's reserved-flag list, and that the list had three groups.
**It was reserved (since `51f62ab`) and the list has four** — the claim was reasoned from this
document's account of the chart instead of from the branch, by two people independently within
an hour. **It is marked retracted in place rather than struck**, which produced **rule 6: a
correction that removes the error without recording it protects against the stale claim and not
against the reasoning that produced it — strike a typo, RECORD a conclusion.** The instance also
produced **rule 4: where an enumeration exists executably in the chart, the design states the
REASONS and does not restate the MEMBERS** (`gd-em`, ruling) — so Phase 0 now names four reasons
and enumerates nothing, and citations name the helper rather than a line number — plus its
sourcing corollary: claims about a **chart file** require fetching the branch, every time.
**Rule 7** comes from the same correction: naming an *internal* identifier where an external one
exists makes a whole category of question unaskable. A second-order error inside the retracted version is worth
its own note: it filed `session-secret`, `dev-auth` and `enable-test-login` under "the chart
already sets these", which asserts the opposite of why they are reserved and would have destroyed
the only group that *can* be verified against rendered `args` (`gd-p0-dev`; revision 8 corrects
the tense — see instance 9). Also new: **§17.1 rule 5 — a
check whose subject is deliberately ABSENT must be proved against a fixture** (`gd-p1-dev`, three
instances in one day), because a scan for something that is not there cannot tell you it would
have noticed. And §5.2 now separates two mechanisms revision 7 had conflated: **`InitGlobal` is
suppressed by the chart's own `emptyDir`, not by hosted mode** — the chart's volume changes which
startup path the hub takes.

**Revision 8** — **two things revision 7 called future risks are open on the branch today, and
that is the revision's theme.**
**(1) `server.hub.public_url` is not hypothetical.** Revision 7 dispatched §18 item 10b as
preventive — a guard against a Phase 5 author editing a template. `gd-p1-dev` reproduced it:
`config.extra` is `mergeOverwrite`d over the settings tree *before* the assertions run, so a
schema-valid values file renders `public_url` **now**, yielding one manifest with two base URLs
and no warning. The general fix is **not** an allow-list over `server.hub` — that is
simultaneously too broad (it blocks the unmodelled keys `config.extra` exists to carry) and too
narrow (**nothing about the prefix is the hazard**). The category is the **alias key**: *a
settings key that is a second name for a value the chart already sets through a different
channel* — invisible to collision detection because the names differ, invisible to prefix
allow-lists because the alias need not live nearby (§18 item 10c, owned by **Phase 5a**).
**(2) Phase 0's group 1 does not satisfy the invariant this document asserted of it.**
`gd-p0-rev-2` found `production` and `port` are members the chart does not render. Group 1's
checkability is therefore a property to **restore**, not to protect, and it needs asserting in
**both directions** — every rendered flag is a member (six missing today) *and* every member is
rendered (two failing). Neither violating member should be **deleted**, which is what group 1's
own comment currently invites: `production` aliases `--hosted` at the variable
(`cmd/server.go:235`), so removing it lets `--production=false` disable hosted mode — one step
from re-opening `/api/v1/system/init`, which calls `os.RemoveAll` inside the tree we mount the
settings file over (**new §18 item 34**). **The four-reason split is what makes the second
direction expressible at all**: it is meaningful only over group 1, and a flat list has no
subset over which it is true. *The split did not only document the reasons; it made one of them
machine-checkable.*
**New: §17.1 rule 8** (`gd-em`) — **when a finding is closed by a CONFIGURATION rather than by
code, it is not closed; it is DEFERRED to whoever changes the configuration, who will not know.**
Tolerance is a stronger guarantee than unreachability: unreachability is a fact about which
features are switched on, decays silently, and **nobody re-checks a note that says "not
reachable"**. Its corollary is about ownership — *"whichever phase next touches this" is not an
owner*, so items name a phase number, and where there is a choice the owner is **the phase that
will breach the guard**. **Rule 5 gains a question ahead of the fixture**: *is the subject
actually absent, or have I only not found the path?* — asymmetric, because a reachable subject
yields the fixture **and** a defect. Three assumed-absent subjects were checked this session and
**all three were reachable**; the base rate is the finding. **Instance 9** logs this document
asserting an invariant in the present tense that the artifact did not satisfy — *state an
invariant as required, or as held, never in the tense that conflates them* — and notes that
fetching the artifact establishes what is **in** it, not what is **true of** it. Also new: **item
33**, a read-only state directory degrades *silently* (`templatecache.New` failure is a
`slog.Warn`), owned by Phase 4.
**Two further rules closed the day, both with more evidence behind them than any earlier rule.**
**Rule 9** — *a check whose pass condition is the ABSENCE OF FAILURE rather than the PRESENCE OF
N SUCCESSES reports success when it runs on nothing* — was extracted only after the **fifth**
instance in one day, from five different authors; its sharpest case is that **a guard which
disappears under `helm template` is worse than none, because the CI that proves the chart safe is
the CI that cannot see it.** **Rule 10** — ***a default may encode a preference; a refusal must
encode a harm*** — requires every refusal to cite the `file:line` **outside the chart** that
makes the refused configuration dangerous, after a guard survived a mutation test, a
false-positive check and a full review before failing on *does the thing it defends against
exist?* The uncomfortable half is that **verifying the mechanism is what produces the feeling of
having checked**, so local thoroughness is a risk factor for missing that axis rather than a
defence against it. Rule 4 also gained a second corollary: **derive operator-facing enumerations
from the definition the code iterates**, because a hand-maintained copy goes stale at the moment
it matters and lies in the safe-sounding direction — *a comment asking people to keep two lists
in sync is a request, not a mechanism.* And §18 item 10c gained the **scope bound** that makes it
closeable: it covers keys reachable *through the chart*, never `config.existingSecret`'s
contents, because **an item that can never be complete is an item nobody can close.**
**Closing out revision 8: rule 11, §7.0, and one new Phase 6 deliverable.**
**Rule 11** (`gd-em`) — *a findings entry about code behaviour without a `file:line` cite is an
**assertion**, and should be **labelled** as one, not suppressed* — carries a corollary from
`gke-deploy-lead` that is the day's nastiest failure in miniature: **a quoted string must be
cited with the line number of the thing it is about, not of where you found it.** "`server.go:236`
deprecates `--config`" is unfalsifiable by inspection; `:236` deprecates `production` and `:237`
binds `--config`, and the correctly-cited version shows the mismatch to anybody. **A cite proves
you opened a file; a cite of the subject proves you found the subject.**
**§7.0 holds Phase 2's HA preconditions by reference**, not by copy — `gke-deploy-lead` sent a
path precisely because a brief written from a message is a derived source. Three things there
outlive Phase 2 and are recorded: **Phase 2 *is* the HA switch** and the diff will read as "add
a `--db` flag"; **do not add a migration Job** (advisory lock `0x5C100008`, a non-instance with
its reasoning); and the loopback gate is now a **trip-wire on §4.1's container table**, because
every later sidecar spends it and none of their authors will read §7. Item 35's preflight-skip
half — which the lead flagged as a claim under review rather than passing it on — is now
verified in source, and the verification produced a **Phase 2 remedy**: a driver delivered by
env survives a settings-load failure; a driver delivered only by a settings key does not.
Finally, **Phase 6 gains a fully specified citation-integrity check** — a mechanism rather than
a sweep, with its own **exact expected count** rather than a floor, because §17.1's rules now
cite §18 items by number and that is five more things that can go stale. The first one already
did, inside rule 8's own worked example, on its first day. *A floor makes growth invisible; an
exact count makes every change to the corpus a reviewed line.*
**Last, and it is the revision's sharpest finding: `--config` IS LIVE ON THE CHART TODAY.**
Not inert. `loadServerFromSettingsFile` decides on a **non-nil top-level `server:` key**
(`hub_config.go:1344-1347`), the embedded defaults have none, and Phase 0 renders no volumes —
so both load routes read `configPath`. The reservation's rationale was true of **Phase 1's**
render and the comment carrying it lives in **Phase 0's** chart. That is the third revision of
this one fact and §5.2 records all three, because the conclusion — *reserve the flag* — survived
every version of the reason and so protected it from scrutiny. Four rules land with it, and they
are one argument: **rule 12** (`gd-em` with the lead's asymmetry) — *a rationale true of one
phase's render must name the phase and the transition*, because **stale comments get tidied and
wrong comments get argued with**, making accuracy-at-time-of-writing a durability hazard;
**rule 13** (`gd-p1-dev`) — *understating a mechanism undersizes the mitigation just as reliably
as overstating it, and reads as caution while doing it*; **rule 14** (`gke-deploy-lead`) — the
variant that is not self-limiting, *a right conclusion on a false reason where the true mechanism
points the same way*, so the check **confirms** the bad reason, catchable only by verification
along a different route and correctable only by naming the downstream victim; and **rule 15**
(`gd-p0-rev-2`, rule 14's remedy) — ***agreement on a conclusion is not corroboration of its
reason***, with the precedent supplied against itself (three of us agreed `RollingUpdate` was
unsafe; the harm did not exist) and the operational half that **the artifact to exchange is the
derivation, not the verdict**. Also new: **item 36**, three phases writing to one `os.Stat`
guard at `server_foreground.go:104`, each ruled safe by citing a different one of the other two.
**Rule 12 then gained its missing half within hours** (`gd-p0-rev-3`, from an axis-d sweep whose
five Required findings were all the same error): **a rationale in a phased artifact must be
TENSED to the phase that renders it, and BOTH tense errors are live.** *True-now-false-later*
goes stale and is caught by nobody; *false-now-true-later* is simply **wrong today** and reaches
an operator inside a `fail` message at the moment they are trying to comply. The demonstration
removes every other explanation: `server_foreground.go:1168-1200` is cited **three times in one
file for one lock, correctly future-tensed once (`:296-299`) and falsely present-tensed twice
(`:234`, `:256`)** — same author, same day, with the correct version already on the page.
**And six further rules, 16–21, from a single incident about a single string** (instance 10):
hand-written revision lists are assertions about history; an unreproducible cite indicts your
search before their file; **a `file:line` cite without its tree is not a citation** (one rule,
three authors); **volume of checks is not independence of checks — ask what premise they
share**; **resolve a rule before citing it by number** (a remembered rule number and a curated
commit list are the same error); and **an exact-string guard against a semantic regression is
defeated by rewording** — verified: the killed phrase is gone at `721fc77` while the same claim
lives one screen away at `_helpers.tpl:848-852`. Rule 9's exact-count extension is now
**project-wide**, not a Phase 6 note. **Then two more: rule 22 — RULE 9 DOES NOT COMPOSE; a set
of individually non-vacuous checks is vacuous at the set level, and *the stronger each
component's internal contract is, the more confident the runner is that green means covered*.
Phase 0 has three fail-closed harness scripts and nothing asserting all three ran; the ambient
form is three `hack/check-*.sh` scripts with no meta-check, and `gd-doc` verified that
`check-authz-guards.sh` — the most rule-9-compliant script in the repository — IS ALREADY WIRED
NOWHERE.** **Rule 22 then gained the half that bites this project: WIRED IS NOT TRIGGERED.**
`ci.yml` fires only on PRs based on `main`, and nine of our ten phases are stacked — observed:
#1093 and #1095 have 2 check runs, **#1096, based on `scion/gke-chart-p0`, has 0**. So Phase 6's
deliverable is no longer *wire the checks* but **assert an observed check run on a PR whose base
is not `main`** — achievable, and the form is verified: a bare `on: pull_request` with **no
`branches:` key**, which produced a SUCCESS check run on PR #1101 against a non-`main` base.
`paths:` filters are prohibited on any workflow running a chart check. **A fourth limb closes the
chain: the check must FAIL CLOSED WHEN ITS TOOLING IS MISSING** — the authz script is wired
nowhere, ours is wired and fails open, and *the failure mode of the fix is worse than the failure
mode of having no CI, because no CI is at least known to be absent*. **Rules 24 and 25** come
from the miss that proved it: *a mutation that asserts one field of a multi-field output has
tested one field, and the more headline-worthy the asserted field the more reliably the others go
unread* — the evidence was already printed in a passing run — and *a mutation suite inherits the
environment of its author, which is the one variable it cannot mutate from the inside*. **Rule 26
(`gke-deploy-lead`) completes the set and is a stated limit on our primary control: REVIEWER
ROTATION SHEDS CONTEXT; IT DOES NOT SHED ENVIRONMENT** — three parties reported 106/106 against
the lead's 0/106 with `helm`'s presence the only variable, so rotating more fresh reviewers adds
confidence and no coverage. The three are organised by **who can find each**, because read as
three warnings they collapse into "test more carefully" and a team satisfies the cheapest.
**Rule 27** falls out of it as a subtree-wide convention: **quote the environment with the
number** — `106/106 with helm 3.16 and kubeconform present`, not `106/106`, since a measurement
without its conditions is an assertion (rule 18, one level up), and it is the conditions rather
than the number that tell a reader what they would have to change to disagree. **Rule 28
(`gke-deploy-lead`): a control must precede the artifact it governs, or it becomes a migration**
— *a pass is applied to the prose that exists when it runs, and the prose that matters is written
after it* — so the justification-prose check is **Phase 0's to build** and Phase 6 wires it and
adds nothing; its presence test was then holed by its own reviewer and **replaced by a committed
RATIO — comment lines and grounding tokens per region, both on inequality either way — because
one citation would otherwise immunise an append-friendly block permanently.** **Rule 14 was
RESTATED rather than joined by a twenty-ninth entry, and it is the merge that matters most: A
CLAIM MUST BE CHECKED AT ITS MECHANISM, NOT AT ITS OUTCOME, BECAUSE THE OUTCOME IS WHAT A BROKEN
WORLD ALSO PRODUCES** — one statement covering the prose defects (R2, R5: right conclusion, false
reason) and the harness defects (`reject()` printing **29 green `ok` lines with no `helm`
installed**, because *binary not found* is also a non-zero exit). It **demotes "every negative
needs a positive twin" (§13.1) to belt-and-braces**, since 29 of 31 assertions still lied and the
two twins were two deletions from vanishing, and it was **discovered three times before it was
stated once**. `gd-p1-dev` supplies the narrower form that says where to look: **a negative
assertion reads its subject, and if the subject can be empty or absent the assertion is satisfied
by its own failure to happen.** **Rule 29 (`gke-deploy-lead`): PRE-REGISTRATION SUBSTITUTES FOR
INDEPENDENCE WHEN INDEPENDENCE IS UNAVAILABLE** — the harm in self-verification is not looking at
your own work, it is *choosing the criterion after seeing the fix*, so criteria fixed and
published before the patch exists remove it. **Rule 30 (`gd-em`): IMPROVING A MEASUREMENT CAN
DESTROY ITS DIAGNOSTIC VALUE — the old number was wrong and loud, the new number is right and
silent.** Counting executed rather than passed assertions was the correct fix and it turns the
`0/106` alarm that began the investigation into `106/106`, because with no `helm` all 106 execute
vacuously; **when you fix a metric, re-run the original bug REPORT against the fix.** **And the
revision's best explanation of its own central finding: `gke-deploy-lead`'s ADJACENCY MECHANISM —
*articulating a principle discharges the felt obligation to apply it*, so the next instance does
not present itself as an instance. It predicts that violations cluster NEAR their articulation in
time and space. THE RULE'S NEIGHBOURHOOD IS THE HIGH-RISK ZONE**, which inverts the instinctive
countermeasure — trying harder near the rule is itself an articulation. **The prediction is
pre-registered at SAME COMMIT and the registration cost its author two of his five instances**
(the sample was selected by the detector and has no denominator); the unbiased measurement is the
grounding density the fifth script will produce, and **if distant regions are as dirty as adjacent
ones the hypothesis is dead.** **Rule 31 is the parent of both instrument errors and of a third:
A METHOD'S SUCCESSES GET COUNTED; ITS INAPPLICABLE CASES DO NOT GET COUNTED AS MISSES, SO EVERY
METHOD LOOKS LIKE IT WORKS** — three instances in one day, all found by the other party, none
catchable by its holder; **name the denominator or say that you cannot.** **Rule 32
(`gke-deploy-lead`, from the #1074 round-cap retrospective): the answer at round 4 is
GENERALISATION and the answer at round 6 is SUBTRACTION**, the round counter belongs to an
**artifact** rather than a phase, and every brief from round 3 asks for **the class the findings
belong to and a sweep for it, not just the instances.** **Rule 33 (`gd-p0-rev-2`) is the parent of
three conventions adopted separately today: A CLAIM MUST CARRY ITS OWN CONTROL, BECAUSE THE
EVIDENCE STAYS BEHIND AND THE CLAIM TRAVELS.** And rule 12 gained the discriminator that tells a
writer what to do about it: **a conditional claim ages; a present-tense claim rots** — with the
finding underneath, that **eleven instances of that class were every one of them found by a person
reading prose, so there is no detector and the guard that was switched off is review itself.**
Rule 23 (`gke-deploy-lead` via `gd-p0-rev-2`) — **a check that exists only in a
reviewer's message has the same durability problem as an instrument description that was never
tested; a review finding is a claim about the future, only a committed script is a mechanism.**
The finding under all of it, and the reason these are written as commands and counts rather than
advice: **the remedy for rule 12 was already in `_helpers.tpl`, written by the author, six
hundred lines above five violations of it — and three separate rules were broken today by the
person who had just authored them. THESE RULES DO NOT WORK AS REMINDERS. THEY ONLY WORK AS
MECHANISMS.**

**Revision 6** — **resolves revision 5's §5.2 placeholder: the chart CAN ship ahead of
ptone/scion#1091.** `ha-lead` confirms every settings-write failure is **soft** — no
`log.Fatal`, no `os.Exit`, no panic; the GitHub App `PUT` returns **HTTP 200** after swallowing
the error. The operational consequence is recorded specifically: pre-#1091 the hub cannot write
`settings.yaml` at all, some writes log warnings, nothing crashloops, and **nothing diverges,
because no write lands** — which is *better* than the copy topology, where a write succeeded
and was lost at pod replacement (consistent beats intermittent). §18 gains **28–29** (the
settings mount is a `subPath` at 0444; no manifest renders `0600`/`0400`) and **30** in the
**Live** set, relocated to `VALIDATION.md` — *a relocated criterion is not a passed criterion*.
**`fsGroup` is now gated on `backend: nfs` (§4.4)** — on coupling grounds, not semantics: a
Filestore-named value was setting a **pod-wide** field governing group ownership of every
volume, with Filestore switched off. That surfaced a four-revision-old contradiction (§5.2
assumed `fsGroup` absent while §4.4 rendered it), and the fix it produced is the strongest
argument the mode has: **`0444` is correct under both `root:root` and `root:<fsGroup>`, and a
mode invariant to a pod-wide field owned by another subsystem cannot be silently broken by a
later phase.** `fsGroupPolicy: None` was also **wrong** — the driver declares no policy and the
**default excludes `ReadWriteMany` NFS** — leaving `0440` rejected on **one** reason, now stated
in its stronger present-tense form (the sidecar reads the file *today*). Whether `fsGroup`
should exist at all is **open for Phase 4**, which must also **re-derive** Phase 1's
"no `fsGroup`" assertion rather than delete it. **⚠ New general rule: the leading zero in a file
mode is load-bearing** — YAML 1.1 reads `444` as decimal 444 = octal `0674`, group-writable,
accepted silently — **measured, not inferred** (`gd-p1-dev` decoded all four spellings through
the apiserver's own YAML→JSON→typed-struct path; §5.2 carries the table). Quoting the literal
**does not install** (`int32`, "cannot unmarshal string"), and is recorded as considered and
rejected rather than dropped (§5.2, §18 item 32).
**Ownership gaps found by `gd-p0-rev`:** `hub.extraEnv`
belongs to **Phase 1** (ptone/scion#1096) and exists to make the negative env-var assertion
testable — *not* as an operator hatch; `README.md` to **Phase 4** with a Phase 6 sweep; and
**`auth.acknowledgeOAuthUnlanded` is a Phase 3 deletion** with §18 item 31 to enforce it. §17.1
gains the rule that **§3.2, §17 and §19 must be reconciled whenever any changes — and that one
section's assumptions about another are verified, not assumed**. §5.2 also records that
**`--config` / `-c` (`cmd/server.go:237`) voids the entire section**; it is reserved in
`hub.args` in a list **split by rationale**, because nothing in the chart sets it and a flat
list invites its removal as tidying. The `https://` base-URL rule is now **unconditional in
§5.4 itself**, with Phase 1's note demoted to history — because **an accepted deviation is
absorbed into the design, not kept as a standing exception**; only pending or temporary ones
stay marked (new §17.1 rule; the test is whether the document would be wrong if the marking were
deleted). Also: §11 distinguishes `scion-hub-gke` as an operator-supplied path
from a shipped default; and §5.2 records that **three of the old eight** cited writers did not
write the file at all.

**Revision 5** — **inverts §5.2's rationale: the settings file is delivered read-only, and the
writable copy is the defect rather than the accommodation.** `settings.yaml` is now a
`subPath` Secret mount at `$HOME/.scion/settings.yaml`, `defaultMode: 0444`, over an
`emptyDir`-backed home — writable **state** directory, unwritable file. Why not a mode-only
copy: `rename(2)` needs write permission on the *directory*, and
`settings_v1.go:2694` renames the settings file, so a read-only **copy** — at any mode — is
moved aside silently while a bind mount `EBUSY`s. Why not `0600`: a Secret volume projects **root-owned**, the pod
is uid 1000, and the failure surfaces as a Block-1 preflight error naming the wrong thing
(caught by `gd-p1-dev`). Why it matters at all: `syncHubSettings` re-seeds `origin="seeded"`
sections from the **pod-local** file on every boot, so a replica's private write can become
shared DB truth — ptone/scion#1091. The inferred list of eight writers is replaced by a
reference to ha-lead's systematically-derived **W1–W13** table; one of the eight was a
different file. Whether the chart may ship ahead of #1091 is left as a **marked placeholder**
in §5.2. **§19 corrections from `gd-p0-dev`, who built Phase 0 against it:** `image.repository`
is now **absent as an active key** (a key defaulted to `""` makes "schema-required" a no-op),
with the published-`scion-hub`-is-not-this-image warning and the `USER root` mechanism that
makes "fails pod admission, by design" true; `hub.args` is **append-only** with a reserved-flag
guard, not a command override; `rbac.agentNamespace` and `runtime.namespace` are reconciled
with `runtime.namespace` canonical and disagreement failing the render; and **`--production`
is not emitted** — a deprecated alias of `--hosted` (`cmd/server.go:235-236`) whose only effect
is a boot-time deprecation warning. Also: §13 gains the default-build-target CI row, and §17.1
gains a running log of rationale-over-mechanism instances (now six) plus a note on pruning
refutable support from correct conclusions.

**Revision 4** — **corrects the Phase 7 image instruction: the `hub-gke` stage is not the
final stage.** Docker's default build target is the last stage, the root `Dockerfile`'s
stage 3 is unnamed and last, and the external `gcloud` consumers pass no `--target` — so
"a new *final* stage" would have silently handed Cloud Run a uid-1000 image, and contradicted
the very rationale Q3 gave for choosing a stage. "Before the final stage" is not the fix
either: a stage can only `FROM` a stage above it. §11 now states the property (the default
build target stays the plain runtime image), the constraint, and the resulting stage order —
stage 3 named `AS runtime`, `hub-gke` derived from it, and a load-bearing empty trailing
stage guarded by a standing CI assertion. §11 also records the **four** hub Dockerfiles
(ptone/scion#1092), that GKE and Cloud Run do **not** run the same image, and the
`scion-hub` artifact-name hazard; §19 makes `image.repository` required with no default.
New **§17.1** records the rationale-over-mechanism rule that falls out of three successive
corrections. See §11, §17.1, Phase 7, §18 items 24–27.

**Revision 3** — **corrects the readiness path from `/api/v1/readyz` to `/readyz`** (19
occurrences). The prefixed form came from the brief and does not exist: `server.go:3363`
registers `/readyz` on the mux and nothing else, and `auth.go:419-421` exempts it by
**exact** string match. A prefixed path would have failed twice — unrouted and
unauthenticated-exempt — so readiness could never pass and the GCLB backend could never go
healthy. See §9.2, and §13 for the CI-assertion lesson that falls out of it.

**Revision 2** — folds in `gke-deploy-lead`'s two updates of 2026-08-17 (sidecar approved;
auth becomes a two-mode discriminated union; hosted mode mandatory; `hub_id` and GCS blob
storage are hard preflight requirements; `ha-lead` owns the IAP-optional preflight split).
**Evidence base:** `parts/C-config-surface.md` (primary), `parts/B-deploy-today.md`,
`parts/D-ingress-auth-ha.md`, `parts/E-ci.md`, `lead-state.md`, `C-verification.md`.
Facts below are drawn from those sources plus direct re-reads of
`pkg/runtime/factory.go`, `pkg/runtime/k8s_runtime.go`, `pkg/hub/handlers_health.go`,
`pkg/config/settings_v1.go`, and `scripts/cloudrun/hub-settings-template.yaml`.
`config-surface.md` (top level) is superseded and was not used.

> **This is a design, not an implementation.** Every YAML and shell fragment below is
> illustrative pseudocode for the developer, not chart source.

---

## 1. Problem & Goals

Scion's hub is deployable today on Cloud Run only, via a hand-rolled `deploy.sh` and a
Secret Manager `settings.yaml`. There is **no Helm chart, no Kustomize base, and no
Kubernetes manifest for the hub anywhere in the repo** (`parts/B-deploy-today.md`). We
want a first-class, reviewable, repeatable GKE deployment.

**Goals**

| # | Goal |
|---|---|
| G1 | A single Helm chart, `deploy/helm/scion-hub`, that installs a working hub on a GKE cluster. |
| G2 | Hub **and** runtime broker in **one process, one pod** (`--enable-runtime-broker`, broker on `127.0.0.1:9800`). No broker subchart, no broker sidecar. |
| G3 | Cloud SQL Postgres as the store, reached through the Cloud SQL Auth Proxy. |
| G4 | Filestore NFS as the workspace backend (`workspace_storage.backend: nfs`). |
| G5 | Configuration delivered as a **rendered `settings.yaml`**, never as `SCION_SERVER_*` env vars. |
| G6 | Secrets never on argv. |
| G7 | GCLB Ingress, with a documented, non-magical answer to the backend-service-ID chicken-and-egg. |
| G8 | CI that fails on a broken chart: `helm lint` + `helm template | kubeconform`, reachable as `make` targets. |
| G9 | The chart never ships a control that is wired to nothing. Anything inert because of fork issue #1075, or because `ha-lead`'s preflight split has not landed, is either not rendered or loudly flagged. |
| G10 | **One chart, two auth modes** — `auth.mode: oauth` (primary) and `auth.mode: proxy`+`provider: iap` — expressed as a discriminated union on one config subtree, not as two deployment paths. **IAP is not required.** |
| G11 | Satisfy the HA preflight contract explicitly and stably: hosted mode, a fixed `server.hub.hub_id`, Postgres, GCS blob storage, and a durable session secret. |

**Success criteria.** A GKE Autopilot cluster, a Cloud SQL Postgres instance, a Filestore
instance and an OAuth client are the only prerequisites; `helm install` (in the two
documented steps) yields a hub that answers `200` on `/readyz`, serves the web UI
through IAP, and can start an agent pod.

---

## 2. Non-Goals

- **Provisioning GCP infrastructure.** The chart does not create the Cloud SQL instance,
  the Filestore instance, the GCS bucket, the OAuth client, the static IP, or any IAM
  binding. It *emits the exact `gcloud` commands* in `NOTES.txt` and validates that the
  operator supplied the resulting identifiers. (Alternative — Config Connector — rejected
  in §12.)
- **Building or publishing the image.** §11 recommends an image; producing it is a
  separate issue.
- **Fixing #1075.** Owned by `gke-deploy-lead`. This design assumes it lands and states
  precisely what is undeliverable until then (§14.1).
- **Designing or implementing the preflight split that makes IAP optional.** Owned by
  `ha-lead`. This design consumes `ha-lead`'s config-surface *sketch* and treats any
  divergence as a values rename, not a redesign (§6.5, §14.2).
- **Provisioning the GCS bucket** required by the HA preflight (§8.4).
- **Fixing the single-node hub components** (`ControlChannelManager`, `PresenceManager`,
  `PortTunnelManager`, `LocalDiskAttachmentStore`, in-process event bus,
  `settings.yaml` writes). The chart works *around* them (§4.2) and does not repair them.
- **A kind-based smoke test in CI.** Deferred, scoped in Phase 9.
- **Multi-cluster, multi-region, or non-GKE Kubernetes.** The chart hard-depends on GKE
  CRDs (`BackendConfig`, `FrontendConfig`, `ManagedCertificate`) and Workload Identity,
  each behind a toggle but not exercised elsewhere.
- **Agent-side image or harness configuration.** All eight `.design/kubernetes/*` docs
  concern agent execution and are out of date; treated as non-binding.

---

## 3. Proposed Design — overview

```
                     ┌──────────────────────── GCLB (global static IP) ───────────────────┐
   user ──HTTPS──►   │  ManagedCertificate · FrontendConfig(redirect,TLS policy)          │
                     │  BackendConfig(timeoutSec 3600, healthCheck /readyz,               │
                     │                IAP — only when auth.mode=proxy)                    │
                     └────────────────────────────┬──────────────────────────────────────┘
                                                  │ NEG (container-native LB)
                                        ┌─────────▼──────────┐
                                        │ Service :80 → 8080 │
                                        └─────────┬──────────┘
   ┌──────────────────────────────── Deployment: scion-hub (replicas: 1) ─────────────────────┐
   │                                                                                          │
   │  (no init container — the emptyDir IS the prepared $HOME/.scion; settings.yaml is a      │
   │   read-only subPath Secret mount over it, 0444 — §5.2)                                    │
   │  native sidecar cloud-sql-proxy : 127.0.0.1:5432 → Cloud SQL (Workload Identity)          │
   │  container      hub             : scion server start --enable-hub --enable-runtime-broker │
   │                                                     --enable-web --web-port 8080          │
   │                                   in-process broker on 127.0.0.1:9800                     │
   │                                   probes → /readyz  (exact path — §9.2)                 │
   │                                                                                          │
   │  volumes: scion-home (emptyDir)  ·  settings (Secret, ro)  ·  workspace (PVC, RWX)        │
   │           workspace mounted at  <nfs.mountRoot>/<nfs.shares[0].id>                        │
   └──────────────────────────────────────────────────────────────────────────────────────────┘
        │ Workload Identity (KSA → GSA)              │ RWX PVC → static PV → Filestore CSI
        ▼                                            ▼
   Cloud SQL · GCS bucket (hub blob store)       Filestore (agent WORKSPACES; also mounted
   · iamcredentials · gstatic                    by agent pods post-#1075)
   ── §8.4: GCS and Filestore are DIFFERENT subsystems; do not conflate them ──
        │
        │ in-cluster ServiceAccount (KUBECONFIG explicitly empty)
        ▼
   agent pods in <runtime.namespace>  ← Role/RoleBinding rendered by the chart
```

### 3.1 One process, one pod — and why a sidecar is still fine

The user's constraint is that **the hub and the runtime broker share a process**. It is
satisfied by `--enable-runtime-broker`, which starts the broker in-process on
`127.0.0.1:9800`; `scripts/cloudrun/hub-settings-template.yaml` already models this as
`server.broker: {host: 127.0.0.1, port: 9800, auto_provide: true}`.

"One process" is **not** "one container" — the user confirmed this directly: the
hub-broker constraint is "just about scion, not a limit on full deployment topology", and
"if sidecar is the correct way to do CloudSQL, it can be incorporated." `cloudsqlconn` is
absent from `go.mod`, so the hub cannot dial Cloud SQL natively; the Cloud SQL Auth Proxy
runs as a **sidecar container in the same pod** and the hub connects to it over loopback.
This is recorded here so no later reader construes "one process" as forbidding it.

The pod has **no init container**. It had one until revision 7; §4.1 records why it went and
why nothing needs to replace it. The Cloud SQL proxy is an `initContainers` **entry** but is
not an init container — it is a native sidecar (§7), and the distinction matters to anyone
writing an assertion over rendered output.

### 3.2 Chart layout and template inventory

```
deploy/helm/scion-hub/
  Chart.yaml                    # apiVersion v2, type application, appVersion = hub version
  values.yaml
  values.schema.json
  README.md                     # generated section + the two-step install runbook
  templates/
    _helpers.tpl                # names, labels, and ALL cross-field assertions (fail)
    NOTES.txt                   # post-install runbook + every non-fatal warning
    serviceaccount.yaml         # KSA + iam.gke.io/gcp-service-account annotation
    rbac-role.yaml              # Role in the agent namespace  (see §9.3 for verbs)
    rbac-rolebinding.yaml
    rbac-clusterrole.yaml       # only when runtime.listAllNamespaces
    rbac-clusterrolebinding.yaml
    secret-settings.yaml        # the rendered settings.yaml            (§5)
    secret-session.yaml         # session secret, only if inline-supplied (§6)
    configmap-env.yaml          # the *three* env vars that actually work (§5.4)
    deployment.yaml             # hub + Cloud SQL sidecar, probes, volumes (§4, §7, §10)
    service.yaml                # ClusterIP + cloud.google.com/neg
    pv-filestore.yaml           # static PV, CSI, RWX, resource-policy: keep   (§8)
    pvc-workspace.yaml          # RWX PVC bound to the PV, resource-policy: keep
    ingress.yaml                # gce class, static IP, cert                   (§10)
    backendconfig.yaml          # IAP, timeoutSec, healthCheck /readyz
    frontendconfig.yaml         # HTTPS redirect, SSL policy
    managedcertificate.yaml
    poddisruptionbudget.yaml    # only when replicaCount > 1
    networkpolicy.yaml          # opt-in egress allowlist                      (§9.4)
    tests/test-readyz.yaml      # helm test hook: curl /readyz
  ci/                           # values files exercised by CI (§13)
    values-minimal.yaml         # sqlite, local storage, no ingress
    values-cloudsql.yaml        # postgres + proxy, no ingress
    values-full-ha.yaml         # postgres + filestore + ingress + IAP
    values-bootstrap.yaml       # bootstrap.deferHub = true
  golden/                       # committed `helm template` output, diffed in CI (§13)
  crd-schemas/                  # vendored BackendConfig/FrontendConfig/ManagedCertificate
```

**Why one chart rather than two (hub + infra):** every toggle here is a property of one
deployment, and a split forces cross-chart value duplication for `mountRoot`, share ID,
and namespace — precisely the values whose divergence causes the silent failures in §8.2.

---

## 4. Deployment: pod shape

### 4.1 Containers

| Container | Kind | Purpose |
|---|---|---|
| `cloud-sql-proxy` | **native sidecar** (`initContainers` entry with `restartPolicy: Always`) | Cloud SQL connectivity (§7). |
| `hub` | container | `scion server start --foreground --hosted --enable-hub --enable-runtime-broker --enable-web --web-port 8080 --host 0.0.0.0 --auto-provide` |

> ### ⚠ TRIP-WIRE ON THIS TABLE: adding a container to this pod reduces the `/api/v1/system/*` defence from two gates to one
>
> This is a property of the **pod spec**, not of any one phase, and it is stated here rather
> than in §7 because §7 is merely the first phase to trip it.
>
> `/api/v1/system/init` is defended twice: the route is registered wrapped in
> `requireWorkstation` (`pkg/hub/server.go:3594`), and `handleSystemInit`
> (`pkg/hub/system_handlers.go:446`) *additionally* calls `assertLoopback` at `:452`.
> `assertLoopback` (`pkg/hub/server.go:3705`) parses the host out of `r.RemoteAddr` and
> requires `ip.IsLoopback()` — the transport peer address, not a forwarded header.
> **Every container in a Kubernetes pod shares one network namespace**, so any co-resident
> process connecting to `127.0.0.1` presents a loopback `RemoteAddr` and **satisfies
> `assertLoopback` by construction**. Verified independently by `gke-deploy-lead` and
> recorded as §18 item 34.
>
> Nothing is exposed today: `requireWorkstation` still holds, and it is the gate actually
> doing the work. The trip-wire is that after the *first* additional container the defence
> is **single, not layered** — and the surviving gate is the one nobody has been treating as
> load-bearing. The Cloud SQL proxy trips it; so would an IAP proxy, a metrics agent or a
> log shipper, and none of those authors would think to read §7.
>
> **What is owed by whoever adds a container:** keeping `requireWorkstation` shut, and a
> test that fails if it stops holding. **What is not owed:** re-hardening `assertLoopback`.
> That is a separate decision, and inventing a fix at the point of tripping the wire would
> be a refusal without a harm (§17.1 rule 10).

**There is no init container, and the reason is not "we removed the copy".** A `settings-init`
container survived revisions 3–6 in a reduced role — *prepare the directory, do not copy the
file* — after revision 5 correctly killed the copy. Revision 7 removes it, because the reduced
role was **already nobody's job**:

- **The directory needs no preparation beyond existing, and the kubelet does that.** An
  `emptyDir` is created before any container in the pod starts. `$HOME/.scion` itself is the
  only thing that has to be there.
- **The subdirectories the init container was creating are not needed in hosted mode.** The
  `storage/` and `templates/` example came from `scripts/cloudrun/entrypoint.sh:6`, which runs
  in a **non-hosted** shape. `cmd/server_foreground.go:110-117` refreshes local templates only
  in the `else if !hostedMode` branch, and its own comment says hosted mode "bootstraps directly
  into the Hub via `BootstrapBundledResources`, **bypassing local `~/.scion` materialization**".
  The chart mandates hosted mode (§6.5), so it is on the branch that never materialises them.
- **`runAsNonRoot` makes the ownership job impossible anyway.** The init container ran as uid
  1000, so it could not have `chown`ed anything it did not already own; its only real power was
  `mkdir`.

**Do not add one back to satisfy a document that still mentions it.** An init container with
nothing to do is worse than none: it renders, it passes review, and the next reader gives it a
job — most likely copying `settings.yaml`, which is the ptone/scion#1091 defect shape §5.2
exists to refuse. This paragraph is the positive twin of that refusal: §5.2 says what must not
happen, and this says why nothing needs to.

**`--production` is deliberately not emitted.** `cmd/server.go:235-236` binds it to the same
variable as `--hosted` and marks it deprecated, so passing both changes nothing and prints a
deprecation warning on **every boot**. That trains operators to ignore boot warnings, which is
expensive on a deployment whose characteristic failure mode is a warning nobody reads (§5.5,
§13.1). Cloud Run's `entrypoint.sh` passes it; the chart does not.

**Hosted mode is mandatory and is not a tuning knob** (§6.5). `--host 0.0.0.0` is passed
explicitly for the same reason.

The hub command lives in `deployment.yaml` as `args:` rendered from
`.Values.hub.args`, **not** baked into the image, so it can change without a rebuild.
`_helpers.tpl` asserts that no rendered arg matches `(?i)secret|password|token` — the
mechanical guard against re-introducing fork issue #1070
(`setup-gcp.md:801` puts the session secret on argv today;
`scripts/cloudrun/deploy.sh:160` does it correctly).

**`--global` is carried over from the Cloud Run arg set pending confirmation** (Open
Question Q7).

### 4.2 Replicas — default **1**, deliberately

Two independent findings must not be conflated:

- **Schema migration is multi-replica safe.** `migrateStore` serialises on
  `pg_advisory_lock(0x5C100008)`; three replicas racing real Postgres was verified
  empirically. **Therefore: no migration Job, no Helm hook, no "install at 1 then scale"
  contract.** The chart ships none of those.
- **Several hub features are single-node.** `ControlChannelManager`,
  `PortTunnelManager`, `PresenceManager`, `LocalDiskAttachmentStore` and the in-process
  event bus hold state in the pod (`C-config-surface.md` §4.5). An agent's control
  channel terminates on one pod; a web-terminal, log-tail, exec or port-forward request
  that lands on any other pod fails. With N replicas that is roughly (N−1)/N of attempts
  (§7.5).

GCLB session affinity does **not** repair this: it pins a *client* to a pod, but the
agent's control channel is on an arbitrary pod, so the pairing is still a coin flip. The
only reliable mitigation available to a chart is `replicaCount: 1`.

**Decision:** `replicaCount: 1` by default; the schema permits more; `NOTES.txt` prints a
prominent warning whenever `replicaCount > 1` naming the exact features that break. This
is Open Question **Q1** — it trades availability for a working web terminal and needs the
user's call.

**Consequence for update strategy:** with `replicaCount == 1` and `backend: nfs`, a
rolling update briefly runs two pods that both write `<share>/hub-projects` — the same
shared-writer hazard as `replicaCount: 2`. The chart therefore defaults
`updateStrategy` to **`Recreate` when `replicaCount == 1`** and `RollingUpdate`
(`maxUnavailable: 0`) otherwise, with an explicit override. Cost: a few seconds of
downtime per upgrade. Given the hub already loses PTY sessions across a restart, this is
cheap.

### 4.3 Volumes

| Volume | Source | Mount | Why |
|---|---|---|---|
| `scion-home` | `emptyDir` | `$HOME` (`/home/scion`) | `$HOME/.scion` is the hub's **state** directory (storage, templates, `scion-token`), so it must be writable and **per-pod** (§5.2). |
| `settings` | `Secret`, **`defaultMode: 0444`** — leading zero **mandatory**, unquoted | `$HOME/.scion/settings.yaml` via **`subPath`** (ro) | Writable directory, read-only file (§5.2). **`0444`, not `0600`**: Secret files are projected **root-owned** and the process is uid 1000, so owner-only bits make the file unreadable — and the symptom is a Block-1 preflight error naming the wrong thing. `0444` is also correct whether the group is `root` or the pod's `fsGroup` (§4.4), which is why it cannot be broken by a later phase. **Never write `444`**: YAML 1.1 reads that as decimal 444 = octal `0674`, group-writable and group-executable, accepted silently (§5.2). |
| `workspace` | `PersistentVolumeClaim` (RWX) | `{{ nfs.mountRoot }}/{{ nfs.shares[0].id }}` | Filestore. The mount path is **derived, never free-form** (§8.2). |
| `tmp` | `emptyDir` | `/tmp` | Allows `readOnlyRootFilesystem` later; also the agent startup gate marker path in the local backend. |

### 4.4 Security context

```yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser:  {{ .Values.hub.securityContext.runAsUser  | default 1000 }}
  runAsGroup: {{ .Values.hub.securityContext.runAsGroup | default 1000 }}
  {{- if eq .Values.workspaceStorage.backend "nfs" }}
  fsGroup:    {{ .Values.workspaceStorage.nfs.gid }}     # gated — see below
  {{- end }}
  seccompProfile: { type: RuntimeDefault }
containerSecurityContext:
  allowPrivilegeEscalation: false
  capabilities: { drop: [ALL] }
```

**`fsGroup` is rendered only when `backend: nfs` (revision 6). It has no default.** Earlier
revisions rendered it unconditionally as `workspaceStorage.nfs.gid | default 1000`, and the
reason for the change is **coupling, not `fsGroup` semantics**: a value named for one
subsystem was setting a **pod-wide** field that governs group ownership of **every** volume in
the pod — including while that subsystem was switched off entirely. The settings mount's group
ownership was being decided by a Filestore value, and **nobody chose that**. Gating makes the
blast radius match the intent. It is a design edit against unbuilt Phase 4 work, so it costs
nothing today; it would not have been free in three phases' time.

When it *is* rendered, `fsGroup` equals `nfs.gid`, matching `ApplyNFSDefaults` (uid/gid 1000).
**Open for Phase 4, deliberately not settled here: whether `fsGroup` should exist at all.** The
CSI default-policy finding below suggests it may do nothing for Filestore either — which would
make it a field that applies to every volume **except the one it was added for**. That reading
comes from the *upstream* driver manifest, is unverified against GKE's managed deployment, and
needs a cluster: `kubectl get csidriver filestore.csi.storage.gke.io`. Gate it now; decide its
existence when someone can run that. Note that
`fsGroup` semantics on a CSI-mounted NFS share depend on the driver's `fsGroupPolicy` and
must not be relied on for ownership — the upstream Filestore CSI driver declares no
`fsGroupPolicy`, so it takes the default `ReadWriteOnceWithFSType`, under which `fsGroup` is
**not applied to a `ReadWriteMany` NFS volume at all** (upstream manifest; GKE's managed
deployment may differ, and `kubectl get csidriver filestore.csi.storage.gke.io` settles it) — the load-bearing controls are `runAsUser`/
`runAsGroup` matching the values written into `settings.yaml`'s `nfs.uid`/`nfs.gid`, and
the Filestore export's own squash configuration. `_helpers.tpl` asserts
`securityContext.runAsUser == nfs.uid` and `runAsGroup == nfs.gid` when
`backend: nfs`, because a mismatch produces unwritable project directories that surface
much later as agent failures.

`runAsNonRoot: true` **requires the image change in §11** — `Dockerfile.hub` declares no
`USER` and runs as root today.

---

## 5. Configuration intake: a rendered `settings.yaml`

### 5.1 Why not environment variables

Not a style preference. Three independent loaders with three different mappers consume
`SCION_SERVER_*`; the load error is discarded and unmatched keys are ignored, so a wrong
spelling is a **silent no-op** (`C-config-surface.md` §0). Concretely:

- **`SCION_SERVER_DATABASE_*` and every `SCION_SERVER_OIDC_*` are unreachable by any env
  spelling** (snake_case koanf tags). Those are exactly the settings a chart would reach
  for env vars to deliver — including `database.max_open_conns`, whose floor of 2 is
  load-bearing (§7.4).
- The repo's own JSON schema advertises names that do not work (fork issue #1081).
- Do **not** over-correct into "underscored names never work": three *do*, via direct
  `os.Getenv` — `SCION_SERVER_ADMIN_MODE`, `SCION_SERVER_MAINTENANCE_MESSAGE`,
  `SCION_SERVER_BASE_URL`. Those three are the only env the chart emits (§5.4).
- Six keys are nested under `server:`, not top level: `notification_channels`,
  `message_broker`, `native_chat`, `plugins`, `scheduler`, `github_app`. A rendered file
  that places them top level is silently not read. The template must place them
  correctly; a golden-file test locks it (Phase 1 acceptance).

### 5.2 Why a Secret, and how it is mounted: writable directory, read-only file

**The chart delivers `settings.yaml` as a `subPath` Secret mount at
`$HOME/.scion/settings.yaml`, `defaultMode: 0444`, over an `emptyDir`-backed `$HOME/.scion`.
The directory is writable; that one file is not.** `settings.yaml` is read from `$HOME/.scion/settings.yaml`
with **no override env** — the only lever is `HOME` (`C-config-surface.md` §2.1) — so the
path is fixed and the only question is what sits at it.

**The mount's authority depends on the config path not being redirectable, and that is not
settled by the mount.** `cmd/server.go:237` registers `--config` / `-c`
(`StringVarP(&serverConfigPath, "config", "c", ...)`). `--config` and `-c` are reserved in
**Phase 0's reserved-flag deny-list** (§17 Phase 0, §19 `hub.args`); found by `gd-p0-rev`, and
`gd-p0-dev` owns it. Note the asymmetry the deny-list must respect: `--config` is reserved
because **nothing** may ever pass it, which is a different reason from "the chart already sets
it" and must not be filed with it. **Four reasons, four groups — see Phase 0**, which names the
reasons; the membership lives in `scion-hub.hubArgs`.

> **⚠ ERRATUM, revision 8 — the mechanism given for this reservation through revision 7 was
> FALSE, and the reservation is still right.** Revisions 5–7 stated that `--config` "points the
> hub's configuration load at a different file entirely" and so "bypasses everything in this
> section". It does not, on this chart. `gd-p1-rev` found the real load path and it was verified
> here independently: `LoadGlobalConfig` (`pkg/config/hub_config.go:628`) calls
> `loadGlobalConfigFromSettings` (`:640`), which resolves `GetGlobalDir()` **first and
> unconditionally** and reads `configPath` **only if the global lookup did not find anything**
> (`:647-660`). `found` is true exactly when `$HOME/.scion/settings.yaml` parses and carries a
> non-nil top-level **`server:`** key (`loadServerFromSettingsFile`, `:1331`, decided at
> `:1344-1347`). **The trigger is the KEY, not the presence of a FILE** — `:1331-1347` never
> tests for existence in the sense that matters; it reads whatever is there and returns
> `(nil, false)` unless `raw["server"]` is present *and* non-nil. **Once Phase 1 renders that
> key, `found` is true and `configPath` is never read for loading. Before Phase 1, it is not —
> see the second erratum immediately below.**
>
> **Under this chart `--config` is SILENT, not merely deprecated** — worth stating precisely,
> because "it only prints a deprecation warning" was the first correction offered and it is also
> wrong, in the direction that matters. There is no deprecation warning on the flag:
> `MarkDeprecated` is registered only for `production` (`cmd/server.go:236`, `:290`), never for
> `config`. The only `configPath`-dependent output in the load path is the **`server.yaml`**
> deprecation notice at `hub_config.go:678`, which additionally requires `hasServerYAML(dir)`
> (`:1393`) — a `server.yaml` or `server.yml` beside the `--config` target. The chart creates
> neither, anywhere. So an operator who appends `--config` gets **no error, no warning and no log
> line**, and the flag is accepted and ignored. That makes it the fourth instance of *config
> accepted, silently does nothing* — and, because the silence is a consequence of the rendered
> output rather than of the binary, **the only one of the four that can stop being one with no
> code change at all.**
>
> **The true reason is better than the false one, because it names a cross-phase dependency
> nothing else in this document records.** `--config` is inert here **not as a property of the
> binary but as a property of Phase 1's rendered output** — one top-level `server:` key. A Phase 1
> refactor that changed the settings document's top-level shape, or any values permutation
> rendering no `server:` key, flips `found` to false and makes `--config` **live**, with exactly
> the redirect the old text wrongly claimed was always available. That is rule 8's shape
> precisely: **closed by a configuration, therefore deferred to whoever changes the
> configuration.** So the reservation stands, and Phase 1 acquires a constraint it did not know
> it was carrying — *the top-level `server:` key is load-bearing for a Phase 0 guard.*
>
> Recorded rather than struck (rule 6), because this is the **third** reservation-on-a-false-
> reason found today and the pattern is the point: a true conclusion resting on a checkable,
> wrong reason is deleted by the next maintainer who checks the reason, and **the deletion looks
> like tidying**. Same shape as Phase 0's group-1 comment (direction B) and as instance 9. The
> claim also propagated: it was asserted in a brief, written into the chart's group-2 comment,
> and repeated here, without anyone reading the load path — three surfaces, one unverified
> sentence, which is why §17.1 rule 4's sourcing corollary now covers *mechanism* claims about
> the hub and not only membership claims about the chart.

> **⚠ SECOND ERRATUM, revision 8 — the erratum above was keyed to the wrong phase, and the
> consequence is that `--config` IS LIVE ON THE CHART AS IT STANDS TODAY.**
>
> **THE SETTLED STATEMENT, and it is the only one that belongs in a chart comment:**
> ***`--config` is live at Phase 0 because the global settings document has no non-nil top-level
> `server:` key.*** Not because no file exists. Not because the hub creates one. Not because of
> who mounts what. It is settled precisely because it **survives all three mechanisms that touch
> that question**, and each of the other three formulations has now been wrong once.
>
> The two-state form, keyed to the transition (§17.1 rule 12): ***`--config` is live today; it
> becomes inert when a `settings.yaml` carrying a non-nil top-level `server:` key is present at
> `$HOME/.scion/` — which Phase 1's Secret is the only thing that will ever do.***
>
> **Why "live" is the right word, both routes.** With no `server:` key,
> `loadServerFromSettingsFile` returns `(nil, false)` at `:1344-1347` — route A never reaches
> `ConvertV1ServerToGlobalConfig` (`:1360`) — so `loadGlobalConfigFromSettings` reads
> `configPath` at `:647-660`, **and** `LoadGlobalConfig` falls through to
> `loadGlobalConfigLegacy` (`:635`), whose layering reads `configPath` directly
> (`:772-788`, the `file.Provider(configPath)` branch). *(Line numbers per `gd-p0-rev-2`,
> re-verified here: `gd-p1-dev` had `:1359` and `:777-787`; `:1359` is the closing brace of the
> preceding error block, and `:777-787` omits the global-config step that makes it a layering.
> Rule 11's corollary firing on its first day — right file, right function, one line off.)*
>
> **This is the THIRD revision of this one fact, and recording it as a sequence is the point
> (rule 6).** Every version had the same conclusion — reserve the flag — so the conclusion never
> flagged the reason:
>
> | # | Stated reason | Author | Why it was wrong |
> |---|---|---|---|
> | 1 | "`--config` points the load at a different file, bypassing this section" | revisions 5–7 | describes no code path; `GetGlobalDir()` is tried first and unconditionally |
> | 2 | "the chart always renders the `server:` key, so `found` is true" | this document, first erratum | true of **Phase 1's** render; the comment carrying it lives in **Phase 0's** chart |
> | 3 | "no settings file exists in the image, so the lookup fails" / "the hub creates one on every boot" | `gd-em`, corrected by `gd-em` | both are about the file; the guard at `cmd/server_foreground.go:104` is an `os.Stat` on the **directory**, so `InitGlobal` fires only when `$HOME/.scion` is absent — true today, false the moment any phase mounts there |
>
> The Phase 0 conclusion is unmoved by all of it, because **an empty mounted directory has no
> `server:` key either**. That is what makes the key formulation settled and the other three
> incidental.
>
> **The named consequence, without which this reads as pedantry** (rule 14). The conclusion
> "reserve `--config`" survives every version of the reason, so the reason attracts no scrutiny
> — but *which* reason is believed decides what Phase 1 must deliver. Believe the **file** and
> Phase 1 discharges the dependency by mounting *any* Secret, then nests server config under a
> `profiles:` entry or renames the top-level key, and `--config` is **live with the comment
> still saying inert** — and the embedded defaults already carry a `profiles:` tree, so that is
> the shape closest to hand. Believe the **key** and Phase 1's obligation is exact and testable:
> every values permutation renders a non-nil top-level `server:`. One surviving conclusion, two
> different deliverables.

This **inverts** the rationale of earlier revisions, which copied the Secret into a writable
file *because the hub writes it*. The writable copy is not an accommodation of hub behaviour.
**It is the defect** (ptone/scion#1091), and the chart declines to reproduce it. Cloud Run
copies (`scripts/cloudrun/entrypoint.sh:8`); we deliberately do not follow it.

**The directory must stay writable, because `$HOME/.scion` is the hub's entire *state*
directory — not a config directory.** ha-lead's confirmed target state makes the settings
**file** read-only, **not** the directory, and that distinction is the whole design. There is
**no `SCION_HOME` and no config-path override**: `GetGlobalDir` is `os.UserHomeDir()` +
`/.scion`, hardcoded (`pkg/config/paths.go`), so `HOME` is the only lever that moves any of
it. A whole-directory read-only mount there breaks the hub **for reasons that have nothing to
do with `settings.yaml`** — `scripts/cloudrun/entrypoint.sh:6` creates `storage/` and
`templates/` under it and `cmd/hub.go:423` reads `$HOME/.scion/scion-token`, but those are
today's three examples of a set that will grow, not the argument. The argument is that the
chart would be making a state directory read-only. Hence: the Secret is mounted **one file
deep** with `subPath`, over an `emptyDir` that supplies the writable directory. **Nothing
prepares that directory** — the kubelet creates the `emptyDir` before any container starts, and
hosted mode does not materialise anything under it (§4.1).

**Why the file must not be writable: the write-back into shared DB truth.** Pod-local loss is
the visible symptom; it is not the hazard. `syncHubSettings`
(`cmd/server_foreground.go:1898`) performs an **every-boot** re-sync — for every registered
section whose row is absent or whose `origin` is still `"seeded"`, it writes that section from
`bootstrapKoanf`, and `LoadBootstrapKoanf` reads the **pod-local** `settings.yaml`. A replica
that has written its own copy therefore **promotes its private divergence into shared DB
state** at its next boot. Tracked as **ptone/scion#1091**. This section describes what the
chart does and why; it does not design that fix.

**The race needs an in-place container restart, not a pod replacement** — narrower than
"every restart", and worse for being narrow. An `emptyDir` dies with the pod but **survives a
container restart**, and init containers **run once per pod**: they do not re-run when the
kubelet restarts a container in place. A liveness kill, an OOMKill or a panic-restart
therefore brings the hub back up reading the file it corrupted before it died, with no
re-seed and nothing in the pod's events saying so. It triggers preferentially when the hub is
already unhealthy, which is the worst moment to start reading divergent configuration.

**Why a `subPath` mount and not a read-only *copy* (any mode).** This is the part that does
not survive paraphrase. **A file mode does not stop a rename.** `rename(2)` needs write permission on the
**directory**, not on the file — and the directory must stay writable, per above.
`pkg/config/settings_v1.go:2694` calls `os.Rename(settingsPath, backupPath)` inside
`MigrateSettingsFile`. Against a mode-only copy that rename **succeeds**, silently moving our
configuration aside and leaving the hub to carry on against whatever is written next; against
a `subPath` bind mount it fails with `EBUSY`. Mode protects the contents; the bind mount
protects the **name**, and the name is what is under attack.

`gke-deploy-lead` swept `os.Rename` across `pkg/config` and the surface is larger than that
one call: `settings_v1.go:2702` renames `server.yaml` (non-fatal in code), and `:2714`/`:2716`
are the rollback pair that restores both backups if the rewrite fails. **Under the `subPath`
mount none of the rollback surface is reachable** — `:2694` `EBUSY`s first and returns. That
is a point in favour of the mount and worth recording: it fails at the **first** rename rather
than partway through a multi-file shuffle whose rollback would itself be operating on a
bind-mounted path.

**The Secret's `defaultMode` is `0444`, and the reason must travel with the number.** A bare
`0444` in a manifest looks loose, and the next reader will tighten it to `0600` and break the
install. **`0600` is wrong here.** Under the old *copy* shape it was correct: the init
container ran as uid 1000, so it **owned** the file it wrote and the owner bits applied. A
Secret volume is not that — Kubernetes projects Secret files **owned by `root`** on tmpfs
(group `root`, or the pod's `fsGroup` where one is set — §4.4), while the pod runs
`runAsUser: 1000` with `runAsNonRoot: true`. `defaultMode: 0600` therefore yields
`-rw------- root …`, which uid 1000 **cannot read at all** under either projection, because
the failure is **owner-only bits on a file the process does not own**. Nor does it fail
legibly: the hub comes up on defaults and, in hosted mode, dies on the Block-1 preflight
naming some missing key (§6.5) — a diagnosis pointing nowhere near a file permission. The
file is projected root-owned and the process is uid 1000, so it **must be readable by
other**.

**And that last clause is the strongest argument the `0444` decision has, so it is stated
plainly: `0444` is correct under *both* projections — `root:root` and `root:<fsGroup>`.** That
is not a lucky escape. It is the property that makes it the right answer. **A mode that is
invariant to a pod-wide field set by an unrelated subsystem cannot be silently broken by a
later phase changing that field.** `fsGroup` is exactly such a field: Phase 4 owns it, it is
named for Filestore, and it governs group ownership of every volume in the pod (§4.4). Any mode
that depends on the group bit — `0440` being the obvious candidate — makes this file's
readability a downstream consequence of a Filestore value. `0444` does not, and that
independence is worth more than the group bit ever bought.

**⚠ The leading zero is load-bearing — a general rule, not a fact about this field.**
Kubernetes parses manifests as **YAML 1.1**, in which a leading zero means **octal**.
`defaultMode: 0444` is decimal **292**, i.e. `r--r--r--`. Drop the zero and `444` is read as
**decimal 444** = octal **0674** — `rw-rwxr--`: **group-writable and group-executable**. That
is a perfectly valid mode, it is accepted **silently**, and nothing anywhere errors. So a
cosmetic simplification of one character inverts the entire security argument for this mount,
which *is* a file mode and nothing else. **Every file mode in this chart must be written with
its leading zero and unquoted.** This applies to every later phase that sets a mode — Phase 3's
secrets and Phase 4's Filestore both will — which is why it is stated here as a rule rather than
left in a template comment next to one field.

**The four spellings, decoded — measured, not reasoned.** `gd-p1-dev` decoded each through
`sigs.k8s.io/yaml` into `k8s.io/api` `corev1.SecretVolumeSource` — the same YAML→JSON→typed-struct
path the apiserver uses, at the versions this repo already depends on (`k8s.io/api` v0.35.0,
`sigs.k8s.io/yaml` v1.6.0):

| Written | Decodes to | Effective mode | Verdict |
|---|---|---|---|
| `defaultMode: 0444` | 292 | `r--r--r--` | **correct — write this** |
| `defaultMode: 444` | 444 | `rw-rwxr--` | **accepted silently, wrong** |
| `defaultMode: "0444"` | — | — | **hard error**, see below |
| `defaultMode: 0o444` | 292 | `r--r--r--` | correct, but see below |

**Row 2 is the whole argument for the rule, and it is now confirmed empirically rather than
inferred:** `444` produces no error and no warning, and the file it yields is **group-writable
and group-executable**.

***Quoting the literal (`"0444"`) was considered and is REJECTED — it does not install.***
`defaultMode` is an `int32`, and a JSON string does not unmarshal into one:
`json: cannot unmarshal string into Go struct field SecretVolumeSource.defaultMode of type
int32`. `kubeconform -strict` catches it too — *"expected integer or null, but got string"* —
so it fails at **validate/install time, not at runtime**. That is the one consolation: the idea
is wrong but not dangerous, because it cannot ship a wrong mode, only a chart that will not
install. Recorded here rather than dropped because it is a natural idea and the next person to
have it should find it answered.

***`0o444` (YAML 1.2 octal) also decodes correctly, and is still not what we write.*** Anyone
arguing for it is right about the mechanism, so the reason is not mechanical: `0444` is the
spelling every Kubernetes example in the world uses, and an unfamiliar-looking literal invites
someone to "fix" it back — which is precisely the failure this rule exists to prevent.

**Rejected: `0440` plus a pod-level `fsGroup: 1000`.** It also produces a readable file —
`fsGroup` rewrites group ownership on the volume — and it was **rejected because it buys no
isolation**. Note the form of that claim, because an earlier draft stated it weakly, as a
principle about what `fsGroup` *would* do. It is not a hypothetical: **with `backend: nfs` this
chart renders `fsGroup` today** (§4.4), so the Cloud SQL proxy sidecar (Phase 2, §7.1) receives
that gid as a supplemental group and **reads the file — now, as designed**, not in some
imagined configuration. `0440` is the *appearance* of a boundary rather than a boundary, and
`0444` states the same access truthfully. **A present objection and a hypothetical one are not
the same argument**; this is the present one, it is decisive on its own, and the decision rests
on it alone.

Secondary, and only that: `fsGroup` and this mount are **coupled across phases** in a way the
chart does not need — which is why §4.4 now gates `fsGroup` on `backend: nfs`. Note what this is
**not** claiming — an earlier draft of this paragraph asserted that `fsGroup` would force a costly recursive `chown`
across the workspace share at every pod start. It would not, on two independent counts:
`fsGroupChangePolicy: OnRootMismatch` skips the walk when the volume root already matches, and
**`fsGroupPolicy` defaults to `ReadWriteOnceWithFSType`**, under which `fsGroup` is applied only
when an fstype is defined *and* the access mode includes `ReadWriteOnce` — Filestore is
`ReadWriteMany` NFS, so `fsGroup` would not be applied to it at all. Two caveats travel with
that: it was read from the **upstream** `kubernetes-sigs/gcp-filestore-csi-driver` CSIDriver
manifest (which declares only `attachRequired: false` and `podInfoOnMount: true`), and **GKE's
managed deployment may differ** — one `kubectl get csidriver filestore.csi.storage.gke.io` away,
on a cluster nobody here has. And `fsGroupPolicy` governs **CSI volumes only**: Secret volumes
and `emptyDir` are unaffected by any of it, so **none of this touches the `0444` decision**.
The cost is therefore not "high"; it is **Phase 4's to determine**, not this section's to assert.

**Which plank is holding:** reason 1 alone. It is verified from the pod spec's own semantics
and needs no cluster. Everything in the paragraph above is a *withdrawal* of a second reason,
not a second reason — recorded so the next reader can see that the decision does not depend on
any of it.

**Forward note, and the §4.4 inconsistency that produced it — RESOLVED in revision 6.** Through
revisions 2–5 this section reasoned as though `fsGroup` were absent while §4.4 rendered it
**unconditionally**, defaulted from `workspaceStorage.nfs.gid`. Two things came out of that.
**The ruling:** §4.4 now gates `fsGroup` on `backend: nfs`, on coupling grounds — a
Filestore-named value must not decide the group ownership of every volume in the pod while
Filestore is switched off. **The correction:** `fsGroup` **does** apply to Secret and `emptyDir`
volumes (the CSI `fsGroupPolicy` discussion above governs **CSI** volumes only), so with the NFS
backend on, this file is projected `root:<fsGroup>` rather than `root:root` — the phrasing above
is corrected accordingly, and the `0600` diagnosis is unchanged because that failure is
**owner-only**, not group. `0444` is unaffected under either projection, which is the invariance
argument made above. **Do not "fix" any of this by tightening the mode.** The forward note that
remains: if a later phase changes what `fsGroup` renders, re-read the invariance argument — it
is what makes such a change safe, and it is only safe while the mode does not use the group bit.
Whether `fsGroup` should exist *at all* is an open question for Phase 4 (§4.4), not this
section's to settle.

**The options, so the next reader does not re-derive them:**

- **(a) `subPath` Secret mount at `$HOME/.scion/settings.yaml`, `defaultMode: 0444`, over an
  `emptyDir` home — CHOSEN.** Directory writable, file unwritable and unrenameable.
- **(b) Copy into a writable file from an init container (any mode) — REJECTED.** This is the
  shape that carries the #1091 write-back, and mode alone does not stop the rename.
- **(c) Copy in the container's ENTRYPOINT rather than an init container — FALLBACK, keep it
  recorded** (`gke-deploy-lead` asked for this explicitly). It is why **Cloud Run does not
  have this problem**: an entrypoint **re-runs on every container start**, an init container
  does not, so an in-place restart re-seeds the file instead of inheriting the corrupted one.
  Take (c) if (a) ever fails on a cluster; it closes the restart race but not the write-back,
  so it is a fallback and not a co-equal.
- **(d) Init container symlinking `$HOME/.scion/settings.yaml` at the read-only `/etc/scion`
  mount — REJECTED.** Writes through the symlink give `EROFS`, which looks equivalent, but a
  rename renames the **symlink** and succeeds — reintroducing exactly the hazard it appears to
  close.

Three of these four name an init container. **All three are rejected, and the pod has none at
all** (§4.1) — the list records shapes that were considered, not components that exist.

**Known cost of `subPath`: it does not receive updates when the Secret changes.** A rotated or
edited Secret will not reach a running pod. **Mitigation, named and required: a checksum
annotation over the rendered Secret on the pod template**, so a config change rolls the
Deployment. The chart wants that regardless — without it a config change leaves replicas on
mixed configuration for as long as they happen to live — so this cost is paid by a mechanism
we would ship anyway. It is a known cost with a named mitigation, not a footnote.

**Load-bearing side effect: mounting anything at `$HOME/.scion` suppresses `InitGlobal`.**
`cmd/server_foreground.go:104-108` calls `config.InitGlobal` **only when the global directory
does not exist**. Mounting makes it exist, so that path never runs, and **nothing else fills
the gap**: the `else` branch that refreshes templates is guarded `else if !hostedMode`, and the
chart is always hosted. So an installed hub has a `$HOME/.scion` containing exactly one file —
the projected `settings.yaml` — and creates the rest as it needs it.

**Two independent mechanisms are at work here and revision 7 initially conflated them**
(`gd-p1-dev`). Keep them apart, because they have different triggers:

1. **`InitGlobal` is suppressed by the MOUNT, not by hosted mode.** It *does* run in hosted mode
   — `InitMachineOpts{SkipRuntimeCheck: hostedMode}` is passed precisely so it can — and it
   would create `agents/` and `harness-configs/`. It never fires only because
   `os.Stat(globalDir)` finds the directory present, and the `emptyDir` is what makes it present.
2. **Template refresh is suppressed by hosted mode, not by the mount.** That is the
   `else if !hostedMode` branch, and `:112-113` explains it: hosted mode "bootstraps directly
   into the Hub via `BootstrapBundledResources`, bypassing local `~/.scion` materialization".

**So the chart's own volume changes which startup path the hub takes.** Not the settings mount —
the `emptyDir` underneath it, which exists for an unrelated reason (keeping the directory
writable). That is a deployment-introduced behaviour change, not hub behaviour, and it is the
kind of thing that is invisible from both sides: the chart author sees a volume, the hub author
sees a `Stat`.

**It is believed harmless, and the part that would not have been is checked.** `InitMachine`
seeds a default `settings.yaml` only under `if settingsPath == ""` (`pkg/config/init.go`), so
even had it run, it would not have written over the mounted file — the one outcome that would
have been serious. Recorded as *checked*, not as *assumed*: "believed harmless" is the phrase
this project has spent the day correcting.

Until revision 7 this paragraph justified the suppression as "the chart owns the directory's
contents" — true only while a `settings-init` container was populating them. That container is
gone (§4.1), so the old reason would now be circular. Note what the corrected version makes
load-bearing: **§5.2 is safe because the chart is hosted-only *and* because the mount exists.**
If a later phase makes hosted mode optional, or moves the writable directory, this paragraph and
§4.1 are both re-derived rather than re-read.

**✅ ANSWERED (`ha-lead`, 2026-08-17) — the chart CAN ship ahead of ptone/scion#1091.**
This paragraph replaces the revision-5 placeholder, which asked whether a settings-write
failure is a logged warning, a failed request, or fatal to the process. **The answer is: all
soft.** No write path to `settings.yaml` calls `log.Fatal`, `os.Exit`, or panics. W1 (the
GitHub App `PUT`) logs `slog.Warn`, swallows the error, and the handler still returns **HTTP
200**. Of the W2–W7 integration writers, four log a warning and swallow; one returns HTTP 500
to the client while the server continues. W9 (startup) logs a warning and continues. W8 is
file-mode only and unreachable in Postgres mode; W10–W12 are workstation-gated and unreachable
in hosted mode. Nothing crashloops, so Phase 1 delivery is unblocked.

**The operational consequence, stated specifically, because "it ships" is the uninteresting
half.** Pre-#1091 the hub **cannot write `settings.yaml` at all**. Some of those writes log a
warning; none of them fail loudly to the caller. Concretely: an operator who configures a
GitHub App through the API gets **HTTP 200 and no effect, indefinitely**, until #1091 lands.
That must be documented where an operator will meet it — `VALIDATION.md` and `NOTES.txt` —
citing #1091, not left to be inferred from this design.

**Why the new failure is better even though it looks worse.** Read quickly this is a
regression: writes used to work and now silently do not. It is the opposite. Previously the
write **succeeded**, into a pod-local file that was lost at the next pod replacement — so the
feature appeared to work for a while, and worse, `syncHubSettings` could promote that private
write into shared DB truth on the next boot. Now the write never persists and **nothing
diverges, because no write lands**. Consistent beats intermittent: a behaviour that never
works is discoverable on the first attempt and documentable; one that works until the pod is
rescheduled is discovered in production by whoever inherits it.

**The writer inventory lives in ha-lead's table, not here.** See the **W1–W13** table in
`/scion-volumes/scratchpad/projects/ha/investigations/settings-writes.md` (§1a, with the
DB-mirroring classification in §3 and hosted-mode reachability in §4). The ad-hoc list of
eight call sites that stood here has been removed, and the reason matters more than the
correction: ha-lead's table was produced by **systematic grep**, the eight were assembled by
**inference**, and **three of the eight did not write the file in question at all** — which is
why the design now cites a systematically produced table instead of a list. That is not a
citation slip; it is a list that was never checked, and §5.2's entire original rationale rested
on it. A reader who does not know the old list was *measurably* wrong has no reason to prefer
the new source, and will assemble another ad-hoc list next time. The three: `init.go:528` was a
**file confusion**. It is
`os.WriteFile` to `filepath.Join(externalPath, "settings.yaml")`: a **per-project** settings
file at an external project path, not the hub's global one. ha-lead classifies that class
(W13) as DB-only with the file a regenerable cache. `pkg/hubsync/sync.go:1536` also writes a
**project** settings file, and ha-lead classifies `hubsync` as CLI-side rather than
hub-process. `pkg/config/hub_config.go:1310` sits inside `MergeServerIntoSettings`, which has
**no non-test callers** in the tree and is therefore dead as a runtime writer. Separately, and
not to be confused with the above, the table records that three of the *thirteen* sites are
workstation-gated and unreachable in a hosted deployment — a narrowing the
inferred list did not have. **Do not reproduce the table here**; a copy would drift, and this
paragraph exists because a copy already did.

**A Secret rather than a ConfigMap because the file necessarily contains secrets.**
OIDC is configurable *only* through `settings.yaml` (§5.1), so `oidc.client_secret` must
live in the file; with password DB auth the DSN password does too. Splitting the file
into a public ConfigMap plus a substituted secret fragment was considered and rejected
(§12, alternative C).

**Escape hatch:** `config.existingSecret` / `config.existingSecretKey`. When set, the
chart renders **no** settings Secret and mounts the operator's instead — the path for
External Secrets Operator or the Secret Manager CSI driver. `_helpers.tpl` fails if both
`config.existingSecret` and inline secret values are supplied.

### 5.3 Rendered file shape

Modelled on `scripts/cloudrun/hub-settings-template.yaml`, which is the only known-good
example of this file:

```yaml
schema_version: "1"
active_profile: default
image_registry: {{ .Values.agents.imageRegistry | quote }}

server:
  mode: hosted                                   # NOT negotiable — see §6.5
  hub:
    hub_id: {{ required "hub.hubId is required" .Values.hub.hubId | quote }}   # §6.6
    name:   {{ .Values.hub.name  | quote }}
  storage:                                       # the HUB'S BLOB STORE — GCS, not Filestore
    provider: gcs                                # §8.4
    bucket:   {{ required "storage.bucket is required" .Values.storage.bucket | quote }}
  auth:                                          # discriminated union on auth.mode — §6.5
    enabled: true
    mode: {{ .Values.auth.mode }}                # oauth | proxy
    {{- if eq .Values.auth.mode "proxy" }}
    proxy:
      provider: iap
      iap: { audience: {{ .Values.auth.proxy.iap.audience | quote }} }
    transport:
      mode: iap
      oidc_audience:   {{ .Values.auth.transport.oidcAudience | quote }}
      platform_auth_sa: {{ .Values.auth.transport.platformAuthServiceAccount | quote }}
    {{- else }}
    oauth:                                       # KEY NAMES ARE ha-lead's SKETCH — §6.5
      issuer:        {{ .Values.auth.oauth.issuer | quote }}
      client_id:     {{ .Values.auth.oauth.clientId | quote }}
      client_secret: {{ .Values.auth.oauth.clientSecret | quote }}
    {{- end }}
  database:
    driver: postgres
    dsn: "postgres://{{ user }}@127.0.0.1:5432/{{ db }}?sslmode=disable"
    max_open_conns: {{ .Values.database.maxOpenConns }}     # schema minimum: 2
    max_idle_conns: {{ .Values.database.maxIdleConns }}
    conn_max_lifetime: {{ .Values.database.connMaxLifetime }}
    conn_max_idle_time: {{ .Values.database.connMaxIdleTime }}
  workspace_storage:                                          # §8, §14
    backend: nfs
    nfs:
      mount_root:   {{ .Values.workspaceStorage.nfs.mountRoot }}
      subpath_root: {{ .Values.workspaceStorage.nfs.subPathRoot }}
      uid: {{ .Values.workspaceStorage.nfs.uid }}
      gid: {{ .Values.workspaceStorage.nfs.gid }}
      mount_options: {{ .Values.workspaceStorage.nfs.mountOptions | quote }}
      storage_class: {{ .Values.workspaceStorage.nfs.storageClass | quote }}
      shares:
        - id:      share1
          server:  10.0.0.2
          export:  /vol1
          pv_name: scion-hub-share1
  broker: { host: "127.0.0.1", port: 9800, auto_provide: true }
  secrets: { ... }                               # incl. the durable session secret (§6)
  # the six nested keys, when enabled, go HERE — not top level:
  # notification_channels, message_broker, native_chat, plugins, scheduler, github_app

profiles:
  default:
    runtime: kubernetes

runtimes:
  kubernetes:
    type: kubernetes
    namespace: {{ .Values.runtime.namespace | default .Release.Namespace }}
    gke: true
    list_all_namespaces: {{ .Values.runtime.listAllNamespaces }}
```

Field names come from `pkg/config/settings_v1.go` (`V1WorkspaceStorageConfig`,
`V1NFSConfig`, `V1NFSShare`, `V1RuntimeConfig`), not from prose.

`config.extra` is deep-merged over the rendered tree as a documented escape hatch for
keys the chart does not model, so an unmodelled setting never forces a chart fork.

### 5.4 Environment: the three `SCION_SERVER_*` vars that actually work, and nothing else

```yaml
env:
  - name: HOME                          # the ONLY lever on the settings.yaml location
    value: /home/scion
  - name: KUBECONFIG                    # explicitly emptied — see §11
    value: ""
  - name: SCION_SERVER_BASE_URL         # works via direct os.Getenv
    value: https://hub.example.com
  - name: SCION_REQUIRE_STABLE_SIGNING_KEY
    value: "true"
  - name: POD_NAMESPACE
    valueFrom: { fieldRef: { fieldPath: metadata.namespace } }
envFrom:
  - secretRef: { name: <session secret> }   # §6
```

`SCION_SERVER_ADMIN_MODE` and `SCION_SERVER_MAINTENANCE_MESSAGE` are exposed as optional
values. `SCION_SERVER_BASE_URL` is **mandatory on GKE**: absence only *warns*, and the
fallback `http://localhost:<port>` breaks agents and silently clears the session cookie's
`Secure` flag (`cookie Secure == strings.HasPrefix(BaseURL, "https://")`,
`parts/D-ingress-auth-ha.md`). **The schema requires it and requires the `https://` prefix
unconditionally** — not gated on `ingress.enabled`, and not gated on anything else. The
`Secure` attribute is a literal `HasPrefix` on this value, so any non-`https` base URL yields
a session cookie without `Secure`: **a security regression that presents as nothing at all.**

*History: this rule was conditional on `ingress.enabled` until Phase 1, which made it
unconditional; accepted by gd-em in revision 6 (§17 Phase 1). Phase 5a may revisit it **if a
real plaintext path ever appears** — none exists in this chart today.*

**One resolver's sources are a strict SUBSET of the other's, so the two base URLs cannot be
reconciled by reordering** (`gd-p0-dev`; verified against source, revision 7). Lead with the
subset relation, not with "two resolvers disagree" — the second framing invites the wrong fix.

| | agent-facing endpoint | OAuth redirect base |
|---|---|---|
| Function | `resolveHubEndpoint` (`cmd/server_foreground.go:1310`) | `initWebServer` (`:2094`, base-URL block `:2102-2108`) |
| Sources, in order | settings `server.hub.public_url` → `--base-url` → `SCION_SERVER_BASE_URL` → project settings `SCION_HUB_ENDPOINT` → IAP-derived → `http://localhost:<port>` | `--base-url` → `SCION_SERVER_BASE_URL` → `http://localhost:<webPort>` |
| Logging | a line on **every** branch — five gated on `--debug`, two unconditional | **none, on any branch, at any verbosity** |

(The first source is the settings file's `server.hub.public_url`, which populates
`cfg.Hub.Endpoint` at `pkg/config/settings_v1.go:1405`. Name the operator-facing key, not the
Go field — the key is what a phase renders.)

**Three of the six sources have no path to the OAuth side at all.** That is why the divergence
is structural rather than a precedence accident: no reordering fixes it, because the sources are
not present to be ordered. An operator who configures the base URL through
`server.hub.public_url` or through `SCION_HUB_ENDPOINT` — both reasonable, and both honoured by
the resolver they will think to check — gets a correct agent-facing endpoint while the OAuth
redirect base falls **all the way through** to `http://localhost:8080`.

**And then the cookie.** That fallback is `http://`, so `pkg/hub/web.go:484`
(`Secure: strings.HasPrefix(cfg.BaseURL, "https://")`) yields a session cookie **without
`Secure`** — transmissible in clear. **Nobody has to do anything wrong for this to happen.** No
misconfiguration, no unsupported value: two defensible choices produce a correct-looking
deployment whose session cookie has lost its transport protection, with no error, no warning,
and no log line. Of everything in this section, this is the consequence that is **not** closed
anywhere else — Phase 0's reserved-flag guard closes the argv route (below), but nothing closes
this one, because it needs no argv at all.

⚠ **Therefore: no phase may render `server.hub.public_url` into the settings file, and no value
may cause it to be rendered.** It outranks both argv and the environment for the agent endpoint
and is invisible to the OAuth resolver, so rendering it is the one action this chart could take
that produces two different base URLs in a single process — each correct by its own rule. The
chart does not render it today and §5.3's layout does not include it; this is written down so it
stays that way, because it is the kind of key a later phase adds thinking it is being helpful.
See §18 item 10b.

**`--debug` is not merely unhelpful here — it is actively misleading.** `gd-p0-dev`'s
distinction, which is the point of the whole finding: absent instrumentation leaves you
searching; instrumentation that confidently reports on the wrong channel **stops** you
searching. An operator who suspects a base-URL problem, enables `--debug`, and reads
`Hub endpoint resolved from settings (SCION_HUB_ENDPOINT): https://hub.example.com` has been
told something true, reassuring, and about the side that is working. There is no verbosity at
which the broken side says anything. **Any operator-facing guidance this chart ships on
diagnosing base-URL problems must not say "enable `--debug`" on its own** — for this failure
that advice steers the reader away from the fault.

**Why the chart is nonetheless immune, and what that immunity depends on.**
`SCION_SERVER_BASE_URL` is the **only source both resolvers honour** that the chart can set
declaratively — it is in the subset. Setting it makes the two resolvers agree *by construction*,
which is the real reason it is schema-required with an `https://` prefix rather than merely
documented. The same shape as §5.2's mode argument: the safe choice is the one that is correct
under **both** consumers, so no later phase can break it from one side. **Correctness by
invariance rather than by vigilance** — that is the property to preserve, and the three things
below are what preserve it. Two are already built; the third is the open one:

- **`SCION_SERVER_BASE_URL` must stay schema-required.** Making it optional does not produce a
  missing-config error; it produces a working hub with a broken login and no diagnostic.
- **`hub.extraEnv` must reject shadowing it** — a Phase 1 guard, built. Shadowing does not
  desynchronise the resolvers (both read the env var), but it bypasses the schema's `https://`
  validation, which is what stands between the deployment and a cookie without `Secure`.
- **`--base-url` is already a reserved `hub.args` flag** — since commit `51f62ab`, in
  `scion-hub.hubArgs` under the "delivered through another channel" reason; the render fails
  naming the flag. Revision 7 first recorded this as an outstanding Phase 0 gap. **It was not
  one** — the entry predated the finding, and what was missing was only the *channel* named
  beside it, which `gd-p0-dev` has since added. Corrected here rather than deleted: a design that
  silently drops a claim it made against the chart teaches the next reviewer nothing.
- **Nothing renders `server.hub.public_url`** — the one route still open, and the only one of
  the four that no guard closes. See the warning above and §18 item 10b.

**No `SCION_SERVER_DATABASE_*` or `SCION_SERVER_OIDC_*` may ever appear.** A golden-file
assertion enforces this (Phase 1).

### 5.5 The Layer-1 shadowing hazard

With Postgres, opsettings live in DB rows and **DB rows beat the bootstrap merge; env is
not merged on top** (`C-config-surface.md` §2.3). So once the hub has run, a chart value
for `runtimes`, `profiles`, or `admin_emails` may be **inert** — `helm upgrade` appears
to succeed and changes nothing. This is the single most surprising operational property
of the deployment. It goes in `README.md`, in `NOTES.txt` on every upgrade, and in the QA
acceptance list.

---

## 6. Secrets

The session secret derives **both** the cookie encryption key and the shared JWT signing
key. It must arrive via `env`/`envFrom` from a Secret and **never on argv** (#1070).

```
auth.existingSecret / auth.existingSecretKey   → preferred; chart renders no Secret
auth.sessionSecret                             → inline; chart renders secret-session.yaml
neither                                        → `fail` at template time
```

**The chart does not generate a random session secret.** A `randAlphaNum` default
regenerates on every `helm upgrade` unless guarded with `lookup`, and `lookup` returns
empty under `helm template`/`--dry-run`, so the golden output and the installed output
would diverge. Worse, silent rotation invalidates every session *and* the shared JWT
signing key — precisely what `SCION_REQUIRE_STABLE_SIGNING_KEY=true` exists to prevent.
Failing loudly is correct.

Other secret material:

| Secret | Delivery |
|---|---|
| Session secret | `envFrom` a Secret (chart-rendered or existing). |
| DB password | Avoided entirely under IAM DB auth (§7.2). Under password auth it is inside the settings Secret. |
| OAuth / OIDC client secret (`auth.mode: oauth`) | Inside the settings Secret — no other channel exists. Same rule as the session secret: **never on argv.** |
| IAP OAuth client (`auth.mode: proxy`) | A pre-existing Secret referenced by `BackendConfig.spec.iap.oauthclientCredentials.secretName`; the chart never creates it. |

No secret is ever placed in a ConfigMap, in `args`, or in a pod annotation. Phase 3's
acceptance test greps the rendered output for all three.

### 6.5 Hosted mode, the HA preflight contract, and the two auth modes

**Hosted mode is mandatory, and the obvious escape from the preflight is fake.**
`cmd/server_foreground.go:920` gates the HA guards on
`hostedMode && enableHub && isHADeployment(cfg)`, and `:927-938` makes `isHADeployment`
true on `database.driver == postgres` **alone** — so Cloud SQL trips HA mode
unconditionally. The tempting shortcut is to not set hosted mode, which skips the whole
preflight. **That shortcut is unusable in production:** at `:838-845`, when
`!hostedMode` the server runs `applyWorkstationDefaults`, takes `cfg.Auth.Enabled` from a
*dev* flag, and **forces `cfg.Hub.Host = "127.0.0.1"`** unless `--host` was explicitly
changed. A chart that shipped that would bind loopback behind a load balancer and derive
its auth-enabled state from a development toggle. The chart therefore always renders
`server.mode: hosted` and always passes `--host 0.0.0.0`, and `values.schema.json` offers
no way to turn hosted mode off.

The preflight consequently splits — per `ha-lead` — into two blocks:

**Block 1 — HA consistency. Always enforced. Unchanged by the refactor.**
`server.hub.hub_id` (§6.6) · `database.driver: postgres` · a Postgres URL ·
`storage.provider: gcs` + `storage.bucket` (§8.4) · a durable session secret (§6).
The chart renders all five in **both** auth modes.

**Block 2 — Auth topology. Conditional on `auth.mode`.**

| `auth.mode` | Chart renders | Status |
|---|---|---|
| `oauth` | `auth.oauth.{issuer,client_id,client_secret}`. **No IAP audience, no `transport.mode: iap`.** | **The primary documented path.** Blocked on `ha-lead`'s preflight split (§14.2). |
| `proxy` + `provider: iap` | `auth.proxy.iap.audience`, `auth.transport.mode: iap`, `transport.oidc_audience`, `transport.platform_auth_sa` — today's four hard checks at `:951+`. | Works today. Optional toggle. |
| `proxy` + `provider != iap` | — | Out of scope. `ha-lead` says not in the first cut; the schema rejects it. |

**Design consequence, and it is the useful one:** the two modes differ in exactly one
config subtree. `values.schema.json` expresses this as a **discriminated union on
`auth.mode`** (`oneOf` with `if/then` on the `mode` const), and `deployment.yaml` /
`secret-settings.yaml` branch once. There are **not** two deployment paths, two charts, or
two sets of probes, storage, RBAC or ingress plumbing. If `ha-lead`'s final key names
differ from the sketch above, the change is a values rename inside one subtree.

> **ASSUMPTION, pending confirmation (lead has asked the user).** The user wrote that auth
> "should support oauth flows on GKE, or a Cloud Run IAP proxy — but this should bear
> directly on the main GKE chart," which reads two ways. **We assume: OAuth-on-GKE is the
> primary documented path and IAP is an optional toggle in the same chart.** That satisfies
> either reading. If the answer is "keep the Cloud Run IAP variant out of the main chart",
> the change is to delete the `proxy` branch and its four values — contained, not structural.

> **PLACEHOLDER, ours.** The `auth.oauth.*` key names above are transcribed from
> `ha-lead`'s sketch, which they marked approximate and subject to revision. They are
> **not** a settled contract. The developer must confirm them against `ha-lead`'s issue
> before Phase 5b; until then the chart must not claim the oauth path works.

### 6.6 `server.hub.hub_id` — explicit, required, and upgrade-stable

`:958` hard-fails on `IsHubIDUnconfigured()`, and `ha-lead` confirms this check **stays
hard in both auth modes**. It is also audit finding C1, and it is the most dangerous
possible default for a Deployment: absent an explicit value, each replica derives its hub
ID **from its hostname**, and Deployment pod hostnames are random per pod. Every replica
would then diverge on GCS prefixes and secret scopes — silently, and worse on each
rollout.

Chart rules:

- `hub.hubId` is **schema-required**, non-empty, and additionally guarded by `required`
  in the template so the failure message names the value.
- It is **never** derived from anything Helm regenerates: not `.Release.Revision`, not
  `randAlphaNum`, not `uuidv4`, not `.Release.Name` (renameable), not the pod hostname.
  A CI assertion greps the rendered `settings.yaml` for those generators.
- `NOTES.txt` states that changing `hub.hubId` on an existing deployment re-scopes GCS
  prefixes and secrets and is **not** a safe upgrade.
- Two releases in one cluster must not share a `hubId`; `NOTES.txt` says so.

---

## 7. Cloud SQL

### 7.0 HA preconditions — held by reference, not copied

**The Phase 2 preconditions live in
`/scion-volumes/scratchpad/projects/gke-deploy/briefs/phase2-ha-preconditions.md`
(author: `gke-deploy-lead`, 2026-08-17). Include that file by path. Read it before writing
the Phase 2 brief and before reviewing the Phase 2 PR.**

It is not reproduced here on purpose. The lead sent a path rather than text because **a
brief written from a message is a derived source**, and this project has already lost time
to four of them (§17.1 rule 4, sourcing corollary). Copying it into §7 would create a fifth
and would put the two copies on independent maintenance schedules. Every claim in that file
carries a **[VERIFIED]** or **[MUST VERIFY]** marker; **the markers are the content**, and
they do not survive summarisation. **Do not promote a [MUST VERIFY] to a fact by restating
it** — in particular the blocking one, that Postgres LISTEN/NOTIFY works over the Cloud SQL
Auth Proxy connection path at the driver and pooling settings this chart configures. That is
unverified, it is answered by connecting rather than by reading Cloud SQL documentation, and
if the answer is no the pod does not degrade — it fails to start.

Three things in that file **outlive Phase 2** and are therefore recorded here as well, since
a reader who never opens a Phase 2 brief still needs them.

**(a) Phase 2 *is* the HA switch. It is not preparation for it. [VERIFIED]**
`hostedHAGuardsRequired` is `hostedMode && enableHub && cfg != nil && isHADeployment(cfg)`
(`cmd/server_foreground.go:921`), and `isHADeployment` (`:927-938`) is a disjunction whose
postgres-driver branch **alone** flips it. The chart already renders `--hosted` and
`--enable-hub` (`_helpers.tpl:467-478`), so the moment the driver is configured every HA
guard in the binary becomes live — **at `replicaCount: 1`, on the first pod, before anything
scales.** There is no staging period. This must be stated in the Phase 2 PR description,
because it is the single most consequential behavioural change in the chart's history **and
it will read in the diff as "add a `--db` flag".** Today `isHADeployment` is false at every
replica count (no `--db`, no `--storage-bucket`, no volumes) — recorded as a non-instance
with its reasoning, because it means any argument about multi-replica behaviour made against
the *current* chart is an argument about a configuration that does not exist. Those arguments
become live in Phase 2 and must be **re-made there, not inherited**.

**(b) Do NOT add a migration Job. [VERIFIED]** `migrateStore` takes Postgres advisory lock
`0x5C100008` around `AutoMigrate` (`cmd/server_foreground.go:1168-1200`), so concurrent first
boot of N replicas against one fresh database serialises correctly. This is recorded as a
**non-instance with its reasoning** rather than left as a silence, because "run migrations in
a Helm hook Job" is the obvious first instinct at this phase and is what an outside reviewer
will ask for. It is unnecessary here, and adding it introduces an ordering dependency and a
failure surface for a problem the binary already solved. A phase that declines this reasoning
must say why in the PR body rather than silently add the Job.

**(c) The loopback gate is spent by the pod shape, not by Cloud SQL.** Phase 2 is the first
phase to place a second process inside the pod's network namespace, but the consequence
belongs to the pod spec and applies to every later container. **It is recorded as a
trip-wire on the container table in §4.1**, and as §18 item 34. It is deliberately *not* a
Phase 2 note: the next author to add a sidecar will read §4.1 and will not read §7.

**Inherited premise — §18 item 35.** The lead's file flags item 35 as a **claim under review
with its author named** (`gd-doc`), not as a finding: it verified the load half — that an
unfound settings file falls through to `loadGlobalConfigLegacy` and embedded defaults — and
**explicitly did not verify the preflight-skip half.** That half is now verified, by opening
the code rather than by restating the claim: the embedded default driver is `sqlite`
(`pkg/config/hub_config.go:540`), `validateHostedHAPreflight` returns `nil` immediately when
`!hostedHAGuardsRequired(cfg)` (`cmd/server_foreground.go:951-954`), and it has exactly one
non-test caller, at `:151`, whose error aborts startup. So on the settings-load-failure path
the preflight does not fail — it does not run.

That verification also produces the **remedy, and it is a Phase 2 choice rather than a Phase
4 one**: the legacy fallback still applies `SCION_SERVER_*` environment overrides
(`pkg/config/hub_config.go:798-803`, whose own comment names
`SCION_SERVER_DATABASE_DRIVER → database.driver`; `envKeyToConfigKey` at `:976-986` performs
that mapping), and the settings path applies them too (`:685`). **A driver delivered by env
survives a settings-load failure; a driver delivered only by a settings key does not.** If
Phase 2 configures the driver through the settings file alone, a settings-load failure
silently downgrades the deployment to the non-HA guard set — the guard for the HA case
disabled by the failure that puts you in the HA case. Phase 2 should say which channel it
chose and why. This does not close item 35, which is about the general degradation shape and
remains Phase 4's; it removes the specific instance §7 would otherwise create. Per §17.1
rule 8, a finding closed by a configuration is **deferred, not closed** — and the deferral
lands on whoever next changes how the driver is rendered.

### 7.1 Auth Proxy as a native sidecar

`cloudsqlconn` is not in `go.mod`, so the connector is unavailable. The proxy runs as a
**native sidecar** — an `initContainers` entry with `restartPolicy: Always` (GKE ≥ 1.29):

```yaml
initContainers:
  - name: cloud-sql-proxy
    restartPolicy: Always                    # native sidecar
    image: gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.x@sha256:...
    args:
      - "--structured-logs"
      - "--port=5432"
      - "--health-check"
      - "--http-address=0.0.0.0"
      - "--http-port=9801"
      - "--auto-iam-authn"                   # when database.auth == iam
      - "--private-ip"                       # when cloudsql.privateIp
      - "{{ .Values.cloudsql.instanceConnectionName }}"
    startupProbe:   { httpGet: { path: /startup,   port: 9801 }, failureThreshold: 60, periodSeconds: 2 }
    readinessProbe: { httpGet: { path: /readiness, port: 9801 } }
    securityContext: { runAsNonRoot: true, allowPrivilegeEscalation: false, capabilities: { drop: [ALL] } }
```

**Why native and not a plain sidecar:** the hub connects to the DB and runs
`AutoMigrate` immediately at startup. A plain sidecar has no ordering guarantee, so the
hub races the proxy and enters `CrashLoopBackOff` until it wins — self-healing but ugly,
and it muddies the startup-probe budget in §9.1. A native sidecar is started and made
ready before the main container, and is torn down after it. `cloudsql.nativeSidecar` may
be set `false` for clusters below 1.29; `NOTES.txt` then warns about the crash-loop
window.

### 7.2 Authentication: both modes; the **default is contingent on Phase 2 verification**

`database.auth: iam` uses `--auto-iam-authn`; the DSN carries **no password**:

```
postgres://<gsa-name>@<project>.iam@127.0.0.1:5432/scion?sslmode=disable
```

That removes the only mandatory secret from `settings.yaml` in a non-OIDC install, removes
a rotation burden, and reuses the Workload Identity SA the proxy already needs. It is
where we want to land. `database.auth: password` is the alternative, with the password
rendered into the settings Secret.

**DECIDED (Q4, lead, 2026-08-17): implement both; Phase 2 verifies IAM *first* and the
verification picks the default.** Nobody has yet checked that the hub's DSN handling
tolerates a passwordless DSN, and we will not assert it. If IAM works, IAM is the default
and password is the documented escape. If it does not, password is the default **and the
failure is written up as a finding**, not quietly worked around.

Note for the phase author, because it is easy to flatten: this is deliberately *not* the
same treatment as `auth.mode` in §6.5. There the default is `proxy` because `oauth` is
**known broken** by preflight. Here IAM is merely **unverified**. Known-broken and
unverified are different states and get different defaults — do not collapse the two into
a blanket "default to whatever boots today" rule.

`sslmode=disable` is **correct, not a defect**: the proxy terminates a mutually
authenticated TLS tunnel to Cloud SQL and the hub↔proxy hop is loopback inside the pod.
This is called out in `README.md` so it is not "fixed" by a later reviewer.

### 7.3 Workload Identity binding

```
KSA  scion-hub  (namespace <ns>)
  annotation: iam.gke.io/gcp-service-account: <GSA>@<project>.iam.gserviceaccount.com

GSA roles:
  roles/cloudsql.client                       # always
  roles/cloudsql.instanceUser                 # when database.auth == iam
  roles/iam.serviceAccountTokenCreator        # ON THE TRANSPORT SA, for HA OIDC
  roles/secretmanager.secretAccessor          # only if Secret Manager CSI is used

Binding:
  gcloud iam service-accounts add-iam-policy-binding <GSA> \
    --role roles/iam.workloadIdentityUser \
    --member "serviceAccount:<project>.svc.id.goog[<ns>/scion-hub]"
```

The chart renders the annotation and prints these commands verbatim in `NOTES.txt` with
values substituted. It does not execute them. (Direct Workload Identity Federation
principals — `principal://.../subject/ns/<ns>/sa/<ksa>` — would remove the GSA, but the
hub must *impersonate* the transport SA for HA OIDC, which needs an SA to impersonate;
GSA impersonation is therefore the default. The direct form is documented as an option
for non-HA installs.)

### 7.4 Connection budget

`database.max_open_conns` has a **floor of 2** — a pool of 1 self-deadlocks against the
migration advisory-lock connection. It is currently rescued by defaulting code, i.e.
load-bearing by accident, and it cannot be delivered by env, so the chart both renders it
into `settings.yaml` and enforces `"minimum": 2` in `values.schema.json`.

The hub also opens **secondary pgx pools** (event publisher, command bus) that are not
chart-configurable and count against the Cloud SQL connection limit. Total budget is
`replicaCount × (max_open_conns + S)` where `S` is that fixed overhead. The chart cannot
know `S`; `NOTES.txt` prints the formula and the instance's `max_connections` must be
sized accordingly. Measuring `S` is an acceptance item for the Phase 2 manual smoke test.

---

## 8. Storage — two different subsystems

**Do not conflate them. This is an easy and expensive mistake.**

| Subsystem | Config key | Backing | Who uses it |
|---|---|---|---|
| **Hub blob storage** | `server.storage.{provider,bucket}` | **GCS.** Required by the HA preflight; Filestore does not satisfy it. | The hub itself. |
| **Workspace storage** | `server.workspace_storage.*` | **Filestore NFS**, as the user specified. | Agent workspaces — the subject of #1075. |

Both exist in one deployment. Reading the user's "Filestore NFS" as satisfying
`server.storage` produces a chart that fails preflight on every install; reading it as
satisfying only the workspace backend is correct. §8.1–8.3 cover workspace storage; §8.4
covers hub blob storage.

### 8.1 One static PV, one RWX PVC, shared by hub and agents

The hub needs a *mounted path*; agent pods need a **PVC name**
(`RunConfig.NFSPVClaimName`, "the K8s PVC name for the NFS-backed workspace volume … the
PVC references a static RWX PV", `pkg/runtime/interface.go:59-72`). An in-tree `nfs:`
volume would satisfy the hub but not the agents, so the chart creates both objects:

```yaml
# pv-filestore.yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: {{ include "scion-hub.pvName" . }}      # == settings.yaml nfs.shares[0].pv_name
  annotations: { "helm.sh/resource-policy": keep }
spec:
  capacity: { storage: {{ .Values.workspaceStorage.filestore.capacity }} }
  accessModes: [ReadWriteMany]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: ""                          # static binding
  csi:
    driver: filestore.csi.storage.gke.io
    volumeHandle: "modeInstance/<project>/<location>/<instance>/<share>"
    volumeAttributes: { ip: <filestore-ip>, volume: <share> }
---
# pvc-workspace.yaml  — RWX, volumeName-bound, resource-policy: keep
```

- **`ReadWriteMany` is required** and is what Filestore CSI provides. The Autopilot
  `standard-rwo` problem is about *dynamically provisioned PD* volumes, not this.
- `resource-policy: keep` on both objects: `helm uninstall` must never delete the share
  binding. Data loss on uninstall is not an acceptable default.
- `filestore.existingClaim` skips both objects for operators who manage storage
  elsewhere. Dynamic provisioning through a `*-rwx` StorageClass is supported the same
  way but is not the default (§12, alternative G).
- `nfs.shares[0].pv_name` in `settings.yaml` and the PV's `metadata.name` are rendered
  from the same helper, so they cannot drift.

### 8.2 The mount path is derived, and a mismatch is fatal by design

`checkWorkspaceStorageHealth` (`pkg/hub/handlers_health.go:111-115`) stats exactly
`filepath.Join(nfs.MountRoot, Shares[0].ID)`, and `hubManagedProjectPath` resolves
`<MountRoot>/<share.ID>/hub-projects/<slug>`. Therefore:

```
volumeMounts:
  - name: workspace
    mountPath: {{ printf "%s/%s" .Values.workspaceStorage.nfs.mountRoot (first .Values.workspaceStorage.nfs.shares).id }}
```

`_helpers.tpl` **fails the render** if any user-supplied `mountPath` diverges from that
join. The reason is the failure mode: the readiness check is a plain `os.Stat`, so a
directory conjured on the writable container filesystem at the right path reads *healthy*
while nothing is actually mounted — the same silent-latch class that PR #1074 closed for
`gke-shared-volume` and which remains **open for the `nfs` backend**. Deriving the path
from a single source makes the mismatch unrepresentable rather than merely unlikely.

Two further properties to encode:

- The stat has a **2s internal timeout**, against Kubernetes' **1s default probe
  timeout** — hence the explicit `timeoutSeconds: 3` in §9.1. A hung NFS mount otherwise
  times out at the kubelet before the hub can report it.
- Only `Shares[0]` gates readiness. The schema sets `minItems: 1`; v1 recommends exactly
  one share and `NOTES.txt` warns that shares 1..n are unmonitored.

### 8.3 Autopilot and shared-dir PVCs (pre-existing, and #1075-dependent)

`k8s_runtime.go:851` hardcodes `ReadWriteMany` for per-shared-dir PVCs on the **local**
backend, and `DefaultScratchpad` means every project gets a shared dir. On Autopilot with
a non-RWX default StorageClass those PVCs stay `Pending` forever and the agent hangs —
**Pending, not an error**, so it looks like a slow start.

Under the `nfs` backend `createSharedDirPVCs` is a **no-op**
(`k8s_runtime.go:788`) — shared dirs become subPaths on the workspace PVC
(`k8s_runtime.go:1429`). So the nfs backend *fixes* this. But only once #1075 lands.
Until then the chart's mitigation is documentation plus a prerequisite check: the
cluster's default StorageClass must be RWX-capable (a Filestore `*-rwx` class). This is
in `README.md` prerequisites and in `NOTES.txt`.

### 8.4 Hub blob storage: GCS

The HA preflight requires `server.storage.provider: gcs` and a non-empty
`server.storage.bucket`. The chart renders both from `storage.provider` / `storage.bucket`
and makes the bucket **schema-required whenever `database.driver: postgres`** — i.e.
always, in a real GKE install — so the failure lands at `helm template` time with a clear
message rather than as a pod-startup preflight error five minutes later.

Access is via Workload Identity: the GSA in §7.3 additionally needs
`roles/storage.objectAdmin` scoped to the bucket. `NOTES.txt` prints the binding command.
The chart does not create the bucket (§2).

The GCS prefix is scoped by `hub_id` — see §6.6 for why that value must be explicit and
stable.

### 8.5 Chart contract: never mount an `emptyDir` under `/mnt`

From the #1074 round-2 review, and it generalises to any workspace mount root.
`securityContext.readOnlyRootFilesystem: true` needs a writable scratch volume, and an
`emptyDir` at `/mnt` is the most common way people produce one. Combined with a mistyped
volume name it **silently reintroduces the workspace-on-ephemeral-disk bug**: the
directory exists, `os.Stat` succeeds, `/readyz` reports healthy, and every workspace write
goes to a disk that disappears with the pod. `isMountedVolume`'s device comparison in
#1074 closes part of this for `gke-shared-volume`; it does not close it here, and the
disclosed residual is exactly this case.

The chart's controls, in order of strength:

1. **Structural.** The only volume the chart mounts under the workspace mount root is the
   workspace PVC, at the derived path in §8.2. Scratch volumes go to `/tmp` (§4.3), never
   under `/mnt`.
2. **Render-time.** `_helpers.tpl` fails if any entry in `hub.extraVolumeMounts` has a
   `mountPath` under `workspaceStorage.nfs.mountRoot` (default `/mnt/...`) whose volume is
   not the workspace PVC. This catches the realistic version of the mistake — an operator
   adding a scratch volume through the chart's own extension point.
3. **Schema.** `values.schema.json` cannot see the relationship between a volume's name
   and its type, so it cannot express this; the check must live in the template.
   Documented here so nobody spends a day trying.
4. **Documented.** `README.md` and `NOTES.txt` carry the rule verbatim: *do not mount an
   `emptyDir` under `/mnt`.*

---

## 9. Probes, RBAC, networking

### 9.1 Probes

```yaml
startupProbe:                      # migration takes an unbounded advisory-lock wait,
  httpGet: { path: /readyz, port: 8080 }   # before the listener binds
  periodSeconds: 5
  failureThreshold: 60             # 5 minutes, tunable
  timeoutSeconds: 3
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
  periodSeconds: 10
  timeoutSeconds: 3                # EXPLICIT: > the 2s internal mount-check timeout
  failureThreshold: 3
livenessProbe:                     # disabled by default
  tcpSocket: { port: 8080 }
```

- **`/readyz` exactly — not `/healthz`, and not a prefixed variant.** `/healthz` returns
  200 unconditionally and GFE intercepts it specifically
  (`.design/healthz-proxy-fix.md:43`); pointing a probe at it disables the only guard and
  reopens a silent-data-loss path. There is no `/livez`. And the path is **`/readyz` at
  the root** — `/api/v1/readyz` does not exist (§9.2). Both mistakes are asserted against
  in CI (§13).
- **`timeoutSeconds` is always explicit** for the reason in §8.2.
- **A startup probe, not a tight liveness probe.** First boot blocks on
  `pg_advisory_lock` with no bound.
- **Liveness defaults off.** `/readyz` folds in the DB and the NFS mount — both external.
  A liveness probe on it would kill healthy pods during a Cloud SQL failover. When
  enabled it is a TCP check on the web port, which tests the process and nothing else.
- **`/metrics` is not auth-exempt** (fork issue #1082) — scrapers get 401. The chart
  therefore ships **no** `ServiceMonitor`/`PodMonitor` and no `prometheus.io/scrape`
  annotations. Shipping a scrape config that returns 401 is exactly the "control
  connected to nothing" we are avoiding.

### 9.2 The GCLB health check is a second, separate probe — and the path match is **exact**

`BackendConfig.spec.healthCheck.requestPath` defaults to `/` and must be set to `/readyz`
explicitly. It bypasses IAP, so it must reach an auth-exempt endpoint; if `/readyz`
required auth the backend would go `UNHEALTHY` and no traffic would ever reach IAP.

**Q6 is answered, green** (`ha-lead`, verified in source at 066eeba). `/readyz` **is**
auth-exempt: `auth.go:419-421` defines `isHealthEndpoint` as
`path == "/healthz" || path == "/health" || path == "/readyz"`, reached from
`isUnauthenticatedEndpoint` (`:427`) at the top of `UnifiedAuthMiddleware` (`:140`) before
token validation. The GCLB health check is therefore safe. This was a real risk, not a
false alarm — it is recorded as answered rather than deleted.

> **⚠ HAZARD — the match is an exact string comparison, on both sides.**
> `server.go:3363` registers `/readyz` on the mux and nothing else, and the auth exemption
> above compares `path ==`, not a prefix. So **any** variant fails twice: unrouted *and*
> not auth-exempt. This design originally carried `/api/v1/readyz` — inherited from the
> brief, wrong, and it would have shipped a chart that was dead on arrival and read as an
> infrastructure mystery rather than a typo.
>
> The forward-looking risk is a tidy-up: someone will eventually want `readyz` moved under
> `/api/v1/` with the rest of the API. **That refactor would silently un-exempt it from
> auth** unless `isHealthEndpoint` is changed in the same commit. Anyone touching either
> the route registration or `isHealthEndpoint` must change both, and the CI assertion in
> §13 must be updated to match the new literal.

### 9.3 RBAC

Rendered into `runtime.namespace` (default: the release namespace). **`rbac.agentNamespace` is
accepted for the same namespace and `runtime.namespace` is canonical; if both are set and they
disagree, the render fails** rather than one winning quietly and granting RBAC where no agent
pod lands (§19).

```
pods                     get list watch create delete
pods/exec                create
pods/attach              create
pods/log                 get
pods/portforward         create
persistentvolumeclaims   create get list delete      # ← currently missing; folded in here
configmaps               get list create delete
secrets                  get list create delete
events                   get list watch
```

The PVC verbs are the gap identified in `lead-state.md`; without them the local-backend
shared-dir path fails with a permission error rather than a `Pending` PVC. A
`ClusterRole`/`ClusterRoleBinding` pair replaces the namespaced pair when
`runtime.listAllNamespaces` is true, matching `V1RuntimeConfig.ListAllNamespaces`.

### 9.4 Egress

Optional `NetworkPolicy` (default off) allowing egress to: the kube-apiserver, DNS, the
GCE metadata server, `www.gstatic.com` and `iamcredentials.googleapis.com` (both required
by IAP/HA OIDC per `parts/D-ingress-auth-ha.md`), `sqladmin.googleapis.com` and
`oauth2.googleapis.com` (proxy), and the Filestore IP on 2049/111. Default off because
FQDN egress rules need CIDRs the chart cannot know; supplied as CIDR values when enabled.

---

## 10. Ingress, IAP, and the two-step install

### 10.1 Objects

| Object | Key settings |
|---|---|
| `Service` | `ClusterIP`, `cloud.google.com/neg: '{"ingress": true}'` (container-native LB), `cloud.google.com/backend-config`. |
| `Ingress` | `kubernetes.io/ingress.class: gce`, `kubernetes.io/ingress.global-static-ip-name`, `networking.gke.io/managed-certificates` or `ingress.gcp.kubernetes.io/pre-shared-cert`, `networking.gke.io/v1beta1.FrontendConfig`. |
| `BackendConfig` | `iap.enabled` + `oauthclientCredentials.secretName`; `timeoutSec: 3600`; `connectionDraining.drainingTimeoutSec: 60`; `healthCheck.requestPath: /readyz`. |
| `FrontendConfig` | `redirectToHttps.enabled: true`, optional `sslPolicy`. |
| `ManagedCertificate` | Optional; domains from `ingress.managedCertificate.domains`. |

**`timeoutSec: 3600` is load-bearing, not tuning.** The GCLB backend timeout is a
*stream* timeout for WebSockets; the default 30s severs
`/api/v1/runtime-brokers/connect`, `/api/v1/agents/{slug}/pty`, `/attach`, and
`/ports/tunnel`. `parts/C-config-surface.md` §7.4 also requires: no path/query/body
rewriting and no mutation of `X-Scion-*` headers (they are inputs to an HMAC canonical
string), no SSE buffering, ≥1 MB frame/body, and ≤5 minutes of clock skew. GCE Ingress
does not rewrite by default — the design constraint is recorded so nobody adds a rewrite
annotation later. A CI assertion fails on any rewrite-class annotation.

### 10.2 The two audiences are different things — **`auth.mode: proxy` only**

- `proxy.iap.audience` — the **resource path**
  `/projects/<number>/global/backendServices/<id>`.
- `transport.oidc_audience` — an **OAuth 2.0 Client ID**, and it must be a *custom*
  client (`.design/ha-oidc.md` §3.4).

They are separate values with separate schema patterns, and `NOTES.txt` explains the
distinction, because conflating them produces an authentication failure that reads like a
misconfigured IAP.

### 10.3 Schema validation of the IAP audience

`isSupportedIAPAudience` accepts only the 7-part Cloud Run and 6-part GCLB forms; App
Engine and regional forms are rejected fail-closed. The chart's schema therefore checks
*shape*, and rejects exactly one literal:

```json
"iap": { "properties": { "audience": {
  "type": "string",
  "pattern": "^/projects/[0-9]+/global/backendServices/[0-9]+$",
  "not": { "const": "/projects/000000000/global/backendServices/0" }
}}}
```

That literal is provably not issuable. **Everything else that merely looks like a
placeholder — including `123456789`, the canonical docs dummy, which could be a real
project number — is a `NOTES.txt` warning and never a failed install.** A chart that
refuses to install on a valid-but-odd-looking value is worse than one that warns.

### 10.4 The bootstrap chicken-and-egg — **`auth.mode: proxy` only**

**Under `auth.mode: oauth` this section does not apply.** No IAP audience is required, so
there is nothing that is unknowable at first install and the chart installs in **one
step**. That is a substantial ergonomic argument for OAuth as the primary path, and it is
one of the reasons §6.5 makes it the default once `ha-lead`'s change lands.

Under `auth.mode: proxy`, two facts collide:

1. The GCLB backend-service ID does not exist until the Ingress has provisioned, so
   `iap.audience` is **unknowable at first `helm install`** (`parts/D-ingress-auth-ha.md`).
2. `isHADeployment` trips on `driver: postgres` **alone**, after which all ten preflight
   checks are hard gates — including the IAP audience. There is **no supported "Postgres
   but no IAP" tier.**

So a naive single-step install cannot start the hub. The chart makes the two steps
explicit rather than magical:

```bash
# Step 1 — provision the load balancer with the hub scaled to zero.
helm install scion-hub ./scion-hub -f values.yaml --set bootstrap.deferHub=true
kubectl get ingress scion-hub -w                    # wait for the address
BSID=$(gcloud compute backend-services list --filter="name~scion-hub" --format="value(id)")
PNUM=$(gcloud projects describe "$PROJECT" --format="value(projectNumber)")

# Step 2 — supply the audience and start the hub.
helm upgrade scion-hub ./scion-hub -f values.yaml \
  --set iap.audience="/projects/$PNUM/global/backendServices/$BSID"
```

`bootstrap.deferHub: true` renders `replicas: 0`. Everything else — Service, NEG,
Ingress, BackendConfig, certificate — is created, which is what mints the backend-service
ID. `NOTES.txt` prints exactly these commands with values substituted.

Doing nothing special also *works*: the pods crash-loop on the preflight failure (with a
clear error string) while the Ingress provisions, and the step-2 upgrade fixes them. That
is the documented fallback for GitOps flows that cannot run two steps —
`bootstrap.deferHub` simply makes the intent legible instead of alarming.

A third option — relaxing the preflight to warn-only until the first successful start —
is a **code** change, out of chart scope. `ha-lead` owns that call, and their preflight
split supersedes the need for it: with `auth.mode: oauth` the audience gate disappears
entirely rather than being softened.

---

## 11. Image

**Recommendation: a non-root hub image with an embedded web UI and no baked
`KUBECONFIG`.** None of the three existing lineages — four files, see the second table
below — is usable as-is:

| Lineage | Blocker |
|---|---|
| `scripts/cloudrun/Dockerfile` | Bakes `ENV KUBECONFIG=/home/scion/.kube/config`. `pkg/k8s/client.go:88-103` prefers an explicit kubeconfig, so this **silently disables in-cluster ServiceAccount auth** — the hub cannot schedule agent pods, and the failure looks like an RBAC problem. |
| `image-build/hub/Dockerfile` | Built with `no_embed_web`; no web UI to serve under `--enable-web`. Otherwise the best hygiene of the three (runtime uid 1000 via the `sciontool init` privilege-dropping shim) — but its **image `USER` is root**, which is what pod admission judges; see the four-file table below. |
| root `Dockerfile` / `Dockerfile.hub` (byte-identical, orphaned) | Embeds the web UI — correct — but `debian:bookworm-slim` with **no `USER`**, so the hub runs as root, and no `HOME`/writable config dir. |

**DECIDED (Q3, lead, 2026-08-17; correction history in §17.1): a new `hub-gke` *stage*
inside the existing root `Dockerfile`, not an in-place edit and not a second Dockerfile.
The stage is *not* the final one.**

State the requirement as a property and a constraint, not as a position in the file — the
position is a consequence, and two attempts to specify the position directly were wrong:

- **Required property:** the **default build target must remain the plain runtime image**
  — the uid-0 `debian:bookworm-slim` stage that exists today, unchanged in behaviour.
- **Constraint:** `hub-gke` must `FROM` a stage defined **earlier** in the file. Dockerfiles
  resolve stage references backwards only, so `hub-gke` can only derive from something above
  it.
- **Consequence of the two together:** the file must **give the existing stage 3 a name** and
  **terminate with an empty stage derived from that name**. There is no arrangement without
  both: the default build target is the *last* stage, and stage 3 is currently both last and
  unnamed, so today it cannot be pinned with `--target` either.
- **Why it matters:** the last stage is what `docker build .` produces. **No in-repo consumer
  builds the root `Dockerfile`** — the consumers are external `gcloud run deploy --source` /
  `gcloud builds submit`, which pass **no `--target`**. Appending `hub-gke` as the last stage
  therefore hands Cloud Run a uid-1000 image, silently, with nothing in this repo failing.

Stage order after Phase 7:

```
frontend         # unchanged
builder          # unchanged
runtime          # existing stage 3, gains "AS runtime". Metadata only; no instruction changed.
hub-gke          # FROM runtime: uid 1000, HOME, writable ~/.scion, no ENV KUBECONFIG
<trailing>       # a bare "FROM runtime": an empty stage whose only job is to be last
```

```dockerfile
# stage 3, unchanged except for the name:
FROM debian:bookworm-slim AS runtime
# ... apt-get, COPY --from=builder, EXPOSE, ENTRYPOINT — all exactly as today ...

FROM runtime AS hub-gke             # built with --target hub-gke
RUN useradd -u 1000 -m -d /home/scion scion \
 && mkdir -p /home/scion/.scion && chown -R 1000:1000 /home/scion
ENV HOME=/home/scion
USER 1000:1000
# no ENV KUBECONFIG — deliberately absent
# no CMD with secrets; the chart supplies args

# Load-bearing. Do not delete: the default build target is the last stage, and external
# `gcloud` builds pass no --target. This empty stage keeps `docker build .` == `runtime`.
FROM runtime
```

**Do not "put `hub-gke` before the final stage" instead.** That was the first correction and
it is also wrong: placed third, `hub-gke` can only derive from `frontend` (node) or `builder`
(golang), neither of which is the debian runtime it needs. To sit there it must re-declare
`FROM debian:bookworm-slim` and duplicate the `apt-get`, the `COPY --from=builder`, the
`EXPOSE` and the `ENTRYPOINT` — a duplicated recipe inside a single file, which is precisely
the drift surface Q3 chose a stage to avoid. That outcome looks correct in review, which
makes it the worst one available.

**The trailing stage is load-bearing, and it is guarded.** It exists solely to hold the last
position. It contains no instructions, it looks exactly like dead code, and **removing it
silently restores the defect with nothing failing** — no build error, no test failure, just a
different image reaching Cloud Run. Phase 7 therefore carries **both**:

1. a comment in the `Dockerfile` at the trailing stage saying why it is there, and
2. a **standing CI assertion** that the default build target (no `--target`) is still the
   plain runtime image — see §17 Phase 7 and §18.

A comment is a request, not a guard; the assertion is the guard, and the comment exists so
the assertion's failure is intelligible.

Rationale for the shape, recorded so a later author does not "simplify" it back:

- **Not an in-place `USER 1000` on the root image.** That is a behaviour change for every
  existing consumer — anything relying on uid 0 breaks. Same reasoning the lead used to
  decline bundling #1073. The user also pre-empted it: "this may need a new image."
- **A stage, not a separate `image-build/hub-gke/Dockerfile`.** One build file means no
  drift surface between two nearly identical recipes, while `--target` keeps the existing
  default target's behaviour identical — which is true **only** with the trailing stage in
  place.
- **Fallback.** If a stage cannot work — e.g. the root `Dockerfile`'s final stage is not
  extensible without restructuring it — fall back to `image-build/hub-gke/`, **and record
  here why**, because two Dockerfiles *will* drift and the next reader deserves to know we
  accepted that knowingly rather than drifted into it.

**Four Dockerfiles in this repo produce a hub image.** All four verified at 066eeba:

| File | Toolchain / base | Built by |
|---|---|---|
| `/Dockerfile` | `node:22-alpine`, `golang:1.26.1-alpine` | external `gcloud run deploy --source` / `gcloud builds submit`, **no `--target`** |
| `/Dockerfile.hub` | byte-identical copy of the root file | no known consumer |
| `/scripts/cloudrun/Dockerfile` | **`node:20-slim`, `golang:1.25`** — drifted | `scripts/cloudrun/deploy.sh:249`, with `-f` |
| `/image-build/hub/Dockerfile` | `FROM ${BASE_IMAGE}` (extends `scion-base`), **`USER root`** — set at line 24 and never switched back, so the image's effective user is root; `scion-base`'s `sciontool init` entrypoint drops privileges at *runtime*, which pod admission does not see | the `image-build/` Cloud Build system; `scripts/lib/targets.sh:134` maps `scion-hub` to it |

Consolidating them is tracked as **ptone/scion#1092** and routed to repo-maintenance. This
design references the issue and proposes no fix — the chart's phases must not take it on.

Two things follow that this section is careful **not** to claim:

- **GKE and Cloud Run do not run the same image.** On the scripted Cloud Run path they
  already do not: `scripts/cloudrun/Dockerfile` builds with a different Node and a different
  Go toolchain from the root file. Nothing in this design may be read as "one image, two
  platforms", and no chart behaviour may depend on it.
- **The artifact-name hazard.** `image-build/hub` publishes an artifact literally named
  `scion-hub` (`image-build/cloudbuild-hub.yaml:50`) and it runs as **root**. An operator who
  sees the chart wanting a `scion-hub` image, finds a published `scion-hub`, and points the
  chart at it gets a root image — and if the securityContext is looser than intended it
  *appears to work* while running as root. §19 previously defaulted `image.repository` to a
  `scion-hub` path and no longer does; see the next paragraph.

**`image.repository` is REQUIRED, schema-enforced, with no default (§19).** We publish no
`hub-gke` image anywhere, so any default we could ship points either at an empty registry
path or at somebody else's artifact — and **a default that cannot be correct is worse than a
required value, because it converts an install-time error into a runtime mystery.** A
cosmetically distinct default such as `scion-hub-gke` was **considered and rejected**: it
still names a path we do not publish, and it reads as an endorsement of an image that does
not exist. What is rejected is `scion-hub-gke` **as a shipped default**, not the name itself —
§19's commented example points at `…/my-repo/scion-hub-gke` in the *operator's own* registry,
which is a value they supply, not one we ship. Documentation alone does not close the artifact-name hazard, so the decision is
paired with a guard: the chart sets `runAsNonRoot: true` in a way that is not quietly
loosenable (§4.4), so that pointing the chart at a root image **fails loudly at pod admission
rather than running**.

Why it matters beyond hygiene: `runAsNonRoot: true` (§4.4) will not schedule a root
image, and a root hub writing to the Filestore share produces uid-0-owned project
directories that agent pods running as uid 1000 cannot write — a failure that surfaces
much later and looks like a Filestore problem.

**Belt and braces:** the chart sets `KUBECONFIG: ""` explicitly (§5.4), so even a base
image that bakes it falls back to in-cluster auth. That is cheap and removes a whole
class of hard-to-diagnose regression.

Building and publishing the image is out of chart scope.

---

## 12. Alternatives Considered

| # | Alternative | Why rejected |
|---|---|---|
| A | **Drive the hub with `SCION_SERVER_*` env vars.** | `SCION_SERVER_DATABASE_*` and all `SCION_SERVER_OIDC_*` are unreachable by any spelling; wrong keys are silent no-ops; the repo's own schema advertises names that do not work (#1081). Non-viable, not merely inferior. |
| B | **Mount the Secret as a read-only *directory* at `$HOME/.scion`.** | Still rejected, but note the reason changed in revision 5: not "the hub writes `settings.yaml`" — that write is the defect (#1091) and the chart now mounts the **file** read-only on purpose — but that `$HOME/.scion` is the hub's whole **state** directory with no path override, so a read-only directory breaks the hub for reasons unrelated to settings. The single-file `subPath` mount is what §5.2 adopts. |
| C | **Public ConfigMap + `sed`/`envsubst` substitution of secrets in an init container.** | Keeps config auditable via `kubectl get cm`, but the OIDC client secret must be in the file regardless, so the split buys little; two artifacts drift; `sed` breaks on delimiter characters in secret values; `envsubst` is not guaranteed present in `debian:bookworm-slim`. Rejected in favour of one Secret plus a first-class `config.existingSecret` hand-off to a real secret manager. |
| D | **Cloud SQL Go connector (`cloudsqlconn`).** | Absent from `go.mod`. Would be the cleanest option and is worth a future issue; not available now. |
| E | **Proxy over a unix socket in a shared `emptyDir` instead of TCP.** | Genuinely viable and marginally safer (nothing on loopback). TCP chosen because the proxy's HTTP health endpoints and the DSN form are better documented, and pod-local loopback is not a meaningful exposure. Revisit if a second container is ever added. |
| F | **In-tree `nfs:` volume (server + path) instead of CSI PV/PVC.** | Simpler for the hub, but agent pods need a **PVC name** (`NFSPVClaimName`). We need the PVC anyway, so one mechanism is better than two. |
| G | **Dynamic Filestore provisioning via a `*-rwx` StorageClass.** | Makes Helm the owner of the share's lifecycle, so `helm uninstall` destroys user data. Static PV + `resource-policy: keep` is the safe default; dynamic provisioning is reachable through `filestore.existingClaim`. |
| H | **Broker as a sidecar or subchart.** | Contradicts the user's decision. An earlier note of the lead's suggesting "structure for a broker subchart from day one" is superseded. |
| I | **Migration `Job` or Helm pre-upgrade hook.** | Unnecessary: `pg_advisory_lock(0x5C100008)` serialises `migrateStore`, verified against real Postgres with three replicas. A hook would add a failure mode and a `helm upgrade` stall for zero benefit. (Caveat: event-payload and `webchat_*` tables are created *outside* that lock — #1078 — which a hook would not fix either.) |
| J | **Gateway API + `GCPBackendPolicy` instead of Ingress + `BackendConfig`.** | Same audience chicken-and-egg, less mature IAP story on GKE, more new CRD schemas to vendor for `kubeconform`. Deferred, not rejected on principle. |
| K | **Config Connector `IAMPolicyMember`/`SQLInstance` resources so the chart creates GCP IAM.** | Adds a hard cluster-wide operator dependency for a one-time bootstrap step, and turns IAM drift into Helm drift. `NOTES.txt` with exact commands achieves the same outcome with no dependency. Reconsider if Config Connector becomes standard for the fleet. |
| L | **kind-based smoke test in CI now.** | Image build history is 6–83 minutes with no layer cache (`parts/E-ci.md`); a non-HA boot needs no credentials but an HA boot needs Postgres, a GCS fake, a Secret Manager fake and forged IAP JWTs. Deferred to Phase 9 with scope written down. |
| M | **Two charts (hub + infrastructure).** | Forces duplication of `mountRoot`, share ID and namespace across charts — exactly the values whose divergence causes the silent failures in §8.2. |
| N | **GCLB session affinity to mitigate multi-replica PTY breakage.** | Does not work. Affinity pins a *client* to a pod; the agent's control channel lives on an arbitrary pod. `replicaCount: 1` is the only reliable mitigation available to a chart. |
| O | **Skip hosted mode so the HA preflight never runs.** | The obvious way to make IAP optional today, and it is fake. `server_foreground.go:838-845` makes `!hostedMode` apply workstation defaults, take `Auth.Enabled` from a dev flag, and force `Hub.Host = 127.0.0.1`. A production chart cannot ship that (§6.5). |
| P | **Two charts, or two deployment paths, for the two auth modes.** | `ha-lead`'s split shows the modes differ in exactly one config subtree; everything else — hub_id, database, storage, session secret, probes, RBAC, ingress — is identical. Two paths would duplicate all of it to express one `oneOf`. Rejected in favour of a discriminated union on `auth.mode` (§6.5). |
| Q | **Ship only the IAP path now and add OAuth later.** | It is what works today, but it bakes the user's explicitly rejected requirement ("IAP should not be required for HA") into the chart's structure, and the one-step vs two-step install difference (§10.4) would then look like a chart property rather than an IAP property. Rejected: model both modes now, gate the oauth branch on `ha-lead`'s issue (§14.2). |
| R | **Use Filestore for `server.storage` and skip GCS.** | The preflight requires `provider: gcs`; Filestore does not satisfy it. Listed because it is the specific conflation the lead flagged as likely (§8). |

---

## 13. CI: what is validated

Every verb is a `make` target first — `parts/E-ci.md` establishes the Makefile as the
canonical CI entry point — and the workflow job only invokes them.

```make
helm-lint:       # helm lint for each ci/values-*.yaml
helm-template:   # helm template | kubeconform, for each ci/values-*.yaml
helm-golden:     # helm template > golden/, diff against committed output
helm-assert:     # the positive AND negative assertions below
helm-verify: helm-lint helm-template helm-golden helm-assert
```

| Check | Detail |
|---|---|
| `helm lint` | Once per `ci/values-*.yaml`. Free, catches template syntax and schema violations. |
| `helm template \| kubeconform -strict` | GKE CRD schemas (`BackendConfig`, `FrontendConfig`, `ManagedCertificate`, and `Gateway` if Phase J ever lands) are **vendored** under `deploy/helm/crd-schemas/`. **`--ignore-missing-schemas` is forbidden** — it silently degrades the check to "did it parse", which is the failure mode we are buying CI to avoid. |
| Golden files | Committed `helm template` output, diffed. Catches unintended rendering changes and is the mechanism for most assertions below. Chosen over `helm unittest` to avoid a plugin dependency. |
| **Probe-path assertions** | **Positive:** every probe and the `BackendConfig` health check use the literal `/readyz`. **Negative:** no `healthz` anywhere, **and no prefixed variant** — any `readyz` occurrence not equal to `/readyz` fails, `/api/v1/readyz` included by name. Both halves are required; see the note below. |
| Negative assertions | No `healthz` in any probe or `BackendConfig.healthCheck`; no `SCION_SERVER_DATABASE_*` / `SCION_SERVER_OIDC_*` env; no `secret`/`password`/`token`-looking value in `args`; no secret material in any ConfigMap; no path-rewrite annotation on the Ingress; the six `server:`-nested keys are nested; `server.mode: hosted` present and undisableable; `hub_id` not produced by any Helm generator; no non-PVC volume mounted under `nfs.mountRoot` (§8.5). |
| Positive assertions | All five §6.5 Block-1 keys present in **every** `ci/values-*.yaml`: `hub.hub_id`, `database.driver: postgres`, a Postgres URL, `storage.provider: gcs` + `storage.bucket`, a session secret reference. |
| **Default build target of the root `Dockerfile`** | `docker build` with **no `--target`** still produces the plain runtime image — uid 0, no `HOME`, no `~/.scion` (§11, §18 item 24). This is the guard on the load-bearing trailing stage: without it, deleting that stage as dead code silently ships `hub-gke` to the external `gcloud` consumers and nothing fails. **Qualification: this is not a `helm-*` target and not this job.** It needs a Docker build, so it belongs to the image workflow rather than `make helm-verify`, and Phase 7 owns it rather than Phase 6. It is listed here because §13 is the table someone implements CI from, and an assertion recorded only in §11 and §18 is an assertion that gets dropped. |
| Schema tests | `helm template --set database.maxOpenConns=1` must fail; the one banned IAP literal must fail; `123456789` must succeed; `auth.mode: proxy` + `provider: something-else` must fail; `auth.mode: oauth` without the unlanded-guard must fail while §14.2 is open; omitting `hub.hubId` or `storage.bucket` must fail. |

### 13.1 Why every negative assertion needs a positive twin

Worth sitting with, because it happened to this document.

Revisions 1 and 2 specified a CI assertion for exactly this area: *"no `healthz` in any
probe or `BackendConfig.healthCheck`."* Meanwhile every probe in the design pointed at
`/api/v1/readyz`, a path that does not exist. **The assertion would have passed.** It was
written to catch the mistake we had already thought of — someone reaching for `/healthz` —
and it sailed straight past the mistake we were actually making, because a wrong path that
merely isn't `/healthz` satisfies it perfectly.

The general shape: **a negative assertion only ever proves the absence of a specific known
error. It says nothing about correctness.** Pair every one with a positive assertion that
pins the intended value, or the check quietly narrows to "we did not make last year's
mistake."

Concretely, for each negative assertion in the table above, the reviewer should be able to
answer: *what wrong-but-not-forbidden value would still pass this?* Where that question
has an answer, add the positive twin. The probe-path row is the worked example; the
`hub_id` row is the next most likely to need one (asserting no Helm *generator* appears
does not assert that the value is non-empty, stable, or the one the operator supplied).

> **⚠ REVISION 8 DEMOTES THIS SECTION'S ADVICE TO BELT-AND-BRACES. The primary guidance is now
> §17.1 rule 14 — assert the REASON, not the outcome** — and the demotion was measured, not
> argued: `reserved-flags.sh` printed **29 green `ok` lines with no `helm` installed**, and its
> two positive twins (which existed only because a reviewer required them) were the entire
> difference between a script that failed and a script that lied. **A twin is a second assertion,
> so it protects only in aggregate and can be deleted by trimming an unrelated test; matching the
> refusal's own message fixes each assertion at the site, for the cost of a substring.** Keep the
> twins. Lead with the reason.

**The standing question gains its missing half** (`gd-p0-rev-2`, adopted project-wide). The
question every check must answer is *what input would make this pass while the thing it tests is
broken?* — **and the environment is an input.** rev-2's own diagnosis of how it missed this:
*every one of my six mutations varied the chart, and none varied the runner.* It had treated the
toolchain as **apparatus** rather than as input. That is §17.1 rule 19 — volume is not
independence, ask what premise the checks share — arriving from a completely different direction:
**six mutations sharing one unexamined assumption is one mutation.**

**And a narrower formulation that subsumes it, which is the one to put in a brief because it says
where to look** (`gd-p1-dev`, measured at `fda8c558` with helm 3.16.3 and kubeconform 0.6.7
present):

> **A NEGATIVE ASSERTION READS ITS SUBJECT. IF THE SUBJECT CAN BE EMPTY OR ABSENT, THE ASSERTION
> IS SATISFIED BY ITS OWN FAILURE TO HAPPEN.**

Every instance found in one day is that sentence: `reject()` reading an exit status that *binary
not found* also produces; `package excludes ci/ and tests/` matched against an **empty listing**;
and `gd-p1-dev`'s five greps against **files `helm` never wrote** — `PATH=/usr/bin:/bin bash
hack/verify.sh` printing `ok  minimal emits no dead SCION_SERVER_DATABASE_/OIDC_ variable`, which
is the assertion its own brief names as Phase 1's acceptance criterion, green off five empty files.

Two reasons to prefer this wording to *the environment is an input*, and `gd-doc` records them as
the author's argument rather than as a relay:
1. **It names the fix without further thought** — assert something about the subject's existence
   *in the same breath* — which "add a positive twin" does not, since a twin can be added on an
   unrelated axis and leave the hole wide open.
2. **It covers an instance with no toolchain in it at all**, which the environment framing misses:
   **a chart that will not render is also a world in which every rejection looks successful.**
   Phase 1 made `hub.baseUrl` required and P0's 29 reject assertions went green against a chart
   that could not produce a manifest — **with `helm` present and working.**

**The cheapest discriminator for a negative assertion, and it needs no new apparatus**
(`gd-p0-rev-3`, from its own method — it got `0` twice from two different vacuums before getting a
real result): **run the counter-form of the same grep as a control, in the same command.** Both
forms returning zero is impossible when the tool works, so the pair distinguishes *the thing is
absent* from *nothing was read* at the cost of one extra pattern. It belongs inside the assertion
helper rather than beside any one call site — the same placement argument as the `%!` guard.

**Network:** core Kubernetes schemas come from the default kubeconform location, which
needs network egress (CI already has it for `go mod`); only the GKE CRDs are vendored.
The flake risk is noted; full vendoring is the fallback if it materialises.

**Branch protection — flagged deliberately.** Only two check names exist today
(`Build & Test`, `golangci-lint`), and branch protection could not be read (403). **A new
`helm` job will not be required automatically; an admin must add it to the required
checks or the gate is decorative.** This is an explicit hand-off item to the lead, not a
developer task.

---

## 14. What the chart CANNOT deliver yet

Two independent upstream dependencies. Neither is ours to fix.

### 14.1 Blocked on #1075 — the Kubernetes runtime never receives workspace storage

`factory.go:150-168` — the `kubernetes`/`k8s` case sets `DefaultNamespace`, `GKEMode`,
`GKEAutoDetected` and `ListAllNamespaces`, and **never** touches workspace storage, while
the `cloudrun` case eleven lines below does (`rt.WorkspaceStorage = vs.Server.WorkspaceStorage`).
`KubernetesRuntime` has no `WorkspaceStorage` field. `RunConfig.WorkspaceBackendName` and
`NFSPVClaimName` have **zero non-test writers repo-wide**, and all eight readers in
`k8s_runtime.go` gate on `== "nfs"`, which in production is always `""`.

So the chart can render a perfect `workspace_storage` block and every agent pod still
gets an `EmptyDir`. Undeliverable until #1075:

1. **Agent workspaces on Filestore.** `k8s_runtime.go:1219` takes the `else` branch:
   `EmptyDir`, not the RWX PVC with `subPath`.
2. **Agent workspace persistence.** Everything an agent writes lives in the pod's
   `EmptyDir` and dies with the pod.
3. **Shared dirs as subPaths.** `k8s_runtime.go:1429` (`nfsSharedDirs`) is false, so
   `createSharedDirPVCs` takes the local path and creates a **per-shared-dir RWX PVC**
   (`:851`). Combined with `DefaultScratchpad`, every project's first agent needs an RWX
   PVC; on Autopilot with a non-RWX default StorageClass it stays `Pending` and the agent
   hangs silently.
4. **Stable `fsGroup` on agent pods.** `k8s_runtime.go:1180` falls through to
   `int64(os.Getgid())` — the hub process's GID — instead of `nfs.gid`. If the hub still
   runs as root (§11), agent pod files are group 0.
5. **The `workspace-provision` init container** (`:1297`, `sciontool provision`, sentinel
   + cross-node advisory lock) is never injected, so there is no git-clone-into-share
   provisioning and no cross-node mutual exclusion.
6. **The workspace-sync skip** (`:411`). Agent pods still take the `kubectl cp` +
   `chown -R` path from the hub's local view of the workspace — slower, and on a
   multi-replica hub it copies from whichever pod happens to serve the request.
7. **`SCION_HOST_UID`/`SCION_HOST_GID` advertisement** to agent pods, which is documented
   as conditional on `WorkspaceBackendName == "nfs"`.

**The dangerous part.** The **hub side** of `workspace_storage` *does* work: the hub reads
`s.config.WorkspaceStorageConfig`, `hubManagedProjectPath` resolves into
`<mount_root>/<share_id>/hub-projects/<slug>`, and `/readyz` stats the mount. So with
`backend: nfs` the hub genuinely uses Filestore while agents do not. Agent edits land in
an `EmptyDir` and are never written back to the share; the hub's copy stays stale. That is
worse than an inert control — it is a split view of the same workspace. Whether any
sync-back path (`Sync`, `GetWorkspacePath`) mitigates it must be **verified by QA**, not
assumed.

**Chart-level guard.** `workspaceStorage.backend: nfs` requires an explicit
`workspaceStorage.acknowledge1075: true` while the issue is open; without it the render
fails with a message naming the issue and this section. When #1075 lands, the flag and
the guard are deleted in one commit. Phases 4 and 8 carry the dependency.

### 14.2 Blocked on `ha-lead`'s preflight split — `auth.mode: oauth`

Today, in hosted mode with `driver: postgres`, `server_foreground.go:951+` hard-errors
unless all four IAP conditions hold. There is **no config path around it**, and the
`!hostedMode` escape is unusable (§6.5, alternative O). So until `ha-lead`'s split lands:

1. **`auth.mode: oauth` does not work**, however the chart renders it. The pod fails
   preflight at startup.
2. **IAP is effectively required** for any Cloud SQL deployment — the exact requirement
   the user rejected.
3. **The one-step install** described in §10.4 is unavailable; every install is two-step.
4. **`ha-lead`'s `auth.oauth.*` key names are unconfirmed** (§6.5). The chart's oauth
   branch is written against a sketch and may need a values rename — contained to one
   subtree by construction, but not zero work.

**Chart-level guard, mirroring §14.1.** `auth.mode: oauth` requires an explicit
`auth.acknowledgeOAuthUnlanded: true` while `ha-lead`'s issue is open; the failure message
names the issue. `auth.mode` **defaults to `proxy`** until then — the chart's default must
be the mode that actually boots — and flips to `oauth` in the same commit that removes the
guard. That flip is a documented breaking change in the chart's own version.

**What is not blocked:** everything in Block 1 of §6.5. `hub_id`, Postgres, GCS storage and
the durable session secret are required identically in both modes and are deliverable now.
The union structure itself is deliverable now; only the oauth branch's *effect* is blocked.

---

## 15. Migration / Rollout

This is greenfield — there is no existing GKE deployment to migrate, and Cloud Run is
unaffected because the chart touches no shared code path. What needs stating is **upgrade
semantics**, which are not obvious:

1. **DB-owned settings shadow chart values.** Once the hub has run against Postgres,
   Layer 1 opsettings live in DB rows and beat the bootstrap merge (§5.5). Changing
   `runtimes`, `profiles` or `admin_emails` in `values.yaml` and running `helm upgrade`
   may do nothing at all. Documented in `README.md`, restated in `NOTES.txt` on every
   upgrade, and on the QA list.
2. **`helm uninstall` must be non-destructive.** `resource-policy: keep` on the PV and
   PVC; the chart never owns the Cloud SQL instance, the Filestore instance, the OAuth
   client, the static IP, or any IAM binding.
3. **Upgrades restart the hub**, which drops PTY sessions, port tunnels, and in-memory
   presence. Unavoidable given §4.2; stated so it is not reported as a bug.
4. **First install is two steps** (§10.4). Not a workaround for a chart defect — a
   consequence of the backend-service ID not existing until the LB is provisioned.
5. **Rollback.** `helm rollback` restores manifests but **not** schema migrations —
   `AutoMigrate` is forward-only. Rolling back to an image with an older schema
   expectation is not supported; `README.md` says so explicitly.
6. **Chart versioning.** `Chart.yaml` `appVersion` tracks the hub version; the chart's own
   `version` is bumped independently. Images are pinned by digest in `ci/values-*.yaml`
   so golden files are stable.

---

## 16. Open Questions

Raised to `gke-deploy-lead` **serially**, highest first. None blocks Phases 0–3.
**Q2, Q3 and Q4 are closed** — Q3 and Q4's answers and reasoning are recorded in the table
below and in §11 / §7.2 respectively. Q5, Q6 and Q9 are routed to `ha-lead`; Q1 and Q8 are
with the user. **Nothing is left in gd-arch's court.**

| # | Question | Blocks | Needs |
|---|---|---|---|
| **Q1** | Default `replicaCount: 1`? Multi-replica is migration-safe but breaks web terminal / exec / logs / port-forward for ~(N−1)/N of requests, and session affinity does not fix it (§4.2, alternative N). Accept single-replica availability, or accept a broken web terminal? | Phase 0 default only | **User** |
| **Q2** | ~~IAP confirmed?~~ **Answered.** IAP must not be required; `ha-lead` owns the preflight split. Superseded by Q8. | — | Closed |
| **Q8** | Does "this should bear directly on the main GKE chart" mean both auth modes live in the main chart, or that the Cloud Run IAP proxy variant stays out of it? **Assumption meanwhile (§6.5): OAuth primary, IAP an optional toggle in the same chart.** | Phase 5b scope | **User** (lead has asked) |
| **Q9** | Confirm `ha-lead`'s `auth.oauth.*` key names before Phase 5b. They are an explicitly approximate sketch; the chart must not claim the oauth path works against unconfirmed keys. | Phase 5b | `ha-lead` |
| **Q3** | ~~Image lineage?~~ **ANSWERED (lead, 2026-08-17): a new *stage* in the existing root `Dockerfile`, built with `--target hub-gke`** — ~~final~~ **not final**: the last stage is the default build target, so the file must name stage 3 and end with an empty stage derived from it (§11, §17.1). Not an in-place `USER 1000` — that breaks every existing consumer of the root image (same reasoning as declining to bundle #1073), and the user pre-empted it with "this may need a new image". A stage rather than a second Dockerfile because one build file has no drift surface while `--target` leaves the default target's behaviour untouched. **Fallback:** `image-build/hub-gke/` only if a stage cannot work, and the reason must be recorded in §11 — two Dockerfiles will drift and that should be a knowing choice. `KUBECONFIG: ""` in the chart stays regardless: defence in depth against a base image is worth one line. | Phase 7 | Closed |
| **Q4** | ~~Default Cloud SQL auth?~~ **ANSWERED (lead, 2026-08-17): implement both; Phase 2 verifies IAM first and the verification sets the default.** IAM is where we want to land — no stored credential, reuses the WI SA the proxy already needs — but nobody has checked that the hub tolerates a passwordless DSN, so we will not assert it. IAM works ⇒ IAM default, password documented escape. IAM fails ⇒ password default **and a written-up finding**. Explicitly *not* the §6.5 treatment: `oauth` is known-broken, IAM is merely unverified, and those get different defaults (§7.2). | Phase 2 | Closed |
| **Q5** | ~~Which of the eight writers are not mirrored to the DB?~~ **ANSWERED (`ha-lead`, 2026-08-17):** the **W1–W13** table in `ha/investigations/settings-writes.md` enumerates them by systematic grep — three are workstation-gated and unreachable in hosted mode, one is a per-project file, one dual-writes correctly, and the write-back into shared DB truth (`syncHubSettings`) is the real hazard. Tracked as **ptone/scion#1091**; §5.2 records what the chart does about it. **The follow-on — is a settings write *failure* fatal? — is also ANSWERED (`ha-lead`, 2026-08-17): all soft.** No write path calls `log.Fatal`, `os.Exit`, or panics; the GitHub App `PUT` returns HTTP **200** after swallowing the error. **So the chart ships ahead of #1091**, at the cost that some writes are accepted and have no effect until it lands — see §5.2, which no longer carries a placeholder. | Closed | Closed |
| **Q6** | ~~Is the readiness endpoint auth-exempt?~~ **ANSWERED GREEN (`ha-lead`, verified at 066eeba): `/readyz` is auth-exempt** via `isHealthEndpoint` (`auth.go:419-421`), reached from `isUnauthenticatedEndpoint` (`:427`) at the top of `UnifiedAuthMiddleware` (`:140`) before token validation. The GCLB health check is safe. **This was a real risk, not a false alarm** — and answering it is what surfaced that the design's path was wrong: the endpoint is `/readyz`, not `/api/v1/readyz`, and both the route (`server.go:3363`) and the exemption match **exactly**. See the hazard note in §9.2. | Phase 5a | Closed |
| **Q7** | Keep `--global` from the Cloud Run arg set for GKE, or drop it? | Phase 0 default only | Lead |

---

## 17. Implementation Phases

Each phase is independently reviewable and, except where noted, independently
installable. Rebase onto upstream `main` at the start of each phase.

### 17.1 Read this before writing a phase brief — rationale over mechanism

> **Prefer the stated rationale and the acceptance criterion over prescribed mechanism, at
> every level.** Mechanism prescribed at a distance from the artifact keeps being wrong while
> the stated rationale keeps being right. Where §17 prose and a §18 acceptance criterion
> conflict, **the criterion governs**, and the conflict is **reported to the issue owner**
> rather than silently resolved.

> **And a second rule, added in revision 6: §3.2, §17 and §19 must be reconciled whenever any
> one of them changes.** §3.2 (chart layout) and §19 (values appendix) describe a **finished
> chart**; §17 describes the **work**. Nothing was checking that everything named in the first
> two is produced by the third, and two things slipped through it — `hub.extraEnv` existed only
> as a values key, and `README.md` was assumed by a Phase 4 acceptance criterion while being no
> phase's deliverable. A key in the consolidated appendix reads as committed; a file in the
> layout reads as planned. `gd-p0-rev` found both by walking that boundary deliberately, which
> is the check: for every entry in §3.2 and §19, name the phase that produces it — or record it
> as a deliberate non-feature and take it out. **The same rule covers one section asserting
> something about another:** §5.2 reasoned for four revisions on the premise that `fsGroup` was
> absent while §4.4 rendered it unconditionally, and nothing was reconciling the two. Two
> sections describing one artifact with no check between them is the same defect whether the
> sections are §3.2/§17/§19 or §4.4/§5.2 — so the check extends to it: **if a section's argument
> depends on what another section renders, that dependency is verified, not assumed.**

> **A third rule, revision 6: an ACCEPTED deviation is absorbed into the design with a history
> line. Only PENDING or TEMPORARY deviations stay marked as deviations.** Once a deviation is
> accepted it is not going to be reverted, so leaving the old rule standing in the spec creates
> **two sources of truth** — and the copy a developer reads while implementing is the spec, not
> the phase note recording the exception. A later phase then implements the superseded rule and
> is *correct according to the document*. **The test: would the document be wrong if you deleted
> the marking?** If yes, the marking is load-bearing and stays — `auth.acknowledgeOAuthUnlanded`
> is genuinely temporary, and the trailing `Dockerfile` stage is a permanent oddity whose
> marking is its guard (§11). If no, absorb it and leave one history line saying what it was,
> when it changed, why, and who may revisit it. The unconditional `https://` base-URL rule
> (§5.4, Phase 1) is the worked example.

> **A fourth rule, revision 7 (`gd-em`, ruling): where an enumeration exists EXECUTABLY in the
> chart, the design states the REASONS and does not restate the MEMBERS.** The design's content
> is *why* — each reason named, with the failure it prevents. The chart's content is *what* —
> the lists themselves, asserted separately so that deleting one fails a test. **Membership is
> defined once, in the artifact that runs.** This is not a style preference, and the evidence is
> dated: this document's copy of Phase 0's reserved-flag list went stale **within hours**, and in
> the worst available way — the stale version was internally coherent and well argued (§17.1
> instance 8). **Two enumerations of the same set will diverge, and the divergence is undetectable
> from either side alone**; keeping one of them non-executable guarantees that the executable one
> is right and the readable one is wrong. Note this stays checkable: a reviewer can ask whether
> the chart's lists match the reasons given here, which is the useful question anyway. **Cite the
> helper or the function, never a line number** — line numbers drift too, and a stale citation
> invites the same false confidence in miniature.
>
> **Corollary on sourcing, which applies to whoever holds this document.** Claims about the
> *hub source* and about this document's internal consistency can be made from what is already
> read; **claims about a CHART FILE require fetching the branch**, every time, because the chart
> is under active construction by three developers and this document trails their work rather
> than gating it. Those are different reliability classes and they do not feel different while
> writing — instance 8 was produced in the confident register, not the tentative one. So the test
> is not *how sure am I*, it is *what kind of thing is this a claim about*.
>
> **Second corollary, revision 8: this applies to enumerations the CHART renders for operators,
> not only to the design's prose.** When a message or a document must list what a set contains —
> the assertions that stop running under `config.existingSecret` is the live case — **derive the
> list from the same definition the code iterates**; do not hand-maintain it beside them. A
> hand-written copy **goes stale at the exact moment it matters most**: someone adds an assertion
> to `assertSettings`, the list still names five, and **the operator reads five items and takes
> on six.** Note the direction of the lie — it is always the safe-sounding one, because the
> omission is invisible to the person relying on it. So: define the set once, have the code
> iterate it, and render the enumeration from the same source; where iterating is impractical,
> add a **parity guard that fails the render when the code contains a member the list does not**
> — a check on the check. **A comment asking people to keep two lists in sync is a request, not
> a mechanism**, and it is the same defect class as the `server:` dependency in §5.2: an artifact
> whose correctness rests on a property of another artifact that nothing enforces. Where the
> enumeration must appear in more than one place, **one copy is rendered and the others point at
> it** — a third copy is a third thing to drift.

> **A fifth rule, revision 7 (`gd-p1-dev`, from three independent instances in one day): a check
> whose subject is deliberately ABSENT must be proved against a fixture, not against the
> subject.** When "the thing is not there" is the passing state, a scan can only tell you the
> thing is not there — **it cannot tell you the scan would have noticed**. The two are
> indistinguishable from the output, and a detector that always returns success reports exactly
> the same green. Every negative assertion in this chart has this shape: no
> `SCION_SERVER_DATABASE_*` env var, no `--config` in args, no `0600` mode, no run-once init
> container, no `server.hub.public_url`. **The fixture is the only thing separating a working
> detector from a broken one**, so each negative assertion ships with at least one input it must
> flag and one near-miss it must not. The near-miss is the half that gets skipped and the half
> that catches naive implementations — a "does `restartPolicy: Always` appear anywhere in this
> section" check passes a list containing one native sidecar *and* one run-once container, which
> is precisely the Phase 2 shape. This rule is the executable form of the positive-twin
> requirement in §13.1: `hub.extraEnv` gives the assertion something real to catch, and the
> fixture proves it still catches it.
>
> **Revision 8 puts a question AHEAD of the fixture, not instead of it** (`gd-p1-dev`, ruling
> `gd-em`): ***"is the subject actually absent, or have I only not found the path?"*** The
> asymmetry is what makes it the first question rather than merely a better one: **if the subject
> turns out to be reachable you get the fixture for free AND you have found a defect**, so the
> search costs nothing when it succeeds and costs one search when it fails. There is no version
> of this where looking first is the wrong order. **The base rate is the finding**: three
> assumed-absent subjects were investigated in one day and all three were reachable — most
> sharply `server.hub.public_url`, which this document had dispatched as *preventive* and which
> `config.extra` renders today (§18 item 10b). Assumed absence has been wrong every time it has
> been checked on this project, including by the people writing this rule.

> **A sixth rule, revision 7 (`gd-em`, ruling): a correction that removes the error without
> recording it protects against the stale claim and not against the reasoning that produced it.
> Strike a typo; RECORD a conclusion.** A silently deleted claim leaves the next reader free to
> re-derive it — and the things worth deleting are usually the things a fresh reader re-checks,
> so the phantom does not disappear when the sentence does. It comes back fully formed in someone
> else's head, and they pay the whole cost again with no record that anyone had been there. One
> sentence saying *this was checked, it was wrong, here is why* is cheaper than the second
> occurrence. There is also an audit consequence: **an erratum with no anchor is an assertion.** A
> log entry in the table below that points at a correction leaving no trace in the text cannot be
> verified by the person who most needs to — the one deciding whether to trust the rest of the
> log. §5.4's `--base-url` bullet is the worked example: the claim was wrong, it is marked wrong
> in place, and the reasoning that produced it is what rule 4 now guards against.

> **A seventh rule, revision 7: when the design names an INTERNAL identifier where an EXTERNAL
> one exists, it is not merely imprecise — it makes a whole category of question unaskable.**
> §5.4's precedence table named `cfg.Hub.Endpoint`, a Go struct field. The operator-facing key is
> the settings-file `server.hub.public_url`, which populates it (`pkg/config/settings_v1.go:1405`).
> Nobody renders a Go field, so with the internal name in place the question *"could a phase
> render this?"* has no natural occasion to be asked — the name silently answers *no*. Correcting
> the name to the renderable key is what exposed the last unguarded route to a base-URL split
> (§18 item 10b). **The error was concealing the hole**, and the concealment is systematic rather
> than lucky: an internal name hides every interface-level question about the thing it names. So
> prefer the name that appears in the artifact an operator or a template writes, and when both
> exist, give the internal one only as a cross-reference. Note the corollary that made this
> trustworthy: the correct name was **verified** at the assignment site rather than adopted from
> whoever supplied it, which is the difference between a correction and a deference.
>
> **Revision 8 validated this rule against a live defect, one revision after writing it**
> (`gd-p1-dev`). Naming `server.hub.public_url` is what made *"can `config.extra` reach this?"*
> an askable question — a Go struct field has no values path, so with `cfg.Hub.Endpoint` in place
> there was nothing to test and no test would have been written. Asked, it reproduced
> immediately (§18 item 10b). The rule predicted that an internal name hides interface-level
> questions; the corrected name produced the hidden question and the question produced the bug.

> **An eighth rule, revision 8 (`gd-em`, from `gd-p1-dev`'s reachability split): when a finding
> is closed by a CONFIGURATION rather than by code, it is not closed — it is DEFERRED to whoever
> changes the configuration, who will not know.** That person is us, three phases from now. The
> distinction the review menu was missing is between two verdicts that look identical on the day:
> **tolerance is a stronger guarantee than unreachability.** Unreachability is a property of
> which features happen to be switched on. It decays silently the first time a later phase
> enables something, and **nobody re-checks a note that says "not reachable"** — the note reads as
> a fact about the code and is actually a fact about a configuration. Tolerance is a property of
> the reader's own handling and survives the feature being turned on. So a closure recorded here
> must name **which kind it is**, and a configuration-closed finding must additionally name **the
> phase that would open it** — an owner, not a condition. Worked examples now in the document:
> `/api/v1/system/init` is closed by `--hosted`, not by code (§18 item 34); `--port` is inert
> only because the chart renders `--enable-web` (Phase 0, direction B); and `agents/` readers are
> reachable at all **because the chart turns on a second server inside the hub**
> (`--enable-runtime-broker`).
>
> **The corollary is about ownership, and it is the half that gets skipped.** "This belongs to
> whichever phase next touches it" **is not an owner** — an item with a conditional owner is not
> queued, it is dropped, and it is dropped in the particular way that leaves behind a note
> explaining why dropping it was fine. Name a phase number. Where a choice exists, **give it to
> the phase that will breach the guard**, because that phase has the motive to make it general:
> Phase 5a owns the **alias enumeration** (§18 item 10c) precisely because 5a is the phase that
> wants a public URL. *(Not "the `server.hub` allow-list" — that was the first form of the
> assignment and it was rejected the same day; see 10c for why the prefix is not the hazard.)*

> **A ninth rule, revision 8 (`gd-em`, on the fifth instance in one day): a check whose pass
> condition is the ABSENCE OF FAILURE rather than the PRESENCE OF N SUCCESSES reports success
> when it runs on nothing.** The two are identical on every passing run and differ only when the
> input set is empty — which is exactly the state a mistake produces. Today's members, and the
> count is the argument: empty-set `kubeconform`; `assertStartupBudget` over the `deferHub`
> render; an unrecorded sweep silence; eleven rules evaluated over an empty table; and a guard
> that no-ops under `helm template`. **Five distinct authors, one shape.** So every check states
> the number of things it examined and fails when that number is zero or unexpected. This is the
> generalisation of rule 5 — rule 5 says prove the detector fires, rule 9 says prove it was
> *pointed at anything* — and it is the cheaper of the two to add.
>
> **Its sharpest special case is worth naming separately, because it is invisible rather than
> merely weak:** *a guard that disappears under `helm template` is worse than none, because the
> CI that proves the chart safe is the CI that cannot see it.* `lookup` returns empty during
> `helm template`, `--dry-run` and every render-only client, so a `lookup`-based guard is absent
> in precisely the tooling that renders manifests for review and present only at install. Phase
> 6's whole suite would then be evaluating a chart it cannot inspect. **This is why no chart-side
> render guard can close a route whose data exists only at apply time** — a scope fact, not a
> cost trade-off, and the reason to record in any comment rejecting one.
>
> **Extension, revision 8, adopted PROJECT-WIDE rather than as a Phase 6 note (`gd-em`): where a
> check counts anything, COMMIT THE NUMBER AND FAIL ON INEQUALITY IN EITHER DIRECTION.** A floor
> — *"at least N"* — goes stale in the safe-sounding direction: the corpus grows, the floor does
> not, and `32 > 30` prints green while coverage silently falls. **That is rule 9 wearing a
> number**, which is what makes this an extension rather than a separate rule. A floor makes
> growth invisible; an exact count makes every change to the corpus a reviewed line in a diff.
>
> **The cost is real and is accepted knowingly: the check fails on legitimate additions. That
> cost IS the mechanism.** And the fuse lit within the hour it was specified — the citation
> corpus went 29 → 30 references and 38 → 39 defined items during the same editing session that
> wrote the rule. Under a floor of 29, every bit of that would have passed unremarked.

> **A tenth rule, revision 8 (`gd-em`): a DEFAULT may encode a preference; a REFUSAL must encode
> a HARM.** For every configuration the chart refuses, cite the `file:line` **outside the chart**
> that makes the refused configuration dangerous — or downgrade the refusal to a default. The
> evidence is a guard that survived a mutation test, a false-positive check and a full review,
> and then failed on the question *does the thing it defends against actually exist?* That axis
> is answerable only from **outside the file the guard lives in**, which is why exhaustive local
> verification does not reach it — and worse: **verifying the mechanism is what produces the
> feeling of having checked.** Thoroughness on the reachable axes is therefore a *risk factor*
> for missing this one, not a defence against it. Two procedural consequences, both adopted:
> **record the non-instances with reasoning**, because a silence is indistinguishable from not
> having looked; and **an author names their own most-invested refusal for external audit**, on
> the reasoning that the newest claim one has the most invested in is the one its author is least
> able to audit fairly. That reason is correct and is almost never the one given.

> **An eleventh rule, revision 8 (`gd-em`, with a corollary from `gke-deploy-lead`): a findings
> entry about CODE BEHAVIOUR without a `file:line` cite is an ASSERTION, and should be labelled
> as one.** Not suppressed — labelled. An unsourced observation is often the most valuable thing
> in a findings message, and the cost of demanding a cite before it may be written is that it
> does not get written. The cost of *not* marking it is worse and arrives later: an assertion
> that spends one relay hop next to sourced claims becomes indistinguishable from them, and the
> next reader inherits it as established. So write it, and write what it is.
>
> **Corollary (`gke-deploy-lead`): a quoted string must be cited with the line number of the
> thing it is about, not the line number of where you found it.** These come apart constantly
> and the failure is invisible to the author, because the author *did* open the file and *did*
> read the line — the cite is honest and still wrong. The worked example is the nastiest
> derived-source failure of the day: right file, right function, **one line off**.
> "`cmd/server.go:236` deprecates `--config`" is a sentence nobody can falsify by inspection;
> `:236` deprecates `production` and `:237` binds `--config`, so the correctly-cited version —
> "`:237` binds `--config`" — makes the mismatch visible to **any** reader, including the
> author re-reading their own draft. **A cite proves you opened a file; a cite of the subject
> proves you found the subject.** The two feel identical while writing and only the second one
> survives a relay.
>
> This is why §17.2 and the rules above cite `§18 item N` by number rather than by description,
> and why Phase 6 carries a mechanical check that those numbers resolve (Phase 6, citation
> integrity). Rule 9 applies to that check as it applies to everything else: it must count the
> citations it resolved, not merely fail to find a broken one.

> **A twelfth rule, revision 8 (`gd-em`, with the asymmetry from `gke-deploy-lead` and the
> second half from `gd-p0-rev-3`): A RATIONALE IN A PHASED ARTIFACT MUST BE TENSED TO THE PHASE
> THAT RENDERS IT.** Rule 8 covers findings deferred by a configuration; nothing covered
> **rationales** deferred the same way, and that was the gap.
>
> **BOTH TENSE ERRORS ARE LIVE ON THIS PROJECT AND THEY FAIL DIFFERENTLY.** The rule was first
> written with only the first half; `gd-p0-rev-3` found the second half in the artifact within
> hours, so both are stated here rather than split across two rules:
>
> | | **TRUE NOW, FALSE LATER** | **FALSE NOW, TRUE LATER** |
> |---|---|---|
> | **Failure mode** | Goes stale | Does not go stale — it is simply **wrong today** |
> | **What it costs** | A good reason is lost to tidying | **An operator is misinformed at the moment of refusal** |
> | **Who catches it** | **Nobody** — a stale comment reads as merely old | **Anyone who checks the claim** |
>
> The second is **strictly worse**, and the reason is not severity in the abstract: two of the
> five instances `gd-p0-rev-3` found are in **`fail` messages an operator reads at the moment
> they are being refused** — that is, while they are trying to comply. A false explanation
> delivered exactly then is the worst possible delivery time for one.
>
> **Note the asymmetry in how the two are caught, because it explains the shape of our own
> record.** The false-now half was found five times in a single axis-d sweep of PR 1093 at
> `721fc77` — all five Required findings in `gd-p0-rev-3`'s REQUEST CHANGES were this one error
> — because checking any claim against source surfaces it immediately. The true-now half went
> unnoticed until we reasoned about it abstractly, because there is no check it fails. **A rule
> whose violations are easy to find is not thereby the more dangerous rule.**
>
> The remedy for both is a **two-state statement keyed to the transition**, naming the condition
> that flips it and the phase that makes the condition hold: not *"X is inert"* but *"X is live
> today; it becomes inert when C holds, and P is the phase that makes C hold."* §5.2's second
> erratum is the true-now worked example, and it was found the same day this rule was written:
> the `--config` rationale is true of Phase 1's render, false of Phase 0's, and the comment
> carrying it lives in Phase 0's chart. Note what the two-state form buys beyond accuracy — it
> makes the comment **falsifiable by a test**, because a condition is assertable and a mood is
> not.
>
> **🔴 THE DISCRIMINATOR, FOUND BY COMPARING TWO CLAIMS THAT AGED DIFFERENTLY IN ONE FILE — AND IT
> IS THE MOST USEFUL SENTENCE ON THIS RULE** (`gd-p0-rev-3`): **R1 survived the phase boundary
> because it was written CONDITIONALLY** — *"once the settings document carries a non-nil `server:`
> key…"* — **and `_helpers.tpl:1089` did not, because it was written in the PRESENT TENSE.**
>
> > **A CONDITIONAL CLAIM AGES. A PRESENT-TENSE CLAIM ROTS.**
>
> `:1089` is *"This chart delivers none of them yet"*: **true at Phase 0, false at `11a78701`** —
> cited, grounded, and accurate on the day it was written. **This is why the grounding-count check
> cannot catch this class** (rule 28): nothing about the sentence is ungrounded, so no density
> moves when the phase that falsifies it lands.
>
> **The two acceptable forms, and everything else is a rot candidate:** rewrite the claim
> **conditionally** so it ages, or **key it to a committed state number** so the number and the
> paragraph must move in the same diff. The second is stronger where a number already exists.
>
> **🔴 AND THE FINDING UNDER ALL OF IT, WHICH IS THE SHARPEST APPLICATION OF THE SWITCHED-OFF-GUARD
> FAMILY ANYONE HAS MADE, BECAUSE THE DISABLED GUARD IS US** (`gd-p0-rev-2`): **eleven instances of
> this class are on record and every single one was found by a person reading prose. THERE IS NO
> DETECTOR.** The class was always caught by review, **so nobody ever asked what catches it when
> review is not looking** — a guard switched off by exactly the condition that makes it necessary
> (rule 14's family label), with review itself in the guard's place. That is the argument for
> sweeping the class rather than patching the sentence, and the sweep is Required in Phase 0's
> Commit A (§17.2).
>
> **Two worked examples for the false-now half, both from one file, and the PAIR is the
> argument** (`gd-p0-rev-3`).
>
> **First — the remedy was already in the file and did not get applied.** `_helpers.tpl:622-627`
> (`721fc77`) is `gd-p0-dev`'s own paragraph: ***"NOTHING ABOVE IS A STATEMENT ABOUT A LATER
> PHASE… a requirement stated here, not an observation of code that exists here."*** That is
> exactly the correct treatment, written by the author, in the file — **and applied to precisely
> one of the sites that needed it.** Record this next to `gd-p0-dev` authoring rule 15's content
> into `_helpers.tpl` about an hour before committing an instance of rule 15, because the two are
> the same evidence: **THE RULES DO NOT WORK AS REMINDERS EVEN WHEN THEY ARE IN THE FILE BEING
> EDITED.** They work as mechanisms. Nothing else on this project has worked.
>
> **Second — the cleanest demonstration anyone has produced.** `cmd/server_foreground.go:1168-1200`
> is cited in `_helpers.tpl` **twice for the same advisory lock, in two different tenses**:
> - `:296-299` — **correctly future-tensed**: *"The Cloud SQL phase sets the postgres driver and
>   turns `isHADeployment` true… answered there by Postgres advisory locks… by the hub, not by
>   this chart."*
> - `:256` — **falsely present-tensed**, inside the `assertStartupBudget` fail message: *"First
>   boot blocks on an unbounded schema-migration advisory lock before the listener binds."*
>
> `migrateStore` returns `s.Migrate(ctx)` directly unless the driver is `postgres`
> (`server_foreground.go:1169`), and the default driver is `sqlite` (`hub_config.go:540`). On
> this chart today there is no lock and no blocking. **One citation, two tenses, one false, same
> file, same author, same day.** Use this example in preference to any other, because it removes
> every competing explanation: not a bad source, not a dropped SHA, not a misread mechanism, and
> **the correct version was already on the page.** Only tense discipline is left.
>
> **`gd-doc` adds a third citation of the same range, found while verifying the second example:
> `_helpers.tpl:234`** carries the same claim in the same false tense — *"The lock is a BLOCKING
> `pg_advisory_lock(LockSchemaMigration)` taken before `Migrate` returns and therefore before the
> hub serves"* — in the doc-comment block that **supplies the fail message its harm**. So the
> ratio is three citations, two false, and the false pair is a source and its derivative rather
> than two independent slips: the comment justifies the refusal and the fail message repeats it
> to the operator. Note also that `:226-240` is **mixed-tense within one paragraph** — the
> `CompositeStore.Migrate` ordered-sequence half (`composite.go:179-227`) is true under `sqlite`
> today, and only the *lock* half is Phase 2's. A paragraph can be half-tensed, which is why
> the check has to be per-claim and not per-block.

> **A thirteenth rule, revision 8 (`gd-p1-dev`): a mechanism restated MORE strongly than it is
> gets a mitigation sized to the restatement — and UNDERSTATING it undersizes the mitigation
> just as reliably, while reading as caution.** The two are mirrors and belong adjacent, which
> is why they are one rule. Only the overstating half was ever in circulation here; it had never
> been written down, and the understating half is the one that escapes notice, because a
> too-weak description of a hazard *sounds* like the responsible register. Nobody audits a
> sentence for being insufficiently alarming.
>
> The worked example is a containment ladder, and each rung defeats the mitigation built for the
> rung below: **a mitigation scoped to prevent REDIRECTION does not cover an OVERLAY, and one
> scoped to prevent an OVERLAY does not cover a SOLE-SOURCE SUBSTITUTION.** Both live in this
> document. `server.hub.public_url` was described as a preventive risk and is an **overlay**
> that `config.extra` applies today (§18 item 10b).
>
> **`--config` is the sharper example, because one flag spans two rungs and the chart's comment
> currently names only the lower one.** Which rung you get depends on the shape of the file at
> the target, and nothing in the flag distinguishes them:
> - Target directory holds a `settings.yaml` with a non-nil `server:` key → route A's fallback
>   (`hub_config.go:647-660`) returns that section **as the whole config**
>   (`ConvertV1ServerToGlobalConfig`, `:1360`). **Sole-source substitution.**
> - Anything else → route B (`:772-788`) layers the target file over the embedded defaults.
>   **Overlay.**
>
> The reserved-flag `fail` message at `_helpers.tpl:859` (`721fc77`) says `--config` and `-c`
> *"layer a second config file over the hub's own"* — accurate for route B and one rung short of
> route A. The guard is unaffected because it refuses the flag outright; the **reason** is
> undersized, and a future reader sizing a narrower mitigation to that sentence would build for
> the overlay. **The same sentence carries a second defect of a different kind**, which is why it
> is worth quoting in full: it continues *"and go silently inert once the configuration phase
> renders a settings file"* — the banned file-existence formulation, in a `fail` message, where
> the trigger is the **key** (rule 12's false-now half, §5.2). One sentence, understated
> mechanism and wrong trigger, and the guard above it is correct in both cases. **That is the
> pattern this rule exists to catch: a right refusal is not evidence about the sentence
> explaining it.**
> **So the test on any hazard sentence is not "is this defensible?" but "what is the strongest
> form of this mechanism, and is the mitigation sized to that?"** — answered by describing the
> mechanism before choosing the mitigation, never the other way round.
>
> **A note on how the unwritten half stayed unwritten, because it generalises past this rule**
> (`gd-em`). The overstating half circulated for the whole project, was cited by number as an
> "existing rule", and had no home. That is rule 15 operating on our *process* rather than on
> the code: **a claim that circulates unchallenged accumulates the SOCIAL evidence of being
> established while accumulating none of the DOCUMENTARY evidence, and the two feel identical to
> everyone in the conversation.** Agreement substituted for the artifact. The countermeasure is
> rule 20.

> **A fourteenth rule, revision 8, RESTATED — and the restatement is a MERGE, not an addition
> (`gd-p0-rev-3`'s unification, ruled by `gd-em`; the diagnosis is `gke-deploy-lead`'s and the
> remedy is `gd-p0-rev-2`'s):**
>
> > **A CLAIM MUST BE CHECKED AT ITS MECHANISM, NOT AT ITS OUTCOME, BECAUSE THE OUTCOME IS WHAT A
> > BROKEN WORLD ALSO PRODUCES.**
>
> One statement covering the prose defects and the harness defects, which had been sitting in
> this registry as two separate entries all day while three people independently rediscovered
> the connection. The four cases are the argument, and note that **two are sentences and two are
> shell**:
>
> | Case | Conclusion | Reason | Result |
> |---|---|---|---|
> | **R2** — *"no `settings.yaml` exists"* | right | **false** (the trigger is the `server:` key, not the file) | survived every check |
> | **R5** — the 300s startup budget | right | **false** (a lock never taken under sqlite) | survived every check |
> | **`reject()`** — the flag was refused | right | **unverified** (*something* exited non-zero) | 29 green `ok` lines with no `helm` |
> | **`chart-integrity.sh`** | right | **asserted** (matches the schema's own error text) | degrades closed for free |
>
> **The measurement is `gd-p0-rev-2`'s and it is the only place this property is measured rather
> than argued.** Same author, same hour, three scripts, at `60b2912` with `PATH=/usr/bin:/bin`
> and no `helm` installed (rule 27):
>
> | Script | `ok` | `FAIL` | What its negatives assert |
> |---|---|---|---|
> | `reserved-flags.sh` | **29** | 2 | the **outcome** — a non-zero exit |
> | `update-strategy.sh` | 0 | 4 | a **positive property** — the type appears in output |
> | `chart-integrity.sh` | 1 | 25 | the **reason** — the schema's own error text |
>
> Twenty-nine assertions declaring the reserved-flag mechanism verified, **by a script that
> cannot render a template**, because `reject()` treated any non-zero exit as proof of refusal
> and *binary not found* is non-zero. Then the counterfactual, measured rather than argued:
> remove **only** the two `accept` calls, set `EXPECTED_TOTAL=29`, run with no `helm` —
> **`PASS 29/29`, exit 0.** Fail-closed contract green, committed count green, `-ne` inequality
> green, and `run-all.sh` would have summed it. **Two assertions out of thirty-one were carrying
> the entire script**, and they exist only because a reviewer required a positive twin.
>
> **This supersedes "every negative assertion needs a positive twin" (§13.1) as the PRIMARY
> guidance, and the twin survives as belt-and-braces.** A twin is a *second* assertion: it costs
> an extra case and protects the script only in aggregate — 29 of 31 still lied, and the script
> was two deletions away from lying completely. Reason-matching fixes each assertion **at the
> site**, costs a substring, and **cannot be deleted by trimming a different test.**
>
> **The provenance argues for the rule better than the rule does.** `chart-integrity.sh` is
> near-immune *by accident*: its author matched `Additional property … is not allowed` for an
> unrelated purpose — to stop a `_helpers.tpl` `fail` masquerading as schema enforcement — and
> got fail-closed-under-tool-absence for free. **The safe property was a side effect of demanding
> specificity for a different reason**, which suggests specificity is the load-bearing virtue and
> fail-closed is downstream of it.
>
> **Project-wide, not a Phase 0 rule (`gd-em`): a negative test asserts the MESSAGE of the guard
> it names, never merely a non-zero exit.** Every phase writes negative tests. Phase 7 already
> complies and arrived there independently for a third unrelated reason — its guard-defeat matrix
> matches per-rule substrings so that a fixture rejected by a *different* rule still goes red.
> **Rule 28 was discovered three times before it was stated once**, by three authors each solving
> a different problem, which is the strongest evidence behind any entry in this registry.
>
> **Its place in the 24–26 set: it is the CHEAPEST, and therefore the one most likely to actually
> be applied.** It is the only member whose remedy is available **at the moment of writing a
> single assertion** — no outsider, no constructed environment, no re-reading of logs.
>
> **The scoping corollary, and it is the same ruling arriving at the harness that was made on the
> prose four hours earlier: A FIX SCOPED TO THE REPORTED INSTANCE RATHER THAN TO THE MECHANISM IS
> A DEFERRAL WEARING A PATCH'S CLOTHING** — and it is *harder* to catch than an open deferral,
> because the reporter's test goes green. The prose precedent: five tense defects were ruled one
> defect and fixed as a per-claim pass, and treating them as a shape rather than a list found
> **eight**. The harness instance is in §17.2 under Phase 0.
>
> **`gd-p0-rev-3`'s name for the family, which is better than "stale comment" and should be the
> label the instances sit under: A GUARD SWITCHED OFF BY THE CONDITION THAT MAKES IT NECESSARY.**
> Three instances in two days: the `-lt` floor, `run-all.sh:114`, and now `reject()`.
>
> **The sharpest positive instance anyone has produced is `gd-p1-dev`'s, arrived at independently
> and in a fourth codebase** (`gke-deploy-lead`): `expect_render_failure` asserts the **message**
> rather than the exit status, **and additionally rejects any message containing `%!`.** That
> second clause is the rule in one substring — a Go format verb that failed to render its own
> value means the diagnostic is broken, so **a `fail` message the template could not even format
> is exactly a right-outcome-wrong-mechanism pass**, and the exit status cannot tell you. The
> author's own statement of it is the sharpest sentence on this rule in the project: **the wording
> is the outcome; the rendered value is the mechanism.**
>
> The measurement (`gd-p1-dev`, at `fda8c558`, helm 3.16.3 and kubeconform 0.6.7 present):
> `printf "%q"` against a nil renders the literal text `%!q(<nil>)`, **and the asserted wording is
> still perfectly there** — so six diagnostics could reach an operator with a Go format error
> sitting in the middle of the one sentence telling them what the chart read, with no assertion
> anywhere noticing. Mutation-tested by adding a `%q` of a nonexistent value to a message whose
> asserted wording was left untouched: two assertions went red.
>
> **Note where the fix went, because that is the rule 22 half of it.** Not *"quote the value
> properly at these six sites"* but **one line in the negative-test helper** —
> `if grep -qF -- '%!' <<<"$out"; then fail …` — so **a diagnostic added by a later phase is
> covered without anyone remembering to cover it.** And nil is not exotic, which is why this is
> not cosmetic: `mode:` with nothing after it is **null, not absent**, so `dig`'s default never
> applies.
>
> ⚠ **A numbering collision, recorded rather than resolved silently** (rule 20). `gd-em` issued
> the reason-versus-outcome rule as **"rule 28"** and, an hour later, ruled it a merge into rule
> 14. The number 28 was already taken by `gke-deploy-lead`'s *a control must precede the artifact
> it governs*, ruled on earlier the same day. **There is one rule 28 and it is the control-precedes
> rule**; the reason-versus-outcome material lives here, in 14. This is the second time in one day
> a "new" rule turned out to be in the registry under another number, and both were caught by
> **resolving** the citation rather than recalling it — rule 20 earning its place twice.
>
> **The diagnosis this rule was originally recorded for is unchanged and is now its first half:
> the dangerous shape is a RIGHT CONCLUSION ON A FALSE REASON WHERE THE TRUE MECHANISM POINTS THE
> SAME WAY.** The standing form recorded
> in this section was that a true conclusion resting on a checkable, wrong reason **dies when
> the reason is checked**. This variant does the opposite: it **survives** the check, so the
> outcome *confirms the bad reason* and nobody revisits it. Four successive false rationales
> stood on `--config` while every check came back "yes, reserve it."
>
> Two things attach, and both are about how the shape is caught and how a correction is made to
> land:
> - **Verification by a DIFFERENT ROUTE is the only method on record that has caught this
>   class** — not re-reading, because a re-derivation along the original route reproduces the
>   original premise. §5.2's second erratum was found from the boot path and the image, not from
>   the load path everyone had already read.
> - **A correction to a reason under a surviving conclusion is unpersuasive without a NAMED
>   CONSEQUENCE.** This is the configuration in which corrections are hardest to get accepted:
>   the author holds a verified conclusion and hears the correction as pedantry, correctly
>   noting that nothing they shipped changes. So raise it by naming the downstream victim.
>   Worked example: if the `--config` trigger is believed to be *the file*, Phase 1 satisfies it
>   by mounting any Secret, then nests server config under a `profiles:` entry and leaves
>   `--config` live with the comment saying inert. That sentence is what makes the correction
>   land; "your reason is imprecise" is not.

> **A fifteenth rule, revision 8 (`gd-p0-rev-2`) — and it is rule 14's REMEDY, so read them
> together: AGREEMENT ON A CONCLUSION IS NOT CORROBORATION OF ITS REASON.** Two derivations that
> agree on the answer have checked nothing about each other until the **reasons** are compared.
> The precedent is from our own record and `gd-p0-rev-2` supplied it against itself: three of us
> agreed `RollingUpdate` was unsafe and read the agreement as confirmation — **the harm did not
> exist.**
>
> The operational half is what makes this a rule rather than an aphorism: **when two agents reach
> the same conclusion, the artifact to exchange is the DERIVATION, not the VERDICT.** Verdict
> exchange is cheap, and verdict exchange is exactly what let four successive false rationales
> stand on one flag while everybody kept agreeing the flag should be reserved.
>
> This also corrects why rule 14's different-route method works, and the correction matters
> because the wrong version is the intuitive one. **The second route is not more reliable.**
> It works because **different routes produce different REASONS, and somebody has to diff the
> reasons.** Two routes to one conclusion are worth nothing unless the routes are compared —
> which means a second verification that reports only its verdict has done no work at all.

> **Rules 16 to 21, revision 8 — six rules from one incident.** They are numbered separately
> because they fire separately, but they came out of a single disagreement about a single string
> in a single file, recorded as **instance 10** below. Read the instance first if any of them
> looks like a truism; every one of them was violated by an experienced agent on this project
> today, and four of them were violated by the agent who wrote the rule.

> **Rule 16 (`gd-p0-dev`): A HAND-WRITTEN LIST OF REVISIONS IS AN ASSERTION ABOUT HISTORY, NOT A
> SEARCH OF IT.** "This string appears in commits X and Y" is a claim of the same standing as any
> uncited claim about code behaviour (rule 11) — with one extra hazard specific to history:
> **the list cannot be curated by someone who already has an answer in mind, but a search can't
> be, and that is the entire difference.** Run the search; paste the command; paste its output.
>
> **⚠ `gd-doc` correction to the prescribed remedy, and it matters because two developers are
> about to adopt it.** The prescribed command was
> `git log --all -S <string> -- <path>`. **`-S` is a pickaxe: it reports commits where the
> string's COUNT CHANGED, not commits that CONTAIN it.** Verified against this very incident —
> for the `$neverPassed` fail string in `_helpers.tpl`, `-S` returns `7911e16` and `51f62ab`
> and **does not return `cb183de`, which contains the string verbatim at line 435**. The
> prescribed remedy would itself have missed one of the two commits the incident is about, and
> would have missed it *silently*, which is rule 9. **A containment question needs a containment
> search**: `git grep -n <pattern> $(git rev-list --all) -- <path>`, or `git show <rev>:<path>`
> per revision. Use `-S` only when the question is genuinely *"where did this change?"* The rule
> stands; its mechanism is corrected.

> **Rule 17 (`gd-p0-dev`): WHEN SOMEONE SENDS YOU A CITE YOU CANNOT REPRODUCE, THE FIRST
> HYPOTHESIS IS THAT YOUR SEARCH IS WRONG, NOT THAT THEIR FILE IS.** The ordering is the rule.
> Both hypotheses get tested eventually; which one is tested *first* decides what gets said in
> the interval, and "your cite doesn't exist" is unrecoverable in a way that "I can't find it,
> here is exactly what I ran" is not. The second form also hands the other party the thing they
> need to resolve it — your command — so it costs nothing and does the work. In instance 10 the
> failed reproduction was caused by a wrong search (rule 16's `-S`) *and* by a tree that was
> deliberately behind (rule 18), and neither party's file was wrong at any point.

> **Rule 18 — ONE RULE, THREE AUTHORS, and it is presented as one because they are one:
> A `file:line` CITE IS A COORDINATE IN A TREE, AND A COORDINATE WITHOUT ITS ORIGIN IS NOT A
> CITATION.**
> - `gd-p0-rev-2`: a line number is a coordinate in a tree; without the tree it names nothing.
> - `gd-em`: **a `file:line` cite crossing an agent boundary carries the SHA, or it is an
>   assertion** — the same standing rule 11 assigns to an uncited claim.
> - `gd-p1-dev`: **when your tree is deliberately behind someone else's, every cite you send is
>   a cite INTO THE PAST and has to say so.**
>
> The third is not hygiene, it is **structural in a phase stack**: Phase N's developer is
> *supposed* to be behind Phase N+1's, that is what phasing means, so the condition producing
> stale coordinates is permanent here rather than accidental. Every cite in this document from
> the hub source is against `721fc77` unless it says otherwise, and that sentence is the cheapest
> possible compliance. Where an agent is working a branch of its own, the SHA goes on the cite.

> **Rule 19 (`gd-em`, and he most wants this one in): VOLUME OF CHECKS IS NOT INDEPENDENCE OF
> CHECKS. ASK WHAT PREMISE THEY SHARE.** Four checks resting on one premise are **one check**,
> and they report as four. This is the mechanism behind rule 15 — agreement felt like
> corroboration precisely because the count was mistaken for the coverage.
>
> **This attaches directly to the axis a/b/c versus axis d distinction** (rule 10, §13.1). Axes
> a, b and c all ask *does the artifact do what it says*; axis d asks *is what it says true of
> the tree*. Any number of a/b/c passes cannot substitute for one d pass, because they share the
> premise that the artifact's own account of the world is accurate — and that premise is exactly
> what the false-now tense error (rule 12) violates. `gd-p0-rev-3`'s axis-d sweep found five
> Required findings in one pass, in a file that had already been reviewed. **So the review
> question is not "how many checks passed" but "how many DISTINCT PREMISES did the passing
> checks rest on".**

> **Rule 20 (`gd-em`, against himself): BEFORE CITING A RULE BY NUMBER, OR BY THE WORD
> "EXISTING", RESOLVE IT.** A rule cited from memory is an assertion, exactly as a `file:line`
> cite from memory is (rules 11 and 18) — the artifact is this document instead of a source
> file, and nothing else about it differs. The incident: an instruction to place rule 13's new
> half *"directly beside the existing rule"* about overstatement. **§17.1 had no such rule.** It
> had circulated in messages for the whole project and was never written down (see rule 13's
> closing note). **A hand-curated list of commits and a remembered rule number are the same
> error, and the tell in both cases is that the citation was produced without the artifact being
> open.** Phase 6's citation-integrity check is the mechanised form of this rule for `§18 item N`
> references; for `§17.1 rule N` there is no check yet, so it is a discipline — which is a
> weaker thing and is acknowledged as one.

> **Rule 21 (`gd-p0-rev-2`, from catching itself): AN EXACT-STRING GUARD AGAINST A SEMANTIC
> REGRESSION IS DEFEATED BY REWORDING. PREFER A POSITIVE ASSERTION ABOUT WHAT AN ARTIFACT IS —
> *this block is byte-identical to that one* — OVER A NEGATIVE ASSERTION ABOUT A PHRASE IT MUST
> NOT CONTAIN.** A negative guard enumerates the wordings you thought of; the space of wordings
> is unbounded, and a rewrite that preserves the false claim is the *likeliest* way the claim
> survives, since anyone editing the sentence is already touching the words.
>
> **The existence proof is in the tree and `gd-doc` verified it.** The killed phrase — *"it
> redirects the hub's entire configuration load away from the settings file this chart
> delivers"* — has **zero occurrences at `721fc77`**, so an exact-string guard passes clean.
> The same false claim survives one screen away at `_helpers.tpl:848-852`: *"`--CONFIG` would
> crash-loop as an unknown flag **rather than redirect the config load**."* Guard green, claim
> alive, and the green run is the thing that stops anyone looking (rule 9).
>
> **`gd-doc` note on scope, because this rule can be over-applied.** A positive assertion is
> only available where the intended state is *expressible* — byte-equality against a fixture,
> an exact count (rule 9), a resolved reference (Phase 6's check). Where it is not, a negative
> guard is better than nothing and must be **labelled with what it does not cover**, in the
> guard, next to the pattern. An unlabelled negative guard is a claim of coverage it does not
> have, which is rule 13's understatement half pointed at a test.

> **Rule 22, revision 8 (`gd-em`) — RULE 9 DOES NOT COMPOSE. A SET OF INDIVIDUALLY NON-VACUOUS
> CHECKS IS VACUOUS AT THE SET LEVEL.** Each script's fail-closed contract is a claim about
> **that script's own execution**, and no script can make a claim about a script that was never
> invoked. Rule 9 hardens the leaf; nothing was hardening the root.
>
> **Keep the corollary, because it is the part that makes this dangerous rather than merely
> untidy: THE STRONGER EACH COMPONENT'S INTERNAL CONTRACT IS, THE MORE CONFIDENT THE RUNNER IS
> THAT GREEN MEANS COVERED.** Rigour at the leaves buys **false assurance at the root**. This is
> the same shape as the axis-d corollary that a well-tested guard *raises* the prior that its
> justification was skipped (rule 10) — in both cases the visible quality of the artifact is what
> stops the unasked question from being asked.
>
> **Two worked examples, and the pair is what makes it a rule rather than an observation.**
>
> **First, the ambient one** (`gd-p7-rev-2`): the repository has three `hack/check-*.sh` scripts
> and **no meta-check that every one of them is wired**. Enforcement for the Dockerfile guard
> lives in **two deletable lines** — `.github/workflows/ci.yml:90-91` and the `dockerfile-stages`
> prerequisite in `Makefile:165` (`ci:`), repeated at `:170` (`ci-full:`), all at `89bd1c0`
> (PR 1095 head; rule 18). Delete the step and the prerequisite and the script stays in the tree,
> passes its own self-test on demand, and never runs. `gd-p7-rev-2` correctly declined to file it
> against PR 1095 as ambient rather than introduced, and `gd-em` took it as ambient.
>
> **⚠ `gd-doc` verified the ambient example and it is not hypothetical — ONE OF THE THREE IS
> ALREADY UNWIRED.** `hack/check-authz-guards.sh` has **no reference in `Makefile` or anywhere
> under `.github/`**, on `origin/main` and on `89bd1c0` alike; its own design doc
> (`.design/agent-id-fix.md:1258`) lists `Makefile` and `.github/workflows/ci.yml` among the
> files it changes, so it was **intended to be wired and is not**. And it is the most
> rule-9-compliant script in the repository — a `--self-test` arm, an `analysed <provenance>`
> count on every run, and an explicit `NOTHING WAS ANALYSED (skipped, not clean)` on both the
> missing-`rg` and zero-candidate paths. **Maximum rigour at the leaf, zero coverage at the root,
> and the leaf's rigour is precisely what made nobody look.** That is the corollary with a name
> and a line number rather than a prediction.
>
> **Second, and this is the one to lead with: WE REPRODUCED THE FAILURE INSIDE THE REMEDY FOR THE
> FINDING IT CAME FROM, WITHIN THE HOUR.** Phase 0 now has three harness scripts that each
> implement the fail-closed contract correctly and independently — `gd-p0-rev-2`'s 31-plus-4,
> `gd-p0-rev-2`'s `chart-integrity.sh` with 25, `gd-p0-dev`'s `render-guards.sh` with 46 — and
> **nothing asserts that all three ran.** Record explicitly that **nobody made a mistake at any
> leaf**: three authors applied rule 9 correctly, and the set was vacuous anyway. **The failure
> is compositional and not attributable, which is exactly why it needs a rule rather than better
> care.**
>
> **The remedy is rule 9's exact-count extension pointed at the runner — AND THAT INCLUDES
> COUNTING THE CHECKS.** One entry point enumerates the scripts and asserts the number executed
> against a committed count, so **a fourth script fails the suite until someone bumps the number
> in a diff**. Assigned to `gd-p0-dev`. Note the recursion and stop there: the entry point is
> itself a leaf whose invocation nothing asserts, so the wiring of the *single* entry point is a
> reviewed line in `ci.yml` and the `Makefile` and is not solved by a further meta-check. **One
> ring is closable; two are a regress.**
>
> ---
>
> **⚠ RULE 22's SECOND CONDITION, and it is the half that will bite THIS PROJECT: WIRED IS NOT
> TRIGGERED** (`gd-em`, joining the unwired script to a fact we had filed as plumbing). A check
> can be **absent from `ci.yml`** — the authz guard — or **present in `ci.yml` and never fire on
> the PRs that matter**. *Both produce a green PR page. Both are invisible from inside the
> script, however rigorous it is.*
>
> `ci.yml:20-21` (`89bd1c0`) is `pull_request: branches: [ main ]`, **so a PR based on another
> phase branch gets no check runs at all.** Verified by observed execution rather than by reading
> the YAML, which is the only evidence rule 9 accepts here:
>
> | PR | base | head | check runs |
> |---|---|---|---|
> | #1093 | `main` | `scion/gke-chart-p0` | 2 |
> | #1095 | `main` | `scion/gke-chart-p7` | 2 |
> | **#1096** | **`scion/gke-chart-p0`** | `scion/gke-chart-p1` | **0** |
>
> **Nine of this project's ten phases are stacked.** So a Phase 6 that wires every chart check
> correctly still delivers **zero enforcement on the phases it was built to protect** — the
> checks exist, are correct, are wired, and never run. That is rule 22 at the trigger layer, and
> it is worse than the wiring layer because the YAML *reads* as covering the PR.
>
> **`gd-doc` adds a third condition of the same shape, because it is the one Phase 6 will
> introduce itself: PATH FILTERS.** `docs.yml` already carries `paths: ['docs-site/**', …]`
> alongside the same `branches: [ main ]`, so it is doubly conditional today. `ci.yml` has no
> `paths:` filter — and *adding one is the obvious first optimisation* for a chart suite someone
> wants to keep fast (`paths: ['deploy/helm/**']`). **That filter would silently disable every
> chart check on precisely the PRs that change the hub source the chart's guards cite** — the
> `file:line` outside the chart that rule 10 requires a refusal to encode. Recorded here as a
> **standing prohibition rather than a caution**: no `paths:` filter on any workflow running a
> chart check, and if one is ever added the reason is written next to it and names what it stops
> checking.
>
> **So the condition to assert is FOUR-part, and each part is invisible to the part below it:**
> the check **exists** and fails closed (rule 9) → it is **wired** into an entry point whose
> count is committed (rule 22) → it **produced an observed check run on a PR whose base is not
> `main`** (this extension) → **and it FAILS CLOSED WHEN ITS TOOLING IS MISSING** (the fourth
> limb, `gke-deploy-lead`). Phase 6 owns all four, including for the meta-check itself, *or the
> meta-check is the next thing on this list.*
>
> **The fourth limb is not decoration on a CI runner, and the pairing is what makes it a limb
> rather than a detail: the authz script is WIRED NOWHERE; ours is WIRED AND FAILS OPEN. Neither
> is visible from inside the script.** A `helm` install step that silently fails is a normal
> Tuesday. Without the fourth limb, Phase 6 wires in a suite that reports a broken chart as green
> to everyone who reads the PR page — and **the failure mode of the fix is worse than the failure
> mode of having no CI, because no CI is at least KNOWN to be absent.** That sentence is the
> reason this project builds checks at all, stated as a constraint on how they are built.
>
> **✅ The trigger limb is achievable and the form is verified, so no phase has to discover it.**
> `gd-ci-probe-2` established **by observed execution** that a workflow with a bare
> `on: pull_request` and **no `branches:` key at all** fires on a PR whose base is not `main`.
> `gd-doc` re-verified from the API rather than from the report: **PR #1101, base
> `scion/ci-probe2-base`, one check run `probe`, conclusion SUCCESS, attached via
> `statusCheckRollup`**; probe branches confirmed absent from `ls-remote`. The workflow that
> produced it, at `79e306a`, in full:
>
> ```yaml
> name: CI Probe2 No Filter
>
> on: pull_request
>
> jobs:
>   probe:
>     runs-on: ubuntu-latest
>     steps:
>       - name: Echo
>         run: echo "ci-probe2 no-filter workflow ran"
> ```
>
> The property worth keeping is what is **absent**: no `branches:` key, and **nothing
> fork-specific** — no `scion/gke-chart-*` patterns that would ride upstream in a compare diff.
> Stacked-PR coverage costs the deletion of two lines, not the addition of a pattern list.
>
> **Out of scope, reported not fixed:** `check-authz-guards.sh` is authorization-guard
> enforcement that has been silently absent, is nothing to do with this chart, and was escalated
> to repo-maintenance for an owner rather than absorbed. Recorded here only as rule 22's
> measured evidence. **Ours to report, not ours to fix** — and note that reporting it *is* the
> deliverable, per rule 8: a finding outside scope is routed to a named owner, not deferred to
> nobody.

> **Rule 23, revision 8 (`gke-deploy-lead` via `gd-p0-rev-2`, and it goes in every review brief
> from here): A CHECK THAT EXISTS ONLY IN A REVIEWER'S MESSAGE HAS THE SAME DURABILITY PROBLEM AS
> AN INSTRUMENT DESCRIPTION THAT WAS NEVER TESTED.** **A review finding is a claim about the
> future; only a committed script is a mechanism.** A finding in a message is executed once, by
> one reader, at one moment, and then depends on that reader's diligence forever — which is the
> definition of the thing rule 9 exists to refuse.
>
> The example carries its author's name because of the symmetry: **`gd-p0-rev-2` filed a finding
> about an untested instrument description and wrote an untested prose requirement in the same
> message** — then converted it to a **mutation-tested script** rather than arguing the point.
> That is the whole rule: not that the prose was wrong, but that prose and script have different
> durabilities and only one of them survives the reviewer leaving.
>
> This closes the loop on rules 20 and 22: a remembered rule is an assertion, an unwired script
> is not a check, and a check that was never written is neither. **The only three things this
> project has found that reliably work are a committed count, a committed fixture, and a
> committed byte-comparison.**
>
> **A settled corollary about WHERE a script lives** (`gke-deploy-lead`, decided empirically
> rather than by argument): a script in `/tmp` is **unreachable to a reviewer at any cost**,
> however good it is. The in-repo version was run by the first person who was not its author
> within **four minutes** of landing. The cost of moving it in-repo had been deferred as
> expensive; **the cost was four minutes, and it was not the author's to spend** — the person
> paying for `/tmp` is every future reader, one at a time, forever. Rule 23 therefore covers
> location as well as existence: *committed* is the operative word, not *written*.
>
> **EXTENSION, and it is this rule's own family left open — recorded here rather than as a new
> number, because the first round fixed the reported instance and not the mechanism** (rule 14's
> scoping corollary, second worked example). `chart-integrity.sh` was built *because* of the
> durability objection above. It survived that round **because nobody asked which of the quoted
> gates were committed**:
>
> > **`grep -rn kubeconform tests/*.sh` returns nothing. There are 106 committed assertions and
> > none of them is `kubeconform`.**
>
> `5 valid / 0 invalid / 0 errors / 0 skipped` had been quoted in this project by **three
> separate agents**, including inside the verdict that approved the chart, and **it exists only
> in shell histories and in these messages.** The harness is *correctly* green without it.
> `gke-deploy-lead` found it by varying the apparatus **one notch past `helm`** — the environment
> mutation of rule 25, applied to a second tool.
>
> **The quoting is what hid it: five citations of a number made it look institutional.** That is
> the nastiest form of this rule, because repetition is normally evidence of provenance and here
> it was a substitute for it.
>
> > **STANDING, PROJECT-WIDE: A GATE YOU QUOTE IN A VERDICT MUST BE A GATE THAT RUNS IN THE
> > HARNESS, OR YOU SAY IN THE SAME SENTENCE THAT IT DOES NOT.**
>
> Same shape as rule 27 one level up — **provenance of the CHECK, not just of the run.** Where
> rule 27 asks *under what conditions was this number produced*, this asks *and was the thing
> that produced it committed*. A verdict citing an uncommitted gate is a measurement with no
> instrument on file.

> **RULES 24, 25 AND 26 ARE ONE SET AND THE ORGANISING LINE IS *WHO CAN FIND EACH*.** They came
> out of one harness failure and they sound similar, which is the danger: read as three warnings
> they collapse into "test more carefully", and a team then satisfies the cheapest of the three
> and believes it has discharged all of them. **They are distinguished by the vantage each
> requires, and that is what decides who has to go looking.**
>
> | Rule | Who can find it | Why the others cannot |
> |---|---|---|
> | **24** — one asserted field | **Nobody extra.** The author, on their own machine, from a log they already printed. | The evidence is *in* the passing run. |
> | **25** — inherited environment | **An outsider with a different environment**, or a deliberately impoverished one. | The environment is the constant the suite is expressed in. |
> | **26** — rotation sheds context, not environment | **Nobody in the rotation.** It must be *constructed*. | Fresh reviewers are the control, and this is the class it is blind to. |
> | **14** (restated) — assert the reason, not the outcome | **The author, at the moment of writing one assertion.** | It is not a *finding* at all — it is a way of writing that has no defect to find. |
>
> Rule 24 is the nastiest because nobody else is coming. Rule 26 is the most structural because it
> names the control that fails. **Rule 14 is the cheapest and therefore the one most likely to be
> applied** — it is the only member of the set whose remedy needs no outsider, no constructed
> environment and no re-reading of logs, and the only one that is discharged *before* the defect
> exists rather than after.

> **Rule 24, revision 8 (`gd-em`): A MUTATION THAT ASSERTS ONE FIELD OF A MULTI-FIELD OUTPUT HAS
> TESTED ONE FIELD. THE REST IS DECORATION THE RUNNER HAS TRAINED ITSELF TO SKIM — AND THE MORE
> HEADLINE-WORTHY THE ASSERTED FIELD, THE MORE RELIABLY THE OTHERS GO UNREAD.**
>
> **Corollary, and it is the operational half: THE EVIDENCE FOR A DEFECT IS OFTEN ALREADY IN A
> PASSING TEST'S OUTPUT.** Re-reading the suite's own logs is cheaper than any new check, and it
> is **the only method on record that finds this class without a second party.**
>
> **The worked example is the short-circuit defect, and it is chosen because it defeats the
> comfortable explanation.** The natural reading of that miss is rule 25's — an environment the
> suite could not vary. That is true of two of the three findings and **false of the headline
> one**, which was reachable from the inside all along. Short-circuiting is triggered by *a
> script reporting an assertion failure*, which is exactly what mutation **MM6** did, run
> deliberately, on a machine with `helm`. **MM6 printed a count line at the moment the count
> check was suppressed. The defect was in the suite's own output while the suite was being
> declared green.**
>
> What MM6 asserted was the **exit code** — *want 1, got 1* — and the count line went unread. So
> the gap was never the mutation; the mutation existed and fired. The gap was **an assertion
> about the field the mutation was not aimed at.** The suite held the evidence and skimmed it.
>
> **The remedy is mechanical and is assigned: assert the WHOLE LINE** — exit code *and* scripts
> *and* assertions *and* meta-failures — on all seven mutations, plus **MM7** for the
> removed-tool case, in the committed table. `gd-em` expects that to surface at least one more
> defect, which is a prediction the re-run will settle either way; record the outcome here when
> it does. **If it surfaces nothing, that is the MORE valuable entry** — we have no instances on
> record of a predicted defect turning out not to exist, and without one we cannot distinguish a
> project whose diagnoses are good from a project whose diagnoses are always confirmed. The
> precedent to cite at that empty slot is `RollingUpdate` (rule 15): three of us agreed and the
> harm did not exist.
>
> **A related outcome is already in, and it is a failure mode of PREDICTION rather than of
> testing** (`gd-em`, against himself). He predicted the false-accusation class correctly and
> **sampled its magnitude at one line. `tests/verify-failopen.sh` fails at `60b2912` with
> nineteen accusations against the chart.** An accurate diagnosis carrying a badly wrong severity
> estimate is its own failure mode, and it is a specific one: **it is how a real finding gets
> scheduled as a nit.** So a prediction states a magnitude as well as a class, and the magnitude
> is measured before it is used to rank the work.
>
> **Why this is nastier than rule 25 and belongs above it in a reviewer's attention:** it needs
> **no external vantage at all**. A suite exhibiting this defect can be caught by its own author,
> on their own machine, by reading what they already printed — which means nobody else is coming.

> **Rule 25, revision 8 (`gke-deploy-lead`): A MUTATION SUITE INHERITS THE ENVIRONMENT OF ITS
> AUTHOR, AND THE ENVIRONMENT IS THEREFORE THE ONE VARIABLE IT CANNOT MUTATE FROM THE INSIDE.**
> Every mutation the author can imagine is a mutation of the *artifact*; the machine the artifact
> runs on is the constant the whole suite is expressed in terms of. A suite written on a machine
> with `helm` cannot mutate `helm` away, and a suite that could would be testing something its
> author was not thinking about.
>
> **Rules 24 and 25 are recorded separately and deliberately, because they differ on the axis
> that decides WHO HAS TO FIND IT.** Rule 24's class is reachable from the inside — the evidence
> is in your own logs. Rule 25's class requires an **outsider, or a deliberately impoverished
> environment**, because no amount of care inside the suite reaches it. Merging them would let a
> team satisfy the one that needs no outsider and believe it had discharged the one that does.
> This is the axis a/b/c versus axis d distinction (rule 19) arriving a third time, now at the
> level of the test harness itself, and the fourth limb of rule 22's chain is its concrete form:
> **fail closed when the tooling is missing** is precisely the assertion an author with the
> tooling installed will never think to write.

> **Rule 26, revision 8 (`gke-deploy-lead`) — and this one is a stated limit on our PRIMARY
> CONTROL, which is why it is a rule and not an observation: REVIEWER ROTATION SHEDS CONTEXT; IT
> DOES NOT SHED ENVIRONMENT.**
>
> **The measurement is the argument.** Three parties — the author, `gd-p0-rev-2` and
> `gd-p0-rev-3` — reported **106/106**. `gke-deploy-lead` reported **0/106**. The only variable
> was the presence of `helm`. Rotating a fourth, fifth and sixth fresh reviewer through it would
> have produced 106/106 each time, and each pass would have added confidence while adding no
> coverage — **rule 19 with people instead of scripts.**
>
> Our entire control model is fresh reviewers, and it is **blind by construction** to any defect
> whose trigger is environmental. A fresh reviewer sheds the author's assumptions, their reading
> of the diff, their memory of why a line is there — all context, all valuable, and **none of it
> the machine.** The remedy cannot be *more rotation*; it has to be a **constructed** environment,
> which is why rule 22's fourth limb is an explicit mutation (MM7) rather than a hope that someone
> without `helm` eventually reviews us.
>
> Recorded here rather than left to be rediscovered, because a limit on a control is exactly the
> kind of thing a control's success hides: every one of those 106/106 runs was an honest,
> competent review.

> **Rule 27, revision 8 (`gke-deploy-lead`, adopted by `gd-em` as a subtree-wide convention
> effective immediately): QUOTE THE ENVIRONMENT WITH THE NUMBER.**
>
> Not `106/106`. **`106/106 with helm 3.16 and kubeconform present`.**
>
> The reason is exactly rule 18's, one level up: **a measurement without its conditions is an
> assertion.** `106/106` is unfalsifiable — there is nothing in it for a reader to disagree with.
> The annotated form is falsifiable **by someone without those tools, which is precisely how this
> defect was found.** And note what the clause actually communicates: it is not the number that
> tells the next reader what to do, it is **the conditions, because they say what they would have
> to change to get a different answer.**
>
> This costs a clause and converts an unfalsifiable claim into a falsifiable one, so it applies to
> every count this project reports, including the ones in this document. **Worked example, applied
> to `gd-doc`'s own headline number:** not *"30 references resolved"* but ***"30 references, 12
> distinct, 39 items defined, 0 unresolved — measured over `design.md` at revision 8 with the
> Phase 6 spec subsection excluded, naive line-based matcher 25."*** The exclusion and the naive
> number are the conditions; without them the 30 is a boast.
>
> **SCOPE NOTE, and it exists because a convention that can be discharged by a meaningless clause
> is worse than none** — it manufactures the appearance of provenance (`gd-doc`, ruled in by
> `gd-em`). **`106/106 on my machine` satisfies the letter of this rule and communicates nothing.**
> Two halves, and they answer different questions:
>
> - **The test — is the clause any good?** *Could a reader rebuild the environment from it?*
>   `git archive` into a clean tree with no `helm` on `PATH` passes; *"on my machine"* does not.
> - **The operational half — which conditions do I list?** The test does not answer that, and
>   this is where the convention will actually fail: not by writing *"on my machine"* but by
>   listing eleven true and irrelevant facts (kernel, distro, shell version) while omitting the
>   one that mattered. **Over-listing defeats the rule as thoroughly as under-listing and looks
>   far more diligent** — rule 13's understatement half wearing the opposite costume. So:
>
>   > **THE CONDITIONS WORTH QUOTING ARE THE ONES YOU HAD TO ESTABLISH.** If you installed it,
>   > exported it, mounted it, or checked it out, it is a condition. If it was simply there, it is
>   > probably background.
>
> The evidence is `gd-p0-rev-3`'s own confession: it quoted `106/106` as a gate, and **the tell
> was that it had put `helm` and `kubeconform` on `PATH` itself.** The act of provisioning is the
> signal, and it is available to the writer at the moment of writing with **no judgement about
> relevance required** — you are asking someone to recall what they did, not to predict what a
> future reader will need, which is why this is mechanisable and *"list what matters"* is not.
>
> **The strongest form states the falsifier inline.** Not `106/106`, and better than the
> annotated form: ***`106/106 across 4/4 scripts with helm 3.16.3 and kubeconform 0.6.7 on PATH;
> 0/106 with PATH=/usr/bin:/bin`.*** That does not merely permit disagreement — **it names the
> experiment.**
>
> **THE INPUT SIDE, which the toolchain framing misses entirely** (`gd-p1-dev`; it bites Phase 2
> rather than Phase 0, and is recorded now so it is not rediscovered there). A rendered-document
> count is a fact about the **values file**, not about the chart: **7 documents under the base
> values, 6 under `existing-secret`.** So a committed count is meaningless without the inputs it
> was measured under, and the environment clause must name them. Toolchain and inputs are two
> different halves of the same convention and the second is easier to forget, because the values
> file feels like part of the test rather than part of the environment.

> **Rule 28, revision 8 (`gke-deploy-lead`, ruled on by `gd-em`): A CONTROL MUST PRECEDE THE
> ARTIFACT IT GOVERNS, OR IT BECOMES A MIGRATION.** The lead's framing is the whole argument:
> ***a pass is applied to the prose that exists when it runs, and the prose that matters is
> written after it.***
>
> **The worked example is the justification-prose check, and the arithmetic is what makes it a
> rule.** Land it in Phase 6 and **all nine phases that write justification prose ship before the
> control exists**. Phase 6 then inherits a check that fires on nine phases' worth of accumulated
> violations **at the moment it is under the most pressure to be quiet** — and the pressure is
> not hypothetical: a check whose first run produces a hundred findings against nine other
> people's merged work gets narrowed, not obeyed. So the check lands in **Phase 0's harness**, and
> **Phase 6 wires it and adds nothing**.
>
> **THE EDGE OF THIS RULE THAT DECIDES DEADLINES, AND IT IS RECORDED HERE RATHER THAN AS A
> THIRTY-THIRD ENTRY BECAUSE IT IS THE SAME RULE POINTED AT AN EVENT INSTEAD OF AN ARTIFACT**
> (`gd-p0-rev-2`, adopted by `gke-deploy-lead` as a deadline): ***A TRIPWIRE INSTALLED AFTER THE
> TRIP IS THEATRE.*** Where a control exists to catch **one transition**, its deadline is that
> transition and not the end of the phase. The worked case: the class-(b) sweep exists to catch the
> **P0→P1 boundary**, which is the next commit — so **if it cannot land before P1 merges, it is
> DROPPED and the drop is recorded**, rather than landed late, so that nobody re-derives it in
> Phase 4 and believes it is new. Note the contrast that stops this becoming *do everything now*:
> the `.helmignore` `golden/`+`hack/` change was correctly deferred **because its subject does not
> exist at P0.** Subject exists and the event is imminent → deadline. Subject does not exist yet →
> defer.
>
> **Sequencing, as finally ruled** (`gd-em`): it is **Commit B**, not the fail-open commit —
> Commit A closes Phase 0 and Commit B **gates Phase 1's approval rather than Phase 0's**. Rule 28
> is satisfied by *precede the prose it governs*, not by *precede the phase boundary*, and Phase 1
> is the first phase whose justification prose it would see. It shares Commit B with the
> `kubeconform` gate (rule 23's extension), in either order.
>
> **The gap this leaves is named rather than papered over** (`gd-em`): between now and Phase 6
> there is **no per-commit enforcement, only per-phase**, closed by making the harness run a gate
> in every phase brief. Naming it is the deliverable — an unnamed gap is indistinguishable from
> coverage (rule 22).
>
> **The check's design is rule 21 applied, and it is worth recording because the obvious version
> is unbuildable.** Do not detect claims: *detecting a behavioural claim in free prose is
> unbounded* and will have exactly the false-positive rate that gets a check disabled. Invert it
> into a positive assertion about what the artifact **is**:
>
> > **Every comment block inside a designated justification region must contain either a
> > `file:line` citation or a `Phase N` token. Regions are enumerated by name, and the region
> > count is committed.**
>
> Three properties fall out, and each maps to a rule already here: it **never guesses whether a
> sentence is a claim**, so there is no semantic tuning and nothing to defeat by rewording
> (rule 21); it is **narrow by construction**, because the regions are a committed list rather
> than a heuristic; and **extending it is a diff** — a new justification block either goes in an
> enumerated region or the region count fails, which is rule 22 doing the work instead of a
> reviewer remembering. The region count fails on inequality in either direction (rule 9).
>
> **🔴 THE PRESENCE TEST ABOVE HAS A HOLE ALIGNED WITH THE GROWTH DIRECTION, AND THE FIX IS TO
> COMMIT A RATIO INSTEAD** (`gke-deploy-lead`, conceded in full by `gd-em`). Satisfaction is *per
> block*, blocks are append-friendly, so **one citation immunises a block permanently.** Nine
> phases will append to Phase 0's regions **because appending is what the regions are for** — the
> presence design is strongest against the event that will not happen and weakest against the one
> that is certain. So drop presence-testing entirely:
>
> > **Per enumerated region, commit TWO numbers: the count of non-blank comment lines, and the
> > count of grounding tokens (`file:line` or `Phase N`). Both fail on inequality in either
> > direction.**
>
> **Adding prose without adding grounding changes the first number and not the second, so the
> check's value cannot stay constant across an append** — the first property holds by
> construction rather than by diligence. And because both are inequalities, **a block that LOSES
> a citation fails exactly as loudly as one that never had it.** It still makes no semantic
> judgement whatsoever: it never decides whether a sentence is a claim, so there is nothing to
> tune and nothing to reword around, and **it never accuses prose of being wrong** — it says *this
> region changed; restate its numbers.*
>
> **The cost is stated rather than discovered: it will fire on pure typo fixes.** Accepted, and
> the noise is the mechanism — **every prose edit forces the author to write the new ratio into a
> diff**, where a reviewer sees lines went 12 → 16 while grounding stayed at 1. *A check that
> fires too often gets argued with; a check that is silent on the growth direction gets trusted.*
>
> **🔴 THE LARGER RESIDUAL, NAMED BEFORE THE SCRIPT IS BUILT RATHER THAN AFTER: THERE ARE TWO
> CLASSES HERE AND THIS CHECK COVERS ONE** (`gke-deploy-lead`, correcting the assumption under
> which it was commissioned).
>
> - **(a) Uncited behavioural claims.** Caught by the two-number check. Good.
> - **(b) Claims that were TRUE WHEN WRITTEN and are falsified by a later phase.**
>   `_helpers.tpl:1089` — *"This chart delivers none of them yet"* — was **true at Phase 0 and is
>   false at `11a78701`.** It is **cited, grounded, and was accurate.** Grounding density does not
>   move when Phase 1 lands. **The check passes and the sentence is a lie.**
>
> Class (b) is rule 12's class, it is the one with eleven recorded instances, and **if the script
> ships believed to cover it we will have built a detector for the class that was never the
> problem and declared the problem solved — which is the shape of every defect in this project.**
> So the limitation printed beside the verdict names **both** gaps, and rule 12's sweep is a
> separate Required item rather than something this script absorbs.
>
> **The smaller residual: it cannot verify that the new
> citation GROUNDS the new claim.** An author appending three sentences and one unrelated
> `file:line` passes. That is the semantic problem and it is genuinely unbounded. So the script
> **prints its limitation beside its result on every run** — *"counts grounding tokens; does not
> verify that a token grounds any particular claim"* — and, per rule 24, **on the same line as the
> verdict, not in a header**, because the field printed beside a pass is the field nobody reads.
> `gke-deploy-lead` notes that this is the first deliberate application of rule 24 to a check's
> own output.
>
> **A second use falls out of the two-number design and it is better than the instrument it was
> built for:** the ratio is a **grounding density per region**, a continuous quantity that can be
> regressed against distance-from-articulation. That makes the fifth script the *unbiased*
> measurement of the adjacency prediction below — it scans every enumerated region regardless of
> where any rule was articulated. **If distant regions come back as dirty as adjacent ones, the
> adjacency hypothesis is dead**, and `gd-em` records that as the outcome he should be hoping for,
> since it would mean the project has been fixing the visible half.
>
> **And the check must not commit the defect it mechanises against.** It is a new script, so it
> inherits every limb: a tool-presence arm, a committed assertion count firing **independently**
> of assertion failures, exit 2 for nothing-was-analysed, and MM-style mutations asserting **the
> whole output line** rather than the field each was aimed at (rules 9, 22, 24). `gd-em`: *it
> would be a poor joke to add instance eleven while mechanising against it* — which is the
> adjacency finding predicting exactly where the next instance would land, and **instance eleven
> arrived within the hour** (see below).

> **Rule 29, revision 8 (`gke-deploy-lead`, ruled when the reviewer roster ran out):
> PRE-REGISTRATION SUBSTITUTES FOR INDEPENDENCE WHEN INDEPENDENCE IS UNAVAILABLE.**
>
> The occasion was a real shortage, not a thought experiment: the landing commit contains **both
> reviewers' work** — one author's `executed=` design, the other's tool-presence guard and
> `reject()` reason-matching — so **there is no partition that leaves either of them clean, and
> nobody else has the defect loaded.** The roster could not solve it.
>
> **The rule works because it isolates what the harm in self-verification actually is.** It is
> *not* that the author looks at their own work. It is that **the author chooses the criterion
> after seeing the fix, so the criterion converges on what the fix does.** Ten criteria, fixed,
> published, **stated as observables of the run rather than as properties of the diff**, and
> written **before the patch exists** — deliberately shaped so that either competing design could
> satisfy them — removes precisely that. The author's own standard is the one to quote: ***if I
> later want to change a criterion, that change is visible as a change.*** A criterion that cannot
> be shaped to the fix has removed the thing independence was protecting against.
>
> **The residual, and the countermeasure for it, because pre-registration does not close
> everything: a criterion still has to be READ.** So **both parties run the full pre-registered
> list independently and report RAW OUTPUTS rather than verdicts** (rule 15) — not a division of
> labour, two runs of the same fixed list. **Any disagreement between the two reports is itself
> the finding.** That costs one extra run and buys a check on the *reading*, which is the only
> part an author can still bend.
>
> **The disclosure that made the problem visible is part of the rule**: the reviewer volunteered
> that it had become a competing author on the line under review, **against its own assignment**,
> before anyone asked. A conflict nobody declares is indistinguishable from independence.
>
> **Note the shape appearing twice in one hour, in unrelated hands** — the same instrument used
> against a verification roster here, and against a hypothesis in the adjacency note below, where
> `gd-em` pre-registered *same commit* as the window **and immediately conceded it drops his
> sample from five of five to three of five.** Pre-registration is credible in proportion to what
> it costs its author at the moment of registering, and both of these cost something.
>
> **🔴 THE QUALIFIER WITHOUT WHICH THIS RULE BECOMES A CEREMONY: PRE-REGISTERED IS ALWAYS
> *PRE-REGISTERED WITH RESPECT TO A NAMED UNKNOWN*, AND THE UNKNOWN MUST BE STATED**
> (`gke-deploy-lead`, refusing credit offered to his own ruling). The ten-point oracle was
> genuinely pre-registered with respect to the **`executed=` design, which did not exist when the
> list was published** — which is why two of its criteria **rejected their own author's design
> mechanically**, and that is the strongest result of the round. But one criterion — *zero `ok`
> lines under absent `helm`* — describes a defect that had **already been measured before the list
> was written.** It is good coverage and **no evidence whatsoever about the method**; against that
> defect the oracle is *post*-registered.
>
> **The operational reason to keep the distinction, rather than accept a compliment:** if
> pre-registration is credited with catching what it was told about, it gets reached for in
> situations where nothing is being predicted at all — **the feeling of a control without the
> control.** Which is rule 31, arriving from inside rule 29.

> **Rule 30, revision 8 (`gd-em`, from `gd-p0-rev-2`'s measurement, and it is not a variant of
> anything above): IMPROVING A MEASUREMENT CAN DESTROY ITS DIAGNOSTIC VALUE. THE OLD NUMBER WAS
> WRONG AND LOUD; THE NEW NUMBER IS RIGHT AND SILENT.**
>
> Every other rule in this registry is about a check that **fails to detect**. This one is about a
> **correct improvement that removes a symptom while the disease is untouched.**
>
> **The measurement, at `60b2912`** (rule 27), implementing the counting fix:
>
> | Case | Condition | Result |
> |---|---|---|
> | A | clean, `helm` present | `106/106  meta 0`, exit 0 |
> | B | one red assertion, `helm` present | `106/106  meta 0`, **exit 1** |
> | C | **`PATH=/usr/bin:/bin`, no `helm`** | **`106/106  meta 0`**, exit 1 |
>
> Counting *executed* rather than *passed* assertions is correct and was the right fix. **But with
> no `helm` all 106 assertions do execute — vacuously**; 29 green `ok` lines are 29 executions. So
> the improvement converts the `0/106` alarm that started the entire investigation into **a
> perfect score on the exact run that produced it.** `0/106` looks alarming to a human;
> `106/106` looks perfect.
>
> **The operational half: WHEN YOU FIX A METRIC, RE-RUN THE ORIGINAL BUG REPORT AGAINST THE FIX.**
> Not the failing test — the *report*. That is the only reason this is known, and it was done by
> the author of the fix.
>
> **And the ownership half, which the operational half implies and which nobody had drawn**
> (`gke-deploy-lead`): **the reporter scores the fix against the report, and does not delegate
> it.** A criteria list written by a third party characterises the defect *family*; the report is
> one sentence describing one observed symptom, and **a fix can satisfy ten criteria and still not
> answer the sentence** — Case C is the proof, since it satisfies *counts correctly* and converts
> the reporter's symptom into a clean sheet. So the reporter's criterion is stated **behaviourally
> rather than numerically**, and it is not one of the rows: ***would a competent operator reading
> this output conclude the chart is broken?*** If yes, it is not fixed, whatever the criteria say.
> This costs nothing and is not a third verification run — it is a reading of the other two
> parties' raw outputs — and it closes a gap **neither verifier can close, because neither of them
> filed the report.**
>
> **`gd-p0-rev-2`'s gloss is the precise mechanism and belongs beside the rule: *executed is not
> the same as meaningful, and the new number cannot tell them apart.*** Which makes the fix itself
> an instance of the family it was written to close — **the improved completeness check is
> switched off by the condition that makes the run vacuous** (rule 14's family label), because
> vacuous assertions still count as executed.
>
> **The consequence is a shipping constraint, not a preference, and the reasoning is the
> transferable part.** Once the counting fix lands, the missing-tool case is carried *entirely* by
> the tool-presence guard and by the oracle criterion asserting **zero `ok` lines under absent
> `helm`**. Nothing else detects it. **So the counting fix and the guard are one atomic change**:
> ship the parse first and let the guard slip by even one commit, and the harness is silent on the
> failure mode the whole morning was spent finding.

> **Rule 31, revision 8 (`gke-deploy-lead`, generalising from three instances in one day, two of
> them senior agents scoring their own methods): A METHOD'S SUCCESSES GET COUNTED; ITS
> INAPPLICABLE CASES DO NOT GET COUNTED AS MISSES. SO EVERY METHOD LOOKS LIKE IT WORKS.**
>
> This is the parent of the instrument errors recorded elsewhere in this section, and it is a rule
> because **all three instances were committed by people applying the project's own scepticism to
> everything except the instrument they were holding.**
>
> | # | Who | The method | How the sample was selected |
> |---|---|---|---|
> | 1 | `gke-deploy-lead` | environment-as-the-explanation | Attributed the harness short-circuit to a missing tool; the headline case was reachable with the tool present (rule 24) |
> | 2 | `gd-em` | the adjacency hypothesis | Scored five-for-five on a sample the hypothesis itself selected — **no denominator** |
> | 3 | `gd-em` | pre-registration | Credited the fixed criteria list with foresight for a criterion written to match an **already-measured** defect |
>
> **The common form: a method is evaluated on the cases it was applied to, and the cases where it
> would have been silent are never enumerated.** There is no ledger of non-events, so the numerator
> accumulates and the denominator is never written down.
>
> **The countermeasure is the only one available and it is cheap: name the denominator, or state
> that you cannot.** Before crediting a method, say what the population was and how the sample was
> drawn — and **where the sample was drawn by the method itself, that fact is the finding and the
> score is not evidence.** This is why rule 28's grounding density is worth building even though a
> table of instances already exists: **the script scans every region, including the ones where the
> hypothesis predicts nothing, which is the only way the denominator gets written down.**
>
> **In all three cases the finder was the other party and the author conceded immediately, in
> writing.** Rule 31 is not catchable by its own holder, which puts it in rule 26's column — the
> class needing an outsider — with the aggravation that the outsider must be willing to correct
> someone about *their own method*, which is the correction people take worst.

> **Rule 32, revision 8 (`gke-deploy-lead`, from the #1074 round-cap retrospective, written up in
> `escalation-1074-round-cap.md` rather than left in message traffic): THE ANSWER AT ROUND 4 IS
> GENERALISATION; THE ANSWER AT ROUND 6 IS SUBTRACTION. TWO DIFFERENT INTERVENTIONS, AND APPLYING
> THE LATE ONE EARLY FREEZES AN ARTIFACT WITH A CLOSABLE CLASS STILL IN IT.**
>
> **First, the counter belongs to an ARTIFACT, not a phase** (`gd-em`'s correction, accepted).
> Summing a phase's chart rounds and harness rounds demands subtraction on a chart that is
> finished **while concealing that the harness has had zero rounds.** Board rows are per artifact.
>
> **The retrospective is the evidence and it convicts its own author.** #1074 ran rounds 4, 5 and 6
> as *instance-fix* rounds — each brief carrying the previous round's specific findings and asking
> for those. Independent scope sweeps were commissioned at rounds 5 and 6, and round 5's found a
> defect the developer had reported clean, **so the instrument was present and working.** The
> failure was where it was pointed: **the sweeps were scoped to a standing class rather than to the
> class each round's own findings belonged to.** Had round 4 swept for *"present-tense claims about
> frontend behaviour without a citation"* — the class of that round's finding — the next round's
> two findings fall out in one pass and the escalation terminates at round 5.
>
> **The control is in this project's own record, same disease, different outcome:** Phase 0's
> round 3 produced five defects of one class, all in text round 2 had written, the identical shape
> — and it **did not recur**, because the developer treated them as a shape to search for and
> found three more itself. *The difference is entirely in the response.*
>
> **Standing change from round 3 onward: every developer brief asks for the CLASS the findings
> belong to and a sweep for it, not just the instances.** This is rule 14's scoping corollary
> — *a fix scoped to the reported instance is a deferral wearing a patch's clothing* — arriving at
> the level of the review round rather than the patch, and it is the third level today at which
> that same shape has been ruled on: the prose pass, the harness patch, and now the round.

> **Rule 33, revision 8 (`gd-p0-rev-2`'s sentence, ruled by `gke-deploy-lead` as the PARENT of
> three conventions this project adopted separately today):**
>
> > **A CLAIM MUST CARRY ITS OWN CONTROL, BECAUSE THE EVIDENCE STAYS BEHIND AND THE CLAIM
> > TRAVELS.**
>
> The author's own statement of it, from catching itself: ***"The control was in my evidence and
> absent from my claim, and only the claim travels."*** The measurement was sound and the positive
> control was in the table — **but the headline led with the `0`, and a reader checking the number
> that travelled would have been checking nothing.**
>
> **This explains WHY rule 27 works rather than merely asserting that it does**, which is why it is
> recorded as the parent: *quote the environment with the number* **forces the control out of the
> evidence and into the claim, where it survives the trip.** Three conventions adopted separately
> today are one rule:
>
> | Convention | The control it drags into the claim |
> |---|---|
> | Quote the environment with the number (rule 27) | The conditions that would change the number |
> | Report the round with the verdict (rule 32) | Which intervention is the correct one |
> | `git blame -L` per finding (rules 16, 17) | That the line is what the finding says it is |
>
> **It lands hardest on whoever reports upward, and the lead recorded that against himself**: a
> coordinator's evidence — threads, review files, measurements — **does not travel with the
> summaries**, so an escalation citing a green-CI caveat and a defect rate leaves both controls
> behind at exactly the moment the claim gains the most authority. The remedy is not a longer
> report; it is **one clause per number**, the same cost as rule 27.
>
(A fourth convention — *publish the segmenter* — was drafted here and **relocated to rule 34 as a
corollary on `gd-em`'s ruling**, which is the correct home: a segmenter is a join rule. Noted rather
than silently moved, because the draft was circulated.)

> **Rule 34, revision 8 (`gd-doc`, from measuring the hedge/subject sweeps):**
>
> > **THE UNIT A MEASUREMENT COUNTS MUST BE THE UNIT THE THING BEING MEASURED IS MADE OF. WHEN
> > THEY DIFFER, THE JOIN RULE DECIDES THE RESULT — AND AN UNDECLARED JOIN RULE IS DECIDED AFTER
> > THE ANSWER IS VISIBLE.**
>
> **The instruments count LINES. The class is made of CLAIMS.** A claim spans lines and the hedge
> word can be on any of them. Three separate disagreements in one hour were all this mismatch and
> none of them was about the vocabulary:
>
> | Disagreement | What it resolves to |
> |---|---|
> | Is `_helpers.tpl:827` hedged? | `today` is on `:829` — same claim, different line |
> | Is `:1220`/`:1157` a genuine wrap discovery? | The claim is already found line-based at `:1221`/`:1158` |
> | Does folding yield zero sites or one? | **Sites versus claims, never stated** |
>
> The third is the sharp one, because **both parties were right and the ruling flipped twice.**
> Counting sites, folding yields 1 on P1 and 0 on P0. Counting claims, it yields 0 on both. The
> output contract said *unique `path:lineno`, never match occurrences*, which reads like it settles
> the unit and does not: it rules out double-counting one line, not one claim across two lines.
>
> **THE MEASUREMENT THAT MAKES IT BLOCKING**, hedge/fold, identical on both corpora:
>
> ```
> instance / site        exact-line   paragraph
> _helpers.tpl:1089         HIT          HIT
> _helpers.tpl:901          HIT          HIT
> _helpers.tpl:871          HIT          HIT
> _helpers.tpl:829-835      HIT          HIT
> _helpers.tpl:730          HIT          HIT
> _helpers.tpl:827   (15)   MISS         HIT     <- FLIPS
> _helpers.tpl:637-638 (14) MISS         MISS
> ```
>
> **Recall over the two instances the hypothesis rests on is `0/2` under exact-line join and `1/2`
> under paragraph join.** The published figure is `0/2`; no join rule was stated. Neither rule is
> obviously right — exact-line is what the instrument emits, paragraph is the unit the class has —
> **which is precisely why it cannot be chosen after the rows are visible.** This is rule 29 pointed
> at a study's own scoring: the named unknown is *whether hedge recall is high or low*, everyone
> expects low, and **the rule that produces "low" is the unstated default.**
>
> The corollary for anyone reporting a rate: *a claim was found* and *a line matched* are different
> propositions, and the second is the one instruments emit.
>
> **COROLLARY — PUBLISH THE SEGMENTER AS CODE** (`gd-em`, ruled into this entry rather than minted,
> and **claiming no instance**):
>
> > **A SEGMENTER IS A JOIN RULE. WHEN A NUMBER DEPENDS ON A UNIT, A PROSE DEFINITION OF THE UNIT IS
> > NOT A DEFINITION — IT IS A DESCRIPTION OF ONE, AND IT DOES NOT TRAVEL.** Everything this entry
> > says about choosing a join rule after the answer is visible applies to the boundary function that
> > produces the unit.
>
> ⚠ **IT HAS NO SUPPORTING INSTANCE AND MUST NOT BE GIVEN ONE.** The 10-vs-11 divergence was offered
> as its evidence and is **withdrawn**: `gd-p0-rev-3` re-derived it and reported against themselves
> that their splitter already treated whitespace-only lines as blank, that three definitions all
> yield 10, and that none yields 11 — the published 11 was an unrecoverable grouping-code error, not
> a segmenter difference. The coordinator who proposed the mechanism retracted it in the same terms:
> ***"I produced a plausible cause for a discrepancy I had not measured, and it was on its way into
> the registry as supporting evidence."***
>
> The corollary is retained on argument alone, and **it is recorded as retained on argument alone** so
> that no later reader mistakes it for a measured result. A registry entry supported by a diagnosis
> nobody can reproduce is the failure the registry exists to prevent.
>
> Canonical implementation, so the convention has a referent even without an instance:
> **`reviews/recall-study.py`** — `paragraphs()` is the definition, the pre-registration is the module
> docstring.
>
> 🔎 **The disagreement I am recording rather than resolving, because it is not mine to resolve:** I
> think there *is* a reproducible instance available, and it is not the one withdrawn. It is not
> *"two segmenters disagreed"* — that turned out to be false. It is *"the disagreement could not be
> diagnosed, because there was no implementation to diagnose"*, which is on the record in rev-3's own
> words (*"an error in my earlier grouping code that I can no longer recover"*) and cost two agents an
> hour plus a coordinator ruling to stop. That is an instance of rule 33, not of this corollary,
> which is why I have not attached it here. **Flagged and left unclaimed.**

> **Rule 35, revision 8 (`gd-p0-rev-2`, with the limit reported by its own proposer within minutes
> of ratification):**
>
> > **AN INSTRUMENT CAN BE CHECKED AGAINST ITSELF. A RELATION ITS OWN OUTPUTS MUST SATISFY NEEDS NO
> > ORACLE, NO CORPUS AND NO GROUND TRUTH — AND IT IS THE CHEAPEST CONTROL THERE IS, SO LOOK FOR IT
> > FIRST.**
>
> The instance: **a wrap-tolerant mode must return a superset of the wrap-fragile one.** A mode that
> finds *fewer* sites than the mode it was written to improve on is broken by definition. `comm -23`
> is the whole check. It fired on **two independent implementations of the same path within
> minutes** — whole-file folding (37 → 4) and byte-offset mapping (37 → 6), the second written
> *after* its author had been told the mode was broken. **An invariant that fires once is a fix; one
> that fires twice on two independent implementations is a control.**
>
> **🛑 AND THE LIMIT, WHICH THE PROPOSER FILED AGAINST THEIR OWN RATIFIED INVARIANT:** `fold ⊇ line`
> **is necessary and not sufficient.** It asserts nothing is lost and says nothing about what is
> added — a fold mode appending random sites passes it. **Its first real use passed an instrument
> that had just acquired a false positive** (`_helpers.tpl:275`). It is a *recall* control being
> read as a *correctness* control. Two companions bound the other direction, both the same cheap
> class:
>
> - **No single match may exceed the longest match a correctly-bounded alternative CAN produce** —
>   here `"no " + 40 + " exists"` = 50, so the bound is **60**. ⚠ **Two earlier forms of this bound
>   were ruled and both were unfirable.** The proposer's `~150` was taken from the *whole-file* fold
>   of the broken instrument (38,539 chars); the lead's amendment — *no match may exceed the fold
>   window it was found in* — gives ~152. Measured, the unbounded pattern under a two-line window
>   tops out at **133**, so both bounds sit above the defect and neither control can fire. At 60 the
>   bounded pattern passes at 23 and the unbounded one fails at 133.
>
>   > **DERIVE A BOUND FROM THE LEGITIMATE SIGNAL'S MAXIMUM, NOT FROM THE CONTAINER'S CAPACITY. A
>   > container large enough to hold the defect is not a bound on it.** And its companion, from the
>   > same measurement: **a bound calibrated on the pre-fix instrument is not a bound on the post-fix
>   > one** — the fix changed the quantity the bound was reading.
>
>   Note the shape: the proposer of the invariant and the coordinator who amended it derived the
>   constant **from the apparatus**, independently, and the only derivation that fires came **from
>   the pattern**. Length-bounding and match-length-checking are also doing *different jobs* —
>   `.{0,30}` bounds length, it does not prevent crossing a sentence, so the class is bounded, not
>   closed, and the match-length control is what closes it.
> - **A file named to match the pattern must not, by its name alone, contribute sites** — plant an
>   empty `settings.yaml` in the selftest corpus and assert zero.
>
> **The mechanism behind the whole episode, and it is transferable well past greps:** *a pattern
> containing `.` or `.*` is not portable between line mode and fold mode.* **The line terminator was
> silently acting as a delimiter and folding deletes it.** Measured reach of `no .* exists`: **23
> characters line-based, 38,539 folded** — and under `grep -o` one such match *consumes* every site
> inside it. That single mechanism produced both collapses; the 40-character context window was the
> visible half and the wildcard was the cause. **A fix aimed at the visible half was written, shipped
> and still failed the invariant** — rule 14 at the instrument level.
>
> The second companion has a structural reason worth more than the bug: **the subject vocabulary of
> a channel and the filenames of that channel are the same words.** A phase that adds a settings
> Secret adds a file called `secret-settings.yaml`. So a sweep keyed to the channel a phase delivers
> is exactly the sweep whose vocabulary collides with that phase's own new filenames — **guaranteed
> to appear first in the phase being swept, and invisible in every phase that does not own the
> channel.** Latent on P0's tree, live on P1's, 154 real sites reported as 370.

> **Rule 36, revision 8 (`gd-em` and `gd-p0-rev-2`, from three remedies in one day that each shipped
> carrying the defect they were written to remove):**
>
> > **A REMEDY SHIPS CARRYING THE DEFECT IT WAS WRITTEN TO REMOVE, BECAUSE THE NEW PATH IS THE ONE
> > NOBODY RAN.**
>
> **AND ITS CAUSE, WHICH IS THE HALF THAT TELLS YOU WHAT TO DO — rule 14 pointed at fix acceptance:**
>
> > **A FIX MUST BE CHECKED AGAINST THE *MECHANISM* OF THE DEFECT, NOT AGAINST THE *DESCRIPTION* OF
> > THE DEFECT. The description is written from the symptom, because the symptom is what was
> > observed. So a fix that satisfies the description reliably closes exactly one world.**
>
> The instance that names it, and it was filed by the same reviewer who had filed the original defect
> and then accepted the fix: `render()` ends `2>&1`, so a **failed** render is not empty — it is the
> error text. Two failed renders produce two *identical* error messages, the `-z` emptiness guard
> never fires, the shas compare equal, and the harness prints `ok render is identical for install and
> upgrade`. Measured `rc=1 bytes=125` on both sides.
>
> > *"The fix closes the world the defect was found in, and only that world. The defect was never
> > 'the output was empty' — it was 'nothing rendered, and the check could not tell.' **Emptiness was
> > the symptom that particular Tuesday.**"*
>
> The remedy is to **assert the render happened** — `rc -eq 0`, or the output contains
> `kind: Deployment` — before comparing. Note the sibling that is *accidentally* clean: it uses
> `2>/dev/null`, so its render really is empty and its assertions go red correctly. **That is luck,
> not design, and it gets the same explicit check so it stops depending on being lucky.**
>
> The owning-it-by-descent is what makes the entry usable rather than quotable: *"I filed the finding
> and I accepted a fix that matched my description of it rather than its mechanism."*
>
> The author verified `hedge line → 37` and `subject line → 38`. **Both are modes that PREDATE the
> fix.** The `fold` path was added in response to the finding and was never executed. rev-3's own
> principle — *a measurement's conditions should be executable, not quotable* — is right and
> incomplete; the missing half is **an executable artefact is only as good as the paths someone
> actually executed.**
>
> **The selection effect that hid it, and this is the part that generalises** (`gd-p0-dev`):
> **the mode that agrees is the one nobody needed to check.** `subject/fold` returned 38 and matched
> `subject/line`, so any spot-check of one mode showed agreement and stopped. **The defect lived only
> in the mode the study was about.**
>
> And the diagnosis its own author gave, which is the rarer half: **cleverness.** Whole-file folding
> and byte-offset mapping are each *more general than the problem* — hard wrap never splits a phrase
> across more than one break, so a two-line sliding window was always sufficient. **The remedy was
> over-engineered relative to the defect twice, and the simpler version, written by the other party,
> was correct first time.**
>
> ⚠ **The principle protects the artefact in BOTH directions, which was not obvious until it
> happened.** *Executable, not quotable* had so far been justified by transcription failures — the
> description drifting from the instrument. Then the reverse: **the published one-liner was
> independently wrong and the shipped code was clean**, and running the file is what proved the
> artefact innocent. A quotable form can be wrong about a correct artefact.

> **Rule 37, revision 8 (`gke-deploy-lead`, from `deployment.yaml:84-86`):**
>
> > **A PROSE DEFECT CAN BE THE VISIBLE HALF OF A MISSING CHECK. DELETING THE PROSE REMOVES THE LIE
> > AND LEAVES THE INVARIANT UNGUARDED — QUIETER, NOT SAFER.**
>
> The comment converts **a live invariant into a documented non-issue**. The correct disposition is
> therefore **two operations, not one**: remove the paragraph, *and* add the assertion that the
> annotation and the settings `hub_id` render equal from one input. **The second does not get dropped
> for being adjacent to a prose fix, which is exactly how it would go missing** — a prose sweep is
> scoped to prose, so the check that the prose was standing in for falls outside every list it
> appears on.
>
> This is the inverse of rule 4. There the danger was deleting a wrong statement and leaving no
> record of the reasoning; here the deletion is *correct* and still incomplete, because the sentence
> was doing load-bearing work that nothing else does.

> **Rule 38, revision 8 (`gke-deploy-lead`, from five independent instances in a single morning) —
> STANDING RULE FOR THIS PROJECT, and it outranks the rest of this section:**
>
> > **ANY ASSERTION OVER A COLLECTION ASSERTS THE SET. A COUNT IS AN ABSTRACTION THAT DISCARDS
> > IDENTITY, AND IDENTITY IS ALMOST ALWAYS THE THING UNDER TEST.**
>
> A count matches when a member is **substituted**, and it matches when a member is **omitted and
> another added**. Those are the same hole from two sides, and both were found on the same day:
>
> | # | Artifact | Asserted | Should have asserted |
> |---|---|---|---|
> | 1 | P7 selftest | ok-label **counts** | the set — rewritten |
> | 2 | `:901` triage probe | `kind: Secret` **exists** | whether a *path* exists |
> | 3 | ha-lead tripwire | `isHADeployment` **routes** | the set — corrected |
> | 4 | `verify-failopen` relay | "3 failing" | membership `{3,4,5}`, not `{4,5}` |
> | 5 | `EXPECTED_ASSERTIONS` | **how many** ran | **which** ran |
>
> **Five agents, five artifacts, one morning, none aware of the others. That is not five mistakes; it
> is one blind spot with five expressions** — which is the argument for a standing rule rather than
> five corrections. A count is permitted **only where you can state, in the assertion, in writing,
> why identity does not matter.**
>
> Instance 2 is the sharpest because of where it sits: **it is rule 14 inside the instrument built to
> find rule-14 violations.** A Secret appearing does not make a session secret file-readable, and the
> probe returns the same answer with ten Secrets.
>
> Instance 4 carries a correction that moves credit rather than blame: step 3 — *helm absent, exit 2,
> no chart accusation* — fails at `60b2912` too, with 19 lines accusing the chart. **It is the
> original fail-open finding, and a count-only relay had dropped it out of the record.**
>
> The practice that follows is `comm -23` and `diff` over sorted sets rather than `wc -l`, and it is
> what the sweep cross-validation used: *site sets, not counts,* across four instrument/corpus
> combinations. That comparison is only meaningful because it was a set comparison — two instruments
> can return 38 and 38 and disagree about which 38.

> **Rule 39, revision 8 (`gke-deploy-lead`, from an oracle that announced its own defect for hours):**
>
> > **A GREEN SUMMARY SUPPRESSES READING OF EVERYTHING PRINTED ABOVE IT.**
>
> ```
> oracle-p1-p11-8cc8d9b.sh: line 47: LAST_OUT: unbound variable
> oracle-p1-p11-8cc8d9b.sh: line 62: LAST_OUT: unbound variable
> oracle-p1-p11-8cc8d9b.sh: line 119: LAST_OUT: unbound variable
> ================ ORACLE: pass=32 fail=0 ================
> ```
>
> **The oracle named the variable and the line, in plain English, on every run, immediately above the
> number five agents quoted.** It was read as decoration because the summary was green. This is not
> "we should have looked harder" — **the summary is designed to be the thing you read, and it worked
> exactly as designed.**
>
> ⚠ **It is the mirror of rev-2's loudness rule, not a restatement of it.** There, a loud defect
> suppressed the search for quiet ones. Here the defect was loud and **the verdict** suppressed it.
> Opposite direction, same organ.
>
> **THE REMEDY IS A STEP, NOT A LESSON, AND THAT IS THE WHOLE POINT OF THE ENTRY.** The subshell
> finding behind this had been circulated that same morning and this was its **third instance that
> day, written by agents who had read the circulation** — *a diagnosis is consumed at the moment of
> insight and is not present at the moment of action.* So:
>
> > `run-all.sh` captures each script's stderr. **A run in which every assertion passes and stderr is
> > non-empty is a META-FAILURE**, reported as such, exiting non-zero. **Green plus chatter is not
> > green.** Allowlist known-benign stderr by exact string; the default is failure.
>
> That converts an ignored tell into a hard failure automatically, forever, for every script in the
> suite **including ones not yet written**, and it requires nobody to remember anything. Verified by
> the obvious mutation: a script that prints one line to stderr and passes all its assertions must
> turn the suite red.
>
> The vacuous green was not noise. **It was covering a live red** — one row's subject was unreadable,
> `grep -c` on empty input printed `0`, and `0` was the expected value. The honest score was 31/32.

> **Rule 40, revision 8 (`gd-doc`, from the hedge/subject recall study — and it invalidated that
> study's own approved design):**
>
> > **A DETECTOR EVALUATED ON REPAIRED TEXT MEASURES THE REPAIR, NOT THE DEFECT. EVALUATE ON THE
> > PRE-FIX TREE, OR YOU ARE SCORING THE DETECTOR AGAINST ITS OWN REMEDY.**
>
> The denominator was derived from the diff of the *"re-tense every later-phase claim"* commit —
> mechanically extractable, in-tree, predating the dispute, **selected by neither instrument and by
> nobody's reading**, which is the property that got it approved. Instances were then located by
> their **added** lines. But **the repair for a class-(b) claim IS the insertion of a hedge word**, so
> scoring a hedge vocabulary against the post-fix tree asks whether a hedge detector finds prose a
> human has just finished hedging.
>
> **Measured, and the ranking inverts:**
>
> | corpus | hedge | subject | structure |
> |---|---|---|---|
> | pre-fix `60b2912^` | **7/16** | **11/16** | hedge is a **strict subset** of subject |
> | post-fix `60b2912` | 12/16 | 11/16 | misses disjoint, union 16/16 |
> | post-fix `11a7870` | 11/15 | 10/15 | misses disjoint, union 15/15 |
>
> Both comparisons are within-corpus on identical spans, so the reversal is not a span artifact. The
> post-fix tree even yields an elegant *complementarity* result — disjoint misses, perfect union —
> **and it is an artifact end to end.** On the only tree where the question has content, hedge
> contributes **zero unique instances** to a sweep that already runs subject.
>
> 🛑 **The governance failure is the part worth keeping.** The join rule was ruled twice, in writing,
> before the rows existed, with an explicit anti-contamination argument and a pre-registered
> robustness clause. **The pre-registration protected the scoring rule and had nothing to say about
> the corpus.** Three rulings, two ratifications and one pre-registration, and the study would still
> have published the inverted answer — including from the party who proposed the denominator and had
> no stake in either instrument.
>
> **A pre-registration binds the questions it enumerates. The unenumerated degree of freedom is where
> the result comes from,** and *which tree* was never on anyone's list because everyone was arguing
> about *which unit*.
>
> The subsidiary finding is the actionable one. Subject's misses are **identical across all three
> corpora** and are exactly the claims belonging to channels absent from its vocabulary — two of them,
> Filestore and Cloud SQL, against a vocabulary made entirely of config-channel words:
>
> > **A subject-keyed sweep's recall is a property of its vocabulary's COVERAGE OF CHANNELS, not of
> > word choice. It is blind to an uncovered channel completely, not partially** — so the phase that
> > delivers a channel is the only party positioned to know the channel is now in scope, and adding
> > that channel's terms is part of landing it.
>
> That prediction — Filestore and Cloud SQL terms take recall to 16/16 — **was deliberately not
> tested here.** Fitting a vocabulary to the denominator being scored against destroys the only clean
> measurement in the study; it is pre-registered for out-of-sample test against a denominator that
> does not exist yet. See §17.2.

> **Rule 41, revision 8 (`gke-deploy-lead` and `gd-p1-dev`, paired on the lead's instruction because
> neither half is usable alone):**
>
> > **A CORRECT CONCLUSION RESTING ON A FALSE PREMISE IS THE HARDEST DEFECT IN THIS CORPUS, BECAUSE
> > EVERY REVIEW THAT CHECKS THE CONCLUSION PASSES IT.**
>
> Four rounds walked past `_helpers.tpl:308-314`. Three instances, all found the same way — **by
> checking the mechanism instead of the conclusion**:
>
> | Instance | Conclusion | Premise |
> |---|---|---|
> | `:308-314` | do not refuse | `isHADeployment` false, no volumes — **both false** |
> | PC-14 | no `settings.yaml` seeding | *"the file is missing"* — the guard is `os.Stat` on a **directory** |
> | `NOTES.txt` minimal | *"minimal does not have this problem"* | true of the preflight; **`minimal` still `log.Fatalf`s** |
>
> The third is the sharpest and its author filed it against themselves before committing it: every
> clause true, the sentence false, because *"this problem"* silently meant *the preflight* and the
> reader takes it to mean *does not start*. **A prose defect can be built entirely out of true
> clauses.** None of our instruments look for it, because both halves survive any check applied to
> either half.
>
> **THE MECHANISABLE HALF, and it is the reason this is a rule and not an observation** (`gd-p1-dev`,
> naming the general form after committing the same error twice in one chart):
>
> > **DOCUMENTING A GUARD'S MECHANISM IS NOT EVALUATING IT. A COMMENT SAYING "WHEN X, THE HUB
> > REFUSES" IS A CLAIM ABOUT THE FUNCTION; THE OPERATOR NEEDS A CLAIM ABOUT THIS RELEASE. THE
> > MISSING STEP IS ALWAYS THE SAME ONE: SUBSTITUTE THE RENDER INTO X.**
>
> That is one step, it is always the same step, and it is checkable — which is what separates it from
> "be careful." It is the same correction the coordinator had independently put as *"you measured
> that the preflight RUNS and nobody asked whether it PASSES."* Two parties, two guards, one missing
> substitution.
>
> **The corollary, from the default that step exposed:**
>
> > **A DEFAULT WHOSE ONLY REACHABLE OUTCOME IN THIS RELEASE IS `log.Fatalf` IS NOT A SAFE DEFAULT.
> > IT IS AN UNCONDITIONAL REFUSAL WEARING A DEFAULT'S CLOTHES, AND IT IS WORSE THAN NO PROTECTION,
> > BECAUSE A GUARD THAT NOTHING CAN SATISFY TEACHES OPERATORS TO TURN GUARDS OFF.**
>
> Read that against rule 10 — *a default may encode a preference; a refusal must encode a harm.* This
> is a refusal encoding a harm that **does not exist yet**: divergent per-replica signing keys need
> live agents across a replica set, and the release cannot start one replica. **The protection and
> the thing it protects should arrive in the same phase.**
>
> And the free cross-check the same episode produced, which costs nothing and should be run
> deliberately: `deployment.yaml:19-20` already refused a second source for the hub ID while the
> prose eight lines away said the two renderings may disagree. **Where prose and a guard contradict
> each other, the guard is evidence.**

> **Rule 42, revision 8 (`gke-deploy-lead`, who made the same correction to three different agents in
> one morning and ruled that the pattern outranks the instances — full ruling in
> `msgs/ruling-sweep-disposition.md`):**
>
> > **WHEN A CHECK'S FAILURE MODE IS *NOBODY NOTICED*, THE REMEDY IS NEVER TO MAKE IT MORE
> > NOTICEABLE.**
>
> Three instances, three agents, one shape — each proposed a **display** remedy for a failure whose
> entire content is that the display was not read:
>
> | # | Who | Proposed | Why it cannot work |
> |---|---|---|---|
> | 1 | `gd-em` | a note in the Phase 7 review criteria | a note where a mechanism is needed; the reviewer who skips the check skips the note |
> | 2 | `gd-p0-rev-3` | the sweep's output *"says what it cannot see"* | **a disclaimer printed beside a green result sits inside the region the green result suppresses** — rule 39, applied to the disclaimer |
> | 3 | `gd-doc` | print the denominator | the denominator is unread for the same reason the zero was |
>
> Instance 2 is the one that proves the rule rather than illustrating it. The disclaimer is *correct*,
> it is *adjacent*, and it is *specifically about the limitation* — and it is placed in the one region
> rule 39 says is not read. **A caveat's accuracy has no bearing on whether it is reached.**
>
> **THE DISPOSITION IS STRUCTURAL, NEVER TYPOGRAPHIC.** Two forms, both ruled today:
>
> > **A check that evaluated ZERO CASES MUST EXIT NON-ZERO. The denominator is ASSERTED, not
> > displayed.**
>
> Six instances of the zero-evaluated green in one day, all in this project: Phase 0's `0/106`,
> `gd-p0-rev-3`'s corpus harness scoring **0 of 0 fixtures and calling it clean**, the oracle's
> `LAST_OUT` vacuous green, the oracle's terminating `[ "$fail" -eq 0 ]`, **three of the four controls
> in rev-3's own selftest passing on an empty corpus**, and `kubeconform-strict.sh` under mutation
> M-K1 returning `ok 0 invalid / ok 0 errors / ok 0 skipped` on a render that produced nothing. The
> mechanism is the same two characters every time: `awk … END{print m+0}` prints `0` on empty input,
> and `0 -le 60` passes. **That is `grep -c` on empty input, shipped inside the two controls added
> specifically to bound what the other controls cannot see.** A control that cannot fail on nothing is
> the class's favourite hiding place, because bounding controls are written last and tested least.
>
> 🔴 **THE RULED FORM IS TOO WEAK, AND ITS OWN TARGET SAID SO** (`gd-p0-dev`, declining to add the
> zero-arm it was told to add and installing something stronger):
>
> > **DO NOT SPECIAL-CASE ZERO. ASSERT THE EXACT DENOMINATOR, AND FAIL IN BOTH DIRECTIONS.**
>
> `[ "$fail" -eq 0 ]` exits 0 on `pass=0 fail=0` — that is the ruled defect. **But it also passes on
> `pass=31 fail=0`**, which is a renamed phase function, an early `exit`, or a `git archive` that
> dropped a file. **Zero is merely the loudest value of the defect, and it is the only value anyone
> was going to look for.** Its author's sentence is the reason this supersedes rather than refines:
> ***"`pass=31 fail=0` does not look like anything."*** Replaced with `EXPECTED_ROWS=33` asserted at
> the bottom, inequality failing in both directions, and **zero caught as the extreme case rather than
> as its own arm.** Mutations M-Z1 (`28/33` → exit 2), M-Z2 (`0/33` → exit 2) and an unmutated
> satisfiability run (`33/33` → exit 0) are on the record, which is rule 30 applied to the tripwire —
> *a tripwire installed after the trip is theatre.*
>
> **Generalise the correction, not the constant:** a check that asserts *no failures* is satisfiable
> by absence; a check that asserts *exactly N evaluated, N committed* is not. The second costs one
> integer.
>
> > **An instrument that cannot be a gate ships with NO PASS STATE** — no green, no summary line, no
> > meaningful exit code, and it never appears in CI as a check. It emits candidates or it emits
> > nothing. **Then it cannot be read as coverage structurally, rather than by disclaimer.**
>
> The test that separates the two kinds of remedy is one question: *does this change what happens when
> nobody reads the output?* A note, a caveat, a printed denominator and a louder banner all answer
> **no**. A non-zero exit and a removed pass state answer **yes**.
>
> ⚠ This is the **prescriptive** sibling of rule 39, which is diagnostic. 39 explains why the region
> above a green summary is unread; 42 covers the cases with no summary at all — instance 1 is a bullet
> in a review checklist and there is nothing green anywhere near it. Registered separately for that
> reason and cross-referenced here so neither is looked up alone.
>
> ⚠ **AND IT IS NOT SPECIFIC TO CHECKS.** Two of the three instances are not checks at all: instance 1
> is a line in a review checklist, instance 2 is a sentence of prose. **Anything whose only mechanism
> is that a human reads it is in scope**, which is why "add it to the criteria" and "say so in the
> output" are the same move as "print it louder."
>
> **WHY IT RECURS — `gd-em`, from the inside, and it is the part that makes the rule stick:**
>
> > **The display remedy *feels* like it addresses the finding, because the finding was discovered by
> > noticing. We generalise from HOW THE DEFECT WAS FOUND to HOW IT SHOULD BE PREVENTED, and those are
> > different questions.**
>
> That is the complete mechanism. Every one of the three defects here was found by a human noticing
> something, so noticing is salient at the moment the remedy is written — and the remedy inherits the
> discovery method instead of addressing the failure. **`gd-doc`'s instance is the proof: the printed
> denominator is what caught rev-3's `0 of 0`, and being caught by a print is exactly what made
> printing look like the fix.**
>
> **A FOURTH INSTANCE, RECORDED BECAUSE ITS DISPOSITION IS THE TEMPLATE** (`gke-deploy-lead`, on
> `gd-em`'s self-diagnosis about assigning work at an inherited scope): the lead's response was
> *"your self-diagnosis is right and **agreeing changes nothing**, so here is the step"* —
>
> > **Every assignment states its extent AND how the extent was derived. *"Inherited from the previous
> > message"* is the flag — visible at write time, instead of after someone re-measures.**
>
> **Accepting a correction is itself a display remedy.** It feels like the strongest possible response
> and it changes nothing about the next artifact. The conversion is always the same: find the moment
> the defect is *written* and put the question there, in a form that names its own input.

> **Rule 43, revision 8 (`gd-em`, from the class it escalated and the check it wrote in the same
> message — and recorded as ONE entry at its author's explicit instruction):**
>
> > **WHEN YOU FIND AN OBLIGATION WHOSE TRIGGER EMITS NO SIGNAL, THE DISPOSITION IS AN ASSERTION THAT
> > FIRES ON THE TRIGGER — NOT A NOTE.**
> >
> > **AND THE CHARACTERISTIC DEFECT OF THAT ASSERTION IS BEING UNABLE TO FIRE.**
>
> The two halves are one entry because separating them is what went wrong. `gd-em` named the class and
> then, four paragraphs later in the same message, keyed its own gate to three exact sentences —
> **inside the ten minutes it had unblocked `gd-p1-dev` to rewrite the paragraph family those sentences
> live in, having read that unblocking first.** The strings stop existing on the rewrite, the grep goes
> green, and *it goes green identically whether the claim was retired or merely reworded.*
>
> Its author's own diagnosis, which is sharper than the lead's: **the obligation is about a CLAIM and
> the instrument counts LINES** — rule 34, inside the check built to retire an instance of rule 34,
> about the three sentences that produced rule 34. And: keying the remedy to *content that is about to
> change* is **coordinate-thinking wearing the other costume**, written four paragraphs after
> self-correcting for assigning work by coordinate.
>
> **Why the remedy carries the disease** (`gke-deploy-lead`, and this is the part worth keeping):
>
> > *Three of the four instances were checks someone had to be stopped from shipping in a form that
> > could not fire.*
>
> The mechanism is motivational, not technical: **writing the assertion feels like discharging the
> obligation**, so the effort stops at the point where the artifact exists rather than at the point
> where it can fail. Hence the standing companion requirement — rule 30's non-vacuity demonstration is
> not optional garnish on this disposition, it *is* the disposition's second half.
>
> **THE ADOPTED FORM — `gd-em`'s proposal, ruled in by `gke-deploy-lead`
> (`msgs/ruling-stale-when-marker.md`) EXPRESSLY NOT FOR THE REASON PROPOSED.** Stop grepping the
> prose; grep a marker the prose carries — `<!-- stale-when: hub-gke-stage -->` adjacent to a
> provisional claim, with the check *"if the Dockerfile contains the `hub-gke` stage, no
> `stale-when: hub-gke-stage` marker may remain in the tree."* Both limbs mechanical.
>
> ⚠ **The proposed rationale was that it generalises to every channel. IT DOES NOT — it inherits the
> coverage problem exactly, because you can only mark a claim whose trigger you already identified.**
> The proposer's stated limit ("covers only marked claims") is therefore not a footnote on a general
> solution; it is the whole boundary. **rev-3's item B is the proof: nobody knew that claim would go
> stale, so nobody would have marked it.**
>
> What it *does* cover is narrower, real, and mechanisable — **claims the author already knows are
> provisional at the moment of writing.** The two held-out items separate cleanly on exactly that:
>
> | Claim | Author knew it was provisional? |
> |---|---|
> | *"there is no published `hub-gke` image yet"* | **yes** — markable |
> | *"when true, the hub refuses to start"* | **no** — unmarkable, and this is item B |
>
> **THREE INSTRUMENTS, THREE CLASSES, EACH DEFINED BY WHAT IT CANNOT SEE — and nothing covers all
> three:** the marker is blind to the unforeseen; the sweep is blind to unhedged and out-of-corpus
> claims; the *substitute-the-render-into-the-trigger* review step is blind to claims not attached to
> a guard. **The review step is the only one that found either held-out item.**
>
> 🔴 **WHY THIS IS FUNDED IN THE SAME HOUR THE SWEEP WAS CAPPED, and it is the generalisable half:**
>
> > **AN INSTRUMENT MAY EMIT A PASS ONLY IF ITS SUBJECT IS A CLOSED, DECIDABLE PROPERTY.**
> >
> > Otherwise a pass is a claim about the unexamined. The sweep's subject — *are there stale claims?*
> > — is open, so the sweep **can be worse than nothing**; it manufactures assurance. The marker's
> > subject — *does any `stale-when: X` marker survive X landing?* — is closed and decidable, and its
> > failure mode is an unmarked claim, **which is the status quo.**
>
> ⚠ **Formulation history, kept because both versions circulated:** the lead's original was *an
> instrument that can emit a pass can manufacture false confidence; one that can only emit findings
> cannot* — true, and it describes the symptom. **`gd-em`'s supersedes it and the lead has said so**,
> because it supplies the *test*: closed and decidable is checkable before the instrument is built,
> whereas "can manufacture false confidence" can only be judged after. This is the second formulation
> today the lead has handed over to `gd-em`'s version. **Where both are on file, `gd-em`'s stands.**
>
> That is the criterion for funding a partial instrument, and it decides the sweep and the marker in
> opposite directions on one question rather than on how good each is. The same structural condition
> attaches: **the marker has NO PASS STATE, ever** (rule 42).
>
> **Four conditions before build, all ruled:** (1) a trigger name may not be minted without its
> mechanical predicate; (2) the trigger vocabulary is **closed and asserted**, so a typo'd
> `stale-when` never fires and is *caught* rather than looking correct forever; (3) non-vacuity
> demonstrated once at build time (rule 30); (4) markers are **added by the author, not retrofitted** —
> retrofitting is a third party guessing which claims were provisional, which is the coverage problem
> again wearing a maintenance costume.
>
> **And on the one file where the marker has no comment syntax** (`values.schema.json`): the sibling-key
> route is the obvious answer, *and it is a correct-by-construction claim from an agent who has been
> wrong on two of those today* — so **measure it with `helm lint` and `kubeconform` rather than
> asserting how JSON Schema is supposed to behave.** If it fails, that one site becomes a named review
> item: **one awkward file does not decide the convention for the tree.**
>
> **The registry-form rule this entry is an instance of, and the reason it is not two entries:**
>
> > **A REMEDY RECORDED WITHOUT ITS CHARACTERISTIC FAILURE MODE IS THE NEXT AGENT'S TRAP.**

> **Rule 44, revision 8 (`gke-deploy-lead`, who asked for it against itself; evidenced by five agents
> who each reported their own instance):**
>
> > **AN AGENT ENFORCES ITS STANDING RULE OUTWARD AND DOES NOT APPLY IT TO ITS OWN OUTPUT. THE
> > VIOLATION IS OFTEN IN THE SAME ARTIFACT THAT STATES THE RULE.**
>
> Five instances in one morning, **every one self-reported** — which is why the entry can be trusted
> and also why it needs to exist, since self-report is not a mechanism:
>
> | # | Who | The rule they hold | What they shipped |
> |---|---|---|---|
> | 1 | `gke-deploy-lead` | shared-plain is standing; every agent must bind its tree by SHA via `git archive` | *"on a branch"*, no location given, into a tree it knew was shared-plain with three live developers |
> | 2 | `gd-em` | obligations with no failure signal need assertions (rule 43) | a gate that cannot fail, **in the message escalating the class** |
> | 3 | `gd-doc` | controls that cannot fire are not controls | banked a `~150` bound that cannot fire, ~40 minutes after banking the neighbouring entry |
> | 4 | `gd-p0-rev-2` | the bare-negative defect | filed it, then committed the same bare negative |
> | 5 | `gd-p1-dev` | *documenting a guard is not evaluating it* (rule 41) | named the general form **after committing the same error twice in one chart** |
>
> ⚠ **This is NOT rule 31 and the difference is the whole content.** Rule 31 is a *sampling* error — a
> method's inapplicable cases go uncounted, so every method looks like it works. Here the rule is
> stated correctly, applied correctly to others, and simply **never turned inward on the artifact
> being produced at that moment.** Instances 2, 3 and 5 have the rule and its violation in the same
> document, so no amount of better sampling reaches them.
>
> **The mechanism, which is why exhortation is useless here** (and note that "apply your own rules"
> *is* an exhortation, so rule 42 forbids it as the disposition): a rule is held as a thing to *check
> others against*. Reviewing is a mode you enter; authoring is a different mode, and the rule is not
> loaded in it. Instance 1 is the cleanest — the lead had spent the morning refusing SHA-unpinned
> claims from four agents and then wrote an instruction that presumed a private tree, with no lapse in
> its understanding of shared-plain at any point.
>
> **THE DISPOSITION, and it is a step because rule 42 requires one:** before an instruction or an
> artifact goes out, apply **the rule most recently stated in it** to itself. Not all forty-four —
> **the one you just wrote down**, because instances 2, 3 and 5 are all *proximity* failures and the
> most recently invoked rule is where the hazard concentrates. It is a single question, it names its
> own input, and it costs one re-read.
>
> The cost of instance 1 was zero only because `gd-doc` held two contradictory instructions inside two
> minutes and stopped rather than picking one. **That is not a control, it is a coincidence of
> timing**, and it is the reason this is registered rather than noted.

> **Rule 45, revision 8 (`gd-doc`, from two independent instances measured within twenty minutes —
> one self-reported by `gd-p0-dev`, one measured against `gd-p0-rev-2`'s corpus):**
>
> > **A CHANGE IS NOT DELIVERED WHEN IT IS PUSHED. IT IS DELIVERED WHEN THE PARTY BLOCKED ON IT CAN
> > SEE IT. IN A REVIEW TOPOLOGY THE ANNOUNCEMENT IS NOT A FORMALITY — IT IS THE EVENT.**
>
> | # | The artifact existed | The blocked party's state |
> |---|---|---|
> | 1 | `7a54ba7c` pushed to `origin/scion/gke-chart-p0` | `gd-p0-rev-2` reported *"holding for the Blocking-1 commit"* **twice**, after it had landed |
> | 2 | `8cc8d9b` and `f3fabfd9` both real commits | **neither is resolvable from the other reviewer's clone** — `8cc8d9b` is not reachable from `/workspace` at all, only via `refs/pull/*/head` |
>
> Instance 1 is its author's own filing and the diagnosis is exact: ***"I treated the push as the
> event and the announcement as a formality, and in a review topology the announcement IS the
> event."*** Two reviewer messages were spent blocked on a state that had already cleared. **Nothing
> was broken, nothing was missing, and the cost was two rounds** — which is why this class survives:
> it never produces an error, only latency, and latency is nobody's finding.
>
> Instance 2 is the same defect one level down and it is worse, because it is silent on both ends.
> Agents published numbers at SHAs their readers could not resolve, **in a thread where those numbers
> were being compared** — and rule 27 was satisfied throughout, because everyone did quote the SHA.
> Quoting an identifier the reader cannot dereference is a citation, not a reference.
>
> **THE DISPOSITION, two clauses, both cheap:**
>
> > **Announce the push, in the channel the blocked party reads, naming the SHA and what it closes.**
> > **And if the SHA is not on a pushed branch, say how to fetch it** — `git fetch origin
> > '+refs/pull/*/head:refs/remotes/pr/*'` into a throwaway clone resolves a PR head in one command
> > and mutates nothing.
>
> ⚠ **This is not rule 27 and the difference is the whole content.** Rule 27 makes the claim carry the
> conditions that would change it, so a *reader* can judge the number. 45 is about whether the reader
> can **obtain the object at all**. A perfectly-controlled measurement at an unfetchable SHA is
> unfalsifiable, and today it was 4x off the same measurement at a fetchable one — see rule 34's
> corpus rows. **An unreachable reference is a claim with its evidence deleted, and it looks exactly
> like a rigorous one.**
>
> 🔗 **45 IS THE SENDER-SIDE COUNTERPART OF RULE 17, AND THE PAIR SHOULD BE READ TOGETHER.** 17 binds
> the receiver: *when you cannot reproduce a cite, your search is the first hypothesis, not their
> file.* 45 binds the sender: *make the object reachable and say that it moved.* Note that both were
> filed by `gd-p0-dev`, months apart in registry order and hours apart in fact, from opposite ends of
> the same failure — and that rule 18's *"a tree that was deliberately behind"* is the third face of
> it. **Instance 2 is the case where 17 alone is not enough: rev-2 did exactly what 17 requires,
> declined to assume, and flagged the unreachability — and the number still could not be checked,
> because the remedy was not theirs to apply.** A rule that binds only the party without the power to
> fix the problem is half a rule.

The rule is unpersuasive without its evidence, so: the Phase 7 image instruction was
corrected **three times, each time one level further down**, and every correction was right
about the goal and wrong about the mechanism.

1. **Issue owner:** "a new **final** stage" — right that it should be a stage, wrong that it
   is last. The last stage is Docker's default build target, so "final" contradicted the very
   rationale given for choosing a stage (§16 Q3: `--target` leaves the default target
   untouched).
2. **Engineering manager:** "put it **before** the final stage" — right that it must not be
   last, wrong that *before* is reachable: a stage can only `FROM` a stage defined earlier,
   and nothing earlier is the debian runtime.
3. **Developer:** "that is unbuildable without duplicating the block; here is what works" —
   name stage 3, derive `hub-gke` from it, and terminate the file with an empty stage (§11).

The general lesson, plainly: **the correction came from whoever was closest to the file.**
That is why the rule is about distance from the artifact, not about seniority or document
precedence. When you write a phase brief, state the property the artifact must have and the
criterion that proves it; leave the mechanism to whoever is holding the file.

**Running log — append to it.** The rule is only as persuasive as its instance count, so
instances are logged here rather than left in message history.

| # | Prescribed mechanism | Why it was wrong | Caught by | Recorded in |
|---|---|---|---|---|
| 1–3 | Phase 7: "a new **final** stage" → "put it **before** the final stage" → the working arrangement | the last stage is the default build target; a stage can only `FROM` a stage above it | issue owner → engineering manager → **developer** | §11, and the three items above |
| 4 | Probes target `/api/v1/readyz` | the route and its auth exemption are both **exact** matches on `/readyz` (`server.go:3363`, `auth.go:419-421`) | verification against source | revision 3 note, §9.2, §13.1 |
| 5 | `defaultMode: 0600` on the settings Secret, carried forward from the copy shape | correct when a uid-1000 init container **owned** the file; unreadable once the projection is root-owned and the process is uid 1000 | **`gd-p1-dev`**, mid-build | §5.2 |
| 6 | §19: "`hub.args` overrides the default command" | contradicted the Phase 0 criterion "no value can disable hosted mode"; `hub.args` is append-only with a reserved-flag guard | **`gd-p0-dev`**, mid-build | §19, §4.1, Phase 0 acceptance |
| 7 | Phase 1: "the `settings-init` init container prepares the directory" | the `emptyDir` is created by the kubelet before any container starts, and hosted mode never materialises `~/.scion` — so the reduced role was already nobody's job | **`gd-p1-dev`**, against the built branch | §4.1, §5.2, Phase 1, §18 item 28 |
| 8 | **This document's own claim** that `--base-url` was missing from Phase 0's reserved list, and that the list had three groups | it was reserved in `scion-hub.hubArgs` (since `51f62ab`) and the list had **four** groups; `gd-doc` reasoned about the chart from the design instead of from the branch | **`gd-em`** and **`gd-p0-dev`**, from the branch | this row, §5.4, Phase 0, §19, and rule 4 above |

Instance 5 is the one to notice: it was written by the engineering manager who commissioned
this section, in the next brief he sent after it landed, and it was caught by the developer
holding the file. **The rule predicts that its own authors will need it** — a
brief is mechanism prescribed at a distance from the artifact by construction, and seniority
does not shorten that distance.

**Instance 6 is the rule working, not the rule being broken** — and it is the one to imitate.
Prose and criterion disagreed; the developer implemented the **criterion**, reported the
conflict upward, and did **not** silently resolve it in either direction. The document was
then corrected to match what the criterion had always required.

**Instance 7 is the failure mode of a PARTIAL correction, and it is worth its own rule.**
Revision 5 removed the copy, the `/etc/scion` path and the `0600` — correctly — and left the
init container standing in a reduced role. Revision 6's decision removed the role as well, but
only in the sentences that happened to be under the pen. What survived was the *harmless*
clause: "prepares the directory rather than copying the file" **reads as the correction**, which
is exactly why five rounds of review passed over it. A leftover that still looks wrong gets
deleted; a leftover that has been half-fixed acquires camouflage.

So, the rule: **when a correction reduces a thing's role rather than removing it, the next
question is whether the reduced role is a job at all.** Ask what would break if the reduced form
were deleted outright. If the answer is "nothing", it was never the corrected version — it was
the original with its justification removed, and it will be re-expanded by the first reader who
notices a component doing nothing. Two mechanical consequences: **grep for the name, not the
behaviour** (the copy was gone from every section; the string `settings-init` was still in five),
and **a phase's file list is as normative as its acceptance bullets** — an implementer reads the
list first, and item 7 was found in a list, not in a criterion.

**Instance 8 is this document doing it to the chart, and it is instance 7's mechanism turned
around.** Revision 7 asserted, in bold, that `--base-url` was missing from Phase 0's
reserved-flag list. It had been there since `51f62ab`, and the list had **four** groups rather
than the two the design described. The claim was derived from the design's own account of the
chart instead of from the branch.

**The trap is that the reasoning was internally coherent.** Given a two-group list, "`base-url`
shares `--config`'s failure mode but for an unrelated reason, so group by reason" is a correct
analysis and a genuine improvement. It read as a finding *because it was good reasoning* — about
a file that had stopped existing in that form some hours earlier. **Sound reasoning over a stale
premise is indistinguishable from a finding**, including to its author, and no amount of care
applied to the argument detects it. Only re-reading the artifact does. The rule at the top of
this section is usually invoked to protect implementers from briefs; this instance is the design
document needing it in the other direction, and the sentence about seniority not shortening the
distance applies unchanged.

**It also produced a second-order error worth its own note.** The proposed grouping filed
`session-secret`, `dev-auth` and `enable-test-login` under "reserved because the chart already
sets them" — flags the chart must **never** set, whose entire reason for being reserved is the
opposite of that rationale. `gd-p0-dev` caught it. Beyond being wrong, it would have destroyed
the one property that made that group maintainable: **it is the only group that CAN be verified
against rendered `args`**, so a maintainer checking it finds every entry, and an entry that
leaves the chart leaves the list at the same time. Adding three flags the chart never emits
imports the tidying-deletion failure into the single group that was capable of being immune to
it. **A misfiled entry does not merely sit in the wrong place; it can destroy the invariant that
made the right place checkable.**

**Instance 9 is that same sentence being half-wrong, and the half it got wrong is the
instructive one.** Revision 7 wrote that group 1 "**is** the only group verifiable against
rendered `args`" — present tense, as a description of the branch. `gd-p0-rev-2` found that the
property does not hold there: `production` and `port` are members the chart does not render. The
analysis was right about which group *could* carry the invariant and wrong about whether it
*does*, and that difference is the entire value of the claim. **A document asserting an invariant
the artifact does not satisfy is worse than one asserting nothing, because the next reader takes
it as a licence to skip the check.** State an invariant as *required*, or as *held* — never in
the tense that makes those indistinguishable. The concrete damage was one step away: group 1's
comment instructs removal of members absent from the rendered args, so this document's
description would have sent a maintainer to delete `production`, and that deletion re-opens
hosted mode (Phase 0, direction B). Note also what class this is. Rule 4 says claims about a
chart file require fetching the branch; the branch *had* been fetched, and the membership was
right. What was never checked was the **modality** — whether the property asserted of the
members actually held of them. Fetching the artifact establishes what is in it, not what is true
of it, and the second question has to be asked separately.

**The resolution is this section's own rule applied to itself: the design no longer enumerates
the reserved flags.** It states the property — grouped by reason, each group failing with its
own message, only some verifiable against rendered output, channel changes move an entry rather
than adding an emitter — and the enumeration lives in `_helpers.tpl`, where it is executable and
cannot drift. **Prose that duplicates an executable list is a second source of truth that no
test covers**, and it went stale in under a day. Where the design and the chart must agree on a
list, the chart holds the list and the design holds the reason.

**Instance 10 is the source of rules 16–21, and it is worth its length because it contains TWO
ERRORS POINTING IN OPPOSITE DIRECTIONS ABOUT THE SAME STRING.** All cites below are verified by
`gd-doc` against the repository, per revision, rather than relayed.

**The string.** A `fail` message in `_helpers.tpl`'s `$neverPassed` group: *"…it redirects the
hub's entire configuration load away from the settings file this chart delivers, so the hub
would run on a file the chart has never seen while every rendered value continued to report the
operator's intent."*

**The disagreement.** One agent asserted the string had been present in revisions X and Y from a
**hand-written list** (rule 16). Another could not reproduce one of the cites and concluded the
*cite* was wrong (rule 17), when its own search was wrong and its tree was deliberately behind
(rule 18). Two errors, opposite directions, same string: one party over-claimed history it had
not searched, the other under-credited a cite it had not been able to search *for* correctly.

**Ground truth, by containment search rather than pickaxe.** The string is present verbatim at
`cb183de:435` and at `51f62ab:384`; count is 0 at `7911e16` and 0 at `721fc77`. Ancestry:
`721fc77` ← `afced2a` ← `50ca615` ← `7911e16` ← `cb183de` ← `51f62ab`. **`git log --all -S` on
that string returns `7911e16` and `51f62ab` and NOT `cb183de`** — see rule 16's correction; the
remedy prescribed for this very incident would have reproduced a version of the incident.

**The remedy prescribed for this incident would have reproduced it, and that is one level worse
than the disease** (`gd-em`, against himself). `git log --all -S` was prescribed as the
instrument that *cannot be curated by someone who already has an answer* — and it omits
`cb183de`, the very commit whose omission started the affair. **A hand-curated list can at least
be interrogated: *how did you choose those four?* A wrong command cannot, because the answer is
"I didn't, `git` did."** Prescribing tooling converts an error of judgement into an error of
infrastructure, and infrastructure errors are invisible at exactly the moment the output looks
authoritative. Hence rule 16's mechanism is corrected in place rather than the rule being
withdrawn: **a rule about not trusting curation that ships with a curating command is worse than
the disease.**

**Rule 19 caught this, on its own authors, within the hour of being written:** everyone who
checked the incident reached for the pickaxe, so **four checks shared one premise about what
`-S` answers, and were therefore one check.** Note the pattern this completes, because it is no
longer anecdote — **a rule violated by the person who had just authored it**, five times in one
day across four agents. Authorship of a rule confers no immunity to it and appears to confer the
opposite, which is the strongest available argument for **mechanisms over reminders**.

---

#### The adjacency finding — why reminders fail, stated as a mechanism with a testable prediction

**The mechanism** (`gke-deploy-lead`): ***articulating a principle discharges the felt obligation
to apply it.*** Having written the refutation, the matter feels handled — so **the next instance
does not present itself as an instance.** It is not forgotten and it is not ignored; it is
*filed*. The cognitive slot the rule would have occupied is already full, of the rule.

**Why this is a finding and not a theory of mind: it makes a prediction, and the prediction is
falsifiable.** If ordinary carelessness were the cause, the violations would be spread evenly
across each author's output. The discharge mechanism says they should **cluster near the
articulation, in time and in space.** They do — in every instance on record:

| # | Author | The rule | Where the violation sat relative to it | Evidence |
|---|---|---|---|---|
| 1 | `gd-p0-dev` | rule 15 (agreement ≠ corroboration) | Authored into `_helpers.tpl`, **~1 hour** before committing an instance | verified by `gd-doc` in-tree |
| 2 | `gd-em` | rule 8 (findings closed by configuration) | **Same hour** as sending it | relayed, `gd-em`'s own report |
| 3 | `gd-em` | rule 16 (do not trust curation — use an instrument) | Prescribed an **uninterrogated instrument** as the remedy, in the rule itself | verified by `gd-doc` (`-S` omits `cb183de`) |
| 4 | `gd-doc` | rule 11's corollary (cite the subject precisely) | Mis-cited `:859` as `:858` **inside the rule about citing precisely** | verified, self-caught |
| 5 | `gd-p0-rev-2` | its own refutation | **One screen** from the refutation | relayed |
| 6 | `gd-p0-rev-3` | the sentence falsifying its finding | **Inside the finding that quoted it** | relayed |

| 7 | `gd-p0-rev-2` | its own filed finding — a bare negative that passes on an empty subject | **Filed the defect against another author's script, then committed the same defect in its own** (`package excludes ci/ and tests/`) | relayed, `gd-em`'s report |

*(Rows 2, 5, 6 and 7 are relayed rather than verified by `gd-doc` and are labelled as such under
rule 11. `gd-em` counts five across four agents; the table lists more because rows 2 and 3 are
both his and row 7 arrived later. The count is not the claim — the adjacency is.)*

**Row 7 is instance eleven and it arrived within the hour of `gd-em` writing *"it would be a poor
joke to add instance eleven while mechanising against it."*** That is the prediction operating
prospectively rather than retrospectively for the first time, and it landed in the place the
prediction pointed at.

**A twelfth followed the same day and it is the cleanest of all of them, because the author
supplied both halves**: `gd-p1-dev` wrote the registry's best statement of the empty-subject
family (§13.1) **out of the discovery that its own phase-acceptance criterion was an instance of
it** — five `ok` lines from five empty files, against the exact mechanical assertion its brief
named as acceptance. `gke-deploy-lead`: **authorship of a rule confers no immunity to it, and
appears to confer the opposite** — holding for the fifth time in one day, across five agents.
*(Under the pre-registered same-commit window below, this row and row 7 are candidates rather
than confirmations until the commits are checked; `gd-doc` has not verified either in-tree.)*

**None of that is what ordinary carelessness looks like.** Carelessness has no reason to prefer
the neighbourhood of a rule.

> **🔴 THE EVIDENCE ABOVE IS SELECTED BY THE DETECTOR, AND THE HYPOTHESIS' AUTHOR SAYS SO FIRST.**
> `gd-em`, conceding it in full and against himself: *"I generalised from a sample selected by the
> detector, and then presented the sample's shape as evidence for a hypothesis about the
> population. **Adjacency is the phenomenon I was measuring and also the reason I could see any
> instances at all**, so five-for-five is exactly what I would observe whether the hypothesis is
> true or false. **There is no denominator.**"* This is an instrument error committed while
> quoting the project's own rule about checking the instrument rather than the conclusion — which
> is, itself, an instance of the mechanism under discussion. `gke-deploy-lead` caught it, on a
> **second** reading of its own note, and that is recorded as **the control working rather than as
> an additional instance.**
>
> **The window is therefore PRE-REGISTERED, before the next instance rather than after it: SAME
> COMMIT.** One disjunct, not four, chosen because it is **mechanically checkable from git rather
> than by recollection** — which is the property that stops it being adjusted after the fact
> (rule 29).
>
> **Registered together with what it costs: at least two of the original five are disqualified.**
> Rule 8's violation reached its author by message and the violation was in a *ruling*, not a
> commit; the `-S` pickaxe error likewise. **The pre-registered score on the existing sample is
> roughly three of five, not five of five.** The three that survive — rev-2's `-lt` beside its own
> refutation, `gd-p0-dev`'s instance ten, rev-3's finding quoting the sentence that falsified it —
> are the cases where **the artifact and the violation shipped together**, which is the tightest
> form of the claim. The caveat is recorded **in this note rather than elsewhere**, deliberately:
> *a prediction filed as five-for-five and qualified somewhere else reads as five-for-five.*
>
> **The unbiased measurement is already commissioned and it is not this table.** Rule 28's
> two-number design yields a **grounding density per enumerated region**, scanned regardless of
> where any rule was articulated — a continuous quantity to regress against
> distance-from-articulation instead of a binary anyone can argue about. **If distant regions come
> back as dirty as adjacent ones, this hypothesis is dead** and the project learns it has been
> fixing the visible half, which is the more valuable outcome of the two.

**The operational consequence inverts the intuition, and it is the reason this is recorded rather
than merely noted: THE RULE'S NEIGHBOURHOOD IS THE HIGH-RISK ZONE.** The instinctive
countermeasure — *try harder near the rule* — points exactly the wrong way, and worse, trying
harder is itself an articulation. So the countermeasures are the ones that do not run on
attention at all:
- **Mechanise the rule** in the same commit that states it, or accept that you have written a
  reminder (rules 22, 23).
- **Review the artifact adjacent to a newly written rule FIRST**, not last, and preferably by
  someone else. The paragraph above the new rule is the likeliest place in the file for an
  instance of it.
- **Treat "I have just written this down" as a risk marker rather than a competence marker.**

This is also why the prose check must be a script and not a review pass, and why it lands in
Phase 0 rather than Phase 6 (rule 28).

**The coordinator-seat lesson, and it is the part that generalises** (`gd-em`, against his own
seat). Two agents disagreed about a matter of fact, and **presented confidence decided it —
which is exactly the input that should have decided nothing.** Both parties could have settled
it in one command. The seat amplifies this: **only the coordinator seat can convert a claim into
an instruction aimed at another agent**, so a claim that wins on confidence there does not stay
a claim, it becomes work. The mechanism: **WHEN TWO AGENTS DISAGREE ABOUT A MATTER OF FACT, ASK
BOTH FOR THE SAME ARTIFACT AT THE SAME TIME** — same command, same output, both directions, and
before any instruction issues from it. Neither party is asked to concede; the artifact is.

**Rule 15 applied to the incident.** Everybody's conclusion — *reserve the flag, the guard is
right* — was correct throughout, and every check returned "refuse". So the derivation went
unaudited for as long as the verdict kept agreeing, which is precisely rule 14's non-self-limiting
shape, with rule 15 as the reason it persisted.

**And the unification with rule 20's phantom rule, which is why these are one instance rather
than two.** `gd-em`: *"both of us remembered the artifact instead of opening it."* A hand-curated
list of commits and a remembered rule number are the same error, and **the tell in both cases is
that the citation was produced without the artifact being open.** The final observation is the
one to carry into every phase brief, and it is the reason rules 16–21 are specified as commands
and counts rather than as advice: **`gd-p0-dev` had written rule 15's content into `_helpers.tpl`
about an hour before committing an instance of it, and `gd-p0-dev`'s own "NOTHING ABOVE IS A
STATEMENT ABOUT A LATER PHASE" remedy sat six hundred lines up from five violations of it
(rule 12).** THESE RULES DO NOT WORK AS REMINDERS. THEY ONLY WORK AS MECHANISMS.

**A different failure, worth logging separately: a right conclusion resting on a refutable
reason.** The `0440`-plus-`fsGroup` rejection in §5.2 was argued on two grounds — no isolation
gained, and a costly recursive `chown` on the Filestore share. The first is decisive; the
second was wrong (`fsGroupChangePolicy: OnRootMismatch` skips the walk), and it was
`gke-deploy-lead` who challenged it and the documentation that settled it. The conclusion never
moved. **It then failed a second time in the same place:** the replacement text offered
`fsGroupPolicy: None` as the plausible-but-unverified mechanism, and when `gke-deploy-lead`
fetched the actual CSIDriver manifest that was wrong too — the driver declares no
`fsGroupPolicy` at all and the *default* excludes `ReadWriteMany` NFS. Right conclusion, wrong
route, twice. **A hedge is not a substitute for verification:** "plausible; unverified" made the
claim survivable, not correct, and it still had to be replaced. **The danger is not the immediate decision but the later reversal:** a reader who knows
about `OnRootMismatch` meets the weak reason, concludes the analysis was sloppy, and reopens a
settled question — discarding the strong reason along with the weak one. **One decisive reason
is stronger than one decisive reason plus one refutable one.** So: prune the supporting
argument you cannot defend, even when the answer it supports is right, and audit the reasoning
of conclusions you agree with — the six instances above show the rule catching wrong answers;
this one shows a right answer still needing it.

**Revision 8 splits a nastier variant off this one — see rules 14 and 15.** The failure above
is *self-limiting*: the refutable reason is checkable, so checking it kills the reason and the
only risk is that the good reason goes with it. The variant is not self-limiting, because the
**true mechanism points the same way**, so the check comes back green and **confirms the bad
reason**. `--config` carried four successive false rationales that way (§5.2), and the only
method that has ever caught it is verification by a different route — with `gd-p0-rev-2`'s
correction attached, that the second route helps only because it produces a different *reason*
and somebody compares them. Agreement on the verdict is not corroboration of the reason.

**And a third failure, in the same reasoning, different in kind — log it separately or the
lesson is lost.** The first two were wrong claims about **how Kubernetes behaves**. The third
was a wrong claim about **our own chart**: §5.2 asserted that `fsGroup` was absent while §4.4
had been rendering it unconditionally **since revision 2**, and the assertion survived four
revisions. So the lesson is not "verify external mechanism" — it is that **a forward note
assuming something about another section of the same document is a claim about a file you can
open, and nobody opened it.** External claims at least *look* like they need checking; a claim
about your own artifact reads as background. It was caught only because someone applying an
unrelated correction read the neighbouring section. That is luck, not process, which is why the
reconciliation rule above was extended to cover it rather than left as a third anecdote.

### 17.2 Phase deltas for revisions 5–8 — read this when writing a brief

**What this section is, and why it is in the document rather than in a message.** Revisions 5–8
changed what several downstream phases build, and a phase developer reading the design cold will
not know which parts are new or which of them reverse an earlier instruction. This is the diff
against what such a developer would otherwise assume — **not a summary of the design**, and it
states nothing that is not stated normatively elsewhere; every entry routes to the section or
acceptance item that owns it.

It lives here because a brief written from a message is a **derived source**, and derived sources
consulted in place of the artifact they describe have cost this project three separate errors in
one day (§17.1 instances 8 and 9, and §5.2's `--config` erratum). The delivery channel has also
now dropped a message that was confirmed delivered. **A phase brief should be checkable against
this document, and this document should not require a message to be complete.**

**Phases 0, 1 and 7 — in flight, and revision 8 changes all three.** Read §5.2's **two errata**
and item 36. Recorded here as well as messaged, because a brief written from a message is a
derived source and these three are the ones already being implemented.
- **`--config` is LIVE on the chart as it stands, not inert.** The settled statement, and the
  only one that belongs in a chart comment: ***`--config` is live at Phase 0 because the global
  settings document has no non-nil top-level `server:` key.*** Say nothing about a file existing
  or about who authors it — **each of those formulations has now been wrong once.**
- **Phase 0's group-2 comment must be two-state and name the transition** (rule 12): *live
  today; inert once a `settings.yaml` carrying a non-nil top-level `server:` key sits at
  `$HOME/.scion/`, which Phase 1's Secret is the only thing that will ever do.* The reservation
  is unchanged and is now justified by the flag **working**, which is a far stronger reason to
  reserve it than "it does nothing anyway" — the latter reads as tidyable.
- **Phase 1's obligation is exact and testable:** every values permutation renders a non-nil
  top-level `server:` key, and a comment at the render site says the key is load-bearing for a
  Phase 0 guard.
- **Item 36 binds Phases 7, 0 and 1 together:** three deliverables decide one `os.Stat` at
  `cmd/server_foreground.go:104`, each was ruled safe by citing one of the other two, and no
  brief names the other two. Whichever lands next states in its PR body what the other two do to
  `$HOME/.scion`.
- **🔴 Phase 0 owes a TENSE PASS over every comment and every `fail` message in `_helpers.tpl`,
  and it is a sweep with a count, not an impression.** `gd-p0-rev-3`'s axis-d review of PR 1093
  at `721fc77` returned REQUEST CHANGES with **five Required findings, all the same error**: a
  present-tense claim about what the chart delivers or what the binary does that becomes true
  only in Phase 1, 2 or 4 (rule 12's false-now half). **Two of the five are in `fail` messages**,
  which is where it costs most — an operator reads them while trying to comply. The remedy is
  already in the file at `_helpers.tpl:622-627` and was applied to exactly one site; apply it to
  the rest, or two-state the claim. The sharpest instance to fix first, because it is cited
  three times in one file and is wrong twice: `server_foreground.go:1168-1200`, correctly
  future-tensed at `:296-299` and falsely present-tensed at `:234` and inside the
  `assertStartupBudget` fail message at `:256`. `migrateStore` takes no lock unless the driver
  is `postgres` (`server_foreground.go:1169`) and the default is `sqlite` (`hub_config.go:540`).
  **The false pair is a source and its derivative** — `:234` is the doc comment that supplies
  `:256` its harm — so fixing the fail message alone leaves the comment that justifies it wrong,
  and the next reader repairs the message back *from* the comment.
- **The pass is PER-CLAIM, not per-paragraph, and rev-3's five sites are a FLOOR, not the list.**
  `_helpers.tpl:226-240` is mixed-tense inside one paragraph: the `CompositeStore.Migrate`
  ordered-sequence claim (`composite.go:179-227`) is true under sqlite today and only the lock
  half is Phase 2's. Classifying paragraphs is the cheap version of this pass and it does not
  work. Report the **count of claims examined** (rule 9), not the count of sites fixed.
- **Two further live findings in that file, both rule 13's understatement half.** (i)
  `_helpers.tpl:859` says `--config`/`-c` *"layer a second config file over the hub's own"* —
  true of route B, one rung short of route A, which returns the target's `server:` section as
  the **whole** config. The same sentence then says the flags *"go silently inert once the
  configuration phase renders a settings file"* — the banned file-existence formulation, in a
  `fail` message. (ii) `_helpers.tpl:623-624` reasons from *"where no `settings.yaml` exists and
  the overlay is live"* — same formulation, and it sits in the very paragraph that models the
  correct treatment for rule 12. The trigger is the **key**, in both places. Neither changes a
  guard; both undersize or misstate a reason a later phase will build against.
- **🔴 The harness fail-open fix is PHASE 0's, before approval — it is NOT deferred to Phase 6,
  and the reasoning is worth keeping because it generalises to every "fix it at the wiring
  stage" proposal.** A harness that reports a broken chart when `helm` is missing is **worse in
  CI than out of it**, and Phase 6 is where it enters CI — so shipping it broken means **the
  wiring phase inherits a defect it did not create and will be under pressure to wire around**.
  Six items, one commit, `gd-p0-dev`, files under `tests/` only: `gke-deploy-lead`'s tool-presence
  arm, exit 2 with the *nothing-was-analysed* wording, the count check firing independently of
  assertion failures, plus `gd-p0-rev-2`'s three stale contract comments.
- **The gate is on the PHASE, not on the review, and that distinction is the mechanism.**
  `gd-p0-rev-3` approves *the chart* and its pre-commitment stands untouched — a Markdown
  deletion cannot move a byte-identical render. **The phase-complete signal is held separately
  and is not sent until the harness fix is verified.** This is the answer to the standing worry
  that **an item which blocks nothing gets carried by nobody**: it blocks something specific and
  named.
- **The verification is `gd-p0-rev-2`'s, not the author's, and by the lead's route** — because
  **an author verifying their own fail-open fix is the same shape as the original defect**
  (rules 25 and 26). Exactly: `git archive` of the head into a clean tree, `bash
  tests/run-all.sh`, **no `helm` on `PATH`**. Want: **exit 2**, a **non-zero meta-failure count**,
  and **no sentence accusing the chart of anything.** The third want is the one that would be
  dropped from a paraphrase and is the whole point — a suite may fail, but it may not blame the
  artifact for its own missing tooling.
- **Also assigned:** MM7 for the removed-tool case in the committed table, and a re-run of
  **MM0–MM6 asserting the WHOLE OUTPUT LINE** rather than the single field each mutation was
  aimed at (rule 24). `gd-em` predicts at least one further defect; the outcome is recorded in
  rule 24 either way.
- **Report every count with its environment from here (rule 27).** Not `106/106` but
  `106/106 with helm 3.16 and kubeconform present`. Subtree-wide convention, effective now.
- **🔴 THE FIX SCOPED TO THE REPORTED INSTANCE DOES NOT SHIP — and this is the same ruling made on
  the prose four hours earlier, arriving at the harness** (rule 14's scoping corollary). The held
  patch guarded the short-circuit with `[ "$total_assertions" -eq 0 ] ||`, which closes the
  reported instance and leaves the family open: **`gd-p0-rev-3` measured one genuine red assertion
  at `60b2912` with helm and kubeconform present producing `assertions: 102/106  meta-failures: 0`
  — 102 is not 0, so the clause never fires.** rev-2's own Step-4 sentence had already been broader
  than its patch: *"not merely quiet during a tool outage; it is switched off by ANY assertion
  failure."* **A fix scoped to the reported instance is a deferral wearing a patch's clothing, and
  it is harder to catch than an open deferral because the reporter's test goes green.**
- **The correct layer is a conceptual correction, not a patch, and step 1 is the whole of it:**
  (1) **parse `executed=` regardless of exit code and sum that** — the number then answers *did
  every assertion RUN*, which is **orthogonal to whether they passed**, and that is what rule 9
  says the number is for. *We had been computing a pass-count and reading it as a coverage-count
  all day.* (2) **Then delete `real_failure -eq 0` entirely** — once the total counts executed
  rather than passed assertions a red run is no longer legitimately short, so the spurious note the
  conjunct suppressed cannot occur. The original reason was sound and it **stops applying**: that
  is a correct deletion, not a loosening, **and the comment beside it says so.** (3)
  `chart-integrity.sh` needs an `executed=` line; the other three already print one
  (`reserved-flags.sh:66`, `update-strategy.sh:53`, `render-guards.sh:182`). **Target behaviour:
  `106/106  meta-failures: 0`, exit 1 — a red chart with an intact check set, correctly
  distinguished, which is what the two exit codes were for.**
- **The amplifier that proves it is the right layer:** `run-all.sh:104` sets `real_failure=1` and
  **never sums that script's count at all**, so one red assertion in `render-guards.sh` silently
  removes all 46. **The size of the shortfall is unrelated to the size of the breach.** Step 1
  fixes that as a side effect.
- **🔴 ATOMIC: the `executed=` parse and the tool-presence guard are ONE change (rule 30).** After
  the parse lands, the missing-tool case is carried *entirely* by that guard and by the oracle's
  **zero-`ok`-lines-under-absent-helm** criterion — nothing else detects it, because with no
  `helm` all 106 assertions execute vacuously and the run reports `106/106`. Ship the parse a
  single commit ahead of the guard and the harness is silent on the failure mode the morning was
  spent finding.
- **`reject()` matches `hub.args may not contain -<flag>:`, and specifically NOT `execution error
  at`.** Three different worlds produce the refusal outcome — an absent toolchain, a chart that
  cannot render for an unrelated reason, and any future required value — and **only the
  flag-naming message distinguishes all three** (rule 14).
- **`tests/verify-failopen.sh` lands in Commit A, in-repo, and it still fails 2/4 at `60b2912` —
  which is what makes it worth committing** (rule 23). Two conditions that must land in the *same*
  commit or Commit A fails on itself: it is **enumerated and excluded from the assertion count as
  an explicitly named exception** (it invokes `run-all.sh` and takes a `<sha>`), and the exception
  list ships with the file, because a fifth `.sh` otherwise trips the `ls -1 *.sh` disk check as
  unenumerated. **It is also mutated rather than run as given**: its own author disclosed that its
  step 4 asserts `a != b` and so would not have caught the 102/106 counter-example — **the verifier
  is incomplete in exactly the way the patch was.**
- **🔴 REQUIRED IN COMMIT A, AND IT IS A SWEEP RATHER THAN A FIX: every claim in the chart's prose
  about what the chart does NOT YET do** — `yet`, `today`, `currently`, `none of them`, `no
  longer`, `does not`. **For each one: rewrite it conditionally so it ages, or key it to a
  committed state number** in the `DELIVERS_*=0` form, so the number and the paragraph must move
  in the same diff (rule 12's discriminator). **Report the count found. If the sweep returns one,
  that is a finding about the sweep, not about the chart** — eleven instances of the parent class
  say otherwise. A control proposed for a single sentence is the round-6 answer applied at round 3
  (rule 32), and this class *reproduces in new prose* rather than draining.
- **🔴 DEADLINE, NOT A PRIORITY: it lands before Phase 1 merges or it is DROPPED AND THE DROP IS
  RECORDED.** The only transition it exists to catch is P0→P1 and that is the next commit — **a
  tripwire installed after the trip is theatre** (rule 28). Ahead of the `kubeconform` script in
  the remaining time, because that one retires nothing that is about to change.
- **The test for what Commit A may carry is NOT "does the number change" — it is "is the number
  TRUE AND MEASURABLE AT THIS HEAD."** `DELIVERS_BASE_URL_CHANNEL=0` is a fact about **P0's**
  chart, measured at P0's head with a positive control against five bare negatives, so it raises
  106 to **107 legitimately** — it is a new assertion about the tree it ships in. P1's `7`, `22`
  and `hub.baseUrl` fail that test and Phase 0 cannot represent them. **The two rulings are not in
  tension and the second must not be used to keep the first out.**
- **🔴 THE EXIT-CODE QUESTION, AND ITS RESOLUTION IS THE THIRD TIME TODAY THE ANSWER WAS "INVERT
  IT".** The proposal was a `127)` arm, on the ground that a missing binary returns 127. The
  counter-measurement is right: every script captures `helm` in `$(…)` and converts the outcome to
  `pass`/`fail`, **so 127 is consumed at the call site and `run-all.sh` only ever sees `1`** — the
  arm is unreachable. **But the counter-measurement was computed against `60b2912`, and the
  tool-presence guard does not exist at `60b2912`.** Its entire purpose is to change what the
  scripts return, and a bare `command -v helm || exit $?` propagates 127 directly. *A property of
  the tree before the change was used to rule out a guard for the tree after it* — rule 12's tense
  error, arriving in a review verdict rather than in a comment.
  **🛑 THE RESOLUTION IS "ADD NOTHING", AND THE ROUTE TO IT IS WORTH MORE THAN THE ANSWER.** The
  committed-set proposal — *`run-all.sh` commits the set `{0, 1, 2}` and treats anything outside it
  as a meta-failure* — was **retracted by its own author within four minutes**, on the ground that
  it already existed. Verified here against the file rather than against the retraction:

  ```
  run-all.sh:104   1) echo ">>> ${s}: ASSERTION FAILURE (exit 1)"; real_failure=1 ;;
  run-all.sh:105   2) note "${s} exited 2: it did not run its full set." ;;
  run-all.sh:106   *) note "${s} exited ${rc}, which is not part of the contract." ;;
  ```

  The `case` handles 0, 1 and 2 explicitly and sends everything else to `*)`. **That IS the
  committed set, in exactly the proposed form, ten lines from the defect being discussed.** So the
  outcome is: **no `127)` arm, no set check, nothing added.**
  > **A GUARD PROPOSAL MUST STATE WHETHER THE GUARD ALREADY EXISTS, AND THE ANSWER MUST COME FROM
  > THE FILE, NOT FROM THE PROPOSER'S MODEL OF THE FILE.**

  This is rule 16's shape — an assertion produced without the artifact open — with an aggravating
  factor the earlier instances did not have: **it carried coordinator authority, so deference would
  have shipped it.** The proposer's own account is the sharpest version, and it was volunteered:
  *I proposed a guard against a state that was already covered, in a file I had not read, in the
  message where I corrected someone else for reasoning against the wrong tree.* Same message, both
  directions. The generalisation the lead drew from it — **articulating a principle discharges the
  felt obligation to apply it** — now has seven or eight instances across six agents, and the only
  defence that has worked all day is someone else looking.
- **The observable to act on either way:** `0 ok` lines and `0/106` with **four scripts at exit 1**
  is the real signature of the toolless run, and it is what `run-all.sh:114` swallows.
- **The trailing colon in the `reject()` matcher is an ANCHOR, not punctuation** — without it `-c`
  matches `-config`, `-g` matches `-grove` and `-p` matches `-profile`, all three live in
  `$neverPassed`, which converts a fail-closed red into a **false green**: the one direction the
  entire repair exists to prevent.
- **🔴 `render-guards.sh` emits 2 vacuous greens with no `helm`, and the way that went unnoticed is
  the finding.** The three-row degrades-safely table everyone had been reasoning from **omitted
  that script**, so *"46 assertions of the good kind"* was an **assumption, not a number** — rule
  31's missing denominator inside the very measurement that established the family. Find which 2
  before Commit A. **A fourth author's code failing the rule the first three established is now the
  expected outcome rather than a surprise.**
- **Commit A, as ruled:** the three counting steps **plus the tool-presence guard atomically**;
  `reject()` reason-matching and the empty-`listing` guard; the three stale contract comments;
  MM7/MM8; the whole-line MM re-run; and `verify-failopen.sh` with its named exception. **Gate:
  the pre-registered ten-point oracle, run independently by both reviewers, raw outputs exchanged
  rather than verdicts** (rule 29).
- **🔴 THE FOUR NUMERIC `tests/` DELTAS ARE PHASE 1's, NOT PHASE 0's — and Phase 0 CANNOT
  represent them.** Landing them here turns P0's harness red against the chart approved at
  `60b2912`, on the commit whose entire job is to make the harness trustworthy: P0's chart renders
  **5** documents and packages **14** files, and the deltas are `7` and `22` and a raised
  `EXPECTED_TOTAL`. Worse than a wrong number — `hub.baseUrl` is not in P0's schema and `hub` is
  `additionalProperties: false`, so delta 1 makes **all 106 fail at P0's head. The numbers are
  branch-dependent; the harness logic is not, and that is the line the split follows.** Phase 0
  takes the logic and changes no constants; Phase 1 rebases onto the final constants after
  Commit A lands.
- **🔴 A FIFTH HARNESS SCRIPT — the justification-prose check — IS PHASE 0's TO BUILD, NOT PHASE 6's
  (rule 28), and it is COMMIT B, gating PHASE 1's approval rather than Phase 0's.** It shares
  Commit B with the `kubeconform` gate (rule 23's extension), in either order. *A control must
  precede the artifact it governs or it becomes a migration.* Nine
  phases write justification prose; a Phase 6 control fires on nine phases' worth of accumulated
  violations at the moment it is under most pressure to be quiet. **Phase 6 wires it and adds
  nothing.** The design is fixed and is rule 21 applied — do **not** try to detect claims in free
  prose, which is unbounded. **And do not build the presence test: it is superseded by the ratio.**
  *Per enumerated region, commit TWO numbers — the count of non-blank comment lines and the count
  of grounding tokens (`file:line` or `Phase N`) — both failing on inequality in either direction*,
  because satisfaction per block is append-friendly and **one citation would otherwise immunise a
  block permanently, in the exact direction the regions are designed to grow.** The script prints
  its own limitation **on the same line as the verdict** — *counts grounding tokens; does not
  verify that a token grounds any particular claim* — and inherits all four limbs (tool-presence
  arm, independent assertion count, exit 2 for nothing-was-analysed, MM mutations asserting the
  whole output line). It must not commit the defect it mechanises.
- **The `kubeconform` gate is Commit B's other half, and it is rule 23's own family left open.**
  `grep -rn kubeconform tests/*.sh` returns **nothing**: 106 committed assertions and not one of
  them is `kubeconform`, while `5 valid / 0 invalid / 0 errors / 0 skipped` has been quoted by
  three agents including inside the verdict that approved the chart. **It exists only in shell
  histories and in messages**, and the harness is *correctly* green without it. Found by varying
  the apparatus one notch past `helm`. Not a P0 blocker.
- **Named gap, not papered over:** between now and Phase 6 there is **no per-commit enforcement,
  only per-phase.** Every phase brief makes the harness run a gate. An unnamed gap is
  indistinguishable from coverage.
- **P0 closes as: chart APPROVED at `60b2912`** (axis d, 0 Critical, 0 Required, 1 Optional),
  **plus Commit A — `tests/` only — gated on the pre-registered ten-point oracle run independently
  by both reviewers**, and the phase-complete signal is held until both. Commit B (the fifth script
  and the `kubeconform` gate) is Phase 0's to write but gates **Phase 1's** approval, so it does not
  hold P0. **There is no `VALIDATION.md` deletion** — `gd-p0-rev-3`
  withdrew that Required after finding `:157` and `:194` are siblings under `:152 Relocated
  per-phase checks`.

**Phase 1 — harness constants and `.helmignore`, added in revision 8.** Read §13.1 in full first.
- **The four numeric `tests/` deltas are yours and they land AFTER Phase 0's Commit A**, rebased
  onto the final constants rather than racing them. They are facts about **P1's** chart — `7`
  rendered documents, `22` packaged files, a raised `EXPECTED_TOTAL` — and P0's chart renders 5 and
  packages 14. Post the constants the moment Commit A lands.
- **`.helmignore` gaining `golden/` and `hack/` is Phase 1's to land, with two conditions from
  `gd-p0-rev-2`:** `chart-integrity.sh` must **assert** the exclusions, and the empty-`listing`
  guard becomes load-bearing rather than incidental.
- **🔴 Required condition on that change, not advice:** **`.helmignore` applies when Helm LOADS a
  chart directory, not only when it packages one**, so an over-broad pattern silently shrinks every
  `template`, `lint` and `package` at once — and **`hack/` is a bare directory name matching at any
  depth.** This is a live mutation in rev-2's suite (M1) and it was **green on every other gate**.
- **A finding from this phase that is recorded rather than merely fixed:** `PATH=/usr/bin:/bin bash
  hack/verify.sh` printed **5 `ok` lines from five empty files**, against the exact mechanical
  assertion the phase brief names as its acceptance criterion. **This is the empty-subject family
  in a third agent's independent code, written today, after the family was named** — which is
  `gke-deploy-lead`'s point made empirically: *it is a mode of writing, not a backlog.* §13.1
  carries the formulation this produced.
- **`printf "%q"` against a nil renders `%!q(<nil>)` while the asserted wording stays intact.** The
  fix is one line in the negative-test helper, not six fixes at six sites (§17.1 rule 14). Note
  that `mode:` with nothing after it is **null, not absent**, so `dig`'s default never applies —
  the chart prints `null` for that and a quoted string otherwise, which is distinguishable from
  `""`, what a key the operator never wrote produces.

**🔒 PRE-REGISTRATIONS, RECORDED 2026-08-17, BEFORE THE EVENTS THAT WOULD TRIGGER THEM.** Both are
written down at a date that precedes their trigger **so the criterion cannot be shaped by the
findings that fire it**. That is the whole value; a stopping rule chosen after seeing the round it
would stop is not a stopping rule.

1. **The P1 prose stopping rule** (`gd-em`, carve-out granted by `gke-deploy-lead`):

   > **If the P1 prose commit produces a second round of findings in its own new text, that is the
   > `#1074` shape confirmed in a second corpus, and prose edits STOP SUBTREE-WIDE rather than run a
   > third round.**

   The shape being tested is a prose edit whose own new text is the next round's finding source —
   i.e. editing prose to fix prose defects generates prose defects at a rate that does not decay.
   One corpus is an anecdote; the second corpus is the result. **The rule fires on the existence of
   a second round, not on the severity of what it finds**, because severity is the judgement the
   pre-registration exists to remove.

2. **The channel-coverage prediction** (`gd-doc`, from §17.1 rule 40):

   > **Adding Filestore-channel and Cloud SQL-channel terms to the subject vocabulary takes its
   > recall on the `60b2912` pre-fix denominator from 11/16 to 16/16, because all five of its misses
   > are claims about those two channels and it is blind to an uncovered channel completely rather
   > than partially.**

   ⚠ **This must NOT be tested by extending the vocabulary and re-running against that denominator.**
   Fitting a vocabulary to the set it is scored on destroys the measurement — it would convert the
   one instrument-independent denominator in this project into a training set. **It is testable only
   out-of-sample: on the phase that actually lands one of those channels, against a denominator that
   does not exist yet.** If the phase lands and recall does not move as stated, the channel-coverage
   account of subject keying is wrong and the registry entry is retracted, not amended.

**Phase 2 — Cloud SQL.** Read **§7.0 first**, then §7, §4.1, §4.4, items 28 and 34.
- **§7.0 holds `gke-deploy-lead`'s HA preconditions BY REFERENCE** — the brief must include
  `briefs/phase2-ha-preconditions.md` **by path**. Do not paste it; the `[VERIFIED]` /
  `[MUST VERIFY]` markers are the content and do not survive summarisation.
- **Phase 2 *is* the HA switch, not preparation for it.** The postgres branch of
  `isHADeployment` alone makes every HA guard live, at `replicaCount: 1`, on the first pod —
  and **the diff will read as "add a `--db` flag"**. Say so in the PR description.
- **Do not add a migration Job.** Advisory lock `0x5C100008` already serialises `AutoMigrate`
  (§7.0(b)). Recorded as a non-instance because it is the obvious first instinct.
- **Blocking [MUST VERIFY]:** LISTEN/NOTIFY over the Cloud SQL Auth Proxy. Answer it by
  connecting. If it fails the pod does not degrade, it does not start.
- **Say which channel delivers the database driver** — env survives a settings-load failure,
  a settings key does not (§7.0, inherited premise).
- The init container is **deleted from the design, not reduced** (revision 7). Any brief clause
  giving `settings-init` a residual job is void.
- **The input is weaker than what exists.** Phase 1 already narrowed item 28 and shipped
  fixtures. The instruction is *run Phase 1's fixture before writing the proxy*, not *narrow the
  assertion*. A brief repeating the old framing sends a developer to redo finished work.
- Reuse `scion-hub.nonRootSecurityContext` for the proxy.
- **Phase 2 spends the loopback gate, and the disposition changed in revision 8.** It is the
  first phase to put a second process inside the pod's network namespace, but the consequence
  is now a **trip-wire on §4.1's container table** rather than a Phase 2 note, because every
  later sidecar spends the same gate and none of their authors will read §7. Phase 2 owes
  keeping `requireWorkstation` shut and a test that fails if it stops holding; it does **not**
  owe re-hardening `assertLoopback` (rule 10).
- Naming trap (instance 7): the proxy **is** an `initContainers` entry and **is not** an init
  container. State it in the brief; a developer grepping `initContainers` will otherwise
  conclude §4.1 and the chart disagree.

**Phase 3 — Secrets.** Read §6, §5.2, items 4, 9, 31.
- **Substantively unchanged by revisions 7–8**, which is the useful answer. `auth.acknowledge
  OAuthUnlanded` (revision 6) stands.
- One method change reaches it: Phase 3 has more deliberately-absent subjects than any other
  phase ("no secret material in `args`, ConfigMaps or annotations"; "the chart never generates a
  random session secret"), so **rule 5 applies throughout, including its new first question.**
- `hub.extraEnv` has **four** guards and the fourth is on the **value** axis.

**Phase 4 — Filestore.** Read §8, §4.4, §14.1, items 33 and 35.
- **Two new acceptance items, both Phase 4's**: item 33 (a read-only state directory degrades
  *silently*) and item 35 (an unfound settings file starts the hub on defaults, where the §6.5
  HA preflight never runs).
- The **standing mount question** (below) is most acute here, because Phase 4 is the mounting
  phase.
- `fsGroup` re-derivation and `README.md` ownership are revision 6 and unchanged.
- **Inherited from Phase 0, routed with its MEASUREMENT rather than its conclusion:
  `VALIDATION.md:184-185`.** Phase 0 declined to delete it and gave it a named owner instead of a
  deferral. The class is *a removal justified on "a correct copy exists elsewhere"*, which
  requires external checking — and here **the external checker was the party that had just
  retracted its own reading of that same file**, so the check was not independent (rule 15).
  `:186-188` already routes a reader to the precise version, so **the cost of keeping it is
  drift; the cost of removing it wrongly is a lost criterion.** The measurement, which is what
  makes this checkable rather than a judgement call: **`:184-185` names `runAsUser` AND
  `runAsGroup`. If `:210` names only the uid, deleting `:184-185` silently drops the gid
  criterion — and the diff looks identical either way.** Phase 4 quotes `:210` verbatim, confirms
  both limbs, and only then relocates or deletes.

**Phase 5a — Ingress and the IAP auth mode.** Read §10, §9.2, **§5.4 in full**, items 10a, 10b,
10c, 34. *This phase changed more than any other.*
- **5a owns item 10c**, the alias enumeration — assigned by phase number under rule 8.
- **Item 10b is a live defect, not a future risk**, and 5a is the phase whose developer will want
  the key. The only base-URL channel is `hub.baseUrl` → `SCION_SERVER_BASE_URL`.
- **The debugging advice this phase will reach for is wrong**: `--debug` is silent on the
  resolver that fails and verbose on the one that works (§5.4).
- Item 34: anything 5a terminates in-pod satisfies the loopback gate.

**Phase 6 — CI.** Read §13, §13.1, **§17.1 rules 1–33**, §18 in full.
- **🔴 PHASE 6's DELIVERABLE IS NOT "WIRE THE CHECKS". IT IS A FOUR-PART CONDITION, AND EACH PART
  IS INVISIBLE TO THE ONE BELOW IT:** the check **exists and fails closed** → it is **wired into
  an entry point whose count is committed** → it **produced an observed check run on a PR whose
  base is not `main`** → it **fails closed when its tooling is missing**. `ci.yml:20-21`
  (`89bd1c0`) is `pull_request: branches: [ main ]`, and observed execution confirms the
  consequence: #1093 and #1095 (base `main`) have 2 check runs each; **#1096, based on
  `scion/gke-chart-p0`, has 0.** Nine of ten phases are stacked, so a correctly wired suite
  delivers **zero enforcement on the phases it exists to protect**. The acceptance evidence is a
  **link to a real check run**, not a job in a YAML file — and it covers the meta-check too, or
  the meta-check is the next unwired script. See rule 22's second condition.
- **✅ The trigger limb is solved and the form is committed, so do not re-derive it.**
  `gd-ci-probe-2` proved by observed execution that a bare **`on: pull_request` with no
  `branches:` key** fires on a non-`main` base — PR #1101, base `scion/ci-probe2-base`, check run
  `probe` SUCCESS, re-verified from the API by `gd-doc`. The full workflow is quoted in rule 22.
  **The property to preserve is what is ABSENT**: no `branches:` key and **nothing fork-specific**
  — no `scion/gke-chart-*` patterns, which would ride upstream in a compare diff. This costs the
  deletion of two lines, not the addition of a pattern list.
- **The justification-prose check is NOT built here — Phase 6 WIRES IT AND ADDS NOTHING**
  (rule 28). It is Phase 0's Commit B, because a control landing after nine phases of prose is a
  migration, not a control. If it arrives here unbuilt, that is a finding against Phase 0, not a
  Phase 6 deliverable to absorb. **Wire the two-number form** — non-blank comment lines and
  grounding tokens per region, both on inequality either way — **and do not "simplify" it back to
  a presence test**; the presence test was designed, holed by its author's reviewer, and replaced,
  and the hole is in the direction the corpus grows.
- **The `kubeconform` gate is likewise Phase 0's (Commit B) and not a Phase 6 addition.** Phase 6's
  job is to assert it produced a check run, per the four-part condition. **Before quoting any gate
  in this phase's acceptance evidence, confirm it is committed** — three agents quoted
  `5 valid / 0 invalid` for a gate that was in nobody's `tests/` (rule 23).
- **The fourth limb is the one this phase will skip, because it looks like CI-runner hygiene.**
  A `helm` install step that silently fails is a normal Tuesday, and a suite that then reports a
  broken chart as green on the PR page is **worse than no CI, because no CI is at least known to
  be absent**. Rules 24 and 25 are why: assert the **whole output line** of every mutation, not
  the headline field, and remember that **a suite cannot mutate away the tooling its author has
  installed** — so the missing-tool case needs an explicit mutation (MM7) rather than care.
- **Standing prohibition: no `paths:` filter on any workflow that runs a chart check.** `ci.yml`
  has none today; `docs.yml` shows the pattern (`paths: ['docs-site/**', …]` *plus*
  `branches: [ main ]`, doubly conditional). Adding `paths: ['deploy/helm/**']` is the obvious
  way to keep a chart suite fast and it would **silently disable every chart check on the PRs
  that change the hub source those guards cite** — the very `file:line` outside the chart that
  rule 10 makes a refusal encode. If one is ever added, the reason is written beside it and
  names what it stops checking.
- **New deliverable, fully specified: the citation-integrity check** (Phase 6, "citation
  integrity"). It is a **mechanism, not a sweep** — a human re-reading the document is
  disqualified by rule 9. The spec carries an **exact committed count** measured against this
  revision (30 references, 12 distinct, 39 items defined), the three corpus properties a naive
  regex gets wrong, a self-exclusion clause, two fixtures, and a scope bound. It is written to
  be implemented without asking the author.
- **The count is exact, not a floor, and the reason generalises to every check this phase
  builds:** a floor is an absolute recorded once against a corpus that only grows, so its
  reassurance scales with the corpus while its coverage stays pinned. That is rule 9 wearing a
  number. **This is now a PROJECT-WIDE extension of rule 9, not a Phase 6 note**: where a check
  counts anything, commit the number and fail on inequality in either direction.
- **Rule 19 is Phase 6's organising question, and it is not "how many checks does the suite
  run".** Volume of checks is not independence of checks: four checks sharing one premise are
  one check. Phase 6's suite is overwhelmingly axis a/b/c — *does the artifact do what it says*
  — and every one of those shares the premise that the artifact's account of the world is true.
  **`gd-p0-rev-3`'s single axis-d pass found five Required findings in an already-reviewed
  file.** So the suite states, per check, which premise it rests on, and Phase 6 owes at least
  one axis-d mechanism rather than another dozen a/b/c ones.
- **Rule 21 shapes how guards are written here:** prefer a positive assertion about what an
  artifact **is** — byte-equality against a fixture, an exact count, a resolved reference — over
  a negative assertion about a phrase it must not contain, which any rewording defeats. Where
  only a negative guard is possible, label what it does not cover **in the guard**.
- **Extend the citation check to `§17.1 rule N`.** It currently resolves `§18 item N` only, which
  leaves rule 20 as a discipline rather than a mechanism — and a remembered rule number has
  already been wrong once. Recorded as an explicit Phase 6 deliverable rather than an
  assumption, per `gd-em`: **an unmechanised rule recorded as mechanised is worse than no rule,
  because it consumes the attention that would have built the mechanism.**
- **🔴 The suite needs an entry point that counts the CHECKS (rule 22).** Phase 0 has three
  harness scripts each individually fail-closed and nothing asserting all three ran. One entry
  point, enumerating the scripts, asserting the executed count against a committed number, so a
  fourth script fails until someone bumps the number in a diff. **Verified live precedent, not a
  hypothetical:** `hack/check-authz-guards.sh` is wired nowhere in `Makefile` or `.github/` on
  `origin/main` or `89bd1c0`, despite its own design doc listing both files — and it is the most
  rule-9-compliant script in the repository. The entry point's own wiring is a reviewed line and
  is **not** solved by a further meta-check; one ring is closable, two are a regress.
- `helm template | kubeconform` **exiting 0 on a failed render** is this phase's flagship case —
  and **rule 9 names its family**, which now has five members from five different authors in one
  day. Phase 6 owns the family, not just the instance: every check reports how many things it
  examined and fails on zero.
- **Rule 5 upgrades Phase 6's existing bullet**: the near-miss half, and the prior question of
  whether the subject is reachable at all.
- **No guard may depend on `lookup`.** It returns empty under `helm template` and `--dry-run`, so
  such a guard is absent in exactly the tooling Phase 6 runs — the suite would be evaluating a
  chart it cannot see (rule 9).
- Phase 6 asserts Phase 0's **two directions** if Phase 0's round does not land them.
- §17.1's rules cite §18 items by number, so the rules are **in the citation check's corpus**,
  not in the human reconciliation sweep. That was an open question and it was answered
  *neither*: a sweep over rules is still a person re-reading a document.
- **Phase 6 is where a check that cannot fail is most likely to be built**, because its entire
  content is checks — the ⚠ derivation warning in Phase 0 is the general form.

**Carried by every remaining phase:**
1. **The standing mount question:** *does anything this phase mounts or writes make `os.Stat`
   succeed on a path whose absence the hub uses as a signal?* (§5.2, `InitGlobal`.)
2. **Its second instance, which is why it is standing rather than a Phase 1 note:** `agents/`
   readers are reachable because **the chart turns on a second server inside the hub**
   (`--enable-runtime-broker`). Same class — the chart changing the conditions the reasoning
   assumes — and it is invisible from both sides.
3. **Rule 8:** a "not reachable" verdict names the phase that would open it, or it is not a
   verdict.

### Phase 0 — Chart skeleton and core workload *(unblocked)*
`Chart.yaml`, `_helpers.tpl`, `values.yaml`, a minimal `values.schema.json`,
`serviceaccount.yaml`, RBAC, `service.yaml`, `deployment.yaml` (hub container only,
sqlite, local storage, no ingress), probes, `NOTES.txt`, `ci/values-minimal.yaml`.

**Acceptance**
- `helm lint` clean; `helm template -f ci/values-minimal.yaml` passes `kubeconform -strict`.
- Every probe targets the literal `/readyz` — **root path, no `/api/v1/` prefix** (§9.2);
  `healthz` appears nowhere; no prefixed `readyz` variant appears anywhere.
- `readinessProbe.timeoutSeconds` is set explicitly and is > 2.
- A `startupProbe` exists with `failureThreshold ≥ 60`; no liveness probe by default.
- `replicaCount` defaults to 1; `updateStrategy` resolves to `Recreate` at 1 replica.
- RBAC includes `persistentvolumeclaims: create,get,list,delete`.
- No secret-looking value appears in `args`.
- `args` contain `--hosted` and `--host 0.0.0.0`; **no value can disable hosted mode.**
  `hub.args` is **append-only** — it never replaces the mandatory args — and a reserved-flag
  guard rejects reserved flags by name, in **four groups, one per reason**. **Membership is
  defined in `_helpers.tpl` (`scion-hub.hubArgs`) and is deliberately not duplicated here** —
  the design owns the *why*, the chart owns the *what*, because only the chart's copy is
  executable (§17.1, rule 4). This document enumerated the members once and the copy went stale
  within hours (§17.1 instance 8). The four reasons, which a reviewer *can* check the chart
  against:
  1. **The chart already sets this flag itself** — a second value would contradict the rendered
     manifest. **This is the only group that CAN be verified against rendered `args`. It does not
     currently satisfy that property, and restoring it is Phase 0 work — see the two directions
     below.**
  2. **Nothing may ever pass this — not the operator, not a future phase.** Never appears in
     rendered `args`, forever, by design.
  3. **The chart delivers this setting through a channel other than argv**, so passing it on
     argv creates a second source that silently wins. **Name the channel per entry** — it is not
     the same channel for every member, and the precedence differs.
  4. **This flag weakens authentication or places credential material where it can be read.**

  **Group 1 must be asserted in BOTH directions, and it satisfies neither today.** Revision 7
  first wrote group 1's checkability as a property to protect. That was wrong: it is a property
  to **restore**. Verified against `origin/scion/gke-chart-p0` at `cb183de` — the chart renders
  `--foreground --hosted --enable-hub --enable-runtime-broker --enable-web --web-port <n>
  --host 0.0.0.0 --auto-provide --global`, and group 1 is `hosted production host web-port port`:
  - **Direction A — every rendered flag appears in group 1.** Catches a flag the chart passes
    that nobody reserved. **Six flags fail this today**: `foreground`, `enable-hub`,
    `enable-runtime-broker`, `enable-web`, `auto-provide`, `global`. This is not a free assertion
    to add — it is a six-member gap, and the members matter: all six are `BoolVar`, pflag takes
    the **last** value, and the guard already splits on `=` before matching, so
    `hub.args: ["--enable-web=false"]` is caught the moment the flag is listed and is accepted
    today. `--foreground=false` is the sharpest: the hub daemonises, PID 1 exits, and the
    container restart-loops with a manifest that still reads `--foreground`.
  - **Direction B — every group-1 member appears in the rendered flags.** Catches a member the
    chart *claims* to set and does not. **Two members fail this today**: `production` and `port`.

  **Direction B's remedy is not deletion, and the group's own comment currently says it is.**
  This is the trap in its most concrete form, because both violating members have a *different*
  correct fix and neither is the one the comment names:
  - **`production`** is a deprecated alias binding the *same variable* as `--hosted`
    (`cmd/server.go:235`, `BoolVar(&hostedMode, ...)`). The chart must not emit it (two bullets
    below) and must not drop it: `--production=false` sets `hostedMode = false` while the
    manifest still reads `--hosted`. Correct fix: **move it to a reason of its own** — reserved
    because it aliases a rendered flag.
  - **`port`** (`cmd/server.go:241`) is not set by the chart and is *inert* here — its own help
    text says "ignored when `--enable-web` is set", and the chart sets `--enable-web`. It is
    reserved because an operator passing it would believe they had changed a port and would be
    wrong. Correct fix: **move it too** — and note that this reason is closed by a
    **configuration** rather than by code (rule 8): a phase that stops rendering `--enable-web`
    makes `--port` live again.

  So group 1 today contains the exact tidying-deletion hazard that group 2 was split out to
  prevent, **under a comment arguing for the removal**, and deleting `production` is one step
  from re-opening `/api/v1/system/init` (§18 item 34). The document held both halves of this
  contradiction at once: the `--production` bullet two bullets below has been stating the
  refuting fact for several revisions. Neither half was wrong; nothing read them together.

  **The four-reason split is what makes direction B expressible at all** (`gd-em`). Direction B
  is meaningful **only** for group 1 — groups 2, 3 and 4 are reserved *precisely because* the
  chart does not render them, so "member ⇒ rendered" is false for them by design. A flat list
  could not state the assertion, because the flat list has no subset over which it is true. **The
  split did not only document the reasons; it made one of them machine-checkable.** That is the
  strongest available argument for grouping, and it is worth more than the readability one.

  Both directions are the same few lines of template, and **B must be implemented as the thing
  that makes group 1's comment true by construction**: a membership rule enforced by a comment is
  enforced by whoever read the comment most recently.

  **⚠ DO NOT derive group 1 from the rendered `$args` list.** It is the obvious simplification,
  it removes the duplication both directions exist to police, and it is wrong — **a set defined
  as the thing the check compares it against makes both containments tautologies, producing a
  check that cannot fail.** That is the single failure mode this project has spent the day
  chasing, and this would be the most elegant instance of it yet. There is a secondary cost too
  (`gd-p0-dev`): derivation silently expands the *operator-facing* reserved set every time a
  maintainer adds a flag to the render. **Distinguish it from the sweep's own preference order** —
  *make the invariant unnecessary, then assert it, then bound its factors* — because the two look
  identical in a diff and are opposites. Deriving `service.targetPort` from `service.port`
  removes the coupling and so makes the **invariant** unnecessary; deriving one side of this
  comparison from the other makes the **check** unnecessary while leaving the invariant exactly
  as breachable as before.

  Three properties of the grouping matter more than the membership:
  - **Each group fails with its own message**, so the reason reaches the person arguing with the
    guard and not only the person reading the file. The comment is correct; **the message is the
    interface**, and it is the only half that travels to the point of failure.
  - **Groups 2 and 3 are unverifiable against rendered `args` by construction** — the rendered
    args are exactly where their members must *not* appear. For those the failure message is the
    only available assertion. **Do not remove an entry because the chart does not emit it:** that
    is the point of the entry, not evidence of a mistake, and the removal looks like tidying.
  - **Do not merge groups because they share that property.** Groups 2 and 3 share a failure mode
    for unrelated reasons; merging by failure mode loses both reasons and, worse, would pull
    members into group 1 and destroy the one group that can be made checkable at all — the only
    group over which direction B is even expressible.

  A later phase may change a flag's delivery channel. When it does it **moves** the entry between
  groups rather than adding a second emitter beside the existing one.

  **This list is a security control, not tidiness, and here is the argument in one sentence**
  (`gd-p1-dev`): *a flag that outranks a value the chart renders makes the chart's own assertions
  unfalsifiable, because the manifest keeps reporting the operator's intent.* `--config` and
  `--base-url` are two instances of one pattern rather than two findings. Neither is visible from
  the output side: with either appended, every golden file, every rendered-env assertion and the
  whole settings document stay byte-identical and correct while the running hub uses something
  else. **No assertion downstream of the render can catch this class** — the reserved list is the
  only place it can be caught, which is why removing an entry is not a cosmetic change.
- **`--production` is not emitted** — a deprecated alias of `--hosted`
  (`cmd/server.go:235-236`); emitting both buys nothing and prints a deprecation warning on
  every boot (§4.1).
- `rbac.agentNamespace` and `runtime.namespace` are the same namespace: `runtime.namespace` is
  canonical, both are accepted, and **disagreement fails the render** (§19).
- `hub.hubId` is schema-required; omitting it fails the render with a message naming the
  value; the rendered `settings.yaml` contains no `randAlphaNum`/`uuidv4`/`.Release.Revision`
  in the hub-ID position.

### Phase 1 — `settings.yaml` rendering *(unblocked)*
`secret-settings.yaml`, `scion-home` emptyDir, `configmap-env.yaml`, `config.existingSecret`,
`config.extra` deep merge, golden files. **No init container** — see §4.1 for why none is
needed, and do not add one to match an older copy of this list.

**Acceptance**
- Rendered file parses as YAML and matches the §5.3 layout, including
  `schema_version: "1"`, `active_profile`, top-level `profiles:` and `runtimes:`.
- `server.mode: hosted`, `server.hub.hub_id`, `server.storage.provider: gcs` and
  `server.storage.bucket` are all present — the five Block-1 preflight requirements of
  §6.5 render in **both** auth modes.
- `storage.bucket` is schema-required whenever `database.driver: postgres`.
- The six nested keys (`notification_channels`, `message_broker`, `native_chat`,
  `plugins`, `scheduler`, `github_app`) render under `server:`, never top level.
- **No `SCION_SERVER_DATABASE_*` or `SCION_SERVER_OIDC_*` env var is emitted** — asserted.
- `HOME`, `KUBECONFIG: ""`, `SCION_SERVER_BASE_URL` and `SCION_REQUIRE_STABLE_SIGNING_KEY`
  are present; `SCION_SERVER_BASE_URL` is schema-required and **`https://`-prefixed
  unconditionally** (§5.4).
- **`settings.yaml` is a `subPath` Secret mount at `$HOME/.scion/settings.yaml` with
  `defaultMode: 0444`, over the `scion-home` `emptyDir` — the file is read-only and the
  directory is not (§5.2).** **No init container renders in any permutation** — the `emptyDir`
  is the prepared directory (§4.1). Superseded in revision 5: the earlier "mount at
  `/etc/scion`, init container copies and `chmod 0600`s" is **wrong twice over** — the copy is
  the #1091 defect shape, and `0600` on a root-owned Secret projection is unreadable by uid
  1000. Superseded again in revision 7: the reduced "init container prepares the directory"
  role went too, and the phase is correct as built in ptone/scion#1096.
- No manifest renders `defaultMode: 0600`/`0400` on the settings Secret — asserted, with the
  reason in a template comment so it is not "tightened" back (§5.2).
- `config.existingSecret` suppresses the chart's Secret; supplying both it and inline
  secrets fails at template time.
- **`hub.extraEnv` renders into the hub container, with four guards** — three on the **name**
  axis and one on the **value** axis. On the name: it rejects any name beginning
  `SCION_SERVER_DATABASE_` or `SCION_SERVER_OIDC_`; it rejects shadowing the four env vars the
  chart sets itself (`HOME`, `KUBECONFIG`, `SCION_SERVER_BASE_URL`,
  `SCION_REQUIRE_STABLE_SIGNING_KEY`); and it rejects an entry whose **name looks like
  credential material** when the value is a **literal**, because a literal lives in the
  Deployment and is readable by more people than a Secret is. **`secretKeyRef` passes** — the
  guard is about where the value lives, not about the name alone. On the value: `gd-p0-dev`'s
  `scion-hub.assertNoCredential` inspects the **literal value** and catches credentials in URL
  userinfo — `postgres://scion:hunter2@host` under a perfectly innocuous variable name.
  **The fourth guard is a different axis, not a fourth pattern.** Every name-based check shares
  one blind spot: it can only refuse names someone thought to enumerate, and the operator
  chooses the name. A value-based check does not care what the entry is called. Counting the
  guards is therefore the wrong completeness test — ask instead whether both axes are covered,
  because three excellent name checks still pass a password in a variable called `DSN`.
  (Recorded at four in revision 7; it read "three guards" while the fourth already shipped.)
  **Met by Phase 1 (ptone/scion#1096).** Worth recording *how*: this criterion and the
  implementation were derived independently and converged, `HOME` included, with the same
  reason given for `HOME` on both sides — it decides where `$HOME/.scion` resolves and is
  therefore the **second** way to void §5.2 (the first being `--config`). Two independent
  derivations agreeing is worth more than either alone, which is why the agreement is written
  down rather than the criterion simply being ticked.
- **No `fsGroup` appears in any rendered permutation** — true at Phase 1 because
  `workspaceStorage` does not exist yet. **Phase 4 will break this assertion, correctly** — see
  the instruction in Phase 4 to *re-derive* it rather than delete it.
- **`auth.mode: oauth` requires `auth.acknowledgeOAuthUnlanded: true`** while the client
  secret is undelivered (§14.2). **Phase 3 deletes this key** — see Phase 3's deliverable and
  §18 item 31.
- **`server.base_url` / `SCION_SERVER_BASE_URL` is `https://`-prefixed unconditionally** —
  the rule, stated in §5.4; the record of how it got there is below.

**Where the unconditional `https://` rule came from (revision 6) — a record, not a competing
statement of the rule.** §5.4 conditioned it on `ingress.enabled`; Phase 1 implemented it
unconditionally and gd-em accepted that, so **§5.4 now states the unconditional rule and this
paragraph is history**. The reasons, worth keeping because they are what a revisit has to
overturn: ingress values do not exist yet at Phase 1; there is no plaintext path anywhere in
this chart; and the session cookie's `Secure` attribute is a **literal `HasPrefix` on this
value**, so a non-`https` base URL yields a cookie without `Secure` — **a security regression
that presents as nothing at all**. Phase 5a may revisit *if a real plaintext path ever appears*;
until one does, the stricter rule costs nothing.

**Why `hub.extraEnv` exists, because the obvious answer is wrong and will get it deleted.** It
is **not** an escape hatch for operator convenience. The chart must assert that **no
`SCION_SERVER_DATABASE_*` or `SCION_SERVER_OIDC_*` env var is ever emitted** (§13.1, §18 item
3) — and that assertion is **vacuous if nothing in the chart can emit one**. It passes trivially
on a chart with no env-var path at all, and it would keep passing after some later phase quietly
adds a path that can. `hub.extraEnv` is what gives the negative assertion something real to
catch: it is the **positive twin** §13.1 requires. **The escape hatch exists to make the guard
testable.** A later reader who sees only the hatch will judge it unnecessary surface and remove
it — taking the test with it, and leaving behind an assertion that passes because there is
nothing left to fail it.

### Phase 2 — Cloud SQL *(unblocked; Q4 answered — **verify IAM first**)*

**Do this first, before writing the rest of the phase.** Stand up the proxy with
`--auto-iam-authn` and confirm the hub connects on a **passwordless** DSN. The result sets
`database.auth`'s default: IAM if it works, `password` if it does not — and if it does
not, write the failure up as a finding rather than silently defaulting around it (§7.2).
Both modes ship either way; only the default is contingent.

Proxy native sidecar, Workload Identity annotation, DSN construction, pool settings,
`NOTES.txt` IAM commands, `ci/values-cloudsql.yaml`.

**Acceptance**
- The proxy is an `initContainers` entry with `restartPolicy: Always` when
  `cloudsql.nativeSidecar`; a plain sidecar otherwise, with the crash-loop warning in
  `NOTES.txt`.
- **Phase 1's "no init container" assertion stays green through this phase, and must stay
  present** (§18 item 28). Nothing to change: Phase 1 pre-emptively narrowed it to *every
  `initContainers` entry carries `restartPolicy: Always`*, which the proxy satisfies. A fixture
  in Phase 1's suite already proves the rule does not fire on a native sidecar — **run it before
  writing the proxy**, so a green result is a verified expectation rather than a relief. If it
  ever does go red here, the answer is to narrow further, never to delete: deleting reopens the
  #1091 copy shape (§4.1), and it would *look* like the correct response to a failing test.
- Proxy image pinned by digest; startup and readiness probes on the health-check port.
- `--set database.maxOpenConns=1` **fails schema validation**; 2 succeeds.
- Under `database.auth: iam` the DSN contains no password and `--auto-iam-authn` is
  present; under `password` the credential is in the settings Secret and in no `args`.
- **The IAM verification above is recorded** — its outcome, and the resulting default,
  written into §7.2. `values.yaml`'s `database.auth` default matches that outcome.
- `NOTES.txt` prints the exact `gcloud` binding commands with values substituted, and the
  connection-budget formula.
- Manual smoke: hub reaches Postgres, `AutoMigrate` completes, `/readyz` is 200.
  Record the observed secondary-pool overhead `S`.

### Phase 3 — Secrets *(unblocked; may run parallel with Phase 2)*
`secret-session.yaml`, `auth.existingSecret`, `envFrom`, the argv guard, **and the removal of
`auth.acknowledgeOAuthUnlanded`**.

**Deliverable: delete `auth.acknowledgeOAuthUnlanded` (revision 6).** Phase 1 renders
oauth-mode configuration, but oauth cannot start until *this* phase delivers the client
secret, so Phase 1 added a schema-required acknowledgement rather than let an operator select
a mode that silently cannot work. That is the right call for the window it covers, and the
window closes here. **The key is temporary by construction, and a removal that lives only in a
comment does not happen** — so it is a deliverable with a criterion (§18 item 31), not a note.
Otherwise the chart ships a permanent required key guarding a gap we have already closed.

**Acceptance**
- **`auth.acknowledgeOAuthUnlanded` is absent from `values.yaml` and from
  `values.schema.json`**, and `auth.mode: oauth` with **no acknowledgement key** now installs
  successfully. Both halves: absence alone could mean the key was renamed.
- Install with neither `auth.sessionSecret` nor `auth.existingSecret` **fails at template
  time** with a message naming the value.
- The chart never generates a random session secret.
- Rendered output contains no secret material in `args`, in any ConfigMap, or in any
  annotation — asserted mechanically over all `ci/values-*.yaml`.
- `SCION_REQUIRE_STABLE_SIGNING_KEY=true` by default.

### Phase 4 — Filestore *(hub side deliverable now; agent side BLOCKED ON #1075)*
`pv-filestore.yaml`, `pvc-workspace.yaml`, the derived mount path, the
`workspace_storage` block, the `acknowledge1075` guard, **and `README.md` (created here)**.

**`README.md` is assigned to Phase 4 (revision 6).** It appears in the §3.2 layout and is
*assumed* by a Phase 4 acceptance criterion below, but no phase produced it. Phase 4 is the
earliest phase with a hard acceptance dependency on it and — unlike Phase 0, which already
shipped `NOTES.txt` and `VALIDATION.md` and is in review — Phase 4 can absorb it without a
rebase. Phase 4 creates the file with the two-step install runbook and the documented-surprise
entries owed so far (§6.5, §8.1, §8.3, §8.5); **later phases append their own**, and Phase 6
carries the criterion that every "documented in `README.md`" claim in this design is actually
in the file.

**Acceptance**
- PV and PVC are `ReadWriteMany` and carry `helm.sh/resource-policy: keep`.
- `volumeMounts[workspace].mountPath` is **derived** from
  `nfs.mountRoot` + `shares[0].id`; a user-supplied mismatch **fails the render**.
- The PV's `metadata.name` equals `settings.yaml`'s `nfs.shares[0].pv_name`.
- `runAsUser`/`runAsGroup` equal `nfs.uid`/`nfs.gid`; a mismatch fails the render.
- **`fsGroup` renders only when `backend: nfs`, and Phase 1's "no `fsGroup` anywhere"
  assertion is RE-DERIVED, not deleted.** Phase 4 is the phase that introduces `fsGroup`
  (§4.4), so it is the phase that breaks that assertion — and the break is **correct and
  expected**. The replacement asserts both halves: `fsGroup` is **absent** when the backend is
  off, and **present with the operator's `nfs.gid`** when it is on. *An assertion deleted
  because a later phase made it fail is how the only check on a field disappears at exactly the
  moment the field starts doing something.*
- **Answer the open question in §4.4: does `fsGroup` do anything for Filestore at all?**
  `kubectl get csidriver filestore.csi.storage.gke.io` on the managed GKE deployment. If the
  driver takes the default `fsGroupPolicy`, `fsGroup` is not applied to the RWX NFS volume — and
  the field would then apply to every volume in the pod **except the one it was added for**.
  Record the observed value and the resulting decision (keep, or remove) in §4.4. Do not
  pre-empt it from the upstream manifest.
- `backend: nfs` without `acknowledge1075: true` fails with a message naming #1075 and
  §14.
- `filestore.existingClaim` suppresses both objects.
- An `hub.extraVolumeMounts` entry whose `mountPath` is under `nfs.mountRoot` and whose
  volume is not the workspace PVC **fails the render** (§8.5); `README.md` and
  `NOTES.txt` carry the "no `emptyDir` under `/mnt`" rule verbatim.
- The GSA binding commands in `NOTES.txt` include `roles/storage.objectAdmin` on the
  bucket (§8.4).
- Manual smoke: the hub mounts the share, `/readyz` is 200, and
  `<mount_root>/<share_id>/hub-projects/` is created and writable by uid 1000.
- `NOTES.txt` warns that shares beyond `shares[0]` are not health-checked, and that the
  cluster default StorageClass must be RWX-capable until #1075 lands.

### Phase 5a — Ingress and the IAP auth mode *(unblocked; verifies Q6)*
`ingress.yaml`, `backendconfig.yaml`, `frontendconfig.yaml`, `managedcertificate.yaml`,
`bootstrap.deferHub`, the `auth.mode` discriminated union in the schema with **only the
`proxy` branch functional**, audience schema, `ci/values-full-ha.yaml`,
`ci/values-bootstrap.yaml`.

**Acceptance**
- `BackendConfig.healthCheck.requestPath` is `/readyz`; `timeoutSec ≥ 3600`;
  connection draining set.
- `Service` carries the NEG annotation and the backend-config annotation.
- No rewrite-class annotation on the Ingress — asserted.
- Schema rejects `/projects/000000000/global/backendServices/0` and **accepts**
  `/projects/123456789/global/backendServices/1` (with a `NOTES.txt` warning).
- `iap.audience` and `oidc.audience` are distinct values with distinct patterns, and
  `NOTES.txt` explains the difference.
- `bootstrap.deferHub: true` renders `replicas: 0` and still renders Service, Ingress,
  BackendConfig and the certificate.
- `NOTES.txt` prints the full two-step runbook with values substituted.
- Live confirmation of **Q6** (answered green in source, still worth observing end to
  end): the GCLB backend reaches `HEALTHY` — the health check is answered 200, not 401 and
  not 404. **A 404 here means a prefixed path slipped through**; see §9.2.
- Live verification: a WebSocket to `/api/v1/agents/{slug}/pty` survives > 120 s, and
  `X-Scion-*` headers arrive unmodified.
- `auth.mode` is a schema-level discriminated union; `mode: proxy` + `provider != iap` is
  rejected; `mode: oauth` requires `auth.acknowledgeOAuthUnlanded: true` and otherwise
  fails with a message naming `ha-lead`'s issue (§14.2).
- `auth.mode` defaults to `proxy` — the mode that actually boots today.

### Phase 5b — The OAuth auth mode *(BLOCKED ON `ha-lead`'s preflight split; needs Q8, Q9)*
The `oauth` branch of the union: `auth.oauth.{issuer,clientId,clientSecret}` rendered into
the settings Secret; IAP keys and the two-step bootstrap suppressed; default flipped to
`oauth`; the `acknowledgeOAuthUnlanded` guard removed. `ci/values-oauth.yaml`.

**Acceptance**
- Key names confirmed against `ha-lead`'s landed issue, not the sketch (Q9).
- With `auth.mode: oauth`, the rendered `settings.yaml` contains **no** `iap.audience`,
  no `transport.mode: iap`, no `oidc_audience`, no `platform_auth_sa`.
- All five Block-1 keys (§6.5) are still rendered — the union changes one subtree only.
- The OAuth client secret is Secret-sourced and appears in **no** `args` — same assertion
  as the session secret.
- **Live: a single-step `helm install` produces a ready hub** with no backend-service ID
  and no `bootstrap.deferHub`.
- `auth.mode` default flipped to `oauth`; the chart version records a breaking change.
- Q8's answer applied: if the IAP variant is out of scope for the main chart, the `proxy`
  branch and its four values are removed in this phase.

### Phase 6 — CI *(unblocked; depends on Phases 0–5 for content)*
`make helm-*` targets, the `helm` workflow job, vendored CRD schemas, golden files.

**Acceptance**
- `make helm-verify` passes locally and in CI over every `ci/values-*.yaml`.
- `kubeconform` runs **without** `--ignore-missing-schemas`.
- Every negative assertion in §13 is implemented and demonstrated to fail on a
  deliberately broken input.
- Actions are SHA-pinned; the job is reachable purely through `make`.
- **Hand-off recorded:** an admin must add `helm` to the branch's required checks. The
  phase is not complete until that request is filed with the lead.
- **Documentation reconciliation (revision 6).** Every place this design says something is
  "documented in `README.md`" or "carried in `NOTES.txt`" is present in the delivered file
  (§6.5, §8.1, §8.3, §8.5, §14.1, §14.2, Phase 4). This is the phase that owns the sweep
  because it is the last one whose content depends on all the others.
- **§3.2 / §17 / §19 reconciliation (revision 6).** Every file in the §3.2 layout is produced
  by some phase, and every key in the §19 appendix is either produced by a phase or recorded
  as a deliberate non-feature. **And every cross-section assumption is checked**: where one
  section's argument depends on what another renders, the two agree (§17.1 — the §4.4/§5.2
  `fsGroup` case is the worked example).

#### Phase 6 deliverable: the citation-integrity check

**This is a mechanism, not a sweep.** A human re-reading `design.md` looking for broken
citations is disqualified by §17.1 rule 9 — its pass condition would be "nobody spotted
one", which is the reading a tired reviewer and an empty run both produce. What follows is
specified to be implemented without asking the author.

**Why it exists.** §17.1 rules 4, 5, 8, 9, 10, 11 and 12 and all of §17.2 cite `§18 item N` by
number, which is what makes them checkable — and is also that many things that can go stale.
**Rule 20 is the un-mechanised half of this and the check does not yet cover it**: rules cite
each other by number too (`§17.1 rule N`), and a remembered rule number has already been wrong
once. Extending this check to `§17.1 rule N` is the obvious next increment and is *not* in
scope here — recorded so the gap is a known one rather than an assumed absence.
The failure has already fired once, on its first day: rule 8's worked example cited "the
`server.hub` allow-list conversion (item 10b)" for about an hour after the allow-list was
abolished, i.e. the rule teaching that ownership must be *concrete* pointed at a deliverable
that no longer existed and named the wrong number. Nobody noticed by reading.

**Subject.** `design.md` only. This check does **not** validate `file:line` cites into the
hub source or the chart — see the scope bound below.

**Self-exclusion, which is not optional.** The check's specification lives inside the file
the check scans, and it necessarily contains example citations — a fixture number, a range,
and the literal syntax. **Exclude this subsection**: everything from the
`#### Phase 6 deliverable: the citation-integrity check` heading to the next `###` heading.
Without the exclusion the check reads its own examples as live citations, fails on the
deliberately-invalid fixture number, and the first fix anyone reaches for is loosening the
matcher. Note the hazard the exclusion creates and keep it small: a broken citation moved
into this block would become invisible. The block is therefore specified to contain **no
live citations** — every `§18 item` inside it is an example, and if that ever stops being
true the exclusion is wrong rather than the citation.

**Reference syntax, which the check also defines.** A citation is
`§18 item <N>` or `§18 items <A>–<B>`, where `<N>` matches `[0-9]+[a-c]?`. Three properties
of the real corpus that a naive implementation gets wrong, all present today:

1. **References wrap across lines.** Two of them currently break between `item` and the
   number. **Normalise whitespace across the whole file before matching** — match on the
   flattened text, not line by line.
2. **Ranges exist and are plural.** `§18 items 24–27` (en dash) must expand to four
   references, not one, and not zero. Accept both `–` and `-`.
3. **Some occurrences are the syntax being DESCRIBED, not used.** The literal string
   `§18 item N` appears **four times outside this subsection** — in rule 11, in rule 12's
   citation note, in rule 20, and in "Why it exists" below — and the count grew from one to
   four in a single day, so do not hard-code it. **Exclude the literal token `N`**; do not
   "fix" it by loosening the number pattern, which would silently swallow future typos of the
   same shape. These are correctly excluded by requiring a digit, which is the point: the
   exclusion is a property of the pattern, not a list of sites (rule 21 — assert what a
   reference **is**, do not enumerate what it is not).

**Definition side.** Parse the item numbers *defined* in §18 — list markers matching
`^\s{0,3}(\d+[a-z]?)\.\s` between the `## 18.` heading and the `## 19` heading. Bounded to
§18 deliberately: §14.1's and §15's numbered lists use the same marker and are not acceptance
items.

**Pass condition — a count, not a silence (rule 9).** The check prints
`citation integrity: resolved <X> of <X> references to §18 (<D> distinct items, <F> items
defined)` and **fails** if:

- any reference does not resolve to a defined item; **or**
- `X` is **zero** — no references found at all, which is what a broken regex, a renamed
  section heading or a moved file produces, and which every absence-of-failure check reports
  as success; **or**
- `X` is **not exactly equal to the count committed in the check itself**, in either direction.

> **⚠ AN EXACT COUNT, NOT A FLOOR — and the difference is rule 9 with a slower fuse
> (`gd-em`).** A floor is an absolute recorded once against a corpus that only grows. This
> document is ~3900 lines and climbing; suppose citations reach 62 and someone deletes thirty.
> **`32 > 30`: green.** The floor then certifies a corpus half of which it never sees, and it
> does so *more confidently every time the document grows* — the check's reassurance scales with
> the corpus while its coverage stays pinned at revision 8. Nor does anyone raise the floor,
> because nothing tells them to: a threshold that ratchets only on a deliberate human edit,
> inside a check whose entire justification is that humans do not reliably re-read, is **rule 9
> wearing a number.**
>
> So commit the exact expected count and fail on any inequality. Adding a citation then fails
> the check, and the fix is a one-line diff bumping the number — **which puts the count in the
> diff**, where a reviewer sees `30 → 32` and can ask whether two were added, or three added
> and one deleted. A floor makes growth invisible; an exact count makes every change to the
> corpus a reviewed line. **The cost is real and is accepted knowingly: the check fails on
> legitimate additions. That cost is the mechanism, not a side effect.** It is the same
> argument as the naive-matcher number below — "25 looks healthy" is not acceptable, and
> "32 clears the floor" is the identical failure with a longer fuse.

**The committed count, and the number to seed the check with — stated with its conditions per
rule 27, because a measurement without them is an assertion:** **30 references, 12 distinct items
(3, 10b, 10c, 24, 25, 26, 27, 28, 31, 32, 34, 35), 39 items defined in §18 (1–36 plus
10a/10b/10c), 0 unresolved — measured 2026-08-17 over `design.md` at revision 8, whitespace
flattened across the whole file, en-dash and hyphen ranges expanded, this subsection excluded,
and the literal token `N` not counted.** If a fresh implementation reports materially fewer than
30 it has a regex bug, not a clean document — **25** is the number you get by handling neither
wrapping nor ranges, and **25 looks perfectly healthy on its own.** That is the whole reason this
specification carries numbers instead of descriptions.

> **⚠ AN UNRECONCILED MOVEMENT IN ONE OF THE CONDITIONS, DISCLOSED RATHER THAN QUIETLY RESTATED
> (rules 31 and 33).** The four substantive numbers — 30 / 12 / 39 / 0 — are **unchanged across
> roughly 900 lines added to this document in revision 8**, re-measured after every editing round.
> **The naive figure moved from 24 to 25 and `gd-doc` cannot say why.** No `§18 item` reference was
> added by those edits, and this scratchpad is not under version control, so the earlier corpus
> cannot be re-measured to settle it. The two candidate explanations are a differently-drawn
> exclusion boundary in the earlier run and a line-wrap change splitting one reference — **and
> distinguishing them is a Phase 6 input, not a matter of picking the more flattering number.**
> Recorded because a count that moves without an explanation is a finding under rule 9 whether or
> not the number that moved is the load-bearing one, and because the alternative — restating 24
> because 24 is what was published — is the exact failure this specification exists to prevent. **Every condition in that sentence is a
thing a reimplementation can get wrong while still printing a plausible number**, which is what
rule 27 buys: the reader who disagrees now knows exactly which knob to turn.

**Fixture, per rule 5.** Ship two inputs the check must classify correctly, because "no
broken citations found" is indistinguishable from "the matcher matched nothing":
a copy of the document with `§18 item 99` inserted (**must fail**, naming 99 — and note this
fixture also exercises the exact-count arm, since it raises the count by one), and a copy with
`§18 items 24–27` deleted (**must fail on the count**, not merely report a smaller number).
The near-miss that catches naive implementations is the literal `§18 item N` (rules 11, 12, 20
and "Why it exists") — it must be reported as neither a reference nor a failure.

**Advisory second pass, non-failing.** Report — do not fail on — occurrences of a bare
`item <N>` outside §18 that are not preceded by `§18`. Some are correct references in a
context that already established the section; some are about something else entirely, and
`item 7` in the running log means *instance 7*, not §18 item 7. That ambiguity is why this
half advises rather than fails: the fix is to write the `§18` prefix, and a check that cannot
tell the two apart must not be given the power to block. §18 item 33 is currently referenced
only in the bare form and would be caught here.

> ⚠ **SCOPE BOUND — this check does not validate `file:line` cites, and must not grow to.**
> §17.1 rule 11's corollary (a quoted string is cited with the line number of *the thing it
> is about*) is a discipline for authors, not a target for this mechanism. Validating it
> means resolving hub-source line numbers from a scratchpad document whose citations drift
> against a branch under active construction by three developers — an unbounded job, and
> **an item that can never be complete is an item nobody can close.** The `§18 item N` space
> is closed, internal to one file, and therefore mechanisable; that is the whole reason this
> check is worth having and the reason to keep it where it is.

### Phase 7 — Image *(unblocked; Q3 answered; can run any time after Phase 0)*
A **new stage `hub-gke` inside the existing root `Dockerfile`**, built with
`--target hub-gke` (§11): non-root uid 1000, embedded web UI, `HOME` set, writable
`~/.scion`, no `ENV KUBECONFIG`.

**It is not the final stage** — the final stage is the default build target, and the external
`gcloud` consumers pass no `--target`. The property to hold is that **the default build target
remains the plain runtime image**; because `hub-gke` must `FROM` a stage defined earlier in
the file, holding that property requires naming the existing stage 3 (`AS runtime`) and
terminating the file with an empty `FROM runtime` stage. Full reasoning and stage order in
§11 — read it before editing the file, because the answer without the reasoning reads as dead
code and invites deletion.

**Acceptance**
- The change is a **new stage**; the root `Dockerfile`'s existing default build target
  produces a byte-identical image to before — verified, not assumed. No `USER` is added to
  any pre-existing stage, and stage 3 gains **only** the name `AS runtime`, no instruction
  change.
- `docker build .` with **no `--target`** produces the plain runtime image: uid 0, no `HOME`,
  no `~/.scion` — asserted in CI as a standing check, not verified once by hand. The
  assertion is what stops the trailing empty stage being deleted as dead code (§11).
- The trailing empty stage carries a comment in the `Dockerfile` saying why it exists.
- No second Dockerfile is created, and the phase does **not** attempt the four-Dockerfile
  consolidation — that is ptone/scion#1092, routed to repo-maintenance (§11). If a stage
  genuinely cannot work, the fallback to `image-build/hub-gke/` is taken **and the reason is
  written into §11** before the phase is closed.
- `docker inspect` on `hub-gke` shows `User: 1000` and **no** `KUBECONFIG` in `Env`.
- The web UI is embedded (`--enable-web` serves it).
- The pod schedules with `runAsNonRoot: true` and `/readyz` returns 200.
- Files the hub creates on the Filestore share are owned by `nfs.uid`/`nfs.gid`.
- The chart still sets `KUBECONFIG: ""` — the image fix does not remove the chart-side
  defence.

### Phase 8 — Agent-side NFS *(BLOCKED ON #1075)*
No new chart templates expected — this phase **verifies** that the chart's rendered
configuration actually reaches agent pods once the plumbing exists, and removes the
`acknowledge1075` guard.

**Acceptance**
- An agent pod manifest shows the workspace PVC with a `subPath`, **not** an `EmptyDir`.
- Shared dirs are subPaths on the same PVC; **no per-shared-dir PVC is created**.
- `fsGroup` on the agent pod equals `nfs.gid`.
- The `workspace-provision` init container is present when a git clone is configured.
- Files written by an agent are visible to the hub under
  `<mount_root>/<share_id>/hub-projects/<slug>` and survive pod deletion.
- The `acknowledge1075` value and its guard are removed in the same commit.

### Phase 9 — kind smoke test *(deferred; scoped only)*
Non-HA boot in kind (sqlite, local storage) needs no credentials and is the cheap 80%.
An HA boot needs Postgres plus a GCS fake, a Secret Manager fake, and forged IAP JWTs.
Blocked in practice by image-build time (6–83 min, no layer cache). Revisit once an image
cache exists.

**Dependency graph**

```
P0 ─┬─ P1 ─┬─ P2 ──┐
    │      └─ P3 ──┼─ P4 ── P5a ── P6
    └─ P7 ─────────┘
                     ha-lead preflight split ── P5b
                     #1075 ─────────────────── P8
                                                P9 (deferred)
```

Phases 6 and 8 depend on P5a only; P5b and P8 are the two externally blocked leaves and
can land in either order.

---

## 18. Acceptance Criteria (reviewer / QA, whole chart)

Where a criterion here conflicts with §17 prose, **the criterion governs** and the conflict
is reported to the issue owner rather than silently resolved (§17.1).

**Static**
1. `make helm-verify` passes for every `ci/values-*.yaml`; `kubeconform -strict` with no
   `--ignore-missing-schemas`.
2. Every probe and the `BackendConfig` health check use the **literal `/readyz`**;
   `healthz` appears nowhere; no prefixed `readyz` variant appears anywhere (§9.2, §13.1).
3. No `SCION_SERVER_DATABASE_*` or `SCION_SERVER_OIDC_*` env var is ever emitted.
4. No secret material in `args`, ConfigMaps, or annotations.
5. `database.maxOpenConns: 1` is rejected; `2` is accepted.
6. `/projects/000000000/global/backendServices/0` is rejected; `123456789` variants are
   accepted with a warning.
7. A `mountPath` that diverges from `nfs.mountRoot + "/" + shares[0].id` fails the render.
8. `backend: nfs` without `acknowledge1075` fails while #1075 is open; `auth.mode: oauth`
   without `acknowledgeOAuthUnlanded` fails while §14.2 is open.
9. Installing without a session secret, without `hub.hubId`, or without `storage.bucket`
   fails at template time, each with a message naming the value.
10. `server.mode: hosted` is always rendered and cannot be disabled by any value; `args`
    carry `--host 0.0.0.0`.
10a. **Every reserved flag is rejected when supplied through `hub.args`, and the assertion names
    which failure MESSAGE fires** — not merely that the render failed. Asserting only that a
    rejection occurred lets one guard silently cover for a deleted one, and it is what allowed a
    two-layer guard to be mis-counted as one-layer earlier in this project. Each reserved group
    must therefore turn a row red on its own. **Do not assume every entry is checkable against
    rendered `args`** — for the groups whose reason is "delivered through another channel" or
    "nothing may ever pass this", the failure message is the only available assertion, by
    construction. Include a **must-still-accept** set, so that widening a group into uselessness
    fails too. *Met by Phase 0 at `cb183de` (54 cases).*
10b. **No rendered `settings.yaml` contains `server.hub.public_url`, under any values
    permutation** — asserted, not assumed. It outranks both argv and `SCION_SERVER_BASE_URL` for
    the agent-facing endpoint while being invisible to the OAuth resolver, so rendering it is the
    one action this chart could take that yields two different base URLs in one process. This is
    the **only** route to the divergence that no existing guard closes: the reserved-flag list
    closes argv, the schema closes the env var, and nothing closes this.
    **Note which channel it is.** Of the three, this is the only one the chart *controls
    directly* — argv is reserved because a chart cannot stop an operator, and the env var is
    schema-closed for the same reason. The settings file is **ours; we write it**, which is
    precisely why nobody thought to guard it. **The channel you own is the one you do not
    defend.** And the action is not an attack or a slip: it looks like a helpful key to add, and
    a Phase 5 developer wiring an ingress hostname will consider exactly this with every instinct
    approving. That is why it needs a test rather than a note (§5.4).
    **This is a LIVE path, not a future one — revision 8, reproduced by `gd-p1-dev`.** The item
    was written as a guard against a Phase 5 author editing a template. It is that, but
    `config.extra` is `mergeOverwrite`d over the settings tree **before** the assertions run, so
    an operator renders `public_url` today, with a schema-valid values file and no template
    change, and gets `settings.yaml: server.hub.public_url` alongside
    `SCION_SERVER_BASE_URL` — one manifest, two base URLs, each correct by its own resolver,
    nothing warning. Any text here reading "the risk is a later phase" understates who must act.
    The assertion runs on the **post-merge** document for exactly this reason.
    **Positive twin, which must not be lost** (`gd-p1-dev`): `config.extra` must still reach
    *unmodelled* keys under `server.hub` — that is what the escape hatch is for, and it is
    asserted. **The rule is on the key, not on the namespace.** Text reading "the chart closes
    `server.hub` to `config.extra`" would be wrong and would authorise a one-line fix that breaks
    the extension point.
    **Refuse outright; do NOT permit-when-equal** (`gd-em`, ruling, against the more reasonable-
    looking reviewer version). Allowing `public_url` when it equals `hub.baseUrl` **creates a
    second source of truth that must be kept in sync, and the failure of that sync IS the bug**:
    an operator who sets both equal today and changes `hub.baseUrl` tomorrow gets precisely the
    split this item exists to prevent, **from a values file that passed the guard on the day it
    was written.** The permissive form guards the moment of authorship and not the lifetime of
    the file. There is nothing on the other side of the trade — a `public_url` equal to `baseUrl`
    buys the operator nothing — so the only configurations it admits are useless today and
    dangerous later.
    **Independently reproduced from both ends within one hour**, which is why it is stated this
    strongly: `gd-p1-dev` from the merge path (`config.extra` → `mergeOverwrite` → rendered
    document), `gd-p1-rev` from the binary (`settings_v1.go:517` koanf tag → `:1404-1405`
    assignment to `Hub.Endpoint` → `server_foreground.go:1311-1312`, the **first** statement of
    `resolveHubEndpoint`, ahead of both `--base-url` and the env var → `:2102-2108`, where
    `initWebServer` never reads it). Neither knew the other was looking.
    Per rule 5, the assertion ships with a fixture that renders the key and must be flagged.
    *Phase 1 implements the narrow matcher.*
10c. **The alias-key enumeration, and a `config.extra` collision check. Owner: Phase 5a**
    (`gd-em`, ruling). The general form of 10b is **not** an allow-list over `server.hub`, and
    the reason that option was rejected is the whole content of this item:
    - **Too broad.** An allow-list over `server.hub` blocks unmodelled `server.hub` keys, which
      is precisely the case `config.extra` exists to serve — it prevents a chart fork. It would
      break the extension point to close one key.
    - **Too narrow.** *Nothing about the `server.hub` prefix is dangerous.* The hazard is that
      `public_url` is a second name for a value the chart already controls by another channel
      (`hub.baseUrl` → `SCION_SERVER_BASE_URL`). A prefix allow-list catches this instance and is
      silent on every alias elsewhere in the tree.

    The category, in the words to reuse: **an ALIAS KEY is a settings key that is a second name
    for a value the chart already sets through a different channel. Collision detection cannot
    see it, because the two names differ. Prefix allow-lists cannot see it, because the alias
    need not live near the original.** So the enumeration Phase 5a owes is not "all of
    `server.hub`" — it is **every settings key that is a second name for something the chart sets
    elsewhere**: small, closed, checkable, and derivable from the chart side alone by walking
    what the chart sets and asking what else names each quantity.

    **⚠ SCOPE BOUND, and it is what makes this item closeable** (`gd-p1-dev`, ruling `gd-em`).
    The enumeration covers keys reachable **through the chart** — those `config.extra` can carry.
    It does **not** cover the contents of `config.existingSecret`, which the chart never sees:
    the chart holds a name, the contents exist only at apply time. **Do not grow this item to
    cover that route.** Doing so turns it into an enumeration of *everything the hub reads*,
    which is unbounded — and **an item that can never be complete is an item nobody can close**,
    which is how a required item quietly becomes decoration. That route is instead a
    **documented transfer of an invariant**, stated at `config.existingSecret` in `values.yaml`
    where the person taking it on reads it at the moment they take it on. Per rule 8 that is a
    **deferral and not a closure**, and it stays labelled as one.

    **The framing Phase 5a inherits, and it is better than the category name** (`gd-p1-rev`):
    > *A map of what the chart renders is necessary and not sufficient. The complementary
    > question — **which keys the binary READS that the chart does not render, and which of those
    > `config.extra` can reach** — is where both of today's new findings live.*

    That is the alias category from the other side, and it is stronger because it is a **search
    procedure rather than a category name**: it tells 5a what to *do*, and it is enumerable from
    the koanf tags. `assertSettings` is the only assertion that sees the merged document, so it
    is where the question gets answered in general.

    **Also required, and deliberately a guard that does NOT catch `public_url`:** intersect the
    pre-merge settings document's key set with `config.extra`'s. Any key in both is an operator
    overwriting a value the chart itself wrote — the settings-file analogue of the reserved-flag
    list, which is already this chart's idiom for the same problem on argv, and it needs no
    enumeration of anything because both documents exist at merge time. Asking for a guard that
    demonstrably misses today's bug is normally a bad sign; it is not here, because **the two
    rules cover disjoint halves and each half is invisible to the other** — collision covers keys
    the chart *writes*, the alias rule covers keys the chart does not write but whose *value* it
    controls. `config.extra` breaches both and only one had been conceptualised.
    **Do not add a second key to the narrow matcher instead of converting it** (`gd-p1-dev`; the
    load-bearing line, and the reasoning is better than the ruling it refines). The failure mode
    of a denylist of one is not that it stays at one. It is that the second key is added cheaply
    **and the addition makes the list look adequate**: two entries read as a considered policy,
    one entry reads as a stub — and **a stub is the only thing that ever gets converted.** The
    matcher must stay visibly provisional until 5a generalises it.

    **`config.extra` must also be DOCUMENTED as unguarded by default, and this is a §19 item.**
    "`config.extra` is deep-merged before the assertions run" has two readings: (a) the
    assertions apply to the merged document — deliberate and good — and (b) **the merged document
    may contain anything the assertions do not name.** The chart states (a) nowhere an operator
    reads and (b) nowhere at all, so an operator has no way to learn that **the guard surface is
    a NAMED LIST rather than a schema.** `values.yaml` names the assertions that exist and says
    the remainder is unguarded by design. That is not a hedge — **it is the difference between an
    escape hatch and a trapdoor.**

    *Owner assigned by phase number, not by condition (rule 8): 5a is ingress and IAP, the phase
    that will reach for a hostname and a public URL. The phase that will breach the guard is the
    phase that should have to make it general.*
11. `server.storage.provider` is `gcs` — **not** the Filestore share (§8).
12. A non-PVC volume mounted under `nfs.mountRoot` fails the render (§8.5).
13. Switching `auth.mode` between `oauth` and `proxy` changes exactly one config subtree;
    the five Block-1 keys are byte-identical across the two golden files.

**Live (GKE Autopilot + Cloud SQL + Filestore + GCS)** — **the whole block is relocated to
`deploy/helm/scion-hub/VALIDATION.md`**, because no agent on this project has a cluster.
**A relocated criterion is not a passed criterion.**
14. `auth.mode: proxy` — the two-step install completes; `/readyz` returns 200; the
    GCLB backend is `HEALTHY`; the web UI is reachable through IAP and a signed-in user
    gets a session.
15. `auth.mode: oauth` (after §14.2 clears) — a **single-step** install completes and a
    signed-in user gets a session with no IAP in the path.
16. All replicas report the **same** `hub_id`, and it survives `helm upgrade` unchanged
    (§6.6).
17. The hub reads and writes the configured **GCS bucket**; blob storage is not on the
    Filestore share (§8, §8.4).
18. A WebSocket to the PTY endpoint survives > 120 s; SSE is not buffered; `X-Scion-*`
    headers arrive unmodified.
19. An agent can be created and reaches `Running` — on Autopilot, without a `Pending` PVC.
20. `helm upgrade` with a changed image succeeds and the hub returns to ready.
21. `helm uninstall` leaves the PV, PVC, Filestore instance, GCS bucket and Cloud SQL
    instance intact.
22. **Documented-surprise check:** change `runtimes`/`admin_emails` in values, run
    `helm upgrade`, and confirm the DB-owned value wins — i.e. the behaviour matches
    §5.5's documentation rather than the operator's intuition.
23. **#1075 regression check:** with `backend: nfs` and #1075 unlanded, confirm agent pods
    take the `EmptyDir` path — and confirm whether agent edits reach the share (the
    split-view risk in §14.1).
30. *(number out of sequence deliberately — items 24–29 are live and must not be renumbered;
    this is a Live criterion, so it sits with the Live block rather than with its own group.)*
    **Writable directory, unwritable file — checked on a running pod (§5.2).** The hub starts
    and reads its settings; a write to `$HOME/.scion/settings.yaml` **fails**; a write to
    `$HOME/.scion/probe` **succeeds**. Both halves are required: the first alone passes on a
    wholly read-only home, which breaks the hub for unrelated reasons, and the second alone
    passes on the copy topology this design rejects.
    Relocated with the rest of the block. **Do not substitute a runnable
    approximation:** asserting the rendered manifest's `defaultMode` (item 29) is a template
    check, not this check, and the thing this criterion exists to catch is a cluster where the
    projected mode or the `subPath` mount does not behave as the manifest says it will.

**Image (§11, Phase 7)**
24. **The default build target of the root `Dockerfile` is still the plain runtime image.**
    `docker build .` with no `--target` yields uid 0, no `HOME`, no `~/.scion`. This is a
    **standing CI assertion**, not a one-off check, and it is what fails if the trailing
    empty stage is removed as dead code.
25. `--target hub-gke` yields `User: 1000`, no `KUBECONFIG` in `Env`, and a writable
    `~/.scion`; stage 3 differs from its previous form by the name `AS runtime` alone.
26. **`image.repository` has no default and is schema-required**: rendering without it fails
    at template time with a message naming the value, and no `scion-hub`-shaped path ships as
    a default anywhere in the chart (§11 artifact-name hazard, §19).
27. `runAsNonRoot: true` cannot be disabled or loosened by any value, so a chart pointed at a
    root image **fails at pod admission** instead of running as root (§4.4, §11).

**Configuration mount (§5.2, Phase 1)** — static; the live half is item 30.
28. **`settings.yaml` renders as a `subPath` Secret mount at `$HOME/.scion/settings.yaml`,
    over the `scion-home` `emptyDir`, and no init container renders at all.** No values
    combination produces an init-container copy of the file, and no manifest mounts the
    settings Secret as a whole directory at `$HOME/.scion`. **Asserted per ENTRY, not per
    section:** every `initContainers` entry must carry `restartPolicy: Always`, so a native
    sidecar passes and a run-once container fails. *Phase 1 shipped the narrowed form ahead of
    need (`03b7a765`, `aecc2f08`), so Phase 2 adds the Cloud SQL proxy without touching it.*
    The criterion is "no init container", never "no `initContainers` key" — the latter spelling
    would go red on Phase 2's correct code, and "the test is wrong, delete it" is the cheapest
    resolution available to someone heads-down on something else. That deletion reopens the
    #1091 copy shape with a green suite (§4.1).
    With zero entries the rule is vacuously true, so per §17.1 rule 5 it is proved against three
    fixtures: a run-once `settings-init` that **must** be flagged; a native sidecar with
    `restartPolicy: Always` that must **not** be; and both in one list, where the run-once one
    must still be flagged — the case a naive "does `restartPolicy: Always` appear anywhere in
    this section" check gets wrong. **The middle fixture is standing, runnable proof that the
    rule does not fire on the Cloud SQL proxy**, available to Phase 2 before it writes anything.
29. **The settings Secret renders `defaultMode: 0444`, and no manifest renders `0600` or
    `0400` on it** — asserted, not merely documented, because a root-owned `0600` projection is
    unreadable by uid 1000 and surfaces as a Block-1 preflight error naming the wrong key
    (§5.2). This criterion exists to stop the mode being re-tightened by routine security
    hygiene.
32. **Every `mode` / `defaultMode` literal in the chart carries its leading zero, unquoted** —
    asserted mechanically over all rendered output, not reviewed by eye. YAML 1.1 reads `444` as
    **decimal** 444 = octal `0674`, group-writable and group-executable, and accepts it
    **silently** — verified by decoding it, not by reading the spec (§5.2's table). Item 29 does
    not catch this: it forbids `0600`/`0400`, and `444` is neither. Note the criterion also says
    **unquoted**: `"0444"` is an `int32` unmarshalling error, so it fails validation rather than
    shipping — a different failure, still worth asserting against, since a developer who reads
    only "carries its leading zero" may reach for quotes. Applies to Phase 3's and Phase 4's
    modes as they land, not only to this one.

**Temporary values that must not become permanent (§17 Phase 3)**
31. **`auth.acknowledgeOAuthUnlanded` is gone once Phase 3 lands** — absent from `values.yaml`
    **and** from `values.schema.json`, **and** `auth.mode: oauth` with no acknowledgement key
    installs successfully. Both halves are required: absence alone could mean the key was
    renamed. It guards only the window between Phase 1 rendering oauth config and Phase 3
    delivering the client secret, and this criterion is what makes its removal happen rather
    than remain a comment. *It supersedes item 8's `acknowledgeOAuthUnlanded` clause on the day
    it passes.*
33. **A read-only state directory degrades SILENTLY, and Phase 4 must make it not do that.**
    `templatecache.New` failing is a `slog.Warn` and the runtime broker continues without a
    template cache (`pkg/runtimebroker/server.go:337`, `gd-p1-dev`). So the failure mode of
    getting the state-directory mount wrong is not a crash-loop a reviewer would see — it is a
    hub that comes up ready, serves, and has quietly lost template caching. **Phase 4 owns the
    mount, so Phase 4 owns the assertion**: a positive check that the cache directory is
    writable by the hub's own UID, not an absence check. The related positive twin
    (`gd-p1-dev`, and the reason this was found): `cache/` existing proves the state directory
    was writable **by the hub's UID at startup**, where the `touch` probe it replaces only ever
    proved it for the exec'ing shell — **a different principal answering a question about the
    hub.** *Configuration-closed, not code-closed (rule 8).*
34. **`/api/v1/system/init` is closed by a CONFIGURATION, and the phase that opens it must know.**
    `handleSystemInit` (`pkg/hub/system_handlers.go:446`) calls `config.InitMachine`
    (`:514`) and then `cleanupUnselectedHarnessConfigs` (`:522`, defined `:529`), which calls
    `os.RemoveAll` (`:547`) **inside the tree over which this chart mounts the read-only settings
    file**. Two gates hold it shut, and **neither is visible from the chart side**:
    `requireWorkstation` (`pkg/hub/server.go:3594`, defined `:3694` — 404s when
    `s.workstation` is false) and `Workstation: !hostedMode` (`cmd/server_foreground.go:1496`),
    plus `assertLoopback` (`system_handlers.go:452`) within the handler itself. So the endpoint is
    shut **because the chart renders `--hosted`** — a fact about a configuration wearing the
    appearance of a fact about the code (rule 8). Three consequences a phase must carry:
    - **The loopback gate is weaker in a pod than it looks.** Every container in the pod shares a
      network namespace, so a sidecar reaches it as `127.0.0.1`. That is Phase 2's Cloud SQL
      proxy and anything Phase 5a terminates in-pod. Verified independently by
      `gke-deploy-lead`, and **recorded as a trip-wire on §4.1's container table** — the author
      who adds the *next* container reads the pod spec, not this item.
    - **It is one deletion away.** Removing `production` from Phase 0's group 1 — the removal that
      group's own comment currently invites (Phase 0, direction B) — lets `--production=false`
      set `hostedMode = false`, which flips `Workstation` to true and opens all of
      `/api/v1/system/*` while the manifest still reads `--hosted`.
    - **Ownership, and rule 8 applied to this item honestly.** "Whichever phase first makes
      workstation mode reachable" is a *condition*, and rule 8's own corollary says a conditional
      owner is not an owner. No phase in §17 plans to enable workstation mode, so there is no
      phase to assign the opening to — which means this item splits, and pretending otherwise
      would leave the enforceable half unowned too:
      - **Keeping it shut is owned, now: Phase 0.** `--hosted` always rendered and not
        disableable (item 10), and group 1 repaired in both directions so `--production=false`
        cannot reach `hostedMode` (direction B). Those are testable today and they are the entire
        practical defence.
      - **Opening it is owed by no one, so this is a TRIP-WIRE rather than a queued item.** It is
        numbered so that a phase proposing to flip `Workstation` finds it *before* the design
        review rather than after, and §17.1's standing question for every remaining phase points
        here.
      Citations are given so the next person **re-verifies the gates rather than trusting this
      note** — per rule 8 these are gates that decay, and a note about a decaying gate decays with
      it.
35. **A settings file the hub does not FIND degrades to defaults silently — and under this
    chart's defaults the HA preflight then does not run.** Verified: `isHADeployment`
    (`cmd/server_foreground.go:927-938`) returns true only for `K_SERVICE` set (Cloud Run, not
    GKE), `Database.Driver == postgres`, or `Storage.Provider == gcs` **and** `Auth.Mode ==
    proxy` — all read from the **loaded** config. A hub that fails to load
    `$HOME/.scion/settings.yaml` has none of them, so it starts on defaults, reports ready, and
    **the HA preflight that §6.5 makes the contract for multi-replica safety never executes.**
    `gd-p1-rev` found this behind a `hub.home` path bug; the bug is Phase 1's and is fixed, but
    the *consequence* is structural and outlives it: **the guard for the HA case is disabled by
    the very failure that puts you in the HA case.** Two requirements follow, and the second is
    the one that generalises:
    - The chart asserts the settings path it mounts is the path the hub reads, **derived from
      `hub.home` rather than written out** — a check hardcoding `/home/scion/.scion` while
      `hub.home` defaults to `/home/scion` has the same expected value whether the derivation is
      correct or broken, which is §13.1's axis (b) exactly.
    - **A "file not found" that resolves to defaults is not a safe default here.** Prefer a
      positive assertion that the hub loaded *this* file — the settings-file analogue of item
      33's principle, and the same reason `cache/` beats a `touch` probe.

    **Review record, kept because the halves were verified by different people at different
    times (rule 11).** `gke-deploy-lead` verified the **load** half — `LoadGlobalConfig`
    (`pkg/config/hub_config.go:628`) falls through to `loadGlobalConfigLegacy` (`:635`) and
    embedded defaults, and `loadGlobalConfigFromSettings` returns `(nil, false)` on every
    not-found route including a `GetGlobalDir()` error (`:640-660`) — and explicitly declined
    the **preflight-skip** half, sending it on as a *claim under review with its author named*
    rather than as a finding. That half is now verified, in source rather than by restatement:
    the embedded default driver is `sqlite` (`pkg/config/hub_config.go:540`),
    `validateHostedHAPreflight` returns `nil` immediately when `!hostedHAGuardsRequired(cfg)`
    (`cmd/server_foreground.go:951-954`), and it has exactly one non-test caller — `:151`,
    whose error aborts startup. **The preflight does not fail on this path; it does not run.**

    **A third requirement follows, and it is Phase 2's rather than Phase 4's** (§7.0): the
    legacy fallback still applies `SCION_SERVER_*` overrides (`pkg/config/hub_config.go:798-803`;
    `envKeyToConfigKey`, `:976-986`, maps `SCION_SERVER_DATABASE_DRIVER` → `database.driver`),
    and the settings path applies them too (`:685`). **A driver delivered by env survives a
    settings-load failure; a driver delivered only by a settings key does not.** Whichever phase
    configures the driver states which channel it chose. That removes the instance; it does not
    close the item, whose subject is the degradation shape.

    *Configuration-closed, not code-closed (rule 8).*

36. **THREE PHASES WRITE TO THE SAME `os.Stat` GUARD AND NO BRIEF MENTIONS THE OTHER TWO.**
    Raised by `gd-em`; it has no owner, and it is recorded as a numbered item specifically so it
    cannot close by deferral. `cmd/server_foreground.go:104` `os.Stat`s `$HOME/.scion` and calls
    `config.InitGlobal` at `:107` **only when the directory does not exist**, seeding
    `settings.yaml` from `pkg/config/embeds/default_settings.yaml` (`pkg/config/init.go:667` →
    `:575-576`, `:597-598`). Three separate deliverables decide the branch:
    - **Phase 7** — the `hub-gke` stage `mkdir`s `$HOME/.scion` in the image (§11).
    - **Phase 0/1** — the chart mounts an `emptyDir` at the same path (§4.3, §5.2).
    - **Phase 1** — the settings Secret is mounted at `$HOME/.scion/settings.yaml` (§5.2).

    **Every owner here was judged safe by citing a different owner, and none of the three briefs
    names the other two.** Phase 7's `mkdir` was ruled inert *because the chart mounts there* —
    which is a claim about Phase 0 and Phase 1, made in Phase 7, verified by nobody at the time.
    That is the whole defect: three conditional safety arguments arranged in a ring, each true
    only while the others hold, and no artifact where the ring is visible. Rule 8's corollary
    applies at the level of the *set*: a condition is not an owner, and three interlocking
    conditions are not three owners.

    **What is owed.** Whichever of the three lands next states, in its PR body, what the other
    two do to `$HOME/.scion` and what the resulting `os.Stat` outcome is — and a test asserts
    the outcome rather than the intent. **Do not resolve this by deciding the guard "doesn't
    matter",** which is the tempting move because the Phase 0 conclusion in §5.2 survives every
    branch. It survives for an unrelated reason (an empty directory has no `server:` key either
    — rule 15: agreement on the conclusion is not corroboration of the reason), and the guard
    also controls whether the hub seeds its own state directory, which is not a `--config`
    question.

    *Cross-phase, unowned by construction; this item is the owner of record until one phase
    takes it.*

---

## 19. Appendix A — `values.yaml`, consolidated

Illustrative. Comments mark schema-enforced constraints. `REQUIRED` means the render fails
without it.

**`image.repository` is absent as an active key — only a commented example.** That looks like
an oversight and is the mechanism: **a key defaulted to `""` would make "schema-required" a
no-op**, because the value is then always present and the schema has nothing to reject. The
key must be *missing* for the requirement to bite. **The chart has no default image because
four Dockerfiles in the repository produce a hub image and only the `hub-gke` stage is correct
(issue #1092); every candidate default is either an empty registry path or another team's
artifact, and a default that cannot be correct turns an install-time error into a runtime
mystery.** A cosmetically distinct default such as `scion-hub-gke` was considered and rejected
for the same reason: it still names a path we do not publish (§11).

```yaml
nameOverride: ""
fullnameOverride: ""

replicaCount: 1              # >1 breaks web terminal / exec / logs / port-forward — §4.2, Q1
updateStrategy:
  type: ""                   # "" → Recreate at 1 replica, RollingUpdate above (§4.2)

image:
  # REQUIRED. No default, and the chart will not install without it.
  #
  # Must be built from the root Dockerfile with --target hub-gke: runs as
  # uid 1000, web UI embedded, no baked KUBECONFIG. No such image is
  # published today; you build and push it yourself.
  #
  # The published artifact named scion-hub
  # (us-central1-docker.pkg.dev/PROJECT_ID/public-docker/scion-hub, built
  # from image-build/hub/Dockerfile) is NOT this image. It runs as root and
  # is built without the embedded web UI. The matching name is a
  # coincidence. Pointing the chart at it fails pod admission, by design.
  #
  # It runs as root because image-build/hub/Dockerfile:24 sets USER root and
  # never switches back: the drop to uid 1000 happens at ENTRYPOINT time via
  # the sciontool init shim, which Kubernetes never sees. Pod admission
  # judges the image USER, which is why runAsNonRoot: true rejects it — see
  # §11.
  #
  #   repository: us-docker.pkg.dev/my-project/my-repo/scion-hub-gke
  tag: ''       # defaults to .Chart.AppVersion; prefer digest
  digest: ''    # preferred, mutually exclusive with tag
  pullPolicy: IfNotPresent
  pullSecrets: []

hub:
  hubId: ""                  # REQUIRED, stable across upgrades, never generated — §6.6
  name: scion-hub
  baseUrl: ""                # REQUIRED, https:// UNCONDITIONALLY (not gated on
                             # ingress.enabled) — the session cookie's Secure attribute is a
                             # literal HasPrefix on this value, so a non-https base URL drops
                             # Secure and the regression presents as nothing at all.
                             # §5.4, §17 Phase 1 accepted deviation.
  webPort: 8080
  home: /home/scion
  args: []                   # APPEND-ONLY — does NOT override the default command. The
                             # mandatory args are always rendered (§4.1, §6.5), and a
                             # reserved-flag guard rejects by name. The reserved flags are
                             # grouped BY REASON and each group fails with its own message.
                             # The authoritative list is in _helpers.tpl (scion-hub.hubArgs),
                             # NOT here and not in the design — it is executable there, so it
                             # cannot drift from what the chart rejects. Duplicating it into
                             # prose has already gone stale once (§17.1 instance 8).
                             # What you need to know without reading it:
                             #   - Some reserved flags NEVER appear in rendered args and never
                             #     will. That is WHY they are reserved. Do not remove an entry
                             #     because you cannot find it in the args — that removal looks
                             #     like tidying and it is the whole failure.
                             #   - Do not merge groups because they share that property. They
                             #     share a failure mode for unrelated reasons; merging loses
                             #     both reasons and, worse, breaks the one group that IS
                             #     verifiable against rendered args.
                             #   - Changing a flag's delivery channel means MOVING its entry
                             #     between groups, never adding a second emitter.
                             # NEVER contains secrets — §4.1. (Earlier revisions said
                             # "overrides the default command"; that contradicted the Phase 0
                             # criterion "no value can disable hosted mode", and the criterion
                             # governed — see §17.1.)
  extraEnv: []               # NOT an operator-convenience hatch. It exists so the "no
                             # SCION_SERVER_DATABASE_*/OIDC_* env var" assertion has something
                             # real to catch — the positive twin §13.1 requires. Delete it and
                             # that assertion becomes vacuous. Guards (Phase 1, #1096): both
                             # dead prefixes rejected; shadowing HOME, KUBECONFIG,
                             # SCION_SERVER_BASE_URL, SCION_REQUIRE_STABLE_SIGNING_KEY rejected;
                             # a credential-looking NAME with a LITERAL value rejected
                             # (a literal lives in the Deployment, readable by more people than
                             # a Secret) — secretKeyRef passes. Plus a fourth on the VALUE axis:
                             # assertNoCredential catches credentials in URL userinfo, e.g.
                             # postgres://user:pw@host, under any name at all. Name checks
                             # cannot see that — the operator picks the name.
  extraVolumes: []
  extraVolumeMounts: []      # NEVER under workspaceStorage.nfs.mountRoot — §8.5
  adminMode: ""              # SCION_SERVER_ADMIN_MODE (one of the 3 env vars that work)
  maintenanceMessage: ""     # SCION_SERVER_MAINTENANCE_MESSAGE
  resources:
    requests: { cpu: 500m, memory: 1Gi }
    limits:   { memory: 2Gi }
  securityContext:
    runAsUser: 1000          # must equal workspaceStorage.nfs.uid — §4.4
    runAsGroup: 1000         # must equal workspaceStorage.nfs.gid
    runAsNonRoot: true       # not loosenable by any value — a root image must fail at pod
                             # admission, not run (§4.4, §11 artifact-name hazard)
  podAnnotations: {}
  nodeSelector: {}
  tolerations: []
  affinity: {}

config:
  existingSecret: ""         # supply the whole settings.yaml yourself — §5.2
  existingSecretKey: settings.yaml
  extra: {}                  # deep-merged over the rendered tree — §5.3

database:
  driver: postgres           # postgres | sqlite   (postgres ⇒ HA preflight — §6.5)
  host: 127.0.0.1            # the proxy sidecar
  port: 5432
  name: scion
  user: ""                   # IAM SA email (auth=iam) or SQL user (auth=password)
  auth: iam                  # iam | password. Default CONTINGENT on the Phase 2 IAM
                             # verification — see §7.2. Both modes always ship.
  password: ""               # auth=password only; prefer existingSecret
  existingSecret: ""
  existingSecretKey: password
  sslMode: disable           # correct: the proxy terminates TLS — §7.2
  maxOpenConns: 10           # SCHEMA MINIMUM 2 — §7.4
  maxIdleConns: 2
  connMaxLifetime: 30m
  connMaxIdleTime: 5m

cloudsql:
  enabled: true
  instanceConnectionName: "" # REQUIRED when enabled: project:region:instance
  nativeSidecar: true        # initContainer + restartPolicy: Always (GKE >= 1.29) — §7.1
  privateIp: false
  autoIamAuthn: true
  image: gcr.io/cloud-sql-connectors/cloud-sql-proxy@sha256:...   # pinned by digest
  port: 5432
  healthCheckPort: 9801
  extraArgs: []
  resources: {}

storage:                     # THE HUB'S BLOB STORE — GCS, not Filestore — §8.4
  provider: gcs
  bucket: ""                 # REQUIRED when database.driver == postgres

workspaceStorage:            # AGENT WORKSPACES — Filestore — §8.1
  backend: nfs               # nfs | local
  acknowledge1075: false     # REQUIRED true to use nfs while #1075 is open — §14.1
  nfs:
    mountRoot: /mnt/scion
    subPathRoot: projects
    uid: 1000
    gid: 1000                # also the pod's fsGroup — but ONLY rendered when backend: nfs
                             # (§4.4). Never render fsGroup unconditionally: it is pod-wide
                             # and would let a Filestore value decide the group ownership of
                             # every volume in the pod, including the settings mount, with
                             # Filestore switched off.
    mountOptions: "vers=3,hard,nconnect=4,_netdev"
    storageClass: ""
    shares:                  # minItems 1; only shares[0] gates readiness — §8.2
      - id: share1
        server: ""           # Filestore IP
        export: /vol1
        pvName: ""           # defaults to <fullname>-<id>
  filestore:
    createPV: true
    createPVC: true
    existingClaim: ""
    capacity: 1Ti
    csiVolumeHandle: ""      # modeInstance/<project>/<location>/<instance>/<share>
    reclaimPolicy: Retain

auth:                        # DISCRIMINATED UNION ON auth.mode — §6.5
  mode: proxy                # proxy | oauth. Default flips to oauth when §14.2 clears.
  acknowledgeOAuthUnlanded: false   # REQUIRED true for mode=oauth while §14.2 is open.
                                    # TEMPORARY BY CONSTRUCTION: Phase 3 DELETES this key when
                                    # it delivers the oauth client secret (§17 Phase 3, §18
                                    # item 31). It is a deliverable with a criterion, not a
                                    # comment, because a removal that lives only in a comment
                                    # does not happen.
  sessionSecret: ""          # REQUIRED unless existingSecret — never generated — §6
  existingSecret: ""
  existingSecretKey: session-secret
  requireStableSigningKey: true
  proxy:                     # mode=proxy only
    provider: iap            # schema rejects anything else — §6.5
    iap:
      audience: ""           # /projects/<num>/global/backendServices/<id> — §10.3
      oauthSecretName: ""    # pre-existing Secret for BackendConfig
  transport:                 # mode=proxy only
    mode: iap
    oidcAudience: ""         # an OAuth 2.0 CLIENT ID, not a resource path — §10.2
    platformAuthServiceAccount: ""
  oauth:                     # mode=oauth only — KEY NAMES ARE ha-lead's SKETCH (Q9)
    issuer: https://accounts.google.com
    clientId: ""
    clientSecret: ""         # Secret-sourced; never on argv

serviceAccount:
  create: true
  name: ""
  gcpServiceAccount: ""      # Workload Identity target — §7.3
  annotations: {}

rbac:
  create: true
  agentNamespace: ""         # THE SAME NAMESPACE as runtime.namespace, which is CANONICAL.
                             # Both keys are accepted — the chart does not silently prefer
                             # one. Empty: inherits runtime.namespace (which itself defaults
                             # to the release namespace). Set to a value that DISAGREES with
                             # runtime.namespace: the render FAILS, rather than letting one
                             # win quietly and granting RBAC in a namespace where no agent
                             # pod lands. §9.3.

runtime:
  type: kubernetes
  namespace: ""              # where agent pods land; CANONICAL for rbac.agentNamespace too
  gke: true
  listAllNamespaces: false   # true ⇒ ClusterRole instead of Role — §9.3

agents:
  imageRegistry: ""          # settings.yaml image_registry

probes:
  startup:   { enabled: true,  periodSeconds: 5,  failureThreshold: 60, timeoutSeconds: 3 }
  readiness: { periodSeconds: 10, timeoutSeconds: 3, failureThreshold: 3 }   # >2s — §8.2
  liveness:  { enabled: false, type: tcpSocket, periodSeconds: 20, failureThreshold: 6 }

service:
  type: ClusterIP
  port: 80
  annotations: {}            # chart adds cloud.google.com/neg and backend-config

ingress:
  enabled: true
  className: gce
  hostname: ""
  staticIpName: ""
  managedCertificate: { create: true, domains: [] }
  preSharedCert: ""
  extraAnnotations: {}       # rewrite-class annotations are rejected — §10.1

backendConfig:
  create: true
  timeoutSec: 3600           # load-bearing for WebSockets — §10.1
  connectionDraining: { drainingTimeoutSec: 60 }
  healthCheck: { requestPath: /readyz, port: 8080, type: HTTP }   # never /healthz

frontendConfig:
  create: true
  redirectToHttps: true
  sslPolicy: ""

bootstrap:
  deferHub: false            # step 1 of the two-step IAP install — §10.4

podDisruptionBudget: { create: false, minAvailable: 1 }   # only meaningful above 1 replica
networkPolicy: { enabled: false, allowedCidrs: [] }       # §9.4
```

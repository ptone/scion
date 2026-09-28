# dev-comment-cleanup: trim substrate-specific rationale from core comments

**Branch:** `scion/substrate-refactor-comments`, cut from
`9f176b817b52d36afe30b99688ab27e3c9641a73` (tip of `scion/substrate-refactor`
at dispatch time, R2-integrated).
**HEAD after this work:** see `git log -1` on the branch (single new commit
on top of the base SHA).

## Scope

Comments/docs-only change. Acceptance allows a comment to name substrate as
an example of a generic seam; it does not allow paragraphs of substrate's
own internal rationale (ContainerSpec, ateapi, bootstrap protocol, actor
lifecycle specifics) sitting in core (non-substrate) files. The job was to
cut those down to the generic seam contract plus at most one pointer, and
relocate anything worth keeping to where substrate specifics belong
(`pkg/sciontool/substrate`, `cmd/sciontool/commands/substrate_serve.go`, or
`.design/kubernetes/substrate-runtime.md`).

## What was found vs. the dispatch note

The dispatch named `pkg/config/init.go` as the file with the
ContainerSpec/UID 0/egress/ateapi paragraphs at :102-196. That file has no
substrate mentions at all — it's `InitProject`/`InitMachine` project-init
code, unrelated to the harness-init path. The actual home of that content is
`cmd/sciontool/commands/init.go`'s `InitRunOptions` struct (fields
`ForwardTermSignal`, `RequirePrivilegeDrop`, `DisablePortForwarding`,
`WorkingDir`, `ResolveWorkingDir`), which matches the quoted terms verbatim.
I treated that as the real target and note the mislabel here for whoever
integrates this.

Separately, `pkg/config`'s ~19-25 non-test substrate hits are entirely inside
`V1SubstrateConfig` and its doc comments in `settings_v1.go` — explicitly
allowlisted schema per the dispatch ("DO NOT touch ... V1SubstrateConfig and
its schema"). `pkg/config` also has substrate mentions in
`schemas/settings-v1.schema.json` (the JSON Schema `V1SubstrateConfig`
serializes to/validates against) and in schema-focused test files
(`schema_test.go`, `substrate_schema_tie_test.go`,
`substrate_broker_deploy_manifest_test.go`,
`substrate_broker_profiles_test.go`) — all schema/test artifacts of the same
allowlisted config type, not comment prose. No edits were made in
`pkg/config`.

## What changed

- `cmd/sciontool/commands/init.go`: rewrote the `InitRunOptions` struct's
  field doc comments to state the generic seam contract (what each field
  does, when RunInit uses it, what a caller sets) and cut the
  substrate-internal justification (ContainerSpec has no user field, ateapi
  has no workingDir, Substrate's egress model, bootstrap ordering) down to a
  single pointer: "See pkg/sciontool/substrate /
  cmd/sciontool/commands/substrate_serve.go ... (also
  .design/kubernetes/substrate-runtime.md §§5.6-5.7)". Also trimmed the
  `exitCodePrivilegeDropRequired`/`exitCodeNoUsableHarnessCwd` doc comments,
  `reportInitFailure`'s doc comment, and `resolveProjectHookPath`'s doc
  comment the same way — each now points at `pkg/sciontool/substrate`'s
  `StateInitFailed` or `pkg/sciontool/hooks`'s `EnforcedHooksDir` instead of
  re-deriving the reasoning inline.
- `pkg/sciontool/hooks/exec_enforced.go`: trimmed `EnforcedHooksDir`'s doc
  comment to describe the generic mechanism (a privilege-drop-enforcing
  caller's redirect target) with substrate named once as "the current
  example", instead of asserting it's substrate-serve's own mechanism.
- `pkg/sciontool/hub/client.go`: trimmed `enforceTokenFileOwnerChecks`'s doc
  comment the same way — generic contract first, substrate named once as
  the current caller.
- `pkg/sciontool/rootexec/guard_test.go`: **required, non-substantive
  follow-up.** `execSiteAllowlist` keys every exempted exec call site by
  `file:line`. Deleting comment lines in `init.go` shifted 16 call sites by
  a constant -58 lines; `TestNoRootContextExecUsesABareUnresolvedCommandName`
  failed until the map's keys were updated to match. This is a line-number
  data correction, not a change to what the guard checks or which sites are
  exempt — same file names, same reasons, same call sites, just the
  post-edit line numbers. No test assertion logic changed.

## Where the relocated rationale now lives

Nothing new was written to `.design/kubernetes/substrate-runtime.md` —
every piece of substrate-internal rationale cut from `init.go` was already
covered there in more detail than the code comment had:

- ForwardTermSignal / SIGTERM handling → §5.6
- WorkingDir / ResolveWorkingDir / ateapi's missing `workingDir` → §5.7
- DisablePortForwarding / egress model → §1 (the "Egress" bullet, ~L61-65)
  and §11 (Phase 2+ re-enabling an on-demand tunnel)
- RequirePrivilegeDrop / UID 0 → §8 (capability set was already there; the
  ateapi `ContainerSpec`-has-no-`user`-field *cause* of UID 0 was missing
  and is added in Round 2 below)
- exitCodePrivilegeDropRequired's "why report at all" reasoning →
  `pkg/sciontool/substrate/types.go`'s `StateInitFailed` doc comment (the
  PID-1-exit-isn't-a-failure-signal half was already there; the
  agent-info.json-fallback half was missing and is added in Round 2 below)
- resolveProjectHookPath's `EnforcedHooksDir` redirect →
  `pkg/sciontool/hooks/exec_enforced.go`'s `EnforcedHooksDir` doc comment
  (already had it; lightly trimmed for the same generic-seam framing)

## Gates run

- `env -i HOME=... PATH=... GOCACHE=/scion-volumes/gocache ... go build
  -buildvcs=false ./...` — green.
- `env -i HOME=... PATH=... GOCACHE=/scion-volumes/gocache ... make ci`
  (fmt-check, lint, check-custom, test-fast, build) — green after the
  guard_test.go line-number fix above. First run failed only on
  `TestNoRootContextExecUsesABareUnresolvedCommandName`; every other package
  passed on the first run.
- `gofmt -l` on every changed file — clean.
- `git diff` on the four changed files reviewed line-by-line: every `+`/`-`
  line is a comment, blank line, or (in guard_test.go) a string-literal line
  number — no executable line changed.

## Carve-out honored

Did not touch anything under `pkg/runtimebroker/` (sibling unit's territory
this wave). Its substrate comment volume (test files: `substrate_*_test.go`)
was not surveyed in depth beyond confirming no edits landed there — flagging
as a follow-up candidate for a future comment-cleanup pass, per the
dispatch's carve-out instructions.

## Remaining core hits naming substrate — sent to sb-em with justification

See message to sb-em for the itemized list (grouped by file, each with a
one-line reason it's an allowlisted generic-seam example or allowlisted
wiring/schema). Summary: `pkg/config/settings_v1.go`'s `V1SubstrateConfig`
(explicitly allowlisted schema, untouched); everything remaining in
`cmd/sciontool/commands/init.go`, `pkg/sciontool/hooks/*.go`,
`pkg/sciontool/services/manager.go`, and `pkg/sciontool/hub/client.go` is a
single-clause naming of substrate as the current example of a
generic seam (a boolean flag, a directory constant, a design-doc section
pointer) — not a restated paragraph of substrate's own internal rationale.

## Round 2 (post-review fixes)

The independent reviewer's fresh pass was APPROVE-after-one-fix. Two facts
had been deleted from `init.go` rather than moved, and one aggregated
pointer undercounted its own targets. Fixed as a trailing comment/markdown-
only commit, fast-forwarded on top of the first commit (no rewrite):

1. **agent-info.json fallback fact, relocated.** `reportInitFailure`'s doc
   comment pointed at `pkg/sciontool/substrate`'s `StateInitFailed` "for why
   substrate is one" (of the runtimes with no local-agent-info.json
   fallback), but `StateInitFailed`'s comment never actually said that —
   it stated the direct-Hub-report is "the primary failure signal today"
   without saying *why* it's the *only* one. Added the missing sentence to
   `StateInitFailed`'s own doc comment
   (`pkg/sciontool/substrate/types.go`): "the substrate broker does not
   read agent-info.json out of the actor the way other runtimes' brokers
   do, so that direct Hub report ... is the only failure signal that
   reaches the Hub at all here." `reportInitFailure`'s cross-reference in
   `init.go` is unchanged in destination (still points at
   `StateInitFailed`) since that's now accurate; reworded only for flow,
   net line-count neutral (no guard_test.go churn).
2. **UID-0 cause, relocated.** The pre-cleanup `RequirePrivilegeDrop`
   comment gave the cause "the ateapi ContainerSpec has no user field";
   §8 of `substrate-runtime.md` stated the UID-0 *fact* but not this
   *cause*. Added: "the ateapi `Container` spec has no `user` field, so
   there is no way to ask Substrate to start the process as anything
   else" to §8's opening paragraph.
3. **Project-log corrections** (this file, edited in place above): the
   `StateInitFailed` "(already had it)" claim was false for the
   agent-info.json half (now fixed by (1) above); the `DisablePortForwarding`
   → §7 mapping was wrong (correct: §1's "Egress" bullet + §11, not §7,
   which covers the sdsmint MITM trust-bundle detail, not the
   port-forward/autoexpose disable); the "Nothing else in `pkg/config`
   mentions substrate" claim missed `schemas/settings-v1.schema.json` and
   four schema-focused test files (all allowlisted schema/test artifacts,
   not comment prose — no edits needed, just an inaccurate claim to fix).
4. **Cheap accuracy nits folded in:** `init.go`'s aggregated
   `ResolveWorkingDir` pointer now cites `.design/kubernetes/substrate-
   runtime.md §§1, 5.6-5.7, 8` (was `§§5.6-5.7` only, undercounting
   `DisablePortForwarding`'s §1 and `RequirePrivilegeDrop`'s §8).

**Not changed this round:** the three symlink-defense blocks
(`blockClaudeDebugSymlink` / `cleanGcloudConfigForMetadata` /
`readServicesYAML` in `init.go`) — the reviewer accepted these as
generic-seam documentation belonging next to the code they protect. An
optional seam-first reword is a deferred follow-up, not this round.

**Line-number impact on `pkg/sciontool/rootexec/guard_test.go`:** none.
Every edit this round was made net line-count neutral (same number of
comment lines removed as added in each hunk), and all edits landed above
the exec-call-site region (`init.go` lines >2069) or in files
`guard_test.go` doesn't index. Re-ran
`go test ./pkg/sciontool/rootexec/...` after these edits: still green, no
line-number drift, `guard_test.go` itself untouched this round.

**Gates re-run after Round 2:** `go build -buildvcs=false ./...` and
`make ci` both green under the same `env -i` scrub +
`GOCACHE=/scion-volumes/gocache`; `gofmt -l` clean on all touched files;
`git diff --name-only` confirmed comment/markdown-only, and a
comment-stripped token diff (gofmt-normalized source with `//` and `/* */`
comments removed) confirmed token-for-token identical to the parent commit
on every touched `.go` file.

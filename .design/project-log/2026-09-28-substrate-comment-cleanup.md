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

Separately, `pkg/config`'s ~19-25 substrate hits are entirely inside
`V1SubstrateConfig` and its doc comments in `settings_v1.go` — explicitly
allowlisted schema per the dispatch ("DO NOT touch ... V1SubstrateConfig and
its schema"). Nothing else in `pkg/config` mentions substrate. So no edits
were made in `pkg/config`.

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
- DisablePortForwarding / egress model → §7
- RequirePrivilegeDrop / UID 0 / ContainerSpec capability set → §8
- exitCodePrivilegeDropRequired's "why report at all" reasoning →
  `pkg/sciontool/substrate/types.go`'s `StateInitFailed` doc comment
  (already had it)
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

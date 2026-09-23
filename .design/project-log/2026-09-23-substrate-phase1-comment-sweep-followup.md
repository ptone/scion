# Substrate Phase 1 — comment sweep follow-up

A second, smaller pass over the comment sweep, addressing three remaining
process-history parentheticals the first pass missed, a tautology in two
test assertions, and two test doc comments that described how a bug was
found rather than what the test itself checks. Comment- and test-name-only,
same as the first sweep: no identifiers, logic, strings, or error/log
messages changed in any non-test `.go` file, and no test behavior changed
anywhere.

## Remaining process-history parentheticals

- `pkg/runtime/substrate_bootstrap.go`: dropped a trailing internal-process
  citation from the sentence explaining why `externalEnvValues` isn't
  reusable for bootstrap-payload redaction. The sentence already states the
  technical gap in full ("it silently has no coverage for
  ResolvedAuth.EnvVars or ResolvedSecrets at all, which is exactly what let
  real secret values reach an unredacted error") — the citation added
  nothing beyond attribution.
- `pkg/runtime/substrate_egress.go`: replaced a citation with the actual
  technical description of the risk it stood in for — a "validate what you
  send" drift: if validation and the string actually sent were computed
  independently (e.g. one function validates, a separate one trims and
  sends), the two could disagree, and an entry like `"GitHub.COM."` could
  validate fine but be sent merely trimmed rather than fully normalized,
  which Substrate's own `HostnameRule` would then reject.
- `pkg/runtime/substrate_runtime_test.go`: reworded a parenthetical that
  attributed a design fact to an internal review artifact, to just state
  the fact — substrate is an auxiliary runtime, rebuilt on every `start`
  that isn't the default profile.

## Tautological reason-substring checks

`TestValidateEgressAllow_KnownBypasses`'s `reasonArpa` and `reasonOnion`
constants (and the near-identical checks in the standalone
`ArpaRejectionReason`/`RejectsOnion` tests) matched on the bare words
`"arpa"`/`"onion"`. Both words are already present in the *quoted entry*
inside the error message itself (e.g. `egress_allow entry "home.arpa": ...`,
`egress_allow entry "foo.onion": ...`), so the assertion would pass even if
some unrelated rejection rule fired instead of the special-use-TLD check —
it wasn't actually verifying which code path produced the error, only that
an error occurred and happened to echo the entry back. Changed both to
match the reason text instead: `"reserved for special-use"` (the `.arpa`
message) and `"Tor hidden services"` (the `.onion` message). Re-ran every
row in the consolidated table plus the two standalone tests to confirm
they still pass with the tightened check — they do, since the actual
rejection reason text always contains those phrases for these entries.

## Test doc comments

- `TestWriteBootstrapFile_EnforcesModeOnPreExistingFile` and
  `TestWriteBootstrapFile_SetsModeAndOwnerAtomically`
  (`pkg/sciontool/substrate/server_test.go`): reworded both doc comments to
  lead with what the test actually asserts (exact mode/owner/content on the
  final file, for a pre-existing file and for both the fresh- and
  pre-existing-file cases respectively), keeping the supporting technical
  detail (os.WriteFile's mode argument not applying to existing files; the
  atomic-write result being what's verifiable, not the transient window)
  as secondary explanation rather than the lead.
- `TestValidateEgressAllow_KnownBypasses`: dropped a paragraph restating
  that two row names were changed from an earlier wording — the current
  row names and their adjacent inline comment already say why
  (`foo.local`/`foo.internal` are rejected by the TLD check, not the
  suffix blocklist, because the TLD check always runs first); the doc
  comment doesn't need to also narrate that as a change from something
  else.

## Verification

- Self-check (1): `git diff dedd4c076 HEAD -- '*.go' ':!*_test.go' | grep
  '^[+-]' | grep -vE '^(\+\+\+|---)' | grep -vE '^[+-]\s*(//|$)'` — empty.
  Every non-test `.go` file changed only in its comments.
- Self-check (2), `SCION_*` unset:
  - `go build ./...` — pass.
  - `go vet` on `pkg/config/...`, `pkg/runtime/...`,
    `pkg/sciontool/substrate/...` — pass, no output.
  - `gofmt -l` on every changed file — clean.
  - `go test -count=1` on the same three package trees — all pass,
    including every row of the consolidated bypass table with the
    tightened `reasonArpa`/`reasonOnion` substrings.
  - `go test -race -count=1` on `pkg/config/...` — pass, no races.
- Self-check (3): the multi-line grep over every added line in the whole
  branch diff (`git diff c3b6e821d...HEAD -- . ':!third_party'
  ':!.design'`, added lines only, matched against
  `review|round\s*[0-9]|sb-|finding`) returns 23 hits, all genuine
  non-citation uses, reviewed individually:
  - 22 are `findings.md` — this project's own design/spec document,
    cited the same way as `phase1-spec.md` throughout (e.g.
    `findings.md §6`, `findings.md D3`). This is a design-doc reference,
    not an internal review-process artifact, and stays.
  - 1 is plain English: "Reached the filesystem root without **finding**
    an existing ..." (the verb, not the noun).
  No `round`, `sb-`, or `review`-as-citation hits remain anywhere in the
  branch's added lines outside `third_party/` and `.design/`.

No `go.mod`/`go.sum` changes. No production behavior changed.

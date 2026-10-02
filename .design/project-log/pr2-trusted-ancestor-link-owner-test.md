# PR2: close the security-audit test-coverage gap on the trusted-ancestor link-owner check

**Date:** 2026-10-02
**Agent:** dev-pr1-reconcile
**Branch:** scion/dev-pr1-reconcile (this entry); work performed on a local `pr2` branch tracking `scion/substrate-pr2-restack-wip`

## Context

PR2's trusted-ancestor-follow fix (an earlier round on this same stack) decides whether to follow a symlink in a secret's directory chain via `trustedAncestorSymlinkAt`, which requires both the symlink's own owner and its containing directory's owner to be the trusted uid, plus no group/other write bit on the directory. The pure decision function, `symlinkTrustedByStat`, was already table-tested against every individual condition. A follow-on security audit found the call site itself was not: mutating `trustedAncestorSymlinkAt`'s call from `symlinkTrustedByStat(linkSt.Uid, dirSt.Uid, ...)` to `symlinkTrustedByStat(dirSt.Uid, dirSt.Uid, ...)` — silently dropping the requirement that the symlink's own owner be trusted — left every existing dirfd and stagedsecrets test green. Independently reproduced before fixing.

The reason no existing test caught this: an unprivileged test process can only ever create a symlink owned by its own real uid, the same uid that already owns the directory it creates the symlink in. There is no way to produce a real on-disk fixture where the link's own owner differs from its (otherwise trustworthy) containing directory's owner without actual root.

## What changed

Added `fstatatTrustedAncestor`, a package-level var defaulting to `unix.Fstatat`, as `trustedAncestorSymlinkAt`'s own call site for stat'ing the symlink's directory entry (production leaves it at its default always). A new test overrides this var with a wrapper that calls the real `unix.Fstatat` and then overwrites only the reported owner field to a known-untrusted uid, while a real directory and a real symlink into it — both genuinely trustworthy except for this one fabricated field — exercise the actual walk end to end. The test asserts both that the walk refuses and that nothing is created past the refusal.

The production diff is exactly the var declaration and the one call substitution; confirmed by grep that nothing outside a `_test.go` file ever assigns the var.

## Verification

Reproduced the finding first (the call-site mutation left the whole suite green), then confirmed the new test is discriminating: applying the same mutation made the new test fail with every other test in both packages still green, and restoring the production file (from a saved clean copy, confirmed via an empty `git diff`) made it pass again. Full gate suite green: `gofmt`, `go build`/`go vet`, `go test -p 2` on `pkg/sciontool/dirfd`, `pkg/stagedsecrets`, and `cmd/sciontool/commands`, darwin amd64/arm64 cross-compiles, and `golangci-lint` scoped to `pkg/sciontool/dirfd` against both `97ded44d` and `origin/main` as the diff base — 0 issues both ways.

Pushed to `scion/substrate-pr2-restack-wip` at tip `e53858ef0acf6ec4a24f3f70509a7201658e51de`. The real `scion/substrate-pr2` branch was not touched. Full report: `preflight/pr2-l1-fix.md` (scratchpad).

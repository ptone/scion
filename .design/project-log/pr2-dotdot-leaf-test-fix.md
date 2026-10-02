# PR2 round-3 fix: make the dot/dot-dot leaf rejection test exercise its guard

**Date:** 2026-10-02
**Agent:** dev-pr1-reconcile
**Branch:** scion/dev-pr1-reconcile (this entry); work performed on a local `pr2` branch tracking `scion/substrate-pr2-restack-wip`

## Context

PR2's symlinked-ancestor fix (an earlier round on this same stack) added `stagedsecrets.writeAs` guards refusing a file-secret `Target` ending in a path separator, or whose leaf is `"."` or `".."`, before resolving its directory — closing a silent-wrong-write hole where `filepath.Base` strips a trailing separator and `filepath.Clean` silently resolves a `".."` leaf elsewhere. A fresh review on that fix round found the regression test for the dot/dot-dot half, `TestWriteAs_RejectsDotDotTarget`, was vacuous: it built its target with `filepath.Join(targetDir, "secrets", "..")`, and `filepath.Join` cleans its own result — collapsing the target back to `targetDir` itself before `writeAs` ever saw a `".."` leaf. The test was asserting a refusal for an ordinary, already-covered path, not exercising the guard it claimed to pin. Independently reproduced: deleting the guard block in `stagedsecrets.go` left the test green.

## What changed

Replaced the single test with `TestWriteAs_RejectsDotAndDotDotTarget`, a two-row table (`pkg/stagedsecrets/stagedsecrets_test.go`) building both the dot-dot leaf (`targetDir + "/secrets/x/.."`) and dot leaf (`targetDir + "/secrets/."`) targets via plain string concatenation — confirmed first that `filepath.Base` does not collapse these segments the way `Join`/`Clean` do, so the leaf survives to the guard's own `switch filepath.Base(fs.Target)` check. Each row asserts both that `writeAs` returns an error and that nothing was created at the target's parent directory (`os.Stat` returns `os.IsNotExist`) — the second assertion is what would catch a silent wrong-destination write if the first one ever passed for the wrong reason.

Production code (`stagedsecrets.go`) was not touched; the guard itself was already correct, only its test coverage was wrong.

## Verification

Reproduced the original vacuous-test finding, then verified the fix is discriminating: temporarily deleted the `"."`/`".."` switch block, rebuilt, and confirmed both new table rows FAIL (`writeAs() = nil error`), with a throwaway standalone check confirming the actual silent failure mode — a *file* named `secrets` (containing the staged secret's own content) silently created where a directory was expected. Restored the production file from a saved clean copy, confirmed the diff was empty, and re-ran: both rows PASS. Full gate suite green: `gofmt`, `go build`/`go vet` on the whole module, `go test -p 2` on `pkg/stagedsecrets`, darwin amd64/arm64 cross-compiles, and `golangci-lint` scoped to `pkg/stagedsecrets` against both `97ded44d` and `origin/main` as the diff base — 0 issues both ways.

Pushed to `scion/substrate-pr2-restack-wip` at tip `df823b04f8367223d2c6277a597827494e15bce1`. The real `scion/substrate-pr2` branch was not touched. Full report: `preflight/pr2-h56-fix3.md` (scratchpad).

# Brief: remove the `--broadcast` / `--all` flags from `scion message`

## Why
ptone: "agents using the --broadcast incorrectly - we want to entirely remove that flag."
The failure mode is that `--broadcast` sits on the *normal send command*, so an agent
reaches it by habit or by copying a stale example and sprays every running agent.
Removing it forces the deliberate, separate `scion broadcast` command.

## Current state (measured at tranche-g a29d75f77, do not re-derive)
- `cmd/message.go:1213-1214` declares `--broadcast/-b` and `--all/-a`. Both are already
  deprecated and `MarkHidden` (`:1225`), and both are still fully functional.
- `cmd/message.go:519`: `msg.Broadcasted = msgBroadcast || msgAll`.
- `cmd/message.go:67-68`: entries in the `deprecationReplacements` table.
- Roughly ten `--broadcast or --all` mutual-exclusion errors at `:138`–`:267`.
- `cmd/message_deprecation_test.go` asserts the flag warns AND still delivers
  (`:175`, `:198`, `:219`, `:422`, `:446`).
- `resources/platform_skills/scion-messaging/SKILL.md` documents it — this is very
  likely where agents learn it.
- `docs-site/src/content/docs/reference/cli.md` and
  `docs-site/src/content/docs/hosted/user/messaging.md` also document it.

## In scope
1. Delete `--broadcast/-b` and `--all/-a` from `scion message`. **Both.** Removing only
   `--broadcast` leaves the identical hazard on `--all`.
2. Remove their `deprecationReplacements` entries and simplify the now-dead
   mutual-exclusion branches. Do not leave dead `msgBroadcast`/`msgAll` variables.
3. **Failure must be actionable.** A bare cobra `unknown flag: --broadcast` is a dead end
   for an agent. Invoking `scion message --broadcast ...` must fail with a message that
   names `scion broadcast` as the replacement. Mechanism is yours; the requirement is the
   behaviour. Add a test that asserts the replacement command is named in the error text.
4. Update `SKILL.md` and both docs-site pages. The skill matters most — it is the agent's
   source of truth.
5. Update `cmd/message_deprecation_test.go`. Tests currently assert the flag still
   delivers; that contract is being deliberately removed. **Rewrite them to assert the new
   refusal — do not silently delete them.** A deleted test is indistinguishable from a lost
   one at review time.

## Explicitly OUT of scope — do not touch
- The `scion broadcast` command (`cmd/broadcast.go`). It stays. It is the replacement.
- `msg.Broadcasted = true` in `handleProjectBroadcast` (server side). This is a **REQUIRED
  security gate** asserted by `hack/checksecuritymarkergates/main.go:172`. Without it a
  client sending `Broadcasted=false` walks a broadcast through the DM path with a spoofed
  sender. If you find yourself editing this, stop and message me.
- The `Broadcasted` struct field and every consumer under `extras/` (discord, slack,
  telegram, agent-viz, broker-log) and `cmd/server_attribution_report.go`.

## Verification — all required, report actual output
- `go vet ./...` — **mandatory, non-negotiable.** It typechecks test files in every
  package regardless of build tags. The last merge on this branch shipped uncompilable
  test code because `go build` and `make test-fast` both pass without compiling them.
- `make test-fast`, plus the structural gates: `compat-literals`, `check-authz-guards`,
  `check-conversation-upsert-guard`, `check-security-marker-gates`, `fmt-check`.
- `go test ./cmd/ ./pkg/hub/` (full, with sqlite — the blocking gate excludes ~31% of
  test files, so this is where real coverage lives).
- Known-environmental failures in these containers: `TestDeleteStopped_RequiresGroveContext`
  (cmd) and six `pkg/config` project-path tests. Those seven are expected. **Anything else
  red is a finding — report it to me, do not tune it away.**

## Process
- Base on `scion/tranche-g` @ `a29d75f77`.
- Report per-file `git diff --numstat` before you push.
- **Never make a gate pass by weakening the gate.** Report any red to me.
- Do not use `git add -A`.

## Push
`origin` inside your container does NOT point at the right repo. Use exactly:
```
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" HEAD:scion/ca-msg-bcast
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" scion/ca-msg-bcast
```
Verify with the separate `ls-remote` — do not trust the push output alone.
Push to `scion/ca-msg-bcast` ONLY. Never push to `scion/tranche-g` or `main`.

Message `agent:ca-msg-arch` when the branch is pushed and gates are reported.

# DEF-160 P5 — routing error-text pass

You are a **developer** agent. Fresh context, small scoped task. Read this whole
brief before touching anything.

## Background you need and nothing more

Scion has two agent-facing messaging interfaces: `scion message` for sending, and
a structured envelope for receiving. A refactor (DEF-160) has just landed on
`scion/tranche-g` that makes a **conversation reference a complete address on its
own**. The human principal's ruling, verbatim:

> "group conv:id is the recipient. message should require no more than required
> to clearly route. and error clearly with what is required if not met. an agent
> can choose to mention a recipient in message text if they want that recipient
> to be drawn to a group message."

The routing behaviour is done and merged. **P5 is the second half of that
sentence: "error clearly with what is required if not met."** Several error
messages still describe the pre-refactor address space. An agent that hits one of
them is told to do the wrong thing.

This is a **text-and-tests-only** phase. You are not changing routing behaviour.

## Base

Branch from `scion/tranche-g` at `31dfbb414`. Work on a new branch
`scion/ca-msg-p5text`.

## The three sites

### P5-1 — `pkg/hub/handlers_agent_messaging.go:215` (highest value)

```go
	if recipientID == "" && recipient == "" && req.ConversationRef == "" {
		ValidationError(w, "recipient is required — specify a user with 'user:<name>' or 'user:<email>'", nil)
		return
	}
```

This fires **only when all three of recipient, recipientID and conversation_ref
are empty** — i.e. exactly the "not enough information to route" case the ruling
names. The text offers only `user:` forms. It omits `conv:<id>` and
`@<agent-slug>`, both of which are accepted addresses on this endpoint.

Rewrite it to enumerate the *actual* accepted address forms. Note that
`user:<name>` is itself misleading — see `:203`, which refuses a non-UUID,
non-email token with "Names are not unique and cannot be resolved." The
requirement text should not advertise a form the very next guard rejects.

Do not change the guard's condition. Text only.

### P5-2 — `pkg/messages/message_group.go:178`

```go
	if strings.Contains(s, ":") {
		prefix := s[:strings.Index(s, ":")]
		return GroupRecipient{}, fmt.Errorf("unknown recipient prefix %q in group[] element %q", prefix, s)
	}
```

`group[conv:<uuid>]` lands here and gets a generic "unknown recipient prefix"
error. But `conv:` is not unknown — it is a valid address that is simply not a
`group[]` *member*, because a conversation is a whole address, not one recipient
among several. Special-case the `conv:` prefix with a message that says so and
names the remediation (send to the conversation directly, do not wrap it in
`group[]`).

Leave the generic branch in place for genuinely unknown prefixes.

Existing test at `pkg/messages/message_group_test.go:196` asserts on
`"unknown recipient prefix"`. Check whether that case uses a `conv:` prefix; if
it does not, leave it alone. **If it does, that is a finding — report it to me
before changing the assertion.**

### P5-3 — `pkg/messages/types.go:197`

```go
	if m.Recipient == "" {
		return fmt.Errorf("recipient is required")
	}
```

Generic struct validation on a shared path. **Assess and recommend; do not change
it unilaterally.** Specifically: find its callers and tell me whether this error
can surface to an agent as routing guidance, or whether it is only ever an
internal invariant failure. If the latter, leave it and say so. A vaguer message
on a path no agent sees is not worth the blast radius.

## Method constraints — these are not optional

1. **Every function or symbol you name in a report carries its `file:line`.** If
   you cannot produce the line without searching for it, you know the naming
   convention, not the function.
2. **Never make a gate pass by weakening the gate.** Any red goes in your report
   to me, not tuned away. That includes a test whose assertion your text change
   breaks — report it, propose, wait.
3. **Do not strip `!no_sqlite` build tags** to make tests run.
4. **Do not `git add -A`.** The workspace is shared. Add named paths only.
5. Use `GOCACHE=/tmp/gocache-p5` to avoid contending on the shared cache.
6. Run tests with `-v -count=1`. When you report a pass count, **state the
   counting rule you used** — `grep -cE '^--- PASS:'` counts top-level functions,
   `grep -cE '^ *--- PASS:'` includes subtests. The two differ and the difference
   has cost us a round-trip before.
7. `go test -run 'Pattern'` with a pattern that matches nothing prints `ok` and
   exits 0. A green from a `-run` you have not confirmed matches something is not
   evidence.
8. `gofmt -l` must be clean on every file you touch. Note that
   `pkg/hub/handlers_agents_core.go` and `pkg/hub/web_test.go` are **already**
   unformatted on this base — that is pre-existing and not yours.

## Tests

Add or extend tests so that each text change is asserted on. An error-message
change with no test asserting the new text will be silently reverted by the next
person who "improves" it.

For P5-2, assert the new `conv:`-specific text **and** assert that the generic
branch still produces the generic text for some other unknown prefix. A
special-case that swallowed the general case would otherwise pass.

## Report to me (`ca-msg-arch`) when done

- Branch and SHA.
- `git diff --numstat <base> HEAD`, per file.
- Test command run verbatim, including tags and `-run` pattern; pass/fail counts
  with the counting rule stated.
- `gofmt -l` result on changed files; `go vet` result.
- Your P5-3 recommendation with the caller evidence behind it.
- Anything you found that is not in this brief. That section is usually the
  valuable one.

## Push

`origin` inside your container does **not** point at the right repo. Push
explicitly:

```sh
export GITHUB_TOKEN=$(cat /scion-volumes/scratchpad/transition-github-token.txt)
git push "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  HEAD:refs/heads/scion/ca-msg-p5text
```

Then verify separately — do not trust the push output alone:

```sh
git ls-remote "https://x-access-token:${GITHUB_TOKEN}@github.com/ptone/scion.git" \
  scion/ca-msg-p5text
```

Do **not** push to `scion/tranche-g` or to `main`. I do the merge.

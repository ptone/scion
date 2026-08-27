# Brief: document the one missing `deploy-instance` flag

Author: sn-impl-arch (architect). Date: 2026-08-27, 16:10. Task #72. Raised by ptone.

You are the developer. I designed this; I do not implement it. **Read the whole brief first.** If
what you find contradicts it, **stop and message me.** Several developers corrected me today and
every one of them was right.

**This is a small change with a large temptation attached. The temptation is in §4.**

---

## 1. Start here — two things that will bite you

**1. Your `scion` binary is probably stale.** Mine is: `scion deploy-instance` returns
`unknown command "deploy-instance"`. That is *not* "the command does not exist" — it is an old
binary earlier on `PATH`. If you need to run it, build from source and **prepend**:

```bash
go build -tags no_embed_web -o ./scion ./cmd/scion/
mkdir -p "$(go env GOPATH)/bin" && mv ./scion "$(go env GOPATH)/bin/scion"
export PATH="$(go env GOPATH)/bin:$PATH"
```

You should not need to run it for this task. Read the source instead: `cmd/deploy_instance.go`.

**2. Base off current upstream `main`, not the fork's `main`, and not any tier branch.**

```bash
git fetch https://github.com/GoogleCloudPlatform/scion.git main
git checkout -b scion/sn-flagdoc FETCH_HEAD
```

Upstream `main` has moved several times today. **Fetch fresh; do not trust any SHA I quote.** Push
to the fork: `git push origin scion/sn-flagdoc`. Only remote is `origin` = `ptone/scion`.

## 2. The gap

`cmd/deploy_instance.go` defines **nine** flags: `name`, `project`, `image`, `region`, `cpu`,
`memory`, `admin-email`, `service-account`, `image-registry`.

The published tutorial —
`docs-site/src/content/docs/hosted/single-node/hub-setup-cloudrun.md` — documents **eight**.
**`--image-registry` appears nowhere in the file.** Confirmed by grep: zero occurrences.

Verify both halves yourself before you write anything. If you count differently, **stop and tell
me** — my count is from `StringVar`/`BoolVar` declarations and I may have missed a flag defined
elsewhere.

## 3. What `--image-registry` actually does

Read `cmd/deploy_instance.go` around the flag declaration and its use (search `imageRegistry`), and
the derivation helper it calls. What I found, which you should confirm:

- Declared as `"Override image registry (default: derived from --image)"`. **Not required.**
- It sets `SCION_IMAGE_REGISTRY`, which **the broker needs in order to pull agent images**.
- If it is empty, the value is derived from `--image`. If derivation fails, the error says
  *"Use `--image-registry` to set it explicitly"*, and a second guard rejects a derived value that
  does not look like a hostname.

**Why this is worth a doc entry and not just a table row:** when this value is wrong, the deploy
still succeeds and the failure lands much later, on agent creation. That is tier defect #38 — the
one-command deploy came up healthy and could not start a single agent. A flag whose absence breaks
something *other than the command you ran* deserves to be findable.

## 4. THE TEMPTATION — do not oversell it

The obvious execution is to write this up as an important flag operators should set. **That would
be wrong and would make the page worse.**

The happy path **does not need it**. Every deploy we have run derived the registry correctly from
`--image`. If you present it as something the reader should think about, you add a decision to a
tutorial whose whole value is that it removes decisions.

Pitch it as **an escape hatch**: what it is, when derivation fails, that the error text names it.
One table row plus at most one or two sentences near the existing **"Container image"** section.
**If your diff is more than ~6 added lines, you have overwritten it.**

Equally, do not undersell it into meaninglessness — "override the image registry" alone tells the
reader nothing they could not guess. Say what breaks when it is wrong.

## 5. Constraints

- **One file.** `docs-site/src/content/docs/hosted/single-node/hub-setup-cloudrun.md`. Nothing else.
- **Do not restructure, retitle, or reorder anything.** This page merged less than an hour ago.
- **Do not touch** the `:::caution[Always specify harnessConfig]` block, the troubleshooting entry
  for `harness-config "antigravity" not found`, the `:::caution[Temporary workaround]` block, the
  `PATH` prepend line, or any IAP wording. All are load-bearing and each was fixed for a measured
  reason today.
- **Fully qualify every issue/PR number** you write: `ptone/scion#NNNN` or
  `GoogleCloudPlatform/scion#NNNN`. We measured a **100% collision rate** across `#1270`–`#1320` —
  every number exists in both repositories. Before committing, grep for `#1[0-9]{3}` not preceded by
  a repo slug; the answer must be zero. **I broke this rule myself today**, so grep rather than
  trust care. If you mention defect #38, that is our internal task number, not a GitHub issue —
  either omit it or say plainly that it is an internal reference.
- **Do not open a PR.** ptone opens upstream PRs. Push the branch and stop.
- **Do not rebase or force-push** anything. Do not touch `#1265`/`#1266`.
- **Do not deploy or delete anything.** `e2e-omni`, `e2e-walk-r2`, `iap-demo`, `q2-control`,
  `sn-ready`, `sn-adminseed-t`, `sn-adminfix-t`, `sn-step6`, `sn-walk` are **do-not-delete**;
  `sn-ready` is ptone's live Instance.
- **Do not fix any other defect you notice.** Tell me instead.

## 6. Report back

Message `sn-impl-arch` with: the branch name and commit SHA, your independent flag count (source vs
doc), the exact text you added, confirmation that the unqualified-ref grep returned zero, and
confirmation that `git diff --stat` shows one file. Tell me anything here you think I have wrong.

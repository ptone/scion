# Brief: spike-uds-b — Tier B, validate the §4.4a replacement

**Dispatched by** `sn-impl-arch`, 2026-08-25, following `spike-uds`'s Tier A result.
**You are a verification spike.** You run tests and record results. No production
code, no branches, no PRs.

## Step 0 — before any gcloud command

```bash
bash /scion-volumes/scratchpad/update-gcloud.sh   # 2–4 min
```

Agent containers ship a stale gcloud; anything older than ~572 is missing
`gcloud alpha run instances` entirely, and the absence looks like a permissions or
project problem. Guide: `projects/single-node/gcloud-update-guide.md`.

## Where this stands

Tier A is done and the answer is final: **AF_UNIX sockets do not cross the sandbox
boundary in either direction.** The original tmux-socket design is dead. The
replacement — §4.4a — keeps tmux entirely inside the sandbox and carries each
operation in via `sandbox exec`.

Tier A's bonus check already showed `has-session`, `send-keys` and `capture-pane`
work through `sandbox exec`. **That is the easy half.** The hard half — an
interactive PTY, resize, and latency — is untouched, and it is what gates P4.

## Read first

- `/scion-volumes/scratchpad/projects/single-node/cloudrun-instances-sandboxes.md`
  — **§4.4a** (the design you are validating), **§10a Tier B** (your tests, T4–T11),
  **§3.2c** (sandbox CLI facts).
- `/scion-volumes/scratchpad/projects/single-node/ac0-results.md` — Tier A's results
  and format. Append yours; match the format.
- `/scion-volumes/scratchpad/projects/single-node/deploy-instance-with-sandbox.md`
  — standing up an Instance. Raw REST is for spikes like you.

## Ground rules — carried over from Tier A, still binding

1. **Real Cloud Run Sandbox on a real Cloud Run Instance.** Not `unshare`, not local
   Docker, not a locally installed `runsc`. This project has been burned once by a
   substitute-mechanism PASS.
2. **Write each pass/fail predicate down before running the test.**
3. **Characterize failures exactly** — error text, errno, immediate vs hang.
4. **Capture raw output**, not just conclusions.
5. **Report per-test verdicts.** Never average them.

## One change from Tier A: use a representative image

Tier A used `python:3.11`, which was fine for raw socket tests. **T4 is a question
about our actual image**, so use the omni image if you can get it, and if you cannot,
say clearly which image you used and treat T4 as provisional. Do not report "`script`
is present" on the basis of an image we do not ship.

## Tests — T4 through T11 in §10a

The ones that carry the most risk:

- **T4 — is `script` (util-linux) in the image?** Remember **`PATH` is empty inside a
  sandbox** (§3.2c), so use the absolute path `/usr/bin/script`. A "command not
  found" caused by empty PATH is not the same finding as a missing package; do not
  conflate them.
- **T5 — the negative control.** `sandbox exec <id> -- tmux attach` with a
  launcher-side PTY, expected to fail with *"open terminal failed: not a terminal"*.
  **Do not skip this as obviously broken.** If it unexpectedly *works*, the PTY
  propagates and §4.4a's entire workaround is unnecessary — one of the most valuable
  results available to you.
- **T6 — the fix.** `sandbox exec <id> -- /usr/bin/script -qfc 'tmux attach -t scion'
  /dev/null`. Pass = tmux UI renders, keystrokes echo, `C-b d` detaches cleanly and
  the session survives (`has-session` still returns 0).
- **T7 — resize.** `tmux refresh-client -C 120x40` out-of-band; confirm with
  `tmux display -p '#{pane_width}'`.
- **T8 — latency.** p95 keystroke echo **< 150 ms** over one persistent exec. Report
  the actual distribution, not just pass/fail. **This number matters beyond P4**: it
  is the only remaining argument for ever revisiting the dead socket design.
- **T9 — teardown.** `sandbox delete --force` while an exec is attached. Pass = the
  launcher-side process exits promptly and non-zero, no orphan, no hang. This is
  where P4 would leak a process per killed agent.
- **T10 — idle stability.** Exec attached, idle 30 min, still responsive.
- **T11 — `sandbox exec -h | grep -i tty`.** Trivial; a hit deletes the `script`
  wrapper entirely.

## Credentials

Metadata server, no key file. Container SA
`scion-my-grove@deploy-demo-test.iam.gserviceaccount.com` holds Token Creator on
`scion-instance-gym@serverless-team-scion.iam.gserviceaccount.com`. Project
`ptone-experiments`, region **`us-east4`** (`us-central1` is capacity-exhausted).

**Do not print access tokens to stdout.** **Delete your Instance when done** — the
Tier A spike did, and it is the norm here.

## Reporting

Append to `ac0-results.md`, then message `sn-impl-arch` with per-test verdicts and
the T8 latency distribution. Message ptone (`user:ptone@google.com`, channel
`discord`, thread `1534555192450748456`) with the headline.

**Raise a blocker immediately** if you cannot get an Instance up or cannot obtain the
omni image — do not silently work around either.

## Termination

Complete when T4–T11 are recorded and reported. Do not implement anything in P4; that
scope belongs to someone else and is not yet dispatched.

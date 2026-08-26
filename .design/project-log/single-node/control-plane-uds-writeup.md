# Driving a long-lived interactive process inside a Cloud Run Sandbox

**Context for Cloud Run platform engineers.** Extracted from the Scion single-node
design doc, 2026-08-25. Self-contained; no Scion knowledge assumed.

---

## 1. What we are building

A Cloud Run **Instance** runs a control-plane server and acts as a **sandbox
launcher**. Each AI coding agent gets its own **Cloud Run Sandbox**, created with
`--rootfs /` so it inherits the launcher's filesystem read-only, plus explicit
`--mount type=bind,…  --write` for the paths the launcher must read back.

Inside each sandbox, the agent process runs under **`tmux`**. That is not incidental:
tmux is what makes the agent *survivable and steerable*. It gives us

- **persistence** — the agent keeps running when no human is attached;
- **injection** — deliver a task to a running agent via `tmux send-keys`;
- **observation** — scrape recent output via `tmux capture-pane`;
- **liveness** — `tmux has-session`;
- **attach** — a live browser terminal onto a running agent.

Every one of those operations is initiated **from the launcher**, targeting a process
**inside** the sandbox.

## 2. The original design (now ruled out)

`tmux` places its control socket at `$TMUX_TMPDIR/tmux-<uid>/<name>`. `TMUX_TMPDIR`
was an unused lever for us, so the plan was to **relocate the socket onto a bind
mount and speak to it from the launcher**:

1. Launcher creates `<agentDir>/tmux/`.
2. Sandbox starts with that directory bind-mounted and `TMUX_TMPDIR` pointing at it.
3. The tmux **server** runs inside the sandbox and binds
   `<mount>/tmux-1000/default`.
4. The launcher sees the same path on its side of the bind mount and runs tmux as a
   **plain local process**:

| Operation | Command, run in the launcher |
|---|---|
| attach (PTY) | `tmux -S <sock> attach -t scion` |
| send task | `tmux -S <sock> send-keys -t scion:agent …` |
| liveness | `tmux -S <sock> has-session -t scion` |
| logs | `tmux -S <sock> capture-pane -p -t scion:agent` |

**Why this was attractive.** The sandbox CLI has `run`, `exec`, `delete`, `do`,
`fork`, `tar` — but no `attach`, `logs`, `list`, or `inspect`. This one mechanism
substituted for all four missing verbs at once, and it kept the sandbox CLI **out of
the latency-sensitive interactive path** entirely: an attached terminal became a
local socket connection, not a nested process.

## 3. Why it is ruled out

**Answer from the Cloud Run platform team, 2026-08-25:**

> *"This won't work. We would need to run runsc/gvisor with `--host-uds` enabled
> which we don't."*

**AF_UNIX sockets do not cross the sandbox boundary.** The design is dead as
specified.

### Note on how we nearly got this wrong

Worth recording, because the failure mode is general. An early spike tested the
mechanism against an **`unshare` mount namespace** and reported PASS. That is close
to no evidence: `unshare` shares the host VFS outright, so the socket is trivially
the same inode. gVisor proxies the filesystem through a gofer, and that is precisely
where the two diverge. **A positive result from a substitute isolation mechanism
should not be read as a result about gVisor.**

### Scope of the finding

This is a **socket** finding, not a **mount** finding. We verified separately that
bind mounts work correctly in both directions — files written inside an explicitly
mounted, `--write` directory are visible to the launcher, and vice versa. Only
`AF_UNIX` fails. (Writes to *inherited* `--rootfs /` paths correctly land in the
private overlay and are invisible to the launcher, as documented.)

## 4. What we are doing instead

**Reframing:** the original design tried to move the *socket* across the boundary.
Nothing needs to cross except a *command*. tmux client and server both stay
**inside** the sandbox; the socket never leaves; `sandbox exec` carries each
operation in.

| Operation | Command from the launcher |
|---|---|
| send task | `sandbox exec <id> -- tmux send-keys -t scion:agent …` |
| liveness | `sandbox exec <id> -- tmux has-session -t scion` |
| logs | `sandbox exec <id> -- tmux capture-pane -p -t scion:agent` |

Those three are non-interactive — `--stdin/--stdout/--stderr` pipes are sufficient —
so **agent control is fully preserved**. Only the live terminal is affected.

### The interactive terminal, and the trap in it

`sandbox exec` has **no `-t`/`--tty` flag**. Allocating a PTY on the launcher side
gives the PTY to the **`sandbox` CLI process**; the process **inside** still sees
pipes, so `tmux attach` fails with *"open terminal failed: not a terminal"*. The
launcher-side code looks correct and the failure surfaces one layer in.

Our workaround is to allocate the PTY **inside** the sandbox and let pipes carry the
byte stream:

```
sandbox exec <id> -- script -qfc 'tmux attach -t scion' /dev/null
```

Terminal resize needs an out-of-band path as well, since SIGWINCH does not propagate
across pipes: we issue `sandbox exec <id> -- tmux refresh-client -C <W>x<H>` per
resize event.

### Considered and rejected: a TCP shim

`socat TCP-LISTEN:…,fork UNIX-CONNECT:<tmux.sock>` inside the sandbox, exposed via
`-p/--publish`, with the launcher connecting over TCP. It would work, but it adds a
listening port and a socat process per agent, and puts the tmux control channel on a
network surface purely to avoid a process spawn. Held in reserve in case `sandbox
exec` latency proves unacceptable.

## 5. Questions back to the platform team

Neither is blocking — we have a workable path — but both would simplify it.

1. **Is a `--tty` / `-t` flag on `sandbox exec` plausible?** If it is on the roadmap
   we would use it and drop the `script` wrapper. Interactive-exec-into-a-sandbox
   seems likely to be a common need beyond our case.
2. **Any guidance on `sandbox exec` latency for a long-lived interactive stream?**
   This is one persistent exec rather than per-keystroke spawns, so we expect it to
   be fine, but the original design deliberately kept the CLI out of the interactive
   path and the replacement cannot. We will measure regardless.
3. **Lower priority — is `--host-uds` something that could ever be enabled**, or is
   it ruled out on security grounds for the foreseeable future? We are not asking for
   it; we would just like to know whether to treat the door as closed permanently
   when planning further out.

## 6. Impact

**Re-scope, not redesign.** The original design named this exact fallback, so the
replacement was pre-considered rather than improvised. Worst case — if the `script`
PTY trick also fails — we refuse interactive `attach` and keep task delivery and
output scraping working over non-interactive exec. That costs one feature, not the
capability.

**The answer arrived before anything was built on it**, which is the main reason this
is cheap. Two design decisions died on the same sentence: this one, and a proposal to
carry our internal HTTP API over a unix socket into the sandbox.

# Single-node hosted: packaging & onboarding strategies

**Status:** proposal / RFC
**Date:** 2026-08-05

## Framing

**Single-node hosted** already exists as a named tier in `GLOSSARY.md` ("Local → Workstation
→ Single-node hosted → HA hosted"), and `scripts/starter-hub/` is its only realization. The
ask — "something a user could easily deploy to the cloud on a single VM" — is therefore not a
new mode. It is: *make the existing single-node hosted tier deliverable to a user rather than
to a developer.*

This matters for scoping. No new vocabulary, no new config axis, no new server mode flag. The
`--hosted` flag, `sqlite` driver, combo server, and embedded broker all already describe this
deployment. What is missing is a delivery artifact and a defensible onboarding path.

The starter-hub scripts stay as-is. They are a *development* deployment tool — they exist so a
maintainer can push a branch and see it running in 90 seconds, and the in-dashboard
`rebuild-server` maintenance action depends on the source checkout. Nothing below removes that.

## What actually blocks a user today

The friction splits into three independent problems that have been conflated. They can be
sequenced separately, and the largest one is not the packaging.

### Problem 1 — Delivery (medium)

`gce-start-hub.sh` clones the repo to the VM and builds from source: Go 1.23, Node 20,
`make web` (npm install + vite), `go build ./cmd/scion`. Cloud-init additionally installs
build-essential, certbot, Docker, Caddy, and a now-vestigial `nats-server`. The deploy flow
also does `git push origin main` locally first (`gce-start-hub.sh:114`) — the VM pulls from
GitHub, so **you must own a pushable fork to deploy**.

This is fixable and is what "package it lighter" refers to.

### Problem 2 — Onboarding config surface (large — the real barrier)

Before anything works, a user must supply:

| Requirement | Why | Avoidable? |
|---|---|---|
| 2–4 OAuth client ID/secret pairs | Separate Web and CLI clients × Google/GitHub (`hub_config.go:304-313`). Web redirect URI is `BaseURL + /auth/callback/<provider>`, so **you must know your public hostname before you can register the client** | Yes — see Track 0 |
| Registered domain + NS delegated to Cloud DNS + `roles/dns.admin` | certbot DNS-01 wildcard (`gce-certs.sh:86`) | Yes |
| `SCION_IMAGE_REGISTRY` | Hard startup gate: `requireImageRegistryForBroker` (`server_foreground.go:2618`) refuses to boot the broker without one. There are **no published Scion images anywhere** — `.github/workflows/build-images.yml` is `workflow_dispatch`-only and never pushes | Yes |
| `SCION_HUB_STORAGE_BUCKET` (GCS) | Preflight hard-fails without it | Yes — local storage provider exists |
| `SESSION_SECRET` | Preflight hard-fails; otherwise random per boot and all sessions/agent JWTs die on restart | Yes — autogenerate + persist |
| ~11 project IAM roles, 9 GCP APIs | provisioning script | Partly |

Realistic minimum today: *a GCP project, a delegated domain, 2–4 OAuth registrations, a GCS
bucket, a container registry you have populated yourself, and a git remote you can push to.*

Note the chicken-and-egg at the center: you cannot log in without OAuth, you cannot register
OAuth without knowing your hostname, and there is no other way to get an admin session on a
hosted hub. There is no password auth, no static admin token, no bootstrap credential. Invite
codes gate the allow-list but still require OAuth behind them. Proxy auth is IAP-only (the
`header` provider is a stub and `TrustedProxies` is never populated from config), and IAP
requires an HTTPS load balancer — not available on a plain VM.

### Problem 3 — Containerizing the combo server is not free (medium, sharp)

Worth stating up front because it constrains the packaging choice. Cloud Run already runs
hub + embedded broker in a container, so the *hub* half is proven. The *broker* half is not,
on Docker:

- **Bind-mount path mismatch (the blocker).** The broker computes workspace paths from its own
  `os.UserHomeDir()` and passes them to dockerd as `-v` sources
  (`workspace_backend_local.go:76`, `common.go:203-251`). If the broker is in a container,
  dockerd resolves those paths *on the host*. Docker does not error — it creates an empty
  root-owned dir and mounts it. Agents start with a blank `/workspace`. There is **no path
  translation anywhere in the codebase** (I searched for `HOST_PATH`, `hostRoot`, prefix
  rewriting — nothing). The broker also *writes* to these paths (agent tokens, shared dirs,
  git clones), so it is not just a naming problem.
- **`os.Getuid()` is advertised to agents as `SCION_HOST_UID`** (`common.go:310`) — wrong
  unless the container uid matches the host file owner.
- **No docker CLI in any hub image**, and the runtime shells out to `docker` rather than using
  a client library. Relatedly, `runtimes.docker.host` is parsed into `DockerRuntime.Host`
  (`factory.go:131`) and then **never read** — it emits no `-H` flag. Dead config, silent
  no-op, an active trap for anyone containerizing.
- **`rebuild-server` / `rebuild-web` maintenance actions are VM-shaped by construction**:
  `git fetch` + `make web` + `go build` + `sudo install` + `sudo systemctl restart`
  (`maintenance_executors.go:291-347`). None of that exists in a container. (`pull-images` and
  `build-harness-config-image` survive fine over a mounted socket — the docker CLI streams the
  build context client-side, so those never hit the path problem.)

Not a problem, contrary to expectation: **metadata-server iptables**. `--cap-add NET_ADMIN` is
a flag dockerd interprets; the iptables call runs inside the *agent* container as PID 1. The
broker itself needs no privileges. `--privileged` is used nowhere.

The workable containerization constraint is **identity-mapped volumes**: the same absolute path
must resolve to the same data inside and outside the container (`-v /home/scion/.scion:/home/scion/.scion`),
plus `--user` matching the host owner. That works, but it is a sharp edge to document.

---

## Track 0 — Onboarding (do this regardless of packaging strategy)

This is separable from every strategy below, delivers most of the user-visible value, and is
mostly deletion of requirements. Rough order of value per unit of work:

**0.1 Publish images; default the registry.** Add a `release:` trigger to
`build-images.yml` pushing `scion-base` + harness images to a public location; default
`image_registry` to it in `default_settings.yaml`. Deletes a hard startup gate and the single
most opaque prerequisite. *(The onboarding wizard design already assumed prebuilt public images
would be the default — `.design/workstation-onboarding-wizard.md:19` — it just never landed.)*

**0.2 Autogenerate and persist `SESSION_SECRET`,** exactly as the dev token already does
(`devauth.go:47-88`). Deletes a required var and a whole class of "why am I logged out" reports.

**0.3 Default single-node to local storage.** Nothing about one VM needs GCS. Deletes the
bucket prerequisite.

**0.4 Single hostname, ACME HTTP-01, no wildcard.** Nothing in the product needs a wildcard —
agent port forwarding is **path-based**
(`/api/v1/agents/<id>/ports/<port>/proxy/...`), and hub API + web share one vhost. The wildcard
is purely an artifact of the `hub.<name>.<domain>` naming scheme in `hub-config.sh:44`. Dropping
the explicit `tls` directive from the generated Caddyfile and letting Caddy do HTTP-01 collapses
"own a domain + delegate NS to Cloud DNS + `dns.admin` + certbot dns-google" into "point one A
record at the VM". For a true zero-DNS path, `<ip>.sslip.io` gives a working hostname and a real
cert with no domain at all. *(Side note: `hub-setup-gce.md:50` already claims Caddy does
"automatic TLS provisioning" — this would make the docs true.)*

**0.5 A bootstrap admin credential.** The highest-leverage item and the one that needs actual
design. A one-time setup token, printed to stdout/journal/serial console at first boot,
exchangeable in the browser for an admin session. That breaks the chicken-and-egg: you get in,
*then* configure OAuth through the UI now that you know your own hostname. Long-lived
single-user deployments could stop there.

**0.6 Un-gate the onboarding wizard for single-node hosted.** The wizard already automates
~80% of what a single-node operator needs — runtime detection, registry, harness seeding, image
pull with SSE progress, first project. It is dark on a hosted hub behind *three* independent
gates: `requireWorkstation` (404s the whole `/api/v1/system/*` surface, `server.go:3181`),
`assertLoopback` on nearly every handler, and the fact that its auth model *is* the dev-auth
browser auto-login. All three are tied to the loopback bootstrap assumption, not to anything
intrinsic about the steps. With 0.5 in place, the non-filesystem handlers can be re-gated on the
bootstrap token.

**0.7 Security fix, needed independently.** `--host 0.0.0.0` currently leaves dev-auth on with
no warning, and `devAuthMiddleware` (`web.go:1253`) **auto-creates an admin session for any
browser request with no cookie** — no token needed. `scion server start --host 0.0.0.0` is an
advertised invocation (`server.go:123`). That is an unauthenticated public admin UI, and it gets
materially more dangerous the moment we make cloud deployment easy. Refuse dev-auth on a
non-loopback bind, or at minimum disable the auto-login half.

---

## Strategy A — Release binary + installer (lightest, lowest risk)

Keep the architecture native-on-VM. Change only delivery.

The binary already embeds the web assets (`Dockerfile.hub` builds exactly this artifact), and
`scion server install` already generates systemd/launchd units. So:

- Publish a static `scion` binary per release.
- Replace the build steps in `gce-start-hub.sh` (and a new user-facing `install.sh`) with
  download + checksum verify.
- Cloud-init drops to: Docker, Caddy, and the binary. No Go, no Node, no build-essential, no
  git checkout, no nats-server.
- Self-update becomes *simpler*, not harder: a new maintenance executor that downloads the new
  release, `sudo install`s it, and restarts — reusing the two sudoers rules that already exist
  (`gce-start-hub.sh` installs exactly `install -m 755 ... /usr/local/bin/scion` and
  `systemctl restart scion-hub`). `rebuild-server`'s git+Go+npm path stays for source deployments.

**Pros:** smallest diff; Problem 3 never arises (docker runs natively, paths are real host paths,
uid is the real uid); self-update gets better; `curl | sh` on any Linux box, not just GCE.
**Cons:** not "a container image"; still needs Docker + Caddy installed on the host; still
VM-shaped.

## Strategy B — Hub container + native broker (split)

Hub API + Web in a container; runtime broker stays native on the VM as a systemd unit,
registering to the hub over the existing control channel. Multi-broker registration is already a
supported topology (`.design/hosted/multi-broker.md`).

**Pros:** hub upgrade = image pull, which is the clean part of the container story; the broker
stays where the docker socket and the workspaces are, so Problem 3 disappears entirely;
architecturally honest — it is the shape the HA deployment already has.
**Cons:** two artifacts and two upgrade paths for a tier whose whole selling point is
simplicity; broker↔hub HMAC config becomes user-visible; loses the "one `docker run`" pitch.

## Strategy C — Single all-in-one container (the literal ask)

One image: hub + broker (+ optionally Caddy), run with `--user <host-uid>`,
`-v /var/run/docker.sock:/var/run/docker.sock`, and `~/.scion` **identity-mapped**
(`-v /home/scion/.scion:/home/scion/.scion` — same path both sides).

Required work: add the docker CLI to the hub image; wire `DockerRuntime.Host` into an actual
`-H` flag (or set `DOCKER_HOST`) so the dead config stops being a trap; replace
`rebuild-server`/`rebuild-web` with a pull-image-and-recreate operation; document the
identity-mapping constraint loudly. Set `SCION_SERVER_BASE_URL` so agents use bridge networking
via Caddy rather than `--network host` (the existing `colocatedExtraHosts` → `host-gateway`
mechanism keeps working from inside a container).

**Pros:** exactly the requested artifact; one `docker run` line; the same image runs on a VM, a
NUC, or a laptop, which is a genuinely nice story.
**Cons:** the identity-mapped-path constraint is a real sharp edge — violate it and you get
silent empty workspaces rather than an error; a container cannot cleanly restart itself, so
self-update needs `--restart=always` plus a small host-side updater or a systemd unit wrapping
`docker run` (at which point some of the "no host setup" claim erodes).

## Strategy D — NFS/`mount_root` workspace backend on one node (converge with HA)

Adopt the `nfsBackend` (`workspace_backend_nfs.go`) that Cloud Run already uses, where paths
derive from project ID + a configured mount root rather than from the broker's own filesystem.
This structurally removes the path-translation problem and pins uid to 1000:1000
(`common.go:311`) — the escape hatch it was built to be.

**Pros:** single-node and HA share one workspace code path; Strategy C's sharp edge vanishes.
**Cons:** running an NFS server on a single VM to talk to itself is absurd overhead for this
tier. Listed for completeness; recommend against for single-node, but it is the reason Strategy
C's constraint is *containable* rather than permanent — there is already a designed way out.

---

## Recommendation

**Track 0 first, and mostly independent of the rest.** It is where the user-visible win is, it
is largely subtraction, and every item lands value on the existing starter-hub too. 0.7 should
go in on its own merits and soon.

**Then Strategy A** as the delivery mechanism. It is a small diff, it makes self-update simpler
rather than harder, and it delivers "lightweight download, run on a VM" without paying for
Problem 3. `curl | sh` + one A record + a bootstrap token printed to the console is a genuinely
good first-run experience, and it is reachable this quarter.

**Then Strategy C** as the packaging endgame, once A has proven the config surface is small
enough that a container's env-var interface is tolerable. Do the two cheap correctness fixes
(docker CLI in image; wire or delete `DockerRuntime.Host`) early regardless — the dead `host`
setting is a trap sitting in the docs today.

**Strategy B is the fallback** if C's identity-mapping constraint proves too sharp in practice.
It is strictly more robust and strictly less elegant.

One caveat on sequencing: A and C are not mutually exclusive and share ~all of Track 0, but they
*do* diverge on the self-update executor (binary swap vs. image pull). Worth designing that
abstraction once, with two backends, rather than twice.

## Open questions

1. Where do public images live — GHCR, or a public GAR under a Google-owned project? Affects
   0.1 and the default value baked into `default_settings.yaml`.
2. Should the bootstrap token (0.5) expire on first use, on first OAuth config, or on a timer?
   And is "single-user, bootstrap-token-only, no OAuth ever" a supported end state or just a
   waypoint?
3. Does single-node hosted keep the in-dashboard self-update at all, or is redeploy the
   operator's job as it is on Cloud Run? This decides whether the Strategy A/C divergence above
   is worth abstracting.
4. `Dockerfile` and `Dockerfile.hub` at the repo root are byte-identical duplicates. Whichever
   strategy wins, one should go.

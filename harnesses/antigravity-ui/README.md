# Antigravity UI Harness Bundle (experimental)

Runs the graphical [Antigravity](https://antigravity.google/) desktop app (an
Electron app) headless inside a Scion agent container. You use the app
through its own web UI in your browser. Scion does not drive it: tasks and
messages are not delivered. The app signs in with Google Application Default
Credentials (see Auth).

Status: experimental, amd64 only. The image is built locally; no published
image exists.

## How it works

```
agent container (scion-antigravity-ui)
  tmux window "agent": antigravity-ui-wrapper
    ├─ Antigravity app (headless) ── language server: https://127.0.0.1:8443 (not exposed)
    ├─ nginx: http://127.0.0.1:8090 ── TLS on loopback ──► 127.0.0.1:8443
    └─ status banner; pasted messages get a "not delivered" notice
```

- The app's language server serves the UI over HTTPS on loopback with a
  self-signed certificate. The bundled nginx terminates that TLS and serves
  plain HTTP on `127.0.0.1:8090`, the only port the harness exposes.
- The app's UI expects to own the root of its origin. For the Hub's port
  link, which serves it under `/api/v1/agents/<id>/ports/<port>/proxy/`,
  nginx adds a small path shim (`agui_shim.js`) to the app page and patches
  two places in the app's `main.js`. At the root of the port (a tunnel) the
  shim changes nothing. The patches are specific to the pinned app version:
  at start the wrapper checks that they apply, and otherwise turns the shim
  off and offers only the tunnel.
- Known limit: the shim's HTML rule turns `="/` into `="` in every HTML page
  the app serves. The version check refuses an app page with a
  protocol-relative attribute (`="//`), but another HTML document the app
  serves could still contain one, which would then point outside the proxy
  path.
- The wrapper registers port 8090 with `scion expose`. With the shim on it
  asks for the experimental app mode first. If the Hub does not support app
  mode, it registers the port normally, and you connect with `scion tunnel`.
- Interrupts and pasted messages never reach the app. If the app or nginx
  exits, the wrapper stops the other and the agent ends.

## Build the image

On an amd64 docker host, from the repository root:

```sh
docker build \
  --build-arg BASE_IMAGE=scion-base:latest \
  -t scion-antigravity-ui:latest \
  harnesses/antigravity-ui
```

`BASE_IMAGE` is the `scion-base` image on that host. The build fails when the
downloaded package does not match the pinned sha256, and on non-amd64
targets.

The image contains the vendor's app package, so it is built locally and not
published. `image-build.conf` keeps the bundle out of the default catalog
builds (`build-images.sh --target harnesses|common|all|thick` and the
full-catalog Cloud Build configs) and limits it to `linux/amd64`. An
explicit `build-images.sh --target scion-antigravity-ui` still builds it for
amd64. The bundle's `cloudbuild.yaml` has no default registry: a deliberate
Cloud Build run must name a private one.

## Start an agent and connect

```sh
scion start my-agent --harness-config antigravity-ui
scion attach my-agent        # shows the banner and how to connect
```

Without app mode, connect through a tunnel:

```sh
scion tunnel my-agent --port 8090 --local-port 8090
# then open http://127.0.0.1:8090/
```

Websockets and long-lived streams need the `hub.conduit` experiment on the
Hub.

## Auth

The app signs in with Google Application Default Credentials (ADC). The
harness has one auth type, `vertex-ai`. The provisioner sets
`AGY_ADC_AUTH=true` for the app when it finds, in this order:

1. a staged `gcloud-adc` file secret;
2. `GOOGLE_APPLICATION_CREDENTIALS` in the agent's environment;
3. a dedicated GCP service account assigned to the agent (GCP identity mode
   `assign`; on Kubernetes the runtime may carry it as workload identity).

Use a dedicated service account, created for this harness, with only the
roles the app needs. Never use a broad, shared or default one, and never
grant it editor or owner, data or IAM-admin roles. Prefer a project without
production data, and prefer `assign` (short-lived tokens, no key file) over a
staged file. Any process in the container can obtain its tokens.

GCP identity `passthrough` with the host VM's own identity is not used by
default. If the host VM's service account is minimal and you want to use
it, set `AGY_ADC_AUTH=true` through settings or profile environment, or a
harness-config `env` override. Template environment keys are not applied.

Without a `gcloud-adc` file or an assigned service account, Scion refuses to
start the agent. With plain `passthrough` Scion does start it (it counts the
host identity as assigned), but the app is not given ADC: it asks for an
interactive sign-in and the agent stops as described below.

If the app asks for an interactive sign-in code at any time (for example
when the assigned service account stops working), the agent stops with a
message saying how to provide credentials. Signing out in the app's UI has
the same effect: start the agent again to recover.

## Files

| File | Purpose |
|---|---|
| `config.yaml` | harness-config |
| `provision.py` | pre-start provisioner: resolves the app's auth (ADC, see Auth) and turns the app's update check off |
| `scion_harness.py` | vendored provisioner library (generated) |
| `agent-plugins.yaml` | Agent Plugins support (none) |
| `Dockerfile` | pinned app package, nginx, wrapper |
| `antigravity-ui-wrapper.sh` | process supervisor (installed into the image) |
| `agui_helper.py` | renders the nginx config, checks the shim, picks free ports, checks port ownership (installed into the image) |
| `agui_shim.js` | path shim for the Hub port link (installed into the image) |
| `agui_shim.test.mjs` | shim unit tests (`node --test`) |
| `nginx.conf.tmpl` | forwarding proxy template |
| `provision_test.py` | unit tests |
| `PROBE.md` | runtime probe of the image and the path shim in a throwaway container |
| `cloudbuild.yaml` | Cloud Build config for deliberate builds (amd64; no default registry) |
| `image-build.conf` | keeps the image out of catalog builds; amd64 only |

## Wrapper settings

Set in the agent's environment (for example a template's `env`):

| Variable | Default | Meaning |
|---|---|---|
| `JETSKI_LS_PORT` | `8443` | preferred language server HTTPS port (loopback) |
| `AGUI_PROXY_PORT` | `8090` | preferred nginx port, the exposed port |
| `AGUI_PATH_REWRITE` | `auto` | `none`: never use the path shim (tunnel only) |
| `AGUI_SHIM_FORCE_MISMATCH` | `0` | `1`: test hook, make the shim's version check fail |
| `AGUI_LS_TIMEOUT` | `90` | seconds to wait for the language server |
| `AGUI_PROXY_TIMEOUT` | `30` | seconds to wait for nginx |

A preferred port that is already taken (agents with host networking share
the host's loopback) is replaced by a free one; the banner shows the port
in use. Logs are in `~/.local/state/antigravity-ui/` (use the agent's `shell` tmux
window).

## Limitations

- Scion tasks (`--task`) and `scion message` are not delivered to the app.
- Signing in from the app's own "Continue with Google" button does not
  complete in headless mode; use ADC (see Auth).
- amd64 only.
- The app is pinned. It cannot update itself: its update check is turned off,
  and its install directory is read-only for the agent user.

## Security notes

- The app's UI has no login of its own. Anyone who can open the agent's port
  8090 controls the app, which can run commands in the container. Access is
  governed by the Hub's port access permission on the agent.
- The app's Google identity (the ADC service account) is available to
  everyone with port access to the agent.
- The app runs without the Chromium sandbox; the container is the boundary.
  Stage only credentials the app is meant to use.
- On a workstation server whose Hub URL is a loopback address, agents use the
  host network. The app's UI and its other loopback listeners are then on
  the host's 127.0.0.1, where any local user can reach them without Scion
  authorization. Use the harness there only on a single-user machine, or give
  the server a Hub URL that agents reach over the bridge network (which also
  lets GCP identity `assign` work).

## Version bump

1. Take the new version's versioned `.deb` URL from the vendor's update feed.
2. Download it and compute its sha256. Check the file against the feed's
   sha512 for the same file.
3. Change `AG_VERSION` and `AG_DEB_SHA256` in the `Dockerfile` together, in a
   reviewed change that records the feed sha512 check.
4. Re-check the path shim: run `agui_helper.py shim-check` against the new
   app (or check that `main.js` contains each `SHIM_PATCHES` token in
   `agui_helper.py` exactly once), and update the tokens if the bundle
   changed. Until then the wrapper turns the shim off and offers only the
   tunnel.
5. Re-run the path-shim probe in `PROBE.md` against the new image and attach
   its report to the change. The shim patches are specific to one app
   version, so a bump is not done until the probe passes.

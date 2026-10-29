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
- The wrapper registers port 8090 with `scion expose`. It asks for the
  experimental app mode first. If the Hub does not support app mode, it
  registers the port normally, and you connect with `scion tunnel`.
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
3. a GCP service account assigned to the agent (GCP identity mode
   `assign`).

Use a dedicated service account with only the roles the app needs. Never
use a broad or default one: any process in the container can obtain its
tokens.

GCP identity `passthrough` (the host VM's own identity) is not used by
default. If the host VM's service account is minimal and you want to use
it, set `AGY_ADC_AUTH=true` through settings or profile environment, or a
harness-config `env` override. Template environment keys are not applied.

Without credentials the app waits for an interactive sign-in code, and the
agent stops with a message saying how to provide credentials. To sign in
with a personal account instead, set `AGUI_SIGNIN_RELAY=1`: the wrapper
then shows the sign-in URL in the agent's tmux pane (`scion attach`) and
forwards the code you paste back to the app. The resulting Google session
is stored in the agent's home.

## Files

| File | Purpose |
|---|---|
| `config.yaml` | harness-config |
| `provision.py` | pre-start provisioner: turns the app's update check off |
| `scion_harness.py` | vendored provisioner library (generated) |
| `agent-plugins.yaml` | Agent Plugins support (none) |
| `Dockerfile` | pinned app package, nginx, wrapper |
| `antigravity-ui-wrapper.sh` | process supervisor (installed into the image) |
| `agui_helper.py` | renders the nginx config, turns updates off (installed into the image) |
| `nginx.conf.tmpl` | forwarding proxy template |
| `provision_test.py` | unit tests |
| `PROBE.md` | runtime probe of the app in a throwaway container |
| `cloudbuild.yaml` | Cloud Build config (amd64) |

## Wrapper settings

Set in the agent's environment (for example a template's `env`):

| Variable | Default | Meaning |
|---|---|---|
| `JETSKI_LS_PORT` | `8443` | preferred language server HTTPS port (loopback) |
| `AGUI_PROXY_PORT` | `8090` | preferred nginx port, the exposed port |
| `AGUI_PATH_REWRITE` | `none` | `html`: rewrite root-relative URLs in HTML pages |
| `AGUI_SIGNIN_RELAY` | `0` | `1`: relay an interactive sign-in through the tmux pane |
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
- The app's Google identity (the ADC service account, or a personal account
  signed in through the relay) is available to everyone with port access to
  the agent.
- The app runs without the Chromium sandbox; the container is the boundary.
  Stage only credentials the app is meant to use.

## Version bump

1. Take the new version's versioned `.deb` URL from the vendor's update feed.
2. Download it and compute its sha256. Check the file against the feed's
   sha512 for the same file.
3. Change `AG_VERSION` and `AG_DEB_SHA256` in the `Dockerfile` together, in a
   reviewed change that records the feed sha512 check.

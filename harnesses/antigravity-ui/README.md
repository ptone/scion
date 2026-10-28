# Antigravity UI Harness Bundle (experimental)

Runs the graphical [Antigravity](https://antigravity.google/) desktop app (an
Electron app) headless inside a Scion agent container. You use the app
through its own web UI in your browser. Scion does not drive it: tasks and
messages are not delivered, and you sign in inside the app.

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
| `JETSKI_LS_PORT` | `8443` | language server HTTPS port (loopback) |
| `AGUI_PROXY_PORT` | `8090` | nginx port, the exposed port |
| `AGUI_ORIGIN_HEADER` | `on` | send `Origin: https://127.0.0.1` upstream |
| `AGUI_CSRF` | `off` | `inject`: add the language server's CSRF header upstream |
| `AGUI_PATH_REWRITE` | `none` | `html`: rewrite root-relative URLs in HTML pages |
| `AGUI_LS_TIMEOUT` | `90` | seconds to wait for the language server |
| `AGUI_PROXY_TIMEOUT` | `30` | seconds to wait for nginx |

Logs are in `~/.local/state/antigravity-ui/` (use the agent's `shell` tmux
window).

## Limitations

- Scion tasks (`--task`) and `scion message` are not delivered to the app.
- No Scion-staged credentials: sign in inside the app UI. An explicit auth
  type other than `none` fails the start.
- amd64 only.
- The app is pinned. It cannot update itself: its update check is turned off,
  and its install directory is read-only for the agent user.

## Security notes

- The app's UI has no login of its own. Anyone who can open the agent's port
  8090 controls the app, which can run commands in the container. Access is
  governed by the Hub's port access permission on the agent.
- Signing in to the app inside the agent makes that account's session
  available to everyone with port access to the agent.
- The app runs without the Chromium sandbox; the container is the boundary.
  Stage only credentials the app is meant to use.

## Version bump

1. Take the new version's versioned `.deb` URL from the vendor's update feed.
2. Download it and compute its sha256. Check the file against the feed's
   sha512 for the same file.
3. Change `AG_VERSION` and `AG_DEB_SHA256` in the `Dockerfile` together, in a
   reviewed change that records the feed sha512 check.

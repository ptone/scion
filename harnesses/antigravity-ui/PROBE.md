# Antigravity UI: runtime probe (path shim)

A throwaway check of this bundle's image on an amd64 docker host with a
browser. It runs the real wrapper, then checks that the app's UI works under
a simulated Hub port-proxy path (`/api/v1/agents/<id>/ports/<port>/proxy/`),
including reloads, and that it is unchanged at the root of the port.

**Never paste token values, `ps` output, process command lines, the rendered
`nginx.conf`, or screenshots that show request headers or the sign-in URL
into the report.** The report asks only for counts, yes/no answers and URL
paths.

## 0. Get the bundle

```sh
git fetch origin scion/antigravity-ui
git worktree add /tmp/agui-probe-src FETCH_HEAD   # or: git -C /tmp/agui-probe-src checkout FETCH_HEAD
cd /tmp/agui-probe-src
```

## 1. Build

`BASE_IMAGE` is the `scion-base` image on this host.

```sh
docker build --build-arg BASE_IMAGE=scion-base:latest \
  -t scion-antigravity-ui:probe harnesses/antigravity-ui
```

## 2. Start the probe container and the wrapper

Host networking makes the container's loopback the host's loopback, so the
host browser can open `127.0.0.1:8090` and `127.0.0.1:8091`. Ports 8090,
8091 and 8443 should be free on the host (the wrapper picks other ports and
says so if they are not; use the ports it prints below).

The app signs in with Application Default Credentials. On a GCE host whose
service account is minimal, `AGY_ADC_AUTH=true` uses it through the metadata
server. Otherwise mount an ADC file and set `GOOGLE_APPLICATION_CREDENTIALS`
as well.

```sh
docker run --rm -it --network host --name agui-probe \
  -e AGY_ADC_AUTH=true \
  --entrypoint /usr/local/bin/antigravity-ui-wrapper \
  scion-antigravity-ui:probe
```

Outside an agent `scion expose` fails, so the banner says the port could not
be registered. Before the banner, the wrapper prints one of:

- `nginx config rendered (path_rewrite=shim)`: the shim is on (expected for
  the pinned version);
- `path shim off: <reason>`: report the reason.

## 3. Listeners

From a second shell, after the banner:

```sh
docker exec agui-probe ss -ltn
```

- L1. How many listening TCP sockets are there, and are ALL of them on
  `127.0.0.1` or `[::1]` (y/n)? Report the count and any non-loopback
  address. (The wrapper itself refuses to start only if the language server
  or the proxy is not loopback-only; L1 is what covers the app's other
  listeners.)

## 3b. Root check (tunnel case)

Open `http://127.0.0.1:8090/` in the host browser.

- R1. Does the UI load into the workspace? Do RPCs succeed (devtools,
  Network: count failed requests)?
- R2. Start a conversation, then reload the page on that conversation's URL.
  Does it come back?

## 4. Prefix simulation

A second nginx on `127.0.0.1:8091` plays the Hub's port proxy in app mode: it
serves the UI under a path prefix, strips the prefix, drops cookies and
credentials toward the app, drops cookie and policy headers from the app,
adds the app-mode headers, and records every request that escapes the
prefix. From a second shell:

```sh
docker exec -it agui-probe bash
mkdir -p /tmp/agui-prefix
cat > /tmp/agui-prefix/nginx.conf <<'EOF'
worker_processes 1;
daemon off;
pid /tmp/agui-prefix/nginx.pid;
error_log /tmp/agui-prefix/nginx.err warn;
events {}
http {
  access_log off;
  client_body_temp_path /tmp/agui-prefix/b;
  proxy_temp_path /tmp/agui-prefix/p;
  fastcgi_temp_path /tmp/agui-prefix/f;
  uwsgi_temp_path /tmp/agui-prefix/u;
  scgi_temp_path /tmp/agui-prefix/s;
  log_format pathonly '$request_method $uri';
  map $http_upgrade $connection_upgrade { default upgrade; '' close; }
  server {
    listen 127.0.0.1:8091;
    location /api/v1/agents/x/ports/8090/proxy/ {
      proxy_pass http://127.0.0.1:8090/;
      proxy_http_version 1.1;
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection $connection_upgrade;
      proxy_set_header Cookie "";
      proxy_set_header Authorization "";
      proxy_hide_header Set-Cookie;
      proxy_hide_header Content-Security-Policy;
      proxy_hide_header Access-Control-Allow-Origin;
      proxy_hide_header Access-Control-Allow-Credentials;
      add_header Content-Security-Policy "frame-ancestors 'none'" always;
      add_header X-Content-Type-Options nosniff always;
      proxy_buffering off;
      proxy_read_timeout 1h;
    }
    location / {
      access_log /tmp/agui-prefix/escaped.log pathonly;
      return 404;
    }
  }
}
EOF
(nginx -p /tmp/agui-prefix -e /tmp/agui-prefix/nginx.err -c /tmp/agui-prefix/nginx.conf &)
```

Open `http://127.0.0.1:8091/api/v1/agents/x/ports/8090/proxy/` in the host
browser (a fresh tab) and use the UI for a few minutes:

- P1. Does the UI load into the workspace (not "Not Found")?
- P2. Do RPCs succeed (devtools, Network: count failed requests)?
- P3. Start a conversation and run a simple task. Does it stream and
  complete?
- P4. Open settings, a conversation, the file and diff views if available,
  and an artifact or image if one is produced. Anything broken?
- P5. Reload the page while on a conversation URL (it should start with the
  prefix). Does it come back to the same conversation?
- P6. Devtools, Application, Local Storage for `127.0.0.1:8091`: do all of
  the app's keys start with `agui:x:` (y/n, count of keys without it)?
- P7. Console: any errors mentioning `agui` or a path without the prefix?
- P8. Open a diff view (it runs in a web worker). Does it render? Console:
  any Trusted Types errors (for example "requires 'TrustedScriptURL'")?

Then, in the container shell:

```sh
wc -l < /tmp/agui-prefix/escaped.log                                 # requests that escaped
sort /tmp/agui-prefix/escaped.log | uniq -c | sort -rn | head -20    # their paths
```

## 5. Shim off (control)

Stop the container with `docker stop agui-probe` (the wrapper ignores
Ctrl-C by design), start it again with `-e AGUI_PATH_REWRITE=none`, repeat
the start of step 4 (re-create the 8091 config), and open the prefixed URL
once.

- C1. Does it show "Not Found" or a broken page, as in the first probe?

## 6. Phase 1 end-to-end check (agent on a Scion server)

Run on an amd64 docker host with a Scion server built from this branch. Use
a Hub URL that agents reach over the bridge network if you can (GCP identity
`assign` then works and agents stay off the host loopback).

Build checks (once):

```sh
docker build --build-arg BASE_IMAGE=scion-base:latest -t scion-antigravity-ui:latest harnesses/antigravity-ui
docker run --rm --entrypoint bash scion-antigravity-ui:latest -c \
  'for b in /opt/Antigravity/antigravity /opt/Antigravity/chrome_crashpad_handler /opt/Antigravity/resources/bin/language_server; do ldd "$b" | grep -c "not found"; done;
   touch /opt/Antigravity/x 2>/dev/null && echo writable || echo "not writable";
   ls /etc/apt/sources.list.d/; sha256sum /opt/Antigravity/antigravity'
```

Plus the wrong-hash and arm64 builds from step 1 (both must fail).

Start an agent with a dedicated, minimal service account (or stage an ADC
file instead: `scion secret set gcloud-adc @/path/to/adc.json`):

```sh
scion start agui-e2e --harness-config antigravity-ui --service-account <dedicated SA>
scion attach agui-e2e                              # banner; then detach
scion tunnel agui-e2e --port 8090 --local-port 8090   # then http://127.0.0.1:8090/
```

| Item | Check | Expected |
|---|---|---|
| E1 | time from start to the banner | about 10 s or less |
| E2 | UI at root through the tunnel | signed in (no sign-in screen); a simple task runs |
| E3 | `scion expose --list` from inside the agent (`scion attach`, shell window) | only 8090 |
| L1 | inside the agent: `ss -ltn` | every listener of the app (all ports, not only 8090 and the language server) on `127.0.0.1` or `[::1]` |
| E4 | `scion message agui-e2e "x"`, then a Scion interrupt | the pane shows "message not delivered"; the app keeps running |
| E5 | stop the app (`pkill -f /opt/Antigravity/antigravity` in the shell window) | the agent ends |
| E6 | start a second agent with no service account and no ADC file | Scion refuses the start; with plain passthrough the agent exits 1 with the no-auth message |
| E7 | set `AGUI_SHIM_FORCE_MISMATCH=1` in the harness-config env and start an agent | banner: "The port link cannot show the UI (forced version mismatch (test hook))"; only a plain expose in expose.log |
| E8 | on a host-networked workstation hub, start two agents at once | both run, on different ports (the banner shows them) |
| E9 | after 10 minutes: `grep -ciE "update\|download" ~/.local/state/antigravity-ui/app.log` and `sha256sum /opt/Antigravity/antigravity` | no update download; hash unchanged from the build check |

Report E1 to E9, L1 and the build checks with yes/no, counts, seconds and
ports only; never tokens, sign-in URLs or command lines.

## Report template

Send back this table. Values are yes/no, counts or paths; never tokens.

| Item | Question | Result |
|---|---|---|
| Build | clean build (y/n) | |
| Shim | `path_rewrite=shim` printed (y/n; reason if off) | |
| L1 | listening sockets (count); all on loopback (y/n; list any other address) | |
| R1 | root: UI loads; failed requests (count) | |
| R2 | root: reload on a conversation URL works (y/n) | |
| P1 | prefix: UI loads into the workspace (y/n) | |
| P2 | prefix: failed requests (count, top paths) | |
| P3 | prefix: task streams and completes (y/n) | |
| P4 | prefix: views that are broken (list or "none") | |
| P5 | prefix: reload on a conversation URL works (y/n) | |
| P6 | prefix: app storage keys all start with `agui:x:` (y/n, count without) | |
| P7 | prefix: console errors about paths (count, short text) | |
| P8 | prefix: diff view renders (y/n); Trusted Types errors (count) | |
| Escapes | `escaped.log` lines (count, top paths) | |
| C1 | shim off: prefixed page broken as before (y/n) | |

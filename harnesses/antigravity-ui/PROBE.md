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

## 3. Root check (tunnel case)

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

Then, in the container shell:

```sh
wc -l < /tmp/agui-prefix/escaped.log                                 # requests that escaped
sort /tmp/agui-prefix/escaped.log | uniq -c | sort -rn | head -20    # their paths
```

## 5. Shim off (control)

Stop the container with `docker stop agui-probe` (the wrapper ignores
Ctrl-C by design), start it again with `-e AGUI_PATH_REWRITE=none`, repeat the start of step 4
(re-create the 8091 config), and open the prefixed URL once.

- C1. Does it show "Not Found" or a broken page, as in the first probe?

## Report template

Send back this table. Values are yes/no, counts or paths; never tokens.

| Item | Question | Result |
|---|---|---|
| Build | clean build (y/n) | |
| Shim | `path_rewrite=shim` printed (y/n; reason if off) | |
| R1 | root: UI loads; failed requests (count) | |
| R2 | root: reload on a conversation URL works (y/n) | |
| P1 | prefix: UI loads into the workspace (y/n) | |
| P2 | prefix: failed requests (count, top paths) | |
| P3 | prefix: task streams and completes (y/n) | |
| P4 | prefix: views that are broken (list or "none") | |
| P5 | prefix: reload on a conversation URL works (y/n) | |
| P6 | prefix: app storage keys all start with `agui:x:` (y/n, count without) | |
| P7 | prefix: console errors about paths (count, short text) | |
| Escapes | `escaped.log` lines (count, top paths) | |
| C1 | shim off: prefixed page broken as before (y/n) | |

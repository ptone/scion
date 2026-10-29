# Antigravity UI: runtime probe

A throwaway check of the pinned Antigravity app in this bundle's image,
before the harness is relied on. It answers the open questions P1 to P11
below and picks the nginx rewrite tier. Run it on an amd64 docker host with
a browser. It needs no credentials until the sign-in step (P5).

**Never paste token values, `ps` output, process command lines, or
screenshots that show request headers into the report.** The language
server's command line carries its CSRF token. The report asks only for
counts, yes/no answers and URL paths.

## 0. Get the bundle

```sh
git fetch origin scion/antigravity-ui
git worktree add /tmp/agui-probe-src FETCH_HEAD
cd /tmp/agui-probe-src
```

## 1. Build

`BASE_IMAGE` is the `scion-base` image on this host.

```sh
docker build --build-arg BASE_IMAGE=scion-base:latest \
  -t scion-antigravity-ui:probe harnesses/antigravity-ui
```

Build checks (Phase 1 criteria; optional now):

```sh
# Wrong hash: must fail at "sha256sum -c".
docker build --build-arg BASE_IMAGE=scion-base:latest \
  --build-arg AG_DEB_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
  -t scion-antigravity-ui:badhash harnesses/antigravity-ui
# Non-amd64: must fail with "antigravity-ui: amd64 only".
docker build --build-arg BASE_IMAGE=scion-base:latest --build-arg TARGETARCH=arm64 \
  -t scion-antigravity-ui:arm harnesses/antigravity-ui
```

## 2. Start the probe container

Host networking makes the container's loopback the host's loopback, so the
host browser can open `127.0.0.1:8090`. Ports 8090, 8091 and 8443 must be
free on the host. This is a throwaway probe, not how agents run.

```sh
docker run --rm -it --network host --name agui-probe --entrypoint bash \
  -e ELECTRON_OZONE_PLATFORM_HINT=headless -e JETSKI_LS_PORT=8443 \
  scion-antigravity-ui:probe
```

## 3. App checks (inside the container)

```sh
# P6: missing libraries (expect no "not found" lines)
for b in /opt/Antigravity/antigravity /opt/Antigravity/chrome_crashpad_handler \
         /opt/Antigravity/resources/bin/language_server; do
  echo "== $b"; ldd "$b" | grep 'not found'
done
# P6b: install dir not writable by this user (expect "not writable")
touch /opt/Antigravity/probe-write 2>/dev/null && echo writable || echo "not writable"

# P7 (part 1): turn the update check off, as the harness does
python3 -I /usr/local/share/antigravity-ui/agui_helper.py seed-update-off
sha256sum /opt/Antigravity/antigravity > /tmp/app.sha256

# Start the app in tmux, the way the harness does, and time it (P1)
t0=$(date +%s)
tmux new-session -d -s p '/opt/Antigravity/antigravity --ozone-platform=headless --headless --disable-gpu --no-sandbox 2>&1 | tee /tmp/ag.log'
until curl -sk -o /dev/null --max-time 2 https://127.0.0.1:8443/ || [ $(( $(date +%s) - t0 )) -ge 120 ]; do sleep 1; done
echo "P1 seconds to answer on 8443: $(( $(date +%s) - t0 ))"
# P1: the log line the app prints when listening (token masked)
grep -n 'listening on' /tmp/ag.log | sed -E 's/(csrf[_-]?token[=: "]+)[^ "&,]+/\1<redacted>/Ig'
# P1: listening TCP sockets (report the count, whether all are on
# 127.0.0.1, and whether 8443 is one of them)
ss -ltn
# P8: what does typed input do? Report whether the app is still running.
tmux send-keys -t p 'hello from probe' Enter; sleep 3; pgrep -c -f /opt/Antigravity/antigravity
# P3: does the language server serve the UI? (status and content type)
curl -sk -o /tmp/index.html -w '%{http_code} %{content_type}\n' https://127.0.0.1:8443/
# P2: does the page mention a CSRF token? (count only)
grep -c -i csrf /tmp/index.html
# P11: shape of asset URLs (paths only)
grep -oE '(src|href)="[^"]+"' /tmp/index.html | head -30
# P10: does a foreign Origin get a different status?
curl -sk -o /dev/null -w '%{http_code}\n' -H 'Origin: http://127.0.0.1:8090' https://127.0.0.1:8443/
# P9: app state directories (names only)
ls -a ~/.config/Antigravity ~/.gemini 2>/dev/null
```

## 4. Proxy checks (inside the container; browser on the host)

Render and start the forwarding proxy with the harness's own helper:

```sh
mkdir -p /tmp/agui
render() {  # extra args: --path-rewrite html
  python3 -I /usr/local/share/antigravity-ui/agui_helper.py render-nginx \
    --template /usr/local/share/antigravity-ui/nginx.conf.tmpl \
    --out /tmp/agui/nginx.conf --state /tmp/agui \
    --proxy-port 8090 --ls-port 8443 "$@"
}
NGX="nginx -p /tmp/agui -e /tmp/agui/nginx.err -c /tmp/agui/nginx.conf"
render && ($NGX &) && sleep 1
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/
```

1. Open `http://127.0.0.1:8090/` in the host browser. Does the UI load? Do
   its RPCs succeed (devtools, Network tab: count failed requests)? Are there
   `ws://` connections (P4)?
The forwarding proxy sends no Origin or CSRF header: the first probe showed
the UI works without either.

### Prefix simulation (P11)

A second nginx on `127.0.0.1:8091` plays the Hub's port proxy in app mode: it
serves the UI under a path prefix, strips the prefix, drops cookies and
credentials toward the app, drops cookie and policy headers from the app, and
records every request that escapes the prefix.

```sh
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
browser and use the UI for a minute. Then:

```sh
wc -l < /tmp/agui-prefix/escaped.log           # requests that escaped the prefix
sort /tmp/agui-prefix/escaped.log | uniq -c | sort -rn | head -20   # their paths
```

If requests escaped, try the HTML rewrite tier and repeat with a fresh log:

```sh
: > /tmp/agui-prefix/escaped.log
render --path-rewrite html && $NGX -s reload
```

If escapes remain, record which kinds they are (asset files, RPC paths,
`ws://` URLs, worker or module imports). That decides whether a small
script shim is enough or the design needs revising.

### Sign-in and stdin (P5, P8)

- P5: with no credentials, what does the UI show? Does Google sign-in
  complete through `http://127.0.0.1:8090/`? Optional: restart the app with
  `GEMINI_API_KEY` set. Is sign-in skipped?
- P8: stop the tmux app (`tmux kill-session -t p`, then
  `pkill -f /opt/Antigravity/antigravity`), start it with stdin closed, and
  check that it survives:

  ```sh
  /opt/Antigravity/antigravity --ozone-platform=headless --headless --disable-gpu --no-sandbox \
    </dev/null >/tmp/ag2.log 2>&1 &
  sleep 30; curl -sk -o /dev/null -w '%{http_code}\n' https://127.0.0.1:8443/
  ```

### P7 (part 2): updater

After the app has run for at least 3 minutes (10 for the Phase 1 criterion):

```sh
grep -ciE 'update|download' /tmp/ag.log /tmp/ag2.log
sha256sum -c /tmp/app.sha256
```

## 5. Wrapper smoke test (optional)

Stop everything from steps 3 and 4 first:

```sh
pkill nginx; pkill -f /opt/Antigravity/antigravity; sleep 2
antigravity-ui-wrapper
```

Outside an agent `scion expose` fails, so the banner says the port could not
be registered. Then check:

- the UI works at `http://127.0.0.1:8090/` from the host browser;
- typing a line and Enter prints "message not delivered";
- Ctrl-C does nothing;
- from a second shell (`docker exec -it agui-probe bash`),
  `pkill -f /opt/Antigravity/antigravity` makes the wrapper stop nginx and
  exit.

## Report template

Send back this table. Values are yes/no, counts, seconds or paths; never
tokens.

| Item | Question | Result |
|---|---|---|
| Build | clean build; wrong hash fails; arm64 fails | |
| P1 | seconds to "listening"; number of listening TCP sockets; all on 127.0.0.1 (y/n); 8443 among them (y/n) | |
| P2 | `grep -c csrf` count; RPCs work through the proxy (y/n) | |
| P3 | status and content type of `https://127.0.0.1:8443/` | |
| P4 | `ws://` connections seen (y/n, count) | |
| P5 | what the UI shows without credentials; sign-in completes through 8090 (y/n); `GEMINI_API_KEY` skips sign-in (y/n/not tried) | |
| P6 | missing libraries (count); install dir writable (y/n) | |
| P7 | update/download log lines (count); executable hash unchanged (y/n) | |
| P8 | app survives typed input (y/n); survives `</dev/null` (y/n) | |
| P9 | directories under `~/.config/Antigravity` and `~/.gemini` (names) | |
| P10 | status with a foreign Origin; UI works with `--origin off` (y/n) | |
| P11 | escaped requests without rewrite (count, top paths); with `--path-rewrite html` (count, kinds left) | |
| Tier | nginx flags that made the prefixed UI work fully (or "none worked") | |
| Wrapper | smoke test results (optional) | |

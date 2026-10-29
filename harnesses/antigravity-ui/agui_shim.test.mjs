// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Unit tests for agui_shim.js, run with `node --test` (harnesses/node_unit_test.go).
// The shim runs in a vm context against a small fake window.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const SHIM = readFileSync(new URL("./agui_shim.js", import.meta.url), "utf8");
const HOST = "hub.example";
const PREFIX = "/api/v1/agents/abc-123/ports/8090/proxy";

function fakeStorage() {
  const m = new Map();
  return {
    get length() { return m.size; },
    key(i) { return [...m.keys()][i] ?? null; },
    getItem(k) { return m.has(k) ? m.get(k) : null; },
    setItem(k, v) { m.set(String(k), String(v)); },
    removeItem(k) { m.delete(k); },
    _map: m,
  };
}

function load(pathname) {
  const calls = { fetch: [], xhr: [], ctor: [], history: [], writes: [] };
  class XMLHttpRequest {}
  XMLHttpRequest.prototype.open = function (method, url) { calls.xhr.push(url); };
  const ctor = (name) => {
    const C = function (url, opts) { calls.ctor.push([name, url]); this.url = url; this.opts = opts; };
    return C;
  };
  const WebSocket = ctor("WebSocket");
  WebSocket.OPEN = 1;
  class Element {
    constructor() { this.attrs = {}; }
    setAttribute(n, v) { this.attrs[n] = v; }
    getAttribute(n) { return this.attrs[n] ?? null; }
  }
  class HTMLImageElement extends Element {}
  Object.defineProperty(HTMLImageElement.prototype, "src", {
    configurable: true,
    enumerable: true,
    get() { return this._src; },
    set(v) { this._src = v; },
  });
  class Request {
    constructor(url, init) { this.url = typeof url === "string" ? url : url.url; this.init = init; }
  }
  const window = {
    location: { pathname, host: HOST, href: `https://${HOST}${pathname}` },
    document: { write: (s) => calls.writes.push(s), documentElement: null },
    URL,
    Proxy,
    Request,
    fetch: (input, init) => { calls.fetch.push(input); return Promise.resolve(init); },
    XMLHttpRequest,
    WebSocket,
    EventSource: ctor("EventSource"),
    Worker: ctor("Worker"),
    history: {
      pushState: (s, t, u) => calls.history.push(["push", u]),
      replaceState: (s, t, u) => calls.history.push(["replace", u]),
    },
    Element,
    HTMLImageElement,
    localStorage: fakeStorage(),
    sessionStorage: fakeStorage(),
  };
  const realLocal = window.localStorage;
  vm.runInNewContext(SHIM, { window, JSON, Object, Array, String });
  return { window, calls, realLocal };
}

test("without a proxy prefix it only writes a root base", () => {
  const { window, calls, realLocal } = load("/conversations/1");
  assert.deepEqual(calls.writes, ['<base href="/">']);
  assert.equal(window.__AGUI_PREFIX__, undefined);
  window.fetch("/static/x");
  assert.deepEqual(calls.fetch, ["/static/x"]);
  assert.equal(window.localStorage, realLocal);
});

test("finds the proxy prefix and writes its base", () => {
  const { window, calls } = load(`${PREFIX}/conversations/1`);
  assert.equal(window.__AGUI_PREFIX__, PREFIX);
  assert.deepEqual(calls.writes, [`<base href="${PREFIX}/">`]);
});

test("proxyPrefix rejects other paths", () => {
  const { window } = load("/");
  const p = window.__AGUI_SHIM__.proxyPrefix;
  assert.equal(p(`${PREFIX}`), PREFIX);
  assert.equal(p(`${PREFIX}/`), PREFIX);
  assert.equal(p("/api/v1/agents/abc/ports/80a/proxy/"), "");
  assert.equal(p("/api/v1/agents/abc/ports/8090/files/"), "");
  assert.equal(p("/api/v1/agents//ports/8090/proxy/"), "");
  assert.equal(p("/web/abc"), "");
});

test("rewrites root-relative and same-host URLs only", () => {
  const { window } = load(`${PREFIX}/`);
  const r = window.__AGUI_SHIM__.rewrite;
  assert.equal(r("/main.js"), `${PREFIX}/main.js`);
  assert.equal(r("/a?b=/c#/d"), `${PREFIX}/a?b=/c#/d`);
  assert.equal(r(`${PREFIX}/main.js`), `${PREFIX}/main.js`);
  assert.equal(r(PREFIX), PREFIX);
  assert.equal(r("relative.js"), "relative.js");
  assert.equal(r("//cdn.example/x.js"), "//cdn.example/x.js");
  assert.equal(r("https://other.example/x"), "https://other.example/x");
  assert.equal(r("data:text/plain,/x"), "data:text/plain,/x");
  assert.equal(r(`https://${HOST}/exa.Service/Call`), `https://${HOST}${PREFIX}/exa.Service/Call`);
  assert.equal(r(`wss://${HOST}/connect-websocket`), `wss://${HOST}${PREFIX}/connect-websocket`);
  assert.equal(r(`https://${HOST}${PREFIX}/x`), `https://${HOST}${PREFIX}/x`);
  assert.equal(r(null), null);
});

test("wraps fetch, XHR, WebSocket, Worker and history", () => {
  const { window, calls } = load(`${PREFIX}/`);
  window.fetch("/static/artifacts/a.png");
  window.fetch(new window.Request(`https://${HOST}/voice/chats`));
  assert.equal(calls.fetch[0], `${PREFIX}/static/artifacts/a.png`);
  assert.equal(calls.fetch[1].url, `https://${HOST}${PREFIX}/voice/chats`);

  new window.XMLHttpRequest().open("GET", "/x");
  assert.deepEqual(calls.xhr, [`${PREFIX}/x`]);

  const ws = new window.WebSocket(`ws://${HOST}/connect-websocket`, ["p"]);
  assert.equal(ws.url, `ws://${HOST}${PREFIX}/connect-websocket`);
  assert.deepEqual(ws.opts, ["p"]);
  assert.equal(window.WebSocket.OPEN, 1);

  // A TrustedScriptURL-like value is stringified when it needs a prefix.
  const w = new window.Worker({ toString: () => "/diff_worker.js?v=1" });
  assert.equal(w.url, `${PREFIX}/diff_worker.js?v=1`);

  window.history.pushState(null, "", "/c/2");
  window.history.replaceState(null, "", `${PREFIX}/c/3`);
  assert.deepEqual(calls.history, [["push", `${PREFIX}/c/2`], ["replace", `${PREFIX}/c/3`]]);
});

test("rewrites URL-bearing DOM attributes and properties", () => {
  const { window } = load(`${PREFIX}/`);
  const img = new window.HTMLImageElement();
  img.src = "/static/artifacts/x.png";
  assert.equal(img.src, `${PREFIX}/static/artifacts/x.png`);
  img.setAttribute("href", "/a");
  img.setAttribute("alt", "/not-a-url");
  assert.equal(img.attrs.href, `${PREFIX}/a`);
  assert.equal(img.attrs.alt, "/not-a-url");
});

test("namespaces storage per agent", () => {
  const { window, realLocal } = load(`${PREFIX}/`);
  realLocal.setItem("hubKey", "hub");
  const ls = window.localStorage;
  assert.notEqual(ls, realLocal);
  ls.setItem("theme", "dark");
  assert.equal(realLocal.getItem("agui:abc-123:theme"), "dark");
  assert.equal(ls.getItem("theme"), "dark");
  assert.equal(ls.getItem("hubKey"), null);
  assert.equal(ls.theme, "dark");
  ls.other = "1";
  assert.equal(realLocal.getItem("agui:abc-123:other"), "1");
  assert.equal(ls.length, 2);
  assert.deepEqual(Object.keys(ls).sort(), ["other", "theme"]);
  ls.clear();
  assert.equal(ls.length, 0);
  assert.equal(realLocal.getItem("hubKey"), "hub");
});

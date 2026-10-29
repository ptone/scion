/*
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/*
 * antigravity-ui path shim. nginx adds <script src="agui-shim.js"> as the
 * first element of the head of the app page and serves this file for any
 * path ending in agui-shim.js (see agui_helper.py), so the relative URL
 * works at any route depth. It lets the UI of the app, which expects to own
 * the root of its origin, run under the Hub port proxy path
 * /api/v1/agents/<id>/ports/<port>/proxy/.
 *
 * - Writes <base href="PREFIX/"> so relative asset URLs of the page resolve
 *   under the prefix at any route depth (and under "/" without a prefix).
 * - Without a prefix it does nothing else.
 * - With one: sets window.__AGUI_PREFIX__ (read by the patched main.js),
 *   prefixes root-relative and same-host URLs passed to fetch, XHR,
 *   WebSocket, EventSource, Worker, SharedWorker, history and URL-bearing
 *   DOM attributes, and namespaces localStorage and sessionStorage per agent
 *   so keys of the app do not mix with those of the Hub web app.
 */
(function (w) {
  "use strict";

  function proxyPrefix(pathname) {
    var p = String(pathname || "").split("/");
    if (p.length < 8 || p[0] !== "" || p[1] !== "api" || p[2] !== "v1" ||
        p[3] !== "agents" || !p[4] || p[5] !== "ports" || p[7] !== "proxy") {
      return "";
    }
    var port = p[6];
    if (!port) {
      return "";
    }
    for (var i = 0; i < port.length; i++) {
      if ("0123456789".indexOf(port.charAt(i)) < 0) {
        return "";
      }
    }
    return "/" + p.slice(1, 8).join("/");
  }

  var loc = w.location;
  var prefix = proxyPrefix(loc.pathname);
  w.__AGUI_SHIM__ = { prefix: prefix, proxyPrefix: proxyPrefix };

  if (w.document && typeof w.document.write === "function") {
    w.document.write("<base href=" + JSON.stringify(prefix + "/") + ">");
  }
  if (!prefix) {
    return;
  }
  w.__AGUI_PREFIX__ = prefix;

  function under(path) {
    return path === prefix || path.indexOf(prefix + "/") === 0;
  }

  // Returns the rewritten URL as a string, or the original value unchanged
  // (which keeps non-string values such as TrustedScriptURL intact).
  function rewrite(u) {
    if (u === null || u === undefined) {
      return u;
    }
    var s = (typeof u === "string") ? u : String(u);
    if (s.charAt(0) === "/" && s.charAt(1) !== "/") {
      return under(s.split("?")[0].split("#")[0]) ? u : prefix + s;
    }
    if (s.indexOf("//") < 0) {
      return u;
    }
    var x;
    try {
      x = new w.URL(s, loc.href);
    } catch (e) {
      return u;
    }
    var schemes = { "http:": 1, "https:": 1, "ws:": 1, "wss:": 1 };
    if (!schemes[x.protocol] || x.host !== loc.host || under(x.pathname)) {
      return u;
    }
    x.pathname = prefix + x.pathname;
    return x.href;
  }
  w.__AGUI_SHIM__.rewrite = rewrite;

  // fetch and XHR.
  if (typeof w.fetch === "function") {
    var origFetch = w.fetch;
    w.fetch = function (input, init) {
      if (w.Request && input instanceof w.Request) {
        var r = rewrite(input.url);
        if (r !== input.url) {
          input = new w.Request(r, input);
        }
      } else {
        input = rewrite(input);
      }
      return origFetch.call(this, input, init);
    };
  }
  if (w.XMLHttpRequest && w.XMLHttpRequest.prototype) {
    var origOpen = w.XMLHttpRequest.prototype.open;
    w.XMLHttpRequest.prototype.open = function () {
      var args = Array.prototype.slice.call(arguments);
      args[1] = rewrite(args[1]);
      return origOpen.apply(this, args);
    };
  }

  // Constructors that take a URL first.
  function wrapCtor(name) {
    var Orig = w[name];
    if (typeof Orig !== "function") {
      return;
    }
    var Wrapped = function (url, opts) {
      return arguments.length > 1 ? new Orig(rewrite(url), opts) : new Orig(rewrite(url));
    };
    Wrapped.prototype = Orig.prototype;
    var statics = ["CONNECTING", "OPEN", "CLOSING", "CLOSED"];
    for (var i = 0; i < statics.length; i++) {
      if (statics[i] in Orig) {
        Wrapped[statics[i]] = Orig[statics[i]];
      }
    }
    w[name] = Wrapped;
  }
  wrapCtor("WebSocket");
  wrapCtor("EventSource");
  wrapCtor("Worker");
  wrapCtor("SharedWorker");

  // History: keep pushed URLs under the prefix.
  if (w.history) {
    var methods = ["pushState", "replaceState"];
    for (var h = 0; h < methods.length; h++) {
      (function (m) {
        var orig = w.history[m];
        if (typeof orig !== "function") {
          return;
        }
        w.history[m] = function (state, title, url) {
          if (arguments.length > 2 && url !== undefined && url !== null) {
            return orig.call(this, state, title, rewrite(url));
          }
          return orig.apply(this, arguments);
        };
      })(methods[h]);
    }
  }

  // URL-bearing DOM attributes and properties.
  var URL_ATTRS = { src: 1, href: 1, action: 1, formaction: 1, poster: 1 };
  if (w.Element && w.Element.prototype) {
    var origSetAttribute = w.Element.prototype.setAttribute;
    w.Element.prototype.setAttribute = function (name, value) {
      if (typeof value === "string" && URL_ATTRS[String(name).toLowerCase()]) {
        value = rewrite(value);
      }
      return origSetAttribute.call(this, name, value);
    };
    var props = [
      ["HTMLImageElement", "src"], ["HTMLScriptElement", "src"],
      ["HTMLLinkElement", "href"], ["HTMLSourceElement", "src"],
      ["HTMLMediaElement", "src"], ["HTMLVideoElement", "poster"],
      ["HTMLAnchorElement", "href"], ["HTMLIFrameElement", "src"],
      ["HTMLFormElement", "action"]
    ];
    for (var k = 0; k < props.length; k++) {
      (function (ctorName, prop) {
        var C = w[ctorName];
        if (!C || !C.prototype) {
          return;
        }
        var d = Object.getOwnPropertyDescriptor(C.prototype, prop);
        if (!d || typeof d.set !== "function" || !d.configurable) {
          return;
        }
        Object.defineProperty(C.prototype, prop, {
          configurable: true,
          enumerable: d.enumerable,
          get: d.get,
          set: function (v) {
            d.set.call(this, typeof v === "string" ? rewrite(v) : v);
          }
        });
      })(props[k][0], props[k][1]);
    }

    // Markup inserted as HTML strings bypasses the setters above.
    if (w.MutationObserver && w.document) {
      var fixNode = function (el) {
        if (!el || el.nodeType !== 1) {
          return;
        }
        var names = ["src", "href", "poster", "action"];
        for (var n = 0; n < names.length; n++) {
          var v = el.getAttribute(names[n]);
          if (typeof v === "string") {
            var r = rewrite(v);
            if (r !== v) {
              origSetAttribute.call(el, names[n], r);
            }
          }
        }
        var kids = el.querySelectorAll ? el.querySelectorAll("[src],[href],[poster],[action]") : [];
        for (var c = 0; c < kids.length; c++) {
          fixNode(kids[c]);
        }
      };
      new w.MutationObserver(function (records) {
        for (var i = 0; i < records.length; i++) {
          var added = records[i].addedNodes || [];
          for (var j = 0; j < added.length; j++) {
            fixNode(added[j]);
          }
        }
      }).observe(w.document.documentElement || w.document, { childList: true, subtree: true });
    }
  }

  // Per-agent storage namespaces.
  function namespaced(real, ns) {
    function keys() {
      var out = [];
      for (var i = 0; i < real.length; i++) {
        var key = real.key(i);
        if (key !== null && key.indexOf(ns) === 0) {
          out.push(key.slice(ns.length));
        }
      }
      return out;
    }
    var api = {
      getItem: function (key) { return real.getItem(ns + key); },
      setItem: function (key, value) { real.setItem(ns + key, String(value)); },
      removeItem: function (key) { real.removeItem(ns + key); },
      clear: function () {
        var ks = keys();
        for (var i = 0; i < ks.length; i++) {
          real.removeItem(ns + ks[i]);
        }
      },
      key: function (i) {
        var ks = keys();
        return i >= 0 && i < ks.length ? ks[i] : null;
      }
    };
    Object.defineProperty(api, "length", { configurable: true, get: function () { return keys().length; } });
    if (typeof w.Proxy !== "function") {
      return api;
    }
    return new w.Proxy(api, {
      get: function (t, p) {
        if (p in t) {
          return t[p];
        }
        if (typeof p !== "string") {
          return undefined;
        }
        var v = real.getItem(ns + p);
        return v === null ? undefined : v;
      },
      set: function (t, p, v) {
        if (p in t) {
          return false;
        }
        real.setItem(ns + String(p), String(v));
        return true;
      },
      deleteProperty: function (t, p) {
        real.removeItem(ns + String(p));
        return true;
      },
      has: function (t, p) {
        return (p in t) || real.getItem(ns + String(p)) !== null;
      },
      ownKeys: function () {
        return keys();
      },
      getOwnPropertyDescriptor: function (t, p) {
        var v = real.getItem(ns + String(p));
        return v === null ? undefined : { value: v, writable: true, enumerable: true, configurable: true };
      }
    });
  }
  var agent = prefix.split("/")[4];
  var storages = ["localStorage", "sessionStorage"];
  for (var s = 0; s < storages.length; s++) {
    try {
      var real = w[storages[s]];
      if (real) {
        var wrapped = namespaced(real, "agui:" + agent + ":");
        Object.defineProperty(w, storages[s], {
          configurable: true,
          get: (function (v) { return function () { return v; }; })(wrapped)
        });
      }
    } catch (e) {
      // Storage unavailable: leave it as it is.
    }
  }
})(window);

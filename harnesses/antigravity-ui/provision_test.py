#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""Tests for the antigravity-ui bundle: provision.py, agui_helper.py and
static pins on the wrapper, nginx template, Dockerfile and config.yaml."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
import re
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, ".."))
import scion_harness  # type: ignore[import-not-found]  # noqa: E402


def _load(name: str, filename: str):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, filename))
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


provision = _load("antigravity_ui_provision", "provision.py")
helper = _load("antigravity_ui_helper", "agui_helper.py")

TOKEN = "Tok3n-for-tests_0123456789"


def _read(name: str) -> str:
    with open(os.path.join(HERE, name), encoding="utf-8") as f:
        return f.read()


@contextlib.contextmanager
def temporary_home(path: str):
    old = os.environ.get("HOME")
    os.environ["HOME"] = path
    try:
        yield
    finally:
        if old is None:
            os.environ.pop("HOME", None)
        else:
            os.environ["HOME"] = old


@contextlib.contextmanager
def temporary_env(**values: str | None):
    """Set (or, with None, unset) environment variables for a block."""
    old = {k: os.environ.get(k) for k in values}
    for k, v in values.items():
        if v is None:
            os.environ.pop(k, None)
        else:
            os.environ[k] = v
    try:
        yield
    finally:
        for k, v in old.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v


# Ambient identity variables cleared around every provisioner run, so the
# host running the tests cannot change auth resolution.
NO_IDENTITY = dict(GOOGLE_APPLICATION_CREDENTIALS=None, SCION_METADATA_MODE=None,
                   SCION_METADATA_SA_EMAIL=None)


def _make_ctx(home: str, explicit_type: str = "", adc_file: bool = False,
              mapped_adc: bool = False) -> scion_harness.ProvisionContext:
    bundle = os.path.join(home, ".scion", "harness")
    os.makedirs(os.path.join(bundle, "inputs"), exist_ok=True)
    os.makedirs(os.path.join(bundle, "outputs"), exist_ok=True)
    cand = {"schema_version": 1, "env_vars": [], "env_secret_files": {}, "files": []}
    if explicit_type:
        cand["explicit_type"] = explicit_type
    if adc_file:
        os.makedirs(os.path.join(bundle, "secrets"), exist_ok=True)
        with open(os.path.join(bundle, "secrets", "gcloud-adc"), "w", encoding="utf-8") as f:
            f.write("{}")
        cand["file_secret_files"] = {"gcloud-adc": "$HOME/.scion/harness/secrets/gcloud-adc"}
    if mapped_adc:
        # The file mapping Scion adds for GOOGLE_APPLICATION_CREDENTIALS.
        cand["files"] = [{"source_path": "/host/adc.json",
                          "container_path": "~/.config/gcloud/application_default_credentials.json"}]
    cand_path = os.path.join(bundle, "inputs", "auth-candidates.json")
    with open(cand_path, "w", encoding="utf-8") as f:
        json.dump(cand, f)
    manifest = {
        "schema_version": 1,
        "command": "provision",
        "harness_bundle_dir": bundle,
        "harness_config": {},
        "inputs": {"auth_candidates": cand_path},
        "outputs": {
            "env": os.path.join(bundle, "outputs", "env.json"),
            "resolved_auth": os.path.join(bundle, "outputs", "resolved-auth.json"),
        },
    }
    return scion_harness.ProvisionContext("antigravity-ui", manifest)


class _SeedCases:
    """Cases for the updater-off seeding (provision.py)."""

    def seed(self, home: str) -> str:
        raise NotImplementedError

    def storage(self, home: str) -> str:
        return os.path.join(home, ".config", "Antigravity", "app_storage.json")

    def test_creates_file(self):
        with tempfile.TemporaryDirectory() as home:
            self.assertEqual(self.seed(home), "created")
            with open(self.storage(home), encoding="utf-8") as f:
                self.assertEqual(json.load(f), {"autoCheckForUpdates": False})
            self.assertEqual(stat.S_IMODE(os.stat(self.storage(home)).st_mode), 0o600)

    def test_merges_and_keeps_other_keys(self):
        with tempfile.TemporaryDirectory() as home:
            os.makedirs(os.path.dirname(self.storage(home)))
            with open(self.storage(home), "w", encoding="utf-8") as f:
                json.dump({"theme": "dark", "autoCheckForUpdates": True, "nested": {"a": 1}}, f)
            self.assertEqual(self.seed(home), "merged")
            with open(self.storage(home), encoding="utf-8") as f:
                self.assertEqual(
                    json.load(f),
                    {"theme": "dark", "autoCheckForUpdates": False, "nested": {"a": 1}},
                )

    def test_unchanged_when_already_off(self):
        with tempfile.TemporaryDirectory() as home:
            os.makedirs(os.path.dirname(self.storage(home)))
            with open(self.storage(home), "w", encoding="utf-8") as f:
                json.dump({"autoCheckForUpdates": False, "k": "v"}, f)
            self.assertEqual(self.seed(home), "unchanged")

    def test_invalid_json_is_kept_aside_and_replaced(self):
        for bad in ("{not json", "[1, 2]", '"str"'):
            with self.subTest(bad=bad), tempfile.TemporaryDirectory() as home:
                os.makedirs(os.path.dirname(self.storage(home)))
                with open(self.storage(home), "w", encoding="utf-8") as f:
                    f.write(bad)
                self.assertEqual(self.seed(home), "replaced-invalid")
                with open(self.storage(home) + ".invalid", encoding="utf-8") as f:
                    self.assertEqual(f.read(), bad)
                with open(self.storage(home), encoding="utf-8") as f:
                    self.assertEqual(json.load(f), {"autoCheckForUpdates": False})

    def test_empty_file_is_merged(self):
        with tempfile.TemporaryDirectory() as home:
            os.makedirs(os.path.dirname(self.storage(home)))
            open(self.storage(home), "w").close()
            self.assertEqual(self.seed(home), "merged")


class ProvisionSeedTest(_SeedCases, unittest.TestCase):
    def seed(self, home: str) -> str:
        with temporary_home(home), temporary_env(**NO_IDENTITY), contextlib.redirect_stderr(io.StringIO()):
            return provision.seed_update_off(_make_ctx(home))


class ProvisionTest(unittest.TestCase):
    def _run(self, home: str, explicit_type: str = "", adc_file: bool = False,
             mapped_adc: bool = False, **env: str | None):
        with temporary_home(home), temporary_env(**{**NO_IDENTITY, **env}), \
                contextlib.redirect_stderr(io.StringIO()):
            ctx = _make_ctx(home, explicit_type, adc_file, mapped_adc)
            provision.provision(ctx)
            auth_path, env_path = ctx.output_paths()
            with open(auth_path, encoding="utf-8") as f:
                auth = json.load(f)
            with open(env_path, encoding="utf-8") as f:
                overlay = json.load(f)
            return auth, overlay

    def test_no_credentials_resolves_none(self):
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home)
            self.assertEqual(auth["method"], "none")
            self.assertEqual(auth["harness"], "antigravity-ui")
            self.assertEqual(overlay, {})
            self.assertTrue(os.path.exists(os.path.join(home, ".config", "Antigravity", "app_storage.json")))

    def test_staged_adc_file(self):
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home, adc_file=True)
            self.assertEqual(auth["method"], "vertex-ai")
            self.assertTrue(auth["vertex_ai"])
            want = os.path.join(home, ".scion", "harness", "secrets", "gcloud-adc")
            self.assertEqual(overlay, {"AGY_ADC_AUTH": "true", "GOOGLE_APPLICATION_CREDENTIALS": want})

    def test_staged_file_wins_over_mapped_file_and_assignment(self):
        with tempfile.TemporaryDirectory() as home:
            _, overlay = self._run(home, adc_file=True, mapped_adc=True,
                                   SCION_METADATA_MODE="assign", SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com")
            self.assertTrue(overlay["GOOGLE_APPLICATION_CREDENTIALS"].endswith("/secrets/gcloud-adc"))

    def test_provisioner_env_credentials_are_not_used(self):
        # The provisioner runs with a minimal environment; Scion delivers the
        # ADC file as a staged secret or a file mapping instead.
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home, GOOGLE_APPLICATION_CREDENTIALS="/env/adc.json")
            self.assertEqual(auth["method"], "none")
            self.assertEqual(overlay, {})

    def test_mapped_adc_file(self):
        want = None
        with tempfile.TemporaryDirectory() as home:
            want = os.path.join(home, ".config", "gcloud", "application_default_credentials.json")
            auth, overlay = self._run(home, mapped_adc=True)
            self.assertEqual(auth["method"], "vertex-ai")
            self.assertEqual(overlay, {"AGY_ADC_AUTH": "true", "GOOGLE_APPLICATION_CREDENTIALS": want})

    def test_adc_file_present_at_the_mapped_path(self):
        with tempfile.TemporaryDirectory() as home:
            path = os.path.join(home, ".config", "gcloud", "application_default_credentials.json")
            os.makedirs(os.path.dirname(path))
            with open(path, "w") as f:
                f.write("{}")
            auth, overlay = self._run(home)
            self.assertEqual(auth["method"], "vertex-ai")
            self.assertEqual(overlay["GOOGLE_APPLICATION_CREDENTIALS"], path)

    def test_explicit_vertex_ai_with_plain_passthrough_does_not_fail(self):
        # Dispatch selects vertex-ai for passthrough; without an SA email it
        # is the host identity, which is used only through the operator's
        # AGY_ADC_AUTH (read by the app, not here).
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home, "vertex-ai", SCION_METADATA_MODE="passthrough")
            self.assertEqual(auth["method"], "none")
            self.assertEqual(overlay, {})

    def test_explicit_vertex_ai_with_rewritten_assign(self):
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home, "vertex-ai", SCION_METADATA_MODE="passthrough",
                                      SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com")
            self.assertEqual(auth["method"], "vertex-ai")
            self.assertEqual(overlay, {"AGY_ADC_AUTH": "true"})

    def test_assigned_service_account(self):
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home, SCION_METADATA_MODE="assign",
                                      SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com")
            self.assertEqual(auth["method"], "vertex-ai")
            self.assertEqual(overlay, {"AGY_ADC_AUTH": "true"})

    def test_assign_rewritten_to_passthrough_counts(self):
        # A Kubernetes assign is carried as passthrough with the SA email.
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home, SCION_METADATA_MODE="passthrough",
                                      SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com")
            self.assertEqual(auth["method"], "vertex-ai")
            self.assertEqual(overlay, {"AGY_ADC_AUTH": "true"})

    def test_other_identity_modes_do_not_count(self):
        cases = [
            dict(SCION_METADATA_MODE="passthrough", SCION_METADATA_SA_EMAIL=None),
            dict(SCION_METADATA_MODE="passthrough", SCION_METADATA_SA_EMAIL=""),
            dict(SCION_METADATA_MODE="block", SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com"),
            dict(SCION_METADATA_MODE="block", SCION_METADATA_SA_EMAIL=None),
            dict(SCION_METADATA_MODE="assign", SCION_METADATA_SA_EMAIL=""),
        ]
        for env in cases:
            with self.subTest(**env), tempfile.TemporaryDirectory() as home:
                auth, overlay = self._run(home, **env)
                self.assertEqual(auth["method"], "none")
                self.assertNotIn("AGY_ADC_AUTH", overlay)

    def test_explicit_vertex_ai(self):
        with tempfile.TemporaryDirectory() as home:
            auth, _ = self._run(home, "vertex-ai", SCION_METADATA_MODE="assign",
                                SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com")
            self.assertEqual(auth["method"], "vertex-ai")
        with tempfile.TemporaryDirectory() as home:
            with self.assertRaisesRegex(scion_harness.ProvisionError, "no credentials found"):
                self._run(home, "vertex-ai")

    def test_unknown_explicit_type_is_rejected(self):
        for typ in ("api-key", "oauth-token", "auth-file"):
            with self.subTest(typ=typ), tempfile.TemporaryDirectory() as home:
                with self.assertRaisesRegex(scion_harness.ProvisionError, "unknown auth type"):
                    self._run(home, typ)

    def test_declares_one_auth_spec_matching_config(self):
        specs = [v for v in vars(provision).values() if isinstance(v, scion_harness.AuthSpec)]
        self.assertEqual(len(specs), 1)
        self.assertEqual(specs[0].valid_types(), ("vertex-ai",))
        self.assertIn("default_type: vertex-ai", _read("config.yaml"))


class FreePortTest(unittest.TestCase):
    def test_preferred_port_when_free(self):
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        self.assertEqual(helper.free_port(str(port)), port)

    def test_other_port_when_taken(self):
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            s.listen(1)
            port = s.getsockname()[1]
            got = helper.free_port(str(port))
            self.assertNotEqual(got, port)
            self.assertTrue(1024 <= got <= 65535)

    def test_rejects_non_ports(self):
        for bad in ("abc", "0", "70000"):
            with self.subTest(bad=bad), self.assertRaises(helper.HelperError):
                helper.free_port(bad)


class PortOwnerTest(unittest.TestCase):
    """port-owner against a fake /proc."""

    def setUp(self):
        self.proc = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.proc)
        os.makedirs(os.path.join(self.proc, "net"))
        self.tcp = []

    def _listen(self, port: int, inode: int, state: str = "0A", v6: bool = False, addr: str | None = None):
        if addr is None:
            addr = "00000000000000000000000001000000" if v6 else "0100007F"
        local = addr + ":%04X" % port
        line = "  0: %s 00000000:0000 %s 00000000:00000000 00:00000000 00000000  1000  0 %d 1" % (local, state, inode)
        name = "tcp6" if v6 else "tcp"
        path = os.path.join(self.proc, "net", name)
        header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
        mode = "a" if os.path.exists(path) else "w"
        with open(path, mode) as f:
            if mode == "w":
                f.write(header)
            f.write(line + "\n")

    def _pid(self, pid: int, ppid: int, sockets=(), comm="proc (x)", sid=None):
        d = os.path.join(self.proc, str(pid))
        os.makedirs(os.path.join(d, "fd"))
        with open(os.path.join(d, "stat"), "w") as f:
            f.write("%d (%s) S %d %d %d 0" % (pid, comm, ppid, pid, sid if sid is not None else pid))
        for i, inode in enumerate(sockets):
            os.symlink("socket:[%d]" % inode, os.path.join(d, "fd", str(10 + i)))

    def test_listener_held_by_root(self):
        self._listen(8443, 555)
        self._pid(100, 1, [555])
        self.assertEqual(helper.port_owner("8443", "100", self.proc), "ours")

    def test_listener_held_by_a_descendant(self):
        # The language server is a child (or grandchild) of the app.
        self._listen(8443, 556, v6=True)
        self._pid(100, 1)
        self._pid(101, 100)
        self._pid(102, 101, [556], comm="language server")
        self.assertEqual(helper.port_owner("8443", "100", self.proc), "ours")

    def test_listener_held_in_the_same_session(self):
        # Double-forked out of the parent chain (reparented to 1), but still
        # in the app's session.
        self._listen(8443, 561)
        self._pid(100, 1)
        self._pid(103, 1, [561], sid=100)
        self.assertEqual(helper.port_owner("8443", "100", self.proc), "ours")

    def test_listener_held_by_someone_else(self):
        self._listen(8443, 557)
        self._pid(100, 1)
        self._pid(200, 1, [557])
        self.assertEqual(helper.port_owner("8443", "100", self.proc), "foreign")

    def test_shared_port_with_a_foreign_listener_is_foreign(self):
        # Two listeners on one port (SO_REUSEPORT): ours and someone else's.
        self._listen(8443, 562)
        self._listen(8443, 563)
        self._pid(100, 1, [562])
        self._pid(200, 1, [563])
        self.assertEqual(helper.port_owner("8443", "100", self.proc), "foreign")

    def test_listener_invisible_to_us_is_foreign(self):
        # Another container's process: its socket is in the shared net
        # table, but no visible process holds it.
        self._listen(8443, 558)
        self._pid(100, 1, [999])
        self.assertEqual(helper.port_owner("8443", "100", self.proc), "foreign")

    def test_no_listener(self):
        self._listen(8443, 559, state="01")  # established, not listening
        self._listen(9000, 560)
        self._pid(100, 1, [559, 560])
        self.assertEqual(helper.port_owner("8443", "100", self.proc), "none")

    def test_listen_addr_loopback(self):
        self._listen(8443, 570)                                   # 127.0.0.1
        self._listen(8443, 571, v6=True)                          # ::1
        self._listen(8443, 572, v6=True, addr="0000000000000000FFFF00000100007F")  # ::ffff:127.0.0.1
        self._pid(100, 1, [570, 571, 572])
        self.assertEqual(helper.listen_addr("8443", "100", self.proc), "loopback")

    def test_listen_addr_wildcard_is_exposed(self):
        self._listen(8443, 573, addr="00000000")                  # 0.0.0.0
        self._listen(8443, 574)                                   # 127.0.0.1
        self._pid(100, 1, [573, 574])
        self.assertEqual(helper.listen_addr("8443", "100", self.proc), "exposed 0.0.0.0")

    def test_listen_addr_v6_wildcard_and_lan_address(self):
        self._listen(8443, 575, v6=True, addr="00000000000000000000000000000000")  # ::
        self._listen(8443, 576, addr="0100000A")                  # 10.0.0.1
        self._pid(100, 1, [575, 576])
        self.assertEqual(helper.listen_addr("8443", "100", self.proc), "exposed 10.0.0.1,::")

    def test_listen_addr_ignores_listeners_of_others(self):
        self._listen(8443, 577, addr="00000000")
        self._pid(100, 1)
        self._pid(200, 1, [577])
        self.assertEqual(helper.listen_addr("8443", "100", self.proc), "none")

    def test_bad_arguments(self):
        with self.assertRaises(helper.HelperError):
            helper.port_owner("0", "100", self.proc)
        with self.assertRaises(helper.HelperError):
            helper.port_owner("8443", "x", self.proc)


class RenderNginxTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)
        self.state = os.path.join(self.tmp, "state")
        os.makedirs(self.state)
        self.out = os.path.join(self.state, "nginx.conf")

    def _render(self, *extra: str) -> tuple[int, str, str]:
        argv = [
            "render-nginx",
            "--template", os.path.join(HERE, "nginx.conf.tmpl"),
            "--out", self.out,
            "--state", self.state,
            "--proxy-port", "8090",
            "--ls-port", "8443",
            *extra,
        ]
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = helper.main(argv)
        return rc, out.getvalue(), err.getvalue()

    def _conf(self) -> str:
        with open(self.out, encoding="utf-8") as f:
            return f.read()

    def test_default_render(self):
        rc, _, err = self._render()
        self.assertEqual(rc, 0, err)
        conf = self._conf()
        self.assertNotIn("${", conf)
        self.assertIn("listen 127.0.0.1:8090;", conf)
        self.assertIn("proxy_pass https://127.0.0.1:8443;", conf)
        self.assertIn("proxy_set_header Host 127.0.0.1:8443;", conf)
        self.assertNotIn("Origin", conf)
        self.assertIn("pid %s/nginx.pid;" % self.state, conf)
        self.assertNotIn("x-codeium-csrf-token", conf)
        self.assertNotRegex(conf, r"(?m)^\s*sub_filter")
        # nginx's own variables survive rendering.
        self.assertIn("$http_upgrade", conf)
        self.assertIn("$connection_upgrade", conf)
        self.assertEqual(stat.S_IMODE(os.stat(self.out).st_mode), 0o600)
        for sub in ("nx-body", "nx-proxy", "nx-fastcgi", "nx-uwsgi", "nx-scgi"):
            self.assertTrue(os.path.isdir(os.path.join(self.state, sub)), sub)

    def test_shim_tier(self):
        rc, _, err = self._render("--path-rewrite", "shim")
        self.assertEqual(rc, 0, err)
        conf = self._conf()
        self.assertNotIn("${", conf)
        self.assertIn("""sub_filter '<head>' '<head><script src="agui-shim.js"></script>';""", conf)
        self.assertIn("""sub_filter '="/' '="';""", conf)
        self.assertIn("location ~ /agui-shim\\.js$ {", conf)
        self.assertIn("root %s;" % HERE, conf)
        self.assertIn("try_files /agui_shim.js =404;", conf)
        # The shim is served only by nginx, never proxied to the app, so an
        # app path of the same name cannot replace it.
        shim_block = conf[conf.index("location ~ /agui-shim"):]
        shim_block = shim_block[:shim_block.index("}")]
        self.assertNotIn("proxy_pass", shim_block)
        self.assertIn("location = /main.js {", conf)
        for token, replacement in helper.SHIM_PATCHES:
            self.assertIn("sub_filter '%s' '%s';" % (token, replacement), conf)
        # Only loopback upstreams, also in the shim locations.
        passes = set(re.findall(r"proxy_pass\s+([^;]+);", conf))
        self.assertEqual(passes, {"https://127.0.0.1:8443"})
        # nginx rejects config tokens over 4 KiB.
        self.assertLess(max(len(line) for line in conf.splitlines()), 1024)

    def test_shim_tier_needs_the_shim_file(self):
        with self.assertRaisesRegex(helper.HelperError, "not found"):
            helper.shim_rules("8443", self.tmp)

    def test_shim_patches_are_nginx_safe_and_change_something(self):
        for token, replacement in helper.SHIM_PATCHES:
            for value in (token, replacement):
                for c in ("'", "$", "\\"):
                    self.assertNotIn(c, value)
            self.assertNotEqual(token, replacement)

    def test_bad_inputs(self):
        cases = [
            dict(state="relative/path"),
            dict(state="/tmp/has space"),
            dict(state="/tmp/semi;colon"),
            dict(proxy_port="80a"),
            dict(ls_port="70000"),
            dict(path_rewrite="regex"),
        ]
        base = dict(state="/tmp/ok", proxy_port="8090", ls_port="8443", path_rewrite="none")
        for override in cases:
            with self.subTest(**override):
                with self.assertRaises(helper.HelperError):
                    helper.build_values(**{**base, **override})

    def test_shim_check_text(self):
        page = "<html><head></head><body></body></html>"
        js = " ".join(t for t, _ in helper.SHIM_PATCHES)
        self.assertEqual(helper.shim_check_text(page, js), "shim")
        self.assertTrue(helper.shim_check_text("<html></html>", js).startswith("none:"))
        self.assertTrue(helper.shim_check_text(page, js.split(" ")[0]).startswith("none:"))
        self.assertTrue(helper.shim_check_text(page, js + " " + js).startswith("none:"))
        # A protocol-relative attribute would be broken by the ="/ rule.
        page_pr = '<html><head><script src="//cdn.example/x.js"></script></head></html>'
        self.assertEqual(helper.shim_check_text(page_pr, js),
                         "none: the app page has a protocol-relative URL attribute")

    def test_shim_check_forced_mismatch(self):
        # The test hook never contacts the app.
        self.assertEqual(helper.shim_check("1", force_mismatch=True),
                         "none: forced version mismatch (test hook)")
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = helper.main(["shim-check", "--ls-port", "1", "--force-mismatch"])
        self.assertEqual(rc, 0)
        self.assertIn("forced version mismatch", out.getvalue())

    def test_shim_check_unreachable(self):
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = str(s.getsockname()[1])
        self.assertTrue(helper.shim_check(port).startswith("none: could not read"))

    def test_unknown_placeholder_is_an_error(self):
        with self.assertRaisesRegex(helper.HelperError, "TYPO"):
            helper.render_template("listen ${TYPO};", {"STATE": "/x"})

    def test_template_placeholders_match_helper(self):
        found = set(re.findall(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}", _read("nginx.conf.tmpl")))
        self.assertEqual(found, set(helper.PLACEHOLDERS))


class SecurityPinsTest(unittest.TestCase):
    """Static pins on the files baked into the image."""

    def test_template_listens_and_proxies_on_loopback_only(self):
        tmpl = _read("nginx.conf.tmpl")
        listens = re.findall(r"^\s*listen\s+([^;]+);", tmpl, re.M)
        self.assertEqual(listens, ["127.0.0.1:${AGUI_PROXY_PORT}"])
        passes = re.findall(r"^\s*proxy_pass\s+([^;]+);", tmpl, re.M)
        self.assertEqual(passes, ["https://127.0.0.1:${JETSKI_LS_PORT}"])

    def test_wrapper_never_exposes_language_server_directly(self):
        wrapper = _read("antigravity-ui-wrapper.sh")
        self.assertNotIn("remote_accessible", wrapper)
        self.assertNotIn("SCION_AUTO_EXPOSE", wrapper)
        exposes = re.findall(r"scion expose\s+(\S+)", wrapper)
        self.assertTrue(exposes)
        self.assertEqual(set(exposes), {'"$AGUI_PROXY_PORT"'})

    def test_config_does_not_enable_auto_expose(self):
        self.assertNotIn("SCION_AUTO_EXPOSE", _read("config.yaml"))

    def test_wrapper_ignores_sigint_and_isolates_children(self):
        wrapper = _read("antigravity-ui-wrapper.sh")
        self.assertIn("trap '' INT", wrapper)
        self.assertRegex(wrapper, r"setsid \"\$AGUI_APP_BIN\"[^\n]*\\\n\s*<\"\$APP_IN\"")
        self.assertIn('mkfifo -m 600 "$APP_IN"', wrapper)
        self.assertRegex(wrapper, r"setsid nginx [^\n]*\\\n\s*</dev/null")

    def test_dockerfile_pins_and_verifies_the_package(self):
        df = _read("Dockerfile")
        self.assertRegex(df, r"ARG AG_DEB_SHA256=[0-9a-f]{64}\n")
        self.assertIn('sha256sum -c -', df)
        self.assertIn('/${AG_VERSION}/linux-x64/Antigravity.deb"', df)
        self.assertIn('[ "${TARGETARCH:-amd64}" != "amd64" ]', df)
        self.assertIn("chmod -R go-w /opt/Antigravity", df)
        self.assertIn("chown -R root:root /opt/Antigravity", df)
        self.assertNotIn("install.sh", df.replace("install script", ""))

    def test_dockerfile_creates_share_dir_traversable_before_copy(self):
        # A COPY --chmod=0644 into a missing directory creates it 0644, and
        # the agent user then cannot run the helper.
        df = _read("Dockerfile")
        mkdir = df.find("install -d -m 0755 /usr/local/share/antigravity-ui")
        self.assertNotEqual(mkdir, -1)
        first_copy = df.find("/usr/local/share/antigravity-ui/", mkdir + 1)
        self.assertGreater(first_copy, mkdir)
        self.assertLess(mkdir, df.index("\nCOPY "))


class WrapperStartupTest(unittest.TestCase):
    """Runs the wrapper with a fake app; no network listener is started."""

    def setUp(self):
        for tool in ("bash", "setsid", "curl"):
            if not shutil.which(tool):
                self.skipTest(f"{tool} not found")
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.bin = os.path.join(self.tmp, "bin")
        os.makedirs(self.bin)
        os.makedirs(os.path.join(self.tmp, "home"))
        # A fake scion that always fails, so the test can never reach a hub.
        self._script("scion", "#!/bin/sh\nexit 1\n")

    def _script(self, name: str, body: str) -> str:
        path = os.path.join(self.bin, name)
        with open(path, "w", encoding="utf-8") as f:
            f.write(body)
        os.chmod(path, 0o755)
        return path

    @staticmethod
    def _unused_port() -> str:
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            return str(s.getsockname()[1])

    def _env(self, app: str, timeout: str, **extra: str) -> dict[str, str]:
        env = {
            "HOME": os.path.join(self.tmp, "home"),
            "PATH": self.bin + os.pathsep + "/usr/local/bin:/usr/bin:/bin",
            "AGUI_APP_BIN": app,
            "AGUI_SHARE_DIR": HERE,
            "AGUI_LS_TIMEOUT": timeout,
            # Nothing listens there: the fake app never serves.
            "JETSKI_LS_PORT": self._unused_port(),
            "PYTHONDONTWRITEBYTECODE": "1",
        }
        env.update(extra)
        return env

    def _run(self, app: str, timeout: str, **extra: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["bash", os.path.join(HERE, "antigravity-ui-wrapper.sh")],
            env=self._env(app, timeout, **extra), stdin=subprocess.DEVNULL,
            capture_output=True, text=True, timeout=60,
        )

    SIGNIN_APP = (
        "#!/bin/sh\n"
        "echo 'Go to https://accounts.google.com/o/oauth2/auth?client_id=x&state=y'\n"
        "echo 'Enter the authorization code:'\n"
        "IFS= read -r code\n"
        "printf '%s' \"$code\" > \"$HOME/received-code\"\n"
        "exit 7\n"
    )

    def test_sign_in_prompt_fails_without_showing_the_url(self):
        app = self._script("fakeapp", self.SIGNIN_APP)
        proc = self._run(app, "40")
        out = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 1, out)
        self.assertIn("not signed in", out)
        self.assertIn("assign a dedicated GCP service account".lower(), out.lower())
        self.assertNotIn("accounts.google.com", out)
        self.assertNotIn("did not answer", out)

    def test_taken_port_falls_back_to_a_free_one(self):
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            s.listen(1)
            taken = str(s.getsockname()[1])
            proc = self._run("/bin/false", "5", JETSKI_LS_PORT=taken)
        out = proc.stdout + proc.stderr
        self.assertIn(f"port {taken} is not available", out)
        self.assertIn("the app exited during start-up", out)

    def test_sign_in_prompt_under_passthrough_names_the_opt_in(self):
        app = self._script("fakeapp", self.SIGNIN_APP)
        proc = self._run(app, "40", SCION_METADATA_MODE="passthrough")
        out = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 1, out)
        self.assertIn("With GCP identity passthrough the host identity is not used by default", out)
        self.assertIn("AGY_ADC_AUTH=true in the harness-config env", out)
        self.assertNotIn("accounts.google.com", out)

    def test_app_exit_during_start_up(self):
        proc = self._run("/bin/false", "40")
        out = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 1, out)
        self.assertIn("the app exited during start-up", out)

    def test_log_tail_masks_csrf_token(self):
        app = self._script("fakeapp", (
            "#!/bin/sh\n"
            "echo 'starting --csrf_token=" + TOKEN + " now'\n"
            "exec sleep 50\n"
        ))
        proc = self._run(app, "2")
        out = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 1, out)
        self.assertIn("did not answer", out)
        self.assertNotIn(TOKEN, out)
        self.assertIn("<redacted>", out)

    def test_log_tail_hides_sign_in_url(self):
        app = self._script("fakeapp", (
            "#!/bin/sh\n"
            "echo 'Go to https://accounts.google.com/o/oauth2/auth?state=secret-state'\n"
            "exit 3\n"
        ))
        proc = self._run(app, "5")
        out = proc.stdout + proc.stderr
        self.assertIn("the app exited during start-up", out)
        self.assertNotIn("secret-state", out)
        self.assertIn("<sign-in URL hidden>", out)

    def test_wrapper_shell_syntax(self):
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("bash not found")
        proc = subprocess.run([bash, "-n", os.path.join(HERE, "antigravity-ui-wrapper.sh")],
                              capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stderr)


FAKE_APP = r"""#!/usr/bin/env python3
# Fake app for the wrapper tests: serves HTTPS on JETSKI_LS_PORT.
# FAKE_MODE: serve | late-prompt | foreign-first | foreign-then-exit.
# FAKE_WEB_DIR: directory to serve (default: the current directory).
import functools, http.server, os, ssl, sys, threading, time

home = os.environ["HOME"]
port = int(os.environ["JETSKI_LS_PORT"])
cert = os.environ["FAKE_CERT_DIR"]
mode = os.environ.get("FAKE_MODE", "serve")


handler = functools.partial(http.server.SimpleHTTPRequestHandler,
                            directory=os.environ.get("FAKE_WEB_DIR") or os.getcwd())


def serve_http(p):
    http.server.HTTPServer(("127.0.0.1", p), handler).serve_forever()


def serve_https(p):
    srv = http.server.HTTPServer((os.environ.get("FAKE_BIND") or "127.0.0.1", p), handler)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(os.path.join(cert, "cert.pem"), os.path.join(cert, "key.pem"))
    srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
    srv.serve_forever()


attempt_file = os.path.join(home, "fake-attempts")
attempt = int(open(attempt_file).read()) + 1 if os.path.exists(attempt_file) else 1
open(attempt_file, "w").write(str(attempt))
with open(os.path.join(home, "fake-ports"), "a") as f:
    f.write("%d\n" % port)
open(os.path.join(home, "app.pid"), "w").write(str(os.getpid()))

if mode == "foreign-first" and attempt == 1:
    # A listener that is not part of this process tree takes the port.
    if os.fork() == 0:
        os.setsid()
        if os.fork() == 0:
            open(os.path.join(home, "foreign.pid"), "w").write(str(os.getpid()))
            serve_https(port)
        os._exit(0)
    time.sleep(120)
    sys.exit(0)

if mode == "foreign-then-exit" and attempt == 1:
    # Another process (plain HTTP, so the HTTPS probe fails) takes the port
    # and this app exits, as an app that lost the bind race would.
    if os.fork() == 0:
        os.setsid()
        if os.fork() == 0:
            open(os.path.join(home, "foreign.pid"), "w").write(str(os.getpid()))
            serve_http(port)
        os._exit(0)
    time.sleep(1)
    sys.exit(1)

if mode == "late-prompt":
    def later():
        time.sleep(3)
        print("Go to https://accounts.google.com/o/oauth2/auth?state=hidden-state", flush=True)
        print("Enter the authorization code:", flush=True)
    threading.Thread(target=later, daemon=True).start()

serve_https(port)
"""

FAKE_NGINX = r"""#!/usr/bin/env python3
# Fake nginx: serves plain HTTP on the port in the rendered config.
# FAKE_NGINX_MODE=lose-first: on the first run another process takes the
# port (a raw listener that closes every connection, so the HTTP probe
# fails) and this one fails to bind and exits, as nginx would.
import http.server, os, re, socket, sys, time
home = os.environ["HOME"]
conf = open(sys.argv[sys.argv.index("-c") + 1]).read()
err = sys.argv[sys.argv.index("-e") + 1]
port = int(re.search(r"listen 127\.0\.0\.1:(\d+);", conf).group(1))
with open(os.path.join(home, "nginx-ports"), "a") as f:
    f.write("%d\n" % port)
runs = os.path.join(home, "nginx-runs")
run = int(open(runs).read()) + 1 if os.path.exists(runs) else 1
open(runs, "w").write(str(run))
if os.environ.get("FAKE_NGINX_MODE") == "lose-first" and run == 1:
    if os.fork() == 0:
        os.setsid()
        if os.fork() == 0:
            open(os.path.join(home, "foreign-nginx.pid"), "w").write(str(os.getpid()))
            s = socket.socket()
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind(("127.0.0.1", port))
            s.listen(8)
            while True:
                c, _ = s.accept()
                c.close()
        os._exit(0)
    time.sleep(1)
    with open(err, "a") as f:
        f.write("nginx: [emerg] bind() to 127.0.0.1:%d failed (98: Address already in use)\n" % port)
    sys.exit(1)
open(os.path.join(home, "nginx.pid"), "w").write(str(os.getpid()))
http.server.HTTPServer(("127.0.0.1", port), http.server.SimpleHTTPRequestHandler).serve_forever()
"""

FAKE_SCION = """#!/bin/sh
# Fake scion: records expose calls; refuses --app-mode (no app mode on this hub).
echo "$*" >> "$HOME/scion-calls"
for a in "$@"; do [ "$a" = "--app-mode" ] && exit 1; done
exit 0
"""


class WrapperRunningTest(unittest.TestCase):
    """Wrapper behaviour after start-up, with a fake HTTPS app and fake nginx."""

    def setUp(self):
        for tool in ("bash", "setsid", "curl", "openssl"):
            if not shutil.which(tool):
                self.skipTest(f"{tool} not found")
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "home")
        self.bin = os.path.join(self.tmp, "bin")
        self.certs = os.path.join(self.tmp, "certs")
        for d in (self.home, self.bin, self.certs):
            os.makedirs(d)
        subprocess.run(
            ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
             "-subj", "/CN=127.0.0.1", "-keyout", os.path.join(self.certs, "key.pem"),
             "-out", os.path.join(self.certs, "cert.pem")],
            check=True, capture_output=True,
        )
        for name, body in (("fakeapp", FAKE_APP), ("nginx", FAKE_NGINX), ("scion", FAKE_SCION)):
            path = os.path.join(self.bin, name)
            with open(path, "w", encoding="utf-8") as f:
                f.write(body)
            os.chmod(path, 0o755)
        self.out_path = os.path.join(self.tmp, "out.txt")
        self.proc = None
        self.addCleanup(self._cleanup)

    def _cleanup(self):
        if self.proc and self.proc.stdin:
            try:
                self.proc.stdin.close()
            except OSError:
                pass
        if self.proc and self.proc.poll() is None:
            self.proc.send_signal(signal.SIGTERM)
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        for name in ("foreign.pid", "foreign-nginx.pid", "nginx.pid", "app.pid"):
            pid = self._read_pid(name)
            if pid:
                try:
                    os.kill(pid, signal.SIGKILL)
                except OSError:
                    pass

    def _read_pid(self, name: str) -> int | None:
        try:
            with open(os.path.join(self.home, name)) as f:
                return int(f.read().strip())
        except (OSError, ValueError):
            return None

    @staticmethod
    def _unused_port() -> str:
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            return str(s.getsockname()[1])

    def _start(self, **extra: str):
        env = {
            "HOME": self.home,
            "PATH": self.bin + os.pathsep + "/usr/local/bin:/usr/bin:/bin",
            "AGUI_APP_BIN": os.path.join(self.bin, "fakeapp"),
            "AGUI_SHARE_DIR": HERE,
            "AGUI_LS_TIMEOUT": "20",
            "JETSKI_LS_PORT": self._unused_port(),
            "AGUI_PROXY_PORT": self._unused_port(),
            "FAKE_CERT_DIR": self.certs,
            "PYTHONDONTWRITEBYTECODE": "1",
        }
        env.update(extra)
        self.out = open(self.out_path, "w")
        self.addCleanup(self.out.close)
        self.proc = subprocess.Popen(
            ["bash", os.path.join(HERE, "antigravity-ui-wrapper.sh")],
            env=env, stdin=subprocess.PIPE, stdout=self.out, stderr=subprocess.STDOUT, text=True,
        )

    def _output(self) -> str:
        with open(self.out_path) as f:
            return f.read()

    def _wait_for(self, text: str, timeout: float = 40) -> str:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            out = self._output()
            if text in out:
                return out
            if self.proc.poll() is not None:
                break
            time.sleep(0.2)
        self.fail(f"{text!r} not seen; output:\n{self._output()}")

    def _wait_exit(self, timeout: float = 30) -> int:
        try:
            return self.proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            self.fail(f"wrapper did not exit; output:\n{self._output()}")

    def test_runs_and_ends_with_the_app(self):
        self._start()
        self._wait_for("Messages and tasks are not delivered")
        out = self._output()
        self.assertIn("path shim off", out)  # the fake app is not the pinned version
        with open(os.path.join(self.home, "scion-calls")) as f:
            calls = f.read()
        self.assertNotIn("--app-mode", calls)  # no app mode without the shim
        self.assertIn("--label Antigravity (use scion tunnel)", calls)
        # A pasted message gets a notice; SIGINT changes nothing.
        self.proc.stdin.write("hello\n")
        self.proc.stdin.flush()
        self._wait_for("message not delivered")
        os.kill(self.proc.pid, signal.SIGINT)
        time.sleep(1)
        self.assertIsNone(self.proc.poll())
        # The app exiting ends the wrapper with its code, and stops nginx.
        app_pid = int(subprocess.run(["pgrep", "-P", str(self.proc.pid), "-f", "fakeapp"],
                                     capture_output=True, text=True).stdout.split()[0])
        os.kill(app_pid, signal.SIGTERM)
        self.assertEqual(self._wait_exit(), 128 + signal.SIGTERM)
        self.assertIn("the app exited", self._output())
        nginx_pid = self._read_pid("nginx.pid")
        time.sleep(0.5)
        with self.assertRaises(OSError):
            os.kill(nginx_pid, 0)

    def test_forced_mismatch_banner_and_plain_expose(self):
        self._start(AGUI_SHIM_FORCE_MISMATCH="1")
        out = self._wait_for("Messages and tasks are not delivered")
        self.assertIn("path shim off: forced version mismatch (test hook)", out)
        self.assertIn("cannot show the UI (forced version mismatch (test hook))", out)
        with open(os.path.join(self.home, "scion-calls")) as f:
            calls = f.read().splitlines()
        self.assertEqual(len(calls), 1, calls)
        self.assertNotIn("--app-mode", calls[0])

    def test_nginx_death_ends_the_wrapper(self):
        self._start()
        self._wait_for("Messages and tasks are not delivered")
        os.kill(self._read_pid("nginx.pid"), signal.SIGKILL)
        self.assertEqual(self._wait_exit(), 1)
        self.assertIn("nginx exited", self._output())

    def test_sign_in_prompt_after_start_stops_the_agent(self):
        self._start(FAKE_MODE="late-prompt")
        self._wait_for("Messages and tasks are not delivered")
        self.assertEqual(self._wait_exit(), 1)
        out = self._output()
        self.assertIn("not signed in", out)
        self.assertNotIn("accounts.google.com", out)
        self.assertNotIn("hidden-state", out)

    def test_shim_on_path(self):
        # The fake app serves a page and a main.js with the patch points, as
        # the pinned app does.
        web = os.path.join(self.tmp, "web")
        os.makedirs(web)
        with open(os.path.join(web, "index.html"), "w") as f:
            f.write('<html><head><script src="/main.js"></script></head><body></body></html>')
        with open(os.path.join(web, "main.js"), "w") as f:
            f.write(" ".join(t for t, _ in helper.SHIM_PATCHES))
        self._start(FAKE_WEB_DIR=web)
        out = self._wait_for("Messages and tasks are not delivered")
        self.assertIn("nginx config rendered (path_rewrite=shim)", out)
        self.assertIn("App mode is not available here", out)
        with open(os.path.join(self.home, ".local", "state", "antigravity-ui", "nginx.conf")) as f:
            self.assertIn("location = /main.js {", f.read())
        with open(os.path.join(self.home, "scion-calls")) as f:
            calls = f.read().splitlines()
        self.assertEqual(len(calls), 2, calls)
        self.assertIn("--app-mode", calls[0])
        self.assertNotIn("--app-mode", calls[1])

    def test_sigterm_stops_everything(self):
        self._start()
        self._wait_for("Messages and tasks are not delivered")
        app_pid, nginx_pid = self._read_pid("app.pid"), self._read_pid("nginx.pid")
        self.proc.send_signal(signal.SIGTERM)
        self.assertEqual(self._wait_exit(), 143)
        time.sleep(0.5)
        for pid in (app_pid, nginx_pid):
            with self.assertRaises(OSError):
                os.kill(pid, 0)

    def test_proxy_port_lost_to_another_process_is_retried(self):
        self._start(FAKE_NGINX_MODE="lose-first")
        out = self._wait_for("Messages and tasks are not delivered")
        self.assertIn("trying another port", out)
        self.assertNotIn("nginx exited during start-up", out)
        with open(os.path.join(self.home, "nginx-ports")) as f:
            ports = f.read().split()
        self.assertEqual(len(ports), 2, ports)
        self.assertNotEqual(ports[0], ports[1])
        self.assertIn("proxy is up on 127.0.0.1:%s" % ports[1], out)

    def test_app_that_exits_after_losing_its_port_is_retried(self):
        self._start(FAKE_MODE="foreign-then-exit")
        out = self._wait_for("Messages and tasks are not delivered")
        self.assertIn("trying another port", out)
        with open(os.path.join(self.home, "fake-ports")) as f:
            ports = f.read().split()
        self.assertEqual(len(ports), 2, ports)
        self.assertIn("language server is up on 127.0.0.1:%s" % ports[1], out)

    def test_language_server_on_a_non_loopback_address_fails(self):
        self._start(FAKE_BIND="0.0.0.0")
        self.assertEqual(self._wait_exit(), 1)
        out = self._output()
        self.assertIn("is not loopback-only (exposed 0.0.0.0)", out)
        self.assertNotIn("Messages and tasks are not delivered", out)

    def test_foreign_listener_on_the_port_is_not_used(self):
        self._start(FAKE_MODE="foreign-first")
        out = self._wait_for("Messages and tasks are not delivered")
        self.assertIn("is answered by a process outside this agent; trying another port", out)
        with open(os.path.join(self.home, "fake-ports")) as f:
            ports = f.read().split()
        self.assertEqual(len(ports), 2, ports)
        self.assertNotEqual(ports[0], ports[1])
        self.assertIn("language server is up on 127.0.0.1:%s" % ports[1], out)


if __name__ == "__main__":
    unittest.main()

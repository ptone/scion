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
import stat
import subprocess
import sys
import tempfile
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


def _make_ctx(home: str, explicit_type: str = "") -> scion_harness.ProvisionContext:
    bundle = os.path.join(home, ".scion", "harness")
    os.makedirs(os.path.join(bundle, "inputs"), exist_ok=True)
    os.makedirs(os.path.join(bundle, "outputs"), exist_ok=True)
    cand = {"schema_version": 1, "env_vars": [], "env_secret_files": {}, "files": []}
    if explicit_type:
        cand["explicit_type"] = explicit_type
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
    """Shared cases for the two updater-off implementations."""

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
        with temporary_home(home), contextlib.redirect_stderr(io.StringIO()):
            return provision.seed_update_off(_make_ctx(home))


class HelperSeedTest(_SeedCases, unittest.TestCase):
    def seed(self, home: str) -> str:
        return helper.seed_update_off(home)


class ProvisionTest(unittest.TestCase):
    def _run(self, home: str, explicit_type: str = ""):
        with temporary_home(home), contextlib.redirect_stderr(io.StringIO()):
            ctx = _make_ctx(home, explicit_type)
            provision.provision(ctx)
            return ctx

    def test_writes_none_auth_and_empty_env(self):
        with tempfile.TemporaryDirectory() as home:
            ctx = self._run(home)
            auth_path, env_path = ctx.output_paths()
            with open(auth_path, encoding="utf-8") as f:
                auth = json.load(f)
            self.assertEqual(auth["method"], "none")
            self.assertEqual(auth["harness"], "antigravity-ui")
            with open(env_path, encoding="utf-8") as f:
                self.assertEqual(json.load(f), {})
            self.assertTrue(os.path.exists(os.path.join(home, ".config", "Antigravity", "app_storage.json")))

    def test_explicit_none_is_accepted(self):
        with tempfile.TemporaryDirectory() as home:
            self._run(home, "none")

    def test_explicit_credential_type_is_rejected(self):
        for typ in ("api-key", "vertex-ai", "oauth-token"):
            with self.subTest(typ=typ), tempfile.TemporaryDirectory() as home:
                with self.assertRaises(scion_harness.ProvisionError) as cm:
                    self._run(home, typ)
                self.assertIn("sign in inside the Antigravity UI", str(cm.exception))

    def test_declares_no_auth_spec(self):
        specs = [v for v in vars(provision).values() if isinstance(v, scion_harness.AuthSpec)]
        self.assertEqual(specs, [])


class FindCsrfTokenTest(unittest.TestCase):
    def setUp(self):
        self.proc = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.proc)

    def _proc(self, pid: int, *argv: str):
        d = os.path.join(self.proc, str(pid))
        os.makedirs(d)
        with open(os.path.join(d, "cmdline"), "wb") as f:
            f.write(b"\0".join(a.encode() for a in argv) + b"\0")

    def test_space_separated_flag(self):
        self._proc(10, "/opt/Antigravity/resources/bin/language_server", "--port", "8443", "--csrf_token", TOKEN)
        self.assertEqual(helper.find_csrf_token(self.proc), TOKEN)

    def test_equals_flag(self):
        self._proc(11, "/x/language_server_linux_x64", "--csrf_token=" + TOKEN)
        self.assertEqual(helper.find_csrf_token(self.proc), TOKEN)

    def test_ignores_other_processes(self):
        self._proc(12, "/usr/bin/bash", "--csrf_token", "not-this-one-123")
        self._proc(13, "/opt/Antigravity/antigravity", "--csrf_token=" + "not-this-one-456")
        self._proc(14, "/opt/Antigravity/resources/bin/language_server", "--csrf_token", TOKEN)
        with open(os.path.join(self.proc, "self"), "w"):
            pass
        self.assertEqual(helper.find_csrf_token(self.proc), TOKEN)

    def test_ignores_processes_of_other_users(self):
        self._proc(15, "/bin/language_server", "--csrf_token", TOKEN)
        with self.assertRaises(helper.HelperError):
            helper.find_csrf_token(self.proc, uid=os.getuid() + 1)

    def test_none_found(self):
        self._proc(16, "/bin/language_server", "--port", "1")
        with self.assertRaisesRegex(helper.HelperError, "no language server"):
            helper.find_csrf_token(self.proc)

    def test_ambiguous(self):
        self._proc(17, "/bin/language_server", "--csrf_token", TOKEN)
        self._proc(18, "/bin/language_server", "--csrf_token", TOKEN + "x")
        with self.assertRaisesRegex(helper.HelperError, "more than one"):
            helper.find_csrf_token(self.proc)

    def test_rejects_unsafe_characters(self):
        for bad in ('abc"def1234', "abc;def1234", "abc$def1234", "abc def1234", "short"):
            with self.subTest(bad=bad):
                p = tempfile.mkdtemp()
                self.addCleanup(shutil.rmtree, p)
                d = os.path.join(p, "20")
                os.makedirs(d)
                with open(os.path.join(d, "cmdline"), "wb") as f:
                    f.write(b"/bin/language_server\0--csrf_token\0" + bad.encode() + b"\0")
                with self.assertRaisesRegex(helper.HelperError, "unexpected characters"):
                    helper.find_csrf_token(p)


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
        self.assertIn("proxy_set_header Origin https://127.0.0.1;", conf)
        self.assertIn("pid %s/nginx.pid;" % self.state, conf)
        self.assertNotIn("x-codeium-csrf-token", conf)
        self.assertNotRegex(conf, r"(?m)^\s*sub_filter")
        # nginx's own variables survive rendering.
        self.assertIn("$http_upgrade", conf)
        self.assertIn("$connection_upgrade", conf)
        self.assertEqual(stat.S_IMODE(os.stat(self.out).st_mode), 0o600)
        for sub in ("nx-body", "nx-proxy", "nx-fastcgi", "nx-uwsgi", "nx-scgi"):
            self.assertTrue(os.path.isdir(os.path.join(self.state, sub)), sub)

    def test_origin_off(self):
        rc, _, err = self._render("--origin", "off")
        self.assertEqual(rc, 0, err)
        self.assertNotIn("Origin", self._conf())

    def test_html_path_rewrite(self):
        rc, _, err = self._render("--path-rewrite", "html")
        self.assertEqual(rc, 0, err)
        conf = self._conf()
        self.assertIn("sub_filter '=\"/' '=\"./';", conf)
        self.assertIn("sub_filter_once off;", conf)

    def test_csrf_inject_writes_token_only_to_the_config(self):
        proc = os.path.join(self.tmp, "proc")
        os.makedirs(os.path.join(proc, "42"))
        with open(os.path.join(proc, "42", "cmdline"), "wb") as f:
            f.write(b"/opt/Antigravity/resources/bin/language_server\0--csrf_token\0" + TOKEN.encode() + b"\0")
        rc, out, err = self._render("--csrf", "inject", "--proc-root", proc)
        self.assertEqual(rc, 0, err)
        self.assertIn('proxy_set_header x-codeium-csrf-token "%s";' % TOKEN, self._conf())
        self.assertNotIn(TOKEN, out)
        self.assertNotIn(TOKEN, err)
        self.assertIn("csrf=injected", out)
        self.assertEqual(stat.S_IMODE(os.stat(self.out).st_mode), 0o600)

    def test_csrf_inject_without_language_server_fails(self):
        proc = os.path.join(self.tmp, "emptyproc")
        os.makedirs(proc)
        rc, _, err = self._render("--csrf", "inject", "--proc-root", proc)
        self.assertEqual(rc, 1)
        self.assertIn("no language server", err)
        self.assertFalse(os.path.exists(self.out))

    def test_bad_inputs(self):
        cases = [
            dict(state="relative/path"),
            dict(state="/tmp/has space"),
            dict(state="/tmp/semi;colon"),
            dict(proxy_port="80a"),
            dict(ls_port="70000"),
            dict(path_rewrite="regex"),
            dict(csrf_token="bad token value"),
        ]
        base = dict(state="/tmp/ok", proxy_port="8090", ls_port="8443", origin=True,
                    csrf_token=None, path_rewrite="none")
        for override in cases:
            with self.subTest(**override):
                with self.assertRaises(helper.HelperError):
                    helper.build_values(**{**base, **override})

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
        self.assertRegex(wrapper, r"setsid \"\$AGUI_APP_BIN\"[^\n]*\\\n\s*</dev/null")
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

    def _run(self, app: str, timeout: str) -> subprocess.CompletedProcess:
        env = {
            "HOME": os.path.join(self.tmp, "home"),
            "PATH": self.bin + os.pathsep + "/usr/local/bin:/usr/bin:/bin",
            "AGUI_APP_BIN": app,
            "AGUI_SHARE_DIR": HERE,
            "AGUI_LS_TIMEOUT": timeout,
            # A port nothing listens on: the fake app never serves.
            "JETSKI_LS_PORT": "1",
            "PYTHONDONTWRITEBYTECODE": "1",
        }
        return subprocess.run(
            ["bash", os.path.join(HERE, "antigravity-ui-wrapper.sh")],
            env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60,
        )

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

    def test_wrapper_shell_syntax(self):
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("bash not found")
        proc = subprocess.run([bash, "-n", os.path.join(HERE, "antigravity-ui-wrapper.sh")],
                              capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stderr)


if __name__ == "__main__":
    unittest.main()

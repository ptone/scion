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


def _make_ctx(home: str, explicit_type: str = "", adc_file: bool = False) -> scion_harness.ProvisionContext:
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
        with temporary_home(home), temporary_env(**NO_IDENTITY), contextlib.redirect_stderr(io.StringIO()):
            return provision.seed_update_off(_make_ctx(home))


class HelperSeedTest(_SeedCases, unittest.TestCase):
    def seed(self, home: str) -> str:
        return helper.seed_update_off(home)


class ProvisionTest(unittest.TestCase):
    def _run(self, home: str, explicit_type: str = "", adc_file: bool = False, **env: str | None):
        with temporary_home(home), temporary_env(**{**NO_IDENTITY, **env}), \
                contextlib.redirect_stderr(io.StringIO()):
            ctx = _make_ctx(home, explicit_type, adc_file)
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

    def test_staged_file_wins_over_env_and_assignment(self):
        with tempfile.TemporaryDirectory() as home:
            _, overlay = self._run(home, adc_file=True, GOOGLE_APPLICATION_CREDENTIALS="/env/adc.json",
                                   SCION_METADATA_MODE="assign", SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com")
            self.assertTrue(overlay["GOOGLE_APPLICATION_CREDENTIALS"].endswith("/secrets/gcloud-adc"))

    def test_env_credentials_file(self):
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home, GOOGLE_APPLICATION_CREDENTIALS="/env/adc.json")
            self.assertEqual(auth["method"], "vertex-ai")
            self.assertEqual(overlay, {"AGY_ADC_AUTH": "true", "GOOGLE_APPLICATION_CREDENTIALS": "/env/adc.json"})

    def test_assigned_service_account(self):
        with tempfile.TemporaryDirectory() as home:
            auth, overlay = self._run(home, SCION_METADATA_MODE="assign",
                                      SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com")
            self.assertEqual(auth["method"], "vertex-ai")
            self.assertEqual(overlay, {"AGY_ADC_AUTH": "true"})

    def test_other_identity_modes_do_not_count(self):
        cases = [
            dict(SCION_METADATA_MODE="block", SCION_METADATA_SA_EMAIL=None),
            dict(SCION_METADATA_MODE="passthrough", SCION_METADATA_SA_EMAIL="sa@p.iam.gserviceaccount.com"),
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

    def test_html_path_rewrite(self):
        rc, _, err = self._render("--path-rewrite", "html")
        self.assertEqual(rc, 0, err)
        conf = self._conf()
        self.assertIn("sub_filter '=\"/' '=\"./';", conf)
        self.assertIn("sub_filter_once off;", conf)

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

    def test_sign_in_prompt_fails_by_default_without_the_url(self):
        app = self._script("fakeapp", self.SIGNIN_APP)
        proc = self._run(app, "40")
        out = proc.stdout + proc.stderr
        self.assertEqual(proc.returncode, 1, out)
        self.assertIn("not signed in", out)
        self.assertIn("assign a dedicated GCP service account".lower(), out.lower())
        self.assertNotIn("accounts.google.com", out)
        self.assertNotIn("did not answer", out)

    def test_sign_in_relay_forwards_one_code(self):
        app = self._script("fakeapp", self.SIGNIN_APP)
        out_path = os.path.join(self.tmp, "out.txt")
        with open(out_path, "w") as out_f:
            proc = subprocess.Popen(
                ["bash", os.path.join(HERE, "antigravity-ui-wrapper.sh")],
                env=self._env(app, "40", AGUI_SIGNIN_RELAY="1"),
                stdin=subprocess.PIPE, stdout=out_f, stderr=subprocess.STDOUT, text=True,
            )
            try:
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    with open(out_path) as f:
                        if "paste the authorization code" in f.read():
                            break
                    time.sleep(0.2)
                proc.stdin.write("CODE-1234\n")
                proc.stdin.flush()
                rc = proc.wait(timeout=30)
            finally:
                if proc.poll() is None:
                    proc.kill()
        with open(out_path) as f:
            out = f.read()
        with open(os.path.join(self.tmp, "home", "received-code")) as f:
            self.assertEqual(f.read(), "CODE-1234")
        self.assertIn("https://accounts.google.com/o/oauth2/auth?client_id=x&state=y", out)
        self.assertIn("sign-in code forwarded", out)
        # The fake app exits after reading the code, before serving.
        self.assertEqual(rc, 1, out)
        self.assertIn("the app exited during start-up", out)

    def test_taken_port_falls_back_to_a_free_one(self):
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            s.listen(1)
            taken = str(s.getsockname()[1])
            proc = self._run("/bin/false", "5", JETSKI_LS_PORT=taken)
        out = proc.stdout + proc.stderr
        self.assertIn(f"port {taken} is in use", out)
        self.assertIn("the app exited during start-up", out)

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


if __name__ == "__main__":
    unittest.main()

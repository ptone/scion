#!/usr/bin/env python3
"""Unit tests for the antigravity harness provisioner.

Run with:  python3 -m unittest provision_test -v
"""

from __future__ import annotations

import importlib.util
import json
import os
import re
import tempfile
import unittest

PROVISION_PATH = os.path.join(os.path.dirname(__file__), "provision.py")
SPEC = importlib.util.spec_from_file_location("antigravity_provision", PROVISION_PATH)
assert SPEC is not None
provision = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(provision)

scion_harness = provision.scion_harness


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

KEYRING_BINARIES = ("dbus-launch", "gnome-keyring-daemon", "secret-tool")

# Patterns that indicate keyring initialization in the wrapper script.
KEYRING_PATTERNS = [
    re.compile(r"\bdbus-launch\b"),
    re.compile(r"\bgnome-keyring-daemon\b"),
    re.compile(r"\bsecret-tool\s+store\b"),
]


def _generate_wrapper(
    auth_method: str,
    is_enterprise: bool = False,
    thinking_tier: str | None = None,
) -> str:
    """Generate a wrapper script in a temp dir and return its content."""
    with tempfile.TemporaryDirectory() as home:
        # Create dirs the wrapper generator expects.
        os.makedirs(os.path.join(home, ".scion", "harness"), exist_ok=True)
        os.makedirs(
            os.path.join(home, ".gemini", "antigravity-cli", "cache"),
            exist_ok=True,
        )
        provision._generate_wrapper_script(
            home,
            is_enterprise=is_enterprise,
            auth_method=auth_method,
            thinking_tier=thinking_tier,
        )
        wrapper_path = os.path.join(home, ".scion", "harness", "agy-wrapper.sh")
        with open(wrapper_path, "r") as f:
            return f.read()


def _has_keyring_calls(script: str) -> list[str]:
    """Return list of keyring-related commands found in the wrapper script.

    Only matches actual command invocations, not comments. Lines starting
    with '#' (after optional whitespace) are excluded.
    """
    found = []
    for line in script.splitlines():
        stripped = line.lstrip()
        if stripped.startswith("#"):
            continue
        for pat in KEYRING_PATTERNS:
            if pat.search(line):
                found.append(pat.pattern)
                break
    return found


# ---------------------------------------------------------------------------
# Tests — Decision 1: keyring block gated on auth method
# ---------------------------------------------------------------------------


class WrapperKeyringGateTest(unittest.TestCase):
    """The keyring block must appear ONLY for oauth-token auth.

    The mutation that must make these tests fail is reverting the auth-method
    gate to the old is_adc gate (which ran the keyring block for api-key,
    oauth-token, AND none).
    """

    def test_api_key_wrapper_has_no_keyring_calls(self) -> None:
        """api-key auth reads GEMINI_API_KEY from env. No keyring needed."""
        script = _generate_wrapper("api-key")
        hits = _has_keyring_calls(script)
        self.assertEqual(
            hits, [],
            f"api-key wrapper must not contain keyring commands, found: {hits}",
        )

    def test_none_wrapper_has_no_keyring_calls(self) -> None:
        """method=none has no credentials to store. No keyring needed."""
        script = _generate_wrapper("none")
        hits = _has_keyring_calls(script)
        self.assertEqual(
            hits, [],
            f"none wrapper must not contain keyring commands, found: {hits}",
        )

    def test_vertex_ai_wrapper_has_no_keyring_calls(self) -> None:
        """vertex-ai uses ADC (GOOGLE_APPLICATION_CREDENTIALS). No keyring."""
        script = _generate_wrapper("vertex-ai", is_enterprise=True)
        hits = _has_keyring_calls(script)
        self.assertEqual(
            hits, [],
            f"vertex-ai wrapper must not contain keyring commands, found: {hits}",
        )

    def test_vertex_ai_wrapper_exports_adc_auth(self) -> None:
        """vertex-ai wrapper must export AGY_ADC_AUTH=true."""
        script = _generate_wrapper("vertex-ai", is_enterprise=True)
        self.assertIn("export AGY_ADC_AUTH=true", script)

    def test_oauth_token_wrapper_has_keyring_calls(self) -> None:
        """oauth-token is the ONLY method that uses the keyring."""
        script = _generate_wrapper("oauth-token")
        for pat in KEYRING_PATTERNS:
            # Check non-comment lines
            found = False
            for line in script.splitlines():
                if not line.lstrip().startswith("#") and pat.search(line):
                    found = True
                    break
            self.assertTrue(
                found,
                f"oauth-token wrapper must contain keyring command: {pat.pattern}",
            )


# ---------------------------------------------------------------------------
# Tests — Decision 2: oauth-token fails loudly if binaries missing
# ---------------------------------------------------------------------------


class WrapperOAuthBinaryCheckTest(unittest.TestCase):
    """oauth-token wrapper must check for required binaries up front."""

    def test_oauth_token_checks_all_keyring_binaries(self) -> None:
        """The wrapper must check for each required binary by name."""
        script = _generate_wrapper("oauth-token")
        for binary in KEYRING_BINARIES:
            self.assertIn(
                binary,
                script,
                f"oauth-token wrapper must reference '{binary}'",
            )

    def test_oauth_token_has_command_v_check(self) -> None:
        """The wrapper must use 'command -v' to check binary presence."""
        script = _generate_wrapper("oauth-token")
        self.assertIn("command -v", script)

    def test_oauth_token_exits_on_missing_binary(self) -> None:
        """Missing binary must cause exit, not silent continuation."""
        script = _generate_wrapper("oauth-token")
        # The binary check loop must exit 1 when a binary is missing.
        self.assertIn("exit 1", script)

    def test_oauth_token_names_missing_binary_in_error(self) -> None:
        """Error message must identify the specific binary that's missing."""
        script = _generate_wrapper("oauth-token")
        # The FATAL message must include the binary name variable.
        self.assertIn("FATAL", script)
        self.assertIn("not installed", script)


# ---------------------------------------------------------------------------
# Tests — Decision 3: #124 — no eval-masking, no silent stream discard
# ---------------------------------------------------------------------------


class WrapperEvalMaskingTest(unittest.TestCase):
    """The wrapper must not mask failures via eval or stream discard (#124).

    The old pattern:
      eval $(dbus-launch --sh-syntax)          # returns 0 if command missing
      eval $(echo "test" | cmd --unlock 2>/dev/null)  # same
      cmd --start > /dev/null 2>&1             # fatal but silent

    Each of these hid a missing-binary exit 127. The new pattern must capture
    output, check the exit code, and report failures.
    """

    def test_oauth_token_no_bare_eval_of_command(self) -> None:
        """No bare 'eval $(command ...)' — output must be captured first."""
        script = _generate_wrapper("oauth-token")
        # Find non-comment eval lines
        for line in script.splitlines():
            stripped = line.lstrip()
            if stripped.startswith("#"):
                continue
            if "eval $(" in stripped:
                self.fail(
                    f"Bare 'eval $(...)' found — output must be captured "
                    f"and checked before eval. Line: {stripped!r}"
                )

    def test_oauth_token_no_dev_null_on_fatal_commands(self) -> None:
        """Fatal keyring commands must not discard both streams to /dev/null."""
        script = _generate_wrapper("oauth-token")
        # gnome-keyring-daemon --start must not have both streams nulled.
        for line in script.splitlines():
            stripped = line.lstrip()
            if stripped.startswith("#"):
                continue
            if "gnome-keyring-daemon --start" in stripped:
                self.assertNotIn(
                    "> /dev/null 2>&1", stripped,
                    "gnome-keyring-daemon --start must not discard output",
                )
                self.assertNotIn(
                    ">/dev/null 2>&1", stripped,
                    "gnome-keyring-daemon --start must not discard output",
                )


# ---------------------------------------------------------------------------
# Tests — Decision 4: dead has_token parameter removed
# ---------------------------------------------------------------------------


class DeadParameterTest(unittest.TestCase):
    """has_token must not be a parameter of _generate_wrapper_script."""

    def test_has_token_not_in_signature(self) -> None:
        """The dead has_token parameter must be removed from the signature."""
        import inspect
        sig = inspect.signature(provision._generate_wrapper_script)
        self.assertNotIn(
            "has_token", sig.parameters,
            "_generate_wrapper_script still accepts has_token — remove it",
        )

    def test_is_adc_not_in_signature(self) -> None:
        """The is_adc parameter is replaced by auth_method."""
        import inspect
        sig = inspect.signature(provision._generate_wrapper_script)
        self.assertNotIn(
            "is_adc", sig.parameters,
            "_generate_wrapper_script still accepts is_adc — use auth_method",
        )

    def test_auth_method_in_signature(self) -> None:
        """auth_method must be a parameter of _generate_wrapper_script."""
        import inspect
        sig = inspect.signature(provision._generate_wrapper_script)
        self.assertIn(
            "auth_method", sig.parameters,
            "_generate_wrapper_script must accept auth_method",
        )


# ---------------------------------------------------------------------------
# Tests — wrapper script structure
# ---------------------------------------------------------------------------


class WrapperStructureTest(unittest.TestCase):
    """Basic structural checks for the generated wrapper script."""

    def test_wrapper_starts_with_shebang(self) -> None:
        for method in ("api-key", "oauth-token", "vertex-ai", "none"):
            with self.subTest(method=method):
                is_enterprise = method == "vertex-ai"
                script = _generate_wrapper(method, is_enterprise=is_enterprise)
                self.assertTrue(
                    script.startswith("#!/bin/bash"),
                    f"Wrapper for {method} must start with #!/bin/bash",
                )

    def test_wrapper_has_set_e(self) -> None:
        for method in ("api-key", "oauth-token", "vertex-ai", "none"):
            with self.subTest(method=method):
                is_enterprise = method == "vertex-ai"
                script = _generate_wrapper(method, is_enterprise=is_enterprise)
                self.assertIn("set -e", script)

    def test_wrapper_execs_agy(self) -> None:
        for method in ("api-key", "oauth-token", "vertex-ai", "none"):
            with self.subTest(method=method):
                is_enterprise = method == "vertex-ai"
                script = _generate_wrapper(method, is_enterprise=is_enterprise)
                self.assertIn("exec agy", script)

    def test_wrapper_is_executable(self) -> None:
        """Generated wrapper file must have executable permission."""
        with tempfile.TemporaryDirectory() as home:
            os.makedirs(os.path.join(home, ".scion", "harness"), exist_ok=True)
            os.makedirs(
                os.path.join(home, ".gemini", "antigravity-cli", "cache"),
                exist_ok=True,
            )
            provision._generate_wrapper_script(
                home, is_enterprise=False, auth_method="api-key",
            )
            wrapper_path = os.path.join(
                home, ".scion", "harness", "agy-wrapper.sh"
            )
            mode = os.stat(wrapper_path).st_mode
            self.assertTrue(
                mode & 0o111,
                "Wrapper must be executable",
            )

    def test_thinking_tier_passed_to_agy(self) -> None:
        script = _generate_wrapper("api-key", thinking_tier="High")
        self.assertIn("--thinking-level High", script)

    def test_no_thinking_tier_omits_flag(self) -> None:
        script = _generate_wrapper("api-key", thinking_tier=None)
        self.assertNotIn("--thinking-level", script)


if __name__ == "__main__":
    unittest.main()

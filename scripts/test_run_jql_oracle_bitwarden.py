"""Hermetic security and behavior tests for the Bitwarden oracle wrapper."""

import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stderr
from unittest import mock


SCRIPT = Path(__file__).with_name("run-jql-oracle-bitwarden.py")
SPEC = importlib.util.spec_from_file_location("run_jql_oracle_bitwarden", SCRIPT)
wrapper = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(wrapper)

MACHINE_SECRET = "machine-sentinel-NEVER-LEAK"
JIRA_SECRET = "jira-sentinel-NEVER-LEAK"
TOKEN_ID = "12345678-1234-1234-1234-123456789abc"


class Result:
    def __init__(self, stdout="", returncode=0, stderr=""):
        self.stdout = stdout
        self.stderr = stderr
        self.returncode = returncode


class BitwardenOracleWrapperTests(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        self.cwd = Path.cwd()
        self.addCleanup(os.chdir, self.cwd)

    def config_file(self, contents):
        path = Path(self.tempdir.name, "oracle.env")
        path.write_text(contents, encoding="utf-8")
        return path

    def test_dotenv_allowlist_quotes_comments_export_and_environment_precedence(self):
        path = self.config_file(
            'export JIRA_SERVER="https://quoted.example.test" # inline\n'
            "JIRA_EMAIL='person@example.test'\n"
            "JIRA_PROJECT=PROJ # comment\n"
            "JIRA_ORACLE_RELEASE='Release 1'\n"
            "JIRA_API_TOKEN=from-file\n"
            "BWS_ACCESS_TOKEN=from-file\n"
            "UNKNOWN_SETTING=ignored\n"
            "PATH=/attacker/path\n"
        )
        env = {
            "JIRA_SERVER": "https://environment.example.test",
            "JIRA_API_TOKEN": "ambient-jira",
            "BWS_ACCESS_TOKEN": "ambient-machine",
            "PATH": "/safe/path",
            "USER": "test-user",
        }
        result = wrapper.load_config(path, env)
        self.assertEqual(result["JIRA_SERVER"], "https://environment.example.test")
        self.assertEqual(result["JIRA_EMAIL"], "person@example.test")
        self.assertEqual(result["JIRA_PROJECT"], "PROJ")
        self.assertEqual(result["JIRA_ORACLE_RELEASE"], "Release 1")
        self.assertNotIn("JIRA_API_TOKEN", result)
        self.assertNotIn("BWS_ACCESS_TOKEN", result)
        self.assertEqual(result["PATH"], "/safe/path")
        self.assertNotIn("UNKNOWN_SETTING", result)

    def test_dotenv_shell_interpolation_is_literal_and_never_executed(self):
        marker = Path(self.tempdir.name, "shell-was-executed")
        literal = f"$(touch {marker}) ${{HOME}} `touch {marker}`"
        path = self.config_file("JIRA_SERVER='" + literal + "'\n")
        with mock.patch.object(wrapper.subprocess, "run") as run:
            result = wrapper.load_config(path, {})
        self.assertEqual(result["JIRA_SERVER"], literal)
        self.assertFalse(marker.exists())
        run.assert_not_called()

    def test_spaces_require_quotes_and_duplicate_or_malformed_allowed_values_fail_safely(self):
        for contents, expected in (
            ("JIRA_SERVER=one\nJIRA_SERVER=two\n", "duplicate JIRA_SERVER"),
            ("JIRA_SERVER='unterminated\n", "invalid quoting for JIRA_SERVER"),
            ("JIRA_EMAIL=two words\n", "quote values containing spaces for JIRA_EMAIL"),
        ):
            with self.subTest(contents=contents):
                with self.assertRaises(wrapper.ConfigurationError) as caught:
                    wrapper.load_config(self.config_file(contents), {})
                self.assertIn(expected, str(caught.exception))
                self.assertNotIn(MACHINE_SECRET, str(caught.exception))
                self.assertNotIn(JIRA_SECRET, str(caught.exception))

    def test_secret_lookup_isolated_from_argv_and_oracle_environment(self):
        config = {
            "BWS_JIRA_API_TOKEN_ID": TOKEN_ID,
            "USER": "test-user",
            "JIRA_SERVER": "https://jira.example.test",
            "JIRA_API_TOKEN": "ambient-jira",
            "BWS_ACCESS_TOKEN": "ambient-machine",
        }
        calls = []

        def run(command, *, env, **kwargs):
            # Keep the environment mapping itself: the wrapper scrubs its
            # transient Bitwarden token from this mapping after capture returns.
            calls.append((list(command), env, dict(env), kwargs))
            if command[0] == "security":
                return Result(MACHINE_SECRET + "\n")
            if command[0] == "bws":
                return Result(json.dumps({"key": "JIRA_API_TOKEN", "value": JIRA_SECRET}))
            self.fail("unexpected subprocess")

        with mock.patch.object(wrapper.subprocess, "run", side_effect=run):
            token = wrapper.jira_token(config)
        self.assertEqual(token, JIRA_SECRET)
        self.assertEqual(calls[0][0], ["security", "find-generic-password", "-s", "jira-bws-access-token", "-a", "test-user", "-w"])
        self.assertEqual(calls[1][0], ["bws", "secret", "get", TOKEN_ID])
        for command, _, environment, _ in calls:
            self.assertNotIn(MACHINE_SECRET, command)
            self.assertNotIn(JIRA_SECRET, command)
        self.assertNotIn("JIRA_API_TOKEN", calls[0][2])
        self.assertNotIn("BWS_ACCESS_TOKEN", calls[0][2])
        self.assertEqual(calls[1][2]["BWS_ACCESS_TOKEN"], MACHINE_SECRET)
        self.assertNotIn("JIRA_API_TOKEN", calls[1][2])
        self.assertNotIn("BWS_ACCESS_TOKEN", calls[1][1])  # wrapper removes it after capture

    def test_keychain_service_and_account_overrides_are_used(self):
        config = {
            "BWS_JIRA_API_TOKEN_ID": TOKEN_ID,
            "USER": "ambient-user",
            "BWS_ACCESS_TOKEN_KEYCHAIN_SERVICE": "custom-service",
            "BWS_ACCESS_TOKEN_KEYCHAIN_ACCOUNT": "custom-account",
        }
        calls = []

        def run(command, *, env, **kwargs):
            calls.append(list(command))
            if command[0] == "security":
                return Result(MACHINE_SECRET)
            return Result(json.dumps({"key": "JIRA_API_TOKEN", "value": JIRA_SECRET}))

        with mock.patch.object(wrapper.subprocess, "run", side_effect=run):
            wrapper.jira_token(config)
        self.assertEqual(calls[0], ["security", "find-generic-password", "-s", "custom-service", "-a", "custom-account", "-w"])

    def test_lookup_failures_are_closed_and_redact_provider_stderr(self):
        cases = [
            ("security", Result("", returncode=44, stderr=MACHINE_SECRET), "iCloud/Local Items"),
            ("security", Result("", returncode=1, stderr=MACHINE_SECRET), "could not retrieve the Bitwarden machine token from macOS Keychain"),
            ("security", Result("   \n"), "Keychain returned an empty or invalid machine token"),
            ("bws", Result("", returncode=1, stderr=MACHINE_SECRET + JIRA_SECRET), "Bitwarden secret lookup failed"),
            ("bws", Result("not-json " + JIRA_SECRET), "Bitwarden returned invalid secret JSON"),
            ("bws", Result(json.dumps([{"key": "JIRA_API_TOKEN", "value": JIRA_SECRET}])), "Bitwarden secret key must be JIRA_API_TOKEN"),
            ("bws", Result(json.dumps({"key": "OTHER", "value": JIRA_SECRET})), "Bitwarden secret key must be JIRA_API_TOKEN"),
            ("bws", Result(json.dumps({"key": "JIRA_API_TOKEN", "value": ""})), "Bitwarden returned an empty or invalid Jira token"),
            ("bws", Result(json.dumps({"key": "JIRA_API_TOKEN", "value": 12345})), "Bitwarden returned an empty or invalid Jira token"),
            ("bws", Result(json.dumps({"key": "JIRA_API_TOKEN", "value": "bad token " + JIRA_SECRET})), "Bitwarden returned an empty or invalid Jira token"),
        ]
        for failing_command, result, expected in cases:
            with self.subTest(failing_command=failing_command, expected=expected):
                def run(command, **kwargs):
                    if command[0] == failing_command:
                        return result
                    if command[0] == "security":
                        return Result(MACHINE_SECRET)
                    return Result(json.dumps({"key": "JIRA_API_TOKEN", "value": JIRA_SECRET}))

                with mock.patch.object(wrapper.subprocess, "run", side_effect=run):
                    with self.assertRaises(wrapper.ConfigurationError) as caught:
                        wrapper.jira_token({"BWS_JIRA_API_TOKEN_ID": TOKEN_ID, "USER": "test-user"})
                self.assertIn(expected, str(caught.exception))
                self.assertNotIn(MACHINE_SECRET, str(caught.exception))
                self.assertNotIn(JIRA_SECRET, str(caught.exception))

    def test_missing_security_or_bws_executable_fails_safely(self):
        for failing_command, expected in (
            ("security", "could not retrieve the Bitwarden machine token from macOS Keychain"),
            ("bws", "Bitwarden secret lookup failed"),
        ):
            with self.subTest(command=failing_command):
                def run(command, **kwargs):
                    if command[0] == failing_command:
                        raise FileNotFoundError(MACHINE_SECRET + JIRA_SECRET)
                    if command[0] == "security":
                        return Result(MACHINE_SECRET)
                    return Result(json.dumps({"key": "JIRA_API_TOKEN", "value": JIRA_SECRET}))

                with mock.patch.object(wrapper.subprocess, "run", side_effect=run):
                    with self.assertRaises(wrapper.ConfigurationError) as caught:
                        wrapper.jira_token({"BWS_JIRA_API_TOKEN_ID": TOKEN_ID, "USER": "test-user"})
                self.assertIn(expected, str(caught.exception))
                self.assertNotIn(MACHINE_SECRET, str(caught.exception))
                self.assertNotIn(JIRA_SECRET, str(caught.exception))

    def test_invalid_token_id_and_missing_account_fail_before_keychain(self):
        with mock.patch.object(wrapper.subprocess, "run") as run:
            with self.assertRaises(wrapper.ConfigurationError):
                wrapper.jira_token({"BWS_JIRA_API_TOKEN_ID": "not-a-uuid", "USER": "test"})
            with self.assertRaises(wrapper.ConfigurationError):
                wrapper.jira_token({"BWS_JIRA_API_TOKEN_ID": TOKEN_ID})
        run.assert_not_called()

    def test_main_smoke_passes_only_resolved_token_to_runner_and_propagates_exit(self):
        fake_runner = mock.Mock(REQUIRED_ENV=("JIRA_SERVER", "JIRA_EMAIL", "JIRA_API_TOKEN", "JIRA_PROJECT"))
        fake_runner.run.return_value = 7
        calls = []

        def run(command, *, env, **kwargs):
            calls.append((list(command), dict(env)))
            if command[0] == "security":
                return Result(MACHINE_SECRET)
            return Result(json.dumps({"key": "JIRA_API_TOKEN", "value": JIRA_SECRET}))

        env = {
            "JIRA_SERVER": "https://jira.example.test", "JIRA_EMAIL": "user@example.test",
            "JIRA_PROJECT": "PROJ", "USER": "test-user", "JIRA_API_TOKEN": "ambient-jira",
            "BWS_ACCESS_TOKEN": "ambient-machine", "BWS_JIRA_API_TOKEN_ID": TOKEN_ID,
        }
        with mock.patch.dict(os.environ, env, clear=True), \
                mock.patch.object(wrapper, "load_runner", return_value=fake_runner), \
                mock.patch.object(wrapper.subprocess, "run", side_effect=run):
            result = wrapper.main(["--smoke", "--env-file", str(self.config_file(""))])
        self.assertEqual(result, 7)
        oracle_env = fake_runner.run.call_args.args[0]
        self.assertEqual(oracle_env["JIRA_API_TOKEN"], JIRA_SECRET)
        self.assertNotIn("BWS_ACCESS_TOKEN", oracle_env)
        self.assertNotIn(MACHINE_SECRET, oracle_env.values())
        self.assertNotIn("ambient-jira", oracle_env.values())
        self.assertEqual(fake_runner.run.call_args.kwargs, {"smoke": True})
        self.assertNotIn(MACHINE_SECRET, " ".join(calls[0][0] + calls[1][0]))
        self.assertNotIn(JIRA_SECRET, " ".join(calls[0][0] + calls[1][0]))

    def test_full_preflight_rejects_missing_fixtures_before_keychain(self):
        fake_runner = mock.Mock(REQUIRED_ENV=("JIRA_SERVER", "JIRA_EMAIL", "JIRA_API_TOKEN", "JIRA_PROJECT", "JIRA_ORACLE_ISSUE_KEY", "JIRA_ORACLE_SPRINT_ID", "JIRA_ORACLE_RELEASE"))
        output = io.StringIO()
        with mock.patch.dict(os.environ, {
            "JIRA_SERVER": "https://jira.example.test", "JIRA_EMAIL": "user@example.test",
            "JIRA_PROJECT": "PROJ", "BWS_JIRA_API_TOKEN_ID": TOKEN_ID, "USER": "test-user",
        }, clear=True), mock.patch.object(wrapper, "load_runner", return_value=fake_runner), \
                mock.patch.object(wrapper.subprocess, "run") as subprocess_run, redirect_stderr(output):
            result = wrapper.main(["--env-file", str(self.config_file(""))])
        self.assertEqual(result, 2)
        self.assertIn("JIRA_ORACLE_ISSUE_KEY", output.getvalue())
        subprocess_run.assert_not_called()
        fake_runner.run.assert_not_called()

    def test_main_full_run_passes_resolved_token_and_all_fixtures(self):
        required = (
            "JIRA_SERVER", "JIRA_EMAIL", "JIRA_API_TOKEN", "JIRA_PROJECT",
            "JIRA_ORACLE_ISSUE_KEY", "JIRA_ORACLE_SPRINT_ID", "JIRA_ORACLE_RELEASE",
        )
        fake_runner = mock.Mock(REQUIRED_ENV=required)
        fake_runner.run.return_value = 0

        def run(command, *, env, **kwargs):
            if command[0] == "security":
                return Result(MACHINE_SECRET)
            return Result(json.dumps({"key": "JIRA_API_TOKEN", "value": JIRA_SECRET}))

        environment = {
            "JIRA_SERVER": "https://jira.example.test", "JIRA_EMAIL": "user@example.test",
            "JIRA_PROJECT": "PROJ", "JIRA_ORACLE_ISSUE_KEY": "PROJ-123",
            "JIRA_ORACLE_SPRINT_ID": "42", "JIRA_ORACLE_RELEASE": "Release 1",
            "BWS_JIRA_API_TOKEN_ID": TOKEN_ID, "USER": "test-user",
        }
        with mock.patch.dict(os.environ, environment, clear=True), \
                mock.patch.object(wrapper, "load_runner", return_value=fake_runner), \
                mock.patch.object(wrapper.subprocess, "run", side_effect=run):
            result = wrapper.main(["--env-file", str(self.config_file(""))])
        self.assertEqual(result, 0)
        oracle_env = fake_runner.run.call_args.args[0]
        for key in required:
            self.assertTrue(oracle_env.get(key), key)
        self.assertEqual(oracle_env["JIRA_API_TOKEN"], JIRA_SECRET)
        self.assertNotIn("BWS_ACCESS_TOKEN", oracle_env)
        self.assertNotIn(MACHINE_SECRET, oracle_env.values())
        self.assertEqual(fake_runner.run.call_args.kwargs, {"smoke": False})

    def test_missing_explicit_file_and_command_injection_arguments_fail(self):
        output = io.StringIO()
        with mock.patch.dict(os.environ, {}, clear=True), mock.patch.object(wrapper, "load_runner") as load_runner, \
                mock.patch.object(wrapper.subprocess, "run") as subprocess_run, redirect_stderr(output):
            result = wrapper.main(["--env-file", str(Path(self.tempdir.name, "missing.env")), "--smoke"])
        self.assertEqual(result, 2)
        self.assertIn("could not read the requested config file", output.getvalue())
        load_runner.assert_not_called()
        subprocess_run.assert_not_called()
        with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            wrapper.main(["--smoke", "--", ";touch /tmp/unwanted"])


if __name__ == "__main__":
    unittest.main()

"""Behavioral tests for the strict oracle test-stream runner."""

import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest import mock


SCRIPT = Path(__file__).with_name("run-jql-oracle.py")
SPEC = importlib.util.spec_from_file_location("run_jql_oracle", SCRIPT)
runner = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(runner)


CONFIG = {key: "configured" for key in runner.REQUIRED_ENV}
SMOKE_CONFIG = {key: "configured" for key in runner.SMOKE_REQUIRED_ENV}


def event(action, package="example.test", test=None, output=None):
    result = {"Action": action, "Package": package}
    if test is not None:
        result["Test"] = test
    if output is not None:
        result["Output"] = output
    return json.dumps(result)


def smoke_events():
    lines = []
    for package, tests in (
        ("example/internal/llm", runner.SMOKE_TESTS[:2]),
        ("example/internal/jira", runner.SMOKE_TESTS[2:]),
    ):
        lines.append(event("start", package=package))
        for test in tests:
            lines.extend([event("run", package=package, test=test), event("pass", package=package, test=test)])
        lines.append(event("pass", package=package))
    return lines


class FakeProcess:
    def __init__(self, lines, code=0):
        self.stdout = io.StringIO("\n".join(lines) + "\n")
        self.code = code

    def wait(self):
        return self.code


class OracleRunnerTests(unittest.TestCase):
    def setUp(self):
        # Simulated oracle results must not pollute CI's real step summary.
        environment = mock.patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": ""})
        environment.start()
        self.addCleanup(environment.stop)
        capture = redirect_stdout(io.StringIO())
        capture.__enter__()
        self.addCleanup(capture.__exit__, None, None, None)

    def invoke(self, lines, code=0, env=None, smoke=False):
        fake = FakeProcess(lines, code)
        with mock.patch.object(runner.subprocess, "Popen", return_value=fake) as popen:
            result = runner.run(CONFIG if env is None else env, smoke=smoke)
        return result, popen

    def test_success_requires_passing_oracle_leaf_and_writes_summary(self):
        lines = [
            event("start"),
            event("run", test="TestOracle_Search"),
            event("pass", test="TestOracle_Search"),
            event("pass"),
        ]
        with tempfile.TemporaryDirectory() as tempdir:
            summary = Path(tempdir, "summary.md")
            with mock.patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": str(summary)}):
                result, popen = self.invoke(lines)
            self.assertEqual(result, 0)
            self.assertIn("JQL oracle: PASS", summary.read_text())
            self.assertEqual(popen.call_args.args[0], runner.COMMAND)

    def test_nonzero_go_exit_fails_even_when_events_show_pass(self):
        result, _ = self.invoke(
            [event("start"), event("run", test="TestOracle_Search"),
             event("pass", test="TestOracle_Search"), event("pass")], code=1
        )
        self.assertNotEqual(result, 0)

    def test_missing_configuration_is_skipped_without_running_go(self):
        with mock.patch.object(runner.subprocess, "Popen") as popen:
            output = io.StringIO()
            with redirect_stdout(output):
                result = runner.run({})
        self.assertEqual(result, 2)
        self.assertIn("JQL oracle: SKIPPED", output.getvalue())
        self.assertNotIn("JQL oracle: PASS", output.getvalue())
        popen.assert_not_called()

    def test_full_run_still_requires_oracle_fixtures(self):
        with mock.patch.object(runner.subprocess, "Popen") as popen:
            output = io.StringIO()
            with redirect_stdout(output):
                result = runner.run(SMOKE_CONFIG)
        self.assertEqual(result, 2)
        self.assertIn("JIRA_ORACLE_ISSUE_KEY", output.getvalue())
        self.assertIn("JIRA_ORACLE_SPRINT_ID", output.getvalue())
        self.assertIn("JIRA_ORACLE_RELEASE", output.getvalue())
        popen.assert_not_called()

    def test_smoke_uses_fixed_command_and_only_smoke_configuration(self):
        lines = smoke_events()
        result, popen = self.invoke(lines, env=SMOKE_CONFIG, smoke=True)
        self.assertEqual(result, 0)
        self.assertEqual(popen.call_args.args[0], runner.SMOKE_COMMAND)
        self.assertEqual(popen.call_args.kwargs["env"], SMOKE_CONFIG)

    def test_smoke_requires_only_connection_and_project_settings(self):
        result, popen = self.invoke(
            smoke_events(),
            env=SMOKE_CONFIG,
            smoke=True,
        )
        self.assertEqual(result, 0)
        popen.assert_called_once()

    def test_smoke_rejects_omitted_required_activity_test(self):
        lines = [line for line in smoke_events() if "TestOracle_WorkActivityReadEndpoints" not in line]
        result, _ = self.invoke(lines, env=SMOKE_CONFIG, smoke=True)
        self.assertNotEqual(result, 0)

    def test_smoke_skipped_oracle_case_fails(self):
        result, _ = self.invoke(
            [event("start"), event("run", test="TestOracle_DateSortWithoutDirection"),
             event("skip", test="TestOracle_DateSortWithoutDirection"), event("pass")],
            env=SMOKE_CONFIG,
            smoke=True,
        )
        self.assertNotEqual(result, 0)

    def test_smoke_failed_oracle_case_fails(self):
        result, _ = self.invoke(
            [event("start"), event("run", test="TestOracle_DateSortWithoutDirection"),
             event("fail", test="TestOracle_DateSortWithoutDirection"), event("fail")],
            env=SMOKE_CONFIG,
            smoke=True,
        )
        self.assertNotEqual(result, 0)

    def test_cli_smoke_flag_selects_smoke_mode(self):
        with mock.patch.object(runner, "run", return_value=0) as run:
            self.assertEqual(runner.main(["--smoke"]), 0)
        run.assert_called_once_with(smoke=True)

    def test_cli_default_selects_full_mode(self):
        with mock.patch.object(runner, "run", return_value=0) as run:
            self.assertEqual(runner.main([]), 0)
        run.assert_called_once_with(smoke=False)

    def test_skipped_oracle_case_fails(self):
        result, _ = self.invoke(
            [event("start"), event("run", test="TestOracle_Search"),
             event("skip", test="TestOracle_Search"), event("pass")]
        )
        self.assertNotEqual(result, 0)

    def test_zero_oracle_cases_fails_on_package_success_alone(self):
        result, _ = self.invoke([event("start"), event("pass")])
        self.assertNotEqual(result, 0)

    def test_incomplete_oracle_fails_even_when_another_oracle_passes(self):
        lines = [
            event("start"),
            event("run", test="TestOracle_Complete"),
            event("pass", test="TestOracle_Complete"),
            event("run", test="TestOracle_Incomplete"),
            event("pass"),
        ]
        result, _ = self.invoke(lines)
        self.assertNotEqual(result, 0)

    def test_missing_package_terminal_fails_with_passing_oracle(self):
        lines = [event("start"), event("run", test="TestOracle_Search"),
                 event("pass", test="TestOracle_Search")]
        result, _ = self.invoke(lines)
        self.assertNotEqual(result, 0)

    def test_failed_oracle_parent_fails_even_when_child_passes(self):
        lines = [
            event("start"),
            event("run", test="TestOracle_Group"),
            event("run", test="TestOracle_Group/Case"),
            event("pass", test="TestOracle_Group/Case"),
            event("fail", test="TestOracle_Group"),
            event("pass"),
        ]
        result, _ = self.invoke(lines)
        self.assertNotEqual(result, 0)

    def test_non_oracle_failure_fails_even_when_exit_code_is_zero(self):
        lines = [event("start"), event("run", test="TestOracle_Search"),
                 event("pass", test="TestOracle_Search"),
                 event("run", test="TestOrdinary"),
                 event("fail", test="TestOrdinary"), event("pass")]
        result, _ = self.invoke(lines)
        self.assertNotEqual(result, 0)

    def test_malformed_event_fails_even_with_passing_oracle(self):
        lines = [event("start"), event("run", test="TestOracle_Search"),
                 event("pass", test="TestOracle_Search"),
                 '{"Action":"pass","Package":42}', event("pass")]
        result, _ = self.invoke(lines)
        self.assertNotEqual(result, 0)

    def test_nested_oracle_subtests_count_only_deepest_leaves(self):
        lines = [
            event("start"),
            event("run", test="TestOracle_Group"),
            event("run", test="TestOracle_Group/Branch"),
            event("run", test="TestOracle_Group/Branch/One"),
            event("pass", test="TestOracle_Group/Branch/One"),
            event("run", test="TestOracle_Group/Branch/Two"),
            event("pass", test="TestOracle_Group/Branch/Two"),
            event("pass", test="TestOracle_Group/Branch"),
            event("pass", test="TestOracle_Group"),
            event("pass"),
        ]
        output = io.StringIO()
        with redirect_stdout(output):
            result, _ = self.invoke(lines)
        self.assertEqual(result, 0)
        self.assertIn("Leaf oracle tests: 2 passed", output.getvalue())

    def test_malformed_line_is_reported_without_crashing(self):
        result, _ = self.invoke(["not-json", event("start"), event("pass")])
        self.assertNotEqual(result, 0)


if __name__ == "__main__":
    unittest.main()

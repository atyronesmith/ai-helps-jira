#!/usr/bin/env python3
"""Run and strictly summarize Jira-backed JQL oracle tests."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from collections import Counter
from typing import TextIO


REQUIRED_ENV = (
    "JIRA_SERVER",
    "JIRA_EMAIL",
    "JIRA_API_TOKEN",
    "JIRA_PROJECT",
    "JIRA_ORACLE_ISSUE_KEY",
    "JIRA_ORACLE_SPRINT_ID",
    "JIRA_ORACLE_RELEASE",
)
SMOKE_REQUIRED_ENV = REQUIRED_ENV[:4]
SMOKE_TESTS = (
    "TestOracle_DateSortWithoutDirection",
    "TestOracle_ExplicitAbsoluteDateRanges",
    "TestOracle_WorkActivityReadEndpoints",
)
COMMAND = ["go", "test", "-json", "-tags=oracle", "-count=1", "./..."]
SMOKE_COMMAND = [
    "go", "test", "-json", "-tags=oracle", "-count=1",
    "-run", "^TestOracle_(DateSortWithoutDirection|ExplicitAbsoluteDateRanges|WorkActivityReadEndpoints)$",
    "./internal/llm", "./internal/jira",
]


def _summary(message: str, stream: TextIO | None = None) -> None:
    target = stream or sys.stdout
    print(message, file=target)
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary_path:
        with open(summary_path, "a", encoding="utf-8") as summary:
            summary.write(message + "\n")


def run(env: dict[str, str] | None = None, *, smoke: bool = False) -> int:
    """Run full or smoke oracle tests; pass only when oracle cases conclusively pass."""
    environment = dict(os.environ if env is None else env)
    required_env = SMOKE_REQUIRED_ENV if smoke else REQUIRED_ENV
    command = SMOKE_COMMAND if smoke else COMMAND
    absent = [key for key in required_env if not environment.get(key)]
    if absent:
        _summary("## JQL oracle: SKIPPED\nMissing required configuration: " + ", ".join(absent))
        return 2

    try:
        proc = subprocess.Popen(
            command,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            encoding="utf-8",
            errors="replace",
            env=environment,
            bufsize=1,
        )
    except OSError as exc:
        _summary(f"## JQL oracle: FAIL\nCould not start Go test command: {exc}", sys.stderr)
        return 1

    counts: Counter[str] = Counter()
    known_actions = {"start", "run", "pause", "cont", "pass", "fail", "skip", "output", "bench"}
    packages_seen: set[str] = set()
    package_starts: set[str] = set()
    package_terminal: dict[str, str] = {}
    test_runs: set[tuple[str, str]] = set()
    test_terminal: dict[tuple[str, str], str] = {}
    malformed: list[str] = []
    assert proc.stdout is not None
    for raw_line in proc.stdout:
        line = raw_line.rstrip("\n")
        try:
            event = json.loads(line)
            if not isinstance(event, dict) or not isinstance(event.get("Action"), str):
                raise ValueError("event must be an object with a string Action")
            action = event["Action"]
            package = event.get("Package", "")
            test_name = event.get("Test", "")
            output = event.get("Output", "")
            if action not in known_actions:
                raise ValueError("event has an unknown Action")
            if not isinstance(package, str) or not isinstance(test_name, str) or not isinstance(output, str):
                raise ValueError("Package, Test, and Output fields must be strings")
            if not package:
                raise ValueError("event is missing Package")
            packages_seen.add(package)
            if action == "start" and not test_name:
                package_starts.add(package)
            if test_name and action == "run":
                test_runs.add((package, test_name))
            if action in {"pass", "fail", "skip"}:
                counts[action] += 1
                if package and not test_name:
                    package_terminal[package] = action
                if test_name:
                    test_terminal[(package, test_name)] = action
            if action == "fail":
                counts["any_fail"] += 1
            # Preserve human-readable Go output without exposing the environment.
            if output:
                print(output, end="", flush=True)
            elif action in {"fail", "skip"}:
                print(line, flush=True)
        except (json.JSONDecodeError, ValueError) as exc:
            malformed.append(f"{exc}: {line[:300]}")
    exit_code = proc.wait()

    oracle_names = {
        (package, name)
        for package, name in test_runs | set(test_terminal)
        if name.startswith("TestOracle_")
    }
    parent_names = {
        (package, name)
        for package, name in oracle_names
        if any(pkg == package and child.startswith(name + "/") for pkg, child in oracle_names)
    }
    leaf_names = oracle_names - parent_names
    leaf_states = [test_terminal[name] for name in leaf_names if name in test_terminal]
    passed = sum(state == "pass" for state in leaf_states)
    skipped = sum(state == "skip" for state in leaf_states)
    failed = sum(state == "fail" for state in leaf_states)
    oracle_skips = sum(test_terminal.get(name) == "skip" for name in oracle_names)
    incomplete_tests = sorted(test_runs - set(test_terminal))
    orphan_terminals = sorted(set(test_terminal) - test_runs)
    incomplete_packages = sorted(
        pkg for pkg in packages_seen if pkg not in package_starts or pkg not in package_terminal
    )

    problems = []
    if smoke:
        passing_roots = {name for (_, name), state in test_terminal.items() if state == "pass"}
        missing = [name for name in SMOKE_TESTS if name not in passing_roots]
        if missing:
            problems.append("required smoke tests did not pass: " + ", ".join(missing))
    if exit_code != 0:
        problems.append(f"go test exited with status {exit_code}")
    if counts["any_fail"]:
        problems.append(f"Go test fail events: {counts['any_fail']}")
    if malformed:
        problems.append(f"malformed JSON events: {len(malformed)}")
    if oracle_skips:
        problems.append(f"oracle cases skipped: {oracle_skips}")
    if failed:
        problems.append(f"oracle cases failed: {failed}")
    if not passed:
        problems.append("no passing leaf TestOracle_* cases were reported")
    if incomplete_tests:
        problems.append(f"incomplete test events: {len(incomplete_tests)}")
    if orphan_terminals:
        problems.append(f"test terminal events without run: {len(orphan_terminals)}")
    if incomplete_packages:
        problems.append("package streams did not finish: " + ", ".join(incomplete_packages))

    report = (
        "## JQL oracle: " + ("FAIL" if problems else "PASS") + "\n"
        + f"Leaf oracle tests: {passed} passed, {failed} failed, {skipped} skipped.\n"
        + f"Go test events: {counts['pass']} passed, {counts['fail']} failed, {counts['skip']} skipped.\n"
    )
    if problems:
        report += "\n".join("- " + problem for problem in problems) + "\n"
        if malformed:
            report += "\nFirst malformed event: " + malformed[0] + "\n"
    _summary(report.rstrip())
    return 1 if problems else 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--smoke",
        action="store_true",
        help="run fixed date parser checks and one issue's read-only activity endpoints (requires Jira connection and a nonempty visible project)",
    )
    args = parser.parse_args(argv)
    return run(smoke=args.smoke)


if __name__ == "__main__":
    raise SystemExit(main())

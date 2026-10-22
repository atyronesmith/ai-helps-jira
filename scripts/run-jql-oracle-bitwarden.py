#!/usr/bin/env python3
"""Run the fixed, non-mutating Jira oracle using macOS Keychain and Bitwarden."""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys


ROOT = Path(__file__).resolve().parent.parent
CONFIG_KEYS = {
    "JIRA_SERVER", "JIRA_EMAIL", "JIRA_PROJECT", "JIRA_ORACLE_ISSUE_KEY",
    "JIRA_ORACLE_SPRINT_ID", "JIRA_ORACLE_RELEASE", "BWS_JIRA_API_TOKEN_ID",
    "BWS_ACCESS_TOKEN_KEYCHAIN_SERVICE", "BWS_ACCESS_TOKEN_KEYCHAIN_ACCOUNT",
}
ASSIGNMENT = re.compile(r"^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$")
SECRET_ID = re.compile(r"[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}")


class ConfigurationError(Exception):
    """An error whose message is safe to display without secret material."""


def load_config(path: Path, environment: dict[str, str]) -> dict[str, str]:
    """Read allowlisted, single-line dotenv values without executing shell code."""
    config = dict(environment)
    # Bitwarden is authoritative; never reuse an ambient Jira or machine token.
    config.pop("JIRA_API_TOKEN", None)
    config.pop("BWS_ACCESS_TOKEN", None)
    seen: set[str] = set()
    try:
        with path.open(encoding="utf-8") as source:
            for line_number, line in enumerate(source, 1):
                match = ASSIGNMENT.match(line)
                if not match or match[1] not in CONFIG_KEYS:
                    continue
                key, raw_value = match.groups()
                if key in seen:
                    raise ConfigurationError(f"duplicate {key} in config file")
                seen.add(key)
                try:
                    parts = shlex.split(raw_value, comments=True, posix=True)
                except ValueError:
                    raise ConfigurationError(f"invalid quoting for {key} at line {line_number}") from None
                if len(parts) > 1:
                    raise ConfigurationError(f"quote values containing spaces for {key}")
                value = parts[0] if parts else ""
                if not config.get(key):
                    config[key] = value
    except (OSError, UnicodeError):
        raise ConfigurationError("could not read the requested config file") from None
    return config


def capture(command: list[str], environment: dict[str, str], failure: str) -> str:
    """Capture subprocess output; errors must not echo provider diagnostics."""
    try:
        result = subprocess.run(
            command, env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, encoding="utf-8", check=False,
        )
    except (OSError, UnicodeError):
        raise ConfigurationError(failure) from None
    if result.returncode != 0:
        if command[:2] == ["security", "find-generic-password"] and result.returncode == 44:
            raise ConfigurationError(
                failure + " (item not found in this process's Keychain context). "
                "Check the configured service/account and session access. iCloud/Local Items "
                "uses different storage from login Keychain. If the lookup works in your "
                "Terminal, run make test-live there; copying or rotating credentials may not be needed."
            )
        raise ConfigurationError(f"{failure} (command exit {result.returncode})")
    return result.stdout


def jira_token(config: dict[str, str]) -> str:
    config = dict(config)
    config.pop("JIRA_API_TOKEN", None)
    config.pop("BWS_ACCESS_TOKEN", None)
    token_id = config.get("BWS_JIRA_API_TOKEN_ID", "")
    if not SECRET_ID.fullmatch(token_id):
        raise ConfigurationError("BWS_JIRA_API_TOKEN_ID must be a secret UUID")
    service = config.get("BWS_ACCESS_TOKEN_KEYCHAIN_SERVICE") or "jira-bws-access-token"
    account = config.get("BWS_ACCESS_TOKEN_KEYCHAIN_ACCOUNT") or config.get("USER", "")
    if not account:
        raise ConfigurationError("set USER or BWS_ACCESS_TOKEN_KEYCHAIN_ACCOUNT")

    # The Keychain command receives no tokens, and the machine token is passed
    # only in the bws process environment, never argv or the Go test environment.
    machine_token = capture(
        ["security", "find-generic-password", "-s", service, "-a", account, "-w"],
        config, "could not retrieve the Bitwarden machine token from macOS Keychain",
    ).rstrip("\r\n")
    if not machine_token or any(ch.isspace() or ord(ch) < 32 for ch in machine_token):
        raise ConfigurationError("Keychain returned an empty or invalid machine token")
    bitwarden_env = dict(config, BWS_ACCESS_TOKEN=machine_token)
    try:
        raw_secret = capture(
            ["bws", "secret", "get", token_id], bitwarden_env,
            "Bitwarden secret lookup failed",
        )
    finally:
        bitwarden_env.pop("BWS_ACCESS_TOKEN", None)
    try:
        secret = json.loads(raw_secret)
    except (ValueError, TypeError):
        raise ConfigurationError("Bitwarden returned invalid secret JSON") from None
    if not isinstance(secret, dict) or secret.get("key") != "JIRA_API_TOKEN":
        raise ConfigurationError("Bitwarden secret key must be JIRA_API_TOKEN")
    value = secret.get("value")
    if not isinstance(value, str) or not value or any(ch.isspace() or ord(ch) < 32 for ch in value):
        raise ConfigurationError("Bitwarden returned an empty or invalid Jira token")
    return value


def load_runner():
    spec = importlib.util.spec_from_file_location("jql_oracle_runner", ROOT / "scripts/run-jql-oracle.py")
    assert spec is not None and spec.loader is not None
    runner = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(runner)
    return runner


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, help="single-line dotenv config (defaults to .env if present)")
    parser.add_argument("--smoke", action="store_true", help="run fixed parser checks and one issue's read-only activity endpoints without issue/sprint/release fixtures")
    args = parser.parse_args(argv)
    try:
        config = dict(os.environ)
        config.pop("BWS_ACCESS_TOKEN", None)
        config.pop("JIRA_API_TOKEN", None)
        path = args.env_file if args.env_file is not None else ROOT / ".env"
        if args.env_file is not None or path.exists():
            config = load_config(path, config)
        runner = load_runner()
        required = ("JIRA_SERVER", "JIRA_EMAIL", "JIRA_PROJECT") if args.smoke else tuple(
            key for key in runner.REQUIRED_ENV if key != "JIRA_API_TOKEN"
        )
        absent = [key for key in required if not config.get(key)]
        if absent:
            raise ConfigurationError("missing configuration: " + ", ".join(absent))
        config["JIRA_API_TOKEN"] = jira_token(config)
        # Go package paths in the fixed runner are relative to the repository.
        os.chdir(ROOT)
        return runner.run(config, smoke=args.smoke)
    except ConfigurationError as exc:
        print(f"JQL oracle Bitwarden: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())

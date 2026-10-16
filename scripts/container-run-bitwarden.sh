#!/usr/bin/env bash

# Keep credentials out of command traces even when the caller enables xtrace.
set +x
set -euo pipefail

env_file=${1:-.env}
image=${JIRA_CONTAINER_IMAGE:-jira-cli}
container=${JIRA_CONTAINER_NAME:-jira-cli-mcp}
container_host=${JIRA_CONTAINER_HOST:-127.0.0.1}
keychain_service=${BWS_ACCESS_TOKEN_KEYCHAIN_SERVICE:-jira-bws-access-token}
keychain_account=${BWS_ACCESS_TOKEN_KEYCHAIN_ACCOUNT:-${USER:-}}
unset CONTAINER_HOST BWS_ACCESS_TOKEN JIRA_API_TOKEN

temp_env_file=
cleanup() {
	unset access_token jira_api_token BWS_ACCESS_TOKEN
	if [[ -n "${temp_env_file:-}" ]]; then
		rm -f -- "$temp_env_file"
	fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

fail() {
	echo "container-run-bitwarden: $*" >&2
	exit 1
}

for command_name in security bws jq podman; do
	command -v "$command_name" >/dev/null 2>&1 || fail "required command not found: $command_name"
done

[[ -f "$env_file" ]] || fail "env file not found: $env_file"

# The app prefers environment values over mounted secret files. Create a
# private copy that omits JIRA_API_TOKEN so the Bitwarden-backed Podman secret
# is authoritative without requiring the normal .env workflow to be changed.
umask 077
temp_env_file=$(mktemp "${TMPDIR:-/tmp}/jira-container-env.XXXXXX") ||
	fail "could not create a temporary container env file"
while IFS= read -r line || [[ -n "$line" ]]; do
	if [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?JIRA_API_TOKEN[[:space:]]*= ]]; then
		continue
	fi
	printf '%s\n' "$line"
done < "$env_file" > "$temp_env_file"

token_id=${BWS_JIRA_API_TOKEN_ID:-}
if [[ -z "$token_id" ]]; then
	while IFS= read -r line || [[ -n "$line" ]]; do
		if [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?BWS_JIRA_API_TOKEN_ID[[:space:]]*= ]]; then
			value=${line#*=}
			value=${value#"${value%%[![:space:]]*}"}
			value=${value%"${value##*[![:space:]]}"}
			if [[ ${#value} -ge 2 && ( ${value:0:1} == '"' || ${value:0:1} == "'" ) && "${value: -1}" == "${value:0:1}" ]]; then
				value=${value:1:${#value}-2}
			fi
			[[ -z "$token_id" ]] || fail "multiple BWS_JIRA_API_TOKEN_ID assignments in $env_file"
			token_id=$value
		fi
	done < "$env_file"
fi

[[ -n "$token_id" ]] || fail "set BWS_JIRA_API_TOKEN_ID in the environment or $env_file"
[[ "$token_id" =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]] ||
	fail "BWS_JIRA_API_TOKEN_ID must be a UUID"
[[ -n "$keychain_account" ]] || fail "USER is empty; set BWS_ACCESS_TOKEN_KEYCHAIN_ACCOUNT"

access_token=$(security find-generic-password -s "$keychain_service" -a "$keychain_account" -w) ||
	fail "could not retrieve the Bitwarden access token from macOS Keychain"
[[ -n "$access_token" ]] || fail "the Bitwarden access token in macOS Keychain is empty"

# Read and validate the secret once. The access token is present only in the
# bws process environment; the JIRA token remains a non-exported shell value.
if ! jira_api_token=$(BWS_ACCESS_TOKEN="$access_token" bws secret get "$token_id" |
	jq -erj 'select(.key == "JIRA_API_TOKEN") | .value | select(type == "string" and length > 0)'); then
	fail "Bitwarden secret lookup failed or did not return a nonempty JIRA_API_TOKEN secret"
fi
unset access_token
[[ -n "$jira_api_token" ]] || fail "Bitwarden returned an empty JIRA_API_TOKEN secret"

printf '%s' "$jira_api_token" |
	podman secret create --replace JIRA_API_TOKEN - >/dev/null ||
	fail "could not install the JIRA_API_TOKEN Podman secret"

unset jira_api_token

podman stop "$container" >/dev/null 2>&1 || true
podman rm "$container" >/dev/null 2>&1 || true

podman run -d --name "$container" \
	-p "$container_host:8081:8081" -p "$container_host:18080:18080" \
	-v jira-cli-cache:/home/jira-cli/.jira-cli:Z \
	--read-only --tmpfs /tmp \
	--cap-drop=ALL \
	--env-file "$temp_env_file" \
	--secret source=JIRA_API_TOKEN,type=mount,uid=1001,gid=1001,mode=0400 \
	"$image" >/dev/null

echo "MCP HTTP (local client): http://localhost:8081/mcp (published on $container_host)"
echo "Dashboard (local client): http://localhost:18080 (published on $container_host)"
if [[ "$container_host" != 127.0.0.1 && "$container_host" != localhost ]]; then
	echo "Remote clients: replace localhost in these URLs/config with this host's reachable DNS name or IP."
	echo "WARNING: MCP and dashboard have no authentication; remote access must be protected by firewall rules and an authenticated proxy."
fi
echo
echo "Claude Code .mcp.json (for a client on this host):"
echo '  {"mcpServers": {"jira-cli": {"type": "http", "url": "http://localhost:8081/mcp"}}}'

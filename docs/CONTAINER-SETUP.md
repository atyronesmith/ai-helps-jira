# Container Setup Guide

Run the jira-cli MCP server as a container for shared multi-client access via MCP Streamable HTTP. For single-user setups, the simpler approach is building the binary and using stdio — see the [Quick Start](../README.md#quick-start) in the README.

No Go toolchain or source code needed — just a container runtime and your JIRA credentials.

The included `make container-run` target and the manual examples below publish both services on `127.0.0.1` only. The MCP endpoint and dashboard do not provide client authentication. To make them reachable from other machines, configure an authenticated reverse proxy and firewall restrictions first, then explicitly change the host publish address; do not expose the unauthenticated ports directly to an untrusted network.

For macOS hosts using Bitwarden Secrets Manager, `make container-run-bitwarden` retrieves the JIRA token on the host and mounts it as a Podman secret. This requires the Bitwarden Secrets Manager CLI (`bws`), `jq`, and Podman. Store a machine access token in macOS Keychain under service `jira-bws-access-token` and account `$USER` (or configure `BWS_ACCESS_TOKEN_KEYCHAIN_SERVICE` and `BWS_ACCESS_TOKEN_KEYCHAIN_ACCOUNT`). Set `BWS_JIRA_API_TOKEN_ID` to the UUID of the Bitwarden secret whose key is exactly `JIRA_API_TOKEN`; it can be in the process environment or in `.env`. The launcher uses a private temporary copy of `.env` with `JIRA_API_TOKEN` omitted, so the original file remains compatible with non-Bitwarden workflows while the mounted secret remains authoritative.

## Prerequisites

- A JIRA Cloud account with an API token
- A container runtime (podman or docker)
- An MCP client (Claude Code, etc.)

### Install a container runtime

**Fedora / RHEL**

```sh
sudo dnf install -y podman
```

Podman is pre-installed on most Fedora versions. Verify with `podman --version`.

**macOS**

```sh
brew install podman
podman machine init
podman machine start
```

The `podman machine` commands create and start a Linux VM, since containers require a Linux kernel. This only needs to be done once. The VM starts automatically on login after the first setup.

> You can also use Docker Desktop (`brew install --cask docker`). Substitute `docker` for `podman` in all commands below.

## Step 1: Create a JIRA API Token

1. Go to [id.atlassian.com/manage-profile/security/api-tokens](https://id.atlassian.com/manage-profile/security/api-tokens)
2. Click **Create API token**
3. Enter a label (e.g. "jira-cli") and click **Create**
4. Copy the token — it won't be shown again

## Step 2: Create the configuration file

Create a directory for your config and a `.env` file:

```sh
mkdir -p ~/.config/jira-cli
cat > ~/.config/jira-cli/.env <<'EOF'
# Required — JIRA connection
JIRA_SERVER=https://yourcompany.atlassian.net
JIRA_EMAIL=your.email@company.com
JIRA_API_TOKEN=your-api-token-from-step-1
JIRA_PROJECT=PROJ

# Optional — LLM provider for AI features (query, digest, enrich, etc.)
# Without an LLM provider, CRUD tools still work (get/create/edit issues, etc.)
# Pick ONE provider below and uncomment its lines.

# --- OpenAI-compatible API (OpenAI, DeepInfra, etc.) ---
# LLM_PROVIDER=openai
# LLM_BASE_URL=https://api.openai.com/v1
# LLM_API_KEY=sk-your-api-key
# LLM_MODEL=gpt-4o

# --- Ollama (local, no API key needed) ---
# LLM_PROVIDER=ollama
# OLLAMA_BASE_URL=http://host.containers.internal:11434
# LLM_MODEL=llama3.1

# --- Vertex AI (Claude on GCP) ---
# LLM_PROVIDER=vertex
# ANTHROPIC_VERTEX_PROJECT_ID=your-gcp-project-id
# CLOUD_ML_REGION=us-east5

# --- Confluence (optional) ---
# CONFLUENCE_PARENT_PAGE=123456789
EOF
```

Edit the file and fill in your values:

```sh
${EDITOR:-vi} ~/.config/jira-cli/.env
```

**Required fields:**

| Variable | Description | Example |
|----------|-------------|---------|
| `JIRA_SERVER` | Your Atlassian instance URL | `https://yourcompany.atlassian.net` |
| `JIRA_EMAIL` | Email on your Atlassian account | `you@company.com` |
| `JIRA_API_TOKEN` | API token from Step 1 | `ATATT3x...` |
| `JIRA_PROJECT` | Default JIRA project key | `MYPROJ` |

## Step 3: Start the container

```sh
podman run -d --name jira-cli-mcp \
  -p 127.0.0.1:8081:8081 -p 127.0.0.1:18080:18080 \
  -v jira-cli-cache:/home/jira-cli/.jira-cli:Z \
  --read-only --tmpfs /tmp \
  --cap-drop=ALL \
  --env-file ~/.config/jira-cli/.env \
  quay.io/aasmith/jira-cli:latest
```

What this does:

| Flag | Purpose |
|------|---------|
| `-p 127.0.0.1:8081:8081` | MCP Streamable HTTP endpoint, reachable from this host only by default |
| `-p 127.0.0.1:18080:18080` | Web dashboard and health check, reachable from this host only by default |
| `-v jira-cli-cache:...` | Persistent cache so data survives restarts |
| `--read-only` | Read-only root filesystem (security) |
| `--cap-drop=ALL` | Drop all Linux capabilities (security) |
| `--env-file` | Load JIRA/LLM credentials from your config |

The named volume is reused across image rebuilds, `podman rm`, and both
`make container-run` launch paths. Removing the container does not remove this
volume; explicitly removing `jira-cli-cache` or its underlying storage does.
Current scoped caches are stored inside it as `cache-v1/<scope-hash>.db` and
are reopened without clearing their data. The first use of this preproduction
cache format starts cold; no older schema/cache data is migrated.

Dashboard results also write files in the volume, but their current PID-based
filenames and stale-process cleanup do not guarantee history across restarts.
See [Cache Design](../README.md#cache-design) for the lifecycle table.

## Step 4: Verify it's running

```sh
# Health check
curl -sf http://localhost:18080/healthz
# Expected: {"status":"ok"}

# MCP Streamable HTTP endpoint: configure the client as shown in Step 5.
# The /mcp endpoint uses the MCP HTTP protocol and is not an SSE event stream.
```

Open `http://localhost:18080` in a browser to see the web dashboard.

The Make target offers an explicit host address override, for example `make container-run CONTAINER_HOST=192.0.2.10`. Its printed `localhost` URLs and client config are for clients running on the container host. Remote clients must replace `localhost` with the host's reachable DNS name or IP. The MCP endpoint and dashboard have no client authentication, so remote access must be protected by firewall rules and an authenticated proxy. `CONTAINER_HOST=0.0.0.0` publishes on every host interface and is unsafe without those protections. The services still bind to `0.0.0.0` inside the container so Podman or Docker can forward connections; that does not by itself publish them on every host interface.

## Step 5: Connect Claude Code

Create a `.mcp.json` file in your project directory (or home directory for global access):

```json
{
  "mcpServers": {
    "jira": {
      "type": "http",
      "url": "http://localhost:8081/mcp"
    }
  }
}
```

Do not add `"oauth": {}` when connecting to this server; it can trigger an OAuth flow. The server authenticates to Atlassian using the API token from `.env`.

Restart Claude Code (or start a new session). You should see 35 JIRA tools available. Test with:

```
Use jira_summary to show my current issues
```

## Managing the container

```sh
# View logs
podman logs jira-cli-mcp
podman logs -f jira-cli-mcp  # follow

# Stop
podman stop jira-cli-mcp

# Start again (keeps your cache)
podman start jira-cli-mcp

# Remove and recreate (e.g. after pulling a new image)
podman stop jira-cli-mcp && podman rm jira-cli-mcp
# then re-run the "podman run" command from Step 3

# Update to latest image
podman pull quay.io/aasmith/jira-cli:latest
# then stop, rm, and re-run
```

### Auto-start on boot

**Fedora / Linux (systemd with Quadlet)**

Create a Quadlet `.container` unit file:

```sh
mkdir -p ~/.config/containers/systemd
cat > ~/.config/containers/systemd/jira-cli-mcp.container <<'EOF'
[Container]
Image=quay.io/aasmith/jira-cli:latest
ContainerName=jira-cli-mcp
PublishPort=127.0.0.1:8081:8081
PublishPort=127.0.0.1:18080:18080
Volume=jira-cli-cache:/home/jira-cli/.jira-cli:Z
EnvironmentFile=%h/.config/jira-cli/.env
ReadOnly=true
Tmpfs=/tmp
DropCapability=ALL

[Service]
Restart=on-failure

[Install]
WantedBy=default.target
EOF
```

Then enable and start:

```sh
systemctl --user daemon-reload
systemctl --user enable --now jira-cli-mcp
```

To update to a new image version, pull and restart:

```sh
podman pull quay.io/aasmith/jira-cli:latest
systemctl --user restart jira-cli-mcp
```

**macOS**

The podman machine VM starts automatically after `podman machine init`. To auto-start the container when the VM boots, add a restart policy:

```sh
# Stop and recreate with --restart=always
podman stop jira-cli-mcp && podman rm jira-cli-mcp
podman run -d --name jira-cli-mcp \
  --restart=always \
  -p 127.0.0.1:8081:8081 -p 127.0.0.1:18080:18080 \
  -v jira-cli-cache:/home/jira-cli/.jira-cli:Z \
  --read-only --tmpfs /tmp \
  --cap-drop=ALL \
  --env-file ~/.config/jira-cli/.env \
  quay.io/aasmith/jira-cli:latest
```

## Using Ollama (local LLM)

If you run Ollama on your host machine, the container needs to reach it. The special hostname `host.containers.internal` resolves to the host from inside a podman container.

In your `.env`:

```
LLM_PROVIDER=ollama
OLLAMA_BASE_URL=http://host.containers.internal:11434
LLM_MODEL=llama3.1
```

On macOS, this works out of the box. On Linux, you may need to add `--network=host` to the `podman run` command, or use `host.containers.internal` (supported in podman 4.1+).

## Troubleshooting

**"Connection refused" on curl**

The container isn't running. Check `podman ps -a` to see if it exited, then `podman logs jira-cli-mcp` for the error.

**"Missing required env vars"**

Your `.env` file is missing `JIRA_SERVER`, `JIRA_EMAIL`, `JIRA_API_TOKEN`, or `JIRA_PROJECT`. Double-check the file path in `--env-file`.

**AI tools return LLM errors but CRUD tools work**

You haven't configured an LLM provider in `.env`. CRUD tools (get/create/edit issues, etc.) don't need an LLM. AI tools (query, digest, enrich, weekly status, etc.) do.

**"Unauthorized" or "401" from JIRA**

Your API token is invalid or expired. Create a new one at [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens) and update your `.env`.

**Stale data**

Use `refresh: true` on `jira_summary` to force a full fetch. Most tools use cached data that auto-refreshes on changes.

**Can't reach Ollama from container**

See [Using Ollama](#using-ollama-local-llm) above. Use `host.containers.internal` instead of `localhost` in `OLLAMA_BASE_URL`.

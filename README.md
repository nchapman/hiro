# Hiro

A distributed AI agent platform. A single Go binary serves an HTTP API, WebSocket chat, and a React dashboard. Agents are defined as markdown files and run agentic loops that can spawn and manage child agents.

## Quick Start

### Docker (recommended)

```bash
docker compose up
```

Open `http://localhost:8080` to complete setup — you'll configure a password and LLM provider through the onboarding flow. Configuration is stored in `config.yaml` inside the platform root.

Agent state is stored in a Docker volume — it survives container restarts but `docker compose down -v` will destroy it. The port is bound to localhost only; use a reverse proxy to expose it remotely.

### From Source

Requires Go 1.26.1+ and Node.js 24+.

```bash
make build
./hiro
```

## Configuration

On first launch, Hiro starts in setup mode. The dashboard walks you through choosing an LLM provider, entering an API key, and setting an admin password. This is stored in `config.yaml` at the platform root — no environment variables needed for normal operation.

Provider configuration can be updated later through the dashboard settings page.

### Environment Variables

These are optional overrides, not required for normal use:

| Variable | Default | Purpose |
|---|---|---|
| `HIRO_ADDR` | `:8080` | HTTP listen address |
| `HIRO_ROOT` | `.` | Platform root containing `agents/`, `sessions/`, `skills/`, `workspace/` |
| `HIRO_SWARM_CODE` | *(random)* | Swarm join code for worker discovery |
| `HIRO_LOG_LEVEL` | `info` | Log level |

## How It Works

### Agents

Agents are defined as markdown files in the `agents/` directory:

```
agents/
  operator/
    agent.md          # Required: YAML frontmatter + system prompt
    skills/
      delegate.md     # Skills available to this agent
```

The `agent.md` frontmatter configures the agent:

```yaml
---
name: operator
model: claude-sonnet-4-20250514
mode: persistent
description: Manages conversations and coordinates work.
---

Your system prompt goes here.
```

Agents can be **persistent** (survive restarts, have memory and task tracking) or **ephemeral** (run a single task and clean up).

### Skills

Skills use progressive disclosure — only name and description appear in the system prompt. The agent activates a skill on demand, receiving full instructions and any bundled resources.

```yaml
---
name: my-skill
description: When and why to use this skill.
---

Detailed instructions the agent receives when it activates this skill.
```

Skills can be flat `.md` files or directories that bundle scripts, references, and assets alongside a `SKILL.md` file.

### Runtime Agent Creation

Agents can create new agent and skill definitions at runtime using their file tools. No restart needed — new definitions are picked up immediately.

### Platform Root

On first boot, Hiro initializes the platform root with a default operator agent. The operator manages conversations, spawns subagents, and can delegate tasks to remote swarm workers. The directory structure:

```
/hiro/
  agents/       # Agent definitions (operator-writable)
  skills/       # Shared skills (operator-writable)
  sessions/     # Per-agent runtime state (history, memory, todos)
  workspace/    # Shared collaborative space for agent work
```

## Development

```bash
make build           # Build web UI + Go binary
make build-dev       # Go binary only (no web UI, uses -tags dev)
make test            # Run tests in Docker
make test-local      # Run tests locally (mock workers, no Docker)
make test-isolation  # Run UID isolation tests in Docker
make check           # Tests + go vet (in Docker)
make web             # Build web UI only
make docker          # Docker build
```

Run the web UI dev server with hot reload (proxies API calls to `localhost:8080`):

```bash
cd web/ui && npm run dev
```

## Docker

The Docker image is based on Ubuntu 24.04 and includes a full development environment so agents have access to real tooling:

- **Languages**: Node.js 24, Python 3.12 (managed by [mise](https://mise.jdx.dev))
- **Package managers**: npm, [uv](https://docs.astral.sh/uv/)
- **Build tools**: build-essential, pkg-config, cmake
- **Utilities**: git, ripgrep, jq, curl, tree, and more
- **Pre-installed**: typescript, eslint, prettier (Node); ruff, pytest, httpx (Python)

The container is the primary security boundary. Inside it, each agent runs as a dedicated Unix user from a pre-created pool (64 users), providing defense-in-depth isolation:

- **Session isolation**: Each agent's session directory (`sessions/<uuid>/`) is owned by its Unix user with `0700` permissions — agents cannot read each other's memory, history, or todos.
- **Secrets protection**: `config.yaml` is owned by root with `0600` permissions — agents cannot read operator secrets.
- **Collaborative workspace**: The shared `workspace/` directory uses setgid (`2775`) so all agents can read and write collaborative files via group membership. Agent definitions (`agents/`, `skills/`) are writable only by operator-mode agents.
- **Control plane as root**: The control plane runs as root for UID switching; agents run as unprivileged users.

Isolation is auto-detected at startup (enabled when the `hiro-agents` group exists). Outside Docker, all processes run as the same user.

## License

MIT

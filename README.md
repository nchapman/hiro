# Hive

A distributed AI agent platform. A single Go binary serves an HTTP API, WebSocket chat, and a React dashboard. Agents are defined as markdown files and run agentic loops that can spawn and manage child agents.

## Quick Start

### Docker (recommended)

```bash
echo "HIVE_API_KEY=your-api-key" > .env
docker compose up
```

The dashboard is available at `http://localhost:8080`. Omit `HIVE_API_KEY` to run the dashboard without agents.

Agent state is stored in a Docker volume — it survives container restarts but `docker compose down -v` will destroy it. The port is bound to localhost only; use a reverse proxy to expose it remotely.

### From Source

Requires Go 1.26.1+ and Node.js 24+.

```bash
make build
HIVE_API_KEY=your-api-key ./hive
```

## Configuration

All configuration is via environment variables. A `.env` file is loaded automatically.

| Variable | Default | Purpose |
|---|---|---|
| `HIVE_API_KEY` | *(none)* | LLM provider API key (required for agents) |
| `HIVE_PROVIDER` | `anthropic` | LLM provider (`anthropic` or `openrouter`) |
| `HIVE_MODEL` | *(from agent config)* | Override model for all agents |
| `HIVE_ADDR` | `:8080` | HTTP listen address |
| `HIVE_ROOT` | `.` | Platform root containing `agents/`, `sessions/`, `skills/`, `workspace/` |
| `HIVE_SWARM_CODE` | *(random)* | Swarm join code for worker discovery |

## How It Works

### Agents

Agents are defined as markdown files in the `agents/` directory:

```
agents/
  coordinator/
    agent.md          # Required: YAML frontmatter + system prompt
    skills/
      delegate.md     # Skills available to this agent
```

The `agent.md` frontmatter configures the agent:

```yaml
---
name: coordinator
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

On first boot, Hive initializes the platform root with a default coordinator agent. The coordinator manages conversations, spawns subagents, and can delegate tasks to remote swarm workers. The directory structure:

```
/hive/
  agents/       # Agent definitions (coordinator-writable)
  skills/       # Shared skills (coordinator-writable)
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
- **Collaborative workspace**: The shared `workspace/` directory uses setgid (`2775`) so all agents can read and write collaborative files via group membership. Agent definitions (`agents/`, `skills/`) are writable only by coordinator-mode agents.
- **Control plane as root**: The control plane runs as root for UID switching; agents run as unprivileged users.

Isolation is auto-detected at startup (enabled when the `hive-agents` group exists). Outside Docker, all processes run as the same user.

## License

MIT
